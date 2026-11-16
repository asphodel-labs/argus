package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

var listNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return listNow }

type prOpt func(*model.PR)

func approved(p *model.PR) { p.ReviewDecision = model.DecisionApproved }
func draft(p *model.PR)    { p.IsDraft = true }
func bot(p *model.PR)      { p.Author.Typename = model.TypenameBot }

func testPR(id, repo string, n int, title, author string, opts ...prOpt) model.PR {
	owner, name, _ := strings.Cut(repo, "/")
	p := model.PR{
		ID: id, Repo: model.Repo{Owner: owner, Name: name}, Number: n, Title: title,
		Author:    model.Actor{Login: author, Typename: model.TypenameUser},
		CreatedAt: listNow.Add(-3 * time.Hour),
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

var (
	scopeReview   = poller.Scope{Kind: poller.ScopeNeedsReview}
	scopeMine     = poller.Scope{Kind: poller.ScopeMine}
	scopeAcme     = poller.Scope{Kind: poller.ScopeOrg, Org: "acme"}
	scopePersonal = poller.Scope{Kind: poller.ScopePersonal}
)

// listSnapshot has the four section kinds. The org section has 12 more
// PRs to load.
func listSnapshot() *poller.Snapshot {
	return &poller.Snapshot{
		RefreshedAt: listNow,
		Sections: []poller.Section{
			{Scope: scopeReview, Total: 2, PRs: []model.PR{
				testPR("R1", "acme/api", 11, "Fix login", "alice"),
				testPR("R2", "acme/web", 12, "Bump deps", "renovate", bot),
			}},
			{Scope: scopeMine, Total: 3, PRs: []model.PR{
				testPR("M1", "me/tool", 3, "My tool change", "me", draft),
				testPR("M2", "acme/api", 4, "My api change", "me", approved),
				testPR("M3", "me/tool", 5, "Second tool change", "me"),
			}},
			{Scope: scopeAcme, Total: 15, PRs: []model.PR{
				testPR("O1", "acme/api", 21, "Add cache", "bob", approved),
				testPR("O2", "acme/web", 22, "Draft page", "carol", draft),
				testPR("O3", "acme/web", 23, "Dependabot update", "dependabot", bot),
			}},
			{Scope: scopePersonal, Total: 1, PRs: []model.PR{
				testPR("P1", "me/dots", 1, "Dotfiles", "dave"),
			}},
		},
	}
}

func newListModel(t *testing.T) *Model {
	t.Helper()
	m := New(Options{Poller: newFakePoller(), Styles: testStyles(t), Me: "me", BotLogins: []string{"renovate"}, Now: fixedNow})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(pollerMsg{Snapshot: listSnapshot()})
	return m
}

func newListProgram(t *testing.T, p *fakePoller) *teatest.TestModel {
	t.Helper()
	tm := newTestProgram(t, Options{Poller: p, Me: "me", BotLogins: []string{"renovate"}, Now: fixedNow})
	p.ch <- poller.Update{Snapshot: listSnapshot()}
	waitForText(t, tm, "Fix login")
	return tm
}

func sendKeys(m *Model, keys ...string) {
	for _, k := range keys {
		m.Update(key(k))
	}
}

// rowText returns the plain text of each row.
func rowText(m *Model) []string {
	out := make([]string, len(m.list.rows))
	for i, r := range m.list.rows {
		out[i] = strings.TrimSpace(ansi.Strip(m.list.renderRow(r, 200, m.styles)))
	}
	return out
}

func prIDs(m *Model) []string {
	var ids []string
	for _, r := range m.list.rows {
		if r.kind == rowPR {
			ids = append(ids, r.key)
		}
	}
	return ids
}

func selectedID(t *testing.T, m *Model) string {
	t.Helper()
	pr, ok := m.Selected()
	if !ok {
		t.Fatalf("no PR selected, cursor %d", m.list.cursor)
	}
	return pr.ID
}

func TestListSectionsAndGroups(t *testing.T) {
	m := newListModel(t)
	want := []string{
		"▾ Needs my review (2)",
		"⏳ acme/api#11 Fix login",
		"⏳ acme/web#12 Bump deps",
		"▾ My PRs (3)",
		"acme/api",
		"✓  #4 My api change",
		"me/tool",
		"◌  #3 My tool change",
		"⏳ #5 Second tool change",
		"▾ Org acme (3)",
		"✓  acme/api#21 Add cache",
		"◌  acme/web#22 Draft page",
		"⏳ acme/web#23 Dependabot update",
		"… 12 more  [m] load more",
		"▾ Personal (1)",
		"⏳ me/dots#1 Dotfiles",
	}
	got := rowText(m)
	if len(got) != len(want) {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("row %d = %q, want prefix %q", i, got[i], want[i])
		}
	}
	if !strings.HasSuffix(got[1], "alice 3h") {
		t.Errorf("PR row %q has no author and age", got[1])
	}
}

func TestListSectionOrderFollowsKind(t *testing.T) {
	m := newListModel(t)
	snap := listSnapshot()
	slices.Reverse(snap.Sections)
	m.Update(pollerMsg{Snapshot: snap})
	var titles []string
	for _, r := range m.list.rows {
		if r.kind == rowSection {
			titles = append(titles, r.title)
		}
	}
	want := []string{"Needs my review", "My PRs", "Org acme", "Personal"}
	if !slices.Equal(titles, want) {
		t.Fatalf("sections = %v, want %v", titles, want)
	}
}

func TestListStatusColor(t *testing.T) {
	m := newListModel(t)
	r := m.list.rows[slices.IndexFunc(m.list.rows, func(r listRow) bool { return r.kind == rowPR && r.pr.ID == "M1" })]
	if r.status != model.StatusDraft {
		t.Fatalf("status = %v, want draft", r.status)
	}
	line := m.list.renderPR(r, 80, m.styles)
	if !strings.Contains(line, m.styles.Status[model.StatusDraft].Render("◌")) {
		t.Fatalf("row has no draft color: %q", line)
	}
}

func TestListRowFitsWidth(t *testing.T) {
	m := newListModel(t)
	for _, w := range []int{10, 30, 60} {
		for _, r := range m.list.rows {
			if got := lipgloss.Width(m.list.renderRow(r, w, m.styles)); got > w {
				t.Fatalf("row %q width %d > %d", r.key, got, w)
			}
		}
	}
}

func TestListFilters(t *testing.T) {
	m := newListModel(t)
	all := prIDs(m)

	sendKeys(m, "a")
	if got := prIDs(m); slices.Contains(got, "s:org acme/O1") || !slices.Contains(got, "s:my PRs/M2") {
		t.Fatalf("hide approved: %v", got)
	}
	sendKeys(m, "a", "d")
	if got := prIDs(m); slices.Contains(got, "s:org acme/O2") || !slices.Contains(got, "s:my PRs/M1") {
		t.Fatalf("hide drafts: %v", got)
	}
	sendKeys(m, "d", "b")
	want := []string{"s:needs my review/R2", "s:my PRs/M2", "s:my PRs/M1", "s:my PRs/M3", "s:org acme/O3"}
	if got := prIDs(m); !slices.Equal(got, want) {
		t.Fatalf("only bots: %v, want %v", got, want)
	}
	if got := m.Status().Filters; got != "only bots" {
		t.Fatalf("status filters = %q", got)
	}
	sendKeys(m, "b")
	if got := prIDs(m); slices.Contains(got, "s:needs my review/R2") || slices.Contains(got, "s:org acme/O3") {
		t.Fatalf("no bots: %v", got)
	}
	sendKeys(m, "b")
	if got := prIDs(m); !slices.Equal(got, all) || m.Status().Filters != "" {
		t.Fatalf("filters off: %v, status %q", got, m.Status().Filters)
	}
}

func TestListFilterCountInHeader(t *testing.T) {
	m := newListModel(t)
	sendKeys(m, "d")
	if got := rowText(m); !slices.Contains(got, "▾ Org acme (2 of 3)") {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}
}

func TestListAuthorPicker(t *testing.T) {
	p := newFakePoller()
	tm := newListProgram(t, p)
	tm.Send(key("u"))
	waitForText(t, tm, "Author filter", "all authors", "dependabot")
	// Entries: all authors, alice, bob, carol, dave, dependabot, renovate.
	tm.Send(key("j"))
	tm.Send(key("j"))
	tm.Send(key("enter"))
	waitForText(t, tm, "author bob")
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if m.Filters().Author != "bob" {
		t.Fatalf("author = %q, want bob", m.Filters().Author)
	}
	want := []string{"s:my PRs/M2", "s:my PRs/M1", "s:my PRs/M3", "s:org acme/O1"}
	if got := prIDs(m); !slices.Equal(got, want) {
		t.Fatalf("author bob: %v, want %v", got, want)
	}
	if m.Dialogs() != 0 {
		t.Fatalf("dialogs = %d, want 0", m.Dialogs())
	}
}

func TestAuthorPickerClear(t *testing.T) {
	m := newListModel(t)
	m.Update(authorFilterMsg{login: "bob"})
	_, cmd := m.Update(key("u"))
	m.Update(cmd())
	d, ok := m.dialogs[0].(*authorPicker)
	if !ok || d.entries[d.cursor] != "bob" {
		t.Fatal("picker does not start on the current author")
	}
	sendKeys(m, "k", "k", "k", "k", "k")
	_, cmd = m.Update(key("enter"))
	for _, msg := range cmd().(tea.BatchMsg) {
		m.Update(msg())
	}
	if m.Filters().Author != "" || m.Dialogs() != 0 {
		t.Fatalf("author = %q, dialogs %d", m.Filters().Author, m.Dialogs())
	}
}

func TestListLoadMore(t *testing.T) {
	p := newFakePoller()
	tm := newListProgram(t, p)
	// Move to the org section: Needs my review, My PRs, Org acme.
	tm.Send(key("]"))
	tm.Send(key("]"))
	tm.Send(key("j"))
	tm.Send(key("m"))
	waitForText(t, tm, "loading more PRs of Org acme")

	snap := listSnapshot()
	org := &snap.Sections[2]
	org.PRs = append(org.PRs, testPR("O4", "acme/api", 24, "Loaded later", "erin"))
	p.ch <- poller.Update{Snapshot: snap}
	// The renderer redraws only the changed cells, so wait for the new row.
	waitForText(t, tm, "Loaded later")
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if got := rowText(m); !slices.Contains(got, "… 11 more  [m] load more") {
		t.Fatalf("rows after load more:\n%s", strings.Join(got, "\n"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.loadMore) != 1 || p.loadMore[0] != scopeAcme {
		t.Fatalf("LoadMore calls = %v, want [acme]", p.loadMore)
	}
	if got := selectedID(t, m); got != "O1" {
		t.Fatalf("selected %s after load more, want O1", got)
	}
}

func TestListLoadMoreAllLoaded(t *testing.T) {
	m := newListModel(t)
	p := m.poller.(*fakePoller)
	for _, keys := range [][]string{{}, {"]"}, {"]", "]", "]"}} {
		sendKeys(m, "g", "g")
		sendKeys(m, keys...)
		sendKeys(m, "m")
		if !strings.HasPrefix(m.Status().Message(), "all PRs of ") {
			t.Fatalf("keys %v: message %q", keys, m.Status().Message())
		}
	}
	if len(p.loadMore) != 0 {
		t.Fatalf("LoadMore calls = %v, want none", p.loadMore)
	}
}

func TestListSelectionAfterRefresh(t *testing.T) {
	p := newFakePoller()
	tm := newListProgram(t, p)
	// Select O2 (Draft page): ] ] to the org header, then j j.
	for _, k := range []string{"]", "]", "j", "j"} {
		tm.Send(key(k))
	}
	snap := listSnapshot()
	org := &snap.Sections[2]
	org.PRs = append([]model.PR{testPR("O9", "acme/web", 29, "New on top", "frank")}, org.PRs...)
	p.ch <- poller.Update{Snapshot: snap}
	waitForText(t, tm, "New on top")
	tm.Send(key("q"))
	if got := selectedID(t, finalModel(t, tm)); got != "O2" {
		t.Fatalf("selected %s after refresh, want O2", got)
	}
}

func TestListSelectionFallbacks(t *testing.T) {
	m := newListModel(t)
	// R1 is in Needs my review. A refresh moves it to the org section.
	sendKeys(m, "j")
	if selectedID(t, m) != "R1" {
		t.Fatal("setup: R1 not selected")
	}
	snap := listSnapshot()
	r1 := snap.Sections[0].PRs[0]
	snap.Sections[0].PRs = snap.Sections[0].PRs[1:]
	snap.Sections[2].PRs = append(snap.Sections[2].PRs, r1)
	m.Update(pollerMsg{Snapshot: snap})
	if got := m.list.rows[m.list.cursor]; got.pr.ID != "R1" || got.scope != scopeAcme {
		t.Fatalf("cursor on %q, want R1 in the org section", got.key)
	}

	// A PR that is gone keeps the cursor index.
	idx := m.list.cursor
	snap = listSnapshot()
	snap.Sections[0].PRs = snap.Sections[0].PRs[1:]
	m.Update(pollerMsg{Snapshot: snap})
	if m.list.cursor != idx {
		t.Fatalf("cursor = %d, want %d", m.list.cursor, idx)
	}
}

func TestListNavigation(t *testing.T) {
	m := newListModel(t)
	last := len(m.list.rows) - 1
	steps := []struct {
		keys []string
		want int
	}{
		{[]string{"j", "j"}, 2},
		{[]string{"k"}, 1},
		{[]string{"G"}, last},
		{[]string{"j"}, last},
		{[]string{"g", "g"}, 0},
		{[]string{"k"}, 0},
		{[]string{"]"}, 3},
		{[]string{"]"}, 9},
		{[]string{"["}, 3},
		{[]string{"["}, 0},
		{[]string{"["}, 0},
		{[]string{"ctrl+d"}, last},
		{[]string{"ctrl+u"}, 0},
		{[]string{"g", "x", "j"}, 1},
	}
	for i, s := range steps {
		sendKeys(m, s.keys...)
		if m.list.cursor != s.want {
			t.Fatalf("step %d %v: cursor = %d, want %d", i, s.keys, m.list.cursor, s.want)
		}
	}
}

func TestListFold(t *testing.T) {
	m := newListModel(t)
	sendKeys(m, "]", "j", "j", "z", "a")
	if m.list.cursor != 3 || m.list.rows[3].title != "My PRs" {
		t.Fatalf("cursor = %d, want the My PRs row", m.list.cursor)
	}
	got := rowText(m)
	if got[3] != "▸ My PRs (3)" || !strings.HasPrefix(got[4], "▾ Org acme") {
		t.Fatalf("folded rows:\n%s", strings.Join(got, "\n"))
	}
	sendKeys(m, "z", "a")
	if got := rowText(m); got[4] != "acme/api" {
		t.Fatalf("unfolded rows:\n%s", strings.Join(got, "\n"))
	}
}

func TestListScrollKeepsCursorVisible(t *testing.T) {
	m := newListModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	// Pane height 7, list height 5.
	sendKeys(m, "G")
	if m.list.offset != len(m.list.rows)-5 {
		t.Fatalf("offset = %d, want %d", m.list.offset, len(m.list.rows)-5)
	}
	view := ansi.Strip(m.list.view(40, m.styles, true))
	if !strings.Contains(view, "› ⏳ me/dots#1") {
		t.Fatalf("cursor row not visible:\n%s", view)
	}
	sendKeys(m, "g", "g")
	if m.list.offset != 0 {
		t.Fatalf("offset = %d after gg", m.list.offset)
	}
}

func TestListSearch(t *testing.T) {
	p := newFakePoller()
	tm := newListProgram(t, p)
	tm.Send(key("/"))
	// q types a q in the search input; it does not quit.
	for _, k := range []string{"q", "backspace", "c", "h", "a", "n", "g", "e"} {
		tm.Send(key(k))
	}
	tm.Send(key("enter"))
	tm.Send(key("n"))
	tm.Send(key("n"))
	tm.Send(key("N"))
	tm.Send(key("q"))
	m := finalModel(t, tm)
	// Matches in row order: M2 (api change), M1 (tool change), M3.
	if got := selectedID(t, m); got != "M1" {
		t.Fatalf("selected %s, want M1", got)
	}
	if m.list.searching || m.list.query != "change" {
		t.Fatalf("searching %v, query %q", m.list.searching, m.list.query)
	}
}

func TestListSearchMatchesAndWraps(t *testing.T) {
	m := newListModel(t)
	search := func(q string) {
		sendKeys(m, "/")
		for _, r := range q {
			sendKeys(m, string(r))
		}
		sendKeys(m, "enter")
	}
	search("DOTS")
	if selectedID(t, m) != "P1" {
		t.Fatal("repo match failed")
	}
	search("#21")
	if selectedID(t, m) != "O1" {
		t.Fatal("number match failed")
	}
	search("alice")
	if selectedID(t, m) != "R1" {
		t.Fatal("author match with wrap failed")
	}
	sendKeys(m, "N")
	if selectedID(t, m) != "R1" {
		t.Fatal("N with one match moved the cursor")
	}
	search("nothing")
	if m.Status().Message() != "no match: nothing" || selectedID(t, m) != "R1" {
		t.Fatalf("no match: message %q", m.Status().Message())
	}
	// esc cancels the input and keeps the last query.
	sendKeys(m, "/", "x", "esc")
	if m.list.searching || m.list.query != "nothing" || m.Dialogs() != 0 {
		t.Fatal("esc did not cancel the search input")
	}
}

func TestListKeysIgnoredUnderHelpAndDetailFocus(t *testing.T) {
	m := newListModel(t)
	sendKeys(m, "?", "j", "?", "l", "j", "a")
	if m.list.cursor != 0 || m.Filters() != (Filters{}) {
		t.Fatal("list keys work under the help overlay or with the detail focus")
	}
}

func TestFiltersString(t *testing.T) {
	f := Filters{HideApproved: true, HideDrafts: true, Bots: BotsNone, Author: "bob"}
	if got := f.String(); got != "no approved, no drafts, no bots, author bob" {
		t.Fatalf("String = %q", got)
	}
}

func TestAge(t *testing.T) {
	cases := map[time.Duration]string{
		-time.Minute:    "0m",
		5 * time.Minute: "5m",
		3 * time.Hour:   "3h",
		47 * time.Hour:  "47h",
		72 * time.Hour:  "3d",
	}
	for d, want := range cases {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}
