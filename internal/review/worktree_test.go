package review

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
)

// reply is the result of one fake command.
type reply struct {
	stdout, stderr string
	code           int
	err            error
}

// fakeRunner records each command. It returns the reply for the command
// line, or an empty success.
type fakeRunner struct {
	calls   []string
	replies map[string]reply
	// hook runs before the reply of each command, if set.
	hook func(name string, args []string)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if f.hook != nil {
		f.hook(name, args)
	}
	r := f.replies[line]
	return r.stdout, r.stderr, r.code, r.err
}

func (f *fakeRunner) set(line string, r reply) {
	if f.replies == nil {
		f.replies = map[string]reply{}
	}
	f.replies[line] = r
}

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("calls:\n got %q\nwant %q", got, want)
	}
}

// makeClone makes dir with a .git directory.
func makeClone(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

var apiRepo = model.Repo{Owner: "acme", Name: "api"}

func reviewPR() model.PR {
	return model.PR{Repo: apiRepo, Number: 7, HeadSHA: "abc123", BaseRef: "main"}
}

func TestFindClone(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(t.TempDir(), "work-api")
	rootClone := filepath.Join(root, "acme", "api")

	tests := []struct {
		name     string
		repos    map[string]string
		clones   []string
		wantDir  string
		wantMiss bool
	}{
		{"override clone", map[string]string{"acme/api": override}, []string{override, rootClone}, override, false},
		{"override in other case", map[string]string{"ACME/API": override}, []string{override}, override, false},
		{"override missing, root clone", map[string]string{"acme/api": override}, []string{rootClone}, rootClone, false},
		{"no clone, override target", map[string]string{"acme/api": override}, nil, override, true},
		{"no clone, root target", nil, nil, rootClone, true},
		{"root clone", nil, []string{rootClone}, rootClone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.RemoveAll(override)
			os.RemoveAll(rootClone)
			for _, c := range tt.clones {
				makeClone(t, c)
			}
			w := &Worktrees{Runner: &fakeRunner{}, RepoRoot: root, Repos: tt.repos}
			dir, err := w.FindClone(apiRepo)
			var miss *MissingClone
			if tt.wantMiss {
				if !errors.As(err, &miss) {
					t.Fatalf("err = %v, want *MissingClone", err)
				}
				if miss.Dir != tt.wantDir || miss.Repo != "acme/api" {
					t.Fatalf("missing clone = %+v, want dir %s", miss, tt.wantDir)
				}
				return
			}
			if err != nil || dir != tt.wantDir {
				t.Fatalf("FindClone = %q, %v; want %q", dir, err, tt.wantDir)
			}
		})
	}
}

func TestClone(t *testing.T) {
	r := &fakeRunner{}
	w := &Worktrees{Runner: r}
	if err := w.Clone(context.Background(), &MissingClone{Repo: "acme/api", Dir: "/src/acme/api"}); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, r.calls, "gh repo clone acme/api /src/acme/api")

	r.set("gh repo clone acme/api /src/acme/api", reply{code: 1, stderr: "denied\n"})
	err := w.Clone(context.Background(), &MissingClone{Repo: "acme/api", Dir: "/src/acme/api"})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want the stderr text", err)
	}
}

func TestSetupMissingClone(t *testing.T) {
	r := &fakeRunner{}
	w := &Worktrees{Runner: r, RepoRoot: t.TempDir()}
	_, err := w.Setup(context.Background(), reviewPR())
	var miss *MissingClone
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v, want *MissingClone", err)
	}
	wantCalls(t, r.calls)
}

func TestSetupNeedsHeadAndBase(t *testing.T) {
	w := &Worktrees{Runner: &fakeRunner{}}
	pr := reviewPR()
	pr.BaseRef = ""
	if _, err := w.Setup(context.Background(), pr); err == nil {
		t.Fatal("no error for a PR without a base branch")
	}
}

// setupEnv returns a clone under a temporary repo root and its runner.
func setupEnv(t *testing.T) (w *Worktrees, r *fakeRunner, clone, wtDir string) {
	t.Helper()
	root := t.TempDir()
	clone = makeClone(t, filepath.Join(root, "acme", "api"))
	r = &fakeRunner{}
	return &Worktrees{Runner: r, RepoRoot: root}, r, clone, filepath.Join(clone, ".worktrees", "argus-pr-7")
}

func fetchCall(clone string) string {
	return "git -C " + clone + " fetch origin refs/pull/7/head +refs/heads/main:refs/remotes/origin/main"
}

func TestSetupNewWorktree(t *testing.T) {
	w, r, clone, wtDir := setupEnv(t)
	wt, err := w.Setup(context.Background(), reviewPR())
	if err != nil {
		t.Fatal(err)
	}
	if wt != (Worktree{Clone: clone, Dir: wtDir}) {
		t.Fatalf("worktree = %+v", wt)
	}
	wantCalls(t, r.calls,
		fetchCall(clone),
		"git -C "+clone+" worktree add --detach "+wtDir+" abc123",
	)
}

func TestSetupCleanExisting(t *testing.T) {
	w, r, clone, wtDir := setupEnv(t)
	mkdir(t, wtDir)
	r.set("git -C "+clone+" worktree list --porcelain", reply{stdout: "worktree " + clone + "\nHEAD 111\n\nworktree " + wtDir + "\nHEAD 222\ndetached\n"})
	wt, err := w.Setup(context.Background(), reviewPR())
	if err != nil {
		t.Fatal(err)
	}
	if wt.Dirty {
		t.Fatal("clean worktree marked dirty")
	}
	wantCalls(t, r.calls,
		fetchCall(clone),
		"git -C "+clone+" worktree list --porcelain",
		"git -C "+wtDir+" status --porcelain --untracked-files=all",
		"git -C "+wtDir+" checkout --detach abc123",
	)
}

func TestSetupDirtyExisting(t *testing.T) {
	w, r, clone, wtDir := setupEnv(t)
	mkdir(t, wtDir)
	r.set("git -C "+clone+" worktree list --porcelain", reply{stdout: "worktree " + wtDir + "\n"})
	r.set("git -C "+wtDir+" status --porcelain --untracked-files=all", reply{stdout: "?? notes.txt\n"})
	wt, err := w.Setup(context.Background(), reviewPR())
	if err != nil {
		t.Fatal(err)
	}
	if !wt.Dirty || wt.Dir != wtDir {
		t.Fatalf("worktree = %+v, want dirty %s", wt, wtDir)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "checkout") {
			t.Fatalf("dirty worktree moved: %q", r.calls)
		}
	}
}

func TestSetupExistingDirNotWorktree(t *testing.T) {
	w, r, clone, wtDir := setupEnv(t)
	mkdir(t, wtDir)
	r.set("git -C "+clone+" worktree list --porcelain", reply{stdout: "worktree " + clone + "\n"})
	_, err := w.Setup(context.Background(), reviewPR())
	if err == nil || !strings.Contains(err.Error(), "is not a worktree") {
		t.Fatalf("err = %v", err)
	}
	wantCalls(t, r.calls, fetchCall(clone), "git -C "+clone+" worktree list --porcelain")
}

func TestSetupFetchFails(t *testing.T) {
	w, r, clone, _ := setupEnv(t)
	r.set(fetchCall(clone), reply{code: 128, stderr: "fatal: couldn't find remote ref"})
	_, err := w.Setup(context.Background(), reviewPR())
	if err == nil || !strings.Contains(err.Error(), "couldn't find remote ref") {
		t.Fatalf("err = %v", err)
	}
	wantCalls(t, r.calls, fetchCall(clone))
}

func TestMergeBase(t *testing.T) {
	r := &fakeRunner{}
	r.set("git -C /wt merge-base origin/main abc123", reply{stdout: "def456\n"})
	w := &Worktrees{Runner: r}
	got, err := w.MergeBase(context.Background(), "/wt", "main", "abc123")
	if err != nil || got != "def456" {
		t.Fatalf("MergeBase = %q, %v", got, err)
	}

	r.set("git -C /wt merge-base origin/main abc123", reply{code: 1})
	if _, err := w.MergeBase(context.Background(), "/wt", "main", "abc123"); err == nil {
		t.Fatal("no error for exit code 1")
	}
	r.set("git -C /wt merge-base origin/main abc123", reply{err: errors.New("no git")})
	if _, err := w.MergeBase(context.Background(), "/wt", "main", "abc123"); err == nil {
		t.Fatal("no error for a start failure")
	}
	r.set("git -C /wt merge-base origin/main abc123", reply{stdout: "\n"})
	if _, err := w.MergeBase(context.Background(), "/wt", "main", "abc123"); err == nil {
		t.Fatal("no error for empty output")
	}
}

// fakeStates returns fixed PR states and records the refs.
type fakeStates struct {
	states map[gh.PRRef]gh.PRState
	refs   []gh.PRRef
	err    error
}

func (f *fakeStates) PRStates(_ context.Context, refs []gh.PRRef) (map[gh.PRRef]gh.PRState, error) {
	f.refs = append(f.refs, refs...)
	return f.states, f.err
}

func ref(n int) gh.PRRef { return gh.PRRef{Owner: "acme", Repo: "api", Number: n} }

func TestCleanup(t *testing.T) {
	root := t.TempDir()
	clone := makeClone(t, filepath.Join(root, "acme", "api"))
	wts := filepath.Join(clone, ".worktrees")
	mkdir(t, wts)
	merged := filepath.Join(wts, "argus-pr-1")
	closedDirty := filepath.Join(wts, "argus-pr-2")
	open := filepath.Join(wts, "argus-pr-3")
	notFound := filepath.Join(wts, "argus-pr-4")
	closedClean := filepath.Join(wts, "argus-pr-5")
	other := filepath.Join(wts, "feature")
	outside := filepath.Join(root, "elsewhere", "argus-pr-6")
	// A clone with no .worktrees directory gets no git call.
	makeClone(t, filepath.Join(root, "acme", "web"))

	r := &fakeRunner{}
	var list strings.Builder
	for _, d := range []string{clone, merged, closedDirty, open, notFound, closedClean, other, outside} {
		list.WriteString("worktree " + d + "\nHEAD 1\ndetached\n\n")
	}
	r.set("git -C "+clone+" worktree list --porcelain", reply{stdout: list.String()})
	r.set("git -C "+closedDirty+" status --porcelain --untracked-files=all", reply{stdout: " M a.go\n"})

	states := &fakeStates{states: map[gh.PRRef]gh.PRState{
		ref(1): gh.PRStateMerged,
		ref(2): gh.PRStateClosed,
		ref(3): gh.PRStateOpen,
		ref(5): gh.PRStateClosed,
	}}
	w := &Worktrees{Runner: r, RepoRoot: root}
	res, err := w.Cleanup(context.Background(), states)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(states.refs, []gh.PRRef{ref(1), ref(2), ref(3), ref(4), ref(5)}) {
		t.Fatalf("refs = %v", states.refs)
	}
	if !slices.Equal(res.Removed, []string{merged, closedClean}) {
		t.Fatalf("removed = %q", res.Removed)
	}
	if !slices.Equal(res.Dirty, []string{closedDirty}) {
		t.Fatalf("dirty = %q", res.Dirty)
	}
	if len(res.Errs) != 0 {
		t.Fatalf("errs = %v", res.Errs)
	}
	wantCalls(t, r.calls,
		"git -C "+clone+" worktree list --porcelain",
		"git -C "+merged+" status --porcelain --untracked-files=all",
		"git -C "+clone+" worktree remove "+merged,
		"git -C "+closedDirty+" status --porcelain --untracked-files=all",
		"git -C "+closedClean+" status --porcelain --untracked-files=all",
		"git -C "+clone+" worktree remove "+closedClean,
	)
}

func TestCleanupOverrideClone(t *testing.T) {
	root := t.TempDir()
	clone := makeClone(t, filepath.Join(t.TempDir(), "api"))
	mkdir(t, filepath.Join(clone, ".worktrees"))
	wt := filepath.Join(clone, ".worktrees", "argus-pr-9")
	r := &fakeRunner{}
	r.set("git -C "+clone+" worktree list --porcelain", reply{stdout: "worktree " + wt + "\n"})
	states := &fakeStates{states: map[gh.PRRef]gh.PRState{ref(9): gh.PRStateMerged}}
	w := &Worktrees{Runner: r, RepoRoot: root, Repos: map[string]string{"acme/api": clone}}
	res, err := w.Cleanup(context.Background(), states)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Removed, []string{wt}) {
		t.Fatalf("removed = %q, calls %q", res.Removed, r.calls)
	}
}

func TestCleanupErrors(t *testing.T) {
	root := t.TempDir()
	clone := makeClone(t, filepath.Join(root, "acme", "api"))
	wt := filepath.Join(clone, ".worktrees", "argus-pr-1")
	mkdir(t, wt)

	t.Run("no worktrees, no query", func(t *testing.T) {
		r := &fakeRunner{}
		states := &fakeStates{}
		res, err := (&Worktrees{Runner: r, RepoRoot: root}).Cleanup(context.Background(), states)
		if err != nil || len(res.Removed) != 0 || states.refs != nil {
			t.Fatalf("res %+v err %v refs %v", res, err, states.refs)
		}
	})
	t.Run("list fails", func(t *testing.T) {
		r := &fakeRunner{}
		r.set("git -C "+clone+" worktree list --porcelain", reply{code: 128, stderr: "not a git repository"})
		res, err := (&Worktrees{Runner: r, RepoRoot: root}).Cleanup(context.Background(), &fakeStates{})
		if err != nil || len(res.Errs) != 1 {
			t.Fatalf("res %+v err %v", res, err)
		}
	})
	listed := reply{stdout: "worktree " + wt + "\n"}
	t.Run("states fail", func(t *testing.T) {
		r := &fakeRunner{}
		r.set("git -C "+clone+" worktree list --porcelain", listed)
		_, err := (&Worktrees{Runner: r, RepoRoot: root}).Cleanup(context.Background(), &fakeStates{err: errors.New("offline")})
		if err == nil || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("err = %v", err)
		}
		wantCalls(t, r.calls, "git -C "+clone+" worktree list --porcelain")
	})
	merged := &fakeStates{states: map[gh.PRRef]gh.PRState{ref(1): gh.PRStateMerged}}
	t.Run("status fails", func(t *testing.T) {
		r := &fakeRunner{}
		r.set("git -C "+clone+" worktree list --porcelain", listed)
		r.set("git -C "+wt+" status --porcelain --untracked-files=all", reply{code: 128})
		res, err := (&Worktrees{Runner: r, RepoRoot: root}).Cleanup(context.Background(), merged)
		if err != nil || len(res.Errs) != 1 || len(res.Removed) != 0 {
			t.Fatalf("res %+v err %v", res, err)
		}
		for _, c := range r.calls {
			if strings.Contains(c, "remove") {
				t.Fatalf("removed after a status error: %q", r.calls)
			}
		}
	})
	t.Run("remove fails", func(t *testing.T) {
		r := &fakeRunner{}
		r.set("git -C "+clone+" worktree list --porcelain", listed)
		r.set("git -C "+clone+" worktree remove "+wt, reply{code: 128, stderr: "contains modified or untracked files"})
		res, err := (&Worktrees{Runner: r, RepoRoot: root}).Cleanup(context.Background(), merged)
		if err != nil || len(res.Errs) != 1 || len(res.Removed) != 0 {
			t.Fatalf("res %+v err %v", res, err)
		}
	})
}

// gitRun runs real git in a temporary directory of the test.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitExec is a Runner that runs real git with an empty user config. It
// runs only in temporary repositories of the test.
type gitExec struct{ t *testing.T }

func (g gitExec) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	if name != "git" {
		g.t.Fatalf("unexpected command %s %v", name, args)
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), 0, err
}

// TestWorktreeRealGit runs Setup, MergeBase, and Cleanup against real git
// in temporary repositories.
func TestWorktreeRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := t.TempDir()
	origin := mkdir(t, filepath.Join(tmp, "origin"))
	gitRun(t, origin, "init", "-q", "-b", "main")
	gitRun(t, origin, "commit", "-q", "--allow-empty", "-m", "base")
	base := gitRun(t, origin, "rev-parse", "HEAD")
	gitRun(t, origin, "checkout", "-q", "-b", "feature")
	gitRun(t, origin, "commit", "-q", "--allow-empty", "-m", "pr 1")
	head1 := gitRun(t, origin, "rev-parse", "HEAD")
	gitRun(t, origin, "update-ref", "refs/pull/7/head", head1)

	root := filepath.Join(tmp, "src")
	clone := filepath.Join(root, "acme", "api")
	gitRun(t, tmp, "clone", "-q", origin, clone)
	gitRun(t, origin, "checkout", "-q", "main")
	gitRun(t, origin, "commit", "-q", "--allow-empty", "-m", "main moves")

	w := &Worktrees{Runner: gitExec{t}, RepoRoot: root}
	ctx := context.Background()
	pr := reviewPR()
	pr.HeadSHA = head1

	wt, err := w.Setup(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, wt.Dir, "rev-parse", "HEAD"); got != head1 {
		t.Fatalf("worktree HEAD = %s, want %s", got, head1)
	}
	mb, err := w.MergeBase(ctx, wt.Dir, "main", head1)
	if err != nil || mb != base {
		t.Fatalf("MergeBase = %q, %v; want %s", mb, err, base)
	}

	// The PR head moves: the clean worktree follows it.
	gitRun(t, origin, "checkout", "-q", "feature")
	gitRun(t, origin, "commit", "-q", "--allow-empty", "-m", "pr 2")
	head2 := gitRun(t, origin, "rev-parse", "HEAD")
	gitRun(t, origin, "update-ref", "refs/pull/7/head", head2)
	pr.HeadSHA = head2
	if wt, err = w.Setup(ctx, pr); err != nil || wt.Dirty {
		t.Fatalf("Setup = %+v, %v", wt, err)
	}
	if got := gitRun(t, wt.Dir, "rev-parse", "HEAD"); got != head2 {
		t.Fatalf("worktree HEAD = %s, want %s", got, head2)
	}

	// An untracked file makes the worktree dirty: Setup and Cleanup keep it.
	note := filepath.Join(wt.Dir, "notes.txt")
	if err := os.WriteFile(note, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	pr.HeadSHA = head1
	if wt, err = w.Setup(ctx, pr); err != nil || !wt.Dirty {
		t.Fatalf("Setup = %+v, %v; want dirty", wt, err)
	}
	if got := gitRun(t, wt.Dir, "rev-parse", "HEAD"); got != head2 {
		t.Fatalf("dirty worktree moved to %s", got)
	}
	states := &fakeStates{states: map[gh.PRRef]gh.PRState{ref(7): gh.PRStateMerged}}
	res, err := w.Cleanup(ctx, states)
	if err != nil || len(res.Removed) != 0 || len(res.Dirty) != 1 {
		t.Fatalf("Cleanup = %+v, %v", res, err)
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("dirty worktree file is gone: %v", err)
	}

	// A clean worktree of a merged PR goes away.
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	}
	res, err = w.Cleanup(ctx, states)
	if err != nil || len(res.Removed) != 1 || len(res.Errs) != 0 {
		t.Fatalf("Cleanup = %+v, %v", res, err)
	}
	if _, err := os.Stat(wt.Dir); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}
