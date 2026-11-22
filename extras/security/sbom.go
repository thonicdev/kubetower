package security

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The SBOMs trivy-operator writes, one SbomReport per container, with the same
// labels and owner reference as its other reports. A report carries the whole
// CycloneDX document, about 640 bytes a component: the overview reads them as
// the API server's table (the Components and Dependencies columns), and only
// one workload's page reads their bodies, summarized and capped here.
const KindSbom = "sbomreports"

const (
	// MaxComponents is the most components one workload's SBOM lists.
	MaxComponents = 5000
	// MaxLicenses is the most licenses one answer counts by name.
	MaxLicenses = 50
	// MaxPurl is the most characters of a package URL sent.
	MaxPurl = 256
	// maxField clips a component's name, version, type or license.
	maxField = 256
)

// sbomRow is one SbomReport as its table row tells it.
type sbomRow struct {
	reportRow
	components, dependencies int
}

// sbomTask is the read of the SBOM reports as a table, when the cluster
// serves them; their source's state otherwise.
func sbomTask(r *http.Request, h extension.Host, cluster, namespace string, registry extension.CRDRegistry,
	s *snapshot, setFatal func(error)) []task {
	s.sbom = Source{Kind: KindSbom}
	crd, err := registry.Lookup(r.Context(), KindSbom+"."+group)
	switch {
	case errors.Is(err, extension.ErrUnknownKind), errors.Is(err, extension.ErrNotServed):
		s.sbom.State = StateAbsent
		return nil
	case err != nil:
		s.sbom.State, s.sbom.Note = StateUnreadable, "Resolving "+KindSbom+" failed: "+textutil.FirstLineN(err.Error(), 200)
		return nil
	}
	s.sbomCRD = &crd
	return []task{{
		run: func() error {
			page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: namespace, Columns: true, Limit: MaxRead})
			if err != nil {
				return err
			}
			s.sbomRows = sbomRows(page)
			s.sbom.State, s.sbom.Complete, s.sbom.Note, s.sbom.Reports = StateRead, page.Complete, page.Note, len(s.sbomRows)
			if len(s.sbomRows) == 0 && page.Complete {
				// Served and empty: trivy-operator's SBOM generation is off.
				s.sbom.State = StateOff
			}
			return nil
		},
		fail: func(err error) {
			if errors.Is(err, extension.ErrUnknownCluster) {
				setFatal(err)
			}
			classify(&s.sbom, err)
		},
	}}
}

func sbomRows(page extension.Page) []sbomRow {
	base := reportRows(KindSbom, page)
	comps, deps := column(page.Columns, "Components"), column(page.Columns, "Dependencies")
	out := make([]sbomRow, len(base))
	for i := range base {
		out[i] = sbomRow{reportRow: base[i], components: listed(cellInt(page.Rows[i].Cells, comps)),
			dependencies: cellInt(page.Rows[i].Cells, deps)}
	}
	return out
}

// listed is the components an SbomReport lists, from its Components column:
// trivy-operator (v0.36.1) counts there the image itself, the CycloneDX
// metadata.component, beside the components it lists, so a workload's page
// that counts what it lists says one fewer.
func listed(column int) int {
	if column > 0 {
		return column - 1
	}
	return 0
}

// Components is a workload's SBOM as the table tells it: its images'
// components, each image counted once, and each container's.
type Components struct {
	Total        int               `json:"total"`
	Dependencies int               `json:"dependencies"`
	Images       []ImageComponents `json:"images"`
}

// ImageComponents is one container's SBOM.
type ImageComponents struct {
	Container    string `json:"container"`
	Image        string `json:"image"`
	Components   int    `json:"components"`
	Dependencies int    `json:"dependencies"`
}

// sbomChoice is one workload's SBOM reports: those of the object of the
// highest rank, as link.go chooses the other reports.
type sbomChoice struct {
	rank int64
	rows []*sbomRow
}

// chooseSboms ties every SBOM report to its workload.
func chooseSboms(ix *index, rows []sbomRow) map[string]*sbomChoice {
	out := map[string]*sbomChoice{}
	for i := range rows {
		row := &rows[i]
		workload, _, rank, ok := ix.originOf(&row.reportRow)
		if !ok {
			continue
		}
		c := out[workload.key()]
		switch {
		case c == nil:
			out[workload.key()] = &sbomChoice{rank: rank, rows: []*sbomRow{row}}
		case rank > c.rank:
			c.rank, c.rows = rank, []*sbomRow{row}
		case rank == c.rank:
			c.rows = append(c.rows, row)
		}
	}
	return out
}

// components is every workload's Components.
func components(s *snapshot, ix *index) map[string]*Components {
	out := map[string]*Components{}
	for key, c := range chooseSboms(ix, s.sbomRows) {
		comp := &Components{Images: make([]ImageComponents, 0, len(c.rows))}
		seen := map[string]bool{}
		for _, row := range c.rows {
			comp.Images = append(comp.Images, ImageComponents{Container: row.labels[labelContainer], Image: row.image,
				Components: row.components, Dependencies: row.dependencies})
			image := row.image
			if image == "" {
				image = "\x00" + row.name
			}
			if seen[image] {
				continue
			}
			seen[image] = true
			comp.Total += row.components
			comp.Dependencies += row.dependencies
		}
		sort.Slice(comp.Images, func(i, j int) bool {
			if comp.Images[i].Container != comp.Images[j].Container {
				return comp.Images[i].Container < comp.Images[j].Container
			}
			return comp.Images[i].Image < comp.Images[j].Image
		})
		out[key] = comp
	}
	return out
}

// SBOM is `GET /workload/{namespace}/{kind}/{name}/sbom`: one workload's
// components, summarized from its SbomReports' bodies. Never a hash, a
// property or the dependency graph.
type SBOM struct {
	Cluster  string      `json:"cluster"`
	Read     string      `json:"read"`
	Workload WorkloadRef `json:"workload"`
	Source   Source      `json:"source"`
	Images   []SbomImage `json:"images"`
	// Components are at most MaxComponents, by name; Total is how many the
	// reports held.
	Components []Component `json:"components"`
	Total      int         `json:"total"`
	// Types and Licenses count every component, the most frequent first;
	// Licenses holds at most MaxLicenses names.
	Types    []Tally  `json:"types"`
	Licenses []Tally  `json:"licenses"`
	Notes    []string `json:"notes"`
}

// SbomImage is one container's image, as its SbomReport names it. Absent is
// a container trivy-operator scanned for vulnerabilities but wrote no
// SbomReport for - an image in which it found no component: its counts are
// unknown, not zero.
type SbomImage struct {
	Container    string `json:"container"`
	Image        string `json:"image"`
	Digest       string `json:"digest,omitempty"`
	Components   int    `json:"components"`
	Dependencies int    `json:"dependencies"`
	Absent       bool   `json:"absent,omitempty"`
}

// Component is one component, as the SBOM lists it.
type Component struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Type      string `json:"type"`
	License   string `json:"license"`
	Purl      string `json:"purl,omitempty"`
	Container string `json:"container"`
}

// Tally is one name and how many components carry it; an empty name is a
// component that names none.
type Tally struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (x Security) sbom(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		ref := WorkloadRef{Namespace: r.PathValue("namespace"), Kind: r.PathValue("kind"), Name: r.PathValue("name")}
		if !validRef(w, ref) {
			return
		}
		s, err := readSnapshot(r, h, cluster, ref.Namespace, wants{pods: true, owners: true, sbom: true})
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		out := SBOM{Cluster: cluster, Read: x.clock().UTC().Format(time.RFC3339), Workload: ref, Source: s.sbom,
			Images: []SbomImage{}, Components: []Component{}, Types: []Tally{}, Licenses: []Tally{}, Notes: s.notes}
		if out.Source.State == "" {
			out.Source = Source{Kind: KindSbom, State: StateAbsent}
		}
		if out.Notes == nil {
			out.Notes = []string{}
		}
		if out.Source.State == StateRead && s.sbomCRD != nil {
			ix := newIndex(s)
			if c := chooseSboms(ix, s.sbomRows)[ref.key()]; c != nil {
				rows := make([]*reportRow, len(c.rows))
				for i, row := range c.rows {
					rows[i] = &row.reportRow
				}
				items, err := bodies(r, h, cluster, *s.sbomCRD, rows)
				switch {
				case errors.Is(err, extension.ErrUnknownCluster):
					writeFailure(w, cluster, err)
					return
				case err != nil:
					classify(&out.Source, err)
				default:
					summariseSbom(items, &out)
				}
			}
			if out.Source.State == StateRead {
				absentImages(s, ix, ref, &out)
			}
		}
		writeAnswer(w, out)
	})
}

// absentImages adds the workload's containers that have a vulnerability report
// and no SbomReport: trivy-operator writes none for an image in which it
// found no component, and that is not an SBOM of zero components.
func absentImages(s *snapshot, ix *index, ref WorkloadRef, out *SBOM) {
	var vuln chosen
	rows := s.reports[KindVulnerability]
	for i := range rows {
		if workload, scanned, rank, ok := ix.originOf(&rows[i]); ok && workload == ref {
			vuln.offer(rank, scanned, &rows[i])
		}
	}
	have := make(map[string]bool, len(out.Images))
	for _, img := range out.Images {
		have[img.Container] = true
	}
	added := false
	for _, rep := range vuln.reports {
		container := rep.labels[labelContainer]
		if have[container] {
			continue
		}
		have[container] = true
		out.Images = append(out.Images, SbomImage{Container: container, Image: rep.image, Absent: true})
		added = true
	}
	if added {
		sort.Slice(out.Images, func(i, j int) bool { return out.Images[i].Container < out.Images[j].Container })
	}
}

// validRef answers a workload's address that is not one, and says whether it
// is.
func validRef(w http.ResponseWriter, ref WorkloadRef) bool {
	switch {
	case !validNamespace(ref.Namespace):
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ref.Namespace)+" is not a namespace name.", nil)
	case !knownKind(ref.Kind):
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ref.Kind)+" is not a kind of workload: "+
			"Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob or Pod.", nil)
	case !validName(ref.Name):
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ref.Name)+" is not a workload name.", nil)
	default:
		return true
	}
	return false
}

// clip cuts s to n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// licenseNames is a CycloneDX component's licenses: an expression, or each
// license's SPDX id or name.
func licenseNames(m map[string]any) []string {
	var out []string
	for _, l := range list(m, "licenses") {
		lm, ok := l.(map[string]any)
		if !ok {
			continue
		}
		name := str(lm, "expression")
		if name == "" {
			name = str(lm, "license", "id")
		}
		if name == "" {
			name = str(lm, "license", "name")
		}
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, clip(name, maxField))
		}
	}
	return out
}

// summariseSbom keeps each report's image and a line per component, and drops
// the body as soon as it is read: hashes, properties and the dependency graph
// are never kept.
func summariseSbom(items []unstructured.Unstructured, out *SBOM) {
	types, licenses := map[string]int{}, map[string]int{}
	for i := range items {
		report, _ := items[i].Object["report"].(map[string]any)
		container := items[i].GetLabels()[labelContainer]
		comps := list(report, "components", "components")
		// The components listed, never summary.componentsCount: trivy-operator
		// counts the image itself (metadata.component) there too.
		img := SbomImage{Container: container,
			Image:      imageName(str(report, "artifact", "repository"), str(report, "artifact", "tag")),
			Digest:     clip(str(report, "artifact", "digest"), maxField),
			Components: len(comps), Dependencies: len(list(report, "components", "dependencies"))}
		if n := num(report, "summary", "dependenciesCount"); n > 0 {
			img.Dependencies = n
		}
		out.Images = append(out.Images, img)
		for _, c := range comps {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			out.Total++
			names := licenseNames(m)
			comp := Component{Name: clip(str(m, "name"), maxField), Version: clip(str(m, "version"), maxField),
				Type: clip(str(m, "type"), maxField), License: strings.Join(names, ", "), Purl: clip(str(m, "purl"), MaxPurl),
				Container: container}
			if g := str(m, "group"); g != "" {
				comp.Name = clip(g+"/"+comp.Name, maxField)
			}
			types[comp.Type]++
			if len(names) == 0 {
				licenses[""]++
			}
			for _, n := range names {
				licenses[n]++
			}
			out.Components = append(out.Components, comp)
		}
		items[i].Object = nil
	}
	sort.SliceStable(out.Components, func(i, j int) bool {
		a, b := &out.Components[i], &out.Components[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Container < b.Container
	})
	if len(out.Components) > MaxComponents {
		out.Components = append([]Component(nil), out.Components[:MaxComponents]...)
	}
	sort.Slice(out.Images, func(i, j int) bool { return out.Images[i].Container < out.Images[j].Container })
	out.Types = tallies(types, 0)
	out.Licenses = tallies(licenses, MaxLicenses)
}

// tallies is counts by name, the most frequent first, at most limit (0: all).
func tallies(counts map[string]int, limit int) []Tally {
	out := make([]Tally, 0, len(counts))
	for name, n := range counts {
		out = append(out, Tally{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
