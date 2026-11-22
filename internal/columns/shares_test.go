package columns

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real file with pixel widths, read rather than invented: ten Nodes columns
// summing to 1638, `widthsAreDeliberate: true`, and no other kind with a width.
const pixelWidths = `{
  "kinds": {
    "nodes": {
      "widths": {
        "Name": 349, "Status": 118, "Taints": 96, "CPU": 189, "Memory": 189,
        "Disk": 189, "Version": 118, "Internal IP": 152, "Instance type": 152,
        "Age": 86
      }
    }
  },
  "widthsAreDeliberate": true
}`

var nodeTitles = []string{
	"Name", "Status", "Taints", "CPU", "Memory", "Disk", "Version",
	"Internal IP", "Instance type", "Age",
}

// TestAPixelWidthFileIsReadUnchangedAndSilently: reading widths as shares needs
// no migration.
//
// Numbers measured on screen at one panel width have ratios that are already
// the arrangement the operator dragged — reading them as ratios hands back the
// table they made, now filling whatever panel it is in. **So the correct
// migration is to touch nothing**, and this test is what makes that a decision
// rather than an omission: every width comes back at the number in the file.
//
// **And nothing is said about it**: no start-up line. The widthsAreShares flag
// is still written by the next save - it is about what the numbers mean.
//
// It is the negative half of TestTheStoredWidthsAreDroppedOnceAndTheOrderIsKept,
// the migration that does drop data. Both are here so that "which of the two
// shapes is this change" is a question with two worked answers beside each
// other.
func TestAPixelWidthFileIsReadUnchangedAndSilently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(pixelWidths), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	var said []string
	store := New(dir, func(format string, args ...any) {
		said = append(said, format)
		t.Logf(format, args...)
	})

	// One: every width is the number that was in the file. Nothing scaled,
	// nothing dropped, nothing clamped — which is what "no migration" has to mean
	// if it is to mean anything.
	want := map[string]int{
		"Name": 349, "Status": 118, "Taints": 96, "CPU": 189, "Memory": 189,
		"Disk": 189, "Version": 118, "Internal IP": 152, "Instance type": 152, "Age": 86,
	}
	layout := store.Arrangement("nodes", nodeTitles)
	total := 0
	for _, c := range layout.Columns {
		if c.Width != want[c.Title] {
			t.Errorf("%s came back at %d, want %d — something converted the file", c.Title, c.Width, want[c.Title])
		}
		total += c.Width
	}
	if total != 1638 {
		t.Errorf("the ten columns sum to %d, want 1638 — which is the arrangement itself, since "+
			"a share is only meaningful beside the others", total)
	}

	// Two: nothing was said. A file carrying `widthsAreDeliberate` starts in
	// silence.
	if len(said) != 0 {
		t.Errorf("reading a file with stored widths said %d thing(s), want none: %q", len(said), said)
	}

	// Three: reading writes nothing; there is no reason for a read path to
	// write.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back: %v", err)
	}
	if string(raw) != pixelWidths {
		t.Error("reading the file rewrote it; a start with nothing to announce has nothing to write")
	}

	// Four: the next save carries both flags. `widthsAreShares` stays because it
	// is about what the data means, and `widthsAreDeliberate` dropped would make
	// the width-dropping migration run again and throw the widths away.
	if _, err := store.SetVisible("nodes", "Taints", false, false, nodeTitles); err != nil {
		t.Fatalf("hiding a column: %v", err)
	}
	var file persisted
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back: %v", err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the file is not readable JSON: %v", err)
	}
	if !file.WidthsAreShares {
		t.Error("a save did not write widthsAreShares")
	}
	if !file.WidthsAreDeliberate {
		t.Error("the widthsAreDeliberate flag was dropped, which would make that migration run again " +
			"and throw the widths away")
	}

	// And the arrangement is still there after the round trip.
	again := New(dir, func(string, ...any) {})
	for _, c := range again.Arrangement("nodes", nodeTitles).Columns {
		if c.Width != want[c.Title] {
			t.Errorf("after a restart %s is %d, want %d", c.Title, c.Width, want[c.Title])
		}
	}
}

// TestAFileWithNoFlagStillHasItsWidthsDropped is the interaction between the
// two migrations, and it is the one that is easy to get wrong in the direction
// nobody notices.
//
// A file with no flags at all has its widths dropped — they were a side effect
// of a drag posting all of them, not choices — and that migration says so in
// the log, while a file read as shares says nothing.
func TestAFileWithNoFlagStillHasItsWidthsDropped(t *testing.T) {
	dir := t.TempDir()
	raw := `{"kinds":{"pods":{"order":["Age","Name"],"widths":{"Name":349,"Age":118}}}}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	var said []string
	store := New(dir, func(format string, args ...any) { said = append(said, format) })

	dropped := false
	for _, line := range said {
		if strings.Contains(line, "dropping the stored column widths once") {
			dropped = true
		}
	}
	if !dropped {
		t.Error("the width-dropping migration did not run on a file with no flag at all")
	}
	for _, c := range store.Arrangement("pods", []string{"Name", "Age"}).Columns {
		if c.Width != 0 {
			t.Errorf("%s kept %dpx through the width-dropping migration", c.Title, c.Width)
		}
	}
}
