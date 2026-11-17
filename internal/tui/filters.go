package tui

import (
	"strings"

	"github.com/asphodel-labs/argus/internal/model"
)

// BotFilter selects the PRs of bots in the list.
type BotFilter int

// Bot filter values in the order of the `b` cycle.
const (
	BotsAll BotFilter = iota
	BotsOnly
	BotsNone
)

// next returns the next value of the `b` cycle.
func (b BotFilter) next() BotFilter {
	return (b + 1) % 3
}

// Filters are the list filters. They apply to every section except My PRs.
type Filters struct {
	HideApproved bool
	HideDrafts   bool
	Bots         BotFilter
	// Author shows only the PRs of this login. Empty shows all authors.
	Author string
}

// Match reports whether the filters keep pr.
func (f Filters) Match(pr model.PR, botLogins []string) bool {
	if f.HideApproved && pr.ReviewDecision == model.DecisionApproved {
		return false
	}
	if f.HideDrafts && pr.IsDraft {
		return false
	}
	switch f.Bots {
	case BotsOnly:
		if !model.IsBot(pr.Author, botLogins) {
			return false
		}
	case BotsNone:
		if model.IsBot(pr.Author, botLogins) {
			return false
		}
	}
	if f.Author != "" && !strings.EqualFold(pr.Author.Login, f.Author) {
		return false
	}
	return true
}

// String returns the text of the active filters for the status bar. It is
// empty when no filter is active.
func (f Filters) String() string {
	var parts []string
	if f.HideApproved {
		parts = append(parts, "no approved")
	}
	if f.HideDrafts {
		parts = append(parts, "no drafts")
	}
	switch f.Bots {
	case BotsOnly:
		parts = append(parts, "only bots")
	case BotsNone:
		parts = append(parts, "no bots")
	}
	if f.Author != "" {
		parts = append(parts, "author "+f.Author)
	}
	return strings.Join(parts, ", ")
}
