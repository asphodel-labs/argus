package gh

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/model"
)

// okReply returns a 200 reply with data as the GraphQL data.
func okReply(data string) reply {
	return reply{status: http.StatusOK, body: `{"data": ` + data + `}`}
}

// inputOf returns the $input variable of the only request of ft.
func inputOf(t *testing.T, ft *fakeTransport) map[string]any {
	t.Helper()
	reqs := ft.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	input, ok := reqs[0].Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("variables = %v, want an input object", reqs[0].Variables)
	}
	return input
}

func TestMutationRequestBodies(t *testing.T) {
	tests := []struct {
		name      string
		mutation  string
		data      string
		call      func(c *Client) error
		wantInput map[string]any
	}{
		{
			name:     "approve with body",
			mutation: "addPullRequestReview(input: $input)",
			data:     `{"addPullRequestReview": {"pullRequestReview": {"id": "PRR_9"}}}`,
			call: func(c *Client) error {
				return c.Approve(context.Background(), "PR_1", "Looks good.")
			},
			wantInput: map[string]any{"pullRequestId": "PR_1", "event": "APPROVE", "body": "Looks good."},
		},
		{
			name:     "approve with no body",
			mutation: "addPullRequestReview(input: $input)",
			data:     `{"addPullRequestReview": {"pullRequestReview": {"id": "PRR_9"}}}`,
			call: func(c *Client) error {
				return c.Approve(context.Background(), "PR_1", "")
			},
			wantInput: map[string]any{"pullRequestId": "PR_1", "event": "APPROVE"},
		},
		{
			name:     "merge",
			mutation: "mergePullRequest(input: $input)",
			data:     `{"mergePullRequest": {"pullRequest": {"id": "PR_1", "state": "MERGED"}}}`,
			call: func(c *Client) error {
				return c.Merge(context.Background(), "PR_1", model.MergeMethodSquash, "abc123")
			},
			wantInput: map[string]any{"pullRequestId": "PR_1", "mergeMethod": "SQUASH", "expectedHeadOid": "abc123"},
		},
		{
			name:     "merge with no head",
			mutation: "mergePullRequest(input: $input)",
			data:     `{"mergePullRequest": {"pullRequest": {"id": "PR_1", "state": "MERGED"}}}`,
			call: func(c *Client) error {
				return c.Merge(context.Background(), "PR_1", model.MergeMethodMerge, "")
			},
			wantInput: map[string]any{"pullRequestId": "PR_1", "mergeMethod": "MERGE"},
		},
		{
			name:     "enable auto-merge",
			mutation: "enablePullRequestAutoMerge(input: $input)",
			data:     `{"enablePullRequestAutoMerge": {"pullRequest": {"id": "PR_1"}}}`,
			call: func(c *Client) error {
				return c.EnableAutoMerge(context.Background(), "PR_1", model.MergeMethodRebase, "abc123")
			},
			wantInput: map[string]any{"pullRequestId": "PR_1", "mergeMethod": "REBASE", "expectedHeadOid": "abc123"},
		},
		{
			name:     "add comment",
			mutation: "addComment(input: $input)",
			data:     `{"addComment": {"commentEdge": {"node": {"id": "IC_9"}}}}`,
			call: func(c *Client) error {
				return c.AddComment(context.Background(), "PR_1", "Thanks.")
			},
			wantInput: map[string]any{"subjectId": "PR_1", "body": "Thanks."},
		},
		{
			name:     "reply to thread",
			mutation: "addPullRequestReviewThreadReply(input: $input)",
			data:     `{"addPullRequestReviewThreadReply": {"comment": {"id": "PRRC_9"}}}`,
			call: func(c *Client) error {
				return c.ReplyToThread(context.Background(), "PRRT_1", "Done.")
			},
			wantInput: map[string]any{"pullRequestReviewThreadId": "PRRT_1", "body": "Done."},
		},
		{
			name:     "add reaction",
			mutation: "addReaction(input: $input)",
			data:     `{"addReaction": {"reaction": {"content": "HEART"}}}`,
			call: func(c *Client) error {
				return c.AddReaction(context.Background(), "IC_1", "HEART")
			},
			wantInput: map[string]any{"subjectId": "IC_1", "content": "HEART"},
		},
		{
			name:     "resolve thread",
			mutation: "resolveReviewThread(input: $input)",
			data:     `{"resolveReviewThread": {"thread": {"id": "PRRT_1", "isResolved": true}}}`,
			call: func(c *Client) error {
				return c.ResolveThread(context.Background(), "PRRT_1")
			},
			wantInput: map[string]any{"threadId": "PRRT_1"},
		},
		{
			name:     "unresolve thread",
			mutation: "unresolveReviewThread(input: $input)",
			data:     `{"unresolveReviewThread": {"thread": {"id": "PRRT_1", "isResolved": false}}}`,
			call: func(c *Client) error {
				return c.UnresolveThread(context.Background(), "PRRT_1")
			},
			wantInput: map[string]any{"threadId": "PRRT_1"},
		},
		{
			name:     "submit review",
			mutation: "submitPullRequestReview(input: $input)",
			data:     `{"submitPullRequestReview": {"pullRequestReview": {"id": "PRR_9", "state": "COMMENTED"}}}`,
			call: func(c *Client) error {
				return c.SubmitReview(context.Background(), "PRR_9", ReviewComment, "Two notes.")
			},
			wantInput: map[string]any{"pullRequestReviewId": "PRR_9", "event": "COMMENT", "body": "Two notes."},
		},
		{
			name:     "submit review with no body",
			mutation: "submitPullRequestReview(input: $input)",
			data:     `{"submitPullRequestReview": {"pullRequestReview": {"id": "PRR_9", "state": "CHANGES_REQUESTED"}}}`,
			call: func(c *Client) error {
				return c.SubmitReview(context.Background(), "PRR_9", ReviewRequestChanges, "")
			},
			wantInput: map[string]any{"pullRequestReviewId": "PRR_9", "event": "REQUEST_CHANGES"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ft := newTestClient(t, func(gqlRequest) reply { return okReply(tt.data) })
			if err := tt.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if q := ft.requests()[0].Query; !strings.HasPrefix(q, "mutation ") || !strings.Contains(q, tt.mutation) {
				t.Errorf("query = %q, want a mutation with %q", q, tt.mutation)
			}
			if got := inputOf(t, ft); !reflect.DeepEqual(got, tt.wantInput) {
				t.Errorf("input = %v, want %v", got, tt.wantInput)
			}
		})
	}
}

func TestStartReview(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply {
		return okReply(`{"addPullRequestReview": {"pullRequestReview": {"id": "PRR_new"}}}`)
	})

	id, err := c.StartReview(context.Background(), "PR_1", "abc123")
	if err != nil {
		t.Fatalf("StartReview: %v", err)
	}
	if id != "PRR_new" {
		t.Errorf("id = %q, want PRR_new", id)
	}
	want := map[string]any{"pullRequestId": "PR_1", "commitOID": "abc123"}
	if got := inputOf(t, ft); !reflect.DeepEqual(got, want) {
		t.Errorf("input = %v, want %v (no event)", got, want)
	}
}

func TestStartReviewNoID(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply {
		return okReply(`{"addPullRequestReview": {"pullRequestReview": {"id": ""}}}`)
	})
	if _, err := c.StartReview(context.Background(), "PR_1", "abc123"); err == nil {
		t.Fatal("err = nil, want an error for a missing review id")
	}
}

func TestAddReviewThread(t *testing.T) {
	tests := []struct {
		name  string
		in    ReviewThreadInput
		input map[string]any
	}{
		{
			name: "line on the head side",
			in:   ReviewThreadInput{Path: "a.go", Body: "Rename.", Line: 12, Side: SideRight},
			input: map[string]any{"pullRequestReviewId": "PRR_1", "path": "a.go", "body": "Rename.",
				"line": float64(12), "side": "RIGHT"},
		},
		{
			name: "line on the base side",
			in:   ReviewThreadInput{Path: "a.go", Body: "Why removed?", Line: 4, Side: SideLeft},
			input: map[string]any{"pullRequestReviewId": "PRR_1", "path": "a.go", "body": "Why removed?",
				"line": float64(4), "side": "LEFT"},
		},
		{
			name: "range",
			in:   ReviewThreadInput{Path: "a.go", Body: "Split this.", StartLine: 10, Line: 20, Side: SideRight},
			input: map[string]any{"pullRequestReviewId": "PRR_1", "path": "a.go", "body": "Split this.",
				"startLine": float64(10), "line": float64(20), "side": "RIGHT", "startSide": "RIGHT"},
		},
		{
			name: "file level",
			in:   ReviewThreadInput{Path: "docs/x.md", Body: "Move this file.", File: true, Line: 3},
			input: map[string]any{"pullRequestReviewId": "PRR_1", "path": "docs/x.md", "body": "Move this file.",
				"subjectType": "FILE"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ft := newTestClient(t, func(gqlRequest) reply {
				return okReply(`{"addPullRequestReviewThread": {"thread": {"id": "PRRT_new"}}}`)
			})
			id, err := c.AddReviewThread(context.Background(), "PRR_1", tt.in)
			if err != nil {
				t.Fatalf("AddReviewThread: %v", err)
			}
			if id != "PRRT_new" {
				t.Errorf("id = %q, want PRRT_new", id)
			}
			if q := ft.requests()[0].Query; !strings.Contains(q, "addPullRequestReviewThread(input: $input)") {
				t.Errorf("query = %q", q)
			}
			if got := inputOf(t, ft); !reflect.DeepEqual(got, tt.input) {
				t.Errorf("input = %v, want %v", got, tt.input)
			}
		})
	}
}

func TestAddReviewThreadNullThread(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply {
		return okReply(`{"addPullRequestReviewThread": {"thread": null}}`)
	})
	_, err := c.AddReviewThread(context.Background(), "PRR_1", ReviewThreadInput{Path: "a.go", Body: "x", Line: 1, Side: SideRight})
	if err == nil {
		t.Fatal("err = nil, want an error for a null thread")
	}
}

func TestPendingReview(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		want   PendingReview
		wantOK bool
	}{
		{
			name:   "found",
			data:   `{"node": {"reviews": {"nodes": [{"id": "PRR_p", "viewerDidAuthor": true, "commit": {"oid": "abc123"}}]}}}`,
			want:   PendingReview{ID: "PRR_p", CommitOID: "abc123"},
			wantOK: true,
		},
		{
			name: "other author skipped",
			data: `{"node": {"reviews": {"nodes": [
				{"id": "PRR_x", "viewerDidAuthor": false, "commit": {"oid": "def456"}},
				{"id": "PRR_p", "viewerDidAuthor": true, "commit": null}]}}}`,
			want:   PendingReview{ID: "PRR_p"},
			wantOK: true,
		},
		{
			name: "comments",
			data: `{"node": {"reviews": {"nodes": [{"id": "PRR_p", "viewerDidAuthor": true, "commit": {"oid": "abc123"},
				"comments": {"nodes": [
					{"path": "a.go", "body": "fix", "line": 12, "originalLine": 12, "startLine": 10, "subjectType": "LINE"},
					{"path": "b.go", "body": "whole file", "line": null, "originalLine": null, "startLine": null, "subjectType": "FILE"}]}}]}}}`,
			want: PendingReview{ID: "PRR_p", CommitOID: "abc123", Comments: []PendingComment{
				{Path: "a.go", Body: "fix", Line: 12, OriginalLine: 12, StartLine: 10},
				{Path: "b.go", Body: "whole file", File: true},
			}},
			wantOK: true,
		},
		{
			name: "none",
			data: `{"node": {"reviews": {"nodes": []}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ft := newTestClient(t, func(gqlRequest) reply { return okReply(tt.data) })
			got, ok, err := c.PendingReview(context.Background(), "PR_1")
			if err != nil {
				t.Fatalf("PendingReview: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) || ok != tt.wantOK {
				t.Errorf("PendingReview = %+v %v, want %+v %v", got, ok, tt.want, tt.wantOK)
			}
			req := ft.requests()[0]
			if req.Variables["id"] != "PR_1" || !strings.Contains(req.Query, "states: [PENDING]") {
				t.Errorf("request = %+v", req)
			}
		})
	}
}

func TestPendingReviewNotFound(t *testing.T) {
	for name, body := range map[string]string{
		"null node":       `{"data": {"node": null}}`,
		"NOT_FOUND error": `{"data": {"node": null}, "errors": [{"type": "NOT_FOUND", "path": ["node"], "message": "Could not resolve to a node with the global id of 'PR_x'."}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, func(gqlRequest) reply { return reply{status: http.StatusOK, body: body} })
			_, ok, err := c.PendingReview(context.Background(), "PR_x")
			if !errors.Is(err, ErrNotFound) || ok {
				t.Errorf("PendingReview = %v %v, want ErrNotFound", ok, err)
			}
		})
	}
}

func TestMutationGraphQLError(t *testing.T) {
	const body = `{"data": {"mergePullRequest": null}, "errors": [{"type": "UNPROCESSABLE", "path": ["mergePullRequest"],
		"message": "Pull Request is not mergeable"}]}`
	c, _ := newTestClient(t, func(gqlRequest) reply { return reply{status: http.StatusOK, body: body} })

	err := c.Merge(context.Background(), "PR_1", model.MergeMethodSquash, "abc123")
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("err = %v, want GraphQLError", err)
	}
	if !strings.Contains(err.Error(), "Pull Request is not mergeable") || !strings.Contains(err.Error(), "merge PR_1") {
		t.Errorf("err = %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want no ErrNotFound", err)
	}
}

func TestMutationErrorsPassThrough(t *testing.T) {
	const body = `{"data": null, "errors": [{"type": "FORBIDDEN", "message": "Resource not accessible by integration"}]}`
	calls := map[string]func(c *Client) error{
		"approve": func(c *Client) error { return c.Approve(context.Background(), "PR_1", "") },
		"auto-merge": func(c *Client) error {
			return c.EnableAutoMerge(context.Background(), "PR_1", model.MergeMethodMerge, "")
		},
		"comment":   func(c *Client) error { return c.AddComment(context.Background(), "PR_1", "x") },
		"reply":     func(c *Client) error { return c.ReplyToThread(context.Background(), "PRRT_1", "x") },
		"reaction":  func(c *Client) error { return c.AddReaction(context.Background(), "IC_1", "HEART") },
		"resolve":   func(c *Client) error { return c.ResolveThread(context.Background(), "PRRT_1") },
		"unresolve": func(c *Client) error { return c.UnresolveThread(context.Background(), "PRRT_1") },
		"submit":    func(c *Client) error { return c.SubmitReview(context.Background(), "PRR_1", ReviewApprove, "") },
		"start":     func(c *Client) error { _, err := c.StartReview(context.Background(), "PR_1", "abc"); return err },
		"add thread": func(c *Client) error {
			_, err := c.AddReviewThread(context.Background(), "PRR_1", ReviewThreadInput{File: true})
			return err
		},
		"pending": func(c *Client) error { _, _, err := c.PendingReview(context.Background(), "PR_1"); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, func(gqlRequest) reply { return reply{status: http.StatusOK, body: body} })
			err := call(c)
			var gqlErr *api.GraphQLError
			if !errors.As(err, &gqlErr) || !strings.Contains(err.Error(), "Resource not accessible by integration") {
				t.Errorf("err = %v, want the GraphQL error", err)
			}
		})
	}
}

func TestMutationHTTPError(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply {
		return reply{status: http.StatusBadGateway, body: `{"message": "Bad gateway"}`}
	})
	err := c.ResolveThread(context.Background(), "PRRT_1")
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway {
		t.Errorf("err = %v, want HTTP 502", err)
	}
}
