// Package commands runs the custom commands of the config in the local
// clone of the PR repository.
package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/asphodel-labs/argus/internal/config"
	"github.com/asphodel-labs/argus/internal/execx"
	"github.com/asphodel-labs/argus/internal/model"
)

// ErrNoAgterm is the error of an overlay command outside agterm.
var ErrNoAgterm = errors.New("overlay commands require agterm")

// CloneFinder finds the local clone of a repository. *review.Worktrees
// implements it. FindClone returns a *review.MissingClone error if the
// repository has no clone.
type CloneFinder interface {
	FindClone(repo model.Repo) (string, error)
}

// Runner runs custom commands.
type Runner struct {
	// Runner runs sh and agtermctl.
	Runner execx.Runner
	// Clones finds the clone directory of the PR repository.
	Clones CloneFinder
	// Agtermctl is the agtermctl command. It is "agtermctl" when empty.
	Agtermctl string
	// SessionID is AGTERM_SESSION_ID. Empty means that argus does not run
	// in agterm.
	SessionID string
}

// Run renders cmd with the fields of pr and runs it in the clone of the PR
// repository. If a worktree of the clone has the PR head branch checked
// out, Run runs the command in that worktree: git refuses to check out a
// branch that another worktree has checked out. pr must be a PR detail, because the summary has no head
// branch. Run returns the status text "<name>: done" on success. It
// returns ErrNoAgterm for an overlay command outside agterm, and the
// *review.MissingClone error of Clones unchanged.
func (r *Runner) Run(ctx context.Context, cmd config.Command, pr model.PR) (string, error) {
	if cmd.Mode == config.ModeOverlay && r.SessionID == "" {
		return "", ErrNoAgterm
	}
	rendered, err := cmd.Render(pr)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", cmd.Name, err)
	}
	dir, err := r.Clones.FindClone(pr.Repo)
	if err != nil {
		return "", err
	}
	if dir, err = r.branchDir(ctx, dir, pr.HeadRef); err != nil {
		return "", err
	}
	script := "cd " + execx.ShellQuote(dir) + " && " + rendered

	var name string
	var args []string
	if cmd.Mode == config.ModeOverlay {
		// agterm runs the overlay command with the PATH of the agterm app.
		// The PATH of argus makes gh and git resolve as in the user shell.
		name = r.Agtermctl
		if name == "" {
			name = "agtermctl"
		}
		// The overlay closes when its command exits. The script waits for
		// enter, so that the user can read the output. A newline ends the
		// template, so a comment at its end does not hide the wait.
		script = "PATH=" + execx.ShellQuote(os.Getenv("PATH")) + "; export PATH; " + script +
			"\nrc=$?\nprintf '\\n%s: exit %d. Press enter to close.' " + execx.ShellQuote(cmd.Name) +
			` "$rc"` + "\nread _\nexit \"$rc\""
		args = []string{"session", "overlay", "open", "--block",
			"--target", r.SessionID, "--cwd", dir, script}
	} else {
		name = "sh"
		args = []string{"-c", script}
	}

	stdout, stderr, code, err := r.Runner.Run(ctx, name, args...)
	if err != nil {
		return "", fmt.Errorf("run %s: %w", name, err)
	}
	if code != 0 {
		return "", errors.New(failText(stdout, stderr, code))
	}
	return cmd.Name + ": done", nil
}

// branchDir returns the worktree of the clone that has the branch checked
// out, else the clone.
func (r *Runner) branchDir(ctx context.Context, clone, branch string) (string, error) {
	stdout, stderr, code, err := r.Runner.Run(ctx, "git", "-C", clone, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git worktree list: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("git worktree list: %s", failText(stdout, stderr, code))
	}
	var dir string
	for line := range strings.SplitSeq(stdout, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			dir = p
		} else if line == "branch refs/heads/"+branch {
			return dir, nil
		}
	}
	return clone, nil
}

// failText returns the last line of stderr, else the last line of stdout,
// else "exit code N".
func failText(stdout, stderr string, code int) string {
	if line := lastLine(stderr); line != "" {
		return line
	}
	if line := lastLine(stdout); line != "" {
		return line
	}
	return fmt.Sprintf("exit code %d", code)
}

// lastLine returns the last line of s that is not blank, with no spaces at
// the ends.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
