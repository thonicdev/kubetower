package terminal

import (
	"path/filepath"
	"testing"
)

// The trust record goes where Claude Code reads it, which is not inside the
// config directory.
//
// Claude Code reads `~/.claude.json` at the home root, not inside
// `~/.claude`: a trust record joined onto the config directory would be
// written every session and read never, and the trust dialog would go on
// appearing on every open.
//
// # Why these are two paths and not one
//
// They genuinely differ, so this cannot be fixed by correcting a single value.
// `.credentials.json` **is** inside `~/.claude`; only the config file sits at the
// home root. `CLAUDE_CONFIG_DIR` collapses the two under one directory, which is
// why the container is not affected and a container-only test would not see it.
func TestTheClaudeTrustRecordIsNotWrittenInsideTheConfigDirectory(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	// A home this test owns, and **both** variables, because `os.UserHomeDir`
	// reads `USERPROFILE` on Windows and `HOME` everywhere else. Setting one
	// makes the test pass on one family and skip on the other, and a test that
	// skips on Windows is not covering what it is for.
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	// Taken from the same calls the code makes rather than assembled here, so
	// the assertion is about the relationship between the two paths and not
	// about this machine's layout.
	dir, file := claudeConfigDir(), claudeConfigFile()
	if dir == "" || file == "" {
		t.Fatalf("no path came back with a home set: dir=%q file=%q", dir, file)
	}

	if got, unwanted := file, filepath.Join(dir, ".claude.json"); got == unwanted {
		t.Errorf("the trust record is written to %s, inside the config directory. Claude Code "+
			"reads it at the home root, so that file is written every session and read never — "+
			"the trust dialog then appears on every dock Claude open and the CLAUDE.md this "+
			"console writes is never read.", got)
	}
	if want := filepath.Join(filepath.Dir(dir), ".claude.json"); file != want {
		t.Errorf("the config file is %s; Claude Code reads %s", file, want)
	}
}

// And `CLAUDE_CONFIG_DIR` still puts both under it, which is the container.
//
// This is the half that must not regress: the container sets that variable, so
// it has both files in one place and is the deployment where all of this
// works. A change for the desktop that moved the container's trust
// record out of its named volume would trade one silent breakage for another.
func TestCLAUDECONFIGDIRStillHoldsBothFiles(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/state/claude")

	if dir := claudeConfigDir(); dir != "/state/claude" {
		t.Errorf("the config directory is %q, not the one CLAUDE_CONFIG_DIR names", dir)
	}
	if file, want := claudeConfigFile(), filepath.Join("/state/claude", ".claude.json"); file != want {
		t.Errorf("the config file is %q, not %q. When CLAUDE_CONFIG_DIR is set, Claude Code puts "+
			"both files under it — that is why the container was never affected by the home-root "+
			"defect, and it must stay true.", file, want)
	}
}
