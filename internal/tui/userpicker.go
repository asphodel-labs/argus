package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/model"
)

// userPickerRows is the number of user rows that the user picker shows.
const userPickerRows = 12

// userPickerLoadedMsg carries one load of the candidates of a user picker.
// query is "" for the first load. A first load of the R key also has the
// suggested reviewers.
type userPickerLoadedMsg struct {
	p         *userPicker
	query     string
	suggested []model.Actor
	users     []model.Actor
	total     int
	err       error
}

// userPicker is the multi-select dialog of the R and @ keys. It shows the
// users with a check box. The current selection is checked at the start
// and is always in the rows. Each letter types into the filter, so the
// picker moves with the arrow keys and ctrl+n / ctrl+p.
type userPicker struct {
	title string
	// exclude is a login that the picker does not offer, or "".
	exclude string
	muted   lipgloss.Style

	// initial holds the ids of the current selection.
	initial map[string]bool
	// users holds the candidates in row order. checked holds the ids of
	// the checked users.
	users   []model.Actor
	checked map[string]bool
	// kept holds the teams and the bots that the action keeps. They are
	// not rows.
	kept []model.Actor

	filter  string
	matches []int
	cursor  int

	loading bool
	loadErr error
	// total is the count of all assignable users. loaded is the count of
	// the first load. A filter starts a GitHub query when total > loaded.
	total, loaded int
	// searched holds the query of each GitHub query: false while the query
	// runs, true after its result.
	searched map[string]bool

	sending bool
	err     error

	// load starts a load of the candidates for the query.
	load func(p *userPicker, query string) tea.Cmd
	// apply starts the action for the change of the selection.
	apply func(p *userPicker, added, removed []model.Actor) tea.Cmd
	// noChange closes the picker when the selection did not change.
	noChange func() tea.Cmd
}

// newUserPicker returns a picker with current checked and the first load
// in progress.
func newUserPicker(title, exclude string, current, kept []model.Actor, muted lipgloss.Style) *userPicker {
	p := &userPicker{
		title: title, exclude: exclude, muted: muted, kept: kept,
		initial: map[string]bool{}, checked: map[string]bool{}, searched: map[string]bool{},
		loading: true,
	}
	for _, u := range current {
		p.initial[u.ID] = true
		p.checked[u.ID] = true
	}
	p.users = p.merge(current)
	p.refilter()
	return p
}

// merge returns the users of the lists in order, with no duplicate id
// and without the excluded login. The current selection is never left
// out.
func (p *userPicker) merge(lists ...[]model.Actor) []model.Actor {
	var out []model.Actor
	seen := map[string]bool{}
	for _, list := range lists {
		for _, u := range list {
			if u.ID == "" || seen[u.ID] {
				continue
			}
			if !p.initial[u.ID] && p.exclude != "" && strings.EqualFold(u.Login, p.exclude) {
				continue
			}
			seen[u.ID] = true
			out = append(out, u)
		}
	}
	return out
}

// filterKey returns the filter as a GitHub query: lower case, with no
// space at the ends.
func (p *userPicker) filterKey() string {
	return strings.ToLower(strings.TrimSpace(p.filter))
}

// refilter finds the rows that match the filter. A row matches if its
// login or its name has the filter, with no case.
func (p *userPicker) refilter() {
	f := p.filterKey()
	p.matches = p.matches[:0]
	for i, u := range p.users {
		if f == "" || strings.Contains(strings.ToLower(u.Login), f) || strings.Contains(strings.ToLower(u.Name), f) {
			p.matches = append(p.matches, i)
		}
	}
	p.cursor = min(p.cursor, max(len(p.matches)-1, 0))
}

// search starts a GitHub query for the filter if the first load did not
// get all the users. It sends each query once.
func (p *userPicker) search() tea.Cmd {
	q := p.filterKey()
	if q == "" || p.loading || p.total <= p.loaded || p.load == nil {
		return nil
	}
	if _, sent := p.searched[q]; sent {
		return nil
	}
	p.searched[q] = false
	return p.load(p, q)
}

// addLoad adds the candidates of a load. It drops the result of a query
// that is not the filter now.
func (p *userPicker) addLoad(msg userPickerLoadedMsg) tea.Cmd {
	if msg.query == "" {
		p.loading = false
		p.total, p.loaded = msg.total, len(msg.users)
		if msg.err != nil {
			p.loadErr = msg.err
		}
		p.users = p.merge(msg.suggested, p.users, msg.users)
		p.refilter()
		return p.search()
	}
	if msg.query != p.filterKey() {
		delete(p.searched, msg.query)
		return nil
	}
	if msg.err != nil {
		delete(p.searched, msg.query)
		p.loadErr = fmt.Errorf("search %q: %w", msg.query, msg.err)
		return nil
	}
	p.searched[msg.query] = true
	p.users = p.merge(p.users, msg.users)
	p.refilter()
	return nil
}

// diff returns the users that the picker adds to and removes from the
// current selection, in row order.
func (p *userPicker) diff() (added, removed []model.Actor) {
	for _, u := range p.users {
		switch {
		case p.checked[u.ID] && !p.initial[u.ID]:
			added = append(added, u)
		case !p.checked[u.ID] && p.initial[u.ID]:
			removed = append(removed, u)
		}
	}
	return added, removed
}

// checkedUsers returns the checked users in row order.
func (p *userPicker) checkedUsers() []model.Actor {
	var out []model.Actor
	for _, u := range p.users {
		if p.checked[u.ID] {
			out = append(out, u)
		}
	}
	return out
}

func (p *userPicker) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	s := msg.String()
	if p.sending {
		return p, nil, s != "esc" && s != "ctrl+c"
	}
	switch s {
	case "esc", "ctrl+c":
		return p, nil, false
	case "enter":
		added, removed := p.diff()
		if len(added) == 0 && len(removed) == 0 {
			return p, p.noChange(), true
		}
		p.sending, p.err = true, nil
		return p, p.apply(p, added, removed), true
	case "down", "ctrl+n":
		p.cursor = min(p.cursor+1, max(len(p.matches)-1, 0))
	case "up", "ctrl+p":
		p.cursor = max(p.cursor-1, 0)
	case "space":
		if p.cursor < len(p.matches) {
			id := p.users[p.matches[p.cursor]].ID
			p.checked[id] = !p.checked[id]
		}
	case "backspace":
		if r := []rune(p.filter); len(r) > 0 {
			p.filter = string(r[:len(r)-1])
			p.cursor = 0
			p.refilter()
			return p, p.search(), true
		}
	default:
		if msg.Text != "" {
			p.filter += msg.Text
			p.cursor = 0
			p.refilter()
			return p, p.search(), true
		}
	}
	return p, nil, true
}

func (p *userPicker) failed(err error) { p.sending, p.err = false, err }

func (p *userPicker) Unsent() bool {
	added, removed := p.diff()
	return len(added) > 0 || len(removed) > 0
}

func (p *userPicker) View(width int) string {
	width = max(width, 10)
	lines := []string{ansi.Truncate(p.title, width, "…"), ansi.Truncate("/ "+p.filter+"█", width, "…"), ""}
	if p.loading {
		lines = append(lines, "Loading users…")
	}
	if len(p.matches) == 0 && !p.loading {
		lines = append(lines, "No match.")
	}
	n := len(p.matches)
	start := max(min(p.cursor-userPickerRows/2, n-userPickerRows), 0)
	end := min(start+userPickerRows, n)
	for i := start; i < end; i++ {
		u := p.users[p.matches[i]]
		marker := "  "
		if i == p.cursor {
			marker = "› "
		}
		box := "[ ] "
		if p.checked[u.ID] {
			box = "[x] "
		}
		row := marker + box + u.Login
		if u.Name != "" {
			row += " " + p.muted.Render(u.Name)
		}
		lines = append(lines, ansi.Truncate(row, width, "…"))
	}
	if done, sent := p.searched[p.filterKey()]; sent && !done {
		lines = append(lines, "Searching GitHub…")
	}
	var teams, bots []string
	for _, k := range p.kept {
		if k.Typename == model.TypenameTeam {
			teams = append(teams, k.Login)
		} else {
			bots = append(bots, k.Login)
		}
	}
	if len(teams) > 0 || len(bots) > 0 {
		lines = append(lines, "")
	}
	if len(teams) > 0 {
		lines = append(lines, ansi.Wrap("Teams kept: "+strings.Join(teams, ", "), width, ""))
	}
	if len(bots) > 0 {
		lines = append(lines, ansi.Wrap("Bots kept: "+strings.Join(bots, ", "), width, ""))
	}
	if !p.loading && p.total > p.loaded {
		lines = append(lines, p.muted.Render(fmt.Sprintf("%d of %d users loaded; type to search GitHub.", p.loaded, p.total)))
	}
	lines = append(lines, "")
	for _, err := range []error{p.loadErr, p.err} {
		if err != nil {
			lines = append(lines, ansi.Wrap("Error: "+firstLine(err.Error()), width, ""))
		}
	}
	if p.sending {
		lines = append(lines, "Sending…")
	} else {
		lines = append(lines, "[↑/↓] move  [space] toggle  [enter] apply  [esc] cancel")
	}
	return strings.Join(lines, "\n")
}

// handleUserPickerLoaded gives a load result to its picker. The result is
// dropped if the picker is closed.
func (m *Model) handleUserPickerLoaded(msg userPickerLoadedMsg) tea.Cmd {
	if !slices.Contains(m.dialogs, Dialog(msg.p)) {
		return nil
	}
	return msg.p.addLoad(msg)
}

// pickerLoad returns the load function of a picker for pr. suggest adds
// the suggested reviewers to the first load.
func (m *Model) pickerLoad(pr model.PR, suggest bool) func(p *userPicker, query string) tea.Cmd {
	client, ctx := m.github, m.ctx
	return func(p *userPicker, query string) tea.Cmd {
		return func() tea.Msg {
			msg := userPickerLoadedMsg{p: p, query: query}
			var sugErr error
			if suggest && query == "" {
				msg.suggested, sugErr = client.SuggestedReviewers(ctx, pr.ID)
			}
			var err error
			msg.users, msg.total, err = client.AssignableUsers(ctx, pr.Repo.Owner, pr.Repo.Name, query)
			msg.err = errors.Join(sugErr, err)
			return msg
		}
	}
}

// openPicker puts the picker on the dialog stack and starts its first
// load. The push does not go through a command, so the load result
// cannot arrive before the picker is on the stack.
func (m *Model) openPicker(p *userPicker) tea.Cmd {
	p.noChange = func() tea.Cmd { return tea.Batch(CloseDialog(), m.status.SetMessage("no change")) }
	m.dialogs = append(m.dialogs, p)
	return p.load(p, "")
}

// actorIDs returns the ids of the actors.
func actorIDs(actors []model.Actor) []string {
	out := make([]string, 0, len(actors))
	for _, a := range actors {
		out = append(out, a.ID)
	}
	return out
}

// actorLogins returns the logins of the actors, joined with ", ".
func actorLogins(actors []model.Actor) string {
	out := make([]string, 0, len(actors))
	for _, a := range actors {
		out = append(out, a.Login)
	}
	return strings.Join(out, ", ")
}

// requestReview opens the reviewer picker for the detail of the PR. The
// requested users are checked. The requested teams and bots are kept:
// RequestReviews replaces all requested reviewers, so the action sends
// their ids again.
func (m *Model) requestReview(pr model.PR) tea.Cmd {
	ref := prRef(pr)
	if pr.State == "CLOSED" || pr.State == "MERGED" {
		return m.status.SetMessage("cannot request a review: " + ref + " is not open")
	}
	var users, kept []model.Actor
	for _, r := range pr.RequestedReviewers {
		if r.Typename == model.TypenameUser {
			users = append(users, r)
		} else {
			kept = append(kept, r)
		}
	}
	var teamIDs, botIDs []string
	for _, k := range kept {
		if k.Typename == model.TypenameTeam {
			teamIDs = append(teamIDs, k.ID)
		} else {
			botIDs = append(botIDs, k.ID)
		}
	}
	client := m.github
	p := newUserPicker("Request review on "+ref+": "+pr.Title, pr.Author.Login, users, kept, m.styles.Muted)
	p.load = m.pickerLoad(pr, true)
	p.apply = func(p *userPicker, added, removed []model.Actor) tea.Cmd {
		desc := "update reviewers of " + ref
		if len(removed) == 0 {
			desc = "request review from " + actorLogins(added) + " on " + ref
		}
		userIDs := actorIDs(p.checkedUsers())
		return m.action(p, pr.ID, desc, true, func(ctx context.Context) (string, error) {
			return "", client.RequestReviews(ctx, pr.ID, userIDs, teamIDs, botIDs)
		})
	}
	return m.openPicker(p)
}

// assign opens the assignee picker for the detail of the PR. The
// assignees are checked. The action adds the new assignees, then removes
// the unchecked ones.
func (m *Model) assign(pr model.PR) tea.Cmd {
	client, ref := m.github, prRef(pr)
	p := newUserPicker("Assign "+ref+": "+pr.Title, "", pr.Assignees, nil, m.styles.Muted)
	p.load = m.pickerLoad(pr, false)
	p.apply = func(_ *userPicker, added, removed []model.Actor) tea.Cmd {
		var desc string
		switch {
		case len(removed) == 0:
			desc = "assign " + actorLogins(added) + " on " + ref
		case len(added) == 0:
			desc = "unassign " + actorLogins(removed) + " on " + ref
		default:
			desc = "update assignees of " + ref
		}
		addIDs, removeIDs := actorIDs(added), actorIDs(removed)
		return m.action(p, pr.ID, desc, true, func(ctx context.Context) (string, error) {
			if len(addIDs) > 0 {
				if err := client.AddAssignees(ctx, pr.ID, addIDs); err != nil {
					return "", err
				}
			}
			if len(removeIDs) > 0 {
				return "", client.RemoveAssignees(ctx, pr.ID, removeIDs)
			}
			return "", nil
		})
	}
	return m.openPicker(p)
}
