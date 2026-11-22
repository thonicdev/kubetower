package ekstoken

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// The token is `k8s-aws-v1.` followed by the unpadded base64url encoding of a
// presigned STS GetCallerIdentity URL. The cluster's name travels as the
// signed header `x-k8s-aws-id`, which stops a token minted for one cluster
// from being replayed against another. It is the same URL `aws eks
// get-token` prints, signed with the same credentials.
//
// A SigV4 presign is an offline HMAC. Only resolving the credentials (an SSO
// exchange, an AssumeRole) touches the network, and aws.CredentialsCache
// holds those until they expire. So the token is re-minted on a short clock
// rather than held to an expiry this package would have to be right about.

const (
	// tokenPrefix is what the API server's authenticator looks for.
	tokenPrefix = "k8s-aws-v1."

	// clusterHeader is signed, not merely sent: it binds a token to one
	// cluster.
	clusterHeader = "x-k8s-aws-id"

	// mintInterval is how long one token is reused. Implementations disagree
	// on whether a presigned GetCallerIdentity is good for one minute or
	// fifteen; thirty seconds is valid under the least generous reading, and
	// re-signing is an HMAC.
	mintInterval = 30 * time.Second

	// expirySeconds is how long the presigned URL is good for: sixty, as
	// `aws eks get-token` uses. It bounds how long a stolen token can be
	// replayed, and mintInterval sits below it so a held token is never handed
	// out near its expiry. It must stay within 0-900, or the authenticator
	// refuses it (see maxTokenBytes for how).
	expirySeconds = 60

	// maxTokenBytes is the authenticator's ceiling on the whole bearer string,
	// prefix included (`maxTokenLenBytes` in aws-iam-authenticator's
	// pkg/token/token.go).
	//
	// Checked here because over it the cluster answers a bare 401: the
	// authenticator is a webhook, and a token too large, a query parameter
	// outside its allow-list, a duplicated key and an unmapped identity all
	// become the same `Unauthorized`.
	//
	// What grows is the STS session token carried in the query: long-term keys
	// give about 800 bytes, while a session token of about 2 130 characters
	// reaches 4 095, within reach of a role with many session tags.
	maxTokenBytes = 4 * 1024
)

// Provider mints tokens for one context.
//
// One per context, because profile, region and role are per context, and two
// contexts on one profile then share nothing but the files they read.
type Provider struct {
	spec Spec

	// credentials and identities are the two halves of "as whom", held
	// because neither can be got back out of the presign client (identity.go).
	// identities is an interface so tests can drive the refusal path without
	// an AWS account.
	credentials aws.CredentialsProvider
	// base is what credentials was before an assume-role wrapped it (the same
	// value when nothing is assumed): AssumeRoleProvider reports itself as the
	// source and hides what it authenticated with.
	base       aws.CredentialsProvider
	identities callerIdentityAPI

	mu      sync.Mutex
	presign *sts.PresignClient
	token   string
	minted  time.Time
	// identity, report and reported are the refusal diagnosis, under the token's
	// mutex because a goroutine the RoundTripper starts writes them.
	identity Identity
	report   func(string)
	reported bool
}

// callerIdentityAPI is the one STS call this package makes for real, as
// opposed to presigning, declared here so a test can answer it.
type callerIdentityAPI interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (
		*sts.GetCallerIdentityOutput, error)
}

// New builds a provider without reaching anything.
//
// LoadDefaultConfig builds a credential provider without retrieving
// credentials, so an expired SSO session is not an error here. The first
// request is where an authentication problem surfaces and internal/health
// classifies it; failing at construction would drop the cluster from the
// rail instead.
func New(ctx context.Context, spec Spec) (*Provider, error) {
	options := []func(*config.LoadOptions) error{config.WithRegion(spec.Region)}
	if spec.Profile != "" {
		options = append(options, config.WithSharedConfigProfile(spec.Profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("reading the AWS configuration for %q: %w", spec.Cluster, err)
	}

	// After the load, so a malformed `~/.aws/config` is reported as such. A
	// profile carrying both an SSO session and static credentials would sign
	// as another IAM identity here than in the CLI (shadow.go).
	if shadow, ok := shadowedProfile(ctx, spec.Profile); ok {
		return nil, fmt.Errorf("%w: %s", ErrProfileShadowed, shadow.Sentence())
	}

	// Held before the swap below, the only moment it exists:
	// AssumeRoleProvider.Retrieve stamps its own name over Source, which would
	// hide whether the profile was honored on exactly the contexts most
	// likely to be affected.
	base := cfg.Credentials

	if spec.RoleARN != "" {
		// Assumed with the profile's own credentials, as `--role-arn` does,
		// and cached in memory so AssumeRole runs once an hour, not once a token.
		cfg.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), spec.RoleARN))
	}

	client := sts.NewFromConfig(cfg)
	return &Provider{
		spec: spec,
		// After the block above: with a role assumed, the replaced field is the
		// one that signs, and the source profile's identity would be the wrong
		// answer.
		credentials: cfg.Credentials,
		base:        base,
		identities:  client,
		presign:     sts.NewPresignClient(client),
	}, nil
}

// Token returns a token, minting one when the held one is old enough.
func (p *Provider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && time.Since(p.minted) < mintInterval {
		return p.token, nil
	}

	signed, err := p.presign.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{},
		func(o *sts.PresignOptions) {
			o.ClientOptions = append(o.ClientOptions, func(so *sts.Options) {
				// Added before signing, so the signature covers it: a header merely
				// sent would let the token be replayed against any cluster.
				//
				// Nothing else may be added. The authenticator checks the presigned
				// URL against a closed allow-list of query parameters (action,
				// version and seven `x-amz-*`) and rejects anything else as it does
				// an unmapped identity. A presign hoists `X-Amz-*` headers into the
				// query, so any SDK middleware setting a new one on STS breaks every
				// token: keep `auth_scheme_preference` / AWS_AUTH_SCHEME_PREFERENCE
				// away from this path (SigV4a adds `X-Amz-Region-Set`).
				so.APIOptions = append(so.APIOptions,
					smithyhttp.SetHeaderValue(clusterHeader, p.spec.Cluster),
					withExpiry(expirySeconds))
			})
		})
	if err != nil {
		return "", fmt.Errorf("signing a token for %q: %w", p.spec.Cluster, err)
	}

	token, err := composeToken(signed.URL, p.spec.Cluster)
	if err != nil {
		// Not held: a token the authenticator will refuse is not one to keep,
		// and an error here is a sentence internal/health can classify.
		return "", err
	}
	p.token = token
	p.minted = time.Now()
	return p.token, nil
}

// RoundTripper puts the token on every request.
//
// A hook rather than rest.Config.BearerToken, because the token has a
// lifetime: a string set once would turn every cluster 401 after it expired.
type RoundTripper struct {
	Provider *Provider
	Base     http.RoundTripper
}

func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Cloned before it is touched: a RoundTripper must not modify its request
	// (client-go retries some), and a second Authorization header is refused
	// with a message about neither.
	out := req.Clone(req.Context())

	token, err := rt.Provider.Token(req.Context())
	if err != nil {
		return nil, err
	}
	out.Header.Set("Authorization", "Bearer "+token)

	base := rt.Base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(out)

	// The one place that sees an EKS refusal as it happens. By the time
	// client-go turns the 401 into `Unauthorized`, "as whom" can no longer be
	// answered, so it is asked here (Provider.refused: once, on a goroutine,
	// never carrying the token).
	//
	// 401 and not 403: a 403 is a mapped identity that RBAC refused, routine
	// for one resource on a working cluster.
	if err == nil && resp != nil && resp.StatusCode == http.StatusUnauthorized {
		rt.Provider.refused()
	}
	return resp, err
}

// composeToken is the presigned URL wrapped as a bearer token, or a refusal.
//
// Its own function so a test can assert on the ceiling without a presign
// client and credentials.
func composeToken(url, cluster string) (string, error) {
	token := tokenPrefix + encodeForToken(url)
	if len(token) > maxTokenBytes {
		return "", fmt.Errorf("the token for %q is %d bytes and aws-iam-authenticator refuses "+
			"anything over %d, so this cluster would answer a bare `Unauthorized` with no "+
			"reason in it: the presigned URL carries this identity's STS session token, which "+
			"grows with session tags and with the role's path",
			cluster, len(token), maxTokenBytes)
	}
	return token, nil
}

// encodeForToken is base64url without padding, which is what the
// authenticator accepts; a trailing `=` is refused with no message saying so.
func encodeForToken(url string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(url))
}

// DecodeForDiagnosis turns a token back into the URL it wraps.
//
// For a diagnostic tool and for tests, and named so nobody reaches for it by
// accident: the URL it returns is the credential (anyone holding it can call
// GetCallerIdentity as this process). Never log it.
func DecodeForDiagnosis(token string) (string, error) {
	raw, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok {
		return "", fmt.Errorf("not an EKS token: it does not begin with %q", tokenPrefix)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// withExpiry puts `X-Amz-Expires` into the query before the request is signed.
//
// Without it the API server refuses an otherwise well-formed token with a
// bare "Unauthorized"; the CLI's own URL carries it.
//
// The SDK has no presign expiry option for STS, so the parameter is put in
// the query by hand, and before signing or the signature does not cover it:
// a Build-step middleware runs before the signer's Finalize step.
func withExpiry(seconds int) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		return stack.Build.Add(middleware.BuildMiddlewareFunc("kubetowerPresignExpiry",
			func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (
				middleware.BuildOutput, middleware.Metadata, error) {
				req, ok := in.Request.(*smithyhttp.Request)
				if !ok {
					// Not an HTTP request, which cannot happen for STS over HTTPS: passed
					// through unchanged.
					return next.HandleBuild(ctx, in)
				}
				setExpiry(req.URL, seconds)
				return next.HandleBuild(ctx, in)
			}), middleware.After)
	}
}

// setExpiry is the one line the middleware exists to run, lifted out so a
// test can assert on the query without building a smithy stack.
func setExpiry(u *url.URL, seconds int) {
	query := u.Query()
	query.Set("X-Amz-Expires", strconv.Itoa(seconds))
	u.RawQuery = query.Encode()
}
