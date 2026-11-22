package security

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The RBAC assessment kinds, read as tables like the cluster's reports.
const (
	KindRBAC        = "rbacassessmentreports"
	KindClusterRBAC = "clusterrbacassessmentreports"
	KindCompliance  = "clustercompliancereports"
)

// rbacKinds are the kinds `GET /rbac` reads.
var rbacKinds = []string{KindRBAC, KindClusterRBAC}

const (
	// MaxComplianceReports is the most compliance reports read: one per
	// standard, a handful on a cluster.
	MaxComplianceReports = 50
	// MaxControls is the most controls one report lists.
	MaxControls = 500
)

// Compliance is `GET /compliance`: each compliance report's controls,
// passed or failed, from its summary; never the detail of each failure.
type Compliance struct {
	Cluster string             `json:"cluster"`
	Read    string             `json:"read"`
	Source  Source             `json:"source"`
	Reports []ComplianceReport `json:"reports"`
}

// ComplianceReport is one standard, as its report summarizes it.
type ComplianceReport struct {
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Version  string    `json:"version,omitempty"`
	Pass     int       `json:"pass"`
	Fail     int       `json:"fail"`
	Updated  string    `json:"updated,omitempty"`
	Controls []Control `json:"controls"`
	// ControlsTotal is how many controls the report held; Controls is cut
	// at MaxControls, the failed first.
	ControlsTotal int `json:"controlsTotal"`
}

// Control is one control: its failures, or null where the report gives no
// count (a manual control).
type Control struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Fail     *int   `json:"fail"`
}

func (x Security) compliance(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		out := Compliance{Cluster: cluster, Read: x.clock().UTC().Format(time.RFC3339), Source: Source{Kind: KindCompliance},
			Reports: []ComplianceReport{}}
		registry, err := h.Definitions(r, cluster)
		if err != nil {
			if !errors.Is(err, extension.ErrUnknownCluster) {
				err = catalogueError{err}
			}
			writeFailure(w, cluster, err)
			return
		}
		crd, err := registry.Lookup(r.Context(), KindCompliance+"."+group)
		switch {
		case errors.Is(err, extension.ErrUnknownKind), errors.Is(err, extension.ErrNotServed):
			out.Source.State = StateAbsent
			writeAnswer(w, out)
			return
		case err != nil:
			out.Source.State, out.Source.Note = StateUnreadable, "Resolving "+KindCompliance+" failed: "+textutil.FirstLineN(err.Error(), 200)
			writeAnswer(w, out)
			return
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: MaxComplianceReports})
		if errors.Is(err, extension.ErrUnknownCluster) {
			writeFailure(w, cluster, err)
			return
		}
		if err != nil {
			classify(&out.Source, err)
			writeAnswer(w, out)
			return
		}
		for i := range page.Items {
			out.Reports = append(out.Reports, complianceOf(&page.Items[i]))
			page.Items[i].Object = nil
		}
		sort.Slice(out.Reports, func(i, j int) bool { return out.Reports[i].Title < out.Reports[j].Title })
		out.Source.State, out.Source.Complete, out.Source.Note, out.Source.Reports = StateRead, page.Complete, page.Note, len(out.Reports)
		if len(out.Reports) == 0 && page.Complete {
			out.Source.State = StateOff
		}
		writeAnswer(w, out)
	})
}

// complianceOf keeps a report's title, its counts and its controls: the
// names from the spec, the failures from the summary. The detail report,
// which can carry every failed object, is never kept.
func complianceOf(u *unstructured.Unstructured) ComplianceReport {
	rep := ComplianceReport{Name: u.GetName(), Controls: []Control{}}
	spec, _ := u.Object["spec"].(map[string]any)
	status, _ := u.Object["status"].(map[string]any)
	rep.Title = clipTitle(str(spec, "compliance", "title"))
	if rep.Title == "" {
		rep.Title = rep.Name
	}
	rep.Version = str(spec, "compliance", "version")
	rep.Pass, rep.Fail = num(status, "summary", "passCount"), num(status, "summary", "failCount")
	rep.Updated = str(status, "updateTimestamp")

	failed := map[string]int{}
	for _, v := range list(status, "summaryReport", "controlCheck") {
		m, _ := v.(map[string]any)
		if _, ok := m["totalFail"]; ok {
			failed[str(m, "id")] = num(m, "totalFail")
		}
	}
	for _, v := range list(spec, "compliance", "controls") {
		m, _ := v.(map[string]any)
		c := Control{ID: str(m, "id"), Name: clipTitle(str(m, "name")), Severity: severity(str(m, "severity"))}
		if n, ok := failed[c.ID]; ok {
			c.Fail = &n
		}
		rep.Controls = append(rep.Controls, c)
	}
	sort.SliceStable(rep.Controls, func(i, j int) bool {
		a, b := rep.Controls[i], rep.Controls[j]
		fa, fb := a.Fail != nil && *a.Fail > 0, b.Fail != nil && *b.Fail > 0
		if fa != fb {
			return fa
		}
		if severities[a.Severity] != severities[b.Severity] {
			return severities[a.Severity] < severities[b.Severity]
		}
		return a.ID < b.ID
	})
	rep.ControlsTotal = len(rep.Controls)
	if len(rep.Controls) > MaxControls {
		rep.Controls = rep.Controls[:MaxControls:MaxControls]
	}
	return rep
}
