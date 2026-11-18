# argus

argus is a Go terminal UI for GitHub pull requests. The module path is `github.com/asphodel-labs/argus`.
See `README.md` for the features and `docs/plans/` for the design.

## Commands

- `make build`: build `bin/argus` from `./cmd/argus`.
- `make test`: run `go test ./...`.
- `make lint`: check `gofmt -l .`, run `go vet ./...`, then run `golangci-lint run ./...`.
- `go test -race ./...`: run the tests with the race detector.

`make lint` runs golangci-lint only if it is installed. Without it, `make lint` prints "golangci-lint not installed, skipped".

## Package layout

- `cmd/argus/`: parse the `-config` flag, load the config, connect the packages, start the TUI.
- `internal/config`: load `~/.config/argus/config.toml` and resolve the login with `gh api user`.
- `internal/execx`: the `Runner` interface for external commands, and `ShellQuote`.
- `internal/gh`: GraphQL search, PR detail, mutations, and REST calls through go-gh.
- `internal/model`: PR, Check, Thread, Review, and the status rules.
- `internal/poller`: the poll loop, the full refresh, and the snapshot diff to events. It has no TUI imports.
- `internal/notify`: send events with `agtermctl notify`, and keep `seen.json`.
- `internal/state`: the last merge method for each repo.
- `internal/review`: worktree setup and cleanup, the revdiff overlay, the annotation parser, and the hunk check.
- `internal/commands`: run the custom commands of the config in the clone, in background or overlay mode.
- `internal/launch`: the Claude session through `agtermctl session new`.
- `internal/tui`: the Bubble Tea model, the list and detail panes, the dialogs, and the status bar.

## Runner pattern

Every call to `git`, `gh`, `agtermctl`, `revdiff`, or `claude` goes through `execx.Runner`.
`execx.Exec` is the real runner. Tests give a fake runner that records the arguments and returns set output.
`Runner.Run` returns an error only when the command cannot start or the context stops it. A non-zero exit gives the exit code and a nil error.

## Test rules

- Tests never call real GitHub, real `agtermctl`, or `git` on a real clone.
- `internal/gh` tests use an `httptest` server with recorded JSON from `internal/gh/testdata/`.
- `internal/review` tests use golden files in `internal/review/testdata/`. Git tests use temporary repositories.
- TUI tests use `teatest` and fake dependencies.
- Every change needs new or updated tests. Run `make test` and `make lint` before a commit.
