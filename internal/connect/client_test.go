package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// mockTransport is a test double that returns canned responses keyed by path.
type mockTransport struct {
	responses map[string][]byte
	errors    map[string]error
	calls     []string // records paths called
}

func (m *mockTransport) Get(_ context.Context, _ PodRef, path string) ([]byte, error) {
	m.calls = append(m.calls, path)
	if err, ok := m.errors[path]; ok {
		return nil, err
	}
	if data, ok := m.responses[path]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("unexpected path: %s", path)
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		responses: make(map[string][]byte),
		errors:    make(map[string]error),
	}
}

var testRef = PodRef{Name: "pod-0", Namespace: "kafka", URL: "http://10.0.0.1:8083"}

// --- NewClient ---

func TestNewClient_MinConcurrency(t *testing.T) {
	c := NewClient(newMockTransport(), 0)
	if c.concurrency != 10 {
		t.Errorf("concurrency=%d, want 10 (default)", c.concurrency)
	}

	c2 := NewClient(newMockTransport(), -5)
	if c2.concurrency != 10 {
		t.Errorf("negative concurrency should default to 10, got %d", c2.concurrency)
	}
}

// --- ListConnectors ---

func TestListConnectors(t *testing.T) {
	mt := newMockTransport()
	mt.responses["/connectors"] = []byte(`["conn-a","conn-b","conn-c"]`)

	c := NewClient(mt, 5)
	names, err := c.ListConnectors(context.Background(), testRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 connectors, got %d", len(names))
	}
	if names[0] != "conn-a" || names[1] != "conn-b" || names[2] != "conn-c" {
		t.Errorf("names=%v", names)
	}
}

func TestListConnectors_Error(t *testing.T) {
	mt := newMockTransport()
	mt.errors["/connectors"] = fmt.Errorf("connection refused")

	c := NewClient(mt, 5)
	_, err := c.ListConnectors(context.Background(), testRef)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListConnectors_InvalidJSON(t *testing.T) {
	mt := newMockTransport()
	mt.responses["/connectors"] = []byte(`not json`)

	c := NewClient(mt, 5)
	_, err := c.ListConnectors(context.Background(), testRef)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// --- GetConnectorStatus ---

func TestGetConnectorStatus(t *testing.T) {
	status := connectorStatus{
		Name: "my-conn",
		Type: "source",
	}
	status.Connector.State = "RUNNING"
	status.Connector.WorkerID = "10.0.0.1:8083"
	status.Tasks = []struct {
		ID       int    `json:"id"`
		State    string `json:"state"`
		WorkerID string `json:"worker_id"`
		Trace    string `json:"trace,omitempty"`
	}{
		{ID: 0, State: "RUNNING", WorkerID: "10.0.0.1:8083"},
		{ID: 1, State: "FAILED", WorkerID: "10.0.0.2:8083", Trace: "OOM"},
	}

	data, _ := json.Marshal(status)
	mt := newMockTransport()
	mt.responses["/connectors/my-conn/status"] = data

	c := NewClient(mt, 5)
	info, err := c.GetConnectorStatus(context.Background(), testRef, "my-conn")
	if err != nil {
		t.Fatal(err)
	}

	if info.Name != "my-conn" {
		t.Errorf("name=%q", info.Name)
	}
	if info.State != "RUNNING" {
		t.Errorf("state=%q", info.State)
	}
	if info.Type != "source" {
		t.Errorf("type=%q", info.Type)
	}
	if len(info.Tasks) != 2 {
		t.Fatalf("tasks=%d, want 2", len(info.Tasks))
	}
	if info.Tasks[1].State != "FAILED" {
		t.Errorf("task[1].state=%q", info.Tasks[1].State)
	}
	if info.Tasks[1].Trace != "OOM" {
		t.Errorf("task[1].trace=%q", info.Tasks[1].Trace)
	}
}

// --- GetConnectorConfig ---

func TestGetConnectorConfig(t *testing.T) {
	mt := newMockTransport()
	mt.responses["/connectors/my-conn/config"] = []byte(`{
		"connector.class": "io.debezium.connector.mysql.MySqlConnector",
		"tasks.max": "3"
	}`)

	c := NewClient(mt, 5)
	cfg, err := c.GetConnectorConfig(context.Background(), testRef, "my-conn")
	if err != nil {
		t.Fatal(err)
	}
	if cfg["connector.class"] != "io.debezium.connector.mysql.MySqlConnector" {
		t.Errorf("class=%q", cfg["connector.class"])
	}
}

// --- GetAllConnectors ---

func TestGetAllConnectors_Success(t *testing.T) {
	mt := newMockTransport()
	mt.responses["/connectors"] = []byte(`["conn-a","conn-b"]`)

	// Status responses.
	for _, name := range []string{"conn-a", "conn-b"} {
		status := connectorStatus{Name: name, Type: "sink"}
		status.Connector.State = "RUNNING"
		status.Connector.WorkerID = "10.0.0.1:8083"
		data, _ := json.Marshal(status)
		mt.responses[fmt.Sprintf("/connectors/%s/status", name)] = data
		mt.responses[fmt.Sprintf("/connectors/%s/config", name)] = []byte(`{"connector.class":"com.example.Sink"}`)
	}

	c := NewClient(mt, 5)
	result, err := c.GetAllConnectors(context.Background(), testRef)
	if err != nil {
		t.Fatal(err)
	}

	if result.Total != 2 {
		t.Errorf("total=%d, want 2", result.Total)
	}
	if result.Errors != 0 {
		t.Errorf("errors=%d, want 0", result.Errors)
	}
	if len(result.Connectors) != 2 {
		t.Fatalf("connectors=%d, want 2", len(result.Connectors))
	}
	if result.Connectors[0].ClassName != "com.example.Sink" && result.Connectors[1].ClassName != "com.example.Sink" {
		t.Error("className should be populated from config")
	}
}

func TestGetAllConnectors_PartialFailure(t *testing.T) {
	mt := newMockTransport()
	mt.responses["/connectors"] = []byte(`["good","bad"]`)

	// good connector succeeds.
	goodStatus := connectorStatus{Name: "good", Type: "source"}
	goodStatus.Connector.State = "RUNNING"
	data, _ := json.Marshal(goodStatus)
	mt.responses["/connectors/good/status"] = data
	mt.responses["/connectors/good/config"] = []byte(`{}`)

	// bad connector fails.
	mt.errors["/connectors/bad/status"] = fmt.Errorf("timeout")

	c := NewClient(mt, 5)
	result, err := c.GetAllConnectors(context.Background(), testRef)
	if err != nil {
		t.Fatal(err)
	}

	if result.Total != 2 {
		t.Errorf("total=%d, want 2", result.Total)
	}
	if result.Errors != 1 {
		t.Errorf("errors=%d, want 1", result.Errors)
	}
	if len(result.Connectors) != 1 {
		t.Errorf("connectors=%d, want 1", len(result.Connectors))
	}
	if len(result.Warnings) != 1 {
		t.Errorf("warnings=%d, want 1", len(result.Warnings))
	}
}

func TestGetAllConnectors_Empty(t *testing.T) {
	mt := newMockTransport()
	mt.responses["/connectors"] = []byte(`[]`)

	c := NewClient(mt, 5)
	result, err := c.GetAllConnectors(context.Background(), testRef)
	if err != nil {
		t.Fatal(err)
	}

	if result.Total != 0 {
		t.Errorf("total=%d, want 0", result.Total)
	}
}

// --- ProxyTransport validation ---

func TestProxyTransport_RequiresNameAndNamespace(t *testing.T) {
	pt := NewProxyTransport(func(_ context.Context, _, _ string, _ int, _ string) ([]byte, error) {
		return nil, nil
	}, 8083)

	_, err := pt.Get(context.Background(), PodRef{}, "/connectors")
	if err == nil {
		t.Error("expected error for empty PodRef")
	}
}

func TestProxyTransport_StripsLeadingSlash(t *testing.T) {
	var gotPath string
	pt := NewProxyTransport(func(_ context.Context, _, _ string, _ int, path string) ([]byte, error) {
		gotPath = path
		return []byte("ok"), nil
	}, 8083)

	pt.Get(context.Background(), PodRef{Name: "pod-0", Namespace: "kafka"}, "/connectors")
	if gotPath != "connectors" {
		t.Errorf("path=%q, want connectors (leading slash stripped)", gotPath)
	}
}

// --- ExecTransport ---

func TestExecTransport_RequiresNameAndNamespace(t *testing.T) {
	et := NewExecTransport(func(_ context.Context, _, _, _ string, _ []string) (string, string, error) {
		return "", "", nil
	}, 8083, "connect")

	_, err := et.Get(context.Background(), PodRef{}, "/connectors")
	if err == nil {
		t.Error("expected error for empty PodRef")
	}
}

func TestExecTransport_CurlSuccess(t *testing.T) {
	et := NewExecTransport(func(_ context.Context, _, _, _ string, cmd []string) (string, string, error) {
		if cmd[0] != "curl" {
			t.Errorf("expected curl, got %q", cmd[0])
		}
		return `["conn-a"]`, "", nil
	}, 8083, "connect")

	data, err := et.Get(context.Background(), PodRef{Name: "pod-0", Namespace: "kafka"}, "/connectors")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `["conn-a"]` {
		t.Errorf("data=%q", string(data))
	}
}

func TestExecTransport_FallbackToWget(t *testing.T) {
	callCount := 0
	et := NewExecTransport(func(_ context.Context, _, _, _ string, cmd []string) (string, string, error) {
		callCount++
		if cmd[0] == "curl" {
			return "", "curl: not found", fmt.Errorf("executable file not found")
		}
		if cmd[0] == "wget" {
			return `{"status":"ok"}`, "", nil
		}
		return "", "", fmt.Errorf("unexpected command")
	}, 8083, "connect")

	data, err := et.Get(context.Background(), PodRef{Name: "pod-0", Namespace: "kafka"}, "/test")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"status":"ok"}` {
		t.Errorf("data=%q", string(data))
	}
	if callCount != 2 {
		t.Errorf("callCount=%d, want 2 (curl + wget)", callCount)
	}
}

func TestExecTransport_EmptyResponse(t *testing.T) {
	et := NewExecTransport(func(_ context.Context, _, _, _ string, _ []string) (string, string, error) {
		return "", "", nil
	}, 8083, "connect")

	_, err := et.Get(context.Background(), PodRef{Name: "pod-0", Namespace: "kafka"}, "/test")
	if err == nil {
		t.Error("expected error for empty response")
	}
}

// --- truncate ---

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := truncate("a long string that should be truncated", 10); got != "a long str..." {
		t.Errorf("got %q", got)
	}
}
