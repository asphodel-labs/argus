package tui

import (
	"errors"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// yankMsgs presses y and returns the messages of the returned commands.
// It does not give the messages to the model. It skips a command that does
// not return in a short time, such as the timer of a status message.
func yankMsgs(t *testing.T, env *actionEnv) []tea.Msg {
	t.Helper()
	_, cmd := env.m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y returned no command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("y did not return a batch")
	}
	var msgs []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		select {
		case msg := <-ch:
			msgs = append(msgs, msg)
		case <-time.After(200 * time.Millisecond):
		}
	}
	return msgs
}

const yankURL = "https://github.com/acme/api/pull/7"

func TestYankWithoutPbcopy(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.m.pbcopy = false
	msgs := yankMsgs(t, env)
	if !slices.Contains(msgs, tea.SetClipboard(yankURL)()) {
		t.Fatalf("no clipboard message in %v", msgs)
	}
	if msg := env.m.Status().Message(); msg != "copied "+yankURL {
		t.Fatalf("message %q", msg)
	}
	if len(env.runner.calls) != 0 {
		t.Fatalf("runner calls %q", env.runner.calls)
	}
}

func TestYankWithPbcopy(t *testing.T) {
	env := newActionModel(t, actionPR())
	env.m.pbcopy = true
	msgs := yankMsgs(t, env)
	if !slices.Contains(msgs, tea.SetClipboard(yankURL)()) {
		t.Fatalf("no clipboard message in %v", msgs)
	}
	want := []string{"sh", "-c", `printf %s "$1" | pbcopy`, "sh", yankURL}
	if len(env.runner.calls) != 1 || !slices.Equal(env.runner.calls[0], want) {
		t.Fatalf("runner calls %q", env.runner.calls)
	}
	for _, msg := range msgs {
		if done, ok := msg.(yankDoneMsg); ok {
			_, next := env.m.Update(done)
			drive(env.m, next)
		}
	}
	if msg := env.m.Status().Message(); msg != "copied "+yankURL {
		t.Fatalf("message %q", msg)
	}
}

func TestYankPbcopyFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		code   int
		stderr string
		err    error
		want   string
	}{
		{"exit code", 1, "no pasteboard\n", nil, "copy URL failed: pbcopy exited with code 1: no pasteboard"},
		{"start error", 0, "", errors.New("sh not found"), "copy URL failed: sh not found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newActionModel(t, actionPR())
			env.m.pbcopy = true
			env.runner.code, env.runner.stderr, env.runner.err = c.code, c.stderr, c.err
			msgs := yankMsgs(t, env)
			// The OSC 52 command runs also when pbcopy fails.
			if !slices.Contains(msgs, tea.SetClipboard(yankURL)()) {
				t.Fatalf("no clipboard message in %v", msgs)
			}
			for _, msg := range msgs {
				_, next := env.m.Update(msg)
				drive(env.m, next)
			}
			if msg := env.m.Status().Message(); msg != c.want {
				t.Fatalf("message %q", msg)
			}
		})
	}
}

func TestYankNeedsOpenedPR(t *testing.T) {
	env := &actionEnv{runner: &fakeRunner{}}
	env.m = New(Options{Runner: env.runner, Styles: testStyles(t), Me: "me", Now: fixedNow})
	env.m.list.setSnapshot(detailSnapshot(actionPR()))
	env.press("j", "j", "y")
	if msg := env.m.Status().Message(); msg != "open a PR with enter" {
		t.Fatalf("message %q", msg)
	}
	if len(env.runner.calls) != 0 {
		t.Fatalf("runner calls %q", env.runner.calls)
	}
}
