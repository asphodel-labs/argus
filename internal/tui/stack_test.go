package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/asphodel-labs/argus/internal/model"
	"github.com/asphodel-labs/argus/internal/poller"
)

// inStack puts the PR at the position of a stack of the size.
func inStack(id string, position, size int) prOpt {
	return func(p *model.PR) {
		p.Stack = model.Stack{ID: id, Number: len(id), Size: size, Position: position}
	}
}

func TestStackOrder(t *testing.T) {
	// The input is in update order, newest first. The newest PR of stack A
	// is its top PR, so the stack takes the first place.
	prs := []model.PR{
		testPR("A3", "acme/api", 3, "A top", "bob", inStack("A", 3, 3)),
		testPR("X", "acme/api", 9, "Not stacked", "bob"),
		testPR("A1", "acme/api", 1, "A bottom", "bob", inStack("A", 1, 3)),
		testPR("B2", "acme/api", 5, "B top", "bob", inStack("B", 2, 2)),
		testPR("A2", "acme/api", 2, "A middle", "bob", inStack("A", 2, 3)),
		testPR("C2", "acme/api", 7, "C alone", "bob", inStack("C", 2, 2)),
	}
	got := stackOrder(prs)
	var ids, lines []string
	for i, pr := range got {
		ids = append(ids, pr.ID)
		lines = append(lines, stackLine(got, i))
	}
	if want := []string{"A1", "A2", "A3", "X", "B2", "C2"}; !slices.Equal(ids, want) {
		t.Fatalf("order %v, want %v", ids, want)
	}
	// B and C show one PR each, so they have no connector.
	if want := []string{"┌", "├", "└", "", "", ""}; !slices.Equal(lines, want) {
		t.Fatalf("connectors %q, want %q", lines, want)
	}
}

func TestListShowsStack(t *testing.T) {
	m := mustNew(t, Options{Poller: newFakePoller(), Styles: testStyles(t), Me: "me", Now: fixedNow})
	m.Update(pollerMsg{Snapshot: &poller.Snapshot{RefreshedAt: listNow, Sections: []poller.Section{
		{Scope: scopeAcme, Total: 3, PRs: []model.PR{
			testPR("S2", "acme/api", 46, "Monthly min age", "bob", inStack("S", 2, 2), updated(10)),
			testPR("O1", "acme/api", 50, "Other change", "carol", updated(20)),
			testPR("S1", "acme/api", 42, "Hourly min age", "bob", inStack("S", 1, 2), updated(30)),
		}},
	}}})
	got := rowText(m)
	want := []string{
		"▾ Org acme (3)",
		"▾ acme/api (3)",
		"┌ ⏳ #42 [1/2] Hourly min age",
		"└ ⏳ #46 [2/2] Monthly min age",
		"⏳ #50 Other change",
	}
	if len(got) != len(want) {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("row %d = %q, want prefix %q", i, got[i], w)
		}
	}
}
