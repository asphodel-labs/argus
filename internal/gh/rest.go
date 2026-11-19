package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/asphodel-labs/argus/internal/model"
)

// RerunFailedJobs reruns the failed jobs of a GitHub Actions workflow run.
func (c *Client) RerunFailedJobs(ctx context.Context, repo model.Repo, runID int64) error {
	return c.rerun(ctx, repo, runID, "rerun-failed-jobs")
}

// RerunWorkflow reruns all jobs of a GitHub Actions workflow run.
func (c *Client) RerunWorkflow(ctx context.Context, repo model.Repo, runID int64) error {
	return c.rerun(ctx, repo, runID, "rerun")
}

func (c *Client) rerun(ctx context.Context, repo model.Repo, runID int64, action string) error {
	path := fmt.Sprintf("repos/%s/%s/actions/runs/%d/%s", repo.Owner, repo.Name, runID, action)
	resp, err := c.rest.RequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return fmt.Errorf("%s %s run %d: %w", action, repo.FullName(), runID, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// DefaultBranch returns the default branch name of repo.
func (c *Client) DefaultBranch(ctx context.Context, repo model.Repo) (string, error) {
	path := fmt.Sprintf("repos/%s/%s", repo.Owner, repo.Name)
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.rest.DoWithContext(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", fmt.Errorf("default branch %s: %w", repo.FullName(), err)
	}
	return out.DefaultBranch, nil
}

type workflowRunJSON struct {
	ID             int64     `json:"id"`
	RunNumber      int       `json:"run_number"`
	RunAttempt     int       `json:"run_attempt"`
	Name           string    `json:"name"`
	DisplayTitle   string    `json:"display_title"`
	Event          string    `json:"event"`
	Status         string    `json:"status"`
	Conclusion     string    `json:"conclusion"`
	HeadSHA        string    `json:"head_sha"`
	UpdatedAt      time.Time `json:"updated_at"`
	HTMLURL        string    `json:"html_url"`
	HeadRepository struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

// BranchRuns returns the last n GitHub Actions runs of branch in repo,
// newest first. The branch filter of GitHub matches the head branch name,
// so a fork PR from a branch with the same name matches too. BranchRuns
// drops a run whose head repository is not repo. Thus the result can hold
// fewer than n runs.
func (c *Client) BranchRuns(ctx context.Context, repo model.Repo, branch string, n int) ([]model.WorkflowRun, error) {
	q := url.Values{}
	q.Set("branch", branch)
	q.Set("per_page", strconv.Itoa(n))
	q.Set("exclude_pull_requests", "true")
	path := fmt.Sprintf("repos/%s/%s/actions/runs?%s", repo.Owner, repo.Name, q.Encode())
	var out struct {
		WorkflowRuns []workflowRunJSON `json:"workflow_runs"`
	}
	if err := c.rest.DoWithContext(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("branch runs %s: %w", repo.FullName(), err)
	}
	var runs []model.WorkflowRun
	for _, r := range out.WorkflowRuns {
		if !strings.EqualFold(r.HeadRepository.FullName, repo.FullName()) {
			continue
		}
		runs = append(runs, model.WorkflowRun{
			ID:         r.ID,
			Number:     r.RunNumber,
			Attempt:    r.RunAttempt,
			Workflow:   r.Name,
			Title:      r.DisplayTitle,
			Event:      r.Event,
			Status:     r.Status,
			Conclusion: r.Conclusion,
			HeadSHA:    r.HeadSHA,
			UpdatedAt:  r.UpdatedAt,
			URL:        r.HTMLURL,
		})
	}
	return runs, nil
}

// DefaultPollInterval is the poll interval when GitHub sends no
// X-Poll-Interval header.
const DefaultPollInterval = 60 * time.Second

// notificationsPage is the page size of the notifications request.
const notificationsPage = 50

// Notification is one notification thread.
type Notification struct {
	ID        string
	Reason    string
	Unread    bool
	UpdatedAt time.Time
	Title     string
	// Type is the subject type, for example PullRequest.
	Type string
	// URL is the API URL of the subject.
	URL  string
	Repo model.Repo
}

// NotificationsResult is the result of a notifications check.
type NotificationsResult struct {
	// Changed is false when GitHub answers 304 Not Modified.
	Changed bool
	// LastModified is the value for the next If-Modified-Since header.
	LastModified string
	// PollInterval is the X-Poll-Interval of the response.
	PollInterval time.Duration
	// Threads is empty when Changed is false.
	Threads []Notification
}

type notificationJSON struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	Unread    bool      `json:"unread"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		Type  string `json:"type"`
		URL   string `json:"url"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// Notifications reads the first page of the notification threads of the
// viewer. ifModifiedSince is the LastModified of the previous result, or
// empty. A 304 Not Modified gives Changed false and no error.
func (c *Client) Notifications(ctx context.Context, ifModifiedSince string) (NotificationsResult, error) {
	path := fmt.Sprintf("notifications?per_page=%d", notificationsPage)
	if ifModifiedSince != "" {
		h := http.Header{}
		h.Set("If-Modified-Since", ifModifiedSince)
		ctx = withHeaders(ctx, h)
	}
	resp, err := c.rest.RequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotModified {
			return NotificationsResult{
				LastModified: ifModifiedSince,
				PollInterval: pollInterval(httpErr.Headers),
			}, nil
		}
		return NotificationsResult{}, fmt.Errorf("notifications: %w", err)
	}
	defer resp.Body.Close()

	var items []notificationJSON
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return NotificationsResult{}, fmt.Errorf("notifications: decode: %w", err)
	}
	out := NotificationsResult{
		Changed:      true,
		LastModified: resp.Header.Get("Last-Modified"),
		PollInterval: pollInterval(resp.Header),
	}
	for _, it := range items {
		n := Notification{
			ID:        it.ID,
			Reason:    it.Reason,
			Unread:    it.Unread,
			UpdatedAt: it.UpdatedAt,
			Title:     it.Subject.Title,
			Type:      it.Subject.Type,
			URL:       it.Subject.URL,
		}
		if owner, name, ok := strings.Cut(it.Repository.FullName, "/"); ok {
			n.Repo = model.Repo{Owner: owner, Name: name}
		}
		out.Threads = append(out.Threads, n)
	}
	return out, nil
}

// pollInterval returns the X-Poll-Interval of h, or DefaultPollInterval
// if the header is missing or not a positive number of seconds.
func pollInterval(h http.Header) time.Duration {
	s, err := strconv.Atoi(h.Get("X-Poll-Interval"))
	if err != nil || s <= 0 {
		return DefaultPollInterval
	}
	return time.Duration(s) * time.Second
}
