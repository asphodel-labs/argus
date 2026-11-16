package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/model"
)

// Detailer loads the detail of one PR. gh.Client implements it.
type Detailer interface {
	PRDetail(ctx context.Context, owner, repo string, n int) (model.PR, error)
}

// detailTTL is the time that a loaded detail stays in the cache. A new
// selection of the PR in this time uses the cache and sends no query.
const detailTTL = 30 * time.Second

// detailLoadedMsg carries the result of one PRDetail call.
type detailLoadedMsg struct {
	id  string
	pr  model.PR
	err error
	// at is the start time of the call.
	at time.Time
}

// detailEntry is one cached detail.
type detailEntry struct {
	pr model.PR
	at time.Time
}

// detailItem is one foldable block of the conversation: a review thread, a
// PR comment, or a review with a body.
type detailItem struct {
	key string
	// line is the index of the header line in the content lines.
	line int
}

// detailPane is the state of the detail pane: the cache, the selected PR,
// the scroll position, the current item, and the folds.
type detailPane struct {
	me     string
	now    func() time.Time
	styles Styles

	cache   map[string]detailEntry
	loading map[string]bool
	errs    map[string]error
	// fetchedFor is the UpdatedAt of the summary at the start of the last
	// call for each PR. A newer summary starts a new call.
	fetchedFor map[string]time.Time

	sel    model.PR
	hasSel bool

	width, height int
	offset        int
	// cur is the index of the current item, or -1.
	cur int
	// folds holds the fold state that the user set with za. Without an
	// entry, a resolved thread is folded and all other items are open.
	folds   map[string]bool
	pending string

	// The content lines and the items for the width. dirty asks for a new
	// layout.
	lines []string
	items []detailItem
	dirty bool

	// The Glamour renderer for glamourWidth, and the last rendered body.
	renderer     *glamour.TermRenderer
	glamourWidth int
	bodyKey      string
	bodyOut      string
}

func newDetailPane(me string, now func() time.Time, st Styles) detailPane {
	if now == nil {
		now = time.Now
	}
	return detailPane{
		me: me, now: now, styles: st,
		cache: map[string]detailEntry{}, loading: map[string]bool{}, errs: map[string]error{},
		fetchedFor: map[string]time.Time{}, folds: map[string]bool{},
		cur: -1, width: 20, height: 1, dirty: true,
	}
}

func (d *detailPane) setSize(width, height int) {
	if width != d.width {
		d.dirty = true
	}
	d.width, d.height = max(width, 1), max(height, 1)
	d.clamp()
}

// entry returns the cached detail of the selected PR.
func (d *detailPane) entry() (detailEntry, bool) {
	if !d.hasSel {
		return detailEntry{}, false
	}
	e, ok := d.cache[d.sel.ID]
	return e, ok
}

// selectPR makes pr the selected PR. It reports whether the selection
// changed. A new selection starts at the top with no current item.
func (d *detailPane) selectPR(pr model.PR) bool {
	changed := !d.hasSel || d.sel.ID != pr.ID
	d.sel, d.hasSel = pr, true
	if changed {
		d.offset, d.cur, d.pending = 0, -1, ""
		d.dirty = true
	}
	return changed
}

func (d *detailPane) clearSelection() {
	if d.hasSel {
		d.hasSel = false
		d.sel = model.PR{}
		d.offset, d.cur, d.pending = 0, -1, ""
		d.dirty = true
	}
}

// needsFetch reports whether the selected PR needs a PRDetail call. A new
// selection needs one if the cache has no entry younger than detailTTL. A
// kept selection needs one only if the summary is newer than the summary
// of the last call, so that a failed call does not repeat on each message.
func (d *detailPane) needsFetch(changed bool) bool {
	id := d.sel.ID
	if d.loading[id] {
		return false
	}
	e, cached := d.cache[id]
	if changed && (!cached || d.now().Sub(e.at) >= detailTTL) {
		return true
	}
	last, fetched := d.fetchedFor[id]
	return fetched && d.sel.UpdatedAt.After(last)
}

// loaded stores the result of a PRDetail call.
func (d *detailPane) loaded(msg detailLoadedMsg) {
	delete(d.loading, msg.id)
	if msg.err != nil {
		d.errs[msg.id] = msg.err
	} else {
		delete(d.errs, msg.id)
		d.cache[msg.id] = detailEntry{pr: msg.pr, at: msg.at}
	}
	if d.hasSel && d.sel.ID == msg.id {
		d.dirty = true
	}
}

// syncDetail follows the list selection: it selects the PR in the detail
// pane and starts a PRDetail call if the cache cannot serve it.
func (m *Model) syncDetail() tea.Cmd {
	d := &m.detail
	pr, ok := m.list.Selected()
	if !ok {
		d.clearSelection()
		return nil
	}
	changed := d.selectPR(pr)
	if m.detailer == nil || !d.needsFetch(changed) {
		return nil
	}
	id := pr.ID
	d.loading[id] = true
	d.fetchedFor[id] = pr.UpdatedAt
	ctx, det, at := m.ctx, m.detailer, d.now()
	return func() tea.Msg {
		p, err := det.PRDetail(ctx, pr.Repo.Owner, pr.Repo.Name, pr.Number)
		return detailLoadedMsg{id: id, pr: p, err: err, at: at}
	}
}

// handleDetailKey handles a key press of the focused detail pane.
func (m *Model) handleDetailKey(msg tea.KeyPressMsg) tea.Cmd {
	d := &m.detail
	d.layout()
	s := msg.String()
	if p := d.pending; p != "" {
		d.pending = ""
		switch p + s {
		case "gg":
			d.offset = 0
			return nil
		case "za":
			d.toggleFold()
			return nil
		}
	}
	switch s {
	case "g", "z":
		d.pending = s
	case "j", "down":
		d.offset++
	case "k", "up":
		d.offset--
	case "G":
		d.offset = len(d.lines)
	case "ctrl+d":
		d.offset += max(d.height/2, 1)
	case "ctrl+u":
		d.offset -= max(d.height/2, 1)
	case "]":
		d.jumpItem(1)
	case "[":
		d.jumpItem(-1)
	}
	d.clamp()
	return nil
}

// clamp keeps the offset inside the content.
func (d *detailPane) clamp() {
	d.offset = max(min(d.offset, len(d.lines)-d.height), 0)
}

// jumpItem makes the next (dir 1) or previous (dir -1) item current and
// scrolls its header to the top.
func (d *detailPane) jumpItem(dir int) {
	if len(d.items) == 0 {
		return
	}
	d.cur = max(min(d.cur+dir, len(d.items)-1), 0)
	d.dirty = true
	d.layout()
	d.offset = d.items[d.cur].line
}

// toggleFold folds or opens the current item. The header of the item stays
// visible.
func (d *detailPane) toggleFold() {
	if d.cur < 0 || d.cur >= len(d.items) {
		return
	}
	it := d.items[d.cur]
	d.folds[it.key] = !d.isFolded(it.key, d.defaultFold(it.key))
	d.dirty = true
	d.layout()
	if it.line < d.offset {
		d.offset = it.line
	}
	d.clamp()
}

// defaultFold returns the fold state of an item without a user choice. Only
// a resolved thread starts folded.
func (d *detailPane) defaultFold(key string) bool {
	e, ok := d.entry()
	if !ok {
		return false
	}
	for _, t := range e.pr.Threads {
		if t.ID == key {
			return t.IsResolved
		}
	}
	return false
}

func (d *detailPane) isFolded(key string, def bool) bool {
	if f, ok := d.folds[key]; ok {
		return f
	}
	return def
}

// view renders the visible lines for the inner width and height.
func (d *detailPane) view() string {
	st := d.styles
	if !d.hasSel {
		return st.Muted.Render("No PR selected.")
	}
	d.layout()
	if _, ok := d.entry(); !ok {
		ref := d.sel.Repo.FullName() + "#" + strconv.Itoa(d.sel.Number)
		if err := d.errs[d.sel.ID]; err != nil {
			return strings.Join([]string{
				st.Error.Render(ansi.Truncate("Cannot load "+ref, d.width, "…")),
				ansi.Wrap(firstLine(err.Error()), d.width, ""),
				st.Muted.Render(ansi.Truncate("Select the PR again to retry.", d.width, "…")),
			}, "\n")
		}
		return st.Muted.Render(ansi.Truncate("Loading "+ref+"…", d.width, "…"))
	}
	end := min(d.offset+d.height, len(d.lines))
	out := make([]string, 0, end-d.offset)
	for _, line := range d.lines[d.offset:end] {
		out = append(out, ansi.Truncate(line, d.width, "…"))
	}
	return strings.Join(out, "\n")
}

// layout makes the content lines of the cached detail for the width.
func (d *detailPane) layout() {
	if !d.dirty {
		return
	}
	d.dirty = false
	d.lines, d.items = nil, nil
	e, ok := d.entry()
	if !ok {
		d.clamp()
		return
	}
	b := &detailBuilder{d: d, width: d.width}
	b.build(e.pr)
	d.lines, d.items = b.lines, b.items
	if d.cur >= len(d.items) {
		d.cur = len(d.items) - 1
	}
	d.clamp()
}

// renderBody renders the description with Glamour. On a Glamour error it
// returns the plain text.
func (d *detailPane) renderBody(pr model.PR, width int) string {
	key := pr.ID + "\x00" + strconv.Itoa(width) + "\x00" + pr.Body
	if key == d.bodyKey {
		return d.bodyOut
	}
	out := ansi.Wrap(pr.Body, width, "")
	if d.renderer == nil || d.glamourWidth != width {
		r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(styles.DarkStyle), glamour.WithWordWrap(width))
		if err == nil {
			d.renderer, d.glamourWidth = r, width
		}
	}
	if d.renderer != nil && d.glamourWidth == width {
		if r, err := d.renderer.Render(pr.Body); err == nil {
			out = strings.Trim(r, "\n")
		}
	}
	d.bodyKey, d.bodyOut = key, out
	return out
}

// detailBuilder collects the content lines of one PR.
type detailBuilder struct {
	d     *detailPane
	width int
	lines []string
	items []detailItem
}

func (b *detailBuilder) add(lines ...string) {
	for _, l := range lines {
		b.lines = append(b.lines, strings.Split(strings.ReplaceAll(l, "\r", ""), "\n")...)
	}
}

// wrap adds text wrapped to the width, with indent spaces before each line.
func (b *detailBuilder) wrap(text string, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, l := range strings.Split(ansi.Wrap(strings.ReplaceAll(text, "\r", ""), max(b.width-indent, 1), ""), "\n") {
		b.lines = append(b.lines, pad+l)
	}
}

func (b *detailBuilder) build(pr model.PR) {
	st := b.d.styles
	b.wrap(st.Title.Render(pr.Title), 0)
	status := model.DeriveStatus(pr, b.d.me)
	ss := st.Status[status]
	b.add(ss.Render(status.Icon()+" "+status.String()) + st.Muted.Render(fmt.Sprintf(" · %s#%d · %s · into %s",
		pr.Repo.FullName(), pr.Number, pr.Author.Login, pr.BaseRef)))
	b.add("Reviews: " + b.reviewsLine(pr))
	b.add("Merge: " + b.mergeLine(pr))
	b.add("")
	b.checks(pr)
	b.add("", st.Title.Render("Description"))
	if strings.TrimSpace(pr.Body) == "" {
		b.add(st.Muted.Render("No description."))
	} else {
		b.add(b.d.renderBody(pr, b.width))
	}
	b.add("", st.Title.Render(fmt.Sprintf("Threads (%d)", len(pr.Threads))))
	if len(pr.Threads) == 0 {
		b.add(st.Muted.Render("No review threads."))
	}
	for _, t := range pr.Threads {
		head := t.Path
		if t.Line > 0 {
			head += ":" + strconv.Itoa(t.Line)
		}
		extra := fmt.Sprintf("%d comments", len(t.Comments))
		if len(t.Comments) == 1 {
			extra = "1 comment"
		}
		if t.IsResolved {
			extra = "resolved · " + extra
		}
		b.item(t.ID, t.IsResolved, head+" "+st.Muted.Render(extra), func() {
			for _, c := range t.Comments {
				b.comment(c.Author.Login, c.CreatedAt, c.Body, c.Reactions)
			}
		})
	}
	b.conversation(pr)
}

// item adds a foldable block: the header line and, if the block is open,
// the content from body.
func (b *detailBuilder) item(key string, defFold bool, head string, body func()) {
	st := b.d.styles
	folded := b.d.isFolded(key, defFold)
	arrow := "▾"
	if folded {
		arrow = "▸"
	}
	marker := " "
	if len(b.items) == b.d.cur {
		marker = st.Title.Render("›")
	}
	b.items = append(b.items, detailItem{key: key, line: len(b.lines)})
	b.add(marker + arrow + " " + head)
	if !folded {
		body()
	}
}

// comment adds the author line, the wrapped body, and the reactions of one
// thread comment.
func (b *detailBuilder) comment(author string, at time.Time, body string, reactions []model.Reaction) {
	st := b.d.styles
	if author == "" {
		author = "ghost"
	}
	line := "   " + lipgloss.NewStyle().Bold(true).Render(author)
	b.add(line + st.Muted.Render(" · "+age(b.d.now().Sub(at))+" ago"))
	b.commentBody(body, reactions)
}

// commentBody adds the wrapped body and the reactions of one comment.
func (b *detailBuilder) commentBody(body string, reactions []model.Reaction) {
	if strings.TrimSpace(body) != "" {
		b.wrap(body, 5)
	}
	if len(reactions) > 0 {
		parts := make([]string, 0, len(reactions))
		for _, r := range reactions {
			parts = append(parts, fmt.Sprintf("%s %d", reactionEmoji(r.Content), r.Count))
		}
		b.add("     " + b.d.styles.Muted.Render(strings.Join(parts, "  ")))
	}
}

// conversation adds the PR comments and the reviews with a body, in time
// order.
func (b *detailBuilder) conversation(pr model.PR) {
	st := b.d.styles
	type entry struct {
		key, author, verb, body string
		at                      time.Time
		reactions               []model.Reaction
	}
	var entries []entry
	for _, c := range pr.Comments {
		entries = append(entries, entry{key: c.ID, author: c.Author.Login, verb: "commented", body: c.Body, at: c.CreatedAt, reactions: c.Reactions})
	}
	for _, r := range pr.Reviews {
		if strings.TrimSpace(r.Body) == "" || r.State == "PENDING" {
			continue
		}
		entries = append(entries, entry{key: r.ID, author: r.Author.Login, verb: "reviewed: " + reviewWord(r.State), body: r.Body, at: r.SubmittedAt})
	}
	slices.SortStableFunc(entries, func(a, b entry) int { return a.at.Compare(b.at) })

	b.add("", st.Title.Render(fmt.Sprintf("Conversation (%d)", len(entries))))
	if len(entries) == 0 {
		b.add(st.Muted.Render("No comments."))
	}
	for _, e := range entries {
		author := e.author
		if author == "" {
			author = "ghost"
		}
		head := author + " " + e.verb + st.Muted.Render(" · "+age(b.d.now().Sub(e.at))+" ago")
		b.item(e.key, false, head, func() { b.commentBody(e.body, e.reactions) })
	}
}

// reviewsLine returns the review state of each reviewer. An approval or a
// change request stays until the same reviewer approves, asks for changes,
// or GitHub dismisses the review. A comment alone counts only if the
// reviewer has no other state. A pending review is not shown.
func (b *detailBuilder) reviewsLine(pr model.PR) string {
	st := b.d.styles
	var order []string
	state := map[string]string{}
	for _, r := range pr.Reviews {
		login := r.Author.Login
		if login == "" || r.State == "PENDING" {
			continue
		}
		prev, seen := state[login]
		if !seen {
			order = append(order, login)
		}
		if r.State == "COMMENTED" && seen && prev != "COMMENTED" {
			continue
		}
		state[login] = r.State
	}
	if len(order) == 0 {
		return st.Muted.Render("none")
	}
	parts := make([]string, 0, len(order))
	for _, login := range order {
		s := state[login]
		var text string
		switch s {
		case "APPROVED":
			text = st.Status[model.StatusApproved].Render("✓ " + login)
		case "CHANGES_REQUESTED":
			text = st.Status[model.StatusChangesRequested].Render("✎ " + login)
		default:
			text = st.Muted.Render(login + " (" + reviewWord(s) + ")")
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", ")
}

func (b *detailBuilder) mergeLine(pr model.PR) string {
	st := b.d.styles
	var text string
	switch pr.Mergeable {
	case model.MergeableYes:
		text = st.Status[model.StatusApproved].Render("✓ mergeable")
	case model.MergeableConflicting:
		text = st.Status[model.StatusConflict].Render("⊘ conflicts")
	default:
		text = st.Muted.Render("? unknown")
	}
	if pr.MergeStateStatus != "" {
		text += st.Muted.Render(" · " + strings.ToLower(string(pr.MergeStateStatus)))
	}
	return text
}

// checks adds the checks title and one line for each check.
func (b *detailBuilder) checks(pr model.PR) {
	st := b.d.styles
	b.add(st.Title.Render(fmt.Sprintf("Checks (%d)", len(pr.Checks))))
	if len(pr.Checks) == 0 {
		b.add(st.Muted.Render("No checks."))
		return
	}
	for _, c := range pr.Checks {
		icon, s := checkIcon(c, st)
		line := " " + s.Render(icon) + " " + c.Name
		if c.App != "" {
			line += st.Muted.Render(" · " + c.App)
		}
		b.add(line)
	}
}

// checkIcon returns the icon and the style of a check: green for success,
// red for a failure, blue while the check runs, and muted for a neutral or
// skipped check.
func checkIcon(c model.Check, st Styles) (string, lipgloss.Style) {
	if c.Status != "COMPLETED" {
		return "⏳", st.Status[model.StatusPendingReview]
	}
	switch c.Conclusion {
	case "SUCCESS":
		return "✓", st.Status[model.StatusApproved]
	case "NEUTRAL", "SKIPPED", "STALE":
		return "○", st.Muted
	default:
		return "✗", st.Status[model.StatusCIFailed]
	}
}

// reviewWord returns the lower-case words of a review state.
func reviewWord(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	case "COMMENTED":
		return "commented"
	case "DISMISSED":
		return "dismissed"
	default:
		return strings.ToLower(state)
	}
}

// reactionEmoji returns the emoji of a GitHub ReactionContent value.
func reactionEmoji(content string) string {
	switch content {
	case "THUMBS_UP":
		return "👍"
	case "THUMBS_DOWN":
		return "👎"
	case "LAUGH":
		return "😄"
	case "HOORAY":
		return "🎉"
	case "CONFUSED":
		return "😕"
	case "HEART":
		return "❤️"
	case "ROCKET":
		return "🚀"
	case "EYES":
		return "👀"
	default:
		return strings.ToLower(content)
	}
}
