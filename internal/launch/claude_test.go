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
	return "", r.stderr, r.code, r.err
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
	}, r, clone
}

func TestCommand(t *testing.T) {
	c := &Claude{}
	name, args := c.Command(testPR(), review.Worktree{Dir: "/src/acme/api/.worktrees/argus-pr-7"})
	if name != "agtermctl" {
		t.Errorf("name = %q", name)
	}
	want := []string{"session", "new", "--workspace-name", "reviews", "--create-workspace",
		"--cwd", "/src/acme/api/.worktrees/argus-pr-7", "--name", "api#7",
		"--command", "'claude' '/pr https://github.com/acme/api/pull/7'"}
	if !slices.Equal(args, want) {
		t.Errorf("args:\n got %q\nwant %q", args, want)
	}

	c = &Claude{Agtermctl: "/bin/agtermctl", Bin: "/opt/homebrew/bin/claude"}
	name, args = c.Command(testPR(), review.Worktree{Dir: "/w"})
	if name != "/bin/agtermctl" {
		t.Errorf("name = %q", name)
	}
	if got := args[len(args)-1]; got != "'/opt/homebrew/bin/claude' '/pr https://github.com/acme/api/pull/7'" {
		t.Errorf("command = %q", got)
	}
}

// TestClaudeCommandQuoting splits the --command value with a real sh. The
// value must give two arguments also for a path with a space or a quote.
func TestClaudeCommandQuoting(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	pr := testPR()
	pr.URL = "https://github.com/acme/api/pull/7?x=$HOME&y=`id`'z"
	c := &Claude{Bin: "/Applications/My Tools/it's claude"}
	out, err := exec.Command(sh, "-c", `for a in `+c.ClaudeCommand(pr)+`; do printf '%s\n' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := c.Bin + "\n/pr " + pr.URL + "\n"
	if string(out) != want {
		t.Errorf("sh split:\n got %q\nwant %q", out, want)
	}
}

func TestLaunch(t *testing.T) {
	c, r, clone := setup(t)
	if err := c.Launch(context.Background(), testPR()); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(clone, ".worktrees", "argus-pr-7")
	if len(r.calls) != 3 {
		t.Fatalf("calls = %q", r.calls)
	}
	if got := strings.Join(r.calls[0], " "); got != "git -C "+clone+" fetch origin refs/pull/7/head +refs/heads/main:refs/remotes/origin/main" {
		t.Errorf("fetch = %q", got)
	}
	if got := strings.Join(r.calls[1], " "); got != "git -C "+clone+" worktree add --detach "+wtDir+" abc123" {
		t.Errorf("worktree add = %q", got)
	}
	want := []string{"agtermctl", "session", "new", "--workspace-name", "reviews", "--create-workspace",
		"--cwd", wtDir, "--name", "api#7",
		"--command", "'/opt/homebrew/bin/claude' '/pr https://github.com/acme/api/pull/7'"}
	if !slices.Equal(r.calls[2], want) {
		t.Errorf("launch:\n got %q\nwant %q", r.calls[2], want)
	}
}

func TestLaunchRequiresAgterm(t *testing.T) {
	c, r, _ := setup(t)
	c.SessionID = ""
	if err := c.Launch(context.Background(), testPR()); !errors.Is(err, ErrNoAgterm) {
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
	if err := c.Launch(context.Background(), pr); !errors.As(err, &missing) {
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
			err := c.Launch(context.Background(), testPR())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
