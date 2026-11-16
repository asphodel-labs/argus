// Package state keeps the small user state of argus between runs: the
// last merge method for each repository.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/tdragon/argus/internal/model"
)

// MergeMethodsFile is the name of the file in the state directory that
// holds the last merge method for each repository.
const MergeMethodsFile = "merge-methods.json"

// Store holds the last merge method for each repository. It is safe for
// use from more than one goroutine.
type Store struct {
	path string

	mu      sync.Mutex
	methods map[string]model.MergeMethod
}

// Open reads the store from dir. A missing file gives an empty store.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, MergeMethodsFile), methods: map[string]model.MergeMethod{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read merge methods: %w", err)
	}
	if err := json.Unmarshal(data, &s.methods); err != nil {
		return nil, fmt.Errorf("parse merge methods %s: %w", s.path, err)
	}
	if s.methods == nil {
		s.methods = map[string]model.MergeMethod{}
	}
	return s, nil
}

// MergeMethod returns the last merge method for the repository
// "owner/name". ok is false if the store has no method for it.
func (s *Store) MergeMethod(repo string) (method model.MergeMethod, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	method, ok = s.methods[repo]
	return method, ok
}

// SetMergeMethod records method for the repository "owner/name" and writes
// the file with an atomic write. On a write error the method stays in
// memory.
func (s *Store) SetMergeMethod(repo string, method model.MergeMethod) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods[repo] = method
	data, err := json.MarshalIndent(s.methods, "", "  ")
	if err != nil {
		return fmt.Errorf("encode merge methods: %w", err)
	}
	return writeAtomic(s.path, data)
}

// writeAtomic writes a temporary file in the directory of path and renames
// it to path.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".merge-methods-*.json")
	if err != nil {
		return fmt.Errorf("write merge methods: %w", err)
	}
	defer os.Remove(tmp.Name()) // no effect after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write merge methods: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write merge methods: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write merge methods: %w", err)
	}
	return nil
}
