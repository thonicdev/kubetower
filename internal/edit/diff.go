package edit

import (
	"strconv"
	"strings"
)

// The lines that will change, shown before anything is sent.
//
// "Are you sure?" teaches people to say yes; a confirmation that says what
// will change gets read. The diff and the patch are computed from the same
// two documents, so a mismatch between screen and payload shows up here
// rather than in a cluster.

// Mark is what a diff line is.
type Mark string

const (
	MarkSame   Mark = " "
	MarkAdd    Mark = "+"
	MarkRemove Mark = "-"
	// MarkGap stands for a run of unchanged lines that was left out, with the
	// count, because "…" alone reads as a rendering failure.
	MarkGap Mark = "…"
)

// DiffLine is one row of the confirmation.
type DiffLine struct {
	Mark Mark
	Text string
	// Old and New are the 1-based line numbers on each side, 0 where the line
	// does not exist on that side.
	Old, New int
}

// Added reports whether this line is one the save introduces, so the
// template need not compare strings to pick a color.
func (d DiffLine) Added() bool   { return d.Mark == MarkAdd }
func (d DiffLine) Removed() bool { return d.Mark == MarkRemove }
func (d DiffLine) Gap() bool     { return d.Mark == MarkGap }

// diffContext is how many unchanged lines are kept either side of a change.
const diffContext = 3

// diffCellCap bounds the O(n·m) comparison: a ConfigMap holding a 100 KB
// nginx.conf would otherwise allocate a hundred million cells to draw a
// confirmation. Over the cap the changed region is shown as a replacement.
const diffCellCap = 4_000_000

// diffStat counts what changed, for the sentence above the diff.
type diffStat struct {
	Added   int
	Removed int
}

// diffLines compares two documents line by line.
func diffLines(before, after string) ([]DiffLine, diffStat) {
	a := splitLines(before)
	b := splitLines(after)

	// Common prefix and suffix first: on a manifest edit almost everything is
	// unchanged, and trimming keeps the table small enough to build.
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}

	midA := a[head : len(a)-tail]
	midB := b[head : len(b)-tail]

	var lines []DiffLine
	oldNo, newNo := 1, 1
	for i := 0; i < head; i++ {
		lines = append(lines, DiffLine{Mark: MarkSame, Text: a[i], Old: oldNo, New: newNo})
		oldNo++
		newNo++
	}

	var stat diffStat
	middle := middleDiff(midA, midB)
	for _, line := range middle {
		switch line.Mark {
		case MarkRemove:
			line.Old, line.New = oldNo, 0
			oldNo++
			stat.Removed++
		case MarkAdd:
			line.Old, line.New = 0, newNo
			newNo++
			stat.Added++
		default:
			line.Old, line.New = oldNo, newNo
			oldNo++
			newNo++
		}
		lines = append(lines, line)
	}

	for i := len(a) - tail; i < len(a); i++ {
		lines = append(lines, DiffLine{Mark: MarkSame, Text: a[i], Old: oldNo, New: newNo})
		oldNo++
		newNo++
	}

	return collapse(lines), stat
}

// middleDiff compares the part that is not common prefix or suffix.
func middleDiff(a, b []string) []DiffLine {
	switch {
	case len(a) == 0 && len(b) == 0:
		return nil
	case len(a) == 0:
		return allOf(b, MarkAdd)
	case len(b) == 0:
		return allOf(a, MarkRemove)
	case len(a)*len(b) > diffCellCap:
		// Too large to align line by line: show the region replaced.
		return append(allOf(a, MarkRemove), allOf(b, MarkAdd)...)
	}

	// Longest common subsequence, so a two-line change reads as two lines.
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			table[i][j] = max(table[i+1][j], table[i][j+1])
		}
	}

	var out []DiffLine
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{Mark: MarkSame, Text: a[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			out = append(out, DiffLine{Mark: MarkRemove, Text: a[i]})
			i++
		default:
			out = append(out, DiffLine{Mark: MarkAdd, Text: b[j]})
			j++
		}
	}
	out = append(out, allOf(a[i:], MarkRemove)...)
	out = append(out, allOf(b[j:], MarkAdd)...)
	return out
}

func allOf(lines []string, mark Mark) []DiffLine {
	out := make([]DiffLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, DiffLine{Mark: mark, Text: line})
	}
	return out
}

// collapse leaves out the unchanged middle of a long document.
//
// A 400-line manifest with one changed line is a confirmation nobody
// scrolls through, and so nobody reads.
func collapse(lines []DiffLine) []DiffLine {
	keep := make([]bool, len(lines))
	for i, line := range lines {
		if line.Mark == MarkSame {
			continue
		}
		for j := i - diffContext; j <= i+diffContext; j++ {
			if j >= 0 && j < len(lines) {
				keep[j] = true
			}
		}
	}

	var out []DiffLine
	skipped := 0
	for i, line := range lines {
		if keep[i] {
			if skipped > 0 {
				out = append(out, DiffLine{Mark: MarkGap, Text: gapText(skipped)})
				skipped = 0
			}
			out = append(out, line)
			continue
		}
		skipped++
	}
	if skipped > 0 {
		out = append(out, DiffLine{Mark: MarkGap, Text: gapText(skipped)})
	}
	return out
}

func gapText(count int) string {
	if count == 1 {
		return "1 unchanged line"
	}
	return strconv.Itoa(count) + " unchanged lines"
}

// splitLines drops the one trailing newline a YAML document always ends with, so
// the last line is not compared against an empty one that is not really there.
func splitLines(doc string) []string {
	doc = strings.TrimSuffix(doc, "\n")
	if doc == "" {
		return nil
	}
	return strings.Split(doc, "\n")
}
