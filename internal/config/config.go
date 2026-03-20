// Package config provides configuration loading for kc-hunter.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime settings.
type Config struct {
	Kubeconfig string   `yaml:"kubeconfig"`
	Namespaces []string `yaml:"namespaces"`
	Labels     string   `yaml:"labels"`

	ConnectURLs []string `yaml:"connectURLs"` // explicit; auto-discovered if empty
	ConnectPort int      `yaml:"connectPort"` // default 8083

	PrometheusURL string `yaml:"prometheusURL"`
	MetricsSource string `yaml:"metricsSource"` // "prometheus", "scrape", "none"
	MetricsPort   int    `yaml:"metricsPort"`   // JMX exporter port, default 9404

	Timeout      time.Duration `yaml:"timeout"`
	OutputFormat string        `yaml:"outputFormat"` // "table" or "json"
	TopN         int           `yaml:"topN"`

	// Concurrency controls how many parallel requests to the Connect REST API.
	// With 870 connectors, sequential is too slow; unlimited is too aggressive.
	Concurrency int `yaml:"concurrency"`

	// UseProxy routes Connect REST calls through the K8s API server proxy
	// instead of direct HTTP to pod IPs. Required when the client machine
	// (e.g. a bastion host) cannot reach the pod network directly.
	UseProxy bool `yaml:"useProxy"`

	Scoring ScoringConfig `yaml:"scoring"`

	// Explain enables detailed score breakdown output.
	Explain bool `yaml:"-"`
}

// ScoringConfig controls the suspicion scoring engine behavior.
type ScoringConfig struct {
	// Thresholds — when each signal fires.
	MemoryPercentHot   float64 `yaml:"memoryPercentHot"`
	PollTimeHighMs     float64 `yaml:"pollTimeHighMs"`
	PutTimeHighMs      float64 `yaml:"putTimeHighMs"`
	BatchSizeHigh      float64 `yaml:"batchSizeHigh"`
	HighTaskCount      int     `yaml:"highTaskCount"`
	HighRetryCount     float64 `yaml:"highRetryCount"`
	OffsetCommitHighMs float64 `yaml:"offsetCommitHighMs"`

	// Weights — how much each signal contributes to the score (0–100).
	Weights ScoringWeights `yaml:"weights"`

	// RiskyClasses — connector class patterns matched by substring.
	// e.g. "io.debezium" matches "io.debezium.connector.mysql.MySqlConnector".
	RiskyClasses []string `yaml:"riskyClasses"`
}

// ScoringWeights controls the relative importance of each scoring signal.
// Setting a weight to 0 effectively disables that signal.
type ScoringWeights struct {
	HotWorker     int `yaml:"hotWorker"`
	FailedTask    int `yaml:"failedTask"`
	HighRetry     int `yaml:"highRetry"`
	HighPollTime  int `yaml:"highPollTime"`
	HighPutTime   int `yaml:"highPutTime"`
	HighBatch     int `yaml:"highBatch"`
	HighTaskCount int `yaml:"highTaskCount"`
	RiskyClass    int `yaml:"riskyClass"`
	HighCommit    int `yaml:"highCommit"`
}

// DefaultScoringConfig returns production-reasonable scoring defaults.
func DefaultScoringConfig() ScoringConfig {
	return ScoringConfig{
		MemoryPercentHot:   80.0,
		PollTimeHighMs:     5000,
		PutTimeHighMs:      5000,
		BatchSizeHigh:      10000,
		HighTaskCount:      5,
		HighRetryCount:     10,
		OffsetCommitHighMs: 10000,
		Weights: ScoringWeights{
			HotWorker:     25,
			FailedTask:    20,
			HighRetry:     15,
			HighPollTime:  10,
			HighPutTime:   10,
			HighBatch:     10,
			HighTaskCount: 10,
			RiskyClass:    5,
			HighCommit:    5,
		},
		RiskyClasses: DefaultRiskyClasses(),
	}
}

// DefaultRiskyClasses returns the built-in list of high-memory connector classes.
func DefaultRiskyClasses() []string {
	return []string{
		"io.confluent.connect.jdbc.JdbcSourceConnector",
		"io.confluent.connect.jdbc.JdbcSinkConnector",
		"org.apache.camel.kafkaconnector.ftp.CamelFtpSourceConnector",
		"org.apache.kafka.connect.file.FileStreamSourceConnector",
		"io.debezium.connector.mysql.MySqlConnector",
		"io.debezium.connector.postgresql.PostgresConnector",
		"io.confluent.connect.s3.S3SinkConnector",
		"io.confluent.connect.elasticsearch.ElasticsearchSinkConnector",
	}
}

// DefaultConfig returns sensible defaults for Strimzi environments.
func DefaultConfig() *Config {
	return &Config{
		Namespaces:    []string{""},
		Labels:        "strimzi.io/kind=KafkaConnect",
		ConnectPort:   8083,
		MetricsPort:   9404,
		MetricsSource: "none",
		Timeout:       30 * time.Second,
		OutputFormat:  "table",
		TopN:          10,
		Concurrency:   10,
		UseProxy:      false,
		Scoring:       DefaultScoringConfig(),
	}
}

// LoadFromFile reads config from a YAML file, merging with defaults.
func LoadFromFile(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	applyDefaults(cfg)
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.ConnectPort == 0 {
		cfg.ConnectPort = 8083
	}
	if cfg.MetricsPort == 0 {
		cfg.MetricsPort = 9404
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.OutputFormat == "" {
		cfg.OutputFormat = "table"
	}
	if cfg.TopN == 0 {
		cfg.TopN = 10
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 10
	}
	applyScoringDefaults(&cfg.Scoring)
}

func applyScoringDefaults(s *ScoringConfig) {
	d := DefaultScoringConfig()
	if s.MemoryPercentHot == 0 {
		s.MemoryPercentHot = d.MemoryPercentHot
	}
	if s.PollTimeHighMs == 0 {
		s.PollTimeHighMs = d.PollTimeHighMs
	}
	if s.PutTimeHighMs == 0 {
		s.PutTimeHighMs = d.PutTimeHighMs
	}
	if s.BatchSizeHigh == 0 {
		s.BatchSizeHigh = d.BatchSizeHigh
	}
	if s.HighTaskCount == 0 {
		s.HighTaskCount = d.HighTaskCount
	}
	if s.HighRetryCount == 0 {
		s.HighRetryCount = d.HighRetryCount
	}
	if s.OffsetCommitHighMs == 0 {
		s.OffsetCommitHighMs = d.OffsetCommitHighMs
	}
	if s.RiskyClasses == nil {
		s.RiskyClasses = d.RiskyClasses
	}
}

// Validate checks that the resolved configuration has valid values.
// Returns a multi-line error listing every invalid field.
func (c *Config) Validate() error {
	var errs []string

	if c.ConnectPort < 1 || c.ConnectPort > 65535 {
		errs = append(errs, fmt.Sprintf("connectPort: %d is not a valid port (1-65535)", c.ConnectPort))
	}
	if c.MetricsPort < 1 || c.MetricsPort > 65535 {
		errs = append(errs, fmt.Sprintf("metricsPort: %d is not a valid port (1-65535)", c.MetricsPort))
	}
	if c.Concurrency < 1 {
		errs = append(errs, fmt.Sprintf("concurrency: must be >= 1, got %d", c.Concurrency))
	}
	if c.TopN < 1 {
		errs = append(errs, fmt.Sprintf("topN: must be >= 1, got %d", c.TopN))
	}
	if c.Timeout <= 0 {
		errs = append(errs, fmt.Sprintf("timeout: must be > 0, got %s", c.Timeout))
	}

	switch c.OutputFormat {
	case "table", "json":
	default:
		errs = append(errs, fmt.Sprintf(
			"outputFormat: must be \"table\" or \"json\", got %q", c.OutputFormat))
	}

	switch c.MetricsSource {
	case "prometheus", "scrape", "none":
	default:
		errs = append(errs, fmt.Sprintf(
			"metricsSource: must be \"prometheus\", \"scrape\", or \"none\", got %q",
			c.MetricsSource))
	}

	// Scoring thresholds.
	s := c.Scoring
	if s.MemoryPercentHot < 0 || s.MemoryPercentHot > 100 {
		errs = append(errs, fmt.Sprintf(
			"scoring.memoryPercentHot: must be 0-100, got %.1f", s.MemoryPercentHot))
	}
	if s.HighTaskCount < 1 {
		errs = append(errs, fmt.Sprintf("scoring.highTaskCount: must be >= 1, got %d", s.HighTaskCount))
	}

	// Scoring weights — negative weights are invalid.
	w := s.Weights
	for name, val := range map[string]int{
		"hotWorker": w.HotWorker, "failedTask": w.FailedTask,
		"highRetry": w.HighRetry, "highPollTime": w.HighPollTime,
		"highPutTime": w.HighPutTime, "highBatch": w.HighBatch,
		"highTaskCount": w.HighTaskCount, "riskyClass": w.RiskyClass,
		"highCommit": w.HighCommit,
	} {
		if val < 0 {
			errs = append(errs, fmt.Sprintf("scoring.weights.%s: must be >= 0, got %d", name, val))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  %s", strings.Join(errs, "\n  "))
}
