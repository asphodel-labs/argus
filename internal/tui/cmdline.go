package tui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/asphodel-labs/argus/internal/config"
	"github.com/asphodel-labs/argus/internal/model"
)

// Commander runs a custom command of the config on a PR detail.
// commands.Runner implements it. Run returns a *review.MissingClone error
// if the PR repository has no local clone.
type Commander interface {
	Run(ctx context.Context, cmd config.Command, pr model.PR) (string, error)
}

// lineCommand is one command of the command line: the name after ":" and
// the function that runs it.
type lineCommand struct {
	name string
	run  func(m *Model) tea.Cmd
}

// lineCommands returns the commands of the command line, in the order of
// the completion.
func (m *Model) lineCommands() []lineCommand {
	out := []lineCommand{
		{name: "draft", run: (*Model).convertToDraft},
		{name: "ready", run: (*Model).markReady},
		{name: "close", run: (*Model).closePR},
	}
	for _, c := range m.commands {
		out = append(out, lineCommand{name: c.Name, run: func(m *Model) tea.Cmd { return m.runCustom(c) }})
	}
	return out
}

// commandLineMsg tells that the user sent a command name from the command
// line d.
type commandLineMsg struct {
	d    *commandLine
	name string
}

// commandLine is the dialog of the ":" key. It shows one text line in
// place of the status bar. tab completes a command name, enter runs the
// command, and esc closes the line.
type commandLine struct {
	names []string
	text  []rune
}

// newCommandLine returns the command line with the command names of the
// model.
func (m *Model) newCommandLine() *commandLine {
	cmds := m.lineCommands()
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.name
	}
	return &commandLine{names: names}
}

// Update handles every key but esc and ctrl+c, so that each letter types
// into the line.
func (c *commandLine) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return c, nil, false
	case "enter":
		name := strings.TrimSpace(string(c.text))
		return c, func() tea.Msg { return commandLineMsg{d: c, name: name} }, true
	case "tab":
		c.complete()
	case "backspace":
		if n := len(c.text); n > 0 {
			c.text = c.text[:n-1]
		}
	default:
		c.text = append(c.text, []rune(msg.Text)...)
	}
	return c, nil, true
}

// matches returns the command names that start with the text.
func (c *commandLine) matches() []string {
	prefix := string(c.text)
	var out []string
	for _, n := range c.names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// complete extends the text to the longest common prefix of the matching
// command names.
func (c *commandLine) complete() {
	found := c.matches()
	if len(found) == 0 {
		return
	}
	// The prefix is a rune slice, so that a cut does not split a
	// multi-byte rune.
	common := []rune(found[0])
	for _, n := range found[1:] {
		for !strings.HasPrefix(n, string(common)) {
			common = common[:len(common)-1]
		}
	}
	c.text = common
}

// View returns the line: the text and the matching command names.
func (c *commandLine) View(width int) string {
	line := ":" + string(c.text) + "█"
	if found := c.matches(); len(found) > 0 {
		line += "   " + strings.Join(found, " ")
	}
	return ansi.Truncate(line, width, "…")
}

func (c *commandLine) Unsent() bool { return false }

// handleCommandLine closes the command line and runs the command of the
// name. An empty name only closes the line. A message from a line that is
// already closed does nothing. A second enter before the first message
// arrives sends this message, and the command must not run two times.
func (m *Model) handleCommandLine(msg commandLineMsg) tea.Cmd {
	i := slices.Index(m.dialogs, Dialog(msg.d))
	if i < 0 {
		return nil
	}
	m.dialogs = slices.Delete(m.dialogs, i, i+1)
	if msg.name == "" {
		return nil
	}
	for _, c := range m.lineCommands() {
		if c.name == msg.name {
			return c.run(m)
		}
	}
	return m.status.SetMessage("unknown command: " + msg.name)
}

// loadedDetail returns the loaded detail of the opened PR for a GitHub
// command. If the command cannot run, it returns the status message
// command and false.
func (m *Model) loadedDetail() (model.PR, tea.Cmd, bool) {
	if !m.detail.hasSel {
		return model.PR{}, m.status.SetMessage("open a PR with enter"), false
	}
	if m.github == nil {
		return model.PR{}, m.status.SetMessage("GitHub actions are not available"), false
	}
	// The checks read the detail and not m.detail.sel: sel changes only
	// with the next poll snapshot, and a refresh loads only the detail.
	detail, loaded := m.detail.cache[m.detail.sel.ID]
	if !loaded {
		return model.PR{}, m.status.SetMessage("the PR detail is not loaded yet"), false
	}
	return detail.pr, nil, true
}

// convertToDraft runs ":draft" on the opened PR.
func (m *Model) convertToDraft() tea.Cmd {
	pr, cmd, ok := m.loadedDetail()
	if !ok {
		return cmd
	}
	if pr.IsDraft {
		return m.status.SetMessage("already a draft")
	}
	gh := m.github
	return m.action(nil, pr.ID, "convert "+prRef(pr)+" to draft", true, func(ctx context.Context) (string, error) {
		return "", gh.ConvertToDraft(ctx, pr.ID)
	})
}

// markReady runs ":ready" on the opened PR.
func (m *Model) markReady() tea.Cmd {
	pr, cmd, ok := m.loadedDetail()
	if !ok {
		return cmd
	}
	if !pr.IsDraft {
		return m.status.SetMessage("already ready")
	}
	gh := m.github
	return m.action(nil, pr.ID, "mark "+prRef(pr)+" ready", true, func(ctx context.Context) (string, error) {
		return "", gh.MarkReady(ctx, pr.ID)
	})
}

// closePR runs ":close" on the opened PR. It closes the PR without a merge.
func (m *Model) closePR() tea.Cmd {
	pr, cmd, ok := m.loadedDetail()
	if !ok {
		return cmd
	}
	if pr.State != "OPEN" {
		return m.status.SetMessage("the PR is not open")
	}
	gh := m.github
	return m.action(nil, pr.ID, "close "+prRef(pr), true, func(ctx context.Context) (string, error) {
		return "", gh.ClosePR(ctx, pr.ID)
	})
}

// runCustom runs the custom command c on the loaded detail of the opened
// PR. The commander returns the text "<name>: done", and the action
// message adds ": done" to the description, so the action ignores the
// text. A missing clone opens the clone prompt (handleActionDone).
func (m *Model) runCustom(c config.Command) tea.Cmd {
	if !m.detail.hasSel {
		return m.status.SetMessage("open a PR with enter")
	}
	if m.commander == nil {
		return m.status.SetMessage("custom commands are not available")
	}
	detail, loaded := m.detail.cache[m.detail.sel.ID]
	if !loaded {
		return m.status.SetMessage("the PR detail is not loaded yet")
	}
	pr, commander := detail.pr, m.commander
	return m.action(nil, pr.ID, c.Name, true, func(ctx context.Context) (string, error) {
		_, err := commander.Run(ctx, c, pr)
		return "", err
	})
}

// customByKey returns the custom command of the key, or false.
func (m *Model) customByKey(key string) (config.Command, bool) {
	for _, c := range m.commands {
		if c.Key != "" && c.Key == key {
			return c, true
		}
	}
	return config.Command{}, false
}
