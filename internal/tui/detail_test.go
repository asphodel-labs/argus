package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

// fakeDetailer returns the PR or the error for each PR id and counts the
// calls.
type fakeDetailer struct {
	mu    sync.Mutex
	prs   map[string]model.PR
	errs  map[string]error
	calls []string
}

func (f *fakeDetailer) PRDetail(_ context.Context, owner, repo string, n int) (model.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, pr := range f.prs {
		if pr.Repo.Owner == owner && pr.Repo.Name == repo && pr.Number == n {
			f.calls = append(f.calls, id)
			if err := f.errs[id]; err != nil {
				return model.PR{}, err
			}
			return pr, nil
		}
	}
	return model.PR{}, gh.ErrNotFound
}

func (f *fakeDetailer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDetailer) setErr(id string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = map[string]error{}
	}
	f.errs[id] = err
}

// fixtureTransport answers each request with one recorded response.
type fixtureTransport struct{ body []byte }

func (t fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(t.body)),
		Request:    r,
	}, nil
}

// recordedClient returns a gh client that answers each query with the
// recorded file from the gh testdata.
func recordedClient(t *testing.T, file string) *gh.Client {
	t.Helper()
	body, err := os.ReadFile("../gh/testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	c, err := gh.New(api.ClientOptions{Host: "github.com", AuthToken: "test-token", Transport: fixtureTransport{body: body}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// clock is a time source that the tests move.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// detailSnapshot has one section with the PRs.
func detailSnapshot(prs ...model.PR) *poller.Snapshot {
	return &poller.Snapshot{RefreshedAt: listNow, Sections: []poller.Section{
		{Scope: scopeReview, Total: len(prs), PRs: prs},
	}}
}

// newDetailModel returns a model with the snapshot and the cursor on the
// section row. It has no poller, so the commands run without a channel.
func newDetailModel(t *testing.T, det Detailer, now func() time.Time, snap *poller.Snapshot) *Model {
	t.Helper()
	m := New(Options{Detail: det, Styles: testStyles(t), Me: "me", Now: now})
	run(m, m.update(tea.WindowSizeMsg{Width: 160, Height: 80}))
	m.list.setSnapshot(snap)
	run(m, m.syncDetail())
	return m
}

// run runs the command and gives each message to the model, until no
// command is left. It skips the timer of a status message.
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	case clearMessageMsg:
	default:
		_, next := m.Update(msg)
		run(m, next)
	}
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		_, cmd := m.Update(key(k))
		run(m, cmd)
	}
}

func detailText(m *Model) string { return ansi.Strip(m.detail.view()) }

// fullPR is the summary of the recorded PR in detail_full.json.
func fullPR() model.PR {
	p := testPR("PR_full", "acme/api", 42, "Add retry to the uploader", "alice")
	p.UpdatedAt = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	return p
}

func TestDetailRenderRecordedPR(t *testing.T) {
	m := newDetailModel(t, recordedClient(t, "detail_full.json"), fixedNow, detailSnapshot(fullPR()))
	press(m, "j", "j", "enter")
	got := detailText(m)
	for _, want := range []string{
		"Add retry to the uploader",
		"✗ CI failed · acme/api#42 · alice · into main",
		"Reviews: ✎ bob",
		"Reviewers: carol, acme/core, copilot · Assignees: alice, dave",
		"Merge: ✓ mergeable · blocked",
		"Checks (2)", "✗ test · github-actions", "⏳ lint · github-actions",
		"Description", "Retry the upload three times.", "No API change.",
		"Threads (2)",
		"▾ upload/retry.go:17 2 comments", "bob · 4d ago", "Why three?", "👍 2", "It matches the server limit.",
		"▸ upload/old.go resolved · 1 comment",
		"Conversation (2)",
		"renovate-bot commented · 6d ago", "Bot comment.", "🚀 1",
		"bob reviewed: changes requested · 4d ago", "Please add a test.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail has no %q:\n%s", want, got)
		}
	}
	// The resolved thread starts folded. The pending review of me is not
	// in the reviews line.
	for _, absent := range []string{"Outdated note.", "me ("} {
		if strings.Contains(got, absent) {
			t.Errorf("detail has %q:\n%s", absent, got)
		}
	}
	// The conversation is in time order: the bot comment is older.
	if strings.Index(got, "Bot comment.") > strings.Index(got, "Please add a test.") {
		t.Errorf("conversation is not in time order:\n%s", got)
	}
}

func TestPeopleLine(t *testing.T) {
	alice := model.Actor{ID: "U_a", Login: "alice"}
	team := model.Actor{ID: "T_1", Login: "acme/core", Typename: model.TypenameTeam}
	for _, c := range []struct {
		name      string
		reviewers []model.Actor
		assignees []model.Actor
		want      string
	}{
		{"both", []model.Actor{alice, team}, []model.Actor{alice}, "Reviewers: alice, acme/core · Assignees: alice"},
		{"reviewers only", []model.Actor{team}, nil, "Reviewers: acme/core"},
		{"assignees only", nil, []model.Actor{alice}, "Assignees: alice"},
		{"none", nil, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := peopleLine(model.PR{RequestedReviewers: c.reviewers, Assignees: c.assignees}); got != c.want {
				t.Errorf("peopleLine = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDetailNoPeopleLine(t *testing.T) {
	m := newDetailModel(t, recordedClient(t, "detail_no_checks.json"), fixedNow, detailSnapshot(fullPR()))
	press(m, "j", "j", "enter")
	got := detailText(m)
	if strings.Contains(got, "Reviewers:") || strings.Contains(got, "Assignees:") {
		t.Errorf("detail has a people line:\n%s", got)
	}
}

func TestDetailColors(t *testing.T) {
	st := testStyles(t)
	m := newDetailModel(t, recordedClient(t, "detail_full.json"), fixedNow, detailSnapshot(fullPR()))
	press(m, "j", "j", "enter")
	got := m.detail.view()
	for _, want := range []string{
		st.Status[model.StatusCIFailed].Render("✗"),
		st.Status[model.StatusPendingReview].Render("⏳"),
		st.Status[model.StatusChangesRequested].Render("✎ bob"),
		st.Status[model.StatusApproved].Render("✓ mergeable"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail has no styled %q", ansi.Strip(want))
		}
	}
}

func TestCheckIcon(t *testing.T) {
	st := testStyles(t)
	cases := []struct {
		status, conclusion string
		icon               string
		style              string
	}{
		{"COMPLETED", "SUCCESS", "✓", st.Status[model.StatusApproved].Render("x")},
		{"COMPLETED", "FAILURE", "✗", st.Status[model.StatusCIFailed].Render("x")},
		{"COMPLETED", "ERROR", "✗", st.Status[model.StatusCIFailed].Render("x")},
		{"COMPLETED", "TIMED_OUT", "✗", st.Status[model.StatusCIFailed].Render("x")},
		{"COMPLETED", "SKIPPED", "○", st.Muted.Render("x")},
		{"COMPLETED", "NEUTRAL", "○", st.Muted.Render("x")},
		{"IN_PROGRESS", "", "⏳", st.Status[model.StatusPendingReview].Render("x")},
		{"PENDING", "", "⏳", st.Status[model.StatusPendingReview].Render("x")},
	}
	for _, c := range cases {
		icon, s := checkIcon(model.Check{Status: c.status, Conclusion: c.conclusion}, st)
		if icon != c.icon || s.Render("x") != c.style {
			t.Errorf("%s/%s: icon %q, want %q; or the style is wrong", c.status, c.conclusion, icon, c.icon)
		}
	}
}

func TestDetailReviewsLine(t *testing.T) {
	rv := func(login, state string) model.Review {
		return model.Review{Author: model.Actor{Login: login}, State: state}
	}
	cases := []struct {
		name    string
		reviews []model.Review
		want    string
	}{
		{"none", nil, "none"},
		{"comment after approval keeps approval", []model.Review{rv("bob", "APPROVED"), rv("bob", "COMMENTED")}, "✓ bob"},
		{"new state replaces old", []model.Review{rv("bob", "CHANGES_REQUESTED"), rv("bob", "APPROVED")}, "✓ bob"},
		{"comment only", []model.Review{rv("carol", "COMMENTED")}, "carol (commented)"},
		{"dismissed", []model.Review{rv("bob", "APPROVED"), rv("bob", "DISMISSED")}, "bob (dismissed)"},
		{"pending is hidden", []model.Review{rv("me", "PENDING")}, "none"},
		{"order of first review", []model.Review{rv("bob", "COMMENTED"), rv("carol", "APPROVED"), rv("bob", "CHANGES_REQUESTED")}, "✎ bob, ✓ carol"},
	}
	d := newDetailPane("me", fixedNow, testStyles(t))
	b := &detailBuilder{d: &d, width: 80}
	for _, c := range cases {
		if got := ansi.Strip(b.reviewsLine(model.PR{Reviews: c.reviews})); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDetailNoSelection(t *testing.T) {
	m := newDetailModel(t, &fakeDetailer{}, fixedNow, detailSnapshot(fullPR()))
	press(m, "j", "j")
	if got := detailText(m); got != "No PR open. Press enter on a PR in the list." {
		t.Fatalf("detail = %q, want no selection", got)
	}
}

func TestDetailLoadingState(t *testing.T) {
	m := newDetailModel(t, &fakeDetailer{}, fixedNow, detailSnapshot(fullPR()))
	// Do not run the command, so the call does not return.
	press(m, "j", "j")
	m.Update(key("enter"))
	if got := detailText(m); got != "Loading acme/api#42…" {
		t.Fatalf("detail = %q, want the loading state", got)
	}
}

func TestDetailCacheHit(t *testing.T) {
	a := testPR("A", "acme/api", 1, "First", "alice")
	b := testPR("B", "acme/api", 2, "Second", "bob")
	det := &fakeDetailer{prs: map[string]model.PR{"A": a, "B": b}}
	clk := &clock{now: listNow}
	m := newDetailModel(t, det, clk.Now, detailSnapshot(a, b))

	// Rows: section, group, A, B.
	press(m, "j", "j", "space", "j", "space")
	if n := det.callCount(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
	// Back to A inside the TTL: the cache serves it.
	clk.now = clk.now.Add(20 * time.Second)
	press(m, "k", "space")
	if n := det.callCount(); n != 2 {
		t.Fatalf("calls after a cache hit = %d, want 2", n)
	}
	if got := detailText(m); !strings.Contains(got, "First") {
		t.Fatalf("detail does not show A:\n%s", got)
	}
	// Keys that keep the opened PR send no call.
	press(m, "l", "j", "k", "h")
	if n := det.callCount(); n != 2 {
		t.Fatalf("calls after keys that keep the opened PR = %d, want 2", n)
	}
	// B was loaded 20 s ago; 11 s more makes it stale.
	clk.now = clk.now.Add(11 * time.Second)
	press(m, "j", "enter")
	if got := det.calls; len(got) != 3 || got[2] != "B" {
		t.Fatalf("calls = %v, want a new call for B", got)
	}
}

func TestEnterFocusesDetailSpaceKeepsList(t *testing.T) {
	a := testPR("A", "acme/api", 1, "First", "alice")
	b := testPR("B", "acme/api", 2, "Second", "bob")
	det := &fakeDetailer{prs: map[string]model.PR{"A": a, "B": b}}
	m := newDetailModel(t, det, (&clock{now: listNow}).Now, detailSnapshot(a, b))

	// Rows: section, group, A, B.
	press(m, "j", "j", "space")
	if pr, ok := m.Opened(); !ok || pr.ID != "A" || m.Focus() != FocusList {
		t.Fatalf("after space: opened %q %v, focus %v; want A, list", pr.ID, ok, m.Focus())
	}
	press(m, "j", "enter")
	if pr, ok := m.Opened(); !ok || pr.ID != "B" || m.Focus() != FocusDetail {
		t.Fatalf("after enter: opened %q %v, focus %v; want B, detail", pr.ID, ok, m.Focus())
	}
}

func TestDetailCursorMoveSendsNoQuery(t *testing.T) {
	a := testPR("A", "acme/api", 1, "First", "alice")
	b := testPR("B", "acme/api", 2, "Second", "bob")
	det := &fakeDetailer{prs: map[string]model.PR{"A": a, "B": b}}
	m := newDetailModel(t, det, fixedNow, detailSnapshot(a, b))

	press(m, "j", "j", "j", "k", "G", "g", "g", "ctrl+d", "ctrl+u")
	if n := det.callCount(); n != 0 {
		t.Fatalf("calls after cursor moves = %d, want 0", n)
	}
	// The opened PR stays in the detail pane while the cursor moves.
	press(m, "j", "j", "space", "j")
	if pr, ok := m.Opened(); !ok || pr.ID != "A" || selectedID(t, m) != "B" {
		t.Fatalf("opened %q %v, cursor on %s; want A open and the cursor on B", pr.ID, ok, selectedID(t, m))
	}
	if got := detailText(m); !strings.Contains(got, "First") || det.callCount() != 1 {
		t.Fatalf("calls %d, detail:\n%s", det.callCount(), got)
	}
	if m.Focus() != FocusList {
		t.Fatal("enter moved the focus")
	}
	// The list marks the opened row.
	if got := rowText(m); !strings.HasPrefix(got[2], "● ") || strings.HasPrefix(got[3], "● ") {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}
}

func TestDetailNewerSummaryReloads(t *testing.T) {
	a := testPR("A", "acme/api", 1, "First", "alice")
	det := &fakeDetailer{prs: map[string]model.PR{"A": a}}
	m := newDetailModel(t, det, fixedNow, detailSnapshot(a))
	press(m, "j", "j", "enter")

	// A refresh with the same summary keeps the cache.
	m.list.setSnapshot(detailSnapshot(a))
	run(m, m.syncDetail())
	if n := det.callCount(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	// A newer summary loads the detail again inside the TTL.
	newer := a
	newer.UpdatedAt = listNow.Add(time.Minute)
	newer.Title = "First, changed"
	det.prs["A"] = newer
	m.list.setSnapshot(detailSnapshot(newer))
	run(m, m.syncDetail())
	if n := det.callCount(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
	if got := detailText(m); !strings.Contains(got, "First, changed") {
		t.Fatalf("detail does not show the new title:\n%s", got)
	}
}

func TestDetailErrorState(t *testing.T) {
	a := testPR("A", "acme/api", 1, "First", "alice")
	b := testPR("B", "acme/api", 2, "Second", "bob")
	det := &fakeDetailer{prs: map[string]model.PR{"A": a, "B": b}}
	det.setErr("A", errors.New("PR detail acme/api#1: HTTP 502\nmore text"))
	m := newDetailModel(t, det, fixedNow, detailSnapshot(a, b))

	press(m, "j", "j", "enter")
	got := detailText(m)
	for _, want := range []string{"Cannot load acme/api#1", "HTTP 502", "Press enter on the PR to retry."} {
		if !strings.Contains(got, want) {
			t.Errorf("error state has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "more text") {
		t.Errorf("error state shows more than the first line:\n%s", got)
	}
	// Other messages do not repeat the failed call.
	press(m, "l", "j", "h", "j", "k")
	if n := det.callCount(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	// A second open retries; a success clears the error.
	det.setErr("A", nil)
	press(m, "enter")
	if n := det.callCount(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
	if got := detailText(m); !strings.Contains(got, "First") || strings.Contains(got, "Cannot load") {
		t.Fatalf("detail after the retry:\n%s", got)
	}
}

// headerLine returns the content line of the item header that contains s.
func headerLine(t *testing.T, m *Model, s string) string {
	t.Helper()
	for _, it := range m.detail.items {
		if l := ansi.Strip(m.detail.lines[it.line]); strings.Contains(l, s) {
			return l
		}
	}
	t.Fatalf("no item header with %q", s)
	return ""
}

func TestDetailThreadFold(t *testing.T) {
	m := newDetailModel(t, recordedClient(t, "detail_full.json"), fixedNow, detailSnapshot(fullPR()))
	press(m, "j", "j", "enter", "l")

	// za without a current item does nothing.
	press(m, "z", "a")
	if !strings.Contains(detailText(m), "Why three?") {
		t.Fatal("za without a current item folded a thread")
	}
	press(m, "]")
	if got := headerLine(t, m, "upload/retry.go"); !strings.HasPrefix(got, "›▾") {
		t.Fatalf("first thread is not current: %q", got)
	}
	press(m, "z", "a")
	if got := detailText(m); strings.Contains(got, "Why three?") {
		t.Fatalf("za did not fold the thread:\n%s", got)
	}
	if got := headerLine(t, m, "upload/retry.go"); !strings.HasPrefix(got, "›▸") {
		t.Fatalf("folded thread header: %q", got)
	}
	press(m, "z", "a")
	if !strings.Contains(detailText(m), "Why three?") {
		t.Fatal("second za did not open the thread")
	}

	// The resolved thread starts folded; za opens it.
	press(m, "]")
	if got := headerLine(t, m, "upload/old.go"); !strings.HasPrefix(got, "›▸") {
		t.Fatalf("resolved thread header: %q", got)
	}
	press(m, "z", "a")
	if !strings.Contains(detailText(m), "Outdated note.") {
		t.Fatal("za did not open the resolved thread")
	}
}

func TestDetailThreadNavigation(t *testing.T) {
	m := newDetailModel(t, recordedClient(t, "detail_full.json"), fixedNow, detailSnapshot(fullPR()))
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 12})
	press(m, "j", "j", "enter", "l")
	d := &m.detail
	d.layout()
	if len(d.items) != 4 {
		t.Fatalf("items = %d, want 2 threads and 2 conversation entries", len(d.items))
	}

	press(m, "]")
	if d.cur != 0 || d.offset != d.items[0].line {
		t.Fatalf("after ]: cur %d offset %d, want 0 and %d", d.cur, d.offset, d.items[0].line)
	}
	if first := strings.Split(detailText(m), "\n")[0]; !strings.Contains(first, "upload/retry.go") {
		t.Fatalf("top line = %q, want the first thread", first)
	}
	press(m, "]")
	if want := min(d.items[1].line, len(d.lines)-d.height); d.cur != 1 || d.offset != want {
		t.Fatalf("after ]]: cur %d offset %d, want 1 and %d", d.cur, d.offset, want)
	}
	press(m, "[")
	if d.cur != 0 {
		t.Fatalf("after [: cur %d, want 0", d.cur)
	}
	press(m, "[")
	if d.cur != 0 {
		t.Fatalf("[ on the first item: cur %d, want 0", d.cur)
	}
	press(m, "]", "]", "]", "]", "]")
	if d.cur != 3 {
		t.Fatalf("] past the end: cur %d, want 3", d.cur)
	}
	// The last item is near the end, so the offset stops at the bottom.
	if want := len(d.lines) - d.height; d.offset != want {
		t.Fatalf("offset = %d, want the bottom %d", d.offset, want)
	}
}

func TestDetailScroll(t *testing.T) {
	m := newDetailModel(t, recordedClient(t, "detail_full.json"), fixedNow, detailSnapshot(fullPR()))
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 12})
	press(m, "j", "j", "enter", "l")
	d := &m.detail
	d.layout()
	bottom := len(d.lines) - d.height
	steps := []struct {
		keys []string
		want int
	}{
		{[]string{"k"}, 0},
		{[]string{"j", "j"}, 2},
		{[]string{"G"}, bottom},
		{[]string{"j"}, bottom},
		{[]string{"g", "g"}, 0},
		{[]string{"ctrl+d"}, d.height / 2},
		{[]string{"ctrl+u", "ctrl+u"}, 0},
		{[]string{"down", "down", "up"}, 1},
		{[]string{"end"}, bottom},
		{[]string{"home"}, 0},
		{[]string{"pgdown"}, min(d.height, bottom)},
		{[]string{"pgup"}, 0},
	}
	for i, s := range steps {
		press(m, s.keys...)
		if d.offset != s.want {
			t.Fatalf("step %d %v: offset %d, want %d", i, s.keys, d.offset, s.want)
		}
	}
	// The detail keys do not move the list cursor.
	if id := selectedID(t, m); id != "PR_full" {
		t.Fatalf("selected = %s, want PR_full", id)
	}
}

func TestDetailNewSelectionResetsView(t *testing.T) {
	a := testPR("A", "acme/api", 1, "First", "alice")
	a.Threads = []model.Thread{{ID: "T1", Path: "a.go", Line: 1}}
	b := testPR("B", "acme/api", 2, "Second", "bob")
	det := &fakeDetailer{prs: map[string]model.PR{"A": a, "B": b}}
	m := newDetailModel(t, det, fixedNow, detailSnapshot(a, b))
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 10})
	press(m, "j", "j", "enter", "l", "]", "j", "h", "j", "enter")
	if m.detail.cur != -1 || m.detail.offset != 0 {
		t.Fatalf("after a new selection: cur %d offset %d, want -1 and 0", m.detail.cur, m.detail.offset)
	}
}

func TestDetailFitsWidth(t *testing.T) {
	pr := fullPR()
	pr.Title = strings.Repeat("long title ", 20)
	pr.Body = strings.Repeat("word ", 100) + "\n\n" + strings.Repeat("x", 300)
	pr.Comments = []model.Comment{{ID: "C1", Author: model.Actor{Login: "bob"}, Body: strings.Repeat("y", 300), CreatedAt: listNow}}
	det := &fakeDetailer{prs: map[string]model.PR{"PR_full": pr}}
	m := newDetailModel(t, det, fixedNow, detailSnapshot(fullPR()))
	press(m, "j", "j", "enter")
	for _, l := range strings.Split(m.detail.view(), "\n") {
		if w := ansi.StringWidth(l); w > m.detail.width {
			t.Fatalf("line width %d > %d: %q", w, m.detail.width, ansi.Strip(l))
		}
	}
	if !strings.Contains(detailText(m), "yyyy") {
		t.Fatal("the comment is not shown")
	}
}

func TestDetailTeatestFold(t *testing.T) {
	p := newFakePoller()
	tm := teatest.NewTestModel(t, New(Options{
		Poller: p, Detail: recordedClient(t, "detail_full.json"), Styles: testStyles(t), Me: "me", Now: fixedNow,
	}), teatest.WithInitialTermSize(160, 60))
	p.ch <- poller.Update{Snapshot: detailSnapshot(fullPR())}
	waitForText(t, tm, "Needs my review")
	tm.Send(key("j"))
	tm.Send(key("j"))
	tm.Send(key("enter"))
	waitForText(t, tm, "Retry the upload three times.", "upload/old.go")
	for _, k := range []string{"l", "]", "]", "z", "a"} {
		tm.Send(key(k))
	}
	waitForText(t, tm, "Outdated note.")
	tm.Send(key("q"))
	m := finalModel(t, tm)
	if m.detail.cur != 1 {
		t.Fatalf("cur = %d, want 1", m.detail.cur)
	}
}
