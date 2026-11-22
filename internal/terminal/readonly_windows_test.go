//go:build windows

package terminal

// The read-only override, run through the shell it is written for.
//
// # Why this file has to exist
//
// `readonly_test.go` asserts two things: a Go table that mirrors the rule, and
// the generated script's **text**. Both stay green with a Windows guardrail
// that is a no-op — a `#!/bin/sh` file named `kubectl`, which `PATHEXT` does
// not resolve, so a session's `kubectl` walks straight past it to the real
// binary. So the test that must exist alongside them runs the shim through the
// real OS shell for each platform, rather than inspecting the string.
//
// So these tests do the only thing that can catch it: put the override
// on a `PATH`, ask **PowerShell** to run `kubectl`, and look at what answered.
// Almost nothing here reads the script. If the extension is wrong, if `PATHEXT`
// stops carrying `.CMD`, if the batch is malformed, if `shift` stops walking the
// arguments — each of those turns a refusal into a call reaching the binary, and
// each of those fails here.
//
// # No cluster, and nothing that could reach one
//
// `docs/DEFINITION-OF-DONE.md` §3: a test of this guard must never run
// `kubectl delete ns` for real. The stand-in below is a `.cmd` that
// echoes a marker. There is no kubeconfig, no cluster and no network anywhere in
// this file, and the word `delete` never reaches anything that could act on it.
//
// # And why the matrices run in one shell
//
// A PowerShell per case would be 217 of them, 90 seconds, in a package a
// developer on this platform runs on every save. Nothing about the
// fidelity depends on the process count — what is exercised is a PATH search,
// `PATHEXT`, and cmd.exe reading the batch, and all three happen per invocation
// inside the loop exactly as they do per launch. The case number is printed
// between invocations so a failure still names itself.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// windowsTestShell is the shell these tests resolve the override in, or a skip.
func windowsTestShell(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("no PowerShell on this machine, so there is no shell to resolve the override in")
	return ""
}

// standInSaysItWasReached is a `.cmd` that reports being run and does nothing
// else. It is what tells "the override let this through" from "it refused".
const standInSaysItWasReached = "@echo off\r\necho REACHED_REAL_BINARY\r\n"

// windowsShimDir writes an override and its stand-in binary into a fresh
// directory and returns it.
func windowsShimDir(t *testing.T, binary, standIn string) string {
	t.Helper()
	dir := t.TempDir()
	real := filepath.Join(dir, "real-"+binary+".cmd")
	if err := os.WriteFile(real, []byte(standIn), 0o644); err != nil {
		t.Fatalf("writing the stand-in binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, shimFileName(binary)),
		[]byte(shimContent(binary, real)), shimMode); err != nil {
		t.Fatalf("writing the override: %v", err)
	}
	return dir
}

// sweepWindowsShim runs several invocations of one override in a single shell
// and returns each one's output.
//
// **Through `PATH` and not by calling the file directly**, which is the whole
// point: what makes an override an override is that it is what a PATH search
// finds under the name the operator types. A test that invoked
// `<dir>\kubectl.cmd` by its full path would pass on a machine where the
// extension is never resolved — which is the machine this whole file is about.
//
// Prepended and not replaced, because the batch needs cmd.exe's own directory
// to run at all. That is also exactly what `sessionEnv` and `withShims` do.
func sweepWindowsShim(t *testing.T, binary string, cases [][]string) []string {
	t.Helper()

	shell := windowsTestShell(t)
	dir := windowsShimDir(t, binary, standInSaysItWasReached)

	const mark = "KUBETOWER-CASE-"
	var script strings.Builder
	script.WriteString("$ErrorActionPreference = 'Continue'\n")
	for i, args := range cases {
		script.WriteString("Write-Output '" + mark + strconv.Itoa(i) + "'\n")
		script.WriteString("& " + psArgList(append([]string{binary}, args...)) + " 2>&1 | Out-String\n")
	}

	// A file and not `-Command`, because the whole matrix is about 17 KB of
	// script and a Windows command line is capped near 32 KB — passed inline,
	// `CreateProcess` refuses it, and the failure arrives as a shell that
	// produces *nothing at all*. Which is this
	// package's own recurring shape: an empty answer and a failed one look the
	// same. `-ExecutionPolicy Bypass` for localterm_windows.go's reason — the
	// policy applies to script *files*, and this one was written a moment ago in
	// a directory only this test owns.
	path := filepath.Join(dir, "sweep.ps1")
	if err := os.WriteFile(path, []byte(script.String()), 0o600); err != nil {
		t.Fatalf("writing the sweep script: %v", err)
	}
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path)
	cmd.Env = append(os.Environ(), "PATH="+withShims(dir, os.Getenv("PATH")))
	out, err := cmd.CombinedOutput()
	if len(out) == 0 {
		t.Fatalf("the shell produced nothing at all, which is not a result: %v", err)
	}
	// A Windows shell ends its lines `\r\n`, so a marker looked for with a bare
	// `\n` after it is never found — and the failure reads as "case 0 never ran"
	// on a sweep that ran every case perfectly.
	text := strings.ReplaceAll(string(out), "\r\n", "\n")

	results := make([]string, len(cases))
	for i := range cases {
		head := mark + strconv.Itoa(i) + "\n"
		start := strings.Index(text, head)
		if start < 0 {
			t.Fatalf("case %d never ran; the shell produced:\n%s", i, text)
		}
		rest := text[start+len(head):]
		if end := strings.Index(rest, mark); end >= 0 {
			rest = rest[:end]
		}
		results[i] = rest
	}
	return results
}

func reachedTheBinary(output string) bool {
	return strings.Contains(output, "REACHED_REAL_BINARY")
}

// TestTheWindowsOverrideRefusesEveryMutatingVerbThroughARealShell runs the
// override through the resolution Windows actually does.
//
// Every verb, in every flag shape a command line puts in front of one. A
// detached flag value is the shape that defeats a scanner whose rule is "the
// first argument that is not a flag": `kubectl --context prod delete ns X`
// stops on `prod`.
func TestTheWindowsOverrideRefusesEveryMutatingVerbThroughARealShell(t *testing.T) {
	var names []string
	var cases [][]string
	for _, verb := range mutatingVerbs {
		for _, prefix := range prefixes {
			names = append(names, verb+"/"+prefix.name)
			cases = append(cases, append(append([]string{}, prefix.args...),
				verb, "some-resource", "a-name-that-does-not-exist"))
		}
	}

	results := sweepWindowsShim(t, "kubectl", cases)
	for i, name := range names {
		t.Run(name, func(t *testing.T) {
			if reachedTheBinary(results[i]) {
				t.Fatalf("the override let %v through to the binary:\n%s", cases[i], results[i])
			}
			if !strings.Contains(results[i], fenceRefusal) {
				t.Fatalf("the override did not print its own refusal for %v, so the fence probe "+
					"would read this as a hole:\n%s", cases[i], results[i])
			}
		})
	}
}

// A read has to reach the binary, and this is the half that keeps the guardrail
// usable: a marking that refuses `kubectl get pods` is a marking an operator
// learns to ignore, which is the argument readonly.go makes about `test` and
// `certificate`.
func TestTheWindowsOverrideLetsReadsThrough(t *testing.T) {
	cases := [][]string{
		{"get", "ns"},
		{"--context", "prod", "get", "pods", "-A"},
		{"describe", "pod", "x"},
		{"logs", "-f", "pod"},
		{"config", "current-context"},
		{"version", "-o", "json"},
		{"top", "nodes"},
		{"events", "--for", "pod/x"},
		// Values that are not verbs and look like them to a careless matcher: a
		// namespace called `del` or `test`, which is why both are helm-only.
		{"get", "pods", "-n", "del"},
		{"get", "pods", "-n", "test"},
		// An argument carrying a wildcard, which is the batch trap
		// readOnlyShimCmdTemplate avoids by walking `%1` with `shift` rather
		// than iterating `%*`: `for %%A in (%*)` performs *file name* expansion,
		// so the `*` here would be replaced by whatever is in the working
		// directory. The command would still run, with a different query — a
		// wrong answer rather than an error, which is the worst outcome a
		// console has.
		{"get", "pods", "-o", "jsonpath={.items[*].metadata.name}"},
	}

	results := sweepWindowsShim(t, "kubectl", cases)
	for i, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if !reachedTheBinary(results[i]) {
				t.Fatalf("the override refused a read, which is how a guardrail stops being "+
					"believed: %v\n%s", args, results[i])
			}
			// The stand-in echoes only its marker, so a file name from the
			// working directory appearing here means an argument was expanded.
			for _, leaked := range []string{"real-kubectl.cmd", "kubectl.cmd"} {
				if strings.Contains(results[i], leaked) {
					t.Errorf("a file name reached the output, so an argument was expanded by the "+
						"batch: %v\n%s", args, results[i])
				}
			}
		})
	}
}

// The two lists stay two lists on this platform too. `helm test` creates pods in
// the cluster and `kubectl get pods -n test` is an ordinary read, so a single
// shared list would refuse the second to catch the first — and the Windows
// override is generated from the same `verbsFor`, so getting this wrong here
// would mean the two templates had drifted apart.
func TestTheWindowsOverrideKeepsTheHelmOnlyVerbsToHelm(t *testing.T) {
	var helmCases, kubectlCases [][]string
	for _, verb := range helmOnlyVerbs {
		helmCases = append(helmCases, []string{verb, "a-release-that-does-not-exist"})
		kubectlCases = append(kubectlCases, []string{"get", "pods", "-n", verb})
	}

	helmResults := sweepWindowsShim(t, "helm", helmCases)
	kubectlResults := sweepWindowsShim(t, "kubectl", kubectlCases)

	for i, verb := range helmOnlyVerbs {
		t.Run("helm/"+verb, func(t *testing.T) {
			if reachedTheBinary(helmResults[i]) {
				t.Fatalf("the helm override let %q through:\n%s", verb, helmResults[i])
			}
		})
		t.Run("kubectl/"+verb, func(t *testing.T) {
			if !reachedTheBinary(kubectlResults[i]) {
				t.Fatalf("the kubectl override refused a namespace called %q, which is a read:\n%s",
					verb, kubectlResults[i])
			}
		})
	}
}

// The refusal has to name the setting that actually applies, because there are
// two ways to be behind these overrides — a cluster in KT_READONLY_CLUSTERS, and
// a Claude session, which is restricted on every cluster by default — and a shim
// that always blamed the cluster would send half the sessions to the wrong
// setting. `KT_READONLY_WHY` is what carries that, and on this platform it is an
// `if defined` in batch rather than a `${VAR:-default}` in sh.
func TestTheWindowsOverridePrintsTheSessionsOwnReason(t *testing.T) {
	shell := windowsTestShell(t)
	dir := windowsShimDir(t, "kubectl", standInSaysItWasReached)

	const script = "$ErrorActionPreference = 'Continue'\n" +
		"& 'kubectl' 'delete' 'ns' 'a-name-that-does-not-exist' 2>&1 | Out-String\n"
	const reason = "Claude sessions are read-only; set KT_CLAUDE_READONLY=false to allow this."

	run := func(extra ...string) string {
		cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-Command", script)
		cmd.Env = append(append(os.Environ(), "PATH="+withShims(dir, os.Getenv("PATH"))), extra...)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	if withReason := run("KT_READONLY_WHY=" + reason); !strings.Contains(withReason, reason) {
		t.Errorf("the refusal does not carry the session's own reason, so it names the wrong "+
			"setting:\n%s", withReason)
	}
	// And with nothing set, the default sentence rather than a blank second line.
	if bare := run(); !strings.Contains(bare, "KT_READONLY_CLUSTERS") {
		t.Errorf("with no reason set the refusal says nothing actionable:\n%s", bare)
	}
}

// The exit code, which is the half a script sees rather than a person.
//
// A refusal that exited 0 would let `kubectl delete x && echo done` print
// `done`, and an allowed call that lost the real binary's code would make every
// failure look like a success. Both are silent.
func TestTheWindowsOverridePassesExitCodesThrough(t *testing.T) {
	shell := windowsTestShell(t)
	// Exits 7, which no shell produces by accident.
	dir := windowsShimDir(t, "kubectl", "@echo off\r\nexit /b 7\r\n")

	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{"a read reaches the binary and keeps its code", "'get' 'pods'", "7"},
		{"a refusal exits non-zero", "'delete' 'ns' 'a-name-that-does-not-exist'", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-Command",
				"$ErrorActionPreference = 'Continue'\n& 'kubectl' "+tc.args+
					" 2>&1 | Out-Null\nWrite-Output $LASTEXITCODE\n")
			cmd.Env = append(os.Environ(), "PATH="+withShims(dir, os.Getenv("PATH")))
			out, _ := cmd.CombinedOutput()
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("exit code %q, want %q — a code that does not travel makes every failure "+
					"look like a success", got, tc.want)
			}
		})
	}
}

// And the one assertion here that does read the text, because what it checks is
// that the *two* renderings are generated from one list rather than maintained
// side by side.
func TestTheWindowsOverrideIsGeneratedFromTheSameVerbList(t *testing.T) {
	script := buildReadOnlyShimCmd("helm", `C:\real\helm.exe`)
	for _, verb := range slices.Concat(mutatingVerbs, helmOnlyVerbs) {
		if !strings.Contains(script, verb) {
			t.Errorf("verb %q is missing from the generated Windows override", verb)
		}
	}
	if !strings.Contains(script, `"C:\real\helm.exe" %*`) {
		t.Error("the override does not hand allowed commands on to the real binary with its own arguments")
	}
	if strings.Contains(script, "#!/bin/sh") {
		t.Error("the Windows override is a POSIX script, which Windows does not run")
	}
}

// TestTheWindowsShimIsWhatWritesReadOnlyShimsWrites closes the gap between the
// two halves of this file: everything above runs `shimContent` and
// `shimFileName`, and this is what makes those the same thing the running
// process installs.
//
// Without it a change to `writeReadOnlyShims` — back to a bare name, or to a
// `.ps1` — would leave every test above green while the guardrail went away.
func TestTheWindowsShimIsWhatWriteReadOnlyShimsWrites(t *testing.T) {
	root := t.TempDir()
	term := &Terminal{opts: Options{WorkRoot: root}, logf: func(string, ...any) {}}
	if err := term.writeReadOnlyShims(); err != nil {
		t.Fatalf("writeReadOnlyShims: %v", err)
	}

	for _, name := range fenceBinaries {
		if _, err := exec.LookPath(name); err != nil {
			t.Logf("%s is not on this machine's PATH, so no override was installed for it", name)
			continue
		}
		path := filepath.Join(term.roBinDir, name+".cmd")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("the override for %s is not at %s, so PATHEXT will not resolve it: %v",
				name, path, err)
			continue
		}
		if strings.Contains(string(body), "#!/bin/sh") {
			t.Errorf("the override for %s is a POSIX script: Windows has no interpreter for a "+
				"`#!` line and the guardrail is a no-op", name)
		}
		if bare := filepath.Join(term.roBinDir, name); fileExists(bare) {
			t.Errorf("an extensionless %s was also written; it is not executable on Windows and "+
				"it would bypass the guardrail", bare)
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// The plugin scan on this platform: a `kubectl-*.cmd` on PATH is a plugin, and
// the override refuses an invocation that resolves to one. **Runs only on
// Windows, with `task test`: CI has no Windows job**;
// `readonlyplugin_test.go` pins the script's text on every host.
func TestTheWindowsOverrideRefusesAKubectlPlugin(t *testing.T) {
	shell := windowsTestShell(t)
	dir := windowsShimDir(t, "kubectl", standInSaysItWasReached)
	for _, name := range []string{"kubectl-destroyer.cmd", "kubectl-view_secret.cmd"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(standInSaysItWasReached), 0o644); err != nil {
			t.Fatalf("writing the stand-in plugin: %v", err)
		}
	}
	for _, c := range []struct {
		args    []string
		refused bool
	}{
		{[]string{"destroyer", "wipe"}, true},
		{[]string{"view-secret", "api-tls"}, true},
		{[]string{"get", "pods", "-n", "destroyer"}, false},
		{[]string{"--context", "prod", "get", "destroyer"}, false},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			script := "& " + psArgList(append([]string{"kubectl"}, c.args...)) + " 2>&1 | Out-String\n"
			cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-Command", script)
			cmd.Env = append(os.Environ(), "PATH="+withShims(dir, os.Getenv("PATH")))
			out, _ := cmd.CombinedOutput()
			text := string(out)
			if c.refused && (reachedTheBinary(text) || !strings.Contains(text, "kubectl plugin")) {
				t.Fatalf("a plugin was not refused: kubectl %v\n%s", c.args, text)
			}
			if !c.refused && !reachedTheBinary(text) {
				t.Fatalf("a read was refused: kubectl %v\n%s", c.args, text)
			}
		})
	}
}
