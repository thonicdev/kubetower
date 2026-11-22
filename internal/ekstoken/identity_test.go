package ekstoken

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// The refusal diagnosis, tested where it can be tested: this whole mechanism
// exists for a failure that only happens against a real EKS cluster, so what is
// asserted here is the *plumbing* — that a 401 provokes exactly one question,
// that anything else provokes none, and that the answer never carries the
// credential. Whether the ARN it names is the right one is a property of STS.

// stubIdentities answers GetCallerIdentity without an AWS account, and counts.
type stubIdentities struct {
	calls atomic.Int32
	arn   string
	acct  string
	err   error
}

func (s *stubIdentities) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput,
	...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return &sts.GetCallerIdentityOutput{Arn: &s.arn, Account: &s.acct}, nil
}

// roundTripperFunc is the transport under the one being tested.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func answering(status int) roundTripperFunc {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{},
		}, nil
	}
}

// provider builds one with no AWS anywhere: a presign client is never reached
// because Token is stubbed out by pre-seeding the held token.
func provider(t *testing.T, ids *stubIdentities, source string) *Provider {
	t.Helper()
	p := &Provider{
		spec:       Spec{Cluster: "prod-eks-1", Region: "eu-west-3", Profile: "prod-devops"},
		identities: ids,
		credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{Source: source}, nil
		}),
		// Pre-minted so RoundTrip does not reach a presign client. The token is
		// deliberately a real-looking one: the test below greps the report for
		// it.
		token:  tokenPrefix + "aHR0cHM6Ly9zdHMuZXUtd2VzdC0zLmFtYXpvbmF3cy5jb20vP1gtQW16LVNpZ25hdHVyZT1kZWFk",
		minted: time.Now(),
	}
	return p
}

// waitForReport blocks until the diagnosis goroutine has said something, or
// gives up. Its own helper because refused() is asynchronous **on purpose** —
// see identity.go — so every test here has to wait rather than assert straight
// after the call.
func waitForReport(t *testing.T, said chan string) string {
	t.Helper()
	select {
	case note := <-said:
		return note
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was reported: a 401 left the log silent")
		return ""
	}
}

// TestA401NamesTheIdentityBehindIt is the whole point of the mechanism.
//
// The failure it closes: the API server refused the token, client-go turned that
// into the word `Unauthorized`, and by the time internal/health saw it there was
// nothing left saying *whose* token it was. The RoundTripper is the last place
// that knows.
func TestA401NamesTheIdentityBehindIt(t *testing.T) {
	ids := &stubIdentities{arn: "arn:aws:sts::123456789012:assumed-role/devops/alice", acct: "123456789012"}
	p := provider(t, ids, "SSOProvider")

	said := make(chan string, 4)
	p.OnRefusal(func(note string) { said <- note })

	rt := &RoundTripper{Provider: p, Base: answering(http.StatusUnauthorized)}
	req, _ := http.NewRequest(http.MethodGet, "https://example.eks.amazonaws.com/version", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	note := waitForReport(t, said)
	for _, want := range []string{ids.arn, "SSOProvider", "aws-auth"} {
		if !strings.Contains(note, want) {
			t.Errorf("the report does not mention %q:\n%s", want, note)
		}
	}
}

// TestTheDiagnosisNeverCarriesTheCredential.
//
// The token wraps a presigned URL and that URL **is** the credential — anyone
// holding it can call GetCallerIdentity as this process. This diagnosis is
// written into a log file and pasted into chats, which is exactly the journey a
// credential must not make.
func TestTheDiagnosisNeverCarriesTheCredential(t *testing.T) {
	ids := &stubIdentities{arn: "arn:aws:iam::123456789012:user/alice", acct: "123456789012"}
	p := provider(t, ids, "SharedConfigCredentials")

	said := make(chan string, 4)
	p.OnRefusal(func(note string) { said <- note })

	rt := &RoundTripper{Provider: p, Base: answering(http.StatusUnauthorized)}
	req, _ := http.NewRequest(http.MethodGet, "https://example.eks.amazonaws.com/version", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	note := waitForReport(t, said)
	for _, forbidden := range []string{tokenPrefix, p.token, "X-Amz-Signature", "X-Amz-Security-Token"} {
		if strings.Contains(note, forbidden) {
			t.Errorf("the report carries %q, which is a credential:\n%s", forbidden, note)
		}
	}
}

// TestTheDiagnosisIsAskedOnceAndOnly401AsksIt.
//
// Two halves that fail in opposite directions and are one test because one
// RoundTripper decides both.
//
// A refused cluster refuses *everything*, and the pod table asks every two
// seconds: without the once, this writes one paragraph per refusal until the
// disk fills. And a 403 is an identity the authenticator **did** map, which RBAC
// then declined for one resource — reporting that would put a paragraph about
// aws-auth in the log the first time any page touched something the role cannot
// list, on a cluster that is working.
func TestTheDiagnosisIsAskedOnceAndOnly401AsksIt(t *testing.T) {
	t.Run("many 401s, one question", func(t *testing.T) {
		ids := &stubIdentities{arn: "arn:aws:iam::1:user/x", acct: "1"}
		p := provider(t, ids, "SSOProvider")
		said := make(chan string, 8)
		p.OnRefusal(func(note string) { said <- note })

		rt := &RoundTripper{Provider: p, Base: answering(http.StatusUnauthorized)}
		for range 20 {
			req, _ := http.NewRequest(http.MethodGet, "https://example/version", nil)
			if _, err := rt.RoundTrip(req); err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
		}
		waitForReport(t, said)

		// Give a second report every chance to arrive before concluding there
		// is not one.
		time.Sleep(200 * time.Millisecond)
		if n := len(said); n != 0 {
			t.Errorf("%d further reports; a refused cluster refuses every request, so this is "+
				"one sentence repeated until the disk is full", n)
		}
		if n := ids.calls.Load(); n != 1 {
			t.Errorf("STS was asked %d times, want 1", n)
		}
	})

	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status)+" asks nothing", func(t *testing.T) {
			ids := &stubIdentities{arn: "arn:aws:iam::1:user/x", acct: "1"}
			p := provider(t, ids, "SSOProvider")
			said := make(chan string, 4)
			p.OnRefusal(func(note string) { said <- note })

			rt := &RoundTripper{Provider: p, Base: answering(status)}
			req, _ := http.NewRequest(http.MethodGet, "https://example/version", nil)
			if _, err := rt.RoundTrip(req); err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			time.Sleep(200 * time.Millisecond)
			if n := len(said); n != 0 {
				t.Errorf("a %d provoked a refusal diagnosis: %s", status, <-said)
			}
			if n := ids.calls.Load(); n != 0 {
				t.Errorf("a %d provoked %d STS calls, want 0", status, n)
			}
		})
	}
}

// TestKnownIdentityNeverAsks. Its callers are a health row and a page render, so
// a version of it that resolved on demand would put an STS round trip inside a
// template — invisible in the timing and with nowhere for the failure to go.
func TestKnownIdentityNeverAsks(t *testing.T) {
	ids := &stubIdentities{arn: "arn:aws:iam::1:user/x", acct: "1"}
	p := provider(t, ids, "SSOProvider")

	if got := p.KnownIdentity(); got != "" {
		t.Errorf("KnownIdentity() = %q before anything established one, want empty", got)
	}
	if n := ids.calls.Load(); n != 0 {
		t.Fatalf("KnownIdentity asked STS %d times; it must never ask", n)
	}

	if _, err := p.Identity(context.Background()); err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if got := p.KnownIdentity(); !strings.Contains(got, ids.arn) {
		t.Errorf("KnownIdentity() = %q after one was established, want it to carry %q", got, ids.arn)
	}
	// And a second read costs nothing: the answer does not change for the life
	// of the process, and the health page reads it on every render.
	_ = p.KnownIdentity()
	if n := ids.calls.Load(); n != 1 {
		t.Errorf("STS was asked %d times, want 1", n)
	}
}

// TestAFailedLookupIsNotCached. The commonest cause is an expired SSO session,
// which the operator fixes in a shell a minute later — a cached "could not tell"
// would outlive its own cause for the life of the process.
func TestAFailedLookupIsNotCached(t *testing.T) {
	ids := &stubIdentities{err: context.DeadlineExceeded}
	p := provider(t, ids, "SSOProvider")

	if _, err := p.Identity(context.Background()); err == nil {
		t.Fatal("Identity succeeded against an STS that refused")
	}
	ids.err, ids.arn, ids.acct = nil, "arn:aws:iam::1:user/x", "1"
	found, err := p.Identity(context.Background())
	if err != nil {
		t.Fatalf("the second attempt failed too: %v", err)
	}
	if found.ARN != ids.arn {
		t.Errorf("ARN = %q, want %q", found.ARN, ids.arn)
	}
}

// TestTheSentenceIsOneComposer. The log line and the health row both read it, so
// a second spelling anywhere is one cluster described two ways.
func TestTheSentenceIsOneComposer(t *testing.T) {
	cases := []struct {
		name string
		in   Identity
		want string
	}{
		{"nothing established", Identity{}, ""},
		{"the account is already in the ARN and is not repeated",
			Identity{ARN: "arn:aws:sts::123456789012:assumed-role/devops/t", Account: "123456789012",
				Source: "SSOProvider"},
			"signed as arn:aws:sts::123456789012:assumed-role/devops/t, credentials from SSOProvider"},
		{"an ARN that does not carry the account still names it",
			Identity{ARN: "arn:aws:iam:::root", Account: "123456789012"},
			"signed as arn:aws:iam:::root in account 123456789012"},
		{"no source known", Identity{ARN: "arn:aws:iam::1:user/x"}, "signed as arn:aws:iam::1:user/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Sentence(); got != tc.want {
				t.Errorf("Sentence() = %q, want %q", got, tc.want)
			}
		})
	}
}
