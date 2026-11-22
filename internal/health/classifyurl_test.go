package health

import (
	"errors"
	"strings"
	"testing"
)

// The classifier must read the failure and not the address it was fetching.
//
// **Reading the address makes the page name the wrong person.** client-go
// formats a transport failure as `Get "<url>": <reason>` and puts the configured
// timeout into every request URL as `?timeout=32s`. A timeout arm testing for a
// bare "timeout" matches the URL and answers StateTimeout — which internal/web
// prints as "Whoever runs the network".
//
// Because that arm sits fifth, it would shadow every arm below it. So the
// assertion worth making is not only "this one error classifies correctly" but
// "a URL cannot reach any vocabulary at all".
//
// The message below has the shape of a real discovery call against an EKS
// context (example-nonprod) whose credential plugin failed, while the endpoint
// itself answered — so "the cluster is down" would be measurably false.
const observedEKSPluginRefusal = `Get "https://0123456789ABCDEF0123456789ABCDEF.sk1.eu-west-3.eks.amazonaws.com/version?timeout=32s": getting credentials: exec: executable aws failed with exit code 254`

func TestARequestURLCannotDecideWhatWentWrong(t *testing.T) {
	state, detail, _ := classify(errors.New(observedEKSPluginRefusal))

	if state == StateTimeout {
		t.Fatalf("a credential plugin that refused in 1.6s was classified %q, so the page "+
			"blames whoever runs the network for a cluster that answered 401 in 131ms. "+
			"The only word matching that arm is the `?timeout=32s` in the URL", state)
	}
	if state != StatePlugin {
		t.Errorf("classified %q, want %q — this is exactly the error the exit-code arm was "+
			"written for", state, StatePlugin)
	}
	// Both channels, and not one. The plugin's stderr is this
	// process's stderr, which is `docker compose logs` in the container and
	// `kubetower.log` in the state directory on a downloaded binary — where there
	// is no compose file at all. Naming a command the reader's installation does
	// not have sends them nowhere, which is the same defect as naming a banner
	// that is not drawn.
	for _, want := range []string{"docker compose logs", "kubetower.log"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail does not name %q as a place to look. The plugin's own "+
				"message is not in the error, and an installation has exactly one of the "+
				"two channels.\n  got: %s", want, detail)
		}
	}
}

// Every arm below the timeout one would be unreachable for any error carrying a
// URL, which is every transport error there is. One case each, so a re-widened needle
// list names which diagnosis it swallowed rather than failing as a single opaque
// case.
func TestTheArmsBelowTheTimeoutOneAreReachableThroughAURL(t *testing.T) {
	const url = `Get "https://example.eks.amazonaws.com/version?timeout=32s": `

	cases := []struct {
		what    string
		reason  string
		want    string
		swallow string
	}{
		{"a rejected certificate", "x509: certificate signed by unknown authority",
			StateKO, "a certificate this console could be taught to trust"},
		{"an RBAC refusal", `pods is forbidden: User "x" cannot list resource "pods"`,
			StateKO, "a binding the cluster's owner grants"},
		// StateMissing: a binary that is not there has a state of its own, so
		// it does not fall through to "Nobody yet". The property this case is in
		// the table for is that a URL must not decide it.
		{"a missing plugin binary", "getting credentials: exec: executable aws not found",
			StateMissing, "a binary missing from the image, or a PATH that cannot see it"},
		{"a lapsed session", "getting credentials: Error loading SSO Token: Token has expired",
			StateSSO, "ten seconds of the operator's own work"},
		{"a refused role", "An error occurred (AccessDenied) when calling the AssumeRole operation",
			StateIAM, "an IAM grant only a cloud administrator can make"},
		{"a name that does not resolve", `dial tcp: lookup example.internal: no such host`,
			StateDNS, "a VPN that is not up"},
		{"an address that refused", "dial tcp 127.0.0.1:6443: connect: connection refused",
			StateRefused, "the loopback trap, which is this console's own configuration"},
	}

	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			state, _, _ := classify(errors.New(url + c.reason))
			if state == StateTimeout && c.want != StateTimeout {
				t.Fatalf("the URL decided this: %q was reported as a network timeout, "+
					"hiding %s", c.what, c.swallow)
			}
			if state != c.want {
				t.Errorf("classified %q, want %q\n  from: %s", state, c.want, c.reason)
			}
		})
	}
}

// And a real timeout must still be one, or the strip has traded a wrong answer for
// a different wrong answer.
func TestARealTimeoutIsStillATimeout(t *testing.T) {
	for _, message := range []string{
		`Get "https://example.eks.amazonaws.com/version?timeout=32s": dial tcp 10.0.0.1:443: i/o timeout`,
		`Get "https://example.eks.amazonaws.com/version?timeout=32s": context deadline exceeded`,
		`Get "https://example.eks.amazonaws.com/version?timeout=32s": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`,
		`Get "https://example.eks.amazonaws.com/version?timeout=32s": net/http: TLS handshake timeout`,
	} {
		if state, _, _ := classify(errors.New(message)); state != StateTimeout {
			t.Errorf("a genuine timeout classified as %q\n  from: %s", state, message)
		}
	}
}

// The strip must take the address and nothing else: `lookup` and `connection
// refused` sit in the reason, but a needle that only ever appeared in a hostname
// would be silently lost.
func TestStrippingTheURLKeepsTheReasonIntact(t *testing.T) {
	got := withoutRequestURL(`Get "https://a.b.c/version?timeout=32s": dial tcp: lookup a.b.c: no such host`)
	for _, want := range []string{"lookup ", "no such host", "dial tcp"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was removed with the URL\n  left: %s", want, got)
		}
	}
	if strings.Contains(got, "timeout=32s") {
		t.Errorf("the URL survived, so the word that caused this bug is still readable: %s", got)
	}
}

// The two halves of that defense cover each other, which means neither is tested by
// the case above: stripping the URL removes `?timeout=32s` before the needles
// run, and the phrase needles ignore it even when it is left in. Putting either
// half back on its own leaves the suite green.
//
// So each half needs the case the *other* cannot rescue. These two are those
// cases, and they are the only reason both halves are here rather than one.

// The strip's own case: a diagnostic word inside a hostname. Narrowing the
// vocabulary cannot help — "certificate" is precisely the word the certificate
// arm must go on matching.
func TestAWordInTheHostnameIsNotADiagnosis(t *testing.T) {
	const message = `Get "https://certificate-authority.k8s.internal/version?timeout=32s": ` +
		`getting credentials: exec: executable aws failed with exit code 254`

	state, detail, _ := classify(errors.New(message))
	if strings.Contains(detail, "certificate was rejected") {
		t.Fatalf("the cluster's hostname decided this: a credential plugin that refused is "+
			"reported as a TLS problem, and the operator goes looking at a certificate "+
			"that was never presented.\n  detail: %s", detail)
	}
	if state != StatePlugin {
		t.Errorf("classified %q, want %q", state, StatePlugin)
	}
}

// The vocabulary's own case: a diagnostic word inside a Kubernetes name, where
// there is no URL to strip. A namespace called `timeouts` is an ordinary thing
// for a team that tests them to own.
func TestAWordInAKubernetesNameIsNotADiagnosisEither(t *testing.T) {
	const message = `pods is forbidden: User "system:serviceaccount:kt:kubetower" cannot ` +
		`list resource "pods" in API group "" in the namespace "timeouts"`

	state, _, _ := classify(errors.New(message))
	if state == StateTimeout {
		t.Fatalf("a namespace named %q made an RBAC refusal read as a network timeout. The "+
			"page then names whoever runs the network for a missing binding, and no URL "+
			"was involved — only a bare word in the vocabulary", "timeouts")
	}
	if state != StateKO {
		t.Errorf("classified %q, want %q — the credentials are valid and may not read this",
			state, StateKO)
	}
}
