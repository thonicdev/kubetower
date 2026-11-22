package edit

import (
	"strings"
	"testing"
)

func render(lines []DiffLine) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(string(line.Mark))
		b.WriteString(line.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestDiffShowsTheChangedLine(t *testing.T) {
	lines, stat := diffLines("a\nb\nc\n", "a\nB\nc\n")
	if stat.Added != 1 || stat.Removed != 1 {
		t.Fatalf("want one line each way, got %+v", stat)
	}
	got := render(lines)
	if !strings.Contains(got, "-b\n") || !strings.Contains(got, "+B\n") {
		t.Fatalf("the change is not shown:\n%s", got)
	}
	if !strings.Contains(got, " a\n") {
		t.Fatalf("the context is missing:\n%s", got)
	}
}

// A 400-line manifest with one changed line is a confirmation nobody scrolls
// through, and a confirmation nobody reads is what this screen exists to avoid.
func TestDiffElidesTheUnchangedMiddle(t *testing.T) {
	var before, after []string
	for i := range 60 {
		before = append(before, "line "+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}
	after = append([]string{}, before...)
	after[30] = "changed"

	lines, _ := diffLines(strings.Join(before, "\n"), strings.Join(after, "\n"))
	if len(lines) > 20 {
		t.Fatalf("one change in sixty lines produced %d rows", len(lines))
	}

	gaps := 0
	for _, line := range lines {
		if line.Gap() {
			gaps++
			if !strings.Contains(line.Text, "unchanged") {
				t.Fatalf("a gap has to say how much it hid: %q", line.Text)
			}
		}
	}
	if gaps == 0 {
		t.Fatal("nothing was elided")
	}
}

func TestDiffNumbersBothSides(t *testing.T) {
	lines, _ := diffLines("a\nb\n", "a\nb\nc\n")
	for _, line := range lines {
		switch {
		case line.Added() && line.Old != 0:
			t.Fatalf("an added line has no line number on the old side: %+v", line)
		case line.Removed() && line.New != 0:
			t.Fatalf("a removed line has no line number on the new side: %+v", line)
		}
	}
}

func TestDiffOfIdenticalDocumentsIsEmptyOfChanges(t *testing.T) {
	lines, stat := diffLines("a\nb\n", "a\nb\n")
	if stat.Added != 0 || stat.Removed != 0 {
		t.Fatalf("want no changes, got %+v", stat)
	}
	for _, line := range lines {
		if line.Added() || line.Removed() {
			t.Fatalf("a change was invented: %+v", line)
		}
	}
}
