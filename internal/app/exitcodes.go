// Package app defines the Cobra CLI commands for kc-hunter.
//
// Exit codes follow a predictable convention for scripting:
//
//	0  success — all data collected, no failures
//	1  error — command failed (bad config, K8s unreachable, etc.)
//	2  partial — command succeeded but with degraded data or doctor failures
//
// Commands return typed errors (ExitError, PartialError) that main.go maps
// to the corresponding exit code. Regular errors map to exit 1.
package app

import "fmt"

const (
	ExitOK      = 0
	ExitError   = 1
	ExitPartial = 2
)

// ExitCodeError is an error that carries a specific exit code.
// main.go inspects this to determine the process exit code.
type ExitCodeError struct {
	Code    int
	Message string
}

func (e *ExitCodeError) Error() string {
	return e.Message
}

// PartialError signals that the command completed but with degraded data.
// Scripts can check exit code 2 to decide whether to trust the output.
func PartialError(msg string) error {
	return &ExitCodeError{Code: ExitPartial, Message: msg}
}

// SilentError signals that the error message was already printed.
// main.go should exit with the code but not print the message again.
type SilentError struct {
	Code int
}

func (e *SilentError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}
