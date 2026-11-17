// Package execx wraps external commands behind a small interface,
// so that tests can use a fake runner and never call a real binary.
package execx

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Runner runs an external command.
//
// Run returns err only when the command cannot start or the context stops it.
// A command that exits with a non-zero status returns its exit code and a nil err.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// Exec is the real Runner. It uses os/exec.
type Exec struct{}

// Run implements Runner.
func (Exec) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), -1, err
}

// ShellQuote returns s in single quotes for sh. Each single quote in s
// ends the quoted part, adds an escaped quote, and starts a new part.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
