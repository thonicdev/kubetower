package cnpg

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// suggestLimit is how many Clusters the suggestion counts before it says "at
// least": it runs whenever a cluster's pages are opened, so it stays cheap.
const suggestLimit = 200

// Suggest is whether the cluster runs CloudNativePG clusters the person may
// read, for the banner that offers to turn the extra on
// (extension.Suggester). The kind not served is "" - nothing to suggest; a
// refused or failed read is an error, which draws no banner either, and the
// log says which.
func (CNPG) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		return "", err
	}
	crd, err := registry.Lookup(r.Context(), ClusterKind)
	if errors.Is(err, extension.ErrUnknownKind) || errors.Is(err, extension.ErrNotServed) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: suggestLimit})
	if err != nil {
		return "", err
	}
	if len(page.Items) == 0 {
		return "", nil
	}
	namespaces := map[string]bool{}
	for i := range page.Items {
		namespaces[page.Items[i].GetNamespace()] = true
	}
	at := ""
	if !page.Complete {
		at = "At least "
	}
	return fmt.Sprintf("%s%d CloudNativePG %s in %d %s", at, len(page.Items), plural(len(page.Items), "cluster"),
		len(namespaces), plural(len(namespaces), "namespace")), nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func isUnknownCluster(err error) bool { return errors.Is(err, extension.ErrUnknownCluster) }

var _ extension.Suggester = CNPG{}

// Component names what Suggest looks for (extension.Component).
func (CNPG) Component() string { return "CloudNativePG" }

var _ extension.Component = CNPG{}
