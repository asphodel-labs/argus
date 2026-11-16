// Package tui is the Bubble Tea terminal UI of argus.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

// Focus is the pane that gets the pane keys.
type Focus int

// The two panes.
const (
	FocusList Focus = iota
	FocusDetail
)

// Poller is the part of poller.Poller that the TUI uses.
type Poller interface {
	Updates() <-chan poller.Update
	RefreshNow()
	LoadMore(scope poller.Scope)
}

// Notifier turns a snapshot into desktop notifications. notify.Notifier
// implements it.
type Notifier interface {
	Process(ctx context.Context, snap *poller.Snapshot) error
}

// Dialog is a modal dialog on the dialog stack. The top dialog gets each
// key press first.
type Dialog interface {
	// Update handles a key press. If handled is false, the root model
	// applies the global keys: quit and esc.
	Update(msg tea.KeyPressMsg) (d Dialog, cmd tea.Cmd, handled bool)
	// View returns the dialog content for the width.
	View(width int) string
	// Unsent reports whether the dialog has text that the user did not send.
	Unsent() bool
}

// PushDialogMsg puts a dialog on top of the dialog stack.
type PushDialogMsg struct{ Dialog Dialog }

// CloseDialogMsg removes the top dialog from the dialog stack.
type CloseDialogMsg struct{}

// PushDialog returns a command that opens d.
func PushDialog(d Dialog) tea.Cmd {
	return func() tea.Msg { return PushDialogMsg{Dialog: d} }
}

// CloseDialog returns a command that closes the top dialog.
func CloseDialog() tea.Cmd {
	return func() tea.Msg { return CloseDialogMsg{} }
}

// pollerMsg carries one update from the poller channel.
type pollerMsg poller.Update

// pollerClosedMsg tells that the poller closed its channel.
type pollerClosedMsg struct{}

// notifyDoneMsg tells that Notifier.Process returned.
type notifyDoneMsg struct{ err error }

// Options configure the root model.
type Options struct {
	// Context is the context of the notifier calls. It is
	// context.Background when nil.
	Context  context.Context
	Poller   Poller
	Notifier Notifier
	Styles   Styles
	Keys     KeyMap
	// Me is the login of the user. It drives the derived PR status.
	Me string
	// BotLogins are the logins that the bot filter counts as bots.
	BotLogins []string
	// Now gives the time for the PR age. It is time.Now when nil.
	Now func() time.Time
}

// Model is the root Bubble Tea model: two panes, the status bar, the help
// overlay, and the dialog stack.
type Model struct {
	ctx      context.Context
	poller   Poller
	notifier Notifier
	styles   Styles
	keys     KeyMap

	width, height int
	focus         Focus
	help          bool
	dialogs       []Dialog
	status        StatusBar
	snapshot      *poller.Snapshot
	list          listPane

	// notifying is true while a Notifier.Process call runs. The calls run
	// one at a time and in order. pendingNotify is the newest snapshot that
	// waits for the running call.
	notifying     bool
	pendingNotify *poller.Snapshot
}

// New returns the root model. A zero Options.Keys gives DefaultKeyMap.
func New(opts Options) *Model {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	keys := opts.Keys
	if keys.Quit.Keys == nil {
		keys = DefaultKeyMap()
	}
	m := &Model{
		ctx:      ctx,
		poller:   opts.Poller,
		notifier: opts.Notifier,
		styles:   opts.Styles,
		keys:     keys,
		width:    80,
		height:   24,
		list:     newListPane(opts.Me, opts.BotLogins, opts.Now),
	}
	m.resize()
	return m
}

// Selected returns the PR under the list cursor, or false if the cursor is
// not on a PR.
func (m *Model) Selected() (model.PR, bool) { return m.list.Selected() }

// Filters returns the list filters.
func (m *Model) Filters() Filters { return m.list.filters }

// layout returns the width of the list pane and the height of the panes.
// Both include the pane border.
func (m *Model) layout() (listWidth, mainHeight int) {
	return max(m.width*2/5, 20), max(m.height-1, 3)
}

// resize gives the list pane its height inside the border.
func (m *Model) resize() {
	_, h := m.layout()
	m.list.setHeight(h - 2)
}

// Focus returns the focused pane.
func (m *Model) Focus() Focus { return m.focus }

// HelpVisible reports whether the help overlay is open.
func (m *Model) HelpVisible() bool { return m.help }

// Dialogs returns the number of open dialogs.
func (m *Model) Dialogs() int { return len(m.dialogs) }

// Snapshot returns the last snapshot from the poller, or nil.
func (m *Model) Snapshot() *poller.Snapshot { return m.snapshot }

// Status returns the status bar.
func (m *Model) Status() *StatusBar { return &m.status }

// Init starts to read the poller channel.
func (m *Model) Init() tea.Cmd {
	if m.poller == nil {
		return nil
	}
	return waitForUpdate(m.poller.Updates())
}

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case PushDialogMsg:
		m.dialogs = append(m.dialogs, msg.Dialog)
	case CloseDialogMsg:
		m.popDialog()
	case pollerMsg:
		return m, m.handlePoller(poller.Update(msg))
	case pollerClosedMsg:
		return m, nil
	case notifyDoneMsg:
		return m, m.handleNotifyDone(msg.err)
	case clearMessageMsg:
		m.status.clearMessage(msg.id)
	case authorFilterMsg:
		m.list.filters.Author = msg.login
		m.filtersChanged()
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if n := len(m.dialogs); n > 0 {
		d, cmd, handled := m.dialogs[n-1].Update(msg)
		m.dialogs[n-1] = d
		if handled {
			return cmd
		}
	}

	k := m.keys
	// The search input gets every key but ctrl+c, so that q types a q.
	if len(m.dialogs) == 0 && !m.help && m.focus == FocusList && m.list.searching && !k.ForceQuit.Matches(msg) {
		return m.handleSearchInput(msg)
	}
	switch {
	case k.Quit.Matches(msg), k.ForceQuit.Matches(msg):
		return m.requestQuit()
	case k.Close.Matches(msg):
		if m.help {
			m.help = false
		} else {
			m.popDialog()
		}
		return nil
	}
	if len(m.dialogs) > 0 {
		return nil
	}

	switch {
	case k.Help.Matches(msg):
		m.help = !m.help
	case k.Refresh.Matches(msg):
		if m.poller != nil {
			m.poller.RefreshNow()
		}
		return m.status.SetMessage("refresh requested")
	case k.FocusSwitch.Matches(msg):
		m.focus = 1 - m.focus
	case k.FocusList.Matches(msg):
		m.focus = FocusList
	case k.FocusDetail.Matches(msg):
		m.focus = FocusDetail
	case m.help:
	case m.focus == FocusList:
		return m.handleListKey(msg)
	}
	return nil
}

// requestQuit quits at once, or opens the quit confirmation if a dialog has
// unsent text.
func (m *Model) requestQuit() tea.Cmd {
	for _, d := range m.dialogs {
		if _, ok := d.(*confirmQuitDialog); ok {
			return nil
		}
	}
	for _, d := range m.dialogs {
		if d.Unsent() {
			m.dialogs = append(m.dialogs, &confirmQuitDialog{})
			return nil
		}
	}
	return tea.Quit
}

func (m *Model) popDialog() {
	if n := len(m.dialogs); n > 0 {
		m.dialogs[n-1] = nil
		m.dialogs = m.dialogs[:n-1]
	}
}

func (m *Model) handlePoller(u poller.Update) tea.Cmd {
	cmds := []tea.Cmd{waitForUpdate(m.poller.Updates())}
	if u.Err != nil {
		m.status.Err = u.Err
		return tea.Batch(cmds...)
	}
	if u.Snapshot == nil {
		return tea.Batch(cmds...)
	}
	m.snapshot = u.Snapshot
	m.list.setSnapshot(u.Snapshot)
	m.status.Err = nil
	m.status.LastRefresh = u.Snapshot.RefreshedAt
	m.status.PausedUntil = u.Snapshot.PausedUntil
	if m.notifier != nil {
		if m.notifying {
			m.pendingNotify = u.Snapshot
		} else {
			cmds = append(cmds, m.startNotify(u.Snapshot))
		}
	}
	return tea.Batch(cmds...)
}

// startNotify runs Notifier.Process in a command, off the update loop.
func (m *Model) startNotify(snap *poller.Snapshot) tea.Cmd {
	m.notifying = true
	n, ctx := m.notifier, m.ctx
	return func() tea.Msg { return notifyDoneMsg{err: n.Process(ctx, snap)} }
}

func (m *Model) handleNotifyDone(err error) tea.Cmd {
	m.notifying = false
	var cmds []tea.Cmd
	if err != nil {
		cmds = append(cmds, m.status.SetMessage("notify: "+firstLine(err.Error())))
	}
	if snap := m.pendingNotify; snap != nil {
		m.pendingNotify = nil
		cmds = append(cmds, m.startNotify(snap))
	}
	return tea.Batch(cmds...)
}

func waitForUpdate(ch <-chan poller.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return pollerClosedMsg{}
		}
		return pollerMsg(u)
	}
}

// View renders the panes and the status bar, with the help overlay or the
// top dialog over the panes.
func (m *Model) View() tea.View {
	mainHeight := max(m.height-1, 3)
	var main string
	switch {
	case len(m.dialogs) > 0:
		main = m.overlay(mainHeight, m.dialogs[len(m.dialogs)-1].View(min(m.width-4, 72)))
	case m.help:
		main = m.overlay(mainHeight, m.helpView())
	default:
		main = m.panes(mainHeight)
	}
	v := tea.NewView(main + "\n" + m.status.Render(m.width, m.styles))
	v.AltScreen = true
	return v
}

func (m *Model) panes(height int) string {
	listWidth, _ := m.layout()
	detailWidth := max(m.width-listWidth, 20)
	list := m.pane(FocusList, listWidth, height, m.list.view(listWidth-2, m.styles, m.focus == FocusList))
	detail := m.pane(FocusDetail, detailWidth, height, m.styles.Muted.Render("No PR selected."))
	return lipgloss.JoinHorizontal(lipgloss.Top, list, detail)
}

func (m *Model) pane(f Focus, width, height int, content string) string {
	style := m.styles.PaneBorder
	if m.focus == f {
		style = m.styles.PaneBorderFocused
	}
	return style.Width(width).Height(height).MaxHeight(height).Render(content)
}

// helpView renders the help sections for the focused pane. It puts the
// sections side by side when the width permits.
func (m *Model) helpView() string {
	sections := m.keys.HelpFor(m.focus)
	blocks := make([]string, len(sections))
	for i, sec := range sections {
		lines := []string{m.styles.Title.Render(sec.Title)}
		for _, bind := range sec.Bindings {
			keys := m.styles.HelpKey.Render(strings.Join(bind.Keys, " "))
			lines = append(lines, "  "+padRight(keys, 16)+"  "+bind.Desc)
		}
		blocks[i] = strings.Join(lines, "\n")
	}
	// Try all sections in one row, then the first section beside the
	// others, then one column.
	layouts := [][][]string{
		blockColumns(blocks...),
		{blocks[:1], blocks[1:]},
		{blocks},
	}
	var out string
	for _, layout := range layouts {
		cols := make([]string, len(layout))
		for i, col := range layout {
			cols[i] = strings.Join(col, "\n\n")
			if i < len(layout)-1 {
				cols[i] = padBlock(cols[i], 3)
			}
		}
		out = lipgloss.JoinHorizontal(lipgloss.Top, cols...)
		if lipgloss.Width(out) <= m.width-4 {
			break
		}
	}
	return out
}

func blockColumns(blocks ...string) [][]string {
	cols := make([][]string, len(blocks))
	for i, b := range blocks {
		cols[i] = []string{b}
	}
	return cols
}

// padBlock adds n spaces on the right of each line of a block.
func padBlock(block string, n int) string {
	return lipgloss.NewStyle().PaddingRight(n).Render(block)
}

func (m *Model) overlay(height int, content string) string {
	box := m.styles.Dialog.Render(content)
	placed := lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box)
	return lipgloss.NewStyle().MaxHeight(height).MaxWidth(m.width).Render(placed)
}

func padRight(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// confirmQuitDialog asks before quit when a dialog has unsent text.
type confirmQuitDialog struct{}

func (d *confirmQuitDialog) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	switch msg.String() {
	case "y", "enter", "ctrl+c":
		return d, tea.Quit, true
	case "n", "esc", "q":
		return d, CloseDialog(), true
	}
	return d, nil, true
}

func (d *confirmQuitDialog) View(int) string {
	return "A dialog has unsent text.\nQuit and drop the text? [y/enter] quit  [n/esc] stay"
}

func (d *confirmQuitDialog) Unsent() bool { return false }
