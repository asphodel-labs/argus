package gh

import (
	"context"
	"fmt"

	"github.com/tdragon/argus/internal/model"
)

// assignableUsersPage is the number of assignable users that one query
// reads. The query does not paginate; a query text finds other users.
const assignableUsersPage = 100

var assignableUsersQuery = fmt.Sprintf(`query AssignableUsers($owner: String!, $repo: String!, $q: String) {
  repository(owner: $owner, name: $repo) {
    assignableUsers(first: %d, query: $q) { totalCount nodes { id login name } }
  }
}`, assignableUsersPage)

type assignableUsersResponse struct {
	Repository *struct {
		AssignableUsers struct {
			TotalCount int        `json:"totalCount"`
			Nodes      []userJSON `json:"nodes"`
		} `json:"assignableUsers"`
	} `json:"repository"`
}

// AssignableUsers returns the first page of the users that can be
// assigned in the repository, and the count of all matches. A non-empty
// query makes GitHub return only the users whose login or name matches
// it. It returns an error that wraps ErrNotFound if GitHub cannot find
// the repository.
func (c *Client) AssignableUsers(ctx context.Context, owner, repo, query string) (users []model.Actor, total int, err error) {
	name := fmt.Sprintf("assignable users %s/%s", owner, repo)
	vars := map[string]any{"owner": owner, "repo": repo}
	if query != "" {
		vars["q"] = query
	}
	var resp assignableUsersResponse
	if err := c.query(ctx, name, assignableUsersQuery, vars, &resp); err != nil {
		return nil, 0, notFound(name, err)
	}
	if resp.Repository == nil {
		return nil, 0, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	au := resp.Repository.AssignableUsers
	for _, u := range au.Nodes {
		users = append(users, u.actor())
	}
	return users, au.TotalCount, nil
}

const suggestedReviewersQuery = `query SuggestedReviewers($id: ID!) {
  node(id: $id) {
    ... on PullRequest { suggestedReviewers { reviewer { id login name } } }
  }
}`

type suggestedReviewersResponse struct {
	Node *struct {
		SuggestedReviewers []struct {
			Reviewer *userJSON `json:"reviewer"`
		} `json:"suggestedReviewers"`
	} `json:"node"`
}

// SuggestedReviewers returns the users that GitHub suggests as reviewers
// of the PR, in GitHub order. It returns an error that wraps ErrNotFound
// if GitHub cannot find the PR.
func (c *Client) SuggestedReviewers(ctx context.Context, prID string) ([]model.Actor, error) {
	name := "suggested reviewers " + prID
	var resp suggestedReviewersResponse
	if err := c.query(ctx, name, suggestedReviewersQuery, map[string]any{"id": prID}, &resp); err != nil {
		return nil, notFound(name, err)
	}
	if resp.Node == nil {
		return nil, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	var out []model.Actor
	for _, s := range resp.Node.SuggestedReviewers {
		if s.Reviewer != nil {
			out = append(out, s.Reviewer.actor())
		}
	}
	return out, nil
}
