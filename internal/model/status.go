package model

import "strings"

// Status is the derived status of a PR for the list pane.
type Status int

// Statuses in the order of the status table. DeriveStatus returns the first
// status whose rule matches.
const (
	StatusCIFailed Status = iota
	StatusChangesRequested
	StatusNeedsReply
	StatusDraft
	StatusConflict
	StatusPendingReview
	StatusApproved
)

// Icon returns the status icon.
func (s Status) Icon() string {
	switch s {
	case StatusCIFailed:
		return "✗"
	case StatusChangesRequested:
		return "✎"
	case StatusNeedsReply:
		return "💬"
	case StatusDraft:
		return "◌"
	case StatusConflict:
		return "⊘"
	case StatusApproved:
		return "✓"
	default:
		return "⏳"
	}
}

// String returns the status name.
func (s Status) String() string {
	switch s {
	case StatusCIFailed:
		return "CI failed"
	case StatusChangesRequested:
		return "Changes requested"
	case StatusNeedsReply:
		return "Needs reply"
	case StatusDraft:
		return "Draft"
	case StatusConflict:
		return "Conflict"
	case StatusApproved:
		return "Approved"
	default:
		return "Pending review"
	}
}

// DeriveStatus returns the status of pr for the user me.
// The rules follow the status table in the plan. The first match wins.
// A PR that matches no rule, for example an approved PR with pending CI,
// gets StatusPendingReview.
func DeriveStatus(pr PR, me string) Status {
	switch {
	case pr.Rollup == CheckFailure || pr.Rollup == CheckError:
		return StatusCIFailed
	case pr.ReviewDecision == DecisionChangesRequested:
		return StatusChangesRequested
	case needsReply(pr, me):
		return StatusNeedsReply
	case pr.IsDraft:
		return StatusDraft
	case pr.Mergeable == MergeableConflicting:
		return StatusConflict
	case pr.ReviewDecision == DecisionApproved && ciGreen(pr.Rollup):
		return StatusApproved
	default:
		return StatusPendingReview
	}
}

// ciGreen reports whether the rollup is a success. A commit with no checks
// counts as green.
func ciGreen(s CheckState) bool {
	return s == CheckSuccess || s == CheckNone
}

// needsReply reports whether pr has an unresolved thread whose last comment
// is not from me.
func needsReply(pr PR, me string) bool {
	for _, t := range pr.Threads {
		if t.IsResolved {
			continue
		}
		last := t.LastComment()
		if last != nil && !strings.EqualFold(last.Author.Login, me) {
			return true
		}
	}
	return false
}

// IsBot reports whether author is a bot. An author is a bot if its GraphQL
// typename is Bot or if its login is in botLogins. The login compare ignores
// case, as GitHub logins do.
func IsBot(author Actor, botLogins []string) bool {
	if author.Typename == TypenameBot {
		return true
	}
	for _, login := range botLogins {
		if strings.EqualFold(author.Login, login) {
			return true
		}
	}
	return false
}
