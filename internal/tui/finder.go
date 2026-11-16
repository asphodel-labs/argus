package tui

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/model"
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

// finderPickMsg tells that the user picked a PR in the finder.
type finderPickMsg struct{ entry finderEntry }

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

// finder is the fuzzy finder dialog of the `/` key.
type finder struct {
	entries []finderEntry
	query   string
	matches []finderEntry
	cursor  int
}

func newFinder(entries []finderEntry) *finder {
	f := &finder{entries: entries}
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
	var all []scored
	for _, e := range f.entries {
		if s, ok := fuzzyScore(f.query, e.pr); ok {
			all = append(all, scored{e, s})
		}
	}
	slices.SortStableFunc(all, func(a, b scored) int { return b.score - a.score })
	f.matches = f.matches[:0]
	for _, s := range all {
		f.matches = append(f.matches, s.e)
	}
	f.cursor = 0
}

// Update handles every key but esc and ctrl+c, so that each letter types
// into the query.
func (f *finder) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return f, nil, false
	case "enter":
		if len(f.matches) == 0 {
			return f, nil, true
		}
		e := f.matches[f.cursor]
		return f, tea.Batch(CloseDialog(), func() tea.Msg { return finderPickMsg{entry: e} }), true
	case "down", "ctrl+n", "ctrl+j":
		f.cursor = min(f.cursor+1, max(len(f.matches)-1, 0))
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

func (f *finder) View(width int) string {
	lines := []string{"Find PR", ansi.Truncate("/ "+f.query+"█", width, "…"), ""}
	if len(f.matches) == 0 {
		lines = append(lines, "No match.")
	}
	start := max(min(f.cursor-finderRows/2, len(f.matches)-finderRows), 0)
	end := min(start+finderRows, len(f.matches))
	for i := start; i < end; i++ {
		pr := f.matches[i].pr
		marker := "  "
		if i == f.cursor {
			marker = "› "
		}
		lines = append(lines, ansi.Truncate(marker+prRef(pr)+" "+pr.Title+" · "+pr.Author.Login, width, "…"))
	}
	lines = append(lines, "", "[↑/↓] move  [enter] open  [esc] close")
	return strings.Join(lines, "\n")
}

func (f *finder) Unsent() bool { return false }

// handleFinderPick unfolds the section and the group of the picked PR,
// moves the cursor to it, and opens it.
func (m *Model) handleFinderPick(e finderEntry) tea.Cmd {
	l := &m.list
	if !l.reveal(e.row, e.section, e.group) {
		return m.status.SetMessage(prRef(e.pr) + " is not in the list")
	}
	pr, _ := l.Selected()
	return m.openPR(pr)
}
