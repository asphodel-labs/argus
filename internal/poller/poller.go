// Package poller refreshes the PR lists in the background.
//
// The poller checks GET /notifications at the poll interval that GitHub
// sends. It runs a full refresh of all search scopes when new notifications
// arrive, when the refresh interval elapses, or when the user asks for one.
// The package has no TUI imports.
package poller

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
)

// Search queries of the scopes that gh does not define.
const (
	queryMine     = "is:pr is:open author:@me"
	queryOrg      = "is:pr is:open org:%s sort:updated-desc"
	queryPersonal = "is:pr is:open user:%s sort:updated-desc"
)

// RateLimitFloor is the remaining GraphQL rate limit below which the poller
// pauses the full refresh until the rate limit resets.
const RateLimitFloor = 200

// Backoff limits after a failed refresh.
const (
	backoffFirst = time.Minute
	backoffMax   = 10 * time.Minute
)

// updatesBuffer is the capacity of the updates channel.
const updatesBuffer = 8

// GH is the part of the gh client that the poller uses.
type GH interface {
	Search(ctx context.Context, q string, first int, after string) (gh.SearchResult, error)
	SearchAll(ctx context.Context, q string) (gh.SearchResult, error)
	NeedsMyReview(ctx context.Context) (gh.SearchResult, error)
	Notifications(ctx context.Context, ifModifiedSince string) (gh.NotificationsResult, error)
}

// Clock gives the time and timers. Tests use a fake clock.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ScopeKind is the kind of a search scope.
type ScopeKind int

// Scope kinds in section order.
const (
	ScopeNeedsReview ScopeKind = iota
	ScopeMine
	ScopeOrg
	ScopePersonal
)

// Scope is one section of the PR list. Org is set only for ScopeOrg.
type Scope struct {
	Kind ScopeKind
	Org  string
}

// paged reports whether the scope loads pages on request. The other
// scopes load all pages.
func (s Scope) paged() bool {
	return s.Kind == ScopeOrg || s.Kind == ScopePersonal
}

// Section is the PRs of one scope.
type Section struct {
	Scope Scope
	PRs   []model.PR
	// Total is the issueCount of the search. Total minus len(PRs) is the
	// number of PRs that LoadMore can still load.
	Total int
}

// Snapshot is the state of all sections after a refresh or a load more.
type Snapshot struct {
	Sections    []Section
	RefreshedAt time.Time
	RateLimit   gh.RateLimit
	// PausedUntil is set while the rate limit pause holds the full refresh.
	PausedUntil time.Time
}

// Update is one message on the updates channel. It has a snapshot or an
// error. After an error the TUI keeps the last snapshot.
type Update struct {
	Snapshot *Snapshot
	Err      error
}

// Options configure a Poller.
type Options struct {
	User            string
	Orgs            []string
	RefreshInterval time.Duration
	// MinRefreshGap is the minimum time between the start of the last full
	// refresh and a full refresh that new notifications start.
	MinRefreshGap time.Duration
	// Clock is the real clock when nil.
	Clock Clock
}

// scopeState is the paging state of a scope.
type scopeState struct {
	pages   int
	cursor  string
	hasNext bool
}

// Poller runs the poll loop. Create it with New and start it with Run.
type Poller struct {
	gh      GH
	opts    Options
	clock   Clock
	scopes  []Scope
	updates chan Update
	wake    chan struct{}

	mu          sync.Mutex
	refreshReq  bool
	loadMoreReq []Scope

	// The fields below belong to the Run goroutine.
	state        map[Scope]*scopeState
	snapshot     *Snapshot
	lastModified string
	nextPoll     time.Time
	lastRefresh  time.Time // start of the last full refresh
	lastSuccess  time.Time // start of the last full refresh that succeeded
	notifyDue    bool      // new notifications wait for a full refresh
	userDue      bool      // a user request waits for the rate limit pause
	failures     int
	retryAt      time.Time
	pausedUntil  time.Time
}

// New returns a Poller for the scopes of opts.
func New(g GH, opts Options) *Poller {
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	scopes := []Scope{{Kind: ScopeNeedsReview}, {Kind: ScopeMine}}
	for _, org := range opts.Orgs {
		scopes = append(scopes, Scope{Kind: ScopeOrg, Org: org})
	}
	scopes = append(scopes, Scope{Kind: ScopePersonal})

	state := make(map[Scope]*scopeState, len(scopes))
	for _, s := range scopes {
		state[s] = &scopeState{pages: 1}
	}
	return &Poller{
		gh:      g,
		opts:    opts,
		clock:   clock,
		scopes:  scopes,
		updates: make(chan Update, updatesBuffer),
		wake:    make(chan struct{}, 1),
		state:   state,
	}
}

// Updates returns the channel of snapshots and errors. Run closes it when
// it returns.
func (p *Poller) Updates() <-chan Update { return p.updates }

// RefreshNow asks for a full refresh. The refresh skips the minimum gap and
// the backoff. It waits only for the rate limit pause. The call does not
// block.
func (p *Poller) RefreshNow() {
	p.mu.Lock()
	p.refreshReq = true
	p.mu.Unlock()
	p.signal()
}

// LoadMore asks for the next page of a paged scope (an org or personal).
// The request skips the minimum gap. The call does not block.
func (p *Poller) LoadMore(scope Scope) {
	p.mu.Lock()
	p.loadMoreReq = append(p.loadMoreReq, scope)
	p.mu.Unlock()
	p.signal()
}

func (p *Poller) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run runs the poll loop until ctx ends. It starts with a full refresh.
func (p *Poller) Run(ctx context.Context) error {
	defer close(p.updates)
	p.userDue = true
	p.nextPoll = p.clock.Now()
	for {
		now := p.clock.Now()
		p.handleRequests(ctx, now)
		if !now.Before(p.nextPoll) {
			p.checkNotifications(ctx, now)
		}
		if !now.Before(p.refreshAt()) {
			p.refresh(ctx, now)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		next := p.nextPoll
		if at := p.refreshAt(); at.Before(next) {
			next = at
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.clock.After(next.Sub(now)):
		case <-p.wake:
		}
	}
}

// handleRequests takes the pending user requests. A refresh request makes
// the refresh due at once; load more requests run at once.
func (p *Poller) handleRequests(ctx context.Context, now time.Time) {
	p.mu.Lock()
	refresh, loads := p.refreshReq, p.loadMoreReq
	p.refreshReq, p.loadMoreReq = false, nil
	p.mu.Unlock()

	if refresh {
		p.userDue = true
	}
	for _, s := range loads {
		p.loadMore(ctx, now, s)
	}
}

// refreshAt returns the time of the next full refresh.
func (p *Poller) refreshAt() time.Time {
	var at time.Time
	switch {
	case p.userDue:
		// A user request skips the gap and the backoff.
	case p.failures > 0:
		at = p.retryAt
	default:
		at = p.lastSuccess.Add(p.opts.RefreshInterval)
		if p.notifyDue {
			if gapEnd := p.lastRefresh.Add(p.opts.MinRefreshGap); gapEnd.Before(at) {
				at = gapEnd
			}
		}
	}
	if p.pausedUntil.After(at) {
		at = p.pausedUntil
	}
	return at
}

// checkNotifications sends GET /notifications. A 200 with threads after the
// first check makes a full refresh due at the end of the minimum gap.
func (p *Poller) checkNotifications(ctx context.Context, now time.Time) {
	res, err := p.gh.Notifications(ctx, p.lastModified)
	if err != nil {
		p.nextPoll = now.Add(gh.DefaultPollInterval)
		p.send(ctx, Update{Err: fmt.Errorf("check notifications: %w", err)})
		return
	}
	p.nextPoll = now.Add(res.PollInterval)
	// The first check only records Last-Modified: the start refresh
	// already reads the current state.
	if res.Changed && len(res.Threads) > 0 && p.lastModified != "" {
		p.notifyDue = true
	}
	if res.LastModified != "" {
		p.lastModified = res.LastModified
	}
}

// refresh reads all scopes. On a failure it keeps the last snapshot and
// sets the backoff.
func (p *Poller) refresh(ctx context.Context, now time.Time) {
	p.userDue, p.notifyDue = false, false
	p.lastRefresh = now

	sections, rl, err := p.fetchAll(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		p.failures++
		p.retryAt = now.Add(backoff(p.failures))
		p.send(ctx, Update{Err: fmt.Errorf("refresh: %w", err)})
		return
	}
	p.failures = 0
	p.lastSuccess = now
	p.snapshot = &Snapshot{Sections: sections, RefreshedAt: now}
	p.noteRateLimit(rl)
	p.sendSnapshot(ctx, now)
}

// fetchAll reads every scope. A paged scope reads the page count that the
// user loaded.
func (p *Poller) fetchAll(ctx context.Context) ([]Section, gh.RateLimit, error) {
	sections := make([]Section, 0, len(p.scopes))
	states := make(map[Scope]scopeState, len(p.scopes))
	var rl gh.RateLimit
	for _, s := range p.scopes {
		var (
			res gh.SearchResult
			err error
		)
		switch s.Kind {
		case ScopeNeedsReview:
			res, err = p.gh.NeedsMyReview(ctx)
		case ScopeMine:
			res, err = p.gh.SearchAll(ctx, queryMine)
		default:
			res, err = p.fetchPages(ctx, p.query(s), p.state[s].pages)
		}
		if err != nil {
			return nil, gh.RateLimit{}, fmt.Errorf("%s: %w", s, err)
		}
		rl = res.RateLimit
		sections = append(sections, Section{Scope: s, PRs: res.PRs, Total: res.IssueCount})
		states[s] = scopeState{pages: p.state[s].pages, cursor: res.EndCursor, hasNext: res.HasNextPage}
	}
	// Change the paging state only when every scope succeeded.
	for s, st := range states {
		*p.state[s] = st
	}
	return sections, rl, nil
}

// fetchPages reads up to pages pages of q from the start.
func (p *Poller) fetchPages(ctx context.Context, q string, pages int) (gh.SearchResult, error) {
	var all gh.SearchResult
	after := ""
	for range pages {
		page, err := p.gh.Search(ctx, q, gh.PageSize, after)
		if err != nil {
			return gh.SearchResult{}, err
		}
		all.PRs = append(all.PRs, page.PRs...)
		all.IssueCount = page.IssueCount
		all.EndCursor = page.EndCursor
		all.HasNextPage = page.HasNextPage
		all.RateLimit = page.RateLimit
		if !page.HasNextPage || page.EndCursor == "" {
			break
		}
		after = page.EndCursor
	}
	return all, nil
}

// loadMore reads the next page of a paged scope and adds it to the
// snapshot. A failure sends an error and does not change the backoff.
func (p *Poller) loadMore(ctx context.Context, now time.Time, s Scope) {
	st, ok := p.state[s]
	if !ok || !s.paged() || p.snapshot == nil || !st.hasNext {
		return
	}
	page, err := p.gh.Search(ctx, p.query(s), gh.PageSize, st.cursor)
	if err != nil {
		if ctx.Err() == nil {
			p.send(ctx, Update{Err: fmt.Errorf("load more %s: %w", s, err)})
		}
		return
	}
	st.pages++
	st.cursor = page.EndCursor
	st.hasNext = page.HasNextPage && page.EndCursor != ""

	i := slices.IndexFunc(p.snapshot.Sections, func(sec Section) bool { return sec.Scope == s })
	sec := &p.snapshot.Sections[i]
	// The order of sort:updated-desc can move a PR to the next page.
	// Such a PR stays at its first position.
	for _, pr := range page.PRs {
		if !slices.ContainsFunc(sec.PRs, func(old model.PR) bool { return old.ID == pr.ID }) {
			sec.PRs = append(sec.PRs, pr)
		}
	}
	sec.Total = page.IssueCount
	p.noteRateLimit(page.RateLimit)
	p.sendSnapshot(ctx, now)
}

// noteRateLimit stores rl and starts the pause when the remaining points
// fall below RateLimitFloor.
func (p *Poller) noteRateLimit(rl gh.RateLimit) {
	p.snapshot.RateLimit = rl
	if rl.Remaining < RateLimitFloor && rl.ResetAt.After(p.pausedUntil) {
		p.pausedUntil = rl.ResetAt
	}
}

func (p *Poller) query(s Scope) string {
	if s.Kind == ScopeOrg {
		return fmt.Sprintf(queryOrg, s.Org)
	}
	return fmt.Sprintf(queryPersonal, p.opts.User)
}

// sendSnapshot sends a copy of the snapshot. The copy shares no slices with
// the poller, so a later load more does not change it.
func (p *Poller) sendSnapshot(ctx context.Context, now time.Time) {
	snap := *p.snapshot
	if p.pausedUntil.After(now) {
		snap.PausedUntil = p.pausedUntil
	}
	snap.Sections = slices.Clone(snap.Sections)
	for i := range snap.Sections {
		snap.Sections[i].PRs = slices.Clone(snap.Sections[i].PRs)
	}
	p.send(ctx, Update{Snapshot: &snap})
}

func (p *Poller) send(ctx context.Context, u Update) {
	select {
	case p.updates <- u:
	case <-ctx.Done():
	}
}

// backoff returns the retry delay after n failed refreshes in a row:
// 1, 2, 4, 8, then 10 minutes.
func backoff(n int) time.Duration {
	d := backoffFirst
	for i := 1; i < n && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}

// String returns a short name of the scope for error messages.
func (s Scope) String() string {
	switch s.Kind {
	case ScopeNeedsReview:
		return "needs my review"
	case ScopeMine:
		return "my PRs"
	case ScopeOrg:
		return "org " + s.Org
	default:
		return "personal"
	}
}
