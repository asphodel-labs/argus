package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/model"
)

// fakeSearcher records each search query and returns the set PRs or error.
type fakeSearcher struct {
	mu      sync.Mutex
	prs     []model.PR
	err     error
	queries []string
	firsts  []int
}

func (f *fakeSearcher) SearchPRs(_ context.Context, q string, first int) ([]model.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	f.firsts = append(f.firsts, first)
	return f.prs, f.err
}

func (f *fakeSearcher) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// backendPR returns globex/Backend#n with the id and the state.
func backendPR(id string, n int, title, state string) model.PR {
	p := testPR(id, "globex/Backend", n, title, "bob")
	p.State = state
	return p
}

type searchEnv struct {
	m   *Model
	s   *fakeSearcher
	det *fakeDetailer
	gh  *fakeGitHub
}

// newSearchEnv returns a model with listSnapshot, the org acme, the user
// me, and the fake searcher. The detailer knows the PRs in prs.
func newSearchEnv(t *testing.T, prs ...model.PR) *searchEnv {
	t.Helper()
	env := &searchEnv{s: &fakeSearcher{}, det: &fakeDetailer{prs: map[string]model.PR{}}, gh: &fakeGitHub{}}
	for _, p := range prs {
		env.det.prs[p.ID] = p
	}
	env.m = New(Options{
		Search: env.s, Orgs: []string{"acme"}, Detail: env.det, GitHub: env.gh,
		Styles: testStyles(t), Me: "me", Now: fixedNow,
	})
	drive(env.m, env.m.update(tea.WindowSizeMsg{Width: 120, Height: 40}))
	env.m.list.setSnapshot(listSnapshot())
	return env
}

func (e *searchEnv) press(keys ...string) {
	for _, k := range keys {
		_, cmd := e.m.Update(key(k))
		drive(e.m, cmd)
	}
}

// finderRowsText returns the plain text of the finder rows.
func finderRowsText(m *Model) []string {
	f, ok := m.topDialog().(*finder)
	if !ok {
		return nil
	}
	out := make([]string, f.rows())
	for i := range out {
		out[i] = ansi.Strip(f.rowText(i))
	}
	return out
}

func lastRow(m *Model) string {
	rows := finderRowsText(m)
	if len(rows) == 0 {
		return ""
	}
	return rows[len(rows)-1]
}

func TestParseLookup(t *testing.T) {
	tests := []struct {
		query string
		want  prLookup
		ok    bool
	}{
		{"6505", prLookup{number: 6505}, true},
		{"#6505", prLookup{number: 6505}, true},
		{" #6505 ", prLookup{number: 6505}, true},
		{"backend#6505", prLookup{repo: "backend", number: 6505}, true},
		{"globex/Backend#6505", prLookup{owner: "globex", repo: "Backend", number: 6505}, true},
		{"my.repo_x#7", prLookup{repo: "my.repo_x", number: 7}, true},
		{"#", prLookup{}, false},
		{"a/b/c#1", prLookup{}, false},
		{"abc", prLookup{}, false},
		{"#abc", prLookup{}, false},
		{"abc#", prLookup{}, false},
		{"abc#1x", prLookup{}, false},
		{"backend 6505", prLookup{}, false},
		{"/backend#1", prLookup{}, false},
		{"0", prLookup{}, false},
		{"", prLookup{}, false},
	}
	for _, tt := range tests {
		got, ok := parseLookup(tt.query)
		if ok != tt.ok || got != tt.want {
			t.Errorf("%q: got %+v %v, want %+v %v", tt.query, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLookupSearchQuery(t *testing.T) {
	orgs := []string{"globex", "acme"}
	tests := []struct {
		query, want string
	}{
		{"6505", "is:pr 6505 org:globex org:acme user:octocat"},
		{"backend#6505", "is:pr 6505 org:globex org:acme user:octocat"},
		{"globex/Backend#6505", "is:pr 6505 repo:globex/Backend"},
	}
	for _, tt := range tests {
		lk, _ := parseLookup(tt.query)
		if got := lk.searchQuery(orgs, "octocat"); got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.query, got, tt.want)
		}
	}
}

func TestLookupFilter(t *testing.T) {
	prs := []model.PR{
		backendPR("A", 6505, "Export", "MERGED"),
		backendPR("B", 5105, "Follow-up to 6505", "OPEN"),
		testPR("C", "globex/Frontend", 6505, "Other repo", "bob"),
	}
	ids := func(prs []model.PR) []string {
		var out []string
		for _, p := range prs {
			out = append(out, p.ID)
		}
		return out
	}
	for query, want := range map[string][]string{
		"6505":                   {"A", "C"},
		"backend#6505":           {"A"},
		"globex/BACKEND#6505": {"A"},
		"other/backend#6505":     nil,
	} {
		lk, _ := parseLookup(query)
		if got := ids(lk.filter(prs)); !slices.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", query, got, want)
		}
	}
}

func TestFinderSearchRow(t *testing.T) {
	env := newSearchEnv(t)
	openFinder(t, env.m)
	// O1 is acme/api#21: a loaded PR matches, so no search row.
	for _, q := range []string{"21", "#21", "api#21", "ACME/Api#21"} {
		typeQuery(env.m, q)
		if ids := finderIDs(env.m); len(ids) == 0 || ids[0] != "O1" {
			t.Fatalf("%q: matches %v, want O1 first", q, ids)
		}
		if row := lastRow(env.m); strings.Contains(row, "search GitHub") {
			t.Fatalf("%q: search row %q", q, row)
		}
		for range q {
			sendKeys(env.m, "backspace")
		}
	}
	typeQuery(env.m, "web#21")
	if got := lastRow(env.m); got != "search GitHub for web#21" {
		t.Fatalf("web#21: last row %q", got)
	}
	sendKeys(env.m, "esc")
	openFinder(t, env.m)
	typeQuery(env.m, "#2")
	// #2 matches 21, 22, 23 by prefix, but no PR has the number 2.
	rows := finderRowsText(env.m)
	if len(rows) < 2 || rows[len(rows)-1] != "search GitHub for #2" || !strings.HasPrefix(rows[0], "acme/") {
		t.Fatalf("#2: rows %q", rows)
	}
	if len(env.s.Queries()) != 0 {
		t.Fatal("typing ran a search")
	}
}

func TestFinderSearchNoSearcher(t *testing.T) {
	m := newListModel(t)
	openFinder(t, m)
	typeQuery(m, "6505")
	if !strings.Contains(m.topDialog().View(72), "No match.") {
		t.Fatal("a finder without a searcher offers the search")
	}
}

func TestFinderSearchOneMatchNotInList(t *testing.T) {
	merged := backendPR("PR_6505", 6505, "Move the export", "MERGED")
	env := newSearchEnv(t, merged)
	env.s.prs = []model.PR{merged, backendPR("PR_5105", 5105, "Follow-up to 6505", "OPEN")}
	env.press("j")
	cursor := env.m.list.cursor
	openFinder(t, env.m)
	typeQuery(env.m, "6505")
	env.press("enter")

	if got := env.s.Queries(); !slices.Equal(got, []string{"is:pr 6505 org:acme user:me"}) {
		t.Fatalf("queries %q", got)
	}
	if env.s.firsts[0] != 10 {
		t.Fatalf("first = %d, want 10", env.s.firsts[0])
	}
	if env.m.Dialogs() != 0 {
		t.Fatal("the finder is open")
	}
	if pr, ok := env.m.Opened(); !ok || pr.ID != "PR_6505" || env.m.Focus() != FocusDetail {
		t.Fatalf("opened %q %v", pr.ID, ok)
	}
	if !slices.Contains(env.det.calls, "PR_6505") {
		t.Fatalf("detail calls %v", env.det.calls)
	}
	if got := env.m.Status().Message(); got != "opened globex/Backend#6505 (not in the list)" {
		t.Fatalf("status %q", got)
	}
	if got := env.m.list.cursor; got != cursor {
		t.Fatalf("cursor on %d, want %d", got, cursor)
	}

	// M on the merged PR shows the reason; it does not merge.
	env.press("M")
	d, ok := env.m.topDialog().(*mergeDialog)
	if !ok || !strings.Contains(ansi.Strip(d.View(72)), "Blocked: the PR is not open") {
		t.Fatalf("merge dialog %v", env.m.topDialog())
	}
	env.press("enter")
	wantCalls(t, env.gh.Calls())
}

func TestFinderSearchOneMatchInList(t *testing.T) {
	found := testPR("O9", "acme/api", 6505, "Old cache work", "bob")
	env := newSearchEnv(t, found)
	env.s.prs = []model.PR{found}
	openFinder(t, env.m)
	typeQuery(env.m, "6505")
	// A refresh while the finder is open puts the PR in a folded group.
	snap := listSnapshot()
	snap.Sections[2].PRs = append(snap.Sections[2].PRs, found)
	env.m.list.folded[groupKey(sectionKey(scopeAcme), "acme/api")] = true
	env.m.list.setSnapshot(snap)
	env.press("enter")

	if env.m.Dialogs() != 0 {
		t.Fatal("the finder is open")
	}
	if got := selectedID(t, env.m); got != "O9" {
		t.Fatalf("cursor on %s, want O9", got)
	}
	if pr, ok := env.m.Opened(); !ok || pr.ID != "O9" {
		t.Fatalf("opened %q %v", pr.ID, ok)
	}
	if strings.Contains(env.m.Status().Message(), "not in the list") {
		t.Fatalf("status %q", env.m.Status().Message())
	}
}

func TestFinderSearchSeveralMatches(t *testing.T) {
	a := testPR("X1", "acme/api", 30, "Api thirty", "bob")
	a.State = "OPEN"
	w := testPR("X2", "acme/web", 30, "Web thirty", "bob")
	w.State = "CLOSED"
	env := newSearchEnv(t, a, w)
	env.s.prs = []model.PR{a, testPR("X3", "acme/api", 300, "Mentions 30", "bob"), w}
	openFinder(t, env.m)
	typeQuery(env.m, "30")
	env.press("enter")

	if env.m.Dialogs() != 1 {
		t.Fatal("the finder closed")
	}
	want := []string{"acme/api#30 Api thirty · open", "acme/web#30 Web thirty · closed"}
	if got := finderRowsText(env.m); !slices.Equal(got, want) {
		t.Fatalf("rows %q, want %q", got, want)
	}
	env.press("down", "enter")
	if env.m.Dialogs() != 0 {
		t.Fatal("the finder is open")
	}
	if pr, ok := env.m.Opened(); !ok || pr.ID != "X2" {
		t.Fatalf("opened %q %v", pr.ID, ok)
	}
	if got := env.m.Status().Message(); got != "opened acme/web#30 (not in the list)" {
		t.Fatalf("status %q", got)
	}
	if len(env.s.Queries()) != 1 {
		t.Fatalf("queries %q", env.s.Queries())
	}
}

func TestFinderSearchNoMatch(t *testing.T) {
	env := newSearchEnv(t)
	env.s.prs = []model.PR{backendPR("PR_5105", 5105, "Follow-up to 6505", "OPEN")}
	openFinder(t, env.m)
	typeQuery(env.m, "6505")
	env.press("enter")
	if env.m.Dialogs() != 1 {
		t.Fatal("the finder closed")
	}
	if got := env.m.Status().Message(); got != "no PR #6505 in your orgs or repos" {
		t.Fatalf("status %q", got)
	}
	if got := lastRow(env.m); got != "search GitHub for #6505" {
		t.Fatalf("last row %q", got)
	}

	sendKeys(env.m, "esc")
	openFinder(t, env.m)
	typeQuery(env.m, "backend#6505")
	env.press("enter")
	if got := env.m.Status().Message(); got != "no PR backend#6505" {
		t.Fatalf("status %q", got)
	}
	if env.m.Dialogs() != 1 {
		t.Fatal("the finder closed")
	}
}

func TestFinderSearchError(t *testing.T) {
	env := newSearchEnv(t)
	env.s.err = errors.New("HTTP 502: bad gateway")
	openFinder(t, env.m)
	typeQuery(env.m, "6505")
	env.press("enter")
	if env.m.Dialogs() != 1 {
		t.Fatal("the finder closed")
	}
	if got := env.m.Status().Message(); got != "search GitHub for #6505: HTTP 502: bad gateway" {
		t.Fatalf("status %q", got)
	}
	// enter runs the search again.
	env.press("enter")
	if got := env.s.Queries(); len(got) != 2 {
		t.Fatalf("queries %q", got)
	}
}

func TestFinderSearchEnterWhileSearching(t *testing.T) {
	env := newSearchEnv(t)
	openFinder(t, env.m)
	typeQuery(env.m, "6505")
	_, cmd := env.m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter started no search")
	}
	if got := lastRow(env.m); got != "searching…" {
		t.Fatalf("last row %q", got)
	}
	if _, second := env.m.Update(key("enter")); second != nil {
		t.Fatal("a second enter started a command")
	}
	drive(env.m, cmd)
	if got := env.s.Queries(); len(got) != 1 {
		t.Fatalf("queries %q", got)
	}
	if got := lastRow(env.m); got != "search GitHub for #6505" {
		t.Fatalf("last row %q", got)
	}
}

func TestFinderSearchResultAfterClose(t *testing.T) {
	merged := backendPR("PR_6505", 6505, "Move the export", "MERGED")
	env := newSearchEnv(t, merged)
	env.s.prs = []model.PR{merged}
	openFinder(t, env.m)
	typeQuery(env.m, "6505")
	_, cmd := env.m.Update(key("enter"))
	sendKeys(env.m, "esc")
	drive(env.m, cmd)
	if _, ok := env.m.Opened(); ok {
		t.Fatal("a search result opened a PR after the finder closed")
	}
}
