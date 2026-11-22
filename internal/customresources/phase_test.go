package customresources

import (
	"slices"
	"testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/resources"
)

// A `.status.phase` column grades the three words the Kubernetes convention
// gives a meaning to, and nothing else in a custom row is graded: not another
// word in that column, not the same word in another column, not a word spelt
// differently.
func TestOnlyTheConventionalPhaseWordsOfAStatusPhaseColumnTakeATone(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, widgetCRD),
		widget(t, "failed", "apps", `{"status": {"phase": "Failed"}}`),
		widget(t, "succeeded", "apps", `{"status": {"phase": "Succeeded"}}`),
		widget(t, "pending", "apps", `{"status": {"phase": "Pending"}}`),
		widget(t, "running", "apps", `{"status": {"phase": "Running"}}`),
		widget(t, "lowercase", "apps", `{"status": {"phase": "failed"}}`),
		widget(t, "elsewhere", "apps", `{"status": {"ready": "Failed"}}`),
	)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	titles := make([]string, 0, len(table.Columns))
	for _, c := range table.Columns {
		titles = append(titles, c.Title)
	}
	phase, ready := slices.Index(titles, "Phase"), slices.Index(titles, "Ready")
	if phase < 0 || ready < 0 {
		t.Fatalf("the fixture's Phase and Ready columns are not in the table: %v", titles)
	}

	rows := map[string]resources.Row{}
	for _, row := range table.Rows {
		rows[row.Name] = row
	}

	cases := []struct {
		name, column string
		at           int
		text, class  string
		why          string
	}{
		{"failed", "Phase", phase, "Failed", resources.ClassBad, "the convention's failure word is the failure tone"},
		{"succeeded", "Phase", phase, "Succeeded", resources.ClassOK, "the convention's success word is the success tone"},
		{"pending", "Phase", phase, "Pending", resources.ClassNone, "pending is neutral: in flight, not a fault"},
		{"running", "Phase", phase, "Running", resources.ClassNone, "no other word of the column is graded"},
		{"lowercase", "Phase", phase, "failed", resources.ClassNone, "a word spelt otherwise is the controller's own"},
		{"elsewhere", "Ready", ready, "Failed", resources.ClassNone, "the word outside `.status.phase` means nothing known"},
	}
	for _, c := range cases {
		row, ok := rows[c.name]
		if !ok {
			t.Errorf("%s: no row", c.name)
			continue
		}
		cell := row.Cells[c.at]
		if cell.Text != c.text || cell.Class != c.class {
			t.Errorf("%s: the %s cell is %q in class %q, want %q in class %q — %s",
				c.name, c.column, cell.Text, cell.Class, c.text, c.class, c.why)
		}
		if cell.Rank != 0 {
			t.Errorf("%s: the %s cell claims rank %d; a custom cell carries a tone and no rank",
				c.name, c.column, cell.Rank)
		}
		for i, other := range row.Cells {
			if i != phase && other.Class != resources.ClassNone {
				t.Errorf("%s: cell %d (%q) is graded %q; only the phase column is", c.name, i, other.Text, other.Class)
			}
		}
	}

	// The tone moves nothing: the rows still read by name, a failed one included.
	names := make([]string, 0, len(table.Rows))
	for _, row := range table.Rows {
		names = append(names, row.Name)
	}
	if !slices.IsSorted(names) {
		t.Errorf("custom rows no longer sort by name: %v", names)
	}
}
