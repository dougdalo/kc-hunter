package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig_HasValidDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should be valid: %v", err)
	}
}

func TestLoadFromFile_NonexistentReturnsDefaults(t *testing.T) {
	cfg, err := LoadFromFile("/nonexistent/path.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnectPort != 8083 {
		t.Errorf("ConnectPort=%d, want 8083", cfg.ConnectPort)
	}
	if cfg.Concurrency != 10 {
		t.Errorf("Concurrency=%d, want 10", cfg.Concurrency)
	}
}

func TestLoadFromFile_MergesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
connectPort: 9090
topN: 5
`), 0644)

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ConnectPort != 9090 {
		t.Errorf("ConnectPort=%d, want 9090", cfg.ConnectPort)
	}
	if cfg.TopN != 5 {
		t.Errorf("TopN=%d, want 5", cfg.TopN)
	}
	// Defaults should be preserved for unset fields.
	if cfg.MetricsPort != 9404 {
		t.Errorf("MetricsPort=%d, want 9404 (default)", cfg.MetricsPort)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout=%s, want 30s (default)", cfg.Timeout)
	}
}

func TestLoadFromFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("{{{{invalid yaml"), 0644)

	_, err := LoadFromFile(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadFromFile_ScoringOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
scoring:
  memoryPercentHot: 95
  pollTimeHighMs: 8000
  weights:
    hotWorker: 40
`), 0644)

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Scoring.MemoryPercentHot != 95 {
		t.Errorf("MemoryPercentHot=%f, want 95", cfg.Scoring.MemoryPercentHot)
	}
	if cfg.Scoring.PollTimeHighMs != 8000 {
		t.Errorf("PollTimeHighMs=%f, want 8000", cfg.Scoring.PollTimeHighMs)
	}
	if cfg.Scoring.Weights.HotWorker != 40 {
		t.Errorf("HotWorker weight=%d, want 40", cfg.Scoring.Weights.HotWorker)
	}
}

// --- Validate ---

func TestValidate_ValidConfig(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should validate: %v", err)
	}
}

func TestValidate_InvalidPorts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConnectPort = 0
	cfg.MetricsPort = 70000

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "connectPort") {
		t.Error("should report connectPort error")
	}
	if !strings.Contains(err.Error(), "metricsPort") {
		t.Error("should report metricsPort error")
	}
}

func TestValidate_InvalidOutputFormat(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OutputFormat = "xml"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for outputFormat=xml")
	}
	if !strings.Contains(err.Error(), "outputFormat") {
		t.Error("should report outputFormat error")
	}
}

func TestValidate_InvalidMetricsSource(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MetricsSource = "graphite"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for metricsSource=graphite")
	}
	if !strings.Contains(err.Error(), "metricsSource") {
		t.Error("should report metricsSource error")
	}
}

func TestValidate_NegativeConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Concurrency = -1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "concurrency") {
		t.Error("should report concurrency error")
	}
}

func TestValidate_NegativeScoringThresholds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Scoring.PollTimeHighMs = -100
	cfg.Scoring.PutTimeHighMs = -50

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative thresholds")
	}
	if !strings.Contains(err.Error(), "pollTimeHighMs") {
		t.Error("should report pollTimeHighMs error")
	}
	if !strings.Contains(err.Error(), "putTimeHighMs") {
		t.Error("should report putTimeHighMs error")
	}
}

func TestValidate_NegativeWeights(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Scoring.Weights.HotWorker = -5

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative weight")
	}
	if !strings.Contains(err.Error(), "hotWorker") {
		t.Error("should report hotWorker weight error")
	}
}

func TestValidate_MemoryPercentOutOfRange(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Scoring.MemoryPercentHot = 120

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "memoryPercentHot") {
		t.Error("should report memoryPercentHot error")
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConnectPort = 0
	cfg.Concurrency = -1
	cfg.OutputFormat = "csv"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	// All three errors should be reported.
	errStr := err.Error()
	if !strings.Contains(errStr, "connectPort") ||
		!strings.Contains(errStr, "concurrency") ||
		!strings.Contains(errStr, "outputFormat") {
		t.Errorf("should report all errors, got: %s", errStr)
	}
}

// --- applyDefaults ---

func TestApplyDefaults_FillsZeroValues(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	if cfg.ConnectPort != 8083 {
		t.Errorf("ConnectPort=%d, want 8083", cfg.ConnectPort)
	}
	if cfg.MetricsPort != 9404 {
		t.Errorf("MetricsPort=%d, want 9404", cfg.MetricsPort)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout=%s, want 30s", cfg.Timeout)
	}
	if cfg.OutputFormat != "table" {
		t.Errorf("OutputFormat=%s, want table", cfg.OutputFormat)
	}
	if cfg.TopN != 10 {
		t.Errorf("TopN=%d, want 10", cfg.TopN)
	}
	if cfg.Concurrency != 10 {
		t.Errorf("Concurrency=%d, want 10", cfg.Concurrency)
	}
}

func TestApplyDefaults_DoesNotOverrideSetValues(t *testing.T) {
	cfg := &Config{
		ConnectPort: 9090,
		Timeout:     60 * time.Second,
	}
	applyDefaults(cfg)

	if cfg.ConnectPort != 9090 {
		t.Errorf("ConnectPort=%d, should not be overridden", cfg.ConnectPort)
	}
	if cfg.Timeout != 60*time.Second {
		t.Errorf("Timeout=%s, should not be overridden", cfg.Timeout)
	}
}
