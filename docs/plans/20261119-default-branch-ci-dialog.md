# Default-branch CI dialog

## Overview
- Add a dialog that shows the recent GitHub Actions runs of the default branch (main or master) of one repository.
- The `C` key opens the dialog for the repository under the list cursor. The cursor can be on a PR row or on a repo group row.
- In the dialog, the user selects a run and does one of these actions:
  - rerun the failed jobs
  - rerun all jobs
  - open the failed log in an agterm overlay
  - open the run in the browser
- Today argus shows CI only for a PR. A failure on the default branch is visible only on github.com. This dialog shows it in argus.
- The dialog uses the dialog stack, so the list, the detail pane, `followCursor`, and `syncDetail` do not change.

## Context (from discovery)
- `internal/gh/rest.go`: `RerunFailedJobs` and `RerunWorkflow` already exist (REST `actions/runs/{id}/rerun-failed-jobs` and `rerun`).
- `internal/tui/actions.go`:
  - the `GitHub` interface
  - `m.action(origin actionDialog, ...)`, which keeps an error in the origin dialog
  - the PR `rerun` (`f` / `F`)
- `internal/tui/dialogs.go`: the `Dialog` and `actionDialog` interfaces. `app.go` renders the top dialog at `min(width-4, 72)` columns.
- `internal/commands/commands.go`: `Runner.Run` opens an overlay with `agtermctl session overlay open --block --target <session> --cwd <dir> <script>`. The script sets the argus `PATH`, then waits for enter. Outside agterm, it returns `ErrNoAgterm`.
- `internal/tui/list.go`: a repo group row has `title` = `owner/name`. Its PRs give the `model.Repo`.
- `internal/tui/keys.go`: `withCommandKeys` rejects a custom command key that is a built-in key. `C` becomes a built-in key.

## Development Approach
- **testing approach**: Regular (code first, then tests).
- Complete each task fully before you start the next task.
- Make small, focused changes.
- **CRITICAL: every task MUST include new or updated tests** for the code changes in that task.
  - Write unit tests for new functions and for changed functions.
  - Add test cases for new code paths, and update the test cases of changed behaviour.
  - Cover the success scenarios and the error scenarios.
- **CRITICAL: all tests must pass before you start the next task.** There are no exceptions.
- **CRITICAL: update this plan file when the scope changes during implementation.**
- Follow the project rules in `CLAUDE.md`:
  - Tests never call real GitHub.
  - `internal/gh` tests use `httptest` with recorded JSON in `internal/gh/testdata/`.
  - TUI tests use `teatest` and fakes.
  - Run `make test` and `make lint` before each commit.

## Testing Strategy
- Unit tests are required for every task.
- `internal/gh`: an `httptest` server with recorded JSON for the repository and the runs requests.
- `internal/commands`: a fake `execx.Runner` records the `agtermctl` arguments for the log overlay.
- `internal/tui`: dialog unit tests (keys, view, states) and a `teatest` flow: `C`, then select, then `f`, then the fake GitHub records the call.
- The project has no browser e2e tests.

## Progress Tracking
- Mark completed items with `[x]` immediately when they are done.
- Add newly discovered tasks with the ➕ prefix.
- Document issues and blockers with the ⚠️ prefix.
- Keep the plan in sync with the actual work.

## Solution Overview
- `C` in the list pane finds the repository of the cursor row and pushes a `ciDialog`.
  - `handleActionKey` handles `C` before the fixed action key set and before the `hasSel` check. The repo comes from `m.list`, not from the opened PR.
  - A section row or a footer row has no repository. In that case, `C` shows the status message "move the cursor to a PR or a repo".
- The dialog starts in the loading state. A command loads the default branch name and the last 20 runs of that branch.
  - The result arrives as a `ciRunsMsg` that carries the dialog pointer and a load sequence number. `Model.update` gets a `case ciRunsMsg`.
  - A result for a dialog that is not open anymore is dropped.
  - A result with an old sequence number is dropped. Thus an old, slow load cannot overwrite a newer load.
- The dialog does not use `m.action`. On success, `handleActionDone` deletes the origin dialog (`internal/tui/actions.go:105`), but the CI dialog must stay open. The dialog therefore has its own result message `ciActionMsg{dialog, desc, err, reload}`.
- Keys in the dialog:
  - `j` / `k`: select a run.
  - `f`: rerun the failed jobs of the selected run.
  - `F`: rerun all jobs of the selected run.
  - `L`: open the failed log in an agterm overlay.
  - `o`: open the run URL in the browser.
  - `ctrl+r`: load the runs again.
  - `esc`: close the dialog.
- `f` and `F` set the `sending` flag and send the rerun request. The dialog ignores `f` and `F` while the flag is set, so a second key press sends no second POST. The `ciActionMsg` clears the flag.
  - On an error, the error shows in the dialog.
  - On success, the status bar shows "<desc>: done", the dialog stays open, and the runs load again. The new attempt then shows as queued.
- `L` and `o` send a `ciActionMsg` with `reload` = false. The dialog stays open after the overlay or the browser opens.
- `L` runs `gh run view <id> --repo owner/name --log-failed | less -R` in the overlay. The run must have a failed conclusion (`failure`, `timed_out`, or `startup_failure`). For other runs, `L` shows "run #N has no failed jobs" in the dialog. Outside agterm, `L` shows "log view requires agterm".
- The dialog does not poll. The user selected "load on open and ctrl+r" in planning.

## Technical Details
- New type in `internal/model`:
  ```go
  // WorkflowRun is one GitHub Actions run.
  type WorkflowRun struct {
      ID         int64
      Number     int    // run_number
      Attempt    int    // run_attempt
      Workflow   string // name
      Title      string // display_title
      Event      string
      Status     string // queued, in_progress, completed, ...
      Conclusion string // success, failure, cancelled, ... or ""
      HeadSHA    string
      UpdatedAt  time.Time
      URL        string // html_url
  }
  ```
- New in `internal/gh/rest.go`:
  - `DefaultBranch(ctx, repo) (string, error)`: `GET repos/{owner}/{repo}`, field `default_branch`.
  - `BranchRuns(ctx, repo, branch string, n int) ([]model.WorkflowRun, error)`: `GET repos/{owner}/{repo}/actions/runs?branch=<b>&per_page=<n>&exclude_pull_requests=true`.
  - The `branch` filter matches `head_branch`. A PR from a fork branch that is also named `main` matches too, and `exclude_pull_requests` does not remove it. `BranchRuns` therefore decodes `head_repository.full_name` and drops a run whose head repository is not the base repository. It does not filter on `event`, because that hides the `schedule` and `workflow_dispatch` runs on main.
  - Because of this filter, the dialog can show fewer than 20 runs.
- `GitHub` interface: add `DefaultBranch` and `BranchRuns`.
- The dialog caches the default branch name after the first load. A reload then needs only one request.
- Dialog row format: `<status glyph> #<number> <workflow> · <title> · <event> · <age>`, truncated to the dialog width.
  - The glyphs and colors are the same as the PR checks in `detail.go`: success, failure, pending, and other.
  - The title line is `CI · owner/name@<branch>`.
  - The age comes from `now func() time.Time`, which the model gives from `Options.Now`. Thus the tests are deterministic.
- The repo group `listRow` gets a `repo model.Repo` field, which `buildRows` sets. A folded group has no PR rows after it, so `cursorRepo` reads the repo from the row itself.
- Log overlay: add `func (r *Runner) Overlay(ctx context.Context, title, script string) error` to `internal/commands`.
  - It reuses the PATH prefix and the wait-for-enter tail of `Run`.
  - It runs with `--cwd` = the home directory, because `gh run view --repo` needs no clone.
  - `Run` calls the same helper, so the overlay script is in one place.
  - The TUI gets it through a new interface `LogViewer` and a new `Options.LogViewer` field. `Options.Commander` has the interface type `Commander`, so `cmd/argus` passes the same `*commands.Runner` value to both fields.
  - The TUI checks `errors.Is(err, commands.ErrNoAgterm)`, as `launch.go` does for `launch.ErrNoAgterm`.
  - The overlay is blocking, so the call runs in a `tea.Cmd`.
- An error from a load or from an action shows in the dialog. The runs list stays.

## What Goes Where
- **Implementation Steps**: the code, the tests, README, and CLAUDE.md in this repository.
- **Post-Completion**: the manual check against a real repository in agterm.

## Implementation Steps

### Task 1: Add the WorkflowRun model and the gh REST calls

**Files:**
- Modify: `internal/model/pr.go`
- Modify: `internal/gh/rest.go`
- Modify: `internal/gh/rest_test.go`
- Create: `internal/gh/testdata/repo.json`
- Create: `internal/gh/testdata/branch_runs.json`

- [x] add `model.WorkflowRun` and the method `Failed() bool` (conclusion `failure`, `timed_out`, or `startup_failure`)
- [x] add `(*Client).DefaultBranch` and `(*Client).BranchRuns` in `internal/gh/rest.go`, with the error text "<op> owner/name: <err>"
- [x] in `BranchRuns`, drop a run whose `head_repository.full_name` is not the base repository
- [x] write tests: the httptest server checks the paths and the query (`branch`, `per_page`, `exclude_pull_requests`), and the JSON maps to `WorkflowRun`
- [x] write a test: the fixture has a fork `pull_request` run on a branch named `main`, and `BranchRuns` drops it. A `schedule` run stays.
- [x] write tests for the error cases: HTTP 404 on the repository, HTTP 500 on the runs, and an empty `workflow_runs`
- [x] write a table test for `WorkflowRun.Failed`
- [x] run `make test` and `make lint`. They must pass before task 2.

### Task 2: Add the log overlay to the commands runner

**Files:**
- Modify: `internal/commands/commands.go`
- Modify: `internal/commands/commands_test.go`

- [x] move the overlay script wrap (the PATH prefix, the exit code line, and the wait for enter) of `Run` into one helper
- [x] add `(*Runner).Overlay(ctx, title, script string) error`. It returns `ErrNoAgterm` without a session, and the fail text on a non-zero exit.
- [x] write tests: `Overlay` sends the expected `agtermctl session overlay open --block --target ... --cwd <home>` arguments, and the script holds the PATH prefix and the wait
- [x] write tests for the error cases: `ErrNoAgterm` without a session, and a non-zero exit
- [x] check that the current `Run` overlay tests pass unchanged
- [x] run `make test` and `make lint`. They must pass before task 3.

### Task 3: Add the CI dialog: load and view

**Files:**
- Create: `internal/tui/cidialog.go`
- Create: `internal/tui/cidialog_test.go`
- Modify: `internal/tui/actions.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/dialogs_test.go` (the fake GitHub)

- [x] add `DefaultBranch` and `BranchRuns` to the `GitHub` interface and to the test fake
- [x] create `ciDialog` (repo, branch, runs, cursor, loading, seq, sending, err, note, now). It implements `Dialog`.
- [x] add the load command and the `ciRunsMsg{dialog, seq, branch, runs, err}` message
- [x] add `case ciRunsMsg` to `Model.update` in `app.go`. The handler drops a message whose dialog is not on the stack, or whose `seq` is not the current `seq` of the dialog.
- [x] render the view: the title line, the loading text, the run rows with the status glyphs, the selected row, the error line, and the key line `[f] failed [F] all [L] log [o] open [ctrl+r] reload [esc] close`
- [x] handle `j` / `k` / `down` / `up` and `esc` in the dialog. The cursor stays within the bounds of the list.
- [x] write tests: a load fills the rows, a load error shows in the dialog, a result for a closed dialog is dropped, an empty run list shows "no runs on <branch>", and the view truncates to the width
- [x] write a test: two loads finish in the wrong order, and the dialog keeps the result of the newer load
- [x] run `make test` and `make lint`. They must pass before task 4.

### Task 4: Open the dialog with `C` from the list cursor

**Files:**
- Modify: `internal/tui/keys.go`
- Modify: `internal/tui/actions.go` (or the key dispatch in `app.go`)
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/list_test.go`
- Modify: `internal/tui/cidialog_test.go`

- [x] add a `repo model.Repo` field to the group `listRow`, set in `buildRows`
- [x] add `listPane.cursorRepo() (model.Repo, bool)`: a PR row gives `pr.Repo`, and a repo group row gives its `repo` field
- [x] add the `C` binding ("CI of default branch") to `Actions` in `keys.go`. The help and the custom key check pick it up.
- [x] handle `C` in `handleActionKey` before the fixed key set and the `hasSel` check: push a `ciDialog` and start the load. On a section or footer row, show "move the cursor to a PR or a repo".
- [x] write tests for `cursorRepo` on a PR row, an open group row, a folded group row, a section row, and a footer row
- [x] write a test: `C` works when no PR was opened before
- [x] write a teatest flow: `C` on a group row opens the dialog with the runs of that repo
- [x] write a test: a custom command with key `C` is now rejected as a built-in key
- [x] run `make test` and `make lint`. They must pass before task 5.

### Task 5: Add the run actions: rerun, log, open, and reload

**Files:**
- Modify: `internal/tui/cidialog.go`
- Modify: `internal/tui/cidialog_test.go`
- Modify: `internal/tui/app.go` (the `ciActionMsg` case and the `Options.LogViewer` field)
- Modify: `cmd/argus/main.go`

- [x] add `ciActionMsg{dialog, desc, err, reload}` and its `case` in `Model.update`. The handler clears `sending`, shows an error in the dialog, or shows "<desc>: done" in the status bar. When `reload` is true, it starts a new load. It never closes the dialog.
- [x] `f`: set `sending` and run `RerunFailedJobs`. If the run has no failed jobs, show "run #N has no failed jobs" and send no request.
- [x] `F`: set `sending` and run `RerunWorkflow`
- [x] ignore `f` and `F` while `sending` is true
- [x] `L`: if the run has failed jobs, call `LogViewer.Overlay` with the title `log #N` and the script `gh run view <id> --repo owner/name --log-failed | less -R`, both shell-quoted. Map `ErrNoAgterm` to "log view requires agterm".
- [x] `o`: open `run.URL` with `openCommand()` through `m.runner`. `ctrl+r`: load the runs again.
- [x] add the `LogViewer` interface and the `Options` field, and pass the `commands.Runner` in `cmd/argus`
- [x] write tests: `f` and `F` call the fake with the repo and the run id, a rerun error stays in the dialog, and after a success the dialog stays open and the runs load again
- [x] write a test: `f` two times before the result gives one call
- [x] write tests: `L` sends the expected script, `L` on a successful run sends nothing, `L` without agterm shows the message, `o` runs the open command with the URL, and the dialog stays open after `L` and `o`
- [x] run `make test` and `make lint`. They must pass before task 6.

### Task 6: Verify acceptance criteria
- [x] verify that `C` opens the runs of the default branch for a PR row and for a repo group row
- [x] verify that `f`, `F`, `L`, `o`, `ctrl+r`, and `esc` work as the Solution Overview describes
- [x] verify the edge cases: a section row, no runs, a load error, a closed dialog before the load ends, and no agterm
- [x] run the full test suite: `make test` and `go test -race ./...`
- [x] run `make lint`

### Task 7: [Final] Update documentation
- [ ] README.md: add `C` to the features list and to the key table, add a short "Default-branch CI" section, and add a note that `C` is a built-in key now
- [ ] CLAUDE.md: add one line if the implementation finds a new pattern (for example, the dialog result message that carries the dialog pointer)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items that need manual action or an external system. These items have no checkboxes.*

**Manual verification:**
- In agterm, press `C` on a repository that has a failed run on main. Check the rows, then press `L`. The overlay shows the failed log in `less`.
- Press `f` on a failed run. Then press `ctrl+r`. The run shows as queued or in progress with a new attempt.
- Run argus outside agterm. `L` shows "log view requires agterm".
- Check a repository whose default branch is `master`.
