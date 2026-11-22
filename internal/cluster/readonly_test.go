package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The four shapes an entry in KT_READONLY_CLUSTERS can arrive in.
//
// The assertions are on what a request sees — `Has`, which is the
// `func(string) bool` every write path in this project consults — and on what
// the operator is told at boot, because only some of the failures are visible
// from a return value. An entry that names nothing has
// to *say so*: a marking that silently does not apply is worse than no marking,
// since the operator believes the cluster is protected.

// theARN is a context named after its EKS ARN, which is the case that makes the
// difference between comparing a string and resolving a name. Fictional account
// and cluster: nothing here may name a real installation.
const theARN = "arn:aws:eks:eu-west-1:000000000000:cluster/example-prod-eks"

// kubeconfigWithContexts writes a kubeconfig declaring the named contexts, all
// pointing at server. A file rather than a hand-built Manager because Resolve is
// the thing under test and it reads what was loaded from disk.
//
// ca nil means "trust nothing and skip verification": the readonly tests never
// open a connection, and the ones that do pass a real certificate.
func kubeconfigWithContexts(t *testing.T, server string, ca string, names ...string) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\nclusters:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- name: %q\n  cluster:\n    server: %s\n", name, server)
		if ca == "" {
			b.WriteString("    insecure-skip-tls-verify: true\n")
		} else {
			fmt.Fprintf(&b, "    certificate-authority-data: %s\n", ca)
		}
	}
	b.WriteString("contexts:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- name: %q\n  context:\n    cluster: %q\n    user: %q\n", name, name, name)
	}
	b.WriteString("users:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- name: %q\n  user:\n    token: TEST-TOKEN\n", name)
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "current-context: %q\n", names[0])
	}

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	return path
}

// markingsFor builds the set the way internal/launch/run.go does, and captures what it said.
func markingsFor(t *testing.T, raw string, names ...string) (*ReadOnly, []string) {
	t.Helper()

	m, err := NewManager(kubeconfigWithContexts(t, "https://127.0.0.1:6443", "", names...))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	var said []string
	marks := newReadOnly(raw, m.Resolve, m.Contexts, func(format string, args ...any) {
		said = append(said, fmt.Sprintf(format, args...))
	})
	return marks, said
}

func TestTheReadOnlyMarkingResolvesEveryFormOfAName(t *testing.T) {
	contexts := []string{"docker-desktop", theARN}
	friendly := MakeDNSFriendly(theARN)

	cases := []struct {
		name string
		// raw is the value of KT_READONLY_CLUSTERS.
		raw string
		// refused is the names a write must be refused on, in every spelling a
		// caller can hand over — a URL segment or a resolved context.
		refused []string
		// allowed is the names that must still be writable. A guardrail that marks
		// the wrong cluster is its own defect.
		allowed []string
		// announced is a fragment the operator must be told at boot.
		announced string
		// unmatched is what the set reports as naming nothing.
		unmatched []string
	}{
		{
			name:      "the exact context name",
			raw:       "docker-desktop",
			refused:   []string{"docker-desktop"},
			allowed:   []string{theARN, friendly},
			announced: "read-only: docker-desktop",
		},
		{
			// The URL spelling of a name, which is not the context name. Older
			// links carry it, so it is what an operator pastes out of the address
			// bar — and comparing
			// it as a string marks nothing on precisely the clusters the guardrail
			// was written for.
			name:      "the DNS-friendly form older links carry",
			raw:       friendly,
			refused:   []string{theARN, friendly},
			allowed:   []string{"docker-desktop"},
			announced: "which is the URL form of",
		},
		{
			name:      "surrounding whitespace and a list",
			raw:       "  docker-desktop ,\t" + theARN + "  ",
			refused:   []string{"docker-desktop", theARN, friendly, " docker-desktop "},
			allowed:   []string{"kind-kind"},
			announced: "read-only: docker-desktop, " + theARN,
		},
		{
			// The one that needs a voice. Nothing is protected, and something
			// must say so.
			name:      "a name that matches no context",
			raw:       "dcoker-desktop",
			refused:   nil,
			allowed:   []string{"docker-desktop", theARN, friendly},
			announced: `names "dcoker-desktop", which is not a context in this kubeconfig`,
			unmatched: []string{"dcoker-desktop"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marks, said := markingsFor(t, tc.raw, contexts...)

			// Taken as the callback, not called as a method: what
			// internal/launch/run.go hands the five packages is this value, and
			// the signature is part of the contract.
			var guard func(string) bool = marks.Has

			for _, name := range tc.refused {
				if !guard(name) {
					t.Errorf("a write to %q is allowed. KT_READONLY_CLUSTERS=%q was meant to "+
						"refuse it, and an operator reading that variable believes it does",
						name, tc.raw)
				}
			}
			for _, name := range tc.allowed {
				if guard(name) {
					t.Errorf("a write to %q is refused, and nothing in KT_READONLY_CLUSTERS=%q "+
						"names it", name, tc.raw)
				}
			}

			joined := strings.Join(said, "\n")
			if !strings.Contains(joined, tc.announced) {
				t.Errorf("the operator was told:\n%s\nwant a line containing %q", joined, tc.announced)
			}

			if got := strings.Join(marks.Unmatched(), ","); got != strings.Join(tc.unmatched, ",") {
				t.Errorf("Unmatched() = %v, want %v", marks.Unmatched(), tc.unmatched)
			}
		})
	}
}

// An unset variable marks nothing, and says that too.
//
// The line is not decoration: KT_READONLY_CLUSTERS set in a .env that never
// reached the process is indistinguishable, from every screen, from one that
// did. This is the only place that difference is visible.
func TestNoMarkingIsAnnouncedAsLoudlyAsOne(t *testing.T) {
	marks, said := markingsFor(t, "   ", "docker-desktop")

	if marks.Has("docker-desktop") {
		t.Error("an empty KT_READONLY_CLUSTERS marked a cluster read-only")
	}
	if len(marks.Marked()) != 0 {
		t.Errorf("Marked() = %v on an empty variable", marks.Marked())
	}
	if want := "no cluster is marked read-only"; !strings.Contains(strings.Join(said, "\n"), want) {
		t.Errorf("boot said %q, want a line containing %q", said, want)
	}
}

// A nil set is the "no guardrail configured" case, and must not panic on the
// path every write goes through.
func TestANilMarkingRefusesNothing(t *testing.T) {
	var marks *ReadOnly
	if marks.Has("docker-desktop") {
		t.Error("a nil ReadOnly marked a cluster")
	}
	if marks.Marked() != nil || marks.Unmatched() != nil {
		t.Error("a nil ReadOnly reported entries")
	}
}

// An entry naming a context that only appears later is still honored.
//
// It is filed under what it said, and Has resolves live rather than at boot, so
// a kubeconfig reloaded with that context in it protects it without a restart.
// Worth a test because the alternative implementation — resolve everything once
// and keep only what matched — is the obvious one and loses this silently.
func TestAnEntryStartsWorkingWhenItsContextAppears(t *testing.T) {
	path := kubeconfigWithContexts(t, "https://127.0.0.1:6443", "", "docker-desktop")
	m, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	marks := newReadOnly(theARN, m.Resolve, m.Contexts, func(string, ...any) {})

	if !marks.Has(theARN) {
		t.Fatal("an unmatched entry stopped marking even the name it literally names")
	}

	// The context arrives.
	later := kubeconfigWithContexts(t, "https://127.0.0.1:6443", "", "docker-desktop", theARN)
	raw, err := os.ReadFile(later)
	if err != nil {
		t.Fatalf("reading the second kubeconfig: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("rewriting the kubeconfig: %v", err)
	}
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if !marks.Has(MakeDNSFriendly(theARN)) {
		t.Error("after a reload the marked context is reachable under its URL form and no " +
			"longer marked — the set resolved once at boot and kept the answer")
	}
}
