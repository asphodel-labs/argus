package tui

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/asphodel-labs/argus/internal/model"
)

// finderRows is the number of matches that the finder shows.
const finderRows = 12

// finderEntry is one PR in the fuzzy finder, with the keys of its rows in
// the list.
type finderEntry struct {
	pr      model.PR
	section string
	group   string
	row     string
}

// finderPickMsg tells that the user picked a PR in the finder. A PR from
// the GitHub search has no row keys.
type finderPickMsg struct{ entry finderEntry }

// PRSearcher runs a GitHub PR search. gh.Client implements it.
type PRSearcher interface {
	SearchPRs(ctx context.Context, q string, first int) ([]model.PR, error)
}

// lookupResults is the number of search results that a number lookup reads.
// The search also returns PRs whose text has the number.
const lookupResults = 10

// prLookup is a query that names a PR by number: "6505", "#6505",
// "repo#6505", or "owner/repo#6505".
type prLookup struct {
	owner, repo string
	number      int
}

var lookupRE = regexp.MustCompile(`^(?:(?:([A-Za-z0-9-]+)/)?([A-Za-z0-9._-]+)#|#?)([0-9]+)$`)

// parseLookup returns the number lookup of the query, or false if the
// query is not a number lookup.
func parseLookup(query string) (prLookup, bool) {
	sm := lookupRE.FindStringSubmatch(strings.TrimSpace(query))
	if sm == nil {
		return prLookup{}, false
	}
	n, err := strconv.Atoi(sm[3])
	if err != nil || n <= 0 {
		return prLookup{}, false
	}
	return prLookup{owner: sm[1], repo: sm[2], number: n}, true
}

// String returns "#n", "repo#n", or "owner/repo#n".
func (lk prLookup) String() string {
	ref := "#" + strconv.Itoa(lk.number)
	switch {
	case lk.owner != "":
		return lk.owner + "/" + lk.repo + ref
	case lk.repo != "":
		return lk.repo + ref
	}
	return ref
}

// matches reports whether pr has the number, and the repository and the
// owner of the lookup when it gives them. Names match without case.
func (lk prLookup) matches(pr model.PR) bool {
	return pr.Number == lk.number &&
		(lk.repo == "" || strings.EqualFold(pr.Repo.Name, lk.repo)) &&
		(lk.owner == "" || strings.EqualFold(pr.Repo.Owner, lk.owner))
}

// searchQuery returns the GitHub search query of the lookup. Without an
// owner and a repository it searches the orgs and the repositories of me.
// The query has no is:open, so it also finds closed and merged PRs.
func (lk prLookup) searchQuery(orgs []string, me string) string {
	parts := []string{"is:pr", strconv.Itoa(lk.number)}
	if lk.owner != "" {
		return strings.Join(append(parts, "repo:"+lk.owner+"/"+lk.repo), " ")
	}
	for _, org := range orgs {
		parts = append(parts, "org:"+org)
	}
	if me != "" {
		parts = append(parts, "user:"+me)
	}
	return strings.Join(parts, " ")
}

// filter returns the PRs that match the lookup. It drops the PRs that the
// search found only for the number in their text.
func (lk prLookup) filter(prs []model.PR) []model.PR {
	var out []model.PR
	for _, pr := range prs {
		if lk.matches(pr) {
			out = append(out, pr)
		}
	}
	return out
}

// finderSearchDoneMsg carries the result of the GitHub search of a finder.
type finderSearchDoneMsg struct {
	f      *finder
	lookup prLookup
	prs    []model.PR
	err    error
}

// finderEntries returns the PRs that the list shows after the filters, in
// list order. PRs in folded sections and groups are included. A PR that is
// in more than one section is included once, with its first section.
func (l *listPane) finderEntries() []finderEntry {
	if l.snapshot == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []finderEntry
	for _, si := range l.sectionOrder() {
		sec := l.snapshot.Sections[si]
		key := sectionKey(sec.Scope)
		for _, g := range groupByRepo(l.visiblePRs(sec)) {
			for _, pr := range g.prs {
				if seen[pr.ID] {
					continue
				}
				seen[pr.ID] = true
				out = append(out, finderEntry{pr: pr, section: key, group: groupKey(key, g.repo), row: key + "/" + pr.ID})
			}
		}
	}
	return out
}

// fuzzyScore scores pr for the query. Each word of the query must match
// the number, the title, the repository, or the author. A higher score is
// a better match. ok is false if a word does not match.
func fuzzyScore(query string, pr model.PR) (score int, ok bool) {
	fields := []string{
		strings.ToLower(pr.Title), strings.ToLower(pr.Repo.FullName()), strings.ToLower(pr.Author.Login),
	}
	number := strconv.Itoa(pr.Number)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		best := 0
		if digits := strings.TrimPrefix(word, "#"); isDigits(digits) {
			switch {
			case digits == number:
				best = 1000
			case strings.HasPrefix(number, digits):
				best = 300
			}
		}
		for _, f := range fields {
			best = max(best, textScore(word, f))
		}
		if best == 0 {
			return 0, false
		}
		score += best
	}
	return score, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// textScore scores one query word against one lower-case field. A full
// word scores 100, a word prefix 80, a substring 50. A subsequence scores
// 10 to 30: fewer gaps give more. No match scores 0.
func textScore(word, field string) int {
	words := strings.FieldsFunc(field, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	switch {
	case slices.Contains(words, word):
		return 100
	case slices.ContainsFunc(words, func(w string) bool { return strings.HasPrefix(w, word) }):
		return 80
	case strings.Contains(field, word):
		return 50
	}
	// Find the shortest span over all start positions.
	q, f := []rune(word), []rune(field)
	best := -1
	for start := range f {
		if f[start] != q[0] {
			continue
		}
		i := 1
		end := start
		for pos := start + 1; pos < len(f) && i < len(q); pos++ {
			if f[pos] == q[i] {
				i++
				end = pos
			}
		}
		if i == len(q) {
			if gaps := end - start + 1 - len(q); best < 0 || gaps < best {
				best = gaps
			}
		}
	}
	if best < 0 {
		return 0
	}
	return 10 + max(20-best, 0)
}

// finder is the fuzzy finder dialog of the `/` key. After the matches it
// can show the GitHub search row, or the PRs that the search found.
type finder struct {
	entries []finderEntry
	query   string
	matches []finderEntry
	cursor  int

	// search starts the GitHub search of a lookup. It is nil if the search
	// is not available.
	search func(f *finder, lk prLookup) tea.Cmd
	// lookup is the number lookup of the query when no loaded PR matches
	// it. hasLookup is false otherwise.
	lookup    prLookup
	hasLookup bool
	searching bool
	// found holds the PRs of a search with more than one match.
	found []model.PR
}

// newFinder returns the finder with the GitHub search of the model.
func (m *Model) newFinder(entries []finderEntry) *finder {
	f := &finder{entries: entries}
	if m.searcher != nil {
		searcher, ctx := m.searcher, m.ctx
		f.search = func(f *finder, lk prLookup) tea.Cmd {
			q := lk.searchQuery(m.orgs, m.detail.me)
			return func() tea.Msg {
				prs, err := searcher.SearchPRs(ctx, q, lookupResults)
				return finderSearchDoneMsg{f: f, lookup: lk, prs: prs, err: err}
			}
		}
	}
	f.match()
	return f
}

// match sorts the entries that match the query by score. Equal scores
// keep the list order.
func (f *finder) match() {
	type scored struct {
		e     finderEntry
		score int
	}
	lk, isLookup := parseLookup(f.query)
	exact := false
	var all []scored
	for _, e := range f.entries {
		s, ok := fuzzyScore(f.query, e.pr)
		if isLookup && lk.matches(e.pr) {
			// The fuzzy score does not match "repo#n".
			s, ok, exact = max(s, 1000), true, true
		}
		if ok {
			all = append(all, scored{e, s})
		}
	}
	slices.SortStableFunc(all, func(a, b scored) int { return b.score - a.score })
	f.matches = f.matches[:0]
	for _, s := range all {
		f.matches = append(f.matches, s.e)
	}
	f.cursor = 0
	f.lookup, f.hasLookup = lk, isLookup && !exact && f.search != nil
	f.found = nil
}

// extraRows returns the number of rows after the matches: the PRs that the
// search found, or the search row.
func (f *finder) extraRows() int {
	switch {
	case len(f.found) > 0:
		return len(f.found)
	case f.hasLookup:
		return 1
	}
	return 0
}

// rows returns the number of rows.
func (f *finder) rows() int { return len(f.matches) + f.extraRows() }

// Update handles every key but esc and ctrl+c, so that each letter types
// into the query.
func (f *finder) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return f, nil, false
	case "enter":
		return f, f.enter(), true
	case "down", "ctrl+n", "ctrl+j":
		f.cursor = min(f.cursor+1, max(f.rows()-1, 0))
	case "up", "ctrl+p", "ctrl+k":
		f.cursor = max(f.cursor-1, 0)
	case "backspace":
		if r := []rune(f.query); len(r) > 0 {
			f.query = string(r[:len(r)-1])
			f.match()
		}
	default:
		if msg.Text != "" {
			f.query += msg.Text
			f.match()
		}
	}
	return f, nil, true
}

// enter opens the PR of the cursor row, or starts the GitHub search on the
// search row. It does nothing while the search runs.
func (f *finder) enter() tea.Cmd {
	i := f.cursor
	switch {
	case i < len(f.matches):
		e := f.matches[i]
		return tea.Batch(CloseDialog(), func() tea.Msg { return finderPickMsg{entry: e} })
	case len(f.found) > 0:
		e := finderEntry{pr: f.found[i-len(f.matches)]}
		return tea.Batch(CloseDialog(), func() tea.Msg { return finderPickMsg{entry: e} })
	case f.hasLookup && !f.searching:
		f.searching = true
		return f.search(f, f.lookup)
	}
	return nil
}

// rowText returns the text of row i.
func (f *finder) rowText(i int) string {
	if i < len(f.matches) {
		pr := f.matches[i].pr
		return prRef(pr) + " " + pr.Title + " · " + pr.Author.Login
	}
	if len(f.found) > 0 {
		pr := f.found[i-len(f.matches)]
		return prRef(pr) + " " + pr.Title + " · " + strings.ToLower(pr.State)
	}
	if f.searching {
		return "searching…"
	}
	return "search GitHub for " + f.lookup.String()
}

func (f *finder) View(width int) string {
	lines := []string{"Find PR", ansi.Truncate("/ "+f.query+"█", width, "…"), ""}
	rows := f.rows()
	if rows == 0 {
		lines = append(lines, "No match.")
	}
	start := max(min(f.cursor-finderRows/2, rows-finderRows), 0)
	end := min(start+finderRows, rows)
	for i := start; i < end; i++ {
		marker := "  "
		if i == f.cursor {
			marker = "› "
		}
		lines = append(lines, ansi.Truncate(marker+f.rowText(i), width, "…"))
	}
	lines = append(lines, "", "[↑/↓] move  [enter] open  [esc] close")
	return strings.Join(lines, "\n")
}

func (f *finder) Unsent() bool { return false }

// handleFinderPick unfolds the section and the group of the picked PR,
// moves the cursor to it, and opens it. A PR from the GitHub search that
// the list does not show opens with no change of the list cursor.
// The focus moves to the detail pane, as for enter in the list.
func (m *Model) handleFinderPick(e finderEntry) tea.Cmd {
	l := &m.list
	if e.row == "" {
		entries := l.finderEntries()
		i := slices.IndexFunc(entries, func(x finderEntry) bool { return x.pr.ID == e.pr.ID })
		if i < 0 {
			m.focus = FocusDetail
			return tea.Batch(m.openPR(e.pr), m.status.SetMessage("opened "+prRef(e.pr)+" (not in the list)"))
		}
		e = entries[i]
	}
	if !l.reveal(e.row, e.section, e.group) {
		return m.status.SetMessage(prRef(e.pr) + " is not in the list")
	}
	pr, _ := l.Selected()
	m.focus = FocusDetail
	return m.openPR(pr)
}

// handleFinderSearchDone shows the result of the GitHub search. One match
// closes the finder and opens the PR. More matches replace the search row.
// No match and an error keep the finder open with a status message. The
// result is dropped if the finder is closed or the query changed.
func (m *Model) handleFinderSearchDone(msg finderSearchDoneMsg) tea.Cmd {
	f := msg.f
	f.searching = false
	if m.topDialog() != Dialog(f) || !f.hasLookup || f.lookup != msg.lookup {
		return nil
	}
	if msg.err != nil {
		return m.status.SetMessage("search GitHub for " + msg.lookup.String() + ": " + firstLine(msg.err.Error()))
	}
	prs := msg.lookup.filter(msg.prs)
	switch len(prs) {
	case 0:
		if msg.lookup.repo != "" {
			return m.status.SetMessage("no PR " + msg.lookup.String())
		}
		return m.status.SetMessage("no PR " + msg.lookup.String() + " in your orgs or repos")
	case 1:
		m.popDialog()
		return m.handleFinderPick(finderEntry{pr: prs[0]})
	}
	f.found = prs
	return nil
}
