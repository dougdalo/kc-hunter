// Package doctor validates the operational prerequisites for kc-hunter.
// Each check probes a specific dependency and reports pass/warn/fail with
// actionable remediation messages.
package doctor

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Status represents the outcome of a single check.
type Status int

const (
	StatusPass Status = iota
	StatusWarn
	StatusFail
	StatusSkip
)

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "PASS"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "FAIL"
	case StatusSkip:
		return "SKIP"
	default:
		return "UNKNOWN"
	}
}

// CheckResult is the outcome of a single diagnostic check.
type CheckResult struct {
	Name        string
	Status      Status
	Message     string
	Remediation string
	Duration    time.Duration
}

// Report holds the results of all checks.
type Report struct {
	Results   []CheckResult
	Transport string
	Timestamp time.Time
}

// HasFailures returns true if any check failed.
func (r *Report) HasFailures() bool {
	for _, c := range r.Results {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// CountByStatus returns the number of checks with the given status.
func (r *Report) CountByStatus(s Status) int {
	n := 0
	for _, c := range r.Results {
		if c.Status == s {
			n++
		}
	}
	return n
}

// PrintTable writes the report as a human-readable table.
// colorize is optional — when non-nil it receives the status string (e.g.
// "PASS") and the display text to wrap with ANSI codes.
func (r *Report) PrintTable(w io.Writer, colorize func(statusStr string, text string) string) {
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "kc-hunter doctor — %s\n", r.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "Transport mode: %s\n\n", r.Transport)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tSTATUS\tDURATION\tMESSAGE")
	fmt.Fprintln(tw, "-----\t------\t--------\t-------")

	for _, c := range r.Results {
		status := c.Status.String()
		if colorize != nil {
			status = colorize(status, status)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			c.Name, status, c.Duration.Round(time.Millisecond), c.Message)
	}
	tw.Flush()

	// Print remediations for non-pass checks.
	var remediations []CheckResult
	for _, c := range r.Results {
		if c.Remediation != "" && c.Status != StatusPass {
			remediations = append(remediations, c)
		}
	}
	if len(remediations) > 0 {
		fmt.Fprintf(w, "\nRemediation:\n")
		for _, c := range remediations {
			fmt.Fprintf(w, "  [%s] %s\n", c.Name, c.Remediation)
		}
	}

	// Summary line.
	fmt.Fprintf(w, "\n%d passed, %d warnings, %d failed, %d skipped\n",
		r.CountByStatus(StatusPass),
		r.CountByStatus(StatusWarn),
		r.CountByStatus(StatusFail),
		r.CountByStatus(StatusSkip),
	)
}

// --- Check functions ---
//
// Each Check* function returns a CheckResult. The runner (CLI command) calls
// them in sequence, passing the appropriate dependencies.

// CheckClusterAccess verifies that the Kubernetes API is reachable and the
// client can perform a basic server version request.
func CheckClusterAccess(ctx context.Context, getVersion func(ctx context.Context) (string, error)) CheckResult {
	start := time.Now()
	r := CheckResult{Name: "cluster-access"}

	version, err := getVersion(ctx)
	r.Duration = time.Since(start)
	if err != nil {
		r.Status = StatusFail
		r.Message = fmt.Sprintf("cannot reach API server: %v", err)
		r.Remediation = "check --kubeconfig, $KUBECONFIG, or in-cluster config; verify network access to the API server"
		return r
	}

	r.Status = StatusPass
	r.Message = fmt.Sprintf("connected to Kubernetes %s", version)
	return r
}

// CheckNamespaces verifies that the configured namespaces exist (or that
// the client can list namespaces when no specific namespace is set).
func CheckNamespaces(
	ctx context.Context,
	configuredNS []string,
	listNamespaces func(ctx context.Context) ([]string, error),
) CheckResult {
	start := time.Now()
	r := CheckResult{Name: "namespaces"}

	allNS, err := listNamespaces(ctx)
	r.Duration = time.Since(start)
	if err != nil {
		r.Status = StatusWarn
		r.Message = fmt.Sprintf("cannot list namespaces: %v", err)
		r.Remediation = "grant list namespaces permission, or use -n to specify namespaces explicitly"
		return r
	}

	// No specific namespace configured — scanning all.
	effective := configuredNS
	if len(effective) == 0 || (len(effective) == 1 && effective[0] == "") {
		r.Status = StatusPass
		r.Message = fmt.Sprintf("scanning all namespaces (%d available)", len(allNS))
		return r
	}

	// Check that each configured namespace exists.
	nsSet := make(map[string]bool, len(allNS))
	for _, ns := range allNS {
		nsSet[ns] = true
	}

	var missing []string
	for _, ns := range effective {
		if ns != "" && !nsSet[ns] {
			missing = append(missing, ns)
		}
	}

	if len(missing) > 0 {
		r.Status = StatusFail
		r.Message = fmt.Sprintf("namespace(s) not found: %s", strings.Join(missing, ", "))
		r.Remediation = "check spelling or create the namespace; verify RBAC permissions"
		return r
	}

	r.Status = StatusPass
	r.Message = fmt.Sprintf("namespace(s) exist: %s", strings.Join(effective, ", "))
	return r
}

// PodDiscoveryResult carries both the check result and the discovered pods
// for downstream checks to use.
type PodDiscoveryResult struct {
	Check    CheckResult
	PodCount int
	Pods     []PodSummary
}

// PodSummary is a minimal pod representation for the doctor report.
type PodSummary struct {
	Name      string
	Namespace string
	IP        string
	Ready     bool
}

// CheckPodDiscovery verifies that Kafka Connect pods can be found.
func CheckPodDiscovery(
	ctx context.Context,
	selector string,
	discover func(ctx context.Context) ([]PodSummary, error),
) PodDiscoveryResult {
	start := time.Now()
	r := PodDiscoveryResult{Check: CheckResult{Name: "pod-discovery"}}

	pods, err := discover(ctx)
	r.Check.Duration = time.Since(start)
	if err != nil {
		r.Check.Status = StatusFail
		r.Check.Message = fmt.Sprintf("pod discovery failed: %v", err)
		r.Check.Remediation = fmt.Sprintf("verify label selector %q matches your Connect pods; check RBAC for pod list", selector)
		return r
	}

	r.PodCount = len(pods)
	r.Pods = pods

	if len(pods) == 0 {
		r.Check.Status = StatusFail
		r.Check.Message = fmt.Sprintf("no pods found with selector %q", selector)
		r.Check.Remediation = "verify the label selector (-l) and namespace (-n); ensure Connect pods are running"
		return r
	}

	notReady := 0
	for _, p := range pods {
		if !p.Ready {
			notReady++
		}
	}

	if notReady > 0 {
		r.Check.Status = StatusWarn
		r.Check.Message = fmt.Sprintf("found %d pod(s), but %d not ready", len(pods), notReady)
		r.Check.Remediation = "check pod events and logs for not-ready pods"
	} else {
		r.Check.Status = StatusPass
		r.Check.Message = fmt.Sprintf("found %d pod(s), all ready", len(pods))
	}

	return r
}

// CheckMetricsServer verifies that the Kubernetes metrics-server is available.
func CheckMetricsServer(
	ctx context.Context,
	probe func(ctx context.Context) error,
) CheckResult {
	start := time.Now()
	r := CheckResult{Name: "metrics-server"}

	err := probe(ctx)
	r.Duration = time.Since(start)
	if err != nil {
		r.Status = StatusWarn
		r.Message = fmt.Sprintf("metrics-server not available: %v", err)
		r.Remediation = "install metrics-server for memory% data; scoring still works without it using Connect REST signals"
		return r
	}

	r.Status = StatusPass
	r.Message = "metrics-server is available"
	return r
}

// CheckConnectREST verifies that the Kafka Connect REST API is reachable
// through the configured transport.
func CheckConnectREST(
	ctx context.Context,
	transportName string,
	probe func(ctx context.Context) (int, error),
) CheckResult {
	start := time.Now()
	r := CheckResult{Name: "connect-rest"}

	connectorCount, err := probe(ctx)
	r.Duration = time.Since(start)
	if err != nil {
		r.Status = StatusFail
		r.Message = fmt.Sprintf("Connect REST unreachable via %s: %v", transportName, err)
		switch transportName {
		case "exec":
			r.Remediation = "verify exec permissions on pods (RBAC: pods/exec); ensure curl or wget is available in the container"
		case "proxy":
			r.Remediation = "verify API server proxy access (RBAC: pods/proxy); check --connect-port"
		case "direct":
			r.Remediation = "verify --connect-url is correct and pod IPs are routable from this host"
		}
		return r
	}

	r.Status = StatusPass
	r.Message = fmt.Sprintf("Connect REST reachable via %s (%d connectors)", transportName, connectorCount)
	return r
}

// CheckMetricsProvider verifies that the configured metrics provider is reachable.
func CheckMetricsProvider(
	ctx context.Context,
	source string,
	available func(ctx context.Context) bool,
) CheckResult {
	start := time.Now()
	r := CheckResult{Name: "metrics-provider"}

	if source == "none" {
		r.Duration = time.Since(start)
		r.Status = StatusSkip
		r.Message = "metrics source is 'none'; scoring uses K8s + Connect REST signals only"
		return r
	}

	ok := available(ctx)
	r.Duration = time.Since(start)
	if !ok {
		r.Status = StatusWarn
		r.Message = fmt.Sprintf("%s metrics provider is not reachable", source)
		switch source {
		case "prometheus":
			r.Remediation = "verify --prometheus-url is correct and Prometheus is reachable"
		case "scrape":
			r.Remediation = "verify --metrics-port is correct and JMX exporter is running on Connect pods"
		}
		return r
	}

	r.Status = StatusPass
	r.Message = fmt.Sprintf("%s metrics provider is reachable", source)
	return r
}

// CheckExecPermissions verifies that the client can exec into a pod.
// Only relevant for exec transport mode and deep-inspect.
func CheckExecPermissions(
	ctx context.Context,
	tryExec func(ctx context.Context) error,
) CheckResult {
	start := time.Now()
	r := CheckResult{Name: "exec-permissions"}

	err := tryExec(ctx)
	r.Duration = time.Since(start)
	if err != nil {
		r.Status = StatusWarn
		r.Message = fmt.Sprintf("exec into pod failed: %v", err)
		r.Remediation = "grant pods/exec RBAC permission; needed for default transport mode and deep-inspect"
		return r
	}

	r.Status = StatusPass
	r.Message = "exec into pods works"
	return r
}
