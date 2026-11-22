package health

import (
	"errors"
	"strings"
	"testing"
)

// The classifier's SSO arm against the strings the **SDK** produces, which is
// the path most EKS contexts take.
//
// Why this file exists at all: a recognized EKS exec block is not run.
// `internal/cluster/manager.go` nils `ExecProvider` and signs the presigned STS
// URL in this process (`internal/ekstoken`), so what reaches `classify` for
// those contexts is written by `aws-sdk-go-v2` and not by the AWS CLI, and the
// SDK has two SSO paths, both of which the arm has to match.
//
// **Every string below is quoted from the module in go.mod, with the file and
// line, so the next reader can check whether it still says that rather than
// trusting this test.** The module is `github.com/aws/aws-sdk-go-v2/credentials
// v1.20.1`; re-derive with:
//
//	grep -rn 'expired or is invalid\|cached SSO token' \
//	  "$(go env GOMODCACHE)/github.com/aws/aws-sdk-go-v2/credentials@v1.20.1/ssocreds/"
//
// A version bump that rewords one of them turns this test red, which is the
// whole point: otherwise the arm would go quiet instead, and a quiet arm is a row
// that reads *"unclassified — Nobody yet"* for the commonest failure an operator
// can actually fix in ten seconds.
func TestTheSSOArmMatchesWhatTheAWSSDKActuallySays(t *testing.T) {
	// How the message arrives: net/http prefixes the RoundTrip failure with the
	// address, ekstoken.Provider.Token wraps it, and aws.CredentialsCache wraps
	// that. The prefix matters — a destination is not a diagnosis, and the
	// classifier must read past it — so the cases carry it rather than testing a
	// bare sentence.
	const prefix = `Get "https://ABC123.gr7.eu-west-3.eks.amazonaws.com/version?timeout=32s": ` +
		`signing a token for "prod-eks": operation error STS: PresignGetCallerIdentity, ` +
		`get identity: get credentials: failed to refresh cached credentials, `

	cases := []struct {
		name string
		// where the string comes from, quoted in the failure so a reader is sent
		// to the source and not to this file.
		source  string
		message string
		// why this one is the operator's, in one clause, for the failure text.
		because string
	}{{
		name: "the modern sso-session form, which is what this installation uses",
		source: "credentials@v1.20.1 ssocreds/sso_token_provider.go:104 wrapping :120 — " +
			"the SSOTokenProvider path, taken whenever ~/.aws/config uses an " +
			"[sso-session] block, which is what `aws sso login --sso-session <name>` writes",
		message: prefix + "refresh cached SSO token failed, cached SSO token is expired, " +
			"or not present, and cannot be refreshed",
		because: "the session lapsed and its refresh token cannot renew it, which is " +
			"`aws sso login` and nothing else",
	}, {
		name: "the legacy per-profile form",
		source: "credentials@v1.20.1 ssocreds/sso_credentials_provider.go:160 — " +
			"InvalidTokenError.Error(), `const msg = \"the SSO session has expired or is invalid\"`",
		message: prefix + "the SSO session has expired or is invalid",
		because: "same fix, reached through the older sso_start_url form",
	}, {
		name: "never logged in on this machine at all",
		source: "credentials@v1.20.1 ssocreds/sso_cached_token.go:149, wrapped by " +
			"sso_token_provider.go:104",
		message: prefix + "refresh cached SSO token failed, failed to read cached SSO " +
			"token file, open /root/.aws/sso/cache/9f2.json: no such file or directory",
		because: "there is no session to renew, which is still `aws sso login` and is " +
			"still the operator's",
	}, {
		name: "the token file is there and does not parse",
		source: "credentials@v1.20.1 ssocreds/sso_cached_token.go:154, wrapped by " +
			"sso_token_provider.go:104",
		message: prefix + "refresh cached SSO token failed, failed to parse cached SSO " +
			"token file, invalid character 'x' looking for beginning of value",
		because: "logging in again rewrites the file, and no other row on this page can " +
			"say so",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, detail, _ := classify(errors.New(c.message))

			if state != StateSSO {
				t.Fatalf("classified %q, want %q.\n"+
					"  source: %s\n"+
					"  why it is StateSSO: %s\n"+
					"  what the page draws for %q: `unclassified` and `Nobody yet — this "+
					"failure is not one of the known shapes`, with no `yours to fix` mark, "+
					"for a failure that is ten seconds of the operator's time",
					state, StateSSO, c.source, c.because, state)
			}
			// And the row's own sentence, since StateSSO is the one row on this page
			// that is drawn as actionable: it has to name the act.
			if !strings.Contains(strings.ToLower(detail), "sign in") {
				t.Errorf("detail = %q, want it to say what to do", detail)
			}
			if !(Cluster{State: state}).Actionable() {
				t.Error("the row is not Actionable(), so the `yours to fix` mark is not drawn " +
					"on the one state where it belongs")
			}
		})
	}
}

// The SSO needle must not swallow the arms below it.
//
// The lesson is not "strip the URL" but *an ordered switch over text is a
// priority list, and a needle that is too broad silently deletes everything
// after it*: classify on the reason, never on the address or a stray word. The
// SSO arm is **first**, so anything added to it can shadow all eight below.
//
// One case per arm rather than one opaque case, so a re-widened list names which
// diagnosis it swallowed.
func TestWideningTheSSOArmDidNotShadowTheArmsBelowIt(t *testing.T) {
	const url = `Get "https://example.eks.amazonaws.com/version?timeout=32s": `

	for _, c := range []struct {
		want    string
		message string
	}{
		{StateIAM, url + `operation error STS: AssumeRole, https response error StatusCode: 403, AccessDenied: User is not authorized to perform: sts:AssumeRole`},
		{StateDNS, url + `dial tcp: lookup api.example.internal on 127.0.0.11:53: no such host`},
		{StateRefused, url + `dial tcp 127.0.0.1:6443: connect: connection refused`},
		{StateTimeout, url + `net/http: TLS handshake timeout`},
		{StateKO, url + `x509: certificate signed by unknown authority`},
		{StateSSO, url + `You must be logged in to the server (Unauthorized)`},
		{StateKO, url + `pods is forbidden: User cannot list resource "pods"`},
		{StatePlugin, url + `getting credentials: exec: executable aws failed with exit code 254`},
		{StateMissing, url + `getting credentials: exec: executable gke-gcloud-auth-plugin not found`},
	} {
		state, detail, _ := classify(errors.New(c.message))
		if state != c.want {
			t.Errorf("classified %q, want %q — the SSO arm is first and now swallows this "+
				"diagnosis: a needle too broad deletes every arm after it.\n"+
				"  message: %s\n  detail:  %s", state, c.want, c.message, detail)
		}
	}
}

// A bare `Unauthorized` does not claim to know which of two things it is.
//
// With EKS tokens signed in this process (internal/ekstoken), a lapsed session
// on that path surfaces as an SDK error carrying SSO
// wording and is caught by the arm **above** this one — pinned by
// TestTheSSOArmMatchesWhatTheAWSSDKActuallySays — and an AssumeRole refusal by
// the arm after it. So an EKS context that reaches the bare-401 arm is one whose
// URL was signed with credentials that worked and whose *identity* the API
// server refused: an `aws-auth` entry or an EKS access entry, which is a ticket
// for somebody else and not ten seconds of the operator's own time.
//
// EKS answers an unmapped principal with 401 and not 403, so nothing in the
// string separates that from an expired OIDC token — which is the other half of
// this arm's population and where "sign in again" really is the answer. Two
// populations, one string: the row names both rather than sending somebody to
// renew a session that has not expired.
func TestABare401DoesNotPickOneOfItsTwoCauses(t *testing.T) {
	state, detail, _ := classify(errors.New(
		`Get "https://ABC.gr7.eu-west-3.eks.amazonaws.com/version?timeout=32s": Unauthorized`))

	if state != StateSSO {
		t.Fatalf("state = %q; the row must still lead the page, it is the one to look at first", state)
	}
	if strings.Contains(strings.ToLower(detail), "usually") {
		t.Errorf("the row still asserts which of the two causes this is: %q", detail)
	}
	if !strings.Contains(strings.ToLower(detail), "map") {
		t.Errorf("the row does not offer the identity half at all, so an EKS operator is sent "+
			"to renew a session that has not expired: %q", detail)
	}
	if !strings.Contains(strings.ToLower(detail), "laps") &&
		!strings.Contains(strings.ToLower(detail), "expir") {
		t.Errorf("the row does not offer the session half, which is the usual cause on every "+
			"context that is not AWS: %q", detail)
	}
}
