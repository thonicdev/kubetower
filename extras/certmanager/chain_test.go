package certmanager

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

func owned(t testing.TB, doc map[string]any, ownerKind, ownerName string, created time.Time) unstructured.Unstructured {
	meta := doc["metadata"].(map[string]any)
	meta["creationTimestamp"] = created.Format(time.RFC3339)
	if ownerKind != "" {
		meta["ownerReferences"] = []any{map[string]any{"apiVersion": "x/v1", "kind": ownerKind, "name": ownerName, "uid": "u-" + ownerName}}
	}
	return obj(t, doc)
}

func request(t testing.TB, name, certName string, created time.Time, conds ...any) unstructured.Unstructured {
	return owned(t, map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "CertificateRequest",
		"metadata": map[string]any{"namespace": "shop", "name": name},
		"status":   map[string]any{"conditions": conds},
	}, "Certificate", certName, created)
}

func ownedOrder(t testing.TB, name, reqName, state, reason string) unstructured.Unstructured {
	o := order(t, "shop", name, state, reason)
	return owned(t, o.Object, "CertificateRequest", reqName, testNow.Add(-time.Hour))
}

func ownedChallenge(t testing.TB, name, orderName, state, reason string) unstructured.Unstructured {
	c := challenge(t, "shop", name, state, reason)
	return owned(t, c.Object, "Order", orderName, testNow.Add(-time.Hour))
}

func chainHost(t testing.TB) *fakeHost {
	h := newHost()
	h.add("certificates", certificate(t, "shop", "web", time.Time{}, "False"))
	h.add("clusterissuers", issuer(t, "ClusterIssuer", "", "letsencrypt", "True", ""))
	return h
}

func chainOf(t testing.TB, h *fakeHost) Chain {
	t.Helper()
	at(t)
	rec := serve(t, h, "/x/prod/certmanager/objects/certificates.cert-manager.io/shop/web")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := decode[ObjectAnswer](t, rec)
	c, ok := got.Chains["web"]
	if !ok {
		t.Fatalf("no chain in %+v", got)
	}
	return c
}

func TestChainComplete(t *testing.T) {
	h := chainHost(t)
	old := testNow.Add(-48 * time.Hour)
	h.add("certificaterequests",
		request(t, "web-1", "web", old, cond("Ready", "True", "Issued", "ok")),
		request(t, "web-2", "web", testNow.Add(-2*time.Hour), cond("Approved", "True", "cert-manager.io", ""), cond("Ready", "True", "Issued", "Certificate fetched")),
		request(t, "other-1", "other", testNow, cond("Denied", "True", "Nope", "no")),
	)
	h.add("orders", ownedOrder(t, "web-2-1", "web-2", "valid", ""), ownedOrder(t, "web-1-1", "web-1", "errored", "old"))
	h.add("challenges", ownedChallenge(t, "web-2-1-c", "web-2-1", "valid", ""))
	c := chainOf(t, h)
	if c.Request.Name != "web-2" || c.Request.Phase != "issued" || c.Request.Tone != ToneOK {
		t.Errorf("request %+v", c.Request)
	}
	if c.Order.Name != "web-2-1" || c.Order.Tone != ToneOK {
		t.Errorf("order %+v", c.Order)
	}
	if len(c.Challenges) != 1 || c.Challenges[0].Type != "HTTP-01" || c.Challenges[0].Tone != ToneOK {
		t.Errorf("challenges %+v", c.Challenges)
	}
	if c.Issuer.State != LinkFound || c.Issuer.Tone != ToneOK || c.Issuer.Type != "acme" || c.Stuck != "" {
		t.Errorf("issuer %+v stuck %q", c.Issuer, c.Stuck)
	}
	if c.Secret != "web-tls" {
		t.Errorf("secret %q", c.Secret)
	}
}

func TestChainRequestDenied(t *testing.T) {
	h := chainHost(t)
	h.add("certificaterequests", request(t, "web-1", "web", testNow,
		cond("Approved", "False", "x", ""), cond("Denied", "True", "policy", "the policy refused it")))
	c := chainOf(t, h)
	if c.Request.Phase != "denied" || c.Request.Tone != ToneBad || c.Request.Reason != "policy" || c.Request.Message != "the policy refused it" {
		t.Errorf("request %+v", c.Request)
	}
	if len(c.Request.Conditions) != 2 {
		t.Errorf("conditions %+v", c.Request.Conditions)
	}
	// Partial: nothing came after a denied request, and that is not an error.
	if c.Order.State != LinkNone || len(c.Challenges) != 0 || c.Stuck != "CertificateRequest" {
		t.Errorf("order %+v challenges %+v stuck %q", c.Order, c.Challenges, c.Stuck)
	}
}

func TestChainOrderErroredWithoutChallenge(t *testing.T) {
	h := chainHost(t)
	h.add("certificaterequests", request(t, "web-1", "web", testNow, cond("Ready", "False", "Pending", "Waiting on certificate issuance")))
	h.add("orders", ownedOrder(t, "web-1-1", "web-1", "errored", "Failed to create Order: policy forbids the name"))
	c := chainOf(t, h)
	if c.Request.Phase != "pending" || c.Request.Tone != ToneWarn {
		t.Errorf("request %+v", c.Request)
	}
	if c.Order.Phase != "errored" || c.Order.Tone != ToneBad || !strings.Contains(c.Order.Reason, "policy forbids") {
		t.Errorf("order %+v", c.Order)
	}
	// The challenges kind was read to its end and holds none: none, not refused.
	if len(c.Challenges) != 0 || c.Stuck != "CertificateRequest" {
		t.Errorf("challenges %+v stuck %q", c.Challenges, c.Stuck)
	}
}

func TestChainChallengePending(t *testing.T) {
	h := chainHost(t)
	h.add("certificaterequests", request(t, "web-1", "web", testNow, cond("Approved", "True", "ok", "")))
	h.add("orders", ownedOrder(t, "web-1-1", "web-1", "pending", ""))
	h.add("challenges",
		ownedChallenge(t, "web-1-1-a", "web-1-1", "pending", "Waiting for HTTP-01 challenge propagation: wrong status code '404'"),
		ownedChallenge(t, "web-1-1-b", "web-1-1", "valid", ""),
		ownedChallenge(t, "elsewhere", "another-order", "errored", "not ours"),
	)
	c := chainOf(t, h)
	if c.Order.Tone != ToneWarn || len(c.Challenges) != 2 {
		t.Fatalf("order %+v challenges %+v", c.Order, c.Challenges)
	}
	if a := c.Challenges[0]; a.Name != "web-1-1-a" || a.Tone != ToneBad || !strings.Contains(a.Reason, "404") || a.DNSName == "" {
		t.Errorf("challenge a %+v", a)
	}
	if c.Challenges[1].Tone != ToneOK {
		t.Errorf("challenge b %+v", c.Challenges[1])
	}
}

func TestChainRefusedIsNotAbsent(t *testing.T) {
	h := chainHost(t)
	h.add("certificaterequests", request(t, "web-1", "web", testNow, cond("Approved", "True", "ok", "")))
	h.add("orders", ownedOrder(t, "web-1-1", "web-1", "pending", ""))
	h.listErr["challenges"] = extension.ErrDenied
	h.listErr["clusterissuers"] = extension.ErrDenied
	c := chainOf(t, h)
	if len(c.Challenges) != 1 || c.Challenges[0].State != LinkRefused {
		t.Errorf("challenges %+v", c.Challenges)
	}
	if c.Issuer.State != LinkRefused || c.Issuer.Name != "letsencrypt" {
		t.Errorf("issuer %+v", c.Issuer)
	}

	// The same with no request refused: the request is refused, the order after it too.
	h2 := chainHost(t)
	h2.listErr["certificaterequests"] = extension.ErrDenied
	c = chainOf(t, h2)
	if c.Request.State != LinkRefused || c.Order.State != LinkRefused {
		t.Errorf("request %+v order %+v", c.Request, c.Order)
	}
	// A failed read is neither.
	h3 := chainHost(t)
	h3.listErr["certificaterequests"] = errors.New("boom")
	if c = chainOf(t, h3); c.Request.State != LinkFailed {
		t.Errorf("request %+v", c.Request)
	}
}

func TestChainAbsent(t *testing.T) {
	// Nothing was ever requested: every kind was read, none holds an object.
	c := chainOf(t, chainHost(t))
	if c.Request.State != LinkNone || c.Order.State != LinkNone || c.Issuer.State != LinkFound {
		t.Errorf("%+v", c)
	}
	// A missing issuer is none; an ACME-less cluster serves no Order kind.
	h := chainHost(t)
	h.objects["clusterissuers"] = nil
	h.add("certificaterequests", request(t, "web-1", "web", testNow))
	delete(h.registry.served, OrdersKind)
	c = chainOf(t, h)
	if c.Issuer.State != LinkNone || c.Order.State != LinkUnserved {
		t.Errorf("issuer %+v order %+v", c.Issuer, c.Order)
	}
	crds[OrdersKind] = extension.CRD{Group: "acme.cert-manager.io", Version: "v1", Plural: "orders", Kind: "Order", Namespaced: true}
}

func TestChainIsBoundedAndNeverReadsSecrets(t *testing.T) {
	h := chainHost(t)
	h.add("certificaterequests", request(t, "web-1", "web", testNow, cond("Approved", "True", "ok", "")))
	h.add("orders", ownedOrder(t, "web-1-1", "web-1", "pending", ""))
	for i := 0; i < maxChallenges+5; i++ {
		h.add("challenges", ownedChallenge(t, "c-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "web-1-1", "pending", ""))
	}
	c := chainOf(t, h)
	if len(c.Challenges) != maxChallenges || c.MoreChallenges != 5 {
		t.Errorf("%d challenges, %d more", len(c.Challenges), c.MoreChallenges)
	}
	for _, q := range h.listed {
		if q.GVR.Resource == "secrets" {
			t.Fatal("a Secret was read")
		}
		if q.Namespace != "shop" && q.GVR.Resource != "clusterissuers" {
			t.Errorf("a read left the namespace: %+v", q)
		}
		if q.Limit == 0 || q.Limit > listLimit {
			t.Errorf("unbounded read: %+v", q)
		}
	}
}
