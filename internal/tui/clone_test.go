package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/tdragon/argus/internal/review"
)

func (r *fakeRunner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

var missingAPI = &review.MissingClone{Repo: "acme/api", Dir: "/src/acme/api"}

// newCloneProgram starts a teatest program whose Cloner is a
// review.Worktrees with a fake runner, and opens the clone prompt.
func newCloneProgram(t *testing.T, r *fakeRunner) *teatest.TestModel {
	t.Helper()
	w := &review.Worktrees{Runner: r}
	tm := teatest.NewTestModel(t, New(Options{
		Cloner: w, Styles: testStyles(t), Me: "me", Now: fixedNow,
	}), teatest.WithInitialTermSize(160, 50))
	tm.Send(CloneNeededMsg{Missing: missingAPI})
	waitForText(t, tm, "No local clone of acme/api.", "[y/enter] clone")
	return tm
}

func TestClonePromptConfirm(t *testing.T) {
	r := &fakeRunner{}
	tm := newCloneProgram(t, r)
	tm.Send(key("y"))
	waitForText(t, tm, "clone acme/api: done")
	tm.Send(key("q"))
	m := finalModel(t, tm)

	got := r.Calls()
	want := [][]string{{"git", "clone", "git@github.com:acme/api.git", "/src/acme/api"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if m.Dialogs() != 0 {
		t.Fatalf("%d dialogs open after the clone", m.Dialogs())
	}
}

func TestClonePromptCancel(t *testing.T) {
	for _, k := range []string{"n", "esc"} {
		t.Run(k, func(t *testing.T) {
			r := &fakeRunner{}
			tm := newCloneProgram(t, r)
			tm.Send(key(k))
			tm.Send(key("q"))
			finalModel(t, tm)
			if calls := r.Calls(); len(calls) != 0 {
				t.Fatalf("cancel ran %q", calls)
			}
		})
	}
}

func TestClonePromptCancelCloses(t *testing.T) {
	for _, k := range []string{"n", "esc"} {
		t.Run(k, func(t *testing.T) {
			env := newActionModel(t, actionPR())
			env.m.cloner = &review.Worktrees{Runner: env.runner}
			_, cmd := env.m.Update(CloneNeededMsg{Missing: missingAPI})
			drive(env.m, cmd)
			env.press("x", k)
			if env.m.Dialogs() != 0 {
				t.Fatalf("%d dialogs open after %s", env.m.Dialogs(), k)
			}
			if calls := env.runner.Calls(); len(calls) != 0 {
				t.Fatalf("cancel ran %q", calls)
			}
		})
	}
}

func TestClonePromptError(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.runner.code, env.runner.stderr = 1, "repository not found"
	env.m.cloner = &review.Worktrees{Runner: env.runner}
	_, cmd := env.m.Update(CloneNeededMsg{Missing: missingAPI})
	drive(env.m, cmd)
	env.press("enter")
	got := env.dialogText()
	if !strings.Contains(got, "Error: git clone git@github.com:acme/api.git /src/acme/api") || !strings.Contains(got, "code 1") || !strings.Contains(got, "repository not found") {
		t.Fatalf("dialog does not show the error:\n%s", got)
	}
	if !strings.Contains(got, "[y/enter] clone") {
		t.Fatalf("dialog does not offer a retry:\n%s", got)
	}
}

func TestClonePromptNoCloner(t *testing.T) {
	m := New(Options{Styles: testStyles(t)})
	_, cmd := m.Update(CloneNeededMsg{Missing: missingAPI})
	drive(m, cmd)
	if m.Dialogs() != 0 || !strings.Contains(m.Status().Message(), "no clone of acme/api") {
		t.Fatalf("dialogs %d, message %q", m.Dialogs(), m.Status().Message())
	}
}
