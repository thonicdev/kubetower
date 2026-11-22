//go:build windows

package terminal

// The assistant's override on Windows, run through a real shell.
//
// `readonly_windows_test.go` exists because the POSIX shim is a no-op on this
// platform while every test that reads its *text* stays green — the file is
// named `kubectl` with no extension and `PATHEXT` walks straight past it. This
// file exists for the same reason and one more: the read scan here is **not**
// the same mechanism as the POSIX one. `case` patterns cannot be
// expressed as a `for %%V in (...)` word list, so `shimSecretScanCmd` does the
// same job with substring replacement and a `for /f` token split. Two
// mechanisms, so two suites of literal command lines — a test of the Go strings
// would say nothing about either.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// assistantShimDir writes the assistant override and its stand-in binary into a
// fresh directory, the way windowsShimDir does for the read-only one.
func assistantShimDir(t *testing.T, binary string, denyWrites bool) string {
	t.Helper()
	dir := t.TempDir()
	real := filepath.Join(dir, "real-"+binary+".cmd")
	if err := os.WriteFile(real, []byte(standInSaysItWasReached), 0o644); err != nil {
		t.Fatalf("writing the stand-in binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, shimFileName(binary)),
		[]byte(assistantShimContent(binary, real, denyWrites)), shimMode); err != nil {
		t.Fatalf("writing the override: %v", err)
	}
	return dir
}

// sweepAssistantShim runs several invocations of the assistant override in one
// shell and returns each one's output.
//
// Through `PATH` and not by calling the file directly, for the reason
// `sweepWindowsShim` gives at length: what makes an override an override is that
// it is the file a PATH search finds under the name the operator types.
func sweepAssistantShim(t *testing.T, binary string, denyWrites bool, cases [][]string) []string {
	t.Helper()

	shell := windowsTestShell(t)
	dir := assistantShimDir(t, binary, denyWrites)

	const mark = "KUBETOWER-CASE-"
	var script strings.Builder
	script.WriteString("$ErrorActionPreference = 'Continue'\n")
	for i, args := range cases {
		script.WriteString("Write-Output '" + mark + strconv.Itoa(i) + "'\n")
		script.WriteString("& " + psArgList(append([]string{binary}, args...)) + " 2>&1 | Out-String\n")
	}

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

// The same command lines as the POSIX suite, through cmd.exe.
//
// They are written out again rather than shared with `nosecrets_test.go`,
// because the two files are testing two different implementations of the same
// rule and a shared list would hide the day one of them stops covering a case
// the other does.
func TestTheWindowsAssistantOverrideRefusesEverySpellingOfASecretRead(t *testing.T) {
	cases := [][]string{
		{"get", "secret"},
		{"get", "secrets"},
		{"get", "secret", "api-tls"},
		{"get", "secrets", "api-tls"},
		{"get", "secret/api-tls"},
		{"get", "secrets/api-tls"},
		{"get", "secret.v1."},
		{"get", "secrets.v1."},
		{"get", "secret."},
		{"get", "secrets."},

		// `if /I`, because Windows is not a case-sensitive place and kubectl
		// accepts every one of these.
		{"get", "Secret"},
		{"get", "SECRET"},
		{"get", "Secrets"},
		{"get", "sEcReT"},

		{"describe", "secret", "api-tls"},
		{"get", "secret", "api-tls", "-o", "yaml"},
		{"get", "secret", "api-tls", "-o", "json"},
		{"get", "secret", "api-tls", "-o", "jsonpath={.data}"},
		{"get", "secret", "api-tls", "-o", "go-template={{.data}}"},
		{"get", "secret", "api-tls", "--template={{.data}}"},
		{"get", "secrets", "-A"},
		{"get", "secrets", "--all-namespaces"},

		{"--context", "prod", "get", "secret"},
		{"--kubeconfig", "C:\\k", "-n", "argo", "get", "secret/registry-pull"},

		// The `--raw` path, which names no resource word: on this platform it is
		// caught by `%ktarg:/secrets=%` rather than by a pattern.
		{"get", "--raw", "/api/v1/namespaces/default/secrets"},
		{"get", "--raw", "/api/v1/namespaces/default/secrets/api-tls"},
		{"get", "--raw", "/api/v1/secrets?limit=500"},
		{"get", "--raw=/api/v1/namespaces/default/secrets"},

		// A comma list, in every position. Unquoted, cmd.exe splits it
		// into separate arguments; quoted, the scan's own `,` handling is what
		// refuses it. Both reach here through `psArgList`'s single quotes, so
		// what cmd sees is the unquoted form - the quoted one is asserted on the
		// script's text in nosecrets_test.go.
		{"get", "secrets,configmaps", "-o", "yaml"},
		{"get", "configmaps,secrets"},
		{"get", "pods,secret,configmaps"},
		{"get", "configmap/a,secret/api-tls"},
		{"get", "pods,secrets.v1."},

		// An explicitly empty argument, which is the shape that walks past a
		// scan that stops on emptiness (`readonlyemptyarg_test.go`): the loop must
		// keep scanning rather than treat it as the end of the arguments.
		{"--context", "", "get", "secret"},
		{"-n", "", "get", "secrets", "-o", "yaml"},
	}

	results := sweepAssistantShim(t, "kubectl", true, cases)
	for i, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if reachedTheBinary(results[i]) {
				t.Fatalf("the override let a Secret read through: kubectl %v\n%s", args, results[i])
			}
			if !strings.Contains(results[i], "names a Secret") {
				t.Errorf("refused, but not as a Secret read: %s", results[i])
			}
		})
	}
}

// And the half that fails silently: an override that refuses everything passes
// the sweep above.
func TestTheWindowsAssistantOverrideLetsOrdinaryReadsThrough(t *testing.T) {
	cases := [][]string{
		{"get", "pods"},
		{"get", "pods", "-A", "-o", "yaml"},
		{"describe", "pod", "api-0"},
		{"logs", "-f", "api-0"},
		{"get", "configmap", "app-config", "-o", "yaml"},
		{"get", "sa", "default", "-o", "yaml"},
		{"get", "secretstore", "vault"},
		{"get", "externalsecret", "api-tls"},
		{"get", "sealedsecret", "api-tls"},
		{"get", "pod", "secret-operator-0"},
		{"get", "all"},
		{"get", "--raw", "/api/v1/namespaces/default/pods"},
		// A `%` outside a `--raw` path is not looked at: a template's verb.
		{"get", "pods", "-o", `go-template={{printf "%s" .metadata.name}}`},
		{"get", "pods", "-l", "app=web%20"},
		// Delayed expansion is off on purpose, so a `!` in a selector survives.
		// If it were ever switched on this would still reach the binary, so the
		// assertion below on the *output* is what carries the check.
		{"get", "pods", "-l", "tier!=db"},
	}

	results := sweepAssistantShim(t, "kubectl", true, cases)
	for i, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if !reachedTheBinary(results[i]) {
				t.Fatalf("an ordinary read was refused: kubectl %v\n%s", args, results[i])
			}
		})
	}
}

// helm's doors onto a release, and the words being helm's alone.
func TestTheWindowsAssistantOverrideRefusesHelmsWaysIntoARelease(t *testing.T) {
	refused := [][]string{
		{"get", "manifest", "ingress-nginx"},
		{"get", "all", "ingress-nginx"},
		{"get", "values", "ingress-nginx"},
		{"get", "hooks", "ingress-nginx"},
		{"get", "notes", "ingress-nginx"},
		{"status", "ingress-nginx"},
		{"status", "ingress-nginx", "-o", "json"},
	}
	for i, args := range sweepAssistantShim(t, "helm", true, refused) {
		if reachedTheBinary(args) {
			t.Errorf("helm %v reached the binary:\n%s", refused[i], args)
		}
	}

	allowed := [][]string{
		{"list"},
		{"history", "ingress-nginx"},
	}
	for i, out := range sweepAssistantShim(t, "helm", true, allowed) {
		if !reachedTheBinary(out) {
			t.Errorf("an ordinary helm read was refused: helm %v\n%s", allowed[i], out)
		}
	}
}

// The two guardrails are independent on this platform too, and a shim built
// without the write scan must carry no `:kubetower_refuse` label that nothing
// jumps to — a stray label is harmless, a missing one that something jumps to
// ends the batch with `The system cannot find the batch label`.
func TestTheWindowsAssistantOverrideSeparatesReadsFromWrites(t *testing.T) {
	writable := sweepAssistantShim(t, "kubectl", false, [][]string{
		{"get", "secret", "api-tls"},
		{"delete", "pod", "api-0"},
	})
	if reachedTheBinary(writable[0]) {
		t.Errorf("a writable assistant session read a Secret:\n%s", writable[0])
	}
	if !reachedTheBinary(writable[1]) {
		t.Errorf("a session the operator allowed to write was refused a write:\n%s", writable[1])
	}
	if strings.Contains(writable[1], "cannot find the batch label") {
		t.Errorf("the generated batch jumps to a label it does not carry:\n%s", writable[1])
	}

	readOnly := sweepAssistantShim(t, "kubectl", true, [][]string{
		{"get", "secret", "api-tls"},
		{"delete", "pod", "api-0"},
	})
	if !strings.Contains(readOnly[0], "names a Secret") {
		t.Errorf("the Secret refusal came from the write scan:\n%s", readOnly[0])
	}
	if !strings.Contains(readOnly[1], "read-only") {
		t.Errorf("the read-only refusal is not the mutating-verb one:\n%s", readOnly[1])
	}
}

// The plugin refusal on this platform: the assistant's override refuses a
// `kubectl-*.cmd` plugin whether or not it denies writes, with the no-Secrets
// sentence. **Runs only on Windows, with `task test`: CI has no Windows
// job**, like
// readonly_windows_test.go's plugin test; `readonlyplugin_test.go` pins the
// script's text on every host.
func TestTheWindowsAssistantOverrideRefusesAKubectlPlugin(t *testing.T) {
	for _, denyWrites := range []bool{false, true} {
		shell := windowsTestShell(t)
		dir := assistantShimDir(t, "kubectl", denyWrites)
		for _, name := range []string{"kubectl-view_secret.cmd", "kubectl-krew.cmd", "kubectl-foo-bar.cmd"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(standInSaysItWasReached), 0o644); err != nil {
				t.Fatalf("writing the stand-in plugin: %v", err)
			}
		}
		for _, c := range []struct {
			args    []string
			refused bool
		}{
			{[]string{"view-secret", "x"}, true},
			{[]string{"krew", "install", "view-secret"}, true},
			{[]string{"foo", "bar", "-n", "x"}, true},
			{[]string{"get", "pods", "-n", "view-secret"}, false},
		} {
			t.Run(strconv.FormatBool(denyWrites)+"/"+strings.Join(c.args, " "), func(t *testing.T) {
				script := "& " + psArgList(append([]string{"kubectl"}, c.args...)) + " 2>&1 | Out-String\n"
				cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-Command", script)
				cmd.Env = append(os.Environ(), "PATH="+withShims(dir, os.Getenv("PATH")))
				out, _ := cmd.CombinedOutput()
				text := string(out)
				if c.refused && (reachedTheBinary(text) || !strings.Contains(text, "kubetower:")) {
					t.Fatalf("a plugin was not refused (denyWrites=%t): kubectl %v\n%s", denyWrites, c.args, text)
				}
				if c.refused && !(denyWrites && c.args[1] == "install") && !strings.Contains(text, "does not read Secrets") {
					t.Errorf("the plugin refusal is not the no-Secrets one (denyWrites=%t): %s", denyWrites, text)
				}
				if !c.refused && !reachedTheBinary(text) {
					t.Fatalf("a read was refused (denyWrites=%t): kubectl %v\n%s", denyWrites, c.args, text)
				}
			})
		}
	}
}

// A `--raw` path holding a `%`: client-go decodes it before it is sent, so it
// may ask for Secrets while naming none as typed. cmd.exe cannot decode it, so
// every one is refused, whatever it decodes to - the encoded Secret paths the
// POSIX suite refuses, double encoding, one that does not decode, and the cost,
// one that asks for pods.
func TestTheWindowsAssistantOverrideRefusesAPercentEncodedRawPath(t *testing.T) {
	cases := [][]string{
		{"get", "--raw", "/api/v1/namespaces/default/%73ecrets"},
		{"get", "--raw", "/api/v1/namespaces/default/%73%65%63%72%65%74%73/api-tls"},
		{"get", "--raw", "/api/v1/namespaces/default/%53ECRETS"},
		{"get", "--raw=/api/v1/%73ecrets?limit=500"},
		{"get", "--RAW=/api/v1/%73ecrets"},
		{"get", "--raw", "/api/v1/namespaces/default%2Fsecrets"},
		{"get", "--raw", "%2Fapi%2Fv1%2Fsecrets"},
		{"get", "--raw", "/api/v1/namespaces/default/%2573ecrets"},
		{"get", "--raw", "/api/v1/namespaces/default%252Fsecrets"},
		{"get", "--raw", "/api/v1/namespaces/default/%zzecrets"},
		{"get", "--raw", "/api/v1/namespaces/default/pods%"},
		{"-n", "default", "get", "--raw", "/api/v1/namespaces/default/%70ods"},
	}
	results := sweepAssistantShim(t, "kubectl", true, cases)
	for i, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if reachedTheBinary(results[i]) {
				t.Fatalf("the override let a percent-encoded API path through: kubectl %v\n%s", args, results[i])
			}
			if !strings.Contains(results[i], "percent-encoded API path") {
				t.Errorf("refused, but not as a percent-encoded API path: %s", results[i])
			}
		})
	}
}
