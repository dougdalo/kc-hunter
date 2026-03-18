// Package scoring implements the suspicion scoring engine.
//
// Key design principle: memory is shared by the JVM. We never claim
// per-connector memory attribution. Instead we correlate indirect signals
// (pod placement, task state, latency, retry counts, connector type)
// to rank which connectors are the strongest suspects.
package scoring

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dougdalo/kcdiag/pkg/models"
)

// Thresholds control when each signal fires. Tune to your environment.
type Thresholds struct {
	MemoryPercentHot   float64 // pod memory% to be "hot"
	PollTimeHighMs     float64
	PutTimeHighMs      float64
	BatchSizeHigh      float64
	HighTaskCount      int
	HighRetryCount     float64
	OffsetCommitHighMs float64
}

// DefaultThresholds returns production-reasonable defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MemoryPercentHot:   80.0,
		PollTimeHighMs:     5000,
		PutTimeHighMs:      5000,
		BatchSizeHigh:      10000,
		HighTaskCount:      5,
		HighRetryCount:     10,
		OffsetCommitHighMs: 10000,
	}
}

// Signal weights. Sum of all possible weights exceeds 100 intentionally —
// if many signals fire, the raw total is capped at 100.
const (
	wHotWorker     = 25
	wFailedTask    = 20
	wHighRetry     = 15
	wHighPollTime  = 10
	wHighPutTime   = 10
	wHighBatch     = 10
	wHighTaskCount = 10
	wRiskyClass    = 5
	wHighCommit    = 5
)

// Known connector classes with large memory footprints.
var riskyClasses = map[string]bool{
	"io.confluent.connect.jdbc.JdbcSourceConnector":                  true,
	"io.confluent.connect.jdbc.JdbcSinkConnector":                    true,
	"org.apache.camel.kafkaconnector.ftp.CamelFtpSourceConnector":    true,
	"org.apache.kafka.connect.file.FileStreamSourceConnector":        true,
	"io.debezium.connector.mysql.MySqlConnector":                     true,
	"io.debezium.connector.postgresql.PostgresConnector":             true,
	"io.confluent.connect.s3.S3SinkConnector":                        true,
	"io.confluent.connect.elasticsearch.ElasticsearchSinkConnector":  true,
}

// Engine produces ranked suspect reports.
type Engine struct {
	thresholds  Thresholds
	connectPort int
}

// NewEngine creates a scoring engine with the given thresholds and Connect port.
func NewEngine(t Thresholds, connectPort int) *Engine {
	return &Engine{thresholds: t, connectPort: connectPort}
}

// ScoreAll evaluates every connector/task and returns a descending-score list.
func (e *Engine) ScoreAll(
	pods []models.PodInfo,
	connectors []models.ConnectorInfo,
	metricsMap map[string]*models.ConnectorMetrics,
) []models.SuspectReport {

	hottestPod := findHottestPod(pods)
	workerToPod := buildWorkerToPodMap(pods, e.connectPort)

	var reports []models.SuspectReport
	for _, conn := range connectors {
		for _, task := range conn.Tasks {
			reports = append(reports, e.scoreTask(conn, task, hottestPod, workerToPod, metricsMap))
		}
	}

	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Score > reports[j].Score
	})

	return reports
}

func (e *Engine) scoreTask(
	conn models.ConnectorInfo,
	task models.TaskInfo,
	hottestPod *models.PodInfo,
	workerToPod map[string]string,
	metricsMap map[string]*models.ConnectorMetrics,
) models.SuspectReport {

	r := models.SuspectReport{
		ConnectorName: conn.Name,
		TaskID:        task.TaskID,
		WorkerID:      task.WorkerID,
		PodName:       workerToPod[task.WorkerID],
	}

	total := 0
	var signals []models.ScoringSignal

	// Signal: task on hottest worker
	if hottestPod != nil {
		isHot := isOnHotWorker(task.WorkerID, hottestPod, e.connectPort)
		s := models.ScoringSignal{Name: "on_hottest_worker", Weight: wHotWorker, Active: isHot}
		if isHot {
			s.Description = fmt.Sprintf("assigned to hottest pod %s (%.1f%% mem)", hottestPod.Name, hottestPod.MemoryPercent)
			s.Value = fmt.Sprintf("%.1f%%", hottestPod.MemoryPercent)
			total += wHotWorker
		}
		signals = append(signals, s)
	}

	// Signal: failed or unassigned task
	isBad := task.State == "FAILED" || task.State == "UNASSIGNED"
	s2 := models.ScoringSignal{Name: "task_failed", Weight: wFailedTask, Active: isBad}
	if isBad {
		s2.Description = fmt.Sprintf("task state: %s", task.State)
		s2.Value = task.State
		total += wFailedTask
		if task.Trace != "" {
			trace := task.Trace
			if len(trace) > 120 {
				trace = trace[:120] + "..."
			}
			r.Reasons = append(r.Reasons, "error: "+trace)
		}
	}
	signals = append(signals, s2)

	// Signal: high task count per connector (more tasks = more buffers)
	highTC := len(conn.Tasks) >= e.thresholds.HighTaskCount
	s3 := models.ScoringSignal{Name: "high_task_count", Weight: wHighTaskCount, Active: highTC}
	if highTC {
		s3.Description = fmt.Sprintf("%d tasks (threshold: %d)", len(conn.Tasks), e.thresholds.HighTaskCount)
		s3.Value = fmt.Sprintf("%d", len(conn.Tasks))
		total += wHighTaskCount
	}
	signals = append(signals, s3)

	// Signal: risky connector class
	isRisky := riskyClasses[conn.ClassName]
	s4 := models.ScoringSignal{Name: "risky_connector_class", Weight: wRiskyClass, Active: isRisky}
	if isRisky {
		short := shortClass(conn.ClassName)
		s4.Description = fmt.Sprintf("high-risk type: %s", short)
		s4.Value = short
		total += wRiskyClass
	}
	signals = append(signals, s4)

	// Metrics-based signals (fire only when metrics data exists)
	key := fmt.Sprintf("%s/%d", conn.Name, task.TaskID)
	if m, ok := metricsMap[key]; ok && m != nil {
		if m.PollBatchAvgTimeMs > e.thresholds.PollTimeHighMs {
			signals = append(signals, models.ScoringSignal{
				Name: "high_poll_time", Weight: wHighPollTime, Active: true,
				Description: fmt.Sprintf("poll avg: %.0fms (threshold: %.0f)", m.PollBatchAvgTimeMs, e.thresholds.PollTimeHighMs),
				Value:       fmt.Sprintf("%.0fms", m.PollBatchAvgTimeMs),
			})
			total += wHighPollTime
		}
		if m.PutBatchAvgTimeMs > e.thresholds.PutTimeHighMs {
			signals = append(signals, models.ScoringSignal{
				Name: "high_put_time", Weight: wHighPutTime, Active: true,
				Description: fmt.Sprintf("put avg: %.0fms (threshold: %.0f)", m.PutBatchAvgTimeMs, e.thresholds.PutTimeHighMs),
				Value:       fmt.Sprintf("%.0fms", m.PutBatchAvgTimeMs),
			})
			total += wHighPutTime
		}
		if m.BatchSizeAvg > e.thresholds.BatchSizeHigh {
			signals = append(signals, models.ScoringSignal{
				Name: "high_batch_size", Weight: wHighBatch, Active: true,
				Description: fmt.Sprintf("batch avg: %.0f (threshold: %.0f)", m.BatchSizeAvg, e.thresholds.BatchSizeHigh),
				Value:       fmt.Sprintf("%.0f", m.BatchSizeAvg),
			})
			total += wHighBatch
		}
		if m.RetryCount > e.thresholds.HighRetryCount || m.ErrorRate > 0 {
			signals = append(signals, models.ScoringSignal{
				Name: "high_retry_or_errors", Weight: wHighRetry, Active: true,
				Description: fmt.Sprintf("retries: %.0f, errors: %.2f/s", m.RetryCount, m.ErrorRate),
				Value:       fmt.Sprintf("%.0f retries", m.RetryCount),
			})
			total += wHighRetry
		}
		if m.OffsetCommitAvgTimeMs > e.thresholds.OffsetCommitHighMs {
			signals = append(signals, models.ScoringSignal{
				Name: "high_offset_commit", Weight: wHighCommit, Active: true,
				Description: fmt.Sprintf("offset commit avg: %.0fms", m.OffsetCommitAvgTimeMs),
				Value:       fmt.Sprintf("%.0fms", m.OffsetCommitAvgTimeMs),
			})
			total += wHighCommit
		}
	}

	if total > 100 {
		total = 100
	}

	r.Score = total
	r.Signals = signals

	for _, s := range signals {
		if s.Active {
			r.Reasons = append(r.Reasons, s.Description)
		}
	}

	r.Recommendation = recommend(r)
	return r
}

func findHottestPod(pods []models.PodInfo) *models.PodInfo {
	var hot *models.PodInfo
	var max int64
	for i := range pods {
		if pods[i].MemoryUsage > max {
			max = pods[i].MemoryUsage
			hot = &pods[i]
		}
	}
	return hot
}

func buildWorkerToPodMap(pods []models.PodInfo, port int) map[string]string {
	m := make(map[string]string, len(pods)*2)
	for _, p := range pods {
		if p.IP != "" {
			m[fmt.Sprintf("%s:%d", p.IP, port)] = p.Name
		}
		m[fmt.Sprintf("%s:%d", p.Name, port)] = p.Name
	}
	return m
}

func isOnHotWorker(workerID string, hot *models.PodInfo, port int) bool {
	if hot == nil {
		return false
	}
	return workerID == fmt.Sprintf("%s:%d", hot.IP, port) ||
		workerID == fmt.Sprintf("%s:%d", hot.Name, port)
}

func shortClass(full string) string {
	if idx := strings.LastIndexByte(full, '.'); idx >= 0 {
		return full[idx+1:]
	}
	return full
}

func recommend(r models.SuspectReport) string {
	if r.Score < 30 {
		return "low suspicion — unlikely primary cause"
	}

	var recs []string
	for _, s := range r.Signals {
		if !s.Active {
			continue
		}
		switch s.Name {
		case "task_failed":
			recs = append(recs, "investigate failure trace and restart task")
		case "high_poll_time":
			recs = append(recs, "check source latency; reduce poll.interval.ms or max.batch.size")
		case "high_put_time":
			recs = append(recs, "check sink backpressure; tune batch.size or add sink capacity")
		case "high_batch_size":
			recs = append(recs, "reduce max.poll.records to limit per-batch memory")
		case "high_retry_or_errors":
			recs = append(recs, "fix error root cause; retries hold records in memory")
		case "risky_connector_class":
			recs = append(recs, "profile this connector type; known for large heap usage")
		case "high_task_count":
			recs = append(recs, "distribute tasks across more workers or reduce tasks.max")
		case "high_offset_commit":
			recs = append(recs, "investigate slow offset commits; may indicate storage pressure")
		}
	}

	if len(recs) == 0 {
		return "runs on hottest worker — monitor closely"
	}
	return strings.Join(recs, "; ")
}
