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

	"github.com/tdragon/argus/internal/gh"
)

// fakeReviewAPI records each call and returns the error of its step.
type fakeReviewAPI struct {
	calls   []string
	pending gh.PendingReview
	has     bool
	errs    map[string]error
	// failThread makes AddReviewThread fail for the thread at this path.
	failThread string
}

func (f *fakeReviewAPI) PendingReview(_ context.Context, prID string) (gh.PendingReview, bool, error) {
	f.calls = append(f.calls, "PendingReview "+prID)
	if err := f.errs["PendingReview"]; err != nil {
		return gh.PendingReview{}, false, err
	}
	return f.pending, f.has, nil
}

func (f *fakeReviewAPI) StartReview(_ context.Context, prID, commitOID string) (string, error) {
	f.calls = append(f.calls, "StartReview "+prID+" "+commitOID)
	if err := f.errs["StartReview"]; err != nil {
		return "", err
	}
	f.pending, f.has = gh.PendingReview{ID: "PRR_new", CommitOID: commitOID}, true
	return "PRR_new", nil
}

func (f *fakeReviewAPI) AddReviewThread(_ context.Context, reviewID string, t gh.ReviewThreadInput) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("AddReviewThread %s %s:%d", reviewID, t.Path, t.Line))
	if t.Path == f.failThread {
		return "", errors.New("thread failed")
	}
	f.pending.Comments = append(f.pending.Comments, gh.PendingComment{Path: t.Path, Body: t.Body, Line: t.Line, File: t.File})
	return "T_1", nil
}

func (f *fakeReviewAPI) SubmitReview(_ context.Context, reviewID string, event gh.ReviewEvent, body string) error {
	f.calls = append(f.calls, fmt.Sprintf("SubmitReview %s %s %q", reviewID, event, body))
	return f.errs["SubmitReview"]
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
		BodyFile:       filepath.Join(rd.StateDir, "pr-acme-api-7.body.md"),
		AnnotationFile: filepath.Join(rd.StateDir, "pr-acme-api-7.md"),
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
		"git -C "+clone+" worktree add --detach "+wtDir+" abc123",
		"git -C "+wtDir+" merge-base origin/main abc123",
	)
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
	if err := rd.Submit(context.Background(), s, sub); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PendingReview PR_7",
		"StartReview PR_7 abc123",
		"AddReviewThread PRR_new a.go:3",
		"AddReviewThread PRR_new b.go:0",
		`SubmitReview PRR_new REQUEST_CHANGES "overall\n\na.go:40 — outside"`,
	}
	if !slices.Equal(api.calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", api.calls, want)
	}
	for _, p := range []string{s.AnnotationFile, s.BodyFile} {
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
		{Path: "a.go", Body: "inside\r\n", Line: 3},
	}}
	if err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PendingReview PR_7",
		"AddReviewThread PRR_old b.go:0",
		`SubmitReview PRR_old COMMENT ""`,
	}
	if !slices.Equal(api.calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", api.calls, want)
	}
}

func TestSubmitPendingReviewOnOtherCommit(t *testing.T) {
	rd, _, api, _ := revdiffEnv(t)
	s := testSession(rd)
	writeSessionFiles(t, s)
	api.has = true
	api.pending = gh.PendingReview{ID: "PRR_old", CommitOID: "0ld0ld0ld"}
	err := rd.Submit(context.Background(), s, Submission{Event: gh.ReviewComment, Threads: submitThreads})
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
	}{
		{"find pending review", func(api *fakeReviewAPI) {
			api.errs = map[string]error{"PendingReview": errors.New("boom")}
		}, 1},
		{"start review", func(api *fakeReviewAPI) {
			api.errs = map[string]error{"StartReview": errors.New("boom")}
		}, 2},
		{"add thread", func(api *fakeReviewAPI) { api.failThread = "b.go" }, 4},
		{"submit", func(api *fakeReviewAPI) {
			api.errs = map[string]error{"SubmitReview": errors.New("boom")}
		}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd, _, api, _ := revdiffEnv(t)
			s := testSession(rd)
			writeSessionFiles(t, s)
			tt.setup(api)
			sub := Submission{Event: gh.ReviewComment, Threads: submitThreads}
			if err := rd.Submit(context.Background(), s, sub); err == nil {
				t.Fatal("no error")
			}
			if len(api.calls) != tt.calls {
				t.Fatalf("calls %q, want %d", api.calls, tt.calls)
			}
			if !filesExist(s) {
				t.Fatal("files removed after a failure")
			}

			// The retry finds the pending review and adds no thread twice.
			api.errs, api.failThread = nil, ""
			api.calls = nil
			if err := rd.Submit(context.Background(), s, sub); err != nil {
				t.Fatal(err)
			}
			added := 0
			for _, c := range api.pending.Comments {
				if c.Path == "a.go" {
					added++
				}
			}
			if added != 1 {
				t.Fatalf("a.go thread added %d times", added)
			}
			if filesExist(s) {
				t.Fatal("files kept after the retry")
			}
		})
	}
}

func TestHasComment(t *testing.T) {
	comments := []gh.PendingComment{
		{Path: "a.go", Body: "fix", Line: 3},
		{Path: "a.go", Body: "old line", OriginalLine: 9},
		{Path: "b.go", Body: "file", File: true},
	}
	tests := []struct {
		name string
		t    gh.ReviewThreadInput
		want bool
	}{
		{"same line", gh.ReviewThreadInput{Path: "a.go", Body: " fix\n", Line: 3}, true},
		{"original line", gh.ReviewThreadInput{Path: "a.go", Body: "old line", Line: 9, Side: gh.SideLeft}, true},
		{"other line", gh.ReviewThreadInput{Path: "a.go", Body: "fix", Line: 4}, false},
		{"other body", gh.ReviewThreadInput{Path: "a.go", Body: "fix it", Line: 3}, false},
		{"other path", gh.ReviewThreadInput{Path: "c.go", Body: "fix", Line: 3}, false},
		{"file level", gh.ReviewThreadInput{Path: "b.go", Body: "file", File: true}, true},
		{"line vs file", gh.ReviewThreadInput{Path: "b.go", Body: "file", Line: 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasComment(comments, tt.t); got != tt.want {
				t.Fatalf("hasComment = %v, want %v", got, tt.want)
			}
		})
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
