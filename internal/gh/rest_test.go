package gh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tdragon/argus/internal/model"
)

// restReply is the response of the fake REST transport.
type restReply struct {
	status int
	header http.Header
	body   string
}

// restTransport serves REST replies and records each request.
type restTransport struct {
	handle func(r *http.Request) restReply
	mu     sync.Mutex
	reqs   []*http.Request
}

func (f *restTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()

	rep := f.handle(r)
	h := rep.header
	if h == nil {
		h = http.Header{}
	}
	if rep.body != "" && h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json; charset=utf-8")
	}
	return &http.Response{
		StatusCode: rep.status,
		Status:     http.StatusText(rep.status),
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(rep.body)),
		Request:    r,
	}, nil
}

func (f *restTransport) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.reqs...)
}

func newRESTTestClient(t *testing.T, handle func(r *http.Request) restReply) (*Client, *restTransport) {
	t.Helper()
	ft := &restTransport{handle: handle}
	c, err := New(api.ClientOptions{Host: "github.com", AuthToken: "test-token", Transport: ft})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, ft
}

func TestRerun(t *testing.T) {
	repo := model.Repo{Owner: "acme", Name: "api"}
	tests := []struct {
		name string
		call func(c *Client) error
		path string
	}{
		{"failed jobs", func(c *Client) error { return c.RerunFailedJobs(context.Background(), repo, 1001) },
			"/repos/acme/api/actions/runs/1001/rerun-failed-jobs"},
		{"all jobs", func(c *Client) error { return c.RerunWorkflow(context.Background(), repo, 1001) },
			"/repos/acme/api/actions/runs/1001/rerun"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ft := newRESTTestClient(t, func(*http.Request) restReply {
				return restReply{status: http.StatusCreated, body: "{}"}
			})
			if err := tt.call(c); err != nil {
				t.Fatalf("rerun: %v", err)
			}
			reqs := ft.requests()
			if len(reqs) != 1 {
				t.Fatalf("requests = %d, want 1", len(reqs))
			}
			if reqs[0].Method != http.MethodPost || reqs[0].URL.Path != tt.path || reqs[0].URL.Host != "api.github.com" {
				t.Errorf("request = %s %s", reqs[0].Method, reqs[0].URL)
			}
		})
	}
}

func TestRerunError(t *testing.T) {
	c, _ := newRESTTestClient(t, func(*http.Request) restReply {
		return restReply{status: http.StatusForbidden, body: `{"message": "This workflow run is not rerunnable"}`}
	})
	err := c.RerunFailedJobs(context.Background(), model.Repo{Owner: "acme", Name: "api"}, 7)
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v, want HTTP 403", err)
	}
	if httpErr.Message != "This workflow run is not rerunnable" {
		t.Errorf("message = %q", httpErr.Message)
	}
}

const notificationsBody = `[
  {
    "id": "1",
    "reason": "review_requested",
    "unread": true,
    "updated_at": "2026-11-16T10:00:00Z",
    "subject": {"title": "Add retry", "type": "PullRequest", "url": "https://api.github.com/repos/acme/api/pulls/42"},
    "repository": {"full_name": "acme/api"}
  },
  {
    "id": "2",
    "reason": "ci_activity",
    "unread": false,
    "updated_at": "2026-11-15T09:30:00Z",
    "subject": {"title": "CI failed", "type": "CheckSuite", "url": ""},
    "repository": {"full_name": "dave/tools"}
  }
]`

func TestNotificationsChanged(t *testing.T) {
	c, ft := newRESTTestClient(t, func(*http.Request) restReply {
		return restReply{
			status: http.StatusOK,
			header: http.Header{
				"Last-Modified":   []string{"Mon, 16 Nov 2026 10:00:00 GMT"},
				"X-Poll-Interval": []string{"120"},
			},
			body: notificationsBody,
		}
	})

	res, err := c.Notifications(context.Background(), "")
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	req := ft.requests()[0]
	if req.Method != http.MethodGet || req.URL.Path != "/notifications" || req.URL.Query().Get("per_page") != "50" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if h := req.Header.Get("If-Modified-Since"); h != "" {
		t.Errorf("If-Modified-Since = %q, want none on the first request", h)
	}

	want := NotificationsResult{
		Changed:      true,
		LastModified: "Mon, 16 Nov 2026 10:00:00 GMT",
		PollInterval: 120 * time.Second,
		Threads: []Notification{
			{ID: "1", Reason: "review_requested", Unread: true, UpdatedAt: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
				Title: "Add retry", Type: "PullRequest", URL: "https://api.github.com/repos/acme/api/pulls/42",
				Repo: model.Repo{Owner: "acme", Name: "api"}},
			{ID: "2", Reason: "ci_activity", UpdatedAt: time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC),
				Title: "CI failed", Type: "CheckSuite", Repo: model.Repo{Owner: "dave", Name: "tools"}},
		},
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
}

func TestNotificationsNotModified(t *testing.T) {
	const since = "Mon, 16 Nov 2026 10:00:00 GMT"
	c, ft := newRESTTestClient(t, func(*http.Request) restReply {
		return restReply{status: http.StatusNotModified, header: http.Header{"X-Poll-Interval": []string{"90"}}}
	})

	res, err := c.Notifications(context.Background(), since)
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	want := NotificationsResult{LastModified: since, PollInterval: 90 * time.Second}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if h := ft.requests()[0].Header.Get("If-Modified-Since"); h != since {
		t.Errorf("If-Modified-Since = %q, want %q", h, since)
	}
}

func TestNotificationsDefaultPollInterval(t *testing.T) {
	c, _ := newRESTTestClient(t, func(*http.Request) restReply {
		return restReply{status: http.StatusOK, header: http.Header{"X-Poll-Interval": []string{"bad"}}, body: "[]"}
	})
	res, err := c.Notifications(context.Background(), "")
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	if !res.Changed || res.PollInterval != DefaultPollInterval || res.Threads != nil {
		t.Errorf("result = %+v", res)
	}
}

func TestNotificationsHTTPError(t *testing.T) {
	c, _ := newRESTTestClient(t, func(*http.Request) restReply {
		return restReply{status: http.StatusUnauthorized, body: `{"message": "Bad credentials"}`}
	})
	_, err := c.Notifications(context.Background(), "Mon, 16 Nov 2026 10:00:00 GMT")
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("err = %v, want HTTP 401", err)
	}
}

func TestHeaderTransportOnlyForThatRequest(t *testing.T) {
	c, ft := newRESTTestClient(t, func(*http.Request) restReply {
		return restReply{status: http.StatusOK, body: "[]"}
	})
	if _, err := c.Notifications(context.Background(), "Mon, 16 Nov 2026 10:00:00 GMT"); err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	if _, err := c.Notifications(context.Background(), ""); err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	reqs := ft.requests()
	if reqs[0].Header.Get("If-Modified-Since") == "" || reqs[1].Header.Get("If-Modified-Since") != "" {
		t.Errorf("If-Modified-Since = %q, %q", reqs[0].Header.Get("If-Modified-Since"), reqs[1].Header.Get("If-Modified-Since"))
	}
	if reqs[0].Header.Get("Authorization") == "" {
		t.Error("Authorization header missing")
	}
}
