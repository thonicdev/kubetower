package ekstoken

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
)

// ErrProfileShadowed is a profile with static credentials that hide an SSO
// session.
//
// `~/.aws/config` and `~/.aws/credentials` merge into one profile, the
// credentials file winning key by key, so an SSO profile with an
// `aws_access_key_id` in the credentials file holds both. botocore ranks SSO
// before the shared credentials; aws-sdk-go-v2 ranks static keys second and
// SSO fifth, and does not treat the combination as a collision. With a live
// SSO session both are silent and simply sign as two different identities,
// so the divergence can only be detected from the files.
//
// Presigning is offline, so stale keys still sign a well-formed URL and the
// cluster answers a bare `Unauthorized`, while `kubectl` in a terminal (which
// runs the CLI) works.
//
// This does not reorder the resolution to match botocore: that would mean
// reimplementing the SDK's SSO provider, and getting it wrong is the same
// failure from the other side. The context is handed back to
// `aws eks get-token`, and the log line names the profile and the file. The
// cost is one subprocess; the alternative risks authenticating as somebody
// else.
var ErrProfileShadowed = errors.New("static credentials shadow an SSO profile")

// ProfileShadow is one profile the CLI and this process would resolve
// differently.
type ProfileShadow struct {
	// Profile is the effective profile name, not always the kubeconfig's: a
	// block naming none resolves through AWS_PROFILE and then `default`.
	Profile string
	// SSO is how the profile asks for SSO (a session name or a start URL), so
	// the sentence names the half being ignored.
	SSO string
	// Keys is where the static credentials came from, verbatim from the SDK
	// (`SharedConfigCredentials: <path>`). The path is the fix, and it is not
	// always `~/.aws/credentials`.
	Keys string
}

// Sentence is the one composer for this fact, so nothing describes one
// profile two ways.
func (s ProfileShadow) Sentence() string {
	return fmt.Sprintf("profile %q is configured for AWS SSO (%s) and also carries static "+
		"credentials (%s). `aws eks get-token` would use the SSO session; this process's SDK "+
		"ranks the static credentials higher, so a token signed here would authenticate as "+
		"another IAM identity and the cluster would refuse it with a bare 401. This context is "+
		"left to `aws eks get-token`. Delete the profile's static credentials if they are stale",
		s.Profile, s.SSO, s.Keys)
}

// shadowedProfile reports whether this spec's profile is one of those.
//
// It reaches no network and never turns a failure of its own into a
// refusal: if re-reading the shared config fails, the answer is "no shadow",
// because LoadDefaultConfig already succeeded on the same files and a
// diagnostic must not veto a working credential path.
func shadowedProfile(ctx context.Context, profile string) (ProfileShadow, bool) {
	if profile == "" && envHasStaticKeys() {
		// With no profile named, both this SDK and botocore put the process's
		// own AWS_ACCESS_KEY_ID ahead of every profile, so there is no divergence,
		// and the CLI would resolve the same variables.
		return ProfileShadow{}, false
	}

	shared, err := config.LoadSharedConfigProfile(ctx, effectiveProfile(profile), sharedConfigFiles)
	if err != nil {
		return ProfileShadow{}, false
	}
	// No arm for `shared.Source != nil` on purpose: `source_profile` is
	// resolved before both static keys and SSO, and setting it makes the SDK
	// clear the profile's SSO fields, so ssoAsked already answers nothing.
	// TestAProfileThatResolvesTheSameWayHereAndInTheCLIIsSignedHere pins it,
	// so an SDK that stops clearing them fails a test rather than refusing a
	// working context.
	if !shared.Credentials.HasKeys() || ssoAsked(shared) == "" {
		return ProfileShadow{}, false
	}
	return ProfileShadow{
		Profile: effectiveProfile(profile),
		SSO:     ssoAsked(shared),
		Keys:    keysFrom(shared),
	}, true
}

// effectiveProfile is the name the SDK will actually read, mirroring
// EnvConfig.SharedConfigProfile and its `default` fallback. A block naming
// no profile, the commonest shape, would otherwise hide a shadow on
// `default`.
func effectiveProfile(profile string) string {
	for _, candidate := range []string{
		profile,
		os.Getenv("AWS_PROFILE"),
		os.Getenv("AWS_DEFAULT_PROFILE"),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return "default"
}

// sharedConfigFiles points the second read at the files the first one used.
//
// LoadSharedConfigProfile does not honor AWS_CONFIG_FILE or
// AWS_SHARED_CREDENTIALS_FILE (its defaults are fixed at package init),
// while LoadDefaultConfig does; without this, a container that sets them
// would read a different pair of files from the credentials' own.
func sharedConfigFiles(o *config.LoadSharedConfigOptions) {
	if v := os.Getenv("AWS_CONFIG_FILE"); v != "" {
		o.ConfigFiles = []string{v}
	}
	if v := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); v != "" {
		o.CredentialsFiles = []string{v}
	}
}

// ssoAsked says how the profile asks for SSO, and the empty string when it does
// not.
//
// Any one of the five keys counts, as in the SDK's hasSSOConfiguration: a
// partially written SSO profile is still not meant for static keys.
func ssoAsked(shared config.SharedConfig) string {
	switch {
	case shared.SSOSessionName != "":
		return "sso_session " + shared.SSOSessionName
	case shared.SSOStartURL != "":
		return "sso_start_url " + shared.SSOStartURL
	case shared.SSORegion != "" || shared.SSOAccountID != "" || shared.SSORoleName != "":
		return "sso_* keys"
	}
	return ""
}

// keysFrom names where the static credentials were read, as the SDK stamps it.
func keysFrom(shared config.SharedConfig) string {
	if shared.Credentials.Source != "" {
		return shared.Credentials.Source
	}
	return "the shared AWS configuration"
}

// envHasStaticKeys mirrors EnvConfig.Credentials.HasKeys().
//
// The secret is tested for emptiness and never read further, so it never
// lands in a struct this package holds or anything it might print.
func envHasStaticKeys() bool {
	return os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != ""
}
