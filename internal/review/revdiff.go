package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asphodel-labs/argus/internal/execx"
	"github.com/asphodel-labs/argus/internal/gh"
	"github.com/asphodel-labs/argus/internal/model"
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
	Review(ctx context.Context, reviewID string) (gh.ReviewStatus, bool, error)
	StartReview(ctx context.Context, prID, commitOID string) (string, error)
	AddReviewThread(ctx context.Context, reviewID string, t gh.ReviewThreadInput) (string, error)
	DeleteReviewComment(ctx context.Context, commentID string) error
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
	// ReviewFile records the review that the annotations go into and the
	// comments that argus added to it. It stays after a failed submit.
	ReviewFile string
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

// filePrefix returns "<state>/pr-<owner>+<repo>+<n>". A GitHub owner or
// repository name cannot contain "+", so two PRs never get the same files.
func (r *Revdiff) filePrefix(pr model.PR) string {
	return filepath.Join(r.StateDir, fmt.Sprintf("pr-%s+%s+%d", pr.Repo.Owner, pr.Repo.Name, pr.Number))
}

// AnnotationFile returns the annotation file of the PR.
func (r *Revdiff) AnnotationFile(pr model.PR) string { return r.filePrefix(pr) + ".md" }

// BodyFile returns the PR body file of the PR.
func (r *Revdiff) BodyFile(pr model.PR) string { return r.filePrefix(pr) + ".body.md" }

// ReviewFile returns the review file of the PR.
func (r *Revdiff) ReviewFile(pr model.PR) string { return r.filePrefix(pr) + ".review.json" }

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
		ReviewFile:     r.ReviewFile(pr),
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

// Submit posts the submission as one review and returns a note for the
// status line:
//  1. find the pending review of the viewer, or start one on the session
//     head;
//  2. pair each thread with one comment of the pending review that has its
//     path, line, and body;
//  3. delete each other comment of the pending review that argus added and
//     that nobody changed on GitHub;
//  4. add each thread that has no comment yet;
//  5. submit the pending review with the event and the body.
//
// The review file records the review and each comment that argus added,
// with its path, line, and body. A failure after step 1 leaves the pending
// review on GitHub, and the next Submit continues it. Thus a retry adds no
// thread twice, and a thread that the user edited or deleted after the
// failure does not stay in the review. Submit stops before a change if an
// unpaired pending comment is not in the record, or if its body on GitHub
// is not the recorded body: the user wrote or edited it on GitHub, and
// argus does not publish or delete it.
//
// If the recorded review is not pending any more, an earlier Submit
// reached GitHub. Submit then posts nothing. If that review has the
// submission, Submit reports a success. Otherwise it returns an error that
// names the review, and it keeps the files and the record, so that each
// later Submit gives the same error. Discard is the way out.
// On a success Submit removes the annotation file, the body file, and the
// review file. On a failure it keeps them.
func (r *Revdiff) Submit(ctx context.Context, s Session, sub Submission) (string, error) {
	rec, err := readRecord(s.ReviewFile)
	if err != nil {
		return "", err
	}
	pending, ok, err := r.GitHub.PendingReview(ctx, s.PR.ID)
	if err != nil {
		return "", err
	}
	if rec.ReviewID != "" && (!ok || pending.ID != rec.ReviewID) {
		st, found, err := r.GitHub.Review(ctx, rec.ReviewID)
		if err != nil {
			return "", err
		}
		if found && st.State != "PENDING" {
			if diff := submittedDiff(st, s, sub); diff != "" {
				return "", &SubmittedError{ReviewID: rec.ReviewID, URL: st.URL, State: st.State, Diff: diff}
			}
			return r.finish(s, "; the review was already on GitHub"), nil
		}
	}
	reviewID := pending.ID
	switch {
	case !ok:
		reviewID, err = r.GitHub.StartReview(ctx, s.PR.ID, s.HeadSHA)
		if err != nil {
			return "", err
		}
	case pending.CommitOID != "" && pending.CommitOID != s.HeadSHA:
		// The lines of the threads refer to s.HeadSHA. GitHub puts a new
		// thread on the commit of the pending review.
		return "", fmt.Errorf("you have a pending review on commit %s, not on %s; submit or delete it on GitHub first",
			short(pending.CommitOID), short(s.HeadSHA))
	}
	if rec.ReviewID != reviewID {
		// The comments of another review are not in this review.
		rec = reviewRecord{ReviewID: reviewID}
	}
	missing, stale := matchComments(pending.Comments, sub.Threads)
	var foreign []gh.PendingComment
	for _, c := range stale {
		if !rec.unchanged(c) {
			foreign = append(foreign, c)
		}
	}
	if len(foreign) > 0 {
		return "", fmt.Errorf("your pending review on GitHub has %d draft comments that argus did not add or that changed on GitHub (%s); "+
			"delete them or submit the pending review on GitHub first", len(foreign), commentLocations(foreign))
	}
	if err := writeRecord(s.ReviewFile, rec); err != nil {
		return "", err
	}
	for _, c := range stale {
		if err := r.GitHub.DeleteReviewComment(ctx, c.ID); err != nil {
			return "", err
		}
	}
	for _, t := range missing {
		id, err := r.GitHub.AddReviewThread(ctx, reviewID, t)
		if err != nil {
			return "", err
		}
		rec.Added = append(rec.Added, addedComment{ID: id, Path: t.Path, StartLine: t.StartLine, Line: t.Line, File: t.File, Body: t.Body})
		if err := writeRecord(s.ReviewFile, rec); err != nil {
			return "", err
		}
	}
	if err := r.GitHub.SubmitReview(ctx, reviewID, sub.Event, sub.ReviewBody()); err != nil {
		return "", err
	}
	note := ""
	if len(stale) > 0 {
		note = fmt.Sprintf("; removed %d outdated draft comments", len(stale))
	}
	return r.finish(s, note), nil
}

// SubmittedError is the error of a Submit when the recorded review is
// already submitted on GitHub and does not have the submission. Submit
// posts nothing then. Discard removes the local files and the record.
type SubmittedError struct {
	ReviewID string
	// URL is the URL of the review, or "" if GitHub did not send it.
	URL string
	// State is the state of the review on GitHub, for example APPROVED.
	State string
	// Diff names the parts that are not the same, for example "event, body".
	Diff string
}

func (e *SubmittedError) Error() string {
	ref := e.URL
	if ref == "" {
		ref = e.ReviewID
	}
	return fmt.Sprintf("an earlier submit from argus is on GitHub as review %s (%s), and its %s is not the same as this submission; "+
		"argus posted nothing; check that review on GitHub, then press ctrl+x to discard the local annotations", ref, e.State, e.Diff)
}

// submittedDiff compares a submitted review with the submission. It
// returns "" if the review has the session head, the event, the review
// body, and the same threads. Otherwise it names the parts that are not
// the same.
func submittedDiff(st gh.ReviewStatus, s Session, sub Submission) string {
	var diff []string
	if st.CommitOID != s.HeadSHA {
		diff = append(diff, "commit")
	}
	if st.State != submittedState(sub.Event) {
		diff = append(diff, "event")
	}
	if normalizeBody(st.Body) != normalizeBody(sub.ReviewBody()) {
		diff = append(diff, "body")
	}
	if missing, extra := matchComments(st.Comments, sub.Threads); len(missing) > 0 || len(extra) > 0 {
		diff = append(diff, "comments")
	}
	return strings.Join(diff, ", ")
}

// submittedState returns the GitHub PullRequestReviewState of a review
// that was submitted with the event.
func submittedState(e gh.ReviewEvent) string {
	switch e {
	case gh.ReviewApprove:
		return "APPROVED"
	case gh.ReviewRequestChanges:
		return "CHANGES_REQUESTED"
	default:
		return "COMMENTED"
	}
}

// finish removes the session files after a submit and returns note. A
// remove error becomes a warning in the note: the review is on GitHub, so
// a retry must not post it again.
func (r *Revdiff) finish(s Session, note string) string {
	if err := r.Discard(s); err != nil {
		return note + "; warning: " + err.Error()
	}
	return note
}

// Discard removes the annotation file, the review file, and the body file
// of the session. A missing file is not an error. If Discard cannot remove
// the annotation file, it keeps the review file too: the next Submit of
// the preloaded annotations then finds the submitted review.
func (r *Revdiff) Discard(s Session) error {
	if err := removeFile(s.AnnotationFile); err != nil {
		return err
	}
	return errors.Join(removeFile(s.ReviewFile), removeFile(s.BodyFile))
}

// removeFile removes path. A missing file is not an error.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// reviewRecord is the content of the review file.
type reviewRecord struct {
	// ReviewID is the review that the annotations go into.
	ReviewID string `json:"reviewId,omitempty"`
	// Added are the comments that argus added to the review.
	Added []addedComment `json:"added,omitempty"`
}

// addedComment is one comment that argus added: its id and the path, the
// lines, and the body that argus sent. StartLine is 0 for one line.
type addedComment struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine,omitempty"`
	Line      int    `json:"line,omitempty"`
	File      bool   `json:"file,omitempty"`
	Body      string `json:"body"`
}

// unchanged reports whether argus added the comment and the comment still
// has the path, the lines, and the body that argus sent. Only such a
// comment can be deleted: a changed body is a draft of the user.
func (rec reviewRecord) unchanged(c gh.PendingComment) bool {
	for _, a := range rec.Added {
		if a.ID == c.ID {
			return sameComment(c, gh.ReviewThreadInput{Path: a.Path, StartLine: a.StartLine, Line: a.Line, File: a.File, Body: a.Body})
		}
	}
	return false
}

// readRecord reads the review file. A missing file gives an empty record.
func readRecord(path string) (reviewRecord, error) {
	var rec reviewRecord
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return rec, fmt.Errorf("read review file: %w", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return reviewRecord{}, fmt.Errorf("read review file %s: %w", path, err)
	}
	return rec, nil
}

// writeRecord writes the review file. It writes a temporary file and then
// renames it, so that a crash does not leave half a file.
func writeRecord(path string, rec reviewRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("write review file: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write review file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write review file: %w", err)
	}
	return nil
}

// matchComments pairs each thread with one comment that has its path, its
// lines, and its body. missing are the threads with no comment. stale are
// the comments with no thread.
func matchComments(comments []gh.PendingComment, threads []gh.ReviewThreadInput) (missing []gh.ReviewThreadInput, stale []gh.PendingComment) {
	used := make([]bool, len(comments))
	for _, t := range threads {
		i := -1
		for j, c := range comments {
			if !used[j] && sameComment(c, t) {
				i = j
				break
			}
		}
		if i < 0 {
			missing = append(missing, t)
			continue
		}
		used[i] = true
	}
	for i, c := range comments {
		if !used[i] {
			stale = append(stale, c)
		}
	}
	return missing, stale
}

// sameComment reports whether the comment has the path, the first line,
// the last line, and the body of t. GitHub gives no side for a comment, so
// the side is not compared. A removed-line comment can have its lines in
// originalStartLine and originalLine. StartLine is 0 for one line.
func sameComment(c gh.PendingComment, t gh.ReviewThreadInput) bool {
	if c.Path != t.Path || c.File != t.File || normalizeBody(c.Body) != normalizeBody(t.Body) {
		return false
	}
	if t.File {
		return true
	}
	return (c.Line == t.Line && c.StartLine == t.StartLine) ||
		(c.OriginalLine == t.Line && c.OriginalStartLine == t.StartLine)
}

// commentLocations returns "path:L, path" for the first three comments,
// and the number of the other comments.
func commentLocations(comments []gh.PendingComment) string {
	const shown = 3
	var locs []string
	for i, c := range comments {
		if i == shown {
			locs = append(locs, fmt.Sprintf("and %d more", len(comments)-shown))
			break
		}
		line := c.Line
		if line == 0 {
			line = c.OriginalLine
		}
		if c.File || line == 0 {
			locs = append(locs, c.Path)
			continue
		}
		locs = append(locs, fmt.Sprintf("%s:%d", c.Path, line))
	}
	return strings.Join(locs, ", ")
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
