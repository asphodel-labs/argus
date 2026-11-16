package execx

import (
	"context"
	"testing"
)

// These tests call /bin/sh, not git, gh, or agtermctl.

func TestExecSuccess(t *testing.T) {
	stdout, stderr, code, err := Exec{}.Run(context.Background(), "/bin/sh", "-c", "echo out; echo err >&2")
	if err != nil || code != 0 {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	if stdout != "out\n" || stderr != "err\n" {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestExecNonZeroExit(t *testing.T) {
	_, _, code, err := Exec{}.Run(context.Background(), "/bin/sh", "-c", "exit 10")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if code != 10 {
		t.Errorf("code = %d, want 10", code)
	}
}

func TestExecStartFailure(t *testing.T) {
	_, _, code, err := Exec{}.Run(context.Background(), "/nonexistent/argus-test-binary")
	if err == nil {
		t.Fatal("err = nil, want a start error")
	}
	if code != -1 {
		t.Errorf("code = %d, want -1", code)
	}
}

func TestExecCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := Exec{}.Run(ctx, "/bin/sh", "-c", "sleep 5")
	if err == nil {
		t.Fatal("err = nil, want a context error")
	}
}
