// Package review prepares a local worktree for a PR review: it finds the
// clone, adds or moves the PR worktree, and removes old PR worktrees.
package review

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/gh"
	"github.com/tdragon/argus/internal/model"
)

// worktreesDir is the directory inside a clone that holds the PR worktrees.
const worktreesDir = ".worktrees"

// worktreeName matches the directory name of a PR worktree.
var worktreeName = regexp.MustCompile(`^argus-pr-([0-9]+)$`)

// Worktrees finds the clones and manages the PR worktrees in them.
type Worktrees struct {
	Runner execx.Runner
	// RepoRoot is the root of the clone lookup: <RepoRoot>/<owner>/<repo>.
	RepoRoot string
	// Repos maps "owner/repo" to a clone directory. It overrides RepoRoot.
	Repos map[string]string
}

// MissingClone is the error of a repository that has no local clone. The
// TUI asks the user before it calls Clone.
type MissingClone struct {
	// Repo is "owner/repo".
	Repo string
	// Dir is the directory that Clone clones into.
	Dir string
}

func (e *MissingClone) Error() string {
	return fmt.Sprintf("no clone of %s at %s", e.Repo, e.Dir)
}

// Worktree is the PR worktree that Setup prepared.
type Worktree struct {
	// Clone is the directory of the clone.
	Clone string
	// Dir is the directory of the worktree.
	Dir string
	// Dirty is true if the worktree has changes. Setup does not move a
	// dirty worktree to the new head.
	Dirty bool
}

// cloneDir returns the clone directory of repo and whether a clone exists
// there. It tries the Repos override first, then RepoRoot. With no clone,
// dir is the override if one is set, else the RepoRoot path.
func (w *Worktrees) cloneDir(repo model.Repo) (dir string, ok bool) {
	full := repo.FullName()
	override := w.Repos[full]
	if override == "" {
		for name, d := range w.Repos {
			if strings.EqualFold(name, full) {
				override = d
				break
			}
		}
	}
	rootDir := filepath.Join(w.RepoRoot, repo.Owner, repo.Name)
	for _, d := range []string{override, rootDir} {
		if d != "" && isClone(d) {
			return d, true
		}
	}
	if override != "" {
		return override, false
	}
	return rootDir, false
}

// isClone reports whether dir holds a .git directory or file.
func isClone(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// FindClone returns the clone directory of repo, or a *MissingClone error.
func (w *Worktrees) FindClone(repo model.Repo) (string, error) {
	dir, ok := w.cloneDir(repo)
	if !ok {
		return "", &MissingClone{Repo: repo.FullName(), Dir: dir}
	}
	return dir, nil
}

// Clone runs `gh repo clone` for the missing clone.
func (w *Worktrees) Clone(ctx context.Context, c *MissingClone) error {
	_, err := w.run(ctx, "gh", "repo", "clone", c.Repo, c.Dir)
	return err
}

// Setup fetches the PR head and the base branch, then adds the PR worktree
// at <clone>/.worktrees/argus-pr-<n> in detached HEAD state. An existing
// worktree moves to the PR head only if its tree is clean. Setup returns a
// *MissingClone error if the repository has no clone.
func (w *Worktrees) Setup(ctx context.Context, pr model.PR) (Worktree, error) {
	if pr.HeadSHA == "" || pr.BaseRef == "" {
		return Worktree{}, errors.New("the PR has no head SHA or base branch")
	}
	clone, err := w.FindClone(pr.Repo)
	if err != nil {
		return Worktree{}, err
	}
	n := strconv.Itoa(pr.Number)
	if _, err := w.git(ctx, clone, "fetch", "origin",
		"refs/pull/"+n+"/head",
		"+refs/heads/"+pr.BaseRef+":refs/remotes/origin/"+pr.BaseRef); err != nil {
		return Worktree{}, err
	}

	wt := Worktree{Clone: clone, Dir: filepath.Join(clone, worktreesDir, "argus-pr-"+n)}
	_, err = os.Stat(wt.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		_, err = w.git(ctx, clone, "worktree", "add", "--detach", wt.Dir, pr.HeadSHA)
		return wt, err
	}
	if err != nil {
		return Worktree{}, err
	}

	// The directory is inside the clone. Without this check, git in a
	// directory that is not a worktree acts on the clone itself.
	listed, err := w.listWorktrees(ctx, clone)
	if err != nil {
		return Worktree{}, err
	}
	if !slices.ContainsFunc(listed, func(p string) bool { return resolve(p) == resolve(wt.Dir) }) {
		return Worktree{}, fmt.Errorf("%s exists but is not a worktree of %s", wt.Dir, clone)
	}
	clean, err := w.isClean(ctx, wt.Dir)
	if err != nil {
		return Worktree{}, err
	}
	if !clean {
		wt.Dirty = true
		return wt, nil
	}
	_, err = w.git(ctx, wt.Dir, "checkout", "--detach", pr.HeadSHA)
	return wt, err
}

// MergeBase returns the merge base of origin/<base> and headSha.
func (w *Worktrees) MergeBase(ctx context.Context, worktree, base, headSha string) (string, error) {
	out, err := w.git(ctx, worktree, "merge-base", "origin/"+base, headSha)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("git merge-base origin/%s %s returned no commit", base, headSha)
	}
	return sha, nil
}

// StateFetcher gets the state of PRs. gh.Client implements it.
type StateFetcher interface {
	PRStates(ctx context.Context, refs []gh.PRRef) (map[gh.PRRef]gh.PRState, error)
}

// CleanupResult lists what Cleanup did.
type CleanupResult struct {
	// Removed are the worktree directories that Cleanup removed.
	Removed []string
	// Dirty are the worktrees of merged or closed PRs that have changes.
	// Cleanup keeps them.
	Dirty []string
	// Errs are the errors of single clones or worktrees. Cleanup goes on
	// after such an error.
	Errs []error
}

// prWorktree is one argus-pr-* worktree found by Cleanup.
type prWorktree struct {
	clone string
	dir   string
	ref   gh.PRRef
}

// Cleanup removes the argus-pr-* worktrees whose PR is merged or closed and
// whose tree is clean. It never removes a worktree with changes, and it
// uses `git worktree remove` without --force. ctx stops the state query.
// The git commands run to the end, so that a remove does not stop halfway.
func (w *Worktrees) Cleanup(ctx context.Context, states StateFetcher) (CleanupResult, error) {
	var res CleanupResult
	gitCtx := context.WithoutCancel(ctx)
	var found []prWorktree
	for _, c := range w.clonesWithWorktrees() {
		wts, err := w.prWorktrees(gitCtx, c.dir, c.repo)
		if err != nil {
			res.Errs = append(res.Errs, err)
			continue
		}
		found = append(found, wts...)
	}
	if len(found) == 0 {
		return res, nil
	}

	refs := make([]gh.PRRef, len(found))
	for i, wt := range found {
		refs[i] = wt.ref
	}
	got, err := states.PRStates(ctx, refs)
	if err != nil {
		return res, fmt.Errorf("get PR states: %w", err)
	}
	for _, wt := range found {
		if s := got[wt.ref]; s != gh.PRStateMerged && s != gh.PRStateClosed {
			continue
		}
		clean, err := w.isClean(gitCtx, wt.dir)
		if err != nil {
			res.Errs = append(res.Errs, err)
			continue
		}
		if !clean {
			res.Dirty = append(res.Dirty, wt.dir)
			continue
		}
		if _, err := w.git(gitCtx, wt.clone, "worktree", "remove", wt.dir); err != nil {
			res.Errs = append(res.Errs, err)
			continue
		}
		res.Removed = append(res.Removed, wt.dir)
	}
	return res, nil
}

// cloneRef is a clone directory and its repository.
type cloneRef struct {
	dir  string
	repo model.Repo
}

// clonesWithWorktrees returns the clones from Repos and from
// <RepoRoot>/<owner>/<repo> that have a .worktrees directory.
func (w *Worktrees) clonesWithWorktrees() []cloneRef {
	var out []cloneRef
	seen := map[string]bool{}
	add := func(dir string, repo model.Repo) {
		key := resolve(dir)
		if seen[key] {
			return
		}
		if info, err := os.Stat(filepath.Join(dir, worktreesDir)); err != nil || !info.IsDir() {
			return
		}
		seen[key] = true
		out = append(out, cloneRef{dir: dir, repo: repo})
	}
	for name, dir := range w.Repos {
		owner, repo, ok := strings.Cut(name, "/")
		if ok && owner != "" && repo != "" {
			add(dir, model.Repo{Owner: owner, Name: repo})
		}
	}
	if w.RepoRoot != "" {
		matches, _ := filepath.Glob(filepath.Join(w.RepoRoot, "*", "*", worktreesDir))
		for _, m := range matches {
			dir := filepath.Dir(m)
			add(dir, model.Repo{Owner: filepath.Base(filepath.Dir(dir)), Name: filepath.Base(dir)})
		}
	}
	return out
}

// prWorktrees returns the registered worktrees of the clone whose
// directory is <clone>/.worktrees/argus-pr-<n>.
func (w *Worktrees) prWorktrees(ctx context.Context, clone string, repo model.Repo) ([]prWorktree, error) {
	listed, err := w.listWorktrees(ctx, clone)
	if err != nil {
		return nil, err
	}
	parent := resolve(filepath.Join(clone, worktreesDir))
	var out []prWorktree
	for _, path := range listed {
		m := worktreeName.FindStringSubmatch(filepath.Base(path))
		if m == nil || resolve(filepath.Dir(path)) != parent {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out = append(out, prWorktree{
			clone: clone,
			dir:   path,
			ref:   gh.PRRef{Owner: repo.Owner, Repo: repo.Name, Number: n},
		})
	}
	return out, nil
}

// listWorktrees returns the worktree paths of the clone from
// `git worktree list --porcelain`, in the order that git prints them.
func (w *Worktrees) listWorktrees(ctx context.Context, clone string) ([]string, error) {
	out, err := w.git(ctx, clone, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for line := range strings.SplitSeq(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// isClean reports whether the worktree has no modified, staged, or
// untracked files. The flag overrides a status.showUntrackedFiles config.
func (w *Worktrees) isClean(ctx context.Context, dir string) (bool, error) {
	out, err := w.git(ctx, dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// git runs git in dir.
func (w *Worktrees) git(ctx context.Context, dir string, args ...string) (string, error) {
	return w.run(ctx, "git", append([]string{"-C", dir}, args...)...)
}

// run runs the command and turns a non-zero exit code into an error.
func (w *Worktrees) run(ctx context.Context, name string, args ...string) (string, error) {
	stdout, stderr, code, err := w.Runner.Run(ctx, name, args...)
	cmd := name + " " + strings.Join(args, " ")
	if err != nil {
		return "", fmt.Errorf("run %s: %w", cmd, err)
	}
	if code != 0 {
		return "", fmt.Errorf("%s exited with code %d: %s", cmd, code, strings.TrimSpace(stderr))
	}
	return stdout, nil
}

// resolve returns the absolute path with the symbolic links resolved. It
// returns the cleaned path if the path does not exist.
func resolve(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
