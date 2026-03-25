// Package kcerr defines domain-specific error types for kc-hunter.
//
// These errors carry structured context that helps users debug failures
// in environments like bastion hosts, where generic "connection refused"
// messages are unhelpful. Each error type includes remediation hints.
package kcerr

import (
	"fmt"
	"strings"
)

// K8sConnectivityError indicates the Kubernetes API server is unreachable.
type K8sConnectivityError struct {
	Operation string // e.g. "create client", "list pods"
	Cause     error
}

func (e *K8sConnectivityError) Error() string {
	return fmt.Sprintf("kubernetes %s: %v", e.Operation, e.Cause)
}

func (e *K8sConnectivityError) Unwrap() error { return e.Cause }

func (e *K8sConnectivityError) Remediation() string {
	return "check --kubeconfig, $KUBECONFIG, or in-cluster service account; verify API server is reachable"
}

// PodDiscoveryError indicates no Connect pods were found.
type PodDiscoveryError struct {
	Namespace string
	Selector  string
	Cause     error
}

func (e *PodDiscoveryError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("discover pods in namespace %q with selector %q: %v", e.Namespace, e.Selector, e.Cause)
	}
	return fmt.Sprintf("no Kafka Connect pods found in namespace %q with selector %q", e.Namespace, e.Selector)
}

func (e *PodDiscoveryError) Unwrap() error { return e.Cause }

func (e *PodDiscoveryError) Remediation() string {
	return "verify --namespace and --selector match your Strimzi deployment; run 'kubectl get pods -l " + e.Selector + "'"
}

// ConnectRESTError indicates the Kafka Connect REST API is unreachable.
type ConnectRESTError struct {
	Transport string // "exec", "proxy", or "direct"
	PodName   string
	Namespace string
	Path      string
	Cause     error
}

func (e *ConnectRESTError) Error() string {
	return fmt.Sprintf("connect REST via %s (%s/%s) %s: %v",
		e.Transport, e.Namespace, e.PodName, e.Path, e.Cause)
}

func (e *ConnectRESTError) Unwrap() error { return e.Cause }

func (e *ConnectRESTError) Remediation() string {
	switch e.Transport {
	case "exec":
		return "verify exec permissions (RBAC: pods/exec); check that curl or wget is available in the container"
	case "proxy":
		return "verify proxy permissions (RBAC: pods/proxy); check --connect-port matches the Connect REST port"
	case "direct":
		return "verify pod IP is routable from this host; check --connect-url and --connect-port"
	default:
		return "check transport configuration and network connectivity"
	}
}

// JVMAttachError indicates a failure to run jcmd or attach to the JVM process.
type JVMAttachError struct {
	PodName   string
	Namespace string
	Command   string
	Cause     error
	Stderr    string
}

func (e *JVMAttachError) Error() string {
	msg := fmt.Sprintf("jcmd in %s/%s (%s): %v", e.Namespace, e.PodName, e.Command, e.Cause)
	if e.Stderr != "" {
		msg += " (stderr: " + truncate(e.Stderr, 200) + ")"
	}
	return msg
}

func (e *JVMAttachError) Unwrap() error { return e.Cause }

func (e *JVMAttachError) Remediation() string {
	if strings.Contains(e.Stderr, "not found") {
		return "jcmd not found in container; ensure JDK (not JRE) is installed in the Connect image"
	}
	if strings.Contains(e.Stderr, "Permission denied") || strings.Contains(e.Stderr, "Operation not permitted") {
		return "jcmd permission denied; the exec user may differ from the JVM process owner — check securityContext"
	}
	return "verify exec permissions and that PID 1 is the Java process; check container securityContext"
}

// MetricsUnavailableError indicates a metrics provider is not reachable.
type MetricsUnavailableError struct {
	Source string // "prometheus", "scrape", "metrics-server"
	URL    string
	Cause  error
}

func (e *MetricsUnavailableError) Error() string {
	if e.URL != "" {
		return fmt.Sprintf("%s metrics at %s: %v", e.Source, e.URL, e.Cause)
	}
	return fmt.Sprintf("%s metrics: %v", e.Source, e.Cause)
}

func (e *MetricsUnavailableError) Unwrap() error { return e.Cause }

func (e *MetricsUnavailableError) Remediation() string {
	switch e.Source {
	case "prometheus":
		return "check --prometheus-url; verify Prometheus is reachable and has Kafka Connect JMX metrics"
	case "scrape":
		return "check --metrics-port; verify JMX exporter sidecar is running and /metrics endpoint is accessible"
	case "metrics-server":
		return "install metrics-server in the cluster; scoring still works without it using Connect REST signals"
	default:
		return "check metrics provider configuration"
	}
}

// KubeconfigNotFoundError indicates no valid kubeconfig was found
// and the binary is not running inside a cluster.
type KubeconfigNotFoundError struct {
	Cause error
}

func (e *KubeconfigNotFoundError) Error() string {
	return fmt.Sprintf("kubeconfig not found: %v", e.Cause)
}

func (e *KubeconfigNotFoundError) Unwrap() error { return e.Cause }

func (e *KubeconfigNotFoundError) Remediation() string {
	return "não foi possível conectar ao cluster Kubernetes.\n" +
		"O kc-hunter não está rodando dentro de um Pod e nenhum arquivo kubeconfig válido foi encontrado.\n" +
		"Por favor, defina a variável $KUBECONFIG ou use a flag --kubeconfig."
}

// TimeoutError indicates an operation exceeded its time budget.
type TimeoutError struct {
	Operation string
	Duration  string
	Cause     error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s timed out after %s: %v", e.Operation, e.Duration, e.Cause)
}

func (e *TimeoutError) Unwrap() error { return e.Cause }

func (e *TimeoutError) Remediation() string {
	return "increase --timeout or check network latency to the cluster; for large clusters (800+ connectors) use --timeout 60s or higher"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
