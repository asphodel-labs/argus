package review

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
)

// Exit codes of revdiff with --exit-code-on-annotations. agtermctl with
// --block exits with the status of revdiff.
const (
	exitNoAnnotations = 0
	exitAnnotations   = 10
)

// ErrNoAgterm is the error of a review outside agterm. The overlay needs
// the AGTERM_SESSION_ID of the session that runs argus.
var ErrNoAgterm = errors.New("revdiff review requires agterm")

// ReviewAPI is the part of gh.Client that the review submit uses.
type ReviewAPI interface {
	PendingReview(ctx context.Context, prID string) (gh.PendingReview, bool, error)
	StartReview(ctx context.Context, prID, commitOID string) (string, error)
	AddReviewThread(ctx context.Context, reviewID string, t gh.ReviewThreadInput) (string, error)
	SubmitReview(ctx context.Context, reviewID string, event gh.ReviewEvent, body string) error
}

// Revdiff runs revdiff for a PR in an agterm overlay and submits the
// annotations as one GitHub review.
type Revdiff struct {
	// Worktrees prepares the PR worktree. Its Runner also runs agtermctl.
	Worktrees *Worktrees
	// GitHub submits the review.
	GitHub ReviewAPI
	// StateDir holds the PR body files and the annotation files.
	StateDir string
	// SessionID is AGTERM_SESSION_ID. Empty means that argus does not run
	// in agterm.
	SessionID string
	// Agtermctl is the agtermctl command. It is "agtermctl" when empty.
	Agtermctl string
	// Bin is the revdiff command that the overlay runs. The overlay shell
	// has the PATH of the agterm app, so main gives an absolute path. It is
	// "revdiff" when empty.
	Bin string
}

// Session is one revdiff run for a PR.
type Session struct {
	PR model.PR
	// Worktree is the PR worktree.
	Worktree Worktree
	// HeadSHA is the PR head that revdiff shows. The pending review is
	// made on this commit.
	HeadSHA string
	// MergeBase is the merge base of the base branch and HeadSHA.
	MergeBase string
	// BodyFile holds the PR body for the revdiff info popup.
	BodyFile string
	// AnnotationFile is the -o file of revdiff. It stays after a failed
	// submit, and the next run preloads it.
	AnnotationFile string
}

// Result is the outcome of one revdiff run.
type Result struct {
	// Annotated is false if revdiff exited with no annotations.
	Annotated bool
	// Threads are the annotations inside the diff hunks.
	Threads []gh.ReviewThreadInput
	// Moved are the line comments outside every hunk of their side. The
	// submit puts them into the review body.
	Moved []gh.ReviewThreadInput
}

// filePrefix returns "<state>/pr-<owner>-<repo>-<n>".
func (r *Revdiff) filePrefix(pr model.PR) string {
	return filepath.Join(r.StateDir, fmt.Sprintf("pr-%s-%s-%d", pr.Repo.Owner, pr.Repo.Name, pr.Number))
}

// AnnotationFile returns the annotation file of the PR.
func (r *Revdiff) AnnotationFile(pr model.PR) string { return r.filePrefix(pr) + ".md" }

// BodyFile returns the PR body file of the PR.
func (r *Revdiff) BodyFile(pr model.PR) string { return r.filePrefix(pr) + ".body.md" }

// Prepare sets up the PR worktree, computes the merge base, and writes the
// PR body file. pr must be a PR detail with the head SHA, the base branch,
// and the body. Prepare returns ErrNoAgterm outside agterm, and a
// *MissingClone error if the repository has no clone.
func (r *Revdiff) Prepare(ctx context.Context, pr model.PR) (Session, error) {
	if r.SessionID == "" {
		return Session{}, ErrNoAgterm
	}
	wt, err := r.Worktrees.Setup(ctx, pr)
	if err != nil {
		return Session{}, err
	}
	base, err := r.Worktrees.MergeBase(ctx, wt.Dir, pr.BaseRef, pr.HeadSHA)
	if err != nil {
		return Session{}, err
	}
	s := Session{
		PR:             pr,
		Worktree:       wt,
		HeadSHA:        pr.HeadSHA,
		MergeBase:      base,
		BodyFile:       r.BodyFile(pr),
		AnnotationFile: r.AnnotationFile(pr),
	}
	if err := os.MkdirAll(r.StateDir, 0o700); err != nil {
		return Session{}, fmt.Errorf("make state directory: %w", err)
	}
	if err := os.WriteFile(s.BodyFile, []byte(pr.Body), 0o600); err != nil {
		return Session{}, fmt.Errorf("write PR body file: %w", err)
	}
	return s, nil
}

// RevdiffCommand returns the revdiff command line of the session as one
// sh string. agterm runs the overlay command through `sh -c`, so each
// argument is quoted. preload adds --annotations with the annotation file.
func (r *Revdiff) RevdiffCommand(s Session, preload bool) string {
	bin := r.Bin
	if bin == "" {
		bin = "revdiff"
	}
	args := []string{bin, "--vim-motion", "--exit-code-on-annotations",
		"-o", s.AnnotationFile, "--description-file", s.BodyFile}
	if preload {
		args = append(args, "--annotations", s.AnnotationFile)
	}
	args = append(args, s.MergeBase, s.HeadSHA)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = execx.ShellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// OverlayCommand returns the agtermctl command and its arguments that open
// revdiff in an overlay on the argus session and wait for it. preload adds
// --annotations with the annotation file.
func (r *Revdiff) OverlayCommand(s Session, preload bool) (name string, args []string) {
	name = r.Agtermctl
	if name == "" {
		name = "agtermctl"
	}
	return name, []string{"session", "overlay", "open", "--block",
		"--target", r.SessionID, "--cwd", s.Worktree.Dir, r.RevdiffCommand(s, preload)}
}

// Open runs revdiff in the overlay and waits for it. Exit 10 reads the
// annotations and splits them by the diff hunks. Exit 0 gives a Result
// with Annotated false. Another exit code is an error. An annotation file
// from a failed submit is preloaded.
func (r *Revdiff) Open(ctx context.Context, s Session) (Result, error) {
	_, err := os.Stat(s.AnnotationFile)
	preload := err == nil
	name, args := r.OverlayCommand(s, preload)
	_, stderr, code, err := r.Worktrees.Runner.Run(ctx, name, args...)
	if err != nil {
		return Result{}, fmt.Errorf("run %s: %w", name, err)
	}
	switch code {
	case exitNoAnnotations:
		return Result{}, nil
	case exitAnnotations:
	default:
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			return Result{}, fmt.Errorf("revdiff overlay exited with code %d", code)
		}
		return Result{}, fmt.Errorf("revdiff overlay exited with code %d: %s", code, msg)
	}

	f, err := os.Open(s.AnnotationFile)
	if err != nil {
		return Result{}, fmt.Errorf("read annotations: %w", err)
	}
	defer f.Close()
	threads, err := ParseAnnotations(f)
	if err != nil {
		return Result{}, err
	}
	if len(threads) == 0 {
		return Result{}, nil
	}
	hunks, err := r.Worktrees.Hunks(ctx, s.Worktree.Dir, s.MergeBase, s.HeadSHA)
	if err != nil {
		return Result{}, err
	}
	inside, outside := SplitOutside(threads, hunks)
	return Result{Annotated: true, Threads: inside, Moved: outside}, nil
}

// Submission is what the review dialog submits.
type Submission struct {
	Event gh.ReviewEvent
	// Body is the review body that the user typed.
	Body string
	// Threads become review threads. Moved go into the review body.
	Threads []gh.ReviewThreadInput
	Moved   []gh.ReviewThreadInput
}

// ReviewBody returns the body text of the submitted review: the typed
// body, then the comments outside the hunks.
func (sub Submission) ReviewBody() string {
	body := strings.TrimSpace(sub.Body)
	if len(sub.Moved) == 0 {
		return body
	}
	moved := MovedBody(sub.Moved)
	if body == "" {
		return moved
	}
	return body + "\n\n" + moved
}

// Submit posts the submission as one review:
//  1. find the pending review of the viewer, or start one on the session
//     head;
//  2. add each thread that the pending review does not have yet;
//  3. submit the pending review with the event and the body.
//
// A failure after step 1 leaves the pending review on GitHub, and the next
// Submit adds to it. Step 2 skips a thread whose path, line, and body
// match a comment of the pending review, so a retry adds no thread twice.
// On a success Submit removes the annotation file and the body file. On a
// failure it keeps them.
func (r *Revdiff) Submit(ctx context.Context, s Session, sub Submission) error {
	pending, ok, err := r.GitHub.PendingReview(ctx, s.PR.ID)
	if err != nil {
		return err
	}
	reviewID := pending.ID
	switch {
	case !ok:
		reviewID, err = r.GitHub.StartReview(ctx, s.PR.ID, s.HeadSHA)
		if err != nil {
			return err
		}
	case pending.CommitOID != "" && pending.CommitOID != s.HeadSHA:
		// The lines of the threads refer to s.HeadSHA. GitHub puts a new
		// thread on the commit of the pending review.
		return fmt.Errorf("you have a pending review on commit %s, not on %s; submit or delete it on GitHub first",
			short(pending.CommitOID), short(s.HeadSHA))
	}
	for _, t := range sub.Threads {
		if hasComment(pending.Comments, t) {
			continue
		}
		if _, err := r.GitHub.AddReviewThread(ctx, reviewID, t); err != nil {
			return err
		}
	}
	if err := r.GitHub.SubmitReview(ctx, reviewID, sub.Event, sub.ReviewBody()); err != nil {
		return err
	}
	return r.Discard(s)
}

// Discard removes the annotation file and the body file of the session.
// A missing file is not an error.
func (r *Revdiff) Discard(s Session) error {
	var errs []error
	for _, path := range []string{s.AnnotationFile, s.BodyFile} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// hasComment reports whether the pending review has a comment with the
// path, the line, and the body of t. GitHub gives no side for a comment,
// so the side is not compared. A removed-line comment can have its line
// in originalLine.
func hasComment(comments []gh.PendingComment, t gh.ReviewThreadInput) bool {
	body := normalizeBody(t.Body)
	for _, c := range comments {
		if c.Path != t.Path || c.File != t.File || normalizeBody(c.Body) != body {
			continue
		}
		if t.File || c.Line == t.Line || c.OriginalLine == t.Line {
			return true
		}
	}
	return false
}

// normalizeBody removes the carriage returns and the outer white space.
func normalizeBody(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
}

// short returns the first 7 characters of a SHA.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Ref returns "owner/repo#n" of the session PR.
func (s Session) Ref() string {
	return s.PR.Repo.FullName() + "#" + strconv.Itoa(s.PR.Number)
}
