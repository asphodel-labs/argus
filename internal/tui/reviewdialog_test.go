package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
	"github.com/tdragon/argus/internal/review"
)

// fakeReviewer records each call. Open waits on block if it is set.
type fakeReviewer struct {
	mu         sync.Mutex
	calls      []string
	prepareErr error
	result     review.Result
	openErr    error
	submitErr  error
	submitNote string
	subs       []review.Submission
	block      chan struct{}
	excludeErr error
	dirty      bool
	discardErr error
}

func (f *fakeReviewer) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeReviewer) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeReviewer) Prepare(_ context.Context, pr model.PR) (review.Session, error) {
	f.record(fmt.Sprintf("Prepare %s %s %s %q", pr.ID, pr.HeadSHA, pr.BaseRef, pr.Body))
	if f.prepareErr != nil {
		return review.Session{}, f.prepareErr
	}
	return review.Session{PR: pr, Worktree: review.Worktree{ExcludeErr: f.excludeErr, Dirty: f.dirty},
		HeadSHA: pr.HeadSHA, MergeBase: "mb1", AnnotationFile: "/state/a.md"}, nil
}

func (f *fakeReviewer) Open(_ context.Context, s review.Session) (review.Result, error) {
	f.record("Open " + s.PR.ID + " " + s.MergeBase)
	if f.block != nil {
		<-f.block
	}
	return f.result, f.openErr
}

func (f *fakeReviewer) Submit(_ context.Context, s review.Session, sub review.Submission) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Submit "+s.PR.ID+" "+s.HeadSHA)
	f.subs = append(f.subs, sub)
	if f.submitErr != nil {
		return "", f.submitErr
	}
	return f.submitNote, nil
}

func (f *fakeReviewer) Discard(s review.Session) error {
	f.record("Discard " + s.PR.ID + " " + s.AnnotationFile)
	return f.discardErr
}

// reviewPR is actionPR with a body and a base branch.
func reviewPR() model.PR {
	p := actionPR()
	p.Body = "PR body"
	p.BaseRef = "main"
	return p
}

var annotated = review.Result{
	Annotated: true,
	Threads: []gh.ReviewThreadInput{
		{Path: "a.go", Body: "inside", Line: 3, Side: gh.SideRight},
		{Path: "a.go", Body: "range", StartLine: 5, Line: 7, Side: gh.SideRight},
		{Path: "b.go", Body: "file note", File: true},
	},
	Moved: []gh.ReviewThreadInput{{Path: "a.go", Body: "old code", Line: 40, Side: gh.SideLeft}},
}

func newReviewEnv(t *testing.T, pr model.PR, r *fakeReviewer) *actionEnv {
	t.Helper()
	env := newActionModel(t, pr)
	env.m.reviewer = r
	return env
}

func TestReviewKeyRunsRevdiffOnOpenedPR(t *testing.T) {
	r := &fakeReviewer{}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	want := []string{`Prepare PR_1 abc123 main "PR body"`, "Open PR_1 mb1"}
	if got := r.Calls(); !slices.Equal(got, want) {
		t.Fatalf("calls:\n got %q\nwant %q", got, want)
	}
	if env.m.reviewing["PR_1"] {
		t.Fatalf("reviewing %v", env.m.reviewing)
	}
}

// TestCleanRevdiffAsksForApproval checks that revdiff with no annotations
// on the PR of another user opens the approve dialog for the reviewed head.
func TestCleanRevdiffAsksForApproval(t *testing.T) {
	env := newReviewEnv(t, reviewPR(), &fakeReviewer{})
	env.press("v")
	if env.m.Dialogs() != 1 {
		t.Fatalf("dialogs %d, want the approve dialog", env.m.Dialogs())
	}
	got := env.dialogText()
	if !strings.HasPrefix(got, "No comments. Approve acme/api#7?\n") || strings.Contains(got, "head moved") {
		t.Fatalf("dialog:\n%s", got)
	}
	env.typeText("lgtm")
	env.send(ctrlS)
	wantCalls(t, env.gh.Calls(), "Approve PR_1 lgtm abc123")
	if env.m.Dialogs() != 0 {
		t.Fatal("approve dialog is open")
	}
	if msg := env.m.Status().Message(); msg != "approve acme/api#7: done" {
		t.Fatalf("message %q", msg)
	}
}

func TestCleanRevdiffApproveCancel(t *testing.T) {
	env := newReviewEnv(t, reviewPR(), &fakeReviewer{})
	env.press("v")
	env.typeText("x")
	env.press("esc")
	if env.m.Dialogs() != 0 {
		t.Fatalf("dialogs %d after esc", env.m.Dialogs())
	}
	wantCalls(t, env.gh.Calls())
	if msg := env.m.Status().Message(); msg != "revdiff acme/api#7: no annotations; not approved" {
		t.Fatalf("message %q", msg)
	}
}

func TestCleanRevdiffOwnPR(t *testing.T) {
	pr := reviewPR()
	pr.Author.Login = "Me"
	env := newReviewEnv(t, pr, &fakeReviewer{})
	env.press("v")
	if env.m.Dialogs() != 0 {
		t.Fatalf("dialogs %d on an own PR", env.m.Dialogs())
	}
	if msg := env.m.Status().Message(); msg != "revdiff acme/api#7: no annotations" {
		t.Fatalf("message %q", msg)
	}
	wantCalls(t, env.gh.Calls())
}

// TestCleanRevdiffHeadMoved checks that the approve dialog warns about a
// new head and still approves the reviewed head.
func TestCleanRevdiffHeadMoved(t *testing.T) {
	env := newReviewEnv(t, reviewPR(), &fakeReviewer{})
	env.press("v")
	moved := reviewPR()
	moved.HeadSHA = "def4567890"
	env.m.list.setSnapshot(detailSnapshot(moved))
	if got := env.dialogText(); !strings.Contains(got, "⚠ The PR head moved to def4567 after the checkout.") || !strings.Contains(got, "abc123.") {
		t.Fatalf("no head-moved warning:\n%s", got)
	}
	env.send(ctrlS)
	wantCalls(t, env.gh.Calls(), "Approve PR_1  abc123")
}

// TestCleanRevdiffApproveError checks that a GitHub error, for example for
// a pending review, keeps the approve dialog open with its text.
func TestCleanRevdiffApproveError(t *testing.T) {
	env := newReviewEnv(t, reviewPR(), &fakeReviewer{})
	env.gh.setErr("Approve", errors.New("a pending review exists"))
	env.press("v")
	env.typeText("ok")
	env.send(ctrlS)
	if env.m.Dialogs() != 1 {
		t.Fatalf("dialogs %d, want the approve dialog", env.m.Dialogs())
	}
	got := env.dialogText()
	if !strings.Contains(got, "Error: a pending review exists") || !strings.Contains(got, "ok█") {
		t.Fatalf("dialog:\n%s", got)
	}
}

// TestAnnotatedRevdiffOpensReviewDialog checks that exit 10 with
// annotations still opens the review dialog.
func TestAnnotatedRevdiffOpensReviewDialog(t *testing.T) {
	env := newReviewEnv(t, reviewPR(), &fakeReviewer{result: annotated})
	env.press("v")
	if _, ok := env.m.topDialog().(*reviewDialog); !ok || env.m.Dialogs() != 1 {
		t.Fatalf("top dialog %T, want the review dialog", env.m.topDialog())
	}
	wantCalls(t, env.gh.Calls())
}

// TestReviewKeyShowsExcludeWarning checks that an info/exclude error shows
// in the status bar and does not stop the review.
func TestReviewKeyShowsExcludeWarning(t *testing.T) {
	warn := errors.New("add .worktrees/ to info/exclude: permission denied")
	r := &fakeReviewer{excludeErr: warn}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	if msg := env.m.Status().Message(); msg != "revdiff acme/api#7: approve dialog open; warning: "+warn.Error() {
		t.Fatalf("message %q", msg)
	}

	r = &fakeReviewer{excludeErr: warn, result: annotated}
	env = newReviewEnv(t, reviewPR(), r)
	env.press("v")
	if env.m.Dialogs() != 1 {
		t.Fatalf("dialogs %d, want the review dialog", env.m.Dialogs())
	}
	if msg := env.m.Status().Message(); msg != "revdiff acme/api#7: review dialog open; warning: "+warn.Error() {
		t.Fatalf("message %q", msg)
	}
}

// TestReviewKeyShowsDirtyWarning checks that a dirty worktree shows a
// warning in the status bar and does not stop the review.
func TestReviewKeyShowsDirtyWarning(t *testing.T) {
	const warn = "; warning: the worktree has local changes, so argus did not move it to the PR head"
	r := &fakeReviewer{dirty: true}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	if msg := env.m.Status().Message(); msg != "revdiff acme/api#7: approve dialog open"+warn {
		t.Fatalf("message %q", msg)
	}

	r = &fakeReviewer{dirty: true, result: annotated}
	env = newReviewEnv(t, reviewPR(), r)
	env.press("v")
	if env.m.Dialogs() != 1 {
		t.Fatalf("dialogs %d, want the review dialog", env.m.Dialogs())
	}
	if msg := env.m.Status().Message(); msg != "revdiff acme/api#7: review dialog open"+warn {
		t.Fatalf("message %q", msg)
	}
}

func TestReviewKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		edit func(r *fakeReviewer)
		want string
	}{
		{"no agterm", func(r *fakeReviewer) { r.prepareErr = review.ErrNoAgterm }, "revdiff review requires agterm"},
		{"prepare error", func(r *fakeReviewer) { r.prepareErr = errors.New("fetch failed\nmore") }, "revdiff acme/api#7 failed: fetch failed"},
		{"overlay exit code", func(r *fakeReviewer) {
			r.openErr = errors.New("revdiff overlay exited with code 2: boom")
		}, "revdiff acme/api#7 failed: revdiff overlay exited with code 2: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeReviewer{}
			tt.edit(r)
			env := newReviewEnv(t, reviewPR(), r)
			env.press("v")
			if msg := env.m.Status().Message(); msg != tt.want {
				t.Fatalf("message %q, want %q", msg, tt.want)
			}
			if env.m.Dialogs() != 0 || env.m.reviewing["PR_1"] {
				t.Fatalf("dialogs %d, reviewing %v", env.m.Dialogs(), env.m.reviewing)
			}
		})
	}
}

func TestReviewKeyMissingCloneOpensClonePrompt(t *testing.T) {
	r := &fakeReviewer{prepareErr: &review.MissingClone{Repo: "acme/api", Dir: "/src/acme/api"}}
	env := newReviewEnv(t, reviewPR(), r)
	env.m.cloner = &review.Worktrees{Runner: env.runner}
	env.press("v")
	if got := env.dialogText(); !strings.Contains(got, "No local clone of acme/api.") {
		t.Fatalf("no clone prompt:\n%s", got)
	}
}

func TestReviewKeyWithoutReviewerOrDetail(t *testing.T) {
	env := newActionModel(t, reviewPR())
	env.press("v")
	if msg := env.m.Status().Message(); msg != "revdiff review is not available" {
		t.Fatalf("message %q", msg)
	}

	env = newReviewEnv(t, reviewPR(), &fakeReviewer{})
	delete(env.m.detail.cache, "PR_1")
	env.press("v")
	if msg := env.m.Status().Message(); msg != "the PR detail is not loaded yet" {
		t.Fatalf("message %q", msg)
	}
}

func TestReviewKeyDisabledWhileOverlayOpen(t *testing.T) {
	r := &fakeReviewer{block: make(chan struct{})}
	env := newReviewEnv(t, reviewPR(), r)
	_, cmd := env.m.Update(key("v"))
	// The overlay command waits on r.block; run it in the background.
	done := make(chan tea.Msg, 1)
	go func() { done <- runBatch(cmd) }()
	waitUntil(t, func() bool { return len(r.Calls()) == 2 })

	env.press("v")
	if msg := env.m.Status().Message(); msg != "revdiff is already open for acme/api#7" {
		t.Fatalf("message %q", msg)
	}
	close(r.block)
	msg := <-done
	_, cmd = env.m.Update(msg)
	drive(env.m, cmd)
	if env.m.reviewing["PR_1"] {
		t.Fatal("PR still marked as reviewing")
	}
	if n := len(r.Calls()); n != 2 {
		t.Fatalf("calls %q", r.Calls())
	}
}

// runBatch runs the commands of a batch at the same time and returns the
// first revdiffDoneMsg. The status message timer of the batch never
// returns in time, so it is not waited for.
func runBatch(cmd tea.Cmd) tea.Msg {
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return msg
	}
	ch := make(chan tea.Msg, len(batch))
	for _, c := range batch {
		if c != nil {
			go func() { ch <- c() }()
		}
	}
	for m := range ch {
		if done, ok := m.(revdiffDoneMsg); ok {
			return done
		}
	}
	return nil
}

func TestReviewDialogSubmit(t *testing.T) {
	r := &fakeReviewer{result: annotated}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	got := env.dialogText()
	for _, want := range []string{
		"Review acme/api#7: Add retry",
		"Comments (4):",
		"a.go:3  inside",
		"a.go:5-7  range",
		"b.go (file)  file note",
		"a.go:40 (-)  [outside the diff: moves to the body]  old code",
		"Event: [Comment]  Approve  Request changes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dialog does not show %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "head moved") {
		t.Fatalf("head-moved warning without a move:\n%s", got)
	}

	env.press("tab", "tab")
	env.typeText("needs work")
	if !strings.Contains(env.dialogText(), "Event: Comment  Approve  [Request changes]") {
		t.Fatalf("event not selected:\n%s", env.dialogText())
	}
	env.press("shift+tab", "tab")
	env.send(ctrlS)

	if env.m.Dialogs() != 0 {
		t.Fatalf("%d dialogs open after the submit", env.m.Dialogs())
	}
	if msg := env.m.Status().Message(); msg != "review acme/api#7: done" {
		t.Fatalf("message %q", msg)
	}
	want := review.Submission{Event: gh.ReviewRequestChanges, Body: "needs work", Threads: annotated.Threads, Moved: annotated.Moved}
	if len(r.subs) != 1 || !submissionEqual(r.subs[0], want) {
		t.Fatalf("submissions %+v, want %+v", r.subs, want)
	}
	if got := r.Calls(); got[len(got)-1] != "Submit PR_1 abc123" {
		t.Fatalf("calls %q", got)
	}
}

func submissionEqual(a, b review.Submission) bool {
	return a.Event == b.Event && a.Body == b.Body && slices.Equal(a.Threads, b.Threads) && slices.Equal(a.Moved, b.Moved)
}

func TestReviewDialogSubmitErrorKeepsText(t *testing.T) {
	r := &fakeReviewer{result: annotated, submitErr: errors.New("add review thread a.go:3: line not in the diff")}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	env.typeText("hello")
	env.send(ctrlS)
	got := env.dialogText()
	if !strings.Contains(got, "Error: add review thread a.go:3: line not in the diff") || !strings.Contains(got, "hello█") {
		t.Fatalf("dialog after the error:\n%s", got)
	}
	if !env.m.topDialog().Unsent() {
		t.Fatal("dialog text not unsent")
	}

	r.mu.Lock()
	r.submitErr = nil
	r.mu.Unlock()
	env.send(ctrlS)
	if env.m.Dialogs() != 0 || len(r.subs) != 2 {
		t.Fatalf("retry: dialogs %d, submissions %d", env.m.Dialogs(), len(r.subs))
	}
}

func TestReviewDialogOwnPR(t *testing.T) {
	pr := reviewPR()
	pr.Author.Login = "me"
	env := newReviewEnv(t, pr, &fakeReviewer{result: annotated})
	env.press("v")
	if got := env.dialogText(); !strings.Contains(got, "Event: [Comment]\n") {
		t.Fatalf("own PR offers other events:\n%s", got)
	}
	env.press("tab")
	if got := env.dialogText(); !strings.Contains(got, "Event: [Comment]\n") {
		t.Fatalf("tab changed the event on an own PR:\n%s", got)
	}
}

func TestReviewDialogHeadMoved(t *testing.T) {
	env := newReviewEnv(t, reviewPR(), &fakeReviewer{result: annotated})
	env.press("v")
	moved := reviewPR()
	moved.HeadSHA = "def4567890"
	env.m.list.setSnapshot(detailSnapshot(moved))
	got := env.dialogText()
	if !strings.Contains(got, "⚠ The PR head moved to def4567 after the checkout.") || !strings.Contains(got, "abc123.") {
		t.Fatalf("no head-moved warning:\n%s", got)
	}
}

func TestReviewDialogEditAndCancel(t *testing.T) {
	env := newReviewEnv(t, reviewPR(), &fakeReviewer{result: annotated})
	env.press("v")
	env.typeText("ab")
	env.press("enter", "backspace", "backspace")
	_, cmd := env.m.Update(tea.PasteMsg{Content: "x\r\ny"})
	drive(env.m, cmd)
	if got := env.dialogText(); !strings.Contains(got, "ax\ny█") {
		t.Fatalf("body:\n%s", got)
	}
	env.press("esc")
	if env.m.Dialogs() != 0 {
		t.Fatal("esc did not close the dialog")
	}
}

func TestReviewDialogLongList(t *testing.T) {
	var res review.Result
	res.Annotated = true
	for i := 1; i <= 12; i++ {
		res.Threads = append(res.Threads, gh.ReviewThreadInput{Path: "a.go", Line: i, Body: fmt.Sprintf("note %d", i), Side: gh.SideRight})
	}
	d := newReviewDialog(review.Session{PR: reviewPR()}, res, false)
	got := d.View(72)
	if !strings.Contains(got, "a.go:7  note 7") || strings.Contains(got, "note 8") || !strings.Contains(got, "… 5 more") {
		t.Fatalf("list:\n%s", got)
	}
}

// TestReviewDialogTeatest runs the review flow in a teatest program: v,
// the dialog, approve, and the submit.
func TestReviewDialogTeatest(t *testing.T) {
	p := newFakePoller()
	pr := reviewPR()
	det := &fakeDetailer{prs: map[string]model.PR{pr.ID: pr}}
	r := &fakeReviewer{result: annotated}
	tm := teatest.NewTestModel(t, New(Options{
		Poller: p, Detail: det, GitHub: &fakeGitHub{}, Reviewer: r,
		Styles: testStyles(t), Me: "me", Now: fixedNow,
	}), teatest.WithInitialTermSize(160, 50))
	p.ch <- poller.Update{Snapshot: detailSnapshot(pr)}
	waitForText(t, tm, "Needs my review")
	tm.Send(key("j"))
	tm.Send(key("j"))
	tm.Send(key("enter"))
	waitForText(t, tm, "Reviews:")
	tm.Send(key("v"))
	waitForText(t, tm, "Comments (4):", "[tab] event")
	tm.Send(key("tab"))
	waitForText(t, tm, "[Approve]")
	tm.Send(ctrlS)
	// The status bar redraws only the changed cells, so the frame can miss
	// the first letters of the message.
	waitForText(t, tm, "acme/api#7: done")
	tm.Send(key("q"))
	finalModel(t, tm)

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.subs) != 1 || r.subs[0].Event != gh.ReviewApprove {
		t.Fatalf("submissions %+v", r.subs)
	}
}

func TestReviewDialogSubmitNote(t *testing.T) {
	r := &fakeReviewer{result: annotated, submitNote: "; removed 1 outdated draft comments"}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	env.send(ctrlS)
	if msg := env.m.Status().Message(); msg != "review acme/api#7: done; removed 1 outdated draft comments" {
		t.Fatalf("message %q", msg)
	}
}

// TestReviewDialogEscWhileSubmitting checks that esc does not close the
// dialog while the submit runs, so that v cannot start a second revdiff
// run on the same session files.
func TestReviewDialogEscWhileSubmitting(t *testing.T) {
	r := &fakeReviewer{result: annotated}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	_, submit := env.m.Update(ctrlS)
	env.press("esc", "v")
	d, ok := env.m.topDialog().(*reviewDialog)
	if !ok || !d.sending || env.m.Dialogs() != 1 {
		t.Fatalf("dialogs %d, top %T", env.m.Dialogs(), env.m.topDialog())
	}
	if got := r.Calls(); len(got) != 2 {
		t.Fatalf("calls %q, want only the first Prepare and Open", got)
	}
	if !strings.Contains(env.dialogText(), "Submitting…") || string(d.text) != "" {
		t.Fatalf("dialog:\n%s", env.dialogText())
	}

	drive(env.m, submit)
	if env.m.Dialogs() != 0 || env.m.Status().Message() != "review acme/api#7: done" {
		t.Fatalf("dialogs %d, message %q", env.m.Dialogs(), env.m.Status().Message())
	}
}

// TestReviewDialogResultUnderQuitConfirm checks the submit result when the
// quit confirmation is over the review dialog.
func TestReviewDialogResultUnderQuitConfirm(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%v", fail), func(t *testing.T) {
			r := &fakeReviewer{result: annotated}
			if fail {
				r.submitErr = errors.New("boom")
			}
			env := newReviewEnv(t, reviewPR(), r)
			env.press("v")
			env.typeText("body")
			_, submit := env.m.Update(ctrlS)
			env.press("ctrl+c")
			if _, ok := env.m.topDialog().(*confirmQuitDialog); !ok || env.m.Dialogs() != 2 {
				t.Fatalf("dialogs %d, top %T", env.m.Dialogs(), env.m.topDialog())
			}

			drive(env.m, submit)
			if _, ok := env.m.topDialog().(*confirmQuitDialog); !ok {
				t.Fatalf("top %T, want the quit confirmation", env.m.topDialog())
			}
			if !fail {
				if env.m.Dialogs() != 1 || env.m.Status().Message() != "review acme/api#7: done" {
					t.Fatalf("dialogs %d, message %q", env.m.Dialogs(), env.m.Status().Message())
				}
				return
			}
			if env.m.Dialogs() != 2 || env.m.Status().Message() != "review acme/api#7 failed: boom" {
				t.Fatalf("dialogs %d, message %q", env.m.Dialogs(), env.m.Status().Message())
			}
			env.press("n")
			d, ok := env.m.topDialog().(*reviewDialog)
			if !ok || d.sending || !strings.Contains(env.dialogText(), "Error: boom") {
				t.Fatalf("top %T:\n%s", env.m.topDialog(), env.dialogText())
			}
			env.press("esc")
			if env.m.Dialogs() != 0 {
				t.Fatalf("%d dialogs open after esc", env.m.Dialogs())
			}
		})
	}
}

var ctrlX = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}

// TestReviewDialogDiscard checks that ctrl+x discards the session files
// only after y, and that each other key keeps them.
func TestReviewDialogDiscard(t *testing.T) {
	r := &fakeReviewer{result: annotated, submitErr: errors.New("an earlier submit from argus is on GitHub; press ctrl+x to discard")}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	if got := env.dialogText(); !strings.Contains(got, "[ctrl+x] discard") {
		t.Fatalf("help does not show ctrl+x:\n%s", got)
	}
	env.typeText("body")
	env.send(ctrlS)
	if got := env.dialogText(); !strings.Contains(got, "press ctrl+x to") {
		t.Fatalf("dialog after the error:\n%s", got)
	}

	for _, k := range []string{"n", "esc", "x"} {
		env.send(ctrlX)
		if got := env.dialogText(); !strings.Contains(got, "Discard the local annotations and the review record of acme/api#7?") {
			t.Fatalf("no confirmation:\n%s", got)
		}
		env.press(k)
		if env.m.Dialogs() != 1 || slices.ContainsFunc(r.Calls(), func(c string) bool { return strings.HasPrefix(c, "Discard") }) {
			t.Fatalf("key %q: dialogs %d, calls %q", k, env.m.Dialogs(), r.Calls())
		}
		if got := env.dialogText(); strings.Contains(got, "Discard the local") || !strings.Contains(got, "body█") {
			t.Fatalf("key %q: dialog:\n%s", k, got)
		}
	}

	env.send(ctrlX)
	env.press("y")
	if got := r.Calls(); got[len(got)-1] != "Discard PR_1 /state/a.md" {
		t.Fatalf("calls %q", got)
	}
	if env.m.Dialogs() != 0 || env.m.Status().Message() != "discard local annotations of acme/api#7: done" {
		t.Fatalf("dialogs %d, message %q", env.m.Dialogs(), env.m.Status().Message())
	}
}

func TestReviewDialogDiscardError(t *testing.T) {
	r := &fakeReviewer{result: annotated, discardErr: errors.New("remove /state/a.md: permission denied")}
	env := newReviewEnv(t, reviewPR(), r)
	env.press("v")
	_, cmd := env.m.Update(ctrlX)
	drive(env.m, cmd)
	_, discard := env.m.Update(key("y"))
	if got := env.dialogText(); !strings.Contains(got, "Discarding…") {
		t.Fatalf("dialog while the discard runs:\n%s", got)
	}
	drive(env.m, discard)
	d, ok := env.m.topDialog().(*reviewDialog)
	if !ok || d.sending || d.discarding || !strings.Contains(env.dialogText(), "Error: remove /state/a.md: permission denied") {
		t.Fatalf("top %T, dialog:\n%s", env.m.topDialog(), env.dialogText())
	}
}
