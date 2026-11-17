package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/review"
)

// Cloner clones a repository that has no local clone. review.Worktrees
// implements it.
type Cloner interface {
	Clone(ctx context.Context, c *review.MissingClone) error
}

// CloneNeededMsg opens the clone prompt for a repository that has no local
// clone. The review and launch keys send it when review.Worktrees.Setup
// returns a *review.MissingClone error.
type CloneNeededMsg struct{ Missing *review.MissingClone }

// handleCloneNeeded opens the clone prompt. Only a confirm clones the
// repository.
func (m *Model) handleCloneNeeded(msg CloneNeededMsg) tea.Cmd {
	if m.cloner == nil {
		return m.status.SetMessage("no clone of " + msg.Missing.Repo)
	}
	d := &cloneDialog{missing: msg.Missing}
	cloner := m.cloner
	d.submit = func() tea.Cmd {
		return m.action(d, "", "clone "+d.missing.Repo, false, func(ctx context.Context) (string, error) {
			return "", cloner.Clone(ctx, d.missing)
		})
	}
	m.dialogs = append(m.dialogs, d)
	return nil
}

// cloneDialog asks before argus clones a repository.
type cloneDialog struct {
	missing *review.MissingClone
	sending bool
	err     error
	submit  func() tea.Cmd
}

func (d *cloneDialog) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	s := msg.String()
	// The root model closes the dialog on esc and quits on q and ctrl+c.
	if s == "esc" || s == "q" || s == "ctrl+c" {
		return d, nil, false
	}
	if d.sending {
		return d, nil, true
	}
	switch s {
	case "y", "enter":
		d.sending, d.err = true, nil
		return d, d.submit(), true
	case "n":
		return d, CloseDialog(), true
	}
	return d, nil, true
}

func (d *cloneDialog) failed(err error) { d.sending, d.err = false, err }

func (d *cloneDialog) Unsent() bool { return false }

func (d *cloneDialog) View(width int) string {
	width = max(width, 10)
	lines := []string{
		ansi.Wrap("No local clone of "+d.missing.Repo+".", width, ""),
		ansi.Wrap("Clone it into "+d.missing.Dir+"?", width, ""),
		"",
	}
	if d.err != nil {
		lines = append(lines, ansi.Wrap("Error: "+firstLine(d.err.Error()), width, ""))
	}
	if d.sending {
		lines = append(lines, "Cloning…")
	} else {
		lines = append(lines, "[y/enter] clone  [n/esc] cancel")
	}
	return strings.Join(lines, "\n")
}
