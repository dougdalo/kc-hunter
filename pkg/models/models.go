// Package models defines the core domain types shared across kc-hunter.
// All types use JSON tags for machine-readable output.
package models

import "time"

// PodInfo holds Kubernetes pod details and resource usage.
type PodInfo struct {
	Name          string            `json:"name"`
	Namespace     string            `json:"namespace"`
	NodeName      string            `json:"node"`
	ClusterName   string            `json:"cluster"`    // strimzi.io/cluster label
	MemoryUsage   int64             `json:"memoryUsage"` // bytes, from metrics-server
	MemoryLimit   int64             `json:"memoryLimit"` // bytes, from pod spec
	MemoryPercent float64           `json:"memoryPercent"`
	CPUUsage      int64             `json:"cpuMillicores"`
	Labels        map[string]string `json:"-"`
	IP            string            `json:"ip"`
	ConnectURL    string            `json:"connectURL"` // http://<podIP>:<port>
	Ready         bool              `json:"ready"`
	RestartCount  int32             `json:"restartCount"`
}

// ConnectorInfo holds the state of a Kafka Connect connector.
type ConnectorInfo struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`      // "source" or "sink"
	State      string            `json:"state"`     // RUNNING, PAUSED, FAILED, UNASSIGNED
	WorkerID   string            `json:"workerID"`
	Config     map[string]string `json:"-"`         // omit full config from default JSON
	Tasks      []TaskInfo        `json:"tasks"`
	ClassName  string            `json:"class"`
	ClusterURL string            `json:"clusterURL"`
}

// TaskInfo holds the state of a single connector task.
type TaskInfo struct {
	ConnectorName string `json:"connector"`
	TaskID        int    `json:"taskID"`
	State         string `json:"state"`    // RUNNING, FAILED, UNASSIGNED
	WorkerID      string `json:"workerID"`
	Trace         string `json:"trace,omitempty"`
}

// WorkerInfo aggregates data about a Kafka Connect worker process.
type WorkerInfo struct {
	WorkerID   string          `json:"workerID"`
	Pod        *PodInfo        `json:"pod,omitempty"`
	Connectors []ConnectorInfo `json:"connectors"`
	Tasks      []TaskInfo      `json:"tasks"`
	TaskCount  int             `json:"taskCount"`
}

// ConnectorMetrics holds metrics scraped for a connector/task.
type ConnectorMetrics struct {
	ConnectorName string `json:"connector"`
	TaskID        int    `json:"taskID"`

	// Source connector metrics
	PollBatchAvgTimeMs      float64 `json:"pollBatchAvgTimeMs,omitempty"`
	SourceRecordPollRate    float64 `json:"sourceRecordPollRate,omitempty"`
	SourceRecordActiveCount float64 `json:"sourceRecordActiveCount,omitempty"`

	// Sink connector metrics
	PutBatchAvgTimeMs     float64 `json:"putBatchAvgTimeMs,omitempty"`
	SinkRecordReadRate    float64 `json:"sinkRecordReadRate,omitempty"`
	OffsetCommitAvgTimeMs float64 `json:"offsetCommitAvgTimeMs,omitempty"`

	// Common
	BatchSizeAvg          float64 `json:"batchSizeAvg,omitempty"`
	RetryCount            float64 `json:"retryCount,omitempty"`
	ErrorRate             float64 `json:"errorRate,omitempty"`
	TotalRecordsProcessed float64 `json:"totalRecordsProcessed,omitempty"`

	// Raw metrics for extensibility
	Raw map[string]float64 `json:"raw,omitempty"`
}

// SuspectReport is the diagnostic output for one connector/task.
type SuspectReport struct {
	ConnectorName  string          `json:"connector"`
	TaskID         int             `json:"taskID"`
	WorkerID       string          `json:"workerID"`
	PodName        string          `json:"pod"`
	Score          int             `json:"score"` // 0-100
	Reasons        []string        `json:"reasons"`
	Signals        []ScoringSignal `json:"signals"`
	Recommendation string          `json:"recommendation"`
}

// ScoringSignal is one factor that contributed to the suspicion score.
type ScoringSignal struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Weight      int    `json:"weight"`
	Active      bool   `json:"active"`
	Value       string `json:"value,omitempty"`
}

// ClusterDiagnostic is the top-level report for one Kafka Connect cluster.
type ClusterDiagnostic struct {
	ClusterName string       `json:"cluster"`
	Pods        []PodInfo    `json:"pods"`
	HottestPod  *PodInfo     `json:"hottestPod,omitempty"`
	Workers     []WorkerInfo `json:"workers"`
	Suspects    []SuspectReport `json:"suspects"`
	CollectedAt time.Time    `json:"collectedAt"`
}

// DiagnosticReport is the full output across all clusters.
type DiagnosticReport struct {
	Clusters    []ClusterDiagnostic `json:"clusters"`
	CollectedAt time.Time           `json:"collectedAt"`
}
