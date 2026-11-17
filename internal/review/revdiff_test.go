package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/asphodel-labs/argus/internal/gh"
	"github.com/asphodel-labs/argus/internal/model"
)

// fakeReviewAPI records each call and returns the error of its step. It
// keeps one pending review and the submitted reviews, like GitHub.
type fakeReviewAPI struct {
	calls   []string
	pending gh.PendingReview
	has     bool
	errs    map[string]error
	// failThread makes AddReviewThread fail for the thread at this path.
	failThread string
	// lostReply makes SubmitReview submit the review and then return an
	// error, like a reply that does not come back.
	lostReply bool
	// submitted are the submitted reviews by id.
	submitted map[string]gh.ReviewStatus
	started   int
}

func (f *fakeReviewAPI) PendingReview(_ context.Context, prID string) (gh.PendingReview, bool, error) {
	f.calls = append(f.calls, "PendingReview "+prID)
	if err := f.errs["PendingReview"]; err != nil {
		return gh.PendingReview{}, false, err
	}
	return f.pending, f.has, nil
}

func (f *fakeReviewAPI) Review(_ context.Context, reviewID string) (gh.ReviewStatus, bool, error) {
	f.calls = append(f.calls, "Review "+reviewID)
	if err := f.errs["Review"]; err != nil {
		return gh.ReviewStatus{}, false, err
	}
	if st, ok := f.submitted[reviewID]; ok {
		return st, true, nil
	}
	if f.has && f.pending.ID == reviewID {
		return gh.ReviewStatus{State: "PENDING", CommitOID: f.pending.CommitOID, Comments: f.pending.Comments}, true, nil
	}
	return gh.ReviewStatus{}, false, nil
}

func (f *fakeReviewAPI) StartReview(_ context.Context, prID, commitOID string) (string, error) {
	f.calls = append(f.calls, "StartReview "+prID+" "+commitOID)
	if err := f.errs["StartReview"]; err != nil {
		return "", err
	}
	f.started++
	id := "PRR_new"
	if f.started > 1 {
		id = fmt.Sprintf("PRR_new%d", f.started)
	}
	f.pending, f.has = gh.PendingReview{ID: id, CommitOID: commitOID}, true
	return id, nil
}

func (f *fakeReviewAPI) AddReviewThread(_ context.Context, reviewID string, t gh.ReviewThreadInput) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("AddReviewThread %s %s:%d", reviewID, t.Path, t.Line))
	if t.Path == f.failThread {
		return "", errors.New("thread failed")
	}
	id := fmt.Sprintf("C_%d", len(f.calls))
	f.pending.Comments = append(f.pending.Comments, gh.PendingComment{ID: id, Path: t.Path, Body: t.Body, Line: t.Line, StartLine: t.StartLine, File: t.File})
	return id, nil
}

func (f *fakeReviewAPI) DeleteReviewComment(_ context.Context, commentID string) error {
	f.calls = append(f.calls, "DeleteReviewComment "+commentID)
	if err := f.errs["DeleteReviewComment"]; err != nil {
		return err
	}
	f.pending.Comments = slices.DeleteFunc(f.pending.Comments, func(c gh.PendingComment) bool { return c.ID == commentID })
	return nil
}

func (f *fakeReviewAPI) SubmitReview(_ context.Context, reviewID string, event gh.ReviewEvent, body string) error {
	f.calls = append(f.calls, fmt.Sprintf("SubmitReview %s %s %q", reviewID, event, body))
	if err := f.errs["SubmitReview"]; err != nil {
		return err
	}
	if f.submitted == nil {
		f.submitted = map[string]gh.ReviewStatus{}
	}
	states := map[gh.ReviewEvent]string{gh.ReviewComment: "COMMENTED", gh.ReviewApprove: "APPROVED", gh.ReviewRequestChanges: "CHANGES_REQUESTED"}
	f.submitted[reviewID] = gh.ReviewStatus{
		State: states[event], CommitOID: f.pending.CommitOID, Body: body, Comments: f.pending.Comments,
		URL: "https://github.com/acme/api/pull/7#pullrequestreview-" + reviewID,
	}
	f.pending, f.has = gh.PendingReview{}, false
	if f.lostReply {
		return errors.New("submit review: context deadline exceeded")
	}
	return nil
}

// threadsOf returns "path:line body" for each comment.
func threadsOf(comments []gh.PendingComment) []string {
	var out []string
	for _, c := range comments {
		out = append(out, fmt.Sprintf("%s:%d %s", c.Path, c.Line, c.Body))
	}
	return out
}

// revdiffEnv returns a Revdiff with a fake runner, a clone at
// <root>/acme/api, and a state directory.
func revdiffEnv(t *testing.T) (*Revdiff, *fakeRunner, *fakeReviewAPI, string) {
	t.Helper()
	w, r, clone, _ := setupEnv(t)
	api := &fakeReviewAPI{}
	rd := &Revdiff{
		Worktrees: w, GitHub: api, StateDir: filepath.Join(t.TempDir(), "state"),
		SessionID: "S1", Bin: "/opt/bin/revdiff",
	}
	mkdir(t, rd.StateDir)
	return rd, r, api, clone
}

// session returns a prepared session without the runner.
func testSession(rd *Revdiff) Session {
	pr := reviewPR()
	pr.ID = "PR_7"
	return Session{
		PR:             pr,
		Worktree:       Worktree{Dir: "/src/acme/api/.worktrees/argus-pr-7"},
		HeadSHA:        "abc123",
		MergeBase:      "mb1",
		BodyFile:       rd.BodyFile(pr),
		AnnotationFile: rd.AnnotationFile(pr),
		ReviewFile:     rd.ReviewFile(pr),
	}
}

func TestPrepare(t *testing.T) {
	rd, r, _, clone := revdiffEnv(t)
	wtDir := filepath.Join(clone, ".worktrees", "argus-pr-7")
	r.set("git -C "+wtDir+" merge-base origin/main abc123", reply{stdout: "mb1\n"})
	pr := reviewPR()
	pr.Body = "## Why\nbecause"
	s, err := rd.Prepare(context.Background(), pr)
	if err != nil {
		t.Fatal(err)
	}
	want := Session{
		PR: pr, Worktree: Worktree{Clone: clone, Dir: wtDir}, HeadSHA: "abc123", MergeBase: "mb1",
		BodyFile:       filepath.Join(rd.StateDir, "pr-acme+api+7.body.md"),
		AnnotationFile: filepath.Join(rd.StateDir, "pr-acme+api+7.md"),
		ReviewFile:     filepath.Join(rd.StateDir, "pr-acme+api+7.review.json"),
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("session:\n got %+v\nwant %+v", s, want)
	}
	got, err := os.ReadFile(s.BodyFile)
	if err != nil || string(got) != pr.Body {
		t.Fatalf("body file = %q, %v", got, err)
	}
	wantCalls(t, r.calls,
		fetchCall(clone),
		excludeCall(clone),
		"git -C "+clone+" worktree add --detach "+wtDir+" abc123",
		"git -C "+wtDir+" merge-base origin/main abc123",
	)
}

// TestStateFileNamesDiffer checks that two PRs with the same owner, repo,
// and number joined by hyphens get different state files.
func TestStateFileNamesDiffer(t *testing.T) {
	rd := &Revdiff{StateDir: "/state"}
	a := model.PR{Repo: model.Repo{Owner: "a-b", Name: "c"}, Number: 7}
	b := model.PR{Repo: model.Repo{Owner: "a", Name: "b-c"}, Number: 7}
	for _, f := range []func(model.PR) string{rd.AnnotationFile, rd.BodyFile, rd.ReviewFile} {
		if f(a) == f(b) {
			t.Fatalf("a-b/c#7 and a/b-c#7 share the file %s", f(a))
		}
	}
}

func TestPrepareErrors(t *testing.T) {
	t.Run("no agterm", func(t *testing.T) {
		rd, r, _, _ := revdiffEnv(t)
		rd.SessionID = ""
		if _, err := rd.Prepare(context.Background(), reviewPR()); !errors.Is(err, ErrNoAgterm) {
			t.Fatalf("err = %v, want ErrNoAgterm", err)
		}
		if len(r.calls) != 0 {
			t.Fatalf("calls %q", r.calls)
		}
	})
	t.Run("missing clone", func(t *testing.T) {
		rd, _, _, _ := revdiffEnv(t)
		rd.Worktrees.RepoRoot = t.TempDir()
		var miss *MissingClone
		if _, err := rd.Prepare(context.Background(), reviewPR()); !errors.As(err, &miss) {
			t.Fatalf("err = %v, want *MissingClone", err)
		}
	})
	t.Run("merge-base fails", func(t *testing.T) {
		rd, r, _, clone := revdiffEnv(t)
		r.set("git -C "+filepath.Join(clone, ".worktrees", "argus-pr-7")+" merge-base origin/main abc123", reply{code: 1})
		if _, err := rd.Prepare(context.Background(), reviewPR()); err == nil {
			t.Fatal("no error")
		}
		if _, err := os.Stat(rd.BodyFile(reviewPR())); err == nil {
			t.Fatal("body file written after a failure")
		}
	})
}

func TestOverlayCommand(t *testing.T) {
	rd, _, _, _ := revdiffEnv(t)
	s := testSession(rd)
	s.AnnotationFile, s.BodyFile = "/state/pr-acme-api-7.md", "/state/pr-acme-api-7.body.md"
	name, args := rd.OverlayCommand(s, false)
	wantCmd := "'/opt/bin/revdiff' '--vim-motion' '--exit-code-on-annotations' '-o' '/state/pr-acme-api-7.md'" +
		" '--description-file' '/state/pr-acme-api-7.body.md' 'mb1' 'abc123'"
	want := []string{"session", "overlay", "open", "--block", "--target", "S1",
		"--cwd", "/src/acme/api/.worktrees/argus-pr-7", wantCmd}
	if name != "agtermctl" || !slices.Equal(args, want) {
		t.Fatalf("command:\n got %s %q\nwant agtermctl %q", name, args, want)
	}

	_, args = rd.OverlayCommand(s, true)
	if got := args[len(args)-1]; !strings.Contains(got, " '--annotations' '/state/pr-acme-api-7.md' 'mb1' 'abc123'") {
		t.Fatalf("preload command = %s", got)
	}

	rd.Bin, rd.Agtermctl = "", "/usr/local/bin/agtermctl"
	name, args = rd.OverlayCommand(s, false)
	if name != "/usr/local/bin/agtermctl" || !strings.HasPrefix(args[len(args)-1], "'revdiff' ") {
		t.Fatalf("defaults: %s %q", name, args)
	}
}

// TestRevdiffCommandQuoting gives the command string to a real sh, as
// agterm does, and checks that sh gets the original arguments back.
func TestRevdiffCommandQuoting(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	rd := &Revdiff{Bin: "/Applications/My Tools/revdiff", StateDir: "/state"}
	s := Session{
		MergeBase:      "mb1",
		HeadSHA:        "abc123",
		AnnotationFile: `/Users/o'neil/state dir/pr-a-b-1.md`,
		BodyFile:       `/tmp/"quoted" $HOME ` + "`x`" + ` \ ;|&.body.md`,
	}
	cmd := rd.RevdiffCommand(s, true)
	out, err := exec.Command(sh, "-c", `printf '%s\n' `+cmd).Output()
	if err != nil {
		t.Fatalf("sh -c: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	want := []string{rd.Bin, "--vim-motion", "--exit-code-on-annotations", "-o", s.AnnotationFile,
		"--description-file", s.BodyFile, "--annotations", s.AnnotationFile, "mb1", "abc123"}
	if !slices.Equal(got, want) {
		t.Fatalf("sh args:\n got %q\nwant %q", got, want)
	}
}

const annotationsOut = `## a.go:3 (+)
inside

## a.go:40 (+)
outside

## b.go (file-level)
file note
`

const openDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 one
+two
 three
 four
`

func TestOpenExitCodes(t *testing.T) {
	diffLine := "git -C /src/acme/api/.worktrees/argus-pr-7 -c core.quotePath=false diff --no-color --no-ext-diff" +
		" --src-prefix=a/ --dst-prefix=b/ --find-renames --unified=3 mb1 abc123"
	tests := []struct {
		name    string
		code    int
		stderr  string
		write   string
		want    Result
		wantErr string
	}{
		{name: "exit 0", code: 0},
		{name: "exit 10", code: 10, write: annotationsOut, want: Result{
			Annotated: true,
			Threads: []gh.ReviewThreadInput{
				{Path: "a.go", Body: "inside", Line: 3, Side: gh.SideRight},
				{Path: "b.go", Body: "file note", File: true},
			},
			Moved: []gh.ReviewThreadInput{{Path: "a.go", Body: "outside", Line: 40, Side: gh.SideRight}},
		}},
		{name: "exit 10 empty file", code: 10, write: "\n"},
		{name: "exit 10 no file", code: 10, wantErr: "read annotations"},
		{name: "exit 10 bad file", code: 10, write: "## bad header\n", wantErr: "malformed record header"},
		{name: "other code", code: 1, stderr: "session not found", wantErr: "exited with code 1: session not found"},
		{name: "other code no stderr", code: 127, wantErr: "exited with code 127"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd, r, _, _ := revdiffEnv(t)
			s := testSession(rd)
			r.hook = func(name string, _ []string) {
				if name == "agtermctl" && tt.write != "" {
					writeFile(t, s.AnnotationFile, tt.write)
				}
			}
			name, args := rd.OverlayCommand(s, false)
			overlay := strings.Join(append([]string{name}, args...), " ")
			r.set(overlay, reply{code: tt.code, stderr: tt.stderr})
			r.set(diffLine, reply{stdout: openDiff})

			got, err := rd.Open(context.Background(), s)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("result:\n got %+v\nwant %+v", got, tt.want)
			}
			if r.calls[0] != overlay {
				t.Fatalf("first call %q, want %q", r.calls[0], overlay)
			}
		})
	}
}

func TestOpenPreloadsAnnotationFile(t *testing.T) {
	rd, r, _, _ := revdiffEnv(t)
	s := testSession(rd)
	writeFile(t, s.AnnotationFile, annotationsOut)
	if _, err := rd.Open(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.calls[0], "'--annotations' '"+s.AnnotationFile+"'") {
		t.Fatalf("no preload in %q", r.calls[0])
	}
}

func TestOpenRunError(t *testing.T) {
	rd, r, _, _ := revdiffEnv(t)
	r.hook = nil
	s := testSession(rd)
	name, args := rd.OverlayCommand(s, false)
	r.set(strings.Join(append([]string{name}, args...), " "), reply{code: -1, err: errors.New("not found")})
	if _, err := rd.Open(context.Background(), s); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenHunksError(t *testing.T) {
	rd, r, _, _ := revdiffEnv(t)
	s := testSession(rd)
	writeFile(t, s.AnnotationFile, annotationsOut)
	name, args := rd.OverlayCommand(s, true)
	r.set(strings.Join(append([]string{name}, args...), " "), reply{code: 10})
	r.hook = func(name string, _ []string) {
		if name == "git" {
			r.set(r.calls[len(r.calls)-1], reply{code: 128, stderr: "bad revision"})
		}
	}
	if _, err := rd.Open(context.Background(), s); err == nil || !strings.Contains(err.Error(), "bad revision") {
		t.Fatalf("err = %v", err)
	}
}

var submitThreads = []gh.ReviewThreadInput{
	{Path: "a.go", Body: "inside", Line: 3, Side: gh.SideRight},
	{Path: "b.go", Body: "file note", File: true},
}

// writeSessionFiles writes the annotation file and the body file.
func writeSessionFiles(t *testing.T, s Session) {
	t.Helper()
	writeFile(t, s.AnnotationFile, annotationsOut)
	writeFile(t, s.BodyFile, "body")
}

func filesExist(s Session) bool {
	_, errA := os.Stat(s.AnnotationFile)
	_, errB := os.Stat(s.BodyFile)
	return errA == nil && errB == nil
}

func TestSubmitNewReview(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	sub := Submission{
		Event: gh.ReviewRequestChanges, Body: "  overall  ", Threads: submitThreads,
		Moved: []gh.ReviewThreadInput{{Path: "a.go", Body: "outside", Line: 40, Side: gh.SideRight}},
	}
	note, err := rd.Submit(context.Background(), s, sub)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls(t, api.calls,
		"PendingReview PR_7",
		"StartReview PR_7 abc123",
		"AddReviewThread PRR_new a.go:3",
		"AddReviewThread PRR_new b.go:0",
		`SubmitReview PRR_new REQUEST_CHANGES "overall\n\na.go:40 — outside"`,
	)
	if note != "" {
		t.Fatalf("note = %q", note)
	}
	for _, p := range []string{s.AnnotationFile, s.BodyFile, s.ReviewFile} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s not removed after a success", p)
		}
	}
}

func TestSubmitReusesPendingReview(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	// A failed submit before added the first thread.
	api.has = true
	api.pending = gh.PendingReview{ID: "PRR_old", CommitOID: "abc123", Comments: []gh.PendingComment{
		{ID: "C_1", Path: "a.go", Body: "inside\r\n", Line: 3},
	}}
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads}); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, api.calls,
		"PendingReview PR_7",
		"AddReviewThread PRR_old b.go:0",
		`SubmitReview PRR_old COMMENT ""`,
	)
}

func TestSubmitPendingReviewOnOtherCommit(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	api.has = true
	api.pending = gh.PendingReview{ID: "PRR_old", CommitOID: "0ld0ld0ld"}
	_, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads})
	if err == nil || !strings.Contains(err.Error(), "pending review on commit 0ld0ld0") {
		t.Fatalf("err = %v", err)
	}
	if len(api.calls) != 1 || !filesExist(s) {
		t.Fatalf("calls %q, files kept %v", api.calls, filesExist(s))
	}
}

func TestSubmitFailureKeepsFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(api *fakeReviewAPI)
		calls int
		// record is true if the review file exists after the failure.
		record bool
	}{
		{"find pending review", func(api *fakeReviewAPI) {
			api.errs = map[string]error{"PendingReview": errors.New("boom")}
		}, 1, false},
		{"start review", func(api *fakeReviewAPI) {
			api.errs = map[string]error{"StartReview": errors.New("boom")}
		}, 2, false},
		{"add thread", func(api *fakeReviewAPI) { api.failThread = "b.go" }, 4, true},
		{"submit", func(api *fakeReviewAPI) {
			api.errs = map[string]error{"SubmitReview": errors.New("boom")}
		}, 5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd, _, api, _ := revdiffEnv(t)
			s := testSession(rd)
			writeSessionFiles(t, s)
			tt.setup(api)
			sub := Submission{Event: gh.ReviewComment, Threads: submitThreads}
			if _, err := rd.Submit(context.Background(), s, sub); err == nil {
				t.Fatal("no error")
			}
			if len(api.calls) != tt.calls {
				t.Fatalf("calls %q, want %d", api.calls, tt.calls)
			}
			if !filesExist(s) {
				t.Fatal("files removed after a failure")
			}
			if _, err := os.Stat(s.ReviewFile); (err == nil) != tt.record {
				t.Fatalf("review file exists %v, want %v", err == nil, tt.record)
			}

			// The retry finds the pending review and adds no thread twice.
			api.errs, api.failThread = nil, ""
			api.calls = nil
			if _, err := rd.Submit(context.Background(), s, sub); err != nil {
				t.Fatal(err)
			}
			if len(api.submitted) != 1 {
				t.Fatalf("%d reviews submitted", len(api.submitted))
			}
			got := threadsOf(api.submitted["PRR_new"].Comments)
			if want := []string{"a.go:3 inside", "b.go:0 file note"}; !slices.Equal(got, want) {
				t.Fatalf("submitted threads %q, want %q", got, want)
			}
			if filesExist(s) {
				t.Fatal("files kept after the retry")
			}
		})
	}
}

// failAtSecondThread runs a submit that adds the a.go thread and fails at
// the b.go thread.
func failAtSecondThread(t *testing.T, rd *Revdiff, api *fakeReviewAPI, s Session) {
	t.Helper()
	api.failThread = "b.go"
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads}); err == nil {
		t.Fatal("no error")
	}
	api.failThread, api.calls = "", nil
}

// TestSubmitRetryRemovesChangedThreads checks that a retry after the user
// edited or deleted an annotation removes the outdated pending comment.
func TestSubmitRetryRemovesChangedThreads(t *testing.T) {
	edited := gh.ReviewThreadInput{Path: "a.go", Body: "inside, edited", Line: 3, Side: gh.SideRight}
	tests := []struct {
		name      string
		threads   []gh.ReviewThreadInput
		calls     []string
		submitted []string
	}{
		{
			name:    "edited",
			threads: []gh.ReviewThreadInput{edited, submitThreads[1]},
			calls: []string{
				"PendingReview PR_7",
				"DeleteReviewComment C_3",
				"AddReviewThread PRR_new a.go:3",
				"AddReviewThread PRR_new b.go:0",
				`SubmitReview PRR_new COMMENT ""`,
			},
			submitted: []string{"a.go:3 inside, edited", "b.go:0 file note"},
		},
		{
			name:    "deleted",
			threads: []gh.ReviewThreadInput{submitThreads[1]},
			calls: []string{
				"PendingReview PR_7",
				"DeleteReviewComment C_3",
				"AddReviewThread PRR_new b.go:0",
				`SubmitReview PRR_new COMMENT ""`,
			},
			submitted: []string{"b.go:0 file note"},
		},
		{
			name:    "same",
			threads: submitThreads,
			calls: []string{
				"PendingReview PR_7",
				"AddReviewThread PRR_new b.go:0",
				`SubmitReview PRR_new COMMENT ""`,
			},
			submitted: []string{"a.go:3 inside", "b.go:0 file note"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd, _, api, _ := revdiffEnv(t)
			s := testSession(rd)
			writeSessionFiles(t, s)
			failAtSecondThread(t, rd, api, s)

			note, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: tt.threads})
			if err != nil {
				t.Fatal(err)
			}
			wantCalls(t, api.calls, tt.calls...)
			if got := threadsOf(api.submitted["PRR_new"].Comments); !slices.Equal(got, tt.submitted) {
				t.Fatalf("submitted threads %q, want %q", got, tt.submitted)
			}
			wantNote := "; removed 1 outdated draft comments"
			if tt.name == "same" {
				wantNote = ""
			}
			if note != wantNote {
				t.Fatalf("note = %q, want %q", note, wantNote)
			}
		})
	}
}

// TestSubmitRetryRangeStartEdited checks that a retry after the user
// changed only the first line of a range replaces the pending comment.
func TestSubmitRetryRangeStartEdited(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	ranged := gh.ReviewThreadInput{Path: "a.go", Body: "range", StartLine: 5, Line: 7, Side: gh.SideRight}
	api.failThread = "b.go"
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: []gh.ReviewThreadInput{ranged, submitThreads[1]}}); err == nil {
		t.Fatal("no error")
	}
	api.failThread, api.calls = "", nil

	ranged.StartLine = 4
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: []gh.ReviewThreadInput{ranged, submitThreads[1]}}); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, api.calls,
		"PendingReview PR_7",
		"DeleteReviewComment C_3",
		"AddReviewThread PRR_new a.go:7",
		"AddReviewThread PRR_new b.go:0",
		`SubmitReview PRR_new COMMENT ""`,
	)
	if got := api.submitted["PRR_new"].Comments; len(got) != 2 || got[0].StartLine != 4 {
		t.Fatalf("submitted comments %+v, want the range from line 4", got)
	}
}

// TestSubmitDeleteFailureThenRetry checks that a failed delete keeps the
// files and the record, and that the next retry deletes and submits.
func TestSubmitDeleteFailureThenRetry(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	failAtSecondThread(t, rd, api, s)
	edited := []gh.ReviewThreadInput{{Path: "a.go", Body: "inside, edited", Line: 3, Side: gh.SideRight}, submitThreads[1]}
	sub := Submission{Event: gh.ReviewComment, Threads: edited}

	api.errs = map[string]error{"DeleteReviewComment": errors.New("boom")}
	if _, err := rd.Submit(context.Background(), s, sub); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
	if !filesExist(s) {
		t.Fatal("files removed after a failure")
	}
	api.errs, api.calls = nil, nil
	if _, err := rd.Submit(context.Background(), s, sub); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, api.calls,
		"PendingReview PR_7",
		"DeleteReviewComment C_3",
		"AddReviewThread PRR_new a.go:3",
		"AddReviewThread PRR_new b.go:0",
		`SubmitReview PRR_new COMMENT ""`,
	)
}

// TestSubmitStaleComment checks which unpaired pending comment argus
// deletes: only a comment that it added and that nobody changed on GitHub.
// Any other unpaired comment stops the submit before a change.
func TestSubmitStaleComment(t *testing.T) {
	added := addedComment{ID: "C_9", Path: "a.go", Line: 8, Body: "outdated"}
	tests := []struct {
		name    string
		comment gh.PendingComment
		// deleted is true if the submit deletes the comment and goes on.
		deleted bool
	}{
		{"recorded and unchanged", gh.PendingComment{ID: "C_9", Path: "a.go", Body: "outdated\r\n", Line: 8}, true},
		{"recorded but edited on GitHub", gh.PendingComment{ID: "C_9", Path: "a.go", Body: "my newer text", Line: 8}, false},
		{"not recorded", gh.PendingComment{ID: "U_1", Path: "x.go", Body: "my draft", Line: 5}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd, _, api, _ := revdiffEnv(t)
			s := testSession(rd)
			writeSessionFiles(t, s)
			rec := reviewRecord{ReviewID: "PRR_web", Added: []addedComment{added}}
			if err := writeRecord(s.ReviewFile, rec); err != nil {
				t.Fatal(err)
			}
			api.has = true
			api.pending = gh.PendingReview{ID: "PRR_web", CommitOID: "abc123", Comments: []gh.PendingComment{
				{ID: "U_2", Path: "a.go", Body: "inside", Line: 3},
				tt.comment,
			}}
			_, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads})
			if tt.deleted {
				if err != nil {
					t.Fatal(err)
				}
				wantCalls(t, api.calls,
					"PendingReview PR_7",
					"DeleteReviewComment C_9",
					"AddReviewThread PRR_web b.go:0",
					`SubmitReview PRR_web COMMENT ""`,
				)
				return
			}
			loc := fmt.Sprintf("%s:%d", tt.comment.Path, tt.comment.Line)
			if err == nil || !strings.Contains(err.Error(), "1 draft comments that argus did not add or that changed on GitHub ("+loc+")") {
				t.Fatalf("err = %v", err)
			}
			wantCalls(t, api.calls, "PendingReview PR_7")
			if !filesExist(s) {
				t.Fatal("files removed after a failure")
			}
			if got, err := readRecord(s.ReviewFile); err != nil || !reflect.DeepEqual(got, rec) {
				t.Fatalf("record = %+v, %v; want %+v", got, err, rec)
			}
		})
	}
}

// TestSubmitRecordsAddedComments checks that the review file keeps the
// path, the line, and the body that argus sent with each comment id.
func TestSubmitRecordsAddedComments(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	failAtSecondThread(t, rd, api, s)
	rec, err := readRecord(s.ReviewFile)
	if err != nil {
		t.Fatal(err)
	}
	want := reviewRecord{ReviewID: "PRR_new", Added: []addedComment{{ID: "C_3", Path: "a.go", Line: 3, Body: "inside"}}}
	if !reflect.DeepEqual(rec, want) {
		t.Fatalf("record = %+v, want %+v", rec, want)
	}
}

// TestSubmitLostReply checks that a retry after a submit that reached
// GitHub without a reply does not post a second review.
func TestSubmitLostReply(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	sub := Submission{Event: gh.ReviewComment, Body: "lgtm", Threads: submitThreads}
	api.lostReply = true
	if _, err := rd.Submit(context.Background(), s, sub); err == nil {
		t.Fatal("no error")
	}
	if !filesExist(s) {
		t.Fatal("files removed after a failure")
	}
	api.lostReply, api.calls = false, nil

	note, err := rd.Submit(context.Background(), s, sub)
	if err != nil {
		t.Fatal(err)
	}
	if note != "; the review was already on GitHub" {
		t.Fatalf("note = %q", note)
	}
	wantCalls(t, api.calls, "PendingReview PR_7", "Review PRR_new")
	if len(api.submitted) != 1 || filesExist(s) {
		t.Fatalf("%d reviews submitted, files kept %v", len(api.submitted), filesExist(s))
	}
	if _, err := os.Stat(s.ReviewFile); err == nil {
		t.Fatal("review file kept")
	}
}

// TestSubmitLostReplyChangedSubmission checks that argus posts nothing
// after a lost reply if the submission changed: each later submit gives
// the same error and keeps the files and the record.
func TestSubmitLostReplyChangedSubmission(t *testing.T) {
	ranged := gh.ReviewThreadInput{Path: "c.go", Body: "range", StartLine: 5, Line: 7, Side: gh.SideRight}
	threads := []gh.ReviewThreadInput{submitThreads[0], submitThreads[1], ranged}
	first := Submission{Event: gh.ReviewApprove, Body: "lgtm", Threads: threads}
	edited := []gh.ReviewThreadInput{{Path: "a.go", Body: "inside, edited", Line: 3, Side: gh.SideRight}, submitThreads[1], ranged}
	movedStart := ranged
	movedStart.StartLine = 4
	tests := []struct {
		name string
		sub  Submission
		diff string
	}{
		{"event", Submission{Event: gh.ReviewRequestChanges, Body: "lgtm", Threads: threads}, "event"},
		{"body", Submission{Event: gh.ReviewApprove, Body: "lgtm, with one note", Threads: threads}, "body"},
		{"edited thread", Submission{Event: gh.ReviewApprove, Body: "lgtm", Threads: edited}, "comments"},
		{"range start", Submission{Event: gh.ReviewApprove, Body: "lgtm", Threads: []gh.ReviewThreadInput{submitThreads[0], submitThreads[1], movedStart}}, "comments"},
		{"removed thread", Submission{Event: gh.ReviewApprove, Body: "lgtm", Threads: threads[:2]}, "comments"},
		{"all", Submission{Event: gh.ReviewComment, Body: "x", Threads: edited}, "event, body, comments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd, _, api, _ := revdiffEnv(t)
			s := testSession(rd)
			writeSessionFiles(t, s)
			api.lostReply = true
			if _, err := rd.Submit(context.Background(), s, first); err == nil {
				t.Fatal("no error")
			}
			api.lostReply = false
			rec, err := readRecord(s.ReviewFile)
			if err != nil {
				t.Fatal(err)
			}

			for range 2 {
				api.calls = nil
				_, err := rd.Submit(context.Background(), s, tt.sub)
				var se *SubmittedError
				if !errors.As(err, &se) || se.Diff != tt.diff || se.State != "APPROVED" {
					t.Fatalf("err = %v, want a SubmittedError with diff %q", err, tt.diff)
				}
				msg := err.Error()
				for _, want := range []string{"https://github.com/acme/api/pull/7#pullrequestreview-PRR_new", "(APPROVED)", "ctrl+x", "argus posted nothing"} {
					if !strings.Contains(msg, want) {
						t.Fatalf("error %q does not contain %q", msg, want)
					}
				}
				if strings.Contains(msg, "submit again") {
					t.Fatalf("error %q suggests a new submit", msg)
				}
				wantCalls(t, api.calls, "PendingReview PR_7", "Review PRR_new")
				if !filesExist(s) {
					t.Fatal("files removed")
				}
				if got, err := readRecord(s.ReviewFile); err != nil || !reflect.DeepEqual(got, rec) {
					t.Fatalf("record = %+v, %v; want %+v", got, err, rec)
				}
				if len(api.submitted) != 1 {
					t.Fatalf("%d reviews submitted", len(api.submitted))
				}
			}
		})
	}
}

// TestSubmitLostReplyNewPendingReview checks that a submitted recorded
// review also stops the submit when the user started a new pending review
// on GitHub after the lost reply.
func TestSubmitLostReplyNewPendingReview(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	api.lostReply = true
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads}); err == nil {
		t.Fatal("no error")
	}
	api.lostReply, api.calls = false, nil
	api.has = true
	api.pending = gh.PendingReview{ID: "PRR_web", CommitOID: "abc123"}

	_, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewApprove, Threads: submitThreads})
	var se *SubmittedError
	if !errors.As(err, &se) || se.Diff != "event" {
		t.Fatalf("err = %v", err)
	}
	wantCalls(t, api.calls, "PendingReview PR_7", "Review PRR_new")
}

// TestSubmittedDiffCommit checks that a submitted review on another commit
// is not the same submission.
func TestSubmittedDiffCommit(t *testing.T) {
	rd, _, _, _ := revdiffEnv(t)
	s := testSession(rd)
	st := gh.ReviewStatus{State: "COMMENTED", CommitOID: "0ld0ld0ld"}
	if got := submittedDiff(st, s, Submission{Event: gh.ReviewComment}); got != "commit" {
		t.Fatalf("submittedDiff = %q", got)
	}
	st.CommitOID = s.HeadSHA
	if got := submittedDiff(st, s, Submission{Event: gh.ReviewComment}); got != "" {
		t.Fatalf("submittedDiff = %q", got)
	}
}

// TestDiscardAfterSubmittedError checks that Discard removes the
// annotation file, the body file, and the record after a SubmittedError,
// and that it calls no GitHub API.
func TestDiscardAfterSubmittedError(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	api.lostReply = true
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads}); err == nil {
		t.Fatal("no error")
	}
	api.lostReply = false
	var se *SubmittedError
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewApprove, Threads: submitThreads}); !errors.As(err, &se) {
		t.Fatalf("err = %v", err)
	}
	api.calls = nil
	if err := rd.Discard(s); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.AnnotationFile, s.BodyFile, s.ReviewFile} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s kept after Discard", p)
		}
	}
	if len(api.calls) != 0 {
		t.Fatalf("calls %q", api.calls)
	}
}

func TestSubmittedErrorWithoutURL(t *testing.T) {
	err := &SubmittedError{ReviewID: "PRR_1", State: "DISMISSED", Diff: "event"}
	if !strings.Contains(err.Error(), "review PRR_1 (DISMISSED), and its event is not the same") {
		t.Fatalf("error = %q", err.Error())
	}
}

// TestSubmitRecordedReviewDeleted checks that a recorded review that the
// user deleted on GitHub gives a new review.
func TestSubmitRecordedReviewDeleted(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	if err := writeRecord(s.ReviewFile, reviewRecord{ReviewID: "PRR_gone", Added: []addedComment{{ID: "C_1", Path: "a.go", Line: 3, Body: "inside"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads}); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, api.calls,
		"PendingReview PR_7",
		"Review PRR_gone",
		"StartReview PR_7 abc123",
		"AddReviewThread PRR_new a.go:3",
		"AddReviewThread PRR_new b.go:0",
		`SubmitReview PRR_new COMMENT ""`,
	)
}

func TestSubmitReviewLookupError(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	if err := writeRecord(s.ReviewFile, reviewRecord{ReviewID: "PRR_1"}); err != nil {
		t.Fatal(err)
	}
	api.errs = map[string]error{"Review": errors.New("boom")}
	if _, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads}); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
	wantCalls(t, api.calls, "PendingReview PR_7", "Review PRR_1")
}

// TestSubmitDiscardFailure checks that a remove error after the submit is
// a warning, not an error, and that a retry posts no second review.
func TestSubmitDiscardFailure(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	// os.Remove cannot remove a directory that has a file.
	s.AnnotationFile = mkdir(t, filepath.Join(rd.StateDir, "annotations"))
	writeFile(t, filepath.Join(s.AnnotationFile, "x"), "x")
	writeFile(t, s.BodyFile, "body")
	sub := Submission{Event: gh.ReviewComment, Threads: submitThreads}

	note, err := rd.Submit(context.Background(), s, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note, "; warning: ") {
		t.Fatalf("note = %q", note)
	}
	for _, p := range []string{s.ReviewFile, s.BodyFile} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed although the annotation file stays: %v", p, err)
		}
	}

	api.calls = nil
	note, err = rd.Submit(context.Background(), s, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note, "; the review was already on GitHub; warning: ") {
		t.Fatalf("note = %q", note)
	}
	wantCalls(t, api.calls, "PendingReview PR_7", "Review PRR_new")
	if len(api.submitted) != 1 {
		t.Fatalf("%d reviews submitted", len(api.submitted))
	}
}

func TestSubmitBadReviewFile(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	writeFile(t, s.ReviewFile, "{not json")
	_, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads})
	if err == nil || !strings.Contains(err.Error(), "read review file") {
		t.Fatalf("err = %v", err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("calls %q", api.calls)
	}
}

func TestSubmitReviewFileWriteError(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	s.ReviewFile = filepath.Join(rd.StateDir, "missing", "review.json")
	_, err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads})
	if err == nil || !strings.Contains(err.Error(), "write review file") {
		t.Fatalf("err = %v", err)
	}
	wantCalls(t, api.calls, "PendingReview PR_7", "StartReview PR_7 abc123")
}

func TestSameComment(t *testing.T) {
	tests := []struct {
		name string
		c    gh.PendingComment
		t    gh.ReviewThreadInput
		want bool
	}{
		{"same line", gh.PendingComment{Path: "a.go", Body: "fix", Line: 3}, gh.ReviewThreadInput{Path: "a.go", Body: " fix\n", Line: 3}, true},
		{"original line", gh.PendingComment{Path: "a.go", Body: "old line", OriginalLine: 9},
			gh.ReviewThreadInput{Path: "a.go", Body: "old line", Line: 9, Side: gh.SideLeft}, true},
		{"other line", gh.PendingComment{Path: "a.go", Body: "fix", Line: 3}, gh.ReviewThreadInput{Path: "a.go", Body: "fix", Line: 4}, false},
		{"other body", gh.PendingComment{Path: "a.go", Body: "fix", Line: 3}, gh.ReviewThreadInput{Path: "a.go", Body: "fix it", Line: 3}, false},
		{"other path", gh.PendingComment{Path: "a.go", Body: "fix", Line: 3}, gh.ReviewThreadInput{Path: "c.go", Body: "fix", Line: 3}, false},
		{"file level", gh.PendingComment{Path: "b.go", Body: "file", File: true}, gh.ReviewThreadInput{Path: "b.go", Body: "file", File: true}, true},
		{"line vs file", gh.PendingComment{Path: "b.go", Body: "file", File: true}, gh.ReviewThreadInput{Path: "b.go", Body: "file", Line: 1}, false},
		{"same range", gh.PendingComment{Path: "a.go", Body: "fix", StartLine: 5, Line: 7},
			gh.ReviewThreadInput{Path: "a.go", Body: "fix", StartLine: 5, Line: 7}, true},
		{"original range", gh.PendingComment{Path: "a.go", Body: "fix", OriginalStartLine: 5, OriginalLine: 7},
			gh.ReviewThreadInput{Path: "a.go", Body: "fix", StartLine: 5, Line: 7, Side: gh.SideLeft}, true},
		{"other range start", gh.PendingComment{Path: "a.go", Body: "fix", StartLine: 5, OriginalStartLine: 5, Line: 7},
			gh.ReviewThreadInput{Path: "a.go", Body: "fix", StartLine: 4, Line: 7}, false},
		{"range vs line", gh.PendingComment{Path: "a.go", Body: "fix", StartLine: 5, Line: 7},
			gh.ReviewThreadInput{Path: "a.go", Body: "fix", Line: 7}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameComment(tt.c, tt.t); got != tt.want {
				t.Fatalf("sameComment = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMatchComments checks that one comment pairs with one thread only.
func TestMatchComments(t *testing.T) {
	th := gh.ReviewThreadInput{Path: "a.go", Body: "fix", Line: 3}
	comments := []gh.PendingComment{
		{ID: "C_1", Path: "a.go", Body: "fix", Line: 3},
		{ID: "C_2", Path: "z.go", Body: "gone", Line: 1},
	}
	missing, stale := matchComments(comments, []gh.ReviewThreadInput{th, th})
	if !slices.Equal(missing, []gh.ReviewThreadInput{th}) {
		t.Fatalf("missing = %+v", missing)
	}
	if len(stale) != 1 || stale[0].ID != "C_2" {
		t.Fatalf("stale = %+v", stale)
	}
}

func TestCommentLocations(t *testing.T) {
	comments := []gh.PendingComment{
		{Path: "a.go", Line: 3},
		{Path: "b.go", File: true},
		{Path: "c.go", OriginalLine: 9},
		{Path: "d.go", Line: 1},
	}
	if got := commentLocations(comments); got != "a.go:3, b.go, c.go:9, and 1 more" {
		t.Fatalf("commentLocations = %q", got)
	}
}

func TestReviewBody(t *testing.T) {
	moved := []gh.ReviewThreadInput{{Path: "a.go", Body: "x", Line: 1}}
	for _, tt := range []struct {
		sub  Submission
		want string
	}{
		{Submission{Body: " text "}, "text"},
		{Submission{Moved: moved}, "a.go:1 — x"},
		{Submission{Body: "text", Moved: moved}, "text\n\na.go:1 — x"},
	} {
		if got := tt.sub.ReviewBody(); got != tt.want {
			t.Errorf("ReviewBody(%+v) = %q, want %q", tt.sub, got, tt.want)
		}
	}
}

func TestDiscardMissingFiles(t *testing.T) {
	rd, _, _, _ := revdiffEnv(t)
	if err := rd.Discard(testSession(rd)); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRef(t *testing.T) {
	if got := (Session{PR: reviewPR()}).Ref(); got != "acme/api#7" {
		t.Fatalf("Ref = %q", got)
	}
}
