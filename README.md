# argus

![argus](assets/argus.png)

argus is a terminal UI for GitHub pull requests.
It shows the open PRs that concern you and lets you act on them with vim-style keys.

The name comes from Argus Panoptes, the giant with a hundred eyes in Greek mythology.
Hera made him a guardian because some of his eyes were always open. Like him, argus watches all your PRs, and no review escapes it.

- The left pane lists the PRs in four sections: Needs my review, My PRs, one section for each configured org, and Personal. An org section does not show a PR that is in Needs my review or My PRs.
- The detail pane shows one PR: the title, the status, the CI checks, the description, and the conversation threads. It follows the list cursor.
- After a refresh, colored marks show the PRs that changed. See [Change marks](#change-marks).
- The actions are approve, merge, rerun checks, comment, reply, react, and resolve a thread.
- `v` opens revdiff for the PR in an agterm overlay. argus posts the annotations as one GitHub review.
- `p` opens a Claude session with `/pr <url>` in a new agterm session.
- The `:` command line converts a PR to a draft, marks it ready, closes it, and runs your custom commands.
- `y` copies the PR URL.
- `C` shows the GitHub Actions runs of the default branch of a repo. See [Default-branch CI](#default-branch-ci).
- A background poller refreshes the data and sends agterm notifications.

argus uses the local `gh` authentication. It needs no separate token.

## Requirements

- `gh`, logged in with `gh auth login`.
- Go 1.27 to build argus.
- agterm with `agtermctl` for the notifications, `v`, and `p`.
- `revdiff` for `v`.
- `claude` (Claude Code) for `p`.

argus runs without agterm. Outside agterm, it sends no notifications, and `v` and `p` show a "requires agterm" message.
argus finds agterm through the `AGTERM_SESSION_ID` environment variable.

## Install

Install argus with Homebrew:

```fish
brew install asphodel-labs/tap/argus
```

Download a binary for macOS or Linux from the [releases page](https://github.com/asphodel-labs/argus/releases).

You can also install argus with Go:

```fish
go install github.com/asphodel-labs/argus/cmd/argus@latest
```

Or clone the repository and build the binary:

```fish
git clone https://github.com/asphodel-labs/argus.git
cd argus
make build
```

`make build` writes the binary to `bin/argus`. Copy it to a directory in your `PATH`.

Start argus:

```fish
argus
argus -config ~/other/config.toml
```

The `-config` flag sets the config file. The default is `~/.config/argus/config.toml`.
`argus -version` shows the version.

## Config

The config file is TOML. argus starts with the default values when the file does not exist.

```toml
orgs = ["acme"]
user = ""                 # empty: argus reads the login from `gh api user`
refresh_interval = "10m"
min_refresh_gap = "5m"
repo_root = "~/sources"
clone_protocol = "ssh"
bot_logins = ["renovate-bot"]
quiet_hours = "22:00-08:00"

[repos]
"acme/api" = "~/work/api"

[owners]
"globex" = "~/sources/globex"   # clones at ~/sources/globex/<repo>

[colors]
approved = "2"

[[commands]]
name = "checkout"
key = "C"
mode = "background"
run = "gh pr checkout {{.Number}}"
```

| Key | Default | Meaning |
|---|---|---|
| `orgs` | empty | The orgs to show. Each org gets one section. |
| `user` | the `gh` login | Your GitHub login. |
| `refresh_interval` | `10m` | The time between two scheduled full refreshes. |
| `min_refresh_gap` | `5m` | The minimum time between two full refreshes that new GitHub notifications start. |
| `repo_root` | `~/sources` | argus looks for a clone at `<repo_root>/<owner>/<repo>`. |
| `clone_protocol` | `ssh` | How argus clones a missing repo. `ssh` runs `git clone git@github.com:<owner>/<repo>.git`. `https` runs `gh repo clone`, which uses the `git_protocol` setting of `gh`. |
| `bot_logins` | empty | Logins that the `b` filter treats as bots. |
| `quiet_hours` | empty | A daily period with no notifications, as `HH:MM-HH:MM` in local time. See [Notifications](#notifications). |
| `[repos]` | empty | The clone path for one repo, if it is not under `repo_root`. |
| `[owners]` | empty | A root directory for all repos of one owner. argus looks for a clone at `<dir>/<repo>`. The owner name ignores case. |
| `[colors]` | ANSI colors | Color overrides. |
| `[[commands]]` | empty | Custom commands. See [Custom commands](#custom-commands). |

`refresh_interval` and `min_refresh_gap` use Go duration syntax, for example `90s` or `10m`. The value must be positive.
A path that starts with `~/` is relative to your home directory.

The color names are `failed`, `changes`, `reply`, `draft`, `conflict`, `pending`, `approved`, `accent`, `error`, `muted`, `selection`, `mark_new`, `mark_activity`, and `mark_commits`.
A color value is an ANSI number from 0 to 255 or `#rrggbb`. argus stops with an error for an unknown name or a bad value.

## PR status

The list shows one status for each PR. The first rule that matches gives the status.

| Icon | Status | Rule |
|---|---|---|
| ✗ | CI failed | The check rollup is FAILURE or ERROR. |
| ✎ | Changes requested | The review decision is CHANGES_REQUESTED. |
| 💬 | Needs reply | An unresolved thread has a last comment that is not yours. |
| ◌ | Draft | The PR is a draft. |
| ⊘ | Conflict | The PR has a merge conflict. |
| ⏳ | Pending review | No other rule matches. |
| ✓ | Approved | The PR is APPROVED and CI is green. |

## Change marks

A full refresh compares the new data with the data of the previous full refresh.
A PR row that changed gets colored `•` marks in the four columns at the start of the row.
The columns are, in order: New, Activity, Commits, and State.

| Mark | Color name | Default | Rule |
|---|---|---|---|
| New | `mark_new` | `208` (orange) | The PR is new, and GitHub created it after the previous full refresh. |
| Activity | `mark_activity` | `33` (blue) | The comment, review, or thread count went up, and the newest author is not you. Also a PR that is new in the list but older than the previous full refresh. |
| Commits | `mark_commits` | `135` (purple) | The head commit changed. |
| State | `approved` / `failed` | green / red | The check rollup changed to SUCCESS (green), or to FAILURE or ERROR (red). The review decision changed to APPROVED (green), or to CHANGES_REQUESTED (red). |

If one refresh gives a green and a red state change, the mark is red. A later refresh replaces the state color.
The marks of a PR add up across refreshes. They stay until the cursor leaves the PR row.
A folded section or repo group shows one `•` if a hidden PR has a mark.
The last line of the list pane is a legend of the mark colors. A list pane with less than three lines has no legend.
A `load more` page adds no marks.
argus keeps the marks in memory only. Each start of argus begins with no marks.

## Keys

Global keys:

| Key | Action |
|---|---|
| `ctrl+r` | Refresh now. |
| `?` | Show the help. |
| `q` / `ctrl+c` | Quit. argus asks first if a dialog has text that you did not send. |
| `tab` | Switch the pane. |
| `h` / `l`, `←` / `→` | Focus the list pane / the detail pane. |
| `esc` | Close a dialog. |

List pane:

| Key | Action |
|---|---|
| `j` / `k`, `↓` / `↑` | Move down / up. |
| `gg` / `G`, `home` / `end` | Move to the top / the bottom. |
| `ctrl+d` / `ctrl+u` | Move a half page down / up. |
| `pgdown` / `pgup` | Move a page down / up. |
| `]` / `[` | Move to the next / previous section. |
| `enter` | Load the PR detail if it is not loaded, and move the focus to the detail pane. |
| `space` | Load the PR detail if it is not loaded. The focus stays in the list. |
| `za` | Fold the repo group or the section. |
| `/` | Open the fuzzy finder. A PR number that matches no loaded PR offers a GitHub search. |
| `a` | Hide the approved PRs. |
| `d` | Hide the draft PRs. |
| `b` | Show all PRs, only bot PRs, or no bot PRs. |
| `u` | Filter by author. |
| `m` | Load 20 more PRs in an org or Personal section. |

The filter keys `a`, `d`, `b`, and `u` apply to every section except My PRs.
The detail pane follows the list cursor. On a section, group, or `load more` row, the pane keeps the last PR.
The cursor keys do not load a PR. The pane shows a loaded detail at once. For a PR with no loaded detail, the pane shows `owner/repo#n  Press enter to load.`
`enter` and `space` load the detail. A loaded detail stays until the next full refresh.
A full refresh drops all loaded details and loads the detail of the shown PR again.
The list marks the PR in the detail pane with `●`.

The list shows the PRs of a GitHub stack (`gh stack`) together, in stack order.
The PR closest to the base branch comes first, and `┌`, `├`, and `└` connect the rows of the stack.
`[2/3]` after the number is the position of the PR in a stack of 3 PRs.
A PR of the stack that the list does not show leaves a gap in the positions.
The detail pane lists all PRs of the stack, marks the open PR with `›`, and shows the state of a merged or closed PR.

The fuzzy finder searches the PRs that the list shows after the filters. It also searches the folded groups and sections.
It matches the title, the number (`812` or `#812`), the repo, and the author.
`enter` moves the cursor to the PR, unfolds its group and section, and opens the PR. The focus moves to the detail pane.

The list loads only the newest PRs of each section, so the finder can miss an older PR.
A query can name a PR by number: `6505`, `#6505`, `repo#6505`, or `owner/repo#6505`. The repo and the owner match without case.
If no loaded PR has that number, the last row is `search GitHub for #6505`.
`enter` on that row sends one GitHub search over the configured orgs and your user. `repo#n` keeps only the PRs of that repo. `owner/repo#n` searches only that repo.
The search also finds closed and merged PRs. The row shows the state.
One match closes the finder and opens the PR. If the list does not show the PR, the list cursor stays, and the status bar shows `opened owner/repo#n (not in the list)`.
More matches replace the search row with one row for each PR. No match or an error shows a status message, and the finder stays open.
The action keys work on a PR that you open this way. `M` on a closed or merged PR shows "the PR is not open".

Detail pane:

| Key | Action |
|---|---|
| `j` / `k`, `↓` / `↑` | Scroll down / up. |
| `gg` / `G`, `home` / `end` | Scroll to the top / the bottom. |
| `ctrl+d` / `ctrl+u` | Scroll a half page down / up. |
| `pgdown` / `pgup` | Scroll a page down / up. |
| `]` / `[` | Move to the next / previous thread. |
| `za` | Fold the thread. |

PR actions work in both panes. They act on the PR in the detail pane. The pane header shows the ref of that PR.
This PR is the PR under the cursor, or the last PR when the cursor is on a section, group, or `load more` row.
With no PR in the detail pane, the actions show "move the cursor to a PR".

| Key | Action |
|---|---|
| `A` | Approve. |
| `M` | Merge. The dialog keeps the last merge method for each repo. If branch protection blocks the merge and the checks are pending, the dialog enables auto-merge. |
| `U` | Update the branch. Merge the base branch into the head branch, or rebase the head branch on the base branch. |
| `R` | Request a review. Type to filter the users, `space` checks a user, and `enter` sends. The requested teams and bots stay. |
| `@` | Assign users. The user list works as for `R`. |
| `f` / `F` | Rerun the failed checks / all checks. |
| `c` | Comment. |
| `r` | Reply to a thread. |
| `+` | Add a reaction. |
| `x` | Resolve a thread. |
| `o` | Open the PR in the browser. |
| `y` | Copy the PR URL to the clipboard. |
| `:` | Open the command line. |
| `p` | Open a Claude `/pr` session. |
| `v` | Review with revdiff. |
| `C` | Show the CI runs of the default branch. The repo comes from the list cursor, not from the PR in the detail pane. See [Default-branch CI](#default-branch-ci). |

`y` writes the URL to the clipboard with OSC 52. On macOS, `y` also runs `pbcopy`, because some terminals block OSC 52.

The mouse also works in the panes:

- A click focuses the pane.
- In the list, a click on a PR moves the cursor to it and loads its detail. A click on a section or a repo row folds it. A click on the `load more` row loads more PRs.
- In the detail pane, a click on the header of a thread, a comment, or a review folds it.
- The wheel scrolls the pane under the pointer. In the list, the cursor stays in the view.
- The mouse does nothing while a dialog or the help is open.

argus captures the mouse, so a plain drag does not select text.
To select text, hold `shift` while you drag. In iTerm2, hold `option`.

## Default-branch CI

`C` opens a dialog with the last 20 GitHub Actions runs of the default branch (for example, `main` or `master`).
The repo is the repo of the PR row or the repo group row under the list cursor. A folded group row also works.
On a section row or a `load more` row, `C` shows "move the cursor to a PR or a repo".

The dialog title is `CI · owner/name@<branch>`. Each row shows the status, the run number, the workflow, the title, the event, and the age.
argus drops a run from a fork branch with the same name, so the dialog can show fewer than 20 runs.
The dialog loads the runs when it opens. It does not refresh them by itself.

| Key | Action |
|---|---|
| `j` / `k`, `↓` / `↑` | Select a run. |
| `f` | Rerun the failed jobs of the run. |
| `F` | Rerun all jobs of the run. |
| `L` | Show the failed log in an agterm overlay with `gh run view <id> --log-failed \| less -R`. |
| `o` | Open the run in the browser. |
| `ctrl+r` | Load the runs again. |
| `esc` | Close the dialog. |

`f` and `L` need a run with a failed conclusion: `failure`, `timed_out`, or `startup_failure`. For another run, the dialog shows "run #N has no failed jobs".
After a rerun, the status bar shows `<action>: done`, and the dialog loads the runs again. The new attempt shows as queued.
A second `f` or `F` while a rerun request runs does nothing.
An error shows in the dialog, and the runs stay. Outside agterm, `L` shows "log view requires agterm".

## Command line

`:` opens the command line in place of the status bar. The line shows the command names that start with the text.
`tab` completes the name, `enter` runs the command, and `esc` closes the line.
A command other than `:q` acts on the PR in the detail pane. The PR detail must be loaded.

| Command | Action |
|---|---|
| `:draft` | Convert the PR to a draft. A draft PR shows "already a draft". |
| `:ready` | Mark the draft PR ready for review. A PR that is not a draft shows "already ready". |
| `:close` | Close the PR without a merge. A PR that is not open shows "the PR is not open". |
| `:q` | Quit argus. If a dialog has unsent text, argus asks before it quits. |
| `:<name>` | Run the custom command with this name. |

An unknown name shows "unknown command: <name>".
The status bar shows the result of the command. After a success, the PR detail loads again, and argus starts a full refresh of the list.

## Custom commands

A `[[commands]]` table in the config adds one custom command.
The command runs a shell template in the local clone of the PR repository.

| Key | Default | Meaning |
|---|---|---|
| `name` | required | The name for the command line, `:<name>`. It must be unique, must have no space, and must not be `draft`, `ready`, `close`, or `q`. |
| `key` | empty | An optional key that runs the command. It must not be a built-in key or the key of another command. `C` is a built-in key: it opens the [default-branch CI](#default-branch-ci) dialog. The help shows the key under the PR actions. |
| `mode` | `background` | `background` or `overlay`. |
| `run` | required | The shell template. |

The `run` template uses the Go `text/template` syntax with these fields:

| Field | Value |
|---|---|
| `{{.Owner}}` | The repo owner. |
| `{{.Repo}}` | The repo name. |
| `{{.Number}}` | The PR number. |
| `{{.URL}}` | The PR URL. |
| `{{.Branch}}` | The head branch. |
| `{{.Base}}` | The base branch. |
| `{{.Title}}` | The PR title. |

argus quotes each field as one shell word. Do not put quotes around a field.
argus stops with an error at the start if the template has a syntax error or an unknown field.

The modes:

- `background` runs `sh -c` with the template. The command gets no input, and argus keeps its output only for the status bar.
- `overlay` runs the template in an agterm overlay, and argus waits until the overlay closes. Use it for a command that needs input. After the command exits, the overlay shows the exit code and stays open until you press enter. The overlay uses the `PATH` of argus, so `gh` and `git` resolve as in your shell. Outside agterm, an overlay command shows "overlay commands require agterm".

On success, the status bar shows `<name>: done`. On a non-zero exit, it shows the last line of stderr, else the last line of stdout, else the exit code.

argus finds the clone as in the [review flow](#review-flow). If a worktree of the clone has the PR head branch checked out, the command runs in that worktree. Git does not check out a branch that another worktree has checked out. If no clone exists, argus asks before it clones the repo. Run the command again after the clone.

For example, this command checks out the stack of a `gh stack` PR and syncs it:

```toml
[[commands]]
name = "stack-sync"
mode = "overlay"
run = "gh stack checkout {{.Number}} && gh stack sync"
```

`gh stack sync` rebases and pushes the branches of the stack. It can ask for input, so the example uses `overlay`.

## Refresh and rate limit

The poller works in this sequence:

1. Every 60 seconds, or the `X-Poll-Interval` value, argus sends `GET /notifications` with `If-Modified-Since`. A `304` reply costs no rate limit.
2. A `200` reply with new threads starts a full refresh. This refresh waits until `min_refresh_gap` passed since the start of the last full refresh.
3. The `refresh_interval` timer and `ctrl+r` start a full refresh. `ctrl+r` does not wait for `min_refresh_gap` or for the backoff.
4. After a failed refresh, argus keeps the last data. It tries again after 1, 2, 4, 8, and then 10 minutes.
5. If fewer than 200 GraphQL points remain, argus stops the full refresh until the rate limit resets. This also applies to `ctrl+r` and to a retry after a failed refresh.
6. `m` loads one more page of one section at once. It does not start a full refresh.

One search page of 20 PRs costs 11 GraphQL points. One page of 100 PRs costs 54 points.
These numbers come from a measurement on 2026-11-16 with the `rateLimit { cost }` field. Thus argus reads pages of 20.
The Needs my review and My PRs sections load all pages. An org section and the Personal section load 20 PRs, and `m` loads 20 more.
A refresh loads the same number of PRs that you loaded.

## Notifications

argus sends an agterm notification for these events:

| Event | Rule |
|---|---|
| Review requested | A PR is new in the review-requested scope or in the assignee scope. |
| Activity on my PR | The comment, review, or thread count went up, and the newest author is not you. |
| CI result on my PR | The check rollup changed from PENDING to SUCCESS, FAILURE, or ERROR. |

argus sends at most 5 notifications for each refresh. If there are more events, the fifth notification is one summary line.
On the first run, argus records the state and sends no notifications.

The `quiet_hours` key sets a daily period with no notifications, for example `"22:00-08:00"`.
The period starts at the first time and stops at the second time. If the second time is earlier, the period goes past midnight.
In the quiet period, argus records the state and drops the events. It does not send them after the period.
The two times must be different.

## Review flow

`v` and `p` need a local clone of the repository.

1. argus finds the clone. It uses the `[repos]` path first, then the `[owners]` path `<dir>/<repo>`, then `<repo_root>/<owner>/<repo>`.
2. If no clone exists, argus asks before it clones the repo. The `clone_protocol` key selects `git clone` over SSH or `gh repo clone`. The clone goes to the first of these paths that the config sets.
3. argus fetches the PR head and the base branch.
4. argus makes a detached worktree at `<clone>/.worktrees/argus-pr-<n>`. It moves an existing worktree to the new head only if the tree is clean. If the tree has changes, argus keeps the old commit and shows a warning.
5. Before the first worktree, argus adds `.worktrees/` to the `info/exclude` file of the clone. An error there shows a warning only.

`v` then opens revdiff in an agterm overlay on the diff from the merge base to the PR head:

- If you write annotations, argus opens the review dialog. Select Comment, Approve, or Request changes, and add a review body.
- If you close revdiff with no annotations, argus asks: `No comments. Approve owner/repo#n?`. Add an optional body and press `ctrl+s` to approve. The approval is for the commit that revdiff showed. If the PR head moved after the checkout, the dialog shows a warning. `esc` sends nothing.
- On your own PR, argus does not ask for approval, because GitHub refuses it. The status bar shows `no annotations`.
- If you have a pending review on the PR, GitHub refuses the approval. The dialog shows the error. Submit or delete the pending review on GitHub first.
- argus posts each annotation as one review thread. It supports line, line range, removed-line, and file-level annotations.
- A line comment outside every diff hunk goes into the review body as `path:L — text`. The dialog marks these comments.
- If the PR head moved after the checkout, the dialog shows a warning.
- If the submit fails, argus keeps the annotation file. The next `v` loads it into revdiff again.
- A retry reuses your pending review on GitHub and does not add a thread two times. If you edit or delete an annotation before the retry, argus deletes the old draft comment that it added. argus deletes a draft only if its text on GitHub is still the text that argus sent.
- If your pending review has a draft comment that argus did not add, or a draft that you changed on github.com, argus does not submit. Delete that draft or submit the pending review on GitHub first.
- argus never posts a second review automatically. If an earlier submit reached GitHub but its reply did not come back, the next `ctrl+s` posts nothing. If that review has the same commit, event, body, and comments, argus shows a success. Otherwise argus shows the review URL and keeps your annotations. Check that review on GitHub.
- `ctrl+x` in the review dialog asks for a confirmation, then discards the local annotations and the review file. It does not change GitHub.
- `esc` does not close the review dialog while the submit runs.

`p` opens a new agterm session in the `reviews` workspace. The session runs `claude '/pr <url>'` in the worktree through your login shell.

At the start, argus removes each `argus-pr-*` worktree that has a clean tree and a merged or closed PR. argus never removes a worktree with changes.

## State files

argus keeps its state in `~/.local/state/argus/`:

| File | Content |
|---|---|
| `seen.json` | The PR state of the last refresh. The notifications compare against it. |
| `merge-methods.json` | The last merge method for each repo. |
| `pr-<owner>+<repo>+<n>.md` | The revdiff annotations of a review that is not submitted. |
| `pr-<owner>+<repo>+<n>.body.md` | The PR body for revdiff. |
| `pr-<owner>+<repo>+<n>.review.json` | The pending review of a submit, and the id, path, lines, and text of each draft comment that argus added to it. |

argus deletes the annotation file, the body file, and the review file after a successful submit and after `ctrl+x` in the review dialog.

## Open manual checks

These checks need real GitHub and agterm. Nobody did them yet.

- Run argus against real GitHub. Check the sections, the statuses, the filters, and load more.
- Merge a test PR with each allowed method and with auto-merge.
- Run `v` on a real PR. Add line, range, removed-line, and file-level annotations. Check the posted review on github.com.
- Run `v` with a state directory or a worktree path that has a space or a single quote. Check the revdiff arguments in the overlay.
- Run `p`. Check that Claude starts in the worktree with `/pr <url>` and finds `gh` and `git` with the fish login shell.
- Press `C` on a repo with a failed run on the default branch. Check the rows, `L`, `f`, and a repo with `master` as the default branch.
- Keep argus open for one hour. Check the notifications and the rate-limit use.

## Development

See [CLAUDE.md](CLAUDE.md) for the build, test, and lint commands.

The CI workflow runs the lint, the tests, and the build on each pull request and on each push to `main`.

To make a release, push a version tag on a commit of `main`:

```fish
git tag v0.1.0 origin/main
git push origin v0.1.0
```

The release workflow builds the binaries with GoReleaser and publishes the release.
The tag publishes the release at once, so push a tag only for a commit that you want to ship.
GoReleaser also writes the Homebrew cask to [asphodel-labs/homebrew-tap](https://github.com/asphodel-labs/homebrew-tap).
It pushes the cask over SSH with a deploy key that has write access to the tap repository.
The private key is the secret `HOMEBREW_TAP_SSH_KEY` in the `release` environment of this repository.

## Disclaimer

argus is provided "as is", without warranty of any kind. See the [LICENSE](LICENSE) for the full terms.

argus acts on GitHub with your `gh` login. Approve, merge, update branch, review requests, and the posted reviews change real pull requests. argus asks before it merges or submits a review, but you are responsible for each action that you confirm. argus is not affiliated with GitHub.

## License

argus is released under the [MIT License](LICENSE).
