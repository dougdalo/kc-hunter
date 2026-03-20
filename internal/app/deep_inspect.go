package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/dougdalo/kc-hunter/internal/jvm"
	"github.com/dougdalo/kc-hunter/internal/k8s"
	"github.com/spf13/cobra"
)

var deepInspectContainer string

var deepInspectCmd = &cobra.Command{
	Use:   "deep-inspect [pod-name]",
	Short: "Run JVM heap/thread inspection inside Connect pod(s)",
	Long: `Executes jcmd inside pods to collect heap histogram, thread count,
and GC information. Requires exec permissions on the target pods.

If a pod name is provided, inspects only that pod.
If no pod name is provided, discovers ALL Kafka Connect pods in the
namespace (-n) and inspects all of them, displaying a summary table.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDeepInspect,
}

func init() {
	deepInspectCmd.Flags().StringVarP(&deepInspectContainer, "container", "c", "", "container name (auto-detected if empty)")
}

// k8sExecAdapter bridges k8s.Client.ExecInPod to the jvm.Executor interface.
type k8sExecAdapter struct {
	client *k8s.Client
}

func (a *k8sExecAdapter) ExecInPod(ctx context.Context, ns, pod, container string, cmd []string) (string, string, error) {
	result, err := a.client.ExecInPod(ctx, ns, pod, container, cmd)
	if err != nil {
		stdout, stderr := "", ""
		if result != nil {
			stdout = result.Stdout
			stderr = result.Stderr
		}
		return stdout, stderr, err
	}
	return result.Stdout, result.Stderr, nil
}

func runDeepInspect(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), suspectTimeout())
	defer cancel()

	ns := ""
	if len(cfg.Namespaces) > 0 && cfg.Namespaces[0] != "" {
		ns = cfg.Namespaces[0]
	}

	k, err := newK8sClient()
	if err != nil {
		return err
	}

	adapter := &k8sExecAdapter{client: k}
	inspector := jvm.NewInspector(adapter)

	// Single pod mode: inspect one specific pod.
	if len(args) == 1 {
		podName := args[0]

		// Discover namespace if not specified.
		if ns == "" {
			pods, discoverErr := k.DiscoverConnectPods(ctx)
			if discoverErr == nil {
				for _, p := range pods {
					if p.Name == podName {
						ns = p.Namespace
						break
					}
				}
			}
			if ns == "" {
				return fmt.Errorf("namespace required: use -n or ensure pod %q is discoverable", podName)
			}
		}

		result, inspectErr := inspector.Inspect(ctx, ns, podName, deepInspectContainer)
		if inspectErr != nil {
			return fmt.Errorf("deep inspection failed: %w", inspectErr)
		}
		printSingleResult(result)
		return nil
	}

	// Multi-pod mode: discover all Connect pods in the namespace.
	if ns == "" {
		return fmt.Errorf("namespace required for multi-pod discovery: use -n <namespace>")
	}

	pods, err := k.DiscoverConnectPods(ctx)
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}

	// Filter to the requested namespace.
	var targets []string
	for _, p := range pods {
		if p.Namespace == ns {
			targets = append(targets, p.Name)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no Kafka Connect pods found in namespace %q", ns)
	}

	fmt.Fprintf(os.Stdout, "Discovered %d Connect pod(s) in namespace %q\n\n", len(targets), ns)

	// Inspect all pods with bounded concurrency.
	type podResult struct {
		result *jvm.Result
		err    error
	}
	results := make([]podResult, len(targets))
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup

	for i, podName := range targets {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			r, inspectErr := inspector.Inspect(ctx, ns, name, deepInspectContainer)
			results[idx] = podResult{result: r, err: inspectErr}
		}(i, podName)
	}
	wg.Wait()

	// Print summary table.
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "POD\tHEAP USED\tTHREADS\tTOP SUSPICIOUS CLASSES")
	fmt.Fprintln(w, "---\t---------\t-------\t----------------------")

	for i, pr := range results {
		pod := targets[i]
		if pr.err != nil {
			fmt.Fprintf(w, "%s\tERROR: %v\t-\t-\n", pod, pr.err)
			continue
		}
		r := pr.result

		heap := extractHeapUsed(r.HeapSummary)
		threads := "-"
		if r.ThreadCount > 0 {
			threads = fmt.Sprintf("%d", r.ThreadCount)
		}
		suspicious := "-"
		if len(r.SuspiciousClasses) > 0 {
			top := r.SuspiciousClasses
			if len(top) > 3 {
				top = top[:3]
			}
			suspicious = strings.Join(top, "; ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", pod, heap, threads, suspicious)
	}
	w.Flush()

	// Print detailed results for each pod.
	for _, pr := range results {
		if pr.err != nil || pr.result == nil {
			continue
		}
		fmt.Fprintf(os.Stdout, "\n")
		printSingleResult(pr.result)
	}

	return nil
}

// extractHeapUsed tries to pull a heap usage number from the heap summary.
// jcmd GC.heap_info output varies, so we do best-effort parsing.
func extractHeapUsed(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "used") {
			return strings.TrimSpace(line)
		}
	}
	if summary != "" {
		// Return first non-empty line as fallback.
		for _, line := range strings.Split(summary, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				return t
			}
		}
	}
	return "-"
}

func printSingleResult(result *jvm.Result) {
	w := os.Stdout

	fmt.Fprintf(w, "=== Deep JVM Inspection: %s ===\n\n", result.PodName)
	fmt.Fprintf(w, "Heap Summary:\n%s\n\n", result.HeapSummary)

	if len(result.TopClasses) > 0 {
		fmt.Fprintln(w, "Top Classes by Memory:")
		for i, c := range result.TopClasses {
			fmt.Fprintf(w, "  %d. %s  (%d instances, %s)\n", i+1, c.ClassName, c.Instances, fmtBytesSimple(c.Bytes))
		}
		fmt.Fprintln(w)
	}

	if result.ThreadCount > 0 {
		fmt.Fprintf(w, "Thread Count: %d\n\n", result.ThreadCount)
	}

	if result.GCInfo != "" {
		fmt.Fprintf(w, "GC Info:\n%s\n\n", result.GCInfo)
	}

	if len(result.SuspiciousClasses) > 0 {
		fmt.Fprintln(w, "Suspicious Connector Classes in Heap:")
		for _, s := range result.SuspiciousClasses {
			fmt.Fprintf(w, "  ! %s\n", s)
		}
	}
}

func fmtBytesSimple(b int64) string {
	const gb = 1024 * 1024 * 1024
	const mb = 1024 * 1024
	const kb = 1024
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1fGB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1fMB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1fKB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
