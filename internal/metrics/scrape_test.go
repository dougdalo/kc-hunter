package metrics

import (
	"strings"
	"testing"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

// --- replacePort ---

func TestReplacePort_IPv4(t *testing.T) {
	got := replacePort("http://10.0.0.1:8083", 9404)
	if got != "http://10.0.0.1:9404" {
		t.Errorf("got %q, want http://10.0.0.1:9404", got)
	}
}

func TestReplacePort_IPv4_NoPort(t *testing.T) {
	got := replacePort("http://10.0.0.1", 9404)
	if got != "http://10.0.0.1:9404" {
		t.Errorf("got %q, want http://10.0.0.1:9404", got)
	}
}

func TestReplacePort_IPv6(t *testing.T) {
	got := replacePort("http://[::1]:8083", 9404)
	if got != "http://[::1]:9404" {
		t.Errorf("got %q, want http://[::1]:9404", got)
	}
}

func TestReplacePort_IPv6_FullAddress(t *testing.T) {
	got := replacePort("http://[fd00::1:2:3]:8083", 9404)
	if got != "http://[fd00::1:2:3]:9404" {
		t.Errorf("got %q, want http://[fd00::1:2:3]:9404", got)
	}
}

func TestReplacePort_InvalidURL(t *testing.T) {
	got := replacePort("not-a-url", 9404)
	if got != "not-a-url" {
		t.Errorf("invalid URL should be returned as-is, got %q", got)
	}
}

// --- parseLine ---

func TestParseLine_WithLabels(t *testing.T) {
	line := `kafka_connect_source_task_metrics_poll_batch_avg_time_ms{connector="my-conn",task="0"} 123.45`
	name, labels, val := parseLine(line)

	if name != "kafka_connect_source_task_metrics_poll_batch_avg_time_ms" {
		t.Errorf("name=%q", name)
	}
	if labels["connector"] != "my-conn" {
		t.Errorf("connector=%q", labels["connector"])
	}
	if labels["task"] != "0" {
		t.Errorf("task=%q", labels["task"])
	}
	if val != 123.45 {
		t.Errorf("val=%f, want 123.45", val)
	}
}

func TestParseLine_NoLabels(t *testing.T) {
	line := "some_metric 42"
	name, labels, val := parseLine(line)

	if name != "some_metric" {
		t.Errorf("name=%q", name)
	}
	if len(labels) != 0 {
		t.Errorf("expected no labels, got %v", labels)
	}
	if val != 42 {
		t.Errorf("val=%f, want 42", val)
	}
}

func TestParseLine_MalformedLine(t *testing.T) {
	tests := []string{
		"",
		"just-a-name",
		"name{broken 123",
	}
	for _, line := range tests {
		name, _, _ := parseLine(line)
		if name != "" {
			t.Errorf("malformed line %q should return empty name, got %q", line, name)
		}
	}
}

// --- parseExposition ---

func TestParseExposition_ExtractsKafkaConnectMetrics(t *testing.T) {
	input := `# HELP kafka_connect_source_task_metrics_poll_batch_avg_time_ms Avg poll time
# TYPE kafka_connect_source_task_metrics_poll_batch_avg_time_ms gauge
kafka_connect_source_task_metrics_poll_batch_avg_time_ms{connector="my-source",task="0"} 250.5
kafka_connect_sink_task_metrics_put_batch_avg_time_ms{connector="my-sink",task="1"} 100.0
unrelated_metric{foo="bar"} 999
`
	result, err := parseExposition(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	sourceKey := MetricsKey("my-source", 0)
	m := result[sourceKey]
	if m == nil {
		t.Fatal("expected metrics for my-source/0")
	}
	if m.PollBatchAvgTimeMs != 250.5 {
		t.Errorf("PollBatchAvgTimeMs=%f, want 250.5", m.PollBatchAvgTimeMs)
	}

	sinkKey := MetricsKey("my-sink", 1)
	m2 := result[sinkKey]
	if m2 == nil {
		t.Fatal("expected metrics for my-sink/1")
	}
	if m2.PutBatchAvgTimeMs != 100.0 {
		t.Errorf("PutBatchAvgTimeMs=%f, want 100.0", m2.PutBatchAvgTimeMs)
	}

	// Unrelated metric should not appear.
	if len(result) != 2 {
		t.Errorf("expected 2 entries, got %d", len(result))
	}
}

func TestParseExposition_EmptyInput(t *testing.T) {
	result, err := parseExposition(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("empty input should produce empty result, got %d", len(result))
	}
}

func TestParseExposition_CommentsOnly(t *testing.T) {
	input := "# just comments\n# and more comments\n"
	result, err := parseExposition(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("comments-only input should produce empty result")
	}
}

func TestParseExposition_NoConnectorLabel(t *testing.T) {
	// kafka_connect metric but without connector label should be skipped.
	input := `kafka_connect_worker_connector_count 5`
	result, err := parseExposition(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("metric without connector label should be skipped")
	}
}

// --- mapToStructured ---

func TestMapToStructured_AllFields(t *testing.T) {
	tests := []struct {
		metricName string
		checkField string
	}{
		{"kafka_connect_poll_batch_avg_time_ms", "PollBatchAvgTimeMs"},
		{"kafka_connect_put_batch_avg_time_ms", "PutBatchAvgTimeMs"},
		{"kafka_connect_source_record_poll_rate", "SourceRecordPollRate"},
		{"kafka_connect_sink_record_read_rate", "SinkRecordReadRate"},
		{"kafka_connect_batch_size_avg", "BatchSizeAvg"},
		{"kafka_connect_offset_commit_avg_time_ms", "OffsetCommitAvgTimeMs"},
	}

	for _, tt := range tests {
		t.Run(tt.checkField, func(t *testing.T) {
			m := &models.ConnectorMetrics{}
			mapToStructured(m, tt.metricName, 42.0)
			// Verify the right field was set by checking it's non-zero.
			switch tt.checkField {
			case "PollBatchAvgTimeMs":
				if m.PollBatchAvgTimeMs != 42.0 {
					t.Errorf("PollBatchAvgTimeMs=%f", m.PollBatchAvgTimeMs)
				}
			case "PutBatchAvgTimeMs":
				if m.PutBatchAvgTimeMs != 42.0 {
					t.Errorf("PutBatchAvgTimeMs=%f", m.PutBatchAvgTimeMs)
				}
			case "SourceRecordPollRate":
				if m.SourceRecordPollRate != 42.0 {
					t.Errorf("SourceRecordPollRate=%f", m.SourceRecordPollRate)
				}
			case "SinkRecordReadRate":
				if m.SinkRecordReadRate != 42.0 {
					t.Errorf("SinkRecordReadRate=%f", m.SinkRecordReadRate)
				}
			case "BatchSizeAvg":
				if m.BatchSizeAvg != 42.0 {
					t.Errorf("BatchSizeAvg=%f", m.BatchSizeAvg)
				}
			case "OffsetCommitAvgTimeMs":
				if m.OffsetCommitAvgTimeMs != 42.0 {
					t.Errorf("OffsetCommitAvgTimeMs=%f", m.OffsetCommitAvgTimeMs)
				}
			}
		})
	}
}

// --- MetricsKey ---

func TestMetricsKey(t *testing.T) {
	if got := MetricsKey("my-conn", 3); got != "my-conn/3" {
		t.Errorf("MetricsKey=%q, want my-conn/3", got)
	}
}
