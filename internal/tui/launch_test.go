package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/asphodel-labs/argus/internal/launch"
	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/review"
)

// fakeLauncher records the PR of each launch.
type fakeLauncher struct {
	mu  sync.Mutex
	prs []model.PR
	wt  review.Worktree
	err error
}

func (f *fakeLauncher) Launch(_ context.Context, pr model.PR) (review.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs = append(f.prs, pr)
	return f.wt, f.err
}

func TestClaudeKeyLaunchesOpenedPR(t *testing.T) {
	l := &fakeLauncher{}
	env := newActionModel(t, reviewPR())
	env.m.launcher = l
	env.press("p")
	if len(l.prs) != 1 || l.prs[0].ID != "PR_1" || l.prs[0].BaseRef != "main" {
		t.Fatalf("launches %+v", l.prs)
	}
	if msg := env.m.Status().Message(); msg != "claude acme/api#7: session started" {
		t.Fatalf("message %q", msg)
	}
}

func TestClaudeKeyShowsExcludeWarning(t *testing.T) {
	l := &fakeLauncher{wt: review.Worktree{ExcludeErr: errors.New("add .worktrees/ to info/exclude: permission denied")}}
	env := newActionModel(t, reviewPR())
	env.m.launcher = l
	env.press("p")
	want := "claude acme/api#7: session started; warning: add .worktrees/ to info/exclude: permission denied"
	if msg := env.m.Status().Message(); msg != want {
		t.Fatalf("message %q, want %q", msg, want)
	}
}

// TestClaudeKeyShowsDirtyWarning checks that a dirty worktree shows a
// warning with the info/exclude error after the launch.
func TestClaudeKeyShowsDirtyWarning(t *testing.T) {
	l := &fakeLauncher{wt: review.Worktree{Dirty: true, ExcludeErr: errors.New("permission denied")}}
	env := newActionModel(t, reviewPR())
	env.m.launcher = l
	env.press("p")
	want := "claude acme/api#7: session started; warning: the worktree has local changes, so argus did not move it to the PR head" +
		"; warning: permission denied"
	if msg := env.m.Status().Message(); msg != want {
		t.Fatalf("message %q, want %q", msg, want)
	}
}

func TestClaudeKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"no agterm", launch.ErrNoAgterm, "Claude launch requires agterm"},
		{"launch error", errors.New("agtermctl session new exited with code 1: no socket\nmore"),
			"claude acme/api#7 failed: agtermctl session new exited with code 1: no socket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newActionModel(t, reviewPR())
			env.m.launcher = &fakeLauncher{err: tt.err}
			env.press("p")
			if msg := env.m.Status().Message(); msg != tt.want {
				t.Fatalf("message %q, want %q", msg, tt.want)
			}
		})
	}
}

func TestClaudeKeyMissingCloneOpensClonePrompt(t *testing.T) {
	env := newActionModel(t, reviewPR())
	env.m.launcher = &fakeLauncher{err: &review.MissingClone{Repo: "acme/api", Dir: "/src/acme/api"}}
	env.m.cloner = &review.Worktrees{Runner: env.runner}
	env.press("p")
	if got := env.dialogText(); !strings.Contains(got, "No local clone of acme/api.") {
		t.Fatalf("no clone prompt:\n%s", got)
	}
}

func TestClaudeKeyWithoutLauncherOrDetail(t *testing.T) {
	env := newActionModel(t, reviewPR())
	env.press("p")
	if msg := env.m.Status().Message(); msg != "Claude launch is not available" {
		t.Fatalf("message %q", msg)
	}

	l := &fakeLauncher{}
	env = newActionModel(t, reviewPR())
	env.m.launcher = l
	delete(env.m.detail.cache, "PR_1")
	env.press("p")
	if msg := env.m.Status().Message(); msg != "the PR detail is not loaded yet" {
		t.Fatalf("message %q", msg)
	}
	if len(l.prs) != 0 {
		t.Fatalf("launches %+v", l.prs)
	}
}

// TestClaudeKeyRunsSetupThenLaunch drives p with the real launch.Claude
// and review.Worktrees on a fake runner: the worktree setup runs first,
// then agtermctl starts Claude with the worktree as --cwd.
func TestClaudeKeyRunsSetupThenLaunch(t *testing.T) {
	env := newActionModel(t, reviewPR())
	root := t.TempDir()
	clone := filepath.Join(root, "acme", "api")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env.m.launcher = &launch.Claude{
		Worktrees: &review.Worktrees{Runner: env.runner, RepoRoot: root},
		Runner:    env.runner,
		SessionID: "s1",
		Bin:       "/bin/claude",
		Getenv:    func(string) string { return "" },
	}
	env.press("p")

	wtDir := filepath.Join(clone, ".worktrees", "argus-pr-7")
	want := [][]string{
		{"git", "-C", clone, "fetch", "origin", "refs/pull/7/head", "+refs/heads/main:refs/remotes/origin/main"},
		{"git", "-C", clone, "rev-parse", "--git-path", "info/exclude"},
		{"git", "-C", clone, "worktree", "add", "--detach", wtDir, "abc123"},
		{"agtermctl", "session", "new", "--workspace-name", "reviews", "--create-workspace",
			"--cwd", wtDir, "--name", "api#7",
			"--command", `'/bin/zsh' -lc ''\''/bin/claude'\'' '\''/pr https://github.com/acme/api/pull/7'\'''`},
	}
	env.runner.mu.Lock()
	got := env.runner.calls
	env.runner.mu.Unlock()
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("calls:\n got %q\nwant %q", got, want)
	}
	// The fake runner gives no info/exclude path. The status bar shows the
	// warning, and the launch goes on.
	msg := env.m.Status().Message()
	if wantMsg := "claude acme/api#7: session started; warning: add .worktrees/ to info/exclude: git rev-parse --git-path info/exclude returned no path"; msg != wantMsg {
		t.Fatalf("message %q, want %q", msg, wantMsg)
	}
}
