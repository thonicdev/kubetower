//go:build windows

package terminal

// The Windows pseudo-terminal, exercised rather than described.
//
// Every other unit test in this package would pass with no cluster shell on
// this platform, because none of them starts a shell: the compiler is silent,
// go vet is silent, and the assertions are silent about a feature that does not
// exist on the target platform. These are the assertions that are not silent.
//
// They run a real ConPTY with a real shell in it. That is affordable here for
// the reason it is not in the container: the process under test *is* on the
// machine the terminal opens on.

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// wideCols is deliberately much wider than any marker below.
//
// conhost wraps at the column count it was given and inserts a `\r\n` where it
// wraps, so a marker straddling the edge would be split by bytes that are not in
// it — and the test would fail on a terminal width rather than on the thing it is
// about.
const wideCols, wideRows = 200, 30

// ansi strips the escape sequences conhost paints with, so an assertion can be
// about the text rather than about a cursor position.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][0-9A-B]`)

// readUntil reads from the terminal until want appears or the deadline passes,
// and returns everything it saw.
func readUntil(t *testing.T, p ptySession, want string, within time.Duration) (string, bool) {
	t.Helper()

	seen := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
				if strings.Contains(ansi.ReplaceAllString(b.String(), ""), want) {
					seen <- b.String()
					return
				}
			}
			if err != nil {
				seen <- b.String()
				return
			}
		}
	}()

	select {
	case out := <-seen:
		return out, strings.Contains(ansi.ReplaceAllString(out, ""), want)
	case <-time.After(within):
		return "", false
	}
}

// TestTheWindowsTerminalCarriesTheShellsOutput runs a shell through ConPTY,
// and **it is also the guard for the trap that is hardest to find.**
//
// `go test` runs with its standard handles on a pipe, which is exactly the
// shape that makes a ConPTY produce nothing: with
// `STARTF_USESTDHANDLES` unset, `CreateProcess` copies the parent's redirected
// handle *values* into the child even with `bInheritHandles` false, the child is
// attached to the pseudoconsole and writes to a handle that names nothing there,
// and the pane shows one 16-byte conhost preamble and stops. pty_windows.go's
// file comment holds the measurement.
//
// So this test does not need to know about that flag: it fails without it,
// because the marker goes to the test binary's own stdout instead of down the
// terminal. Removing the three lines that set it turns this red.
func TestTheWindowsTerminalCarriesTheShellsOutput(t *testing.T) {
	shell := resolveDefaultShell()
	argv := scriptArgv(shell, "Write-Output 'KDMARK1'")
	if shellFamilyOf(shell) == familyCmd {
		argv = scriptArgv(shell, "echo KDMARK1")
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Dir = t.TempDir()

	p, err := startPTY(cmd, wideCols, wideRows)
	if err != nil {
		t.Fatalf("starting a pseudo-terminal: %v", err)
	}
	defer func() {
		_ = p.Close()
		_ = p.Kill()
		_ = p.Wait()
	}()

	out, ok := readUntil(t, p, "KDMARK1", 20*time.Second)
	if !ok {
		t.Fatalf("the shell's output never reached the terminal. That is what a pseudoconsole "+
			"shadowed by this process's own redirected handles looks like — see "+
			"pty_windows.go. What arrived:\n%q", out)
	}
}

// The session's own environment has to reach the shell, and this is the half a
// wrong environment block loses silently.
//
// It matters more than it looks: `KUBECONFIG` is how a session is pointed at one
// cluster and nothing else, and a shell that came up without it uses the
// operator's default — which for a console whose whole job is "you are looking
// at this cluster" is the worst available failure. localterm.go argues the same
// thing about `wt.exe`.
func TestTheWindowsTerminalCarriesTheSessionsEnvironment(t *testing.T) {
	shell := resolveDefaultShell()
	if shellFamilyOf(shell) != familyPowerShell {
		t.Skip("this machine's default session shell is not PowerShell")
	}

	cmd := exec.Command(shell[0], append(shell[1:], "-Command", "Write-Output $env:KT_CLUSTER")...)
	cmd.Env = append(os.Environ(), "KT_CLUSTER=KDMARKCLUSTER")
	cmd.Dir = t.TempDir()

	p, err := startPTY(cmd, wideCols, wideRows)
	if err != nil {
		t.Fatalf("starting a pseudo-terminal: %v", err)
	}
	defer func() {
		_ = p.Close()
		_ = p.Kill()
		_ = p.Wait()
	}()

	if out, ok := readUntil(t, p, "KDMARKCLUSTER", 20*time.Second); !ok {
		t.Fatalf("the session's environment did not reach the shell:\n%q", out)
	}
}

// The prompt, which is the one thing on screen saying which cluster these
// commands will hit.
//
// PowerShell ignores `PS1` entirely, so without shell.go's prompt function the
// Windows shell would open at `PS C:\Users\you>` with no cluster name anywhere:
// a shell where the operator does not know which production cluster they are
// typing at.
func TestTheWindowsSessionPromptNamesTheCluster(t *testing.T) {
	shell := resolveDefaultShell()
	if shellFamilyOf(shell) != familyPowerShell {
		t.Skip("this machine's default session shell is not PowerShell")
	}

	// An EKS-shaped name, so this also covers the shortening: the full ARN is
	// unreadable in a prompt and shortCluster is what makes it a word.
	const cluster = "arn:aws:eks:eu-west-1:123456789012:cluster/ktmark-prod"
	argv := sessionArgv(shell, cluster, true)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = sessionEnv(os.Environ(), sessionSpec{
		cluster:  cluster,
		family:   shellFamilyOf(shell),
		readOnly: true,
	})
	cmd.Dir = t.TempDir()

	p, err := startPTY(cmd, wideCols, wideRows)
	if err != nil {
		t.Fatalf("starting a pseudo-terminal: %v", err)
	}
	defer func() {
		_ = p.Close()
		_ = p.Kill()
		_ = p.Wait()
	}()

	// `[ktmark-prod ro]` — the short name, and the marking that says the cluster
	// is read-only, which the pane's own notice then either confirms or corrects
	// (fenceNotice).
	if out, ok := readUntil(t, p, "[ktmark-prod ro]", 25*time.Second); !ok {
		t.Fatalf("the prompt does not name the cluster, so the operator cannot tell which one "+
			"they are typing at:\n%q", out)
	}
}

// The resize has to reach the shell, and a ConPTY resize is a different call
// from a Unix `TIOCSWINSZ` with different failure modes: a Windows PTY is a
// real implementation with its own resize, signal and encoding behavior.
func TestTheWindowsTerminalResizeReachesTheShell(t *testing.T) {
	shell := resolveDefaultShell()
	if shellFamilyOf(shell) != familyPowerShell {
		t.Skip("this machine's default session shell is not PowerShell")
	}

	// Started wide, resized narrow, and then asked. Asking *after* the resize is
	// what makes this a test of Resize rather than of the size passed to
	// CreatePseudoConsole.
	cmd := exec.Command(shell[0], append(shell[1:],
		"-NoExit", "-Command", "$null")...)
	cmd.Env = os.Environ()
	cmd.Dir = t.TempDir()

	p, err := startPTY(cmd, wideCols, wideRows)
	if err != nil {
		t.Fatalf("starting a pseudo-terminal: %v", err)
	}
	defer func() {
		_ = p.Close()
		_ = p.Kill()
		_ = p.Wait()
	}()

	// Wait for a prompt before typing, or the keystrokes land before the shell
	// is reading.
	if _, ok := readUntil(t, p, "PS ", 25*time.Second); !ok {
		t.Skip("no prompt appeared to type at; the output test above covers the transport")
	}
	if err := p.Resize(97, 21); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	// `\r` and not `\r\n`: a ConPTY takes Enter as a carriage return, and the
	// linefeed after it is a second, empty line.
	if _, err := p.Write([]byte("Write-Output ('KDCOLS' + $Host.UI.RawUI.WindowSize.Width)\r")); err != nil {
		t.Fatalf("writing to the terminal: %v", err)
	}
	if out, ok := readUntil(t, p, "KDCOLS97", 25*time.Second); !ok {
		t.Fatalf("the shell was not told the pane's new width, so anything it draws is drawn for "+
			"a terminal that is not there:\n%q", out)
	}
}

// **The stream has to end when the shell does**, and on this platform that does
// not happen by itself.
//
// On Unix the shell exiting *is* the end of the stream: the last descriptor on
// the far side of the pty closes and the read fails, which is how `serveShell`
// learns the session is over and removes the directory holding live credentials.
// conhost holds the output pipe open after its client has gone, so without the
// waiter in pty_windows.go a `Read` here blocks for ever on a shell the operator
// typed `exit` into — the pane stays open, the workdir stays on disk, and one of
// the capped session slots stays taken.
func TestTheWindowsTerminalEndsWhenTheShellExits(t *testing.T) {
	shell := resolveDefaultShell()
	argv := scriptArgv(shell, "Write-Output 'KDBYE'")
	if shellFamilyOf(shell) == familyCmd {
		argv = scriptArgv(shell, "echo KDBYE")
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Dir = t.TempDir()

	p, err := startPTY(cmd, wideCols, wideRows)
	if err != nil {
		t.Fatalf("starting a pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// Read to EOF, which is the thing being asserted. Nothing calls Close first:
	// that would end the stream for a reason of our own and prove nothing.
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := p.Read(buf); err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the terminal's output never ended after the shell exited, so the pane stays " +
			"open, the session slot stays taken, and the directory holding this session's " +
			"credentials is never removed")
	}

	// And Wait has to come back, since session.close blocks on it.
	waited := make(chan struct{})
	go func() { _ = p.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return for a process that has exited")
	}
}

// --- the environment block ---------------------------------------------------

// The dedup, which **is** the read-only guardrail on this platform.
//
// `sessionEnv` appends `PATH=` after `os.Environ()`, which already contains one,
// and on a read-only session that appended value is the whole fence — the
// override directory prepended. A POSIX shell imports the environment into a
// hash table and the last entry wins, which is why the container
// resolves the shim. A Windows process parameter block is scanned by
// `GetEnvironmentVariable`, which returns the **first** match. Without this the
// child would see the operator's original `Path`, the overrides would be nowhere
// on it, and `KT_READONLY_CLUSTERS` would be a sentence on a page.
func TestTheEnvironmentBlockKeepsTheLastValueOfEachName(t *testing.T) {
	block := environmentBlock([]string{
		`Path=C:\original`,
		"KT_CLUSTER=one",
		`PATH=C:\ro-bin;C:\original`,
		"KT_CLUSTER=two",
	})
	entries := decodeEnvironmentBlock(block)

	// Case-insensitively, because Windows spells it `Path` in os.Environ() and
	// this package writes `PATH=`, and to Windows those are one variable. A
	// block carrying both is a block whose first one wins.
	var paths []string
	for _, e := range entries {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			paths = append(paths, e)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("the block carries %d PATH entries (%v); GetEnvironmentVariable returns the "+
			"first, so the appended one — the one carrying the read-only overrides — loses", len(paths), paths)
	}
	if paths[0] != `PATH=C:\ro-bin;C:\original` {
		t.Errorf("the surviving PATH is %q, not the session's own", paths[0])
	}

	var clusters []string
	for _, e := range entries {
		if strings.HasPrefix(e, "KT_CLUSTER=") {
			clusters = append(clusters, e)
		}
	}
	if len(clusters) != 1 || clusters[0] != "KT_CLUSTER=two" {
		t.Errorf("KT_CLUSTER entries %v, want only the last", clusters)
	}
}

// A NUL inside an entry would truncate the whole block at that point, silently
// dropping everything after it — including, on a read-only session, the PATH
// that carries the overrides.
func TestTheEnvironmentBlockDropsAnEntryItCannotEncode(t *testing.T) {
	entries := decodeEnvironmentBlock(environmentBlock([]string{
		"GOOD=1",
		"BAD=a\x00b",
		"ALSOGOOD=2",
	}))
	for _, want := range []string{"GOOD=1", "ALSOGOOD=2"} {
		if !contains(entries, want) {
			t.Errorf("%q is missing from the block, so an unencodable entry took the rest with it: %v",
				want, entries)
		}
	}
}

// Windows keeps a per-drive current directory in variables whose *name* is
// empty — `=C:=C:\somewhere` — and `os.Environ()` returns them. Keyed on the
// whole entry so two of them do not collide with each other and vanish.
func TestTheEnvironmentBlockKeepsThePerDriveEntries(t *testing.T) {
	entries := decodeEnvironmentBlock(environmentBlock([]string{
		`=C:=C:\one`,
		`=D:=D:\two`,
		"NORMAL=x",
	}))
	for _, want := range []string{`=C:=C:\one`, `=D:=D:\two`, "NORMAL=x"} {
		if !contains(entries, want) {
			t.Errorf("%q is missing from the block: %v", want, entries)
		}
	}
}

// An empty environment still has to be a valid block: `&block[0]` on an empty
// slice panics, and a zero-length block is not the same as no block.
func TestTheEnvironmentBlockIsNeverEmpty(t *testing.T) {
	if block := environmentBlock(nil); len(block) == 0 {
		t.Fatal("an empty environment produced a zero-length block, which panics on &block[0]")
	}
}

// decodeEnvironmentBlock reads a doubly-NUL-terminated UTF-16 block back into
// the entries it holds, which is how a test can assert what the child will see
// rather than what was handed to the encoder.
func decodeEnvironmentBlock(block []uint16) []string {
	var out []string
	start := 0
	for i, u := range block {
		if u != 0 {
			continue
		}
		if i == start {
			break // the terminating empty string
		}
		out = append(out, string(utf16Runes(block[start:i])))
		start = i + 1
	}
	return out
}

func utf16Runes(u []uint16) []rune {
	// The entries here are ASCII, so this is enough for a test and deliberately
	// not a general decoder — a general one belongs in x/sys, which has it.
	r := make([]rune, 0, len(u))
	for _, c := range u {
		r = append(r, rune(c))
	}
	return r
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
