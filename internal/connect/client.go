// Package connect handles Kafka Connect REST API interactions.
//
// It supports two transport modes:
//   - Direct HTTP: for in-cluster or when pod IPs are reachable
//   - K8s API proxy: for bastion hosts that cannot reach pod IPs
//
// Both modes use bounded concurrency for clusters with hundreds of connectors.
package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

// Transport abstracts how HTTP requests reach the Connect REST API.
// This lets us swap between direct HTTP and K8s API server proxy
// without changing any Connect protocol logic.
type Transport interface {
	// Get performs an HTTP GET against a Connect pod and returns a stream
	// of the response body. The caller must close the returned ReadCloser.
	// podRef identifies the target pod (either a URL for direct mode, or
	// structured pod identity for proxy mode).
	// path is the Connect REST path, e.g. "/connectors" or "/connectors/foo/status".
	Get(ctx context.Context, podRef PodRef, path string) (io.ReadCloser, error)
}

// PodRef identifies a Connect pod for both transport modes.
// In direct mode, URL is used. In proxy mode, Name+Namespace are used.
type PodRef struct {
	Name      string // pod name (used by proxy transport)
	Namespace string // pod namespace (used by proxy transport)
	URL       string // e.g. http://10.0.1.5:8083 (used by direct transport)
}

// Client wraps the Kafka Connect REST API.
type Client struct {
	transport   Transport
	concurrency int
}

// NewClient creates a Connect REST API client with the given transport.
func NewClient(transport Transport, concurrency int) *Client {
	if concurrency < 1 {
		concurrency = 10
	}
	return &Client{
		transport:   transport,
		concurrency: concurrency,
	}
}

// --- raw API response types ---

type connectorStatus struct {
	Name      string `json:"name"`
	Connector struct {
		State    string `json:"state"`
		WorkerID string `json:"worker_id"`
	} `json:"connector"`
	Tasks []struct {
		ID       int    `json:"id"`
		State    string `json:"state"`
		WorkerID string `json:"worker_id"`
		Trace    string `json:"trace,omitempty"`
	} `json:"tasks"`
	Type string `json:"type"`
}

// ListConnectors returns names of all connectors on a Connect cluster.
// Uses streaming JSON decode to avoid loading the entire array into a
// single []byte buffer — important for clusters with 800+ connectors.
func (c *Client) ListConnectors(ctx context.Context, ref PodRef) ([]string, error) {
	rc, err := c.transport.Get(ctx, ref, "/connectors")
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	dec := json.NewDecoder(rc)

	// Consume opening '['.
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parse connector list: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return nil, fmt.Errorf("parse connector list: expected '[', got %v", tok)
	}

	var names []string
	for dec.More() {
		var name string
		if err := dec.Decode(&name); err != nil {
			return nil, fmt.Errorf("parse connector name: %w", err)
		}
		names = append(names, name)
	}

	return names, nil
}

// GetConnectorStatus returns status including all task states.
func (c *Client) GetConnectorStatus(
	ctx context.Context, ref PodRef, name string,
) (*models.ConnectorInfo, error) {
	rc, err := c.transport.Get(ctx, ref, fmt.Sprintf("/connectors/%s/status", name))
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var status connectorStatus
	if err := json.NewDecoder(rc).Decode(&status); err != nil {
		return nil, fmt.Errorf("parse status for %s: %w", name, err)
	}

	info := &models.ConnectorInfo{
		Name:       status.Name,
		Type:       status.Type,
		State:      status.Connector.State,
		WorkerID:   status.Connector.WorkerID,
		ClusterURL: ref.URL,
	}

	for _, t := range status.Tasks {
		info.Tasks = append(info.Tasks, models.TaskInfo{
			ConnectorName: status.Name,
			TaskID:        t.ID,
			State:         t.State,
			WorkerID:      t.WorkerID,
			Trace:         t.Trace,
		})
	}

	return info, nil
}

// GetConnectorConfig returns the configuration of a connector.
func (c *Client) GetConnectorConfig(
	ctx context.Context, ref PodRef, name string,
) (map[string]string, error) {
	rc, err := c.transport.Get(ctx, ref, fmt.Sprintf("/connectors/%s/config", name))
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var cfg map[string]string
	if err := json.NewDecoder(rc).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config for %s: %w", name, err)
	}
	return cfg, nil
}

// FetchResult holds the outcome of a bulk connector fetch,
// including partial failure information.
type FetchResult struct {
	Connectors []models.ConnectorInfo
	Warnings   []string // per-connector error messages
	Total      int      // total connectors attempted
	Errors     int      // failed connector fetches
}

// GetAllConnectors fetches status + config for every connector on a cluster.
// Uses bounded concurrency to handle 870+ connectors without overwhelming
// the Connect REST API (or the K8s API server when proxying).
//
// Returns a FetchResult that includes both successful connectors and
// structured warnings for failures. The caller decides how to report them.
func (c *Client) GetAllConnectors(ctx context.Context, ref PodRef) (FetchResult, error) {
	names, err := c.ListConnectors(ctx, ref)
	if err != nil {
		return FetchResult{}, err
	}

	type result struct {
		info models.ConnectorInfo
		err  error
	}

	results := make([]result, len(names))
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup

	for i, name := range names {
		wg.Add(1)
		go func(idx int, n string) {
			defer wg.Done()

			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			info, err := c.GetConnectorStatus(ctx, ref, n)
			if err != nil {
				results[idx] = result{err: fmt.Errorf("status %s: %w", n, err)}
				return
			}

			// Fetch config to get connector.class. Non-fatal if it fails.
			cfg, err := c.GetConnectorConfig(ctx, ref, n)
			if err == nil {
				info.Config = cfg
				info.ClassName = cfg["connector.class"]
			}

			results[idx] = result{info: *info}
		}(i, name)
	}

	wg.Wait()

	fr := FetchResult{Total: len(names)}
	for _, r := range results {
		if r.err != nil {
			fr.Errors++
			fr.Warnings = append(fr.Warnings, r.err.Error())
			continue
		}
		fr.Connectors = append(fr.Connectors, r.info)
	}

	return fr, nil
}

// --- Transport implementations ---

// DirectTransport sends HTTP requests directly to pod IPs.
// Use when the client can reach the pod network.
type DirectTransport struct {
	http *http.Client
}

// NewDirectTransport creates a transport that calls pod IPs directly.
func NewDirectTransport(timeout time.Duration) *DirectTransport {
	return &DirectTransport{
		http: &http.Client{Timeout: timeout},
	}
}

func (d *DirectTransport) Get(ctx context.Context, ref PodRef, path string) (io.ReadCloser, error) {
	url := ref.URL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, truncate(string(body), 200))
	}

	// Return the body directly — caller decodes via json.NewDecoder
	// without buffering the entire payload into memory.
	return resp.Body, nil
}

// ProxyTransport routes requests through the K8s API server proxy.
// Use from bastion hosts that cannot reach pod IPs (10.x.x.x).
//
// Each request becomes:
//
//	GET /api/v1/namespaces/{ns}/pods/{pod}:{port}/proxy/{path}
//
// The API server forwards the request to the pod.
type ProxyTransport struct {
	proxyGet ProxyGetFunc
	port     int
}

// ProxyGetFunc is the signature of k8s.Client.ProxyGet, extracted as a
// function type to avoid a direct dependency from connect -> k8s.
type ProxyGetFunc func(
	ctx context.Context,
	namespace, podName string,
	port int,
	path string,
) ([]byte, error)

// NewProxyTransport creates a transport that uses the K8s API proxy.
func NewProxyTransport(proxyGet ProxyGetFunc, connectPort int) *ProxyTransport {
	return &ProxyTransport{
		proxyGet: proxyGet,
		port:     connectPort,
	}
}

func (p *ProxyTransport) Get(ctx context.Context, ref PodRef, path string) (io.ReadCloser, error) {
	if ref.Namespace == "" || ref.Name == "" {
		return nil, fmt.Errorf("proxy transport requires pod name and namespace, got ref=%+v", ref)
	}

	// Strip leading "/" from path — the K8s proxy Suffix handles it.
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	body, err := p.proxyGet(ctx, ref.Namespace, ref.Name, p.port, path)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// --- Exec-based Transport ---

// ExecFunc is the signature for executing a command inside a pod, extracted
// as a function type to avoid a direct dependency from connect -> k8s.
type ExecFunc func(
	ctx context.Context,
	namespace, podName, container string,
	command []string,
) (stdout, stderr string, err error)

// ExecTransport runs curl/wget inside the pod via kubectl exec to reach the
// Connect REST API on localhost. This bypasses all networking/routing issues
// (e.g. GKE timeouts) since the request never leaves the pod.
type ExecTransport struct {
	execFn    ExecFunc
	port      int
	container string
}

// NewExecTransport creates a transport that execs into pods to reach the
// Connect REST API. Container is the name of the Kafka Connect container.
func NewExecTransport(execFn ExecFunc, connectPort int, container string) *ExecTransport {
	return &ExecTransport{
		execFn:    execFn,
		port:      connectPort,
		container: container,
	}
}

func (e *ExecTransport) Get(ctx context.Context, ref PodRef, path string) (io.ReadCloser, error) {
	if ref.Namespace == "" || ref.Name == "" {
		return nil, fmt.Errorf("exec transport requires pod name and namespace, got ref=%+v", ref)
	}

	url := fmt.Sprintf("http://localhost:%d%s", e.port, path)

	// Try curl first.
	stdout, stderr, err := e.execFn(ctx, ref.Namespace, ref.Name, e.container,
		[]string{"curl", "-s", url})
	if err != nil {
		// Fallback: curl may not exist; try wget.
		notFound := strings.Contains(stderr, "not found") ||
			strings.Contains(err.Error(), "executable file not found")
		if notFound {
			stdout, stderr, err = e.execFn(ctx, ref.Namespace, ref.Name, e.container,
				[]string{"wget", "-qO-", url})
			if err != nil {
				return nil, fmt.Errorf("exec wget %s in %s/%s: %w (stderr: %s)",
					path, ref.Namespace, ref.Name, err, truncate(stderr, 200))
			}
		} else {
			return nil, fmt.Errorf("exec curl %s in %s/%s: %w (stderr: %s)",
				path, ref.Namespace, ref.Name, err, truncate(stderr, 200))
		}
	}

	out := strings.TrimSpace(stdout)
	if out == "" {
		return nil, fmt.Errorf("exec %s in %s/%s: empty response", path, ref.Namespace, ref.Name)
	}

	return io.NopCloser(strings.NewReader(out)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
