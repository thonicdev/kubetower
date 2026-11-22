package certmanager

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

func objectsHost(t *testing.T) *fakeHost {
	h := newHost()
	h.add("certificates",
		certificate(t, "shop", "web", testNow.Add(3*day), "True"),  // secret web-tls
		certificate(t, "shop", "api", testNow.Add(50*day), "True"), // secret api-tls
		certificate(t, "shop", "new", time.Time{}, "False"),        // secret new-tls
		certificate(t, "pay", "old", testNow.Add(-2*day), "False"), // secret old-tls
	)
	h.add("ingresses",
		ingress(t, "shop", "front", "web-tls", "api-tls"),
		ingress(t, "shop", "plain"),
		ingress(t, "shop", "other", "not-managed-tls"),
	)
	return h
}

func TestCells(t *testing.T) {
	at(t)
	h := objectsHost(t)

	got := decode[map[string]Cell](t, serve(t, h, "/x/prod/certmanager/cells?kind=certificates.cert-manager.io"))
	if len(got) != 4 || got["shop/web"].Text != "3 d" || got["shop/web"].Tone != "bad" ||
		got["shop/api"].Tone != "ok" || got["shop/new"].Text != "not issued" || got["pay/old"].Text != "expired" {
		t.Fatalf("certificates %+v", got)
	}
	if got["shop/web"].Href != "/c/prod/x/certmanager?cert=shop%2Fweb" {
		t.Errorf("href %q", got["shop/web"].Href)
	}

	got = decode[map[string]Cell](t, serve(t, h, "/x/prod/certmanager/cells?kind=secrets"))
	if len(got) != 4 || got["shop/web-tls"].Text != "3 d" || got["pay/old-tls"].Text != "expired" {
		t.Fatalf("secrets %+v", got)
	}

	// An Ingress shows the soonest of its certificates; one with none, nothing.
	got = decode[map[string]Cell](t, serve(t, h, "/x/prod/certmanager/cells?kind=ingresses"))
	if len(got) != 1 || got["shop/front"].Text != "3 d" {
		t.Fatalf("ingresses %+v", got)
	}

	for _, resource := range h.listedResources() {
		if resource == "secrets" {
			t.Fatal("a Secret list was asked for")
		}
	}
	if rec := serve(t, h, "/x/prod/certmanager/cells?kind=pods"); rec.Code != http.StatusBadRequest {
		t.Errorf("a pods column answered %d", rec.Code)
	}
}

func TestCellsWithoutCertManagerAreEmpty(t *testing.T) {
	at(t)
	h := objectsHost(t)
	h.registry = fakeRegistry{served: map[string]extension.CRD{}}
	rec := serve(t, h, "/x/prod/certmanager/cells?kind=ingresses")
	if rec.Code != http.StatusOK || len(decode[map[string]Cell](t, rec)) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := h.listedResources(); len(got) != 0 {
		t.Errorf("read %v without cert-manager", got)
	}
}

func TestCellsRefusedOrFailed(t *testing.T) {
	at(t)
	h := objectsHost(t)
	h.listErr["certificates"] = fmt.Errorf("x: %w", extension.ErrDenied)
	if rec := serve(t, h, "/x/prod/certmanager/cells?kind=secrets"); rec.Code != http.StatusForbidden {
		t.Errorf("refused: %d", rec.Code)
	}
	h = objectsHost(t)
	h.listErr["ingresses"] = fmt.Errorf("timeout")
	rec := serve(t, h, "/x/prod/certmanager/cells?kind=ingresses")
	if rec.Code != http.StatusBadGateway || decode[problem](t, rec).Message == "" {
		t.Errorf("ingresses failed: %d %s", rec.Code, rec.Body)
	}
}

func TestObject(t *testing.T) {
	at(t)
	h := objectsHost(t)
	o := decode[ObjectAnswer](t, serve(t, h, "/x/prod/certmanager/objects/secrets/shop/web-tls"))
	if len(o.Certificates) != 1 || o.Certificates[0].Name != "web" {
		t.Fatalf("secret %+v", o)
	}
	o = decode[ObjectAnswer](t, serve(t, h, "/x/prod/certmanager/objects/ingresses/shop/front"))
	if len(o.Certificates) != 2 || o.Certificates[0].Name != "web" {
		t.Fatalf("ingress %+v", o)
	}
	o = decode[ObjectAnswer](t, serve(t, h, "/x/prod/certmanager/objects/certificates.cert-manager.io/shop/api"))
	if len(o.Certificates) != 1 || o.Certificates[0].Name != "api" {
		t.Fatalf("certificate %+v", o)
	}
	for _, target := range []string{
		"/x/prod/certmanager/objects/ingresses/shop/plain",
		"/x/prod/certmanager/objects/secrets/shop/not-managed-tls",
		"/x/prod/certmanager/objects/certificates.cert-manager.io/shop/gone",
	} {
		rec := serve(t, h, target)
		if rec.Code != http.StatusNotFound || decode[problem](t, rec).Code != CodeNoCertificate {
			t.Errorf("%s: %d %s", target, rec.Code, rec.Body)
		}
	}
	// Only the object's namespace is read, and the one ClusterIssuer by name.
	for _, q := range h.listed {
		if q.GVR.Resource == "clusterissuers" && q.FieldSelector != "" && q.Limit == 1 {
			continue
		}
		if q.Namespace != "shop" {
			t.Errorf("read outside the object's namespace: %+v", q)
		}
	}
	if rec := serve(t, h, "/x/prod/certmanager/objects/pods/shop/x"); rec.Code != http.StatusBadRequest {
		t.Errorf("pods: %d", rec.Code)
	}
	h.listErr["certificates"] = fmt.Errorf("x: %w", extension.ErrUnreviewable)
	if rec := serve(t, h, "/x/prod/certmanager/objects/secrets/shop/web-tls"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unreviewable: %d", rec.Code)
	}
}
