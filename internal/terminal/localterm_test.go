package terminal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTheSessionCarriesItsOwnKubeconfigInTheScript.
//
// **This is the defect the whole design is arranged around.** `wt.exe` does not
// become the terminal: it signals an already-running Windows Terminal, which
// creates the tab from *its own* environment. Anything put in the child process's
// `Env` is therefore dropped, silently, on every machine that already has Windows
// Terminal open — which is most of them — and the session comes up using the
// operator's default kubeconfig instead of the one this console narrowed for it.
//
// For a console whose entire premise is "you are looking at this cluster", a
// terminal quietly pointed at a different one is the worst available failure, so
// the environment lives in the script.
func TestTheSessionCarriesItsOwnKubeconfigInTheScript(t *testing.T) {
	script := string(writeLaunchScript(
		localRequest{Cluster: "prod", Namespace: "argocd", Kind: "pods", Name: "web-1"},
		"prod", `C:\Users\x\AppData\Local\Temp\desktop-123\kubeconfig`))

	if !strings.Contains(script, "$env:KUBECONFIG") {
		t.Errorf("the script does not set KUBECONFIG, so the window inherits whichever one "+
			"Windows Terminal was started with:\n%s", script)
	}
	if !strings.Contains(script, `'C:\Users\x\AppData\Local\Temp\desktop-123\kubeconfig'`) {
		t.Errorf("the session kubeconfig path is not in the script verbatim:\n%s", script)
	}
	// Single quotes, because PowerShell expands `$` and backticks inside double
	// quotes and a Windows path is exactly the kind of string that contains
	// neither until the day it does.
	if strings.Contains(script, `$env:KUBECONFIG = "`) {
		t.Errorf("the path is double-quoted, so PowerShell will expand anything in it:\n%s", script)
	}
}

// TestTheScriptChangesNothingButThisWindow. A terminal opened by clicking an icon
// in a web page must not rearrange the machine it opens on. It sets its own
// environment and runs a command; it does not touch PATH, a profile, or anything
// that outlives the window.
func TestTheScriptChangesNothingButThisWindow(t *testing.T) {
	script := string(writeLaunchScript(localRequest{Cluster: "prod"}, "prod", "kc"))

	for _, forbidden := range []string{
		"$env:PATH", // rearranging PATH for a shell somebody else owns
		"$PROFILE",  // editing the operator's PowerShell profile
		"Set-ExecutionPolicy",
		"[Environment]::SetEnvironmentVariable", // this one persists to the registry
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("the launch script contains %q, which outlives the window:\n%s", forbidden, script)
		}
	}
}

// TestTheBannerSaysWhichCluster. The point of this window is that it is pointed
// somewhere specific, and a prompt that looks like every other prompt is how
// somebody runs the right command on the wrong cluster.
func TestTheBannerSaysWhichCluster(t *testing.T) {
	script := string(writeLaunchScript(
		localRequest{Kind: "deployments", Name: "api"}, "staging-eks", "kc"))
	if !strings.Contains(script, "staging-eks") {
		t.Errorf("the banner does not name the cluster:\n%s", script)
	}
	if !strings.Contains(script, "api") {
		t.Errorf("the banner does not name what was selected:\n%s", script)
	}
}

// TestAQuoteInAPathCannotEndTheString. A context name is operator-supplied and an
// ARN is not the strangest thing in a kubeconfig. Inside a PowerShell
// single-quoted literal the only escape that exists is doubling the quote — get
// it wrong and the rest of the name is executed as script.
func TestAQuoteInAPathCannotEndTheString(t *testing.T) {
	if got := psQuote("it's here"); got != "'it''s here'" {
		t.Errorf("psQuote = %s, want 'it''s here'", got)
	}

	script := string(writeLaunchScript(localRequest{}, "cluster'; Remove-Item C:\\ -Recurse; '", "kc"))
	// The injected fragment must not survive as runnable script: every quote in
	// it is doubled, so it stays inside the literal.
	if strings.Contains(script, "Write-Host 'KubeTower — cluster'; Remove-Item") {
		t.Errorf("a quote in the cluster name escaped its literal:\n%s", script)
	}
}

// TestTheSweepTakesDesktopSessionsAndNothingElse.
//
// `sweepLocal` deletes by prefix and age, in a directory it *shares with the
// dock's live sessions*. `term-` and `claude-` directories belong to WebSocket
// sessions somebody may be typing in at that moment, and taking one would delete
// the kubeconfig out from under a running terminal.
func TestTheSweepTakesDesktopSessionsAndNothingElse(t *testing.T) {
	root := t.TempDir()
	term := &Terminal{opts: Options{WorkRoot: root}}

	old := time.Now().Add(-2 * localSessionTTL)
	mk := func(name string, age time.Time) string {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, age, age); err != nil {
			t.Fatal(err)
		}
		return path
	}

	staleDesktop := mk("desktop-abc", old)
	freshDesktop := mk("desktop-xyz", time.Now())
	staleDockShell := mk("term-abc", old)
	staleDockClaude := mk("claude-abc", old)

	term.sweepLocal()

	if _, err := os.Stat(staleDesktop); err == nil {
		t.Error("a desktop session past its TTL survived the sweep")
	}
	if _, err := os.Stat(freshDesktop); err != nil {
		t.Error("the sweep took a desktop session that is almost certainly still open")
	}
	for name, path := range map[string]string{
		"the dock's shell":  staleDockShell,
		"the dock's Claude": staleDockClaude,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the sweep deleted %s's working directory — those belong to live "+
				"WebSocket sessions and hold the kubeconfig they are using", name)
		}
	}
}

// TestOpeningADesktopTerminalRefusesAReadOnlyCluster, by reading the source.
//
// **The refusal has to be here and not only in the drawer**, and the reason is
// specific rather than defensive: the read-only guardrail is an override on the
// session's PATH, and this window is the operator's own shell, whose PATH
// KubeTower does not touch. A terminal opened on a read-only cluster would have
// the guardrail *silently* off.
//
// Read from the source because reaching the check needs a cluster manager and a
// resolvable context, and a test that built one would be testing the fixture. What
// must not happen is the check being deleted as redundant with the drawer.
func TestOpeningADesktopTerminalRefusesAReadOnlyCluster(t *testing.T) {
	source, err := os.ReadFile("localterm.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)

	open := strings.Index(body, "func (t *Terminal) openDesktop(")
	if open < 0 {
		t.Fatal("openDesktop is gone")
	}
	tail := body[open:]
	if end := strings.Index(tail, "\nfunc "); end > 0 {
		tail = tail[:end]
	}

	if !strings.Contains(tail, "t.opts.ReadOnly") {
		t.Error("openDesktop no longer consults ReadOnly. This window's PATH carries no " +
			"shims, so this check is the only thing between KT_READONLY_CLUSTERS and a " +
			"shell with the real kubectl on its PATH")
	}
	if !strings.Contains(tail, "ErrLocalTerminalReadOnly") {
		t.Error("openDesktop does not return ErrLocalTerminalReadOnly, so the console " +
			"cannot tell that refusal apart from a failure to open a window")
	}
	// And it must refuse *before* writing a kubeconfig: a refusal that has
	// already put live credentials on disk has done the dangerous half.
	readOnlyAt := strings.Index(tail, "t.opts.ReadOnly")
	kubeconfigAt := strings.Index(tail, "writeSessionKubeconfig")
	if kubeconfigAt > 0 && readOnlyAt > kubeconfigAt {
		t.Error("the read-only check happens after the session kubeconfig is written; " +
			"a refused request must not leave a credential on disk")
	}
}
