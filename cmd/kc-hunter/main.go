package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dougdalo/kc-hunter/internal/app"
)

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
			os.Exit(exitErr.Code)
		}

		// Generic error: exit 1.
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(app.ExitError)
	}
}
