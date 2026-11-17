package gh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/asphodel-labs/argus/internal/model"
)

// PageSize is the number of PRs in one search page. A page of 20 costs 11
// rate limit points; a page of 100 costs 54. The cost depends on the
// requested size, not on the result size (measured on 2026-11-16).
const PageSize = 20

// Search queries for the "needs my review" scope.
const (
	QueryReviewRequested = "is:pr is:open review-requested:@me"
	QueryAssigned        = "is:pr is:open assignee:@me"
)

// threadsPerPR is the number of the newest review threads that a summary
// reads for the "needs reply" status.
const threadsPerPR = 50

var searchQuery = `query Search($q: String!, $first: Int!, $after: String) {
  rateLimit { cost remaining resetAt }
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    issueCount
    pageInfo { endCursor hasNextPage }
    nodes {
      ... on PullRequest {
        id
        number
        title
        url
        state
        isDraft
        mergeable
        reviewDecision
        createdAt
        updatedAt
        headRefOid
        baseRefName
        repository { name owner { login } }
        author { __typename login }
        stack { id number size }
        stackEntry { position }
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
        comments(last: 1) { totalCount nodes { author { __typename login } createdAt } }
        reviews(last: 1) { totalCount nodes { author { __typename login } createdAt submittedAt } }
        reviewThreads(last: ` + strconv.Itoa(threadsPerPR) + `) {
          totalCount
          nodes {
            id
            path
            line
            isResolved
            comments(last: 1) { nodes { id author { __typename login } createdAt } }
          }
        }
      }
    }
  }
}`

// SearchResult is one or more pages of a PR search.
type SearchResult struct {
	PRs         []model.PR
	IssueCount  int
	EndCursor   string
	HasNextPage bool
	RateLimit   RateLimit
}

type actorJSON struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

func (a *actorJSON) actor() model.Actor {
	if a == nil {
		return model.Actor{}
	}
	return model.Actor{Login: a.Login, Typename: a.Typename}
}

type rateLimitJSON struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

func (r rateLimitJSON) rateLimit() RateLimit {
	return RateLimit(r)
}

type activityJSON struct {
	Author      *actorJSON `json:"author"`
	CreatedAt   time.Time  `json:"createdAt"`
	SubmittedAt *time.Time `json:"submittedAt"`
}

// time returns the submit time of a review, or else the create time.
func (a activityJSON) time() time.Time {
	if a.SubmittedAt != nil {
		return *a.SubmittedAt
	}
	return a.CreatedAt
}

// stackJSON is the stack of a PR. Only the detail query reads the
// entries.
type stackJSON struct {
	ID      string `json:"id"`
	Number  int    `json:"number"`
	Size    int    `json:"size"`
	Entries struct {
		Nodes []struct {
			Position    int `json:"position"`
			PullRequest *struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
				State  string `json:"state"`
			} `json:"pullRequest"`
		} `json:"nodes"`
	} `json:"entries"`
}

// stack returns the place of the PR in the stack, with the entries in
// position order. It skips an entry whose PR the viewer cannot see.
func (s *stackJSON) stack(position int) model.Stack {
	if s == nil {
		return model.Stack{}
	}
	out := model.Stack{ID: s.ID, Number: s.Number, Size: s.Size, Position: position}
	for _, e := range s.Entries.Nodes {
		if e.PullRequest == nil {
			continue
		}
		out.Entries = append(out.Entries, model.StackEntry{
			Position: e.Position, Number: e.PullRequest.Number, Title: e.PullRequest.Title, State: e.PullRequest.State,
		})
	}
	slices.SortStableFunc(out.Entries, func(a, b model.StackEntry) int { return a.Position - b.Position })
	return out
}

type prSummaryJSON struct {
	ID             string     `json:"id"`
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	State          string     `json:"state"`
	IsDraft        bool       `json:"isDraft"`
	Mergeable      string     `json:"mergeable"`
	ReviewDecision *string    `json:"reviewDecision"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	HeadRefOid     string     `json:"headRefOid"`
	BaseRefName    string     `json:"baseRefName"`
	Author         *actorJSON `json:"author"`
	Stack          *stackJSON `json:"stack"`
	StackEntry     *struct {
		Position int `json:"position"`
	} `json:"stackEntry"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Comments struct {
		TotalCount int            `json:"totalCount"`
		Nodes      []activityJSON `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		TotalCount int            `json:"totalCount"`
		Nodes      []activityJSON `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			ID         string `json:"id"`
			Path       string `json:"path"`
			Line       *int   `json:"line"`
			IsResolved bool   `json:"isResolved"`
			Comments   struct {
				Nodes []struct {
					ID        string     `json:"id"`
					Author    *actorJSON `json:"author"`
					CreatedAt time.Time  `json:"createdAt"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

func (p prSummaryJSON) pr() model.PR {
	pr := model.PR{
		ID:           p.ID,
		Repo:         model.Repo{Owner: p.Repository.Owner.Login, Name: p.Repository.Name},
		Number:       p.Number,
		Title:        p.Title,
		URL:          p.URL,
		State:        p.State,
		Author:       p.Author.actor(),
		IsDraft:      p.IsDraft,
		Mergeable:    model.Mergeable(p.Mergeable),
		HeadSHA:      p.HeadRefOid,
		BaseRef:      p.BaseRefName,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
		CommentCount: p.Comments.TotalCount,
		ReviewCount:  p.Reviews.TotalCount,
		ThreadCount:  p.ReviewThreads.TotalCount,
	}
	if p.ReviewDecision != nil {
		pr.ReviewDecision = model.ReviewDecision(*p.ReviewDecision)
	}
	if p.StackEntry != nil {
		pr.Stack = p.Stack.stack(p.StackEntry.Position)
	}
	if n := len(p.Commits.Nodes); n > 0 {
		if r := p.Commits.Nodes[n-1].Commit.StatusCheckRollup; r != nil {
			pr.Rollup = model.CheckState(r.State)
		}
	}

	var newest time.Time
	consider := func(a *actorJSON, at time.Time) {
		if a != nil && !at.Before(newest) {
			newest = at
			pr.NewestAuthor = a.actor()
		}
	}
	for _, c := range p.Comments.Nodes {
		consider(c.Author, c.time())
	}
	for _, r := range p.Reviews.Nodes {
		consider(r.Author, r.time())
	}
	for _, t := range p.ReviewThreads.Nodes {
		th := model.Thread{ID: t.ID, Path: t.Path, IsResolved: t.IsResolved}
		if t.Line != nil {
			th.Line = *t.Line
		}
		for _, c := range t.Comments.Nodes {
			th.Comments = append(th.Comments, model.Comment{ID: c.ID, Author: c.Author.actor(), CreatedAt: c.CreatedAt})
			consider(c.Author, c.CreatedAt)
		}
		pr.Threads = append(pr.Threads, th)
	}
	return pr
}

type searchResponse struct {
	RateLimit rateLimitJSON `json:"rateLimit"`
	Search    struct {
		IssueCount int `json:"issueCount"`
		PageInfo   struct {
			EndCursor   *string `json:"endCursor"`
			HasNextPage bool    `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []prSummaryJSON `json:"nodes"`
	} `json:"search"`
}

// Search returns one page of PRs for the search query q. The page starts
// after the cursor after; an empty cursor starts at the first PR.
func (c *Client) Search(ctx context.Context, q string, first int, after string) (SearchResult, error) {
	vars := map[string]any{"q": q, "first": first}
	if after != "" {
		vars["after"] = after
	}
	var resp searchResponse
	if err := c.query(ctx, "search "+strconv.Quote(q), searchQuery, vars, &resp); err != nil {
		return SearchResult{}, err
	}
	res := SearchResult{
		IssueCount:  resp.Search.IssueCount,
		HasNextPage: resp.Search.PageInfo.HasNextPage,
		RateLimit:   resp.RateLimit.rateLimit(),
		PRs:         make([]model.PR, 0, len(resp.Search.Nodes)),
	}
	if resp.Search.PageInfo.EndCursor != nil {
		res.EndCursor = *resp.Search.PageInfo.EndCursor
	}
	for _, n := range resp.Search.Nodes {
		// A search of type ISSUE can return issues. They decode with no id.
		if n.ID == "" {
			continue
		}
		res.PRs = append(res.PRs, n.pr())
	}
	return res, nil
}

// SearchPRs returns the PRs of the first page of the search query q, with
// first PRs or fewer.
func (c *Client) SearchPRs(ctx context.Context, q string, first int) ([]model.PR, error) {
	res, err := c.Search(ctx, q, first, "")
	if err != nil {
		return nil, err
	}
	return res.PRs, nil
}

// SearchAll returns every PR for the search query q. It reads pages of
// PageSize until the last page. RateLimit is the value after the last page.
func (c *Client) SearchAll(ctx context.Context, q string) (SearchResult, error) {
	var all SearchResult
	after := ""
	for {
		page, err := c.Search(ctx, q, PageSize, after)
		if err != nil {
			return SearchResult{}, err
		}
		all.PRs = append(all.PRs, page.PRs...)
		all.IssueCount = page.IssueCount
		all.EndCursor = page.EndCursor
		all.RateLimit = page.RateLimit
		if !page.HasNextPage || page.EndCursor == "" {
			return all, nil
		}
		after = page.EndCursor
	}
}

// NeedsMyReview returns the PRs that request my review or that are assigned
// to me. A PR in both searches appears once, at its first position.
// IssueCount is the number of PRs after the merge.
func (c *Client) NeedsMyReview(ctx context.Context) (SearchResult, error) {
	requested, err := c.SearchAll(ctx, QueryReviewRequested)
	if err != nil {
		return SearchResult{}, err
	}
	assigned, err := c.SearchAll(ctx, QueryAssigned)
	if err != nil {
		return SearchResult{}, err
	}
	res := SearchResult{RateLimit: assigned.RateLimit}
	seen := make(map[string]bool, len(requested.PRs)+len(assigned.PRs))
	for _, pr := range slices.Concat(requested.PRs, assigned.PRs) {
		if seen[pr.ID] {
			continue
		}
		seen[pr.ID] = true
		res.PRs = append(res.PRs, pr)
	}
	res.IssueCount = len(res.PRs)
	return res, nil
}

// PRRef identifies a PR by repository and number.
type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

// PRState is the state of a PR from the GitHub PullRequestState enum.
type PRState string

// PR states.
const (
	PRStateOpen   PRState = "OPEN"
	PRStateClosed PRState = "CLOSED"
	PRStateMerged PRState = "MERGED"
)

// PRStates returns the state of each PR in refs with one query.
// A PR or a repository that GitHub cannot find is not in the result.
func (c *Client) PRStates(ctx context.Context, refs []PRRef) (map[PRRef]PRState, error) {
	states := make(map[PRRef]PRState, len(refs))
	if len(refs) == 0 {
		return states, nil
	}

	var decl, body strings.Builder
	vars := make(map[string]any, 3*len(refs))
	for i, r := range refs {
		fmt.Fprintf(&decl, ", $o%d: String!, $r%d: String!, $n%d: Int!", i, i, i)
		fmt.Fprintf(&body, "  pr%d: repository(owner: $o%d, name: $r%d) { pullRequest(number: $n%d) { state } }\n", i, i, i, i)
		vars["o"+strconv.Itoa(i)] = r.Owner
		vars["r"+strconv.Itoa(i)] = r.Repo
		vars["n"+strconv.Itoa(i)] = r.Number
	}
	query := "query PRStates(" + strings.TrimPrefix(decl.String(), ", ") + ") {\n" + body.String() + "}"

	type repoJSON struct {
		PullRequest *struct {
			State string `json:"state"`
		} `json:"pullRequest"`
	}
	resp := map[string]*repoJSON{}
	err := c.query(ctx, "PR states", query, vars, &resp)
	var gqlErr *api.GraphQLError
	if err != nil && !(errors.As(err, &gqlErr) && allNotFound(gqlErr)) {
		return nil, err
	}
	for i, r := range refs {
		repo := resp["pr"+strconv.Itoa(i)]
		if repo == nil || repo.PullRequest == nil {
			continue
		}
		states[r] = PRState(repo.PullRequest.State)
	}
	return states, nil
}

// allNotFound reports whether every error of e is of type NOT_FOUND.
func allNotFound(e *api.GraphQLError) bool {
	for _, item := range e.Errors {
		if item.Type != "NOT_FOUND" {
			return false
		}
	}
	return len(e.Errors) > 0
}
