package netinspect

import (
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func podOf(t *testing.T, h *fakeHost, namespace, name string) PodAnswer {
	t.Helper()
	w := serve(t, h, "/x/kind/netinspect/pods/"+namespace+"/"+name)
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body.String())
	}
	return decode[PodAnswer](t, w)
}

// A pod's tab: each direction's isolation, the policies, and what their
// rules allow, with the pods each peer designates now.
func TestAPodSaysWhatSelectsItAndWhomItsRulesAllow(t *testing.T) {
	h := shop(t)
	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, apiFromWeb())
	h.policy(t, webEgress(80))

	api := podOf(t, h, "shop", "api")
	if api.Mode != "rules" || !api.Pod.In || api.Pod.Out || !api.Ingress.Isolated || api.Egress.Isolated {
		t.Fatalf("api: %+v", api)
	}
	if len(api.Ingress.Policies) != 2 || len(api.Ingress.Rules) != 1 {
		t.Fatalf("api's ingress: %+v", api.Ingress)
	}
	rule := api.Ingress.Rules[0]
	if rule.Name != "api-from-web" || rule.Rule != 1 || strings.Join(rule.Ports, ",") != "http" ||
		len(rule.Peers) != 1 || rule.Peers[0].Kind != "pods" || rule.Peers[0].Pods != 1 || rule.Peers[0].Namespaces[0] != "shop" {
		t.Errorf("api's rule: %+v", rule)
	}

	web := podOf(t, h, "shop", "web")
	if !web.Egress.Isolated || len(web.Egress.Rules) != 1 || web.Egress.Rules[0].Peers[0].Pods != 1 {
		t.Errorf("web's egress: %+v", web.Egress)
	}

	if w := serve(t, h, "/x/kind/netinspect/pods/shop/nobody"); w.Code != http.StatusNotFound || decode[problem](t, w).Code != CodeNoSuchPod {
		t.Errorf("a pod that is not there answered %d", w.Code)
	}
}

// The column: one read, every pod, four answers.
func TestTheColumnAnswersEveryPod(t *testing.T) {
	h := shop(t)
	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, webEgress(80))
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "agent"},
		Spec:       corev1.PodSpec{HostNetwork: true},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})
	w := serve(t, h, "/x/kind/netinspect/cells")
	cells := decode[map[string]Cell](t, w)
	want := map[string]string{"shop/api": "in", "shop/web": "in · out", "shop/stranger": "in", "ops/probe": "open", "kube-system/agent": "host network"}
	for key, text := range want {
		if cells[key].Text != text || cells[key].Hint == "" {
			t.Errorf("%s: %+v, want %q", key, cells[key], text)
		}
	}
	if len(cells) != len(want) {
		t.Errorf("%d cells, want %d", len(cells), len(want))
	}
}

// A finished pod is there, and a deleted one is not: the two answers carry
// different codes, so the page can say "finished" and "gone" apart (R5 §5).
func TestAFinishedPodIsNotAGonePod(t *testing.T) {
	h := shop(t)
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "backup-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	})
	w := serve(t, h, "/x/kind/netinspect/pods/shop/backup-1")
	if w.Code != http.StatusNotFound || decode[problem](t, w).Code != CodeFinishedPod {
		t.Errorf("a finished pod answered %d %s", w.Code, w.Body.String())
	}
	w = serve(t, h, "/x/kind/netinspect/pods/shop/backup-2")
	if w.Code != http.StatusNotFound || decode[problem](t, w).Code != CodeNoSuchPod {
		t.Errorf("a deleted pod answered %d %s", w.Code, w.Body.String())
	}
}
