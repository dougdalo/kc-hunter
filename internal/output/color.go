package output

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// ANSI escape sequences.
const (
	ansiReset     = "\033[0m"
	ansiBold      = "\033[1m"
	ansiDim       = "\033[2m"
	ansiRed       = "\033[31m"
	ansiGreen     = "\033[32m"
	ansiYellow    = "\033[33m"
	ansiBoldRed   = "\033[1;31m"
	ansiBoldGreen = "\033[1;32m"
	ansiBoldYellow = "\033[1;33m"
	ansiBoldWhite = "\033[1;37m"
	ansiCyan      = "\033[36m"
	ansiBoldCyan  = "\033[1;36m"
)

// isTTY returns true if the writer appears to be a terminal.
func isTTY(w interface{}) bool {
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

// color wraps a string with ANSI color codes. Returns the string unchanged
// when color is disabled.
type colorizer struct {
	enabled bool
}

func (c colorizer) red(s string) string {
	if !c.enabled {
		return s
	}
	return ansiRed + s + ansiReset
}

func (c colorizer) green(s string) string {
	if !c.enabled {
		return s
	}
	return ansiGreen + s + ansiReset
}

func (c colorizer) yellow(s string) string {
	if !c.enabled {
		return s
	}
	return ansiYellow + s + ansiReset
}

func (c colorizer) boldRed(s string) string {
	if !c.enabled {
		return s
	}
	return ansiBoldRed + s + ansiReset
}

func (c colorizer) boldGreen(s string) string {
	if !c.enabled {
		return s
	}
	return ansiBoldGreen + s + ansiReset
}

func (c colorizer) boldYellow(s string) string {
	if !c.enabled {
		return s
	}
	return ansiBoldYellow + s + ansiReset
}

func (c colorizer) bold(s string) string {
	if !c.enabled {
		return s
	}
	return ansiBold + s + ansiReset
}

func (c colorizer) dim(s string) string {
	if !c.enabled {
		return s
	}
	return ansiDim + s + ansiReset
}

func (c colorizer) cyan(s string) string {
	if !c.enabled {
		return s
	}
	return ansiCyan + s + ansiReset
}

func (c colorizer) boldCyan(s string) string {
	if !c.enabled {
		return s
	}
	return ansiBoldCyan + s + ansiReset
}

// scoreColor returns the appropriate color function for a suspicion score.
func (c colorizer) scoreColor(score int) func(string) string {
	switch {
	case score >= 60:
		return c.boldRed
	case score >= 30:
		return c.boldYellow
	default:
		return c.boldGreen
	}
}

// memColor returns the appropriate color for a memory percentage.
func (c colorizer) memColor(pct float64) func(string) string {
	switch {
	case pct >= 90:
		return c.boldRed
	case pct >= 70:
		return c.yellow
	default:
		return c.green
	}
}

// stateColor returns the appropriate color for a connector/task state.
func (c colorizer) stateColor(state string) func(string) string {
	switch state {
	case "FAILED", "UNASSIGNED":
		return c.boldRed
	case "PAUSED":
		return c.yellow
	case "RUNNING":
		return c.green
	default:
		return func(s string) string { return s }
	}
}

// coloredScoreBar builds a score bar with color applied.
func (c colorizer) coloredScoreBar(score int) string {
	bar := scoreBar(score)
	return c.scoreColor(score)(fmt.Sprintf("%s %d/100", bar, score))
}
