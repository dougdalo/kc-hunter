// Package models defines the core domain types shared across kc-hunter.
// All types use JSON tags for machine-readable output.
package models

import "time"

// PodInfo holds Kubernetes pod details and resource usage.
type PodInfo struct {
	Name          string            `json:"name"`
	Namespace     string            `json:"namespace"`
	NodeName      string            `json:"node"`
	ClusterName   string            `json:"cluster"`     // strimzi.io/cluster label
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
	Type       string            `json:"type"`  // "source" or "sink"
	State      string            `json:"state"` // RUNNING, PAUSED, FAILED, UNASSIGNED
	WorkerID   string            `json:"workerID"`
	Config     map[string]string `json:"-"` // omit full config from default JSON
	Tasks      []TaskInfo        `json:"tasks"`
	ClassName  string            `json:"class"`
	ClusterURL string            `json:"clusterURL"`
}

// TaskInfo holds the state of a single connector task.
type TaskInfo struct {
	ConnectorName string `json:"connector"`
	TaskID        int    `json:"taskID"`
	State         string `json:"state"` // RUNNING, FAILED, UNASSIGNED
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

// CollectionStats tracks what data was successfully collected during a
// diagnostic run. This enables confidence assessment — a suspect score of 75
// means something different with 9/9 signals evaluable vs 4/9.
type CollectionStats struct {
	PodsDiscovered   int      `json:"podsDiscovered"`
	PodsWithMetrics  int      `json:"podsWithMetrics"`
	MetricsSource    string   `json:"metricsSource"`              // "prometheus", "scrape", "none"
	MetricsCollected int      `json:"metricsCollected"`           // connector/task metrics fetched
	ConnectorTotal   int      `json:"connectorTotal"`             // total connectors attempted
	ConnectorErrors  int      `json:"connectorErrors"`            // failed connector fetches
	TotalTasks       int      `json:"totalTasks"`                 // total tasks scored
	SignalsEvaluable int      `json:"signalsEvaluable"`           // how many of 9 signals had data
	Confidence       string   `json:"confidence"`                 // "high", "reduced", "low"
	Warnings         []string `json:"warnings,omitempty"`         // structured warnings from collection
	RetryAttempts    int      `json:"retryAttempts,omitempty"`    // total retry attempts across steps
}

// ClusterDiagnostic is the top-level report for one Kafka Connect cluster.
type ClusterDiagnostic struct {
	ClusterName string           `json:"cluster"`
	Pods        []PodInfo        `json:"pods"`
	HottestPod  *PodInfo         `json:"hottestPod,omitempty"`
	Workers     []WorkerInfo     `json:"workers"`
	Suspects    []SuspectReport  `json:"suspects"`
	Coverage    *CollectionStats `json:"coverage,omitempty"`
	CollectedAt time.Time        `json:"collectedAt"`
}

// DiagnosticReport is the full output across all clusters.
type DiagnosticReport struct {
	Clusters    []ClusterDiagnostic `json:"clusters"`
	CollectedAt time.Time           `json:"collectedAt"`
}

// SnapshotMeta records the collection context — which namespaces, labels,
// transport mode, and metrics source were used. Crucial for interpreting
// diffs: a score change means nothing if the collection parameters differ.
type SnapshotMeta struct {
	Namespaces    []string `json:"namespaces,omitempty"`
	Labels        string   `json:"labels,omitempty"`
	Transport     string   `json:"transport,omitempty"`     // "exec", "proxy", "direct"
	MetricsSource string   `json:"metricsSource,omitempty"` // "prometheus", "scrape", "none"
}

// Snapshot captures cluster state at a point in time for later comparison.
type Snapshot struct {
	Version     string            `json:"version"`
	CollectedAt time.Time         `json:"collectedAt"`
	Meta        *SnapshotMeta     `json:"meta,omitempty"`
	Clusters    []ClusterSnapshot `json:"clusters"`
}

// PodSnapshot captures pod resource state for diffing.
type PodSnapshot struct {
	Name          string  `json:"name"`
	NodeName      string  `json:"node"`
	MemoryUsage   int64   `json:"memoryUsage"`
	MemoryLimit   int64   `json:"memoryLimit"`
	MemoryPercent float64 `json:"memoryPercent"`
	RestartCount  int32   `json:"restartCount"`
	Ready         bool    `json:"ready"`
}

// ClusterSnapshot captures one cluster's connectors, pods, and suspect scores.
type ClusterSnapshot struct {
	ClusterName string              `json:"cluster"`
	Pods        []PodSnapshot       `json:"pods,omitempty"`
	Connectors  []ConnectorSnapshot `json:"connectors"`
	Suspects    []SuspectReport     `json:"suspects"`
}

// TaskSnapshot captures individual task state for diffing.
type TaskSnapshot struct {
	TaskID   int    `json:"taskID"`
	State    string `json:"state"`
	WorkerID string `json:"workerID"`
}

// ConnectorSnapshot captures connector state for diffing.
type ConnectorSnapshot struct {
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	State     string         `json:"state"`
	ClassName string         `json:"class"`
	TaskCount int            `json:"taskCount"`
	Tasks     []TaskSnapshot `json:"tasks,omitempty"`
}

// DiffSummary provides an executive overview of changes between two snapshots.
type DiffSummary struct {
	TimeDelta          string `json:"timeDelta"`
	ConnectorsAdded    int    `json:"connectorsAdded,omitempty"`
	ConnectorsRemoved  int    `json:"connectorsRemoved,omitempty"`
	ConnectorsChanged  int    `json:"connectorsChanged,omitempty"`
	TaskStateChanges   int    `json:"taskStateChanges,omitempty"`
	ScoreIncreases     int    `json:"scoreIncreases,omitempty"`
	ScoreDecreases     int    `json:"scoreDecreases,omitempty"`
	MaxScoreDelta      int    `json:"maxScoreDelta,omitempty"`
	PodRestartChanges  int    `json:"podRestartChanges,omitempty"`
	PodsAdded          int    `json:"podsAdded,omitempty"`
	PodsRemoved        int    `json:"podsRemoved,omitempty"`
	MetaChanged        bool   `json:"metaChanged,omitempty"`
	Headline           string `json:"headline"`
}

// DiffReport holds the result of comparing two snapshots.
type DiffReport struct {
	BeforeTime time.Time           `json:"beforeTime"`
	AfterTime  time.Time           `json:"afterTime"`
	Summary    *DiffSummary        `json:"summary,omitempty"`
	MetaDiff   *MetaDiff           `json:"metaDiff,omitempty"`
	Clusters   []ClusterDiffReport `json:"clusters"`
}

// MetaDiff describes changes in collection parameters between snapshots.
type MetaDiff struct {
	Changes []string `json:"changes"`
}

// PodChange describes what changed for a pod between snapshots.
type PodChange struct {
	Name    string   `json:"name"`
	Changes []string `json:"changes"`
}

// ClusterDiffReport holds diff results for a single cluster.
type ClusterDiffReport struct {
	ClusterName       string              `json:"cluster"`
	AddedPods         []PodSnapshot       `json:"addedPods,omitempty"`
	RemovedPods       []PodSnapshot       `json:"removedPods,omitempty"`
	ChangedPods       []PodChange         `json:"changedPods,omitempty"`
	AddedConnectors   []ConnectorSnapshot `json:"addedConnectors,omitempty"`
	RemovedConnectors []ConnectorSnapshot `json:"removedConnectors,omitempty"`
	ChangedConnectors []ConnectorChange   `json:"changedConnectors,omitempty"`
	AddedSuspects     []SuspectReport     `json:"addedSuspects,omitempty"`
	RemovedSuspects   []SuspectReport     `json:"removedSuspects,omitempty"`
	ChangedSuspects   []SuspectChange     `json:"changedSuspects,omitempty"`
}

// ConnectorChange describes what changed for a connector between snapshots.
type ConnectorChange struct {
	Name        string   `json:"name"`
	Changes     []string `json:"changes"`
	TaskChanges []string `json:"taskChanges,omitempty"`
}

// SuspectChange describes score/signal changes for a connector/task.
type SuspectChange struct {
	ConnectorName string   `json:"connector"`
	TaskID        int      `json:"taskID"`
	ScoreBefore   int      `json:"scoreBefore"`
	ScoreAfter    int      `json:"scoreAfter"`
	ScoreDelta    int      `json:"scoreDelta"`
	Changes       []string `json:"changes"`
}
