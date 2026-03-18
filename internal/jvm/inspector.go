// Package jvm provides deep JVM inspection via kubectl exec.
// Optional advanced feature — the scoring engine works without it.
package jvm

import (
	"context"
	"fmt"
	"strings"
)

// Executor abstracts command execution inside a pod.
// The k8s.Client satisfies this via a thin adapter.
type Executor interface {
	ExecInPod(ctx context.Context, namespace, podName, container string, command []string) (stdout, stderr string, err error)
}

// Inspector runs JVM diagnostic commands inside Kafka Connect pods.
type Inspector struct {
	exec Executor
}

func NewInspector(exec Executor) *Inspector {
	return &Inspector{exec: exec}
}

// Result holds the output of a deep JVM inspection.
type Result struct {
	PodName           string                `json:"pod"`
	HeapSummary       string                `json:"heapSummary"`
	TopClasses        []ClassHistogramEntry `json:"topClasses,omitempty"`
	ThreadCount       int                   `json:"threadCount"`
	GCInfo            string                `json:"gcInfo,omitempty"`
	SuspiciousClasses []string              `json:"suspiciousClasses,omitempty"`
	RawOutput         map[string]string     `json:"-"`
}

// ClassHistogramEntry represents one row from jcmd GC.class_histogram.
type ClassHistogramEntry struct {
	Instances int64  `json:"instances"`
	Bytes     int64  `json:"bytes"`
	ClassName string `json:"class"`
}

// jcmdBuilder returns the command slice needed to run "jcmd <pid> <args...>"
// with the correct user identity. Built once per Inspect call by resolveJcmdRunner.
type jcmdBuilder func(args ...string) []string

// resolveJcmdRunner detects whether there is a UID mismatch between the exec
// user and the Java process owner. If so, it probes for su/setpriv and returns
// a builder that wraps jcmd accordingly. This runs once per pod inspection.
func (i *Inspector) resolveJcmdRunner(ctx context.Context, ns, pod, container, pid string) jcmdBuilder {
	plainJcmd := func(args ...string) []string {
		return append([]string{"jcmd", pid}, args...)
	}

	// Detect Java process UID via /proc/<pid>.
	javaUID, _, err := i.exec.ExecInPod(ctx, ns, pod, container,
		[]string{"stat", "-c", "%u", fmt.Sprintf("/proc/%s", pid)})
	if err != nil {
		return plainJcmd
	}
	javaUID = strings.TrimSpace(javaUID)

	// Detect current (exec) UID.
	currentUID, _, err := i.exec.ExecInPod(ctx, ns, pod, container,
		[]string{"id", "-u"})
	if err != nil {
		return plainJcmd
	}
	currentUID = strings.TrimSpace(currentUID)

	if javaUID == currentUID {
		return plainJcmd
	}

	// UID mismatch — resolve the Java user's name for su.
	javaUser := ""
	out, _, err := i.exec.ExecInPod(ctx, ns, pod, container,
		[]string{"stat", "-c", "%U", fmt.Sprintf("/proc/%s", pid)})
	if err == nil {
		javaUser = strings.TrimSpace(out)
	}

	// Probe: is su available?
	if javaUser != "" {
		_, _, err = i.exec.ExecInPod(ctx, ns, pod, container,
			[]string{"sh", "-c", "command -v su"})
		if err == nil {
			fmt.Printf("info: UID mismatch (java=%s, exec=%s), using su to run jcmd as %s\n",
				javaUID, currentUID, javaUser)
			return func(args ...string) []string {
				jcmdCmd := "jcmd " + pid + " " + strings.Join(args, " ")
				return []string{"su", "-s", "/bin/sh", javaUser, "-c", jcmdCmd}
			}
		}
	}

	// Probe: is setpriv available?
	_, _, err = i.exec.ExecInPod(ctx, ns, pod, container,
		[]string{"sh", "-c", "command -v setpriv"})
	if err == nil {
		fmt.Printf("info: UID mismatch (java=%s, exec=%s), using setpriv to run jcmd as UID %s\n",
			javaUID, currentUID, javaUID)
		return func(args ...string) []string {
			cmd := []string{
				"setpriv",
				"--reuid=" + javaUID,
				"--regid=" + javaUID,
				"--clear-groups",
				"jcmd", pid,
			}
			return append(cmd, args...)
		}
	}

	// Neither su nor setpriv available.
	fmt.Printf("warning: UID mismatch detected (java=%s, exec=%s) but neither su nor setpriv available — jcmd may fail with AttachNotSupportedException\n",
		javaUID, currentUID)
	return plainJcmd
}

// Inspect runs a suite of JVM diagnostics on the target pod.
// Steps: detect user, heap info, class histogram, thread count, GC info.
func (i *Inspector) Inspect(ctx context.Context, namespace, podName, container string) (*Result, error) {
	r := &Result{
		PodName:   podName,
		RawOutput: make(map[string]string),
	}

	// JVM PID is typically 1 inside a container.
	pid := "1"

	// Detect UID mismatch and build a jcmd runner that uses su/setpriv if needed.
	jcmd := i.resolveJcmdRunner(ctx, namespace, podName, container, pid)

	// Heap summary
	stdout, stderr, err := i.exec.ExecInPod(ctx, namespace, podName, container,
		jcmd("GC.heap_info"))
	if err != nil {
		r.HeapSummary = fmt.Sprintf("jcmd failed: %v (stderr: %s)", err, stderr)
	} else {
		r.HeapSummary = stdout
		r.RawOutput["heap_info"] = stdout
	}

	// Class histogram
	stdout, stderr, err = i.exec.ExecInPod(ctx, namespace, podName, container,
		jcmd("GC.class_histogram"))
	if err != nil {
		r.RawOutput["class_histogram_error"] = fmt.Sprintf("%v: %s", err, stderr)
	} else {
		r.TopClasses = parseClassHistogram(stdout, 20)
		r.RawOutput["class_histogram"] = stdout
	}

	// Thread count
	stdout, _, err = i.exec.ExecInPod(ctx, namespace, podName, container,
		jcmd("Thread.print"))
	if err == nil {
		r.ThreadCount = strings.Count(stdout, "\"")
		r.RawOutput["thread_dump"] = stdout
	}

	// GC / VM info
	stdout, _, err = i.exec.ExecInPod(ctx, namespace, podName, container,
		jcmd("VM.info"))
	if err == nil {
		r.GCInfo = extractGCInfo(stdout)
		r.RawOutput["vm_info"] = stdout
	}

	r.SuspiciousClasses = findSuspicious(r.TopClasses)
	return r, nil
}

func parseClassHistogram(output string, topN int) []ClassHistogramEntry {
	var entries []ClassHistogramEntry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "num") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "Total") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		var e ClassHistogramEntry
		fmt.Sscanf(fields[1], "%d", &e.Instances)
		fmt.Sscanf(fields[2], "%d", &e.Bytes)
		e.ClassName = fields[3]
		entries = append(entries, e)
		if len(entries) >= topN {
			break
		}
	}
	return entries
}

func extractGCInfo(vmInfo string) string {
	var lines []string
	for _, l := range strings.Split(vmInfo, "\n") {
		low := strings.ToLower(l)
		if strings.Contains(low, "gc") || strings.Contains(low, "heap") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) > 10 {
		lines = lines[:10]
	}
	return strings.Join(lines, "\n")
}

func findSuspicious(classes []ClassHistogramEntry) []string {
	patterns := []string{"connect", "connector", "kafka", "sink", "source",
		"jdbc", "ftp", "debezium", "camel", "elasticsearch"}

	var found []string
	for _, c := range classes {
		low := strings.ToLower(c.ClassName)
		for _, p := range patterns {
			if strings.Contains(low, p) {
				found = append(found, fmt.Sprintf("%s (%d instances, %s)", c.ClassName, c.Instances, fmtBytes(c.Bytes)))
				break
			}
		}
	}
	return found
}

func fmtBytes(b int64) string {
	const mb = 1024 * 1024
	const kb = 1024
	switch {
	case b >= mb:
		return fmt.Sprintf("%.1fMB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1fKB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
