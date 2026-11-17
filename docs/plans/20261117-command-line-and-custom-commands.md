# Command line, draft/ready/close, yank, and custom commands

## Overview
- Add a vim-style command line. The `:` key opens it at the bottom of the screen.
- The built-in commands are `:draft`, `:ready`, and `:close`. They change the opened PR on GitHub.
- The `y` key copies the URL of the opened PR to the clipboard.
- The config can define custom commands that use `git` or `gh`, for example `gh stack sync`. Each custom command runs with `:name`. An optional key in the config is a shortcut for it.
- A custom command runs in the local clone of the PR repository. Its mode is `background` or `overlay`.

## Context (from discovery)
- `internal/tui/actions.go`: `handleActionKey` runs the PR action keys on the opened PR. `m.action` runs a function off the update loop and reports an `actionDoneMsg`. A success loads the detail again.
- `internal/gh/mutations.go`: each mutation is a const string and a method that calls `c.mutate`. The tests use an `httptest` server and recorded JSON.
- `internal/tui/keys.go`: `KeyMap.Actions` holds the help text of the action keys. The used action keys are `A M U R @ f F c r + x o p v`.
- `internal/tui/finder.go`: the `/` finder is a `Dialog` with a typed query. The command line uses the same dialog pattern.
- `internal/config/config.go`: `fileConfig` holds the TOML keys. `Load` checks each value and returns an error for a bad value.
- `internal/review/worktree.go`: `Worktrees.FindClone` returns the clone directory or a `*review.MissingClone`. `CloneNeededMsg` opens the clone prompt.
- `internal/review/revdiff.go`: `OverlayCommand` opens an agterm overlay with `agtermctl session overlay open --block --target <session> --cwd <dir> <command>`.
- `internal/execx`: every external command goes through `execx.Runner`. `execx.ShellQuote` quotes one shell word.
- `model.PR` has `URL`, `Repo`, `Number`, `BaseRef`, and `IsDraft`. It has no head branch name.

## Development Approach
- **testing approach**: Regular (code first, then tests, in the same task).
- Complete each task fully before the next task.
- Make small, focused changes.
- **CRITICAL: every task MUST include new or updated tests** for the code changes in that task.
- **CRITICAL: all tests must pass before the next task starts.**
- **CRITICAL: update this plan file when the scope changes during implementation.**
- Run `make test` after each change. Run `make lint` before a commit.

## Testing Strategy
- `internal/gh`: `httptest` server tests with recorded JSON for the three new mutations.
- `internal/config`: table tests for the `[[commands]]` parse and its errors.
- `internal/commands`: tests with a fake `execx.Runner` that records the arguments.
- `internal/tui`: model tests with fake dependencies, as in `actions` and `mouse_test.go`. The project has no e2e tests.

## Progress Tracking
- Mark completed items with `[x]` immediately.
- Add newly discovered tasks with the ➕ prefix.
- Mark blockers with the ⚠️ prefix.

## Solution Overview
- **Command line.** `:` opens a `commandLine` dialog with one text line at the bottom. `tab` completes a command name. `enter` runs the command. `esc` closes the line. An unknown name shows "unknown command: <name>" in the status bar.
- **Built-in commands.** `:draft`, `:ready`, and `:close` call the new GitHub mutations through `m.action`. They run at once with no confirm dialog, because the user typed the full name. `:close` closes without a merge. A closed PR can open again on GitHub.
- **State checks.** `:draft` on a draft PR shows "already a draft". `:ready` on a ready PR shows "already ready". `:close` on a PR that is not OPEN shows "the PR is not open". GitHub also refuses these cases, but the local message is clearer.
  - The checks read `IsDraft` and `State` from the loaded detail, `m.detail.cache[id].pr`, not from `m.detail.sel`. `sel` changes only with the next poll snapshot (`tui/detail.go` `syncDetail`). `refreshDetail` loads only the detail again. So after `:draft`, `sel.IsDraft` is still false until the next poll.
  - If the detail is not loaded, the command shows "the PR detail is not loaded yet", as `M` does.
- **Yank.** `y` sends `tea.SetClipboard(url)`, which writes OSC 52. Some terminals block OSC 52, so on macOS argus also runs `sh -c 'printf %s "$1" | pbcopy' sh <url>` through `execx.Runner`. This form needs no stdin support in `Runner`. The status bar shows "copied <url>".
- **Custom commands.** The config has a `[[commands]]` table. A command is a Go `text/template` that argus renders with the PR fields. argus quotes each field with `execx.ShellQuote` before the render. A branch name or a title comes from the PR author, so the quotes stop shell injection.
  - The script that runs is `cd <quoted clone dir> && <rendered>`. `execx.Runner` has no working directory parameter, so the `cd` sets it. The `Runner` interface does not change.
- **Modes.** `background` runs `sh -c <script>` through `execx.Runner`. The status bar shows "<name>: done", or the error text.
  - The error text is the last line of stderr. If stderr is empty, it is the last line of stdout. If both are empty, it is "exit code N".
  - `overlay` opens an agterm overlay with `--block`, so the user sees the output and can type input. agterm runs the overlay command with the PATH of the agterm app (`cmd/argus/main.go` `binPath`). So the overlay script starts with `PATH=<quoted PATH of argus>; export PATH;`, and `gh` and `git` resolve as they do in the shell of the user.
  - Outside agterm, an overlay command returns `commands.ErrNoAgterm` with the text "overlay commands require agterm". The text of `review.ErrNoAgterm` names revdiff, so it is not used here.
- **Keys.** A custom key must not overlap a built-in key. `tui.New` returns an error for an overlap, and argus exits with that error at start.
  - The built-in key set is every key of the global bindings and of `KeyMap.List`, `KeyMap.Detail`, and `KeyMap.Actions`. For a sequence such as `gg` or `za`, the set has the first key, `g` or `z`.
  - This rule matters because `handleActionKey` runs before the pane keys and clears the pending prefix. A custom `g` stops `gg` and `za`.

## Technical Details
- Config format:
  ```toml
  [[commands]]
  name = "stack-sync"          # runs with :stack-sync
  key = "S"                    # optional shortcut
  mode = "background"          # "background" (default) or "overlay"
  run = "gh stack sync"        # a text/template, runs with sh -c
  ```
- Template fields, each one shell-quoted: `{{.Owner}}`, `{{.Repo}}`, `{{.Number}}`, `{{.URL}}`, `{{.Branch}}` (the head branch), `{{.Base}}` (the base branch), `{{.Title}}`.
- `config.Command` struct: `Name`, `Key`, `Mode`, `Run` (the parsed template). `Load` checks these rules:
  - The name is not empty, has no space, and is unique.
  - The name is not `draft`, `ready`, or `close`.
  - The mode is `background` or `overlay`.
  - The template parses, and a render with zero values succeeds. This finds an unknown field at load.
  - Each key is unique among the commands.
- The detail query adds `headRefName`. `model.PR` gets `HeadRef string`. A custom command needs the detail, as `M` does, because the summary has no head branch.
- New mutations in `internal/gh/mutations.go`:
  - `ConvertToDraft(ctx, prID)`: `convertPullRequestToDraft(input: {pullRequestId})`.
  - `MarkReady(ctx, prID)`: `markPullRequestReadyForReview(input: {pullRequestId})`.
  - `ClosePR(ctx, prID)`: `closePullRequest(input: {pullRequestId})`.
- New package `internal/commands`:
  - `Runner` struct with `Runner execx.Runner`, `Clones` (the `FindClone` method of `review.Worktrees`), `Agtermctl`, and `SessionID`.
  - `Run(ctx, cmd config.Command, pr model.PR) (string, error)`. It renders the template, finds the clone, then runs the command in the mode of the command. A missing clone returns the `*review.MissingClone` error, and the TUI opens the clone prompt.
- New TUI interface `Commander` with the `Run` method above. `Options` gets `Commands []config.Command` and `Commander Commander`.
- Help: the `PR actions` section gets `:` "command line", `y` "copy URL", and one line for each custom command with a key. The command line lists the command names when it opens.

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code, tests, and docs in this repository.
- **Post-Completion** (no checkboxes): manual checks in a real terminal and on a real PR.

## Implementation Steps

### Task 1: Add the draft, ready, and close mutations

**Files:**
- Modify: `internal/gh/mutations.go`
- Modify: `internal/gh/mutations_test.go`

- [x] add `ConvertToDraft`, `MarkReady`, and `ClosePR` to `internal/gh/mutations.go` with `c.mutate`
- [x] write tests that check the mutation name and the `pullRequestId` input of each call
- [x] write tests for a GraphQL error response of each call
- [x] run `make test`: it must pass before task 2

### Task 2: Add the head branch to the PR detail

**Files:**
- Modify: `internal/model/pr.go`
- Modify: `internal/gh/detail.go`
- Modify: `internal/gh/testdata/detail_full.json`
- Modify: `internal/gh/detail_test.go`

- [x] add `HeadRef string` to `model.PR`
- [x] add `headRefName` to the detail query and set `HeadRef` in the decode
- [x] add `headRefName` to the recorded detail fixture
- [x] assert `HeadRef` in `detail_test.go`
- [x] run `make test`: it must pass before task 3

### Task 3: Parse custom commands in the config

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] add the `config.Command` struct, the `[[commands]]` TOML table, and the `Config.Commands` field
- [x] add the template data struct `config.CommandData` with the seven fields, and a `Render(pr model.PR)` method that quotes each field with `execx.ShellQuote`
- [x] check the load rules from Technical Details in `Load`
- [x] write tests for a valid config with two commands, the default mode, and `Render` output with a branch that has a quote
- [x] write tests for each load error: empty name, duplicate name, built-in name, bad mode, bad template, unknown field, duplicate key
- [x] run `make test`: it must pass before task 4

### Task 4: Run custom commands in background or overlay mode

**Files:**
- Create: `internal/commands/commands.go`
- Create: `internal/commands/commands_test.go`

- [x] create `commands.Runner` with `Run(ctx, cmd, pr)`
- [x] background mode: run `sh -c 'cd <quoted clone> && <rendered>'`. On a non-zero exit, return the last line of stderr, else the last line of stdout, else "exit code N"
- [x] overlay mode: run `agtermctl session overlay open --block --target <session> --cwd <clone> 'PATH=<quoted PATH>; export PATH; cd <quoted clone> && <rendered>'`
- [x] add `commands.ErrNoAgterm` ("overlay commands require agterm"), and return it for an overlay command without a session
- [x] return the `*review.MissingClone` error unchanged when the repo has no clone
- [x] write tests with a fake runner for both modes: they assert the `cd` in the script, and the PATH prefix in the overlay arguments
- [x] write tests for the error text (stderr, stdout only, no output), the missing clone, and the missing agterm session
- [x] run `make test`: it must pass before task 5

### Task 5: Add the yank key

**Files:**
- Modify: `internal/tui/actions.go`
- Modify: `internal/tui/keys.go`
- Create: `internal/tui/yank_test.go`

- [x] handle `y` in `handleActionKey`: send `tea.SetClipboard(pr.URL)`, and on macOS also run `sh -c 'printf %s "$1" | pbcopy' sh <url>` through `execx.Runner`
- [x] add `y` "copy URL" to `KeyMap.Actions`
- [x] write tests: `y` with an opened PR gives the clipboard command and the status message, and `y` with no opened PR shows "open a PR with enter"
- [x] write a test for a `pbcopy` failure: the status bar shows the error and the OSC 52 command still runs
- [x] run `make test`: it must pass before task 6

### Task 6: Add the command line with the built-in commands

**Files:**
- Create: `internal/tui/cmdline.go`
- Create: `internal/tui/cmdline_test.go`
- Modify: `internal/tui/actions.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/keys.go`

- [x] create the `commandLine` dialog: text input, `tab` completion of names, `enter` to run, `esc` to close, a view on the last line
- [x] open it with `:` from the list and the detail pane
- [x] add the `GitHub` interface methods `ConvertToDraft`, `MarkReady`, and `ClosePR`, and run `:draft`, `:ready`, and `:close` through `m.action` with a detail refresh
- [x] add the state checks from Solution Overview. They read the loaded detail, not `m.detail.sel`. Add "unknown command: <name>" for an unknown name
- [x] add `:` "command line" to `KeyMap.Actions`
- [x] write tests: each built-in command calls the fake GitHub with the PR id, the state checks, completion, `esc`, an unknown name, no opened PR, and a detail that is not loaded
- [x] write a test for `:draft`, a detail refresh to a draft, then `:ready`: `:ready` calls GitHub and does not show "already ready"
- [x] write a test for a GitHub error: the status bar shows "<desc> failed: <error>"
- [x] run `make test`: it must pass before task 7

### Task 7: Connect custom commands to the command line and keys

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/actions.go`
- Modify: `internal/tui/cmdline.go`
- Modify: `internal/tui/keys.go`
- Modify: `cmd/argus/main.go`
- Create: `internal/tui/commands_test.go`

- [ ] add the `Commander` interface and the `Options.Commands` and `Options.Commander` fields
- [ ] return an error from `tui.New` when a custom key overlaps a built-in key, and exit with it in `cmd/argus`. Build the built-in set from the `KeyMap`, with the first key of each sequence such as `gg` and `za`
- [ ] run a custom command from `:name` or its key on the loaded detail, through `m.action`, and send `CloneNeededMsg` for a `*review.MissingClone` error
- [ ] add the custom command names to the completion, and the keyed commands to the help
- [ ] create the `commands.Runner` in `cmd/argus/main.go` with the real runner, the worktrees, and the agterm session id
- [ ] write tests: `:name` and the key call the fake commander with the command and the detail PR, the clone prompt opens on a missing clone, and the key overlap error for `A` and for the prefix key `g`
- [ ] write a test for a command error: the status bar shows the last line
- [ ] run `make test`: it must pass before task 8

### Task 8: Verify acceptance criteria
- [ ] verify `:draft`, `:ready`, `:close`, `y`, and custom commands with `:name` and a key
- [ ] verify the edge cases: no opened PR, the detail not loaded, no clone, no agterm, a key overlap
- [ ] run `make test`
- [ ] run `go test -race ./...`
- [ ] run `make lint`

### Task 9: [Final] Update documentation
- [ ] add the command line, `y`, and the `[[commands]]` config with a `gh stack` example to `README.md`
- [ ] add `internal/commands` to the package layout in `CLAUDE.md`
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items that need manual action, for information only*

**Manual verification:**
- Run `:draft` and `:ready` on a test PR in asphodel-labs/argus. Check the PR state on GitHub.
- Run `:close` on a test PR, then open it again on GitHub.
- Press `y` in agterm and in iTerm2, then paste the URL.
- Define `gh stack sync` as an overlay command, and run it on a stacked PR in a real clone.
