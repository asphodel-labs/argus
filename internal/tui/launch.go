package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/tdragon/argus/internal/launch"
	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/review"
)

// Launcher sets up the PR worktree and starts a Claude session with
// `/pr <url>` in it. launch.Claude implements it.
type Launcher interface {
	Launch(ctx context.Context, pr model.PR) (review.Worktree, error)
}

// claudeDoneMsg carries the result of one Claude launch.
type claudeDoneMsg struct {
	pr  model.PR
	wt  review.Worktree
	err error
}

// launchClaude starts the Claude session for the PR detail in a command.
func (m *Model) launchClaude(pr model.PR) tea.Cmd {
	if m.launcher == nil {
		return m.status.SetMessage("Claude launch is not available")
	}
	ctx, l := m.ctx, m.launcher
	run := func() tea.Msg {
		wt, err := l.Launch(ctx, pr)
		return claudeDoneMsg{pr: pr, wt: wt, err: err}
	}
	return tea.Batch(m.status.SetMessage("claude: start "+prRef(pr)), run)
}

// handleClaudeDone opens the clone prompt for a missing clone, or shows
// the result.
func (m *Model) handleClaudeDone(msg claudeDoneMsg) tea.Cmd {
	ref := prRef(msg.pr)
	var missing *review.MissingClone
	switch {
	case errors.As(msg.err, &missing):
		return func() tea.Msg { return CloneNeededMsg{Missing: missing} }
	case errors.Is(msg.err, launch.ErrNoAgterm):
		return m.status.SetMessage("Claude launch requires agterm")
	case msg.err != nil:
		return m.status.SetMessage("claude " + ref + " failed: " + firstLine(msg.err.Error()))
	}
	return m.status.SetMessage(withWarning("claude "+ref+": session started", msg.wt))
}

// withWarning adds the worktree warnings to the status message: a dirty
// worktree that stays at its old commit, and the info/exclude error. A
// warning does not stop the review or the launch.
func withWarning(text string, wt review.Worktree) string {
	if wt.Dirty {
		text += "; warning: the worktree has local changes, so argus did not move it to the PR head"
	}
	if wt.ExcludeErr != nil {
		text += "; warning: " + firstLine(wt.ExcludeErr.Error())
	}
	return text
}
