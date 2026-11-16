package gh

import (
	"context"
	"errors"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/model"
)

// ReviewEvent is an event from the GitHub PullRequestReviewEvent enum.
type ReviewEvent string

// Review events.
const (
	ReviewApprove        ReviewEvent = "APPROVE"
	ReviewComment        ReviewEvent = "COMMENT"
	ReviewRequestChanges ReviewEvent = "REQUEST_CHANGES"
)

// DiffSide is a side of the diff from the GitHub DiffSide enum.
type DiffSide string

// Diff sides. LEFT is the base side, RIGHT is the head side.
const (
	SideLeft  DiffSide = "LEFT"
	SideRight DiffSide = "RIGHT"
)

// ReviewThreadInput is one thread of a pending review.
// File true makes a file-level thread; GitHub then ignores the lines.
// StartLine 0 makes a thread on one line.
type ReviewThreadInput struct {
	Path      string
	Body      string
	Line      int
	StartLine int
	Side      DiffSide
	File      bool
}

// PendingReview is the pending review of the viewer on a PR.
type PendingReview struct {
	ID        string
	CommitOID string
	// Comments are the first pendingCommentsPage comments of the review.
	Comments []PendingComment
}

// PendingComment is one comment of a pending review. Line and StartLine
// are 0 when GitHub sends null. File is true for a file-level comment.
type PendingComment struct {
	Path         string
	Body         string
	Line         int
	OriginalLine int
	StartLine    int
	File         bool
}

// mutate sends one mutation with the variable $input and decodes the
// payload into resp.
func (c *Client) mutate(ctx context.Context, name, mutation string, input map[string]any, resp any) error {
	if resp == nil {
		resp = &map[string]any{}
	}
	return c.query(ctx, name, mutation, map[string]any{"input": input}, resp)
}

const addReviewMutation = `mutation AddReview($input: AddPullRequestReviewInput!) {
  addPullRequestReview(input: $input) { pullRequestReview { id } }
}`

type addReviewResponse struct {
	AddPullRequestReview struct {
		PullRequestReview struct {
			ID string `json:"id"`
		} `json:"pullRequestReview"`
	} `json:"addPullRequestReview"`
}

// Approve submits an approving review on the PR. An empty body sends no body.
func (c *Client) Approve(ctx context.Context, prID, body string) error {
	input := map[string]any{"pullRequestId": prID, "event": string(ReviewApprove)}
	if body != "" {
		input["body"] = body
	}
	return c.mutate(ctx, "approve "+prID, addReviewMutation, input, &addReviewResponse{})
}

// mergeInput returns the input of a merge or an auto-merge. An empty
// headSHA sends no expectedHeadOid.
func mergeInput(prID string, method model.MergeMethod, headSHA string) map[string]any {
	input := map[string]any{"pullRequestId": prID, "mergeMethod": string(method)}
	if headSHA != "" {
		input["expectedHeadOid"] = headSHA
	}
	return input
}

const mergeMutation = `mutation Merge($input: MergePullRequestInput!) {
  mergePullRequest(input: $input) { pullRequest { id state } }
}`

// Merge merges the PR with method. GitHub refuses the merge if the head
// is not headSHA. The repository setting deleteBranchOnMerge decides if
// GitHub deletes the head branch.
func (c *Client) Merge(ctx context.Context, prID string, method model.MergeMethod, headSHA string) error {
	return c.mutate(ctx, "merge "+prID, mergeMutation, mergeInput(prID, method, headSHA), nil)
}

const autoMergeMutation = `mutation AutoMerge($input: EnablePullRequestAutoMergeInput!) {
  enablePullRequestAutoMerge(input: $input) { pullRequest { id } }
}`

// EnableAutoMerge makes GitHub merge the PR with method when the required
// checks pass.
func (c *Client) EnableAutoMerge(ctx context.Context, prID string, method model.MergeMethod, headSHA string) error {
	return c.mutate(ctx, "enable auto-merge "+prID, autoMergeMutation, mergeInput(prID, method, headSHA), nil)
}

const addCommentMutation = `mutation AddComment($input: AddCommentInput!) {
  addComment(input: $input) { commentEdge { node { id } } }
}`

// AddComment adds a PR comment. subjectID is the PR id.
func (c *Client) AddComment(ctx context.Context, subjectID, body string) error {
	input := map[string]any{"subjectId": subjectID, "body": body}
	return c.mutate(ctx, "add comment "+subjectID, addCommentMutation, input, nil)
}

const replyMutation = `mutation Reply($input: AddPullRequestReviewThreadReplyInput!) {
  addPullRequestReviewThreadReply(input: $input) { comment { id } }
}`

// ReplyToThread adds a reply to a review thread. GitHub adds the reply
// to the pending review of the viewer if one exists.
func (c *Client) ReplyToThread(ctx context.Context, threadID, body string) error {
	input := map[string]any{"pullRequestReviewThreadId": threadID, "body": body}
	return c.mutate(ctx, "reply to thread "+threadID, replyMutation, input, nil)
}

const addReactionMutation = `mutation AddReaction($input: AddReactionInput!) {
  addReaction(input: $input) { reaction { content } }
}`

// AddReaction adds a reaction to a comment. content is a value of the
// GitHub ReactionContent enum, for example THUMBS_UP.
func (c *Client) AddReaction(ctx context.Context, subjectID, content string) error {
	input := map[string]any{"subjectId": subjectID, "content": content}
	return c.mutate(ctx, "add reaction "+subjectID, addReactionMutation, input, nil)
}

const resolveMutation = `mutation Resolve($input: ResolveReviewThreadInput!) {
  resolveReviewThread(input: $input) { thread { id isResolved } }
}`

const unresolveMutation = `mutation Unresolve($input: UnresolveReviewThreadInput!) {
  unresolveReviewThread(input: $input) { thread { id isResolved } }
}`

// ResolveThread resolves a review thread.
func (c *Client) ResolveThread(ctx context.Context, threadID string) error {
	input := map[string]any{"threadId": threadID}
	return c.mutate(ctx, "resolve thread "+threadID, resolveMutation, input, nil)
}

// UnresolveThread unresolves a review thread.
func (c *Client) UnresolveThread(ctx context.Context, threadID string) error {
	input := map[string]any{"threadId": threadID}
	return c.mutate(ctx, "unresolve thread "+threadID, unresolveMutation, input, nil)
}

// pendingReviewsPage is the number of pending reviews the lookup reads.
// GitHub shows the pending reviews of the viewer only, and a viewer has
// at most one pending review on a PR.
const pendingReviewsPage = 10

// pendingCommentsPage is the number of comments the lookup reads from the
// pending review.
const pendingCommentsPage = 100

var pendingReviewQuery = fmt.Sprintf(`query PendingReview($id: ID!) {
  node(id: $id) {
    ... on PullRequest {
      reviews(first: %d, states: [PENDING]) {
        nodes {
          id viewerDidAuthor commit { oid }
          comments(first: %d) { nodes { path body line originalLine startLine subjectType } }
        }
      }
    }
  }
}`, pendingReviewsPage, pendingCommentsPage)

type pendingReviewResponse struct {
	Node *struct {
		Reviews struct {
			Nodes []struct {
				ID              string `json:"id"`
				ViewerDidAuthor bool   `json:"viewerDidAuthor"`
				Commit          *struct {
					OID string `json:"oid"`
				} `json:"commit"`
				Comments struct {
					Nodes []struct {
						Path         string `json:"path"`
						Body         string `json:"body"`
						Line         *int   `json:"line"`
						OriginalLine *int   `json:"originalLine"`
						StartLine    *int   `json:"startLine"`
						SubjectType  string `json:"subjectType"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"nodes"`
		} `json:"reviews"`
	} `json:"node"`
}

// PendingReview returns the pending review of the viewer on the PR.
// ok is false if the viewer has no pending review. It returns an error
// that wraps ErrNotFound if GitHub cannot find the PR.
func (c *Client) PendingReview(ctx context.Context, prID string) (review PendingReview, ok bool, err error) {
	name := "pending review " + prID
	var resp pendingReviewResponse
	if err := c.query(ctx, name, pendingReviewQuery, map[string]any{"id": prID}, &resp); err != nil {
		return PendingReview{}, false, notFound(name, err)
	}
	if resp.Node == nil {
		return PendingReview{}, false, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	for _, r := range resp.Node.Reviews.Nodes {
		if !r.ViewerDidAuthor {
			continue
		}
		out := PendingReview{ID: r.ID}
		if r.Commit != nil {
			out.CommitOID = r.Commit.OID
		}
		for _, c := range r.Comments.Nodes {
			out.Comments = append(out.Comments, PendingComment{
				Path:         c.Path,
				Body:         c.Body,
				Line:         intOrZero(c.Line),
				OriginalLine: intOrZero(c.OriginalLine),
				StartLine:    intOrZero(c.StartLine),
				File:         c.SubjectType == "FILE",
			})
		}
		return out, true, nil
	}
	return PendingReview{}, false, nil
}

// intOrZero returns *p, or 0 for nil.
func intOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// StartReview makes a pending review on commitOID of the PR and returns
// its id. It sends no event, so the review stays pending.
func (c *Client) StartReview(ctx context.Context, prID, commitOID string) (string, error) {
	input := map[string]any{"pullRequestId": prID, "commitOID": commitOID}
	var resp addReviewResponse
	if err := c.mutate(ctx, "start review "+prID, addReviewMutation, input, &resp); err != nil {
		return "", err
	}
	id := resp.AddPullRequestReview.PullRequestReview.ID
	if id == "" {
		return "", fmt.Errorf("start review %s: no review id in the response", prID)
	}
	return id, nil
}

const addThreadMutation = `mutation AddThread($input: AddPullRequestReviewThreadInput!) {
  addPullRequestReviewThread(input: $input) { thread { id } }
}`

type addThreadResponse struct {
	AddPullRequestReviewThread struct {
		Thread *struct {
			ID string `json:"id"`
		} `json:"thread"`
	} `json:"addPullRequestReviewThread"`
}

// threadInput returns the AddPullRequestReviewThreadInput for t.
func threadInput(reviewID string, t ReviewThreadInput) map[string]any {
	input := map[string]any{"pullRequestReviewId": reviewID, "path": t.Path, "body": t.Body}
	if t.File {
		input["subjectType"] = "FILE"
		return input
	}
	input["line"] = t.Line
	input["side"] = string(t.Side)
	if t.StartLine != 0 {
		input["startLine"] = t.StartLine
		input["startSide"] = string(t.Side)
	}
	return input
}

// AddReviewThread adds a thread to the pending review and returns the
// thread id.
func (c *Client) AddReviewThread(ctx context.Context, reviewID string, t ReviewThreadInput) (string, error) {
	name := fmt.Sprintf("add review thread %s:%d", t.Path, t.Line)
	var resp addThreadResponse
	if err := c.mutate(ctx, name, addThreadMutation, threadInput(reviewID, t), &resp); err != nil {
		return "", err
	}
	th := resp.AddPullRequestReviewThread.Thread
	if th == nil || th.ID == "" {
		return "", fmt.Errorf("%s: no thread id in the response", name)
	}
	return th.ID, nil
}

const submitReviewMutation = `mutation SubmitReview($input: SubmitPullRequestReviewInput!) {
  submitPullRequestReview(input: $input) { pullRequestReview { id state } }
}`

// SubmitReview submits the pending review with event. An empty body
// sends no body.
func (c *Client) SubmitReview(ctx context.Context, reviewID string, event ReviewEvent, body string) error {
	input := map[string]any{"pullRequestReviewId": reviewID, "event": string(event)}
	if body != "" {
		input["body"] = body
	}
	return c.mutate(ctx, "submit review "+reviewID, submitReviewMutation, input, nil)
}

// notFound returns an error that wraps ErrNotFound if every GraphQL
// error of err is of type NOT_FOUND. Otherwise it returns err.
func notFound(name string, err error) error {
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) && allNotFound(gqlErr) {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return err
}
