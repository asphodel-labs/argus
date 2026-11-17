package poller

import (
	"cmp"
	"slices"
	"strings"

	"github.com/asphodel-labs/argus/internal/model"
)

// EventKind is the kind of a notification event.
type EventKind int

// Event kinds in the order of the events table.
const (
	EventReviewRequested EventKind = iota
	EventActivity
	EventCIResult
)

// Event is one change between two seen states that the user gets a
// notification for.
type Event struct {
	Kind   EventKind
	PRID   string
	Repo   string
	Number int
	Title  string
	URL    string
	// Rollup is the new rollup state of an EventCIResult.
	Rollup model.CheckState
	// Author is the newest author of an EventActivity.
	Author string
}

// PRState is the part of a PR that the event diff compares.
type PRState struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// ReviewRequested is true if the PR is in the needs-my-review scope.
	ReviewRequested bool `json:"review_requested,omitempty"`
	// Mine is true if the PR is in the my-PRs scope.
	Mine         bool             `json:"mine,omitempty"`
	CommentCount int              `json:"comment_count"`
	ReviewCount  int              `json:"review_count"`
	ThreadCount  int              `json:"thread_count"`
	NewestAuthor string           `json:"newest_author,omitempty"`
	Rollup       model.CheckState `json:"rollup,omitempty"`
}

// Seen is the seen state: the PR state for each PR id. It holds only the
// PRs of the needs-my-review and my-PRs scopes, because no event rule reads
// the other scopes.
type Seen map[string]PRState

// SeenFrom returns the seen state of snap.
func SeenFrom(snap *Snapshot) Seen {
	seen := Seen{}
	if snap == nil {
		return seen
	}
	for _, sec := range snap.Sections {
		if sec.Scope.Kind != ScopeNeedsReview && sec.Scope.Kind != ScopeMine {
			continue
		}
		for _, pr := range sec.PRs {
			st, ok := seen[pr.ID]
			if !ok {
				st = PRState{
					Repo:         pr.Repo.FullName(),
					Number:       pr.Number,
					Title:        pr.Title,
					URL:          pr.URL,
					CommentCount: pr.CommentCount,
					ReviewCount:  pr.ReviewCount,
					ThreadCount:  pr.ThreadCount,
					NewestAuthor: pr.NewestAuthor.Login,
					Rollup:       pr.Rollup,
				}
			}
			if sec.Scope.Kind == ScopeNeedsReview {
				st.ReviewRequested = true
			} else {
				st.Mine = true
			}
			seen[pr.ID] = st
		}
	}
	return seen
}

// Diff returns the events between the seen states prev and next for the
// user me. The rules follow the events table in the plan:
//   - Review requested: the PR is in the needs-my-review scope of next and
//     was not in it in prev.
//   - Activity on my PR: the comment, review, or thread count went up, and
//     the newest author is not me.
//   - CI result on my PR: the rollup changed from PENDING to SUCCESS,
//     FAILURE, or ERROR.
//
// The events are in the order of kind, repo, and number.
func Diff(prev, next Seen, me string) []Event {
	var events []Event
	for id, n := range next {
		p, had := prev[id]
		if n.ReviewRequested && !(had && p.ReviewRequested) {
			events = append(events, newEvent(EventReviewRequested, id, n))
		}
		if !n.Mine || !had || !p.Mine {
			continue
		}
		if activityUp(p, n) && !strings.EqualFold(n.NewestAuthor, me) {
			ev := newEvent(EventActivity, id, n)
			ev.Author = n.NewestAuthor
			events = append(events, ev)
		}
		if p.Rollup == model.CheckPending && ciDone(n.Rollup) {
			ev := newEvent(EventCIResult, id, n)
			ev.Rollup = n.Rollup
			events = append(events, ev)
		}
	}
	slices.SortFunc(events, func(a, b Event) int {
		return cmp.Or(
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(a.Number, b.Number),
		)
	})
	return events
}

func newEvent(kind EventKind, id string, st PRState) Event {
	return Event{Kind: kind, PRID: id, Repo: st.Repo, Number: st.Number, Title: st.Title, URL: st.URL}
}

// activityUp reports whether a comment, review, or thread count went up.
func activityUp(prev, next PRState) bool {
	return next.CommentCount > prev.CommentCount ||
		next.ReviewCount > prev.ReviewCount ||
		next.ThreadCount > prev.ThreadCount
}

// ciDone reports whether s is a final rollup state.
func ciDone(s model.CheckState) bool {
	return s == model.CheckSuccess || s == model.CheckFailure || s == model.CheckError
}
