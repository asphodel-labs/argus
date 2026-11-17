package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/tdragon/argus/internal/model"
)

// finderIDs returns the PR IDs of the finder matches in order.
func finderIDs(m *Model) []string {
	f, ok := m.topDialog().(*finder)
	if !ok {
		return nil
	}
	ids := make([]string, len(f.matches))
	for i, e := range f.matches {
		ids[i] = e.pr.ID
	}
	return ids
}

// openFinder presses / and runs the command that opens the finder.
func openFinder(t *testing.T, m *Model) {
	t.Helper()
	_, cmd := m.Update(key("/"))
	m.Update(cmd())
	if _, ok := m.topDialog().(*finder); !ok {
		t.Fatal("/ did not open the finder")
	}
}

func typeQuery(m *Model, q string) {
	for _, r := range q {
		if r == ' ' {
			sendKeys(m, "space")
			continue
		}
		sendKeys(m, string(r))
	}
}

func TestFinderMatchByNumber(t *testing.T) {
	m := newListModel(t)
	openFinder(t, m)
	// With no query the finder shows all PRs in list order, each PR once.
	want := []string{"R2", "R1", "M2", "M3", "M1", "O2", "O3", "O1", "P1"}
	if got := finderIDs(m); !slices.Equal(got, want) {
		t.Fatalf("matches %v, want %v", got, want)
	}
	typeQuery(m, "21")
	if got := finderIDs(m); len(got) == 0 || got[0] != "O1" {
		t.Fatalf("21: matches %v, want O1 first", got)
	}
	sendKeys(m, "backspace", "backspace")
	typeQuery(m, "#4")
	if got := finderIDs(m); len(got) == 0 || got[0] != "M2" {
		t.Fatalf("#4: matches %v, want M2 first", got)
	}
}

func TestFinderMatchByTitle(t *testing.T) {
	m := newListModel(t)
	openFinder(t, m)
	typeQuery(m, "tool chg")
	if got := finderIDs(m); !slices.Equal(got, []string{"M3", "M1"}) {
		t.Fatalf("tool chg: matches %v, want [M3 M1]", got)
	}
	sendKeys(m, "backspace", "backspace", "backspace")
	typeQuery(m, "second")
	if got := finderIDs(m); !slices.Equal(got, []string{"M3"}) {
		t.Fatalf("tool second: matches %v, want [M3]", got)
	}
	// q types into the query; it does not quit.
	typeQuery(m, "q")
	if got := finderIDs(m); len(got) != 0 || m.Dialogs() != 1 {
		t.Fatalf("matches %v, dialogs %d", got, m.Dialogs())
	}
	if !strings.Contains(m.topDialog().View(72), "No match.") {
		t.Fatal("no text for no match")
	}
	sendKeys(m, "esc")
	if m.Dialogs() != 0 {
		t.Fatal("esc did not close the finder")
	}
}

func TestFuzzyScoreOrder(t *testing.T) {
	pr := func(n int, title string) model.PR { return testPR("x", "acme/api", n, title, "bob") }
	cases := []struct {
		query        string
		better, less model.PR
	}{
		{"812", pr(812, "Other"), pr(8120, "Other")},
		{"812", pr(8120, "Other"), pr(1, "Fix 1812 bug")},
		{"cache", pr(1, "Add cache"), pr(2, "Add caches")},
		{"cache", pr(2, "Add caches"), pr(3, "Clear all che")},
	}
	for _, c := range cases {
		b, okB := fuzzyScore(c.query, c.better)
		l, okL := fuzzyScore(c.query, c.less)
		if !okB || !okL || b <= l {
			t.Errorf("%q: %q scores %d %v, %q scores %d %v", c.query, c.better.Title, b, okB, c.less.Title, l, okL)
		}
	}
	if _, ok := fuzzyScore("zzz", pr(1, "Add cache")); ok {
		t.Error("zzz matched")
	}
	if _, ok := fuzzyScore("bob", pr(1, "Add cache")); !ok {
		t.Error("the author did not match")
	}
}

func TestFinderJumpIntoFoldedGroup(t *testing.T) {
	m := newListModel(t)
	// Fold the me/tool group, then the My PRs section.
	sendKeys(m, "]", "j", "j", "j", "z", "a", "[", "z", "a")
	if slices.Contains(prIDs(m), "s:my PRs/M1") {
		t.Fatal("setup: M1 is visible")
	}
	openFinder(t, m)
	typeQuery(m, "my tool")
	if got := finderIDs(m); len(got) == 0 || got[0] != "M1" {
		t.Fatalf("matches %v, want M1 first", got)
	}
	press(m, "enter")
	if m.Dialogs() != 0 {
		t.Fatal("enter did not close the finder")
	}
	if got := selectedID(t, m); got != "M1" {
		t.Fatalf("cursor on %s, want M1", got)
	}
	if pr, ok := m.Opened(); !ok || pr.ID != "M1" || m.Focus() != FocusDetail {
		t.Fatalf("opened %q %v, want M1", pr.ID, ok)
	}
	if m.list.folded[sectionKey(scopeMine)] || m.list.folded[groupKey(sectionKey(scopeMine), "me/tool")] {
		t.Fatal("the section or the group is still folded")
	}
}

func TestFinderSkipsFilteredPRs(t *testing.T) {
	m := newListModel(t)
	// Hide drafts: O2 is a draft in the org section. M1 is a draft too, but
	// the filters do not apply to My PRs.
	sendKeys(m, "d")
	openFinder(t, m)
	typeQuery(m, "draft page")
	if got := finderIDs(m); len(got) != 0 {
		t.Fatalf("matches %v, want none", got)
	}
	sendKeys(m, "esc")
	openFinder(t, m)
	if got := finderIDs(m); slices.Contains(got, "O2") || !slices.Contains(got, "M1") {
		t.Fatalf("matches %v", got)
	}
}

func TestFinderTeatest(t *testing.T) {
	p := newFakePoller()
	tm := newListProgram(t, p)
	tm.Send(key("/"))
	waitForText(t, tm, "Find PR")
	for _, k := range []string{"#", "2", "3"} {
		tm.Send(key(k))
	}
	// The renderer redraws only the changed cells, so "#23█" can arrive in
	// two frames. The model gets the keys in order, so enter needs no wait.
	tm.Send(key("enter"))
	// The list marks the opened row.
	waitForText(t, tm, "●")
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if got := selectedID(t, m); got != "O3" {
		t.Fatalf("cursor on %s, want O3", got)
	}
	if pr, ok := m.Opened(); !ok || pr.ID != "O3" {
		t.Fatalf("opened %q %v, want O3", pr.ID, ok)
	}
	if m.Dialogs() != 0 {
		t.Fatalf("dialogs = %d", m.Dialogs())
	}
}
