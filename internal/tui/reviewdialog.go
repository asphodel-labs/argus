package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/review"
)

// Reviewer runs revdiff for a PR and submits the annotations as one
// review. review.Revdiff implements it.
type Reviewer interface {
	Prepare(ctx context.Context, pr model.PR) (review.Session, error)
	Open(ctx context.Context, s review.Session) (review.Result, error)
	// Submit returns a note for the status line after a success.
	Submit(ctx context.Context, s review.Session, sub review.Submission) (string, error)
	// Discard removes the annotation file, the body file, and the review
	// record of the session. It does not change GitHub.
	Discard(s review.Session) error
}

// revdiffDoneMsg carries the result of one revdiff run.
type revdiffDoneMsg struct {
	pr      model.PR
	session review.Session
	result  review.Result
	err     error
}

// startReview prepares the worktree and runs revdiff for the PR detail in a
// command. The v key does nothing for a PR while its overlay is open.
func (m *Model) startReview(pr model.PR) tea.Cmd {
	ref := prRef(pr)
	if m.reviewer == nil {
		return m.status.SetMessage("revdiff review is not available")
	}
	if m.reviewing[pr.ID] {
		return m.status.SetMessage("revdiff is already open for " + ref)
	}
	m.reviewing[pr.ID] = true
	ctx, r := m.ctx, m.reviewer
	run := func() tea.Msg {
		s, err := r.Prepare(ctx, pr)
		if err != nil {
			return revdiffDoneMsg{pr: pr, err: err}
		}
		res, err := r.Open(ctx, s)
		return revdiffDoneMsg{pr: pr, session: s, result: res, err: err}
	}
	return tea.Batch(m.status.SetMessage("revdiff: open "+ref), run)
}

// handleRevdiffDone opens the review dialog after exit 10, the clone
// prompt for a missing clone, or shows the error.
func (m *Model) handleRevdiffDone(msg revdiffDoneMsg) tea.Cmd {
	delete(m.reviewing, msg.pr.ID)
	ref := prRef(msg.pr)
	var missing *review.MissingClone
	switch {
	case errors.As(msg.err, &missing):
		return func() tea.Msg { return CloneNeededMsg{Missing: missing} }
	case errors.Is(msg.err, review.ErrNoAgterm):
		return m.status.SetMessage("revdiff review requires agterm")
	case msg.err != nil:
		return m.status.SetMessage("revdiff " + ref + " failed: " + firstLine(msg.err.Error()))
	case !msg.result.Annotated && (m.github == nil || strings.EqualFold(msg.pr.Author.Login, m.detail.me)):
		// GitHub refuses an approval of an own PR.
		return m.status.SetMessage(withWarning("revdiff "+ref+": no annotations", msg.session.Worktree))
	case !msg.result.Annotated:
		return m.approveReviewed(msg.session)
	}
	d := newReviewDialog(msg.session, msg.result, strings.EqualFold(msg.pr.Author.Login, m.detail.me))
	id := msg.pr.ID
	d.headNow = m.headNow(id)
	r, s := m.reviewer, msg.session
	d.submit = func(d *reviewDialog, sub review.Submission) tea.Cmd {
		return m.action(d, id, "review "+ref, true, func(ctx context.Context) (string, error) {
			return r.Submit(ctx, s, sub)
		})
	}
	d.discard = func(d *reviewDialog) tea.Cmd {
		return m.action(d, id, "discard local annotations of "+ref, false, func(context.Context) (string, error) {
			return "", r.Discard(s)
		})
	}
	m.dialogs = append(m.dialogs, d)
	if wt := msg.session.Worktree; wt.Dirty || wt.ExcludeErr != nil {
		return m.status.SetMessage(withWarning("revdiff "+ref+": review dialog open", wt))
	}
	return nil
}

// approveReviewed opens the approve dialog after a revdiff run with no
// annotations. The approval is for the head that revdiff showed. esc sends
// nothing.
func (m *Model) approveReviewed(s review.Session) tea.Cmd {
	gh, pr, ref := m.github, s.PR, prRef(s.PR)
	headNow := m.headNow(pr.ID)
	d := &textDialog{title: "No comments. Approve " + ref + "?", optional: true}
	d.note = func() string {
		if now := headNow(); now != "" && now != s.HeadSHA {
			return headMovedWarning(now, s.HeadSHA)
		}
		return ""
	}
	d.submit = func(d *textDialog, body string) tea.Cmd {
		return m.action(d, pr.ID, "approve "+ref, true, func(ctx context.Context) (string, error) {
			return "", gh.Approve(ctx, pr.ID, body, s.HeadSHA)
		})
	}
	d.cancel = func() tea.Cmd {
		return m.status.SetMessage(withWarning("revdiff "+ref+": no annotations; not approved", s.Worktree))
	}
	m.dialogs = append(m.dialogs, d)
	if wt := s.Worktree; wt.Dirty || wt.ExcludeErr != nil {
		return m.status.SetMessage(withWarning("revdiff "+ref+": approve dialog open", wt))
	}
	return nil
}

// headNow returns a function that gives the head SHA of the PR in the
// newest snapshot, or "".
func (m *Model) headNow(id string) func() string {
	return func() string {
		if pr, ok := m.list.findPR(id); ok {
			return pr.HeadSHA
		}
		return ""
	}
}

// headMovedWarning returns the warning for a PR head that moved from
// reviewed to now after the checkout.
func headMovedWarning(now, reviewed string) string {
	return fmt.Sprintf("⚠ The PR head moved to %s after the checkout. The review applies to %s.",
		shortSHA(now), shortSHA(reviewed))
}

// reviewEvents are the events of the review dialog. GitHub refuses
// Approve and Request changes on an own PR.
var reviewEvents = []gh.ReviewEvent{gh.ReviewComment, gh.ReviewApprove, gh.ReviewRequestChanges}

// reviewEventLabel returns the words of a review event.
func reviewEventLabel(e gh.ReviewEvent) string {
	switch e {
	case gh.ReviewApprove:
		return "Approve"
	case gh.ReviewRequestChanges:
		return "Request changes"
	default:
		return "Comment"
	}
}

// reviewListLines is the maximum number of comment lines that the review
// dialog shows.
const reviewListLines = 8

// reviewDialog shows the revdiff annotations and submits them as one
// review. tab selects the event, enter adds a new line to the body, ctrl+s
// submits, and esc cancels. The annotation file stays after a cancel. esc
// does not close the dialog while the submit runs: the submit keeps the
// session files until it ends, and a cancel cannot stop it. ctrl+x asks,
// and y then discards the annotation file and the review record. It is the
// way out after a submit that is already on GitHub with other content.
type reviewDialog struct {
	session review.Session
	result  review.Result
	events  []gh.ReviewEvent
	event   int
	text    []rune
	sending bool
	// confirming is true while the dialog asks before the discard.
	confirming bool
	// discarding is true while the discard runs. sending is true then too.
	discarding bool
	err        error
	// headNow returns the current head SHA of the PR, or "".
	headNow func() string
	submit  func(d *reviewDialog, sub review.Submission) tea.Cmd
	discard func(d *reviewDialog) tea.Cmd
}

func newReviewDialog(s review.Session, res review.Result, ownPR bool) *reviewDialog {
	events := reviewEvents
	if ownPR {
		events = reviewEvents[:1]
	}
	return &reviewDialog{session: s, result: res, events: events}
}

func (d *reviewDialog) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	s := msg.String()
	if d.sending {
		// ctrl+c goes to the root model, which asks before it quits.
		return d, nil, s != "ctrl+c"
	}
	if s == "ctrl+c" {
		return d, nil, false
	}
	if d.confirming {
		// Only y discards. Each other key keeps the files.
		d.confirming = false
		if s != "y" {
			return d, nil, true
		}
		d.sending, d.discarding, d.err = true, true, nil
		return d, d.discard(d), true
	}
	if s == "esc" {
		return d, nil, false
	}
	switch s {
	case "ctrl+x":
		d.confirming = true
	case "tab":
		d.event = (d.event + 1) % len(d.events)
	case "shift+tab":
		d.event = (d.event + len(d.events) - 1) % len(d.events)
	case "ctrl+s":
		d.sending, d.err = true, nil
		return d, d.submit(d, review.Submission{
			Event:   d.events[d.event],
			Body:    string(d.text),
			Threads: d.result.Threads,
			Moved:   d.result.Moved,
		}), true
	case "enter":
		d.text = append(d.text, '\n')
	case "backspace":
		if n := len(d.text); n > 0 {
			d.text = d.text[:n-1]
		}
	default:
		d.text = append(d.text, []rune(msg.Text)...)
	}
	return d, nil, true
}

func (d *reviewDialog) paste(text string) {
	if !d.sending {
		d.text = append(d.text, []rune(strings.ReplaceAll(text, "\r", ""))...)
	}
}

func (d *reviewDialog) failed(err error) { d.sending, d.discarding, d.err = false, false, err }

func (d *reviewDialog) Unsent() bool { return strings.TrimSpace(string(d.text)) != "" }

// headMoved returns the current head SHA if it is not the head that
// revdiff showed, else "".
func (d *reviewDialog) headMoved() string {
	if d.headNow == nil {
		return ""
	}
	if now := d.headNow(); now != "" && now != d.session.HeadSHA {
		return now
	}
	return ""
}

func (d *reviewDialog) View(width int) string {
	width = max(width, 10)
	pr := d.session.PR
	lines := []string{ansi.Truncate("Review "+prRef(pr)+": "+pr.Title, width, "…")}
	if now := d.headMoved(); now != "" {
		lines = append(lines, ansi.Wrap(headMovedWarning(now, d.session.HeadSHA), width, ""))
	}
	n := len(d.result.Threads) + len(d.result.Moved)
	lines = append(lines, "", fmt.Sprintf("Comments (%d):", n))
	var items []string
	for _, t := range d.result.Threads {
		items = append(items, "  "+threadLocation(t)+"  "+firstLine(t.Body))
	}
	for _, t := range d.result.Moved {
		items = append(items, "  "+threadLocation(t)+"  [outside the diff: moves to the body]  "+firstLine(t.Body))
	}
	if len(items) > reviewListLines {
		more := len(items) - reviewListLines + 1
		items = append(items[:reviewListLines-1], fmt.Sprintf("  … %d more", more))
	}
	for _, it := range items {
		lines = append(lines, ansi.Truncate(it, width, "…"))
	}

	lines = append(lines, "", "Body:")
	body := strings.Split(ansi.Wrap(string(d.text)+"█", width, ""), "\n")
	if len(body) > textLines {
		body = body[len(body)-textLines:]
	}
	lines = append(lines, body...)

	labels := make([]string, len(d.events))
	for i, e := range d.events {
		labels[i] = reviewEventLabel(e)
		if i == d.event {
			labels[i] = "[" + labels[i] + "]"
		}
	}
	lines = append(lines, "", "Event: "+strings.Join(labels, "  "))
	if d.err != nil {
		lines = append(lines, ansi.Wrap("Error: "+firstLine(d.err.Error()), width, ""))
	}
	switch {
	case d.discarding:
		lines = append(lines, "Discarding…")
	case d.sending:
		lines = append(lines, "Submitting…")
	case d.confirming:
		lines = append(lines, ansi.Wrap("Discard the local annotations and the review record of "+prRef(pr)+
			"? argus does not change GitHub. [y] discard  [any other key] keep", width, ""))
	default:
		lines = append(lines, ansi.Wrap("[tab] event  [ctrl+s] submit  [ctrl+x] discard  [enter] new line  [esc] cancel", width, ""))
	}
	return strings.Join(lines, "\n")
}

// threadLocation returns "path:L", "path:A-B", "path:L (-)", or
// "path (file)".
func threadLocation(t gh.ReviewThreadInput) string {
	if t.File {
		return t.Path + " (file)"
	}
	loc := fmt.Sprintf("%s:%d", t.Path, t.Line)
	if t.StartLine != 0 {
		loc = fmt.Sprintf("%s:%d-%d", t.Path, t.StartLine, t.Line)
	}
	if t.Side == gh.SideLeft {
		loc += " (-)"
	}
	return loc
}

// shortSHA returns the first 7 characters of a SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
