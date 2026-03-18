// Package config provides configuration loading for kcdiag.
package config

import (
	"os"
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
}
