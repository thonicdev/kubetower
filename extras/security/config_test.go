package security

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// twiceFailed is web's config audit report as trivy writes it for
// AVD-KSV-0118: one check failed for the pod and again for its container,
// so the summary says 2 high where one check failed.
func twiceFailed() *fakeObj {
	o := configReport("replicaset-web-1", scan{"shop", "ReplicaSet", "web-1", ""}, Counts{High: 2}, 0)
	o.body = func() map[string]any {
		failed := func(message string) map[string]any {
			return map[string]any{"checkID": "AVD-KSV-0118", "title": "Default security context configured", "severity": "HIGH",
				"category": "Kubernetes Security Check", "success": false, "messages": []any{message}}
		}
		return map[string]any{"apiVersion": group + "/v1alpha1", "kind": "ConfigAuditReport",
			"metadata": meta("shop", "replicaset-web-1", reportLabels(scan{"shop", "ReplicaSet", "web-1", ""}), nil),
			"report": map[string]any{"summary": summaryOf(Counts{High: 2}), "checks": []any{
				failed("Deployment 'web' uses the default security context"),
				failed("Container 'app' uses the default security context"),
				map[string]any{"checkID": "KSV900", "title": "Passing check", "severity": "CRITICAL", "success": true},
			}}}
	}
	return o
}

func shopTwiceFailed(t *testing.T) *fakeHost {
	h := shop(t)
	h.objects[KindConfigAudit] = nil
	h.add(KindConfigAudit, twiceFailed())
	return h
}

// One check ID failed twice counts once, in the detail and in /config.
func TestACheckFailedTwiceCountsOnce(t *testing.T) {
	h := shopTwiceFailed(t)
	d := decode[Detail](t, serve(t, h, "/workload/shop/Deployment/web"))
	if len(d.Checks) != 1 || d.Checks[0].ID != "AVD-KSV-0118" {
		t.Fatalf("detail checks %+v", d.Checks)
	}
	got := decode[map[string]*Counts](t, serve(t, h, "/config?w=shop/Deployment/web&w=shop/Pod/web-1-a&w=shop/Deployment/worker"))
	if c := got["shop/Deployment/web"]; c == nil || *c != (Counts{High: 1}) {
		t.Errorf("web: %+v", c)
	}
	if c := got["shop/Pod/web-1-a"]; c == nil || *c != (Counts{High: 1}) {
		t.Errorf("web's pod: %+v", c)
	}
	if c, ok := got["shop/Deployment/worker"]; !ok || c != nil {
		t.Errorf("worker has no config audit report, so null: %+v (present %v)", c, ok)
	}
}

// The overview's own figure stays the table's: /config is what corrects it,
// for the rows on screen. Asking for it reads no report of another namespace.
func TestConfigReadsOnlyTheNamespacesAsked(t *testing.T) {
	h := shopTwiceFailed(t)
	h.add("replicasets", replicaSet("other", "x-1", "x", 1))
	h.add(KindConfigAudit, configReport("replicaset-x-1", scan{"other", "ReplicaSet", "x-1", ""}, Counts{High: 1}, 1))
	serve(t, h, "/config?w=shop/Deployment/web")
	for _, q := range h.reportQueries(KindConfigAudit) {
		if q.Namespace != "shop" {
			t.Errorf("read config reports in %q", q.Namespace)
		}
	}
}

func TestConfigRefusesWhatItCannotAnswer(t *testing.T) {
	h := shop(t)
	problemOf(t, serve(t, h, "/config"), http.StatusBadRequest, CodeBadRequest)
	problemOf(t, serve(t, h, "/config?w=shop/Deployment"), http.StatusBadRequest, CodeBadRequest)
	problemOf(t, serve(t, h, "/config?w=shop/Ingress/web"), http.StatusBadRequest, CodeBadRequest)
	problemOf(t, serve(t, h, "/config?w=shop/Deployment/Bad_Name"), http.StatusBadRequest, CodeBadRequest)
	problemOf(t, serve(t, h, "/config?w=Shop!/Deployment/web"), http.StatusBadRequest, CodeBadRequest)
	q := url.Values{}
	for i := 0; i <= MaxConfigRows; i++ {
		q.Add("w", "shop/Deployment/web-"+strings.Repeat("a", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	problemOf(t, serve(t, h, "/config?"+q.Encode()), http.StatusBadRequest, CodeBadRequest)
	h.unknown = true
	problemOf(t, serve(t, h, "/config?w=shop/Deployment/web"), http.StatusNotFound, CodeUnknownCluster)
}

// Without trivy-operator every workload is null, and nothing fails.
func TestConfigWithoutTheOperatorIsNull(t *testing.T) {
	h := newHost(t)
	h.reg.served = map[string]bool{}
	got := decode[map[string]*Counts](t, serve(t, h, "/config?w=shop/Deployment/web"))
	if c, ok := got["shop/Deployment/web"]; !ok || c != nil {
		t.Errorf("%+v", got)
	}
}
