package security

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

func init() {
	// The SbomReport's printer columns at trivy-operator v0.36.1.
	tableColumns[KindSbom] = []string{"Name", "Repository", "Tag", "Scanner", "Age", "Components", "Dependencies"}
}

// licenses is the pool the fake components draw from: more than MaxLicenses.
func licence(i int) string { return fmt.Sprintf("LicenseRef-%03d", i%(MaxLicenses+20)) }

// sbomReport is an SbomReport on s with n components, each with its hashes,
// properties and a dependency, as trivy writes them - which must never reach
// an answer. As trivy-operator v0.36.1 does, its Components column and
// summary count n+1: the image itself, its metadata.component, as well.
func sbomReport(name string, s scan, repo, tag string, n int) *fakeObj {
	cells := map[string]any{"Repository": repo, "Tag": tag, "Scanner": "Trivy", "Components": int64(n + 1), "Dependencies": int64(n / 2)}
	return &fakeObj{namespace: s.namespace, name: name, labels: reportLabels(s), owners: ownedBy(s.kind, s.name),
		created: testNow.Add(-30 * time.Minute), cells: cells, body: func() map[string]any {
			comps := make([]any, 0, n)
			deps := make([]any, 0, n/2)
			for i := 0; i < n; i++ {
				typ := "library"
				switch i % 10 {
				case 0:
					typ = "operating-system"
				case 1, 2:
					typ = "application"
				}
				c := map[string]any{
					"bom-ref": fmt.Sprintf("pkg:deb/debian/lib%d@1.%d?arch=amd64", i, i), "type": typ,
					"name": fmt.Sprintf("lib%05d", i), "version": fmt.Sprintf("1.%d.0-1", i),
					"purl":   fmt.Sprintf("pkg:deb/debian/lib%05d@1.%d.0-1?arch=amd64&distro=debian-12.5", i, i),
					"hashes": []any{map[string]any{"alg": "SHA-256", "content": "HASH-NEVER-SENT"}},
					"properties": []any{map[string]any{"name": "aquasecurity:trivy:PkgID", "value": "PROPERTY-NEVER-SENT"},
						map[string]any{"name": "aquasecurity:trivy:SrcName", "value": "x"}},
				}
				switch i % 4 {
				case 0:
					c["licenses"] = []any{map[string]any{"license": map[string]any{"id": licence(i)}}}
				case 1:
					c["licenses"] = []any{map[string]any{"license": map[string]any{"name": "GPL-2.0+"}}, map[string]any{"license": map[string]any{"id": licence(i)}}}
				case 2:
					c["licenses"] = []any{map[string]any{"expression": "MIT OR Apache-2.0"}}
				}
				comps = append(comps, c)
				if i%2 == 0 {
					deps = append(deps, map[string]any{"ref": "DEPENDENCY-NEVER-SENT", "dependsOn": []any{"x"}})
				}
			}
			return map[string]any{"apiVersion": group + "/v1alpha1", "kind": "SbomReport",
				"metadata": meta(s.namespace, name, reportLabels(s), nil),
				"report": map[string]any{
					"artifact": map[string]any{"repository": repo, "tag": tag, "digest": "sha256:" + strings.Repeat("ab", 32)},
					"scanner":  map[string]any{"name": "Trivy"},
					"summary":  map[string]any{"componentsCount": int64(n + 1), "dependenciesCount": int64(n / 2)},
					"components": map[string]any{"bomFormat": "CycloneDX", "specVersion": "1.6",
						"metadata":   map[string]any{"component": map[string]any{"name": repo}},
						"components": comps, "dependencies": deps},
				}}
		}}
}

// withSbom is the shop with SBOMs: web's two containers, worker's one.
func withSbom(t *testing.T) *fakeHost {
	h := shop(t)
	h.reg.served[KindSbom] = true
	h.add(KindSbom,
		sbomReport("replicaset-web-1-app", scan{"shop", "ReplicaSet", "web-1", "app"}, "shop/app", "2.0", 40),
		sbomReport("replicaset-web-1-proxy", scan{"shop", "ReplicaSet", "web-1", "proxy"}, "shop/proxy", "1.0", 12),
		sbomReport("replicaset-api-1-app", scan{"shop", "ReplicaSet", "api-1", "app"}, "shop/app", "2.0", 40))
	return h
}

func TestTheOverviewCountsComponentsFromTheTableOnly(t *testing.T) {
	h := withSbom(t)
	o := decode[Overview](t, serve(t, h, "/"))
	if s := o.Sbom; s.Kind != KindSbom || s.State != StateRead || !s.Complete || s.Reports != 3 {
		t.Errorf("source %+v", s)
	}
	web := rowOf(t, o, "Deployment", "web").Components
	if web == nil || web.Total != 52 || web.Dependencies != 26 || len(web.Images) != 2 ||
		web.Images[0] != (ImageComponents{Container: "app", Image: "shop/app:2.0", Components: 40, Dependencies: 20}) {
		t.Errorf("web %+v", web)
	}
	if w := rowOf(t, o, "Deployment", "worker"); w.Components != nil {
		t.Errorf("no SBOM is null, not zero: %+v", w.Components)
	}
	qs := h.reportQueries(KindSbom)
	if len(qs) != 1 || !qs[0].Columns || qs[0].Limit != MaxRead {
		t.Errorf("SBOM read %+v, want one table read", qs)
	}
}

func TestTheSbomSourceSaysItsState(t *testing.T) {
	// Not served: absent, and not read.
	h := shop(t)
	if s := decode[Overview](t, serve(t, h, "/")).Sbom; s.State != StateAbsent || s.Kind != KindSbom {
		t.Errorf("absent: %+v", s)
	}
	if qs := h.reportQueries(KindSbom); len(qs) != 0 {
		t.Errorf("read %+v while not served", qs)
	}
	// Served and empty: off, as trivy-operator's SBOM generation is.
	h.reg.served[KindSbom] = true
	if s := decode[Overview](t, serve(t, h, "/")).Sbom; s.State != StateOff || s.Reports != 0 {
		t.Errorf("empty: %+v", s)
	}
	if d := decode[SBOM](t, serve(t, h, "/workload/shop/Deployment/web/sbom")); d.Source.State != StateOff || len(d.Components) != 0 {
		t.Errorf("empty detail: %+v", d.Source)
	}
	for _, c := range []struct {
		err   error
		state string
	}{
		{fmt.Errorf("x: %w", extension.ErrDenied), StateDenied},
		{fmt.Errorf("x: %w", extension.ErrUnreviewable), StateDenied},
		{fmt.Errorf("the API server did not answer"), StateUnreadable},
	} {
		h := withSbom(t)
		h.listErr[KindSbom] = c.err
		o := decode[Overview](t, serve(t, h, "/"))
		if o.Sbom.State != c.state || o.Sbom.Reports != 0 || o.Sources[0].State != StateRead {
			t.Errorf("%v: %+v", c.err, o.Sbom)
		}
		d := decode[SBOM](t, serve(t, h, "/workload/shop/Deployment/web/sbom"))
		if d.Source.State != c.state || len(d.Components) != 0 {
			t.Errorf("detail %v: %+v", c.err, d.Source)
		}
	}
	h = withSbom(t)
	h.reg.errs = map[string]error{KindSbom + "." + group: fmt.Errorf("discovery timed out")}
	if s := decode[Overview](t, serve(t, h, "/")).Sbom; s.State != StateUnreadable {
		t.Errorf("lookup failed: %+v", s)
	}
	// Narrowed: read, not complete.
	h = withSbom(t)
	h.allowed = []string{"shop"}
	if s := decode[Overview](t, serve(t, h, "/")).Sbom; s.State != StateRead || s.Complete {
		t.Errorf("narrowed: %+v", s)
	}
}

func TestTheSbomOfAWorkloadIsSummarisedWithoutHashesPropertiesOrDependencies(t *testing.T) {
	h := withSbom(t)
	rec := serve(t, h, "/workload/shop/Deployment/web/sbom")
	for _, never := range []string{"HASH-NEVER-SENT", "PROPERTY-NEVER-SENT", "DEPENDENCY-NEVER-SENT", "hashes", "properties", "bom-ref"} {
		if strings.Contains(rec.Body.String(), never) {
			t.Errorf("%s in the answer", never)
		}
	}
	d := decode[SBOM](t, rec)
	if d.Source.State != StateRead || d.Total != 52 || len(d.Components) != 52 || len(d.Images) != 2 {
		t.Fatalf("source %+v total %d components %d images %+v", d.Source, d.Total, len(d.Components), d.Images)
	}
	if img := d.Images[0]; img.Container != "app" || img.Image != "shop/app:2.0" || !strings.HasPrefix(img.Digest, "sha256:ab") ||
		img.Components != 40 || img.Dependencies != 20 {
		t.Errorf("image %+v", img)
	}
	if c := d.Components[0]; c.Name != "lib00000" || c.Type != "operating-system" || c.License != licence(0) || c.Container != "app" ||
		!strings.HasPrefix(c.Purl, "pkg:deb/debian/lib00000") {
		t.Errorf("first component %+v", c)
	}
	if c := d.Components[2]; c.Name != "lib00001" || c.License != "GPL-2.0+, "+licence(1) {
		t.Errorf("two licenses %+v", c)
	}
	// Types and licenses count every component.
	sum := 0
	for _, ty := range d.Types {
		sum += ty.Count
	}
	if sum != 52 || d.Types[0] != (Tally{"library", 35}) {
		t.Errorf("types %+v", d.Types)
	}
	byName := map[string]int{}
	for _, l := range d.Licenses {
		byName[l.Name] = l.Count
	}
	if byName["MIT OR Apache-2.0"] != 13 || byName["GPL-2.0+"] != 13 || byName[""] != 13 {
		t.Errorf("licenses %+v", d.Licenses)
	}
	// A workload no SBOM covers, and the bad addresses.
	if d := decode[SBOM](t, serve(t, h, "/workload/shop/Deployment/worker/sbom")); d.Total != 0 || len(d.Components) != 0 || d.Source.State != StateRead {
		t.Errorf("worker %+v", d)
	}
	body := serve(t, h, "/workload/shop/Deployment/worker/sbom").Body.String()
	for _, want := range []string{`"components":[]`, `"types":[]`, `"licenses":[]`, `"notes":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s missing in %s", want, body)
		}
	}
	for _, path := range []string{"/workload/Shop!/Deployment/web/sbom", "/workload/shop/Service/web/sbom", "/workload/shop/Deployment/Web_/sbom"} {
		problemOf(t, serve(t, h, path), http.StatusBadRequest, CodeBadRequest)
	}
	h.unknown = true
	problemOf(t, serve(t, h, "/workload/shop/Deployment/web/sbom"), http.StatusNotFound, CodeUnknownCluster)
	if d := decode[SBOM](t, serve(t, shop(t), "/workload/shop/Deployment/web/sbom")); d.Source.State != StateAbsent {
		t.Errorf("not served: %+v", d.Source)
	}
}

// trivy-operator writes no SbomReport for an image in which it found no
// component (busybox, a scratch image), though it scans it for
// vulnerabilities: that container is absent, never an SBOM of 0 components.
func TestAContainerWithoutAnSbomReportIsAbsentNotEmpty(t *testing.T) {
	h := withSbom(t)
	d := decode[SBOM](t, serve(t, h, "/workload/shop/Deployment/worker/sbom"))
	if d.Source.State != StateRead || d.Total != 0 || len(d.Images) != 1 ||
		d.Images[0] != (SbomImage{Container: "app", Image: "shop/worker:1.0", Absent: true}) {
		t.Errorf("worker %+v", d)
	}
	if body := serve(t, h, "/workload/shop/Deployment/web/sbom").Body.String(); strings.Contains(body, `"absent"`) {
		t.Errorf("a container with its report said absent: %s", body)
	}
	// One container of two has a report: only the other is absent.
	h = withSbom(t)
	h.add(KindVulnerability, vulnReport("replicaset-web-1-sidecar", scan{"shop", "ReplicaSet", "web-1", "sidecar"}, "busybox", "1.36", Counts{}))
	d = decode[SBOM](t, serve(t, h, "/workload/shop/Deployment/web/sbom"))
	if len(d.Images) != 3 || d.Total != 52 {
		t.Fatalf("images %+v total %d", d.Images, d.Total)
	}
	if a, p, s := d.Images[0], d.Images[1], d.Images[2]; a.Container != "app" || a.Absent || a.Components != 40 ||
		p.Container != "proxy" || p.Absent || s != (SbomImage{Container: "sidecar", Image: "busybox:1.36", Absent: true}) {
		t.Errorf("images %+v", d.Images)
	}
	// SBOM off: no report anywhere, and the source says so rather than each container.
	h = shop(t)
	h.reg.served[KindSbom] = true
	if d := decode[SBOM](t, serve(t, h, "/workload/shop/Deployment/worker/sbom")); d.Source.State != StateOff || len(d.Images) != 0 {
		t.Errorf("off %+v", d)
	}
}

// The page counts the components it lists, the image's row and its header
// alike, and the overview's column agrees: trivy-operator's componentsCount
// also counts the image itself.
func TestOneImageIsCountedOnceTheSameEverywhere(t *testing.T) {
	h := newHost(t)
	h.reg.served[KindSbom] = true
	h.add("pods", pod("edge", "nginx", nil, "", ""))
	h.add(KindSbom, sbomReport("pod-nginx-nginx", scan{"edge", "Pod", "nginx", "nginx"}, "nginx", "1.27-alpine", 69))
	d := decode[SBOM](t, serve(t, h, "/workload/edge/Pod/nginx/sbom"))
	if d.Total != 69 || len(d.Images) != 1 || d.Images[0].Components != 69 {
		t.Errorf("total %d images %+v", d.Total, d.Images)
	}
	if c := rowOf(t, decode[Overview](t, serve(t, h, "/")), "Pod", "nginx").Components; c == nil || c.Total != 69 || c.Images[0].Components != 69 {
		t.Errorf("overview %+v", c)
	}
}

// The bounds: MaxComponents with the total said, MaxLicenses names, a purl
// cut at MaxPurl.
func TestTheSbomIsBounded(t *testing.T) {
	h := newHost(t)
	h.reg.served[KindSbom] = true
	h.add("pods", pod("big", "p", nil, "", ""))
	long := sbomReport("pod-p-a", scan{"big", "Pod", "p", "a"}, "big/a", "1", 3000)
	body := long.body
	long.body = func() map[string]any {
		b := body()
		comps := b["report"].(map[string]any)["components"].(map[string]any)["components"].([]any)
		comps[0].(map[string]any)["purl"] = "pkg:generic/" + strings.Repeat("é", 300)
		return b
	}
	h.add(KindSbom, long, sbomReport("pod-p-b", scan{"big", "Pod", "p", "b"}, "big/b", "1", 3000))
	d := decode[SBOM](t, serve(t, h, "/workload/big/Pod/p/sbom"))
	if d.Total != 6000 || len(d.Components) != MaxComponents || len(d.Licenses) != MaxLicenses {
		t.Errorf("total %d components %d licenses %d", d.Total, len(d.Components), len(d.Licenses))
	}
	for _, c := range d.Components {
		if len(c.Purl) > MaxPurl || !strings.HasPrefix(c.Purl, "pkg:") {
			t.Fatalf("purl of %d bytes: %q", len(c.Purl), c.Purl)
		}
	}
	if clip(strings.Repeat("é", 200), MaxPurl) != strings.Repeat("é", MaxPurl/2) {
		t.Errorf("a clip cuts on a rune")
	}
}

// The load target of the supply chain: 2 000 SBOM reports read as a table on
// the page, and one workload of three containers of 2 000 components each.
func TestALargeClusterWithSbomsCostsABoundedAmountPerRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := largeCluster(t)
	h.reg.served[KindSbom] = true
	for i := 0; i < 2000; i++ {
		ns := fmt.Sprintf("team-%02d", i%40)
		rs := fmt.Sprintf("svc-%04d-7f9c8d6b5", i)
		h.add(KindSbom, sbomReport("replicaset-"+rs+"-app", scan{ns, "ReplicaSet", rs, "app"},
			fmt.Sprintf("registry.example/team-%02d/svc-%03d", i%40, i%600), "1.4.2", 100+i%400))
	}
	// The worst workload carries two more containers, each of 2 000 components,
	// and its app's report is 2 000 too.
	const target = "svc-0000-7f9c8d6b5"
	h.objects[KindSbom][0] = sbomReport("replicaset-"+target+"-app", scan{"team-00", "ReplicaSet", target, "app"}, "registry.example/a", "1", 2000)
	h.add(KindSbom,
		sbomReport("replicaset-"+target+"-proxy", scan{"team-00", "ReplicaSet", target, "proxy"}, "registry.example/b", "1", 2000),
		sbomReport("replicaset-"+target+"-agent", scan{"team-00", "ReplicaSet", target, "agent"}, "registry.example/c", "1", 2000))

	// Every pod carries the admission's verdict on two images.
	for _, p := range h.objects["pods"] {
		annotated(p, annotationVerify, `{"registry.example/app:1.4.2@sha256:0123":"pass","registry.example/sidecar:2":"skip"}`)
	}

	o := decode[Overview](t, serve(t, h, "/"))
	if o.Sbom.State != StateRead || o.Sbom.Reports != 2002 || o.Signatures.State != StateRead || o.Signatures.Reports != 5000 {
		t.Fatalf("sources %+v %+v", o.Sbom, o.Signatures)
	}
	withComponents := 0
	for _, w := range o.Workloads {
		if w.Components != nil {
			withComponents++
		}
	}
	if withComponents != 2000 {
		t.Fatalf("%d workloads with components", withComponents)
	}
	sbom := "/workload/team-00/Deployment/svc-0000/sbom"
	d := decode[SBOM](t, serve(t, h, sbom))
	if d.Total != 6000 || len(d.Components) != MaxComponents || len(d.Images) != 3 {
		t.Fatalf("sbom: %d of %d components, %d images", len(d.Components), d.Total, len(d.Images))
	}
	for _, c := range []struct {
		name, path string
		max        uint64
	}{
		{"page with 2 000 SBOM rows", "/", maxPageAlloc},
		{"SBOM of 3 containers of 2 000 components", sbom, maxSbomAlloc},
	} {
		alloc, retained, took := measure(t, h, c.path)
		t.Logf("%s: allocated %d MiB, retained %d KiB, %s", c.name, alloc>>20, retained>>10, took.Round(time.Millisecond))
		if alloc > c.max || retained > maxRetained || took > maxTook {
			t.Errorf("%s: allocated %d MiB (max %d), retained %d KiB (max %d), %s (max %s)",
				c.name, alloc>>20, c.max>>20, retained>>10, maxRetained>>10, took, maxTook)
		}
	}
}

// maxSbomAlloc is the ceiling of one workload's SBOM: three bodies of 2 000
// components decoded whole, as the console would decode them. Measured
// locally on 2026-11-13 (load average near 5): 24 MiB in 51 ms, and the page
// with 2 000 SBOM rows and 5 000 annotated pods 36 MiB in 70 ms.
const maxSbomAlloc = 96 << 20
