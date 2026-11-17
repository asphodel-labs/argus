package poller

import (
	"testing"

	"github.com/tdragon/argus/internal/model"
)

func mine(rollup model.CheckState, comments, reviews, threads int, newest string) PRState {
	return PRState{
		Repo: "acme/api", Number: 1, Title: "Fix", Mine: true,
		CommentCount: comments, ReviewCount: reviews, ThreadCount: threads,
		NewestAuthor: newest, Rollup: rollup,
	}
}

func TestDiffRules(t *testing.T) {
	requested := PRState{Repo: "acme/web", Number: 7, Title: "Add", ReviewRequested: true}
	tests := []struct {
		name       string
		prev, next Seen
		want       []EventKind
		wantRollup model.CheckState
	}{
		{"new review request", Seen{}, Seen{"a": requested}, []EventKind{EventReviewRequested}, ""},
		{"review request already seen", Seen{"a": requested}, Seen{"a": requested}, nil, ""},
		{"known PR becomes review requested",
			Seen{"a": {Repo: "acme/web", Number: 7, Mine: true}},
			Seen{"a": {Repo: "acme/web", Number: 7, Mine: true, ReviewRequested: true}},
			[]EventKind{EventReviewRequested}, ""},
		{"comment count up by other", Seen{"a": mine("", 1, 0, 0, "me")}, Seen{"a": mine("", 2, 0, 0, "bob")}, []EventKind{EventActivity}, ""},
		{"review count up by other", Seen{"a": mine("", 0, 0, 0, "")}, Seen{"a": mine("", 0, 1, 0, "bob")}, []EventKind{EventActivity}, ""},
		{"thread count up by other", Seen{"a": mine("", 0, 0, 0, "")}, Seen{"a": mine("", 0, 0, 3, "bob")}, []EventKind{EventActivity}, ""},
		{"newest author is me", Seen{"a": mine("", 1, 0, 0, "bob")}, Seen{"a": mine("", 2, 0, 0, "me")}, nil, ""},
		{"newest author is me other case", Seen{"a": mine("", 1, 0, 0, "bob")}, Seen{"a": mine("", 2, 0, 0, "ME")}, nil, ""},
		{"count down", Seen{"a": mine("", 2, 0, 0, "bob")}, Seen{"a": mine("", 1, 0, 0, "bob")}, nil, ""},
		{"new PR of mine", Seen{}, Seen{"a": mine(model.CheckSuccess, 5, 0, 0, "bob")}, nil, ""},
		{"CI pending to success", Seen{"a": mine(model.CheckPending, 0, 0, 0, "")}, Seen{"a": mine(model.CheckSuccess, 0, 0, 0, "")}, []EventKind{EventCIResult}, model.CheckSuccess},
		{"CI pending to failure", Seen{"a": mine(model.CheckPending, 0, 0, 0, "")}, Seen{"a": mine(model.CheckFailure, 0, 0, 0, "")}, []EventKind{EventCIResult}, model.CheckFailure},
		{"CI pending to error", Seen{"a": mine(model.CheckPending, 0, 0, 0, "")}, Seen{"a": mine(model.CheckError, 0, 0, 0, "")}, []EventKind{EventCIResult}, model.CheckError},
		{"CI success to failure", Seen{"a": mine(model.CheckSuccess, 0, 0, 0, "")}, Seen{"a": mine(model.CheckFailure, 0, 0, 0, "")}, nil, ""},
		{"CI pending stays pending", Seen{"a": mine(model.CheckPending, 0, 0, 0, "")}, Seen{"a": mine(model.CheckPending, 0, 0, 0, "")}, nil, ""},
		{"CI none to success", Seen{"a": mine("", 0, 0, 0, "")}, Seen{"a": mine(model.CheckSuccess, 0, 0, 0, "")}, nil, ""},
		{"activity and CI together", Seen{"a": mine(model.CheckPending, 0, 0, 0, "")}, Seen{"a": mine(model.CheckSuccess, 1, 0, 0, "bob")}, []EventKind{EventActivity, EventCIResult}, model.CheckSuccess},
		{"activity on a PR that is not mine",
			Seen{"a": {Repo: "acme/web", Number: 7, ReviewRequested: true, CommentCount: 1}},
			Seen{"a": {Repo: "acme/web", Number: 7, ReviewRequested: true, CommentCount: 2, NewestAuthor: "bob"}},
			nil, ""},
		{"PR removed", Seen{"a": requested}, Seen{}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Diff(tt.prev, tt.next, "me")
			if len(got) != len(tt.want) {
				t.Fatalf("got %d events %+v, want kinds %v", len(got), got, tt.want)
			}
			for i, ev := range got {
				if ev.Kind != tt.want[i] {
					t.Errorf("event %d kind = %v, want %v", i, ev.Kind, tt.want[i])
				}
				if ev.PRID != "a" {
					t.Errorf("event %d PRID = %q", i, ev.PRID)
				}
				if ev.Kind == EventCIResult && ev.Rollup != tt.wantRollup {
					t.Errorf("rollup = %q, want %q", ev.Rollup, tt.wantRollup)
				}
				if ev.Kind == EventActivity && ev.Author != "bob" {
					t.Errorf("author = %q, want bob", ev.Author)
				}
			}
		})
	}
}

func TestDiffOrder(t *testing.T) {
	next := Seen{
		"c": {Repo: "acme/b", Number: 2, ReviewRequested: true},
		"b": {Repo: "acme/b", Number: 1, ReviewRequested: true},
		"a": {Repo: "acme/a", Number: 9, ReviewRequested: true},
		"d": mine(model.CheckFailure, 0, 0, 0, ""),
	}
	prev := Seen{"d": mine(model.CheckPending, 0, 0, 0, "")}
	got := Diff(prev, next, "me")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].PRID != id {
			t.Errorf("event %d = %q, want %q", i, got[i].PRID, id)
		}
	}
}

func TestSeenFrom(t *testing.T) {
	pr := func(id string) model.PR {
		return model.PR{
			ID: id, Repo: model.Repo{Owner: "acme", Name: "api"}, Number: 3, Title: "T", URL: "u",
			CommentCount: 1, ReviewCount: 2, ThreadCount: 3,
			NewestAuthor: model.Actor{Login: "bob"}, Rollup: model.CheckPending,
		}
	}
	snap := &Snapshot{Sections: []Section{
		{Scope: Scope{Kind: ScopeNeedsReview}, PRs: []model.PR{pr("r"), pr("both")}},
		{Scope: Scope{Kind: ScopeMine}, PRs: []model.PR{pr("m"), pr("both")}},
		{Scope: Scope{Kind: ScopeOrg, Org: "acme"}, PRs: []model.PR{pr("o"), pr("m")}},
		{Scope: Scope{Kind: ScopePersonal}, PRs: []model.PR{pr("p")}},
	}}
	seen := SeenFrom(snap)
	if len(seen) != 3 {
		t.Fatalf("seen has %d PRs, want 3: %+v", len(seen), seen)
	}
	want := PRState{
		Repo: "acme/api", Number: 3, Title: "T", URL: "u",
		CommentCount: 1, ReviewCount: 2, ThreadCount: 3, NewestAuthor: "bob", Rollup: model.CheckPending,
	}
	wr, wm, wb := want, want, want
	wr.ReviewRequested = true
	wm.Mine = true
	wb.ReviewRequested, wb.Mine = true, true
	for id, w := range map[string]PRState{"r": wr, "m": wm, "both": wb} {
		if seen[id] != w {
			t.Errorf("seen[%s] = %+v, want %+v", id, seen[id], w)
		}
	}
	if got := SeenFrom(nil); len(got) != 0 {
		t.Errorf("SeenFrom(nil) = %v, want empty", got)
	}
}
