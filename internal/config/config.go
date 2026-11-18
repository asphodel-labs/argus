// Package config loads the argus TOML config file.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/asphodel-labs/argus/internal/execx"
	"github.com/asphodel-labs/argus/internal/model"
)

// Default values for optional settings.
const (
	DefaultRefreshInterval = 10 * time.Minute
	DefaultMinRefreshGap   = 5 * time.Minute
	DefaultRepoRoot        = "~/sources"
	DefaultCloneProtocol   = CloneSSH
)

// Clone protocols for the clone_protocol key.
const (
	// CloneSSH clones with `git clone git@github.com:<owner>/<repo>.git`.
	CloneSSH = "ssh"
	// CloneHTTPS clones with `gh repo clone`.
	CloneHTTPS = "https"
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
	// CloneProtocol is CloneSSH or CloneHTTPS.
	CloneProtocol string
	BotLogins     []string
	Repos         map[string]string
	// Owners maps an owner to a root directory. The clone of a repository
	// of that owner is at <dir>/<repo>.
	Owners map[string]string
	Colors map[string]string
	// Commands holds the custom commands of the [[commands]] tables.
	Commands []Command
}

// Modes of a custom command.
const (
	// ModeBackground runs the command with sh -c and shows the result in
	// the status bar.
	ModeBackground = "background"
	// ModeOverlay runs the command in an agterm overlay.
	ModeOverlay = "overlay"
)

// builtinCommands holds the names of the built-in commands of the command
// line. A custom command must not use one of these names.
var builtinCommands = []string{"draft", "ready", "close", "q"}

// Command is a custom command from a [[commands]] table.
type Command struct {
	// Name runs the command with :name.
	Name string
	// Key is an optional shortcut key. It is empty if the command has no key.
	Key string
	// Mode is ModeBackground or ModeOverlay.
	Mode string
	// Run is the parsed shell template. Render fills it with the PR fields.
	Run *template.Template
}

// CommandData holds the template fields of a custom command.
// Each field is a quoted shell word.
type CommandData struct {
	Owner  string
	Repo   string
	Number string
	URL    string
	// Branch is the head branch.
	Branch string
	// Base is the base branch.
	Base  string
	Title string
}

// NewCommandData returns the template fields of pr. It quotes each field
// with execx.ShellQuote.
func NewCommandData(pr model.PR) CommandData {
	return CommandData{
		Owner:  execx.ShellQuote(pr.Repo.Owner),
		Repo:   execx.ShellQuote(pr.Repo.Name),
		Number: execx.ShellQuote(strconv.Itoa(pr.Number)),
		URL:    execx.ShellQuote(pr.URL),
		Branch: execx.ShellQuote(pr.HeadRef),
		Base:   execx.ShellQuote(pr.BaseRef),
		Title:  execx.ShellQuote(pr.Title),
	}
}

// Render returns the shell script of the command for pr.
func (c Command) Render(pr model.PR) (string, error) {
	return execute(c.Run, NewCommandData(pr))
}

func execute(t *template.Template, data CommandData) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type fileCommand struct {
	Name string `toml:"name"`
	Key  string `toml:"key"`
	Mode string `toml:"mode"`
	Run  string `toml:"run"`
}

type fileConfig struct {
	Orgs            []string          `toml:"orgs"`
	User            string            `toml:"user"`
	RefreshInterval string            `toml:"refresh_interval"`
	MinRefreshGap   string            `toml:"min_refresh_gap"`
	RepoRoot        string            `toml:"repo_root"`
	CloneProtocol   string            `toml:"clone_protocol"`
	BotLogins       []string          `toml:"bot_logins"`
	Repos           map[string]string `toml:"repos"`
	Owners          map[string]string `toml:"owners"`
	Colors          map[string]string `toml:"colors"`
	Commands        []fileCommand     `toml:"commands"`
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
		CloneProtocol:   DefaultCloneProtocol,
		BotLogins:       fc.BotLogins,
		Repos:           map[string]string{},
		Owners:          map[string]string{},
		Colors:          map[string]string{},
	}
	if err := parseDuration("refresh_interval", fc.RefreshInterval, &cfg.RefreshInterval); err != nil {
		return nil, err
	}
	if err := parseDuration("min_refresh_gap", fc.MinRefreshGap, &cfg.MinRefreshGap); err != nil {
		return nil, err
	}
	switch fc.CloneProtocol {
	case "":
	case CloneSSH, CloneHTTPS:
		cfg.CloneProtocol = fc.CloneProtocol
	default:
		return nil, fmt.Errorf("clone_protocol %q must be %q or %q", fc.CloneProtocol, CloneSSH, CloneHTTPS)
	}
	if fc.RepoRoot != "" {
		cfg.RepoRoot = expandHome(fc.RepoRoot, home)
	}
	for repo, dir := range fc.Repos {
		cfg.Repos[repo] = expandHome(dir, home)
	}
	for owner, dir := range fc.Owners {
		cfg.Owners[owner] = expandHome(dir, home)
	}
	for name, color := range fc.Colors {
		cfg.Colors[name] = color
	}
	cmds, err := parseCommands(fc.Commands)
	if err != nil {
		return nil, err
	}
	cfg.Commands = cmds
	return cfg, nil
}

// parseCommands checks the [[commands]] tables and parses their templates.
func parseCommands(fcs []fileCommand) ([]Command, error) {
	var cmds []Command
	names := map[string]bool{}
	keys := map[string]string{}
	for i, fc := range fcs {
		switch {
		case fc.Name == "":
			return nil, fmt.Errorf("commands[%d]: name is empty", i)
		case strings.ContainsFunc(fc.Name, unicode.IsSpace):
			return nil, fmt.Errorf("command %q: name has a space", fc.Name)
		case names[fc.Name]:
			return nil, fmt.Errorf("command %q: name is not unique", fc.Name)
		}
		for _, b := range builtinCommands {
			if fc.Name == b {
				return nil, fmt.Errorf("command %q: name is a built-in command", fc.Name)
			}
		}
		names[fc.Name] = true

		mode := fc.Mode
		switch mode {
		case "":
			mode = ModeBackground
		case ModeBackground, ModeOverlay:
		default:
			return nil, fmt.Errorf("command %q: mode %q must be %q or %q", fc.Name, fc.Mode, ModeBackground, ModeOverlay)
		}

		if fc.Key != "" {
			if other, ok := keys[fc.Key]; ok {
				return nil, fmt.Errorf("command %q: key %q is also the key of command %q", fc.Name, fc.Key, other)
			}
			keys[fc.Key] = fc.Name
		}

		if strings.TrimSpace(fc.Run) == "" {
			return nil, fmt.Errorf("command %q: run is empty", fc.Name)
		}
		t, err := template.New(fc.Name).Parse(fc.Run)
		if err != nil {
			return nil, fmt.Errorf("command %q: parse run: %w", fc.Name, err)
		}
		// The render skips a branch that it does not take, so a walk of
		// the parse tree checks the field names in each branch.
		for _, tt := range t.Templates() {
			if tt.Tree == nil {
				continue
			}
			if err := checkFields(tt.Root); err != nil {
				return nil, fmt.Errorf("command %q: check run: %w", fc.Name, err)
			}
		}
		// A render with zero values finds other execute errors at load.
		if _, err := execute(t, CommandData{}); err != nil {
			return nil, fmt.Errorf("command %q: render run: %w", fc.Name, err)
		}
		cmds = append(cmds, Command{Name: fc.Name, Key: fc.Key, Mode: mode, Run: t})
	}
	return cmds, nil
}

// checkFields returns an error for a field name in the node n that is not
// a field of CommandData. Each CommandData field is a string, so a field of
// a field is also an error.
func checkFields(n parse.Node) error {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := checkFields(c); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return checkFields(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Cmds {
			if err := checkFields(c); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			if err := checkFields(a); err != nil {
				return err
			}
		}
	case *parse.FieldNode:
		return checkField(n.Ident)
	case *parse.VariableNode:
		// $ is the CommandData value. Another variable holds a value
		// that the walk does not know.
		if n.Ident[0] == "$" && len(n.Ident) > 1 {
			return checkField(n.Ident[1:])
		}
	case *parse.ChainNode:
		if err := checkFields(n.Node); err != nil {
			return err
		}
		if len(n.Field) > 0 {
			return fmt.Errorf("field %q of a string", n.Field[0])
		}
	case *parse.IfNode:
		return checkBranch(&n.BranchNode)
	case *parse.RangeNode:
		return checkBranch(&n.BranchNode)
	case *parse.WithNode:
		return checkBranch(&n.BranchNode)
	case *parse.TemplateNode:
		return checkFields(n.Pipe)
	}
	return nil
}

func checkBranch(b *parse.BranchNode) error {
	if err := checkFields(b.Pipe); err != nil {
		return err
	}
	if err := checkFields(b.List); err != nil {
		return err
	}
	return checkFields(b.ElseList)
}

// checkField returns an error if ident is not one CommandData field.
func checkField(ident []string) error {
	if _, ok := reflect.TypeFor[CommandData]().FieldByName(ident[0]); !ok {
		return fmt.Errorf("unknown field %q", ident[0])
	}
	if len(ident) > 1 {
		return fmt.Errorf("field %q of a string", ident[1])
	}
	return nil
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
