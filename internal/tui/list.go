package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

// rowKind is the kind of a row in the list pane.
type rowKind int

const (
	rowSection rowKind = iota
	rowGroup
	rowPR
	rowFooter
)

// listRow is one line of the list pane.
type listRow struct {
	kind rowKind
	// key identifies the row across rebuilds.
	key string
	// section is the index of the section in the snapshot.
	section int
	scope   poller.Scope

	// title is the text of a section or a group row.
	title string
	// shown and loaded are the PR counts of a section row: the PRs that
	// the filters keep and all loaded PRs.
	shown, loaded int
	// more is the number of PRs that load more can still load.
	more int

	pr       model.PR
	status   model.Status
	showRepo bool
	indent   int
}

// listPane is the state of the list pane: the rows, the cursor, the
// filters, the folds, and the search.
type listPane struct {
	me        string
	botLogins []string
	now       func() time.Time

	snapshot *poller.Snapshot
	filters  Filters
	folded   map[string]bool

	rows   []listRow
	cursor int
	offset int
	// height is the number of lines in the pane.
	height int

	// pending is the first key of a two-key sequence: "g" or "z".
	pending string

	// searching is true while the user types the search query in input.
	// query is the last confirmed query for n and N.
	searching bool
	input     string
	query     string
}

func newListPane(me string, botLogins []string, now func() time.Time) listPane {
	if now == nil {
		now = time.Now
	}
	return listPane{me: me, botLogins: botLogins, now: now, folded: map[string]bool{}, height: 1}
}

// Selected returns the PR under the cursor, or false if the cursor is not
// on a PR row.
func (l *listPane) Selected() (model.PR, bool) {
	if l.cursor < 0 || l.cursor >= len(l.rows) || l.rows[l.cursor].kind != rowPR {
		return model.PR{}, false
	}
	return l.rows[l.cursor].pr, true
}

func (l *listPane) setSnapshot(s *poller.Snapshot) {
	l.snapshot = s
	l.rebuild()
}

func (l *listPane) setHeight(h int) {
	l.height = max(h, 1)
	l.scroll()
}

func sectionKey(s poller.Scope) string { return "s:" + s.String() }

func sectionTitle(s poller.Scope) string {
	switch s.Kind {
	case poller.ScopeNeedsReview:
		return "Needs my review"
	case poller.ScopeMine:
		return "My PRs"
	case poller.ScopeOrg:
		return "Org " + s.Org
	default:
		return "Personal"
	}
}

// rebuild makes the rows from the snapshot, the filters, and the folds.
// The cursor stays on the same row key. If that row is gone, the cursor
// goes to the same PR id in another section, or stays at the same index.
func (l *listPane) rebuild() {
	var selKey, selID string
	if l.cursor >= 0 && l.cursor < len(l.rows) {
		selKey = l.rows[l.cursor].key
		if l.rows[l.cursor].kind == rowPR {
			selID = l.rows[l.cursor].pr.ID
		}
	}
	l.rows = l.buildRows()

	idx := slices.IndexFunc(l.rows, func(r listRow) bool { return r.key == selKey })
	if idx < 0 && selID != "" {
		idx = slices.IndexFunc(l.rows, func(r listRow) bool { return r.kind == rowPR && r.pr.ID == selID })
	}
	if idx < 0 {
		idx = min(l.cursor, len(l.rows)-1)
	}
	l.cursor = max(idx, 0)
	l.scroll()
}

func (l *listPane) buildRows() []listRow {
	if l.snapshot == nil {
		return nil
	}
	order := make([]int, len(l.snapshot.Sections))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return int(l.snapshot.Sections[a].Scope.Kind) - int(l.snapshot.Sections[b].Scope.Kind)
	})

	var rows []listRow
	for _, si := range order {
		sec := l.snapshot.Sections[si]
		key := sectionKey(sec.Scope)
		mine := sec.Scope.Kind == poller.ScopeMine
		var prs []model.PR
		for _, pr := range sec.PRs {
			if mine || l.filters.Match(pr, l.botLogins) {
				prs = append(prs, pr)
			}
		}
		rows = append(rows, listRow{
			kind: rowSection, key: key, section: si, scope: sec.Scope,
			title: sectionTitle(sec.Scope), shown: len(prs), loaded: len(sec.PRs),
		})
		if l.folded[key] {
			continue
		}
		prRow := func(pr model.PR, showRepo bool, indent int) listRow {
			return listRow{
				kind: rowPR, key: key + "/" + pr.ID, section: si, scope: sec.Scope,
				pr: pr, status: model.DeriveStatus(pr, l.me), showRepo: showRepo, indent: indent,
			}
		}
		if mine {
			rows = append(rows, l.groupRows(prs, si, sec.Scope, key, prRow)...)
		} else {
			for _, pr := range prs {
				rows = append(rows, prRow(pr, true, 1))
			}
		}
		if more := sec.Total - len(sec.PRs); more > 0 && (sec.Scope.Kind == poller.ScopeOrg || sec.Scope.Kind == poller.ScopePersonal) {
			rows = append(rows, listRow{kind: rowFooter, key: key + "/more", section: si, scope: sec.Scope, more: more})
		}
	}
	return rows
}

// groupRows returns the My PRs rows: one group row for each repository, in
// name order, and the PRs of the repository under it.
func (l *listPane) groupRows(prs []model.PR, si int, scope poller.Scope, key string,
	prRow func(model.PR, bool, int) listRow) []listRow {
	var repos []string
	byRepo := map[string][]model.PR{}
	for _, pr := range prs {
		name := pr.Repo.FullName()
		if _, ok := byRepo[name]; !ok {
			repos = append(repos, name)
		}
		byRepo[name] = append(byRepo[name], pr)
	}
	slices.Sort(repos)
	var rows []listRow
	for _, repo := range repos {
		rows = append(rows, listRow{kind: rowGroup, key: key + "/repo:" + repo, section: si, scope: scope, title: repo})
		for _, pr := range byRepo[repo] {
			rows = append(rows, prRow(pr, false, 2))
		}
	}
	return rows
}

// scroll changes the offset so that the cursor is visible.
func (l *listPane) scroll() {
	h := l.viewHeight()
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+h {
		l.offset = l.cursor - h + 1
	}
	l.offset = max(min(l.offset, len(l.rows)-h), 0)
}

// viewHeight is the number of row lines. The search input takes one line.
func (l *listPane) viewHeight() int {
	if l.searching {
		return max(l.height-1, 1)
	}
	return l.height
}

func (l *listPane) setCursor(i int) {
	if len(l.rows) == 0 {
		return
	}
	l.cursor = max(min(i, len(l.rows)-1), 0)
	l.scroll()
}

func (l *listPane) move(n int) { l.setCursor(l.cursor + n) }

// jumpSection moves the cursor to the next (dir 1) or previous (dir -1)
// section row.
func (l *listPane) jumpSection(dir int) {
	for i := l.cursor + dir; i >= 0 && i < len(l.rows); i += dir {
		if l.rows[i].kind == rowSection {
			l.setCursor(i)
			return
		}
	}
}

// toggleFold folds or unfolds the section of the cursor row. The cursor
// goes to the section row.
func (l *listPane) toggleFold() {
	if l.cursor >= len(l.rows) {
		return
	}
	key := sectionKey(l.rows[l.cursor].scope)
	l.folded[key] = !l.folded[key]
	idx := slices.IndexFunc(l.rows, func(r listRow) bool { return r.key == key })
	l.cursor = max(idx, 0)
	l.rebuild()
}

// matches reports whether a PR row matches the search query. The match
// ignores case and looks at the repository, the number, the title, and the
// author.
func (r listRow) matches(query string) bool {
	if r.kind != rowPR || query == "" {
		return false
	}
	q := strings.ToLower(query)
	for _, s := range []string{r.pr.Repo.FullName(), "#" + strconv.Itoa(r.pr.Number), r.pr.Title, r.pr.Author.Login} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// findMatch moves the cursor to the next (dir 1) or previous (dir -1) row
// that matches the query. The search wraps. It reports whether a row
// matched.
func (l *listPane) findMatch(dir int) bool {
	n := len(l.rows)
	for step := 1; step <= n; step++ {
		i := ((l.cursor+dir*step)%n + n) % n
		if l.rows[i].matches(l.query) {
			l.setCursor(i)
			return true
		}
	}
	return false
}

// authors returns the authors of the PRs in the sections that the filters
// apply to, in login order.
func (l *listPane) authors() []string {
	if l.snapshot == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, sec := range l.snapshot.Sections {
		if sec.Scope.Kind == poller.ScopeMine {
			continue
		}
		for _, pr := range sec.PRs {
			login := pr.Author.Login
			if login != "" && !seen[strings.ToLower(login)] {
				seen[strings.ToLower(login)] = true
				out = append(out, login)
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// view renders the visible rows for the inner width and height of the pane.
func (l *listPane) view(width int, st Styles, focused bool) string {
	if l.snapshot == nil {
		return st.Muted.Render("Loading pull requests…")
	}
	h := l.viewHeight()
	end := min(l.offset+h, len(l.rows))
	lines := make([]string, 0, h+1)
	for i := l.offset; i < end; i++ {
		marker := " "
		if i == l.cursor {
			marker = st.Muted.Render("›")
			if focused {
				marker = st.Title.Render("›")
			}
		}
		lines = append(lines, marker+l.renderRow(l.rows[i], width-1, st))
	}
	if len(l.rows) == 0 {
		lines = append(lines, st.Muted.Render("No pull requests."))
	}
	if l.searching {
		for len(lines) < h {
			lines = append(lines, "")
		}
		lines = append(lines, ansi.Truncate("/"+l.input+"█", width, "…"))
	}
	return strings.Join(lines, "\n")
}

func (l *listPane) renderRow(r listRow, width int, st Styles) string {
	var line string
	switch r.kind {
	case rowSection:
		arrow := "▾"
		if l.folded[r.key] {
			arrow = "▸"
		}
		count := fmt.Sprintf("(%d)", r.loaded)
		if r.shown != r.loaded {
			count = fmt.Sprintf("(%d of %d)", r.shown, r.loaded)
		}
		line = st.Title.Render(arrow+" "+r.title) + " " + st.Muted.Render(count)
	case rowGroup:
		line = "  " + r.title
	case rowFooter:
		line = st.Muted.Render(fmt.Sprintf("  … %d more  [m] load more", r.more))
	default:
		return l.renderPR(r, width, st)
	}
	return ansi.Truncate(line, width, "…")
}

// renderPR renders a PR row: the status icon in its color, the repository
// and number, the title, the author, and the age. The title is cut to fit.
func (l *listPane) renderPR(r listRow, width int, st Styles) string {
	icon := r.status.Icon()
	icon = st.Status[r.status].Render(icon) + strings.Repeat(" ", max(2-lipgloss.Width(icon), 0))
	ref := "#" + strconv.Itoa(r.pr.Number)
	if r.showRepo {
		ref = r.pr.Repo.FullName() + ref
	}
	left := strings.Repeat(" ", r.indent) + icon + " " + st.Status[r.status].Render(ref) + " " + r.pr.Title
	right := st.Muted.Render(r.pr.Author.Login + " " + age(l.now().Sub(r.pr.CreatedAt)))
	room := width - lipgloss.Width(right) - 1
	if room < lipgloss.Width(left) {
		left = ansi.Truncate(left, max(room, 0), "…")
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return ansi.Truncate(left+" "+right, width, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

// age returns a short age: minutes below one hour, hours below two days,
// days after that.
func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 0))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// authorFilterMsg sets the author filter. An empty login shows all authors.
type authorFilterMsg struct{ login string }

// handleListKey handles a key press of the focused list pane.
func (m *Model) handleListKey(msg tea.KeyPressMsg) tea.Cmd {
	l := &m.list
	s := msg.String()
	if p := l.pending; p != "" {
		l.pending = ""
		switch p + s {
		case "gg":
			l.setCursor(0)
			return nil
		case "za":
			l.toggleFold()
			return nil
		}
	}
	switch s {
	case "g", "z":
		l.pending = s
	case "j", "down":
		l.move(1)
	case "k", "up":
		l.move(-1)
	case "G":
		l.setCursor(len(l.rows) - 1)
	case "ctrl+d":
		l.move(max(l.height/2, 1))
	case "ctrl+u":
		l.move(-max(l.height/2, 1))
	case "]":
		l.jumpSection(1)
	case "[":
		l.jumpSection(-1)
	case "/":
		l.searching = true
		l.input = ""
		l.scroll()
	case "n", "N":
		return m.searchNext(s == "n")
	case "a":
		l.filters.HideApproved = !l.filters.HideApproved
		m.filtersChanged()
	case "d":
		l.filters.HideDrafts = !l.filters.HideDrafts
		m.filtersChanged()
	case "b":
		l.filters.Bots = l.filters.Bots.next()
		m.filtersChanged()
	case "u":
		return PushDialog(newAuthorPicker(l.authors(), l.filters.Author))
	case "m":
		return m.loadMore()
	}
	return nil
}

// handleSearchInput handles a key press while the user types the search
// query. enter confirms the query and moves to the first match. esc
// cancels.
func (m *Model) handleSearchInput(msg tea.KeyPressMsg) tea.Cmd {
	l := &m.list
	switch msg.String() {
	case "enter":
		l.searching = false
		l.scroll()
		if l.input == "" {
			return nil
		}
		l.query = l.input
		return m.searchNext(true)
	case "esc":
		l.searching = false
		l.input = ""
		l.scroll()
	case "backspace":
		if r := []rune(l.input); len(r) > 0 {
			l.input = string(r[:len(r)-1])
		}
	default:
		l.input += msg.Text
	}
	return nil
}

func (m *Model) searchNext(forward bool) tea.Cmd {
	l := &m.list
	if l.query == "" {
		return nil
	}
	dir := 1
	if !forward {
		dir = -1
	}
	if !l.findMatch(dir) {
		return m.status.SetMessage("no match: " + l.query)
	}
	return nil
}

func (m *Model) filtersChanged() {
	m.list.rebuild()
	m.status.Filters = m.list.filters.String()
}

// loadMore asks the poller for the next page of the section of the cursor
// row.
func (m *Model) loadMore() tea.Cmd {
	l := &m.list
	if l.cursor >= len(l.rows) || l.snapshot == nil {
		return nil
	}
	r := l.rows[l.cursor]
	sec := l.snapshot.Sections[r.section]
	paged := sec.Scope.Kind == poller.ScopeOrg || sec.Scope.Kind == poller.ScopePersonal
	if !paged || sec.Total <= len(sec.PRs) {
		return m.status.SetMessage("all PRs of " + sectionTitle(sec.Scope) + " are loaded")
	}
	if m.poller != nil {
		m.poller.LoadMore(sec.Scope)
	}
	return m.status.SetMessage("loading more PRs of " + sectionTitle(sec.Scope) + "…")
}

// pickerRows is the number of entries that the author picker shows.
const pickerRows = 12

// authorPicker is the dialog of the `u` key. The first entry clears the
// author filter.
type authorPicker struct {
	entries []string
	cursor  int
}

func newAuthorPicker(authors []string, current string) *authorPicker {
	d := &authorPicker{entries: append([]string{""}, authors...)}
	if i := slices.IndexFunc(d.entries, func(a string) bool { return strings.EqualFold(a, current) }); i > 0 {
		d.cursor = i
	}
	return d
}

func (d *authorPicker) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	switch msg.String() {
	case "j", "down":
		d.cursor = min(d.cursor+1, len(d.entries)-1)
	case "k", "up":
		d.cursor = max(d.cursor-1, 0)
	case "enter":
		login := d.entries[d.cursor]
		return d, tea.Batch(CloseDialog(), func() tea.Msg { return authorFilterMsg{login: login} }), true
	default:
		return d, nil, false
	}
	return d, nil, true
}

func (d *authorPicker) View(int) string {
	lines := []string{"Author filter", ""}
	start := max(min(d.cursor-pickerRows/2, len(d.entries)-pickerRows), 0)
	end := min(start+pickerRows, len(d.entries))
	for i := start; i < end; i++ {
		a := d.entries[i]
		if a == "" {
			a = "all authors"
		}
		marker := "  "
		if i == d.cursor {
			marker = "› "
		}
		lines = append(lines, marker+a)
	}
	lines = append(lines, "", "[j/k] move  [enter] select  [esc] cancel")
	return strings.Join(lines, "\n")
}

func (d *authorPicker) Unsent() bool { return false }
