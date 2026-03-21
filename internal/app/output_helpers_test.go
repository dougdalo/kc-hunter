package app

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr to a buffer for the duration of fn.
func captureStderr(fn func()) string {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestInfo_PrintsToStderr(t *testing.T) {
	quietMode = false
	out := captureStderr(func() {
		info("found %d pods", 3)
	})
	if !strings.Contains(out, "found 3 pods") {
		t.Errorf("info should print to stderr, got %q", out)
	}
}

func TestInfo_SuppressedByQuiet(t *testing.T) {
	quietMode = true
	defer func() { quietMode = false }()

	out := captureStderr(func() {
		info("this should not appear")
	})
	if out != "" {
		t.Errorf("info with --quiet should produce no output, got %q", out)
	}
}

func TestWarn_AlwaysPrints(t *testing.T) {
	// Warn should print even with --quiet.
	quietMode = true
	defer func() { quietMode = false }()

	out := captureStderr(func() {
		warn("metrics unavailable for ns %q", "prod")
	})
	if !strings.Contains(out, "warning:") {
		t.Errorf("warn should prefix with 'warning:', got %q", out)
	}
	if !strings.Contains(out, "metrics unavailable") {
		t.Errorf("warn should print message, got %q", out)
	}
}

func TestWarn_IncludesPrefix(t *testing.T) {
	quietMode = false
	out := captureStderr(func() {
		warn("something went wrong")
	})
	if !strings.HasPrefix(out, "warning: ") {
		t.Errorf("warn should start with 'warning: ', got %q", out)
	}
}
