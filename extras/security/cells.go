package security

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/extension"
)

// Cell is one cell of the "Security" column in the console's lists, the
// shape `@kubetower/ui`'s ExtensionCell declares.
type Cell struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
	Hint string `json:"hint,omitempty"`
	Href string `json:"href,omitempty"`
}

// Brief is `GET /brief/{namespace}/{name}?kind=`: one object's tile in its
// panel. Scanned false is "not scanned", never clean.
type Brief struct {
	Scanned  bool         `json:"scanned"`
	Workload *WorkloadRef `json:"workload,omitempty"`
	Counts   *Counts      `json:"counts,omitempty"`
	Image    string       `json:"image,omitempty"`
	Href     string       `json:"href,omitempty"`
	// Signature is the admission's verdict on the workload's images, absent
	// where no pod of it carries one.
	Signature *Signature `json:"signature,omitempty"`
	// Policies is the workload's non-passing policy results, counted; absent
	// where no policy source is served.
	Policies *PolicyCounts `json:"policies,omitempty"`
}

// detailHref is the page of one workload's findings, as the console's page
// routes it: x/security/:namespace/:kind/:name.
func detailHref(cluster string, w WorkloadRef) string {
	return "/c/" + url.PathEscape(cluster) + "/x/" + Key + "/" + url.PathEscape(w.Namespace) + "/" +
		url.PathEscape(w.Kind) + "/" + url.PathEscape(w.Name)
}

// cellOf is a scanned result's cell: its worst severity's count, or clean.
func cellOf(cluster string, link WorkloadRef, res *result) Cell {
	c := Cell{Text: "clean", Tone: "ok", Href: detailHref(cluster, link)}
	switch {
	case res.counts.Critical > 0:
		c.Text, c.Tone = strconv.Itoa(res.counts.Critical)+" critical", "bad"
	case res.counts.High > 0:
		c.Text, c.Tone = strconv.Itoa(res.counts.High)+" high", "warn"
	}
	k := res.counts
	c.Hint = "Critical " + strconv.Itoa(k.Critical) + " · High " + strconv.Itoa(k.High) + " · Medium " +
		strconv.Itoa(k.Medium) + " · Low " + strconv.Itoa(k.Low) + " · Unknown " + strconv.Itoa(k.Unknown)
	if len(res.images) > 0 {
		c.Hint += " · " + strings.Join(res.images, ", ")
	}
	return c
}

// listKind is `?kind=`, checked: the Kubernetes kind it names.
func listKind(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	plural := r.URL.Query().Get("kind")
	kind, ok := workloadKinds[plural]
	if !ok {
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, "The Security column is drawn on pods and on the "+
			"workloads that run them, not on "+quote(plural)+".", nil)
	}
	return plural, kind, ok
}

// vulnerabilitiesUnread answers a request whose vulnerability reports could
// not be read: refused (403, 503) or failed (502). Absent is not a failure:
// there is nothing to draw. It reports whether it wrote.
func vulnerabilitiesUnread(w http.ResponseWriter, cluster string, s *snapshot) bool {
	src := s.source(KindVulnerability)
	switch src.State {
	case StateDenied:
		if src.Note == extension.UnreviewedText {
			writeFailure(w, cluster, extension.ErrUnreviewable)
		} else {
			writeFailure(w, cluster, extension.ErrDenied)
		}
		return true
	case StateUnreadable:
		writeFailure(w, cluster, errors.New(src.Note))
		return true
	}
	return false
}

// resolve is the result an object of a list stands for, and the workload its
// link leads to. A pod stands for its workload; a ReplicaSet or a Job for its
// own reports, linked to its Deployment or CronJob; any other workload for
// itself.
func resolve(l *linked, namespace, kind, name string) (*result, WorkloadRef) {
	ref := WorkloadRef{Namespace: namespace, Kind: kind, Name: name}
	switch kind {
	case "Pod":
		if w, ok := l.ix.pods[namespace+"/"+name]; ok {
			return l.workloads[w.key()], w
		}
		return l.workloads[ref.key()], ref
	case "ReplicaSet", "Job":
		target := ref
		if parent, ok := l.ix.parentOf(namespace, kind, name); ok {
			target = parent
		}
		return l.objects[ref.key()], target
	}
	return l.workloads[ref.key()], ref
}

// cells answers `GET /cells?kind=`: for every object of that list that a
// vulnerability report covers, its cell, keyed `namespace/name`. An object
// with no report has no cell, which the console draws empty: not scanned,
// never clean.
func (Security) cells(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		_, kind, ok := listKind(w, r)
		if !ok {
			return
		}
		s, err := readSnapshot(r, h, cluster, "", wants{pods: kind == "Pod", owners: true})
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		out := map[string]Cell{}
		if !s.operator {
			writeAnswer(w, out)
			return
		}
		if vulnerabilitiesUnread(w, cluster, s) {
			return
		}
		l := link(s)
		switch kind {
		case "Pod":
			for _, p := range s.pods {
				if res, target := resolve(l, p.namespace, kind, p.name); res != nil && res.hasVuln {
					out[p.namespace+"/"+p.name] = cellOf(cluster, target, res)
				}
			}
		case "ReplicaSet", "Job":
			for _, res := range l.objects {
				if res.ref.Kind != kind || !res.hasVuln {
					continue
				}
				_, target := resolve(l, res.ref.Namespace, kind, res.ref.Name)
				out[res.ref.Namespace+"/"+res.ref.Name] = cellOf(cluster, target, res)
			}
		default:
			for _, res := range l.workloads {
				if res.ref.Kind == kind && res.hasVuln {
					out[res.ref.Namespace+"/"+res.ref.Name] = cellOf(cluster, res.ref, res)
				}
			}
		}
		writeAnswer(w, out)
	})
}

// brief answers `GET /brief/{namespace}/{name}?kind=`: one object's counts
// and its link, read in its namespace alone.
func (Security) brief(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		_, kind, ok := listKind(w, r)
		if !ok {
			return
		}
		if !validNamespace(namespace) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(namespace)+" is not a namespace name.", nil)
			return
		}
		if !validName(name) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(name)+" is not a name.", nil)
			return
		}
		// The pods are read in the namespace alone, as a table, for the
		// admission's annotation as well as a pod's workload.
		s, err := readSnapshot(r, h, cluster, namespace, wants{pods: true, owners: true})
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		out := Brief{}
		var ix *index
		if s.operator {
			if vulnerabilitiesUnread(w, cluster, s) {
				return
			}
			l := link(s)
			res, target := resolve(l, namespace, kind, name)
			out.Workload, ix = &target, l.ix
			if res != nil && res.hasVuln {
				counts := res.counts
				out.Scanned, out.Counts, out.Image, out.Href = true, &counts, strings.Join(res.images, ", "), detailHref(cluster, target)
			}
		}
		pol, err := workloadPolicies(r, h, cluster, WorkloadRef{Namespace: namespace, Kind: kind, Name: name})
		if errors.Is(err, extension.ErrUnknownCluster) {
			writeFailure(w, cluster, err)
			return
		}
		if err == nil && pol != nil {
			counts := pol.Counts
			out.Policies = &counts
		}
		if ix != nil {
			out.Signature = signatures(s, ix, signedOf(pol))[out.Workload.key()]
		}
		writeAnswer(w, out)
	})
}
