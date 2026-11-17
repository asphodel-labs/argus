# argus

![argus](assets/argus.png)

argus is a terminal UI for GitHub pull requests.
It shows the open PRs that concern you and lets you act on them with vim-style keys.

- The left pane lists the PRs in four sections: Needs my review, My PRs, one section for each configured org, and Personal.
- The detail pane shows one PR: the title, the status, the CI checks, the description, and the conversation threads.
- The actions are approve, merge, rerun checks, comment, reply, react, and resolve a thread.
- `v` opens revdiff for the PR in an agterm overlay. argus posts the annotations as one GitHub review.
- `p` opens a Claude session with `/pr <url>` in a new agterm session.
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

Clone the repository and build the binary:

```fish
git clone git@personalgit:tdragon/argus.git
cd argus
make build
```

`make build` writes the binary to `bin/argus`. Copy it to a directory in your `PATH`.
You can also install it with Go from the clone:

```fish
go install ./cmd/argus
```

Start argus:

```fish
argus
argus -config ~/other/config.toml
```

The `-config` flag sets the config file. The default is `~/.config/argus/config.toml`.

## Config

The config file is TOML. argus starts with the default values when the file does not exist.

```toml
orgs = ["acme"]
user = ""                 # empty: argus reads the login from `gh api user`
refresh_interval = "10m"
min_refresh_gap = "5m"
repo_root = "~/sources"
bot_logins = ["renovate-bot"]

[repos]
"acme/api" = "~/work/api"

[owners]
"globex" = "~/sources/globex"   # clones at ~/sources/globex/<repo>

[colors]
approved = "2"
```

| Key | Default | Meaning |
|---|---|---|
| `orgs` | empty | The orgs to show. Each org gets one section. |
| `user` | the `gh` login | Your GitHub login. |
| `refresh_interval` | `10m` | The time between two scheduled full refreshes. |
| `min_refresh_gap` | `5m` | The minimum time between two full refreshes that new GitHub notifications start. |
| `repo_root` | `~/sources` | argus looks for a clone at `<repo_root>/<owner>/<repo>`. |
| `bot_logins` | empty | Logins that the `b` filter treats as bots. |
| `[repos]` | empty | The clone path for one repo, if it is not under `repo_root`. |
| `[owners]` | empty | A root directory for all repos of one owner. argus looks for a clone at `<dir>/<repo>`. The owner name ignores case. |
| `[colors]` | ANSI colors | Color overrides. |

`refresh_interval` and `min_refresh_gap` use Go duration syntax, for example `90s` or `10m`. The value must be positive.
A path that starts with `~/` is relative to your home directory.

The color names are `failed`, `changes`, `reply`, `draft`, `conflict`, `pending`, `approved`, `accent`, `error`, and `muted`.
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

## Keys

Global keys:

| Key | Action |
|---|---|
| `ctrl+r` | Refresh now. |
| `?` | Show the help. |
| `q` / `ctrl+c` | Quit. argus asks first if a dialog has text that you did not send. |
| `tab` | Switch the pane. |
| `h` / `l` | Focus the list pane / the detail pane. |
| `esc` | Close a dialog. |

List pane:

| Key | Action |
|---|---|
| `j` / `k` | Move down / up. |
| `gg` / `G` | Move to the top / the bottom. |
| `ctrl+d` / `ctrl+u` | Move a half page down / up. |
| `]` / `[` | Move to the next / previous section. |
| `enter` | Open the PR in the detail pane and move the focus to it. |
| `space` | Open the PR in the detail pane. The focus stays in the list. |
| `za` | Fold the repo group or the section. |
| `/` | Open the fuzzy finder. A PR number that matches no loaded PR offers a GitHub search. |
| `a` | Hide the approved PRs. |
| `d` | Hide the draft PRs. |
| `b` | Show all PRs, only bot PRs, or no bot PRs. |
| `u` | Filter by author. |
| `m` | Load 20 more PRs in an org or Personal section. |

The filter keys `a`, `d`, `b`, and `u` apply to every section except My PRs.
The cursor keys only move the cursor. They do not load a PR.
The list marks the opened PR with `●`.

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
| `j` / `k` | Scroll down / up. |
| `gg` / `G` | Scroll to the top / the bottom. |
| `ctrl+d` / `ctrl+u` | Scroll a half page down / up. |
| `]` / `[` | Move to the next / previous thread. |
| `za` | Fold the thread. |

PR actions work in both panes. They act on the opened PR, not on the PR under the cursor.
With no opened PR, they show "open a PR with enter".

| Key | Action |
|---|---|
| `A` | Approve. |
| `M` | Merge. The dialog keeps the last merge method for each repo. If branch protection blocks the merge and the checks are pending, the dialog enables auto-merge. |
| `f` / `F` | Rerun the failed checks / all checks. |
| `c` | Comment. |
| `r` | Reply to a thread. |
| `+` | Add a reaction. |
| `x` | Resolve a thread. |
| `o` | Open the PR in the browser. |
| `p` | Open a Claude `/pr` session. |
| `v` | Review with revdiff. |

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

## Review flow

`v` and `p` need a local clone of the repository.

1. argus finds the clone. It uses the `[repos]` path first, then the `[owners]` path `<dir>/<repo>`, then `<repo_root>/<owner>/<repo>`.
2. If no clone exists, argus asks before it runs `gh repo clone`. The clone goes to the first of these paths that the config sets.
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
- Keep argus open for one hour. Check the notifications and the rate-limit use.

## Development

See [CLAUDE.md](CLAUDE.md) for the build, test, and lint commands.
