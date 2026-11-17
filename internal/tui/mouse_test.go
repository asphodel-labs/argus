package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func click(m *Model, x, y int) {
	_, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	run(m, cmd)
}

func wheel(m *Model, x, y int, b tea.MouseButton) {
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: b})
}

// rowIndex returns the index of the first list row with the key.
func rowIndex(t *testing.T, m *Model, key string) int {
	t.Helper()
	i := slices.IndexFunc(m.list.rows, func(r listRow) bool { return r.key == key })
	if i < 0 {
		t.Fatalf("no row %q", key)
	}
	return i
}

func TestMouseClickListOpensPR(t *testing.T) {
	m := newListModel(t)
	m.focus = FocusDetail
	// The top border takes line 0, so row i is on line i+1.
	i := rowIndex(t, m, "s:my PRs/M1")
	click(m, 5, i+1)
	if m.Focus() != FocusList || m.list.cursor != i {
		t.Fatalf("focus %v cursor %d, want list and %d", m.Focus(), m.list.cursor, i)
	}
	if pr, ok := m.Opened(); !ok || pr.ID != "M1" {
		t.Fatalf("opened %q %v, want M1", pr.ID, ok)
	}
}

func TestMouseClickListFoldsSection(t *testing.T) {
	m := newListModel(t)
	i := rowIndex(t, m, sectionKey(scopeMine))
	click(m, 5, i+1)
	if !m.list.folded[sectionKey(scopeMine)] {
		t.Fatal("a click on the section row did not fold it")
	}
	click(m, 5, i+1)
	if m.list.folded[sectionKey(scopeMine)] {
		t.Fatal("a second click did not open the section")
	}
}

func TestMouseClickListFooterLoadsMore(t *testing.T) {
	m := newListModel(t)
	i := slices.IndexFunc(m.list.rows, func(r listRow) bool { return r.kind == rowFooter })
	if i < 0 {
		t.Fatal("no footer row")
	}
	click(m, 5, i+1)
	p := m.poller.(*fakePoller)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.loadMore) != 1 || p.loadMore[0] != scopeAcme {
		t.Fatalf("LoadMore calls = %v, want [acme]", p.loadMore)
	}
}

func TestMouseClickOutsideRowsAndPanes(t *testing.T) {
	m := newListModel(t)
	// The border line, a line under the last row, and the status bar.
	for _, y := range []int{0, len(m.list.rows) + 1, m.height - 1} {
		click(m, 5, y)
		if m.list.cursor != 0 {
			t.Fatalf("click on line %d moved the cursor to %d", y, m.list.cursor)
		}
	}
	listWidth, _ := m.layout()
	click(m, listWidth+2, 3)
	if m.Focus() != FocusDetail {
		t.Fatal("a click on the detail pane did not focus it")
	}
	if _, ok := m.Opened(); ok {
		t.Fatal("a click on the empty detail pane opened a PR")
	}
}

func TestMouseIgnoredUnderDialogAndHelp(t *testing.T) {
	m := newListModel(t)
	i := rowIndex(t, m, "s:my PRs/M1")
	sendKeys(m, "?")
	click(m, 5, i+1)
	sendKeys(m, "?")
	run(m, PushDialog(&confirmQuitDialog{}))
	click(m, 5, i+1)
	if m.list.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.list.cursor)
	}
	if _, ok := m.Opened(); ok {
		t.Fatal("a click under an overlay opened a PR")
	}
}

func TestMouseWheelList(t *testing.T) {
	m := newListModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	h := m.list.viewHeight()
	if len(m.list.rows) <= h+wheelLines {
		t.Fatalf("setup: %d rows do not fill the view of %d", len(m.list.rows), h)
	}
	wheel(m, 5, 3, tea.MouseWheelDown)
	if m.list.offset != wheelLines || m.list.cursor != wheelLines {
		t.Fatalf("offset %d cursor %d, want %d and %d", m.list.offset, m.list.cursor, wheelLines, wheelLines)
	}
	wheel(m, 5, 3, tea.MouseWheelUp)
	if m.list.offset != 0 || m.list.cursor != wheelLines {
		t.Fatalf("offset %d cursor %d, want 0 and %d", m.list.offset, m.list.cursor, wheelLines)
	}
	wheel(m, 5, 3, tea.MouseWheelUp)
	if m.list.offset != 0 {
		t.Fatalf("offset %d above the top", m.list.offset)
	}
	if m.Focus() != FocusList {
		t.Fatal("the wheel changed the focus")
	}
}

func TestMouseDetail(t *testing.T) {
	m := newDetailModel(t, recordedClient(t, "detail_full.json"), fixedNow, detailSnapshot(fullPR()))
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 12})
	press(m, "j", "j", "enter")
	listWidth, _ := m.layout()
	x := listWidth + 2
	d := &m.detail
	d.layout()

	wheel(m, x, 3, tea.MouseWheelDown)
	if d.offset != wheelLines {
		t.Fatalf("offset after wheel = %d, want %d", d.offset, wheelLines)
	}
	if m.Focus() != FocusDetail {
		t.Fatal("setup: enter did not focus the detail pane")
	}

	// A click on a line that is not a header does nothing.
	d.offset = d.items[0].line
	d.clamp()
	row := d.items[0].line - d.offset + 1
	before := detailText(m)
	click(m, x, row+1)
	if d.cur != -1 || detailText(m) != before {
		t.Fatalf("a click on a body line changed the view: cur %d", d.cur)
	}
	// A click on the header of the first thread folds it.
	click(m, x, row)
	if d.cur != 0 || strings.Contains(detailText(m), "Why three?") {
		t.Fatalf("cur %d, thread not folded:\n%s", d.cur, detailText(m))
	}
}
