package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"text/template"

	"github.com/asphodel-labs/argus/internal/config"
	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/review"
)

// fakeRunner records the command and returns set output. It answers
// "git worktree list" with worktrees, and records those arguments in git.
type fakeRunner struct {
	name      string
	args      []string
	stdout    string
	stderr    string
	code      int
	err       error
	worktrees string
	git       []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	if name == "git" {
		f.git = args
		return f.worktrees, "", 0, nil
	}
	f.name, f.args = name, args
	return f.stdout, f.stderr, f.code, f.err
}

// fakeClones returns dir, or a *review.MissingClone error when dir is empty.
type fakeClones struct{ dir string }

func (f fakeClones) FindClone(repo model.Repo) (string, error) {
	if f.dir == "" {
		return "", &review.MissingClone{Repo: repo.FullName(), Dir: "/src/" + repo.FullName()}
	}
	return f.dir, nil
}

func command(t *testing.T, mode, run string) config.Command {
	t.Helper()
	return config.Command{Name: "sync", Mode: mode, Run: template.Must(template.New("sync").Parse(run))}
}

var testPR = model.PR{Repo: model.Repo{Owner: "acme", Name: "api"}, Number: 7, HeadRef: "feat/x"}

func TestRunBackground(t *testing.T) {
	f := &fakeRunner{}
	r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/my api"}}
	got, err := r.Run(context.Background(), command(t, config.ModeBackground, "gh stack sync {{.Branch}}"), testPR)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "sync: done" {
		t.Errorf("status = %q, want %q", got, "sync: done")
	}
	want := []string{"-c", "cd '/src/acme/my api' && gh stack sync 'feat/x'"}
	if f.name != "sh" || !reflect.DeepEqual(f.args, want) {
		t.Errorf("ran %s %q, want sh %q", f.name, f.args, want)
	}
}

func TestRunOverlay(t *testing.T) {
	t.Setenv("PATH", "/opt/homebrew/bin:/usr/bin")
	f := &fakeRunner{}
	r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/api"}, SessionID: "s1"}
	if _, err := r.Run(context.Background(), command(t, config.ModeOverlay, "gh stack sync"), testPR); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"session", "overlay", "open", "--block", "--target", "s1", "--cwd", "/src/acme/api",
		"PATH='/opt/homebrew/bin:/usr/bin'; export PATH; cd '/src/acme/api' && gh stack sync\n" +
			"rc=$?\nprintf '\\n%s: exit %d. Press enter to close.' 'sync' \"$rc\"\nread _\nexit \"$rc\""}
	if f.name != "agtermctl" || !reflect.DeepEqual(f.args, want) {
		t.Errorf("ran %s %q, want agtermctl %q", f.name, f.args, want)
	}
}

func TestRunOverlayCustomAgtermctl(t *testing.T) {
	f := &fakeRunner{}
	r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/api"}, SessionID: "s1", Agtermctl: "/bin/agtermctl"}
	if _, err := r.Run(context.Background(), command(t, config.ModeOverlay, "true"), testPR); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.name != "/bin/agtermctl" {
		t.Errorf("name = %q, want /bin/agtermctl", f.name)
	}
}

func TestRunFailText(t *testing.T) {
	tests := []struct {
		name           string
		stdout, stderr string
		want           string
	}{
		{"stderr", "out line\n", "first\nlast error\n\n", "last error"},
		{"stdout only", "one\ntwo\n", "", "two"},
		{"blank stderr", "only out\n", "  \n", "only out"},
		{"no output", "", "", "exit code 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRunner{stdout: tt.stdout, stderr: tt.stderr, code: 3}
			r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/api"}}
			_, err := r.Run(context.Background(), command(t, config.ModeBackground, "false"), testPR)
			if err == nil || err.Error() != tt.want {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunStartError(t *testing.T) {
	f := &fakeRunner{err: errors.New("no sh"), code: -1}
	r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/api"}}
	_, err := r.Run(context.Background(), command(t, config.ModeBackground, "true"), testPR)
	if err == nil || err.Error() != "run sh: no sh" {
		t.Errorf("err = %v, want %q", err, "run sh: no sh")
	}
}

func TestRunMissingClone(t *testing.T) {
	f := &fakeRunner{}
	r := &Runner{Runner: f, Clones: fakeClones{}}
	_, err := r.Run(context.Background(), command(t, config.ModeBackground, "true"), testPR)
	var missing *review.MissingClone
	if !errors.As(err, &missing) || missing.Repo != "acme/api" {
		t.Fatalf("err = %v, want a *review.MissingClone for acme/api", err)
	}
	if f.name != "" {
		t.Errorf("ran %s, want no command", f.name)
	}
}

func TestRunOverlayNoAgterm(t *testing.T) {
	f := &fakeRunner{}
	r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/api"}}
	_, err := r.Run(context.Background(), command(t, config.ModeOverlay, "true"), testPR)
	if !errors.Is(err, ErrNoAgterm) {
		t.Fatalf("err = %v, want ErrNoAgterm", err)
	}
	if err.Error() != "overlay commands require agterm" {
		t.Errorf("err text = %q", err.Error())
	}
	if f.name != "" {
		t.Errorf("ran %s, want no command", f.name)
	}
}

func TestRunInBranchWorktree(t *testing.T) {
	f := &fakeRunner{worktrees: "worktree /src/acme/api\nHEAD 1111\nbranch refs/heads/main\n\n" +
		"worktree /src/acme/api/.worktrees/argus-pr-7\nHEAD 2222\ndetached\n\n" +
		"worktree /work/feat x\nHEAD 3333\nbranch refs/heads/feat/x\n\n"}
	r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/api"}}
	if _, err := r.Run(context.Background(), command(t, config.ModeBackground, "gh stack sync"), testPR); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"-C", "/src/acme/api", "worktree", "list", "--porcelain"}; !reflect.DeepEqual(f.git, want) {
		t.Errorf("git %q, want %q", f.git, want)
	}
	want := []string{"-c", "cd '/work/feat x' && gh stack sync"}
	if !reflect.DeepEqual(f.args, want) {
		t.Errorf("ran sh %q, want %q", f.args, want)
	}
}

func TestRunBranchPrefixNotMatched(t *testing.T) {
	f := &fakeRunner{worktrees: "worktree /src/acme/api\nbranch refs/heads/main\n\n" +
		"worktree /work/other\nbranch refs/heads/feat/x2\n\n"}
	r := &Runner{Runner: f, Clones: fakeClones{dir: "/src/acme/api"}}
	if _, err := r.Run(context.Background(), command(t, config.ModeBackground, "true"), testPR); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"-c", "cd '/src/acme/api' && true"}; !reflect.DeepEqual(f.args, want) {
		t.Errorf("ran sh %q, want %q", f.args, want)
	}
}

// gitFailRunner fails the git call.
type gitFailRunner struct{}

func (gitFailRunner) Run(context.Context, string, ...string) (string, string, int, error) {
	return "", "fatal: not a git repository\n", 128, nil
}

func TestRunWorktreeListError(t *testing.T) {
	r := &Runner{Runner: gitFailRunner{}, Clones: fakeClones{dir: "/src/acme/api"}}
	_, err := r.Run(context.Background(), command(t, config.ModeBackground, "true"), testPR)
	if err == nil || err.Error() != "git worktree list: fatal: not a git repository" {
		t.Errorf("err = %v", err)
	}
}
