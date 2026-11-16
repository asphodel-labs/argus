// Command argus is a terminal UI for GitHub pull requests.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/config"
	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/notify"
	"github.com/tdragon/argus/internal/poller"
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
	notifier := &notify.Notifier{
		Runner:   execx.Exec{},
		SeenPath: filepath.Join(stateDir, notify.SeenFile),
		Me:       cfg.User,
	}
	m := tui.New(tui.Options{Context: ctx, Poller: p, Notifier: notifier, Styles: styles,
		Me: cfg.User, BotLogins: cfg.BotLogins,
	})
	_, err = tea.NewProgram(m).Run()
	cancel()
	<-done
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
