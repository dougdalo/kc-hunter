package metrics

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

// ScrapeProvider fetches metrics by directly hitting the /metrics endpoint
// on each Kafka Connect pod. Works when Strimzi exposes JMX via the
// Prometheus JMX exporter sidecar (port 9404 by default).
type ScrapeProvider struct {
	metricsPort int
	http        *http.Client
}

func NewScrapeProvider(metricsPort int, timeout time.Duration) *ScrapeProvider {
	if metricsPort == 0 {
		metricsPort = 9404
	}
	return &ScrapeProvider{
		metricsPort: metricsPort,
		http:        &http.Client{Timeout: timeout},
	}
}

func (s *ScrapeProvider) Available(_ context.Context) bool { return true }

func (s *ScrapeProvider) GetConnectorMetrics(_ context.Context, _ string, _ int) (*models.ConnectorMetrics, error) {
	return nil, fmt.Errorf("scrape provider: use GetAllMetrics with a pod URL")
}

// GetAllMetrics scrapes /metrics from a pod and parses connector-level JMX metrics.
// podURL should be the pod's Connect REST base URL (e.g. http://10.0.1.5:8083);
// the port is replaced with the JMX exporter port.
func (s *ScrapeProvider) GetAllMetrics(ctx context.Context, podURL string) (map[string]*models.ConnectorMetrics, error) {
	metricsURL := replacePort(podURL, s.metricsPort) + "/metrics"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape %s: %w", metricsURL, err)
	}
	defer resp.Body.Close()

	return parseExposition(resp.Body)
}

// parseExposition parses Prometheus exposition format, extracting kafka_connect_* metrics.
func parseExposition(r io.Reader) (map[string]*models.ConnectorMetrics, error) {
	result := make(map[string]*models.ConnectorMetrics)
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		if !strings.HasPrefix(line, "kafka_connect_") {
			continue
		}

		metricName, labels, value := parseLine(line)
		if metricName == "" {
			continue
		}

		connector, ok := labels["connector"]
		if !ok {
			continue
		}

		taskID := 0
		if t, ok := labels["task"]; ok {
			taskID, _ = strconv.Atoi(t)
		}

		key := MetricsKey(connector, taskID)
		m := result[key]
		if m == nil {
			m = &models.ConnectorMetrics{
				ConnectorName: connector,
				TaskID:        taskID,
				Raw:           make(map[string]float64),
			}
			result[key] = m
		}

		m.Raw[metricName] = value
		mapToStructured(m, metricName, value)
	}

	return result, scanner.Err()
}

func mapToStructured(m *models.ConnectorMetrics, name string, val float64) {
	switch {
	case strings.Contains(name, "poll_batch_avg_time_ms"):
		m.PollBatchAvgTimeMs = val
	case strings.Contains(name, "put_batch_avg_time_ms"):
		m.PutBatchAvgTimeMs = val
	case strings.Contains(name, "source_record_poll_rate"):
		m.SourceRecordPollRate = val
	case strings.Contains(name, "sink_record_read_rate"):
		m.SinkRecordReadRate = val
	case strings.Contains(name, "batch_size_avg"):
		m.BatchSizeAvg = val
	case strings.Contains(name, "offset_commit_avg_time_ms"):
		m.OffsetCommitAvgTimeMs = val
	}
}

// parseLine handles: metric_name{label="val",...} 123.45
func parseLine(line string) (string, map[string]string, float64) {
	labels := make(map[string]string)

	braceStart := strings.IndexByte(line, '{')
	if braceStart == -1 {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return "", nil, 0
		}
		val, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return "", nil, 0
		}
		return parts[0], labels, val
	}

	name := line[:braceStart]
	braceEnd := strings.IndexByte(line, '}')
	if braceEnd == -1 || braceEnd <= braceStart {
		return "", nil, 0
	}

	for _, pair := range strings.Split(line[braceStart+1:braceEnd], ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			labels[kv[0]] = strings.Trim(kv[1], `"`)
		}
	}

	valStr := strings.TrimSpace(line[braceEnd+1:])
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return "", nil, 0
	}

	return name, labels, val
}

// replacePort swaps the port in a URL to the given port.
// Handles IPv4 (http://10.0.0.1:8083) and IPv6 (http://[::1]:8083) correctly.
func replacePort(rawURL string, port int) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := u.Hostname() // strips brackets from IPv6
	if strings.Contains(host, ":") {
		// IPv6 — must bracket the address
		host = "[" + host + "]"
	}
	u.Host = fmt.Sprintf("%s:%d", host, port)
	return u.String()
}
