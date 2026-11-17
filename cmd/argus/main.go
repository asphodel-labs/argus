// Command argus is a terminal UI for GitHub pull requests.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/config"
	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/launch"
	"github.com/tdragon/argus/internal/notify"
	"github.com/tdragon/argus/internal/poller"
	"github.com/tdragon/argus/internal/review"
	"github.com/tdragon/argus/internal/state"
	"github.com/tdragon/argus/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "argus:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	configPath := flag.String("config", defaultPath, "path to the config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := config.ResolveUser(context.Background(), cfg, execx.Exec{}); err != nil {
		return err
	}
	styles, err := tui.NewStyles(cfg.Colors)
	if err != nil {
		return fmt.Errorf("config %s: %w", *configPath, err)
	}
	stateDir, err := stateDir()
	if err != nil {
		return err
	}
	merges, err := state.Open(stateDir)
	if err != nil {
		return err
	}
	client, err := gh.New(api.ClientOptions{})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := poller.New(client, poller.Options{
		User:            cfg.User,
		Orgs:            cfg.Orgs,
		RefreshInterval: cfg.RefreshInterval,
		MinRefreshGap:   cfg.MinRefreshGap,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.Run(ctx) // Run returns only the context error.
	}()
	worktrees := &review.Worktrees{Runner: execx.Exec{}, RepoRoot: cfg.RepoRoot, Repos: cfg.Repos, Owners: cfg.Owners, HTTPS: cfg.CloneProtocol == config.CloneHTTPS}
	cleaned := make(chan struct{})
	go func() {
		defer close(cleaned)
		// The start-up cleanup removes only clean worktrees of merged or
		// closed PRs. The TUI does not show its result.
		_, _ = worktrees.Cleanup(ctx, client)
	}()
	reviewer := &review.Revdiff{
		Worktrees: worktrees,
		GitHub:    client,
		StateDir:  stateDir,
		SessionID: os.Getenv("AGTERM_SESSION_ID"),
		Bin:       binPath("revdiff"),
	}
	launcher := &launch.Claude{
		Worktrees: worktrees,
		Runner:    execx.Exec{},
		SessionID: os.Getenv("AGTERM_SESSION_ID"),
		Bin:       binPath("claude"),
	}
	notifier := &notify.Notifier{
		Runner:   execx.Exec{},
		SeenPath: filepath.Join(stateDir, notify.SeenFile),
		Me:       cfg.User,
	}
	m := tui.New(tui.Options{Context: ctx, Poller: p, Notifier: notifier, Detail: client, Styles: styles,
		Search: client, Orgs: cfg.Orgs, GitHub: client, Merges: merges, Runner: execx.Exec{}, Cloner: worktrees, Reviewer: reviewer, Launcher: launcher,
		Me: cfg.User, BotLogins: cfg.BotLogins,
	})
	_, err = tea.NewProgram(m).Run()
	cancel()
	<-done
	// Cleanup does not stop a git command halfway, so wait for it.
	<-cleaned
	return err
}

// stateDir returns ~/.local/state/argus.
func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "argus"), nil
}

// binPath returns the absolute path of the command name. agterm runs the
// overlay command and the --command of a new session with the PATH of the
// agterm app, which can miss the directory of revdiff or claude. binPath
// returns name if the lookup fails.
func binPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return name
}
