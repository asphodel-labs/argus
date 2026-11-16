package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

// fakeGitHub records each action call and returns the error of its name.
type fakeGitHub struct {
	mu    sync.Mutex
	calls []string
	errs  map[string]error
}

func (f *fakeGitHub) record(name string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := []string{name}
	for _, a := range args {
		parts = append(parts, fmt.Sprint(a))
	}
	f.calls = append(f.calls, strings.Join(parts, " "))
	return f.errs[name]
}

func (f *fakeGitHub) setErr(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = map[string]error{}
	}
	f.errs[name] = err
}

func (f *fakeGitHub) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeGitHub) Approve(_ context.Context, prID, body string) error {
	return f.record("Approve", prID, body)
}

func (f *fakeGitHub) Merge(_ context.Context, prID string, method model.MergeMethod, headSHA string) error {
	return f.record("Merge", prID, method, headSHA)
}

func (f *fakeGitHub) EnableAutoMerge(_ context.Context, prID string, method model.MergeMethod, headSHA string) error {
	return f.record("EnableAutoMerge", prID, method, headSHA)
}

func (f *fakeGitHub) AddComment(_ context.Context, subjectID, body string) error {
	return f.record("AddComment", subjectID, body)
}

func (f *fakeGitHub) ReplyToThread(_ context.Context, threadID, body string) error {
	return f.record("ReplyToThread", threadID, body)
}

func (f *fakeGitHub) AddReaction(_ context.Context, subjectID, content string) error {
	return f.record("AddReaction", subjectID, content)
}

func (f *fakeGitHub) ResolveThread(_ context.Context, threadID string) error {
	return f.record("ResolveThread", threadID)
}

func (f *fakeGitHub) UnresolveThread(_ context.Context, threadID string) error {
	return f.record("UnresolveThread", threadID)
}

func (f *fakeGitHub) RerunFailedJobs(_ context.Context, repo model.Repo, runID int64) error {
	return f.record("RerunFailedJobs", repo.FullName(), runID)
}

func (f *fakeGitHub) RerunWorkflow(_ context.Context, repo model.Repo, runID int64) error {
	return f.record("RerunWorkflow", repo.FullName(), runID)
}

// fakeMemory is an in-memory MergeMemory.
type fakeMemory struct {
	mu      sync.Mutex
	methods map[string]model.MergeMethod
	err     error
}

func (f *fakeMemory) MergeMethod(repo string) (model.MergeMethod, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.methods[repo]
	return m, ok
}

func (f *fakeMemory) SetMergeMethod(repo string, method model.MergeMethod) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.methods == nil {
		f.methods = map[string]model.MergeMethod{}
	}
	f.methods[repo] = method
	return f.err
}

// fakeRunner records each command and returns code.
type fakeRunner struct {
	mu     sync.Mutex
	calls  [][]string
	code   int
	stderr string
	err    error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	return "", r.stderr, r.code, r.err
}

// actionPR is a PR of bob that the user can merge with MERGE or SQUASH.
func actionPR() model.PR {
	p := testPR("PR_1", "acme/api", 7, "Add retry", "bob")
	p.URL = "https://github.com/acme/api/pull/7"
	p.HeadSHA = "abc123"
	p.Mergeable = model.MergeableYes
	p.MergeStateStatus = "CLEAN"
	p.Rollup = model.CheckSuccess
	p.MergeMethods = []model.MergeMethod{model.MergeMethodMerge, model.MergeMethodSquash}
	p.Threads = []model.Thread{
		{ID: "T1", Path: "a.go", Line: 3, Comments: []model.Comment{
			{ID: "C1", Author: model.Actor{Login: "alice"}, Body: "first"},
			{ID: "C2", Author: model.Actor{Login: "bob"}, Body: "second"},
		}},
		{ID: "T2", Path: "b.go", Line: 9, IsResolved: true, Comments: []model.Comment{
			{ID: "C3", Author: model.Actor{Login: "alice"}, Body: "done"},
		}},
	}
	p.Comments = []model.Comment{{ID: "IC1", Author: model.Actor{Login: "carol"}, Body: "PR comment", CreatedAt: listNow}}
	p.Reviews = []model.Review{{ID: "RV1", Author: model.Actor{Login: "dave"}, State: "COMMENTED", Body: "review body", SubmittedAt: listNow.Add(time.Minute)}}
	return p
}

type actionEnv struct {
	m      *Model
	gh     *fakeGitHub
	det    *fakeDetailer
	mem    *fakeMemory
	runner *fakeRunner
}

// newActionModel returns a model with pr in the list. The cursor is on pr
// and its detail is loaded.
func newActionModel(t *testing.T, pr model.PR) *actionEnv {
	t.Helper()
	env := &actionEnv{
		gh:     &fakeGitHub{},
		det:    &fakeDetailer{prs: map[string]model.PR{pr.ID: pr}},
		mem:    &fakeMemory{},
		runner: &fakeRunner{},
	}
	env.m = New(Options{
		Detail: env.det, GitHub: env.gh, Merges: env.mem, Runner: env.runner, OpenCommand: "open-url",
		Styles: testStyles(t), Me: "me", Now: fixedNow,
	})
	drive(env.m, env.m.update(tea.WindowSizeMsg{Width: 160, Height: 60}))
	env.m.list.setSnapshot(detailSnapshot(pr))
	env.press("j")
	return env
}

// drive runs the command and gives each message to the model, until no
// command is left. A command that does not return in a short time, such
// as the timer of a status message, is skipped.
func drive(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(200 * time.Millisecond):
		return
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			drive(m, c)
		}
	case nil:
	default:
		_, next := m.Update(msg)
		drive(m, next)
	}
}

func (e *actionEnv) press(keys ...string) {
	for _, k := range keys {
		_, cmd := e.m.Update(key(k))
		drive(e.m, cmd)
	}
}

func (e *actionEnv) send(msg tea.KeyPressMsg) {
	_, cmd := e.m.Update(msg)
	drive(e.m, cmd)
}

// typeText types each rune of s.
func (e *actionEnv) typeText(s string) {
	for _, r := range s {
		if r == ' ' {
			e.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			continue
		}
		e.send(key(string(r)))
	}
}

var ctrlS = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}

func (e *actionEnv) dialogText() string {
	d := e.m.topDialog()
	if d == nil {
		return ""
	}
	return ansi.Strip(d.View(72))
}

// waitUntil polls cond until it is true or waitTimeout passes.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("calls:\n got %q\nwant %q", got, want)
	}
}

func TestMergePlan(t *testing.T) {
	base := actionPR()
	tests := []struct {
		name    string
		edit    func(*model.PR)
		blocked string
		auto    bool
	}{
		{"clean", func(*model.PR) {}, "", false},
		{"unstable", func(p *model.PR) { p.MergeStateStatus = "UNSTABLE"; p.Rollup = model.CheckFailure }, "", false},
		{"pending checks not required", func(p *model.PR) { p.Rollup = model.CheckPending }, "", false},
		{"draft", func(p *model.PR) { p.IsDraft = true }, "draft", false},
		{"conflict", func(p *model.PR) { p.Mergeable = model.MergeableConflicting }, "conflicts", false},
		{"dirty", func(p *model.PR) { p.MergeStateStatus = "DIRTY" }, "conflicts", false},
		{"no methods", func(p *model.PR) { p.MergeMethods = nil }, "no merge method", false},
		{"blocked pending", func(p *model.PR) { p.MergeStateStatus = "BLOCKED"; p.Rollup = model.CheckPending }, "", true},
		{"blocked expected", func(p *model.PR) { p.MergeStateStatus = "BLOCKED"; p.Rollup = model.CheckExpected }, "", true},
		{"blocked", func(p *model.PR) { p.MergeStateStatus = "BLOCKED" }, "branch protection", false},
		{"behind", func(p *model.PR) { p.MergeStateStatus = "BEHIND" }, "behind", false},
		{"unknown state", func(p *model.PR) { p.MergeStateStatus = "UNKNOWN" }, "not computed", false},
		{"unknown mergeable", func(p *model.PR) { p.Mergeable = model.MergeableUnknown }, "not computed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base
			tt.edit(&p)
			blocked, auto := mergePlan(p)
			if auto != tt.auto || (tt.blocked == "") != (blocked == "") || !strings.Contains(blocked, tt.blocked) {
				t.Fatalf("got %q %v, want %q %v", blocked, auto, tt.blocked, tt.auto)
			}
		})
	}
}

// newActionProgram starts a teatest program with pr in the list, the
// cursor on pr, and its detail loaded.
func newActionProgram(t *testing.T, pr model.PR, gh *fakeGitHub, mem *fakeMemory) (*teatest.TestModel, *fakeDetailer) {
	t.Helper()
	p := newFakePoller()
	det := &fakeDetailer{prs: map[string]model.PR{pr.ID: pr}}
	tm := teatest.NewTestModel(t, New(Options{
		Poller: p, Detail: det, GitHub: gh, Merges: mem, Runner: &fakeRunner{},
		Styles: testStyles(t), Me: "me", Now: fixedNow,
	}), teatest.WithInitialTermSize(160, 50))
	p.ch <- poller.Update{Snapshot: detailSnapshot(pr)}
	waitForText(t, tm, "Needs my review")
	tm.Send(key("j"))
	waitForText(t, tm, "Reviews:")
	return tm, det
}

func TestMergeDialogAllowed(t *testing.T) {
	gh := &fakeGitHub{}
	mem := &fakeMemory{methods: map[string]model.MergeMethod{"acme/api": model.MergeMethodSquash}}
	tm, det := newActionProgram(t, actionPR(), gh, mem)
	tm.Send(key("M"))
	waitForText(t, tm, "› squash and merge", "[y/enter] merge")
	tm.Send(key("y"))
	waitForText(t, tm, "merge acme/api#7: done")
	waitUntil(t, func() bool { return det.callCount() == 2 })
	tm.Send(key("q"))
	m := finalModel(t, tm)

	wantCalls(t, gh.Calls(), "Merge PR_1 SQUASH abc123")
	if got, _ := mem.MergeMethod("acme/api"); got != model.MergeMethodSquash {
		t.Fatalf("stored method %q", got)
	}
	if m.Dialogs() != 0 {
		t.Fatalf("%d dialogs open after the merge", m.Dialogs())
	}
	if n := det.callCount(); n != 2 {
		t.Fatalf("detail calls = %d, want 2 (load and refresh after the merge)", n)
	}
}

func TestMergeDialogMethodChoice(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.press("M")
	if !strings.Contains(env.dialogText(), "› merge commit") {
		t.Fatalf("default is not the first method:\n%s", env.dialogText())
	}
	env.press("j", "j", "k", "j", "enter")
	wantCalls(t, env.gh.Calls(), "Merge PR_1 SQUASH abc123")
	if got, _ := env.mem.MergeMethod("acme/api"); got != model.MergeMethodSquash {
		t.Fatalf("stored method %q", got)
	}
}

func TestMergeDialogLastMethodNotAllowed(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.mem.methods = map[string]model.MergeMethod{"acme/api": model.MergeMethodRebase}
	env.press("M")
	got := env.dialogText()
	if !strings.Contains(got, "› merge commit") || strings.Contains(got, "rebase") {
		t.Fatalf("dialog shows a method that is not allowed:\n%s", got)
	}
}

func TestMergeDialogBlocked(t *testing.T) {
	pr := actionPR()
	pr.MergeStateStatus = "BLOCKED"
	gh := &fakeGitHub{}
	tm, _ := newActionProgram(t, pr, gh, &fakeMemory{})
	tm.Send(key("M"))
	waitForText(t, tm, "Blocked: branch protection blocks the merge")
	tm.Send(key("y"))
	tm.Send(key("enter"))
	tm.Send(key("esc"))
	tm.Send(key("q"))
	m := finalModel(t, tm)
	wantCalls(t, gh.Calls())
	if m.Dialogs() != 0 {
		t.Fatalf("%d dialogs open", m.Dialogs())
	}
}

func TestMergeDialogAutoMerge(t *testing.T) {
	pr := actionPR()
	pr.MergeStateStatus = "BLOCKED"
	pr.Rollup = model.CheckPending
	gh := &fakeGitHub{}
	mem := &fakeMemory{}
	tm, _ := newActionProgram(t, pr, gh, mem)
	tm.Send(key("M"))
	waitForText(t, tm, "Checks are pending", "[y/enter] enable auto-merge")
	tm.Send(key("enter"))
	waitForText(t, tm, "enable auto-merge for acme/api#7: done")
	tm.Send(key("q"))
	finalModel(t, tm)
	wantCalls(t, gh.Calls(), "EnableAutoMerge PR_1 MERGE abc123")
	if got, _ := mem.MergeMethod("acme/api"); got != model.MergeMethodMerge {
		t.Fatalf("stored method %q", got)
	}
}

func TestMergeErrorKeepsDialog(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.gh.setErr("Merge", errors.New("head moved"))
	env.mem.err = errors.New("unused")
	env.press("M", "y")
	if env.m.Dialogs() != 1 || !strings.Contains(env.dialogText(), "Error: head moved") {
		t.Fatalf("dialog after error:\n%s", env.dialogText())
	}
	if _, ok := env.mem.MergeMethod("acme/api"); ok {
		t.Fatal("failed merge stored the method")
	}
	env.gh.setErr("Merge", nil)
	env.press("y")
	if env.m.Dialogs() != 0 {
		t.Fatal("dialog open after the retry")
	}
	if msg := env.m.Status().Message(); !strings.Contains(msg, "merge acme/api#7: done; save merge method: unused") {
		t.Fatalf("message %q", msg)
	}
}

func TestApproveHiddenOnOwnPR(t *testing.T) {
	pr := actionPR()
	pr.Author.Login = "Me"
	gh := &fakeGitHub{}
	tm, _ := newActionProgram(t, pr, gh, &fakeMemory{})
	tm.Send(key("A"))
	waitForText(t, tm, "you cannot approve your own PR")
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if m.Dialogs() != 0 {
		t.Fatalf("%d dialogs open", m.Dialogs())
	}
	wantCalls(t, gh.Calls())
}

func TestApproveWithAndWithoutBody(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.press("A")
	env.typeText("looks good")
	env.send(ctrlS)
	env.press("A")
	env.send(ctrlS)
	wantCalls(t, env.gh.Calls(), "Approve PR_1 looks good", "Approve PR_1 ")
	if env.m.Dialogs() != 0 {
		t.Fatal("approve dialog is open")
	}
}

func TestCommentErrorKeepsText(t *testing.T) {
	gh := &fakeGitHub{}
	gh.setErr("AddComment", errors.New("boom"))
	tm, _ := newActionProgram(t, actionPR(), gh, &fakeMemory{})
	tm.Send(key("c"))
	waitForText(t, tm, "Comment on acme/api#7")
	for _, k := range []string{"h", "i", "q"} {
		tm.Send(key(k))
	}
	tm.Send(ctrlS)
	waitForText(t, tm, "Error: boom")
	tm.Send(key("esc"))
	tm.Send(key("q"))
	m := finalModel(t, tm)
	wantCalls(t, gh.Calls(), "AddComment PR_1 hiq")
	// esc closed the dialog that kept the text.
	if m.Dialogs() != 0 {
		t.Fatalf("%d dialogs open", m.Dialogs())
	}
}

func TestCommentErrorKeepsTextSync(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.gh.setErr("AddComment", errors.New("boom"))
	env.press("c")
	env.send(ctrlS)
	if !strings.Contains(env.dialogText(), "type a text first") {
		t.Fatalf("empty comment:\n%s", env.dialogText())
	}
	env.typeText("ab")
	env.press("enter", "c", "x", "backspace")
	env.send(ctrlS)
	d := env.m.topDialog().(*textDialog)
	if string(d.text) != "ab\nc" || d.sending || !strings.Contains(env.dialogText(), "Error: boom") {
		t.Fatalf("dialog after error: text %q sending %v\n%s", string(d.text), d.sending, env.dialogText())
	}
	env.gh.setErr("AddComment", nil)
	env.send(ctrlS)
	wantCalls(t, env.gh.Calls(), "AddComment PR_1 ab\nc", "AddComment PR_1 ab\nc")
	if env.m.Dialogs() != 0 {
		t.Fatal("dialog open after success")
	}
	if got := env.det.callCount(); got != 2 {
		t.Fatalf("detail calls = %d, want 2", got)
	}
}

func TestTextDialogPasteAndUnsent(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.press("c")
	drive(env.m, func() tea.Cmd { _, c := env.m.Update(tea.PasteMsg{Content: "line1\r\nline2"}); return c }())
	d := env.m.topDialog().(*textDialog)
	if string(d.text) != "line1\nline2" || !d.Unsent() {
		t.Fatalf("text %q unsent %v", string(d.text), d.Unsent())
	}
	// q types a q and does not quit.
	env.press("q")
	if string(d.text) != "line1\nline2q" {
		t.Fatalf("text %q", string(d.text))
	}
}

func TestActionResultAfterDialogClosed(t *testing.T) {
	env := newActionModel(t, actionPR())
	d := &textDialog{}
	drive(env.m, func() tea.Cmd {
		_, c := env.m.Update(actionDoneMsg{origin: d, prID: "PR_1", desc: "comment", err: errors.New("late")})
		return c
	}())
	if msg := env.m.Status().Message(); msg != "comment failed: late" {
		t.Fatalf("message %q", msg)
	}
}

func TestActionNeedsSelectionAndDetail(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.press("k")
	env.press("M")
	if msg := env.m.Status().Message(); msg != "select a PR first" {
		t.Fatalf("message %q", msg)
	}

	// A PR without a loaded detail.
	pr := actionPR()
	env.det.setErr(pr.ID, errors.New("down"))
	env.m.detail.cache = map[string]detailEntry{}
	env.press("j", "M")
	if msg := env.m.Status().Message(); msg != "the PR detail is not loaded yet" {
		t.Fatalf("message %q", msg)
	}
	if env.m.Dialogs() != 0 {
		t.Fatal("merge dialog opened without a detail")
	}
}

func TestRerun(t *testing.T) {
	pr := actionPR()
	pr.Checks = []model.Check{
		{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", WorkflowRunID: 11, Rerunnable: true},
		{Name: "test", Status: "COMPLETED", Conclusion: "TIMED_OUT", WorkflowRunID: 11, Rerunnable: true},
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS", WorkflowRunID: 12, Rerunnable: true},
		{Name: "circleci", Status: "COMPLETED", Conclusion: "FAILURE"},
		{Name: "running", Status: "IN_PROGRESS", WorkflowRunID: 13, Rerunnable: true},
	}
	env := newActionModel(t, pr)
	env.press("f")
	wantCalls(t, env.gh.Calls(), "RerunFailedJobs acme/api 11")
	if msg := env.m.Status().Message(); msg != "rerun failed jobs of 1 run(s) of acme/api#7: done; rerun not supported: circleci" {
		t.Fatalf("message %q", msg)
	}
	env.gh.calls = nil
	env.press("F")
	wantCalls(t, env.gh.Calls(), "RerunWorkflow acme/api 11", "RerunWorkflow acme/api 12", "RerunWorkflow acme/api 13")
}

func TestRerunNotSupported(t *testing.T) {
	pr := actionPR()
	pr.Checks = []model.Check{{Name: "circleci", Status: "COMPLETED", Conclusion: "FAILURE"}}
	env := newActionModel(t, pr)
	env.press("f")
	wantCalls(t, env.gh.Calls())
	if msg := env.m.Status().Message(); msg != "rerun not supported: circleci" {
		t.Fatalf("message %q", msg)
	}

	pr.Checks = []model.Check{{Name: "ok", Status: "COMPLETED", Conclusion: "SUCCESS", WorkflowRunID: 1, Rerunnable: true}}
	env = newActionModel(t, pr)
	env.press("f")
	if msg := env.m.Status().Message(); msg != "no failed checks on acme/api#7" {
		t.Fatalf("message %q", msg)
	}
	pr.Checks = nil
	env = newActionModel(t, pr)
	env.press("F")
	if msg := env.m.Status().Message(); msg != "no checks on acme/api#7" {
		t.Fatalf("message %q", msg)
	}
}

func TestRerunError(t *testing.T) {
	pr := actionPR()
	pr.Checks = []model.Check{{Name: "b", Status: "COMPLETED", Conclusion: "FAILURE", WorkflowRunID: 5, Rerunnable: true}}
	env := newActionModel(t, pr)
	env.gh.setErr("RerunFailedJobs", errors.New("403"))
	env.press("f")
	if msg := env.m.Status().Message(); msg != "rerun failed jobs of 1 run(s) of acme/api#7 failed: 403" {
		t.Fatalf("message %q", msg)
	}
}

func TestItemActionsNeedCurrentItem(t *testing.T) {
	env := newActionModel(t, actionPR())
	for _, k := range []string{"r", "x", "+"} {
		env.press(k)
		if msg := env.m.Status().Message(); !strings.HasPrefix(msg, "select a thread or a comment") {
			t.Fatalf("%s: message %q", k, msg)
		}
	}
}

func TestReplyResolveReactOnThreads(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.press("l", "]", "r")
	if !strings.Contains(env.dialogText(), "Reply to a.go:3") {
		t.Fatalf("reply dialog:\n%s", env.dialogText())
	}
	env.typeText("ok")
	env.send(ctrlS)
	env.press("x")
	env.press("+", "j", "enter")
	env.press("]", "x")
	env.press("+", "1")
	wantCalls(t, env.gh.Calls(),
		"ReplyToThread T1 ok",
		"ResolveThread T1",
		"AddReaction C2 THUMBS_DOWN",
		"UnresolveThread T2",
		"AddReaction C3 THUMBS_UP",
	)
	if env.m.Dialogs() != 0 {
		t.Fatal("dialog open")
	}
}

func TestReactOnCommentAndReview(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.press("l", "]", "]", "]")
	env.press("r")
	if msg := env.m.Status().Message(); !strings.HasPrefix(msg, "r replies to a review thread") {
		t.Fatalf("message %q", msg)
	}
	env.press("x")
	if msg := env.m.Status().Message(); !strings.HasPrefix(msg, "x resolves a review thread") {
		t.Fatalf("message %q", msg)
	}
	env.press("+")
	if !strings.Contains(env.dialogText(), "React to the comment") {
		t.Fatalf("picker:\n%s", env.dialogText())
	}
	env.press("8")
	env.press("]", "+")
	if !strings.Contains(env.dialogText(), "React to the review") {
		t.Fatalf("picker:\n%s", env.dialogText())
	}
	env.press("k", "j", "j", "enter")
	wantCalls(t, env.gh.Calls(), "AddReaction IC1 EYES", "AddReaction RV1 LAUGH")
}

func TestReactionErrorKeepsPicker(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.gh.setErr("AddReaction", errors.New("nope"))
	env.press("l", "]", "+", "2")
	if env.m.Dialogs() != 1 || !strings.Contains(env.dialogText(), "Error: nope") {
		t.Fatalf("picker after error:\n%s", env.dialogText())
	}
}

func TestOpenBrowser(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.press("o")
	if len(env.runner.calls) != 1 || !slices.Equal(env.runner.calls[0], []string{"open-url", "https://github.com/acme/api/pull/7"}) {
		t.Fatalf("runner calls %q", env.runner.calls)
	}
	if got := env.det.callCount(); got != 1 {
		t.Fatalf("o refreshed the detail: %d calls", got)
	}
	env.runner.code, env.runner.stderr = 3, "no browser\n"
	env.press("o")
	if msg := env.m.Status().Message(); msg != "open acme/api#7 failed: open-url exited with code 3: no browser" {
		t.Fatalf("message %q", msg)
	}
	env.runner.code, env.runner.err = 0, errors.New("not found")
	env.press("o")
	if msg := env.m.Status().Message(); msg != "open acme/api#7 failed: not found" {
		t.Fatalf("message %q", msg)
	}
	wantCalls(t, env.gh.Calls())
}

func TestActionsWithoutGitHub(t *testing.T) {
	env := &actionEnv{m: New(Options{Styles: testStyles(t), Me: "me", Now: fixedNow})}
	env.m.list.setSnapshot(detailSnapshot(actionPR()))
	env.press("j", "A")
	if msg := env.m.Status().Message(); msg != "GitHub actions are not available" {
		t.Fatalf("message %q", msg)
	}
	env.press("o")
	if msg := env.m.Status().Message(); msg != "cannot open a browser" {
		t.Fatalf("message %q", msg)
	}
}

func TestOpenCommandDefault(t *testing.T) {
	if got := openCommand(); got != "open" && got != "xdg-open" {
		t.Fatalf("open command %q", got)
	}
}
