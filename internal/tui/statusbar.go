package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// MessageTimeout is the time that a transient message stays in the status
// bar.
const MessageTimeout = 4 * time.Second

// StatusBar is the bottom line of the TUI. It shows the active filters, the
// time of the last refresh, the last refresh error in red, and a transient
// message.
type StatusBar struct {
	// Filters is the text of the active list filters. Empty means no filter.
	Filters     string
	LastRefresh time.Time
	// Err is the error of the last refresh. A successful refresh clears it.
	Err         error
	PausedUntil time.Time

	message   string
	messageID int
}

// clearMessageMsg clears the transient message with the same id.
type clearMessageMsg struct{ id int }

// SetMessage shows a transient message. The returned command clears it
// after MessageTimeout. A newer message replaces an older one, and the timer
// of the older message does not clear the newer one.
func (s *StatusBar) SetMessage(text string) tea.Cmd {
	s.messageID++
	s.message = text
	id := s.messageID
	return tea.Tick(MessageTimeout, func(time.Time) tea.Msg { return clearMessageMsg{id: id} })
}

// Message returns the current transient message.
func (s *StatusBar) Message() string { return s.message }

func (s *StatusBar) clearMessage(id int) {
	if id == s.messageID {
		s.message = ""
	}
}

// Render returns the status bar for the width.
func (s *StatusBar) Render(width int, st Styles) string {
	var left []string
	if s.Filters != "" {
		left = append(left, "filters: "+s.Filters)
	}
	switch {
	case s.Err != nil:
		left = append(left, st.Error.Render("refresh failed: "+firstLine(s.Err.Error())))
	case s.LastRefresh.IsZero():
		left = append(left, st.Muted.Render("loading…"))
	default:
		left = append(left, st.Muted.Render("refreshed "+s.LastRefresh.Local().Format("15:04")))
	}
	if !s.PausedUntil.IsZero() {
		left = append(left, st.Error.Render("rate limit: paused until "+s.PausedUntil.Local().Format("15:04")))
	}
	if s.message != "" {
		left = append(left, st.Message.Render(s.message))
	}
	line := strings.Join(left, st.Muted.Render(" │ "))
	right := st.Muted.Render("? help")
	gap := width - lipgloss.Width(line) - lipgloss.Width(right)
	if gap < 1 {
		return ansi.Truncate(line, width, "…")
	}
	return line + strings.Repeat(" ", gap) + right
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
