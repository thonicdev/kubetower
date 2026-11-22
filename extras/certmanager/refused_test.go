package certmanager

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

func hasNote(notes []string, part string) bool {
	for _, n := range notes {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

// A person who may list the Issuers of their namespaces but not the
// ClusterIssuers counts the broken Issuers they can see: the tile is a figure,
// not a dash, and a note says the ClusterIssuers were left out.
func TestIssuersBrokenCountsWhatTheNamespacedPersonMayRead(t *testing.T) {
	at(t)
	h := newHost()
	h.add("certificates", certificate(t, "shop", "a", testNow.Add(3*day), "True"))
	h.add("issuers",
		issuer(t, "Issuer", "shop", "ca", "False", "Error getting keypair"),
		issuer(t, "Issuer", "shop", "acme", "False", "register failed"),
		issuer(t, "Issuer", "shop", "fine", "True", ""))
	h.listErr["clusterissuers"] = fmt.Errorf("list: %w", extension.ErrDenied)
	o := decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if o.Counts.IssuersBroken != 2 || o.Issuers.State != StateRead || o.Issuers.Total != 3 {
		t.Fatalf("counts %+v issuers %+v", o.Counts, o.Issuers)
	}
	if !hasNote(o.Notes, "ClusterIssuers") {
		t.Errorf("notes %v do not say the ClusterIssuers were left out", o.Notes)
	}
}

// Neither kind readable: Refused when both were refused, Failed when one
// failed, never "read" with a zero.
func TestIssuersSectionStateWhenNothingIsReadable(t *testing.T) {
	at(t)
	denied := fmt.Errorf("list: %w", extension.ErrDenied)
	h := newHost()
	h.add("certificates", certificate(t, "shop", "a", testNow.Add(3*day), "True"))
	h.listErr["issuers"], h.listErr["clusterissuers"] = denied, denied
	o := decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if o.Issuers.State != StateRefused || o.Issuers.Message == "" {
		t.Errorf("both refused: %+v", o.Issuers)
	}
	h.listErr["issuers"] = errors.New("timeout")
	o = decode[Overview](t, serve(t, h, "/x/prod/certmanager/"))
	if o.Issuers.State != StateFailed {
		t.Errorf("one refused, one failed: %+v", o.Issuers)
	}
}

// A person refused every Certificate read is answered 403 list_forbidden, not
// a 200 with nothing in it; a cluster with none is a 200 with none.
func TestEveryCertificateReadRefusedIsAnswered403(t *testing.T) {
	at(t)
	h := newHost()
	h.listErr["certificates"] = fmt.Errorf("list: %w", extension.ErrDenied)
	rec := serve(t, h, "/x/prod/certmanager/")
	if rec.Code != http.StatusForbidden || decode[problem](t, rec).Code != CodeListForbidden {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	empty := serve(t, newHost(), "/x/prod/certmanager/")
	if empty.Code != http.StatusOK || decode[Overview](t, empty).Counts.Total != 0 {
		t.Fatalf("empty cluster: %d %s", empty.Code, empty.Body)
	}
}
