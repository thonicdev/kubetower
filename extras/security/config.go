package security

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/extension"
)

// MaxConfigRows is the most workloads one `GET /config` answers: a page of
// the workload table.
const MaxConfigRows = 50

// config answers `GET /config?w=<namespace>/<Kind>/<name>` (repeated, at most
// MaxConfigRows): each workload's failed configuration checks, each check ID
// counted once, as the detail lists them. A report's own summary counts a
// check once per place it failed, so the overview's table read cannot give
// this figure; the bodies are read here, for the rows on screen only, one
// namespace at a time. A workload with no config audit report, or whose
// reports could not be read, is null.
func (Security) config(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		refs, msg := configRefs(r.URL.Query()["w"])
		if msg != "" {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, msg, nil)
			return
		}
		byNamespace := map[string][]WorkloadRef{}
		var namespaces []string
		for _, ref := range refs {
			if _, ok := byNamespace[ref.Namespace]; !ok {
				namespaces = append(namespaces, ref.Namespace)
			}
			byNamespace[ref.Namespace] = append(byNamespace[ref.Namespace], ref)
		}
		sort.Strings(namespaces)
		out := make(map[string]*Counts, len(refs))
		for _, ref := range refs {
			out[ref.key()] = nil
		}
		for _, ns := range namespaces {
			if err := r.Context().Err(); err != nil {
				return
			}
			if err := configOf(r, h, cluster, ns, byNamespace[ns], out); err != nil {
				writeFailure(w, cluster, err)
				return
			}
		}
		writeAnswer(w, out)
	})
}

// configRefs parses the `w` parameters, or says what is wrong with them.
func configRefs(values []string) ([]WorkloadRef, string) {
	if len(values) == 0 {
		return nil, "Name at least one workload, as w=<namespace>/<Kind>/<name>."
	}
	seen := map[string]bool{}
	var refs []WorkloadRef
	for _, v := range values {
		parts := strings.Split(v, "/")
		if len(parts) != 3 {
			return nil, quote(v) + " is not <namespace>/<Kind>/<name>."
		}
		ref := WorkloadRef{Namespace: parts[0], Kind: parts[1], Name: parts[2]}
		switch {
		case !validNamespace(ref.Namespace):
			return nil, quote(ref.Namespace) + " is not a namespace name."
		case !knownKind(ref.Kind):
			return nil, quote(ref.Kind) + " is not a kind of workload."
		case !validName(ref.Name):
			return nil, quote(ref.Name) + " is not a workload name."
		}
		if seen[ref.key()] {
			continue
		}
		seen[ref.key()] = true
		refs = append(refs, ref)
	}
	if len(refs) > MaxConfigRows {
		return nil, "At most " + strconv.Itoa(MaxConfigRows) + " workloads at once."
	}
	return refs, ""
}

// configOf fills out for the workloads of one namespace. Only an unknown
// cluster or an unreadable catalog is the request's error; anything else
// leaves the workload null.
func configOf(r *http.Request, h extension.Host, cluster, namespace string, refs []WorkloadRef, out map[string]*Counts) error {
	pods := false
	for _, ref := range refs {
		pods = pods || ref.Kind == "Pod"
	}
	s, err := readSnapshot(r, h, cluster, namespace, wants{pods: pods, owners: true})
	if err != nil {
		return err
	}
	if src := s.source(KindConfigAudit); !s.operator || src == nil || src.State != StateRead {
		return nil
	}
	crd := s.crds[KindConfigAudit]
	l := link(s)
	for _, ref := range refs {
		res, _ := resolve(l, ref.Namespace, ref.Kind, ref.Name)
		if res == nil || !res.hasConf {
			continue
		}
		items, err := bodies(r, h, cluster, crd, res.config.reports)
		if errors.Is(err, extension.ErrUnknownCluster) {
			return err
		}
		if err != nil {
			continue
		}
		c := checkCounts(distinctChecks(items))
		out[ref.key()] = &c
	}
	return nil
}
