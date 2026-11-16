// Package model holds the pull request types and the derived status.
package model

import "time"

// Typename values of a GitHub actor.
const (
	TypenameUser = "User"
	TypenameBot  = "Bot"
)

// Actor is the author of a PR, a review, or a comment.
type Actor struct {
	Login    string
	Typename string
}

// CheckState is the state of the status check rollup of the head commit.
// The empty value means that the head commit has no checks.
type CheckState string

// Rollup states from the GitHub StatusState enum.
const (
	CheckNone     CheckState = ""
	CheckSuccess  CheckState = "SUCCESS"
	CheckPending  CheckState = "PENDING"
	CheckExpected CheckState = "EXPECTED"
	CheckFailure  CheckState = "FAILURE"
	CheckError    CheckState = "ERROR"
)

// ReviewDecision is the review decision of a PR.
// The empty value means that the repository requires no review.
type ReviewDecision string

// Review decisions from the GitHub PullRequestReviewDecision enum.
const (
	DecisionNone             ReviewDecision = ""
	DecisionReviewRequired   ReviewDecision = "REVIEW_REQUIRED"
	DecisionApproved         ReviewDecision = "APPROVED"
	DecisionChangesRequested ReviewDecision = "CHANGES_REQUESTED"
)

// Mergeable is the mergeable state of a PR.
type Mergeable string

// Mergeable states from the GitHub MergeableState enum.
const (
	MergeableUnknown     Mergeable = "UNKNOWN"
	MergeableYes         Mergeable = "MERGEABLE"
	MergeableConflicting Mergeable = "CONFLICTING"
)

// MergeStateStatus is the merge state of a PR from the GitHub
// MergeStateStatus enum, for example CLEAN, BLOCKED, or BEHIND.
type MergeStateStatus string

// MergeMethod is a merge method from the GitHub PullRequestMergeMethod enum.
type MergeMethod string

// Merge methods.
const (
	MergeMethodMerge  MergeMethod = "MERGE"
	MergeMethodSquash MergeMethod = "SQUASH"
	MergeMethodRebase MergeMethod = "REBASE"
)

// Repo identifies a repository.
type Repo struct {
	Owner string
	Name  string
}

// FullName returns "owner/name".
func (r Repo) FullName() string {
	return r.Owner + "/" + r.Name
}

// PR is a pull request. A search fills the summary fields.
// The detail query also fills Body, Checks, Reviews, Comments, and the
// merge fields.
type PR struct {
	ID             string
	Repo           Repo
	Number         int
	Title          string
	URL            string
	Author         Actor
	IsDraft        bool
	Mergeable      Mergeable
	ReviewDecision ReviewDecision
	Rollup         CheckState
	HeadSHA        string
	BaseRef        string
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// Threads holds the review threads. A summary can hold only the last
	// comment of each thread.
	Threads []Thread

	// Activity counts and the author of the newest comment, review, or
	// thread comment. The event diff compares them between snapshots.
	CommentCount int
	ReviewCount  int
	ThreadCount  int
	NewestAuthor Actor

	Body     string
	Checks   []Check
	Reviews  []Review
	Comments []Comment

	// MergeStateStatus, MergeMethods, and DeleteBranchOnMerge come from the
	// detail query. MergeMethods holds the methods that the repository allows.
	MergeStateStatus    MergeStateStatus
	MergeMethods        []MergeMethod
	DeleteBranchOnMerge bool
}

// Check is one check run or status context on the head commit.
// Status holds a CheckStatusState value, for example COMPLETED. Conclusion
// holds a CheckConclusionState value after the check completes. A status
// context maps to PENDING, or to COMPLETED with its state as Conclusion.
type Check struct {
	Name       string
	Status     string
	Conclusion string
	// App is the app slug of a check run, or the creator login of a status
	// context.
	App string
	URL string
	// WorkflowRunID is the GitHub Actions run id. It is 0 for other checks.
	WorkflowRunID int64
	// Rerunnable is true only for a check run of GitHub Actions.
	Rerunnable bool
}

// Review is a submitted or pending review.
type Review struct {
	ID          string
	Author      Actor
	State       string
	Body        string
	SubmittedAt time.Time
}

// Thread is a review thread on a diff line or a file.
type Thread struct {
	ID         string
	Path       string
	Line       int
	IsResolved bool
	Comments   []Comment
}

// LastComment returns the newest comment of the thread, or nil if the
// thread has no comments.
func (t Thread) LastComment() *Comment {
	if len(t.Comments) == 0 {
		return nil
	}
	return &t.Comments[len(t.Comments)-1]
}

// Comment is a PR comment or a review thread comment.
type Comment struct {
	ID        string
	Author    Actor
	Body      string
	CreatedAt time.Time
	Reactions []Reaction
}

// Reaction is the count of one reaction content on a comment.
type Reaction struct {
	Content          string
	Count            int
	ViewerHasReacted bool
}
