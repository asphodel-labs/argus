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

	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/poller"
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

	// group is the key of the repository group row of a PR row.
	group string

	pr     model.PR
	status model.Status
	// stackLine connects the rows of the PRs of one stack: "┌" on the
	// first row, "├" on a middle row, "└" on the last row. It is empty if
	// the row is not next to a row of the same stack.
	stackLine string
}

// listPane is the state of the list pane: the rows, the cursor, the
// filters, and the folds.
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

	// opened is the ID of the PR that the detail pane shows. The list
	// marks its row.
	opened string
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

// findPR returns the newest summary of the PR with the ID from the
// snapshot. It ignores the filters and the folds.
func (l *listPane) findPR(id string) (model.PR, bool) {
	if l.snapshot == nil {
		return model.PR{}, false
	}
	for _, sec := range l.snapshot.Sections {
		for _, pr := range sec.PRs {
			if pr.ID == id {
				return pr, true
			}
		}
	}
	return model.PR{}, false
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

// repoGroup is the PRs of one repository in a section.
type repoGroup struct {
	repo string
	prs  []model.PR
}

// groupKey returns the key of the repository group row in a section.
func groupKey(section, repo string) string { return section + "/repo:" + repo }

// sectionOrder returns the indexes of the snapshot sections in list order.
func (l *listPane) sectionOrder() []int {
	order := make([]int, len(l.snapshot.Sections))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return int(l.snapshot.Sections[a].Scope.Kind) - int(l.snapshot.Sections[b].Scope.Kind)
	})
	return order
}

// visiblePRs returns the PRs of the section that the filters keep. The
// filters do not apply to My PRs.
func (l *listPane) visiblePRs(sec poller.Section) []model.PR {
	mine := sec.Scope.Kind == poller.ScopeMine
	var prs []model.PR
	for _, pr := range sec.PRs {
		if mine || l.filters.Match(pr, l.botLogins) {
			prs = append(prs, pr)
		}
	}
	return prs
}

// groupByRepo groups the PRs by repository. The group with the most
// recently updated PR comes first. Inside a group, the most recently
// updated PR comes first. Equal times keep the snapshot order. The PRs of
// a stack stay together in position order, at the place of the most
// recently updated PR of the stack.
func groupByRepo(prs []model.PR) []repoGroup {
	var groups []repoGroup
	idx := map[string]int{}
	for _, pr := range prs {
		name := pr.Repo.FullName()
		i, ok := idx[name]
		if !ok {
			i = len(groups)
			idx[name] = i
			groups = append(groups, repoGroup{repo: name})
		}
		groups[i].prs = append(groups[i].prs, pr)
	}
	for i := range groups {
		slices.SortStableFunc(groups[i].prs, func(a, b model.PR) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	}
	slices.SortStableFunc(groups, func(a, b repoGroup) int {
		return b.prs[0].UpdatedAt.Compare(a.prs[0].UpdatedAt)
	})
	for i := range groups {
		groups[i].prs = stackOrder(groups[i].prs)
	}
	return groups
}

// stackOrder moves the PRs of each stack next to each other, in position
// order. A stack takes the place of its first PR in prs. The other PRs
// keep their order.
func stackOrder(prs []model.PR) []model.PR {
	out := make([]model.PR, 0, len(prs))
	done := map[string]bool{}
	for i, pr := range prs {
		id := pr.Stack.ID
		if id == "" {
			out = append(out, pr)
			continue
		}
		if done[id] {
			continue
		}
		done[id] = true
		start := len(out)
		for _, p := range prs[i:] {
			if p.Stack.ID == id {
				out = append(out, p)
			}
		}
		slices.SortStableFunc(out[start:], func(a, b model.PR) int { return a.Stack.Position - b.Stack.Position })
	}
	return out
}

// stackLine returns the stack connector of the PR at index i of prs, which
// is in stack order.
func stackLine(prs []model.PR, i int) string {
	id := prs[i].Stack.ID
	if id == "" {
		return ""
	}
	prev := i > 0 && prs[i-1].Stack.ID == id
	next := i < len(prs)-1 && prs[i+1].Stack.ID == id
	switch {
	case prev && next:
		return "├"
	case next:
		return "┌"
	case prev:
		return "└"
	}
	return ""
}

func (l *listPane) buildRows() []listRow {
	if l.snapshot == nil {
		return nil
	}
	var rows []listRow
	for _, si := range l.sectionOrder() {
		sec := l.snapshot.Sections[si]
		key := sectionKey(sec.Scope)
		prs := l.visiblePRs(sec)
		rows = append(rows, listRow{
			kind: rowSection, key: key, section: si, scope: sec.Scope,
			title: sectionTitle(sec.Scope), shown: len(prs), loaded: len(sec.PRs),
		})
		if l.folded[key] {
			continue
		}
		for _, g := range groupByRepo(prs) {
			gk := groupKey(key, g.repo)
			rows = append(rows, listRow{
				kind: rowGroup, key: gk, section: si, scope: sec.Scope, title: g.repo, shown: len(g.prs),
			})
			if l.folded[gk] {
				continue
			}
			for i, pr := range g.prs {
				rows = append(rows, listRow{
					kind: rowPR, key: key + "/" + pr.ID, section: si, scope: sec.Scope, group: gk,
					pr: pr, status: model.DeriveStatus(pr, l.me), stackLine: stackLine(g.prs, i),
				})
			}
		}
		if more := sec.Total - len(sec.PRs); more > 0 && (sec.Scope.Kind == poller.ScopeOrg || sec.Scope.Kind == poller.ScopePersonal) {
			rows = append(rows, listRow{kind: rowFooter, key: key + "/more", section: si, scope: sec.Scope, more: more})
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

// viewHeight is the number of row lines.
func (l *listPane) viewHeight() int { return l.height }

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

// toggleFold folds or unfolds the innermost fold of the cursor row: the
// repository group of a group row or a PR row, or else the section. The
// cursor goes to the row of the fold.
func (l *listPane) toggleFold() {
	if l.cursor >= len(l.rows) {
		return
	}
	r := l.rows[l.cursor]
	key := sectionKey(r.scope)
	switch r.kind {
	case rowGroup:
		key = r.key
	case rowPR:
		key = r.group
	}
	l.folded[key] = !l.folded[key]
	idx := slices.IndexFunc(l.rows, func(r listRow) bool { return r.key == key })
	l.cursor = max(idx, 0)
	l.rebuild()
}

// reveal unfolds the section and the repository group of the PR row key
// and moves the cursor to the row. It reports whether the row exists.
func (l *listPane) reveal(rowKey, section, group string) bool {
	delete(l.folded, section)
	delete(l.folded, group)
	l.rebuild()
	idx := slices.IndexFunc(l.rows, func(r listRow) bool { return r.key == rowKey })
	if idx < 0 {
		return false
	}
	l.setCursor(idx)
	return true
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
		arrow := "▾"
		if l.folded[r.key] {
			arrow = "▸"
		}
		line = "  " + arrow + " " + r.title + " " + st.Muted.Render(fmt.Sprintf("(%d)", r.shown))
	case rowFooter:
		line = st.Muted.Render(fmt.Sprintf("  … %d more  [m] load more", r.more))
	default:
		return l.renderPR(r, width, st)
	}
	return ansi.Truncate(line, width, "…")
}

// renderPR renders a PR row: the open mark, the stack connector, the
// status icon in its color, the number, the stack position, the title, the
// author, and the age. The group row above shows the repository. The title
// is cut to fit.
func (l *listPane) renderPR(r listRow, width int, st Styles) string {
	icon := r.status.Icon()
	icon = st.Status[r.status].Render(icon) + strings.Repeat(" ", max(2-lipgloss.Width(icon), 0))
	ref := "#" + strconv.Itoa(r.pr.Number)
	open, line := " ", " "
	if r.pr.ID == l.opened {
		open = st.Title.Render("●")
	}
	if r.stackLine != "" {
		line = st.Muted.Render(r.stackLine)
	}
	left := "  " + open + line + " " + icon + " " + st.Status[r.status].Render(ref) + " "
	if s := r.pr.Stack; s.InStack() {
		left += st.Muted.Render(fmt.Sprintf("[%d/%d]", s.Position, s.Size)) + " "
	}
	left += r.pr.Title
	right := st.Muted.Render(r.pr.Author.Login + " " + age(l.now().Sub(r.pr.UpdatedAt)))
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
	case "G", "end":
		l.setCursor(len(l.rows) - 1)
	case "home":
		l.setCursor(0)
	case "ctrl+d":
		l.move(max(l.height/2, 1))
	case "ctrl+u":
		l.move(-max(l.height/2, 1))
	case "pgdown":
		l.move(max(l.height, 1))
	case "pgup":
		l.move(-max(l.height, 1))
	case "]":
		l.jumpSection(1)
	case "[":
		l.jumpSection(-1)
	case "enter":
		if pr, ok := l.Selected(); ok {
			m.focus = FocusDetail
			return m.openPR(pr)
		}
	case "space":
		if pr, ok := l.Selected(); ok {
			return m.openPR(pr)
		}
	case "/":
		return PushDialog(m.newFinder(l.finderEntries()))
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
