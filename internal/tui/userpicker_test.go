package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/asphodel-labs/argus/internal/model"
)

func user(id, login, name string) model.Actor {
	return model.Actor{ID: id, Login: login, Name: name, Typename: model.TypenameUser}
}

// pickerPR is the action PR of bob with a requested user, a team, and a
// bot, and with dave as the assignee.
func pickerPR() model.PR {
	p := actionPR()
	p.RequestedReviewers = []model.Actor{
		user("U_carol", "carol", "Carol King"),
		{ID: "T_core", Login: "acme/core", Name: "Core", Typename: model.TypenameTeam},
		{ID: "BOT_cp", Login: "copilot", Typename: model.TypenameBot},
	}
	p.Assignees = []model.Actor{user("U_dave", "dave", "")}
	return p
}

// newPickerEnv returns an action model for pr. GitHub has four assignable
// users, and it suggests frank and bob, the author.
func newPickerEnv(t *testing.T, pr model.PR) *actionEnv {
	t.Helper()
	env := newActionModel(t, pr)
	env.gh.users = []model.Actor{
		user("U_alice", "alice", "Alice Smith"), user("U_bob", "bob", ""),
		user("U_erin", "erin", "Erin Ng"), user("U_dave", "dave", ""),
	}
	env.gh.total = 4
	env.gh.suggested = []model.Actor{user("U_frank", "frank", "Frank O"), user("U_bob", "bob", "")}
	return env
}

func (e *actionEnv) picker(t *testing.T) *userPicker {
	t.Helper()
	p, ok := e.m.topDialog().(*userPicker)
	if !ok {
		t.Fatalf("top dialog is %T, want a user picker", e.m.topDialog())
	}
	return p
}

// wantOrder checks that the texts are in the dialog in order.
func wantOrder(t *testing.T, text string, parts ...string) {
	t.Helper()
	at := -1
	for _, p := range parts {
		i := strings.Index(text, p)
		if i < 0 || i < at {
			t.Fatalf("dialog has no %q after position %d:\n%s", p, at, text)
		}
		at = i
	}
}

func TestRequestReviewPickerRows(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("R")
	text := env.dialogText()
	wantOrder(t, text,
		"Request review on acme/api#7: Add retry",
		"› [ ] frank Frank O", "[x] carol Carol King", "[ ] alice Alice Smith", "[ ] erin Erin Ng", "[ ] dave",
		"Teams kept: acme/core", "Bots kept: copilot")
	if strings.Contains(text, "bob") {
		t.Fatalf("picker offers the author bob:\n%s", text)
	}
	if strings.Contains(text, "Loading users") || strings.Contains(text, "type to search GitHub") {
		t.Fatalf("picker is still loading or offers a search:\n%s", text)
	}
	wantCalls(t, env.gh.Calls(), "SuggestedReviewers PR_1", "AssignableUsers acme/api ")
}

func TestRequestReviewFilterToggleSend(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("R")
	env.typeText("ER")
	text := env.dialogText()
	if !strings.Contains(text, "/ ER") || !strings.Contains(text, "› [ ] erin") || strings.Contains(text, "alice") || strings.Contains(text, "carol") {
		t.Fatalf("filtered picker:\n%s", text)
	}
	env.press("space")
	if !strings.Contains(env.dialogText(), "› [x] erin") {
		t.Fatalf("space did not check erin:\n%s", env.dialogText())
	}
	if !env.picker(t).Unsent() {
		t.Fatal("Unsent = false after a change")
	}
	env.press("enter")
	wantCalls(t, env.gh.Calls(), "SuggestedReviewers PR_1", "AssignableUsers acme/api ",
		"RequestReviews PR_1 [U_carol U_erin] [T_core] [BOT_cp]")
	if env.m.Dialogs() != 0 {
		t.Fatal("picker open after the send")
	}
	if msg := env.m.Status().Message(); msg != "request review from erin on acme/api#7: done" {
		t.Fatalf("message %q", msg)
	}
	if env.det.callCount() != 2 {
		t.Fatalf("detail calls = %d, want a refresh", env.det.callCount())
	}
}

func TestRequestReviewFilterByName(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("R")
	env.typeText("smith")
	text := env.dialogText()
	if !strings.Contains(text, "› [ ] alice Alice Smith") || strings.Contains(text, "erin") {
		t.Fatalf("filtered picker:\n%s", text)
	}
	env.press("backspace", "backspace", "backspace", "backspace", "backspace")
	if !strings.Contains(env.dialogText(), "erin") {
		t.Fatalf("backspace did not clear the filter:\n%s", env.dialogText())
	}
}

func TestRequestReviewRemoveKeepsTeams(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("R", "down", "space", "enter")
	wantCalls(t, env.gh.Calls(), "SuggestedReviewers PR_1", "AssignableUsers acme/api ",
		"RequestReviews PR_1 [] [T_core] [BOT_cp]")
	if msg := env.m.Status().Message(); msg != "update reviewers of acme/api#7: done" {
		t.Fatalf("message %q", msg)
	}
}

func TestPickerEnterWithNoChange(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("R", "space", "space", "enter")
	wantCalls(t, env.gh.Calls(), "SuggestedReviewers PR_1", "AssignableUsers acme/api ")
	if env.m.Dialogs() != 0 {
		t.Fatal("picker open after enter")
	}
	if msg := env.m.Status().Message(); msg != "no change" {
		t.Fatalf("message %q", msg)
	}
}

func TestPickerUnsentAsksBeforeQuit(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("R")
	if env.picker(t).Unsent() {
		t.Fatal("Unsent = true with no change")
	}
	env.press("space", "ctrl+c")
	if _, ok := env.m.topDialog().(*confirmQuitDialog); !ok {
		t.Fatalf("top dialog is %T, want the quit confirmation", env.m.topDialog())
	}
}

func TestRequestReviewBlockedOnClosedPR(t *testing.T) {
	for _, state := range []string{"CLOSED", "MERGED"} {
		t.Run(state, func(t *testing.T) {
			pr := pickerPR()
			pr.State = state
			env := newPickerEnv(t, pr)
			env.press("R")
			if env.m.Dialogs() != 0 {
				t.Fatal("picker opened on a closed PR")
			}
			if msg := env.m.Status().Message(); msg != "cannot request a review: acme/api#7 is not open" {
				t.Fatalf("message %q", msg)
			}
			wantCalls(t, env.gh.Calls())
		})
	}
}

func TestAssignPicker(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("@")
	text := env.dialogText()
	// The author bob is a candidate for an assignee. There are no
	// suggested reviewers and no kept teams.
	wantOrder(t, text, "Assign acme/api#7: Add retry", "› [x] dave", "[ ] alice", "[ ] bob", "[ ] erin")
	if strings.Contains(text, "frank") || strings.Contains(text, "kept") {
		t.Fatalf("assign picker:\n%s", text)
	}
	env.press("space", "down", "space", "enter")
	wantCalls(t, env.gh.Calls(), "AssignableUsers acme/api ", "AddAssignees PR_1 [U_alice]", "RemoveAssignees PR_1 [U_dave]")
	if msg := env.m.Status().Message(); msg != "update assignees of acme/api#7: done" {
		t.Fatalf("message %q", msg)
	}
}

func TestAssignAddOnlyAndRemoveOnly(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.press("@", "down", "space", "enter")
	if msg := env.m.Status().Message(); msg != "assign alice on acme/api#7: done" {
		t.Fatalf("message %q", msg)
	}
	env.press("@", "space", "enter")
	if msg := env.m.Status().Message(); msg != "unassign dave on acme/api#7: done" {
		t.Fatalf("message %q", msg)
	}
	wantCalls(t, env.gh.Calls(),
		"AssignableUsers acme/api ", "AddAssignees PR_1 [U_alice]",
		"AssignableUsers acme/api ", "RemoveAssignees PR_1 [U_dave]")
}

func TestAssignErrorKeepsPicker(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.gh.setErr("AddAssignees", errors.New("forbidden"))
	env.press("@", "space", "down", "space", "enter")
	if env.m.Dialogs() != 1 || !strings.Contains(env.dialogText(), "Error: forbidden") {
		t.Fatalf("picker after error:\n%s", env.dialogText())
	}
	// The failed add stops the remove.
	wantCalls(t, env.gh.Calls(), "AssignableUsers acme/api ", "AddAssignees PR_1 [U_alice]")
	if !strings.Contains(env.dialogText(), "› [x] alice") {
		t.Fatalf("picker lost the selection:\n%s", env.dialogText())
	}
}

func TestAssignAllowedOnClosedPR(t *testing.T) {
	pr := pickerPR()
	pr.State = "CLOSED"
	env := newPickerEnv(t, pr)
	env.press("@")
	env.picker(t)
}

func TestPickerLoadingAndStaleLoad(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	env.gh.gate = gate
	env.press("R")
	p := env.picker(t)
	text := env.dialogText()
	if !strings.Contains(text, "Loading users…") || !strings.Contains(text, "[x] carol") {
		t.Fatalf("picker while it loads:\n%s", text)
	}
	env.press("esc")
	if env.m.Dialogs() != 0 {
		t.Fatal("esc did not close the picker")
	}
	// The load result of the closed picker is dropped.
	env.m.Update(userPickerLoadedMsg{p: p, users: []model.Actor{user("U_zed", "zed", "")}, total: 1})
	if !p.loading || len(p.users) != 1 {
		t.Fatalf("closed picker took the load: loading %v, users %+v", p.loading, p.users)
	}
}

func TestPickerServerSearch(t *testing.T) {
	env := newPickerEnv(t, pickerPR())
	env.gh.total = 300
	env.gh.found = map[string][]model.Actor{"zo": {user("U_zoe", "zoe", "Zoe Lee")}}
	env.press("R")
	if !strings.Contains(env.dialogText(), "4 of 300 users loaded; type to search GitHub.") {
		t.Fatalf("picker:\n%s", env.dialogText())
	}
	env.typeText("Zo")
	if !strings.Contains(env.dialogText(), "› [ ] zoe Zoe Lee") {
		t.Fatalf("picker after the search:\n%s", env.dialogText())
	}
	// A result for a query that is not the filter now is dropped.
	p := env.picker(t)
	env.m.Update(userPickerLoadedMsg{p: p, query: "z", users: []model.Actor{user("U_zack", "zack", "")}, total: 1})
	if strings.Contains(env.dialogText(), "zack") {
		t.Fatalf("picker took a stale search:\n%s", env.dialogText())
	}
	env.press("space", "enter")
	wantCalls(t, env.gh.Calls(), "SuggestedReviewers PR_1", "AssignableUsers acme/api ",
		"AssignableUsers acme/api z", "AssignableUsers acme/api zo",
		"RequestReviews PR_1 [U_carol U_zoe] [T_core] [BOT_cp]")
}
