// Package config loads the argus TOML config file.
package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/tdragon/argus/internal/execx"
)

// Default values for optional settings.
const (
	DefaultRefreshInterval = 10 * time.Minute
	DefaultMinRefreshGap   = 5 * time.Minute
	DefaultRepoRoot        = "~/sources"
)

// Config is the parsed config file.
type Config struct {
	Orgs            []string
	User            string
	RefreshInterval time.Duration
	// MinRefreshGap is the minimum time between two full refreshes that
	// new notifications start.
	MinRefreshGap time.Duration
	RepoRoot      string
	BotLogins     []string
	Repos         map[string]string
	Colors        map[string]string
}

type fileConfig struct {
	Orgs            []string          `toml:"orgs"`
	User            string            `toml:"user"`
	RefreshInterval string            `toml:"refresh_interval"`
	MinRefreshGap   string            `toml:"min_refresh_gap"`
	RepoRoot        string            `toml:"repo_root"`
	BotLogins       []string          `toml:"bot_logins"`
	Repos           map[string]string `toml:"repos"`
	Colors          map[string]string `toml:"colors"`
}

// DefaultPath returns ~/.config/argus/config.toml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "argus", "config.toml"), nil
}

// Load reads the config file at path.
// A missing file gives the default config.
func Load(path string) (*Config, error) {
	var fc fileConfig
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read config %s: %w", path, err)
	default:
		if err := toml.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}

	cfg := &Config{
		Orgs:            fc.Orgs,
		User:            fc.User,
		RefreshInterval: DefaultRefreshInterval,
		MinRefreshGap:   DefaultMinRefreshGap,
		RepoRoot:        expandHome(DefaultRepoRoot, home),
		BotLogins:       fc.BotLogins,
		Repos:           map[string]string{},
		Colors:          map[string]string{},
	}
	if err := parseDuration("refresh_interval", fc.RefreshInterval, &cfg.RefreshInterval); err != nil {
		return nil, err
	}
	if err := parseDuration("min_refresh_gap", fc.MinRefreshGap, &cfg.MinRefreshGap); err != nil {
		return nil, err
	}
	if fc.RepoRoot != "" {
		cfg.RepoRoot = expandHome(fc.RepoRoot, home)
	}
	for repo, dir := range fc.Repos {
		cfg.Repos[repo] = expandHome(dir, home)
	}
	for name, color := range fc.Colors {
		cfg.Colors[name] = color
	}
	return cfg, nil
}

// ResolveUser sets cfg.User from `gh api user` when the config has no user.
func ResolveUser(ctx context.Context, cfg *Config, r execx.Runner) error {
	if cfg.User != "" {
		return nil
	}
	stdout, stderr, code, err := r.Run(ctx, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return fmt.Errorf("run gh api user: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("gh api user exited with code %d: %s", code, strings.TrimSpace(stderr))
	}
	login := strings.TrimSpace(stdout)
	if login == "" {
		return errors.New("gh api user returned an empty login")
	}
	cfg.User = login
	return nil
}

// parseDuration sets *d from the value of the config key name.
// An empty value keeps *d. A value that is not positive is an error.
func parseDuration(name, value string, d *time.Duration) error {
	if value == "" {
		return nil
	}
	v, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", name, value, err)
	}
	if v <= 0 {
		return fmt.Errorf("%s %q must be positive", name, value)
	}
	*d = v
	return nil
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
