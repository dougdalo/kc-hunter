package app

import (
	"errors"
	"testing"
)

func TestExitCodeError_Message(t *testing.T) {
	err := &ExitCodeError{Code: ExitPartial, Message: "doctor found failures"}
	if err.Error() != "doctor found failures" {
		t.Errorf("Error()=%q, want %q", err.Error(), "doctor found failures")
	}
	if err.Code != ExitPartial {
		t.Errorf("Code=%d, want %d", err.Code, ExitPartial)
	}
}

func TestPartialError_UnwrapsToExitCodeError(t *testing.T) {
	err := PartialError("degraded data")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatal("PartialError should be unwrappable to ExitCodeError")
	}
	if exitErr.Code != ExitPartial {
		t.Errorf("Code=%d, want %d", exitErr.Code, ExitPartial)
	}
}

func TestSilentError_Message(t *testing.T) {
	err := &SilentError{Code: ExitPartial}
	if err.Error() == "" {
		t.Error("SilentError should have a non-empty message")
	}
}

func TestExitCodeConstants(t *testing.T) {
	if ExitOK != 0 {
		t.Errorf("ExitOK=%d, want 0", ExitOK)
	}
	if ExitError != 1 {
		t.Errorf("ExitError=%d, want 1", ExitError)
	}
	if ExitPartial != 2 {
		t.Errorf("ExitPartial=%d, want 2", ExitPartial)
	}
}
