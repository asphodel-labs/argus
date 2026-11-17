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
		Owners: map[string]string{"globex": filepath.Join(home, "sources", "globex")},
		Colors: map[string]string{"approved": "2", "failed": "#ff0000"},
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
