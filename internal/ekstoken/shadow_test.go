package ekstoken

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
)

// The shadowed-profile refusal.
//
// This is testable in full, which is unusual for this package: the defect is a
// property of two files and an ordering, not of a cluster. Every case below is
// a shape of `~/.aws/config` plus `~/.aws/credentials`, and the assertion is
// whether `New` hands the context back to `aws eks get-token` or signs it here.
//
// The fixtures use a deliberately impossible key. `AKIAEXAMPLEFAKEKEY123` is
// not a well-formed access key id and the secret says what it is, so nothing
// here can be mistaken for a credential by a person or by a scanner.

const (
	fakeKeyID  = "AKIAEXAMPLEFAKEKEY123"
	fakeSecret = "this-is-not-a-secret-it-is-a-test-fixture"
)

// awsFiles writes the two shared files and points this process at them, the way
// compose.dev.yaml does.
//
// `t.Setenv` rather than a option on the call, because the variables are what
// production reads: a test that passed the paths in as arguments would not
// establish that `sharedConfigFiles` honors them, which is the one thing about
// this that was easy to get wrong.
func awsFiles(t *testing.T, config, credentials string) {
	t.Helper()
	dir := t.TempDir()

	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("writing the config fixture: %v", err)
	}
	credentialsPath := filepath.Join(dir, "credentials")
	if err := os.WriteFile(credentialsPath, []byte(credentials), 0o600); err != nil {
		t.Fatalf("writing the credentials fixture: %v", err)
	}

	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentialsPath)
	// The machine this runs on may have any of these. Cleared so a fixture
	// means what it says, and so case-by-case setting below is the only source
	// of them.
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
}

const ssoSessionProfile = `[profile prod-devops]
sso_session = corp
sso_account_id = 111122223333
sso_role_name = DevOps
region = eu-west-3

[sso-session corp]
sso_start_url = https://example.awsapps.com/start
sso_region = eu-west-1
`

const legacySSOProfile = `[profile prod-devops]
sso_start_url = https://example.awsapps.com/start
sso_region = eu-west-1
sso_account_id = 111122223333
sso_role_name = DevOps
`

func staticKeys(section string) string {
	return "[" + section + "]\n" +
		"aws_access_key_id = " + fakeKeyID + "\n" +
		"aws_secret_access_key = " + fakeSecret + "\n"
}

func TestAProfileWhoseStaticKeysHideAnSSOSessionIsLeftToTheCLI(t *testing.T) {
	for _, shape := range []struct {
		name        string
		config      string
		credentials string
		profile     string
	}{
		{
			// The common shape: an SSO profile in ~/.aws/config and a stale
			// section of the same name in ~/.aws/credentials.
			name:        "sso_session in config, keys in credentials",
			config:      ssoSessionProfile,
			credentials: staticKeys("prod-devops"),
			profile:     "prod-devops",
		},
		{
			name:        "legacy sso_start_url in config, keys in credentials",
			config:      legacySSOProfile,
			credentials: staticKeys("prod-devops"),
			profile:     "prod-devops",
		},
		{
			// Both halves in one file and one section. The merge is not what
			// makes this a defect — the ordering is — so it has to be caught
			// here too.
			name:        "both in the config file's own section",
			config:      legacySSOProfile + "aws_access_key_id = " + fakeKeyID + "\naws_secret_access_key = " + fakeSecret + "\n",
			credentials: "",
			profile:     "prod-devops",
		},
		{
			// The commonest shape of all, and the one an exec block that names
			// no profile lands on. `default` has to be resolved here or the
			// check looks at the empty string and finds nothing.
			name:        "no profile named, and `default` is the shadowed one",
			config:      strings.Replace(legacySSOProfile, "[profile prod-devops]", "[default]", 1),
			credentials: staticKeys("default"),
			profile:     "",
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			awsFiles(t, shape.config, shape.credentials)

			_, err := New(context.Background(), Spec{
				Cluster: "example-prod-eks", Region: "eu-west-3", Profile: shape.profile,
			})
			if !errors.Is(err, ErrProfileShadowed) {
				t.Fatalf("this profile resolves to the static keys here and to SSO in the CLI, "+
					"so it must be handed back to `aws eks get-token`; got err=%v", err)
			}
			// The sentence is what an operator acts on, so it has to name the
			// profile and the file the keys came out of. Without the second the
			// advice is "look at your AWS configuration", which is where they
			// already were.
			if !strings.Contains(err.Error(), "credentials") {
				t.Errorf("the refusal must name where the static credentials were read; got %q", err)
			}
			if strings.Contains(err.Error(), fakeSecret) {
				t.Errorf("the refusal carries the secret it found: %q", err)
			}
		})
	}
}

func TestAProfileThatResolvesTheSameWayHereAndInTheCLIIsSignedHere(t *testing.T) {
	for _, shape := range []struct {
		name        string
		config      string
		credentials string
		profile     string
		env         map[string]string
	}{
		{
			// Nothing to shadow. This is the case the refusal must not touch,
			// because it is every SSO cluster that works.
			name:        "SSO and nothing else",
			config:      ssoSessionProfile,
			credentials: "",
			profile:     "prod-devops",
		},
		{
			name:        "static keys and nothing else",
			config:      "[profile prod-devops]\nregion = eu-west-3\n",
			credentials: staticKeys("prod-devops"),
			profile:     "prod-devops",
		},
		{
			// `source_profile` is resolved before both, in this SDK and in
			// botocore, and setting it clears the profile's own SSO fields. The
			// two agree, so there is nothing to report — and reporting it would
			// take a working context away from the operator.
			name: "source_profile beside SSO fields",
			config: "[profile prod-devops]\nrole_arn = arn:aws:iam::111122223333:role/DevOps\n" +
				"source_profile = base\nsso_account_id = 111122223333\n\n" +
				"[profile base]\nregion = eu-west-3\n",
			credentials: staticKeys("base"),
			profile:     "prod-devops",
		},
		{
			// With no profile named, this SDK and botocore both put the
			// process's own keys ahead of every profile. Handing the context to
			// the CLI would resolve the same variables, one subprocess later.
			name:        "no profile named, and the environment carries the keys",
			config:      strings.Replace(legacySSOProfile, "[profile prod-devops]", "[default]", 1),
			credentials: staticKeys("default"),
			profile:     "",
			env: map[string]string{
				"AWS_ACCESS_KEY_ID":     fakeKeyID,
				"AWS_SECRET_ACCESS_KEY": fakeSecret,
			},
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			awsFiles(t, shape.config, shape.credentials)
			for name, value := range shape.env {
				t.Setenv(name, value)
			}

			if _, err := New(context.Background(), Spec{
				Cluster: "example-prod-eks", Region: "eu-west-3", Profile: shape.profile,
			}); err != nil {
				t.Fatalf("this profile resolves the same way here as in the CLI and must be "+
					"signed in this process; got %v", err)
			}
		})
	}
}

// A profile that is not in the files at all is a different failure, and it must
// not arrive wearing this one's name.
//
// `LoadDefaultConfig` refuses it outright, which is the loud case
// `internal/health` already classifies; the detection here runs afterwards and
// returns nothing. Asserted because the cheapest wrong implementation of
// shadowedProfile — treating its own read failure as a positive — reads
// perfectly and would report every mistyped profile as a shadowed one.
func TestAMissingProfileIsNotReportedAsAShadowedOne(t *testing.T) {
	awsFiles(t, ssoSessionProfile, staticKeys("prod-devops"))

	_, err := New(context.Background(), Spec{
		Cluster: "example-prod-eks", Region: "eu-west-3", Profile: "no-such-profile",
	})
	if err == nil {
		t.Fatal("a profile that is in neither file must not load")
	}
	if errors.Is(err, ErrProfileShadowed) {
		t.Fatalf("a missing profile reported as a shadowed one: %v", err)
	}
}

// The second read has to look at the files the first one used.
//
// `LoadSharedConfigProfile` computes its defaults from the home directory at
// package init and ignores AWS_CONFIG_FILE and AWS_SHARED_CREDENTIALS_FILE,
// which `LoadDefaultConfig` honors. Left alone it would read whatever is in
// the home directory of whoever runs this — nothing on a build agent, the
// operator's own configuration on a laptop — and the check would quietly never
// fire in the container, which is the deployment that sets both variables.
func TestTheShadowCheckReadsTheFilesTheCredentialsCameFrom(t *testing.T) {
	awsFiles(t, ssoSessionProfile, staticKeys("prod-devops"))

	var options config.LoadSharedConfigOptions
	sharedConfigFiles(&options)

	if len(options.ConfigFiles) != 1 || options.ConfigFiles[0] != os.Getenv("AWS_CONFIG_FILE") {
		t.Errorf("the config file was not taken from AWS_CONFIG_FILE: %v", options.ConfigFiles)
	}
	if len(options.CredentialsFiles) != 1 ||
		options.CredentialsFiles[0] != os.Getenv("AWS_SHARED_CREDENTIALS_FILE") {
		t.Errorf("the credentials file was not taken from AWS_SHARED_CREDENTIALS_FILE: %v",
			options.CredentialsFiles)
	}
}

// The effective profile is the one the SDK will read, not the one the
// kubeconfig named.
func TestTheEffectiveProfileFollowsTheSDKsOwnResolution(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	if got := effectiveProfile("named"); got != "named" {
		t.Errorf("a named profile wins: got %q", got)
	}
	if got := effectiveProfile(""); got != "default" {
		t.Errorf("nothing named and nothing in the environment is `default`: got %q", got)
	}

	t.Setenv("AWS_DEFAULT_PROFILE", "from-legacy-variable")
	if got := effectiveProfile(""); got != "from-legacy-variable" {
		t.Errorf("AWS_DEFAULT_PROFILE decides when AWS_PROFILE is unset: got %q", got)
	}
	t.Setenv("AWS_PROFILE", "from-variable")
	if got := effectiveProfile(""); got != "from-variable" {
		t.Errorf("AWS_PROFILE beats AWS_DEFAULT_PROFILE: got %q", got)
	}
	if got := effectiveProfile("named"); got != "named" {
		t.Errorf("the kubeconfig's own profile beats both: got %q", got)
	}
}
