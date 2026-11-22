package security

import (
	"errors"
	"strings"
	"testing"
)

func httpRoute(namespace, name string, parents []map[string]any, backends ...map[string]any) *fakeObj {
	return &fakeObj{namespace: namespace, name: name, body: func() map[string]any {
		var refs, ps []any
		for _, b := range backends {
			refs = append(refs, b)
		}
		for _, p := range parents {
			ps = append(ps, p)
		}
		return map[string]any{"apiVersion": gatewayGroup + "/v1", "kind": "HTTPRoute", "metadata": meta(namespace, name, nil, nil),
			"spec": map[string]any{"parentRefs": ps, "rules": []any{map[string]any{"backendRefs": refs}}}}
	}}
}

// gatewayShop is shop with Gateway API served: the worker, behind a
// ClusterIP only, is reached through an HTTPRoute attached to a Gateway in
// another namespace; a route's backend that is not a Service is ignored.
func gatewayShop(t *testing.T) *fakeHost {
	h := shop(t)
	h.reg.gateway = map[string]bool{"httproutes": true, "grpcroutes": true}
	h.add("httproutes",
		httpRoute("shop", "worker-route", []map[string]any{{"name": "public", "namespace": "infra"}},
			map[string]any{"name": "worker", "port": int64(8080)},
			map[string]any{"name": "bucket", "kind": "Backend", "group": "example.io"}),
		httpRoute("edge", "cross", []map[string]any{{"name": "edge-gw"}, {"name": "svc", "kind": "Service"}},
			map[string]any{"name": "api", "namespace": "shop"}))
	return h
}

func TestARouteToAServiceExposesItsPods(t *testing.T) {
	o := decode[Overview](t, serve(t, gatewayShop(t), "/"))
	worker := rowOf(t, o, "Deployment", "worker")
	if !worker.Exposed || worker.Exposure != "HTTPRoute shop/worker-route (Gateway infra/public)" {
		t.Errorf("worker: %+v", worker)
	}
	d := decode[Detail](t, serve(t, gatewayShop(t), "/workload/shop/Deployment/api"))
	if got := strings.Join(d.Exposure, ";"); got != "HTTPRoute edge/cross (Gateway edge/edge-gw);Ingress shop/public" {
		t.Errorf("api's detail exposure %q: a backend in another namespace, the parent that is not a Gateway left out", got)
	}
}

// A cluster that does not serve Gateway API is asked nothing about it.
func TestNoGatewayAPIReadsNoRoutes(t *testing.T) {
	h := shop(t)
	serve(t, h, "/")
	for _, q := range h.queries {
		if q.GVR.Group == gatewayGroup {
			t.Errorf("read %s on a cluster that does not serve it", q.GVR.Resource)
		}
	}
}

// Routes that may not be read are a note; the rest of the answer stands.
func TestRoutesThatCannotBeReadAreANote(t *testing.T) {
	h := gatewayShop(t)
	h.listErr["httproutes"] = errors.New("httproutes is forbidden")
	o := decode[Overview](t, serve(t, h, "/"))
	if worker := rowOf(t, o, "Deployment", "worker"); worker.Exposed {
		t.Errorf("worker exposed without its route: %+v", worker)
	}
	if !strings.Contains(strings.Join(o.Notes, "\n"), "HTTPRoutes") {
		t.Errorf("notes %q", o.Notes)
	}
	if web := rowOf(t, o, "Deployment", "web"); !web.Exposed {
		t.Errorf("web lost its LoadBalancer: %+v", web)
	}
}

// A pod on its node's network is exposed, read by field as a table.
func TestAPodOnTheHostNetworkIsExposed(t *testing.T) {
	h := shop(t)
	h.add("replicasets", replicaSet("shop", "agent-1", "agent", 1))
	agent := pod("shop", "agent-1-a", map[string]string{"app": "agent"}, "ReplicaSet", "agent-1")
	agent.hostNetwork = true
	h.add("pods", agent)
	o := decode[Overview](t, serve(t, h, "/"))
	if a := rowOf(t, o, "Deployment", "agent"); !a.Exposed || a.Exposure != "Host network" {
		t.Errorf("agent: %+v", a)
	}
	if w := rowOf(t, o, "Deployment", "worker"); w.Exposed {
		t.Errorf("worker is not on the host network: %+v", w)
	}
	for _, q := range h.queries {
		if q.FieldSelector == "spec.hostNetwork=true" && !q.Columns {
			t.Errorf("host-network pods read whole: %+v", q)
		}
	}
}
