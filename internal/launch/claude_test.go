package launch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/review"
)

// reply is the result of one fake command.
type reply struct {
	stdout string
	stderr string
	code   int
	err    error
}

// fakeRunner records each command as its argument list. It returns the
// reply for the command name, or an empty success.
type fakeRunner struct {
	calls   [][]string
	replies map[string]reply
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	r := f.replies[name]
	return r.stdout, r.stderr, r.code, r.err
}

func testPR() model.PR {
	return model.PR{
		Repo:    model.Repo{Owner: "acme", Name: "api"},
		Number:  7,
		URL:     "https://github.com/acme/api/pull/7",
		HeadSHA: "abc123",
		BaseRef: "main",
	}
}

// env returns a Getenv that reads vars.
func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// fishEnv is a Getenv with the SHELL of the user.
var fishEnv = env(map[string]string{"SHELL": "/opt/homebrew/bin/fish"})

// setup returns a Claude with a clone under a temporary repo root. One
// fake runner runs git and agtermctl.
func setup(t *testing.T) (c *Claude, r *fakeRunner, clone string) {
	t.Helper()
	root := t.TempDir()
	clone = filepath.Join(root, "acme", "api")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	r = &fakeRunner{}
	return &Claude{
		Worktrees: &review.Worktrees{Runner: r, RepoRoot: root},
		Runner:    r,
		SessionID: "s1",
		Bin:       "/opt/homebrew/bin/claude",
		Getenv:    fishEnv,
	}, r, clone
}

// fishCommand is the --command value for the default test PR with the fish
// login shell and the Homebrew claude.
const fishCommand = `'/opt/homebrew/bin/fish' -lc ''\''/opt/homebrew/bin/claude'\'' '\''/pr https://github.com/acme/api/pull/7'\'''`

func TestCommand(t *testing.T) {
	c := &Claude{Getenv: env(nil)}
	name, args := c.Command(testPR(), review.Worktree{Dir: "/src/acme/api/.worktrees/argus-pr-7"})
	if name != "agtermctl" {
		t.Errorf("name = %q", name)
	}
	want := []string{"session", "new", "--workspace-name", "reviews", "--create-workspace",
		"--cwd", "/src/acme/api/.worktrees/argus-pr-7", "--name", "api#7",
		"--command", `'/bin/zsh' -lc ''\''claude'\'' '\''/pr https://github.com/acme/api/pull/7'\'''`}
	if !slices.Equal(args, want) {
		t.Errorf("empty SHELL args:\n got %q\nwant %q", args, want)
	}

	c = &Claude{Agtermctl: "/bin/agtermctl", Bin: "/opt/homebrew/bin/claude", Getenv: fishEnv}
	name, args = c.Command(testPR(), review.Worktree{Dir: "/w"})
	if name != "/bin/agtermctl" {
		t.Errorf("name = %q", name)
	}
	want = []string{"session", "new", "--workspace-name", "reviews", "--create-workspace",
		"--cwd", "/w", "--name", "api#7", "--command", fishCommand}
	if !slices.Equal(args, want) {
		t.Errorf("fish args:\n got %q\nwant %q", args, want)
	}
}

func TestShell(t *testing.T) {
	if got := (&Claude{Getenv: fishEnv}).Shell(); got != "/opt/homebrew/bin/fish" {
		t.Errorf("Shell = %q", got)
	}
	if got := (&Claude{Getenv: env(nil)}).Shell(); got != DefaultShell {
		t.Errorf("Shell with empty SHELL = %q, want %q", got, DefaultShell)
	}
}

// argsScript is a program that prints each argument on its own line.
const argsScript = "#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done\n"

// TestClaudeCommandQuoting checks the quoting with a real sh. The claude
// path has a space and a single quote, and the URL has shell characters.
func TestClaudeCommandQuoting(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := filepath.Join(t.TempDir(), "My Tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "it's claude")
	if err := os.WriteFile(bin, []byte(argsScript), 0o755); err != nil {
		t.Fatal(err)
	}
	pr := testPR()
	pr.URL = "https://github.com/acme/api/pull/7?x=$HOME&y=`id`'z"
	c := &Claude{Bin: bin, Getenv: fishEnv}
	inner := c.InnerCommand(pr)

	// sh -c runs the inner command as the login shell does: the claude
	// binary gets the prompt as one argument.
	out, err := exec.Command(sh, "-c", inner).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", inner, err)
	}
	if want := "/pr " + pr.URL + "\n"; string(out) != want {
		t.Errorf("sh -c inner:\n got %q\nwant %q", out, want)
	}

	// fish reads the inner command the same way. The test skips fish if it
	// is not installed.
	if fish, err := exec.LookPath("fish"); err == nil {
		out, err := exec.Command(fish, "--no-config", "-c", inner).Output()
		if err != nil {
			t.Fatalf("fish -c %q: %v", inner, err)
		}
		if want := "/pr " + pr.URL + "\n"; string(out) != want {
			t.Errorf("fish -c inner:\n got %q\nwant %q", out, want)
		}
	}

	// The --command value splits into the shell, -lc, and the inner
	// command.
	out, err = exec.Command(sh, "-c", `for a in `+c.ClaudeCommand(pr)+`; do printf '%s\n' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/opt/homebrew/bin/fish\n-lc\n" + inner + "\n"; string(out) != want {
		t.Errorf("command split:\n got %q\nwant %q", out, want)
	}
}

func TestLaunch(t *testing.T) {
	c, r, clone := setup(t)
	r.replies = map[string]reply{"git": {stdout: ".git/info/exclude\n"}}
	wt, err := c.Launch(context.Background(), testPR())
	if err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(clone, ".worktrees", "argus-pr-7")
	if wt.Dir != wtDir || wt.ExcludeErr != nil {
		t.Errorf("worktree = %+v", wt)
	}
	if len(r.calls) != 4 {
		t.Fatalf("calls = %q", r.calls)
	}
	if got := strings.Join(r.calls[0], " "); got != "git -C "+clone+" fetch origin refs/pull/7/head +refs/heads/main:refs/remotes/origin/main" {
		t.Errorf("fetch = %q", got)
	}
	if got := strings.Join(r.calls[1], " "); got != "git -C "+clone+" rev-parse --git-path info/exclude" {
		t.Errorf("exclude path = %q", got)
	}
	if got := strings.Join(r.calls[2], " "); got != "git -C "+clone+" worktree add --detach "+wtDir+" abc123" {
		t.Errorf("worktree add = %q", got)
	}
	want := []string{"agtermctl", "session", "new", "--workspace-name", "reviews", "--create-workspace",
		"--cwd", wtDir, "--name", "api#7", "--command", fishCommand}
	if !slices.Equal(r.calls[3], want) {
		t.Errorf("launch:\n got %q\nwant %q", r.calls[3], want)
	}
}

func TestLaunchRequiresAgterm(t *testing.T) {
	c, r, _ := setup(t)
	c.SessionID = ""
	if _, err := c.Launch(context.Background(), testPR()); !errors.Is(err, ErrNoAgterm) {
		t.Fatalf("err = %v, want ErrNoAgterm", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %q, want none", r.calls)
	}
}

func TestLaunchMissingClone(t *testing.T) {
	c, r, _ := setup(t)
	pr := testPR()
	pr.Repo.Name = "web"
	var missing *review.MissingClone
	if _, err := c.Launch(context.Background(), pr); !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *review.MissingClone", err)
	}
	if missing.Repo != "acme/web" {
		t.Errorf("missing = %+v", missing)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %q, want none", r.calls)
	}
}

func TestLaunchErrors(t *testing.T) {
	tests := []struct {
		name  string
		reply map[string]reply
		want  string
	}{
		{"setup fails", map[string]reply{"git": {code: 128, stderr: "fatal: no remote"}}, "fatal: no remote"},
		{"agtermctl cannot start", map[string]reply{"agtermctl": {code: -1, err: errors.New("not found")}}, "run agtermctl: not found"},
		{"agtermctl exit with stderr", map[string]reply{"agtermctl": {code: 1, stderr: "no socket\n"}}, "agtermctl session new exited with code 1: no socket"},
		{"agtermctl exit", map[string]reply{"agtermctl": {code: 2}}, "agtermctl session new exited with code 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, r, _ := setup(t)
			r.replies = tt.reply
			_, err := c.Launch(context.Background(), testPR())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
