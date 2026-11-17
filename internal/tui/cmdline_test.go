package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/asphodel-labs/argus/internal/model"
)

// openPR returns actionPR with the OPEN state.
func openPR() model.PR {
	pr := actionPR()
	pr.State = "OPEN"
	return pr
}

// draftPR returns openPR as a draft.
func draftPR() model.PR {
	pr := openPR()
	pr.IsDraft = true
	return pr
}

// runLine opens the command line, types text, and presses enter.
func (e *actionEnv) runLine(text string) {
	e.press(":")
	e.typeText(text)
	e.press("enter")
}

// lastLine returns the last line of the model view without styles.
func lastLine(m *Model) string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	return lines[len(lines)-1]
}

func TestCommandLineBuiltins(t *testing.T) {
	for _, c := range []struct {
		name string
		pr   model.PR
		call string
		msg  string
	}{
		{"draft", openPR(), "ConvertToDraft PR_1", "convert acme/api#7 to draft: done"},
		{"ready", draftPR(), "MarkReady PR_1", "mark acme/api#7 ready: done"},
		{"close", openPR(), "ClosePR PR_1", "close acme/api#7: done"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newActionModel(t, c.pr)
			loads := env.det.callCount()
			env.runLine(c.name)
			if calls := env.gh.Calls(); !slices.Equal(calls, []string{c.call}) {
				t.Fatalf("calls %q", calls)
			}
			if msg := env.m.Status().Message(); msg != c.msg {
				t.Fatalf("message %q", msg)
			}
			if env.m.Dialogs() != 0 {
				t.Fatalf("dialogs %d", env.m.Dialogs())
			}
			if env.det.callCount() != loads+1 {
				t.Fatalf("detail loads %d, want %d", env.det.callCount(), loads+1)
			}
		})
	}
}

func TestCommandLineStateChecks(t *testing.T) {
	merged := openPR()
	merged.State = "MERGED"
	for _, c := range []struct {
		name string
		pr   model.PR
		msg  string
	}{
		{"draft", draftPR(), "already a draft"},
		{"ready", openPR(), "already ready"},
		{"close", merged, "the PR is not open"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newActionModel(t, c.pr)
			env.runLine(c.name)
			if calls := env.gh.Calls(); len(calls) != 0 {
				t.Fatalf("calls %q", calls)
			}
			if msg := env.m.Status().Message(); msg != c.msg {
				t.Fatalf("message %q", msg)
			}
		})
	}
}

// TestCommandLineReadsLoadedDetail checks that the state checks read the
// refreshed detail and not the summary of the list. The summary stays a
// ready PR until the next poll.
func TestCommandLineReadsLoadedDetail(t *testing.T) {
	env := newActionModel(t, openPR())
	env.det.mu.Lock()
	env.det.prs["PR_1"] = draftPR()
	env.det.mu.Unlock()
	env.runLine("draft")
	if env.m.detail.sel.IsDraft {
		t.Fatal("the summary changed before the next poll")
	}
	env.runLine("ready")
	want := []string{"ConvertToDraft PR_1", "MarkReady PR_1"}
	if calls := env.gh.Calls(); !slices.Equal(calls, want) {
		t.Fatalf("calls %q", calls)
	}
	if msg := env.m.Status().Message(); msg != "mark acme/api#7 ready: done" {
		t.Fatalf("message %q", msg)
	}
}

func TestCommandLineGitHubError(t *testing.T) {
	env := newActionModel(t, openPR())
	env.gh.setErr("ClosePR", errors.New("not permitted\nmore text"))
	env.runLine("close")
	if msg := env.m.Status().Message(); msg != "close acme/api#7 failed: not permitted" {
		t.Fatalf("message %q", msg)
	}
}

func TestCommandLineUnknownName(t *testing.T) {
	env := newActionModel(t, openPR())
	env.runLine("merge now")
	if msg := env.m.Status().Message(); msg != "unknown command: merge now" {
		t.Fatalf("message %q", msg)
	}
	if env.m.Dialogs() != 0 || len(env.gh.Calls()) != 0 {
		t.Fatalf("dialogs %d, calls %q", env.m.Dialogs(), env.gh.Calls())
	}
}

func TestCommandLineCompletion(t *testing.T) {
	env := newActionModel(t, draftPR())
	env.press(":")
	if line := lastLine(env.m); !strings.HasPrefix(line, ":█   draft ready close") {
		t.Fatalf("last line %q", line)
	}
	env.typeText("r")
	env.press("tab")
	if line := lastLine(env.m); !strings.HasPrefix(line, ":ready█   ready") {
		t.Fatalf("last line %q", line)
	}
	env.press("enter")
	if calls := env.gh.Calls(); !slices.Equal(calls, []string{"MarkReady PR_1"}) {
		t.Fatalf("calls %q", calls)
	}
}

func TestCommandLineCommonPrefix(t *testing.T) {
	c := &commandLine{names: []string{"stack-sync", "stack-push", "close"}}
	c.text = []rune("s")
	c.complete()
	if got := string(c.text); got != "stack-" {
		t.Fatalf("text %q", got)
	}
	c.text = []rune("x")
	c.complete()
	if got := string(c.text); got != "x" {
		t.Fatalf("text %q", got)
	}
}

func TestCommandLineEsc(t *testing.T) {
	env := newActionModel(t, openPR())
	env.press(":")
	env.typeText("close")
	env.press("esc")
	if env.m.Dialogs() != 0 || len(env.gh.Calls()) != 0 {
		t.Fatalf("dialogs %d, calls %q", env.m.Dialogs(), env.gh.Calls())
	}
	if line := lastLine(env.m); strings.HasPrefix(line, ":") {
		t.Fatalf("last line %q", line)
	}
}

func TestCommandLineFromListPane(t *testing.T) {
	env := newActionModel(t, openPR())
	env.press("h")
	if env.m.Focus() != FocusList {
		t.Fatal("focus is not on the list")
	}
	env.runLine("close")
	if calls := env.gh.Calls(); !slices.Equal(calls, []string{"ClosePR PR_1"}) {
		t.Fatalf("calls %q", calls)
	}
}

func TestCommandLineNeedsOpenedPR(t *testing.T) {
	env := &actionEnv{gh: &fakeGitHub{}}
	env.m = mustNew(t, Options{GitHub: env.gh, Styles: testStyles(t), Me: "me", Now: fixedNow})
	env.m.list.setSnapshot(detailSnapshot(openPR()))
	env.runLine("close")
	if msg := env.m.Status().Message(); msg != "open a PR with enter" {
		t.Fatalf("message %q", msg)
	}
	if len(env.gh.Calls()) != 0 {
		t.Fatalf("calls %q", env.gh.Calls())
	}
}

func TestCommandLineNeedsLoadedDetail(t *testing.T) {
	env := newActionModel(t, openPR())
	delete(env.m.detail.cache, "PR_1")
	env.runLine("draft")
	if msg := env.m.Status().Message(); msg != "the PR detail is not loaded yet" {
		t.Fatalf("message %q", msg)
	}
	if len(env.gh.Calls()) != 0 {
		t.Fatalf("calls %q", env.gh.Calls())
	}
}
