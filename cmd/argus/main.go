// Command argus is a terminal UI for GitHub pull requests.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/tdragon/argus/internal/config"
	"github.com/tdragon/argus/internal/execx"
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
	// The TUI replaces this output in task 8.
	fmt.Printf("argus: user %s, %d orgs, refresh every %s\n", cfg.User, len(cfg.Orgs), cfg.RefreshInterval)
	return nil
}
