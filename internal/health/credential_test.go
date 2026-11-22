package health

import (
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// The exact sentences a read carries when the credential step fails, as the
// console produced them against a kubeconfig whose session had lapsed: the AWS
// CLI run by client-go (its words lost), and the SDK signing in this process.
const (
	readThroughPlugin = `listing pods: Get "https://127.0.0.1:44695/api/v1/pods?limit=500&resourceVersion=0": ` +
		`getting credentials: exec: executable aws failed with exit code 255`
	readThroughSDK = `listing pods: Get "https://127.0.0.1:44695/api/v1/pods?limit=500&resourceVersion=0": ` +
		`signing a token for "demo": operation error STS: GetCallerIdentity, get identity: get credentials: ` +
		`failed to refresh cached credentials, refresh cached SSO token failed, unable to refresh SSO token, ` +
		`operation error SSO OIDC: CreateToken, https response error StatusCode: 400, InvalidGrantException:`
	readThroughLegacySDK = `listing pods: Get "https://x/api/v1/pods": signing a token for "demo": ` +
		`get credentials: the SSO session has expired or is invalid`
)

func TestCredentialTellsTheCredentialStepFromTheCluster(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"nothing":                                    {nil, ""},
		"the AWS CLI refused, its words lost":        {errors.New(readThroughPlugin), StatePlugin},
		"the SDK's sso-session form lapsed":          {errors.New(readThroughSDK), StateSSO},
		"the SDK's legacy form lapsed":               {errors.New(readThroughLegacySDK), StateSSO},
		"wrapped once more":                          {fmt.Errorf("reading the overview: %w", errors.New(readThroughSDK)), StateSSO},
		"a plugin that printed the CLI's own words":  {errors.New("getting credentials: Error when retrieving token from sso: Token has expired and refresh failed"), StateSSO},
		"a bare 401 is Denied's, not this":           {apierrors.NewUnauthorized("Unauthorized"), ""},
		"a cluster that did not answer":              {errors.New(`Get "https://x/api/v1/pods": dial tcp 10.0.0.1:443: i/o timeout`), ""},
		"an object named like a session, not a step": {errors.New(`secrets "sso-session" not found`), ""},
		"a pod's own exit code, not the credential":  {errors.New("container app failed with exit code 1"), ""},
		"the SDK refused for another reason":         {errors.New(`signing a token for "demo": get credentials: no EC2 IMDS role found`), ""},
		"the words in the address are not the error": {errors.New(`Get "https://x/api/v1/namespaces/sso-session/pods?timeout=32s": getting credentials: dial tcp: i/o timeout`), ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Credential(c.err); got != c.want {
				t.Fatalf("Credential(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}
