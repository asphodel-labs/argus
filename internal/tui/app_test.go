package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

const waitTimeout = 3 * time.Second

type fakePoller struct {
	ch chan poller.Update

	mu       sync.Mutex
	refresh  int
	loadMore []poller.Scope
}

func newFakePoller() *fakePoller { return &fakePoller{ch: make(chan poller.Update, 8)} }

func (p *fakePoller) Updates() <-chan poller.Update { return p.ch }

func (p *fakePoller) RefreshNow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refresh++
}

func (p *fakePoller) LoadMore(s poller.Scope) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadMore = append(p.loadMore, s)
}

func (p *fakePoller) refreshCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refresh
}

// fakeNotifier sends each snapshot to calls and returns err.
type fakeNotifier struct {
	calls chan *poller.Snapshot
	err   error
}

func (n *fakeNotifier) Process(_ context.Context, snap *poller.Snapshot) error {
	n.calls <- snap
	return n.err
}

// fakeDialog passes every key to the root model.
type fakeDialog struct{ unsent bool }

func (d *fakeDialog) Update(tea.KeyPressMsg) (Dialog, tea.Cmd, bool) { return d, nil, false }
func (d *fakeDialog) View(int) string                                { return "fake dialog" }
func (d *fakeDialog) Unsent() bool                                   { return d.unsent }

func testStyles(t *testing.T) Styles {
	t.Helper()
	st, err := NewStyles(nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func newTestProgram(t *testing.T, opts Options) *teatest.TestModel {
	t.Helper()
	opts.Styles = testStyles(t)
	return teatest.NewTestModel(t, New(opts), teatest.WithInitialTermSize(100, 30))
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func waitForText(t *testing.T, tm *teatest.TestModel, texts ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		for _, s := range texts {
			if !bytes.Contains(out, []byte(s)) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(waitTimeout), teatest.WithCheckInterval(10*time.Millisecond))
}

func finalModel(t *testing.T, tm *teatest.TestModel) *Model {
	t.Helper()
	m, ok := tm.FinalModel(t, teatest.WithFinalTimeout(waitTimeout)).(*Model)
	if !ok {
		t.Fatal("final model is not *Model")
	}
	return m
}

func TestFocusSwitch(t *testing.T) {
	tm := newTestProgram(t, Options{})
	for _, k := range []string{"tab", "tab", "l", "h", "l"} {
		tm.Send(key(k))
	}
	// The help overlay names the focused pane.
	tm.Send(key("?"))
	waitForText(t, tm, "Detail pane", "next / previous thread")
	tm.Send(key("q"))
	if got := finalModel(t, tm).Focus(); got != FocusDetail {
		t.Fatalf("focus = %v, want detail", got)
	}
}

func TestFocusKeys(t *testing.T) {
	m := New(Options{Styles: testStyles(t)})
	steps := []struct {
		key  string
		want Focus
	}{
		{"tab", FocusDetail},
		{"tab", FocusList},
		{"l", FocusDetail},
		{"l", FocusDetail},
		{"h", FocusList},
		{"h", FocusList},
	}
	for i, s := range steps {
		m.Update(key(s.key))
		if m.Focus() != s.want {
			t.Fatalf("step %d (%s): focus = %v, want %v", i, s.key, m.Focus(), s.want)
		}
	}
}

func TestHelpOverlay(t *testing.T) {
	tm := newTestProgram(t, Options{})
	tm.Send(key("?"))
	waitForText(t, tm, "List pane", "hide approved", "Global")
	// tab moves the focus under the overlay, and the help follows the focus.
	tm.Send(key("tab"))
	waitForText(t, tm, "Detail pane", "fold thread")
	tm.Send(key("esc"))
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if m.HelpVisible() {
		t.Fatal("help is visible after esc")
	}
	if m.Focus() != FocusDetail {
		t.Fatalf("focus = %v, want detail", m.Focus())
	}
}

func TestHelpToggle(t *testing.T) {
	m := New(Options{Styles: testStyles(t)})
	m.Update(key("?"))
	if !m.HelpVisible() {
		t.Fatal("help is not visible after ?")
	}
	if v := m.View().Content; !strings.Contains(v, "hide approved") {
		t.Fatalf("help view has no list bindings:\n%s", v)
	}
	m.Update(key("?"))
	if m.HelpVisible() {
		t.Fatal("help is visible after the second ?")
	}
}

func TestQuitConfirmationWithUnsentText(t *testing.T) {
	tm := newTestProgram(t, Options{})
	tm.Send(PushDialogMsg{Dialog: &fakeDialog{unsent: true}})
	waitForText(t, tm, "fake dialog")

	tm.Send(key("q"))
	waitForText(t, tm, "Quit and drop the text?")
	tm.Send(key("n"))
	waitForText(t, tm, "fake dialog")

	tm.Send(key("ctrl+c"))
	waitForText(t, tm, "Quit and drop the text?")
	tm.Send(key("y"))
	m := finalModel(t, tm)
	if m.Dialogs() != 2 {
		t.Fatalf("dialogs = %d, want 2 (the dialog and the confirmation)", m.Dialogs())
	}
}

func TestQuitWithoutUnsentText(t *testing.T) {
	tm := newTestProgram(t, Options{})
	tm.Send(PushDialogMsg{Dialog: &fakeDialog{}})
	waitForText(t, tm, "fake dialog")
	tm.Send(key("q"))
	if m := finalModel(t, tm); m.Dialogs() != 1 {
		t.Fatalf("dialogs = %d, want 1: no confirmation", m.Dialogs())
	}
}

func TestQuitConfirmationKeys(t *testing.T) {
	m := New(Options{Styles: testStyles(t)})
	m.Update(PushDialogMsg{Dialog: &fakeDialog{unsent: true}})
	if _, cmd := m.Update(key("q")); cmd != nil {
		t.Fatal("q with unsent text returned a command, want the confirmation")
	}
	if m.Dialogs() != 2 {
		t.Fatalf("dialogs = %d, want 2", m.Dialogs())
	}
	// A second q in the confirmation closes it.
	_, cmd := m.Update(key("q"))
	m.Update(cmd())
	if m.Dialogs() != 1 {
		t.Fatalf("dialogs = %d after q in the confirmation, want 1", m.Dialogs())
	}
	// esc with no handled key closes the top dialog.
	m.Update(key("esc"))
	if m.Dialogs() != 0 {
		t.Fatalf("dialogs = %d after esc, want 0", m.Dialogs())
	}
	if _, cmd := m.Update(key("q")); cmd == nil {
		t.Fatal("q with no dialog returned no command, want quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q with no dialog did not quit")
	}
}

func TestDialogBlocksPaneKeys(t *testing.T) {
	m := New(Options{Styles: testStyles(t)})
	m.Update(PushDialogMsg{Dialog: &fakeDialog{}})
	m.Update(key("tab"))
	m.Update(key("?"))
	if m.Focus() != FocusList || m.HelpVisible() {
		t.Fatal("pane keys work under a dialog")
	}
	m.Update(CloseDialogMsg{})
	if m.Dialogs() != 0 {
		t.Fatalf("dialogs = %d after CloseDialogMsg, want 0", m.Dialogs())
	}
}

func testSnapshot(at time.Time, n int) *poller.Snapshot {
	prs := make([]model.PR, n)
	return &poller.Snapshot{
		RefreshedAt: at,
		Sections:    []poller.Section{{Scope: poller.Scope{Kind: poller.ScopeMine}, PRs: prs, Total: n}},
	}
}

func TestPollerSnapshotRunsNotifier(t *testing.T) {
	p := newFakePoller()
	n := &fakeNotifier{calls: make(chan *poller.Snapshot, 4)}
	tm := newTestProgram(t, Options{Poller: p, Notifier: n})

	at := time.Date(2026, 11, 16, 14, 5, 0, 0, time.Local)
	snap := testSnapshot(at, 3)
	p.ch <- poller.Update{Snapshot: snap}
	select {
	case got := <-n.calls:
		if got != snap {
			t.Fatal("notifier got a different snapshot")
		}
	case <-time.After(waitTimeout):
		t.Fatal("notifier was not called")
	}
	waitForText(t, tm, "refreshed 14:05", "My PRs")

	p.ch <- poller.Update{Err: errors.New("search: HTTP 502")}
	waitForText(t, tm, "refresh failed: search: HTTP 502")

	tm.Send(key("q"))
	m := finalModel(t, tm)
	if m.Snapshot() != snap {
		t.Fatal("an error replaced the last snapshot")
	}
	if m.Status().Err == nil {
		t.Fatal("status bar has no refresh error")
	}
	select {
	case <-n.calls:
		t.Fatal("notifier was called for an error update")
	default:
	}
}

func TestPollerClosedChannel(t *testing.T) {
	p := newFakePoller()
	close(p.ch)
	m := New(Options{Poller: p, Styles: testStyles(t)})
	if msg := m.Init()(); msg != (pollerClosedMsg{}) {
		t.Fatalf("Init command returned %T, want pollerClosedMsg", msg)
	}
	if _, cmd := m.Update(pollerClosedMsg{}); cmd != nil {
		t.Fatal("closed channel returned a command")
	}
}

func TestNotifierRunsInOrderOneAtATime(t *testing.T) {
	p := newFakePoller()
	n := &fakeNotifier{calls: make(chan *poller.Snapshot, 4)}
	m := New(Options{Poller: p, Notifier: n, Styles: testStyles(t)})

	s1, s2, s3 := testSnapshot(time.Now(), 1), testSnapshot(time.Now(), 2), testSnapshot(time.Now(), 3)
	m.Update(pollerMsg{Snapshot: s1})
	if !m.notifying {
		t.Fatal("first snapshot did not start the notifier")
	}
	m.Update(pollerMsg{Snapshot: s2})
	m.Update(pollerMsg{Snapshot: s3})
	if m.pendingNotify != s3 {
		t.Fatal("the newest snapshot does not wait for the running call")
	}

	_, cmd := m.Update(notifyDoneMsg{})
	if cmd == nil || !m.notifying || m.pendingNotify != nil {
		t.Fatal("done did not start the waiting snapshot")
	}
	if msg := cmd(); msg != (notifyDoneMsg{}) {
		t.Fatalf("command returned %#v, want notifyDoneMsg", msg)
	}
	if got := <-n.calls; got != s3 {
		t.Fatal("notifier did not get the newest snapshot")
	}
	m.Update(notifyDoneMsg{})
	if m.notifying {
		t.Fatal("notifier still runs after the last done")
	}
}

func TestNotifierErrorShowsMessage(t *testing.T) {
	m := New(Options{Notifier: &fakeNotifier{}, Styles: testStyles(t)})
	m.notifying = true
	m.Update(notifyDoneMsg{err: errors.New("write seen state: disk full")})
	if got := m.Status().Message(); got != "notify: write seen state: disk full" {
		t.Fatalf("message = %q", got)
	}
}

func TestRefreshKey(t *testing.T) {
	p := newFakePoller()
	m := New(Options{Poller: p, Styles: testStyles(t)})
	_, cmd := m.Update(key("ctrl+r"))
	if p.refreshCount() != 1 {
		t.Fatalf("RefreshNow calls = %d, want 1", p.refreshCount())
	}
	if cmd == nil || m.Status().Message() != "refresh requested" {
		t.Fatal("ctrl+r shows no message")
	}
}

func TestViewFitsSize(t *testing.T) {
	for _, size := range [][2]int{{90, 20}, {60, 12}, {160, 40}} {
		w, h := size[0], size[1]
		m := New(Options{Poller: newFakePoller(), Styles: testStyles(t)})
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.Update(pollerMsg{Snapshot: testSnapshot(time.Now(), 2)})
		for _, view := range []string{"panes", "help"} {
			if view == "help" {
				m.Update(key("?"))
			}
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != h {
				t.Fatalf("%dx%d %s: view has %d lines, want %d", w, h, view, len(lines), h)
			}
			for i, l := range lines {
				if lw := lipgloss.Width(l); lw > w {
					t.Fatalf("%dx%d %s: line %d has width %d", w, h, view, i, lw)
				}
			}
		}
	}
}
