package security

import (
	"errors"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Suggest is whether the cluster holds trivy-operator's vulnerability
// reports, or a policy engine's reports, that the person may read, for the
// banner that offers to turn the extra on (extension.Suggester).
//
// The kind not served is "" - nothing to suggest. Otherwise one table read of
// one row, reviewed for the person: never a report's body. A refused or failed
// read is an error, which draws no banner either.
func (Security) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		return "", err
	}
	for _, s := range []struct{ kind, evidence string }{
		{KindVulnerability + "." + group, "trivy-operator's vulnerability reports"},
		{KindPolicyReports, "policy reports"},
		{KindOpenReports, "policy reports"},
	} {
		crd, err := registry.Lookup(r.Context(), s.kind)
		if errors.Is(err, extension.ErrUnknownKind) || errors.Is(err, extension.ErrNotServed) {
			continue
		}
		if err != nil {
			return "", err
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Columns: true, Limit: 1})
		if err != nil {
			return "", err
		}
		if len(page.Rows) > 0 {
			return s.evidence, nil
		}
	}
	return "", nil
}

var _ extension.Suggester = Security{}

// Component names what Suggest looks for (extension.Component).
func (Security) Component() string { return "scanner reports" }

var _ extension.Component = Security{}
