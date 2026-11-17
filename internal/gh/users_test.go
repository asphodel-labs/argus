package gh

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tdragon/argus/internal/model"
)

func TestAssignableUsers(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "assignable_users.json") })

	users, total, err := c.AssignableUsers(context.Background(), "acme", "api", "")
	if err != nil {
		t.Fatalf("AssignableUsers: %v", err)
	}
	want := []model.Actor{
		{ID: "U_alice", Login: "alice", Name: "Alice Smith", Typename: "User"},
		{ID: "U_bob", Login: "bob", Typename: "User"},
	}
	if total != 240 || !reflect.DeepEqual(users, want) {
		t.Errorf("users = %d %+v, want 240 %+v", total, users, want)
	}
	req := ft.requests()[0]
	if !strings.Contains(req.Query, "assignableUsers(first: 100, query: $q)") {
		t.Errorf("query = %q", req.Query)
	}
	wantVars := map[string]any{"owner": "acme", "repo": "api"}
	if !reflect.DeepEqual(req.Variables, wantVars) {
		t.Errorf("variables = %v, want %v (no q)", req.Variables, wantVars)
	}
}

func TestAssignableUsersQuery(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "assignable_users.json") })

	if _, _, err := c.AssignableUsers(context.Background(), "acme", "api", "ali"); err != nil {
		t.Fatalf("AssignableUsers: %v", err)
	}
	if q := ft.requests()[0].Variables["q"]; q != "ali" {
		t.Errorf("q = %v, want ali", q)
	}
}

func TestAssignableUsersNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return fixture(t, "detail_repo_not_found.json") })

	if _, _, err := c.AssignableUsers(context.Background(), "acme", "gone", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSuggestedReviewers(t *testing.T) {
	c, ft := newTestClient(t, func(gqlRequest) reply { return fixture(t, "suggested_reviewers.json") })

	got, err := c.SuggestedReviewers(context.Background(), "PR_1")
	if err != nil {
		t.Fatalf("SuggestedReviewers: %v", err)
	}
	want := []model.Actor{
		{ID: "U_carol", Login: "carol", Name: "Carol King", Typename: "User"},
		{ID: "U_bob", Login: "bob", Typename: "User"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("suggested = %+v, want %+v", got, want)
	}
	if id := ft.requests()[0].Variables["id"]; id != "PR_1" {
		t.Errorf("id = %v, want PR_1", id)
	}
}

func TestSuggestedReviewersNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(gqlRequest) reply { return okReply(`{"node": null}`) })

	if _, err := c.SuggestedReviewers(context.Background(), "PR_x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
