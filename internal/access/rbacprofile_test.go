package access_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

// Under the server profile this version does not change RBAC: every verb but a
// read on the four RBAC kinds is answered NotInProfile before any review is
// asked, whoever asks, and Perform never runs the request.

func allowAll(accesstest.Question) (bool, error) { return true, nil }

func rbacQuestion(verb, resource, namespace string) access.Attributes {
	return access.Attributes{Verb: verb, Group: "rbac.authorization.k8s.io", Version: "v1",
		Resource: resource, Namespace: namespace, Name: "admin"}
}

var rbacResources = map[string]string{
	"roles": "shop", "clusterroles": "", "rolebindings": "shop", "clusterrolebindings": "",
}

var rbacWriteVerbs = []string{"create", "update", "patch", "delete", "deletecollection"}

func TestTheServerProfileRefusesEveryRBACWriteUnasked(t *testing.T) {
	password := identity.With(context.Background(), identity.Who{Name: "admin", Method: identity.Password})
	for resource, namespace := range rbacResources {
		for _, verb := range rbacWriteVerbs {
			for who, ctx := range map[string]context.Context{"sso": alice(), "password": password} {
				t.Run(resource+"/"+verb+"/"+who, func(t *testing.T) {
					r := &accesstest.Reviews{Answer: allowAll}
					g := inCluster(r, nil)
					attrs := rbacQuestion(verb, resource, namespace)
					ran := false
					answer, err := g.Perform(ctx, "prod", attrs, func(context.Context) error {
						ran = true
						return nil
					})
					if ran {
						t.Fatal("an RBAC write was made under the server profile")
					}
					if answer.Decision != access.NotInProfile {
						t.Errorf("decision %q, want not_in_profile", answer.Decision)
					}
					if len(r.Asked()) != 0 {
						t.Errorf("a review was asked for a write the profile refuses: %+v", r.Asked())
					}
					if !errors.Is(err, access.ErrRBACNotInProfile) {
						t.Fatalf("err %v, want ErrRBACNotInProfile", err)
					}
					if errors.Is(err, access.ErrDenied) || errors.Is(err, access.ErrUnreviewable) {
						t.Errorf("the profile's refusal reads as a review's answer: %v", err)
					}
					if !strings.HasPrefix(err.Error(), "RBAC changes are not available in the server profile in this version") {
						t.Errorf("the refusal does not say why: %q", err)
					}
					var refused *access.Refused
					if errors.As(err, &refused) && refused.Status().Code != http.StatusForbidden {
						t.Errorf("status %d, want 403", refused.Status().Code)
					}
				})
			}
		}
	}
}

// Reads of RBAC are untouched under the server profile, and every other kind's
// write still goes to the review.
func TestTheServerProfileStillReadsRBACAndWritesOtherKinds(t *testing.T) {
	r := &accesstest.Reviews{Answer: allowAll}
	g := inCluster(r, nil)
	for _, verb := range []string{"get", "list", "watch"} {
		if got := g.Review(alice(), "prod", rbacQuestion(verb, "rolebindings", "shop")); got.Decision != access.Allowed {
			t.Errorf("%s rolebindings answered %+v, want allowed", verb, got)
		}
	}
	ran := false
	if _, err := g.Perform(alice(), "prod", deletePods, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("a pod delete under the server profile: ran=%t err=%v", ran, err)
	}
}

// The desktop profile is unchanged: the same writes are reviewed and made.
func TestTheDesktopStillWritesRBAC(t *testing.T) {
	for resource, namespace := range rbacResources {
		for _, verb := range rbacWriteVerbs {
			r := &accesstest.Reviews{Answer: allowAll}
			g := r.Gate()
			ran := false
			if _, err := g.Perform(context.Background(), "prod", rbacQuestion(verb, resource, namespace),
				func(context.Context) error { ran = true; return nil }); err != nil || !ran {
				t.Errorf("%s %s on the desktop: ran=%t err=%v", verb, resource, ran, err)
			}
			if len(r.Asked()) == 0 {
				t.Errorf("%s %s on the desktop was not reviewed", verb, resource)
			}
		}
	}
}
