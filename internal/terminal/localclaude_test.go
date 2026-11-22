package terminal

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// The desktop window is a plain shell: it stays beside its kubeconfig and
// starts no assistant.
func TestAPlainDesktopShellKeepsItsOwnDirectory(t *testing.T) {
	req := localRequest{Cluster: "prod-eks"}

	script := string(writeLaunchScript(req, "prod-eks", `C:\tmp\s\kubeconfig`))

	if strings.Contains(script, "Set-Location") {
		t.Errorf("a plain shell was moved out of the directory holding its kubeconfig:\n%s", script)
	}
	if strings.Contains(script, "claude") {
		t.Errorf("a plain shell starts Claude:\n%s", script)
	}
}

// The pre-trust of the dock's workspaces has to be able to find Claude Code's
// config.
//
// `claudeConfigDir` must not read `$HOME`, which **is not a Windows variable** —
// set on a machine with Git for Windows and unset on one without. Reading it
// would return "" on an ordinary Windows desktop, `prepareClaudeConfig` would
// return immediately and silently, and no directory would ever be pre-trusted.
//
// **This is asserted on the mechanism and not on the environment.** Clearing
// `HOME` and expecting a directory anyway holds on Windows, where
// `os.UserHomeDir` reads `USERPROFILE`, and not on Linux, where `HOME` *is*
// the variable and clearing it correctly leaves nowhere to look: what reads as
// a fact about the code is often a fact about the host it was run on.
//
// The property that holds everywhere is that the home is resolved by the
// standard library and not by one variable's name, so that is what is read.
func TestTheClaudeConfigDirDoesNotReadHomeItself(t *testing.T) {
	source, err := os.ReadFile("claude.go")
	if err != nil {
		t.Fatalf("reading claude.go: %v", err)
	}
	text := string(source)

	start := strings.Index(text, "func claudeConfigDir()")
	if start < 0 {
		t.Fatal("claudeConfigDir is gone")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		t.Fatal("claudeConfigDir is unterminated")
	}
	body := text[start : start+end]

	if strings.Contains(body, `os.Getenv("HOME")`) {
		t.Errorf("the home is read from $HOME, which is not a Windows variable, so on a machine "+
			"without Git for Windows this returns nothing and no directory is ever "+
			"pre-trusted:\n%s", body)
	}
	if !strings.Contains(body, "os.UserHomeDir()") {
		t.Errorf("the home is not resolved by the standard library, which is the only reading "+
			"that is right on every platform:\n%s", body)
	}
}

// And the behavior, where it can be asserted without asserting the host.
//
// On Windows clearing `HOME` changes nothing, because
// `USERPROFILE` is what a Windows home is. Elsewhere `HOME` genuinely is the
// variable and there is nothing to prove — the container sets
// `CLAUDE_CONFIG_DIR` regardless, which the test below covers.
func TestOnWindowsTheConfigDirSurvivesAnUnsetHome(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("$HOME is the home variable here; there is no second one to fall back to")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", "")

	if dir := claudeConfigDir(); dir == "" {
		t.Error("with HOME unset there is nowhere to pre-accept the workspace, so Claude Code " +
			"asks whether it trusts the directory on every click and reads no notes until it " +
			"is answered")
	}
}

// And the variable still wins where it is set, which is how the container points
// this at a named volume.
func TestTheConfiguredClaudeConfigDirStillWins(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/state/claude")

	if dir := claudeConfigDir(); dir != "/state/claude" {
		t.Errorf("CLAUDE_CONFIG_DIR was ignored: %q", dir)
	}
}
