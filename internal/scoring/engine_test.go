package scoring

import (
	"strings"
	"testing"

	"github.com/dougdalo/kc-hunter/internal/config"
	"github.com/dougdalo/kc-hunter/pkg/models"
)

// defaultCfg returns the production-default scoring config. Tests that need
// to override specific values copy this and mutate the copy, keeping the
// remaining defaults realistic.
func defaultCfg() config.ScoringConfig {
	return config.DefaultScoringConfig()
}

const defaultPort = 8083

// --- findHottestPod ---

func TestFindHottestPod(t *testing.T) {
	tests := []struct {
		name     string
		pods     []models.PodInfo
		wantName string // "" means expect nil
	}{
		{
			name:     "empty pod list returns nil",
			pods:     nil,
			wantName: "",
		},
		{
			name: "single pod is always hottest",
			pods: []models.PodInfo{
				{Name: "pod-0", MemoryPercent: 50},
			},
			wantName: "pod-0",
		},
		{
			name: "highest memory percent wins",
			pods: []models.PodInfo{
				{Name: "low", MemoryPercent: 40},
				{Name: "high", MemoryPercent: 90},
				{Name: "mid", MemoryPercent: 60},
			},
			wantName: "high",
		},
		{
			name: "equal percent ties broken by absolute memory usage",
			pods: []models.PodInfo{
				{Name: "less-bytes", MemoryPercent: 85, MemoryUsage: 1_000_000},
				{Name: "more-bytes", MemoryPercent: 85, MemoryUsage: 2_000_000},
			},
			wantName: "more-bytes",
		},
		{
			name: "restarts increase pressure score — pod with restarts wins at same percent",
			pods: []models.PodInfo{
				{Name: "stable", MemoryPercent: 85, MemoryUsage: 2_000_000, RestartCount: 0},
				{Name: "crashy", MemoryPercent: 85, MemoryUsage: 2_000_000, RestartCount: 3},
			},
			wantName: "crashy",
		},
		{
			name: "proximity curve makes 95% beat 85% even with fewer bytes",
			pods: []models.PodInfo{
				{Name: "big-but-lower", MemoryPercent: 85, MemoryUsage: 10_000_000},
				{Name: "small-but-hotter", MemoryPercent: 95, MemoryUsage: 5_000_000},
			},
			wantName: "small-but-hotter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findHottestPod(tt.pods)
			if tt.wantName == "" {
				if got != nil {
					t.Fatalf("expected nil, got %q", got.Name)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %q, got nil", tt.wantName)
			}
			if got.Name != tt.wantName {
				t.Errorf("expected hottest=%q, got=%q", tt.wantName, got.Name)
			}
		})
	}
}

// --- podPressureScore ---

func TestPodPressureScore(t *testing.T) {
	tests := []struct {
		name    string
		pod     models.PodInfo
		wantMin float64
		wantMax float64
	}{
		{
			name:    "zero percent gives zero (no restarts)",
			pod:     models.PodInfo{MemoryPercent: 0},
			wantMin: 0, wantMax: 0.01,
		},
		{
			name:    "50% gives roughly 35 (70% weight)",
			pod:     models.PodInfo{MemoryPercent: 50},
			wantMin: 34, wantMax: 36,
		},
		{
			name:    "100% with no restarts gives 90 (70 + 20 proximity)",
			pod:     models.PodInfo{MemoryPercent: 100},
			wantMin: 89, wantMax: 91,
		},
		{
			name:    "100% with 5 restarts gives 100",
			pod:     models.PodInfo{MemoryPercent: 100, RestartCount: 5},
			wantMin: 99, wantMax: 101,
		},
		{
			name:    "negative percent clamped to zero",
			pod:     models.PodInfo{MemoryPercent: -10},
			wantMin: 0, wantMax: 0.01,
		},
		{
			name:    "over 100% clamped to 100",
			pod:     models.PodInfo{MemoryPercent: 150, RestartCount: 5},
			wantMin: 99, wantMax: 101,
		},
		{
			name:    "restarts capped at 5 — 10 restarts same as 5",
			pod:     models.PodInfo{MemoryPercent: 50, RestartCount: 10},
			wantMin: 44, wantMax: 46, // 35 + 10
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podPressureScore(&tt.pod)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("podPressureScore()=%.2f, want [%.2f, %.2f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// --- buildWorkerToPodMap ---

func TestBuildWorkerToPodMap(t *testing.T) {
	pods := []models.PodInfo{
		{Name: "pod-0", IP: "10.0.0.1"},
		{Name: "pod-1", IP: "10.0.0.2"},
		{Name: "pod-no-ip", IP: ""},
	}

	m := buildWorkerToPodMap(pods, 8083)

	// IP-based keys
	if m["10.0.0.1:8083"] != "pod-0" {
		t.Errorf("IP key for pod-0: got %q", m["10.0.0.1:8083"])
	}
	if m["10.0.0.2:8083"] != "pod-1" {
		t.Errorf("IP key for pod-1: got %q", m["10.0.0.2:8083"])
	}

	// Name-based keys
	if m["pod-0:8083"] != "pod-0" {
		t.Errorf("name key for pod-0: got %q", m["pod-0:8083"])
	}

	// Pod with empty IP should only have name key, not ":8083" key
	if _, exists := m[":8083"]; exists {
		t.Error("empty IP should not create a ':8083' key")
	}
	if m["pod-no-ip:8083"] != "pod-no-ip" {
		t.Errorf("name key for pod-no-ip: got %q", m["pod-no-ip:8083"])
	}
}

// --- isOnHotWorker ---

func TestIsOnHotWorker(t *testing.T) {
	hot := &models.PodInfo{Name: "hot-pod", IP: "10.0.0.5"}

	tests := []struct {
		name     string
		workerID string
		hot      *models.PodInfo
		want     bool
	}{
		{"matches by IP", "10.0.0.5:8083", hot, true},
		{"matches by pod name", "hot-pod:8083", hot, true},
		{"different worker", "10.0.0.99:8083", hot, false},
		{"nil hot pod", "10.0.0.5:8083", nil, false},
		{"wrong port", "10.0.0.5:9999", hot, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOnHotWorker(tt.workerID, tt.hot, 8083)
			if got != tt.want {
				t.Errorf("isOnHotWorker(%q)=%v, want %v", tt.workerID, got, tt.want)
			}
		})
	}
}

// --- matchRiskyClass ---

func TestMatchRiskyClass(t *testing.T) {
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	tests := []struct {
		name      string
		className string
		want      bool
	}{
		{"exact match from default list", "io.debezium.connector.mysql.MySqlConnector", true},
		{"another exact match", "io.confluent.connect.s3.S3SinkConnector", true},
		{"unknown class", "com.example.MyCustomConnector", false},
		{"empty class", "", false},
		{"partial match still works (substring)", "io.confluent.connect.jdbc.JdbcSourceConnector", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.matchRiskyClass(tt.className)
			if got != tt.want {
				t.Errorf("matchRiskyClass(%q)=%v, want %v", tt.className, got, tt.want)
			}
		})
	}
}

func TestMatchRiskyClass_SubstringPattern(t *testing.T) {
	cfg := defaultCfg()
	cfg.RiskyClasses = []string{"io.debezium"}
	e := NewEngine(cfg, defaultPort)

	if !e.matchRiskyClass("io.debezium.connector.mysql.MySqlConnector") {
		t.Error("substring pattern 'io.debezium' should match full Debezium class")
	}
	if e.matchRiskyClass("com.other.DebeziumLike") {
		t.Error("should not match class that doesn't contain the pattern")
	}
}

// --- shortClass ---

func TestShortClass(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"io.debezium.connector.mysql.MySqlConnector", "MySqlConnector"},
		{"S3SinkConnector", "S3SinkConnector"},
		{"", ""},
	}

	for _, tt := range tests {
		if got := shortClass(tt.input); got != tt.want {
			t.Errorf("shortClass(%q)=%q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- scoreTask: individual signal tests ---

// helper to build a minimal scenario and score a single task.
func scoreOne(t *testing.T, opts scoreOpts) models.SuspectReport {
	t.Helper()

	cfg := defaultCfg()
	if opts.cfgOverride != nil {
		opts.cfgOverride(&cfg)
	}

	e := NewEngine(cfg, defaultPort)

	pods := opts.pods
	if pods == nil {
		pods = []models.PodInfo{{Name: "default-pod", IP: "10.0.0.1", MemoryPercent: 50}}
	}

	conn := opts.connector
	if conn.Name == "" {
		conn.Name = "test-connector"
	}

	task := opts.task
	if task.State == "" {
		task.State = "RUNNING"
	}
	if task.WorkerID == "" {
		task.WorkerID = "10.0.0.1:8083"
	}
	conn.Tasks = append(conn.Tasks, task)

	metrics := opts.metrics

	reports := e.ScoreAll(pods, []models.ConnectorInfo{conn}, metrics)
	if len(reports) == 0 {
		t.Fatal("ScoreAll returned no reports")
	}
	return reports[0]
}

type scoreOpts struct {
	pods        []models.PodInfo
	connector   models.ConnectorInfo
	task        models.TaskInfo
	metrics     map[string]*models.ConnectorMetrics
	cfgOverride func(*config.ScoringConfig)
}

func hasActiveSignal(r models.SuspectReport, name string) bool {
	for _, s := range r.Signals {
		if s.Name == name && s.Active {
			return true
		}
	}
	return false
}

func TestSignal_FailedTask(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		wantActive bool
	}{
		{"FAILED fires", "FAILED", true},
		{"UNASSIGNED fires", "UNASSIGNED", true},
		{"RUNNING does not fire", "RUNNING", false},
		{"PAUSED does not fire", "PAUSED", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := scoreOne(t, scoreOpts{
				task: models.TaskInfo{State: tt.state},
			})
			if got := hasActiveSignal(r, "task_failed"); got != tt.wantActive {
				t.Errorf("task_failed active=%v, want %v (state=%s)", got, tt.wantActive, tt.state)
			}
		})
	}
}

func TestSignal_FailedTask_TraceInReasons(t *testing.T) {
	r := scoreOne(t, scoreOpts{
		task: models.TaskInfo{
			State: "FAILED",
			Trace: "java.lang.OutOfMemoryError: Java heap space",
		},
	})

	found := false
	for _, reason := range r.Reasons {
		if strings.Contains(reason, "error: java.lang.OutOfMemoryError") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error trace in reasons, got: %v", r.Reasons)
	}
}

func TestSignal_FailedTask_TraceTruncation(t *testing.T) {
	longTrace := strings.Repeat("X", 200)
	r := scoreOne(t, scoreOpts{
		task: models.TaskInfo{State: "FAILED", Trace: longTrace},
	})

	for _, reason := range r.Reasons {
		if strings.HasPrefix(reason, "error: ") {
			// 120 chars of trace + "..." = 123, plus "error: " prefix = 130
			if len(reason) > 131 {
				t.Errorf("trace not truncated: len=%d", len(reason))
			}
			if !strings.HasSuffix(reason, "...") {
				t.Error("truncated trace should end with ...")
			}
			return
		}
	}
	t.Error("no error reason found")
}

func TestSignal_HotWorker(t *testing.T) {
	hotPod := models.PodInfo{Name: "hot", IP: "10.0.0.1", MemoryPercent: 90}
	coldPod := models.PodInfo{Name: "cold", IP: "10.0.0.2", MemoryPercent: 30}

	t.Run("fires when task is on hottest pod above threshold", func(t *testing.T) {
		r := scoreOne(t, scoreOpts{
			pods: []models.PodInfo{hotPod, coldPod},
			task: models.TaskInfo{WorkerID: "10.0.0.1:8083"},
		})
		if !hasActiveSignal(r, "on_hottest_worker") {
			t.Error("on_hottest_worker should be active")
		}
	})

	t.Run("does NOT fire when hottest pod is below MemoryPercentHot threshold", func(t *testing.T) {
		belowThreshold := models.PodInfo{Name: "warmish", IP: "10.0.0.1", MemoryPercent: 70}
		r := scoreOne(t, scoreOpts{
			pods: []models.PodInfo{belowThreshold},
			task: models.TaskInfo{WorkerID: "10.0.0.1:8083"},
		})
		if hasActiveSignal(r, "on_hottest_worker") {
			t.Error("on_hottest_worker should NOT fire when hottest pod < 80%")
		}
	})

	t.Run("does NOT fire when task is on a different worker", func(t *testing.T) {
		r := scoreOne(t, scoreOpts{
			pods: []models.PodInfo{hotPod, coldPod},
			task: models.TaskInfo{WorkerID: "10.0.0.2:8083"},
		})
		if hasActiveSignal(r, "on_hottest_worker") {
			t.Error("on_hottest_worker should NOT fire for task on cold pod")
		}
	})
}

func TestSignal_HighTaskCount(t *testing.T) {
	t.Run("fires at threshold", func(t *testing.T) {
		// Default threshold is 5.
		tasks := make([]models.TaskInfo, 5)
		for i := range tasks {
			tasks[i] = models.TaskInfo{TaskID: i, State: "RUNNING", WorkerID: "10.0.0.1:8083"}
		}
		r := scoreOne(t, scoreOpts{
			connector: models.ConnectorInfo{Name: "multi", Tasks: tasks[:4]}, // 4 existing + 1 added by scoreOne
			task:      models.TaskInfo{TaskID: 4, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
		})
		if !hasActiveSignal(r, "high_task_count") {
			t.Error("high_task_count should fire at threshold (5 tasks)")
		}
	})

	t.Run("does not fire below threshold", func(t *testing.T) {
		r := scoreOne(t, scoreOpts{
			connector: models.ConnectorInfo{Name: "small"},
			task:      models.TaskInfo{State: "RUNNING", WorkerID: "10.0.0.1:8083"},
		})
		if hasActiveSignal(r, "high_task_count") {
			t.Error("high_task_count should NOT fire with 1 task")
		}
	})
}

func TestSignal_RiskyClass(t *testing.T) {
	t.Run("fires for known risky class", func(t *testing.T) {
		r := scoreOne(t, scoreOpts{
			connector: models.ConnectorInfo{
				Name:      "jdbc-conn",
				ClassName: "io.confluent.connect.jdbc.JdbcSourceConnector",
			},
		})
		if !hasActiveSignal(r, "risky_connector_class") {
			t.Error("risky_connector_class should fire for JdbcSourceConnector")
		}
	})

	t.Run("does not fire for safe class", func(t *testing.T) {
		r := scoreOne(t, scoreOpts{
			connector: models.ConnectorInfo{
				Name:      "safe-conn",
				ClassName: "com.example.SafeConnector",
			},
		})
		if hasActiveSignal(r, "risky_connector_class") {
			t.Error("risky_connector_class should NOT fire for unknown class")
		}
	})
}

// --- Metrics-based signals ---

func TestSignal_MetricsBased(t *testing.T) {
	tests := []struct {
		name       string
		metrics    models.ConnectorMetrics
		wantSignal string
	}{
		{
			name:       "high poll time",
			metrics:    models.ConnectorMetrics{PollBatchAvgTimeMs: 6000},
			wantSignal: "high_poll_time",
		},
		{
			name:       "high put time",
			metrics:    models.ConnectorMetrics{PutBatchAvgTimeMs: 6000},
			wantSignal: "high_put_time",
		},
		{
			name:       "high batch size",
			metrics:    models.ConnectorMetrics{BatchSizeAvg: 15000},
			wantSignal: "high_batch_size",
		},
		{
			name:       "high retry count",
			metrics:    models.ConnectorMetrics{RetryCount: 15},
			wantSignal: "high_retry_or_errors",
		},
		{
			name:       "error rate > 0 fires retry signal even with zero retries",
			metrics:    models.ConnectorMetrics{ErrorRate: 0.5, RetryCount: 0},
			wantSignal: "high_retry_or_errors",
		},
		{
			name:       "high offset commit",
			metrics:    models.ConnectorMetrics{OffsetCommitAvgTimeMs: 12000},
			wantSignal: "high_offset_commit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := scoreOne(t, scoreOpts{
				connector: models.ConnectorInfo{Name: "metricked"},
				task:      models.TaskInfo{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
				metrics: map[string]*models.ConnectorMetrics{
					"metricked/0": &tt.metrics,
				},
			})
			if !hasActiveSignal(r, tt.wantSignal) {
				t.Errorf("signal %q should be active", tt.wantSignal)
			}
		})
	}
}

func TestSignal_MetricsBelowThreshold(t *testing.T) {
	// All values below thresholds — no metrics signals should fire.
	m := &models.ConnectorMetrics{
		PollBatchAvgTimeMs:    4000, // < 5000
		PutBatchAvgTimeMs:     4000, // < 5000
		BatchSizeAvg:          9000, // < 10000
		RetryCount:            5,    // < 10 and ErrorRate == 0
		OffsetCommitAvgTimeMs: 5000, // < 10000
	}
	r := scoreOne(t, scoreOpts{
		connector: models.ConnectorInfo{Name: "below"},
		task:      models.TaskInfo{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
		metrics:   map[string]*models.ConnectorMetrics{"below/0": m},
	})

	metricsSignals := []string{"high_poll_time", "high_put_time", "high_batch_size", "high_retry_or_errors", "high_offset_commit"}
	for _, name := range metricsSignals {
		if hasActiveSignal(r, name) {
			t.Errorf("signal %q should NOT be active when below threshold", name)
		}
	}
}

func TestSignal_NoMetricsProvided(t *testing.T) {
	// nil metrics map — no metrics-based signals should appear.
	r := scoreOne(t, scoreOpts{
		connector: models.ConnectorInfo{Name: "no-metrics"},
		task:      models.TaskInfo{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
		metrics:   nil,
	})

	for _, s := range r.Signals {
		switch s.Name {
		case "high_poll_time", "high_put_time", "high_batch_size", "high_retry_or_errors", "high_offset_commit":
			t.Errorf("metrics signal %q should not exist when no metrics provided", s.Name)
		}
	}
}

// --- Score capping ---

func TestScoreCappedAt100(t *testing.T) {
	// Engineer a scenario where all signals fire — raw total would exceed 100.
	hotPod := models.PodInfo{Name: "hot", IP: "10.0.0.1", MemoryPercent: 95}

	tasks := make([]models.TaskInfo, 4)
	for i := range tasks {
		tasks[i] = models.TaskInfo{TaskID: i, State: "RUNNING", WorkerID: "10.0.0.1:8083"}
	}

	r := scoreOne(t, scoreOpts{
		pods: []models.PodInfo{hotPod},
		connector: models.ConnectorInfo{
			Name:      "overloaded",
			ClassName: "io.debezium.connector.mysql.MySqlConnector",
			Tasks:     tasks,
		},
		task: models.TaskInfo{
			TaskID:   4,
			State:    "FAILED",
			WorkerID: "10.0.0.1:8083",
		},
		metrics: map[string]*models.ConnectorMetrics{
			"overloaded/4": {
				PollBatchAvgTimeMs:    10000,
				PutBatchAvgTimeMs:     10000,
				BatchSizeAvg:          20000,
				RetryCount:            50,
				OffsetCommitAvgTimeMs: 20000,
			},
		},
	})

	if r.Score > 100 {
		t.Errorf("score=%d, must be <= 100", r.Score)
	}
	if r.Score != 100 {
		t.Errorf("expected score=100 when all signals fire, got %d", r.Score)
	}
}

// --- ScoreAll: ordering and multi-task ---

func TestScoreAll_DescendingOrder(t *testing.T) {
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	pods := []models.PodInfo{
		{Name: "pod-0", IP: "10.0.0.1", MemoryPercent: 90},
	}
	connectors := []models.ConnectorInfo{
		{
			Name: "healthy",
			Tasks: []models.TaskInfo{
				{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
			},
		},
		{
			Name: "broken",
			Tasks: []models.TaskInfo{
				{TaskID: 0, State: "FAILED", WorkerID: "10.0.0.1:8083"},
			},
		},
	}

	reports := e.ScoreAll(pods, connectors, nil)
	if len(reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(reports))
	}
	if reports[0].Score < reports[1].Score {
		t.Errorf("reports not sorted descending: [0].Score=%d, [1].Score=%d",
			reports[0].Score, reports[1].Score)
	}
	if reports[0].ConnectorName != "broken" {
		t.Errorf("expected 'broken' first, got %q", reports[0].ConnectorName)
	}
}

func TestScoreAll_MultipleTasksPerConnector(t *testing.T) {
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	pods := []models.PodInfo{
		{Name: "pod-0", IP: "10.0.0.1", MemoryPercent: 50},
	}
	connectors := []models.ConnectorInfo{
		{
			Name: "multi-task",
			Tasks: []models.TaskInfo{
				{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
				{TaskID: 1, State: "FAILED", WorkerID: "10.0.0.1:8083"},
				{TaskID: 2, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
			},
		},
	}

	reports := e.ScoreAll(pods, connectors, nil)
	if len(reports) != 3 {
		t.Fatalf("expected 3 reports (one per task), got %d", len(reports))
	}

	// The failed task should rank highest.
	if reports[0].TaskID != 1 {
		t.Errorf("expected failed task (ID=1) first, got task ID=%d", reports[0].TaskID)
	}
}

func TestScoreAll_EmptyConnectors(t *testing.T) {
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	reports := e.ScoreAll(
		[]models.PodInfo{{Name: "pod-0", IP: "10.0.0.1", MemoryPercent: 50}},
		nil,
		nil,
	)
	if len(reports) != 0 {
		t.Errorf("expected 0 reports for nil connectors, got %d", len(reports))
	}
}

func TestScoreAll_NoPods(t *testing.T) {
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	connectors := []models.ConnectorInfo{
		{
			Name: "orphan",
			Tasks: []models.TaskInfo{
				{TaskID: 0, State: "FAILED", WorkerID: "10.0.0.1:8083"},
			},
		},
	}

	// Should not panic — hottest pod is nil, worker map is empty.
	reports := e.ScoreAll(nil, connectors, nil)
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	// task_failed should still fire.
	if !hasActiveSignal(reports[0], "task_failed") {
		t.Error("task_failed should fire even with no pods")
	}
	// PodName should be empty since no worker-to-pod mapping exists.
	if reports[0].PodName != "" {
		t.Errorf("expected empty PodName, got %q", reports[0].PodName)
	}
}

// --- recommend ---

func TestRecommend(t *testing.T) {
	tests := []struct {
		name        string
		score       int
		signals     []models.ScoringSignal
		wantContain string
	}{
		{
			name:        "low score gets low suspicion message",
			score:       20,
			signals:     nil,
			wantContain: "low suspicion",
		},
		{
			name:  "score=29 is still low",
			score: 29,
			signals: []models.ScoringSignal{
				{Name: "on_hottest_worker", Active: true},
			},
			wantContain: "low suspicion",
		},
		{
			name:  "score=30 with only hot_worker gets monitor message",
			score: 30,
			signals: []models.ScoringSignal{
				{Name: "on_hottest_worker", Active: true},
			},
			wantContain: "monitor closely",
		},
		{
			name:  "task_failed recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "task_failed", Active: true},
			},
			wantContain: "investigate failure trace",
		},
		{
			name:  "high_poll_time recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "high_poll_time", Active: true},
			},
			wantContain: "poll.interval.ms",
		},
		{
			name:  "high_put_time recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "high_put_time", Active: true},
			},
			wantContain: "sink backpressure",
		},
		{
			name:  "high_batch_size recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "high_batch_size", Active: true},
			},
			wantContain: "max.poll.records",
		},
		{
			name:  "high_retry_or_errors recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "high_retry_or_errors", Active: true},
			},
			wantContain: "retries hold records",
		},
		{
			name:  "risky_connector_class recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "risky_connector_class", Active: true},
			},
			wantContain: "profile this connector type",
		},
		{
			name:  "high_task_count recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "high_task_count", Active: true},
			},
			wantContain: "distribute tasks",
		},
		{
			name:  "high_offset_commit recommendation",
			score: 50,
			signals: []models.ScoringSignal{
				{Name: "high_offset_commit", Active: true},
			},
			wantContain: "slow offset commits",
		},
		{
			name:  "multiple signals produce combined recommendation",
			score: 60,
			signals: []models.ScoringSignal{
				{Name: "task_failed", Active: true},
				{Name: "high_task_count", Active: true},
				{Name: "on_hottest_worker", Active: false}, // inactive — should be ignored
			},
			wantContain: "; ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := models.SuspectReport{Score: tt.score, Signals: tt.signals}
			got := recommend(r)
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("recommend()=%q, want to contain %q", got, tt.wantContain)
			}
		})
	}
}

// --- Worker mapping in scored reports ---

func TestWorkerToPodMapping(t *testing.T) {
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	pods := []models.PodInfo{
		{Name: "pod-a", IP: "10.0.0.1", MemoryPercent: 50},
		{Name: "pod-b", IP: "10.0.0.2", MemoryPercent: 60},
	}
	connectors := []models.ConnectorInfo{
		{
			Name: "conn-on-a",
			Tasks: []models.TaskInfo{
				{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
			},
		},
		{
			Name: "conn-on-b",
			Tasks: []models.TaskInfo{
				{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.2:8083"},
			},
		},
	}

	reports := e.ScoreAll(pods, connectors, nil)
	podMap := make(map[string]string)
	for _, r := range reports {
		podMap[r.ConnectorName] = r.PodName
	}

	if podMap["conn-on-a"] != "pod-a" {
		t.Errorf("conn-on-a should map to pod-a, got %q", podMap["conn-on-a"])
	}
	if podMap["conn-on-b"] != "pod-b" {
		t.Errorf("conn-on-b should map to pod-b, got %q", podMap["conn-on-b"])
	}
}

func TestWorkerToPodMapping_UnknownWorker(t *testing.T) {
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	pods := []models.PodInfo{
		{Name: "pod-a", IP: "10.0.0.1", MemoryPercent: 50},
	}
	connectors := []models.ConnectorInfo{
		{
			Name: "ghost",
			Tasks: []models.TaskInfo{
				{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.99:8083"},
			},
		},
	}

	reports := e.ScoreAll(pods, connectors, nil)
	if reports[0].PodName != "" {
		t.Errorf("unknown worker should have empty PodName, got %q", reports[0].PodName)
	}
}

// --- Metrics key format ---

func TestMetricsKeyFormat(t *testing.T) {
	// Verify that the metrics map is keyed as "connectorName/taskID".
	cfg := defaultCfg()
	e := NewEngine(cfg, defaultPort)

	pods := []models.PodInfo{{Name: "pod-0", IP: "10.0.0.1", MemoryPercent: 50}}
	connectors := []models.ConnectorInfo{
		{
			Name: "my-connector",
			Tasks: []models.TaskInfo{
				{TaskID: 3, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
			},
		},
	}

	// Only the exact key "my-connector/3" should be matched.
	m := map[string]*models.ConnectorMetrics{
		"my-connector/3": {PollBatchAvgTimeMs: 99999},
	}

	reports := e.ScoreAll(pods, connectors, m)
	if !hasActiveSignal(reports[0], "high_poll_time") {
		t.Error("metrics with key 'my-connector/3' should be picked up for connector=my-connector, taskID=3")
	}

	// Wrong key format should not match.
	m2 := map[string]*models.ConnectorMetrics{
		"my-connector-3": {PollBatchAvgTimeMs: 99999},
	}
	reports2 := e.ScoreAll(pods, connectors, m2)
	if hasActiveSignal(reports2[0], "high_poll_time") {
		t.Error("wrong key format should not match")
	}
}

// --- Score computation: additive weights ---

func TestScoreAdditiveWeights(t *testing.T) {
	// Verify that the score equals the sum of active signal weights.
	hotPod := models.PodInfo{Name: "hot", IP: "10.0.0.1", MemoryPercent: 90}

	r := scoreOne(t, scoreOpts{
		pods: []models.PodInfo{hotPod},
		connector: models.ConnectorInfo{
			Name:      "jdbc-conn",
			ClassName: "io.confluent.connect.jdbc.JdbcSourceConnector",
		},
		task: models.TaskInfo{
			TaskID:   0,
			State:    "FAILED",
			WorkerID: "10.0.0.1:8083",
		},
	})

	// Active signals: on_hottest_worker(25) + task_failed(20) + risky_connector_class(5) = 50
	// high_task_count should NOT fire (only 1 task).
	expectedScore := 25 + 20 + 5
	if r.Score != expectedScore {
		t.Errorf("score=%d, want %d (hotWorker=25 + failedTask=20 + riskyClass=5)",
			r.Score, expectedScore)
	}
}

// --- Custom weight overrides ---

func TestCustomWeights(t *testing.T) {
	r := scoreOne(t, scoreOpts{
		pods: []models.PodInfo{
			{Name: "hot", IP: "10.0.0.1", MemoryPercent: 90},
		},
		task: models.TaskInfo{State: "FAILED", WorkerID: "10.0.0.1:8083"},
		cfgOverride: func(cfg *config.ScoringConfig) {
			cfg.Weights.HotWorker = 40
			cfg.Weights.FailedTask = 30
		},
	})

	// on_hottest_worker(40) + task_failed(30) = 70
	if r.Score != 70 {
		t.Errorf("score=%d with custom weights, want 70", r.Score)
	}
}

// --- Zero-weight disables signal ---

func TestZeroWeightDisablesSignal(t *testing.T) {
	r := scoreOne(t, scoreOpts{
		task: models.TaskInfo{State: "FAILED", WorkerID: "10.0.0.1:8083"},
		cfgOverride: func(cfg *config.ScoringConfig) {
			cfg.Weights.FailedTask = 0
		},
	})

	// task_failed still "fires" (Active=true) but contributes 0 to score.
	if r.Score != 0 {
		t.Errorf("score=%d with FailedTask weight=0, want 0", r.Score)
	}
}

// --- Edge: all signals inactive ---

func TestAllSignalsInactive(t *testing.T) {
	// Healthy task, no hot pod, no metrics, safe class, 1 task.
	r := scoreOne(t, scoreOpts{
		pods: []models.PodInfo{
			{Name: "pod-0", IP: "10.0.0.1", MemoryPercent: 50},
		},
		connector: models.ConnectorInfo{
			Name:      "safe",
			ClassName: "com.example.SafeConnector",
		},
		task:    models.TaskInfo{State: "RUNNING", WorkerID: "10.0.0.1:8083"},
		metrics: nil,
	})

	if r.Score != 0 {
		t.Errorf("healthy task should have score=0, got %d", r.Score)
	}
	if !strings.Contains(r.Recommendation, "low suspicion") {
		t.Errorf("healthy task should get low suspicion recommendation, got %q", r.Recommendation)
	}
}

// --- Reasons list matches active signals ---

func TestReasonsMatchActiveSignals(t *testing.T) {
	hotPod := models.PodInfo{Name: "hot", IP: "10.0.0.1", MemoryPercent: 90}

	r := scoreOne(t, scoreOpts{
		pods: []models.PodInfo{hotPod},
		connector: models.ConnectorInfo{
			Name:      "jdbc",
			ClassName: "io.confluent.connect.jdbc.JdbcSourceConnector",
		},
		task: models.TaskInfo{
			State:    "FAILED",
			WorkerID: "10.0.0.1:8083",
		},
	})

	activeCount := 0
	for _, s := range r.Signals {
		if s.Active {
			activeCount++
		}
	}

	// Reasons includes signal descriptions + any error trace.
	// With no trace, reasons count should equal active signal count.
	if len(r.Reasons) != activeCount {
		t.Errorf("reasons count=%d, active signals=%d — mismatch", len(r.Reasons), activeCount)
	}
}
