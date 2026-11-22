package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/cluster/clustertest"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The project menu, end to end: an API server that answers
// discovery and lists its CRDs, a real cluster manager in front of it, and the
// menu read through the same API operations the browser calls.

// apiServer is a cluster: what its discovery documents say, and its CRDs.
type apiServer struct {
	gatewayAPI bool
	// refuse answers the two discovery roots 403.
	refuse bool
	// beforeCronJobsV1 is a cluster older than 1.21: its CronJobs are
	// batch/v1beta1 only, and batch/v1 serves Jobs alone.
	beforeCronJobsV1 bool
	// batchUnread answers batch/v1's discovery document 503, as an aggregated
	// group whose backend is down answers.
	batchUnread bool
	// coreNotFound answers /api and /api/v1 404, as a proxy that forwards
	// /apis alone does: client-go then has no core list at all.
	coreNotFound bool
	// coreListNotFound answers /api with v1 but /api/v1 404, as a proxy that
	// forwards the root alone does: client-go then has an empty core v1 list.
	coreListNotFound bool
	// hold, when set, keeps the discovery roots from answering until it is
	// closed: a large cluster whose discovery is slow.
	hold <-chan struct{}
	// notFound, when set, records each core discovery path answered 404.
	notFound *corePaths
}

// corePaths is the core discovery paths a fake answered 404, for a test to
// check the 404 it is named for really reached client-go.
type corePaths struct {
	mu    sync.Mutex
	paths map[string]bool
}

func (c *corePaths) add(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paths == nil {
		c.paths = map[string]bool{}
	}
	c.paths[path] = true
}

func (c *corePaths) has(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paths[path]
}

// consoleResources is what this cluster serves of the console's own kinds, by
// group version: every one of the thirty-six, at the version kinds.go reads it.
func (a apiServer) consoleResources() map[string][]string {
	out := map[string][]string{}
	for _, d := range resources.Kinds() {
		q, ok := d.ListQuestion()
		if !ok {
			continue
		}
		gv := q.Attrs.Version
		if q.Attrs.Group != "" {
			gv = q.Attrs.Group + "/" + gv
		}
		if a.beforeCronJobsV1 && q.Attrs.Resource == "cronjobs" {
			gv = "batch/v1beta1"
		}
		out[gv] = append(out[gv], fmt.Sprintf(`{"name":%q,"kind":%q,"namespaced":%t,"verbs":["get","list"]}`,
			q.Attrs.Resource, d.Title, d.Namespaced))
	}
	return out
}

func (a apiServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if a.hold != nil && (r.URL.Path == "/api" || r.URL.Path == "/apis") {
		select {
		case <-a.hold:
		case <-r.Context().Done():
			return
		}
	}
	if a.refuse && (r.URL.Path == "/api" || r.URL.Path == "/apis") {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"discovery is not permitted"}`)
		return
	}
	// Each group version's resources, the console's own first.
	served := a.consoleResources()
	served["storage.k8s.io/v1"] = append(served["storage.k8s.io/v1"],
		`{"name":"volumeattachments","kind":"VolumeAttachment","namespaced":false,"verbs":["get","list"]}`)
	served["cluster.x-k8s.io/v1beta1"] = []string{`{"name":"clusters","kind":"Cluster","namespaced":true,"verbs":["get","list"]}`}
	served["apiextensions.k8s.io/v1"] = []string{`{"name":"customresourcedefinitions","kind":"CustomResourceDefinition","namespaced":false,"verbs":["get","list"]}`}
	crds := []string{crdJSON("cluster.x-k8s.io", "v1beta1", "Cluster", "clusters", "Namespaced")}
	if a.gatewayAPI {
		served["gateway.networking.k8s.io/v1"] = []string{
			`{"name":"gateways","kind":"Gateway","namespaced":true,"verbs":["get","list"]}`,
			`{"name":"httproutes","kind":"HTTPRoute","namespaced":true,"verbs":["get","list"]}`,
		}
		crds = append(crds,
			crdJSON("gateway.networking.k8s.io", "v1", "Gateway", "gateways", "Namespaced"),
			crdJSON("gateway.networking.k8s.io", "v1", "HTTPRoute", "httproutes", "Namespaced"))
	}
	// The named groups and their versions, the first one preferred.
	versions := map[string][]string{}
	for gv := range served {
		if group, version, named := strings.Cut(gv, "/"); named {
			versions[group] = append(versions[group], version)
		}
	}

	switch path := r.URL.Path; {
	// Before the plain /api case, or that case answers /api and the 404 never
	// reaches client-go there.
	case a.coreNotFound && (path == "/api" || path == "/api/v1"),
		a.coreListNotFound && path == "/api/v1":
		a.notFound.add(path)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
	case path == "/api":
		fmt.Fprint(w, `{"kind":"APIVersions","versions":["v1"]}`)
	case path == "/apis":
		var list []string
		for group, vs := range versions {
			var each []string
			for _, version := range vs {
				each = append(each, fmt.Sprintf(`{"groupVersion":"%s/%s","version":%q}`, group, version, version))
			}
			list = append(list, fmt.Sprintf(`{"name":%q,"versions":[%s],"preferredVersion":%s}`,
				group, strings.Join(each, ","), each[0]))
		}
		fmt.Fprintf(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[%s]}`, strings.Join(list, ","))
	case a.batchUnread && path == "/apis/batch/v1":
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"ServiceUnavailable","code":503}`)
	case path == "/apis/apiextensions.k8s.io/v1/customresourcedefinitions":
		fmt.Fprintf(w, `{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinitionList","metadata":{},"items":[%s]}`, strings.Join(crds, ","))
	case strings.HasPrefix(path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/"):
		name := strings.TrimPrefix(path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/")
		for _, crd := range crds {
			if strings.Contains(crd, `"name":"`+name+`"`) {
				fmt.Fprint(w, crd)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
	case path == "/apis/storage.k8s.io/v1/volumeattachments":
		fmt.Fprint(w, `{"apiVersion":"storage.k8s.io/v1","kind":"VolumeAttachmentList","metadata":{},"items":[`+
			`{"apiVersion":"storage.k8s.io/v1","kind":"VolumeAttachment","metadata":{"name":"csi-abc","creationTimestamp":"2026-10-22T08:00:00Z"},`+
			`"spec":{"attacher":"driver.longhorn.io","nodeName":"node-2","source":{"persistentVolumeName":"pvc-1"}},"status":{"attached":true}}]}`)
	default:
		for gv, list := range served {
			at := "/apis/" + gv
			if !strings.Contains(gv, "/") {
				at = "/api/" + gv
			}
			if path == at {
				fmt.Fprintf(w, `{"kind":"APIResourceList","groupVersion":%q,"resources":[%s]}`, gv, strings.Join(list, ","))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
	}
}

func crdJSON(group, version, kind, plural, scope string) string {
	return fmt.Sprintf(`{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition","metadata":{"name":"%s.%s"},`+
		`"spec":{"group":%q,"scope":%q,"names":{"kind":%q,"plural":%q,"singular":%q},`+
		`"versions":[{"name":%q,"served":true,"storage":true}]}}`,
		plural, group, group, scope, kind, plural, strings.ToLower(kind), version)
}

// projectServer is a console in front of one such cluster, called prod.
func projectServer(t *testing.T, cluster apiServer) *Server {
	t.Helper()
	// 127.0.0.1 is the test server; the loopback rewrite must not send the
	// requests anywhere else.
	t.Setenv("KT_IN_CONTAINER", "0")
	served := clustertest.Serve(t, "prod", cluster)
	return serverOver(t, served.Kubeconfig)
}

func serverOver(t *testing.T, kubeconfig string) *Server {
	t.Helper()
	manager, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	server, err := New(Options{Clusters: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

// menuHeading is the heading of the menu with that label, or nil.
func menuHeading(menu api.NavMenu, label string) *api.NavGroup {
	for i := range menu.Groups {
		if menu.Groups[i].Label == label {
			return &menu.Groups[i]
		}
	}
	return nil
}

// hrefsUnder is every href under a heading, one level down included.
func hrefsUnder(group *api.NavGroup) []string {
	if group == nil {
		return nil
	}
	var out []string
	for _, item := range group.Items {
		out = append(out, item.Href)
	}
	for i := range group.Groups {
		out = append(out, hrefsUnder(&group.Groups[i])...)
	}
	return out
}

func holds(hrefs []string, href string) bool {
	for _, h := range hrefs {
		if h == href {
			return true
		}
	}
	return false
}

func readMenu(t *testing.T, server *Server) (api.NavMenu, api.NavGroup) {
	t.Helper()
	menu, fail := apiClusters{server}.Nav(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}
	custom, fail := apiClusters{server}.NavGroup(context.Background(), "prod", navKey(customNavLabel))
	if fail != nil {
		t.Fatalf("the Custom Resources heading failed: %+v", fail.Reason)
	}
	return menu, custom
}

func TestAClusterServingTheGatewayAPIListsItUnderNetworkAndNotUnderCustomResources(t *testing.T) {
	menu, custom := readMenu(t, projectServer(t, apiServer{gatewayAPI: true}))

	network := hrefsUnder(menuHeading(menu, "Network"))
	for _, href := range []string{"/c/prod/gateways.gateway.networking.k8s.io", "/c/prod/httproutes.gateway.networking.k8s.io"} {
		if !holds(network, href) {
			t.Errorf("Network does not hold %s: %v", href, network)
		}
		if holds(hrefsUnder(&custom), href) {
			t.Errorf("Custom Resources still holds %s: a kind is offered once", href)
		}
	}
	// After the thirty-six's own Network kinds, not among them.
	items := menuHeading(menu, "Network").Items
	if items[0].Text != "Services" || items[len(items)-1].Text != "HTTP routes" {
		t.Errorf("Network reads %v; want the console's kinds first and the Gateway API after", items)
	}
	// A SIG project under x-k8s.io stays where third parties are.
	if !holds(hrefsUnder(&custom), "/c/prod/clusters.cluster.x-k8s.io") {
		t.Errorf("Custom Resources lost the cluster.x-k8s.io CRD: %v", hrefsUnder(&custom))
	}
	// A native kind no CRD defines, beside the storage classes.
	if !holds(hrefsUnder(menuHeading(menu, "Storage")), "/c/prod/volumeattachments.storage.k8s.io") {
		t.Errorf("Storage does not hold VolumeAttachments: %v", hrefsUnder(menuHeading(menu, "Storage")))
	}
	if menu.Discovery != nil {
		t.Errorf("a cluster that answered says %+v", menu.Discovery)
	}
}

func TestAClusterWithoutTheGatewayAPIListsItNowhere(t *testing.T) {
	menu, custom := readMenu(t, projectServer(t, apiServer{}))

	all := append(hrefsUnder(&custom), hrefsUnder(menuHeading(menu, "Network"))...)
	for _, href := range all {
		if strings.Contains(href, "gateway.networking.k8s.io") {
			t.Errorf("%s is offered by a cluster that does not serve the Gateway API", href)
		}
	}
}

// A refused discovery is said, never drawn as a cluster serving none.
func TestARefusedDiscoveryIsSaidInTheMenusDiscoveryState(t *testing.T) {
	menu, _ := readMenu(t, projectServer(t, apiServer{gatewayAPI: true, refuse: true}))

	if menu.Discovery == nil || menu.Discovery.State != api.NavDiscoveryFailed || menu.Discovery.Detail == "" || strings.Contains(menu.Discovery.Detail, "\n") {
		t.Errorf("the menu's discovery is %+v; a menu without these kinds must say it failed, in one line", menu.Discovery)
	}
	if note := menu.Groups[0].Note; note != "" {
		t.Errorf("the Cluster heading carries the sentence %q; the state is structured, never a note", note)
	}
	if holds(hrefsUnder(menuHeading(menu, "Storage")), "/c/prod/volumeattachments.storage.k8s.io") {
		t.Error("a kind nobody could ask about is offered")
	}
}

func TestANativeKindsListOpensWithItsOwnColumns(t *testing.T) {
	server := projectServer(t, apiServer{})

	page, fail := apiReads{server}.ListResources(context.Background(), api.ListQuery{Cluster: "prod", Kind: "volumeattachments.storage.k8s.io"})
	if fail != nil {
		t.Fatalf("the list failed: %+v", fail.Reason)
	}
	if page.Label != "Volume attachments" || len(page.Rows) != 1 {
		t.Fatalf("label %q, %d rows", page.Label, len(page.Rows))
	}
	var titles []string
	for _, c := range page.Columns {
		titles = append(titles, c.Label)
	}
	if want := "Name,Attacher,Volume,Node,Attached,Age"; strings.Join(titles, ",") != want {
		t.Errorf("columns %v, want %s", titles, want)
	}
}

// What the menu does with an answer, without a cluster: each kind after the
// heading's own, a heading made where none is left, the current one open.
func TestProjectKindsJoinTheirHeadingAfterItsOwnKinds(t *testing.T) {
	groups := []navGroup{
		{Label: "Workloads", Links: []navLink{{Label: "Pods"}}},
		{Label: "Network", Links: []navLink{{Label: "Services"}}},
	}
	kinds := []customresources.Definition{
		{Group: "gateway.networking.k8s.io", Kind: "Gateway", Plural: "gateways", Namespaced: true, Section: "Network"},
		{Group: "storage.k8s.io", Kind: "CSIDriver", Plural: "csidrivers", Section: "Storage"},
	}
	got := projectNavGroups("prod", "gateways.gateway.networking.k8s.io", groups, kinds)

	if len(got) != 3 || got[2].Label != "Storage" || got[2].Links[0].Label != "CSI drivers" {
		t.Fatalf("headings %+v", got)
	}
	network := got[1]
	if len(network.Links) != 2 || network.Links[1].Label != "Gateways" || !network.Links[1].Namespaced {
		t.Errorf("Network holds %+v", network.Links)
	}
	if !network.Open || !network.Links[1].Current {
		t.Error("the heading holding the current page is not open")
	}
}

// In-cluster, every Configuration kind may be left out; a project kind filed
// there brings its heading back where kinds.go puts it, not after Storage.
func TestARecreatedHeadingGoesWhereTheBuiltInKindsPutIt(t *testing.T) {
	groups := []navGroup{{Label: "Workloads"}, {Label: "Network"}, {Label: "Access Control"}, {Label: "Storage"}}
	kinds := []customresources.Definition{
		{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicy", Plural: "validatingadmissionpolicies", Section: "Configuration"},
	}
	got := projectNavGroups("prod", "", groups, kinds)
	var labels []string
	for _, g := range got {
		labels = append(labels, g.Label)
	}
	if want := "Workloads,Network,Configuration,Access Control,Storage"; strings.Join(labels, ",") != want {
		t.Errorf("headings %v, want %s", labels, want)
	}
}

// A cluster older than 1.21 serves CronJobs at
// batch/v1beta1 only. The console reads them at batch/v1, so its menu leaves
// them out rather than offer a list that opens on an error.
func TestAClusterNotServingCronJobsAtBatchV1HasNoCronJobsEntry(t *testing.T) {
	menu, _ := readMenu(t, projectServer(t, apiServer{beforeCronJobsV1: true}))

	workloads := hrefsUnder(menuHeading(menu, "Workloads"))
	if holds(workloads, "/c/prod/cronjobs") {
		t.Errorf("Workloads offers Cron jobs on a cluster that does not serve batch/v1 cronjobs: %v", workloads)
	}
	if !holds(workloads, "/c/prod/jobs") {
		t.Errorf("Workloads lost Jobs, which batch/v1 still serves: %v", workloads)
	}
	if menu.Discovery != nil {
		t.Errorf("a cluster that answered says %+v: a kind it does not serve is not unknown", menu.Discovery)
	}
}

func TestAClusterServingCronJobsAtBatchV1HasTheEntry(t *testing.T) {
	menu, _ := readMenu(t, projectServer(t, apiServer{}))

	workloads := hrefsUnder(menuHeading(menu, "Workloads"))
	for _, href := range []string{"/c/prod/cronjobs", "/c/prod/jobs"} {
		if !holds(workloads, href) {
			t.Errorf("Workloads does not hold %s: %v", href, workloads)
		}
	}
}

// batch/v1 unread is unknown, not absent. Its kinds stay, and the
// Cluster heading says which group version could not be read.
func TestAKindWhoseGroupDiscoveryCouldNotReadStaysAndIsSaid(t *testing.T) {
	menu, _ := readMenu(t, projectServer(t, apiServer{batchUnread: true}))

	workloads := hrefsUnder(menuHeading(menu, "Workloads"))
	for _, href := range []string{"/c/prod/cronjobs", "/c/prod/jobs"} {
		if !holds(workloads, href) {
			t.Errorf("%s was hidden as if absent although batch/v1 could not be read: %v", href, workloads)
		}
	}
	if menu.Discovery == nil || menu.Discovery.State != api.NavDiscoveryFailed || !strings.Contains(menu.Discovery.Detail, "batch/v1") {
		t.Errorf("the menu's discovery is %+v; it must name batch/v1 as unread", menu.Discovery)
	}
}

func TestWithoutUnservedAddsToHiddenWithoutWritingIt(t *testing.T) {
	hidden := map[string]bool{"secrets": true}
	got := withoutUnserved(hidden, []string{"cronjobs"})
	if !got["secrets"] || !got["cronjobs"] {
		t.Errorf("got %v, want secrets and cronjobs", got)
	}
	if hidden["cronjobs"] {
		t.Error("the access review's map was written to")
	}
	if got := withoutUnserved(nil, nil); got != nil {
		t.Errorf("nothing unserved changed nil into %v", got)
	}
}

// A core group discovery could not read is unknown, not absent: the core kinds
// stay and the Cluster heading names v1. client-go reports neither 404 as an
// error. A 404 on /api leaves no core list at all; /api listing v1 and a 404 on
// /api/v1 leaves an empty v1 list. Each case is its own branch of
// emptyConsoleGroups, so both are run, and each checks its 404 was really hit.
func TestACoreGroupDiscoveryAnsweredNothingForKeepsItsKindsAndIsSaid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster apiServer
		hit     string
	}{
		{"no core list: /api answers 404", apiServer{coreNotFound: true}, "/api"},
		{"empty core list: /api/v1 answers 404", apiServer{coreListNotFound: true}, "/api/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cluster.notFound = &corePaths{}
			menu, _ := readMenu(t, projectServer(t, tc.cluster))

			if !tc.cluster.notFound.has(tc.hit) {
				t.Fatalf("discovery never got the 404 on %s this case is for", tc.hit)
			}
			top := hrefsUnder(&menu.Groups[0])
			for _, href := range []string{"/c/prod/nodes", "/c/prod/namespaces", "/c/prod/events"} {
				if !holds(top, href) {
					t.Errorf("%s left the menu although the core group was never read: %v", href, top)
				}
			}
			if !holds(hrefsUnder(menuHeading(menu, "Workloads")), "/c/prod/pods") {
				t.Error("Pods left the menu although the core group was never read")
			}
			if menu.Discovery == nil || menu.Discovery.State != api.NavDiscoveryFailed || !strings.Contains(menu.Discovery.Detail, "v1") {
				t.Errorf("the menu's discovery is %+v; it must name the core group as unread", menu.Discovery)
			}
		})
	}
}

// A discovery slower than the menu's wait is not an error: the first answer
// says it is pending, carries no sentence, and still lists the typed kinds; the
// next one, after discovery landed, lists the project's kinds and says nothing.
func TestASlowDiscoveryIsPendingNotAnErrorAndTheNextMenuHasItsKinds(t *testing.T) {
	was := projectNavWait
	projectNavWait = 100 * time.Millisecond
	t.Cleanup(func() { projectNavWait = was })
	hold := make(chan struct{})
	released := false
	release := func() {
		if !released {
			released = true
			close(hold)
		}
	}
	t.Cleanup(release)
	server := projectServer(t, apiServer{gatewayAPI: true, hold: hold})

	first, fail := apiClusters{server}.Nav(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}
	if first.Discovery == nil || first.Discovery.State != api.NavDiscoveryPending || first.Discovery.Detail == "" {
		t.Fatalf("the first menu's discovery is %+v; want pending", first.Discovery)
	}
	if note := first.Groups[0].Note; note != "" {
		t.Errorf("the Cluster heading carries %q while discovery runs", note)
	}
	if !holds(hrefsUnder(menuHeading(first, "Workloads")), "/c/prod/pods") {
		t.Error("the typed kinds are missing from a menu waiting on discovery")
	}
	if holds(hrefsUnder(menuHeading(first, "Network")), "/c/prod/gateways.gateway.networking.k8s.io") {
		t.Error("a project kind is offered before discovery said the cluster serves it")
	}

	release()
	deadline := time.Now().Add(10 * time.Second)
	for {
		next, fail := apiClusters{server}.Nav(context.Background(), "prod")
		if fail != nil {
			t.Fatalf("the menu failed: %+v", fail.Reason)
		}
		if next.Discovery == nil {
			if !holds(hrefsUnder(menuHeading(next, "Network")), "/c/prod/gateways.gateway.networking.k8s.io") {
				t.Error("discovery is complete but the Gateway API kinds are not in the menu")
			}
			if note := next.Groups[0].Note; note != "" {
				t.Errorf("the Cluster heading carries %q", note)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery never completed: %+v", next.Discovery)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
