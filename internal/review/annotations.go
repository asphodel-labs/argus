package review

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/tdragon/argus/internal/gh"
)

// Record headers of the revdiff annotation output:
//
//	## path:L (+)
//	## path:L (-)
//	## path:A-B (+)
//	## path (file-level)
var (
	fileHeader = regexp.MustCompile(`^## (.+) \(file-level\)$`)
	lineHeader = regexp.MustCompile(`^## (.+):([0-9]+)(?:-([0-9]+))? \(([+-])\)$`)
)

// ParseAnnotations reads the revdiff annotation output and returns one
// review thread for each record. A body line that starts with " ## " loses
// its one leading space, which revdiff adds to escape the header form. The
// blank lines at the start and end of a body are removed.
func ParseAnnotations(r io.Reader) ([]gh.ReviewThreadInput, error) {
	var (
		threads []gh.ReviewThreadInput
		cur     *gh.ReviewThreadInput
		body    []string
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Body = joinBody(body)
		threads = append(threads, *cur)
		cur, body = nil, nil
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.HasPrefix(line, "## ") {
			t, err := parseHeader(line)
			if err != nil {
				return nil, fmt.Errorf("annotations line %d: %w", n, err)
			}
			flush()
			cur = &t
			continue
		}
		if cur == nil {
			if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("annotations line %d: text before the first record header", n)
			}
			continue
		}
		if strings.HasPrefix(line, " ## ") {
			line = line[1:]
		}
		body = append(body, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read annotations: %w", err)
	}
	flush()
	return threads, nil
}

// parseHeader returns the thread of one record header line.
func parseHeader(line string) (gh.ReviewThreadInput, error) {
	if m := fileHeader.FindStringSubmatch(line); m != nil {
		return gh.ReviewThreadInput{Path: m[1], File: true}, nil
	}
	m := lineHeader.FindStringSubmatch(line)
	if m == nil {
		return gh.ReviewThreadInput{}, fmt.Errorf("malformed record header %q", line)
	}
	t := gh.ReviewThreadInput{Path: m[1], Side: gh.SideRight}
	if m[4] == "-" {
		t.Side = gh.SideLeft
	}
	first, err := strconv.Atoi(m[2])
	if err != nil || first < 1 {
		return gh.ReviewThreadInput{}, fmt.Errorf("bad line number in record header %q", line)
	}
	t.Line = first
	if m[3] == "" {
		return t, nil
	}
	last, err := strconv.Atoi(m[3])
	if err != nil || last < first {
		return gh.ReviewThreadInput{}, fmt.Errorf("bad line range in record header %q", line)
	}
	if last > first {
		t.StartLine, t.Line = first, last
	}
	return t, nil
}

// joinBody joins the body lines without the blank lines at the start and
// the end.
func joinBody(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// lineRange is an inclusive range of line numbers.
type lineRange struct{ first, last int }

// fileHunks holds the hunk ranges of one file for each side of the diff.
type fileHunks struct {
	left, right []lineRange
}

// Hunks maps a file path to the hunk ranges of the file. The path is the
// new path of the file, or the old path of a deleted file.
type Hunks map[string]fileHunks

// Contains tells if every line of t is inside one hunk of its side. A
// file-level thread is always inside.
func (h Hunks) Contains(t gh.ReviewThreadInput) bool {
	if t.File {
		return true
	}
	fh, ok := h[t.Path]
	if !ok {
		return false
	}
	ranges := fh.right
	if t.Side == gh.SideLeft {
		ranges = fh.left
	}
	first := t.Line
	if t.StartLine != 0 {
		first = t.StartLine
	}
	for _, r := range ranges {
		if first >= r.first && t.Line <= r.last {
			return true
		}
	}
	return false
}

// hunkHeader matches "@@ -a[,b] +c[,d] @@".
var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

// ParseHunks reads the unified diff output of `git diff` and returns the
// hunk ranges of each file.
func ParseHunks(diff string) (Hunks, error) {
	hunks := Hunks{}
	var (
		oldPath, newPath string
		oldLeft, newLeft int // lines of the current hunk not read yet
	)
	for n, line := range strings.Split(diff, "\n") {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, " "), line == "":
				oldLeft--
				newLeft--
			case strings.HasPrefix(line, "-"):
				oldLeft--
			case strings.HasPrefix(line, "+"):
				newLeft--
			case strings.HasPrefix(line, `\`):
			default:
				return nil, fmt.Errorf("diff line %d: unexpected line in hunk: %q", n+1, line)
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			oldPath, newPath = "", ""
		case strings.HasPrefix(line, "--- "):
			p, err := diffPath(line[4:], "a/")
			if err != nil {
				return nil, fmt.Errorf("diff line %d: %w", n+1, err)
			}
			oldPath = p
		case strings.HasPrefix(line, "+++ "):
			p, err := diffPath(line[4:], "b/")
			if err != nil {
				return nil, fmt.Errorf("diff line %d: %w", n+1, err)
			}
			newPath = p
		case strings.HasPrefix(line, "@@ "):
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("diff line %d: malformed hunk header %q", n+1, line)
			}
			path := newPath
			if path == "" {
				path = oldPath
			}
			if path == "" {
				return nil, fmt.Errorf("diff line %d: hunk without a file", n+1)
			}
			oldStart, oldCount := hunkNumbers(m[1], m[2])
			newStart, newCount := hunkNumbers(m[3], m[4])
			fh := hunks[path]
			if oldCount > 0 {
				fh.left = append(fh.left, lineRange{oldStart, oldStart + oldCount - 1})
			}
			if newCount > 0 {
				fh.right = append(fh.right, lineRange{newStart, newStart + newCount - 1})
			}
			hunks[path] = fh
			oldLeft, newLeft = oldCount, newCount
		}
	}
	return hunks, nil
}

// hunkNumbers returns the start and the count of one side of a hunk
// header. A missing count is 1.
func hunkNumbers(start, count string) (int, int) {
	s, _ := strconv.Atoi(start)
	c := 1
	if count != "" {
		c, _ = strconv.Atoi(count)
	}
	return s, c
}

// diffPath returns the path of a "---" or "+++" line without the prefix.
// It returns "" for /dev/null. Git puts a path with special characters in
// double quotes, and adds a tab after a path with a space.
func diffPath(s, prefix string) (string, error) {
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return "", nil
	}
	if strings.HasPrefix(s, `"`) {
		u, err := strconv.Unquote(s)
		if err != nil {
			return "", fmt.Errorf("bad quoted path %s", s)
		}
		s = u
	}
	if !strings.HasPrefix(s, prefix) {
		return "", fmt.Errorf("path %q has no %q prefix", s, prefix)
	}
	return s[len(prefix):], nil
}

// Hunks runs `git diff <mergeBase> <headSha>` in the worktree and returns
// the hunk ranges. The flags fix the path prefixes, the quoting, the
// renames, and the context lines, so that the user config cannot change
// the output.
func (w *Worktrees) Hunks(ctx context.Context, worktree, mergeBase, headSha string) (Hunks, error) {
	out, err := w.git(ctx, worktree, "-c", "core.quotePath=false", "diff",
		"--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/",
		"--find-renames", "--unified=3", mergeBase, headSha)
	if err != nil {
		return nil, err
	}
	return ParseHunks(out)
}

// SplitOutside splits the threads into the threads inside the hunks and
// the line comments outside every hunk of their side. GitHub refuses a
// thread outside the diff, so the caller moves the outside comments into
// the review body with MovedBody.
func SplitOutside(threads []gh.ReviewThreadInput, hunks Hunks) (inside, outside []gh.ReviewThreadInput) {
	for _, t := range threads {
		if hunks.Contains(t) {
			inside = append(inside, t)
		} else {
			outside = append(outside, t)
		}
	}
	return inside, outside
}

// MovedBody returns the review body text of the comments outside the
// hunks: one "path:L — text" entry for each comment, with a blank line
// between the entries. A range is "path:A-B", and a LEFT-side line gets
// " (-)" after the line number.
func MovedBody(moved []gh.ReviewThreadInput) string {
	entries := make([]string, 0, len(moved))
	for _, t := range moved {
		loc := fmt.Sprintf("%s:%d", t.Path, t.Line)
		if t.StartLine != 0 {
			loc = fmt.Sprintf("%s:%d-%d", t.Path, t.StartLine, t.Line)
		}
		if t.Side == gh.SideLeft {
			loc += " (-)"
		}
		entries = append(entries, loc+" — "+t.Body)
	}
	return strings.Join(entries, "\n\n")
}
