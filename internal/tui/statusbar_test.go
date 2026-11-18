package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/asphodel-labs/argus/internal/model"
)

func TestStatusBarRender(t *testing.T) {
	st := testStyles(t)
	at := time.Date(2026, 11, 16, 9, 30, 0, 0, time.Local)
	tests := []struct {
		name string
		bar  StatusBar
		want []string
		not  []string
	}{
		{name: "loading", want: []string{"loading…", "? help"}},
		{
			name: "refreshed with filters",
			bar:  StatusBar{Filters: "no approved", LastRefresh: at},
			want: []string{"filters: no approved", "refreshed 09:30"},
		},
		{
			name: "error replaces the refresh time",
			bar:  StatusBar{LastRefresh: at, Err: errors.New("HTTP 502\nbody")},
			want: []string{"refresh failed: HTTP 502"},
			not:  []string{"refreshed", "body"},
		},
		{
			name: "rate limit pause",
			bar:  StatusBar{LastRefresh: at, PausedUntil: at.Add(time.Hour)},
			want: []string{"paused until 10:30"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ansi.Strip(tt.bar.Render(120, st))
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("status bar %q has no %q", out, w)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(out, n) {
					t.Errorf("status bar %q has %q", out, n)
				}
			}
			if w := lipgloss.Width(out); w != 120 {
				t.Errorf("width = %d, want 120", w)
			}
		})
	}
}

func TestStatusBarErrorIsRed(t *testing.T) {
	st := testStyles(t)
	bar := StatusBar{Err: errors.New("boom")}
	want := st.Error.Render("refresh failed: boom")
	if out := bar.Render(80, st); !strings.Contains(out, want) {
		t.Fatalf("status bar %q does not contain the error style %q", out, want)
	}
}

func TestStatusBarTruncates(t *testing.T) {
	bar := StatusBar{Filters: strings.Repeat("x", 100)}
	if w := lipgloss.Width(bar.Render(40, testStyles(t))); w > 40 {
		t.Fatalf("width = %d, want at most 40", w)
	}
}

func TestStatusBarMessage(t *testing.T) {
	var bar StatusBar
	if cmd := bar.SetMessage("first"); cmd == nil {
		t.Fatal("SetMessage returned no clear command")
	}
	bar.SetMessage("second")
	// The timer of the first message does not clear the second message.
	bar.clearMessage(1)
	if bar.Message() != "second" {
		t.Fatalf("message = %q, want second", bar.Message())
	}
	if out := ansi.Strip(bar.Render(80, testStyles(t))); !strings.Contains(out, "second") {
		t.Fatalf("status bar %q has no message", out)
	}
	bar.clearMessage(2)
	if bar.Message() != "" {
		t.Fatalf("message = %q after its timer, want empty", bar.Message())
	}
}

func TestNewStyles(t *testing.T) {
	st, err := NewStyles(map[string]string{"approved": "#00ff00", "failed": "196"})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Status[model.StatusApproved].GetForeground(); got != lipgloss.Color("#00ff00") {
		t.Errorf("approved color = %v, want #00ff00", got)
	}
	if got := st.Status[model.StatusCIFailed].GetForeground(); got != lipgloss.Color("196") {
		t.Errorf("failed color = %v, want 196", got)
	}
	if got := st.Status[model.StatusPendingReview].GetForeground(); got != lipgloss.Color("4") {
		t.Errorf("pending color = %v, want the default 4", got)
	}
	for s := model.StatusCIFailed; s <= model.StatusApproved; s++ {
		if _, ok := st.Status[s]; !ok {
			t.Errorf("no style for status %v", s)
		}
	}
}

func TestNewStylesMarkColors(t *testing.T) {
	st, err := NewStyles(map[string]string{"mark_new": "#ff8800", "approved": "10"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		style lipgloss.Style
		want  string
	}{
		{"mark_new override", st.MarkNew, "#ff8800"},
		{"mark_activity default", st.MarkActivity, "33"},
		{"mark_commits default", st.MarkCommits, "135"},
		{"good state uses approved", st.MarkGood, "10"},
		{"bad state uses failed", st.MarkBad, "1"},
	}
	for _, tt := range tests {
		if got := tt.style.GetForeground(); got != lipgloss.Color(tt.want) {
			t.Errorf("%s: color = %v, want %s", tt.name, got, tt.want)
		}
	}
}

func TestNewStylesErrors(t *testing.T) {
	tests := map[string]map[string]string{
		"unknown name": {"aproved": "2"},
		"number 256":   {"approved": "256"},
		"short hex":    {"approved": "#0f0"},
		"color name":   {"approved": "green"},
		"empty":        {"approved": ""},
	}
	for name, colors := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewStyles(colors); err == nil {
				t.Fatal("no error")
			}
		})
	}
}
