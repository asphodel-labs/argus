package model

import "testing"

const me = "alice"

func thread(resolved bool, authors ...string) Thread {
	t := Thread{IsResolved: resolved}
	for _, a := range authors {
		t.Comments = append(t.Comments, Comment{Author: Actor{Login: a}})
	}
	return t
}

func TestDeriveStatus(t *testing.T) {
	tests := []struct {
		name string
		pr   PR
		want Status
	}{
		// One rule matches.
		{"ci failure", PR{Rollup: CheckFailure}, StatusCIFailed},
		{"ci error", PR{Rollup: CheckError}, StatusCIFailed},
		{"changes requested", PR{ReviewDecision: DecisionChangesRequested, Rollup: CheckSuccess}, StatusChangesRequested},
		{"needs reply", PR{Threads: []Thread{thread(false, me, "bob")}}, StatusNeedsReply},
		{"needs reply login case", PR{Threads: []Thread{thread(false, "Bob")}}, StatusNeedsReply},
		{"draft", PR{IsDraft: true}, StatusDraft},
		{"conflict", PR{Mergeable: MergeableConflicting}, StatusConflict},
		{"pending no decision ci pending", PR{Rollup: CheckPending}, StatusPendingReview},
		{"pending no decision ci expected", PR{Rollup: CheckExpected}, StatusPendingReview},
		{"pending no decision ci green", PR{Rollup: CheckSuccess}, StatusPendingReview},
		{"pending review required", PR{ReviewDecision: DecisionReviewRequired, Rollup: CheckSuccess}, StatusPendingReview},
		{"approved ci green", PR{ReviewDecision: DecisionApproved, Rollup: CheckSuccess}, StatusApproved},
		{"approved no checks", PR{ReviewDecision: DecisionApproved}, StatusApproved},

		// No rule matches: fall back to pending review.
		{"approved ci pending", PR{ReviewDecision: DecisionApproved, Rollup: CheckPending}, StatusPendingReview},

		// Threads that do not need a reply.
		{"resolved thread from other", PR{Threads: []Thread{thread(true, "bob")}}, StatusPendingReview},
		{"last comment mine", PR{Threads: []Thread{thread(false, "bob", me)}}, StatusPendingReview},
		{"last comment mine other case", PR{Threads: []Thread{thread(false, "bob", "ALICE")}}, StatusPendingReview},
		{"thread with no comments", PR{Threads: []Thread{{}}}, StatusPendingReview},

		// Several rules match: the first rule in the table wins.
		{"ci failed over all", PR{
			Rollup:         CheckFailure,
			ReviewDecision: DecisionChangesRequested,
			Threads:        []Thread{thread(false, "bob")},
			IsDraft:        true,
			Mergeable:      MergeableConflicting,
		}, StatusCIFailed},
		{"changes requested over needs reply", PR{
			ReviewDecision: DecisionChangesRequested,
			Threads:        []Thread{thread(false, "bob")},
			IsDraft:        true,
		}, StatusChangesRequested},
		{"needs reply over draft", PR{
			Threads:   []Thread{thread(false, "bob")},
			IsDraft:   true,
			Mergeable: MergeableConflicting,
		}, StatusNeedsReply},
		{"draft over conflict", PR{IsDraft: true, Mergeable: MergeableConflicting}, StatusDraft},
		{"conflict over approved", PR{
			Mergeable:      MergeableConflicting,
			ReviewDecision: DecisionApproved,
			Rollup:         CheckSuccess,
		}, StatusConflict},
		{"conflict over pending", PR{Mergeable: MergeableConflicting, Rollup: CheckPending}, StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveStatus(tt.pr, me); got != tt.want {
				t.Errorf("DeriveStatus = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusIconAndString(t *testing.T) {
	tests := []struct {
		s    Status
		icon string
		name string
	}{
		{StatusCIFailed, "✗", "CI failed"},
		{StatusChangesRequested, "✎", "Changes requested"},
		{StatusNeedsReply, "💬", "Needs reply"},
		{StatusDraft, "◌", "Draft"},
		{StatusConflict, "⊘", "Conflict"},
		{StatusPendingReview, "⏳", "Pending review"},
		{StatusApproved, "✓", "Approved"},
	}
	for _, tt := range tests {
		if got := tt.s.Icon(); got != tt.icon {
			t.Errorf("%v.Icon() = %q, want %q", tt.s, got, tt.icon)
		}
		if got := tt.s.String(); got != tt.name {
			t.Errorf("String() = %q, want %q", got, tt.name)
		}
	}
}

func TestIsBot(t *testing.T) {
	bots := []string{"renovate-bot"}
	tests := []struct {
		name   string
		author Actor
		bots   []string
		want   bool
	}{
		{"bot typename", Actor{Login: "dependabot", Typename: TypenameBot}, nil, true},
		{"config login", Actor{Login: "renovate-bot", Typename: TypenameUser}, bots, true},
		{"config login other case", Actor{Login: "Renovate-Bot", Typename: TypenameUser}, bots, true},
		{"normal user", Actor{Login: "bob", Typename: TypenameUser}, bots, false},
		{"normal user no config", Actor{Login: "bob", Typename: TypenameUser}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBot(tt.author, tt.bots); got != tt.want {
				t.Errorf("IsBot = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRepoFullName(t *testing.T) {
	if got := (Repo{Owner: "acme", Name: "api"}).FullName(); got != "acme/api" {
		t.Errorf("FullName = %q", got)
	}
}

func TestWorkflowRunFailed(t *testing.T) {
	tests := []struct {
		conclusion string
		want       bool
	}{
		{"failure", true},
		{"timed_out", true},
		{"startup_failure", true},
		{"success", false},
		{"cancelled", false},
		{"skipped", false},
		{"neutral", false},
		{"action_required", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.conclusion, func(t *testing.T) {
			if got := (WorkflowRun{Conclusion: tt.conclusion}).Failed(); got != tt.want {
				t.Errorf("Failed() = %v, want %v", got, tt.want)
			}
		})
	}
}
