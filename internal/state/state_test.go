package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asphodel-labs/argus/internal/model"
)

func TestOpenMissingFile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "none"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.MergeMethod("acme/api"); ok {
		t.Fatal("empty store has a method")
	}
}

func TestSetAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMergeMethod("acme/api", model.MergeMethodSquash); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMergeMethod("acme/web", model.MergeMethodRebase); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMergeMethod("acme/api", model.MergeMethodMerge); err != nil {
		t.Fatal(err)
	}

	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for repo, want := range map[string]model.MergeMethod{"acme/api": model.MergeMethodMerge, "acme/web": model.MergeMethodRebase} {
		got, ok := again.MergeMethod(repo)
		if !ok || got != want {
			t.Fatalf("%s: got %q %v, want %q", repo, got, ok, want)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != MergeMethodsFile {
		t.Fatalf("state dir holds %v, want only %s", entries, MergeMethodsFile)
	}
}

func TestOpenBadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MergeMethodsFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("no error for a bad file")
	}
}

func TestOpenNullFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MergeMethodsFile), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMergeMethod("a/b", model.MergeMethodMerge); err != nil {
		t.Fatal(err)
	}
}

func TestSetWriteErrorKeepsMemory(t *testing.T) {
	// A file in place of the state directory makes the write fail.
	parent := t.TempDir()
	dir := filepath.Join(parent, "file")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{path: filepath.Join(dir, MergeMethodsFile), methods: map[string]model.MergeMethod{}}
	if err := s.SetMergeMethod("acme/api", model.MergeMethodSquash); err == nil {
		t.Fatal("no error for a write to a file path")
	}
	if got, ok := s.MergeMethod("acme/api"); !ok || got != model.MergeMethodSquash {
		t.Fatalf("got %q %v, want SQUASH in memory", got, ok)
	}
}
