package terminal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A terminal on the operator's own desktop, instead of one drawn in the
// browser.
//
// The other terminals here are a PTY streamed over a WebSocket into xterm.js,
// which is the only way to give a shell when the server may run elsewhere. The
// desktop binary runs on the operator's own machine, so it can open a real
// terminal window (PowerShell on Windows), with the operator's own font,
// scrollback, copy-paste, profile and key bindings. It does not survive
// navigation, since it is not in the page.
//
// It must not become a hole in the read-only guardrail. The window is the
// operator's own shell: writeLaunchScript sets KUBECONFIG and nothing else, as
// rearranging their PATH would change their machine from a click in a web page.
// The overrides are therefore not in front of this session, so the console does
// not offer it on a cluster in KT_READONLY_CLUSTERS, and openDesktop refuses one
// on its own account rather than trusting the drawer.

// localRequest is one desktop terminal, as asked for by the console.
// Unexported, and the exported entry point takes plain strings, because
// internal/web declares the methods it needs rather than importing this package.
type localRequest struct {
	// Cluster is a context name already resolved by the caller. openDesktop
	// resolves it again before it reaches a command line.
	Cluster string
	// Namespace, Kind and Name are what the drawer had open. They reach the
	// session only as a banner; no command is built from them.
	Namespace string
	Kind      string
	Name      string
}

// localSessionPrefix names this feature's working directories. Not the dock's
// `term-` or `claude-`: sweepLocal deletes by prefix and age, and must never
// take a live dock session's credentials.
const localSessionPrefix = "desktop-"

// localSessionTTL is how long a desktop session's directory is kept before a
// later open sweeps it.
//
// A sweep rather than a cleanup on close: `wt.exe` hands the tab to a running
// Windows Terminal and exits at once, so nothing tells this process when the
// window closes. Twelve hours is the session cookie's life. The files are 0600
// in a directory only this account can read.
const localSessionTTL = 12 * time.Hour

// ErrLocalTerminalUnsupported is returned where there is no desktop to open a
// window on: a container, and every platform without a launcher.
var ErrLocalTerminalUnsupported = errors.New("a terminal on this machine is not available in this build")

// ErrLocalTerminalReadOnly is the guardrail refusing, whether or not the
// console drew the control.
var ErrLocalTerminalReadOnly = errors.New(
	"this cluster is read-only, and a terminal window on this machine is your own shell with " +
		"your own PATH — KubeTower sets KUBECONFIG in it and deliberately nothing else, so the " +
		"read-only overrides are not in front of kubectl there")

// LocalTerminalSupported reports whether this build can open a window at all.
// The console asks before drawing the control, and openDesktop asks again before
// acting: the first is a courtesy, the second the control.
func LocalTerminalSupported() bool { return localTerminalSupported() }

// DesktopTerminalSupported is LocalTerminalSupported as a method, for the
// interface internal/web declares.
func (t *Terminal) DesktopTerminalSupported() bool { return localTerminalSupported() }

// OpenDesktopTerminal starts a terminal window on this machine, pointed at one
// cluster. The window gets its own kubeconfig, narrowed to the context, with
// the loopback rewrite and a private AWS credential cache, as the dock's
// sessions do. KUBECONFIG is set inside the script, not in the child's
// environment; see writeLaunchScript.
func (t *Terminal) OpenDesktopTerminal(cluster, namespace, kind, name string) error {
	return t.openDesktop(localRequest{
		Cluster: cluster, Namespace: namespace, Kind: kind, Name: name,
	})
}

func (t *Terminal) openDesktop(req localRequest) error {
	if !localTerminalSupported() {
		return ErrLocalTerminalUnsupported
	}
	if strings.TrimSpace(req.Cluster) == "" {
		return errors.New("no cluster")
	}
	// Resolved rather than trusted: a context name reaches a command line from
	// here.
	name, ok := t.clusters.Resolve(req.Cluster)
	if !ok {
		return fmt.Errorf("no cluster called %q in the kubeconfig", req.Cluster)
	}
	if t.opts.ReadOnly != nil && t.opts.ReadOnly(name) {
		return ErrLocalTerminalReadOnly
	}

	// Before making a new one, so a machine that opens these all day does not
	// accumulate credential-bearing directories.
	t.sweepLocal()

	workdir, err := os.MkdirTemp(t.opts.WorkRoot, localSessionPrefix)
	if err != nil {
		return fmt.Errorf("making a working directory: %w", err)
	}
	// 0700: it is about to hold a live credential. MkdirTemp already does this;
	// setting it again states it.
	if err := os.Chmod(workdir, 0o700); err != nil {
		_ = os.RemoveAll(workdir)
		return fmt.Errorf("securing the working directory: %w", err)
	}

	kubeconfig := filepath.Join(workdir, "kubeconfig")
	if err := writeSessionKubeconfig(t.opts.Kubeconfig, name, kubeconfig, newSessionAWSHome(workdir), t.logf); err != nil {
		_ = os.RemoveAll(workdir)
		return err
	}

	// The window starts in its session directory, beside its kubeconfig.
	script := filepath.Join(workdir, launchScriptName)
	if err := os.WriteFile(script, writeLaunchScript(req, name, kubeconfig), 0o600); err != nil {
		_ = os.RemoveAll(workdir)
		return fmt.Errorf("writing the launch script: %w", err)
	}

	if err := launchTerminal(workdir, script); err != nil {
		_ = os.RemoveAll(workdir)
		return err
	}

	t.logf("opened a desktop terminal on %s (%s)", name, workdir)
	return nil
}

// sweepLocal removes desktop sessions older than the TTL. Failures are
// ignored, one directory at a time: an open window may hold its files, and a
// new terminal must never be refused because an old one could not be tidied.
func (t *Terminal) sweepLocal() {
	entries, err := os.ReadDir(t.opts.WorkRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-localSessionTTL)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), localSessionPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(t.opts.WorkRoot, e.Name()))
	}
}

// launchScriptName is the file the terminal is told to run.
const launchScriptName = "kubetower-session.ps1"

// writeLaunchScript builds the PowerShell the new window runs.
//
// The environment is set in the script, not on the child process: `wt.exe -w
// 0` signals an already running Windows Terminal, which creates the tab with its
// own environment, so a child Env is silently dropped whenever Windows Terminal
// is already open. The session would then use the operator's default
// kubeconfig, the worst failure for a console saying "you are on this cluster".
// A script carries its environment however it is started.
//
// A file rather than -Command: the paths contain spaces and pass through two
// argument parsers, Windows Terminal's and PowerShell's.
func writeLaunchScript(req localRequest, cluster, kubeconfig string) []byte {
	var b strings.Builder

	// A UTF-8 byte order mark first: Windows PowerShell 5.1 reads a `.ps1` without
	// one in the ANSI code page, where a cluster name's U+2011 (E2 80 91) reads as
	// `â€‘`, and `‘` is a quote PowerShell ends a literal on, after psEscapeSingle's
	// escaping. PowerShell 7 reads UTF-8 either way and skips the mark.
	b.WriteString("\xef\xbb\xbf")
	b.WriteString("# Written by KubeTower for one terminal session. Safe to delete.\n")
	b.WriteString("$ErrorActionPreference = 'Continue'\n")
	// The kubeconfig and nothing else: no PATH change, no profile edit.
	fmt.Fprintf(&b, "$env:KUBECONFIG = %s\n", psQuote(kubeconfig))
	fmt.Fprintf(&b, "$env:KUBETOWER_CLUSTER = %s\n", psQuote(cluster))
	if req.Namespace != "" {
		fmt.Fprintf(&b, "$env:KUBETOWER_NAMESPACE = %s\n", psQuote(req.Namespace))
	}

	// The banner names the cluster: a prompt that looks like every other is how
	// the right command runs on the wrong cluster.
	fmt.Fprintf(&b, "Write-Host ''\n")
	fmt.Fprintf(&b, "Write-Host 'KubeTower — %s' -ForegroundColor Cyan\n", psEscapeSingle(cluster))
	if req.Kind != "" && req.Name != "" {
		fmt.Fprintf(&b, "Write-Host 'selected: %s %s' -ForegroundColor DarkGray\n",
			psEscapeSingle(req.Kind), psEscapeSingle(req.Name))
	}
	b.WriteString("Write-Host 'KUBECONFIG is set for this window only, to this cluster only.' -ForegroundColor DarkGray\n")
	b.WriteString("Write-Host ''\n")

	return []byte(b.String())
}

// psQuote renders a Go string as a PowerShell single-quoted literal. Single
// quotes because PowerShell expands `$` and backticks inside double quotes, and
// `$` is legal in a temp path.
func psQuote(s string) string { return "'" + psEscapeSingle(s) + "'" }

// psEscapeSingle doubles single quotes, PowerShell's escape inside a
// single-quoted literal; there is no backslash escaping.
//
// Every quote PowerShell ends a literal on: its tokenizer treats U+2018,
// U+2019, U+201A and U+201B as single quotes too (IsSingleQuote), so a
// typographic apostrophe pasted into a name would otherwise end the literal.
// Each is doubled as itself, so the name reads back exactly.
func psEscapeSingle(s string) string { return psQuoteDoubler.Replace(s) }

var psQuoteDoubler = strings.NewReplacer(
	"'", "''",
	"\u2018", "\u2018\u2018",
	"\u2019", "\u2019\u2019",
	"\u201a", "\u201a\u201a",
	"\u201b", "\u201b\u201b",
)
