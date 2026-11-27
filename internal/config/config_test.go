package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asphodel-labs/argus/internal/model"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RefreshInterval != DefaultRefreshInterval {
		t.Errorf("RefreshInterval = %s, want %s", cfg.RefreshInterval, DefaultRefreshInterval)
	}
	if cfg.MinRefreshGap != DefaultMinRefreshGap {
		t.Errorf("MinRefreshGap = %s, want %s", cfg.MinRefreshGap, DefaultMinRefreshGap)
	}
	if want := filepath.Join(home, "sources"); cfg.RepoRoot != want {
		t.Errorf("RepoRoot = %q, want %q", cfg.RepoRoot, want)
	}
	if cfg.CloneProtocol != CloneSSH {
		t.Errorf("CloneProtocol = %q, want %q", cfg.CloneProtocol, CloneSSH)
	}
	if cfg.User != "" || len(cfg.Orgs) != 0 || len(cfg.BotLogins) != 0 {
		t.Errorf("unexpected values: %+v", cfg)
	}
	if cfg.Repos == nil || cfg.Owners == nil || cfg.Colors == nil {
		t.Errorf("maps must not be nil: %+v", cfg)
	}
}

func TestLoadEmptyFileGivesDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RefreshInterval != DefaultRefreshInterval {
		t.Errorf("RefreshInterval = %s, want %s", cfg.RefreshInterval, DefaultRefreshInterval)
	}
}

func TestLoadFullFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, `
orgs = ["acme", "globex"]
user = "octocat"
refresh_interval = "5m30s"
min_refresh_gap = "2m"
repo_root = "~/work"
clone_protocol = "https"
bot_logins = ["renovate-bot"]
quiet_hours = "22:00-08:30"

[repos]
"acme/api" = "~/work/api"
"acme/web" = "/opt/web"

[owners]
"globex" = "~/sources/globex"

[colors]
approved = "2"
failed = "#ff0000"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := &Config{
		Orgs:            []string{"acme", "globex"},
		User:            "octocat",
		RefreshInterval: 5*time.Minute + 30*time.Second,
		MinRefreshGap:   2 * time.Minute,
		RepoRoot:        filepath.Join(home, "work"),
		CloneProtocol:   CloneHTTPS,
		BotLogins:       []string{"renovate-bot"},
		Repos: map[string]string{
			"acme/api": filepath.Join(home, "work", "api"),
			"acme/web": "/opt/web",
		},
		Owners:     map[string]string{"globex": filepath.Join(home, "sources", "globex")},
		Colors:     map[string]string{"approved": "2", "failed": "#ff0000"},
		QuietHours: QuietHours{Start: 22 * 60, End: 8*60 + 30},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load =\n%+v\nwant\n%+v", cfg, want)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name string
		body string
		want string
	}{
		{"bad duration", `refresh_interval = "ten minutes"`, "parse refresh_interval"},
		{"negative duration", `refresh_interval = "-1m"`, "must be positive"},
		{"zero duration", `refresh_interval = "0s"`, "must be positive"},
		{"bad gap", `min_refresh_gap = "soon"`, "parse min_refresh_gap"},
		{"negative gap", `min_refresh_gap = "-5m"`, "min_refresh_gap \"-5m\" must be positive"},
		{"zero gap", `min_refresh_gap = "0s"`, "must be positive"},
		{"bad clone protocol", `clone_protocol = "git"`, `clone_protocol "git" must be "ssh" or "https"`},
		{"quiet no dash", `quiet_hours = "22:00"`, "must be HH:MM-HH:MM"},
		{"quiet bad start", `quiet_hours = "25:00-08:00"`, `time "25:00" must be HH:MM`},
		{"quiet bad end", `quiet_hours = "22:00-8am"`, `time "8am" must be HH:MM`},
		{"quiet same", `quiet_hours = "08:00-08:00"`, "start and end are the same"},
		{"bad toml", `orgs = [`, "parse config"},
		{"wrong type", `orgs = "acme"`, "parse config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(writeConfig(t, `
[[commands]]
name = "stack-sync"
key = "S"
run = "gh stack sync"

[[commands]]
name = "checkout"
mode = "overlay"
run = "git switch {{.Branch}}"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Commands) != 2 {
		t.Fatalf("len(Commands) = %d, want 2", len(cfg.Commands))
	}
	pr := model.PR{HeadRef: "feature/x"}
	tests := []struct {
		name, key, mode, script string
	}{
		{"stack-sync", "S", ModeBackground, "gh stack sync"},
		{"checkout", "", ModeOverlay, "git switch 'feature/x'"},
	}
	for i, tt := range tests {
		c := cfg.Commands[i]
		if c.Name != tt.name || c.Key != tt.key || c.Mode != tt.mode {
			t.Errorf("Commands[%d] = {%q %q %q}, want {%q %q %q}", i, c.Name, c.Key, c.Mode, tt.name, tt.key, tt.mode)
		}
		got, err := c.Render(pr)
		if err != nil {
			t.Fatalf("Render %s: %v", c.Name, err)
		}
		if got != tt.script {
			t.Errorf("Render %s = %q, want %q", c.Name, got, tt.script)
		}
	}
}

func TestLoadCommandBranches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(writeConfig(t, `
[[commands]]
name = "open"
run = "{{$b := .Branch}}{{if .Title}}echo {{$b}} {{$.Base}}{{else}}echo {{.Number}}{{end}}{{with .URL}} {{.}}{{end}}"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Commands[0].Render(model.PR{HeadRef: "x", BaseRef: "main", Title: "T", URL: "u"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if want := "echo 'x' 'main' 'u'"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestCommandRenderQuotesFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(writeConfig(t, `
[[commands]]
name = "all"
run = "echo {{.Owner}} {{.Repo}} {{.Number}} {{.URL}} {{.Branch}} {{.Base}} {{.Title}}"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pr := model.PR{
		Repo:    model.Repo{Owner: "acme", Name: "api"},
		Number:  42,
		URL:     "https://github.com/acme/api/pull/42",
		HeadRef: "it's; rm -rf ~",
		BaseRef: "main",
		Title:   "Fix $(whoami)",
	}
	got, err := cfg.Commands[0].Render(pr)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := `echo 'acme' 'api' '42' 'https://github.com/acme/api/pull/42' 'it'\''s; rm -rf ~' 'main' 'Fix $(whoami)'`
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

func TestLoadCommandErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty name", `[[commands]]
run = "true"`, "commands[0]: name is empty"},
		{"name with space", `[[commands]]
name = "stack sync"
run = "true"`, `command "stack sync": name has a space`},
		{"duplicate name", `[[commands]]
name = "sync"
run = "true"
[[commands]]
name = "sync"
run = "false"`, `command "sync": name is not unique`},
		{"built-in name", `[[commands]]
name = "draft"
run = "true"`, `command "draft": name is a built-in command`},
		{"quit name", `[[commands]]
name = "q"
run = "true"`, `command "q": name is a built-in command`},
		{"bad mode", `[[commands]]
name = "sync"
mode = "popup"
run = "true"`, `mode "popup" must be "background" or "overlay"`},
		{"bad template", `[[commands]]
name = "sync"
run = "echo {{.Branch"`, `command "sync": parse run`},
		{"unknown field", `[[commands]]
name = "sync"
run = "echo {{.Head}}"`, `command "sync": check run: unknown field "Head"`},
		{"unknown field in a branch", `[[commands]]
name = "sync"
run = "{{if .Title}}echo {{.Titel}}{{end}}"`, `command "sync": check run: unknown field "Titel"`},
		{"unknown field in an else branch", `[[commands]]
name = "sync"
run = "{{if eq .Number \"'1'\"}}true{{else}}echo {{$.Titel}}{{end}}"`, `unknown field "Titel"`},
		{"unknown field in a defined template", `[[commands]]
name = "sync"
run = "{{define \"t\"}}{{.Titel}}{{end}}{{if .Title}}{{template \"t\" .}}{{end}}"`, `unknown field "Titel"`},
		{"field of a field", `[[commands]]
name = "sync"
run = "{{with .Title}}{{end}}echo {{.Title.Len}}"`, `field "Len" of a string`},
		{"missing run", `[[commands]]
name = "sync"
command = "gh stack sync"`, `command "sync": run is empty`},
		{"blank run", `[[commands]]
name = "sync"
run = "  "`, `command "sync": run is empty`},
		{"duplicate key", `[[commands]]
name = "a"
key = "S"
run = "true"
[[commands]]
name = "b"
key = "S"
run = "true"`, `command "b": key "S" is also the key of command "a"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadUnreadableFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A directory in place of the file gives a read error, not the defaults.
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("Load error = %v, want a read error", err)
	}
}

type fakeRunner struct {
	stdout, stderr string
	code           int
	err            error
	calls          [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.stdout, f.stderr, f.code, f.err
}

func TestResolveUser(t *testing.T) {
	t.Run("keeps configured user", func(t *testing.T) {
		r := &fakeRunner{}
		cfg := &Config{User: "octocat"}
		if err := ResolveUser(context.Background(), cfg, r); err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 0 {
			t.Errorf("runner called: %v", r.calls)
		}
		if cfg.User != "octocat" {
			t.Errorf("User = %q", cfg.User)
		}
	})

	t.Run("reads login from gh", func(t *testing.T) {
		r := &fakeRunner{stdout: "hubot\n"}
		cfg := &Config{}
		if err := ResolveUser(context.Background(), cfg, r); err != nil {
			t.Fatal(err)
		}
		if cfg.User != "hubot" {
			t.Errorf("User = %q, want hubot", cfg.User)
		}
		want := [][]string{{"gh", "api", "user", "--jq", ".login"}}
		if !reflect.DeepEqual(r.calls, want) {
			t.Errorf("calls = %v, want %v", r.calls, want)
		}
	})

	errCases := []struct {
		name string
		r    *fakeRunner
		want string
	}{
		{"start failure", &fakeRunner{err: errors.New("not found"), code: -1}, "not found"},
		{"non-zero exit", &fakeRunner{code: 1, stderr: "auth required\n"}, "auth required"},
		{"empty login", &fakeRunner{stdout: "\n"}, "empty login"},
	}
	for _, tt := range errCases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			err := ResolveUser(context.Background(), cfg, tt.r)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
			if cfg.User != "" {
				t.Errorf("User = %q, want empty", cfg.User)
			}
		})
	}
}

func TestQuietHoursContains(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 27, h, m, 0, 0, time.Local) }
	night := QuietHours{Start: 22 * 60, End: 8 * 60}
	day := QuietHours{Start: 12 * 60, End: 13 * 60}
	tests := []struct {
		name string
		q    QuietHours
		t    time.Time
		want bool
	}{
		{"zero value", QuietHours{}, at(3, 0), false},
		{"night before start", night, at(21, 59), false},
		{"night at start", night, at(22, 0), true},
		{"night after midnight", night, at(0, 30), true},
		{"night before end", night, at(7, 59), true},
		{"night at end", night, at(8, 0), false},
		{"day before start", day, at(11, 59), false},
		{"day at start", day, at(12, 0), true},
		{"day at end", day, at(13, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.q.Contains(tt.t); got != tt.want {
				t.Errorf("Contains(%s) = %v, want %v", tt.t.Format("15:04"), got, tt.want)
			}
		})
	}
}
