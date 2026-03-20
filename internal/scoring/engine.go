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

	"github.com/dougdalo/kc-hunter/internal/config"
	"github.com/dougdalo/kc-hunter/pkg/models"
)

// Engine produces ranked suspect reports.
type Engine struct {
	cfg         config.ScoringConfig
	connectPort int
}

// NewEngine creates a scoring engine with the given config and Connect port.
func NewEngine(cfg config.ScoringConfig, connectPort int) *Engine {
	return &Engine{cfg: cfg, connectPort: connectPort}
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

	w := e.cfg.Weights
	t := e.cfg

	r := models.SuspectReport{
		ConnectorName: conn.Name,
		TaskID:        task.TaskID,
		WorkerID:      task.WorkerID,
		PodName:       workerToPod[task.WorkerID],
	}

	total := 0
	var signals []models.ScoringSignal

	// Signal: task on hottest worker — only fires if the hottest pod
	// actually exceeds the MemoryPercentHot threshold. A pod at 40% usage
	// is not "hot" even if it's the highest in the cluster.
	if hottestPod != nil {
		onHot := isOnHotWorker(task.WorkerID, hottestPod, e.connectPort)
		aboveThreshold := hottestPod.MemoryPercent >= t.MemoryPercentHot
		isHot := onHot && aboveThreshold
		s := models.ScoringSignal{Name: "on_hottest_worker", Weight: w.HotWorker, Active: isHot}
		if isHot {
			s.Description = fmt.Sprintf("assigned to hottest pod %s (%.1f%% mem)", hottestPod.Name, hottestPod.MemoryPercent)
			s.Value = fmt.Sprintf("%.1f%%", hottestPod.MemoryPercent)
			total += w.HotWorker
		}
		signals = append(signals, s)
	}

	// Signal: failed or unassigned task
	isBad := task.State == "FAILED" || task.State == "UNASSIGNED"
	s2 := models.ScoringSignal{Name: "task_failed", Weight: w.FailedTask, Active: isBad}
	if isBad {
		s2.Description = fmt.Sprintf("task state: %s", task.State)
		s2.Value = task.State
		total += w.FailedTask
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
	highTC := len(conn.Tasks) >= t.HighTaskCount
	s3 := models.ScoringSignal{Name: "high_task_count", Weight: w.HighTaskCount, Active: highTC}
	if highTC {
		s3.Description = fmt.Sprintf("%d tasks (threshold: %d)", len(conn.Tasks), t.HighTaskCount)
		s3.Value = fmt.Sprintf("%d", len(conn.Tasks))
		total += w.HighTaskCount
	}
	signals = append(signals, s3)

	// Signal: risky connector class (substring match against configured patterns)
	isRisky := e.matchRiskyClass(conn.ClassName)
	s4 := models.ScoringSignal{Name: "risky_connector_class", Weight: w.RiskyClass, Active: isRisky}
	if isRisky {
		short := shortClass(conn.ClassName)
		s4.Description = fmt.Sprintf("high-risk type: %s", short)
		s4.Value = short
		total += w.RiskyClass
	}
	signals = append(signals, s4)

	// Metrics-based signals (fire only when metrics data exists)
	key := fmt.Sprintf("%s/%d", conn.Name, task.TaskID)
	if m, ok := metricsMap[key]; ok && m != nil {
		if m.PollBatchAvgTimeMs > t.PollTimeHighMs {
			signals = append(signals, models.ScoringSignal{
				Name: "high_poll_time", Weight: w.HighPollTime, Active: true,
				Description: fmt.Sprintf("poll avg: %.0fms (threshold: %.0f)", m.PollBatchAvgTimeMs, t.PollTimeHighMs),
				Value:       fmt.Sprintf("%.0fms", m.PollBatchAvgTimeMs),
			})
			total += w.HighPollTime
		}
		if m.PutBatchAvgTimeMs > t.PutTimeHighMs {
			signals = append(signals, models.ScoringSignal{
				Name: "high_put_time", Weight: w.HighPutTime, Active: true,
				Description: fmt.Sprintf("put avg: %.0fms (threshold: %.0f)", m.PutBatchAvgTimeMs, t.PutTimeHighMs),
				Value:       fmt.Sprintf("%.0fms", m.PutBatchAvgTimeMs),
			})
			total += w.HighPutTime
		}
		if m.BatchSizeAvg > t.BatchSizeHigh {
			signals = append(signals, models.ScoringSignal{
				Name: "high_batch_size", Weight: w.HighBatch, Active: true,
				Description: fmt.Sprintf("batch avg: %.0f (threshold: %.0f)", m.BatchSizeAvg, t.BatchSizeHigh),
				Value:       fmt.Sprintf("%.0f", m.BatchSizeAvg),
			})
			total += w.HighBatch
		}
		if m.RetryCount > t.HighRetryCount || m.ErrorRate > 0 {
			signals = append(signals, models.ScoringSignal{
				Name: "high_retry_or_errors", Weight: w.HighRetry, Active: true,
				Description: fmt.Sprintf("retries: %.0f, errors: %.2f/s", m.RetryCount, m.ErrorRate),
				Value:       fmt.Sprintf("%.0f retries", m.RetryCount),
			})
			total += w.HighRetry
		}
		if m.OffsetCommitAvgTimeMs > t.OffsetCommitHighMs {
			signals = append(signals, models.ScoringSignal{
				Name: "high_offset_commit", Weight: w.HighCommit, Active: true,
				Description: fmt.Sprintf("offset commit avg: %.0fms", m.OffsetCommitAvgTimeMs),
				Value:       fmt.Sprintf("%.0fms", m.OffsetCommitAvgTimeMs),
			})
			total += w.HighCommit
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

// matchRiskyClass returns true if className contains any configured risky
// class pattern as a substring. This allows both exact matches
// ("io.debezium.connector.mysql.MySqlConnector") and prefix/partial patterns
// ("io.debezium", "jdbc").
func (e *Engine) matchRiskyClass(className string) bool {
	if className == "" {
		return false
	}
	for _, pattern := range e.cfg.RiskyClasses {
		if strings.Contains(className, pattern) {
			return true
		}
	}
	return false
}

// podPressureScore computes a composite score (0–100) reflecting how close
// a pod is to OOM. This is more meaningful than raw bytes in Kubernetes
// where pods have different memory limits.
//
// Components:
//   - MemoryPercent (usage/limit): primary signal, weighted 70%
//   - Proximity to limit (non-linear): weighted 20% — a pod at 95% is
//     disproportionately more dangerous than one at 85%
//   - Restart count: weighted 10% — recent OOM kills are a strong indicator
func podPressureScore(p *models.PodInfo) float64 {
	pct := p.MemoryPercent
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	// Proximity score: exponential curve that penalizes the last 20% heavily.
	// At 80% → ~0.0, at 90% → ~0.33, at 95% → ~0.58, at 100% → 1.0.
	var proximity float64
	if pct > 80 {
		proximity = (pct - 80) / 20
		proximity = proximity * proximity // quadratic ramp
	}

	// Restart score: cap at 5 restarts for a max of 10 points.
	restarts := float64(p.RestartCount)
	if restarts > 5 {
		restarts = 5
	}
	restartScore := restarts / 5

	return pct*0.70 + proximity*100*0.20 + restartScore*100*0.10
}

// findHottestPod selects the pod with the highest memory pressure.
// Primary sort: MemoryPercent (usage/limit ratio).
// Tie-breaker: absolute MemoryUsage (higher bytes wins).
func findHottestPod(pods []models.PodInfo) *models.PodInfo {
	if len(pods) == 0 {
		return nil
	}
	hot := &pods[0]
	hotScore := podPressureScore(hot)
	for i := 1; i < len(pods); i++ {
		s := podPressureScore(&pods[i])
		if s > hotScore || (s == hotScore && pods[i].MemoryUsage > hot.MemoryUsage) {
			hot = &pods[i]
			hotScore = s
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
