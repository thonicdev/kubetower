package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// An RBAC write under the server profile reaches a client as 403
// `rbac_not_in_profile`, its sentence said once, whatever the kind read out of
// the document; and the pages draw the verb disabled with that sentence.

var rbacBindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}

const rbacBinding = "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: escalate\n  namespace: shop\n" +
	"roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}\n" +
	"subjects: [{kind: User, name: frank@example.com, apiGroup: rbac.authorization.k8s.io}]\n"

type rbacActsResolver struct{ fakeActsResolver }

func (r rbacActsResolver) Locate(ctx context.Context, cluster, kind string) (edit.Location, error) {
	if strings.EqualFold(kind, "RoleBinding") || kind == "rolebindings" {
		return edit.Location{Cluster: cluster, Resource: rbacBindingGVR, Kind: "RoleBinding", Namespaced: true, Dynamic: r.dyn}, nil
	}
	return r.fakeActsResolver.Locate(ctx, cluster, kind)
}

func TestAnRBACWriteUnderTheServerProfileIsItsOwn403(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rbacBindingGVR: "RoleBindingList"})
	var sent []string
	dyn.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sent = append(sent, action.GetVerb())
		return false, nil, nil
	})
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, InCluster: true, Audit: audit.New(nopSink{})})
	writer := edit.New(edit.Options{Resolve: rbacActsResolver{fakeActsResolver{dyn: dyn}}, Logf: func(string, ...any) {}, Access: gate})
	acts := apiActs{s: &Server{writer: writer}}
	ctx := identity.With(context.Background(), identity.Who{Name: "frank@example.com", Method: identity.SSO})
	req := api.ManifestWrite{Document: rbacBinding}

	for name, call := range map[string]func() error{
		"preview create": func() error { _, err := acts.PreviewCreate(ctx, "prod", req); return err },
		"create":         func() error { _, err := acts.ApplyCreate(ctx, "prod", req); return err },
		"preview apply":  func() error { _, err := acts.PreviewManifest(ctx, "prod", req); return err },
	} {
		err := call()
		if !errors.Is(err, api.ErrRBACNotInProfile) || errors.Is(err, api.ErrRefused) || errors.Is(err, api.ErrInvalid) {
			t.Errorf("%s: %v, want ErrRBACNotInProfile alone", name, err)
			continue
		}
		const sentence = "RBAC changes are not available in the server profile in this version"
		if !strings.HasPrefix(err.Error(), sentence) || strings.Count(err.Error(), sentence) != 1 {
			t.Errorf("%s: the detail does not say it once: %q", name, err)
		}
	}
	if len(sent) != 0 {
		t.Errorf("an RBAC write reached the cluster: %v", sent)
	}
}

// The pages: a verb, the editor and the "+" on an RBAC kind are drawn disabled
// with the profile's sentence, never withheld as the person's refusal.
func TestAnRBACVerbIsDrawnDisabledUnderTheServerProfile(t *testing.T) {
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, InCluster: true, Audit: audit.New(nopSink{})})
	ctx := identity.With(context.Background(), identity.Who{Name: "frank@example.com", Method: identity.SSO})
	question := access.Attributes{Verb: "delete", Group: "rbac.authorization.k8s.io", Version: "v1",
		Resource: "rolebindings", Namespace: "shop", Name: "escalate"}
	v := reviewBoth(ctx, gate, "prod", []access.Attributes{question})[0]
	editable, why := editOffer(v)
	if editable || !strings.HasPrefix(why, "RBAC changes are not available in the server profile in this version") {
		t.Errorf("verdict %+v drawn as editable=%t %q", v, editable, why)
	}
	offered := []api.DetailAction{{Verb: "delete", Label: "Delete", Permitted: true}}
	got := gateActions(offered, map[actions.Verb]verdict{actions.Delete: v})
	if len(got) != 1 || got[0].Permitted || got[0].Reason == nil || !strings.HasPrefix(got[0].Reason.Message, "RBAC changes") {
		t.Errorf("the delete was drawn %s", fmt.Sprintf("%+v", got))
	}
}
