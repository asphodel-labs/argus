package gh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/asphodel-labs/argus/internal/model"
)

func TestPRDetailFull(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "detail_full.json") })

	pr, err := c.PRDetail(context.Background(), "acme", "api", 42)
	if err != nil {
		t.Fatalf("PRDetail: %v", err)
	}

	reqs := ft.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	vars := reqs[0].Variables
	if vars["owner"] != "acme" || vars["repo"] != "api" || vars["number"] != float64(42) {
		t.Errorf("variables = %v", vars)
	}

	if pr.ID != "PR_full" || pr.Number != 42 || pr.Repo != (model.Repo{Owner: "acme", Name: "api"}) {
		t.Errorf("identity = %q %d %+v", pr.ID, pr.Number, pr.Repo)
	}
	if pr.Title != "Add retry to the uploader" || pr.Author != (model.Actor{Login: "alice", Typename: "User"}) {
		t.Errorf("title, author = %q %+v", pr.Title, pr.Author)
	}
	if pr.Body != "Retry the upload three times.\n\n## Notes\nNo API change." {
		t.Errorf("body = %q", pr.Body)
	}
	if pr.State != "OPEN" {
		t.Errorf("state = %q, want OPEN", pr.State)
	}
	if pr.ReviewDecision != model.DecisionChangesRequested || pr.Mergeable != model.MergeableYes || pr.Rollup != model.CheckFailure {
		t.Errorf("decision, mergeable, rollup = %q %q %q", pr.ReviewDecision, pr.Mergeable, pr.Rollup)
	}
	if pr.MergeStateStatus != "BLOCKED" || !pr.DeleteBranchOnMerge {
		t.Errorf("merge state, delete branch = %q %v", pr.MergeStateStatus, pr.DeleteBranchOnMerge)
	}
	wantMethods := []model.MergeMethod{model.MergeMethodSquash, model.MergeMethodRebase}
	if !reflect.DeepEqual(pr.MergeMethods, wantMethods) {
		t.Errorf("merge methods = %v, want %v", pr.MergeMethods, wantMethods)
	}
	if pr.HeadSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || pr.BaseRef != "main" {
		t.Errorf("head, base = %q %q", pr.HeadSHA, pr.BaseRef)
	}
	if pr.HeadRef != "feature/login" {
		t.Errorf("HeadRef = %q, want feature/login", pr.HeadRef)
	}

	wantChecks := []model.Check{
		{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE", App: "github-actions",
			URL: "https://github.com/acme/api/actions/runs/1001/job/5001", WorkflowRunID: 1001, Rerunnable: true},
		{Name: "lint", Status: "IN_PROGRESS", App: "github-actions",
			URL: "https://github.com/acme/api/actions/runs/1002/job/5002", WorkflowRunID: 1002, Rerunnable: true},
	}
	if !reflect.DeepEqual(pr.Checks, wantChecks) {
		t.Errorf("checks = %+v, want %+v", pr.Checks, wantChecks)
	}

	wantReviews := []model.Review{
		{ID: "PRR_1", Author: model.Actor{Login: "bob", Typename: "User"}, State: "CHANGES_REQUESTED",
			Body: "Please add a test.", SubmittedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)},
		{ID: "PRR_2", Author: model.Actor{Login: "me", Typename: "User"}, State: "PENDING"},
	}
	if pr.ReviewCount != 2 || !reflect.DeepEqual(pr.Reviews, wantReviews) {
		t.Errorf("reviews = %d %+v, want %+v", pr.ReviewCount, pr.Reviews, wantReviews)
	}

	if pr.ThreadCount != 2 || len(pr.Threads) != 2 {
		t.Fatalf("threads = %d %d, want 2 2", pr.ThreadCount, len(pr.Threads))
	}
	th := pr.Threads[0]
	if th.ID != "PRRT_1" || th.Path != "upload/retry.go" || th.Line != 17 || th.IsResolved || len(th.Comments) != 2 {
		t.Errorf("thread 0 = %+v", th)
	}
	wantFirst := model.Comment{
		ID: "PRRC_1", Author: model.Actor{Login: "bob", Typename: "User"}, Body: "Why three?",
		CreatedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		Reactions: []model.Reaction{{Content: "THUMBS_UP", Count: 2, ViewerHasReacted: true}},
	}
	if !reflect.DeepEqual(th.Comments[0], wantFirst) {
		t.Errorf("thread 0 comment 0 = %+v, want %+v", th.Comments[0], wantFirst)
	}
	if last := th.LastComment(); last == nil || last.Author.Login != "alice" || last.Reactions != nil {
		t.Errorf("thread 0 last comment = %+v", last)
	}
	th = pr.Threads[1]
	if th.Line != 0 || !th.IsResolved || len(th.Comments) != 1 || th.Comments[0].Author != (model.Actor{}) {
		t.Errorf("outdated thread = %+v", th)
	}

	wantComments := []model.Comment{{
		ID: "IC_1", Author: model.Actor{Login: "renovate-bot", Typename: "Bot"}, Body: "Bot comment.",
		CreatedAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
		Reactions: []model.Reaction{{Content: "ROCKET", Count: 1}},
	}}
	if pr.CommentCount != 1 || !reflect.DeepEqual(pr.Comments, wantComments) {
		t.Errorf("comments = %d %+v, want %+v", pr.CommentCount, pr.Comments, wantComments)
	}

	wantReviewers := []model.Actor{
		{ID: "U_carol", Login: "carol", Name: "Carol King", Typename: "User"},
		{ID: "T_core", Login: "acme/core", Name: "Core", Typename: "Team"},
		{ID: "BOT_copilot", Login: "copilot", Typename: "Bot"},
	}
	if !reflect.DeepEqual(pr.RequestedReviewers, wantReviewers) {
		t.Errorf("requested reviewers = %+v, want %+v", pr.RequestedReviewers, wantReviewers)
	}
	wantAssignees := []model.Actor{
		{ID: "U_alice", Login: "alice", Name: "Alice Smith", Typename: "User"},
		{ID: "U_dave", Login: "dave", Typename: "User"},
	}
	if !reflect.DeepEqual(pr.Assignees, wantAssignees) {
		t.Errorf("assignees = %+v, want %+v", pr.Assignees, wantAssignees)
	}
	if q := ft.requests()[0].Query; !strings.Contains(q, "reviewRequests(first: 50)") || !strings.Contains(q, "assignees(first: 50)") {
		t.Errorf("query has no reviewRequests or assignees: %q", q)
	}

	// The entries come in position order. The entry with no visible PR is
	// left out.
	wantStack := model.Stack{ID: "PRS_40", Number: 40, Size: 3, Position: 2, Entries: []model.StackEntry{
		{Position: 1, Number: 41, Title: "Add the backoff helper", State: "MERGED"},
		{Position: 2, Number: 42, Title: "Add retry to the uploader", State: "OPEN"},
		{Position: 3, Number: 43, Title: "Use the retry in the CLI", State: "OPEN"},
	}}
	if !reflect.DeepEqual(pr.Stack, wantStack) {
		t.Errorf("stack = %+v, want %+v", pr.Stack, wantStack)
	}
	if q := ft.requests()[0].Query; !strings.Contains(q, "entries(first: 50)") {
		t.Errorf("query has no stack entries: %q", q)
	}

	if got := model.DeriveStatus(pr, "me"); got != model.StatusCIFailed {
		t.Errorf("DeriveStatus = %v, want CI failed", got)
	}
}

func TestPRDetailNoChecks(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, "detail_no_checks.json") })

	pr, err := c.PRDetail(context.Background(), "dave", "tools", 7)
	if err != nil {
		t.Fatalf("PRDetail: %v", err)
	}
	if pr.Rollup != model.CheckNone || pr.Checks != nil {
		t.Errorf("rollup, checks = %q %+v, want none", pr.Rollup, pr.Checks)
	}
	if pr.ReviewDecision != model.DecisionNone || !pr.IsDraft || pr.MergeStateStatus != "DRAFT" {
		t.Errorf("decision, draft, merge state = %q %v %q", pr.ReviewDecision, pr.IsDraft, pr.MergeStateStatus)
	}
	if !reflect.DeepEqual(pr.MergeMethods, []model.MergeMethod{model.MergeMethodMerge}) || pr.DeleteBranchOnMerge {
		t.Errorf("merge methods, delete branch = %v %v", pr.MergeMethods, pr.DeleteBranchOnMerge)
	}
	if pr.Reviews != nil || pr.Threads != nil || pr.Comments != nil || pr.Body != "" {
		t.Errorf("want no reviews, threads, comments, body; got %+v", pr)
	}
	if pr.RequestedReviewers != nil || pr.Assignees != nil {
		t.Errorf("reviewers, assignees = %+v %+v, want none", pr.RequestedReviewers, pr.Assignees)
	}
}

func TestPRDetailExternalCheck(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, "detail_external_check.json") })

	pr, err := c.PRDetail(context.Background(), "acme", "web", 9)
	if err != nil {
		t.Fatalf("PRDetail: %v", err)
	}
	want := []model.Check{
		{Name: "ci/circleci: build", Status: "COMPLETED", Conclusion: "SUCCESS", App: "circleci-checks",
			URL: "https://circleci.com/gh/acme/web/77"},
		{Name: "ci/jenkins", Status: "PENDING", App: "jenkins-bot", URL: "https://jenkins.example.com/job/web/12"},
		{Name: "deploy/preview", Status: "COMPLETED", Conclusion: "ERROR"},
	}
	if !reflect.DeepEqual(pr.Checks, want) {
		t.Errorf("checks = %+v, want %+v", pr.Checks, want)
	}
	for _, ch := range pr.Checks {
		if ch.Rerunnable || ch.WorkflowRunID != 0 {
			t.Errorf("check %q is rerunnable, want not", ch.Name)
		}
	}
	if pr.Rollup != model.CheckPending {
		t.Errorf("rollup = %q, want PENDING", pr.Rollup)
	}
}

func TestCheckRerunnableRule(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"other app with a run", `{"__typename": "CheckRun", "name": "x", "checkSuite": {"app": {"slug": "other-app"}, "workflowRun": {"databaseId": 5}}}`},
		{"Actions with no run", `{"__typename": "CheckRun", "name": "x", "checkSuite": {"app": {"slug": "github-actions"}, "workflowRun": null}}`},
		{"no check suite", `{"__typename": "CheckRun", "name": "x", "checkSuite": null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c checkContextJSON
			if err := json.Unmarshal([]byte(tt.json), &c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if ch := c.check(); ch.Rerunnable || ch.WorkflowRunID != 0 {
				t.Errorf("check = %+v, want not rerunnable", ch)
			}
		})
	}
}

func TestPRDetailNotFound(t *testing.T) {
	for _, name := range []string{"detail_not_found.json", "detail_repo_not_found.json"} {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, name) })
			_, err := c.PRDetail(context.Background(), "acme", "gone", 999999)
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestPRDetailNullWithoutError(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply {
		return reply{status: http.StatusOK, body: `{"data": {"repository": null}}`}
	})
	if _, err := c.PRDetail(context.Background(), "acme", "gone", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPRDetailOtherErrors(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, "search_graphql_error.json") })
	_, err := c.PRDetail(context.Background(), "acme", "api", 1)
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || errors.Is(err, ErrNotFound) {
		t.Errorf("GraphQL err = %v, want GraphQLError and not ErrNotFound", err)
	}

	c, _ = newTestClient(t, func(gqlRequest) reply {
		return reply{status: http.StatusBadGateway, body: `{"message": "bad gateway"}`}
	})
	_, err = c.PRDetail(context.Background(), "acme", "api", 1)
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("HTTP err = %v, want HTTPError", err)
	}
}
