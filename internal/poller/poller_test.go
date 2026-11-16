package poller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
)

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// fakeClock has one sleeper at a time: each After call replaces the
// previous timer, because the poll loop waits on one timer only.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	at     time.Time
	ch     chan time.Time
	sleeps int
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.sleeps++
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.at, c.ch = c.now.Add(d), ch
	return ch
}

// advance moves the time and reports whether the timer fired.
func (c *fakeClock) advance(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	if c.ch != nil && !c.at.After(c.now) {
		c.ch <- c.now
		c.ch = nil
		return true
	}
	return false
}

func (c *fakeClock) sleepCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sleeps
}

type searchCall struct {
	q     string
	after string
}

type fakeGH struct {
	mu          sync.Mutex
	totals      map[string]int // PR count for each paged query
	rl          gh.RateLimit
	reviewErr   error
	searchErr   error
	notif       gh.NotificationsResult
	notifErr    error
	reviewCalls int
	notifCalls  []string // If-Modified-Since of each call
	searches    []searchCall
}

func newFakeGH() *fakeGH {
	return &fakeGH{
		totals: map[string]int{},
		rl:     gh.RateLimit{Remaining: 5000, ResetAt: t0.Add(time.Hour)},
		notif:  gh.NotificationsResult{Changed: true, LastModified: "lm0", PollInterval: time.Hour},
	}
}

func (f *fakeGH) set(fn func(f *fakeGH)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeGH) NeedsMyReview(context.Context) (gh.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewCalls++
	if f.reviewErr != nil {
		return gh.SearchResult{}, f.reviewErr
	}
	return gh.SearchResult{PRs: []model.PR{{ID: "review-1"}}, IssueCount: 1, RateLimit: f.rl}, nil
}

func (f *fakeGH) SearchAll(_ context.Context, q string) (gh.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return gh.SearchResult{PRs: []model.PR{{ID: q}}, IssueCount: 1, RateLimit: f.rl}, nil
}

func (f *fakeGH) Search(_ context.Context, q string, first int, after string) (gh.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches = append(f.searches, searchCall{q, after})
	if f.searchErr != nil {
		return gh.SearchResult{}, f.searchErr
	}
	start := 0
	if after != "" {
		start, _ = strconv.Atoi(strings.TrimPrefix(after, "c"))
	}
	total := f.totals[q]
	end := min(start+first, total)
	res := gh.SearchResult{IssueCount: total, RateLimit: f.rl, HasNextPage: end < total}
	for i := start; i < end; i++ {
		res.PRs = append(res.PRs, model.PR{ID: fmt.Sprintf("%s#%d", q, i)})
	}
	if end > start {
		res.EndCursor = "c" + strconv.Itoa(end)
	}
	return res, nil
}

func (f *fakeGH) Notifications(_ context.Context, ifModifiedSince string) (gh.NotificationsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifCalls = append(f.notifCalls, ifModifiedSince)
	return f.notif, f.notifErr
}

func (f *fakeGH) counts() (reviews, notifs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reviewCalls, len(f.notifCalls)
}

func (f *fakeGH) takeSearches() []searchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.searches
	f.searches = nil
	return s
}

type harness struct {
	t  *testing.T
	fc *fakeClock
	g  *fakeGH
	p  *Poller
}

// start runs a poller and waits until the loop sleeps after the start
// refresh.
func start(t *testing.T, g *fakeGH, opts Options) *harness {
	t.Helper()
	fc := &fakeClock{now: t0}
	opts.Clock = fc
	if opts.User == "" {
		opts.User = "me"
	}
	if opts.RefreshInterval == 0 {
		opts.RefreshInterval = 10 * time.Minute
	}
	if opts.MinRefreshGap == 0 {
		opts.MinRefreshGap = 5 * time.Minute
	}
	p := New(g, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	h := &harness{t: t, fc: fc, g: g, p: p}
	h.waitSleep(0)
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
		for range p.Updates() {
		}
	})
	return h
}

// waitSleep waits until the loop has slept more than n times.
func (h *harness) waitSleep(n int) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.fc.sleepCount() <= n {
		if time.Now().After(deadline) {
			h.t.Fatal("poll loop did not go back to sleep")
		}
		time.Sleep(time.Millisecond)
	}
}

// step advances the clock by d. If the timer fires, it waits until the
// loop has done its work and sleeps again.
func (h *harness) step(d time.Duration) bool {
	h.t.Helper()
	n := h.fc.sleepCount()
	fired := h.fc.advance(d)
	if fired {
		h.waitSleep(n)
	}
	return fired
}

// do runs a request and waits until the loop sleeps again.
func (h *harness) do(fn func()) {
	h.t.Helper()
	n := h.fc.sleepCount()
	fn()
	h.waitSleep(n)
}

// drain returns the updates that the loop sent.
func (h *harness) drain() []Update {
	var out []Update
	for {
		select {
		case u := <-h.p.Updates():
			out = append(out, u)
		default:
			return out
		}
	}
}

// snapshot returns the one snapshot in the sent updates.
func (h *harness) snapshot() *Snapshot {
	h.t.Helper()
	ups := h.drain()
	if len(ups) != 1 || ups[0].Snapshot == nil {
		h.t.Fatalf("updates = %+v, want one snapshot", ups)
	}
	return ups[0].Snapshot
}

func (h *harness) noUpdates() {
	h.t.Helper()
	if ups := h.drain(); len(ups) != 0 {
		h.t.Fatalf("updates = %+v, want none", ups)
	}
}

func (h *harness) wantRefreshes(n int) {
	h.t.Helper()
	if got, _ := h.g.counts(); got != n {
		h.t.Fatalf("full refreshes = %d, want %d", got, n)
	}
}

func notModified(poll time.Duration) gh.NotificationsResult {
	return gh.NotificationsResult{LastModified: "lm0", PollInterval: poll}
}

func changed(poll time.Duration) gh.NotificationsResult {
	return gh.NotificationsResult{
		Changed:      true,
		LastModified: "lm1",
		PollInterval: poll,
		Threads:      []gh.Notification{{ID: "1"}},
	}
}

func TestStartRefreshSections(t *testing.T) {
	g := newFakeGH()
	h := start(t, g, Options{Orgs: []string{"acme", "globex"}})

	snap := h.snapshot()
	if !snap.RefreshedAt.Equal(t0) {
		t.Errorf("RefreshedAt = %s, want %s", snap.RefreshedAt, t0)
	}
	want := []Scope{
		{Kind: ScopeNeedsReview}, {Kind: ScopeMine},
		{Kind: ScopeOrg, Org: "acme"}, {Kind: ScopeOrg, Org: "globex"},
		{Kind: ScopePersonal},
	}
	if len(snap.Sections) != len(want) {
		t.Fatalf("sections = %+v", snap.Sections)
	}
	for i, s := range want {
		if snap.Sections[i].Scope != s {
			t.Errorf("section %d = %v, want %v", i, snap.Sections[i].Scope, s)
		}
	}
	if snap.Sections[1].PRs[0].ID != queryMine {
		t.Errorf("my PRs query = %q, want %q", snap.Sections[1].PRs[0].ID, queryMine)
	}
	wantSearches := []searchCall{
		{"is:pr is:open org:acme sort:updated-desc", ""},
		{"is:pr is:open org:globex sort:updated-desc", ""},
		{"is:pr is:open user:me sort:updated-desc", ""},
	}
	got := g.takeSearches()
	if fmt.Sprint(got) != fmt.Sprint(wantSearches) {
		t.Errorf("searches = %v, want %v", got, wantSearches)
	}
	if !snap.PausedUntil.IsZero() {
		t.Errorf("PausedUntil = %s, want zero", snap.PausedUntil)
	}
}

func TestNotModifiedCausesNoRefresh(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(time.Minute)
	h := start(t, g, Options{})
	h.snapshot()

	for range 3 {
		if !h.step(time.Minute) {
			t.Fatal("poll timer did not fire")
		}
	}
	h.noUpdates()
	h.wantRefreshes(1)
	_, notifs := g.counts()
	if notifs != 4 {
		t.Fatalf("notification checks = %d, want 4", notifs)
	}
	if g.notifCalls[0] != "" || g.notifCalls[1] != "lm0" {
		t.Errorf("If-Modified-Since = %q, want empty then lm0", g.notifCalls)
	}
}

func TestChangedAfterGapRefreshesAtOnce(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(6 * time.Minute)
	h := start(t, g, Options{})
	h.snapshot()

	g.set(func(f *fakeGH) { f.notif = changed(time.Hour) })
	if !h.step(6 * time.Minute) {
		t.Fatal("poll timer did not fire")
	}
	snap := h.snapshot()
	if want := t0.Add(6 * time.Minute); !snap.RefreshedAt.Equal(want) {
		t.Errorf("RefreshedAt = %s, want %s", snap.RefreshedAt, want)
	}
	h.wantRefreshes(2)
}

func TestChangedInsideGapDefersRefresh(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(time.Minute)
	h := start(t, g, Options{})
	h.snapshot()

	g.set(func(f *fakeGH) { f.notif = changed(time.Hour) })
	h.step(time.Minute)
	h.noUpdates()
	h.wantRefreshes(1)

	// The refresh waits for the end of the gap at t0+5m.
	if h.step(3*time.Minute + 59*time.Second) {
		t.Fatal("timer fired before the end of the gap")
	}
	if !h.step(time.Second) {
		t.Fatal("timer did not fire at the end of the gap")
	}
	snap := h.snapshot()
	if want := t0.Add(5 * time.Minute); !snap.RefreshedAt.Equal(want) {
		t.Errorf("RefreshedAt = %s, want %s", snap.RefreshedAt, want)
	}
	h.wantRefreshes(2)
	_, notifs := g.counts()
	if notifs != 2 {
		t.Errorf("notification checks = %d, want 2", notifs)
	}
}

func TestRefreshNowInsideGapRefreshesAtOnce(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(time.Hour)
	h := start(t, g, Options{})
	h.snapshot()

	h.step(time.Minute)
	h.do(h.p.RefreshNow)
	snap := h.snapshot()
	if want := t0.Add(time.Minute); !snap.RefreshedAt.Equal(want) {
		t.Errorf("RefreshedAt = %s, want %s", snap.RefreshedAt, want)
	}
	h.wantRefreshes(2)
}

func TestIntervalElapsedRefreshes(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(time.Hour)
	h := start(t, g, Options{RefreshInterval: 10 * time.Minute})
	h.snapshot()

	if h.step(10*time.Minute - time.Second) {
		t.Fatal("timer fired before the interval")
	}
	if !h.step(time.Second) {
		t.Fatal("timer did not fire at the interval")
	}
	snap := h.snapshot()
	if want := t0.Add(10 * time.Minute); !snap.RefreshedAt.Equal(want) {
		t.Errorf("RefreshedAt = %s, want %s", snap.RefreshedAt, want)
	}
	if _, notifs := g.counts(); notifs != 1 {
		t.Errorf("notification checks = %d, want 1", notifs)
	}
}

func TestBackoffSteps(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(24 * time.Hour)
	g.reviewErr = errors.New("boom")
	h := start(t, g, Options{RefreshInterval: time.Hour})

	ups := h.drain()
	if len(ups) != 1 || ups[0].Err == nil || !strings.Contains(ups[0].Err.Error(), "boom") {
		t.Fatalf("updates = %+v, want one refresh error", ups)
	}
	for i, d := range []time.Duration{1, 2, 4, 8, 10, 10} {
		d *= time.Minute
		if h.step(d - time.Second) {
			t.Fatalf("retry %d fired before %s", i+1, d)
		}
		if !h.step(time.Second) {
			t.Fatalf("retry %d did not fire after %s", i+1, d)
		}
		if ups := h.drain(); len(ups) != 1 || ups[0].Err == nil {
			t.Fatalf("retry %d updates = %+v, want one error", i+1, ups)
		}
	}
	h.wantRefreshes(7)

	// A success resets the backoff: the next refresh is at the interval.
	g.set(func(f *fakeGH) { f.reviewErr = nil })
	h.step(10 * time.Minute)
	h.snapshot()
	if h.step(time.Hour - time.Second) {
		t.Fatal("timer fired before the interval")
	}
	if !h.step(time.Second) {
		t.Fatal("timer did not fire at the interval")
	}
	h.snapshot()
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 10, 10, 10}
	for i, w := range want {
		if got := backoff(i + 1); got != w*time.Minute {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w*time.Minute)
		}
	}
}

func TestRateLimitPause(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(time.Hour)
	resetAt := t0.Add(30 * time.Minute)
	g.rl = gh.RateLimit{Remaining: RateLimitFloor - 1, ResetAt: resetAt}
	h := start(t, g, Options{RefreshInterval: 10 * time.Minute})

	snap := h.snapshot()
	if !snap.PausedUntil.Equal(resetAt) {
		t.Errorf("PausedUntil = %s, want %s", snap.PausedUntil, resetAt)
	}
	g.set(func(f *fakeGH) { f.rl = gh.RateLimit{Remaining: 5000, ResetAt: t0.Add(2 * time.Hour)} })

	// The interval elapses at t0+10m, but the pause holds the refresh.
	if h.step(10 * time.Minute) {
		t.Fatal("timer fired inside the pause")
	}
	// A user request also waits for the end of the pause.
	h.do(h.p.RefreshNow)
	h.noUpdates()
	h.wantRefreshes(1)

	if h.step(20*time.Minute - time.Second) {
		t.Fatal("timer fired inside the pause")
	}
	if !h.step(time.Second) {
		t.Fatal("timer did not fire at the end of the pause")
	}
	snap = h.snapshot()
	if !snap.RefreshedAt.Equal(resetAt) || !snap.PausedUntil.IsZero() {
		t.Errorf("snapshot = %+v, want a refresh at %s with no pause", snap, resetAt)
	}
}

func TestKeptPageCount(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(time.Hour)
	org := Scope{Kind: ScopeOrg, Org: "acme"}
	q := "is:pr is:open org:acme sort:updated-desc"
	g.totals[q] = 50
	h := start(t, g, Options{Orgs: []string{"acme"}})

	section := func(s *Snapshot) Section {
		for _, sec := range s.Sections {
			if sec.Scope == org {
				return sec
			}
		}
		t.Fatalf("no section %v", org)
		return Section{}
	}
	if sec := section(h.snapshot()); len(sec.PRs) != 20 || sec.Total != 50 {
		t.Fatalf("start: %d PRs of %d, want 20 of 50", len(sec.PRs), sec.Total)
	}
	g.takeSearches()

	h.do(func() { h.p.LoadMore(org) })
	if sec := section(h.snapshot()); len(sec.PRs) != 40 || sec.PRs[39].ID != q+"#39" {
		t.Fatalf("load more: %d PRs, want 40", len(sec.PRs))
	}
	if got := g.takeSearches(); fmt.Sprint(got) != fmt.Sprint([]searchCall{{q, "c20"}}) {
		t.Errorf("load more searches = %v", got)
	}
	h.wantRefreshes(1)

	// A refresh reads the two loaded pages from the start.
	h.do(h.p.RefreshNow)
	if sec := section(h.snapshot()); len(sec.PRs) != 40 {
		t.Fatalf("refresh: %d PRs, want 40", len(sec.PRs))
	}
	wantSearches := []searchCall{{q, ""}, {q, "c20"}, {"is:pr is:open user:me sort:updated-desc", ""}}
	if got := g.takeSearches(); fmt.Sprint(got) != fmt.Sprint(wantSearches) {
		t.Errorf("refresh searches = %v, want %v", got, wantSearches)
	}

	h.do(func() { h.p.LoadMore(org) })
	if sec := section(h.snapshot()); len(sec.PRs) != 50 {
		t.Fatalf("second load more: %d PRs, want 50", len(sec.PRs))
	}
	g.takeSearches()

	// No page is left, and the other scopes load all pages: no calls.
	h.do(func() {
		h.p.LoadMore(org)
		h.p.LoadMore(Scope{Kind: ScopeMine})
	})
	h.noUpdates()
	if got := g.takeSearches(); len(got) != 0 {
		t.Errorf("searches = %v, want none", got)
	}
}

func TestLoadMoreSkipsGapAndKeepsBackoff(t *testing.T) {
	g := newFakeGH()
	g.notif = notModified(time.Hour)
	q := "is:pr is:open user:me sort:updated-desc"
	g.totals[q] = 30
	h := start(t, g, Options{})
	h.snapshot()

	g.set(func(f *fakeGH) { f.searchErr = errors.New("boom") })
	h.do(func() { h.p.LoadMore(Scope{Kind: ScopePersonal}) })
	ups := h.drain()
	if len(ups) != 1 || ups[0].Err == nil || !strings.Contains(ups[0].Err.Error(), "load more personal") {
		t.Fatalf("updates = %+v, want one load more error", ups)
	}

	g.set(func(f *fakeGH) { f.searchErr = nil })
	h.do(func() { h.p.LoadMore(Scope{Kind: ScopePersonal}) })
	snap := h.snapshot()
	if got := len(snap.Sections[len(snap.Sections)-1].PRs); got != 30 {
		t.Errorf("personal PRs = %d, want 30", got)
	}
	// The failed load more starts no backoff: the next refresh is at the
	// interval.
	h.wantRefreshes(1)
	if !h.step(10 * time.Minute) {
		t.Fatal("timer did not fire at the interval")
	}
	h.snapshot()
}

func TestNotificationsErrorSendsError(t *testing.T) {
	g := newFakeGH()
	g.notifErr = errors.New("offline")
	h := start(t, g, Options{RefreshInterval: time.Hour})

	ups := h.drain()
	if len(ups) != 2 || ups[0].Err == nil || ups[1].Snapshot == nil {
		t.Fatalf("updates = %+v, want an error and a snapshot", ups)
	}
	// The next check is after the default poll interval.
	if !h.step(gh.DefaultPollInterval) {
		t.Fatal("timer did not fire after the default poll interval")
	}
	if _, notifs := g.counts(); notifs != 2 {
		t.Errorf("notification checks = %d, want 2", notifs)
	}
}
