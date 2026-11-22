package terminal

// A guard that looks for the verb at the first argument that is not a flag
// stops scanning "kubectl --context X delete ns Y" at "X" and lets the delete
// through. So: every mutating verb, in every flag form a command line puts in
// front of a verb, and no cluster anywhere near it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// prefixes are the shapes a command line takes before it reaches the verb. The
// second and the sixth are the trap: a flag whose value is a separate
// argument, which is what made "the first non-flag argument" the wrong rule.
var prefixes = []struct {
	name string
	args []string
}{
	{"bare", nil},
	{"detached flag value", []string{"--context", "prod"}},
	{"attached flag value", []string{"--context=prod"}},
	{"short flag", []string{"-n", "kube-system"}},
	{"several flags", []string{"--context", "prod", "-n", "default"}},
	{"path-valued flag", []string{"--kubeconfig", "/tmp/k", "--context", "prod"}},
	{"helm flag", []string{"--kube-context", "prod"}},
}

// refusalFor mirrors, in Go, the decision the generated script makes: look at
// every argument, refuse on the first that is a mutating verb.
//
// Two implementations of one rule is normally a smell. Here the alternative is
// a guard whose only test skips on Windows, and a wrong guard here deletes a
// namespace. The shell test
// below cross-checks the two wherever a POSIX shell exists, so they cannot
// disagree unnoticed.
func refusalFor(args []string) (string, bool) { return refusalForBinary("kubectl", args) }

func refusalForBinary(binary string, args []string) (string, bool) {
	verbs := verbsFor(binary)
	for _, arg := range args {
		if slices.Contains(verbs, arg) {
			return arg, true
		}
	}
	return "", false
}

func TestReadOnlyShimRefusesEveryMutatingVerbInEveryFlagForm(t *testing.T) {
	for _, verb := range mutatingVerbs {
		for _, prefix := range prefixes {
			t.Run(verb+"/"+prefix.name, func(t *testing.T) {
				args := append(append([]string{}, prefix.args...), verb, "some-resource", "some-name")
				refused, ok := refusalFor(args)
				if !ok {
					t.Fatalf("the guard let %v through", args)
				}
				if refused != verb {
					t.Fatalf("the guard stopped on %q instead of the verb %q in %v", refused, verb, args)
				}
			})
		}
	}
}

func TestReadOnlyShimAllowsReads(t *testing.T) {
	cases := [][]string{
		{"get", "ns"},
		{"--context", "prod", "get", "pods", "-A"},
		{"describe", "pod", "x"},
		{"logs", "-f", "pod"},
		{"config", "current-context"},
		{"version", "-o", "json"},
		{"top", "nodes"},
		{"events", "--for", "pod/x"},
		{"list"}, // helm
		{"history", "release"},
		{"diff", "upgrade", "release", "./chart"}, // the plugin name is not the verb
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, refused := refusalFor(args); refused {
				// "diff upgrade" is expected to be refused — it carries
				// "upgrade" — so it is listed as a reminder that the broad scan
				// costs false refusals, and that this is the accepted side of
				// the trade.
				if !slices.Contains(args, "upgrade") {
					t.Fatalf("the guard wrongly refused a read: %v", args)
				}
			}
		})
	}
}

// The generated script and the verb list must never drift apart, which is why
// the script is rendered from the list rather than written out by hand.
func TestGeneratedShimContainsEveryVerbAndTheRealBinary(t *testing.T) {
	script := buildReadOnlyShim("kubectl", "/usr/bin/kubectl")
	for _, verb := range mutatingVerbs {
		if !strings.Contains(script, verb) {
			t.Errorf("verb %q is missing from the generated shim", verb)
		}
	}
	if !strings.Contains(script, `real="/usr/bin/kubectl"`) {
		t.Error("the shim does not point at the real binary")
	}
	if !strings.Contains(script, `exec "$real" "$@"`) {
		t.Error("the shim does not hand allowed commands on to the real binary")
	}
}

// --- the same rule, through a real shell -------------------------------------

// runShim renders the override, writes it next to a stand-in binary, and runs
// it. It reports whether the call reached the binary.
func runShim(t *testing.T, args ...string) (allowed bool, output string) {
	t.Helper()
	return runShimFor(t, "kubectl", args...)
}

func runShimFor(t *testing.T, binary string, args ...string) (allowed bool, output string) {
	t.Helper()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this platform; the Go table above covers the rule")
	}

	dir := t.TempDir()

	// Stand-in for kubectl: succeeds and says so, which is how "let through" is
	// told apart from "refused".
	realBin := filepath.Join(dir, "real")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\necho REACHED_REAL_BINARY\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in binary: %v", err)
	}

	shim := filepath.Join(dir, "shim")
	if err := os.WriteFile(shim, []byte(buildReadOnlyShim(binary, realBin)), 0o755); err != nil {
		t.Fatalf("writing the shim: %v", err)
	}

	out, _ := exec.Command(sh, append([]string{shim}, args...)...).CombinedOutput()
	text := string(out)
	return strings.Contains(text, "REACHED_REAL_BINARY"), text
}

// The shell and the Go table must agree. If they ever do not, one of them is
// the rule the cluster actually sees and the other is a comforting fiction.
func TestShellShimAgreesWithTheRule(t *testing.T) {
	cases := [][]string{
		{"get", "pods"},
		{"delete", "ns", "demo"},
		{"--context", "prod", "delete", "ns", "demo"}, // a flag value before the verb
		{"--context=prod", "delete", "ns", "demo"},
		{"-n", "kube-system", "delete", "pod", "x"},
		{"--kubeconfig", "/tmp/k", "--context", "prod", "scale", "deploy/x", "--replicas=0"},
		{"--kube-context", "prod", "uninstall", "release"},
		{"upgrade", "--install", "release", "./chart"},
		{"--context", "prod", "exec", "-it", "pod", "--", "sh"},
		{"drain", "node-1"},
		{"describe", "pod", "x"},
		{"logs", "-f", "pod"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			allowed, out := runShim(t, args...)
			_, refusedByRule := refusalFor(args)
			if allowed == refusedByRule {
				t.Fatalf("shell and rule disagree on %v: shell allowed=%t, rule refuses=%t\noutput: %s",
					args, allowed, refusedByRule, out)
			}
			if !allowed && !strings.Contains(out, "read-only") {
				t.Errorf("the refusal did not explain itself: %s", out)
			}
		})
	}
}

// Every mutating verb, through the shell, behind a detached flag value.
func TestShellShimRefusesEveryVerbBehindADetachedFlagValue(t *testing.T) {
	for _, verb := range mutatingVerbs {
		t.Run(verb, func(t *testing.T) {
			allowed, out := runShim(t, "--context", "prod", verb, "x")
			if allowed {
				t.Fatalf("the shim let %q through behind a detached flag value\noutput: %s", verb, out)
			}
		})
	}
}

// --- writes that are easy to leave off the list -----------------------------

// Each of these is a write, and each is named here as a literal
// command line rather than read out of `mutatingVerbs`.
//
// That is the whole point. The two tables above iterate the list, so they cover
// every verb in it in every flag form and are blind to a verb that is not in it
// — which is how a verb can be missing unnoticed. A test that spells the command out
// fails when somebody removes the word again.
//
// Every case runs through a real /bin/sh, because the guard the cluster sees is
// a shell script and not the Go mirror of it.
func TestTheShimRefusesTheWritesSEC23Found(t *testing.T) {
	for _, c := range []struct {
		name   string
		binary string
		args   []string
		why    string
	}{
		{"kubectl proxy", "kubectl", []string{"proxy", "--port=8001"},
			"an unauthenticated front door to the API server on localhost; the next " +
				"curl -X DELETE through it is a full write and carries no verb this list sees"},
		{"kubectl proxy behind a flag", "kubectl", []string{"--context", "prod", "proxy"},
			"the detached flag form, on the new verb"},
		{"kubectl auth reconcile", "kubectl", []string{"auth", "reconcile", "-f", "rbac.yaml"},
			"writes RBAC; claude.go narrows its allowlist to `auth can-i` for this reason " +
				"and the shim must agree"},
		{"kubectl certificate approve", "kubectl", []string{"certificate", "approve", "csr-1"},
			"issues a client certificate, which is a credential for the cluster"},
		{"kubectl certificate deny", "kubectl", []string{"certificate", "deny", "csr-1"},
			"the other half of the same subcommand"},
		{"helm test", "helm", []string{"test", "my-release"},
			"runs the chart's test hooks, which creates pods"},
		{"helm test behind a flag", "helm", []string{"--kube-context", "prod", "test", "my-release"},
			"the detached flag form, on the helm-only verb"},
	} {
		t.Run(c.name, func(t *testing.T) {
			allowed, out := runShimFor(t, c.binary, c.args...)
			if allowed {
				t.Fatalf("the shim let %q through: %s\noutput: %s", c.name, c.why, out)
			}
			if !strings.Contains(out, "read-only") {
				t.Errorf("the refusal did not explain itself: %s", out)
			}
		})
	}
}

// And the cost of the widening, stated so it cannot be widened further by
// accident.
//
// `test` is a helm word only. `kubectl get pods -n test` has to keep working:
// "test" is one of the most common namespace names there is, and a read refused
// as a mutation teaches an operator that the marking is noise — which is how the
// markings that matter come to be ignored.
func TestTheHelmOnlyVerbIsNotRefusedForKubectl(t *testing.T) {
	allowed, out := runShimFor(t, "kubectl", "get", "pods", "-n", "test")
	if !allowed {
		t.Fatalf("`kubectl get pods -n test` was refused as a mutation: %s", out)
	}
	if _, refused := refusalForBinary("kubectl", []string{"get", "pods", "-n", "test"}); refused {
		t.Error("the Go mirror refuses what the shell allows; one of them is a comforting fiction")
	}
	// The same word, in the binary where it is a write.
	if _, refused := refusalForBinary("helm", []string{"test", "my-release"}); !refused {
		t.Error("`helm test` is not refused by the rule, so the helm-only list is not reaching the shim")
	}
}

// Every helm-only verb, through the shell, behind a detached flag value — the
// helm half of TestShellShimRefusesEveryVerbBehindADetachedFlagValue.
func TestHelmShimRefusesItsOwnVerbsBehindADetachedFlagValue(t *testing.T) {
	if len(helmOnlyVerbs) == 0 {
		t.Fatal("helmOnlyVerbs is empty, so `helm test` is unguarded again")
	}
	for _, verb := range helmOnlyVerbs {
		t.Run(verb, func(t *testing.T) {
			allowed, out := runShimFor(t, "helm", "--kube-context", "prod", verb, "x")
			if allowed {
				t.Fatalf("the helm shim let %q through behind a detached flag value\noutput: %s", verb, out)
			}
		})
	}
}

// A tool's aliases are part of its surface, and the denylist must name them.
//
// **`helm del` and `helm un` must be refused like `helm uninstall`.**
// `helm uninstall` answers to four words — `uninstall`, `del`, `delete`, `un`.
// On a cluster in `KT_READONLY_CLUSTERS`, and in every Claude session since
// those are read-only by default, an unlisted alias such as `helm del
// ingress-nginx` would remove the release.
//
// The aliases are established against the shipped binary rather than its
// documentation: `helm del <a release that does not exist>` reaches the real
// helm and answers `Error: uninstall: Release not loaded`. That is what reaching
// it looks like — and it is also how a guard can be tested without destroying
// anything, per the checklist's "use a resource that does not exist".
//
// The reads keep their aliases: `helm ls` and `helm hist` must still work, or the
// guardrail refuses the thing it exists to permit.
func TestTheShimKnowsHelmsAliasesAndNotOnlyItsNames(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		why  string
	}{
		{"helm del", []string{"del", "my-release"},
			"an alias for uninstall; it removes the release"},
		{"helm un", []string{"un", "my-release"},
			"the other undocumented alias for uninstall"},
		{"helm delete", []string{"delete", "my-release"},
			"already covered by mutatingVerbs, asserted so a tidy-up cannot drop it"},
		{"helm del behind a flag", []string{"--kube-context", "prod", "del", "my-release"},
			"the detached flag form, on the alias"},
	} {
		t.Run(c.name, func(t *testing.T) {
			allowed, out := runShimFor(t, "helm", c.args...)
			if allowed {
				t.Fatalf("the shim let %q through: %s\noutput: %s", c.name, c.why, out)
			}
			if !strings.Contains(out, "read-only") {
				t.Errorf("the refusal did not explain itself: %s", out)
			}
		})
	}

	// And the reads are still reads. A denylist that grows until it refuses `helm
	// ls` has stopped being a guardrail and started being an outage — which is the
	// same trade `test` is documented under, from the other side.
	for _, c := range []struct {
		name string
		args []string
	}{
		{"helm ls", []string{"ls"}},
		{"helm hist", []string{"hist", "my-release"}},
		{"helm list", []string{"list", "-n", "kube-system"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if allowed, out := runShimFor(t, "helm", c.args...); !allowed {
				t.Errorf("the shim refused %q, which is a read: %s", c.name, out)
			}
		})
	}
}
