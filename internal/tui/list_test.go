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

	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/poller"
)

var listNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return listNow }

type prOpt func(*model.PR)

func approved(p *model.PR) { p.ReviewDecision = model.DecisionApproved }
func draft(p *model.PR)    { p.IsDraft = true }
func bot(p *model.PR)      { p.Author.Typename = model.TypenameBot }

// updated sets UpdatedAt to the minutes before listNow.
func updated(minutes int) prOpt {
	return func(p *model.PR) { p.UpdatedAt = listNow.Add(-time.Duration(minutes) * time.Minute) }
}

func testPR(id, repo string, n int, title, author string, opts ...prOpt) model.PR {
	owner, name, _ := strings.Cut(repo, "/")
	p := model.PR{
		ID: id, Repo: model.Repo{Owner: owner, Name: name}, Number: n, Title: title,
		Author:    model.Actor{Login: author, Typename: model.TypenameUser},
		CreatedAt: listNow.Add(-72 * time.Hour),
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
// PRs to load. The update times give this list order:
//
//	 0 Needs my review     11 Org acme
//	 1   acme/web          12   acme/web
//	 2     R2              13     O2
//	 3   acme/api          14     O3
//	 4     R1              15   acme/api
//	 5 My PRs              16     O1
//	 6   acme/api          17   load more
//	 7     M2              18 Personal
//	 8   me/tool           19   me/dots
//	 9     M3              20     P1
//	10     M1
func listSnapshot() *poller.Snapshot {
	return &poller.Snapshot{
		RefreshedAt: listNow,
		Sections: []poller.Section{
			{Scope: scopeReview, Total: 2, PRs: []model.PR{
				testPR("R1", "acme/api", 11, "Fix login", "alice", updated(30)),
				testPR("R2", "acme/web", 12, "Bump deps", "renovate", bot, updated(10)),
			}},
			{Scope: scopeMine, Total: 3, PRs: []model.PR{
				testPR("M1", "me/tool", 3, "My tool change", "me", draft, updated(60)),
				testPR("M2", "acme/api", 4, "My api change", "me", approved, updated(5)),
				testPR("M3", "me/tool", 5, "Second tool change", "me", updated(20)),
			}},
			{Scope: scopeAcme, Total: 15, PRs: []model.PR{
				testPR("O1", "acme/api", 21, "Add cache", "bob", approved, updated(50)),
				testPR("O2", "acme/web", 22, "Draft page", "carol", draft, updated(40)),
				testPR("O3", "acme/web", 23, "Dependabot update", "dependabot", bot, updated(45)),
			}},
			{Scope: scopePersonal, Total: 1, PRs: []model.PR{
				testPR("P1", "me/dots", 1, "Dotfiles", "dave", updated(70)),
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
	// Every section groups its PRs by repository. The group with the most
	// recently updated PR comes first, and so does the PR in a group.
	want := []string{
		"▾ Needs my review (2)",
		"▾ acme/web (1)",
		"⏳ #12 Bump deps",
		"▾ acme/api (1)",
		"⏳ #11 Fix login",
		"▾ My PRs (3)",
		"▾ acme/api (1)",
		"✓  #4 My api change",
		"▾ me/tool (2)",
		"⏳ #5 Second tool change",
		"◌  #3 My tool change",
		"▾ Org acme (3)",
		"▾ acme/web (2)",
		"◌  #22 Draft page",
		"⏳ #23 Dependabot update",
		"▾ acme/api (1)",
		"✓  #21 Add cache",
		"… 12 more  [m] load more",
		"▾ Personal (1)",
		"▾ me/dots (1)",
		"⏳ #1 Dotfiles",
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
	// The age is the time since the last update, not since the creation.
	if !strings.HasSuffix(got[4], "alice 30m") {
		t.Errorf("PR row %q has no author and age", got[4])
	}
	// PR rows do not show the repository.
	if strings.Contains(got[4], "acme/api") {
		t.Errorf("PR row %q shows the repository", got[4])
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
	want := []string{"s:needs my review/R2", "s:my PRs/M2", "s:my PRs/M3", "s:my PRs/M1", "s:org acme/O3"}
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
	want := []string{"s:my PRs/M2", "s:my PRs/M3", "s:my PRs/M1", "s:org acme/O1"}
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
	// Move to O2 in the org section: Needs my review, My PRs, Org acme.
	for _, k := range []string{"]", "]", "j", "j", "m"} {
		tm.Send(key(k))
	}
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
	if got := selectedID(t, m); got != "O2" {
		t.Fatalf("selected %s after load more, want O2", got)
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
	// Select O3 (Dependabot update): ] ] to the org header, then j j j.
	for _, k := range []string{"]", "]", "j", "j", "j"} {
		tm.Send(key(k))
	}
	snap := listSnapshot()
	org := &snap.Sections[2]
	org.PRs = append([]model.PR{testPR("O9", "acme/web", 29, "New on top", "frank", updated(0))}, org.PRs...)
	p.ch <- poller.Update{Snapshot: snap}
	waitForText(t, tm, "New on top")
	tm.Send(key("q"))
	if got := selectedID(t, finalModel(t, tm)); got != "O3" {
		t.Fatalf("selected %s after refresh, want O3", got)
	}
}

func TestListSelectionFallbacks(t *testing.T) {
	m := newListModel(t)
	// R1 is in Needs my review. A refresh moves it to the org section.
	sendKeys(m, "j", "j", "j", "j")
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
	page := m.list.height
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
		{[]string{"]"}, 5},
		{[]string{"]"}, 11},
		{[]string{"["}, 5},
		{[]string{"["}, 0},
		{[]string{"["}, 0},
		{[]string{"ctrl+d"}, 18},
		{[]string{"ctrl+d"}, last},
		{[]string{"ctrl+u", "ctrl+u"}, 0},
		{[]string{"g", "x", "j"}, 1},
		{[]string{"down", "down", "up"}, 2},
		{[]string{"end"}, last},
		{[]string{"home"}, 0},
		{[]string{"pgdown"}, min(page, last)},
		{[]string{"pgup"}, 0},
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
	sendKeys(m, "]", "z", "a")
	if m.list.cursor != 5 || m.list.rows[5].title != "My PRs" {
		t.Fatalf("cursor = %d, want the My PRs row", m.list.cursor)
	}
	got := rowText(m)
	if got[5] != "▸ My PRs (3)" || !strings.HasPrefix(got[6], "▾ Org acme") {
		t.Fatalf("folded rows:\n%s", strings.Join(got, "\n"))
	}
	sendKeys(m, "z", "a")
	if got := rowText(m); got[6] != "▾ acme/api (1)" {
		t.Fatalf("unfolded rows:\n%s", strings.Join(got, "\n"))
	}
}

func TestListFoldRepoGroup(t *testing.T) {
	m := newListModel(t)
	// za on a PR row folds its repository group: M2 is in acme/api.
	sendKeys(m, "]", "j", "j", "z", "a")
	if m.list.cursor != 6 {
		t.Fatalf("cursor = %d, want the acme/api row 6", m.list.cursor)
	}
	got := rowText(m)
	if got[6] != "▸ acme/api (1)" || got[7] != "▾ me/tool (2)" {
		t.Fatalf("folded rows:\n%s", strings.Join(got, "\n"))
	}
	// The acme/api groups of the other sections stay open.
	if ids := prIDs(m); !slices.Contains(ids, "s:needs my review/R1") || !slices.Contains(ids, "s:org acme/O1") {
		t.Fatalf("the fold changed another section: %v", ids)
	}
	// The fold stays after a refresh.
	m.Update(pollerMsg{Snapshot: listSnapshot()})
	if got := rowText(m); got[6] != "▸ acme/api (1)" || m.list.cursor != 6 {
		t.Fatalf("rows after refresh, cursor %d:\n%s", m.list.cursor, strings.Join(got, "\n"))
	}
	// za on the group row opens it.
	sendKeys(m, "z", "a")
	if got := rowText(m); got[6] != "▾ acme/api (1)" || !strings.HasPrefix(got[7], "✓  #4") {
		t.Fatalf("unfolded rows:\n%s", strings.Join(got, "\n"))
	}
}

func TestListEnterAndSpaceOpenPR(t *testing.T) {
	p := newFakePoller()
	det := &fakeDetailer{prs: map[string]model.PR{}}
	for _, sec := range listSnapshot().Sections {
		for _, pr := range sec.PRs {
			det.prs[pr.ID] = pr
		}
	}
	tm := newTestProgram(t, Options{Poller: p, Detail: det, Me: "me", BotLogins: []string{"renovate"}, Now: fixedNow})
	p.ch <- poller.Update{Snapshot: listSnapshot()}
	waitForText(t, tm, "Fix login")
	// The cursor moves to R2 and R1 with no detail query.
	for _, k := range []string{"j", "j", "j", "j"} {
		tm.Send(key(k))
	}
	tm.Send(key("enter"))
	waitForText(t, tm, "acme/api#11 · alice")
	// enter moved the focus to the detail pane; h moves it back.
	// space opens the PR under the cursor (M2) and keeps the focus.
	tm.Send(key("h"))
	tm.Send(key("j"))
	tm.Send(key("j"))
	tm.Send(key("j"))
	tm.Send(key("space"))
	waitForText(t, tm, "acme/api#4 · me")
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if got := det.calls; !slices.Equal(got, []string{"R1", "M2"}) {
		t.Fatalf("detail calls %v, want [R1 M2]", got)
	}
	if pr, ok := m.Opened(); !ok || pr.ID != "M2" || m.Focus() != FocusList {
		t.Fatalf("opened %q %v, focus %v", pr.ID, ok, m.Focus())
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
	if !strings.Contains(view, "›    ⏳ #1 Dotfiles") {
		t.Fatalf("cursor row not visible:\n%s", view)
	}
	sendKeys(m, "g", "g")
	if m.list.offset != 0 {
		t.Fatalf("offset = %d after gg", m.list.offset)
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

// TestListEmptyScopes checks a snapshot with no orgs and no PRs: the list
// shows the empty sections, and the keys do not fail.
func TestListEmptyScopes(t *testing.T) {
	m := New(Options{Poller: newFakePoller(), Styles: testStyles(t), Me: "me", Now: fixedNow})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(pollerMsg{Snapshot: &poller.Snapshot{RefreshedAt: listNow, Sections: []poller.Section{
		{Scope: scopeReview}, {Scope: scopeMine}, {Scope: scopePersonal},
	}}})
	want := []string{"Needs my review (0)", "My PRs (0)", "Personal (0)"}
	got := rowText(m)
	if len(got) != len(want) {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
	sendKeys(m, "j", "G", "k", "g", "g", "z", "a", "enter", "m", "/", "esc")
	if _, ok := m.Selected(); ok {
		t.Fatal("a PR is selected in an empty list")
	}
	if !strings.Contains(m.View().Content, "My PRs") {
		t.Fatalf("view:\n%s", m.View().Content)
	}
}
