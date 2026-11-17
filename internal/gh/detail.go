package gh

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/model"
)

// ErrNotFound means that GitHub cannot find the repository or the PR.
var ErrNotFound = errors.New("not found")

// actionsAppSlug is the app slug of the check runs of GitHub Actions.
const actionsAppSlug = "github-actions"

// Page sizes of the detail query. The query reads the first or the last
// page only; it does not paginate.
const (
	detailChecks         = 100
	detailReviews        = 100
	detailThreads        = 100
	detailThreadComments = 50
	detailComments       = 100
)

var detailQuery = fmt.Sprintf(`query PRDetail($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    mergeCommitAllowed
    squashMergeAllowed
    rebaseMergeAllowed
    deleteBranchOnMerge
    pullRequest(number: $number) {
      id
      number
      title
      url
      state
      body
      isDraft
      mergeable
      mergeStateStatus
      reviewDecision
      createdAt
      updatedAt
      headRefOid
      baseRefName
      repository { name owner { login } }
      author { __typename login }
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup {
              state
              contexts(first: %d) {
                nodes {
                  __typename
                  ... on CheckRun {
                    name
                    status
                    conclusion
                    detailsUrl
                    checkSuite { app { slug } workflowRun { databaseId } }
                  }
                  ... on StatusContext {
                    context
                    state
                    targetUrl
                    creator { login }
                  }
                }
              }
            }
          }
        }
      }
      reviews(last: %d) {
        totalCount
        nodes { id author { __typename login } state body submittedAt }
      }
      reviewThreads(first: %d) {
        totalCount
        nodes {
          id
          path
          line
          isResolved
          comments(first: %d) { nodes { ...CommentFields } }
        }
      }
      comments(last: %d) {
        totalCount
        nodes { ...CommentFields }
      }
    }
  }
}

fragment CommentFields on Comment {
  ... on Node { id }
  author { __typename login }
  body
  createdAt
  ... on Reactable { reactionGroups { content viewerHasReacted reactors { totalCount } } }
}`, detailChecks, detailReviews, detailThreads, detailThreadComments, detailComments)

type reactionGroupJSON struct {
	Content          string `json:"content"`
	ViewerHasReacted bool   `json:"viewerHasReacted"`
	Reactors         struct {
		TotalCount int `json:"totalCount"`
	} `json:"reactors"`
}

type commentJSON struct {
	ID             string              `json:"id"`
	Author         *actorJSON          `json:"author"`
	Body           string              `json:"body"`
	CreatedAt      time.Time           `json:"createdAt"`
	ReactionGroups []reactionGroupJSON `json:"reactionGroups"`
}

// comment returns the comment. It drops the reaction groups with no reactors.
func (c commentJSON) comment() model.Comment {
	out := model.Comment{ID: c.ID, Author: c.Author.actor(), Body: c.Body, CreatedAt: c.CreatedAt}
	for _, g := range c.ReactionGroups {
		if g.Reactors.TotalCount == 0 {
			continue
		}
		out.Reactions = append(out.Reactions, model.Reaction{
			Content:          g.Content,
			Count:            g.Reactors.TotalCount,
			ViewerHasReacted: g.ViewerHasReacted,
		})
	}
	return out
}

type checkContextJSON struct {
	Typename string `json:"__typename"`

	// CheckRun fields.
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	DetailsURL *string `json:"detailsUrl"`
	CheckSuite *struct {
		App *struct {
			Slug string `json:"slug"`
		} `json:"app"`
		WorkflowRun *struct {
			DatabaseID int64 `json:"databaseId"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`

	// StatusContext fields.
	Context   string  `json:"context"`
	State     string  `json:"state"`
	TargetURL *string `json:"targetUrl"`
	Creator   *struct {
		Login string `json:"login"`
	} `json:"creator"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// check returns the check. Only a check run of GitHub Actions with a
// workflow run is rerunnable.
func (c checkContextJSON) check() model.Check {
	if c.Typename == "StatusContext" {
		out := model.Check{Name: c.Context, URL: deref(c.TargetURL)}
		if c.Creator != nil {
			out.App = c.Creator.Login
		}
		switch model.CheckState(c.State) {
		case model.CheckPending, model.CheckExpected:
			out.Status = "PENDING"
		default:
			out.Status = "COMPLETED"
			out.Conclusion = c.State
		}
		return out
	}
	out := model.Check{
		Name:       c.Name,
		Status:     c.Status,
		Conclusion: deref(c.Conclusion),
		URL:        deref(c.DetailsURL),
	}
	if s := c.CheckSuite; s != nil {
		if s.App != nil {
			out.App = s.App.Slug
		}
		if s.WorkflowRun != nil && out.App == actionsAppSlug {
			out.WorkflowRunID = s.WorkflowRun.DatabaseID
			out.Rerunnable = out.WorkflowRunID != 0
		}
	}
	return out
}

// prDetailJSON embeds the summary fields. Its own Comments, Reviews, and
// ReviewThreads fields hide the summary fields with the same JSON names,
// so the decoder fills only the detail shape.
type prDetailJSON struct {
	prSummaryJSON
	Body             string `json:"body"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Commits          struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []checkContextJSON `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			ID          string     `json:"id"`
			Author      *actorJSON `json:"author"`
			State       string     `json:"state"`
			Body        string     `json:"body"`
			SubmittedAt *time.Time `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			ID         string `json:"id"`
			Path       string `json:"path"`
			Line       *int   `json:"line"`
			IsResolved bool   `json:"isResolved"`
			Comments   struct {
				Nodes []commentJSON `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Comments struct {
		TotalCount int           `json:"totalCount"`
		Nodes      []commentJSON `json:"nodes"`
	} `json:"comments"`
}

type detailResponse struct {
	Repository *struct {
		MergeCommitAllowed  bool          `json:"mergeCommitAllowed"`
		SquashMergeAllowed  bool          `json:"squashMergeAllowed"`
		RebaseMergeAllowed  bool          `json:"rebaseMergeAllowed"`
		DeleteBranchOnMerge bool          `json:"deleteBranchOnMerge"`
		PullRequest         *prDetailJSON `json:"pullRequest"`
	} `json:"repository"`
}

// PRDetail returns one PR with the summary fields and the detail fields:
// body, checks, reviews, threads with all comments and reactions, PR
// comments, and the merge fields. It returns an error that wraps
// ErrNotFound if GitHub cannot find the repository or the PR. It does not
// set NewestAuthor; only the search summaries fill it.
func (c *Client) PRDetail(ctx context.Context, owner, repo string, n int) (model.PR, error) {
	name := fmt.Sprintf("PR detail %s/%s#%d", owner, repo, n)
	vars := map[string]any{"owner": owner, "repo": repo, "number": n}
	var resp detailResponse
	if err := c.query(ctx, name, detailQuery, vars, &resp); err != nil {
		var gqlErr *api.GraphQLError
		if errors.As(err, &gqlErr) && allNotFound(gqlErr) {
			return model.PR{}, fmt.Errorf("%s: %w", name, ErrNotFound)
		}
		return model.PR{}, err
	}
	if resp.Repository == nil || resp.Repository.PullRequest == nil {
		return model.PR{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}

	r := resp.Repository
	d := r.PullRequest
	pr := d.pr()
	pr.Body = d.Body
	pr.MergeStateStatus = model.MergeStateStatus(d.MergeStateStatus)
	pr.DeleteBranchOnMerge = r.DeleteBranchOnMerge
	for _, m := range []struct {
		allowed bool
		method  model.MergeMethod
	}{
		{r.MergeCommitAllowed, model.MergeMethodMerge},
		{r.SquashMergeAllowed, model.MergeMethodSquash},
		{r.RebaseMergeAllowed, model.MergeMethodRebase},
	} {
		if m.allowed {
			pr.MergeMethods = append(pr.MergeMethods, m.method)
		}
	}

	if k := len(d.Commits.Nodes); k > 0 {
		if roll := d.Commits.Nodes[k-1].Commit.StatusCheckRollup; roll != nil {
			pr.Rollup = model.CheckState(roll.State)
			for _, cc := range roll.Contexts.Nodes {
				pr.Checks = append(pr.Checks, cc.check())
			}
		}
	}

	pr.ReviewCount = d.Reviews.TotalCount
	for _, rv := range d.Reviews.Nodes {
		review := model.Review{ID: rv.ID, Author: rv.Author.actor(), State: rv.State, Body: rv.Body}
		if rv.SubmittedAt != nil {
			review.SubmittedAt = *rv.SubmittedAt
		}
		pr.Reviews = append(pr.Reviews, review)
	}

	pr.ThreadCount = d.ReviewThreads.TotalCount
	for _, t := range d.ReviewThreads.Nodes {
		th := model.Thread{ID: t.ID, Path: t.Path, IsResolved: t.IsResolved}
		if t.Line != nil {
			th.Line = *t.Line
		}
		for _, cm := range t.Comments.Nodes {
			th.Comments = append(th.Comments, cm.comment())
		}
		pr.Threads = append(pr.Threads, th)
	}

	pr.CommentCount = d.Comments.TotalCount
	for _, cm := range d.Comments.Nodes {
		pr.Comments = append(pr.Comments, cm.comment())
	}
	return pr, nil
}
