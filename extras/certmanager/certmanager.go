// Package certmanager is the Cert Manager extra: what cert-manager's
// certificates say about themselves, behind its own per-cluster switch.
//
// The page answers one question first - what expires in the next 30 days -
// then which ACME orders and challenges are failing and which issuers are
// broken. Every date is the one cert-manager wrote (`status.notAfter`): no
// certificate is decoded, and no Secret is read.
//
// Every read goes through `Host.List`, reviewed for the person, with a limit
// (`listLimit`): the page holds no cache, starts no goroutine and keeps nothing
// between requests, so a large cluster costs one bounded read per request and
// nothing after it.
//
// Three answers stay three: cert-manager is not installed (its CRD is unknown
// to the cluster), it is installed and this person may not read it (a
// refusal), and it is installed with nothing in scope (an empty list).
package certmanager

import (
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `certmanager`: the page's URL segment, the KT_EXTRAS name and the key
// the per-cluster switch is stored under.
const Key = "certmanager"

// CertManager is the extension `cmd/kubetower` registers. It holds nothing
// between requests.
type CertManager struct{}

// New is the constructor `cmd/kubetower` calls.
func New() CertManager { return CertManager{} }

func (CertManager) Key() string   { return Key }
func (CertManager) Label() string { return "Cert Manager" }

// Summary is the catalog entry.
func (CertManager) Summary() string {
	return "See which certificates expire in the next 30 days, which ACME orders and challenges are failing " +
		"and which issuers are broken, from cert-manager's own resources."
}

// Needs is what the cluster must run.
func (CertManager) Needs() string {
	return "cert-manager, and the right to list its Certificates, Issuers, ClusterIssuers, Orders and Challenges, " +
		"and Ingresses for their column"
}

// Legacy is empty: the key has never been anything else.
func (CertManager) Legacy() []string { return nil }

// Routes are reads only, so every one is a GET.
func (CertManager) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: overviewHandler(h)},
		// The column in the console's lists of Certificates, Secrets and
		// Ingresses: one read for the whole list.
		{Method: http.MethodGet, Pattern: "/cells", Handler: cellsHandler(h)},
		// The tab and the shortcut in the panel of one of those.
		{Method: http.MethodGet, Pattern: "/objects/{kind}/{namespace}/{name}", Handler: objectHandler(h)},
	}
}

// Suggest answers when cert-manager's Certificate CRD is served, with how many
// certificates the person may see - one bounded read, never the whole list.
func (CertManager) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		return "", err
	}
	crd, err := registry.Lookup(r.Context(), CertificatesKind)
	if err != nil {
		if absent(err) {
			return "", nil
		}
		return "", err
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: suggestLimit})
	if err != nil {
		return "", err
	}
	n := len(page.Items)
	switch {
	case n == 0:
		return "cert-manager is installed", nil
	case n >= suggestLimit:
		return fmt.Sprintf("cert-manager is installed, with at least %d certificates", n), nil
	case n == 1:
		return "cert-manager is installed, with 1 certificate", nil
	}
	return fmt.Sprintf("cert-manager is installed, with %d certificates", n), nil
}

// suggestLimit bounds the suggestion's read: it counts, it does not page.
const suggestLimit = 100

// Component names what Suggest looks for (extension.Component).
func (CertManager) Component() string { return "cert-manager" }

var _ extension.Component = CertManager{}
