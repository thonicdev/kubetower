package security

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
)

// Overview is `GET /`: what trivy-operator found, by namespace, by image and
// by workload, the worst first.
type Overview struct {
	Cluster string `json:"cluster"`
	Read    string `json:"read"`
	// Operator is false when none of trivy-operator's report kinds is served:
	// the page then says one thing, that it is absent.
	Operator bool     `json:"operator"`
	Sources  []Source `json:"sources"`
	// Sbom is trivy-operator's SBOM reports, read as a table; Signatures the
	// admission's annotation on the pods and the image verifications of the
	// policy reports (sbom.go, signature.go).
	Sbom       Source `json:"sbom"`
	Signatures Source `json:"signatures"`
	// SignatureFailed is how many workloads have an image whose verification
	// failed; Verified how many have every image passed.
	SignatureFailed int `json:"signatureFailed"`
	Verified        int `json:"verified"`
	// Totals counts each CVE once per distinct image (repository:tag).
	Totals       Counts         `json:"totals"`
	Affected     Affected       `json:"affected"`
	ImageCount   int            `json:"imageCount"`
	ExposedCount int            `json:"exposedCount"`
	SecretCount  int            `json:"secretCount"`
	Namespaces   []NamespaceRow `json:"namespaces"`
	Images       []ImageRow     `json:"images"`
	Workloads    []WorkloadRow  `json:"workloads"`
	// Total is how many workloads there were before MaxWorkloads; Capped says
	// the list was cut.
	Total  int  `json:"total"`
	Capped bool `json:"capped"`
	// Notes are the reads beside the reports that were refused, failed or
	// narrowed - pods, ReplicaSets, Jobs, Services, Ingresses - and so what
	// the linkage or the exposure could not take into account.
	Notes []string `json:"notes"`
}

// Affected is how many workloads carry at least one finding of each severity.
type Affected struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
}

// NamespaceRow is one namespace's bar: its distinct images' counts.
type NamespaceRow struct {
	Namespace string `json:"namespace"`
	Counts    Counts `json:"counts"`
	Workloads int    `json:"workloads"`
}

// ImageRow is one image of the top, and how many workloads run it.
type ImageRow struct {
	Image     string `json:"image"`
	Counts    Counts `json:"counts"`
	Workloads int    `json:"workloads"`
}

// WorkloadRow is one workload of the table. Scanned is absent for a workload
// no vulnerability report covers: not scanned, never clean. Config and
// Secrets are null where no report of that kind covers it.
type WorkloadRow struct {
	WorkloadRef
	Images   []string `json:"images"`
	Counts   Counts   `json:"counts"`
	Config   *Counts  `json:"config"`
	Secrets  *int     `json:"secrets"`
	Exposed  bool     `json:"exposed"`
	Exposure string   `json:"exposure,omitempty"`
	Scanned  string   `json:"scanned,omitempty"`
	// Components is null where no SBOM report covers it; Signature null where
	// no pod of it carries the admission's verdict: unknown, never unsigned.
	Components *Components `json:"components"`
	Signature  *Signature  `json:"signature"`
}

func validNamespace(ns string) bool { return len(validation.IsDNS1123Label(ns)) == 0 }

func (x Security) overview(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		ns := strings.TrimSpace(r.URL.Query().Get("ns"))
		if ns != "" && !validNamespace(ns) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ns)+" is not a namespace name.", nil)
			return
		}
		s, err := readSnapshot(r, h, cluster, ns, wants{pods: true, owners: true, exposure: true, sbom: true, signed: true})
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		writeAnswer(w, buildOverview(cluster, x.clock(), s))
	})
}

// buildOverview folds a snapshot into the page's answer.
func buildOverview(cluster string, now time.Time, s *snapshot) Overview {
	out := Overview{Cluster: cluster, Read: now.UTC().Format(time.RFC3339), Operator: s.operator, Sources: s.sources,
		Namespaces: []NamespaceRow{}, Images: []ImageRow{}, Workloads: []WorkloadRow{}, Notes: s.notes,
		Sbom: s.sbom, Signatures: signatureSource(s)}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if out.Sbom.State == "" {
		out.Sbom = Source{Kind: KindSbom, State: StateAbsent}
	}
	if !s.operator {
		return out
	}
	l := link(s)
	// Every workload the pods name, scanned or not, joins those the reports
	// name: a workload with no report is listed as not scanned.
	for _, w := range l.ix.pods {
		if l.workloads[w.key()] == nil {
			l.workloads[w.key()] = &result{ref: w}
		}
	}
	exposure := l.expose(s, nil)
	comps, sigs := components(s, l.ix), signatures(s, l.ix, s.signed)

	type imageFact struct {
		counts    Counts
		created   time.Time
		workloads int
	}
	images := map[string]*imageFact{}
	nsImages := map[string]map[string]bool{}
	nsWorkloads := map[string]int{}
	rows := make([]WorkloadRow, 0, len(l.workloads))
	for _, res := range l.workloads {
		row := WorkloadRow{WorkloadRef: res.ref, Images: res.images, Counts: res.counts,
			Components: comps[res.ref.key()], Signature: sigs[res.ref.key()]}
		if sig := row.Signature; sig != nil {
			switch sig.Verdict {
			case VerdictFail:
				out.SignatureFailed++
			case VerdictPass:
				out.Verified++
			}
		}
		if row.Images == nil {
			row.Images = []string{}
		}
		if res.hasConf {
			c := res.configCounts
			row.Config = &c
		}
		if res.hasSecr {
			n := res.secrets
			row.Secrets = &n
			if n > 0 {
				out.SecretCount++
			}
		}
		if via := exposure[res.ref.key()]; len(via) > 0 {
			row.Exposed, row.Exposure = true, via[0]
		}
		if res.hasVuln {
			row.Scanned = res.scanned.UTC().Format(time.RFC3339)
			nsWorkloads[res.ref.Namespace]++
			if res.counts.Critical > 0 {
				out.Affected.Critical++
			}
			if res.counts.High > 0 {
				out.Affected.High++
			}
			if res.counts.Medium > 0 {
				out.Affected.Medium++
			}
			if row.Exposed && res.counts.serious() {
				out.ExposedCount++
			}
			for _, rep := range res.vuln.reports {
				if rep.image == "" {
					continue
				}
				f := images[rep.image]
				if f == nil {
					f = &imageFact{}
					images[rep.image] = f
				}
				if f.created.IsZero() || rep.created.After(f.created) {
					f.counts, f.created = rep.counts, rep.created
				}
				if nsImages[res.ref.Namespace] == nil {
					nsImages[res.ref.Namespace] = map[string]bool{}
				}
				nsImages[res.ref.Namespace][rep.image] = true
			}
			for _, image := range res.images {
				images[image].workloads++
			}
		}
		rows = append(rows, row)
	}
	sortWorkloads(rows)
	out.Total = len(rows)
	if len(rows) > MaxWorkloads {
		rows, out.Capped = rows[:MaxWorkloads], true
	}
	out.Workloads = rows

	for name, f := range images {
		out.Totals.add(f.counts)
		if f.counts.serious() {
			out.ImageCount++
		}
		out.Images = append(out.Images, ImageRow{Image: name, Counts: f.counts, Workloads: f.workloads})
	}
	sort.Slice(out.Images, func(i, j int) bool {
		if w, ok := worse(out.Images[i].Counts, out.Images[j].Counts); ok {
			return w
		}
		return out.Images[i].Image < out.Images[j].Image
	})
	if len(out.Images) > MaxImages {
		out.Images = out.Images[:MaxImages]
	}
	for ns, set := range nsImages {
		row := NamespaceRow{Namespace: ns, Workloads: nsWorkloads[ns]}
		for image := range set {
			row.Counts.add(images[image].counts)
		}
		out.Namespaces = append(out.Namespaces, row)
	}
	sort.Slice(out.Namespaces, func(i, j int) bool {
		if w, ok := worse(out.Namespaces[i].Counts, out.Namespaces[j].Counts); ok {
			return w
		}
		return out.Namespaces[i].Namespace < out.Namespaces[j].Namespace
	})
	if len(out.Namespaces) > MaxNamespaces {
		out.Namespaces = out.Namespaces[:MaxNamespaces]
	}
	return out
}

// sortWorkloads puts the worst first, then the scanned before the not
// scanned, then by namespace, kind and name.
func sortWorkloads(rows []WorkloadRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := &rows[i], &rows[j]
		if w, ok := worse(a.Counts, b.Counts); ok {
			return w
		}
		if (a.Scanned != "") != (b.Scanned != "") {
			return a.Scanned != ""
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
}
