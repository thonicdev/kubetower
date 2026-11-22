package netinspect

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/thonicdev/kubetower/extension"
)

// liveHost reads from a real cluster through the dynamic client: the rest of
// extension.Host is the fake's. It exists for the opt-in check below and
// reads with the kubeconfig's own identity.
type liveHost struct {
	*fakeHost
	dyn dynamic.Interface
}

func (h liveHost) List(r *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	var ri dynamic.ResourceInterface = h.dyn.Resource(q.GVR)
	if q.Namespace != "" {
		ri = h.dyn.Resource(q.GVR).Namespace(q.Namespace)
	}
	list, err := ri.List(r.Context(), metav1.ListOptions{Limit: q.Limit})
	if err != nil {
		return extension.Page{}, err
	}
	return extension.Page{Items: list.Items, Complete: list.GetContinue() == ""}, nil
}

// TestVerdictsAgainstALiveCluster prints the verdicts of the flows named in
// KT_CALICO_FLOWS ("ns/a>ns/b:port,..."), read from the cluster of the
// kubeconfig context KT_CALICO_CONTEXT. Skipped unless both are set.
func TestVerdictsAgainstALiveCluster(t *testing.T) {
	ctxName, flows := os.Getenv("KT_CALICO_CONTEXT"), os.Getenv("KT_CALICO_FLOWS")
	if ctxName == "" || flows == "" {
		t.Skip("KT_CALICO_CONTEXT and KT_CALICO_FLOWS are not set")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: ctxName}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := liveHost{fakeHost: newHost(), dyn: dyn}
	// The catalog the console would give: Calico's and AdminNetworkPolicy's kinds.
	h.crds = []extension.CRD{
		{Group: "crd.projectcalico.org", Version: "v1", Plural: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true},
		{Group: "crd.projectcalico.org", Version: "v1", Plural: "globalnetworkpolicies", Kind: "GlobalNetworkPolicy"},
		{Group: "crd.projectcalico.org", Version: "v1", Plural: "tiers", Kind: "Tier"},
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	rd, err := gather(r, h, "live")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("calico: %+v complete=%v notes=%v", rd.calico, rd.complete, rd.notes)
	for _, f := range splitFlows(flows) {
		v, err := rd.verdict(f.from, f.to, f.port, "")
		if err != nil {
			t.Errorf("%v: %v", f, err)
			continue
		}
		t.Logf("VERDICT %s -> %s:%s = %s | egress: %s | ingress: %s", f.from, f.to, f.port, v.Answer, v.Egress.Reason, v.Ingress.Reason)
	}
}

type liveFlow struct{ from, to, port string }

func splitFlows(s string) []liveFlow {
	var out []liveFlow
	for _, one := range strings.Split(s, ",") {
		a, rest, _ := strings.Cut(one, ">")
		b, port, _ := strings.Cut(rest, ":")
		out = append(out, liveFlow{a, b, port})
	}
	return out
}
