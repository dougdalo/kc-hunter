package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dougdalo/kc-hunter/internal/app"
	"github.com/dougdalo/kc-hunter/internal/kcerr"
)

// remediator is implemented by domain errors that can suggest fixes.
type remediator interface {
	Remediation() string
}

func main() {
	if err := app.Execute(); err != nil {
		// SilentError: message already printed (e.g., Cobra usage errors).
		var silent *app.SilentError
		if errors.As(err, &silent) {
			os.Exit(silent.Code)
		}

		// ExitCodeError: semantic exit code (e.g., partial=2, doctor failures).
		var exitErr *app.ExitCodeError
		if errors.As(err, &exitErr) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", exitErr.Message)
			printRemediation(err)
			os.Exit(exitErr.Code)
		}

		// Domain errors: print with remediation hint.
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printRemediation(err)
		os.Exit(exitCodeForError(err))
	}
}

// printRemediation checks if the error (or any wrapped error) provides
// a remediation hint and prints it to stderr.
func printRemediation(err error) {
	var r remediator
	if errors.As(err, &r) {
		fmt.Fprintf(os.Stderr, "  Hint: %s\n", r.Remediation())
	}
}

// exitCodeForError maps domain errors to semantic exit codes.
func exitCodeForError(err error) int {
	var k8sErr *kcerr.K8sConnectivityError
	if errors.As(err, &k8sErr) {
		return app.ExitError
	}

	var timeoutErr *kcerr.TimeoutError
	if errors.As(err, &timeoutErr) {
		return app.ExitPartial
	}

	return app.ExitError
}
