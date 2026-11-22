package terminal

// The assistant's override, run through a real shell, one literal command line
// at a time.
//
// **Literal command lines and not table rows**, which is `readonly.go`'s own
// rule: a verb missing from the table *and* from the list the table is
// generated from leaves the table agreeing with itself and the cluster
// disagreeing with both. A command line written out in
// full is the only form that can be wrong in a way a reader notices.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runAssistantShim writes the override next to a stand-in binary and runs it
// through `sh`, reporting whether the call reached the binary.
func runAssistantShim(t *testing.T, binary string, denyWrites bool, args ...string) (reached bool, output string) {
	t.Helper()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this platform; nosecrets_windows_test.go covers the other one")
	}

	dir := t.TempDir()
	realBin := filepath.Join(dir, "real")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\necho REACHED_REAL_BINARY\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in binary: %v", err)
	}
	shim := filepath.Join(dir, "shim")
	if err := os.WriteFile(shim, []byte(buildAssistantShim(binary, realBin, denyWrites)), 0o755); err != nil {
		t.Fatalf("writing the shim: %v", err)
	}

	out, _ := exec.Command(sh, append([]string{shim}, args...)...).CombinedOutput()
	text := string(out)
	return strings.Contains(text, "REACHED_REAL_BINARY"), text
}

// Every spelling of a Secret read that the installed kubectl accepts.
//
// The list of spellings is not recalled: `nosecrets.go`'s header records the
// `kubectl api-resources` output it was taken from and the live probe of each
// one. `secrets` has no short name, which is the fact a guess would have got
// wrong in the expensive direction.
func TestTheAssistantShimRefusesEverySpellingOfASecretRead(t *testing.T) {
	for _, args := range [][]string{
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

		// kubectl matches a resource case-insensitively; a POSIX `case` does not.
		{"get", "Secret"},
		{"get", "SECRET"},
		{"get", "Secrets"},
		{"get", "sEcReT"},

		// The verb is not what is matched, so every reading verb is covered by
		// the one pattern set.
		{"describe", "secret", "api-tls"},
		{"get", "secret", "api-tls", "-o", "yaml"},
		{"get", "secret", "api-tls", "-o", "json"},
		{"get", "secret", "api-tls", "-o", "jsonpath={.data}"},
		{"get", "secret", "api-tls", "-o", "go-template={{.data}}"},
		{"get", "secret", "api-tls", "--template={{.data}}"},
		{"get", "secrets", "-A"},
		{"get", "secrets", "--all-namespaces"},
		{"get", "secrets", "-o", "yaml", "-n", "kube-system"},

		// The flag before the resource, which is the shape a scan that trusts a
		// position misses, as with `--context prod delete ns X`: every argument is
		// examined, and no position is trusted.
		{"--context", "prod", "get", "secret"},
		{"--kubeconfig", "/tmp/k", "-n", "argo", "get", "secret/registry-pull"},

		// `--raw` against an API path, in both spellings. This is the one that
		// names no resource word at all.
		{"get", "--raw", "/api/v1/namespaces/default/secrets"},
		{"get", "--raw", "/api/v1/namespaces/default/secrets/api-tls"},
		{"get", "--raw", "/api/v1/secrets?limit=500"},
		{"get", "--raw=/api/v1/namespaces/default/secrets"},

		// The same path percent-encoded: client-go decodes it before it is sent,
		// so it asks for Secrets while naming none as typed.
		{"get", "--raw", "/api/v1/namespaces/default/%73ecrets"},
		{"get", "--raw", "/api/v1/namespaces/default/%73%65%63%72%65%74%73/api-tls"},
		{"get", "--raw", "/api/v1/namespaces/default/%53ECRETS"},
		{"get", "--raw=/api/v1/%73ecrets?limit=500"},
		// `%2F` for the slash, in one segment and throughout.
		{"get", "--raw", "/api/v1/namespaces/default%2Fsecrets"},
		{"get", "--raw", "/api/v1/namespaces/default%2fsecrets"},
		{"get", "--raw", "%2Fapi%2Fv1%2Fsecrets"},
		// Double encoding: `%25` is `%`, so this decodes to `%73ecrets`, then to
		// `secrets`.
		{"get", "--raw", "/api/v1/namespaces/default/%2573ecrets"},
		{"get", "--raw", "/api/v1/namespaces/default%252Fsecrets"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			reached, out := runAssistantShim(t, "kubectl", true, args...)
			if reached {
				t.Fatalf("the shim let a Secret read through: kubectl %s", strings.Join(args, " "))
			}
			if !strings.Contains(out, "names a Secret") {
				t.Errorf("refused, but not as a Secret read — so the operator is told the wrong thing: %q", out)
			}
		})
	}
}

// A comma list names several resource types in one argument, and kubectl reads
// each of them. `kubectl get secrets,configmaps -o yaml` must not reach the real
// binary, and patterns anchored at the start of the argument or behind a `/`
// alone would match nothing for a Secret behind a comma, or before one.
//
// Every spelling the single forms above refuse, in every position of the list.
func TestTheAssistantShimRefusesASecretInACommaList(t *testing.T) {
	for _, args := range [][]string{
		{"get", "secrets,configmaps", "-o", "yaml"},
		{"get", "secret,configmaps"},
		{"get", "configmaps,secrets"},
		{"get", "configmaps,secret"},
		{"get", "pods,secrets,configmaps"},
		{"get", "pods,secret,configmaps", "-A"},
		{"get", "Pods,SECRETS"},
		{"get", "configmaps,Secret"},
		{"get", "configmap/app,secret/api-tls"},
		{"get", "configmap/app,secrets/api-tls,pod/api-0"},
		{"get", "secret/api-tls,configmap/app"},
		{"get", "pods,secrets.v1."},
		{"get", "pods,secret.v1.,configmaps"},
		{"get", "secrets.v1.,pods"},
		{"--context", "prod", "get", "deploy,secrets", "-o", "json"},
		{"describe", "pods,secrets"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			reached, out := runAssistantShim(t, "kubectl", true, args...)
			if reached {
				t.Fatalf("the shim let a Secret read through a comma list: kubectl %s", strings.Join(args, " "))
			}
			if !strings.Contains(out, "names a Secret") {
				t.Errorf("refused, but not as a Secret read: %q", out)
			}
		})
	}
}

// And the lists that name no Secret still reach the binary — including the
// custom resources whose names start with the word, which is why no pattern is
// `,secret*`.
func TestTheAssistantShimLetsACommaListWithoutASecretThrough(t *testing.T) {
	for _, args := range [][]string{
		{"get", "configmaps,pods"},
		{"get", "pods,svc,deploy", "-A"},
		{"get", "configmap/app,pod/api-0"},
		{"get", "pods,secretstores"},
		{"get", "externalsecrets,sealedsecrets"},
		{"get", "pods,secretstore.external-secrets.io"},
		{"get", "pods", "-l", "app=secret-operator,tier=db"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if reached, out := runAssistantShim(t, "kubectl", true, args...); !reached {
				t.Fatalf("a list naming no Secret was refused: kubectl %s — %q", strings.Join(args, " "), out)
			}
		})
	}
}

// The Windows override cannot run here, so its comma handling is pinned on the
// text: `nosecrets_windows_test.go` runs it, on Windows only, and no CI job runs
// that file. A quoted list arrives as one argument, so both halves are needed —
// `,` as a `for /f` delimiter for the first element, and the substring
// replacement for any later one.
func TestTheWindowsAssistantOverrideScansACommaList(t *testing.T) {
	script := buildAssistantShimCmd("kubectl", `C:\bin\kubectl.exe`, true)
	for _, want := range []string{
		`call set "ktstrip=%%ktarg:,secret=%%"`,
		`for /f "tokens=1 delims=/.," %%H in ("%ktarg%") do if /I "%%H"=="secret"`,
		`for /f "tokens=1 delims=/.," %%H in ("%ktarg%") do if /I "%%H"=="secrets"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the Windows override does not carry %q:\n%s", want, script)
		}
	}
}

// The other half, and the half that fails silently: a denylist that refuses
// everything passes the test above.
//
// `secretstore` and `externalsecret` are here deliberately. They are somebody
// else's custom resources holding a reference or a ciphertext rather than a
// credential, and `secret*` as a pattern would take both — which is why
// `secretReadPatterns` spells `secret.*` with the dot.
func TestTheAssistantShimLetsOrdinaryReadsThrough(t *testing.T) {
	for _, args := range [][]string{
		{"get", "pods"},
		{"get", "pods", "-A", "-o", "yaml"},
		{"describe", "pod", "api-0"},
		{"logs", "-f", "api-0"},
		{"top", "pod"},
		{"get", "configmap", "app-config", "-o", "yaml"},
		{"get", "sa", "default", "-o", "yaml"},
		{"get", "serviceaccount", "default"},
		{"get", "secretstore", "vault"},
		{"get", "externalsecret", "api-tls"},
		{"get", "sealedsecret", "api-tls"},
		{"get", "events", "--field-selector", "type=Warning"},
		{"get", "pod", "secret-operator-0"},
		{"get", "--raw", "/api/v1/namespaces/default/pods"},
		{"get", "--raw", "/api/v1/namespaces/default/%70ods"},
		{"get", "--raw", "/api/v1/pods?labelSelector=app%3Dweb"},
		// A `%` outside a `--raw` path is not decoded: a template's verb.
		{"get", "pods", "-o", `go-template={{printf "%s" .metadata.name}}`},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			reached, out := runAssistantShim(t, "kubectl", true, args...)
			if !reached {
				t.Fatalf("an ordinary read was refused: kubectl %s — %q", strings.Join(args, " "), out)
			}
		})
	}
}

// helm's own doors onto the same material. A release lives in a Secret, and
// `helm get manifest` prints the objects it rendered — Secrets included, with
// their data. `helm get notes` and `helm status` print the release's notes,
// where charts write the passwords they generated.
func TestTheAssistantShimRefusesHelmsWaysIntoARelease(t *testing.T) {
	for _, args := range [][]string{
		{"get", "manifest", "ingress-nginx"},
		{"get", "all", "ingress-nginx"},
		{"get", "values", "ingress-nginx"},
		{"get", "hooks", "ingress-nginx"},
		{"get", "manifest", "-n", "prod", "ingress-nginx"},
		{"get", "notes", "ingress-nginx"},
		{"status", "ingress-nginx"},
		{"status", "ingress-nginx", "-o", "json"},
	} {
		t.Run("helm "+strings.Join(args, " "), func(t *testing.T) {
			if reached, out := runAssistantShim(t, "helm", true, args...); reached {
				t.Fatalf("helm %s reached the binary: %q", strings.Join(args, " "), out)
			}
		})
	}

	// And those words are helm's alone: `kubectl get all` is one of the most
	// ordinary reads there is, and refusing it is how a marking becomes noise.
	for _, args := range [][]string{
		{"get", "all"},
		{"get", "all", "-A"},
		{"get", "cm", "values"},
	} {
		t.Run("kubectl "+strings.Join(args, " "), func(t *testing.T) {
			if reached, out := runAssistantShim(t, "kubectl", true, args...); !reached {
				t.Fatalf("kubectl %s was refused by helm's word list: %q", strings.Join(args, " "), out)
			}
		})
	}

	// helm's ordinary reads still work.
	for _, args := range [][]string{
		{"list"},
		{"list", "-A"},
		{"history", "ingress-nginx"},
	} {
		t.Run("helm ordinary "+strings.Join(args, " "), func(t *testing.T) {
			if reached, out := runAssistantShim(t, "helm", true, args...); !reached {
				t.Fatalf("an ordinary helm read was refused: helm %s — %q", strings.Join(args, " "), out)
			}
		})
	}
}

// The write guardrail sits on top of the read one and is separately switchable,
// because `KT_CLAUDE_READONLY=false` is an option the operator has.
//
// Both directions, because each fails silently on its own: a session that may
// write must still be refused a Secret, and a read-only session must be refused
// a Secret *by the read scan* — a write refusal on `get secret` would mean the
// read scan had stopped running and nothing would say so.
func TestTheReadAndWriteGuardrailsAreIndependent(t *testing.T) {
	if reached, out := runAssistantShim(t, "kubectl", false, "get", "secret", "api-tls"); reached {
		t.Fatalf("a writable assistant session read a Secret: %q", out)
	}
	if reached, out := runAssistantShim(t, "kubectl", false, "delete", "pod", "api-0"); !reached {
		t.Fatalf("a session the operator allowed to write was refused a write: %q", out)
	}

	if _, out := runAssistantShim(t, "kubectl", true, "get", "secret", "api-tls"); !strings.Contains(out, "names a Secret") {
		t.Errorf("a read-only session's Secret refusal came from the write scan: %q", out)
	}
	if _, out := runAssistantShim(t, "kubectl", true, "delete", "pod", "api-0"); !strings.Contains(out, "read-only") {
		t.Errorf("the read-only refusal is not the mutating-verb one: %q", out)
	}
}

// What this does NOT cover, asserted so the gap is in the suite rather than only
// in a comment somebody can stop reading.
//
// Each of these reaches the real binary today. They are not defects to be fixed
// quietly later; they are the shape of a guardrail, and `nosecrets.go`'s header
// says what the real boundary would be.
func TestWhatTheAssistantShimDoesNotCover(t *testing.T) {
	for name, args := range map[string][]string{
		// The Secret is named inside a file this shim does not open. Reading it
		// would mean a YAML parser in a `case` statement.
		"a manifest passed with -f": {"get", "-f", "/tmp/secret.yaml"},
		// The absolute path resolves nothing through PATH, so no override runs.
		// Written here as its own argument shape rather than as a second binary,
		// because the point is that the shim never sees it at all.
		"a read whose resource word arrives from a variable": {"get", "$KIND"},
	} {
		t.Run(name, func(t *testing.T) {
			if reached, out := runAssistantShim(t, "kubectl", true, args...); !reached {
				t.Fatalf("this is documented as NOT covered, and it was refused — "+
					"update nosecrets.go's header, which claims otherwise: %q", out)
			}
		})
	}
}

// The wiring, which is where a guardrail that works in a unit test stops
// existing: three override directories are written, they hold different files,
// and the ones an assistant gets are the ones that refuse a Secret.
//
// **Asserted on the files on disk**, not on the builders. `writeReadOnlyShims`
// is the only thing that decides which script lands under which name, and the
// a correct script written to a file nothing resolves protects nothing.
func TestThreeOverrideSetsAreWrittenAndTheyDiffer(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("no kubectl on this machine, so no override is installed for it")
	}

	root := t.TempDir()
	term := &Terminal{opts: Options{WorkRoot: root}, logf: func(string, ...any) {}}
	if err := term.writeReadOnlyShims(); err != nil {
		t.Fatalf("writing the overrides: %v", err)
	}

	read := func(dir string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(dir, shimFileName("kubectl")))
		if err != nil {
			t.Fatalf("reading the override in %s: %v", dir, err)
		}
		return string(body)
	}

	shell, assistant, assistantRO := read(term.roBinDir), read(term.assistantBinDir), read(term.assistantROBinDir)

	// A shell on a read-only cluster is unchanged by this: an operator who marked
	// a cluster read-only asked for their writes to be refused, not their reads.
	if strings.Contains(shell, "names a Secret") {
		t.Error("the read-only shell override now refuses Secret reads, which nobody asked for")
	}
	for name, body := range map[string]string{"assistant-bin": assistant, "assistant-ro-bin": assistantRO} {
		if !strings.Contains(body, "names a Secret") {
			t.Errorf("%s does not carry the Secret scan", name)
		}
	}
	// And the pair differs in exactly the thing that distinguishes them.
	if strings.Contains(assistant, "this session is read-only") {
		t.Error("assistant-bin refuses writes, so KT_CLAUDE_READONLY=false would do nothing")
	}
	if !strings.Contains(assistantRO, "this session is read-only") {
		t.Error("assistant-ro-bin does not refuse writes, so the default session can write")
	}
	// Three directories, three paths, none of them empty - an empty one prepended
	// to PATH is the current directory, which is worse than no override at all.
	for name, dir := range map[string]string{
		"roBinDir": term.roBinDir, "assistantBinDir": term.assistantBinDir,
		"assistantROBinDir": term.assistantROBinDir,
	} {
		if dir == "" {
			t.Errorf("%s is empty", name)
		}
	}
}

// The session's own notes say Secrets are out of bounds.
//
// **This is a statement to a cooperative reader and not the enforcement**, which
// is the override above. It is asserted because a refusal with no explanation
// reads as a broken tool, and because notes claiming "reads are unrestricted"
// would be untrue.
func TestTheSessionsNotesSayThatSecretsAreOutOfBounds(t *testing.T) {
	for _, view := range []claudeView{
		{Cluster: "prod", Kind: "Pod", Name: "api-0", ReadOnly: true},
		{Cluster: "prod", Kind: "Pod", Name: "api-0", ReadOnly: false},
		{Cluster: "prod"},
	} {
		notes := buildClaudeContext(view)
		if !strings.Contains(notes, "Secrets are out of bounds") {
			t.Errorf("the notes do not mention Secrets (read-only: %t): %s", view.ReadOnly, notes)
		}
		if strings.Contains(notes, "Reads are unrestricted") {
			t.Errorf("the notes still claim reads are unrestricted (read-only: %t)", view.ReadOnly)
		}
	}
}

// A `--raw` path that does not decode is refused: what it would ask for cannot
// be told, and url.PathUnescape refuses it too.
func TestTheAssistantShimRefusesARawPathThatDoesNotDecode(t *testing.T) {
	for _, args := range [][]string{
		{"get", "--raw", "/api/v1/namespaces/default/%zzecrets"},
		{"get", "--raw", "/api/v1/namespaces/default/%7"},
		{"get", "--raw", "/api/v1/namespaces/default/pods%"},
		{"get", "--raw=/api/v1/%g3ecrets"},
		// Decodes once to `%7`, which does not.
		{"get", "--raw", "/api/v1/namespaces/default/%257"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			reached, out := runAssistantShim(t, "kubectl", true, args...)
			if reached {
				t.Fatalf("the shim let an undecodable API path through: kubectl %s", strings.Join(args, " "))
			}
			if !strings.Contains(out, "does not decode") {
				t.Errorf("refused, but not as a path that does not decode: %q", out)
			}
		})
	}
}
