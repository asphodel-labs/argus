package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/asphodel-labs/argus/internal/commands"
	"github.com/asphodel-labs/argus/internal/execx"
	"github.com/asphodel-labs/argus/internal/model"
)

// ciRunCount is the number of runs that one load asks for.
const ciRunCount = 20

// ciDialog shows the recent GitHub Actions runs of the default branch of
// one repository.
type ciDialog struct {
	repo model.Repo
	// branch is the default branch name. It is "" until the first load
	// gives it. A reload then uses the cached name.
	branch string
	runs   []model.WorkflowRun
	cursor int
	// loading is true while a load runs.
	loading bool
	// seq is the number of the newest load. A load result with another
	// number is dropped.
	seq int
	// sending is true while a rerun request runs.
	sending bool
	err     error
	// note is a message about the selected run, or "".
	note   string
	now    func() time.Time
	styles Styles
	// act starts the action of key on run: "f", "F", "L", or "o".
	act func(d *ciDialog, key string, run model.WorkflowRun) tea.Cmd
	// reload starts a new load of the runs.
	reload func(d *ciDialog) tea.Cmd
}

// LogViewer opens a script in a blocking agterm overlay.
// commands.Runner implements it.
type LogViewer interface {
	Overlay(ctx context.Context, title, script string) error
}

// newCIDialog returns the CI dialog of repo. now gives the time for the
// run age.
func newCIDialog(repo model.Repo, now func() time.Time, st Styles) *ciDialog {
	if now == nil {
		now = time.Now
	}
	return &ciDialog{repo: repo, now: now, styles: st}
}

// ciRunsMsg carries the result of one load of a CI dialog.
type ciRunsMsg struct {
	dialog *ciDialog
	seq    int
	branch string
	runs   []model.WorkflowRun
	err    error
}

// ciActionMsg carries the result of one run action of a CI dialog.
type ciActionMsg struct {
	dialog *ciDialog
	desc   string
	err    error
	// reload is true for a rerun. The result then clears the sending flag
	// and starts a new load.
	reload bool
}

func (d *ciDialog) Update(msg tea.KeyPressMsg) (Dialog, tea.Cmd, bool) {
	s := msg.String()
	switch s {
	case "j", "down":
		d.moveCursor(1)
	case "k", "up":
		d.moveCursor(-1)
	case "ctrl+r":
		d.note = ""
		return d, d.reload(d), true
	case "f", "F", "L", "o":
		return d, d.runAction(s), true
	default:
		return d, nil, false
	}
	return d, nil, true
}

// runAction starts the action of key on the selected run. f and F do
// nothing while a rerun request runs, so a second key press sends no
// second request. f and L need a run with failed jobs.
func (d *ciDialog) runAction(key string) tea.Cmd {
	if len(d.runs) == 0 {
		return nil
	}
	run := d.runs[d.cursor]
	rerun := key == "f" || key == "F"
	if rerun && d.sending {
		return nil
	}
	d.note = ""
	if (key == "f" || key == "L") && !run.Failed() {
		d.note = fmt.Sprintf("run #%d has no failed jobs", run.Number)
		return nil
	}
	if rerun {
		d.sending, d.err = true, nil
	}
	return d.act(d, key, run)
}

// moveCursor moves the cursor by delta and keeps it within the runs.
func (d *ciDialog) moveCursor(delta int) {
	d.cursor = max(min(d.cursor+delta, len(d.runs)-1), 0)
	d.note = ""
}

func (d *ciDialog) Unsent() bool { return false }

// loaded applies a load result. On an error the dialog keeps its runs.
func (d *ciDialog) loaded(msg ciRunsMsg) {
	d.loading = false
	if msg.branch != "" {
		d.branch = msg.branch
	}
	if msg.err != nil {
		d.err = msg.err
		return
	}
	d.runs, d.err = msg.runs, nil
	d.cursor = max(min(d.cursor, len(d.runs)-1), 0)
}

func (d *ciDialog) View(width int) string {
	width = max(width, 10)
	title := "CI · " + d.repo.FullName()
	if d.branch != "" {
		title += "@" + d.branch
	}
	lines := []string{ansi.Truncate(title, width, "…"), ""}
	switch {
	case d.loading && len(d.runs) == 0:
		lines = append(lines, "Loading…")
	case len(d.runs) == 0 && d.err == nil:
		lines = append(lines, "no runs on "+d.branch)
	}
	for i, r := range d.runs {
		marker := "  "
		if i == d.cursor {
			marker = "› "
		}
		icon, s := runIcon(r, d.styles)
		text := fmt.Sprintf("#%d %s · %s · %s · %s", r.Number, r.Workflow, r.Title, r.Event, age(d.now().Sub(r.UpdatedAt)))
		lines = append(lines, ansi.Truncate(marker+s.Render(icon)+" "+text, width, "…"))
	}
	lines = append(lines, "")
	if d.err != nil {
		lines = append(lines, ansi.Wrap("Error: "+firstLine(d.err.Error()), width, ""))
	}
	if d.note != "" {
		lines = append(lines, ansi.Wrap(d.note, width, ""))
	}
	switch {
	case d.sending:
		lines = append(lines, "Sending…")
	case d.loading && len(d.runs) > 0:
		lines = append(lines, "Loading…")
	}
	lines = append(lines, ansi.Truncate("[f] failed [F] all [L] log [o] open [ctrl+r] reload [esc] close", width, "…"))
	return strings.Join(lines, "\n")
}

// runIcon returns the icon and the style of a run. It uses the icons and
// the colors of the PR checks.
func runIcon(r model.WorkflowRun, st Styles) (string, lipgloss.Style) {
	return checkIcon(model.Check{Status: strings.ToUpper(r.Status), Conclusion: strings.ToUpper(r.Conclusion)}, st)
}

// openCIDialog puts the CI dialog of repo on the dialog stack and starts
// its first load. The push does not go through a command, so the load
// result cannot arrive before the dialog is on the stack.
func (m *Model) openCIDialog(repo model.Repo) tea.Cmd {
	if m.github == nil {
		return m.status.SetMessage("GitHub actions are not available")
	}
	d := newCIDialog(repo, m.list.now, m.styles)
	d.act, d.reload = m.ciAction, m.loadCI
	m.dialogs = append(m.dialogs, d)
	return m.loadCI(d)
}

// loadCI starts a new load of the runs of d. The load gets the default
// branch name first if the dialog does not have it.
func (m *Model) loadCI(d *ciDialog) tea.Cmd {
	d.seq++
	d.loading = true
	seq, repo, branch := d.seq, d.repo, d.branch
	client, ctx := m.github, m.ctx
	return func() tea.Msg {
		msg := ciRunsMsg{dialog: d, seq: seq, branch: branch}
		if msg.branch == "" {
			b, err := client.DefaultBranch(ctx, repo)
			if err != nil {
				msg.err = err
				return msg
			}
			msg.branch = b
		}
		msg.runs, msg.err = client.BranchRuns(ctx, repo, msg.branch, ciRunCount)
		return msg
	}
}

// handleCIRuns gives a load result to its dialog. The result is dropped if
// the dialog is closed, or if a newer load started after it.
func (m *Model) handleCIRuns(msg ciRunsMsg) {
	if !slices.Contains(m.dialogs, Dialog(msg.dialog)) || msg.seq != msg.dialog.seq {
		return
	}
	msg.dialog.loaded(msg)
}

// ciAction returns the command of the action of key on run. f and F send
// a rerun request, L opens the failed log in an agterm overlay, and o
// opens the run in the browser.
func (m *Model) ciAction(d *ciDialog, key string, run model.WorkflowRun) tea.Cmd {
	ctx, repo := m.ctx, d.repo
	result := func(desc string, reload bool, fn func() error) tea.Cmd {
		return func() tea.Msg {
			return ciActionMsg{dialog: d, desc: desc, err: fn(), reload: reload}
		}
	}
	switch key {
	case "f":
		return result(fmt.Sprintf("rerun failed jobs of run #%d", run.Number), true, func() error {
			return m.github.RerunFailedJobs(ctx, repo, run.ID)
		})
	case "F":
		return result(fmt.Sprintf("rerun run #%d", run.Number), true, func() error {
			return m.github.RerunWorkflow(ctx, repo, run.ID)
		})
	case "L":
		if m.logViewer == nil {
			d.err = errors.New("log view is not available")
			return nil
		}
		lv := m.logViewer
		title := fmt.Sprintf("log #%d", run.Number)
		script := "gh run view " + execx.ShellQuote(fmt.Sprint(run.ID)) + " --repo " +
			execx.ShellQuote(repo.FullName()) + " --log-failed | less -R"
		return result(title, false, func() error {
			err := lv.Overlay(ctx, title, script)
			if errors.Is(err, commands.ErrNoAgterm) {
				return errors.New("log view requires agterm")
			}
			return err
		})
	case "o":
		if run.URL == "" {
			d.err = errors.New("the run has no URL")
			return nil
		}
		if m.runner == nil {
			d.err = errors.New("cannot open a browser")
			return nil
		}
		r, name, url := m.runner, m.openCmd, run.URL
		return result(fmt.Sprintf("open run #%d", run.Number), false, func() error {
			return runOpen(ctx, r, name, url)
		})
	}
	return nil
}

// handleCIAction shows the result of a run action. An error shows in the
// dialog, and also in the status bar if the dialog is not the top dialog
// or is closed. A success shows in the status bar. The dialog stays open.
// After a rerun the runs load again.
func (m *Model) handleCIAction(msg ciActionMsg) tea.Cmd {
	d := msg.dialog
	at := slices.Index(m.dialogs, Dialog(d))
	if msg.reload {
		d.sending = false
	}
	if msg.err != nil {
		d.err = msg.err
		if at < 0 || at != len(m.dialogs)-1 {
			return m.status.SetMessage(msg.desc + " failed: " + firstLine(msg.err.Error()))
		}
		return nil
	}
	d.err = nil
	cmds := []tea.Cmd{m.status.SetMessage(msg.desc + ": done")}
	if msg.reload && at >= 0 {
		cmds = append(cmds, m.loadCI(d))
	}
	return tea.Batch(cmds...)
}
