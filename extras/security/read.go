package security

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// The states of a source. Four, and never a zero standing for one of the
// other three: absent is the kind not served, denied a refusal for the person,
// unreadable a read that failed, and read with complete false a list that was
// narrowed or cut.
const (
	StateRead       = "read"
	StateAbsent     = "absent"
	StateDenied     = "denied"
	StateUnreadable = "unreadable"
)

// Source is how one of trivy-operator's report kinds was read.
type Source struct {
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
	// Reports is how many reports were read; 0 for any state but read.
	Reports int `json:"reports"`
}

// reportRow is one report as its table row tells it: what it scanned, its
// image and its counts. Never a finding.
type reportRow struct {
	namespace, name string
	labels          map[string]string
	owners          []ownerRef
	created         time.Time
	// image is repository:tag, empty for a configuration audit.
	image  string
	counts Counts
}

// ownerRef is the part of an owner reference the linkage reads.
type ownerRef struct {
	kind, name string
	controller bool
}

// object is a pod, a ReplicaSet or a Job, as its table row's metadata tells
// it: no cell is kept.
type object struct {
	namespace, name string
	labels          map[string]string
	owners          []ownerRef
	created         time.Time
	// revision is a ReplicaSet's deployment.kubernetes.io/revision, 0 when
	// it has none.
	revision int64
	// verified is what the admission wrote on a pod about its images
	// (signature.go); nil when it wrote nothing.
	verified *verdicts
}

// service is a Service as exposure needs it.
type service struct {
	namespace, name, kind string
	selector              map[string]string
}

// ingress is an Ingress as exposure needs it: the Services it names.
type ingress struct {
	namespace, name string
	backends        []string
}

// route is a Gateway API route as exposure needs it: the Services it sends
// to, each `namespace/name`, and the Gateways it attaches to.
type route struct {
	kind, namespace, name string
	backends              []string
	gateways              []string
}

// routeKinds are the Gateway API routes that carry traffic from outside to a
// Service, read when the cluster serves them.
var routeKinds = []struct{ plural, kind string }{{"httproutes", "HTTPRoute"}, {"grpcroutes", "GRPCRoute"}}

const gatewayGroup = "gateway.networking.k8s.io"

// wants says which reads beside the reports a route needs.
type wants struct {
	pods, owners, exposure bool
	// sbom reads trivy-operator's SBOM reports as a table beside the three
	// kinds (sbom.go).
	sbom bool
	// signed reads the namespaced policy reports for their image
	// verifications (signature.go).
	signed bool
}

// snapshot is everything one request read, reduced to what it keeps.
type snapshot struct {
	// operator is false when none of the three report kinds is served.
	operator bool
	sources  []Source
	crds     map[string]extension.CRD
	reports  map[string][]reportRow
	pods     []object
	sets     []object // ReplicaSets
	jobs     []object
	services []service
	ingress  []ingress
	routes   []route
	// hostNetwork is the pods on their node's network, `namespace/name`.
	hostNetwork map[string]bool
	// sbom is the SBOM reports' source and rows, when asked for.
	sbom     Source
	sbomCRD  *extension.CRD
	sbomRows []sbomRow
	// podsRead is how the pods' table read went: nil when it was not asked.
	podsRead *readOutcome
	// signed is the image verifications the policy reports carry, and
	// signedFrom how each report kind was read for them, when asked.
	signed     *reportVerdicts
	signedFrom []Source
	// notes are the reads beside the reports that were refused, failed or
	// narrowed: what the answer could not take into account.
	notes []string
	mu    sync.Mutex // notes are added from the reads
}

func (s *snapshot) addNote(note string) {
	s.mu.Lock()
	s.notes = append(s.notes, note)
	s.mu.Unlock()
}

func (s *snapshot) source(kind string) *Source {
	for i := range s.sources {
		if s.sources[i].Kind == kind {
			return &s.sources[i]
		}
	}
	return nil
}

// task is one read: run does it and keeps its answer, fail is told what went
// wrong when it did not answer.
type task struct {
	run  func() error
	fail func(error)
}

// runAll runs the tasks, at most `parallel` at once, and returns when every
// one has finished: none of them outlives the request. Each goes through
// safego, so a panic is that read's failure and not the process's end.
func runAll(tasks []task) {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, t := range tasks {
		sem <- struct{}{}
		wg.Add(1)
		safego.Go("security: a read", t.run, func(err error) {
			if err != nil {
				t.fail(err)
			}
			<-sem
			wg.Done()
		})
	}
	wg.Wait()
}

// readSnapshot reads the three report kinds as tables, in namespace or, empty,
// in every one, and what w asks for beside them. The error is what the whole
// request cannot answer without: an unknown cluster, or a catalog that could
// not be read. Every other failure is a source's state or a note.
func readSnapshot(r *http.Request, h extension.Host, cluster, namespace string, w wants) (*snapshot, error) {
	registry, err := h.Definitions(r, cluster)
	if errors.Is(err, extension.ErrUnknownCluster) {
		return nil, err
	}
	if err != nil {
		return nil, catalogueError{err}
	}
	s := &snapshot{crds: map[string]extension.CRD{}, reports: map[string][]reportRow{}}
	var tasks []task
	var fatal error
	var fatalMu sync.Mutex
	setFatal := func(err error) {
		fatalMu.Lock()
		if fatal == nil {
			fatal = err
		}
		fatalMu.Unlock()
	}
	for _, kind := range reportKinds {
		s.sources = append(s.sources, Source{Kind: kind})
		src := &s.sources[len(s.sources)-1]
		crd, err := registry.Lookup(r.Context(), kind+"."+group)
		switch {
		case errors.Is(err, extension.ErrUnknownKind), errors.Is(err, extension.ErrNotServed):
			src.State = StateAbsent
			continue
		case err != nil:
			s.operator = true // something answers to the name: not "absent"
			src.State, src.Note = StateUnreadable, "Resolving "+kind+" failed: "+textutil.FirstLineN(err.Error(), 200)
			continue
		}
		s.operator = true
		s.crds[kind] = crd
	}
	if !s.operator {
		return s, nil
	}
	// The sources are a fixed slice from here on: each task writes its own
	// source and its own slot of rows, and the map is filled after the wait.
	rowsBy := make([][]reportRow, len(s.sources))
	for i := range s.sources {
		src, slot := &s.sources[i], &rowsBy[i]
		crd, ok := s.crds[src.Kind]
		if !ok {
			continue
		}
		tasks = append(tasks, task{
			run: func() error {
				page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: namespace, Columns: true, Limit: MaxRead})
				if err != nil {
					return err
				}
				*slot = reportRows(src.Kind, page)
				src.State, src.Complete, src.Note, src.Reports = StateRead, page.Complete, page.Note, len(*slot)
				return nil
			},
			fail: func(err error) {
				if errors.Is(err, extension.ErrUnknownCluster) {
					setFatal(err)
				}
				classify(src, err)
			},
		})
	}
	meta := func(what string, gvr extension.ListQuery, keep func([]extension.Row)) {
		tasks = append(tasks, task{
			run: func() error {
				page, err := h.List(r, cluster, gvr)
				if err != nil {
					return err
				}
				keep(page.Rows)
				if !page.Complete {
					s.addNote(what + ": " + page.Note)
				}
				s.outcome(what, page.Complete, page.Note, nil)
				return nil
			},
			fail: func(err error) {
				if errors.Is(err, extension.ErrUnknownCluster) {
					setFatal(err)
					return
				}
				s.outcome(what, false, "", err)
				s.addNote(what + " " + failedWord(err) + ": " + textutil.FirstLineN(err.Error(), 200))
			},
		})
	}
	var pods, sets, jobs []object
	if w.pods {
		meta("Pods", extension.ListQuery{GVR: podsGVR, Namespace: namespace, Columns: true, Limit: MaxRead},
			func(rows []extension.Row) { pods = objects(rows, false) })
	}
	if w.owners {
		meta("ReplicaSets", extension.ListQuery{GVR: replicaSetsGVR, Namespace: namespace, Columns: true, Limit: MaxRead},
			func(rows []extension.Row) { sets = objects(rows, true) })
		meta("Jobs", extension.ListQuery{GVR: jobsGVR, Namespace: namespace, Columns: true, Limit: MaxRead},
			func(rows []extension.Row) { jobs = objects(rows, false) })
	}
	if w.sbom {
		tasks = append(tasks, sbomTask(r, h, cluster, namespace, registry, s, setFatal)...)
	}
	if w.signed {
		tasks = append(tasks, signedTasks(r, h, cluster, namespace, registry, s, setFatal)...)
	}
	var services []service
	var ingresses []ingress
	routesBy := make([][]route, len(routeKinds))
	var hostNetwork map[string]bool
	if w.exposure {
		// Gateway API, where the cluster serves it: a cluster without it
		// has nothing to say here, and says nothing. Routes are read in every
		// namespace: one may send to a Service in another, where a
		// ReferenceGrant allows it, and a route is a small object.
		for i, rk := range routeKinds {
			crd, err := registry.Lookup(r.Context(), rk.plural+"."+gatewayGroup)
			if err != nil {
				if !errors.Is(err, extension.ErrUnknownKind) && !errors.Is(err, extension.ErrNotServed) {
					s.addNote(rk.kind + "s " + failedWord(err) + ": " + textutil.FirstLineN(err.Error(), 200))
				}
				continue
			}
			slot, kind := &routesBy[i], rk.kind
			tasks = append(tasks, listTask(s, setFatal, kind+"s", func() (bool, string, error) {
				page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: MaxRead})
				if err == nil {
					*slot = routesOf(kind, page.Items)
				}
				return page.Complete, page.Note, err
			}))
		}
		// A pod on the node's network answers on the node's address: the
		// API server selects those pods by field, as a table, so no pod body
		// is read for it.
		tasks = append(tasks, listTask(s, setFatal, "host-network Pods", func() (bool, string, error) {
			page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, Columns: true, Limit: MaxRead,
				FieldSelector: "spec.hostNetwork=true"})
			if err == nil {
				hostNetwork = make(map[string]bool, len(page.Rows))
				for _, row := range page.Rows {
					hostNetwork[row.Namespace+"/"+row.Name] = true
				}
			}
			return page.Complete, page.Note, err
		}))
		tasks = append(tasks, listTask(s, setFatal, "Services", func() (bool, string, error) {
			page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR, Namespace: namespace})
			if err == nil {
				services = servicesOf(page.Items)
			}
			return page.Complete, page.Note, err
		}))
		tasks = append(tasks, listTask(s, setFatal, "Ingresses", func() (bool, string, error) {
			page, err := h.List(r, cluster, extension.ListQuery{GVR: ingressesGVR, Namespace: namespace})
			if err == nil {
				ingresses = ingressesOf(page.Items)
			}
			return page.Complete, page.Note, err
		}))
	}
	runAll(tasks)
	for i := range s.sources {
		if rowsBy[i] != nil {
			s.reports[s.sources[i].Kind] = rowsBy[i]
		}
	}
	if fatal != nil {
		return nil, fatal
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	s.pods, s.sets, s.jobs, s.services, s.ingress = pods, sets, jobs, services, ingresses
	for _, rs := range routesBy {
		s.routes = append(s.routes, rs...)
	}
	s.hostNetwork = hostNetwork
	return s, nil
}

// classify is a report read that did not answer, as its source's state.
func classify(src *Source, err error) {
	src.Complete, src.Reports = false, 0
	switch {
	case errors.Is(err, extension.ErrDenied):
		src.State, src.Note = StateDenied, "You may not list "+src.Kind+" here."
	case errors.Is(err, extension.ErrUnreviewable):
		src.State, src.Note = StateDenied, extension.UnreviewedText
	default:
		src.State, src.Note = StateUnreadable, src.Kind+" could not be read: "+textutil.FirstLineN(err.Error(), 200)
	}
}

func failedWord(err error) string {
	if errors.Is(err, extension.ErrDenied) || errors.Is(err, extension.ErrUnreviewable) {
		return "were refused"
	}
	return "could not be read"
}

// listTask is a read of whole objects beside the reports - Services and
// Ingresses, which are small - whose failure is a note.
func listTask(s *snapshot, setFatal func(error), what string, read func() (bool, string, error)) task {
	return task{
		run: func() error {
			complete, note, err := read()
			if err != nil {
				return err
			}
			if !complete {
				s.addNote(what + ": " + note)
			}
			return nil
		},
		fail: func(err error) {
			if errors.Is(err, extension.ErrUnknownCluster) {
				setFatal(err)
				return
			}
			s.addNote(what + " " + failedWord(err) + ", so what is exposed may be missing: " + textutil.FirstLineN(err.Error(), 200))
		},
	}
}

// column is the index of the named column, -1 when the table has none.
func column(columns []string, name string) int {
	for i, c := range columns {
		if c == name {
			return i
		}
	}
	return -1
}

func cellString(cells []any, i int) string {
	if i < 0 || i >= len(cells) {
		return ""
	}
	s, _ := cells[i].(string)
	return s
}

func cellInt(cells []any, i int) int {
	if i < 0 || i >= len(cells) {
		return 0
	}
	switch n := cells[i].(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// reportRows reduces a table of reports to what is kept: the cells are read
// once and dropped.
func reportRows(kind string, page extension.Page) []reportRow {
	repo, tag := column(page.Columns, "Repository"), column(page.Columns, "Tag")
	crit, high, med := column(page.Columns, "Critical"), column(page.Columns, "High"), column(page.Columns, "Medium")
	low, unknown := column(page.Columns, "Low"), column(page.Columns, "Unknown")
	out := make([]reportRow, 0, len(page.Rows))
	for i := range page.Rows {
		row := &page.Rows[i]
		rep := reportRow{namespace: row.Namespace, name: row.Name, labels: row.Labels, owners: owners(row),
			created: row.Created, counts: Counts{
				Critical: cellInt(row.Cells, crit), High: cellInt(row.Cells, high), Medium: cellInt(row.Cells, med),
				Low: cellInt(row.Cells, low), Unknown: cellInt(row.Cells, unknown),
			}}
		if kind != KindConfigAudit {
			rep.image = imageName(cellString(row.Cells, repo), cellString(row.Cells, tag))
		}
		out = append(out, rep)
	}
	return out
}

// imageName is repository:tag, as the report's columns give it.
func imageName(repository, tag string) string {
	if repository == "" {
		return ""
	}
	if tag == "" {
		return repository
	}
	return repository + ":" + tag
}

func owners(row *extension.Row) []ownerRef {
	if len(row.Owners) == 0 {
		return nil
	}
	out := make([]ownerRef, 0, len(row.Owners))
	for _, o := range row.Owners {
		out = append(out, ownerRef{kind: o.Kind, name: o.Name, controller: o.Controller != nil && *o.Controller})
	}
	return out
}

// objects keeps a table's metadata, and a ReplicaSet's revision when asked.
func objects(rows []extension.Row, revision bool) []object {
	out := make([]object, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		o := object{namespace: row.Namespace, name: row.Name, labels: row.Labels, owners: owners(row), created: row.Created,
			verified: verifiedOf(row.Annotations)}
		if revision {
			_, _ = fmt.Sscan(row.Annotations["deployment.kubernetes.io/revision"], &o.revision)
		}
		out = append(out, o)
	}
	return out
}

func servicesOf(items []unstructured.Unstructured) []service {
	out := make([]service, 0, len(items))
	for i := range items {
		u := &items[i]
		kind, _, _ := unstructured.NestedString(u.Object, "spec", "type")
		selector, _, _ := unstructured.NestedStringMap(u.Object, "spec", "selector")
		out = append(out, service{namespace: u.GetNamespace(), name: u.GetName(), kind: kind, selector: selector})
	}
	return out
}

// routesOf keeps a route's Service backends and the Gateways it attaches
// to; a backend or a parent in no namespace of its own is in the route's.
func routesOf(kind string, items []unstructured.Unstructured) []route {
	out := make([]route, 0, len(items))
	for i := range items {
		u := &items[i]
		rt := route{kind: kind, namespace: u.GetNamespace(), name: u.GetName()}
		parents, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
		for _, p := range parents {
			m, _ := p.(map[string]any)
			if k := str(m, "kind"); k != "" && k != "Gateway" {
				continue
			}
			ns := str(m, "namespace")
			if ns == "" {
				ns = rt.namespace
			}
			if name := str(m, "name"); name != "" {
				rt.gateways = append(rt.gateways, ns+"/"+name)
			}
		}
		rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
		for _, rule := range rules {
			m, _ := rule.(map[string]any)
			for _, b := range list(m, "backendRefs") {
				bm, _ := b.(map[string]any)
				if k := str(bm, "kind"); (k != "" && k != "Service") || (str(bm, "group") != "") {
					continue
				}
				ns := str(bm, "namespace")
				if ns == "" {
					ns = rt.namespace
				}
				if name := str(bm, "name"); name != "" {
					rt.backends = append(rt.backends, ns+"/"+name)
				}
			}
		}
		u.Object = nil
		out = append(out, rt)
	}
	return out
}

func ingressesOf(items []unstructured.Unstructured) []ingress {
	out := make([]ingress, 0, len(items))
	for i := range items {
		u := &items[i]
		in := ingress{namespace: u.GetNamespace(), name: u.GetName()}
		if name, ok, _ := unstructured.NestedString(u.Object, "spec", "defaultBackend", "service", "name"); ok && name != "" {
			in.backends = append(in.backends, name)
		}
		rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
		for _, rule := range rules {
			m, _ := rule.(map[string]any)
			paths, _, _ := unstructured.NestedSlice(m, "http", "paths")
			for _, p := range paths {
				pm, _ := p.(map[string]any)
				if name, ok, _ := unstructured.NestedString(pm, "backend", "service", "name"); ok && name != "" {
					in.backends = append(in.backends, name)
				}
			}
		}
		out = append(out, in)
	}
	return out
}

// owner is the reference that controls, or the first one.
func owner(refs []ownerRef) (ownerRef, bool) {
	for _, o := range refs {
		if o.controller {
			return o, true
		}
	}
	if len(refs) > 0 {
		return refs[0], true
	}
	return ownerRef{}, false
}
