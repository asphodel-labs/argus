package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/asphodel-labs/argus/internal/commands"
	"github.com/asphodel-labs/argus/internal/config"
	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/poller"
)

var ciRepo = model.Repo{Owner: "acme", Name: "api"}

// ciRuns returns a failed run #12 and a successful run #11.
func ciRuns() []model.WorkflowRun {
	return []model.WorkflowRun{
		{ID: 1002, Number: 12, Attempt: 1, Workflow: "CI", Title: "Fix the parser", Event: "push",
			Status: "completed", Conclusion: "failure", UpdatedAt: listNow.Add(-5 * time.Minute),
			URL: "https://github.com/acme/api/actions/runs/1002"},
		{ID: 1001, Number: 11, Attempt: 1, Workflow: "Nightly", Title: "Nightly", Event: "schedule",
			Status: "completed", Conclusion: "success", UpdatedAt: listNow.Add(-3 * time.Hour),
			URL: "https://github.com/acme/api/actions/runs/1001"},
	}
}

// newCIModel returns a model with a fake GitHub that has the default
// branch main and the runs of ciRuns.
func newCIModel(t *testing.T) (*Model, *fakeGitHub) {
	t.Helper()
	f := &fakeGitHub{branch: "main", runs: ciRuns()}
	m := mustNew(t, Options{GitHub: f, Styles: testStyles(t), Me: "me", Now: fixedNow})
	drive(m, m.update(tea.WindowSizeMsg{Width: 160, Height: 60}))
	return m, f
}

// topCI returns the top dialog as a CI dialog.
func topCI(t *testing.T, m *Model) *ciDialog {
	t.Helper()
	d, ok := m.topDialog().(*ciDialog)
	if !ok {
		t.Fatalf("top dialog = %T, want *ciDialog", m.topDialog())
	}
	return d
}

func ciText(d *ciDialog, width int) string { return ansi.Strip(d.View(width)) }

func TestCIDialogLoadFillsRows(t *testing.T) {
	m, f := newCIModel(t)
	drive(m, m.openCIDialog(ciRepo))
	d := topCI(t, m)
	wantCalls(t, f.Calls(), "DefaultBranch acme/api", "BranchRuns acme/api main 20")
	if d.loading || d.branch != "main" || len(d.runs) != 2 {
		t.Fatalf("dialog: loading=%v branch=%q runs=%d", d.loading, d.branch, len(d.runs))
	}
	text := ciText(d, 72)
	for _, want := range []string{
		"CI · acme/api@main",
		"› ✗ #12 CI · Fix the parser · push · 5m",
		"  ✓ #11 Nightly · Nightly · schedule · 3h",
		"[f] failed [F] all [L] log [o] open [ctrl+r] reload [esc] close",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view has no %q:\n%s", want, text)
		}
	}
}

func TestCIDialogLoadingView(t *testing.T) {
	m, _ := newCIModel(t)
	cmd := m.openCIDialog(ciRepo)
	if cmd == nil {
		t.Fatal("open gives no load command")
	}
	text := ciText(topCI(t, m), 72)
	if !strings.Contains(text, "CI · acme/api\n") || !strings.Contains(text, "Loading…") {
		t.Fatalf("loading view:\n%s", text)
	}
}

func TestCIDialogReloadUsesCachedBranch(t *testing.T) {
	m, f := newCIModel(t)
	drive(m, m.openCIDialog(ciRepo))
	d := topCI(t, m)
	drive(m, m.loadCI(d))
	wantCalls(t, f.Calls(), "DefaultBranch acme/api", "BranchRuns acme/api main 20", "BranchRuns acme/api main 20")
}

func TestCIDialogLoadError(t *testing.T) {
	m, f := newCIModel(t)
	f.setErr("DefaultBranch", errors.New("default branch acme/api: HTTP 404"))
	drive(m, m.openCIDialog(ciRepo))
	d := topCI(t, m)
	text := ciText(d, 72)
	if d.loading || !strings.Contains(text, "Error: default branch acme/api: HTTP 404") {
		t.Fatalf("error view:\n%s", text)
	}
	if strings.Contains(text, "no runs on") {
		t.Fatalf("error view shows the empty text:\n%s", text)
	}
	wantCalls(t, f.Calls(), "DefaultBranch acme/api")
}

func TestCIDialogReloadErrorKeepsRuns(t *testing.T) {
	m, f := newCIModel(t)
	drive(m, m.openCIDialog(ciRepo))
	d := topCI(t, m)
	f.setErr("BranchRuns", errors.New("branch runs acme/api: HTTP 500"))
	drive(m, m.loadCI(d))
	text := ciText(d, 72)
	if len(d.runs) != 2 || !strings.Contains(text, "#12 CI") || !strings.Contains(text, "Error: branch runs acme/api: HTTP 500") {
		t.Fatalf("view after a reload error:\n%s", text)
	}
}

func TestCIDialogEmptyRuns(t *testing.T) {
	m, f := newCIModel(t)
	f.branch, f.runs = "master", nil
	drive(m, m.openCIDialog(ciRepo))
	text := ciText(topCI(t, m), 72)
	if !strings.Contains(text, "CI · acme/api@master") || !strings.Contains(text, "no runs on master") {
		t.Fatalf("empty view:\n%s", text)
	}
}

func TestCIDialogResultForClosedDialogDropped(t *testing.T) {
	m, _ := newCIModel(t)
	cmd := m.openCIDialog(ciRepo)
	d := topCI(t, m)
	m.Update(key("esc"))
	if m.Dialogs() != 0 {
		t.Fatalf("dialogs = %d after esc", m.Dialogs())
	}
	m.Update(cmd())
	if !d.loading || d.runs != nil || m.Dialogs() != 0 {
		t.Fatalf("closed dialog got the result: loading=%v runs=%d dialogs=%d", d.loading, len(d.runs), m.Dialogs())
	}
}

func TestCIDialogOldLoadDropped(t *testing.T) {
	m, f := newCIModel(t)
	first := m.openCIDialog(ciRepo)
	d := topCI(t, m)
	old := first()
	f.mu.Lock()
	f.runs = ciRuns()[:1]
	f.mu.Unlock()
	newer := m.loadCI(d)()
	m.Update(newer)
	m.Update(old)
	if d.loading || len(d.runs) != 1 || d.runs[0].Number != 12 {
		t.Fatalf("dialog keeps the old load: loading=%v runs=%+v", d.loading, d.runs)
	}
}

func TestCIDialogCursor(t *testing.T) {
	m, _ := newCIModel(t)
	drive(m, m.openCIDialog(ciRepo))
	d := topCI(t, m)
	for _, step := range []struct {
		key  string
		want int
	}{{"k", 0}, {"j", 1}, {"j", 1}, {"down", 1}, {"up", 0}, {"k", 0}} {
		m.Update(key(step.key))
		if d.cursor != step.want {
			t.Fatalf("after %q cursor = %d, want %d", step.key, d.cursor, step.want)
		}
	}
	m.Update(key("j"))
	if text := ciText(d, 72); !strings.Contains(text, "› ✓ #11 Nightly") {
		t.Fatalf("selected row:\n%s", text)
	}
}

func TestCIDialogCursorWithoutRuns(t *testing.T) {
	d := newCIDialog(ciRepo, fixedNow, Styles{})
	d.Update(key("j"))
	d.Update(key("k"))
	if d.cursor != 0 {
		t.Fatalf("cursor = %d without runs", d.cursor)
	}
}

func TestCIDialogCursorClampedAfterLoad(t *testing.T) {
	d := newCIDialog(ciRepo, fixedNow, Styles{})
	d.loaded(ciRunsMsg{branch: "main", runs: ciRuns()})
	d.cursor = 1
	d.loaded(ciRunsMsg{runs: ciRuns()[:1]})
	if d.cursor != 0 || d.branch != "main" {
		t.Fatalf("cursor = %d, branch = %q", d.cursor, d.branch)
	}
}

func TestCIDialogEscCloses(t *testing.T) {
	m, _ := newCIModel(t)
	drive(m, m.openCIDialog(ciRepo))
	m.Update(key("esc"))
	if m.Dialogs() != 0 {
		t.Fatalf("dialogs = %d after esc", m.Dialogs())
	}
}

func TestCIDialogViewTruncates(t *testing.T) {
	d := newCIDialog(ciRepo, fixedNow, Styles{})
	runs := ciRuns()
	runs[0].Title = strings.Repeat("long title ", 20)
	d.loaded(ciRunsMsg{branch: "main", runs: runs})
	for _, width := range []int{30, 72} {
		for _, line := range strings.Split(ciText(d, width), "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("width %d: line %q has width %d", width, line, w)
			}
		}
	}
}

func TestCIDialogWithoutGitHub(t *testing.T) {
	m := mustNew(t, Options{Styles: testStyles(t), Me: "me", Now: fixedNow})
	m.openCIDialog(ciRepo)
	if m.Dialogs() != 0 || m.Status().Message() != "GitHub actions are not available" {
		t.Fatalf("dialogs = %d, message = %q", m.Dialogs(), m.Status().Message())
	}
}

func TestRunIcon(t *testing.T) {
	st := testStyles(t)
	for _, tc := range []struct {
		status, conclusion, want string
	}{
		{"queued", "", "⏳"},
		{"in_progress", "", "⏳"},
		{"completed", "success", "✓"},
		{"completed", "skipped", "○"},
		{"completed", "neutral", "○"},
		{"completed", "failure", "✗"},
		{"completed", "cancelled", "✗"},
	} {
		got, _ := runIcon(model.WorkflowRun{Status: tc.status, Conclusion: tc.conclusion}, st)
		if got != tc.want {
			t.Errorf("runIcon(%s, %s) = %q, want %q", tc.status, tc.conclusion, got, tc.want)
		}
	}
}

// newCIListModel returns a model with the list snapshot, a fake GitHub,
// and no opened PR.
func newCIListModel(t *testing.T) (*Model, *fakeGitHub) {
	t.Helper()
	f := &fakeGitHub{branch: "main", runs: ciRuns()}
	m := mustNew(t, Options{Poller: newFakePoller(), GitHub: f, Styles: testStyles(t), Me: "me", BotLogins: []string{"renovate"}, Now: fixedNow})
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	m.Update(pollerMsg{Snapshot: listSnapshot()})
	return m, f
}

// pressDrive sends the key and runs the commands that it gives.
func pressDrive(m *Model, k string) {
	_, cmd := m.Update(key(k))
	drive(m, cmd)
}

func TestCIKeyOnGroupRowWithoutOpenedPR(t *testing.T) {
	m, f := newCIListModel(t)
	// Row 3 is the acme/api group of Needs my review. The cursor moves
	// there directly, so the detail pane opens no PR on the way.
	m.list.setCursor(3)
	if m.detail.hasSel {
		t.Fatal("a PR is open before the test")
	}
	pressDrive(m, "C")
	d := topCI(t, m)
	if d.repo.FullName() != "acme/api" || len(d.runs) != 2 {
		t.Fatalf("dialog repo %q, runs %d", d.repo.FullName(), len(d.runs))
	}
	wantCalls(t, f.Calls(), "DefaultBranch acme/api", "BranchRuns acme/api main 20")
}

func TestCIKeyOnPRRow(t *testing.T) {
	m, f := newCIListModel(t)
	// Row 2 is R2 in acme/web.
	sendKeys(m, "j", "j")
	pressDrive(m, "C")
	if d := topCI(t, m); d.repo.FullName() != "acme/web" {
		t.Fatalf("dialog repo %q", d.repo.FullName())
	}
	wantCalls(t, f.Calls(), "DefaultBranch acme/web", "BranchRuns acme/web main 20")
}

func TestCIKeyOnSectionAndFooterRow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cursor int
	}{{"section", 0}, {"footer", 17}} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newCIListModel(t)
			m.list.setCursor(tc.cursor)
			pressDrive(m, "C")
			if m.Dialogs() != 0 || m.Status().Message() != "move the cursor to a PR or a repo" {
				t.Fatalf("dialogs = %d, message = %q", m.Dialogs(), m.Status().Message())
			}
			if calls := f.Calls(); len(calls) != 0 {
				t.Fatalf("calls %v", calls)
			}
		})
	}
}

func TestCIKeyFlow(t *testing.T) {
	p := newFakePoller()
	f := &fakeGitHub{branch: "main", runs: ciRuns()}
	tm := teatest.NewTestModel(t, mustNew(t, Options{
		Poller: p, GitHub: f, Styles: testStyles(t), Me: "me", BotLogins: []string{"renovate"}, Now: fixedNow,
	}), teatest.WithInitialTermSize(160, 50))
	p.ch <- poller.Update{Snapshot: listSnapshot()}
	waitForText(t, tm, "Fix login")
	// Row 3 is the acme/api group of Needs my review.
	for range 3 {
		tm.Send(key("j"))
	}
	tm.Send(key("C"))
	// The renderer writes the "@main" of the title after the load as a
	// separate change, so the wait checks the run rows only.
	waitForText(t, tm, "#12 CI · Fix the parser", "#11 Nightly · Nightly")
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if text := ciText(topCI(t, m), 72); !strings.HasPrefix(text, "CI · acme/api@main\n") {
		t.Fatalf("dialog view:\n%s", text)
	}
	wantCalls(t, f.Calls(), "DefaultBranch acme/api", "BranchRuns acme/api main 20")
}

func TestCIKeyIsBuiltin(t *testing.T) {
	_, err := New(Options{Commands: []config.Command{customCommand("ci", "C")}, Styles: testStyles(t)})
	want := `command ci: key "C" is a built-in key`
	if err == nil || err.Error() != want {
		t.Fatalf("err %v, want %q", err, want)
	}
}

// fakeLogViewer records the Overlay calls.
type fakeLogViewer struct {
	mu      sync.Mutex
	titles  []string
	scripts []string
	err     error
}

func (f *fakeLogViewer) Overlay(_ context.Context, title, script string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titles = append(f.titles, title)
	f.scripts = append(f.scripts, script)
	return f.err
}

func (f *fakeLogViewer) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.scripts)
}

// newCIActionModel returns a model with an open and loaded CI dialog of
// ciRepo, a fake log viewer, and a fake runner.
func newCIActionModel(t *testing.T) (*Model, *fakeGitHub, *fakeLogViewer, *fakeRunner) {
	t.Helper()
	f := &fakeGitHub{branch: "main", runs: ciRuns()}
	lv, r := &fakeLogViewer{}, &fakeRunner{}
	m := mustNew(t, Options{GitHub: f, LogViewer: lv, Runner: r, OpenCommand: "open",
		Styles: testStyles(t), Me: "me", Now: fixedNow})
	drive(m, m.update(tea.WindowSizeMsg{Width: 160, Height: 60}))
	drive(m, m.openCIDialog(ciRepo))
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	return m, f, lv, r
}

func TestCIDialogRerun(t *testing.T) {
	tests := []struct {
		key, call, status string
	}{
		{"f", "RerunFailedJobs acme/api 1002", "rerun failed jobs of run #12: done"},
		{"F", "RerunWorkflow acme/api 1002", "rerun run #12: done"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m, f, _, _ := newCIActionModel(t)
			d := topCI(t, m)
			pressDrive(m, tt.key)
			wantCalls(t, f.Calls(), tt.call, "BranchRuns acme/api main 20")
			if topCI(t, m) != d {
				t.Fatal("the dialog closed after the rerun")
			}
			if d.sending || d.loading || d.err != nil {
				t.Fatalf("dialog: sending=%v loading=%v err=%v", d.sending, d.loading, d.err)
			}
			if got := m.status.Message(); got != tt.status {
				t.Fatalf("status = %q, want %q", got, tt.status)
			}
		})
	}
}

func TestCIDialogRerunAllOnSuccessfulRun(t *testing.T) {
	m, f, _, _ := newCIActionModel(t)
	pressDrive(m, "j")
	pressDrive(m, "F")
	wantCalls(t, f.Calls(), "RerunWorkflow acme/api 1001", "BranchRuns acme/api main 20")
}

func TestCIDialogRerunFailedOnSuccessfulRun(t *testing.T) {
	m, f, _, _ := newCIActionModel(t)
	pressDrive(m, "j")
	pressDrive(m, "f")
	wantCalls(t, f.Calls())
	d := topCI(t, m)
	if d.sending {
		t.Fatal("the dialog is sending")
	}
	if text := ciText(d, 72); !strings.Contains(text, "run #11 has no failed jobs") {
		t.Fatalf("view has no note:\n%s", text)
	}
}

func TestCIDialogRerunError(t *testing.T) {
	m, f, _, _ := newCIActionModel(t)
	f.setErr("RerunFailedJobs", errors.New("403 forbidden"))
	d := topCI(t, m)
	pressDrive(m, "f")
	wantCalls(t, f.Calls(), "RerunFailedJobs acme/api 1002")
	if topCI(t, m) != d || d.sending {
		t.Fatalf("dialog: open=%v sending=%v", topCI(t, m) == d, d.sending)
	}
	if text := ciText(d, 72); !strings.Contains(text, "Error: 403 forbidden") || len(d.runs) != 2 {
		t.Fatalf("view:\n%s", text)
	}
}

func TestCIDialogRerunTwiceSendsOnce(t *testing.T) {
	m, f, _, _ := newCIActionModel(t)
	_, first := m.Update(key("f"))
	_, second := m.Update(key("f"))
	_, third := m.Update(key("F"))
	if second != nil || third != nil {
		t.Fatal("a key press while sending gives a command")
	}
	if !topCI(t, m).sending {
		t.Fatal("the dialog is not sending")
	}
	drive(m, first)
	wantCalls(t, f.Calls(), "RerunFailedJobs acme/api 1002", "BranchRuns acme/api main 20")
}

func TestCIDialogLog(t *testing.T) {
	m, f, lv, _ := newCIActionModel(t)
	d := topCI(t, m)
	pressDrive(m, "L")
	wantCalls(t, lv.calls(), "gh run view '1002' --repo 'acme/api' --log-failed | less -R")
	if lv.titles[0] != "log #12" {
		t.Fatalf("title = %q", lv.titles[0])
	}
	wantCalls(t, f.Calls())
	if topCI(t, m) != d || d.err != nil {
		t.Fatalf("dialog: open=%v err=%v", topCI(t, m) == d, d.err)
	}
}

func TestCIDialogLogOnSuccessfulRun(t *testing.T) {
	m, _, lv, _ := newCIActionModel(t)
	pressDrive(m, "j")
	pressDrive(m, "L")
	wantCalls(t, lv.calls())
	if text := ciText(topCI(t, m), 72); !strings.Contains(text, "run #11 has no failed jobs") {
		t.Fatalf("view has no note:\n%s", text)
	}
}

func TestCIDialogLogWithoutAgterm(t *testing.T) {
	m, _, lv, _ := newCIActionModel(t)
	lv.err = commands.ErrNoAgterm
	pressDrive(m, "L")
	d := topCI(t, m)
	if text := ciText(d, 72); !strings.Contains(text, "Error: log view requires agterm") {
		t.Fatalf("view:\n%s", text)
	}
}

func TestCIDialogLogWithoutLogViewer(t *testing.T) {
	m, _ := newCIModel(t)
	drive(m, m.openCIDialog(ciRepo))
	pressDrive(m, "L")
	if text := ciText(topCI(t, m), 72); !strings.Contains(text, "Error: log view is not available") {
		t.Fatalf("view:\n%s", text)
	}
}

func TestCIDialogOpen(t *testing.T) {
	m, _, _, r := newCIActionModel(t)
	d := topCI(t, m)
	pressDrive(m, "j")
	pressDrive(m, "o")
	calls := r.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"open", "https://github.com/acme/api/actions/runs/1001"}) {
		t.Fatalf("runner calls = %q", calls)
	}
	if topCI(t, m) != d {
		t.Fatal("the dialog closed after o")
	}
	if got := m.status.Message(); got != "open run #11: done" {
		t.Fatalf("status = %q", got)
	}
}

func TestCIDialogOpenError(t *testing.T) {
	m, _, _, r := newCIActionModel(t)
	r.code, r.stderr = 1, "no browser"
	pressDrive(m, "o")
	if text := ciText(topCI(t, m), 72); !strings.Contains(text, "Error: open exited with code 1: no browser") {
		t.Fatalf("view:\n%s", text)
	}
}

func TestCIDialogReloadKey(t *testing.T) {
	m, f, _, _ := newCIActionModel(t)
	f.mu.Lock()
	f.runs = f.runs[:1]
	f.mu.Unlock()
	pressDrive(m, "ctrl+r")
	wantCalls(t, f.Calls(), "BranchRuns acme/api main 20")
	if d := topCI(t, m); len(d.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(d.runs))
	}
}

func TestCIDialogActionWithoutRuns(t *testing.T) {
	m, f, lv, r := newCIActionModel(t)
	topCI(t, m).runs = nil
	for _, k := range []string{"f", "F", "L", "o"} {
		pressDrive(m, k)
	}
	wantCalls(t, f.Calls())
	wantCalls(t, lv.calls())
	if len(r.Calls()) != 0 {
		t.Fatalf("runner calls = %q", r.Calls())
	}
}

func TestCIActionForClosedDialog(t *testing.T) {
	m, f, _, _ := newCIActionModel(t)
	_, cmd := m.Update(key("f"))
	pressDrive(m, "esc")
	drive(m, cmd)
	wantCalls(t, f.Calls(), "RerunFailedJobs acme/api 1002")
	if m.topDialog() != nil {
		t.Fatalf("top dialog = %T", m.topDialog())
	}
	if got := m.status.Message(); got != "rerun failed jobs of run #12: done" {
		t.Fatalf("status = %q", got)
	}
}
