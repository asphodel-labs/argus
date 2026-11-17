package review

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asphodel-labs/argus/internal/gh"
)

var update = flag.Bool("update", false, "write the golden files")

// TestParseAnnotationsGolden parses each testdata/<name>.md and compares
// the threads with testdata/<name>.golden.json.
func TestParseAnnotationsGolden(t *testing.T) {
	for _, name := range []string{"line_added", "line_removed", "range", "file_level", "multiline", "mixed", "empty"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", name+".md"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			threads, err := ParseAnnotations(f)
			if err != nil {
				t.Fatal(err)
			}
			if threads == nil {
				threads = []gh.ReviewThreadInput{}
			}
			got, err := json.MarshalIndent(threads, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("threads:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// TestParseAnnotationsRecords checks the fields of each record type
// without the golden files.
func TestParseAnnotationsRecords(t *testing.T) {
	in := "## a.go:5 (+)\nadded\n\n## a.go:6 (-)\nremoved\n\n## a.go:2-4 (+)\nrange\n\n" +
		"## a.go:9-9 (-)\none-line range\n\n## dir/b c.go (file-level)\nfile\n\n## x:y.go:3 (+)\ncolon\n"
	got, err := ParseAnnotations(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []gh.ReviewThreadInput{
		{Path: "a.go", Line: 5, Side: gh.SideRight, Body: "added"},
		{Path: "a.go", Line: 6, Side: gh.SideLeft, Body: "removed"},
		{Path: "a.go", StartLine: 2, Line: 4, Side: gh.SideRight, Body: "range"},
		{Path: "a.go", Line: 9, Side: gh.SideLeft, Body: "one-line range"},
		{Path: "dir/b c.go", File: true, Body: "file"},
		{Path: "x:y.go", Line: 3, Side: gh.SideRight, Body: "colon"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("threads:\n got %+v\nwant %+v", got, want)
	}
}

func TestParseAnnotationsEscapedHeader(t *testing.T) {
	in := "## a.go:1 (+)\n ## kept as text\n  ## two spaces stay\n## b.go:2 (+)\nnext\n"
	got, err := ParseAnnotations(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d threads, want 2: %+v", len(got), got)
	}
	if want := "## kept as text\n  ## two spaces stay"; got[0].Body != want {
		t.Fatalf("body %q, want %q", got[0].Body, want)
	}
}

func TestParseAnnotationsMalformed(t *testing.T) {
	for name, in := range map[string]string{
		"no side":            "## a.go:5\nbody\n",
		"bad side":           "## a.go:5 (*)\nbody\n",
		"no line":            "## a.go (+)\nbody\n",
		"line zero":          "## a.go:0 (+)\nbody\n",
		"reversed range":     "## a.go:9-3 (+)\nbody\n",
		"open range":         "## a.go:3- (+)\nbody\n",
		"empty path":         "## :3 (+)\nbody\n",
		"bad file-level":     "## a.go (file)\nbody\n",
		"text before header": "stray text\n## a.go:1 (+)\nbody\n",
		"later bad header":   "## a.go:1 (+)\nbody\n## oops\n",
		"line overflow":      "## a.go:99999999999999999999 (+)\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseAnnotations(strings.NewReader(in))
			if err == nil {
				t.Fatalf("no error, threads %+v", got)
			}
			if !strings.Contains(err.Error(), "annotations line ") {
				t.Fatalf("error %q has no line number", err)
			}
		})
	}
}

func TestParseAnnotationsLeadingBlankLines(t *testing.T) {
	got, err := ParseAnnotations(strings.NewReader("\n\n## a.go:1 (+)\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "body" {
		t.Fatalf("threads %+v", got)
	}
}

// testDiff has a modified file with two hunks, a deleted file, a new file
// with a space in the name, and a removed line that looks like a "---"
// header inside a hunk.
var testDiff = strings.ReplaceAll(`diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -10,7 +10,8 @@ func f() {
 ctx1
 ctx2
 ctx3
--- old dash line
+new line
+++ added plus line
 ctx4
 ctx5
 ctx6
@@ -40,3 +41,2 @@
 c1
-gone
 c2
diff --git a/old.go b/old.go
deleted file mode 100644
index 3333333..0000000
--- a/old.go
+++ /dev/null
@@ -1,2 +0,0 @@
-x
-y
diff --git a/new file.go b/new file.go
new file mode 100644
index 0000000..4444444
--- /dev/null
+++ b/new file.go<TAB>
@@ -0,0 +1 @@
+only
\ No newline at end of file
diff --git a/img.png b/img.png
index 5555555..6666666 100644
Binary files a/img.png and b/img.png differ
`, "<TAB>", "\t")

func TestParseHunks(t *testing.T) {
	h, err := ParseHunks(testDiff)
	if err != nil {
		t.Fatal(err)
	}
	want := Hunks{
		"a.go":        {left: []lineRange{{10, 16}, {40, 42}}, right: []lineRange{{10, 17}, {41, 42}}},
		"old.go":      {left: []lineRange{{1, 2}}},
		"new file.go": {right: []lineRange{{1, 1}}},
	}
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("hunks:\n got %+v\nwant %+v", h, want)
	}
}

func TestParseHunksQuotedPath(t *testing.T) {
	diff := "diff --git \"a/t\\tab.go\" \"b/t\\tab.go\"\n--- \"a/t\\tab.go\"\n+++ \"b/t\\tab.go\"\n@@ -1 +1 @@\n-a\n+b\n"
	h, err := ParseHunks(diff)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h["t\tab.go"]; !ok {
		t.Fatalf("hunks %+v", h)
	}
}

func TestParseHunksErrors(t *testing.T) {
	for name, diff := range map[string]string{
		"bad hunk header":   "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -x +1 @@\n",
		"hunk without file": "@@ -1 +1 @@\n-a\n+b\n",
		"bad line in hunk":  "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,2 +1,2 @@\n a\n?b\n",
		"bad prefix":        "diff --git a/a b/a\n--- x/a\n+++ b/a\n",
		"bad quote":         "diff --git a/a b/a\n--- a/a\n+++ \"b/a\n",
	} {
		t.Run(name, func(t *testing.T) {
			if h, err := ParseHunks(diff); err == nil {
				t.Fatalf("no error, hunks %+v", h)
			}
		})
	}
}

func TestHunkCheck(t *testing.T) {
	h, err := ParseHunks(testDiff)
	if err != nil {
		t.Fatal(err)
	}
	right := func(line int) gh.ReviewThreadInput {
		return gh.ReviewThreadInput{Path: "a.go", Line: line, Side: gh.SideRight, Body: "b"}
	}
	for _, tc := range []struct {
		name   string
		thread gh.ReviewThreadInput
		inside bool
	}{
		{"inside a hunk", right(12), true},
		{"first line of a hunk", right(10), true},
		{"last line of a hunk", right(17), true},
		{"outside every hunk", right(30), false},
		{"between the sides", right(18), false},
		{"range inside a hunk", gh.ReviewThreadInput{Path: "a.go", StartLine: 11, Line: 15, Side: gh.SideRight}, true},
		{"range across a hunk edge", gh.ReviewThreadInput{Path: "a.go", StartLine: 15, Line: 20, Side: gh.SideRight}, false},
		{"range across two hunks", gh.ReviewThreadInput{Path: "a.go", StartLine: 12, Line: 41, Side: gh.SideRight}, false},
		{"LEFT line inside", gh.ReviewThreadInput{Path: "a.go", Line: 42, Side: gh.SideLeft}, true},
		{"LEFT line only in the RIGHT hunk", gh.ReviewThreadInput{Path: "a.go", Line: 17, Side: gh.SideLeft}, false},
		{"LEFT line of a deleted file", gh.ReviewThreadInput{Path: "old.go", Line: 2, Side: gh.SideLeft}, true},
		{"RIGHT line of a deleted file", gh.ReviewThreadInput{Path: "old.go", Line: 1, Side: gh.SideRight}, false},
		{"file not in the diff", gh.ReviewThreadInput{Path: "other.go", Line: 1, Side: gh.SideRight}, false},
		{"file-level comment", gh.ReviewThreadInput{Path: "a.go", File: true}, true},
		{"file-level comment on a file not in the diff", gh.ReviewThreadInput{Path: "other.go", File: true}, true},
		{"binary file line", gh.ReviewThreadInput{Path: "img.png", Line: 1, Side: gh.SideRight}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.Contains(tc.thread); got != tc.inside {
				t.Fatalf("Contains = %v, want %v", got, tc.inside)
			}
		})
	}
}

func TestSplitOutsideAndMovedBody(t *testing.T) {
	h, err := ParseHunks(testDiff)
	if err != nil {
		t.Fatal(err)
	}
	threads := []gh.ReviewThreadInput{
		{Path: "a.go", Line: 12, Side: gh.SideRight, Body: "in"},
		{Path: "a.go", Line: 30, Side: gh.SideRight, Body: "out right"},
		{Path: "a.go", StartLine: 15, Line: 20, Side: gh.SideRight, Body: "out range\nsecond line"},
		{Path: "a.go", Line: 5, Side: gh.SideLeft, Body: "out left"},
		{Path: "other.go", File: true, Body: "file"},
	}
	inside, outside := SplitOutside(threads, h)
	if !reflect.DeepEqual(inside, []gh.ReviewThreadInput{threads[0], threads[4]}) {
		t.Fatalf("inside %+v", inside)
	}
	if !reflect.DeepEqual(outside, threads[1:4]) {
		t.Fatalf("outside %+v", outside)
	}
	want := "a.go:30 — out right\n\na.go:15-20 — out range\nsecond line\n\na.go:5 (-) — out left"
	if got := MovedBody(outside); got != want {
		t.Fatalf("body:\n%q\nwant\n%q", got, want)
	}
	if got := MovedBody(nil); got != "" {
		t.Fatalf("empty body %q", got)
	}
}

func TestWorktreesHunks(t *testing.T) {
	r := &fakeRunner{}
	line := "git -C /wt -c core.quotePath=false diff --no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/ --find-renames --unified=3 mb head"
	r.set(line, reply{stdout: testDiff})
	w := &Worktrees{Runner: r}
	h, err := w.Hunks(context.Background(), "/wt", "mb", "head")
	if err != nil {
		t.Fatal(err)
	}
	wantCalls(t, r.calls, line)
	if len(h) != 3 {
		t.Fatalf("hunks %+v", h)
	}

	r.set(line, reply{code: 128, stderr: "bad revision"})
	if _, err := w.Hunks(context.Background(), "/wt", "mb", "head"); err == nil {
		t.Fatal("no error for a failed git diff")
	}
}

// TestWorktreesHunksRealGit runs Hunks against real git with a user config
// that changes the diff prefixes and the path quoting.
func TestWorktreesHunksRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "diff.noprefix", "true")
	gitRun(t, dir, "config", "core.quotePath", "true")
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "line")
	}
	writeFile(t, filepath.Join(dir, "a.txt"), strings.Join(lines, "\n")+"\n")
	writeFile(t, filepath.Join(dir, "ü.txt"), "x\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	base := gitRun(t, dir, "rev-parse", "HEAD")
	lines[19] = "changed"
	writeFile(t, filepath.Join(dir, "a.txt"), strings.Join(lines, "\n")+"\n")
	writeFile(t, filepath.Join(dir, "ü.txt"), "y\n")
	gitRun(t, dir, "commit", "-q", "-am", "head")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	w := &Worktrees{Runner: gitExec{t}}
	h, err := w.Hunks(context.Background(), dir, base, head)
	if err != nil {
		t.Fatal(err)
	}
	want := Hunks{
		"a.txt": {left: []lineRange{{17, 23}}, right: []lineRange{{17, 23}}},
		"ü.txt": {left: []lineRange{{1, 1}}, right: []lineRange{{1, 1}}},
	}
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("hunks:\n got %+v\nwant %+v", h, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
