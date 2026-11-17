package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/model"
)

// gqlRequest is one GraphQL request that the fake transport received.
type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// reply is the response of the fake transport: an HTTP status and a body.
type reply struct {
	status int
	body   string
}

// fixture returns a 200 reply with the body of testdata/name.
func fixture(t *testing.T, name string) reply {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return reply{status: http.StatusOK, body: string(data)}
}

// fakeTransport serves recorded JSON and records each GraphQL request.
type fakeTransport struct {
	t       *testing.T
	handle  func(req gqlRequest) reply
	mu      sync.Mutex
	queries []gqlRequest
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var req gqlRequest
	if r.Body != nil {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &req); err != nil {
			f.t.Errorf("decode request body: %v", err)
		}
	}
	f.mu.Lock()
	f.queries = append(f.queries, req)
	f.mu.Unlock()

	rep := f.handle(req)
	return &http.Response{
		StatusCode: rep.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(rep.body)),
		Request:    r,
	}, nil
}

func (f *fakeTransport) requests() []gqlRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gqlRequest(nil), f.queries...)
}

func newTestClient(t *testing.T, handle func(req gqlRequest) reply) (*Client, *fakeTransport) {
	t.Helper()
	ft := &fakeTransport{t: t, handle: handle}
	c, err := New(api.ClientOptions{Host: "github.com", AuthToken: "test-token", Transport: ft})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, ft
}

func TestSearchOnePage(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "search_page2.json") })

	res, err := c.Search(context.Background(), "is:pr is:open user:dave", 20, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	reqs := ft.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	vars := reqs[0].Variables
	if vars["q"] != "is:pr is:open user:dave" || vars["first"] != float64(20) {
		t.Errorf("variables = %v", vars)
	}
	if _, ok := vars["after"]; ok {
		t.Errorf("first page sends after = %v", vars["after"])
	}
	if !strings.Contains(reqs[0].Query, "rateLimit { cost remaining resetAt }") {
		t.Errorf("query does not request the rate limit")
	}

	if res.IssueCount != 3 || res.HasNextPage || res.EndCursor != "Y3Vyc29yOjM=" {
		t.Errorf("page info = %d %v %q", res.IssueCount, res.HasNextPage, res.EndCursor)
	}
	wantRL := RateLimit{Cost: 11, Remaining: 4889, ResetAt: time.Date(2026, 11, 16, 22, 0, 0, 0, time.UTC)}
	if !res.RateLimit.ResetAt.Equal(wantRL.ResetAt) || res.RateLimit.Cost != wantRL.Cost || res.RateLimit.Remaining != wantRL.Remaining {
		t.Errorf("rate limit = %+v, want %+v", res.RateLimit, wantRL)
	}
	if len(res.PRs) != 1 {
		t.Fatalf("PRs = %d, want 1", len(res.PRs))
	}
	pr := res.PRs[0]
	if pr.Author != (model.Actor{}) {
		t.Errorf("null author = %+v, want empty", pr.Author)
	}
	if pr.Mergeable != model.MergeableConflicting || pr.ReviewDecision != model.DecisionApproved || pr.Rollup != model.CheckSuccess {
		t.Errorf("state = %s %s %s", pr.Mergeable, pr.ReviewDecision, pr.Rollup)
	}
}

func TestSearchSummaryFields(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "search_page1.json") })

	res, err := c.Search(context.Background(), "is:pr is:open org:acme", 2, "abc")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := ft.requests()[0].Variables["after"]; got != "abc" {
		t.Errorf("after = %v, want abc", got)
	}
	if len(res.PRs) != 2 {
		t.Fatalf("PRs = %d, want 2", len(res.PRs))
	}

	one := res.PRs[0]
	if one.ID != "PR_one" || one.Number != 101 || one.Title != "Add retry to the uploader" ||
		one.URL != "https://github.com/acme/api/pull/101" || one.HeadSHA != strings.Repeat("a", 40) || one.BaseRef != "main" {
		t.Errorf("identity fields = %+v", one)
	}
	if one.Repo != (model.Repo{Owner: "acme", Name: "api"}) {
		t.Errorf("repo = %+v", one.Repo)
	}
	if one.Author != (model.Actor{Login: "alice", Typename: model.TypenameUser}) {
		t.Errorf("author = %+v", one.Author)
	}
	if one.Rollup != model.CheckFailure || one.ReviewDecision != model.DecisionChangesRequested || one.Mergeable != model.MergeableYes || one.IsDraft {
		t.Errorf("state = %s %s %s %v", one.Rollup, one.ReviewDecision, one.Mergeable, one.IsDraft)
	}
	if !one.CreatedAt.Equal(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)) || !one.UpdatedAt.Equal(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("times = %v %v", one.CreatedAt, one.UpdatedAt)
	}
	if one.CommentCount != 4 || one.ReviewCount != 2 || one.ThreadCount != 2 {
		t.Errorf("counts = %d %d %d", one.CommentCount, one.ReviewCount, one.ThreadCount)
	}
	// The review submit time (11:00) is newer than the thread comment (10:30)
	// and the PR comment (one day earlier).
	if one.NewestAuthor.Login != "carol" {
		t.Errorf("newest author = %+v, want carol", one.NewestAuthor)
	}
	if len(one.Threads) != 2 {
		t.Fatalf("threads = %d, want 2", len(one.Threads))
	}
	th := one.Threads[0]
	if th.ID != "PRRT_one" || th.Path != "upload.go" || th.Line != 42 || th.IsResolved {
		t.Errorf("thread = %+v", th)
	}
	if last := th.LastComment(); last == nil || last.Author.Login != "carol" || last.ID != "PRRC_one" {
		t.Errorf("last comment = %+v", last)
	}
	if file := one.Threads[1]; file.Line != 0 || !file.IsResolved || file.LastComment().Author != (model.Actor{}) {
		t.Errorf("file thread = %+v", file)
	}
	if got := model.DeriveStatus(one, "alice"); got != model.StatusCIFailed {
		t.Errorf("status = %v, want CI failed", got)
	}

	two := res.PRs[1]
	if two.Rollup != model.CheckNone || two.ReviewDecision != model.DecisionNone || !two.IsDraft {
		t.Errorf("empty rollup and decision = %q %q %v", two.Rollup, two.ReviewDecision, two.IsDraft)
	}
	if !model.IsBot(two.Author, nil) {
		t.Errorf("bot author = %+v", two.Author)
	}
	if two.NewestAuthor != (model.Actor{}) || len(two.Threads) != 0 {
		t.Errorf("no activity = %+v %v", two.NewestAuthor, two.Threads)
	}
}

func TestSearchAllPages(t *testing.T) {
	c, ft := newTestClient(t, func(req gqlRequest) reply {
		switch req.Variables["after"] {
		case nil:
			return fixture(t, "search_page1.json")
		case "Y3Vyc29yOjI=":
			return fixture(t, "search_page2.json")
		default:
			t.Errorf("unexpected after = %v", req.Variables["after"])
			return reply{status: http.StatusInternalServerError, body: "{}"}
		}
	})

	res, err := c.SearchAll(context.Background(), "is:pr is:open author:@me")
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	reqs := ft.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.Variables["first"] != float64(PageSize) {
			t.Errorf("first = %v, want %d", r.Variables["first"], PageSize)
		}
	}
	var ids []string
	for _, pr := range res.PRs {
		ids = append(ids, pr.ID)
	}
	if strings.Join(ids, ",") != "PR_one,PR_two,PR_three" {
		t.Errorf("ids = %v", ids)
	}
	if res.IssueCount != 3 || res.RateLimit.Remaining != 4889 || res.EndCursor != "Y3Vyc29yOjM=" {
		t.Errorf("result = count %d, remaining %d, cursor %q", res.IssueCount, res.RateLimit.Remaining, res.EndCursor)
	}
}

func TestSearchAllEmpty(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "search_empty.json") })

	res, err := c.SearchAll(context.Background(), "is:pr is:open author:@me")
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(res.PRs) != 0 || res.IssueCount != 0 || len(ft.requests()) != 1 {
		t.Errorf("result = %+v, requests = %d", res, len(ft.requests()))
	}
}

func TestSearchGraphQLError(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, "search_graphql_error.json") })

	_, err := c.Search(context.Background(), "is:pr bad:", 20, "")
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("err = %v, want GraphQLError", err)
	}
	if !strings.Contains(err.Error(), "Invalid search query.") {
		t.Errorf("err = %v", err)
	}

	_, err = c.SearchAll(context.Background(), "is:pr bad:")
	if !errors.As(err, &gqlErr) {
		t.Errorf("SearchAll err = %v, want GraphQLError", err)
	}
}

func TestSearchHTTPError(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply {
		return reply{status: http.StatusBadGateway, body: `{"message": "Bad gateway"}`}
	})

	_, err := c.Search(context.Background(), "is:pr", 20, "")
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", httpErr.StatusCode)
	}
}

func TestNeedsMyReviewMerge(t *testing.T) {
	c, ft := newTestClient(t, func(req gqlRequest) reply {
		switch {
		case req.Variables["q"] == QueryReviewRequested && req.Variables["after"] == nil:
			return fixture(t, "search_page1.json")
		case req.Variables["q"] == QueryReviewRequested:
			return fixture(t, "search_page2.json")
		case req.Variables["q"] == QueryAssigned:
			return fixture(t, "search_assigned.json")
		default:
			t.Errorf("unexpected request %v", req.Variables)
			return reply{status: http.StatusInternalServerError, body: "{}"}
		}
	})

	res, err := c.NeedsMyReview(context.Background())
	if err != nil {
		t.Fatalf("NeedsMyReview: %v", err)
	}
	var ids []string
	for _, pr := range res.PRs {
		ids = append(ids, pr.ID)
	}
	// PR_three is in both searches and appears once.
	if strings.Join(ids, ",") != "PR_one,PR_two,PR_three,PR_four" {
		t.Errorf("ids = %v", ids)
	}
	if res.IssueCount != 4 {
		t.Errorf("issue count = %d, want 4", res.IssueCount)
	}
	if len(ft.requests()) != 3 {
		t.Errorf("requests = %d, want 3", len(ft.requests()))
	}
}

func TestNeedsMyReviewError(t *testing.T) {
	c, _ := newTestClient(t, func(req gqlRequest) reply {
		if req.Variables["q"] == QueryAssigned {
			return reply{status: http.StatusUnauthorized, body: `{"message": "Bad credentials"}`}
		}
		return fixture(t, "search_page2.json")
	})

	if _, err := c.NeedsMyReview(context.Background()); err == nil {
		t.Fatal("err = nil, want the error of the assignee search")
	}
}

func TestPRStates(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "prstates.json") })
	refs := []PRRef{
		{Owner: "acme", Repo: "api", Number: 1},
		{Owner: "acme", Repo: "api", Number: 2},
		{Owner: "acme", Repo: "api", Number: 9999},
		{Owner: "gone", Repo: "repo", Number: 5},
		{Owner: "dave", Repo: "tools", Number: 3},
	}

	states, err := c.PRStates(context.Background(), refs)
	if err != nil {
		t.Fatalf("PRStates: %v", err)
	}
	want := map[PRRef]PRState{
		refs[0]: PRStateMerged,
		refs[1]: PRStateOpen,
		refs[4]: PRStateClosed,
	}
	if len(states) != len(want) {
		t.Errorf("states = %v, want %v", states, want)
	}
	for ref, s := range want {
		if states[ref] != s {
			t.Errorf("state of %+v = %q, want %q", ref, states[ref], s)
		}
	}

	reqs := ft.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	// The owner and repo names go in variables, not in the query text.
	if strings.Contains(reqs[0].Query, "acme") {
		t.Errorf("query holds a literal name: %s", reqs[0].Query)
	}
	vars := reqs[0].Variables
	if vars["o3"] != "gone" || vars["r3"] != "repo" || vars["n2"] != float64(9999) {
		t.Errorf("variables = %v", vars)
	}
	if !strings.Contains(reqs[0].Query, "pr4: repository(owner: $o4, name: $r4) { pullRequest(number: $n4) { state } }") {
		t.Errorf("query = %s", reqs[0].Query)
	}
}

func TestPRStatesEmpty(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply {
		t.Error("unexpected request")
		return reply{status: http.StatusInternalServerError, body: "{}"}
	})

	states, err := c.PRStates(context.Background(), nil)
	if err != nil || len(states) != 0 || len(ft.requests()) != 0 {
		t.Errorf("states = %v, err = %v, requests = %d", states, err, len(ft.requests()))
	}
}

func TestPRStatesOtherError(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, "prstates_error.json") })

	_, err := c.PRStates(context.Background(), []PRRef{{Owner: "acme", Repo: "secret", Number: 1}})
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Errorf("err = %v, want GraphQLError", err)
	}
}

func TestPRStatesHTTPError(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply {
		return reply{status: http.StatusServiceUnavailable, body: `{"message": "Unavailable"}`}
	})

	_, err := c.PRStates(context.Background(), []PRRef{{Owner: "acme", Repo: "api", Number: 1}})
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("err = %v, want HTTPError", err)
	}
}

func TestSearchPRs(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "search_number.json") })

	q := "is:pr 6505 org:globex user:octocat"
	prs, err := c.SearchPRs(context.Background(), q, 10)
	if err != nil {
		t.Fatalf("SearchPRs: %v", err)
	}
	reqs := ft.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if vars := reqs[0].Variables; vars["q"] != q || vars["first"] != float64(10) {
		t.Errorf("variables = %v", vars)
	}
	if !strings.Contains(reqs[0].Query, "state\n") {
		t.Errorf("query does not request the PR state")
	}
	// The search also finds #5105 for the number in its title.
	if len(prs) != 2 || prs[0].Number != 6505 || prs[1].Number != 5105 {
		t.Fatalf("PRs = %+v", prs)
	}
	if prs[0].State != "MERGED" || prs[1].State != "OPEN" {
		t.Errorf("states = %q %q", prs[0].State, prs[1].State)
	}
	if prs[0].Repo != (model.Repo{Owner: "globex", Name: "Backend"}) || prs[0].Rollup != model.CheckSuccess {
		t.Errorf("summary = %+v", prs[0])
	}
}

func TestSearchPRsError(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, "search_graphql_error.json") })
	if _, err := c.SearchPRs(context.Background(), "is:pr 1", 10); err == nil {
		t.Fatal("SearchPRs: no error")
	}
}
