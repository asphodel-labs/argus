# Detail pane follows the cursor, and change marks after a refresh

## Overview
- The detail pane follows the list cursor. When the cursor moves to a PR row, the pane shows the cached detail of that PR.
- If the detail of that PR is not cached, the pane is empty and shows "Press enter to load." Then the pane does not show the detail of the previous PR, which can confuse the user.
- `enter` and `space` load an uncached detail, as now. A cursor move sends no API call.
- A full refresh drops all cached details. Then it loads the detail of the shown PR again.
- A full refresh compares the new snapshot with the previous one. It puts colored marks on the PR rows that changed. The marks of a PR accumulate across refreshes. They stay until the cursor leaves the PR row.
- The marks are in memory only. Each start of argus begins with no marks. The first snapshot is the baseline.

## Context (from discovery)
- `internal/tui/detail.go`: `detailPane` holds `cache`, `loading`, `errs`, and `fetchedFor`. `openPR` selects a PR and fetches the detail if the cache has no entry younger than `detailTTL` (30 s). `syncDetail` runs after each message in `Model.Update`. It loads the detail again when a newer summary arrives. `view` shows "Loading …" for an uncached PR.
- `internal/tui/list.go`: `listPane` holds `cursor`, `rows`, and `opened`, the ID of the PR in the detail pane. `renderPR` draws the row: two spaces, the open mark `●`, the stack connector, the status icon, the number, and the title. `setCursor` and `move` change the cursor. `rebuild` keeps the cursor on the same row key after a new snapshot.
- `internal/tui/mouse.go` `clickList` and `internal/tui/finder.go` `handleFinderPick` also move the cursor and call `openPR`.
- `internal/tui/app.go` `handlePoller` stores each snapshot. A full refresh sets a new `Snapshot.RefreshedAt`. A load more keeps the old value (`internal/poller/poller.go` `refresh` and `loadMore`). So the TUI can tell the two cases apart with no poller change.
- `internal/tui/actions.go` `refreshDetail` drops one cache entry and loads it again after an action.
- `internal/poller/events.go` has the notification diff. It reads only the Needs my review and My PRs sections, and it compares `seen.json` states. The marks need all sections and other fields, so they get their own diff in the TUI.
- `internal/tui/styles.go`: `defaultColors` and `NewStyles` give the named colors. The `[colors]` table of the config overrides them.
- `model.PR` summary fields for the diff: `ID`, `CreatedAt`, `CommentCount`, `ReviewCount`, `ThreadCount`, `NewestAuthor`, `HeadSHA`, `Rollup`, and `ReviewDecision`.

## Development Approach
- **testing approach**: Regular (code first, then tests, in the same task).
- Complete each task fully before the next task.
- Make small, focused changes.
- **CRITICAL: every task MUST include new or updated tests** for the code changes in that task.
- **CRITICAL: all tests must pass before the next task starts.**
- **CRITICAL: update this plan file when the scope changes during implementation.**
- Run `make test` after each change. Run `make lint` before a commit.

## Testing Strategy
- `internal/tui`: model tests with fake dependencies, as in `list_test.go`, `detail_test.go`, and `mouse_test.go`. Use `teatest` where a test needs the full program.
- The diff function is a pure function. Use table tests for it.
- The project has no e2e tests.

## Progress Tracking
- Mark completed items with `[x]` immediately.
- Add newly discovered tasks with the ➕ prefix.
- Mark blockers with the ⚠️ prefix.

## Solution Overview
- **One step after each message.** `Model.Update` already calls `syncDetail` after each message. A new step, `followCursor`, runs before it. It compares the PR under the list cursor with the PR of the previous step. This one place covers the keys, the mouse, the finder, and the row changes after a new snapshot.
- **The pane follows the cursor.** When the cursor is on a PR row, `followCursor` selects that PR in the detail pane and sets `list.opened`. It does not fetch. When the cursor is on a section, group, or footer row, the pane keeps the last PR.
- **Explicit loads.** `enter`, `space`, a mouse click on a PR row, and a finder pick call `openPR`. Each one is an explicit user action, so each one loads an uncached detail. Only the cursor keys and the mouse wheel move the cursor without a load.
- **Action target.** The action keys (`A`, `c`, `o`, `y`, the custom keys, and the `:` commands) act on `m.detail.sel`. After this change, `sel` is the PR under the cursor, or the last PR when the cursor is on a section, group, or footer row. The pane header shows the ref of that PR, so the target is visible. Update the doc comment of `handleActionKey` and the message "open a PR with enter" to match.
- **Cache life.** A cached detail stays until the next full refresh. `detailTTL` goes away. `enter` and `space` fetch only an uncached or failed detail. The `r` key starts a full refresh, and that refresh gives fresh details.
- **Refresh.** On a full refresh, `handlePoller` clears `cache`, `errs`, and `fetchedFor`. It does not clear `loading`, so a running call lands and fills the cache. If the shown PR had a cached detail, `handlePoller` fetches it again after `setSnapshot`, with the new summary from `m.list.findPR`. Then `fetchedFor` records the new `UpdatedAt`, and `syncDetail` starts no second call. A load more snapshot does not clear the cache.
- **No generation number.** A call that runs during a refresh gives data that is at most one call duration older than the snapshot. No incident shows that this age is a problem, so the plan adds no guard for it.
- **Marks.** On a full refresh, a pure function `diffMarks(prev, next, me)` returns the new marks for each PR ID. `listPane.marks` merges them into the marks it already has. The marks of a PR clear when the cursor leaves the PR row. So the user sees the marks while the cursor is on the row.
- **Mark display.** The PR row starts with two spaces now. These become a mark field of four columns, one column for each mark kind in a fixed order. A folded section or group row shows one dot if a hidden PR has a mark.

## Technical Details
- Mark kinds and rules. `prev` and `next` are the summaries of one PR ID in the two snapshots. The first section in list order gives the summary.

  | Mark | Color name | Default | Rule |
  |------|------------|---------|------|
  | New | `mark_new` | `208` (orange) | The ID is not in `prev`, and `CreatedAt` is after `prev.RefreshedAt`. |
  | Activity | `mark_activity` | `33` (blue) | The comment, review, or thread count went up, and `NewestAuthor.Login` is not me. Also: the ID is not in `prev`, and `CreatedAt` is not after `prev.RefreshedAt`. Such a PR only entered the loaded pages, because the search sorts by update time. |
  | Commits | `mark_commits` | `135` (purple) | Both `HeadSHA` values are set, and they differ. |
  | State | `approved` / `failed` | green / red | `Rollup` changed to `SUCCESS` (green), or to `FAILURE` or `ERROR` (red). `ReviewDecision` changed to `APPROVED` (green), or to `CHANGES_REQUESTED` (red). |

- One PR can have a state change in the CI and in the review in one refresh. The red color wins over the green color in one diff. In the merge, the state color of the newest refresh replaces the old one.
- Data types in `internal/tui/marks.go`:
  ```go
  type markSet struct {
      New, Activity, Commits bool
      // State is 0 (none), stateGood, or stateBad.
      State markState
  }
  func diffMarks(prev, next *poller.Snapshot, me string) map[string]markSet
  func (a markSet) merge(b markSet) markSet
  ```
- Row layout, before and after:
  ```
  before: "  " + open + stack + " " + icon + " #12 Title"
  after:  m1 m2 m3 m4 + open + stack + " " + icon + " #12 Title"
  ```
  Each `m` column is `•` in the color of its mark, or a space. The order is New, Activity, Commits, State.
- Flow of `Model.Update`:
  1. `update(msg)` handles the message. For a full refresh snapshot, `handlePoller` runs `diffMarks` against the previous snapshot before it stores the new snapshot. Then it clears the detail cache, calls `setSnapshot`, and starts the fetch of the shown PR with the new summary.
  2. `followCursor()` reads the PR ID under the cursor. If the ID differs from the last ID, it clears the marks of the last ID and selects the new PR in the detail pane.
  3. `syncDetail()` runs as now.
- The empty pane text: `"<owner/name>#<n>  Press enter to load."` in the muted style. The text "Loading …" stays for a running call.

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code changes, tests, and document updates in this repository.
- **Post-Completion** (no checkboxes): manual checks in a real terminal.

## Implementation Steps

### Task 1: Detail pane follows the list cursor

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/detail_test.go`
- Modify: `internal/tui/list_test.go`

- [x] add `followCursor` in `internal/tui/detail.go`: on a new PR under the cursor, call `selectPR` and set `list.opened`, with no fetch
- [x] call `followCursor` in `Model.Update` before `syncDetail`
- [x] change `view` for an uncached PR with no running call and no error: show "<ref>  Press enter to load."
- [x] remove `detailTTL`: `needsFetch(true)` fetches only an uncached PR; keep the retry of a failed call
- [x] write tests: a cursor move to a cached PR shows its detail; a cursor move to an uncached PR shows the empty text and sends no `PRDetail` call
- [x] write tests: `enter` on an uncached PR fetches it; `enter` on a cached PR sends no call; a cursor move to a section row keeps the last PR
- [x] write tests: a mouse click and a finder pick select the PR and send one `PRDetail` call for an uncached PR; the mouse wheel sends no call
- [x] update the doc comment of `handleActionKey` in `internal/tui/actions.go` and the message "open a PR with enter" for the new action target
- [x] write a test: `A` after a cursor move acts on the PR under the cursor
- [x] update the existing tests that depend on `detailTTL` (`TestDetailCacheHit` and `TestDetailNewerSummaryReloads` in `internal/tui/detail_test.go`)
- [x] run `make test`: it must pass before Task 2

### Task 2: Drop the detail cache on a full refresh

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/detail_test.go`

- [x] in `handlePoller`, detect a full refresh: the new `RefreshedAt` differs from the stored one
- [x] on a full refresh, clear `cache`, `errs`, and `fetchedFor`, but not `loading`
- [x] after `setSnapshot`, fetch the shown PR again with its new summary from `m.list.findPR`, if it had a cached detail
- [x] write tests: a full refresh drops the cache of other PRs and sends exactly one `PRDetail` call for the shown PR, also when its `UpdatedAt` is newer
- [x] write tests: `enter` on an uncached PR, then a refresh while the call runs: the result lands and the pane shows the detail
- [x] write tests: a load more snapshot keeps the cache; the first snapshot fetches nothing
- [x] run `make test`: it must pass before Task 3

### Task 3: Compute the change marks

**Files:**
- Create: `internal/tui/marks.go`
- Create: `internal/tui/marks_test.go`

- [ ] add `markSet`, `markState`, `merge`, and `diffMarks` in `internal/tui/marks.go` with the rules of the table in Technical Details
- [ ] write table tests for each rule: new PR, PR that entered the loaded pages, each count up, count up by me, new head SHA, empty head SHA, CI and review changes, red wins over green
- [ ] write tests for `merge`: flags accumulate, the newest state color wins, an empty state keeps the old color
- [ ] write a test: a nil `prev` gives no marks
- [ ] run `make test`: it must pass before Task 4

### Task 4: Keep the marks in the list and clear them

**Files:**
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/list_test.go`

- [ ] add `marks map[string]markSet` to `listPane`
- [ ] in `handlePoller`, on a full refresh with a previous snapshot, merge `diffMarks` into `list.marks`
- [ ] in `followCursor`, clear the marks of the last PR when the cursor leaves it
- [ ] write tests: marks accumulate across two refreshes; a load more adds no marks; the marks of a PR stay while the cursor is on it and clear when the cursor leaves
- [ ] write a test: a mark on a PR in two sections clears in both sections
- [ ] run `make test`: it must pass before Task 5

### Task 5: Show the marks in the rows

**Files:**
- Modify: `internal/tui/styles.go`
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/list_test.go`
- Modify: `internal/tui/statusbar_test.go` (it has the `NewStyles` override tests)

- [ ] add the colors `mark_new`, `mark_activity`, and `mark_commits` to `defaultColors`, and their styles to `Styles`
- [ ] in `renderPR`, replace the two leading spaces with the mark field of four columns
- [ ] in `renderRow`, add one dot to a folded section or group row if a hidden PR has a mark
- [ ] update the expected row text in the existing list tests for the wider field
- [ ] write tests: each mark kind renders `•` in its column; a row with no marks keeps the alignment; a folded section shows the dot; a color override from `[colors]` applies to a mark
- [ ] run `make test`: it must pass before Task 6

### Task 6: Verify acceptance criteria
- [ ] verify each item of the Overview
- [ ] verify the edge cases: the first snapshot, a load more, a refresh with an error, a cursor on a section row, a PR that leaves the list
- [ ] run the full test suite: `make test` and `go test -race ./...`
- [ ] run `make lint`

### Task 7: [Final] Update documentation
- [ ] update `README.md`: the detail pane follows the cursor, `enter` loads an uncached detail, the cache lives until the next refresh, the mark kinds and colors, and the new `[colors]` names
- [ ] update `CLAUDE.md` if a new pattern appears
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items that need manual action. No checkboxes.*

**Manual verification:**
- Run argus with a real config. Move the cursor across cached and uncached PRs. Check that the pane never shows the detail of the previous PR.
- Comment on a PR from the GitHub web page, then press `r`. Check that the blue mark shows, and that it clears when the cursor leaves the row.
- Check the mark colors in a light and a dark terminal theme.

**Follow-up:**
- `refreshDetail` in `internal/tui/actions.go` returns early while a call is running. The running call started before the action, so its result can be older than the action.
