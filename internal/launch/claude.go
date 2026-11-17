// Package launch starts a Claude session for a PR in a new agterm session.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/review"
)

// Workspace is the agterm workspace that holds the Claude sessions.
const Workspace = "reviews"

// DefaultShell is the login shell when SHELL is empty.
const DefaultShell = "/bin/zsh"

// ErrNoAgterm is the error of a launch outside agterm.
var ErrNoAgterm = errors.New("the Claude launch requires agterm")

// Claude starts `claude '/pr <url>'` through the login shell of the user
// in the PR worktree in a new agterm session.
type Claude struct {
	// Worktrees prepares the PR worktree.
	Worktrees *review.Worktrees
	// Runner runs agtermctl.
	Runner execx.Runner
	// SessionID is AGTERM_SESSION_ID. Empty means that argus does not run
	// in agterm.
	SessionID string
	// Agtermctl is the agtermctl command. It is "agtermctl" when empty.
	Agtermctl string
	// Bin is the claude command. main gives an absolute path. It is
	// "claude" when empty.
	Bin string
	// Getenv reads an environment variable. It is os.Getenv when nil.
	Getenv func(key string) string
}

// Shell returns the login shell of the user: SHELL, or DefaultShell when
// SHELL is empty.
func (c *Claude) Shell() string {
	getenv := c.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if sh := getenv("SHELL"); sh != "" {
		return sh
	}
	return DefaultShell
}

// InnerCommand returns the command that the login shell runs: the claude
// binary and the "/pr <url>" prompt, each in single quotes. The command
// has only single-quoted words, so fish and POSIX sh read it the same way.
func (c *Claude) InnerCommand(pr model.PR) string {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	return execx.ShellQuote(bin) + " " + execx.ShellQuote("/pr "+pr.URL)
}

// ClaudeCommand returns the --command value: `<shell> -lc '<inner>'`.
// agterm splits the value into arguments with no shell and respects the
// quotes. The new session has the PATH of the agterm app, so the login
// shell gives Claude the full PATH of the user (gh, git, Homebrew).
func (c *Claude) ClaudeCommand(pr model.PR) string {
	return execx.ShellQuote(c.Shell()) + " -lc " + execx.ShellQuote(c.InnerCommand(pr))
}

// Command returns the agtermctl command and its arguments that make the
// Claude session for the PR in the worktree.
func (c *Claude) Command(pr model.PR, wt review.Worktree) (name string, args []string) {
	name = c.Agtermctl
	if name == "" {
		name = "agtermctl"
	}
	return name, []string{"session", "new",
		"--workspace-name", Workspace, "--create-workspace",
		"--cwd", wt.Dir,
		"--name", pr.Repo.Name + "#" + strconv.Itoa(pr.Number),
		"--command", c.ClaudeCommand(pr)}
}

// Launch sets up the PR worktree and starts Claude in a new agterm
// session. pr must be a PR detail with the head SHA and the base branch.
// Launch returns the worktree, so that the caller can show Worktree.Dirty
// and Worktree.ExcludeErr. Launch returns ErrNoAgterm outside agterm, and a
// *review.MissingClone error if the repository has no clone.
func (c *Claude) Launch(ctx context.Context, pr model.PR) (review.Worktree, error) {
	if c.SessionID == "" {
		return review.Worktree{}, ErrNoAgterm
	}
	wt, err := c.Worktrees.Setup(ctx, pr)
	if err != nil {
		return review.Worktree{}, err
	}
	name, args := c.Command(pr, wt)
	_, stderr, code, err := c.Runner.Run(ctx, name, args...)
	if err != nil {
		return wt, fmt.Errorf("run %s: %w", name, err)
	}
	if code != 0 {
		if msg := strings.TrimSpace(stderr); msg != "" {
			return wt, fmt.Errorf("%s session new exited with code %d: %s", name, code, msg)
		}
		return wt, fmt.Errorf("%s session new exited with code %d", name, code)
	}
	return wt, nil
}
