package textutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// moduleRoot finds the directory holding go.mod by walking up from this test's
// own directory, rather than counting "..".
//
// Counting breaks silently when the package moves: one level too many and the
// walk scans a directory with no .go files in it, reports nothing and passes -
// a guard test that guards nothing.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving this test's directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s - this test cannot know what tree to scan", dir)
		}
		dir = parent
	}
}

func TestFirstLineCutsBothKindsOfLineBreak(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"unix":            {"boom\nstack frame", "boom"},
		"windows":         {"boom\r\nstack frame", "boom"},
		"carriage only":   {"boom\rstack frame", "boom"},
		"no break at all": {"boom", "boom"},
		"leading space":   {"  boom\nrest", "boom"},
		"empty":           {"", ""},
	} {
		if got := FirstLine(c.in); got != c.want {
			t.Errorf("%s: FirstLine(%q) = %q, want %q — the copies that looked only for \n "+
				"passed a Windows-authored error through to a table cell whole", name, c.in, got, c.want)
		}
	}
}

// Slicing bytes would cut a CJK or emoji-bearing message mid-character, and the
// recorder would store it that way.
func TestClipCountsRunesAndNotBytes(t *testing.T) {
	const cjk = "这是一个很长的消息需要被截断"
	got := Clip(cjk, 5)
	if got != "这是一个"+Ellipsis {
		t.Errorf("Clip(cjk, 5) = %q, want four runes and an ellipsis — slicing bytes here is what "+
			"wrote a replacement glyph into the archive that get_event_history reads back", got)
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("Clip produced a replacement character: %q", got)
		}
	}
}

// The ellipsis is inside the budget, never one character past it.
func TestClipKeepsItsOwnBound(t *testing.T) {
	for _, max := range []int{1, 2, 5, 12} {
		got := []rune(Clip("abcdefghijklmnopqrstuvwxyz", max))
		if len(got) > max {
			t.Errorf("Clip(_, %d) returned %d runes (%q) — a column sized to its bound then "+
				"overflows by one", max, len(got), string(got))
		}
	}
}

func TestClipLeavesShortStringsAlone(t *testing.T) {
	if got := Clip("short", 20); got != "short" {
		t.Errorf("Clip added something to a string under its bound: %q", got)
	}
	if got := Clip("anything at all", 0); got != "anything at all" {
		t.Errorf("a max of 0 must mean no ceiling so a caller can pass a configured bound "+
			"through without branching; got %q", got)
	}
}

// The composition every caller actually wants, and the reason both halves exist.
func TestFirstLineNIsBothHalves(t *testing.T) {
	got := FirstLineN("  a very long first line indeed\r\nand a second one", 10)
	if got != "a very lo"+Ellipsis {
		t.Errorf("FirstLineN = %q — it must trim, cut at the break, and then bound", got)
	}
}

// TestNothingReimplementsFirstLine guards that nothing re-implements FirstLine.
//
// Copies disagree about the case that matters: one cutting on "\n" alone
// returns a webhook denial arriving as "denied by webhook\rsecond line"
// **intact**, and it renders as one run-on line in a table cell that is
// `white-space: nowrap`. So any `func firstLine` must be a one-line forwarder to
// FirstLineN. The test reads source rather than behavior on purpose: the
// behavior of a stray copy is exactly what nobody would notice until an error
// message came back whole.
func TestNothingReimplementsFirstLine(t *testing.T) {
	root := moduleRoot(t)

	// A forwarder is one line: `func firstLine(s string) string { return
	// textutil.FirstLineN(...) }`. Anything with a body of its own is a
	// reimplementation, whatever it happens to do today.
	decl := regexp.MustCompile(`func firstLine\(s string\) string \{([^}]*)\}`)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			body := strings.TrimSpace(m[1])
			if strings.HasPrefix(body, "return textutil.FirstLine") {
				continue
			}
			offenders = append(offenders, path+": "+body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	for _, o := range offenders {
		t.Errorf("a package implements firstLine itself instead of forwarding to textutil; a "+
			"copy that disagrees is how %q comes back whole:\n  %s", "denied by webhook\rsecond line", o)
	}
}

// TestNothingCutsAtTheFirstNewlineItself is the same guard looking at what a
// function *does* rather than at what it is called.
//
// The test above matches a name, and **a check that matches a name only stops
// the copies that keep the name**: a `shortError(err error) string` cutting on
// '\n' alone would pass it.
//
// So this one looks for the operation instead. Taking everything before the
// first '\n' by hand is what every divergent copy has in common, and
// nothing in this tree has a reason to write it, because textutil is one import
// away and treats a bare '\r' as a line break too.
func TestNothingCutsAtTheFirstNewlineItself(t *testing.T) {
	root := moduleRoot(t)

	// The two spellings of "everything before the first line break". The `\\n` in
	// each is a literal backslash-n, because that is what the source file holds.
	cut := regexp.MustCompile(`strings\.(IndexByte\([^,]+, '\\n'\)|Cut\([^,]+, "\\n"\))`)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// textutil itself is where this operation belongs.
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "textutil" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range cut.FindAllString(string(src), -1) {
			offenders = append(offenders, path+": "+m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	for _, o := range offenders {
		t.Errorf("something takes the text before the first newline by hand. FirstLine and "+
			"FirstLineN exist for it, they treat a bare carriage return as a line break too, "+
			"and they cut runes rather than bytes. Whatever it is called, it is copy fourteen:"+
			"\n  %s", o)
	}
}

// TestTheNewlineGuardWouldHaveCaughtCopyFourteen. A walker over a clean tree
// passes whether or not its pattern works, so the pattern is checked against the
// text it exists to find rather than against the tree it happens to run on.
func TestTheNewlineGuardWouldHaveCaughtCopyFourteen(t *testing.T) {
	cut := regexp.MustCompile(`strings\.(IndexByte\([^,]+, '\\n'\)|Cut\([^,]+, "\\n"\))`)

	// A hand-written cut on '\n', the shape the guard exists to find.
	const copyFourteen = "\tif i := strings.IndexByte(text, '\\n'); i >= 0 {\n\t\ttext = text[:i]\n\t}"
	if !cut.MatchString(copyFourteen) {
		t.Errorf("the guard does not match the copy it was written for:\n%s", copyFourteen)
	}
	const withCut = `before, _, _ := strings.Cut(text, "\n")`
	if !cut.MatchString(withCut) {
		t.Errorf("the guard does not match the other spelling:\n%s", withCut)
	}
	// And it must not fire on the ordinary things. Splitting into all lines is a
	// different operation and is nobody's copy of FirstLine.
	for _, ordinary := range []string{
		`lines := strings.Split(text, "\n")`,
		`strings.TrimSuffix(text, "\n")`,
		`strings.Contains(text, "\n")`,
	} {
		if cut.MatchString(ordinary) {
			t.Errorf("the guard fires on ordinary code, which is how a guard comes to be deleted:\n%s", ordinary)
		}
	}
}

// And the case the copies disagreed about, asserted here so the sentence above is
// backed by a fact rather than by a story.
func TestTheCaseTheThirteenCopiesDisagreedAbout(t *testing.T) {
	const webhook = "denied by webhook\rsecond line"

	if got := FirstLine(webhook); got != "denied by webhook" {
		t.Errorf("FirstLine(%q) = %q — a bare carriage return is a line break, and the copies that "+
			"only looked for \n returned the whole thing", webhook, got)
	}
	// The cap is the other half. `.grid td` is `white-space: nowrap`, so an
	// uncapped refusal does not wrap — it widens the table past its panel and
	// `overflow: hidden` takes the rest.
	long := strings.Repeat("x", 4000)
	if got := FirstLineN(long, 200); len([]rune(got)) > 200 {
		t.Errorf("FirstLineN capped a 4 000-character refusal at %d runes", len([]rune(got)))
	}
}

// The three inputs a byte formatter is easiest to get wrong, asserted as values
// rather than as "every caller calls the same function".
//
// A test that only checks the callers delegate is the shape that lets a
// behavior change ship: it passes whatever the one implementation does. These
// three numbers are the ones that tell implementations apart, so they are the
// ones worth writing down.
func TestBytesAtTheSizesTheFourCopiesDisagreedOn(t *testing.T) {
	const (
		petabyte = int64(1) << 50 // 1125899906842624 — the one that panicked
		eightGiB = int64(8) << 30 // 8589934592 — "8 GiB" or "8.0 GiB"
		negative = -(int64(1) << 40)
	)

	for _, c := range []struct {
		what           string
		in             int64
		plain, compact string
	}{
		// An uncapped exponent would index past the unit table. That does not
		// return the wrong string, it panics.
		{"a petabyte", petabyte, "1024.0 TiB", "1024 TiB"},
		// Bytes keeps the zero and BytesCompact trims it. Both are spellable,
		// which is the point.
		{"eight gibibytes", eightGiB, "8.0 GiB", "8 GiB"},
		// A negative size is clamped, never printed raw.
		{"a negative size", negative, "0 B", "0 B"},
		// And the ordinary cases the existing callers' tables already assert, so
		// this file fails too if the shared one moves under them.
		{"under a kibibyte", 512, "512 B", "512 B"},
		{"one below the boundary", 1023, "1023 B", "1023 B"},
		{"the boundary", 1024, "1.0 KiB", "1 KiB"},
		{"a megabyte", 1 << 20, "1.0 MiB", "1 MiB"},
		{"three and a half gibibytes", 3*(1<<30) + (1 << 29), "3.5 GiB", "3.5 GiB"},
		{"nothing", 0, "0 B", "0 B"},
	} {
		if got := bytesOrPanic(Bytes, c.in); got != c.plain {
			t.Errorf("Bytes(%d) — %s — = %q, want %q", c.in, c.what, got, c.plain)
		}
		if got := bytesOrPanic(BytesCompact, c.in); got != c.compact {
			t.Errorf("BytesCompact(%d) — %s — = %q, want %q", c.in, c.what, got, c.compact)
		}
	}
}

// bytesOrPanic reports a panic as a value.
//
// A panic inside a test aborts the whole binary, so the copy that ran off the end
// of its units would have taken every other case's verdict with it and named only
// the first table row. Recovering here means the petabyte row reads as one wrong
// answer, which is what it is.
func bytesOrPanic(f func(int64) string, n int64) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("panicked: %v", r)
		}
	}()
	return f(n)
}

// A size past the largest unit must come back as a large number of the largest
// unit, and must come back at all.
//
// Separate from the table because the failure mode being pinned is a panic and
// not a wrong string: a table case that panics fails the whole test function and
// says nothing about which input did it.
func TestBytesDoesNotRunOffTheEndOfItsUnits(t *testing.T) {
	for _, n := range []int64{1 << 50, 1 << 59, 1<<62 - 1} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Bytes(%d) panicked with %v. That is the internal/web copy's bug: "+
						"an uncapped exponent indexing a four-element slice", n, r)
				}
			}()

			if got := bytesOrPanic(Bytes, n); !strings.HasSuffix(got, " TiB") {
				t.Errorf("Bytes(%d) = %q, want the largest unit this formatter has", n, got)
			}
		}()
	}
}

// What stops copy five.
//
// Four packages wrote this function, and the one that had no exponent cap is the
// one that crashed. So the rule is the same one FirstLine got: a package may keep
// a local name for the call sites, but its body must be a single delegation. This
// reads source rather than behavior on purpose — a fifth copy would pass every
// behavioral test in its own package.
func TestNothingReimplementsBytes(t *testing.T) {
	root := moduleRoot(t)

	decl := regexp.MustCompile(`func (?:humanBytes|formatBytes)\((\w+) int64\) string \{([^}]*)\}`)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			body := strings.TrimSpace(m[2])
			if strings.HasPrefix(body, "return textutil.Bytes") {
				continue
			}
			offenders = append(offenders, path+": "+body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	for _, o := range offenders {
		t.Errorf("a package formats bytes itself instead of forwarding to textutil. That is how "+
			"there came to be four of them, one of which panicked on 1 PiB and one of which "+
			"printed a negative size raw:\n  %s", o)
	}
}
