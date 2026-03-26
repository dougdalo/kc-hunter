package app

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/AlecAivazis/survey/v2"
	"github.com/dougdalo/kc-hunter/internal/connect"
	"github.com/dougdalo/kc-hunter/internal/k8s"
	"github.com/dougdalo/kc-hunter/pkg/models"
	"github.com/spf13/cobra"
)

var connectorLogsCmd = &cobra.Command{
	Use:   "connector-logs [connector-name]",
	Short: "Tail logs for a specific connector across all workers",
	Long: `Streams logs from all Kafka Connect pods (workers) and filters lines
that match the selected connector name.

Because Kafka Connect distributes tasks across multiple worker pods,
this command opens concurrent log streams to all pods and merges them
in real time, prefixing each line with the pod name for easy debugging.

If no connector name is provided as an argument, an interactive prompt
lets you pick one from the running connectors.

Press Ctrl+C to stop tailing.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConnectorLogs,
}

var logTailLines int64

func init() {
	connectorLogsCmd.Flags().Int64Var(&logTailLines, "tail", 500,
		"number of historical log lines to fetch per pod before following")
}

func runConnectorLogs(cmd *cobra.Command, args []string) error {
	// Use a long-lived context — log tailing runs until Ctrl+C.
	ctx, cancel := signalContext(24 * 60 * 60 * 1e9) // 24h ceiling; signal cancels it
	defer cancel()

	k, err := newK8sClient()
	if err != nil {
		return err
	}

	// Step 1: Discover Connect pods.
	pods, err := k.DiscoverConnectPods(ctx)
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}
	if len(pods) == 0 {
		return fmt.Errorf("no Kafka Connect pods found")
	}

	// Step 2: Resolve connector name — from arg or interactive selection.
	connectorName, err := resolveConnectorName(ctx, k, pods, args)
	if err != nil {
		return err
	}

	return tailConnectorLogs(ctx, k, pods, connectorName)
}

// tailConnectorLogs is the core log-tailing logic, reusable from both the
// CLI subcommand and the interactive TUI. It fetches the connector status
// trace first (to surface past failures), then fans in live log streams
// from all pods, filters by connector name, and prints until the context
// is cancelled.
func tailConnectorLogs(
	ctx context.Context,
	k *k8s.Client,
	pods []models.PodInfo,
	connectorName string,
) error {
	// Step 1: Fetch and print any FAILED task traces from the REST API.
	// This is the most reliable source of truth for past failures that may
	// no longer appear in the last N log lines.
	printStatusTraces(ctx, k, pods, connectorName)

	info("Tailing logs for connector %q across %d pod(s)...", connectorName, len(pods))
	info("Press Ctrl+C to stop.\n")

	// Step 2: Fan-in log lines from all pods into a single channel.
	lines := make(chan logLine, 256)

	var wg sync.WaitGroup
	for _, pod := range pods {
		wg.Add(1)
		go func(p models.PodInfo) {
			defer wg.Done()
			streamPodLogs(ctx, k, p, lines)
		}(pod)
	}

	// Close the channel once all goroutines finish (context cancelled).
	go func() {
		wg.Wait()
		close(lines)
	}()

	return filterAndPrint(lines, connectorName)
}

// printStatusTraces queries the Kafka Connect REST API for the connector's
// current status and prints any FAILED task traces to the terminal. This
// catches exceptions that happened hours ago and are no longer in the pod
// log tail window.
func printStatusTraces(
	ctx context.Context,
	k *k8s.Client,
	pods []models.PodInfo,
	connectorName string,
) {
	cc := newConnectClient(k)

	// Try each pod until we get a successful status response.
	var status *models.ConnectorInfo
	for _, pod := range pods {
		ref := connect.PodRef{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			URL:       pod.ConnectURL,
		}
		s, err := cc.GetConnectorStatus(ctx, ref, connectorName)
		if err != nil {
			slog.Debug("status fetch failed, trying next pod",
				"pod", pod.Name, "error", err)
			continue
		}
		status = s
		break
	}

	if status == nil {
		warn("could not fetch connector status from REST API")
		return
	}

	// Print connector-level state if not RUNNING.
	if status.State != "RUNNING" {
		fmt.Fprintf(os.Stderr,
			"\033[1;33m[REST API] Connector %q state: %s\033[0m\n\n",
			connectorName, status.State)
	}

	// Print each failed task trace.
	hasFailures := false
	for _, task := range status.Tasks {
		if task.State != "FAILED" {
			continue
		}
		hasFailures = true
		trace := task.Trace
		if trace == "" {
			trace = "(no trace available)"
		}
		fmt.Fprintf(os.Stderr,
			"\033[1;31m[REST API] Task %d FAILED:\033[0m\n\033[31m%s\033[0m\n\n",
			task.TaskID, trace)
	}

	if hasFailures {
		fmt.Fprintf(os.Stderr,
			"\033[2m--- end of REST API traces, starting live log tail ---\033[0m\n\n")
	}
}

// logLine carries a single log line with its source pod identifier.
type logLine struct {
	pod  string
	text string
}

// resolveConnectorName determines the connector to tail — either from CLI args
// or by presenting an interactive selection from the running connectors.
func resolveConnectorName(
	ctx context.Context,
	k *k8s.Client,
	pods []models.PodInfo,
	args []string,
) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}

	// Fetch connector names from the Connect REST API.
	cc := newConnectClient(k)
	connectors := fetchAllConnectors(ctx, cc, pods)
	if len(connectors) == 0 {
		return "", fmt.Errorf("no connectors found in cluster")
	}

	names := make([]string, len(connectors))
	for i, c := range connectors {
		names[i] = c.Name
	}

	var selected string
	err := survey.AskOne(&survey.Select{
		Message:  "Select connector to tail:",
		Options:  names,
		PageSize: 20,
	}, &selected, survey.WithValidator(survey.Required))
	if err != nil {
		return "", handleInterrupt(err)
	}

	return selected, nil
}

// streamPodLogs opens a follow-mode log stream for a single pod and sends
// each line to the shared channel. It returns when the context is cancelled
// or the stream ends.
func streamPodLogs(
	ctx context.Context,
	k *k8s.Client,
	pod models.PodInfo,
	out chan<- logLine,
) {
	stream, err := k.StreamPodLogs(ctx, pod.Namespace, pod.Name, logTailLines)
	if err != nil {
		slog.Warn("failed to open log stream",
			"pod", pod.Name, "error", err)
		return
	}
	defer stream.Close()

	// Shorten pod name for the prefix: last 12 chars or full name if shorter.
	prefix := podPrefix(pod.Name)

	scanner := bufio.NewScanner(stream)
	// Kafka Connect log lines can be long (stack traces). 1 MB buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		case out <- logLine{pod: prefix, text: scanner.Text()}:
		}
	}

	if err := scanner.Err(); err != nil {
		// Context cancellation is expected (Ctrl+C); don't warn about it.
		if ctx.Err() == nil {
			slog.Warn("log scanner error",
				"pod", pod.Name, "error", err)
		}
	}
}

// filterAndPrint reads from the merged log channel and prints lines
// containing the connector name, prefixed with the pod identifier.
// It filters out noisy REST API access logs and continues printing
// multi-line Java stack traces that follow a matching line.
func filterAndPrint(lines <-chan logLine, connectorName string) error {
	// Track stack trace continuation state per pod. When a line matches
	// the connector name, we start printing. Subsequent lines that look
	// like stack trace continuations (\tat ..., Caused by:, ...) are
	// printed too, even if they don't contain the connector name.
	inTrace := make(map[string]bool)

	for line := range lines {
		text := line.text

		// Noise filter: skip REST API access logs polling status/config.
		if isAccessLogNoise(text) {
			continue
		}

		if strings.Contains(text, connectorName) {
			// Matched line — print it and enter trace-continuation mode.
			inTrace[line.pod] = true
			printLogLine(line.pod, text)
			continue
		}

		// Stack trace continuation: lines starting with whitespace
		// (e.g. "\tat org.apache...") or "Caused by:" belong to the
		// preceding exception. Keep printing until we see a normal log line.
		if inTrace[line.pod] && isStackTraceContinuation(text) {
			printLogLine(line.pod, text)
			continue
		}

		// Non-matching, non-continuation line — stop trace mode for this pod.
		inTrace[line.pod] = false
	}
	return nil
}

// isAccessLogNoise returns true for HTTP access log lines generated by
// Kafka Connect status/config polling — these flood the output and carry
// no diagnostic value.
func isAccessLogNoise(text string) bool {
	if !strings.Contains(text, "GET /connectors/") {
		return false
	}
	return strings.Contains(text, "/status") || strings.Contains(text, "/config")
}

// isStackTraceContinuation returns true for lines that are typically part
// of a Java stack trace following an exception line.
func isStackTraceContinuation(text string) bool {
	if len(text) == 0 {
		return false
	}
	// Java stack frames: "\tat org.apache.kafka..."
	if text[0] == '\t' || strings.HasPrefix(text, "    at ") {
		return true
	}
	// Chained exceptions.
	if strings.HasPrefix(text, "Caused by:") {
		return true
	}
	// "... N more" at the end of truncated traces.
	if strings.HasPrefix(text, "... ") && strings.HasSuffix(text, " more") {
		return true
	}
	return false
}

// printLogLine writes a single log line with a dimmed pod prefix.
func printLogLine(pod, text string) {
	fmt.Fprintf(os.Stdout, "\033[2m[%s]\033[0m %s\n", pod, text)
}

// podPrefix returns a short identifier from a pod name.
// For a typical Strimzi pod like "kafka-connect-connect-0", it returns
// the last segment after the final hyphen, or the last 12 characters.
func podPrefix(name string) string {
	if idx := strings.LastIndex(name, "-"); idx >= 0 && idx < len(name)-1 {
		suffix := name[idx+1:]
		// If suffix is just a number (StatefulSet ordinal), include more context.
		if len(suffix) <= 3 {
			// Use from the second-to-last hyphen.
			short := name
			if idx2 := strings.LastIndex(name[:idx], "-"); idx2 >= 0 {
				short = name[idx2+1:]
			}
			return short
		}
		return suffix
	}
	if len(name) > 12 {
		return name[len(name)-12:]
	}
	return name
}
