package ekstoken

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/safego"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Who the token is signed as.
//
// When credentials cannot be resolved, signing fails loudly. The quiet case
// is a token that was signed and replayed against STS, whose IAM identity the
// cluster's access entries or aws-auth ConfigMap did not map: the answer is
// `Unauthorized` with no word about who was asking. A profile name does not
// tell: the same profile can resolve to different roles on two machines. So
// after a refusal this asks STS, once per context per process, and reports
// the ARN the API server will have seen.
//
// The token and its presigned URL are never in here: that URL is the
// credential. An ARN is not; it is in every CloudTrail record and in the
// mapping it is compared against.

// identityTimeout bounds the STS call. Nothing waits on it; it only keeps a
// wedged network from leaving the goroutine behind.
const identityTimeout = 15 * time.Second

// Identity is who this provider's credentials belong to.
type Identity struct {
	// ARN is what the authenticator matched against, and the value to compare
	// with an access entry or an aws-auth mapping.
	ARN string
	// Account is the AWS account the identity is in, on its own because the
	// commonest form of this failure is the wrong account altogether.
	Account string
	// Source is where the SDK found the credentials (SSOProvider,
	// AssumeRoleProvider, EnvConfigCredentials…). It separates two failures
	// that read the same: EnvConfigCredentials means the process inherited an
	// AWS_ACCESS_KEY_ID and the kubeconfig's profile had no say (unset the
	// variable); SSOProvider means the profile was honored and the mapping is
	// missing (an administrator's fix).
	Source string
	// Base is where the credentials the role was assumed with came from, on a
	// context whose exec block carries `--role-arn`; empty otherwise. Source
	// there is AssumeRoleProvider's own name, and the diagnosis needs the one
	// underneath (`SharedConfigCredentials` is the shadowed profile of
	// shadow.go).
	Base string
}

// Sentence is the one composer for this fact, so the log line and the health
// row cannot describe one identity two ways.
func (i Identity) Sentence() string {
	if i.ARN == "" {
		return ""
	}
	out := "signed as " + i.ARN
	if i.Account != "" && !strings.Contains(i.ARN, ":"+i.Account+":") {
		// Only when the ARN does not carry it already.
		out += " in account " + i.Account
	}
	if i.Source != "" {
		out += ", credentials from " + i.Source
	}
	if i.Base != "" && i.Base != i.Source {
		out += " via " + i.Base
	}
	return out
}

// Identity resolves who this provider signs as, by asking STS.
//
// Cached on success and not on failure: the commonest failure is an expired
// SSO session, fixed in a shell a minute later, which a cached "could not
// tell" would outlive.
func (p *Provider) Identity(ctx context.Context) (Identity, error) {
	p.mu.Lock()
	held := p.identity
	p.mu.Unlock()
	if held.ARN != "" {
		return held, nil
	}

	// Outside the lock: Token takes the same mutex on every request, and an
	// STS round trip under it would stall every request to this cluster.
	source, under := credentialSource(ctx, p.credentials), credentialSource(ctx, p.base)
	out, err := p.identities.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Identity{}, fmt.Errorf("asking STS who signs for %q: %w", p.spec.Cluster, err)
	}

	found := Identity{Source: source, Base: under}
	if out.Arn != nil {
		found.ARN = *out.Arn
	}
	if out.Account != nil {
		found.Account = *out.Account
	}

	p.mu.Lock()
	p.identity = found
	p.mu.Unlock()
	return found, nil
}

// credentialSource asks a provider where it found what it holds.
//
// A failure is the empty string, never an error: the label is wanted after
// the credential was already used and refused, and failing to name it must
// not replace the diagnosis. Only the Source field is read; everything else
// Retrieve returns is the credential.
func credentialSource(ctx context.Context, provider aws.CredentialsProvider) string {
	if provider == nil {
		return ""
	}
	creds, err := provider.Retrieve(ctx)
	if err != nil {
		return ""
	}
	return creds.Source
}

// KnownIdentity is the answer if one has already been established, and the
// empty string otherwise. It never asks: its callers are a health row and a
// page render, where a network call would be invisible.
func (p *Provider) KnownIdentity() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.identity.Sentence()
}

// OnRefusal sets what to say the first time this cluster refuses a token.
//
// A callback, because the context name lives in internal/cluster, and so
// that what this credential-holding package says is composed where it can
// be read.
func (p *Provider) OnRefusal(report func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.report = report
}

// refused is what the RoundTripper calls when the API server answers 401.
//
// Once per provider, on a goroutine, with a context of its own:
//
//   - once, because a refused cluster refuses every request, and a line per
//     refusal would repeat until the disk fills;
//   - on a goroutine, because this is inside RoundTrip, and an STS round trip
//     would be added to every failing request;
//   - its own context, because the refused request's is usually canceled as
//     soon as its answer is read, which would cancel the diagnosis too.
func (p *Provider) refused() {
	p.mu.Lock()
	report, already := p.report, p.reported
	p.reported = true
	p.mu.Unlock()

	if report == nil || already {
		return
	}

	// Through safego: STS's answer is decoded here, and a panic in it must not
	// take the process down.
	safego.Go("eks token: asking STS who signed it", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
		defer cancel()

		found, err := p.Identity(ctx)
		if err != nil {
			report("the cluster refused this token, and STS would not say who signed it " +
				"either: " + err.Error())
			return nil
		}
		// A hyphen, not an em dash: a log line, and on code page 850 an em dash
		// arrives as garbage (internal/logs tests for it).
		report("the cluster refused this token - " + found.Sentence() + ". A bare 401 from " +
			"EKS is an identity the authenticator could not map, so compare that ARN with " +
			"the cluster's EKS access entries or its aws-auth ConfigMap. If kubectl works " +
			"in a terminal on this same context, run `aws sts get-caller-identity` there " +
			"and compare the two")
		return nil
	}, nil)
}
