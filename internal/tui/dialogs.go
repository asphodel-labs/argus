package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdragon/argus/internal/model"
)

// actionDialog is a dialog that sends a GitHub action. The dialog stays
// open while the action runs. On an error the root model calls failed, and
// the dialog keeps its input.
type actionDialog interface {
	Dialog
	failed(err error)
}

// paster is a dialog that takes pasted text.
type paster interface {
	paste(text string)
}

// errEmptyText is the error of a text dialog that needs text.
var errEmptyText = errors.New("type a text first")

// textLines is the maximum number of text lines that a text dialog shows.
const textLines = 10

// textDialog is a dialog with a text input: approve, comment, and reply.
// enter adds a new line, ctrl+s sends, and esc cancels. esc does not close
// the dialog while the text is sent: a cancel cannot stop the send, and a
// closed dialog hides its result.
type textDialog struct {
	title string
	// optional is true if the dialog can send an empty text.
	optional bool
	text     []rune
	sending  bool
	err      error
	// note returns a warning under the title, or "". It can be nil.
	note func() string
	// submit starts the action with the trimmed text.
	submit func(d *textDialog, text string) tea.Cmd
	// cancel returns a command for esc, or is nil.
	cancel func() tea.Cmd
}

func (d *textDialog) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	s := msg.String()
	if d.sending {
		// ctrl+c goes to the root model, which asks before it quits.
		return d, nil, s != "ctrl+c"
	}
	if s == "esc" && d.cancel != nil {
		return d, tea.Batch(CloseDialog(), d.cancel()), true
	}
	if s == "esc" || s == "ctrl+c" {
		return d, nil, false
	}
	switch s {
	case "ctrl+s":
		text := strings.TrimSpace(string(d.text))
		if text == "" && !d.optional {
			d.err = errEmptyText
			return d, nil, true
		}
		d.sending, d.err = true, nil
		return d, d.submit(d, text), true
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

func (d *textDialog) paste(text string) {
	if !d.sending {
		d.text = append(d.text, []rune(strings.ReplaceAll(text, "\r", ""))...)
	}
}

func (d *textDialog) failed(err error) { d.sending, d.err = false, err }

func (d *textDialog) Unsent() bool { return strings.TrimSpace(string(d.text)) != "" }

func (d *textDialog) View(width int) string {
	width = max(width, 10)
	lines := []string{ansi.Truncate(d.title, width, "…")}
	if d.note != nil {
		if note := d.note(); note != "" {
			lines = append(lines, ansi.Wrap(note, width, ""))
		}
	}
	lines = append(lines, "")
	body := strings.Split(ansi.Wrap(string(d.text)+"█", width, ""), "\n")
	if len(body) > textLines {
		body = body[len(body)-textLines:]
	}
	lines = append(lines, body...)
	lines = append(lines, "")
	if d.err != nil {
		lines = append(lines, ansi.Wrap("Error: "+firstLine(d.err.Error()), width, ""))
	}
	switch {
	case d.sending:
		lines = append(lines, "Sending…")
	case d.optional:
		lines = append(lines, "[ctrl+s] send (text is optional)  [esc] cancel")
	default:
		lines = append(lines, "[ctrl+s] send  [enter] new line  [esc] cancel")
	}
	return strings.Join(lines, "\n")
}

// mergeLabel returns the words of a merge method.
func mergeLabel(m model.MergeMethod) string {
	switch m {
	case model.MergeMethodMerge:
		return "merge commit"
	case model.MergeMethodSquash:
		return "squash and merge"
	case model.MergeMethodRebase:
		return "rebase and merge"
	default:
		return strings.ToLower(string(m))
	}
}

// mergePlan returns why GitHub cannot merge the PR now, or an empty
// reason. auto is true when branch protection blocks the merge and the
// checks are pending: the dialog then enables auto-merge. GitHub refuses
// auto-merge on a PR that it can merge now, so a PR with pending checks
// that are not required gets a direct merge.
func mergePlan(pr model.PR) (blocked string, auto bool) {
	pending := pr.Rollup == model.CheckPending || pr.Rollup == model.CheckExpected
	switch {
	case pr.State == "CLOSED" || pr.State == "MERGED":
		return "the PR is not open", false
	case pr.IsDraft:
		return "the PR is a draft", false
	case pr.Mergeable == model.MergeableConflicting || pr.MergeStateStatus == "DIRTY":
		return "the PR has merge conflicts", false
	case len(pr.MergeMethods) == 0:
		return "the repository allows no merge method", false
	case pr.MergeStateStatus == "BLOCKED" && pending:
		return "", true
	case pr.MergeStateStatus == "BLOCKED":
		return "branch protection blocks the merge: a required review or check is missing", false
	case pr.MergeStateStatus == "BEHIND":
		return "the head branch is behind the base branch", false
	case pr.Mergeable == model.MergeableUnknown || pr.MergeStateStatus == "UNKNOWN" || pr.MergeStateStatus == "":
		return "GitHub has not computed the merge state yet; refresh and try again", false
	}
	return "", false
}

// mergeDialog is the dialog of the M key. It shows the allowed methods, or
// the reason that blocks the merge.
type mergeDialog struct {
	pr      model.PR
	methods []model.MergeMethod
	cursor  int
	auto    bool
	blocked string
	sending bool
	err     error
	// confirm starts the merge or the auto-merge with method.
	confirm func(d *mergeDialog, method model.MergeMethod) tea.Cmd
}

// newMergeDialog returns the merge dialog for the detail of pr. last is
// the last merge method for the repository, or empty.
func newMergeDialog(pr model.PR, last model.MergeMethod) *mergeDialog {
	d := &mergeDialog{pr: pr, methods: pr.MergeMethods}
	d.blocked, d.auto = mergePlan(pr)
	if i := slices.Index(d.methods, last); i >= 0 {
		d.cursor = i
	}
	return d
}

func (d *mergeDialog) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	if d.sending {
		return d, nil, msg.String() != "esc" && msg.String() != "ctrl+c"
	}
	if d.blocked != "" {
		return d, nil, false
	}
	switch msg.String() {
	case "j", "down", "tab":
		d.cursor = min(d.cursor+1, len(d.methods)-1)
	case "k", "up", "shift+tab":
		d.cursor = max(d.cursor-1, 0)
	case "y", "enter":
		d.sending, d.err = true, nil
		return d, d.confirm(d, d.methods[d.cursor]), true
	default:
		return d, nil, false
	}
	return d, nil, true
}

func (d *mergeDialog) failed(err error) { d.sending, d.err = false, err }

func (d *mergeDialog) Unsent() bool { return false }

func (d *mergeDialog) View(width int) string {
	width = max(width, 10)
	title := fmt.Sprintf("Merge %s#%d: %s", d.pr.Repo.FullName(), d.pr.Number, d.pr.Title)
	lines := []string{ansi.Truncate(title, width, "…"), ""}
	if d.blocked != "" {
		lines = append(lines, ansi.Wrap("Blocked: "+d.blocked, width, ""), "", "[esc] close")
		return strings.Join(lines, "\n")
	}
	if d.auto {
		lines = append(lines, ansi.Wrap("Checks are pending. argus enables auto-merge: GitHub merges when the checks pass.", width, ""), "")
	}
	for i, m := range d.methods {
		marker := "  "
		if i == d.cursor {
			marker = "› "
		}
		lines = append(lines, marker+mergeLabel(m))
	}
	lines = append(lines, "")
	if d.err != nil {
		lines = append(lines, ansi.Wrap("Error: "+firstLine(d.err.Error()), width, ""))
	}
	verb := "merge"
	if d.auto {
		verb = "enable auto-merge"
	}
	if d.sending {
		lines = append(lines, "Sending…")
	} else {
		lines = append(lines, "[j/k] method  [y/enter] "+verb+"  [esc] cancel")
	}
	return strings.Join(lines, "\n")
}

// reactionContents are the GitHub ReactionContent values in picker order.
var reactionContents = []string{"THUMBS_UP", "THUMBS_DOWN", "LAUGH", "HOORAY", "CONFUSED", "HEART", "ROCKET", "EYES"}

// reactionPicker is the dialog of the + key.
type reactionPicker struct {
	target  string
	cursor  int
	sending bool
	err     error
	// pick starts the reaction with content.
	pick func(d *reactionPicker, content string) tea.Cmd
}

func (d *reactionPicker) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	s := msg.String()
	if d.sending {
		return d, nil, s != "esc" && s != "ctrl+c"
	}
	switch s {
	case "j", "down", "l", "right":
		d.cursor = min(d.cursor+1, len(reactionContents)-1)
	case "k", "up", "h", "left":
		d.cursor = max(d.cursor-1, 0)
	case "enter":
		d.sending, d.err = true, nil
		return d, d.pick(d, reactionContents[d.cursor]), true
	default:
		if len(s) == 1 && s[0] >= '1' && int(s[0]-'0') <= len(reactionContents) {
			d.cursor = int(s[0] - '1')
			d.sending, d.err = true, nil
			return d, d.pick(d, reactionContents[d.cursor]), true
		}
		return d, nil, false
	}
	return d, nil, true
}

func (d *reactionPicker) failed(err error) { d.sending, d.err = false, err }

func (d *reactionPicker) Unsent() bool { return false }

func (d *reactionPicker) View(width int) string {
	width = max(width, 10)
	lines := []string{ansi.Truncate("React to "+d.target, width, "…"), ""}
	for i, c := range reactionContents {
		marker := "  "
		if i == d.cursor {
			marker = "› "
		}
		lines = append(lines, fmt.Sprintf("%s%d %s %s", marker, i+1, reactionEmoji(c), strings.ToLower(c)))
	}
	lines = append(lines, "")
	if d.err != nil {
		lines = append(lines, ansi.Wrap("Error: "+firstLine(d.err.Error()), width, ""))
	}
	if d.sending {
		lines = append(lines, "Sending…")
	} else {
		lines = append(lines, "[j/k] move  [enter/1-8] react  [esc] cancel")
	}
	return strings.Join(lines, "\n")
}
