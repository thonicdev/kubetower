package terminal

// The read-only guardrail, measured rather than believed — see fence.go.
//
// Two kinds of test here, and they cover different halves.
//
// The classification tests below inject the probe's output, so they run
// everywhere, a Windows host included. The shell tests at the bottom run the
// real thing and are POSIX-only, so they run in the Linux image `docker build`
// runs go test in and not here — the same bargain readonly_test.go makes, for the
// same reason.
//
// **They are gated on runtime.GOOS and not on finding a shell.** A Windows
// developer host may well resolve all four — `exec.LookPath` returning Git
// Bash's `sh.exe` and `bash.exe`, Docker Desktop's `kubectl.exe` and WinGet's
// `helm.exe` — so "no shell found" is not why these skip. skipWithoutBash returns
// before it ever looks, because a resolved Git Bash is precisely what makes the
// path assertions compare two spellings of the same path (see its own comment).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// intactFence is the fence a test that is not about the fence should get.
//
// Its own helper because claude_test.go needs it too: the banner takes the
// fence, and a zero-valued Fence means "unmeasured", so a test that passed one by
// accident would be asserting against the degraded wording.
func intactFence() Fence {
	return Fence{Measured: true, Intact: true, Detail: "measured at start-up."}
}

// --- the marker the probe reads --------------------------------------------------

// The probe decides that a session is fenced by finding the override's own
// refusal in the output. If that sentence ever moves, every intact fence is
// reported as a hole — a false alarm on every start-up, which is how a warning
// stops being read and then gets switched off.
//
// So the marker is checked against the script it is a substring of, rather than
// being a constant two files apart from the text it has to match.
func TestTheFenceLooksForWhatTheShimPrints(t *testing.T) {
	if !strings.Contains(readOnlyShimTemplate, fenceRefusal) {
		t.Fatalf("fenceRefusal %q is not in the shim the probe runs:\n%s", fenceRefusal, readOnlyShimTemplate)
	}
	// And it has to be in what the shim actually *renders*, not only in the
	// template: a %s in the wrong place would put it there and not there.
	rendered := buildReadOnlyShim("kubectl", "/usr/local/bin/kubectl")
	if !strings.Contains(rendered, fenceRefusal) {
		t.Fatalf("the rendered shim does not carry %q:\n%s", fenceRefusal, rendered)
	}
}

// The probe runs a word on the denylist, which is the whole point: it exercises
// the refusal rather than inspecting a file. A probe whose verb had quietly
// fallen off the list would pass through and report a hole on every intact
// installation.
func TestTheProbeRunsAWordTheOverrideRefuses(t *testing.T) {
	for _, name := range fenceBinaries {
		args := fenceProbeArgs(name)
		if args[0] != name {
			t.Fatalf("the probe for %s runs %q", name, args[0])
		}
		if _, refused := refusalFor(args[1:]); !refused {
			t.Fatalf("the probe %v carries no word %s's override refuses, so it can never observe one",
				args, name)
		}
		// And `--help`, which is what makes running it safe against the real
		// binary: no connection, no kubeconfig, nothing changed. CLAUDE.md's
		// rule is never to run a mutating command against a real cluster to
		// test something.
		if args[len(args)-1] != "--help" {
			t.Fatalf("the probe %v does not end in --help, so it would reach a cluster when the override is absent", args)
		}
	}
}

// --- the classification ----------------------------------------------------------

// stubRun answers every probe with one canned output.
func stubRun(out string, err error) func(argv, env []string) (string, error) {
	return func([]string, []string) (string, error) { return out, err }
}

// A refusal in the output is the only thing that means fenced.
func TestAnOverrideThatRefusesIsAnIntactFence(t *testing.T) {
	dir := t.TempDir()
	writeFakeShims(t, dir)

	f := measureFence(posixShell(), dir, "/usr/bin", stubRun(
		fenceResolvedPrefix+filepath.Join(dir, "kubectl")+"\n"+
			"kubetower: 'delete' "+fenceRefusal+".\n", errors.New("exit status 1")))

	if !f.Measured || !f.Intact || f.State() != "intact" {
		t.Fatalf("a refused probe was not read as an intact fence: %+v", f)
	}
	if !strings.Contains(f.Detail, dir) {
		t.Errorf("the intact sentence does not name the override directory: %q", f.Detail)
	}
}

// The real hole: no override was written, and the session finds the real
// binary. This is what a macOS launch from the Dock produces, and what
// `writeReadOnlyShims` logs one line about and continues past.
func TestAnUnwrittenOverrideIsAnAbsentFence(t *testing.T) {
	dir := t.TempDir() // deliberately empty: no shim was installed

	f := measureFence(posixShell(), dir, "/usr/bin", stubRun(
		fenceResolvedPrefix+"/opt/homebrew/bin/kubectl\n"+
			"Delete resources by file names, stdin, resources and names...\n", nil))

	if !f.Measured {
		t.Fatalf("a probe that ran was reported as unmeasured: %+v", f)
	}
	if f.Intact || f.State() != "absent" {
		t.Fatalf("a session resolving the real kubectl was read as fenced: %+v", f)
	}
	for _, want := range []string{"kubectl", "NOT behind", "none was installed", "/opt/homebrew/bin/kubectl"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("the sentence does not say %q, so the operator cannot act on it: %q", want, f.Detail)
		}
	}
}

// The other absence: an override *was* written and is not what the session
// resolves. Nothing produces this today — roBinDir is prepended — and that is
// exactly why it is tested: PATHEXT on Windows, an `Options.Shell` set
// to a login shell, a `withShims` that appends, and a `noexec` mount all land
// here, and none of them would be noticed by a reader of this package.
func TestAnOverrideThatIsShadowedIsAnAbsentFence(t *testing.T) {
	dir := t.TempDir()
	writeFakeShims(t, dir)

	f := measureFence(posixShell(), dir, "/usr/bin", stubRun(
		fenceResolvedPrefix+"/opt/homebrew/bin/kubectl\nusage: kubectl delete\n", nil))

	if f.Intact || f.State() != "absent" {
		t.Fatalf("a shadowed override was read as fenced: %+v", f)
	}
	if strings.Contains(f.Detail, "none was installed") {
		t.Errorf("the sentence blames a missing override for a shadowed one: %q", f.Detail)
	}
	if !strings.Contains(f.Detail, filepath.Join(dir, shimFileName("kubectl"))) {
		t.Errorf("the sentence does not say what should have answered: %q", f.Detail)
	}
}

// **A binary that resolves to nothing is not a hole**, and getting this
// backwards would report a missing fence on every image without helm — a warning
// that is wrong on most installations is a warning that gets turned off.
// Nothing is in front of a binary the session cannot run, and nothing is behind
// it either: kubectl refusing and helm absent is an intact fence.
func TestABinaryTheSessionCannotFindIsNotAHole(t *testing.T) {
	dir := t.TempDir()
	writeFakeShims(t, dir)

	f := measureFence(posixShell(), dir, "/usr/bin", func(argv, _ []string) (string, error) {
		if argv[0] == "helm" {
			return fenceResolvedPrefix + "\nsh: helm: not found\n", errors.New("exit status 127")
		}
		return fenceResolvedPrefix + filepath.Join(dir, "kubectl") + "\n" +
			"kubetower: 'delete' is refused, this session is read-only.\n", errors.New("exit status 1")
	})

	if !f.Intact || f.State() != "intact" {
		t.Fatalf("a session with kubectl fenced and no helm was reported as %q: %+v", f.State(), f)
	}
}

// **But neither binary resolving is not a fence that held.** It is what the
// Windows probe reads with no PATHEXT in its environment while a session's own
// shell finds both. Nothing was refused, so nothing was established, and the
// sentence must not say each binary "refused a mutating verb when asked".
func TestAProbeThatResolvedNothingIsUnmeasuredAndNeverIntact(t *testing.T) {
	dir := t.TempDir() // no shims, and nothing resolves

	f := measureFence(posixShell(), dir, "/usr/bin", stubRun(
		fenceResolvedPrefix+"\nsh: kubectl: not found\n", errors.New("exit status 127")))

	if f.Intact {
		t.Fatalf("a probe that resolved neither binary reported the fence intact: %+v", f)
	}
	if f.State() != "unmeasured" {
		t.Fatalf("a probe that established nothing was reported as %q: %+v", f.State(), f)
	}
	for _, want := range []string{"neither kubectl nor helm", "not established"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("the sentence does not say %q: %q", want, f.Detail)
		}
	}
	if strings.Contains(f.Detail, "refused a mutating verb") {
		t.Errorf("the sentence claims a refusal that never happened: %q", f.Detail)
	}
}

// A shell that would not start is *unmeasured*, never absent. See Fence's own
// comment: a diagnostic that failed is not a finding.
func TestAProbeThatCouldNotRunIsUnmeasuredAndNotAbsent(t *testing.T) {
	dir := t.TempDir()
	writeFakeShims(t, dir)

	f := measureFence(posixShell(), dir, "/usr/bin",
		stubRun("", errors.New(`fork/exec /bin/bash: no such file or directory`)))

	if f.State() != "unmeasured" {
		t.Fatalf("a shell that would not start was reported as %q: %+v", f.State(), f)
	}
	if f.Intact {
		t.Fatal("an unmeasured fence claimed to be intact")
	}
	if !strings.Contains(f.Detail, "could not be probed") {
		t.Errorf("the sentence does not say the probe failed: %q", f.Detail)
	}
}

// The three states have to be three different sentences, or the whole file is a
// boolean with extra steps.
func TestEachFenceStateSaysSomethingDifferent(t *testing.T) {
	seen := map[string]string{}
	for _, f := range []Fence{
		{Measured: true, Intact: true, Detail: "a"},
		{Measured: true, Detail: "b"},
		{Detail: "c"},
	} {
		state := f.State()
		if prev, ok := seen[state]; ok {
			t.Fatalf("two fences report the same state %q (%s and %s)", state, prev, f.Detail)
		}
		seen[state] = f.Detail
	}
	if len(seen) != 3 {
		t.Fatalf("Fence has %d states, not three: %v", len(seen), seen)
	}
	// And each of them is something a session can be told, except the one that
	// has nothing to correct.
	if fenceNotice(Fence{Measured: true, Intact: true, Detail: "a"}) != "" {
		t.Error("an intact fence writes a notice into every read-only session")
	}
	for _, f := range []Fence{{Measured: true, Detail: "b"}, {Detail: "c"}} {
		notice := fenceNotice(f)
		if notice == "" {
			t.Fatalf("state %q writes no notice, so the session is told nothing: %+v", f.State(), f)
		}
		if !strings.Contains(notice, f.Detail) {
			t.Errorf("the notice for %q drops the reason: %q", f.State(), notice)
		}
	}
}

// The resolved path is read off a marked line and not off "the first line".
// The real binary's help text arrives on the same stream, and a shell may put a
// job-control line in front of everything.
func TestTheResolvedPathIsReadOffItsOwnLine(t *testing.T) {
	out := "bash: cannot set terminal process group: Inappropriate ioctl\n" +
		fenceResolvedPrefix + "  /ro-bin/kubectl  \n" +
		"Delete resources by file names\n"
	if got := fenceResolved(out); got != "/ro-bin/kubectl" {
		t.Fatalf("fenceResolved read %q", got)
	}
	if got := fenceResolved("no marker here\n"); got != "" {
		t.Fatalf("fenceResolved invented %q out of unmarked output", got)
	}
}

// The probe's environment is built from nothing, which is the second guard on
// its safety: `--help` means the real binary opens no connection, and an
// environment with no AWS credentials, no inherited KUBECONFIG and no in-cluster
// service account means that even a `kubectl` which is somebody's own wrapper
// script has no cluster to reach.
//
// **The exception is a fixed list of names that describe the machine and not
// the operator** — `PATHEXT` above all, without which Windows resolves no
// `.cmd` and the probe finds neither the override nor the real binary.
// The list is `fenceProbeMachineVars` and it is *closed*: this test is the thing
// that keeps it closed, so a name added to it has to be argued for here as well.
// Nothing on Linux or macOS defines any of them, so the container's probe
// environment is unaffected.
func TestTheProbeEnvironmentCarriesNoCredentials(t *testing.T) {
	env := fenceProbeEnv("/ro-bin", "/usr/bin:/bin")

	allowed := map[string]bool{"PATH": true, "KUBECONFIG": true, "HOME": true, "TERM": true}
	for _, name := range fenceProbeMachineVars {
		allowed[name] = true
	}

	var kubeconfig string
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if !allowed[name] {
			t.Errorf("the probe environment carries %q, which was not argued for", name)
			continue
		}
		if name == "KUBECONFIG" {
			kubeconfig = value
		}
	}

	// And the list itself, because "a name that describes the machine" is a
	// judgment and this is where it is recorded. Anything holding a credential,
	// a token, a profile or a path into the operator's own configuration is what
	// the "from nothing" rule exists to keep out; a name added below without a
	// line here is the regression to catch.
	for _, name := range fenceProbeMachineVars {
		switch name {
		case "PATHEXT", "ComSpec", "SystemRoot", "SystemDrive", "windir", "TEMP", "TMP":
			// Windows facts: which extensions are executable, where cmd.exe is,
			// where the system lives, and somewhere to write. None of them says
			// anything about who is running or what they can reach.
		default:
			t.Errorf("fenceProbeMachineVars carries %q with no argument for it; the probe "+
				"environment is built from nothing on purpose", name)
		}
	}
	if kubeconfig == "" {
		t.Fatal("the probe inherits a KUBECONFIG, so a wrapper script could reach a real cluster")
	}
	if _, err := os.Stat(kubeconfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the probe's KUBECONFIG %q exists; it must name a file that does not", kubeconfig)
	}
	// The PATH is the read-only session's PATH exactly. Probing any other one
	// measures something no session runs.
	if got, want := env[0], "PATH="+withShims("/ro-bin", "/usr/bin:/bin"); got != want {
		t.Fatalf("the probe PATH is %q, not the read-only session's %q", got, want)
	}
}

// --- the permission mode -------------------------------------------------------

// The argument for `auto` being the default rests, in claudeReadTools' own
// words, on the shims being the only hard boundary on kubectl and helm. When the
// probe says that boundary is absent, the premise is gone and the question is
// the only layer left.
func TestAReadOnlyClaudeSessionAsksWhenTheFenceIsAbsent(t *testing.T) {
	absent := Fence{Measured: true, Detail: "kubectl is NOT behind the read-only overrides."}
	quiet := func(string, ...any) {}

	for _, mode := range []string{"auto", "dontAsk"} {
		got := claudeModeFor(mode, claudeView{ReadOnly: true}, absent, quiet)
		if got != fencelessClaudeMode {
			t.Errorf("a read-only session with no fence starts in %q rather than %q", got, fencelessClaudeMode)
		}
	}
	// The three that already ask are left alone: `plan` runs nothing at all and
	// is *safer* than manual, so "downgrading" it would be a widening.
	for _, mode := range []string{"manual", "acceptEdits", "plan"} {
		if got := claudeModeFor(mode, claudeView{ReadOnly: true}, absent, quiet); got != mode {
			t.Errorf("configured mode %q was changed to %q for no reason", mode, got)
		}
	}
	// A writable session promised nothing, so there is nothing to correct.
	if got := claudeModeFor("auto", claudeView{}, absent, quiet); got != "auto" {
		t.Errorf("a writable session was downgraded to %q", got)
	}
	// And **unmeasured is not absent**: downgrading on a diagnostic that could
	// not run would change every Windows build's behavior over nothing.
	for _, f := range []Fence{{Detail: "could not be probed"}, intactFence()} {
		if got := claudeModeFor("auto", claudeView{ReadOnly: true}, f, quiet); got != "auto" {
			t.Errorf("state %q changed the mode to %q", f.State(), got)
		}
	}
	// The mode it falls back to has to be one the CLI accepts, or the pane opens
	// and closes on a usage message.
	if !claudePermissionModes[fencelessClaudeMode] {
		t.Fatalf("%q is not a mode this console offers", fencelessClaudeMode)
	}
}

// The banner is where the operator reads back after Claude exits, and the word
// `read-only` in it must not be a claim nothing checks.
func TestTheBannerDoesNotClaimAFenceThatIsNotThere(t *testing.T) {
	view := claudeView{Cluster: "docker-desktop", ReadOnly: true}

	if got := buildClaudeBanner(view, intactFence()); !strings.Contains(got, "(read-only)") {
		t.Fatalf("the intact banner stopped saying read-only: %q", got)
	}
	for _, f := range []Fence{{Measured: true, Detail: "x"}, {Detail: "y"}} {
		got := buildClaudeBanner(view, f)
		if strings.Contains(got, "(read-only)") {
			t.Errorf("the banner claims a plain read-only session with a %q fence: %q", f.State(), got)
		}
		if !strings.Contains(got, f.State()) {
			t.Errorf("the banner does not name the fence state %q: %q", f.State(), got)
		}
	}
}

// --- through a real shell --------------------------------------------------------

// posixShell is the shell New defaults to, read from the production function
// rather than retyped.
//
// **That is what makes the mutation real.** With a literal here, deleting
// `--norc --noprofile` from `defaultSessionShell` would leave every test in this
// file green: the test would be measuring a slice it had written itself.
func posixShell() []string { return defaultSessionShell() }

func writeFakeShims(t *testing.T, dir string) {
	t.Helper()
	for _, name := range fenceBinaries {
		// shimFileName, because what `classifyFenceProbe` looks for is the file
		// writeReadOnlyShims writes — `kubectl.cmd` on Windows, where resolution
		// goes through PATHEXT. A stand-in under the POSIX spelling would send
		// this test down the "none was installed" arm on that platform and prove
		// nothing about a shadowed one.
		if err := os.WriteFile(filepath.Join(dir, shimFileName(name)),
			[]byte(shimContent(name, "/nonexistent/"+name)), shimMode); err != nil {
			t.Fatalf("writing the stand-in override: %v", err)
		}
	}
}

// skipWithoutBash skips where the session's own shell is not the shell these
// tests are about.
//
// **Windows is skipped even though `bash` is usually on PATH there**, and that
// is not laziness. Git Bash is a real POSIX shell on a host where
// `filepath.Join` produces backslashes and `command -v` answers
// `/c/Users/...`, so every assertion here would be comparing two spellings of
// the same path, reporting a correct resolution as a failure. No session opens on Windows
// anyway (`ptySupported` is false there), so the platform these tests describe
// is the container: they run under `docker build` and in CI's `go test -race`
// job on ubuntu, and the injected-output tests above are what covers the
// Windows host.
func skipWithoutBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only: on Windows the path spellings differ and no dock session opens at all")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this platform; the injected-output tests above cover the classification")
	}
	return bash
}

// **The measurement the whole file exists for: a profile that prepends cannot
// re-point the session's kubectl, because neither session is ever a login
// shell.**
//
// Established rather than reasoned about. The profile below is what
// `brew shellenv` writes, in all three files a bash login shell reads, and the
// decoy it prepends is what one would expect to win. The control at the
// bottom runs the *same* PATH through `bash -l -c` and shows the decoy winning
// there — which is what makes this a test of the flags rather than a test of
// nothing.
//
// Deleting `--norc --noprofile` from Options.Shell's default fails the first two
// subtests while the control keeps passing.
func TestTheSessionShellReadsNoInitFile(t *testing.T) {
	bash := skipWithoutBash(t)

	home := t.TempDir()
	decoy := filepath.Join(home, "brew")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatalf("decoy directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "kubectl"),
		[]byte("#!/bin/sh\necho DECOY\n"), 0o755); err != nil {
		t.Fatalf("decoy kubectl: %v", err)
	}
	profile := "export PATH=\"" + decoy + ":$PATH\"\n"
	for _, name := range []string{".bash_profile", ".bashrc", ".profile"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(profile), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	roBin := t.TempDir()
	writeFakeShims(t, roBin)
	env := []string{
		"HOME=" + home,
		"PATH=" + withShims(roBin, decoy),
		"TERM=dumb",
	}
	// The production default, with only argv[0] relocated to wherever bash is
	// on this machine. Everything after it is `defaultSessionShell`'s own, so
	// removing a flag there fails this test rather than being invisible to it.
	shell := append([]string{bash}, defaultSessionShell()[1:]...)
	want := filepath.Join(roBin, "kubectl")

	run := func(argv []string, stdin string) string {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	// 1. The dock's shell tab, verbatim: the configured argv, the command typed
	// at it.
	t.Run("the shell tab", func(t *testing.T) {
		out := run(shell, "command -v kubectl\n")
		if !strings.Contains(out, want) {
			t.Fatalf("the shell tab resolves something else:\n%s", out)
		}
	})

	// 2. The Claude launcher, which is that argv with -i swapped for -c. Same
	// derivation the production code uses, so a change to one is a change to
	// both.
	t.Run("the claude launcher", func(t *testing.T) {
		out := run(claudeArgv(shell, "command -v kubectl"), "")
		if !strings.Contains(out, want) {
			t.Fatalf("the Claude launcher resolves something else:\n%s", out)
		}
	})

	// 3. The control. Without those flags the profile wins — which is the
	// reason the flags are the guarantee rather than an aesthetic choice. If this
	// subtest ever passes, the decoy is not being installed and the two above
	// prove nothing.
	t.Run("CONTROL a login shell is re-pointed", func(t *testing.T) {
		out := run([]string{bash, "-l", "-c", "command -v kubectl"}, "")
		if !strings.Contains(out, filepath.Join(decoy, "kubectl")) {
			t.Fatalf("the profile did not re-point a login shell, so the two subtests above are vacuous:\n%s", out)
		}
	})
}

// The probe end to end, against the real shell and the real override.
//
// The empty-override subtest is a session with no fence, which nothing else
// would say.
func TestTheProbeReadsARealShellAndARealOverride(t *testing.T) {
	bash := skipWithoutBash(t)
	shell := append([]string{bash}, defaultSessionShell()[1:]...)

	// A stand-in kubectl, so this needs no kubectl on the machine and reaches no
	// cluster: `--help` on the real binary prints a help text, and this prints
	// one word.
	real := t.TempDir()
	for _, name := range fenceBinaries {
		if err := os.WriteFile(filepath.Join(real, name),
			[]byte("#!/bin/sh\necho REACHED_REAL_BINARY\n"), 0o755); err != nil {
			t.Fatalf("stand-in %s: %v", name, err)
		}
	}

	t.Run("with the override in front", func(t *testing.T) {
		roBin := t.TempDir()
		for _, name := range fenceBinaries {
			if err := os.WriteFile(filepath.Join(roBin, name),
				[]byte(buildReadOnlyShim(name, filepath.Join(real, name))), 0o755); err != nil {
				t.Fatalf("override: %v", err)
			}
		}
		f := measureFence(shell, roBin, real, fenceRun(shell))
		if !f.Intact {
			t.Fatalf("a real override in front of a real binary was not read as a fence: %+v", f)
		}
	})

	t.Run("with no override at all", func(t *testing.T) {
		f := measureFence(shell, t.TempDir(), real, fenceRun(shell))
		if f.State() != "absent" {
			t.Fatalf("an empty override directory with a reachable binary was read as %q: %+v", f.State(), f)
		}
		if !strings.Contains(f.Detail, "none was installed") {
			t.Errorf("the sentence does not name the cause: %q", f.Detail)
		}
	})
}
