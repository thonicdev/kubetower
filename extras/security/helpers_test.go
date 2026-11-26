package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thonicdev/kubetower/extension"
)

// No goroutine a route starts outlives its request: every read is waited for.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var testNow = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

// fakeObj is one object as the fake cluster holds it: its metadata, the cells
// its table row carries, and its body, built only when a whole object is read
// - as a report's findings would be decoded only then.
type fakeObj struct {
	namespace, name string
	labels          map[string]string
	annotations     map[string]string
	owners          []metav1.OwnerReference
	created         time.Time
	cells           map[string]any
	body            func() map[string]any
	// hostNetwork answers a `spec.hostNetwork=true` field selector.
	hostNetwork bool
}

// tableColumns is each resource's table, as the API server draws it: a CRD's
// printer columns after Name, a core kind's own.
var tableColumns = map[string][]string{
	KindVulnerability:        {"Name", "Repository", "Tag", "Scanner", "Age", "Critical", "High", "Medium", "Low", "Unknown"},
	KindConfigAudit:          {"Name", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	KindExposedSecret:        {"Name", "Repository", "Tag", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	KindClusterVulnerability: {"Name", "Repository", "Tag", "Scanner", "Age", "Critical", "High", "Medium", "Low", "Unknown"},
	KindClusterConfigAudit:   {"Name", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	KindInfraAssessment:      {"Name", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	KindClusterInfra:         {"Name", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	KindRBAC:                 {"Name", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	KindClusterRBAC:          {"Name", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	"policyreports":          {"Name", "Kind", "Name", "Pass", "Fail", "Warn", "Error", "Skip", "Age"},
	"reports":                {"Name", "Pass", "Fail", "Warn", "Error", "Skip", "Age"},
	"pods":                   {"Name", "Ready", "Status", "Restarts", "Age"},
	"replicasets":            {"Name", "Desired", "Current", "Ready", "Age"},
	"jobs":                   {"Name", "Status", "Completions", "Duration", "Age"},
}

// registry serves the report kinds in served; errs answers a lookup with an
// error instead.
type registry struct {
	served map[string]bool
	errs   map[string]error
	// gateway is the Gateway API routes served, by plural.
	gateway map[string]bool
	// other is any other kind served, by plural.group, and constraints the
	// Gatekeeper constraint kinds, by kind.
	other       map[string]extension.CRD
	constraints []string
}

func (g registry) All(context.Context) ([]extension.CRD, error) {
	var out []extension.CRD
	for _, kind := range g.constraints {
		out = append(out, extension.CRD{Group: KindGatekeeper, Version: "v1beta1", Plural: strings.ToLower(kind), Kind: kind})
	}
	return out, nil
}

func (g registry) Lookup(_ context.Context, kind string) (extension.CRD, error) {
	if err := g.errs[kind]; err != nil {
		return extension.CRD{}, err
	}
	if crd, ok := g.other[kind]; ok {
		return crd, nil
	}
	plural, grp, _ := strings.Cut(kind, ".")
	if grp == gatewayGroup && g.gateway[plural] {
		return extension.CRD{Group: grp, Version: "v1", Plural: plural, Namespaced: true}, nil
	}
	if grp != group || !g.served[plural] {
		return extension.CRD{}, fmt.Errorf("%s: %w", kind, extension.ErrUnknownKind)
	}
	return extension.CRD{Group: grp, Version: "v1alpha1", Plural: plural, Namespaced: true}, nil
}

// fakeHost is extension.Host with Definitions and List made real, List
// answering a table read from the cells alone. Every other door fails the
// test: this extra reads, and through those two only.
type fakeHost struct {
	t  *testing.T
	mu sync.Mutex

	reg     registry
	defErr  error
	unknown bool
	objects map[string][]*fakeObj
	listErr map[string]error
	// allowed narrows the person to these namespaces.
	allowed []string
	queries []extension.ListQuery
	// inFlight and peak count the reads running at once.
	inFlight, peak int
	delay          time.Duration
}

func newHost(t *testing.T) *fakeHost {
	return &fakeHost{t: t, reg: registry{served: map[string]bool{KindVulnerability: true, KindConfigAudit: true, KindExposedSecret: true}},
		objects: map[string][]*fakeObj{}, listErr: map[string]error{}}
}

func (h *fakeHost) add(resource string, objs ...*fakeObj) {
	h.objects[resource] = append(h.objects[resource], objs...)
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	h.t.Error("Host.Client was called: this extra reads only through the reviewed doors")
	return extension.Clients{}, errors.New("not used")
}
func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	if h.unknown {
		return nil, fmt.Errorf("kind: %w", extension.ErrUnknownCluster)
	}
	if h.defErr != nil {
		return nil, h.defErr
	}
	return h.reg, nil
}
func (h *fakeHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	h.t.Error("Host.Reveal was called")
	return "", errors.New("not used")
}
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	h.t.Error("Host.ConfiguredSecret was called")
	return "", extension.ErrNotConfigured
}
func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	h.t.Error("Host.ConfiguredService was called")
	return corev1.Service{}, extension.ErrNotConfigured
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	h.t.Error("Host.Exec was called")
	return extension.ExecResult{}, errors.New("not used")
}
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error {
	h.t.Error("Host.MayReadLogs was called")
	return errors.New("not used")
}
func (h *fakeHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	h.t.Error("Host.Usage was called")
	return extension.Usage{}, errors.New("not used")
}
func (h *fakeHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	h.t.Error("Host.ContainerUsage was called")
	return extension.LiveUsage{}, errors.New("not used")
}
func (h *fakeHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	h.t.Error("Host.UsageWindow was called")
	return extension.Window{}, errors.New("not used")
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

func (h *fakeHost) VolumeFill(*http.Request, string, []extension.ClaimRef) (extension.VolumeFill, error) {
	return extension.VolumeFill{State: extension.WindowNoPrometheus}, nil
}
func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.ForwardAsConsole was called")
	return extension.Tunnel{}, errors.New("not used")
}
func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.Forward was called")
	return extension.Tunnel{}, errors.New("not used")
}
func (h *fakeHost) Setting(*http.Request, string, string) string { return "" }
func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string {
	h.t.Error("Host.ConfirmToken was called: nothing here writes")
	return ""
}

func labelsMatch(labels map[string]string, selector string) bool {
	if selector == "" {
		return true
	}
	for _, req := range strings.Split(selector, ",") {
		k, v, _ := strings.Cut(req, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	// Kept without its callback, which holds whatever the walk built: the
	// load test measures what a request leaves behind, not what the fake does.
	kept := q
	if kept.Each != nil {
		kept.Each = walkedMarker
	}
	h.queries = append(h.queries, kept)
	h.inFlight++
	if h.inFlight > h.peak {
		h.peak = h.inFlight
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.inFlight--
		h.mu.Unlock()
	}()
	if h.delay > 0 {
		time.Sleep(h.delay)
	}
	if h.unknown {
		return extension.Page{}, fmt.Errorf("kind: %w", extension.ErrUnknownCluster)
	}
	if err := h.listErr[q.GVR.Resource]; err != nil {
		return extension.Page{}, err
	}
	page := extension.Page{Complete: true}
	if len(h.allowed) > 0 && q.Namespace == "" {
		page.Complete, page.Note = false, "You may not list "+q.GVR.Resource+" in every namespace, so this holds only the namespaces where you may."
	}
	if q.Columns {
		page.Columns = tableColumns[q.GVR.Resource]
		if page.Columns == nil {
			h.t.Errorf("a table read of %s, which the fake draws no table for", q.GVR.Resource)
		}
	}
	walked := 0
	for _, o := range h.objects[q.GVR.Resource] {
		if q.Namespace != "" && o.namespace != q.Namespace {
			continue
		}
		if len(h.allowed) > 0 && !contains(h.allowed, o.namespace) {
			continue
		}
		if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok && o.name != name {
			continue
		}
		if q.FieldSelector == "spec.hostNetwork=true" && !o.hostNetwork {
			continue
		}
		if !labelsMatch(o.labels, q.LabelSelector) {
			continue
		}
		n := len(page.Items) + len(page.Rows) + walked
		if q.Limit > 0 && int64(n) >= q.Limit {
			page.Complete, page.Note = false, "Cut at the limit."
			break
		}
		if q.Columns {
			cells := make([]any, len(page.Columns))
			for i, c := range page.Columns {
				switch c {
				case "Name":
					cells[i] = o.name
				case "Age":
					cells[i] = "1h"
				default:
					cells[i] = o.cells[c]
				}
			}
			page.Rows = append(page.Rows, extension.Row{Namespace: o.namespace, Name: o.name, Labels: o.labels,
				Annotations: o.annotations, Owners: o.owners, Created: o.created, Cells: cells})
			continue
		}
		if o.body == nil {
			h.t.Errorf("a whole-object read of %s, which the fake holds no body for", q.GVR.Resource)
			continue
		}
		if q.Each != nil {
			walked++
			if err := q.Each(unstructured.Unstructured{Object: o.body()}); err != nil {
				return extension.Page{}, err
			}
			continue
		}
		page.Items = append(page.Items, unstructured.Unstructured{Object: o.body()})
	}
	return page, nil
}

// walkedMarker stands for a query's Each in what the fake records.
func walkedMarker(unstructured.Unstructured) error { return nil }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// reportQueries is every List this host answered for one report kind.
func (h *fakeHost) reportQueries(kind string) []extension.ListQuery {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []extension.ListQuery
	for _, q := range h.queries {
		if q.GVR.Resource == kind {
			out = append(out, q)
		}
	}
	return out
}

// --- the objects ---------------------------------------------------------------

func ownedBy(kind, name string) []metav1.OwnerReference {
	yes := true
	api := "apps/v1"
	switch kind {
	case "Job", "CronJob":
		api = "batch/v1"
	case "Pod":
		api = "v1"
	}
	return []metav1.OwnerReference{{APIVersion: api, Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &yes}}
}

func meta(namespace, name string, labels map[string]string, owners []metav1.OwnerReference) map[string]any {
	m := map[string]any{"namespace": namespace, "name": name, "creationTimestamp": testNow.Add(-time.Hour).Format(time.RFC3339)}
	if len(labels) > 0 {
		l := map[string]any{}
		for k, v := range labels {
			l[k] = v
		}
		m["labels"] = l
	}
	return m
}

func pod(namespace, name string, labels map[string]string, ownerKind, ownerName string) *fakeObj {
	o := &fakeObj{namespace: namespace, name: name, labels: labels, created: testNow.Add(-time.Hour)}
	if ownerKind != "" {
		o.owners = ownedBy(ownerKind, ownerName)
	}
	return o
}

func replicaSet(namespace, name, deployment string, revision int) *fakeObj {
	return &fakeObj{namespace: namespace, name: name, owners: ownedBy("Deployment", deployment),
		annotations: map[string]string{"deployment.kubernetes.io/revision": fmt.Sprint(revision)}, created: testNow.Add(-time.Hour)}
}

func job(namespace, name, cronJob string, created time.Time) *fakeObj {
	o := &fakeObj{namespace: namespace, name: name, created: created}
	if cronJob != "" {
		o.owners = ownedBy("CronJob", cronJob)
	}
	return o
}

func svc(namespace, name, kind string, selector map[string]string) *fakeObj {
	sel := map[string]any{}
	for k, v := range selector {
		sel[k] = v
	}
	return &fakeObj{namespace: namespace, name: name, body: func() map[string]any {
		return map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": meta(namespace, name, nil, nil),
			"spec": map[string]any{"type": kind, "selector": sel}}
	}}
}

func ing(namespace, name string, backends ...string) *fakeObj {
	return &fakeObj{namespace: namespace, name: name, body: func() map[string]any {
		var paths []any
		for _, b := range backends {
			paths = append(paths, map[string]any{"path": "/", "backend": map[string]any{"service": map[string]any{"name": b}}})
		}
		return map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": meta(namespace, name, nil, nil),
			"spec": map[string]any{"rules": []any{map[string]any{"host": "x.example", "http": map[string]any{"paths": paths}}}}}
	}}
}

// scan is what a report was taken on.
type scan struct {
	namespace, kind, name, container string
}

func reportLabels(s scan) map[string]string {
	return map[string]string{labelKind: s.kind, labelName: s.name, labelNamespace: s.namespace, labelContainer: s.container}
}

func countCells(c Counts, unknown bool) map[string]any {
	m := map[string]any{"Critical": int64(c.Critical), "High": int64(c.High), "Medium": int64(c.Medium), "Low": int64(c.Low)}
	if unknown {
		m["Unknown"] = int64(c.Unknown)
	}
	return m
}

func summaryOf(c Counts) map[string]any {
	return map[string]any{"criticalCount": int64(c.Critical), "highCount": int64(c.High), "mediumCount": int64(c.Medium),
		"lowCount": int64(c.Low), "unknownCount": int64(c.Unknown), "noneCount": int64(0)}
}

// description is what a CVE's description weighs in a real report: a few
// hundred characters the extra must never send.
const description = "DESCRIPTION-NEVER-SENT A flaw was found in the way the library parses crafted input: a remote " +
	"attacker able to send a specially formed request could cause an out-of-bounds read, leading to a crash of the " +
	"process or, in some configurations, to the disclosure of memory contents. Versions before the fixed release are affected."

// vulnerabilities is n findings spread over c's severities, the worst first.
func vulnerabilities(c Counts, prefix string) []any {
	var out []any
	add := func(sev string, n int, score float64) {
		for i := 0; i < n; i++ {
			fixed := fmt.Sprintf("1.%d.1", i)
			if i%3 == 2 {
				fixed = ""
			}
			out = append(out, map[string]any{
				"vulnerabilityID": fmt.Sprintf("CVE-2026-%s%05d", prefix, len(out)), "severity": sev, "score": score,
				"resource": fmt.Sprintf("libexample%d", i%40), "installedVersion": fmt.Sprintf("1.%d.0", i), "fixedVersion": fixed,
				"title":       fmt.Sprintf("libexample: out-of-bounds read in the request parser when handling crafted header %d of a long series of very similar findings that keeps going well past the limit", i),
				"description": description, "primaryLink": fmt.Sprintf("https://avd.aquasec.com/nvd/cve-2026-%05d", len(out)),
				"links":  []any{"https://nvd.nist.gov/vuln/detail/x", "https://security-tracker.example/x"},
				"cvss":   map[string]any{"nvd": map[string]any{"V3Score": score, "V3Vector": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
				"target": "registry.example/app (debian 12.5)", "class": "os-pkgs", "packageType": "debian",
				"publishedDate": "2026-01-01T00:00:00Z", "lastModifiedDate": "2026-02-01T00:00:00Z",
			})
		}
	}
	add("CRITICAL", c.Critical, 9.8)
	add("HIGH", c.High, 7.5)
	add("MEDIUM", c.Medium, 5.3)
	add("LOW", c.Low, 3.1)
	add("UNKNOWN", c.Unknown, 0)
	return out
}

// vulnReport is a VulnerabilityReport on s, owned by what it scanned, with
// c's counts in its columns and as many findings in its body.
func vulnReport(name string, s scan, repo, tag string, c Counts) *fakeObj {
	cells := countCells(c, true)
	cells["Repository"], cells["Tag"], cells["Scanner"] = repo, tag, "Trivy"
	return &fakeObj{namespace: s.namespace, name: name, labels: reportLabels(s), owners: ownedBy(s.kind, s.name),
		created: testNow.Add(-30 * time.Minute), cells: cells, body: func() map[string]any {
			return map[string]any{"apiVersion": group + "/v1alpha1", "kind": "VulnerabilityReport",
				"metadata": meta(s.namespace, name, reportLabels(s), nil),
				"report": map[string]any{
					"artifact":        map[string]any{"repository": repo, "tag": tag, "digest": "sha256:0123"},
					"registry":        map[string]any{"server": "registry.example"},
					"os":              map[string]any{"family": "debian", "name": "12.5", "eosl": false},
					"scanner":         map[string]any{"name": "Trivy", "vendor": "Aqua Security", "version": "0.58.0"},
					"summary":         summaryOf(c),
					"updateTimestamp": testNow.Add(-30 * time.Minute).Format(time.RFC3339),
					"vulnerabilities": vulnerabilities(c, name[:min(3, len(name))]),
				}}
		}}
}

// configReport is a ConfigAuditReport with failed failing checks and two
// passing ones.
func configReport(name string, s scan, c Counts, failed int) *fakeObj {
	cells := countCells(c, false)
	cells["Scanner"] = "Trivy"
	return &fakeObj{namespace: s.namespace, name: name, labels: reportLabels(s), owners: ownedBy(s.kind, s.name),
		created: testNow.Add(-30 * time.Minute), cells: cells, body: func() map[string]any {
			var checks []any
			for i := 0; i < failed; i++ {
				checks = append(checks, map[string]any{"checkID": fmt.Sprintf("KSV%03d", i+1), "title": "Root file system is not read-only",
					"severity": []string{"HIGH", "MEDIUM", "LOW"}[i%3], "category": "Kubernetes Security Check", "success": false,
					"description": description, "messages": []any{"Container 'app' should set readOnlyRootFilesystem"}})
			}
			checks = append(checks,
				map[string]any{"checkID": "KSV900", "title": "Passing check", "severity": "CRITICAL", "success": true},
				map[string]any{"checkID": "KSV901", "title": "Another passing check", "severity": "HIGH", "success": true})
			return map[string]any{"apiVersion": group + "/v1alpha1", "kind": "ConfigAuditReport",
				"metadata": meta(s.namespace, name, reportLabels(s), nil),
				"report":   map[string]any{"summary": summaryOf(c), "checks": checks}}
		}}
}

// secretMatch is the text a secret's `match` carries, which must never reach
// an answer.
const secretMatch = "AWS_SECRET_ACCESS_KEY=MATCH-NEVER-SENT"

func secretReport(name string, s scan, repo, tag string, c Counts) *fakeObj {
	cells := countCells(c, false)
	cells["Repository"], cells["Tag"], cells["Scanner"] = repo, tag, "Trivy"
	return &fakeObj{namespace: s.namespace, name: name, labels: reportLabels(s), owners: ownedBy(s.kind, s.name),
		created: testNow.Add(-30 * time.Minute), cells: cells, body: func() map[string]any {
			var secrets []any
			for i := 0; i < c.total(); i++ {
				secrets = append(secrets, map[string]any{"ruleID": "aws-secret-access-key", "title": "AWS Secret Access Key",
					"category": "AWS", "severity": "CRITICAL", "target": fmt.Sprintf("/app/config-%d.env", i), "match": secretMatch})
			}
			return map[string]any{"apiVersion": group + "/v1alpha1", "kind": "ExposedSecretReport",
				"metadata": meta(s.namespace, name, reportLabels(s), nil),
				"report": map[string]any{"artifact": map[string]any{"repository": repo, "tag": tag}, "summary": summaryOf(c),
					"secrets": secrets}}
		}}
}

// --- serving -------------------------------------------------------------------

func serveMethod(t *testing.T, h extension.Host, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	x := Security{now: func() time.Time { return testNow }}
	mux := http.NewServeMux()
	for _, r := range x.Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/security"+r.Pattern, r.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/x/kind/security"+path, nil))
	return rec
}

func serve(t *testing.T, h extension.Host, path string) *httptest.ResponseRecorder {
	t.Helper()
	return serveMethod(t, h, http.MethodGet, path)
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return out
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) problem {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("content type %q, want application/problem+json", ct)
	}
	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if p.Code != code {
		t.Errorf("code %q, want %q (%s)", p.Code, code, p.Message)
	}
	return p
}
