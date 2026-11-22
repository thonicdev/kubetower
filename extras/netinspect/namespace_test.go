package netinspect

import (
	"strconv"
	"strings"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
)

// One namespace in the pod tab's shape: from where, by which label, into which
// workload; to where, out of which; and each workload's isolation.
func TestANamespaceSaysWhatComesInAndWhatGoesOut(t *testing.T) {
	h := shop(t)
	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, apiFromWeb())
	h.policy(t, webEgress(80))
	v := overviewOf(t, h, "?ns=shop").View
	if v == nil {
		t.Fatal("no view under ?ns=")
	}

	// Ingress: api-from-web lets app=web into api; stranger and web are
	// isolated by the default deny with nothing allowing them.
	var lane *Lane
	for i := range v.Ingress.Lanes {
		if v.Ingress.Lanes[i].Name == "api-from-web" {
			lane = &v.Ingress.Lanes[i]
		}
	}
	if lane == nil || lane.Kind != "allow" || strings.Join(lane.Targets, ",") != "api" || lane.Pods != 1 ||
		len(lane.Peers) != 1 || !strings.Contains(lane.Peers[0].Label, "app=web") || strings.Join(lane.Ports, ",") != "http" {
		t.Fatalf("ingress lanes: %+v", v.Ingress.Lanes)
	}
	if len(v.Ingress.Open) != 0 || strings.Join(v.Ingress.Closed, ",") != "stranger,web" {
		t.Errorf("ingress open %v closed %v, want none open and stranger,web closed", v.Ingress.Open, v.Ingress.Closed)
	}

	// Egress: web is isolated and may reach app=api on 80; the others are open.
	if len(v.Egress.Lanes) != 1 || strings.Join(v.Egress.Lanes[0].Targets, ",") != "web" || strings.Join(v.Egress.Lanes[0].Ports, ",") != "80/TCP" {
		t.Errorf("egress lanes: %+v", v.Egress.Lanes)
	}
	if strings.Join(v.Egress.Open, ",") != "api,stranger" || v.Egress.OpenPods != 2 || len(v.Egress.Closed) != 0 {
		t.Errorf("egress open %v (%d) closed %v", v.Egress.Open, v.Egress.OpenPods, v.Egress.Closed)
	}

	// Each workload: its isolation and the labels its rules allow.
	byName := map[string]WorkloadView{}
	for _, w := range v.Workloads {
		byName[w.Name] = w
	}
	if api := byName["api"]; api.IsolatedIn != 1 || api.IsolatedOut != 0 || len(api.From) != 1 || !strings.Contains(api.From[0], "app=web") {
		t.Errorf("api: %+v", api)
	}
	if web := byName["web"]; web.IsolatedOut != 1 || len(web.To) != 1 || !strings.Contains(web.To[0], "app=api") {
		t.Errorf("web: %+v", web)
	}
	if v.WorkloadsTotal != 3 {
		t.Errorf("%d workloads, want 3", v.WorkloadsTotal)
	}

	// The cluster's answer carries no namespace view.
	if overviewOf(t, h, "").View != nil {
		t.Error("the cluster's answer drew a namespace view")
	}
}

// A namespace with no policy is open both ways, and says so per workload.
func TestAnOpenNamespaceIsOpenBothWays(t *testing.T) {
	v := overviewOf(t, shop(t), "?ns=ops").View
	if v == nil || strings.Join(v.Ingress.Open, ",") != "probe" || strings.Join(v.Egress.Open, ",") != "probe" ||
		len(v.Ingress.Lanes) != 0 || len(v.Egress.Lanes) != 0 {
		t.Fatalf("ops: %+v", v)
	}
}

// The lanes are bounded however many rules select the namespace, and the
// view of a large namespace is built in bounded time.
func TestANamespacesLanesAreBounded(t *testing.T) {
	h := newHost()
	h.namespace(t, "big", map[string]string{"team": "big"})
	for i := 0; i < 300; i++ {
		h.pod(t, "big", "p"+itoa(i), "10.1."+itoa(i/250)+"."+itoa(i%250), map[string]string{"app": "a" + itoa(i)})
		p := np("big", "allow-"+itoa(i), map[string]string{"app": "a" + itoa(i)})
		p.Spec.Ingress = append(p.Spec.Ingress, ingressFrom(map[string]string{"app": "a" + itoa((i+1)%300)}))
		h.policy(t, p)
	}
	start := time.Now()
	v := overviewOf(t, h, "?ns=big").View
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the view took %s", took)
	}
	if v == nil || len(v.Ingress.Lanes) != lanesPerSide || v.Ingress.LanesTotal != 300 || v.WorkloadsTotal != 300 {
		t.Fatalf("lanes %d of %d, workloads %d", len(v.Ingress.Lanes), v.Ingress.LanesTotal, v.WorkloadsTotal)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func ingressFrom(set map[string]string) networkingv1.NetworkPolicyIngressRule {
	return networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{podPeer(set)}}
}
