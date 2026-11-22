package statefile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAnOlderBinaryIsToldSoRatherThanRewritingTheState is the property the stamp
// exists for.
//
// Every store in this project loads defensively: a file it cannot parse becomes
// the defaults. That is right on its own — a corrupt file must not stop the
// console — and it means an *older* binary meeting a newer format does not
// complain, it quietly replaces the operator's arrangement with an empty one. The
// next start of the newer binary then finds that empty one and believes it.
//
// Hence a stamp, so an older binary warns loudly instead of rewriting.
func TestAnOlderBinaryIsToldSoRatherThanRewritingTheState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDir, dir)

	if err := Record("0.2.0"); err != nil {
		t.Fatalf("recording the stamp: %v", err)
	}

	recorded, older := Older("0.1.0")
	if !older {
		t.Error("0.1.0 opening a directory last written by 0.2.0 was not told it is older")
	}
	if recorded != "0.2.0" {
		t.Errorf("recorded = %q, want 0.2.0 — the caller has to be able to name the version "+
			"that wrote this, or the warning says nothing actionable", recorded)
	}

	if _, older := Older("0.2.0"); older {
		t.Error("a binary of the same version was told it is older")
	}
	if _, older := Older("0.3.0"); older {
		t.Error("a newer binary was told it is older")
	}
}

// TestADirectoryWithNoStampIsNotAProblem. A state directory written by a build
// that did not stamp it has none, and warning about those would be a wall of
// noise.
func TestADirectoryWithNoStampIsNotAProblem(t *testing.T) {
	t.Setenv(EnvDir, t.TempDir())
	if recorded, older := Older("0.1.0"); older || recorded != "" {
		t.Errorf("an unstamped directory answered (%q, %v), want (\"\", false)", recorded, older)
	}
}

// TestAnUnreadableStampIsTreatedAsAbsent. It is a diagnostic note, not a lock —
// refusing to start over a corrupt cosmetic file is the trade this whole package
// argues against.
func TestAnUnreadableStampIsTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDir, dir)
	if err := os.WriteFile(filepath.Join(dir, stampFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if recorded, older := Older("0.1.0"); older || recorded != "" {
		t.Errorf("a corrupt stamp answered (%q, %v), want it treated as absent", recorded, older)
	}
}

// TestADevelopmentBuildIsNeverTheOlderOne.
//
// A build nobody stamped with a version reports `dev`, which is not a release and
// cannot be ordered against one. Somebody running `go run ./cmd/kubetower` against
// their own state directory must not be warned about their own build — and
// inventing an order between `dev` and `0.2.0` would produce a warning with no
// action behind it.
func TestADevelopmentBuildIsNeverTheOlderOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDir, dir)
	if err := Record("0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, running := range []string{"dev", "", "0.2", "v0.1.0", "0.1.0-rc1"} {
		if _, older := Older(running); older {
			t.Errorf("%q was ordered against 0.2.0; anything that is not three numbers must "+
				"compare as no-opinion", running)
		}
	}
}

// TestTheVersionIsReadOutOfTheWholeLine. `--version` prints
// `0.2.0 (full edition abc123, built …)`, and a caller that passes the whole line
// must still be compared correctly — otherwise the stamp silently stops working
// the day somebody passes what the binary actually prints.
func TestTheVersionIsReadOutOfTheWholeLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDir, dir)
	if err := Record("0.2.0 (full edition 6843a58, built 2026-08-31)"); err != nil {
		t.Fatal(err)
	}
	if _, older := Older("0.1.0 (full edition 1111111, built 2026-08-28)"); !older {
		t.Error("a version line carrying the edition and the revision was not compared by its " +
			"version")
	}
}
