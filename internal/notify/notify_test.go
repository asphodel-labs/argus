package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

// fakeRunner records the commands. No test calls the real agtermctl.
type fakeRunner struct {
	calls [][]string
	code  int
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return "", "", -1, f.err
	}
	if f.code != 0 {
		return "", "boom", f.code, nil
	}
	return "", "", 0, nil
}

func env(session string) func(string) string {
	return func(k string) string {
		if k == SessionEnv {
			return session
		}
		return ""
	}
}

func events(n int) []poller.Event {
	evs := make([]poller.Event, n)
	for i := range evs {
		evs[i] = poller.Event{Kind: poller.EventReviewRequested, Repo: "acme/api", Number: i + 1, Title: fmt.Sprintf("PR %d", i+1)}
	}
	return evs
}

func TestSendCommand(t *testing.T) {
	r := &fakeRunner{}
	if err := Send(context.Background(), r, "sess-1", events(1)); err != nil {
		t.Fatal(err)
	}
	want := []string{"agtermctl", "notify", "acme/api#1 PR 1", "--title", "Review requested", "--target", "sess-1"}
	if len(r.calls) != 1 || !slices.Equal(r.calls[0], want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func TestSendCap(t *testing.T) {
	tests := []struct {
		events    int
		calls     int
		summary   string
		lastShown int
	}{
		{0, 0, "", 0},
		{5, 5, "", 5},
		{6, 5, "2 more PR updates", 4},
		{12, 5, "8 more PR updates", 4},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.events), func(t *testing.T) {
			r := &fakeRunner{}
			if err := Send(context.Background(), r, "s", events(tt.events)); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != tt.calls {
				t.Fatalf("got %d calls, want %d", len(r.calls), tt.calls)
			}
			if tt.calls == 0 {
				return
			}
			last := r.calls[len(r.calls)-1]
			if tt.summary != "" {
				if last[2] != tt.summary || last[4] != "argus" {
					t.Errorf("summary call = %q, want body %q", last, tt.summary)
				}
				last = r.calls[len(r.calls)-2]
			}
			if want := fmt.Sprintf("acme/api#%d PR %d", tt.lastShown, tt.lastShown); last[2] != want {
				t.Errorf("last event body = %q, want %q", last[2], want)
			}
		})
	}
}

func TestSendNoSession(t *testing.T) {
	r := &fakeRunner{}
	if err := Send(context.Background(), r, "", events(3)); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %q, want none", r.calls)
	}
}

func TestSendErrors(t *testing.T) {
	r := &fakeRunner{code: 2}
	err := Send(context.Background(), r, "s", events(2))
	if err == nil || !strings.Contains(err.Error(), "exit 2: boom") {
		t.Errorf("err = %v, want exit 2", err)
	}
	if len(r.calls) != 2 {
		t.Errorf("got %d calls, want 2: a failure does not stop the next notification", len(r.calls))
	}
	start := errors.New("no binary")
	r = &fakeRunner{err: start}
	if err := Send(context.Background(), r, "s", events(1)); !errors.Is(err, start) {
		t.Errorf("err = %v, want %v", err, start)
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		ev   poller.Event
		want string
	}{
		{poller.Event{Kind: poller.EventReviewRequested}, "Review requested"},
		{poller.Event{Kind: poller.EventActivity, Author: "bob"}, "New activity by bob"},
		{poller.Event{Kind: poller.EventActivity}, "New activity"},
		{poller.Event{Kind: poller.EventCIResult, Rollup: model.CheckSuccess}, "CI passed"},
		{poller.Event{Kind: poller.EventCIResult, Rollup: model.CheckFailure}, "CI failed"},
		{poller.Event{Kind: poller.EventCIResult, Rollup: model.CheckError}, "CI error"},
	}
	for _, tt := range tests {
		if got := Title(tt.ev); got != tt.want {
			t.Errorf("Title(%+v) = %q, want %q", tt.ev, got, tt.want)
		}
	}
}

func snapshot(rollup model.CheckState, comments int, newest string, requested ...string) *poller.Snapshot {
	var review []model.PR
	for _, id := range requested {
		review = append(review, model.PR{ID: id, Repo: model.Repo{Owner: "acme", Name: "web"}, Number: 1, Title: id})
	}
	mine := model.PR{
		ID: "m", Repo: model.Repo{Owner: "acme", Name: "api"}, Number: 2, Title: "Mine",
		Rollup: rollup, CommentCount: comments, NewestAuthor: model.Actor{Login: newest},
	}
	return &poller.Snapshot{Sections: []poller.Section{
		{Scope: poller.Scope{Kind: poller.ScopeNeedsReview}, PRs: review},
		{Scope: poller.Scope{Kind: poller.ScopeMine}, PRs: []model.PR{mine}},
	}}
}

func TestProcessFirstRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", SeenFile)
	r := &fakeRunner{}
	n := &Notifier{Runner: r, Getenv: env("s"), SeenPath: path, Me: "me"}
	if err := n.Process(context.Background(), snapshot(model.CheckPending, 0, "", "r1")); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Errorf("first run sent %q, want nothing", r.calls)
	}
	seen, found, err := LoadSeen(path)
	if err != nil || !found {
		t.Fatalf("LoadSeen: found = %v, err = %v", found, err)
	}
	if len(seen) != 2 || !seen["r1"].ReviewRequested || seen["m"].Rollup != model.CheckPending {
		t.Errorf("seen = %+v", seen)
	}

	// The second snapshot is diffed against the recorded state.
	if err := n.Process(context.Background(), snapshot(model.CheckFailure, 1, "bob", "r1", "r2")); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, c := range r.calls {
		titles = append(titles, c[4])
	}
	want := []string{"Review requested", "New activity by bob", "CI failed"}
	if !slices.Equal(titles, want) {
		t.Errorf("titles = %q, want %q", titles, want)
	}
}

func TestProcessLoadsSeenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), SeenFile)
	if err := SaveSeen(path, poller.SeenFrom(snapshot(model.CheckPending, 0, "", "r1"))); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	n := &Notifier{Runner: r, Getenv: env("s"), SeenPath: path, Me: "me"}
	// A restart with a seen.json is not a first run.
	if err := n.Process(context.Background(), snapshot(model.CheckSuccess, 1, "me", "r1")); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0][4] != "CI passed" {
		t.Errorf("calls = %q, want one CI passed", r.calls)
	}
}

func TestProcessNoSessionKeepsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), SeenFile)
	r := &fakeRunner{}
	n := &Notifier{Runner: r, Getenv: env(""), SeenPath: path, Me: "me"}
	for _, snap := range []*poller.Snapshot{snapshot("", 0, ""), snapshot("", 0, "", "r1")} {
		if err := n.Process(context.Background(), snap); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %q, want none outside agterm", r.calls)
	}
	seen, _, _ := LoadSeen(path)
	if !seen["r1"].ReviewRequested {
		t.Errorf("seen = %+v, want r1 recorded", seen)
	}
}

func TestProcessBadSeenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), SeenFile)
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	n := &Notifier{Runner: r, Getenv: env("s"), SeenPath: path, Me: "me"}
	if err := n.Process(context.Background(), snapshot("", 0, "", "r1")); err == nil {
		t.Error("err = nil, want a parse error")
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %q, want none", r.calls)
	}
}

func TestSaveSeenAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SeenFile)
	first := poller.Seen{"a": {Repo: "acme/api", Number: 1}}
	second := poller.Seen{"b": {Repo: "acme/api", Number: 2, Mine: true}}
	for _, s := range []poller.Seen{first, second} {
		if err := SaveSeen(path, s); err != nil {
			t.Fatal(err)
		}
	}
	got, found, err := LoadSeen(path)
	if err != nil || !found {
		t.Fatalf("found = %v, err = %v", found, err)
	}
	if len(got) != 1 || got["b"] != second["b"] {
		t.Errorf("seen = %+v, want %+v", got, second)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != SeenFile {
		t.Errorf("dir has %v, want only %s", entries, SeenFile)
	}
}

func TestSaveSeenError(t *testing.T) {
	// A file in place of the state directory makes the write fail.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSeen(filepath.Join(blocker, SeenFile), poller.Seen{}); err == nil {
		t.Error("err = nil, want an error")
	}
}

func TestLoadSeenMissing(t *testing.T) {
	seen, found, err := LoadSeen(filepath.Join(t.TempDir(), SeenFile))
	if err != nil || found || seen != nil {
		t.Errorf("seen = %v, found = %v, err = %v", seen, found, err)
	}
}
