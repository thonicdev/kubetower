package ekstoken

import (
	"net/url"
	"strings"
	"testing"

	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// TestTheSignedURLCarriesAnExpiry guards the expiry parameter of the presigned
// URL, and it is worth its length because a token without it carries no symptom
// but a bare "Unauthorized".
//
// A presigned URL can be perfectly formed — the regional STS endpoint,
// `X-Amz-SignedHeaders=host;x-k8s-aws-id` so the cluster binding is covered by
// the signature, a session token proving the role was assumed — and the API
// server still answers a bare **"Unauthorized"** without `X-Amz-Expires`, which
// the AWS CLI's own URL carries and aws-sdk-go-v2 does not add for STS: presign
// expiry is plumbed per service there, and STS has no `PresignOptions.Expires`
// the way S3 does.
//
// Nothing offline can catch it, which is why the check is here: a token that is
// well-formed and refused looks exactly like a permissions problem, and that is
// where somebody would go looking.
func TestTheSignedURLCarriesAnExpiry(t *testing.T) {
	signed, err := url.Parse("https://sts.eu-west-3.amazonaws.com/?Action=GetCallerIdentity")
	if err != nil {
		t.Fatal(err)
	}
	setExpiry(signed, expirySeconds)

	got := signed.Query().Get("X-Amz-Expires")
	if got == "" {
		t.Fatal("X-Amz-Expires is not in the query. The signed URL is well-formed and the API " +
			"server refuses it with a bare Unauthorized, which reads as a permissions problem " +
			"and sends the investigation somewhere else entirely")
	}
	if got != "60" {
		t.Errorf("X-Amz-Expires = %s, want 60 — what `aws eks get-token` uses", got)
	}
	// Everything the URL already carried is still there: Encode() rebuilds the
	// whole query, so dropping a parameter here would be silent.
	if signed.Query().Get("Action") != "GetCallerIdentity" {
		t.Error("setExpiry dropped the Action parameter while rewriting the query")
	}

	// The other half, and the one a later refactor would break: it has to be in
	// the query *before* the signature is computed, or it is not part of the
	// canonical request and the signature does not cover it. A Build-step
	// middleware runs before the signer's Finalize step, which is what makes the
	// placement correct rather than convenient.
	stack := middleware.NewStack("test", smithyhttp.NewStackRequest)
	if err := withExpiry(expirySeconds)(stack); err != nil {
		t.Fatalf("the middleware would not attach: %v", err)
	}
	if _, ok := stack.Build.Get("kubetowerPresignExpiry"); !ok {
		t.Error("the expiry is no longer added in the Build step. Anywhere after signing and it " +
			"is a parameter the signature does not cover")
	}
	if _, ok := stack.Finalize.Get("kubetowerPresignExpiry"); ok {
		t.Error("the expiry is added in the Finalize step, which is where signing happens")
	}
}

// TestTheTokenIsBase64URLWithoutPadding. The authenticator rejects a padded
// token, and the difference is one trailing `=` that no error message mentions —
// the same class of silent refusal as the missing expiry.
func TestTheTokenIsBase64URLWithoutPadding(t *testing.T) {
	// Built by hand rather than minted: this asserts the encoding, and minting
	// needs credentials this test must not require.
	const url = "https://sts.eu-west-3.amazonaws.com/?Action=GetCallerIdentity&X-Amz-Expires=60"
	token := tokenPrefix + encodeForToken(url)

	if got, err := DecodeForDiagnosis(token); err != nil || got != url {
		t.Fatalf("round trip failed: %q, %v", got, err)
	}
	for _, bad := range []string{"=", "+", "/"} {
		if strings.Contains(strings.TrimPrefix(token, tokenPrefix), bad) {
			t.Errorf("the encoded token contains %q; it must be base64url without padding", bad)
		}
	}
}

// The authenticator's ceiling, checked here rather than discovered as a 401.
//
// `maxTokenLenBytes = 1024 * 4` in aws-iam-authenticator's pkg/token/token.go,
// applied to the whole bearer string. What makes this worth a test rather than a
// comment is that going over it is *invisible*: the webhook answers 403 with
// `Authenticated:false` for a token that is too long, for a query parameter
// outside its allow-list and for an identity it cannot map alike, and the API
// server renders all three as the same bare `Unauthorized`.
func TestATokenTooLargeForTheAuthenticatorIsRefusedRatherThanSent(t *testing.T) {
	const cluster = "example-prod-eks"

	ordinary, err := composeToken(
		"https://sts.eu-west-3.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15", cluster)
	if err != nil {
		t.Fatalf("an ordinary presigned URL must compose: %v", err)
	}
	if !strings.HasPrefix(ordinary, "k8s-aws-v1.") {
		t.Errorf("the token lost its prefix: %q", ordinary)
	}

	// A session token is what grows, so that is what this lengthens. Static keys
	// cannot reach the ceiling; an identity carrying many session tags can.
	oversize, err := composeToken(
		"https://sts.eu-west-3.amazonaws.com/?Action=GetCallerIdentity&X-Amz-Security-Token="+
			strings.Repeat("A", 4000), cluster)
	if err == nil {
		t.Fatalf("a %d-byte token must be refused here, not handed to a cluster that will "+
			"answer 401 with no reason in it", len(oversize))
	}
	if oversize != "" {
		t.Errorf("a refused token must not also be returned: %q", oversize)
	}
	// The sentence has to carry the two facts an operator cannot recover: which
	// cluster, and that a size is the cause. Without the second this reads as one
	// more unexplained refusal.
	for _, want := range []string{cluster, "4096", "bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}
