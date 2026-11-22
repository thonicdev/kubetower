package health

import (
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// A credential failure says as whom.
//
// `kubectl` works in a terminal and the console answers `Unauthorized` from the
// same kubeconfig, with nothing anywhere saying the two resolved different AWS
// profiles. A bare 401 from EKS is an identity the cluster could not map, so
// the identity is the whole diagnosis.
func TestACredentialFailureNamesTheProfileItSignedWith(t *testing.T) {
	failed := probeResult{state: StateSSO, detail: "Unauthorized — the session has lapsed"}

	named := withIdentity(failed, &cluster.Client{
		EKS: &cluster.EKSSigning{Cluster: "prod", Region: "eu-west-3", Profile: "prod-admin"},
	})
	if !strings.Contains(named.detail, "prod-admin") {
		t.Errorf("the profile is not on the row: %q", named.detail)
	}

	// The empty profile is the case that matters, and it must not read as "no
	// credentials": it means the kubeconfig named none, so this process's own
	// environment decided — and a shortcut launch does not have a shell's.
	defaulted := withIdentity(failed, &cluster.Client{
		EKS: &cluster.EKSSigning{Cluster: "prod", Region: "eu-west-3"},
	})
	if !strings.Contains(defaulted.detail, "AWS_PROFILE") {
		t.Errorf("the row does not say what decided the identity: %q", defaulted.detail)
	}
	if !strings.Contains(defaulted.detail, "kubectl") {
		t.Errorf("the row does not connect this to the terminal that works, which is the whole "+
			"of the report it exists for: %q", defaulted.detail)
	}
}

// It is added where it is the answer and nowhere else. A row that says
// everything says nothing, and on a name that does not resolve the profile is
// noise.
func TestTheIdentityIsNotAddedToFailuresItCannotExplain(t *testing.T) {
	signing := &cluster.Client{EKS: &cluster.EKSSigning{Cluster: "prod", Region: "eu-west-3"}}

	for _, state := range []string{StateDNS, StateTimeout, StateRefused, StateOK} {
		before := probeResult{state: state, detail: "something else"}
		if got := withIdentity(before, signing); got.detail != before.detail {
			t.Errorf("%s carries the identity note it cannot use: %q", state, got.detail)
		}
	}

	// And a context this process does not sign for has no profile to name: the
	// CLI resolved it, or it is not EKS at all.
	plain := probeResult{state: StateSSO, detail: "Unauthorized"}
	if got := withIdentity(plain, &cluster.Client{}); got.detail != plain.detail {
		t.Errorf("a context KubeTower does not sign for was given a profile: %q", got.detail)
	}
	if got := withIdentity(plain, nil); got.detail != plain.detail {
		t.Errorf("a probe with no client at all invented one: %q", got.detail)
	}
}

// TestTheARNWinsOverTheProfileOnceThereIsOne.
//
// The profile is the *question* — which line of `~/.aws/config` was consulted —
// and two machines whose `prod-devops` resolve to two different roles produce
// the identical row. The ARN is what the authenticator matched against, so it is
// what an access entry or an aws-auth mapping is compared with, and it goes
// first for that reason. The profile stays beside it because it is what the
// operator will edit.
func TestTheARNWinsOverTheProfileOnceThereIsOne(t *testing.T) {
	const arn = "signed as arn:aws:sts::123456789012:assumed-role/devops/alice, " +
		"credentials from SSOProvider"
	failed := probeResult{state: StateIAM, detail: "Unauthorized"}

	got := withIdentity(failed, &cluster.Client{
		EKS: &cluster.EKSSigning{
			Cluster: "prod-eks-1", Region: "eu-west-3", Profile: "prod-devops",
			Identity: func() string { return arn },
		},
	})
	if !strings.Contains(got.detail, arn) {
		t.Errorf("the ARN is not on the row, so the row still cannot answer 'as whom': %q", got.detail)
	}
	if !strings.Contains(got.detail, "prod-devops") {
		t.Errorf("the profile went away with the ARN's arrival; it is what the operator edits: %q",
			got.detail)
	}
	if !strings.Contains(got.detail, "aws-auth") {
		t.Errorf("the row names an identity and not what to compare it with: %q", got.detail)
	}
}

// TestNoIdentityYetFallsBackToTheProfile.
//
// The ARN is established after the first refusal, asynchronously and off the
// request path, so **every row before that one has none** — and a probe that
// answered with nothing rather than with the profile would lose information, on
// exactly the first render an operator sees.
func TestNoIdentityYetFallsBackToTheProfile(t *testing.T) {
	failed := probeResult{state: StateSSO, detail: "Unauthorized"}

	for _, tc := range []struct {
		name    string
		signing *cluster.EKSSigning
	}{
		{"nothing has asked yet", &cluster.EKSSigning{
			Cluster: "prod", Region: "eu-west-3", Profile: "prod-admin",
			Identity: func() string { return "" },
		}},
		{"no identity function at all", &cluster.EKSSigning{
			Cluster: "prod", Region: "eu-west-3", Profile: "prod-admin",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := withIdentity(failed, &cluster.Client{EKS: tc.signing})
			if !strings.Contains(got.detail, "prod-admin") {
				t.Errorf("the row lost the profile it had before the ARN existed: %q", got.detail)
			}
		})
	}
}
