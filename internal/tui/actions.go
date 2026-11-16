package tui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/model"
)

// GitHub is the part of gh.Client that the PR actions use.
type GitHub interface {
	Approve(ctx context.Context, prID, body string) error
	Merge(ctx context.Context, prID string, method model.MergeMethod, headSHA string) error
	EnableAutoMerge(ctx context.Context, prID string, method model.MergeMethod, headSHA string) error
	AddComment(ctx context.Context, subjectID, body string) error
	ReplyToThread(ctx context.Context, threadID, body string) error
	AddReaction(ctx context.Context, subjectID, content string) error
	ResolveThread(ctx context.Context, threadID string) error
	UnresolveThread(ctx context.Context, threadID string) error
	RerunFailedJobs(ctx context.Context, repo model.Repo, runID int64) error
	RerunWorkflow(ctx context.Context, repo model.Repo, runID int64) error
}

// MergeMemory keeps the last merge method for each repository.
// state.Store implements it.
type MergeMemory interface {
	MergeMethod(repo string) (model.MergeMethod, bool)
	SetMergeMethod(repo string, method model.MergeMethod) error
}

// openCommand returns the command that opens a URL in the browser.
func openCommand() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// actionDoneMsg carries the result of one PR action.
type actionDoneMsg struct {
	// origin is the dialog that started the action, or nil.
	origin actionDialog
	prID   string
	desc   string
	// note is extra text for the success message.
	note string
	err  error
	// refresh asks for a new detail of the PR after a success.
	refresh bool
}

// action returns a command that runs fn off the update loop and reports
// the result as an actionDoneMsg.
func (m *Model) action(origin actionDialog, prID, desc string, refresh bool, fn func(ctx context.Context) (string, error)) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		note, err := fn(ctx)
		return actionDoneMsg{origin: origin, prID: prID, desc: desc, note: note, err: err, refresh: refresh}
	}
}

// handleActionDone shows the result of an action. On an error the dialog
// that started the action stays open with its input. On a success the
// dialog closes and the detail of the PR loads again.
func (m *Model) handleActionDone(msg actionDoneMsg) tea.Cmd {
	top := m.topDialog()
	fromTop := msg.origin != nil && top == Dialog(msg.origin)
	if msg.err != nil {
		if fromTop {
			msg.origin.failed(msg.err)
			return nil
		}
		return m.status.SetMessage(msg.desc + " failed: " + firstLine(msg.err.Error()))
	}
	if fromTop {
		m.popDialog()
	}
	cmds := []tea.Cmd{m.status.SetMessage(msg.desc + ": done" + msg.note)}
	if msg.refresh {
		cmds = append(cmds, m.refreshDetail(msg.prID))
	}
	return tea.Batch(cmds...)
}

// topDialog returns the top dialog, or nil.
func (m *Model) topDialog() Dialog {
	if n := len(m.dialogs); n > 0 {
		return m.dialogs[n-1]
	}
	return nil
}

// refreshDetail drops the cached detail of the PR and loads it again if
// the PR is selected. The list summary updates with the next poller
// refresh.
func (m *Model) refreshDetail(id string) tea.Cmd {
	d := &m.detail
	delete(d.cache, id)
	delete(d.errs, id)
	if !d.hasSel || d.sel.ID != id {
		return nil
	}
	d.dirty = true
	if m.detailer == nil || d.loading[id] {
		return nil
	}
	return m.fetchDetail(d.sel)
}

// prRef returns "owner/name#n".
func prRef(pr model.PR) string {
	return pr.Repo.FullName() + "#" + strconv.Itoa(pr.Number)
}

// handleActionKey runs the PR action of the key on the selected PR. ok is
// false if the key is not an action key.
func (m *Model) handleActionKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	s := msg.String()
	switch s {
	case "A", "M", "f", "F", "c", "r", "+", "x", "o":
	default:
		return nil, false
	}
	m.list.pending, m.detail.pending = "", ""
	pr, sel := m.list.Selected()
	if !sel {
		return m.status.SetMessage("select a PR first"), true
	}
	if s == "o" {
		return m.openBrowser(pr), true
	}
	if m.github == nil {
		return m.status.SetMessage("GitHub actions are not available"), true
	}
	switch s {
	case "A":
		return m.approve(pr), true
	case "c":
		return m.comment(pr), true
	}
	detail, loaded := m.detail.cache[pr.ID]
	if !loaded {
		return m.status.SetMessage("the PR detail is not loaded yet"), true
	}
	switch s {
	case "M":
		return m.merge(detail.pr), true
	case "f", "F":
		return m.rerun(detail.pr, s == "f"), true
	default:
		return m.itemAction(detail.pr, s), true
	}
}

func (m *Model) approve(pr model.PR) tea.Cmd {
	if strings.EqualFold(pr.Author.Login, m.detail.me) {
		return m.status.SetMessage("you cannot approve your own PR")
	}
	gh, ref := m.github, prRef(pr)
	d := &textDialog{title: "Approve " + ref + ": " + pr.Title, optional: true}
	d.submit = func(d *textDialog, body string) tea.Cmd {
		return m.action(d, pr.ID, "approve "+ref, true, func(ctx context.Context) (string, error) {
			return "", gh.Approve(ctx, pr.ID, body)
		})
	}
	return PushDialog(d)
}

func (m *Model) comment(pr model.PR) tea.Cmd {
	gh, ref := m.github, prRef(pr)
	d := &textDialog{title: "Comment on " + ref + ": " + pr.Title}
	d.submit = func(d *textDialog, body string) tea.Cmd {
		return m.action(d, pr.ID, "comment on "+ref, true, func(ctx context.Context) (string, error) {
			return "", gh.AddComment(ctx, pr.ID, body)
		})
	}
	return PushDialog(d)
}

// merge opens the merge dialog for the detail of the PR. The merge sends
// the head SHA of the detail as expectedHeadOid, so GitHub refuses the
// merge if the head moved after the detail loaded.
func (m *Model) merge(pr model.PR) tea.Cmd {
	repo := pr.Repo.FullName()
	var last model.MergeMethod
	if m.merges != nil {
		last, _ = m.merges.MergeMethod(repo)
	}
	gh, memory, ref := m.github, m.merges, prRef(pr)
	d := newMergeDialog(pr, last)
	d.confirm = func(d *mergeDialog, method model.MergeMethod) tea.Cmd {
		auto := d.auto
		desc := "merge " + ref
		if auto {
			desc = "enable auto-merge for " + ref
		}
		return m.action(d, pr.ID, desc, true, func(ctx context.Context) (string, error) {
			var err error
			if auto {
				err = gh.EnableAutoMerge(ctx, pr.ID, method, pr.HeadSHA)
			} else {
				err = gh.Merge(ctx, pr.ID, method, pr.HeadSHA)
			}
			if err != nil {
				return "", err
			}
			if memory != nil {
				if err := memory.SetMergeMethod(repo, method); err != nil {
					return "; save merge method: " + firstLine(err.Error()), nil
				}
			}
			return "", nil
		})
	}
	return PushDialog(d)
}

// checkFailed reports whether a check completed with a failure. It uses
// the same rule as checkIcon.
func checkFailed(c model.Check) bool {
	if c.Status != "COMPLETED" {
		return false
	}
	switch c.Conclusion {
	case "SUCCESS", "NEUTRAL", "SKIPPED", "STALE":
		return false
	}
	return true
}

// rerunTargets returns the GitHub Actions run ids of the checks to rerun,
// in check order without duplicates, and the names of the other checks.
// failedOnly selects only the failed checks.
func rerunTargets(checks []model.Check, failedOnly bool) (runIDs []int64, unsupported []string) {
	for _, c := range checks {
		if failedOnly && !checkFailed(c) {
			continue
		}
		if !c.Rerunnable || c.WorkflowRunID == 0 {
			unsupported = append(unsupported, c.Name)
			continue
		}
		if !slices.Contains(runIDs, c.WorkflowRunID) {
			runIDs = append(runIDs, c.WorkflowRunID)
		}
	}
	return runIDs, unsupported
}

// rerun reruns the failed jobs (failedOnly) or all jobs of the GitHub
// Actions runs of the PR checks. Other checks get "rerun not supported".
func (m *Model) rerun(pr model.PR, failedOnly bool) tea.Cmd {
	runIDs, unsupported := rerunTargets(pr.Checks, failedOnly)
	note := ""
	if len(unsupported) > 0 {
		note = "; rerun not supported: " + strings.Join(unsupported, ", ")
	}
	if len(runIDs) == 0 {
		switch {
		case len(unsupported) > 0:
			return m.status.SetMessage(strings.TrimPrefix(note, "; "))
		case failedOnly:
			return m.status.SetMessage("no failed checks on " + prRef(pr))
		default:
			return m.status.SetMessage("no checks on " + prRef(pr))
		}
	}
	gh := m.github
	desc := fmt.Sprintf("rerun all jobs of %d run(s) of %s", len(runIDs), prRef(pr))
	if failedOnly {
		desc = fmt.Sprintf("rerun failed jobs of %d run(s) of %s", len(runIDs), prRef(pr))
	}
	return m.action(nil, pr.ID, desc, true, func(ctx context.Context) (string, error) {
		var errs []error
		for _, id := range runIDs {
			var err error
			if failedOnly {
				err = gh.RerunFailedJobs(ctx, pr.Repo, id)
			} else {
				err = gh.RerunWorkflow(ctx, pr.Repo, id)
			}
			errs = append(errs, err)
		}
		return note, errors.Join(errs...)
	})
}

// currentItem returns the current detail item of the PR, or false.
func (m *Model) currentItem() (detailItem, bool) {
	d := &m.detail
	d.layout()
	if d.cur < 0 || d.cur >= len(d.items) {
		return detailItem{}, false
	}
	return d.items[d.cur], true
}

// itemAction runs reply (r), react (+), or resolve (x) on the current
// detail item.
func (m *Model) itemAction(pr model.PR, s string) tea.Cmd {
	it, ok := m.currentItem()
	if !ok {
		return m.status.SetMessage("select a thread or a comment with ] and [ in the detail pane")
	}
	var thread *model.Thread
	if it.kind == itemThread {
		if i := slices.IndexFunc(pr.Threads, func(t model.Thread) bool { return t.ID == it.key }); i >= 0 {
			thread = &pr.Threads[i]
		}
	}
	switch s {
	case "r":
		if thread == nil {
			return m.status.SetMessage("r replies to a review thread; use c for a PR comment")
		}
		return m.reply(pr, *thread)
	case "x":
		if thread == nil {
			return m.status.SetMessage("x resolves a review thread; select a thread")
		}
		return m.resolve(pr, *thread)
	default:
		subject, target := it.key, "the comment"
		if it.kind == itemReview {
			target = "the review"
		}
		if thread != nil {
			last := thread.LastComment()
			if last == nil {
				return m.status.SetMessage("the thread has no comments")
			}
			subject, target = last.ID, "the last comment of "+threadRef(*thread)
		}
		return m.react(pr, subject, target)
	}
}

// threadRef returns "path:line" of a thread, or the path of a file thread.
func threadRef(t model.Thread) string {
	if t.Line > 0 {
		return t.Path + ":" + strconv.Itoa(t.Line)
	}
	return t.Path
}

func (m *Model) reply(pr model.PR, t model.Thread) tea.Cmd {
	gh, ref := m.github, threadRef(t)
	d := &textDialog{title: "Reply to " + ref}
	d.submit = func(d *textDialog, body string) tea.Cmd {
		return m.action(d, pr.ID, "reply to "+ref, true, func(ctx context.Context) (string, error) {
			return "", gh.ReplyToThread(ctx, t.ID, body)
		})
	}
	return PushDialog(d)
}

func (m *Model) resolve(pr model.PR, t model.Thread) tea.Cmd {
	gh, ref := m.github, threadRef(t)
	if t.IsResolved {
		return m.action(nil, pr.ID, "unresolve "+ref, true, func(ctx context.Context) (string, error) {
			return "", gh.UnresolveThread(ctx, t.ID)
		})
	}
	return m.action(nil, pr.ID, "resolve "+ref, true, func(ctx context.Context) (string, error) {
		return "", gh.ResolveThread(ctx, t.ID)
	})
}

func (m *Model) react(pr model.PR, subject, target string) tea.Cmd {
	gh := m.github
	d := &reactionPicker{target: target}
	d.pick = func(d *reactionPicker, content string) tea.Cmd {
		desc := "react " + reactionEmoji(content) + " to " + target
		return m.action(d, pr.ID, desc, true, func(ctx context.Context) (string, error) {
			return "", gh.AddReaction(ctx, subject, content)
		})
	}
	return PushDialog(d)
}

// openBrowser opens the PR URL with the open command through the runner.
func (m *Model) openBrowser(pr model.PR) tea.Cmd {
	if pr.URL == "" {
		return m.status.SetMessage("the PR has no URL")
	}
	if m.runner == nil {
		return m.status.SetMessage("cannot open a browser")
	}
	r, name, url := m.runner, m.openCmd, pr.URL
	return m.action(nil, pr.ID, "open "+prRef(pr), false, func(ctx context.Context) (string, error) {
		return "", runOpen(ctx, r, name, url)
	})
}

func runOpen(ctx context.Context, r execx.Runner, name, url string) error {
	_, stderr, code, err := r.Run(ctx, name, url)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s exited with code %d: %s", name, code, strings.TrimSpace(stderr))
	}
	return nil
}
