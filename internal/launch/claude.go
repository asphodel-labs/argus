// Package launch starts a Claude session for a PR in a new agterm session.
package launch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/review"
)

// Workspace is the agterm workspace that holds the Claude sessions.
const Workspace = "reviews"

// ErrNoAgterm is the error of a launch outside agterm.
var ErrNoAgterm = errors.New("the Claude launch requires agterm")

// Claude starts `claude '/pr <url>'` in the PR worktree in a new agterm
// session.
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
	// Bin is the claude command. The new session has the PATH of the
	// agterm app, so main gives an absolute path. It is "claude" when
	// empty.
	Bin string
}

// ClaudeCommand returns the --command value: the claude binary and the
// "/pr <url>" prompt, each in single quotes. agterm splits the value into
// arguments and respects the quotes.
func (c *Claude) ClaudeCommand(pr model.PR) string {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	return execx.ShellQuote(bin) + " " + execx.ShellQuote("/pr "+pr.URL)
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
// Launch returns ErrNoAgterm outside agterm, and a *review.MissingClone
// error if the repository has no clone.
func (c *Claude) Launch(ctx context.Context, pr model.PR) error {
	if c.SessionID == "" {
		return ErrNoAgterm
	}
	wt, err := c.Worktrees.Setup(ctx, pr)
	if err != nil {
		return err
	}
	name, args := c.Command(pr, wt)
	_, stderr, code, err := c.Runner.Run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	if code != 0 {
		if msg := strings.TrimSpace(stderr); msg != "" {
			return fmt.Errorf("%s session new exited with code %d: %s", name, code, msg)
		}
		return fmt.Errorf("%s session new exited with code %d", name, code)
	}
	return nil
}
