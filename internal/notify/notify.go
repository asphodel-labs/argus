// Package notify turns poller events into agterm desktop notifications and
// keeps the seen state in seen.json.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tdragon/argus/internal/execx"
	"github.com/tdragon/argus/internal/model"
	"github.com/tdragon/argus/internal/poller"
)

// MaxNotifications is the maximum number of notifications for each refresh.
// When there are more events, the last notification is one summary line.
const MaxNotifications = 5

// SessionEnv is the environment variable that agterm sets in its shells.
const SessionEnv = "AGTERM_SESSION_ID"

// SeenFile is the name of the seen state file in the state directory.
const SeenFile = "seen.json"

// Notifier compares each snapshot with the seen state, stores the new seen
// state, and sends the events. Use one Notifier from one goroutine.
type Notifier struct {
	Runner execx.Runner
	// Getenv reads the environment. It is os.Getenv when nil.
	Getenv func(string) string
	// SeenPath is the path of seen.json.
	SeenPath string
	// Me is the login of the user.
	Me string

	seen   poller.Seen
	loaded bool
}

// Process diffs snap against the seen state, writes the new seen state, and
// sends the events. On the first run with no seen.json, Process only
// records the state and sends no notifications.
func (n *Notifier) Process(ctx context.Context, snap *poller.Snapshot) error {
	next := poller.SeenFrom(snap)
	if !n.loaded {
		prev, found, err := LoadSeen(n.SeenPath)
		if err != nil {
			return err
		}
		n.loaded = true
		if !found {
			n.seen = next
			return SaveSeen(n.SeenPath, next)
		}
		n.seen = prev
	}

	events := poller.Diff(n.seen, next, n.Me)
	n.seen = next
	if err := SaveSeen(n.SeenPath, next); err != nil {
		return err
	}
	getenv := n.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return Send(ctx, n.Runner, getenv(SessionEnv), events)
}

// LoadSeen reads the seen state at path. found is false if the file does
// not exist.
func LoadSeen(path string) (seen poller.Seen, found bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read seen state: %w", err)
	}
	if err := json.Unmarshal(data, &seen); err != nil {
		return nil, false, fmt.Errorf("parse seen state %s: %w", path, err)
	}
	if seen == nil {
		seen = poller.Seen{}
	}
	return seen, true, nil
}

// SaveSeen writes the seen state to path with an atomic write: it writes a
// temporary file in the same directory and renames it to path.
func SaveSeen(path string, seen poller.Seen) error {
	data, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return fmt.Errorf("encode seen state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".seen-*.json")
	if err != nil {
		return fmt.Errorf("write seen state: %w", err)
	}
	defer os.Remove(tmp.Name()) // no effect after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write seen state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write seen state: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write seen state: %w", err)
	}
	return nil
}

// Send posts one `agtermctl notify` for each event to the agterm session
// sessionID. It posts at most MaxNotifications notifications. When there
// are more events, it posts MaxNotifications-1 events and one summary line
// for the remainder. Send does nothing when sessionID is empty, which is
// the case outside agterm.
func Send(ctx context.Context, r execx.Runner, sessionID string, events []poller.Event) error {
	if sessionID == "" || len(events) == 0 {
		return nil
	}
	type note struct{ title, body string }
	notes := make([]note, 0, MaxNotifications)
	shown := events
	if len(events) > MaxNotifications {
		shown = events[:MaxNotifications-1]
	}
	for _, ev := range shown {
		notes = append(notes, note{title: Title(ev), body: Body(ev)})
	}
	if rest := len(events) - len(shown); rest > 0 {
		notes = append(notes, note{title: "argus", body: fmt.Sprintf("%d more PR updates", rest)})
	}

	var errs []error
	for _, nt := range notes {
		_, stderr, code, err := r.Run(ctx, "agtermctl", "notify", nt.body, "--title", nt.title, "--target", sessionID)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("agtermctl notify: %w", err))
		case code != 0:
			errs = append(errs, fmt.Errorf("agtermctl notify: exit %d: %s", code, strings.TrimSpace(stderr)))
		}
	}
	return errors.Join(errs...)
}

// Title returns the notification title of ev.
func Title(ev poller.Event) string {
	switch ev.Kind {
	case poller.EventReviewRequested:
		return "Review requested"
	case poller.EventActivity:
		if ev.Author != "" {
			return "New activity by " + ev.Author
		}
		return "New activity"
	default:
		switch ev.Rollup {
		case model.CheckSuccess:
			return "CI passed"
		case model.CheckFailure:
			return "CI failed"
		default:
			return "CI error"
		}
	}
}

// Body returns the notification body of ev: "owner/repo#N title".
func Body(ev poller.Event) string {
	return fmt.Sprintf("%s#%d %s", ev.Repo, ev.Number, ev.Title)
}
