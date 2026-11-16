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
	subs       []review.Submission
	block      chan struct{}
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
	return review.Session{PR: pr, HeadSHA: pr.HeadSHA, MergeBase: "mb1", AnnotationFile: "/state/a.md"}, nil
}

func (f *fakeReviewer) Open(_ context.Context, s review.Session) (review.Result, error) {
	f.record("Open " + s.PR.ID + " " + s.MergeBase)
	if f.block != nil {
		<-f.block
	}
	return f.result, f.openErr
}

func (f *fakeReviewer) Submit(_ context.Context, s review.Session, sub review.Submission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Submit "+s.PR.ID+" "+s.HeadSHA)
	f.subs = append(f.subs, sub)
	return f.submitErr
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
	if msg := env.m.Status().Message(); msg != "revdiff acme/api#7: no annotations" {
		t.Fatalf("message %q", msg)
	}
	if env.m.Dialogs() != 0 || env.m.reviewing["PR_1"] {
		t.Fatalf("dialogs %d, reviewing %v", env.m.Dialogs(), env.m.reviewing)
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
