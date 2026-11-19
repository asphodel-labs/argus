// Package tui is the Bubble Tea terminal UI of argus.
package tui

import (
	"context"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/asphodel-labs/argus/internal/config"
	"github.com/asphodel-labs/argus/internal/execx"
	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/poller"
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
	// Detail loads the PR detail for the detail pane. Without it the
	// detail pane shows "Loading" for each PR.
	Detail Detailer
	// Search finds a PR by number on GitHub for the finder. Without it the
	// finder does not offer the GitHub search.
	Search PRSearcher
	// Orgs are the configured organizations. The finder search uses them.
	Orgs []string
	// GitHub runs the PR actions. Without it the action keys show a
	// message.
	GitHub GitHub
	// Merges keeps the last merge method for each repository. It is
	// optional.
	Merges MergeMemory
	// Runner runs the command that opens a URL in the browser.
	Runner execx.Runner
	// Cloner clones a repository after the user confirms the clone prompt.
	Cloner Cloner
	// Reviewer runs revdiff for the v key and submits the review. Without
	// it the v key shows a message.
	Reviewer Reviewer
	// Launcher starts the Claude session for the p key. Without it the p
	// key shows a message.
	Launcher Launcher
	// Commands are the custom commands of the config. A command runs with
	// ":name", or with its key if it has one.
	Commands []config.Command
	// Commander runs the custom commands. Without it a custom command
	// shows a message.
	Commander Commander
	// LogViewer opens the failed log of a run for the L key of the CI
	// dialog. Without it the L key shows a message.
	LogViewer LogViewer
	// OpenCommand is the command that opens a URL. It is "open" on macOS
	// and "xdg-open" on other systems when empty.
	OpenCommand string
	Styles      Styles
	Keys        KeyMap
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
	ctx       context.Context
	poller    Poller
	notifier  Notifier
	detailer  Detailer
	searcher  PRSearcher
	orgs      []string
	github    GitHub
	merges    MergeMemory
	runner    execx.Runner
	cloner    Cloner
	reviewer  Reviewer
	launcher  Launcher
	commands  []config.Command
	commander Commander
	logViewer LogViewer
	openCmd   string
	// pbcopy is true on macOS. Then the y key also runs pbcopy.
	pbcopy bool
	styles Styles
	keys   KeyMap

	width, height int
	focus         Focus
	help          bool
	dialogs       []Dialog
	status        StatusBar
	snapshot      *poller.Snapshot
	list          listPane
	detail        detailPane
	// cursorID is the ID of the PR under the list cursor at the last
	// followCursor step, or "" for a row that is not a PR row.
	cursorID string

	// notifying is true while a Notifier.Process call runs. The calls run
	// one at a time and in order. pendingNotify is the newest snapshot that
	// waits for the running call.
	notifying     bool
	pendingNotify *poller.Snapshot

	// reviewing holds the ids of the PRs whose revdiff overlay is open.
	reviewing map[string]bool
}

// New returns the root model. A zero Options.Keys gives DefaultKeyMap.
// New returns an error if the key of a custom command is a built-in key.
func New(opts Options) (*Model, error) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	keys := opts.Keys
	if keys.Quit.Keys == nil {
		keys = DefaultKeyMap()
	}
	keys, err := withCommandKeys(keys, opts.Commands)
	if err != nil {
		return nil, err
	}
	openCmd := opts.OpenCommand
	if openCmd == "" {
		openCmd = openCommand()
	}
	m := &Model{
		ctx:       ctx,
		github:    opts.GitHub,
		searcher:  opts.Search,
		orgs:      opts.Orgs,
		merges:    opts.Merges,
		runner:    opts.Runner,
		cloner:    opts.Cloner,
		reviewer:  opts.Reviewer,
		launcher:  opts.Launcher,
		commands:  opts.Commands,
		commander: opts.Commander,
		logViewer: opts.LogViewer,
		openCmd:   openCmd,
		pbcopy:    runtime.GOOS == "darwin",
		poller:    opts.Poller,
		notifier:  opts.Notifier,
		detailer:  opts.Detail,
		styles:    opts.Styles,
		keys:      keys,
		width:     80,
		height:    24,
		list:      newListPane(opts.Me, opts.BotLogins, opts.Now),
		detail:    newDetailPane(opts.Me, opts.Now, opts.Styles),

		reviewing: map[string]bool{},
	}
	m.resize()
	return m, nil
}

// Selected returns the PR under the list cursor, or false if the cursor is
// not on a PR.
func (m *Model) Selected() (model.PR, bool) { return m.list.Selected() }

// Opened returns the PR that the detail pane shows, or false if no PR is
// open. The action keys act on this PR.
func (m *Model) Opened() (model.PR, bool) { return m.detail.sel, m.detail.hasSel }

// Filters returns the list filters.
func (m *Model) Filters() Filters { return m.list.filters }

// layout returns the width of the list pane and the height of the panes.
// Both include the pane border.
func (m *Model) layout() (listWidth, mainHeight int) {
	return max(m.width*2/5, 20), max(m.height-1, 3)
}

// resize gives the panes their size inside the border.
func (m *Model) resize() {
	listWidth, h := m.layout()
	m.list.setHeight(h - 2)
	m.detail.setSize(m.detailWidth(listWidth)-2, h-2)
}

// detailWidth returns the width of the detail pane with its border.
func (m *Model) detailWidth(listWidth int) int {
	return max(m.width-listWidth, 20)
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

// Update handles one message. After each message the detail pane follows
// the list cursor, and the opened PR gets its newest summary from the
// snapshot.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.followCursor()
	if sync := m.syncDetail(); sync != nil {
		cmd = tea.Batch(cmd, sync)
	}
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg, tea.MouseWheelMsg:
		return m.handleMouse(msg.(tea.MouseMsg))
	case PushDialogMsg:
		m.dialogs = append(m.dialogs, msg.Dialog)
	case CloseDialogMsg:
		m.popDialog()
	case pollerMsg:
		return m.handlePoller(poller.Update(msg))
	case pollerClosedMsg:
		return nil
	case notifyDoneMsg:
		return m.handleNotifyDone(msg.err)
	case actionDoneMsg:
		return m.handleActionDone(msg)
	case yankDoneMsg:
		return m.handleYankDone(msg)
	case CloneNeededMsg:
		return m.handleCloneNeeded(msg)
	case revdiffDoneMsg:
		return m.handleRevdiffDone(msg)
	case claudeDoneMsg:
		return m.handleClaudeDone(msg)
	case tea.PasteMsg:
		if p, ok := m.topDialog().(paster); ok {
			p.paste(msg.Content)
		}
	case detailLoadedMsg:
		m.detail.loaded(msg)
	case clearMessageMsg:
		m.status.clearMessage(msg.id)
	case commandLineMsg:
		return m.handleCommandLine(msg)
	case finderPickMsg:
		return m.handleFinderPick(msg.entry)
	case finderSearchDoneMsg:
		return m.handleFinderSearchDone(msg)
	case userPickerLoadedMsg:
		return m.handleUserPickerLoaded(msg)
	case ciRunsMsg:
		m.handleCIRuns(msg)
	case ciActionMsg:
		return m.handleCIAction(msg)
	case authorFilterMsg:
		m.list.filters.Author = msg.login
		m.filtersChanged()
	}
	return nil
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
	default:
		if cmd, ok := m.handleActionKey(msg); ok {
			return cmd
		}
		if m.focus == FocusList {
			return m.handleListKey(msg)
		}
		return m.handleDetailKey(msg)
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
	// A full refresh sets a new RefreshedAt. A load more keeps the old one.
	full := m.snapshot != nil && !u.Snapshot.RefreshedAt.Equal(m.snapshot.RefreshedAt)
	if full {
		m.list.addMarks(diffMarks(m.snapshot, u.Snapshot, m.list.me))
	}
	m.snapshot = u.Snapshot
	m.list.setSnapshot(u.Snapshot)
	// The new rows can put the cursor on another PR. The pane follows the
	// cursor before the cache drop, so that the reload goes to the PR that
	// the pane shows after the refresh.
	m.followCursor()
	if full && m.detail.dropCache() {
		cmds = append(cmds, m.reloadShown())
	}
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
	bottom := m.status.Render(m.width, m.styles)
	var main string
	if cl, ok := m.topDialog().(*commandLine); ok {
		// The command line takes the place of the status bar.
		main, bottom = m.panes(mainHeight), cl.View(m.width)
	}
	switch {
	case main != "":
	case len(m.dialogs) > 0:
		main = m.overlay(mainHeight, m.dialogs[len(m.dialogs)-1].View(min(m.width-4, 72)))
	case m.help:
		main = m.overlay(mainHeight, m.helpView())
	default:
		main = m.panes(mainHeight)
	}
	v := tea.NewView(main + "\n" + bottom)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *Model) panes(height int) string {
	listWidth, _ := m.layout()
	list := m.pane(FocusList, listWidth, height, m.list.view(listWidth-2, m.styles, m.focus == FocusList))
	detail := m.pane(FocusDetail, m.detailWidth(listWidth), height, m.detail.view())
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
