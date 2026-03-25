package kcerr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestK8sConnectivityError(t *testing.T) {
	cause := fmt.Errorf("connection refused")
	err := &K8sConnectivityError{Operation: "create client", Cause: cause}

	if !strings.Contains(err.Error(), "kubernetes create client") {
		t.Errorf("error message should contain operation: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error message should contain cause: %s", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("should unwrap to cause")
	}
	if err.Remediation() == "" {
		t.Error("should provide remediation")
	}
}

func TestPodDiscoveryError(t *testing.T) {
	t.Run("with cause", func(t *testing.T) {
		cause := fmt.Errorf("forbidden")
		err := &PodDiscoveryError{Namespace: "kafka", Selector: "app=connect", Cause: cause}
		if !strings.Contains(err.Error(), "kafka") {
			t.Errorf("should contain namespace: %s", err.Error())
		}
		if !errors.Is(err, cause) {
			t.Error("should unwrap to cause")
		}
	})

	t.Run("without cause", func(t *testing.T) {
		err := &PodDiscoveryError{Namespace: "kafka", Selector: "app=connect"}
		if !strings.Contains(err.Error(), "no Kafka Connect pods found") {
			t.Errorf("should indicate no pods found: %s", err.Error())
		}
	})
}

func TestConnectRESTError(t *testing.T) {
	tests := []struct {
		transport string
		wantHint  string
	}{
		{"exec", "exec permissions"},
		{"proxy", "proxy permissions"},
		{"direct", "pod IP is routable"},
	}
	for _, tt := range tests {
		err := &ConnectRESTError{
			Transport: tt.transport,
			PodName:   "pod-1",
			Namespace: "kafka",
			Path:      "/connectors",
			Cause:     fmt.Errorf("timeout"),
		}
		if !strings.Contains(err.Remediation(), tt.wantHint) {
			t.Errorf("transport=%s: remediation should mention %q, got: %s",
				tt.transport, tt.wantHint, err.Remediation())
		}
	}
}

func TestJVMAttachError(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		err := &JVMAttachError{
			PodName:   "pod-1",
			Namespace: "kafka",
			Command:   "jcmd 1 GC.heap_info",
			Cause:     fmt.Errorf("exit code 1"),
			Stderr:    "jcmd: not found",
		}
		if !strings.Contains(err.Remediation(), "JDK") {
			t.Errorf("should suggest JDK: %s", err.Remediation())
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		err := &JVMAttachError{
			PodName:   "pod-1",
			Namespace: "kafka",
			Command:   "jcmd 1 GC.heap_info",
			Cause:     fmt.Errorf("exit code 1"),
			Stderr:    "Permission denied",
		}
		if !strings.Contains(err.Remediation(), "securityContext") {
			t.Errorf("should mention securityContext: %s", err.Remediation())
		}
	})
}

func TestMetricsUnavailableError(t *testing.T) {
	err := &MetricsUnavailableError{
		Source: "prometheus",
		URL:    "http://prometheus:9090",
		Cause:  fmt.Errorf("connection refused"),
	}
	if !strings.Contains(err.Error(), "prometheus") {
		t.Errorf("should contain source: %s", err.Error())
	}
	if !strings.Contains(err.Remediation(), "--prometheus-url") {
		t.Errorf("should mention flag: %s", err.Remediation())
	}
}

func TestTimeoutError(t *testing.T) {
	err := &TimeoutError{
		Operation: "connect REST",
		Duration:  "30s",
		Cause:     fmt.Errorf("context deadline exceeded"),
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("should say timed out: %s", err.Error())
	}
	if !strings.Contains(err.Remediation(), "--timeout") {
		t.Errorf("should suggest --timeout: %s", err.Remediation())
	}
}

func TestErrorsAsChain(t *testing.T) {
	inner := fmt.Errorf("dial tcp: connection refused")
	k8sErr := &K8sConnectivityError{Operation: "list pods", Cause: inner}
	wrapped := fmt.Errorf("discover pods: %w", k8sErr)

	var target *K8sConnectivityError
	if !errors.As(wrapped, &target) {
		t.Error("errors.As should find K8sConnectivityError through wrapping")
	}
	if target.Operation != "list pods" {
		t.Errorf("wrong operation: %s", target.Operation)
	}
}
