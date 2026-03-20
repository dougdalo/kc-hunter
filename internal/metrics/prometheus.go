package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

// PrometheusProvider fetches connector metrics from a Prometheus HTTP API.
type PrometheusProvider struct {
	baseURL string
	http    *http.Client
}

func NewPrometheusProvider(baseURL string, timeout time.Duration) *PrometheusProvider {
	return &PrometheusProvider{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

func (p *PrometheusProvider) Available(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/-/healthy", nil)
	if err != nil {
		return false
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (p *PrometheusProvider) GetConnectorMetrics(ctx context.Context, connectorName string, taskID int) (*models.ConnectorMetrics, error) {
	m := &models.ConnectorMetrics{
		ConnectorName: connectorName,
		TaskID:        taskID,
		Raw:           make(map[string]float64),
	}

	// Each query targets a specific JMX metric exposed via Strimzi's JMX exporter.
	queries := map[string]string{
		"poll_batch_avg_time_ms": fmt.Sprintf(
			`kafka_connect_source_task_metrics_poll_batch_avg_time_ms{connector="%s",task="%d"}`, connectorName, taskID),
		"put_batch_avg_time_ms": fmt.Sprintf(
			`kafka_connect_sink_task_metrics_put_batch_avg_time_ms{connector="%s",task="%d"}`, connectorName, taskID),
		"source_record_poll_rate": fmt.Sprintf(
			`kafka_connect_source_task_metrics_source_record_poll_rate{connector="%s",task="%d"}`, connectorName, taskID),
		"sink_record_read_rate": fmt.Sprintf(
			`kafka_connect_sink_task_metrics_sink_record_read_rate{connector="%s",task="%d"}`, connectorName, taskID),
		"batch_size_avg": fmt.Sprintf(
			`kafka_connect_task_metrics_batch_size_avg{connector="%s",task="%d"}`, connectorName, taskID),
		"offset_commit_avg_time_ms": fmt.Sprintf(
			`kafka_connect_task_metrics_offset_commit_avg_time_ms{connector="%s",task="%d"}`, connectorName, taskID),
	}

	for key, query := range queries {
		val, err := p.instantQuery(ctx, query)
		if err != nil {
			continue
		}
		m.Raw[key] = val
	}

	m.PollBatchAvgTimeMs = m.Raw["poll_batch_avg_time_ms"]
	m.PutBatchAvgTimeMs = m.Raw["put_batch_avg_time_ms"]
	m.SourceRecordPollRate = m.Raw["source_record_poll_rate"]
	m.SinkRecordReadRate = m.Raw["sink_record_read_rate"]
	m.BatchSizeAvg = m.Raw["batch_size_avg"]
	m.OffsetCommitAvgTimeMs = m.Raw["offset_commit_avg_time_ms"]

	return m, nil
}

func (p *PrometheusProvider) GetAllMetrics(ctx context.Context, _ string) (map[string]*models.ConnectorMetrics, error) {
	// Full implementation would use label_values() to enumerate connectors,
	// then batch-query. For MVP, callers use GetConnectorMetrics per connector.
	return map[string]*models.ConnectorMetrics{}, nil
}

// --- Prometheus response parsing ---

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]interface{}    `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (p *PrometheusProvider) instantQuery(ctx context.Context, query string) (float64, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", p.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, err
	}

	if pr.Status != "success" || len(pr.Data.Result) == 0 {
		return 0, fmt.Errorf("no result for query: %s", query)
	}

	valStr, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected prometheus value type")
	}

	return strconv.ParseFloat(valStr, 64)
}
