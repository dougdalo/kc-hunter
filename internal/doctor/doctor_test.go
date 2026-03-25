package doctor

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStatus_String(t *testing.T) {
	tests := []struct {
		s    Status
		want string
	}{
		{StatusPass, "PASS"},
		{StatusWarn, "WARN"},
		{StatusFail, "FAIL"},
		{StatusSkip, "SKIP"},
		{Status(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Status(%d).String()=%q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestReport_HasFailures(t *testing.T) {
	t.Run("no failures", func(t *testing.T) {
		r := &Report{Results: []CheckResult{
			{Status: StatusPass},
			{Status: StatusWarn},
			{Status: StatusSkip},
		}}
		if r.HasFailures() {
			t.Error("should not have failures")
		}
	})

	t.Run("with failure", func(t *testing.T) {
		r := &Report{Results: []CheckResult{
			{Status: StatusPass},
			{Status: StatusFail},
		}}
		if !r.HasFailures() {
			t.Error("should have failures")
		}
	})

	t.Run("empty report", func(t *testing.T) {
		r := &Report{}
		if r.HasFailures() {
			t.Error("empty report should not have failures")
		}
	})
}

func TestReport_CountByStatus(t *testing.T) {
	r := &Report{Results: []CheckResult{
		{Status: StatusPass},
		{Status: StatusPass},
		{Status: StatusWarn},
		{Status: StatusFail},
		{Status: StatusSkip},
		{Status: StatusSkip},
		{Status: StatusSkip},
	}}

	if got := r.CountByStatus(StatusPass); got != 2 {
		t.Errorf("pass count=%d, want 2", got)
	}
	if got := r.CountByStatus(StatusWarn); got != 1 {
		t.Errorf("warn count=%d, want 1", got)
	}
	if got := r.CountByStatus(StatusFail); got != 1 {
		t.Errorf("fail count=%d, want 1", got)
	}
	if got := r.CountByStatus(StatusSkip); got != 3 {
		t.Errorf("skip count=%d, want 3", got)
	}
}

func TestReport_PrintTable(t *testing.T) {
	r := &Report{
		Timestamp: time.Date(2026, 3, 20, 14, 0, 0, 0, time.UTC),
		Transport: "exec",
		Results: []CheckResult{
			{Name: "cluster-access", Status: StatusPass, Message: "connected", Duration: 50 * time.Millisecond},
			{Name: "pod-discovery", Status: StatusFail, Message: "no pods", Remediation: "check labels", Duration: 100 * time.Millisecond},
			{Name: "metrics-provider", Status: StatusSkip, Message: "not configured"},
		},
	}

	var buf bytes.Buffer
	r.PrintTable(&buf, nil)
	out := buf.String()

	// Check structural elements.
	if !strings.Contains(out, "kc-hunter doctor") {
		t.Error("should contain header")
	}
	if !strings.Contains(out, "exec") {
		t.Error("should contain transport mode")
	}
	if !strings.Contains(out, "cluster-access") {
		t.Error("should contain check name")
	}
	if !strings.Contains(out, "PASS") {
		t.Error("should contain PASS status")
	}
	if !strings.Contains(out, "FAIL") {
		t.Error("should contain FAIL status")
	}
	if !strings.Contains(out, "Remediation") {
		t.Error("should contain remediation section")
	}
	if !strings.Contains(out, "check labels") {
		t.Error("should contain remediation text")
	}
	if !strings.Contains(out, "1 passed") {
		t.Error("should contain summary counts")
	}
}

func TestReport_PrintTable_WithColorize(t *testing.T) {
	r := &Report{
		Timestamp: time.Date(2026, 3, 20, 14, 0, 0, 0, time.UTC),
		Transport: "exec",
		Results: []CheckResult{
			{Name: "test", Status: StatusPass, Message: "ok"},
		},
	}

	var buf bytes.Buffer
	colorize := func(statusStr string, text string) string {
		return "[" + statusStr + "]" + text + "[/]"
	}
	r.PrintTable(&buf, colorize)
	out := buf.String()

	if !strings.Contains(out, "[PASS]PASS[/]") {
		t.Errorf("colorize function should be called; got: %s", out)
	}
}

func TestReport_PrintTable_NoRemediationForPassChecks(t *testing.T) {
	r := &Report{
		Timestamp: time.Date(2026, 3, 20, 14, 0, 0, 0, time.UTC),
		Transport: "exec",
		Results: []CheckResult{
			{Name: "test", Status: StatusPass, Message: "ok", Remediation: "should not appear"},
		},
	}

	var buf bytes.Buffer
	r.PrintTable(&buf, nil)

	if strings.Contains(buf.String(), "Remediation") {
		t.Error("remediation section should not appear when all checks pass")
	}
}

// --- Check function tests ---

func TestCheckClusterAccess(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		r := CheckClusterAccess(context.Background(), func(ctx context.Context) (string, error) {
			return "v1.28.3", nil
		})
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
		if !strings.Contains(r.Message, "v1.28.3") {
			t.Errorf("message should contain version, got %q", r.Message)
		}
	})

	t.Run("failure", func(t *testing.T) {
		r := CheckClusterAccess(context.Background(), func(ctx context.Context) (string, error) {
			return "", errors.New("connection refused")
		})
		if r.Status != StatusFail {
			t.Errorf("status=%v, want FAIL", r.Status)
		}
		if r.Remediation == "" {
			t.Error("should have remediation")
		}
	})

	t.Run("records duration", func(t *testing.T) {
		r := CheckClusterAccess(context.Background(), func(ctx context.Context) (string, error) {
			return "v1.28.3", nil
		})
		if r.Duration == 0 {
			t.Error("duration should be > 0")
		}
	})
}

func TestCheckNamespaces(t *testing.T) {
	listAll := func(ctx context.Context) ([]string, error) {
		return []string{"default", "kafka-prod", "kube-system"}, nil
	}

	t.Run("all namespaces mode", func(t *testing.T) {
		r := CheckNamespaces(context.Background(), []string{""}, listAll)
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
		if !strings.Contains(r.Message, "3 available") {
			t.Errorf("message=%q, want 3 available", r.Message)
		}
	})

	t.Run("specific namespace exists", func(t *testing.T) {
		r := CheckNamespaces(context.Background(), []string{"kafka-prod"}, listAll)
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
	})

	t.Run("namespace missing", func(t *testing.T) {
		r := CheckNamespaces(context.Background(), []string{"kafka-prod", "nonexistent"}, listAll)
		if r.Status != StatusFail {
			t.Errorf("status=%v, want FAIL", r.Status)
		}
		if !strings.Contains(r.Message, "nonexistent") {
			t.Errorf("message should name missing namespace, got %q", r.Message)
		}
	})

	t.Run("cannot list namespaces", func(t *testing.T) {
		r := CheckNamespaces(context.Background(), []string{"kafka-prod"}, func(ctx context.Context) ([]string, error) {
			return nil, errors.New("forbidden")
		})
		if r.Status != StatusWarn {
			t.Errorf("status=%v, want WARN", r.Status)
		}
	})

	t.Run("nil configured namespaces", func(t *testing.T) {
		r := CheckNamespaces(context.Background(), nil, listAll)
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
	})
}

func TestCheckPodDiscovery(t *testing.T) {
	t.Run("pods found and ready", func(t *testing.T) {
		r := CheckPodDiscovery(context.Background(), "strimzi.io/kind=KafkaConnect",
			func(ctx context.Context) ([]PodSummary, error) {
				return []PodSummary{
					{Name: "pod-0", Ready: true},
					{Name: "pod-1", Ready: true},
				}, nil
			})
		if r.Check.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Check.Status)
		}
		if r.PodCount != 2 {
			t.Errorf("podCount=%d, want 2", r.PodCount)
		}
	})

	t.Run("pods found but some not ready", func(t *testing.T) {
		r := CheckPodDiscovery(context.Background(), "selector",
			func(ctx context.Context) ([]PodSummary, error) {
				return []PodSummary{
					{Name: "pod-0", Ready: true},
					{Name: "pod-1", Ready: false},
				}, nil
			})
		if r.Check.Status != StatusWarn {
			t.Errorf("status=%v, want WARN", r.Check.Status)
		}
		if !strings.Contains(r.Check.Message, "1 not ready") {
			t.Errorf("message=%q, want mention of not-ready", r.Check.Message)
		}
	})

	t.Run("no pods found", func(t *testing.T) {
		r := CheckPodDiscovery(context.Background(), "selector",
			func(ctx context.Context) ([]PodSummary, error) {
				return nil, nil
			})
		if r.Check.Status != StatusFail {
			t.Errorf("status=%v, want FAIL", r.Check.Status)
		}
	})

	t.Run("discovery error", func(t *testing.T) {
		r := CheckPodDiscovery(context.Background(), "selector",
			func(ctx context.Context) ([]PodSummary, error) {
				return nil, errors.New("forbidden")
			})
		if r.Check.Status != StatusFail {
			t.Errorf("status=%v, want FAIL", r.Check.Status)
		}
	})

	t.Run("pods available for downstream", func(t *testing.T) {
		r := CheckPodDiscovery(context.Background(), "selector",
			func(ctx context.Context) ([]PodSummary, error) {
				return []PodSummary{
					{Name: "pod-0", Namespace: "kafka", IP: "10.0.0.1", Ready: true},
				}, nil
			})
		if len(r.Pods) != 1 || r.Pods[0].Name != "pod-0" {
			t.Error("discovered pods should be available in result")
		}
	})
}

func TestCheckMetricsServer(t *testing.T) {
	t.Run("available", func(t *testing.T) {
		r := CheckMetricsServer(context.Background(), func(ctx context.Context) error {
			return nil
		})
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		r := CheckMetricsServer(context.Background(), func(ctx context.Context) error {
			return errors.New("not found")
		})
		if r.Status != StatusWarn {
			t.Errorf("status=%v, want WARN (not FAIL — scoring works without it)", r.Status)
		}
	})
}

func TestCheckConnectREST(t *testing.T) {
	t.Run("reachable", func(t *testing.T) {
		r := CheckConnectREST(context.Background(), "exec", func(ctx context.Context) (int, error) {
			return 42, nil
		})
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
		if !strings.Contains(r.Message, "42 connectors") {
			t.Errorf("message=%q, want connector count", r.Message)
		}
	})

	t.Run("unreachable via exec", func(t *testing.T) {
		r := CheckConnectREST(context.Background(), "exec", func(ctx context.Context) (int, error) {
			return 0, errors.New("connection refused")
		})
		if r.Status != StatusFail {
			t.Errorf("status=%v, want FAIL", r.Status)
		}
		if !strings.Contains(r.Remediation, "exec") {
			t.Errorf("remediation should mention exec, got %q", r.Remediation)
		}
	})

	t.Run("unreachable via proxy", func(t *testing.T) {
		r := CheckConnectREST(context.Background(), "proxy", func(ctx context.Context) (int, error) {
			return 0, errors.New("403")
		})
		if !strings.Contains(r.Remediation, "proxy") {
			t.Errorf("remediation should mention proxy, got %q", r.Remediation)
		}
	})

	t.Run("unreachable via direct", func(t *testing.T) {
		r := CheckConnectREST(context.Background(), "direct", func(ctx context.Context) (int, error) {
			return 0, errors.New("timeout")
		})
		if !strings.Contains(r.Remediation, "connect-url") {
			t.Errorf("remediation should mention connect-url, got %q", r.Remediation)
		}
	})
}

func TestCheckMetricsProvider(t *testing.T) {
	t.Run("none source skips", func(t *testing.T) {
		r := CheckMetricsProvider(context.Background(), "none", func(ctx context.Context) bool {
			t.Fatal("should not be called")
			return false
		})
		if r.Status != StatusSkip {
			t.Errorf("status=%v, want SKIP", r.Status)
		}
	})

	t.Run("prometheus available", func(t *testing.T) {
		r := CheckMetricsProvider(context.Background(), "prometheus", func(ctx context.Context) bool {
			return true
		})
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
	})

	t.Run("prometheus unreachable", func(t *testing.T) {
		r := CheckMetricsProvider(context.Background(), "prometheus", func(ctx context.Context) bool {
			return false
		})
		if r.Status != StatusWarn {
			t.Errorf("status=%v, want WARN", r.Status)
		}
		if !strings.Contains(r.Remediation, "prometheus-url") {
			t.Errorf("remediation should mention prometheus-url, got %q", r.Remediation)
		}
	})

	t.Run("scrape unreachable", func(t *testing.T) {
		r := CheckMetricsProvider(context.Background(), "scrape", func(ctx context.Context) bool {
			return false
		})
		if !strings.Contains(r.Remediation, "metrics-port") {
			t.Errorf("remediation should mention metrics-port, got %q", r.Remediation)
		}
	})
}

func TestCheckExecPermissions(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		r := CheckExecPermissions(context.Background(), func(ctx context.Context) error {
			return nil
		})
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
	})

	t.Run("failure", func(t *testing.T) {
		r := CheckExecPermissions(context.Background(), func(ctx context.Context) error {
			return errors.New("forbidden")
		})
		if r.Status != StatusWarn {
			t.Errorf("status=%v, want WARN", r.Status)
		}
		if !strings.Contains(r.Remediation, "pods/exec") {
			t.Errorf("remediation should mention pods/exec, got %q", r.Remediation)
		}
	})
}

func TestCheckReplicationFactor(t *testing.T) {
	t.Run("all factors within broker count", func(t *testing.T) {
		r := CheckReplicationFactor(context.Background(), func(ctx context.Context) ([]ReplicationFactorInfo, error) {
			return []ReplicationFactorInfo{
				{
					KafkaName:     "my-kafka",
					KafkaReplicas: 3,
					ConnectName:   "my-connect",
					Mismatches:    nil,
				},
			}, nil
		})
		if r.Status != StatusPass {
			t.Errorf("status=%v, want PASS", r.Status)
		}
		if !strings.Contains(r.Message, "my-kafka/my-connect") {
			t.Errorf("message should list checked pairs, got %q", r.Message)
		}
	})

	t.Run("replication factor exceeds brokers", func(t *testing.T) {
		r := CheckReplicationFactor(context.Background(), func(ctx context.Context) ([]ReplicationFactorInfo, error) {
			return []ReplicationFactorInfo{
				{
					KafkaName:     "my-kafka",
					KafkaReplicas: 1,
					ConnectName:   "my-connect",
					Mismatches: []ReplicationMismatchDetail{
						{FactorName: "config.storage.replication.factor", Value: 3, Brokers: 1},
						{FactorName: "offset.storage.replication.factor", Value: 3, Brokers: 1},
					},
				},
			}, nil
		})
		if r.Status != StatusFail {
			t.Errorf("status=%v, want FAIL", r.Status)
		}
		if !strings.Contains(r.Message, "2 mismatch") {
			t.Errorf("message should mention mismatch count, got %q", r.Message)
		}
		if !strings.Contains(r.Remediation, "rebalance") {
			t.Errorf("remediation should mention rebalance loops, got %q", r.Remediation)
		}
	})

	t.Run("no CRs found", func(t *testing.T) {
		r := CheckReplicationFactor(context.Background(), func(ctx context.Context) ([]ReplicationFactorInfo, error) {
			return nil, nil
		})
		if r.Status != StatusSkip {
			t.Errorf("status=%v, want SKIP", r.Status)
		}
	})

	t.Run("CRD access error", func(t *testing.T) {
		r := CheckReplicationFactor(context.Background(), func(ctx context.Context) ([]ReplicationFactorInfo, error) {
			return nil, errors.New("forbidden")
		})
		if r.Status != StatusWarn {
			t.Errorf("status=%v, want WARN", r.Status)
		}
		if !strings.Contains(r.Remediation, "kafkas.kafka.strimzi.io") {
			t.Errorf("remediation should mention CRD resource, got %q", r.Remediation)
		}
	})

	t.Run("multiple connect instances mixed results", func(t *testing.T) {
		r := CheckReplicationFactor(context.Background(), func(ctx context.Context) ([]ReplicationFactorInfo, error) {
			return []ReplicationFactorInfo{
				{
					KafkaName:     "kafka-prod",
					KafkaReplicas: 3,
					ConnectName:   "connect-ok",
					Mismatches:    nil,
				},
				{
					KafkaName:     "kafka-prod",
					KafkaReplicas: 3,
					ConnectName:   "connect-bad",
					Mismatches: []ReplicationMismatchDetail{
						{FactorName: "status.storage.replication.factor", Value: 5, Brokers: 3},
					},
				},
			}, nil
		})
		if r.Status != StatusFail {
			t.Errorf("status=%v, want FAIL (one Connect has mismatch)", r.Status)
		}
		if !strings.Contains(r.Message, "connect-bad") {
			t.Errorf("message should name the bad connect, got %q", r.Message)
		}
	})
}
