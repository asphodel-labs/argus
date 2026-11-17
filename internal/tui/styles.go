package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/asphodel-labs/argus/internal/model"
)

// Color names for the [colors] table of the config. Each status has one
// name. The other names color the frame of the TUI.
const (
	ColorFailed   = "failed"
	ColorChanges  = "changes"
	ColorReply    = "reply"
	ColorDraft    = "draft"
	ColorConflict = "conflict"
	ColorPending  = "pending"
	ColorApproved = "approved"
	ColorAccent   = "accent"
	ColorError    = "error"
	ColorMuted    = "muted"
)

// defaultColors are the 16 ANSI colors of the status table.
var defaultColors = map[string]string{
	ColorFailed:   "1",
	ColorChanges:  "3",
	ColorReply:    "3",
	ColorDraft:    "8",
	ColorConflict: "1",
	ColorPending:  "4",
	ColorApproved: "2",
	ColorAccent:   "6",
	ColorError:    "1",
	ColorMuted:    "8",
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Styles are the Lip Gloss styles of the TUI.
type Styles struct {
	Status map[model.Status]lipgloss.Style

	PaneBorder        lipgloss.Style
	PaneBorderFocused lipgloss.Style
	Dialog            lipgloss.Style
	Title             lipgloss.Style
	Muted             lipgloss.Style
	Error             lipgloss.Style
	Message           lipgloss.Style
	HelpKey           lipgloss.Style
}

// NewStyles returns the styles for the default colors and the overrides
// from the [colors] table. A value is an ANSI color number (0 to 255) or
// #rrggbb. An unknown name or a bad value is an error.
func NewStyles(overrides map[string]string) (Styles, error) {
	colors := make(map[string]color.Color, len(defaultColors))
	for name, value := range defaultColors {
		colors[name] = lipgloss.Color(value)
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		value := strings.TrimSpace(overrides[name])
		if _, ok := defaultColors[name]; !ok {
			return Styles{}, fmt.Errorf("unknown color name %q in [colors]", name)
		}
		if !validColor(value) {
			return Styles{}, fmt.Errorf("color %s %q: use an ANSI number from 0 to 255 or #rrggbb", name, value)
		}
		colors[name] = lipgloss.Color(value)
	}

	fg := func(name string) lipgloss.Style { return lipgloss.NewStyle().Foreground(colors[name]) }
	border := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	return Styles{
		Status: map[model.Status]lipgloss.Style{
			model.StatusCIFailed:         fg(ColorFailed),
			model.StatusChangesRequested: fg(ColorChanges),
			model.StatusNeedsReply:       fg(ColorReply),
			model.StatusDraft:            fg(ColorDraft).Faint(true),
			model.StatusConflict:         fg(ColorConflict),
			model.StatusPendingReview:    fg(ColorPending),
			model.StatusApproved:         fg(ColorApproved),
		},
		PaneBorder:        border.BorderForeground(colors[ColorMuted]),
		PaneBorderFocused: border.BorderForeground(colors[ColorAccent]),
		Dialog:            border.BorderForeground(colors[ColorAccent]).Padding(0, 1),
		Title:             fg(ColorAccent).Bold(true),
		Muted:             fg(ColorMuted),
		Error:             fg(ColorError).Bold(true),
		Message:           fg(ColorAccent),
		HelpKey:           fg(ColorAccent).Bold(true),
	}, nil
}

func validColor(value string) bool {
	if hexColor.MatchString(value) {
		return true
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 0 && n <= 255
}
