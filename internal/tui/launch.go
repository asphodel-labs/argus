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
	Launch(ctx context.Context, pr model.PR) error
}

// claudeDoneMsg carries the result of one Claude launch.
type claudeDoneMsg struct {
	pr  model.PR
	err error
}

// launchClaude starts the Claude session for the PR detail in a command.
func (m *Model) launchClaude(pr model.PR) tea.Cmd {
	if m.launcher == nil {
		return m.status.SetMessage("Claude launch is not available")
	}
	ctx, l := m.ctx, m.launcher
	run := func() tea.Msg {
		return claudeDoneMsg{pr: pr, err: l.Launch(ctx, pr)}
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
	return m.status.SetMessage("claude " + ref + ": session started")
}
