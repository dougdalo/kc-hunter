package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

func TestBuildSnapshot_CapturesPodData(t *testing.T) {
	diags := []models.ClusterDiagnostic{{
		ClusterName: "test",
		CollectedAt: time.Now(),
		Pods: []models.PodInfo{
			{Name: "pod-0", NodeName: "node-1", MemoryUsage: 7e9, MemoryLimit: 8e9, MemoryPercent: 87.5, RestartCount: 2, Ready: true},
			{Name: "pod-1", NodeName: "node-2", MemoryUsage: 3e9, MemoryLimit: 8e9, MemoryPercent: 37.5, RestartCount: 0, Ready: true},
		},
		Suspects: []models.SuspectReport{},
	}}

	meta := &models.SnapshotMeta{
		Namespaces:    []string{"kafka"},
		Labels:        "strimzi.io/kind=KafkaConnect",
		Transport:     "exec",
		MetricsSource: "none",
	}

	snap := BuildSnapshot(diags, meta)

	if snap.Version != "2" {
		t.Errorf("version=%s, want 2", snap.Version)
	}
	if snap.Meta == nil {
		t.Fatal("meta should be set")
	}
	if snap.Meta.Transport != "exec" {
		t.Errorf("transport=%s, want exec", snap.Meta.Transport)
	}
	if len(snap.Clusters) != 1 {
		t.Fatalf("clusters=%d, want 1", len(snap.Clusters))
	}
	if len(snap.Clusters[0].Pods) != 2 {
		t.Fatalf("pods=%d, want 2", len(snap.Clusters[0].Pods))
	}
	p := snap.Clusters[0].Pods[0]
	if p.Name != "pod-0" || p.RestartCount != 2 || p.MemoryPercent != 87.5 {
		t.Errorf("pod data not captured correctly: %+v", p)
	}
}

func TestBuildSnapshot_CapturesTaskData(t *testing.T) {
	diags := []models.ClusterDiagnostic{{
		ClusterName: "test",
		CollectedAt: time.Now(),
		Workers: []models.WorkerInfo{{
			WorkerID: "10.0.0.1:8083",
			Connectors: []models.ConnectorInfo{{
				Name:      "my-connector",
				Type:      "sink",
				State:     "RUNNING",
				ClassName: "org.example.Connector",
				Tasks: []models.TaskInfo{
					{TaskID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
					{TaskID: 1, State: "FAILED", WorkerID: "10.0.0.2:8083"},
				},
			}},
		}},
		Suspects: []models.SuspectReport{
			{ConnectorName: "my-connector", TaskID: 0, Score: 40},
		},
	}}

	snap := BuildSnapshot(diags, nil)

	if len(snap.Clusters[0].Connectors) != 1 {
		t.Fatalf("connectors=%d, want 1", len(snap.Clusters[0].Connectors))
	}
	conn := snap.Clusters[0].Connectors[0]
	if conn.TaskCount != 2 {
		t.Errorf("taskCount=%d, want 2", conn.TaskCount)
	}
	if len(conn.Tasks) != 2 {
		t.Fatalf("tasks=%d, want 2", len(conn.Tasks))
	}
	if conn.Tasks[0].State != "RUNNING" {
		t.Errorf("task-0 state=%s, want RUNNING", conn.Tasks[0].State)
	}
	if conn.Tasks[1].State != "FAILED" {
		t.Errorf("task-1 state=%s, want FAILED", conn.Tasks[1].State)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	snap := models.Snapshot{
		Version:     "2",
		CollectedAt: time.Now().Truncate(time.Second),
		Meta: &models.SnapshotMeta{
			Namespaces: []string{"ns1"}, Transport: "proxy", MetricsSource: "prometheus",
		},
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Pods:        []models.PodSnapshot{{Name: "p1", RestartCount: 3}},
			Connectors:  []models.ConnectorSnapshot{{Name: "conn1", Tasks: []models.TaskSnapshot{{TaskID: 0, State: "RUNNING"}}}},
		}},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	if err := Save(snap, path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Transport != "proxy" {
		t.Errorf("meta.transport=%s, want proxy", loaded.Meta.Transport)
	}
	if len(loaded.Clusters[0].Pods) != 1 {
		t.Errorf("pods not preserved")
	}
	if loaded.Clusters[0].Pods[0].RestartCount != 3 {
		t.Errorf("restartCount not preserved")
	}
	if len(loaded.Clusters[0].Connectors[0].Tasks) != 1 {
		t.Errorf("tasks not preserved")
	}
}

func TestSaveLoad_BackwardCompatV1(t *testing.T) {
	// Simulate a v1 snapshot (no meta, no pods, no tasks in connectors).
	v1JSON := `{
		"version": "1",
		"collectedAt": "2025-01-01T00:00:00Z",
		"clusters": [{
			"cluster": "old-cluster",
			"connectors": [{"name": "c1", "state": "RUNNING", "taskCount": 2}],
			"suspects": [{"connector": "c1", "taskID": 0, "score": 30}]
		}]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "v1.json")
	os.WriteFile(path, []byte(v1JSON), 0644)

	snap, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Meta != nil {
		t.Error("v1 snapshot should have nil meta")
	}
	if len(snap.Clusters[0].Pods) != 0 {
		t.Error("v1 snapshot should have no pods")
	}
	if len(snap.Clusters[0].Connectors[0].Tasks) != 0 {
		t.Error("v1 snapshot should have no task snapshots")
	}
}

func TestLoad_UnsupportedVersion(t *testing.T) {
	v99JSON := `{"version": "99", "collectedAt": "2025-01-01T00:00:00Z", "clusters": []}`
	dir := t.TempDir()
	path := filepath.Join(dir, "v99.json")
	os.WriteFile(path, []byte(v99JSON), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if !strings.Contains(err.Error(), "unsupported snapshot version") {
		t.Errorf("error=%q, want 'unsupported snapshot version'", err.Error())
	}
}

func TestLoad_NoVersion(t *testing.T) {
	// Snapshots without a version field should still load (legacy).
	noVerJSON := `{"collectedAt": "2025-01-01T00:00:00Z", "clusters": []}`
	dir := t.TempDir()
	path := filepath.Join(dir, "nover.json")
	os.WriteFile(path, []byte(noVerJSON), 0644)

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("no-version snapshot should load: %v", err)
	}
	if snap.Version != "" {
		t.Errorf("version=%q, want empty", snap.Version)
	}
}

func TestDiff_ExecutiveSummary(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Connectors: []models.ConnectorSnapshot{
				{Name: "conn-a", State: "RUNNING", TaskCount: 1},
			},
			Suspects: []models.SuspectReport{
				{ConnectorName: "conn-a", TaskID: 0, Score: 30},
			},
		}},
	}
	after := models.Snapshot{
		CollectedAt: time.Date(2025, 1, 1, 12, 30, 0, 0, time.UTC),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Connectors: []models.ConnectorSnapshot{
				{Name: "conn-a", State: "RUNNING", TaskCount: 1},
				{Name: "conn-b", State: "RUNNING", TaskCount: 2},
			},
			Suspects: []models.SuspectReport{
				{ConnectorName: "conn-a", TaskID: 0, Score: 55},
				{ConnectorName: "conn-b", TaskID: 0, Score: 20},
			},
		}},
	}

	diff := Diff(before, after)

	if diff.Summary == nil {
		t.Fatal("summary should be set")
	}
	if diff.Summary.TimeDelta != "2h30m" {
		t.Errorf("timeDelta=%s, want 2h30m", diff.Summary.TimeDelta)
	}
	if diff.Summary.ConnectorsAdded != 1 {
		t.Errorf("connectorsAdded=%d, want 1", diff.Summary.ConnectorsAdded)
	}
	if diff.Summary.ScoreIncreases != 1 {
		t.Errorf("scoreIncreases=%d, want 1", diff.Summary.ScoreIncreases)
	}
	if diff.Summary.MaxScoreDelta != 25 {
		t.Errorf("maxScoreDelta=%d, want 25", diff.Summary.MaxScoreDelta)
	}
	if diff.Summary.Headline == "" {
		t.Error("headline should not be empty")
	}
}

func TestDiff_MetaChanges(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Now(),
		Meta:        &models.SnapshotMeta{Transport: "exec", MetricsSource: "none"},
	}
	after := models.Snapshot{
		CollectedAt: time.Now(),
		Meta:        &models.SnapshotMeta{Transport: "proxy", MetricsSource: "prometheus"},
	}

	diff := Diff(before, after)

	if diff.MetaDiff == nil {
		t.Fatal("metaDiff should be set when meta changes")
	}
	if len(diff.MetaDiff.Changes) != 2 {
		t.Errorf("changes=%d, want 2 (transport + metricsSource)", len(diff.MetaDiff.Changes))
	}
	if diff.Summary.MetaChanged != true {
		t.Error("summary.metaChanged should be true")
	}
}

func TestDiff_MetaNoChange(t *testing.T) {
	meta := &models.SnapshotMeta{Transport: "exec", MetricsSource: "none"}
	before := models.Snapshot{CollectedAt: time.Now(), Meta: meta}
	after := models.Snapshot{CollectedAt: time.Now(), Meta: meta}

	diff := Diff(before, after)
	if diff.MetaDiff != nil {
		t.Error("metaDiff should be nil when meta is identical")
	}
}

func TestDiff_PodChanges(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Pods: []models.PodSnapshot{
				{Name: "pod-0", NodeName: "node-1", RestartCount: 0, MemoryPercent: 50.0, Ready: true},
				{Name: "pod-1", NodeName: "node-2", RestartCount: 1, MemoryPercent: 60.0, Ready: true},
			},
		}},
	}
	after := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Pods: []models.PodSnapshot{
				{Name: "pod-0", NodeName: "node-1", RestartCount: 3, MemoryPercent: 85.0, Ready: true},
				// pod-1 removed
				{Name: "pod-2", NodeName: "node-3", RestartCount: 0, MemoryPercent: 30.0, Ready: true},
			},
		}},
	}

	diff := Diff(before, after)
	cd := diff.Clusters[0]

	if len(cd.AddedPods) != 1 || cd.AddedPods[0].Name != "pod-2" {
		t.Errorf("added pods: %+v", cd.AddedPods)
	}
	if len(cd.RemovedPods) != 1 || cd.RemovedPods[0].Name != "pod-1" {
		t.Errorf("removed pods: %+v", cd.RemovedPods)
	}
	if len(cd.ChangedPods) != 1 || cd.ChangedPods[0].Name != "pod-0" {
		t.Fatalf("changed pods: %+v", cd.ChangedPods)
	}
	changes := cd.ChangedPods[0].Changes
	if len(changes) != 2 {
		t.Errorf("pod-0 should have 2 changes (restarts + memory), got %d: %v", len(changes), changes)
	}
	if diff.Summary.PodRestartChanges != 1 {
		t.Errorf("podRestartChanges=%d, want 1", diff.Summary.PodRestartChanges)
	}
}

func TestDiff_TaskStateChanges(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Connectors: []models.ConnectorSnapshot{{
				Name:      "conn-a",
				State:     "RUNNING",
				TaskCount: 2,
				Tasks: []models.TaskSnapshot{
					{TaskID: 0, State: "RUNNING", WorkerID: "w1"},
					{TaskID: 1, State: "RUNNING", WorkerID: "w2"},
				},
			}},
		}},
	}
	after := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Connectors: []models.ConnectorSnapshot{{
				Name:      "conn-a",
				State:     "RUNNING",
				TaskCount: 3,
				Tasks: []models.TaskSnapshot{
					{TaskID: 0, State: "FAILED", WorkerID: "w1"},
					{TaskID: 1, State: "RUNNING", WorkerID: "w3"}, // rebalanced
					{TaskID: 2, State: "RUNNING", WorkerID: "w3"}, // new task
				},
			}},
		}},
	}

	diff := Diff(before, after)
	cd := diff.Clusters[0]

	if len(cd.ChangedConnectors) != 1 {
		t.Fatalf("changedConnectors=%d, want 1", len(cd.ChangedConnectors))
	}
	cc := cd.ChangedConnectors[0]
	if len(cc.Changes) != 1 { // tasks: 2 -> 3
		t.Errorf("connector-level changes=%d, want 1 (task count)", len(cc.Changes))
	}
	if len(cc.TaskChanges) != 3 { // task-0 state, task-1 worker, task-2 added
		t.Errorf("taskChanges=%d, want 3, got %v", len(cc.TaskChanges), cc.TaskChanges)
	}
	if diff.Summary.TaskStateChanges != 3 {
		t.Errorf("summary.taskStateChanges=%d, want 3", diff.Summary.TaskStateChanges)
	}
}

func TestDiff_SuspectScoreDelta(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Suspects: []models.SuspectReport{
				{ConnectorName: "conn-a", TaskID: 0, Score: 40, Signals: []models.ScoringSignal{
					{Name: "task_failed", Weight: 20, Active: false},
					{Name: "high_task_count", Weight: 10, Active: true, Value: "6 tasks"},
				}},
			},
		}},
	}
	after := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Suspects: []models.SuspectReport{
				{ConnectorName: "conn-a", TaskID: 0, Score: 70, Signals: []models.ScoringSignal{
					{Name: "task_failed", Weight: 20, Active: true, Value: "FAILED"},
					{Name: "high_task_count", Weight: 10, Active: true, Value: "8 tasks"},
				}},
			},
		}},
	}

	diff := Diff(before, after)
	cd := diff.Clusters[0]

	if len(cd.ChangedSuspects) != 1 {
		t.Fatalf("changedSuspects=%d, want 1", len(cd.ChangedSuspects))
	}
	sc := cd.ChangedSuspects[0]
	if sc.ScoreDelta != 30 {
		t.Errorf("scoreDelta=%d, want 30", sc.ScoreDelta)
	}

	// Should have: score change, signal fired (task_failed), signal value change (high_task_count)
	if len(sc.Changes) < 3 {
		t.Errorf("changes=%d, want >= 3: %v", len(sc.Changes), sc.Changes)
	}
}

func TestDiff_SuspectSignalCleared(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Suspects: []models.SuspectReport{
				{ConnectorName: "c", TaskID: 0, Score: 20, Signals: []models.ScoringSignal{
					{Name: "task_failed", Weight: 20, Active: true},
				}},
			},
		}},
	}
	after := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Suspects: []models.SuspectReport{
				{ConnectorName: "c", TaskID: 0, Score: 0, Signals: []models.ScoringSignal{
					{Name: "task_failed", Weight: 20, Active: false},
				}},
			},
		}},
	}

	diff := Diff(before, after)
	sc := diff.Clusters[0].ChangedSuspects[0]
	if sc.ScoreDelta != -20 {
		t.Errorf("scoreDelta=%d, want -20", sc.ScoreDelta)
	}
	found := false
	for _, ch := range sc.Changes {
		if len(ch) > 15 && ch[:15] == "signal cleared:" {
			found = true
		}
	}
	if !found {
		t.Errorf("should show signal cleared, got %v", sc.Changes)
	}
}

func TestDiff_HeadlineNoChanges(t *testing.T) {
	snap := models.Snapshot{CollectedAt: time.Now()}
	diff := Diff(snap, snap)

	if diff.Summary.Headline != "No significant changes detected" {
		t.Errorf("headline=%q", diff.Summary.Headline)
	}
}

func TestDiff_HeadlineScoreWorsened(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Suspects:    []models.SuspectReport{{ConnectorName: "c", TaskID: 0, Score: 10}},
		}},
	}
	after := models.Snapshot{
		CollectedAt: time.Now(),
		Clusters: []models.ClusterSnapshot{{
			ClusterName: "c1",
			Suspects:    []models.SuspectReport{{ConnectorName: "c", TaskID: 0, Score: 50}},
		}},
	}

	diff := Diff(before, after)
	if diff.Summary.MaxScoreDelta != 40 {
		t.Errorf("maxScoreDelta=%d, want 40", diff.Summary.MaxScoreDelta)
	}
	if diff.Summary.Headline == "" || diff.Summary.Headline == "No significant changes detected" {
		t.Errorf("headline should indicate worsened scores, got %q", diff.Summary.Headline)
	}
}

func TestDiff_JSON_IncludesSummary(t *testing.T) {
	before := models.Snapshot{
		CollectedAt: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		Meta:        &models.SnapshotMeta{Transport: "exec"},
	}
	after := models.Snapshot{
		CollectedAt: time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC),
		Meta:        &models.SnapshotMeta{Transport: "proxy"},
	}

	diff := Diff(before, after)
	data, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	json.Unmarshal(data, &result)

	if _, ok := result["summary"]; !ok {
		t.Error("JSON should include summary")
	}
	if _, ok := result["metaDiff"]; !ok {
		t.Error("JSON should include metaDiff when meta changed")
	}
}
