package tui

import (
	"strings"

	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/poller"
)

// markState is the state mark of a PR: none, a good change, or a bad change.
type markState int

// State marks. The order matters: a bad change wins over a good change in
// one diff.
const (
	stateNone markState = iota
	stateGood
	stateBad
)

// markSet holds the change marks of one PR. A full refresh adds marks. The
// marks clear when the cursor leaves the PR row.
type markSet struct {
	New, Activity, Commits bool
	// State is stateNone, stateGood, or stateBad.
	State markState
}

// empty reports whether the set has no mark.
func (a markSet) empty() bool {
	return a == markSet{}
}

// merge returns a with the marks of b added. b is the newer set. The flags
// accumulate. The state of b replaces the state of a if b has a state.
func (a markSet) merge(b markSet) markSet {
	out := markSet{
		New:      a.New || b.New,
		Activity: a.Activity || b.Activity,
		Commits:  a.Commits || b.Commits,
		State:    a.State,
	}
	if b.State != stateNone {
		out.State = b.State
	}
	return out
}

// diffMarks returns the new marks for each PR ID between the snapshots prev
// and next for the user me. The rules follow the marks table in the plan:
//   - New: the ID is not in prev, and CreatedAt is after prev.RefreshedAt.
//   - Activity: a comment, review, or thread count went up, and the newest
//     author is not me. Also: the ID is not in prev, and CreatedAt is not
//     after prev.RefreshedAt. Such a PR only entered the loaded pages.
//   - Commits: both head SHAs are set, and they differ.
//   - State: the rollup or the review decision changed to a good or a bad
//     value. A bad change wins over a good change.
//
// The first section in list order gives the summary of a PR. A nil prev or
// next gives no marks. The result holds only PRs with at least one mark.
func diffMarks(prev, next *poller.Snapshot, me string) map[string]markSet {
	marks := map[string]markSet{}
	if prev == nil || next == nil {
		return marks
	}
	old := summaries(prev)
	seen := map[string]bool{}
	for _, sec := range next.Sections {
		for _, n := range sec.PRs {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			var m markSet
			if p, ok := old[n.ID]; ok {
				m = diffPR(p, n, me)
			} else if n.CreatedAt.After(prev.RefreshedAt) {
				m.New = true
			} else {
				m.Activity = true
			}
			if !m.empty() {
				marks[n.ID] = m
			}
		}
	}
	return marks
}

// summaries returns the summary of each PR ID in s. The first section in
// list order gives the summary.
func summaries(s *poller.Snapshot) map[string]model.PR {
	out := map[string]model.PR{}
	for _, sec := range s.Sections {
		for _, pr := range sec.PRs {
			if _, ok := out[pr.ID]; !ok {
				out[pr.ID] = pr
			}
		}
	}
	return out
}

// diffPR returns the marks between two summaries of one PR.
func diffPR(p, n model.PR, me string) markSet {
	var m markSet
	countUp := n.CommentCount > p.CommentCount ||
		n.ReviewCount > p.ReviewCount ||
		n.ThreadCount > p.ThreadCount
	if countUp && !strings.EqualFold(n.NewestAuthor.Login, me) {
		m.Activity = true
	}
	if p.HeadSHA != "" && n.HeadSHA != "" && p.HeadSHA != n.HeadSHA {
		m.Commits = true
	}
	if n.Rollup != p.Rollup {
		m.State = max(m.State, rollupState(n.Rollup))
	}
	if n.ReviewDecision != p.ReviewDecision {
		m.State = max(m.State, decisionState(n.ReviewDecision))
	}
	return m
}

// rollupState returns the state mark for a new rollup value.
func rollupState(s model.CheckState) markState {
	switch s {
	case model.CheckSuccess:
		return stateGood
	case model.CheckFailure, model.CheckError:
		return stateBad
	}
	return stateNone
}

// decisionState returns the state mark for a new review decision.
func decisionState(d model.ReviewDecision) markState {
	switch d {
	case model.DecisionApproved:
		return stateGood
	case model.DecisionChangesRequested:
		return stateBad
	}
	return stateNone
}
