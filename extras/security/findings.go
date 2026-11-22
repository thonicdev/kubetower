package security

import (
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The report kinds no workload carries: what trivy-operator found in the
// cluster's own components, its cluster-scoped objects and its nodes.
const (
	KindClusterVulnerability = "clustervulnerabilityreports"
	KindClusterConfigAudit   = "clusterconfigauditreports"
	KindInfraAssessment      = "infraassessmentreports"
	KindClusterInfra         = "clusterinfraassessmentreports"
)

// clusterKinds are the kinds `GET /cluster` reads, in the order drawn.
var clusterKinds = []string{KindClusterVulnerability, KindClusterConfigAudit, KindInfraAssessment, KindClusterInfra}

// MaxFindingRows is the most rows one answer of report tables lists, the
// worst first.
const MaxFindingRows = 500

// StateOff is a kind the cluster serves with no report at all: its scanner
// is off, since trivy-operator writes a report for every object it scans,
// a clean one included.
const StateOff = "off"

// Findings is `GET /cluster`: reports that are about no workload, each as
// its table row tells it.
type Findings struct {
	Cluster string         `json:"cluster"`
	Read    string         `json:"read"`
	Sources []Source       `json:"sources"`
	Rows    []FindingRow   `json:"rows"`
	Totals  Counts         `json:"totals"`
	Total   int            `json:"total"`
	Capped  bool           `json:"capped"`
	Notes   []string       `json:"notes"`
	ByKind  map[string]int `json:"byKind"`
}

// FindingRow is one report: what it is about, and its counts.
type FindingRow struct {
	// Source is the report kind (its plural).
	Source string `json:"source"`
	// Kind, Namespace and Name are what the report is about, as its labels
	// say; Name is the report's own where a label holds only a hash.
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Image     string `json:"image,omitempty"`
	Counts    Counts `json:"counts"`
	Scanned   string `json:"scanned,omitempty"`
}

// findings answers a route that lists some report kinds as tables, every
// namespace, the worst rows first. No body is read.
func (x Security) findings(h extension.Host, kinds []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		out, err := readFindings(r, h, cluster, kinds)
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		out.Cluster, out.Read = cluster, x.clock().UTC().Format(time.RFC3339)
		writeAnswer(w, out)
	})
}

func readFindings(r *http.Request, h extension.Host, cluster string, kinds []string) (Findings, error) {
	out := Findings{Rows: []FindingRow{}, Notes: []string{}, ByKind: map[string]int{}}
	registry, err := h.Definitions(r, cluster)
	if errors.Is(err, extension.ErrUnknownCluster) {
		return out, err
	}
	if err != nil {
		return out, catalogueError{err}
	}
	out.Sources = make([]Source, len(kinds))
	rowsBy := make([][]FindingRow, len(kinds))
	var tasks []task
	var fatal error
	var fatalMu sync.Mutex
	for i, kind := range kinds {
		src := &out.Sources[i]
		*src = Source{Kind: kind}
		crd, err := registry.Lookup(r.Context(), kind+"."+group)
		switch {
		case errors.Is(err, extension.ErrUnknownKind), errors.Is(err, extension.ErrNotServed):
			src.State = StateAbsent
			continue
		case err != nil:
			src.State, src.Note = StateUnreadable, "Resolving "+kind+" failed: "+textutil.FirstLineN(err.Error(), 200)
			continue
		}
		slot := &rowsBy[i]
		tasks = append(tasks, task{
			run: func() error {
				page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Columns: true, Limit: MaxRead})
				if err != nil {
					return err
				}
				*slot = findingRows(kind, page)
				src.State, src.Complete, src.Note, src.Reports = StateRead, page.Complete, page.Note, len(*slot)
				if len(*slot) == 0 && page.Complete {
					src.State = StateOff
				}
				return nil
			},
			fail: func(err error) {
				if errors.Is(err, extension.ErrUnknownCluster) {
					fatalMu.Lock()
					fatal = err
					fatalMu.Unlock()
				}
				classify(src, err)
			},
		})
	}
	runAll(tasks)
	if fatal != nil {
		return out, fatal
	}
	if err := r.Context().Err(); err != nil {
		return out, err
	}
	for _, rows := range rowsBy {
		for _, row := range rows {
			out.Totals.add(row.Counts)
			out.ByKind[row.Source]++
			out.Rows = append(out.Rows, row)
		}
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i].Counts, out.Rows[j].Counts
		if a.Critical != b.Critical {
			return a.Critical > b.Critical
		}
		if a.High != b.High {
			return a.High > b.High
		}
		if a.Medium != b.Medium {
			return a.Medium > b.Medium
		}
		if a.Low != b.Low {
			return a.Low > b.Low
		}
		return out.Rows[i].Name < out.Rows[j].Name
	})
	out.Total = len(out.Rows)
	if len(out.Rows) > MaxFindingRows {
		out.Rows, out.Capped = out.Rows[:MaxFindingRows:MaxFindingRows], true
	}
	return out, nil
}

// findingRows reduces a table of reports to rows: what each is about, from
// its trivy-operator labels, its image where it has one, and its counts.
func findingRows(kind string, page extension.Page) []FindingRow {
	reps := reportRows(kind, page)
	out := make([]FindingRow, 0, len(reps))
	for _, rep := range reps {
		row := FindingRow{Source: kind, Kind: rep.labels[labelKind], Namespace: rep.labels[labelNamespace],
			Name: rep.labels[labelName], Image: rep.image, Counts: rep.counts}
		if row.Name == "" || rep.labels[labelNameHash] != "" {
			row.Name = rep.name
		}
		if row.Namespace == "" {
			row.Namespace = rep.namespace
		}
		if !rep.created.IsZero() {
			row.Scanned = rep.created.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}
