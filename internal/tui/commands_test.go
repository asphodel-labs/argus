package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"text/template"

	tea "charm.land/bubbletea/v2"

	"github.com/asphodel-labs/argus/internal/config"
	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/review"
)

// fakeCommander records each run and returns err.
type fakeCommander struct {
	mu   sync.Mutex
	cmds []config.Command
	prs  []model.PR
	err  error
}

func (f *fakeCommander) Run(_ context.Context, cmd config.Command, pr model.PR) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	f.prs = append(f.prs, pr)
	if f.err != nil {
		return "", f.err
	}
	return cmd.Name + ": done", nil
}

func (f *fakeCommander) runs() ([]config.Command, []model.PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cmds, f.prs
}

// customCommand returns a background command with the name and the key.
func customCommand(name, key string) config.Command {
	return config.Command{Name: name, Key: key, Mode: config.ModeBackground,
		Run: template.Must(template.New(name).Parse("gh stack sync"))}
}

// newCommandModel returns a model with the custom commands and the opened
// PR. The detail has the head branch "feat/x" and the list summary has
// none, so a test can tell which PR the commander gets.
func newCommandModel(t *testing.T, cmds []config.Command, c *fakeCommander) *actionEnv {
	t.Helper()
	summary := openPR()
	detail := summary
	detail.HeadRef = "feat/x"
	env := &actionEnv{
		det:    &fakeDetailer{prs: map[string]model.PR{detail.ID: detail}},
		runner: &fakeRunner{},
	}
	env.m = mustNew(t, Options{
		Detail: env.det, Runner: env.runner, Cloner: &review.Worktrees{Runner: env.runner},
		Commands: cmds, Commander: c, Styles: testStyles(t), Me: "me", Now: fixedNow,
	})
	drive(env.m, env.m.update(tea.WindowSizeMsg{Width: 160, Height: 60}))
	env.m.list.setSnapshot(detailSnapshot(summary))
	env.press("j", "j", "enter")
	return env
}

func TestCustomCommandRuns(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(e *actionEnv)
	}{
		{"command line", func(e *actionEnv) { e.runLine("stack-sync") }},
		{"key", func(e *actionEnv) { e.press("S") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeCommander{}
			stackSync := customCommand("stack-sync", "S")
			env := newCommandModel(t, []config.Command{customCommand("other", ""), stackSync}, fc)
			loads := env.det.callCount()
			c.run(env)
			cmds, prs := fc.runs()
			if len(cmds) != 1 || cmds[0].Name != "stack-sync" {
				t.Fatalf("commands %v", cmds)
			}
			if prs[0].ID != "PR_1" || prs[0].HeadRef != "feat/x" {
				t.Fatalf("PR %s with head %q, want the detail of PR_1", prs[0].ID, prs[0].HeadRef)
			}
			if msg := env.m.Status().Message(); msg != "stack-sync: done" {
				t.Fatalf("message %q", msg)
			}
			if env.det.callCount() != loads+1 {
				t.Fatalf("detail loads %d, want %d", env.det.callCount(), loads+1)
			}
		})
	}
}

func TestCustomCommandError(t *testing.T) {
	fc := &fakeCommander{err: errors.New("last error")}
	env := newCommandModel(t, []config.Command{customCommand("stack-sync", "")}, fc)
	env.runLine("stack-sync")
	if msg := env.m.Status().Message(); msg != "stack-sync failed: last error" {
		t.Fatalf("message %q", msg)
	}
}

func TestCustomCommandMissingClone(t *testing.T) {
	fc := &fakeCommander{err: &review.MissingClone{Repo: "acme/api", Dir: "/src/acme/api"}}
	env := newCommandModel(t, []config.Command{customCommand("stack-sync", "S")}, fc)
	env.press("S")
	if _, ok := env.m.topDialog().(*cloneDialog); !ok {
		t.Fatalf("top dialog %T, want the clone prompt", env.m.topDialog())
	}
	if text := env.dialogText(); !strings.Contains(text, "No local clone of acme/api.") {
		t.Fatalf("dialog %q", text)
	}
}

func TestCustomCommandNeedsDetail(t *testing.T) {
	fc := &fakeCommander{}
	env := newCommandModel(t, []config.Command{customCommand("stack-sync", "S")}, fc)
	delete(env.m.detail.cache, "PR_1")
	env.press("S")
	if msg := env.m.Status().Message(); msg != "the PR detail is not loaded yet" {
		t.Fatalf("message %q", msg)
	}
	if cmds, _ := fc.runs(); len(cmds) != 0 {
		t.Fatalf("commands %v", cmds)
	}
}

func TestCustomCommandCompletion(t *testing.T) {
	env := newCommandModel(t, []config.Command{customCommand("stack-sync", ""), customCommand("stack-push", "")}, &fakeCommander{})
	env.press(":")
	if line := lastLine(env.m); !strings.HasPrefix(line, ":█   draft ready close stack-sync stack-push") {
		t.Fatalf("last line %q", line)
	}
	env.typeText("st")
	env.press("tab")
	if line := lastLine(env.m); !strings.HasPrefix(line, ":stack-█   stack-sync stack-push") {
		t.Fatalf("last line %q", line)
	}
}

func TestCustomCommandKeyOverlap(t *testing.T) {
	for _, k := range []string{"A", "g", "z", "?", "enter"} {
		t.Run(k, func(t *testing.T) {
			_, err := New(Options{Commands: []config.Command{customCommand("stack-sync", k)}, Styles: testStyles(t)})
			want := `command stack-sync: key "` + k + `" is a built-in key`
			if err == nil || err.Error() != want {
				t.Fatalf("err %v, want %q", err, want)
			}
		})
	}
}

func TestCustomCommandHelp(t *testing.T) {
	m := mustNew(t, Options{Commands: []config.Command{customCommand("stack-sync", "S"), customCommand("other", "")}, Styles: testStyles(t)})
	actions := m.keys.HelpFor(FocusList)[1]
	last := actions.Bindings[len(actions.Bindings)-1]
	if actions.Title != "PR actions" || len(last.Keys) != 1 || last.Keys[0] != "S" || last.Desc != "stack-sync" {
		t.Fatalf("last action %v in %q", last, actions.Title)
	}
	if n := len(DefaultKeyMap().Actions); len(actions.Bindings) != n+1 {
		t.Fatalf("%d actions, want %d", len(actions.Bindings), n+1)
	}
}
