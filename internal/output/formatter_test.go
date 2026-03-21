package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

func TestPrintSuspects_CoverageSection(t *testing.T) {
	diag := models.ClusterDiagnostic{
		ClusterName: "test-cluster",
		Pods: []models.PodInfo{
			{Name: "pod-0", MemoryUsage: 7e9, MemoryLimit: 8e9, MemoryPercent: 87.5},
		},
		HottestPod: &models.PodInfo{Name: "pod-0", MemoryPercent: 87.5, MemoryUsage: 7e9, MemoryLimit: 8e9},
		Suspects: []models.SuspectReport{
			{ConnectorName: "test-conn", TaskID: 0, Score: 45, WorkerID: "10.0.0.1:8083", PodName: "pod-0"},
		},
		Coverage: &models.CollectionStats{
			PodsDiscovered:   3,
			PodsWithMetrics:  3,
			MetricsSource:    "none",
			MetricsCollected: 0,
			ConnectorTotal:   10,
			ConnectorErrors:  0,
			TotalTasks:       25,
			SignalsEvaluable: 4,
			Confidence:       "reduced",
		},
		CollectedAt: time.Now(),
	}

	var buf bytes.Buffer
	f := NewFormatter("table", &buf)
	if err := f.PrintSuspects(diag, 10); err != nil {
		t.Fatal(err)
	}

	out := buf.String()

	if !strings.Contains(out, "Confidence") {
		t.Error("should contain Confidence line")
	}
	if !strings.Contains(out, "REDUCED") {
		t.Error("should show REDUCED confidence")
	}
	if !strings.Contains(out, "4/9 signals evaluable") {
		t.Error("should show signal count")
	}
	if !strings.Contains(out, "Data sources") {
		t.Error("should show Data sources line")
	}
	if !strings.Contains(out, "connectors=10") {
		t.Error("should show connector count")
	}
}

func TestPrintSuspects_WarningsSection(t *testing.T) {
	diag := models.ClusterDiagnostic{
		ClusterName: "test-cluster",
		Suspects:    []models.SuspectReport{},
		Coverage: &models.CollectionStats{
			PodsDiscovered:   1,
			ConnectorTotal:   5,
			ConnectorErrors:  2,
			SignalsEvaluable: 3,
			Confidence:       "reduced",
			Warnings: []string{
				"pod metrics unavailable: memory% and hottest-pod signal may be inaccurate",
				"2/5 connector status fetches failed",
			},
		},
		CollectedAt: time.Now(),
	}

	var buf bytes.Buffer
	f := NewFormatter("table", &buf)
	_ = f.PrintSuspects(diag, 10)
	out := buf.String()

	if !strings.Contains(out, "WARNINGS") {
		t.Error("should show WARNINGS header")
	}
	if !strings.Contains(out, "pod metrics unavailable") {
		t.Error("should show pod metrics warning")
	}
	if !strings.Contains(out, "connector status fetches failed") {
		t.Error("should show connector error warning")
	}
}

func TestPrintSuspects_FooterConfidence(t *testing.T) {
	t.Run("reduced confidence footer", func(t *testing.T) {
		diag := models.ClusterDiagnostic{
			ClusterName: "test",
			Suspects:    []models.SuspectReport{{ConnectorName: "c", Score: 10}},
			Coverage: &models.CollectionStats{
				ConnectorTotal:   1,
				SignalsEvaluable: 4,
				Confidence:       "reduced",
			},
			CollectedAt: time.Now(),
		}

		var buf bytes.Buffer
		f := NewFormatter("table", &buf)
		_ = f.PrintSuspects(diag, 10)

		if !strings.Contains(buf.String(), "interpret scores with caution") {
			t.Error("reduced confidence should show caution in footer")
		}
	})

	t.Run("high confidence footer", func(t *testing.T) {
		diag := models.ClusterDiagnostic{
			ClusterName: "test",
			Suspects:    []models.SuspectReport{{ConnectorName: "c", Score: 10}},
			Coverage: &models.CollectionStats{
				ConnectorTotal:   1,
				SignalsEvaluable: 9,
				Confidence:       "high",
			},
			CollectedAt: time.Now(),
		}

		var buf bytes.Buffer
		f := NewFormatter("table", &buf)
		_ = f.PrintSuspects(diag, 10)

		if strings.Contains(buf.String(), "interpret scores with caution") {
			t.Error("high confidence should NOT show caution")
		}
	})
}

func TestPrintSuspects_NoCoverage(t *testing.T) {
	// Ensure backward compatibility — nil Coverage should not crash.
	diag := models.ClusterDiagnostic{
		ClusterName: "test",
		Suspects:    []models.SuspectReport{{ConnectorName: "c", Score: 10}},
		CollectedAt: time.Now(),
	}

	var buf bytes.Buffer
	f := NewFormatter("table", &buf)
	err := f.PrintSuspects(diag, 10)
	if err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if strings.Contains(out, "Confidence") {
		t.Error("should not show Confidence when Coverage is nil")
	}
}

func TestPrintSuspects_JSON_IncludesCoverage(t *testing.T) {
	diag := models.ClusterDiagnostic{
		ClusterName: "test",
		Suspects:    []models.SuspectReport{},
		Coverage: &models.CollectionStats{
			PodsDiscovered:   3,
			ConnectorTotal:   10,
			SignalsEvaluable: 4,
			Confidence:       "reduced",
			Warnings:         []string{"test warning"},
		},
		CollectedAt: time.Now(),
	}

	var buf bytes.Buffer
	f := NewFormatter("json", &buf)
	if err := f.PrintSuspects(diag, 10); err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	coverage, ok := result["coverage"]
	if !ok {
		t.Fatal("JSON output should include coverage field")
	}

	cov := coverage.(map[string]interface{})
	if cov["confidence"] != "reduced" {
		t.Errorf("confidence=%v, want reduced", cov["confidence"])
	}
	if cov["signalsEvaluable"] != float64(4) {
		t.Errorf("signalsEvaluable=%v, want 4", cov["signalsEvaluable"])
	}
}

func TestPrintDiff_ExecutiveSummary(t *testing.T) {
	diff := models.DiffReport{
		BeforeTime: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		AfterTime:  time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		Summary: &models.DiffSummary{
			TimeDelta:       "2h0m",
			ConnectorsAdded: 1,
			ScoreIncreases:  2,
			MaxScoreDelta:   25,
			Headline:        "Scores worsened significantly (max delta: +25)",
		},
		MetaDiff: &models.MetaDiff{
			Changes: []string{"transport: exec -> proxy"},
		},
		Clusters: []models.ClusterDiffReport{{
			ClusterName: "c1",
			AddedPods:   []models.PodSnapshot{{Name: "pod-new", NodeName: "node-1", MemoryPercent: 40}},
			ChangedPods: []models.PodChange{{Name: "pod-0", Changes: []string{"restarts: 0 -> 3 (+3)"}}},
			ChangedConnectors: []models.ConnectorChange{{
				Name:        "conn-a",
				Changes:     []string{"state: RUNNING -> FAILED"},
				TaskChanges: []string{"task-0: state RUNNING -> FAILED"},
			}},
			ChangedSuspects: []models.SuspectChange{{
				ConnectorName: "conn-a",
				TaskID:        0,
				ScoreBefore:   30,
				ScoreAfter:    55,
				ScoreDelta:    25,
				Changes:       []string{"score: 30 -> 55 (+25)"},
			}},
		}},
	}

	var buf bytes.Buffer
	f := NewFormatter("table", &buf)
	if err := f.PrintDiff(diff); err != nil {
		t.Fatal(err)
	}

	out := buf.String()

	// Executive summary elements.
	if !strings.Contains(out, "2h0m") {
		t.Error("should show time delta")
	}
	if !strings.Contains(out, "Scores worsened") {
		t.Error("should show headline")
	}

	// Metadata warning.
	if !strings.Contains(out, "Collection parameters changed") {
		t.Error("should show metadata change warning")
	}
	if !strings.Contains(out, "transport: exec -> proxy") {
		t.Error("should show specific meta change")
	}

	// Infrastructure section.
	if !strings.Contains(out, "Infrastructure") {
		t.Error("should show Infrastructure section")
	}
	if !strings.Contains(out, "pod-new") {
		t.Error("should show added pod")
	}
	if !strings.Contains(out, "restarts: 0 -> 3") {
		t.Error("should show restart change")
	}

	// Task changes.
	if !strings.Contains(out, "task-0: state RUNNING -> FAILED") {
		t.Error("should show task state change")
	}

	// Score delta.
	if !strings.Contains(out, "(+25)") {
		t.Error("should show score delta")
	}
}

func TestPrintDiff_NoChanges(t *testing.T) {
	diff := models.DiffReport{
		BeforeTime: time.Now(),
		AfterTime:  time.Now(),
		Summary:    &models.DiffSummary{TimeDelta: "0m", Headline: "No significant changes detected"},
		Clusters:   []models.ClusterDiffReport{{ClusterName: "c1"}},
	}

	var buf bytes.Buffer
	f := NewFormatter("table", &buf)
	_ = f.PrintDiff(diff)

	if !strings.Contains(buf.String(), "No changes detected") {
		t.Error("should show 'No changes detected'")
	}
}

func TestPrintDiff_JSON_IncludesSummaryAndMeta(t *testing.T) {
	diff := models.DiffReport{
		BeforeTime: time.Now(),
		AfterTime:  time.Now(),
		Summary:    &models.DiffSummary{TimeDelta: "1h0m", Headline: "test"},
		MetaDiff:   &models.MetaDiff{Changes: []string{"transport changed"}},
		Clusters:   []models.ClusterDiffReport{},
	}

	var buf bytes.Buffer
	f := NewFormatter("json", &buf)
	if err := f.PrintDiff(diff); err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := result["summary"]; !ok {
		t.Error("JSON should include summary")
	}
	if _, ok := result["metaDiff"]; !ok {
		t.Error("JSON should include metaDiff")
	}
}
