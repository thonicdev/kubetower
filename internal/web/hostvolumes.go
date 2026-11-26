package web

import (
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// Host.VolumeFill: each claim's fill out of the cluster's Prometheus (#91).
//
// The review comes first and decides what is asked: the claims in namespaces
// the person may not list claims in are dropped before a query is built, so
// their names never reach the backend on that person's behalf.

// claimsQuestion is the review VolumeFill asks, per namespace.
func claimsQuestion(namespace string) access.Attributes {
	return access.Attributes{Verb: "list", Version: "v1", Resource: "persistentvolumeclaims", Namespace: namespace}
}

// engineOf says what answers the cluster's Prometheus API, when the series
// source keeps a discovery: the console's own (metrics.Metrics) does.
type engineOf interface {
	Cached(name string) *metrics.Discovery
}

// VolumeFill answers extension.Host.VolumeFill.
func (h host) VolumeFill(r *http.Request, name string, claims []extension.ClaimRef) (extension.VolumeFill, error) {
	client, err := h.client(r, name)
	if err != nil {
		return extension.VolumeFill{}, err
	}
	keys := make([]string, 0, len(claims))
	seen := map[string]bool{}
	for _, c := range claims {
		key := c.Namespace + "/" + c.Name
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	kept, visible, err := access.Keep(r.Context(), h.s.gate, client.Context, claimsQuestion(""), keys, namespaceOfKey)
	if err != nil {
		return extension.VolumeFill{}, err
	}
	// Narrowed to nothing is the person refused everywhere asked: said as the
	// gate's own refusal of the first namespace, never an empty read.
	if len(keys) > 0 && len(kept) == 0 {
		_, _, err := access.Keep[string](r.Context(), h.s.gate, client.Context, claimsQuestion(namespaceOfKey(&keys[0])), nil, nil)
		if err == nil {
			err = fmt.Errorf("%w: you may not list %s in any namespace asked", extension.ErrDenied,
				qualifiedResource(claimsQuestion("")))
		}
		return extension.VolumeFill{}, err
	}
	asked := make([]metrics.ClaimKey, 0, len(kept))
	for _, key := range kept {
		for _, c := range claims {
			if c.Namespace+"/"+c.Name == key {
				asked = append(asked, metrics.ClaimKey{Namespace: c.Namespace, Name: c.Name})
				break
			}
		}
	}

	var q metrics.RangeQuerier
	if h.s.series != nil {
		q = h.s.series
	}
	read, err := metrics.ReadVolumeFill(r.Context(), q, client.Context, asked)
	if err != nil {
		return extension.VolumeFill{}, err
	}
	out := extension.VolumeFill{State: extension.WindowState(read.State), Detail: read.Detail,
		Complete: !visible.Narrowed, ReadAt: read.ReadAt}
	if visible.Narrowed {
		out.Note = "You may not list " + qualifiedResource(claimsQuestion("")) +
			" in every namespace asked, so only the claims in namespaces where you may were read."
	}
	if found, ok := h.s.series.(engineOf); ok && read.State == metrics.WindowRead {
		if d := found.Cached(client.Context); d != nil && d.Kind == metrics.KindPrometheus {
			out.Engine = d.Engine
			if out.Engine == "" {
				out.Engine = extension.EnginePrometheus
			}
		}
	}
	out.Claims = make([]extension.ClaimFill, len(read.Claims))
	for i, c := range read.Claims {
		out.Claims[i] = extension.ClaimFill{ClaimRef: extension.ClaimRef{Namespace: c.Namespace, Name: c.Name},
			UsedBytes: c.Used, CapacityBytes: c.Capacity}
	}
	return out, nil
}
