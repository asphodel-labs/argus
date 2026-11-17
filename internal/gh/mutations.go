package gh

import (
	"context"
	"errors"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/asphodel-labs/argus/internal/model"
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
	// Comments are all the comments of the review.
	Comments []PendingComment
}

// ReviewStatus is the state, the commit, the body, and the comments of one
// review.
type ReviewStatus struct {
	// State is a value of the GitHub PullRequestReviewState enum, for
	// example PENDING or COMMENTED.
	State     string
	CommitOID string
	Body      string
	// URL is the URL of the review on GitHub.
	URL      string
	Comments []PendingComment
}

// PendingComment is one comment of a review. A line field is 0 when
// GitHub sends null. File is true for a file-level comment.
type PendingComment struct {
	// ID is the node id of the comment.
	ID                string
	Path              string
	Body              string
	Line              int
	OriginalLine      int
	StartLine         int
	OriginalStartLine int
	File              bool
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

// Approve submits an approving review on the PR. An empty body sends no
// body. commitOID is the commit that the review approves. An empty
// commitOID sends no commitOID, and GitHub uses the PR head.
func (c *Client) Approve(ctx context.Context, prID, body, commitOID string) error {
	input := map[string]any{"pullRequestId": prID, "event": string(ReviewApprove)}
	if body != "" {
		input["body"] = body
	}
	if commitOID != "" {
		input["commitOID"] = commitOID
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

// reviewCommentsPage is the number of review comments in one page.
const reviewCommentsPage = 100

// reviewCommentFields are the fields of one review comment and the page
// info of the comment connection.
const reviewCommentFields = `pageInfo { hasNextPage endCursor }
          nodes { id path body line originalLine startLine originalStartLine subjectType }`

var pendingReviewQuery = fmt.Sprintf(`query PendingReview($id: ID!) {
  node(id: $id) {
    ... on PullRequest {
      reviews(first: %d, states: [PENDING]) {
        nodes {
          id viewerDidAuthor commit { oid }
          comments(first: %d) { %s }
        }
      }
    }
  }
}`, pendingReviewsPage, reviewCommentsPage, reviewCommentFields)

var reviewQuery = fmt.Sprintf(`query Review($id: ID!, $after: String) {
  node(id: $id) {
    ... on PullRequestReview {
      state body url commit { oid }
      comments(first: %d, after: $after) { %s }
    }
  }
}`, reviewCommentsPage, reviewCommentFields)

type commitRef struct {
	OID string `json:"oid"`
}

// commentConnection is one page of review comments.
type commentConnection struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []struct {
		ID                string `json:"id"`
		Path              string `json:"path"`
		Body              string `json:"body"`
		Line              *int   `json:"line"`
		OriginalLine      *int   `json:"originalLine"`
		StartLine         *int   `json:"startLine"`
		OriginalStartLine *int   `json:"originalStartLine"`
		SubjectType       string `json:"subjectType"`
	} `json:"nodes"`
}

// comments returns the comments of the page.
func (cc commentConnection) comments() []PendingComment {
	var out []PendingComment
	for _, c := range cc.Nodes {
		out = append(out, PendingComment{
			ID:                c.ID,
			Path:              c.Path,
			Body:              c.Body,
			Line:              intOrZero(c.Line),
			OriginalLine:      intOrZero(c.OriginalLine),
			StartLine:         intOrZero(c.StartLine),
			OriginalStartLine: intOrZero(c.OriginalStartLine),
			File:              c.SubjectType == "FILE",
		})
	}
	return out
}

// next returns the cursor of the next page, or "" after the last page.
func (cc commentConnection) next() string {
	if !cc.PageInfo.HasNextPage {
		return ""
	}
	return cc.PageInfo.EndCursor
}

type pendingReviewResponse struct {
	Node *struct {
		Reviews struct {
			Nodes []struct {
				ID              string            `json:"id"`
				ViewerDidAuthor bool              `json:"viewerDidAuthor"`
				Commit          *commitRef        `json:"commit"`
				Comments        commentConnection `json:"comments"`
			} `json:"nodes"`
		} `json:"reviews"`
	} `json:"node"`
}

type reviewResponse struct {
	Node *struct {
		State    string            `json:"state"`
		Body     string            `json:"body"`
		URL      string            `json:"url"`
		Commit   *commitRef        `json:"commit"`
		Comments commentConnection `json:"comments"`
	} `json:"node"`
}

// PendingReview returns the pending review of the viewer on the PR with
// all its comments. ok is false if the viewer has no pending review. It
// returns an error that wraps ErrNotFound if GitHub cannot find the PR.
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
		out := PendingReview{ID: r.ID, Comments: r.Comments.comments()}
		if r.Commit != nil {
			out.CommitOID = r.Commit.OID
		}
		if after := r.Comments.next(); after != "" {
			st, found, err := c.review(ctx, r.ID, after)
			if err != nil {
				return PendingReview{}, false, err
			}
			if !found {
				return PendingReview{}, false, fmt.Errorf("%s: review %s: %w", name, r.ID, ErrNotFound)
			}
			out.Comments = append(out.Comments, st.Comments...)
		}
		return out, true, nil
	}
	return PendingReview{}, false, nil
}

// Review returns the state, the commit, the body, the URL, and all the
// comments of a review.
// found is false if GitHub cannot find the review, for example after the
// viewer deleted a pending review.
func (c *Client) Review(ctx context.Context, reviewID string) (ReviewStatus, bool, error) {
	return c.review(ctx, reviewID, "")
}

// review reads the review and its comments after the cursor after, one
// page at a time.
func (c *Client) review(ctx context.Context, reviewID, after string) (ReviewStatus, bool, error) {
	name := "review " + reviewID
	var out ReviewStatus
	for {
		vars := map[string]any{"id": reviewID}
		if after != "" {
			vars["after"] = after
		}
		var resp reviewResponse
		if err := c.query(ctx, name, reviewQuery, vars, &resp); err != nil {
			if err := notFound(name, err); errors.Is(err, ErrNotFound) {
				return ReviewStatus{}, false, nil
			}
			return ReviewStatus{}, false, err
		}
		if resp.Node == nil {
			return ReviewStatus{}, false, nil
		}
		out.State, out.Body, out.URL = resp.Node.State, resp.Node.Body, resp.Node.URL
		if resp.Node.Commit != nil {
			out.CommitOID = resp.Node.Commit.OID
		}
		out.Comments = append(out.Comments, resp.Node.Comments.comments()...)
		if after = resp.Node.Comments.next(); after == "" {
			return out, true, nil
		}
	}
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
  addPullRequestReviewThread(input: $input) { thread { id comments(first: 1) { nodes { id } } } }
}`

type addThreadResponse struct {
	AddPullRequestReviewThread struct {
		Thread *struct {
			ID       string `json:"id"`
			Comments struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"comments"`
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

// AddReviewThread adds a thread to the pending review and returns the id
// of the first comment of the thread. DeleteReviewComment takes this id.
func (c *Client) AddReviewThread(ctx context.Context, reviewID string, t ReviewThreadInput) (string, error) {
	name := fmt.Sprintf("add review thread %s:%d", t.Path, t.Line)
	var resp addThreadResponse
	if err := c.mutate(ctx, name, addThreadMutation, threadInput(reviewID, t), &resp); err != nil {
		return "", err
	}
	th := resp.AddPullRequestReviewThread.Thread
	if th == nil || len(th.Comments.Nodes) == 0 || th.Comments.Nodes[0].ID == "" {
		return "", fmt.Errorf("%s: no comment id in the response", name)
	}
	return th.Comments.Nodes[0].ID, nil
}

const deleteCommentMutation = `mutation DeleteComment($input: DeletePullRequestReviewCommentInput!) {
  deletePullRequestReviewComment(input: $input) { clientMutationId }
}`

// DeleteReviewComment deletes a review comment.
func (c *Client) DeleteReviewComment(ctx context.Context, commentID string) error {
	input := map[string]any{"id": commentID}
	return c.mutate(ctx, "delete review comment "+commentID, deleteCommentMutation, input, nil)
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

// UpdateMethod is a value of the GitHub PullRequestBranchUpdateMethod enum.
type UpdateMethod string

// Branch update methods. MERGE merges the base branch into the head
// branch. REBASE rebases the head branch on the base branch.
const (
	UpdateMerge  UpdateMethod = "MERGE"
	UpdateRebase UpdateMethod = "REBASE"
)

const updateBranchMutation = `mutation UpdateBranch($input: UpdatePullRequestBranchInput!) {
  updatePullRequestBranch(input: $input) { pullRequest { id headRefOid } }
}`

// UpdateBranch brings the head branch of the PR up to date with the base
// branch. GitHub refuses the update if the head is not headSHA. An empty
// headSHA sends no expectedHeadOid.
func (c *Client) UpdateBranch(ctx context.Context, prID string, method UpdateMethod, headSHA string) error {
	input := map[string]any{"pullRequestId": prID, "updateMethod": string(method)}
	if headSHA != "" {
		input["expectedHeadOid"] = headSHA
	}
	return c.mutate(ctx, "update branch "+prID, updateBranchMutation, input, nil)
}

const requestReviewsMutation = `mutation RequestReviews($input: RequestReviewsInput!) {
  requestReviews(input: $input) { pullRequest { id } }
}`

// RequestReviews replaces the requested reviewers of the PR with the
// users, the teams, and the bots of the ids. It sends union false, so
// GitHub removes each requested reviewer that the ids do not include.
// A caller that keeps a team or a bot must give its id.
func (c *Client) RequestReviews(ctx context.Context, prID string, userIDs, teamIDs, botIDs []string) error {
	input := map[string]any{
		"pullRequestId": prID,
		"userIds":       idList(userIDs),
		"teamIds":       idList(teamIDs),
		"botIds":        idList(botIDs),
		"union":         false,
	}
	return c.mutate(ctx, "request reviews "+prID, requestReviewsMutation, input, nil)
}

// idList returns ids, or an empty list for nil. GitHub then gets [] and
// not null.
func idList(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

const addAssigneesMutation = `mutation AddAssignees($input: AddAssigneesToAssignableInput!) {
  addAssigneesToAssignable(input: $input) { clientMutationId }
}`

const removeAssigneesMutation = `mutation RemoveAssignees($input: RemoveAssigneesFromAssignableInput!) {
  removeAssigneesFromAssignable(input: $input) { clientMutationId }
}`

// AddAssignees assigns the users of ids to the PR or the issue of
// assignableID. GitHub keeps the other assignees.
func (c *Client) AddAssignees(ctx context.Context, assignableID string, ids []string) error {
	input := map[string]any{"assignableId": assignableID, "assigneeIds": idList(ids)}
	return c.mutate(ctx, "add assignees "+assignableID, addAssigneesMutation, input, nil)
}

// RemoveAssignees removes the users of ids from the assignees of the PR or
// the issue of assignableID.
func (c *Client) RemoveAssignees(ctx context.Context, assignableID string, ids []string) error {
	input := map[string]any{"assignableId": assignableID, "assigneeIds": idList(ids)}
	return c.mutate(ctx, "remove assignees "+assignableID, removeAssigneesMutation, input, nil)
}
