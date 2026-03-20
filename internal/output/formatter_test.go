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
