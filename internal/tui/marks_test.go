package tui

import (
	"maps"
	"testing"
	"time"

	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/poller"
)

var marksPrevAt = time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)

// markPR returns a summary with set counts, head SHA, and states.
func markPR(id string) model.PR {
	return model.PR{
		ID:             id,
		CreatedAt:      marksPrevAt.Add(-24 * time.Hour),
		CommentCount:   2,
		ReviewCount:    1,
		ThreadCount:    1,
		NewestAuthor:   model.Actor{Login: "alice"},
		HeadSHA:        "aaa",
		Rollup:         model.CheckPending,
		ReviewDecision: model.DecisionReviewRequired,
	}
}

func markSnap(at time.Time, prs ...model.PR) *poller.Snapshot {
	return &poller.Snapshot{
		RefreshedAt: at,
		Sections:    []poller.Section{{Scope: poller.Scope{Kind: poller.ScopeMine}, PRs: prs}},
	}
}

func TestDiffMarks(t *testing.T) {
	base := markPR("PR_1")
	with := func(f func(*model.PR)) model.PR {
		pr := base
		f(&pr)
		return pr
	}
	nextAt := marksPrevAt.Add(time.Minute)
	cases := []struct {
		name string
		prev []model.PR
		next []model.PR
		want map[string]markSet
	}{
		{"no change", []model.PR{base}, []model.PR{base}, map[string]markSet{}},
		{
			"new PR", nil,
			[]model.PR{with(func(p *model.PR) { p.CreatedAt = marksPrevAt.Add(time.Second) })},
			map[string]markSet{"PR_1": {New: true}},
		},
		{
			"PR entered the loaded pages", nil, []model.PR{base},
			map[string]markSet{"PR_1": {Activity: true}},
		},
		{
			"PR created at prev refresh time", nil,
			[]model.PR{with(func(p *model.PR) { p.CreatedAt = marksPrevAt })},
			map[string]markSet{"PR_1": {Activity: true}},
		},
		{
			"comment count up", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.CommentCount++; p.NewestAuthor.Login = "bob" })},
			map[string]markSet{"PR_1": {Activity: true}},
		},
		{
			"review count up", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.ReviewCount++ })},
			map[string]markSet{"PR_1": {Activity: true}},
		},
		{
			"thread count up", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.ThreadCount++ })},
			map[string]markSet{"PR_1": {Activity: true}},
		},
		{
			"count down", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.CommentCount-- })},
			map[string]markSet{},
		},
		{
			"count up by me", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.CommentCount++; p.NewestAuthor.Login = "Me" })},
			map[string]markSet{},
		},
		{
			"new head SHA", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.HeadSHA = "bbb" })},
			map[string]markSet{"PR_1": {Commits: true}},
		},
		{
			"empty new head SHA", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.HeadSHA = "" })},
			map[string]markSet{},
		},
		{
			"empty old head SHA", []model.PR{with(func(p *model.PR) { p.HeadSHA = "" })},
			[]model.PR{base},
			map[string]markSet{},
		},
		{
			"CI success", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.Rollup = model.CheckSuccess })},
			map[string]markSet{"PR_1": {State: stateGood}},
		},
		{
			"CI failure", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.Rollup = model.CheckFailure })},
			map[string]markSet{"PR_1": {State: stateBad}},
		},
		{
			"CI error", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.Rollup = model.CheckError })},
			map[string]markSet{"PR_1": {State: stateBad}},
		},
		{
			"CI to expected", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.Rollup = model.CheckExpected })},
			map[string]markSet{},
		},
		{
			"CI success stays", []model.PR{with(func(p *model.PR) { p.Rollup = model.CheckSuccess })},
			[]model.PR{with(func(p *model.PR) { p.Rollup = model.CheckSuccess })},
			map[string]markSet{},
		},
		{
			"review approved", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.ReviewDecision = model.DecisionApproved })},
			map[string]markSet{"PR_1": {State: stateGood}},
		},
		{
			"review changes requested", []model.PR{base},
			[]model.PR{with(func(p *model.PR) { p.ReviewDecision = model.DecisionChangesRequested })},
			map[string]markSet{"PR_1": {State: stateBad}},
		},
		{
			"red CI wins over green review", []model.PR{base},
			[]model.PR{with(func(p *model.PR) {
				p.Rollup = model.CheckFailure
				p.ReviewDecision = model.DecisionApproved
			})},
			map[string]markSet{"PR_1": {State: stateBad}},
		},
		{
			"red review wins over green CI", []model.PR{base},
			[]model.PR{with(func(p *model.PR) {
				p.Rollup = model.CheckSuccess
				p.ReviewDecision = model.DecisionChangesRequested
			})},
			map[string]markSet{"PR_1": {State: stateBad}},
		},
		{
			"all marks on a known PR", []model.PR{base},
			[]model.PR{with(func(p *model.PR) {
				p.ThreadCount++
				p.HeadSHA = "ccc"
				p.Rollup = model.CheckSuccess
			})},
			map[string]markSet{"PR_1": {Activity: true, Commits: true, State: stateGood}},
		},
		{"PR left the list", []model.PR{base}, nil, map[string]markSet{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diffMarks(markSnap(marksPrevAt, tc.prev...), markSnap(nextAt, tc.next...), "me")
			if !maps.Equal(got, tc.want) {
				t.Errorf("diffMarks = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDiffMarksFirstSectionGivesSummary(t *testing.T) {
	prev := markSnap(marksPrevAt, markPR("PR_1"))
	first := markPR("PR_1")
	first.HeadSHA = "bbb"
	stale := markPR("PR_1")
	next := &poller.Snapshot{
		RefreshedAt: marksPrevAt.Add(time.Minute),
		Sections: []poller.Section{
			{Scope: poller.Scope{Kind: poller.ScopeMine}, PRs: []model.PR{first}},
			{Scope: poller.Scope{Kind: poller.ScopeOrg, Org: "acme"}, PRs: []model.PR{stale}},
		},
	}
	got := diffMarks(prev, next, "me")
	want := map[string]markSet{"PR_1": {Commits: true}}
	if !maps.Equal(got, want) {
		t.Errorf("diffMarks = %+v, want %+v", got, want)
	}
}

func TestDiffMarksNilPrev(t *testing.T) {
	next := markSnap(marksPrevAt, markPR("PR_1"))
	if got := diffMarks(nil, next, "me"); len(got) != 0 {
		t.Errorf("diffMarks(nil, next) = %+v, want no marks", got)
	}
}

func TestMarkSetMerge(t *testing.T) {
	cases := []struct {
		name string
		a, b markSet
		want markSet
	}{
		{"flags accumulate", markSet{New: true}, markSet{Activity: true, Commits: true},
			markSet{New: true, Activity: true, Commits: true}},
		{"flag stays", markSet{Commits: true}, markSet{}, markSet{Commits: true}},
		{"newest state wins over bad", markSet{State: stateBad}, markSet{State: stateGood}, markSet{State: stateGood}},
		{"newest state wins over good", markSet{State: stateGood}, markSet{State: stateBad}, markSet{State: stateBad}},
		{"empty state keeps old color", markSet{State: stateBad}, markSet{Activity: true},
			markSet{Activity: true, State: stateBad}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.merge(tc.b); got != tc.want {
				t.Errorf("merge = %+v, want %+v", got, tc.want)
			}
		})
	}
}
