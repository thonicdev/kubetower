package customresources

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/consolekinds"
	"github.com/thonicdev/kubetower/internal/resources"
)

// --- the group rule -----------------------------------------------------------

func TestTheProjectsGroupsAreTheIssuesRulePlusTheFourLegacyOnes(t *testing.T) {
	owned := []string{
		"", "k8s.io", "kubernetes.io",
		"gateway.networking.k8s.io", "snapshot.storage.k8s.io", "storage.k8s.io",
		"resource.k8s.io", "flowcontrol.apiserver.k8s.io", "node.kubernetes.io",
		"apps", "batch", "autoscaling", "policy",
	}
	for _, group := range owned {
		if !ProjectGroup(group) {
			t.Errorf("%q is the Kubernetes project's and the rule says it is not", group)
		}
	}
	notOwned := []string{
		// x-k8s.io is the suffix SIG projects chose to say "not the API".
		"cluster.x-k8s.io", "x-k8s.io", "kueue.x-k8s.io",
		"cert-manager.io", "argoproj.io", "k8s.keycloak.org",
		// Ending with the letters is not being under the domain.
		"notk8s.io", "acme.k8s.io.example.com", "evilkubernetes.io",
	}
	for _, group := range notOwned {
		if ProjectGroup(group) {
			t.Errorf("%q is not the Kubernetes project's and the rule says it is", group)
		}
	}
}

// The menu joins a project kind to the heading with the section's exact label,
// so every section this package can answer has to be one kinds.go declares. A
// heading renamed there fails here instead of growing a second heading.
func TestEverySectionIsAHeadingTheBuiltInKindsDeclare(t *testing.T) {
	declared := map[string]bool{}
	for _, d := range resources.Kinds() {
		declared[d.Group] = true
	}
	answers := []string{sectionOf("", "podtemplates"), sectionOf("example.k8s.io", "anything")}
	for _, s := range sections {
		answers = append(answers, s.section)
	}
	for _, n := range natives {
		answers = append(answers, sectionOf(n.group, n.resource))
	}
	for _, section := range answers {
		if !declared[section] {
			t.Errorf("section %q is not a heading kinds.go declares; the menu would draw a second heading", section)
		}
	}
}

func TestTheIssuesKindsLandInTheirNaturalSections(t *testing.T) {
	cases := []struct{ group, resource, want string }{
		{"gateway.networking.k8s.io", "gateways", "Network"},
		{"gateway.networking.k8s.io", "httproutes", "Network"},
		{"networking.k8s.io", "servicecidrs", "Network"},
		{"snapshot.storage.k8s.io", "volumesnapshots", "Storage"},
		{"storage.k8s.io", "volumeattachments", "Storage"},
		{"storage.k8s.io", "csidrivers", "Storage"},
		// "the section that already holds the webhook configurations"
		{"admissionregistration.k8s.io", "validatingadmissionpolicies", "Configuration"},
		{"resource.k8s.io", "resourceclaims", "Configuration"},
		{"certificates.k8s.io", "certificatesigningrequests", "Access Control"},
		{"apps", "controllerrevisions", "Workloads"},
		{"", "podtemplates", "Workloads"},
		// A group the project publishes later has somewhere to go.
		{"brand-new.k8s.io", "things", "Configuration"},
	}
	for _, c := range cases {
		if got := sectionOf(c.group, c.resource); got != c.want {
			t.Errorf("%s.%s is filed under %q, want %q", c.resource, c.group, got, c.want)
		}
	}
}

func TestAProjectKindIsTitledAsTheConsoleTitlesItsOwn(t *testing.T) {
	cases := []struct{ kind, plural, want string }{
		{"Gateway", "gateways", "Gateways"},
		{"HTTPRoute", "httproutes", "HTTP routes"},
		{"GRPCRoute", "grpcroutes", "GRPC routes"},
		{"GatewayClass", "gatewayclasses", "Gateway classes"},
		{"BackendTLSPolicy", "backendtlspolicies", "Backend TLS policies"},
		{"VolumeAttachment", "volumeattachments", "Volume attachments"},
		{"CSIDriver", "csidrivers", "CSI drivers"},
		{"CSIStorageCapacity", "csistoragecapacities", "CSI storage capacities"},
		{"ServiceCIDR", "servicecidrs", "Service CIDRs"},
		{"IPAddress", "ipaddresses", "IP addresses"},
		{"APIService", "apiservices", "API services"},
		{"ValidatingAdmissionPolicyBinding", "validatingadmissionpolicybindings", "Validating admission policy bindings"},
		{"VolumeSnapshotContent", "volumesnapshotcontents", "Volume snapshot contents"},
		{"DNSEndpoint", "dnsendpoints", "DNS endpoints"},
		// Digits stay with their word.
		{"IPv6Pool", "ipv6pools", "IPv6 pools"},
		{"S3Bucket", "s3buckets", "S3 buckets"},
		{"ABCd", "abcds", "ABCds"},
		// A plural that does not spell the kind keeps the author's word.
		{"Widget", "gizmos", "Gizmos"},
		{"", "things", "Things"},
	}
	for _, c := range cases {
		if got := kindTitle(c.kind, c.plural); got != c.want {
			t.Errorf("kindTitle(%q, %q) = %q, want %q", c.kind, c.plural, got, c.want)
		}
	}
}

// --- the native table ---------------------------------------------------------

func TestTheNativeBandsAreTheBuiltInKindsBands(t *testing.T) {
	declared, _ := resources.Lookup("storageclasses")
	reconciling, _ := resources.Lookup("leases")
	if declaredRefresh != declared.Refresh() || reconcilingRefresh != reconciling.Refresh() {
		t.Fatalf("native bands %v and %v, kinds.go's %v and %v: a native kind would refresh on a band of its own",
			declaredRefresh, reconcilingRefresh, declared.Refresh(), reconciling.Refresh())
	}
}

func TestTheNativeTableHoldsOnlyProjectKindsTheMenuOffers(t *testing.T) {
	console := consoleKinds()
	seen := map[string]bool{}
	for _, n := range natives {
		key := n.resource + "." + n.group
		if seen[key] {
			t.Errorf("%s is in the table twice", key)
		}
		seen[key] = true
		if !ProjectGroup(n.group) {
			t.Errorf("%s is not in a project group", key)
		}
		if console[schema.GroupResource{Group: n.group, Resource: n.resource}] {
			t.Errorf("%s is one of the thirty-six and would be listed twice", key)
		}
		if reason, out := excluded(n.group, n.resource); out {
			t.Errorf("%s is excluded (%s) and has columns nobody will see", key, reason)
		}
		if len(n.versions) == 0 || n.refresh == 0 {
			t.Errorf("%s names no version or no refresh band", key)
		}
		for _, r := range compile(n.columns) {
			if r.path == nil {
				t.Errorf("%s: column %q does not parse: %s", key, r.column.Name, r.column.JSONPath)
			}
		}
	}
}

// --- discovery ----------------------------------------------------------------

func listable(name, kind string, namespaced bool) metav1.APIResource {
	return metav1.APIResource{Name: name, Kind: kind, Namespaced: namespaced, Verbs: []string{"get", "list", "watch"}}
}

func served(groupVersion string, resources ...metav1.APIResource) *metav1.APIResourceList {
	return &metav1.APIResourceList{GroupVersion: groupVersion, APIResources: resources}
}

// aCluster is what a 1.37 API server says of itself, cut to the cases: the
// console's own kinds, native project kinds, the ones excluded and the ones
// that cannot be listed, a SIG project under x-k8s.io and a vendor's CRD.
func aCluster() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		served("v1",
			listable("pods", "Pod", true),
			metav1.APIResource{Name: "pods/log", Kind: "Pod", Namespaced: true, Verbs: []string{"get"}},
			listable("podtemplates", "PodTemplate", true),
			listable("componentstatuses", "ComponentStatus", false),
			metav1.APIResource{Name: "bindings", Kind: "Binding", Namespaced: true, Verbs: []string{"create"}},
		),
		served("apps/v1", listable("deployments", "Deployment", true), listable("controllerrevisions", "ControllerRevision", true)),
		served("storage.k8s.io/v1", listable("storageclasses", "StorageClass", false), listable("volumeattachments", "VolumeAttachment", false)),
		served("authentication.k8s.io/v1", metav1.APIResource{Name: "tokenreviews", Kind: "TokenReview", Verbs: []string{"create"}}),
		served("events.k8s.io/v1", listable("events", "Event", true)),
		served("metrics.k8s.io/v1beta1", listable("pods", "PodMetrics", true)),
		served("apiextensions.k8s.io/v1", listable("customresourcedefinitions", "CustomResourceDefinition", false)),
		served("cluster.x-k8s.io/v1beta1", listable("clusters", "Cluster", true)),
		served("cert-manager.io/v1", listable("certificates", "Certificate", true)),
	}
}

// withGatewayAPI adds the Gateway API's standard channel, at two versions as
// its CRDs serve it.
func withGatewayAPI(lists []*metav1.APIResourceList) []*metav1.APIResourceList {
	gateway := []metav1.APIResource{
		listable("gatewayclasses", "GatewayClass", false),
		listable("gateways", "Gateway", true),
		listable("httproutes", "HTTPRoute", true),
	}
	return append(lists,
		served("gateway.networking.k8s.io/v1", gateway...),
		served("gateway.networking.k8s.io/v1beta1", gateway...),
	)
}

func fakeDiscovery(lists []*metav1.APIResourceList) *fakediscovery.FakeDiscovery {
	return &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{Resources: lists}}
}

func projectRegistry(t *testing.T, d discovery.DiscoveryInterface, objects ...runtime.Object) (*Registry, *cluster.Client) {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		crdGVR: "CustomResourceDefinitionList",
		{Group: "storage.k8s.io", Version: "v1", Resource: "volumeattachments"}:   "VolumeAttachmentList",
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}: "GatewayList",
	}
	fake := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	registry := NewRegistry()
	registry.newDiscovery = func(*cluster.Client) (discovery.DiscoveryInterface, error) { return d, nil }
	return registry, &cluster.Client{Context: "test", Config: &rest.Config{}, Dynamic: fake}
}

func sectionsOf(kinds []Definition) map[string]string {
	out := map[string]string{}
	for _, d := range kinds {
		out[d.Slug()] = d.Section
	}
	return out
}

func TestAClusterServingTheGatewayAPIOffersGatewaysAndHTTPRoutesUnderNetwork(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(withGatewayAPI(aCluster())))

	found, err := registry.ProjectKinds(t.Context(), client)
	if err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}
	got := sectionsOf(found.Kinds)
	for _, slug := range []string{"gateways.gateway.networking.k8s.io", "httproutes.gateway.networking.k8s.io", "gatewayclasses.gateway.networking.k8s.io"} {
		if got[slug] != SectionNetwork {
			t.Errorf("%s is filed under %q, want Network (%v)", slug, got[slug], got)
		}
	}
	for _, d := range found.Kinds {
		if d.Group == "gateway.networking.k8s.io" && d.Version != "v1" {
			t.Errorf("%s is read at %s; the group's preferred version is v1", d.Slug(), d.Version)
		}
	}
}

func TestAClusterWithoutTheGatewayAPIOffersNoGateway(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(aCluster()))

	found, err := registry.ProjectKinds(t.Context(), client)
	if err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}
	for _, d := range found.Kinds {
		if d.Group == "gateway.networking.k8s.io" {
			t.Errorf("%s is offered by a cluster that does not serve the Gateway API", d.Slug())
		}
	}
}

func TestOnlyTheProjectsListableKindsBeyondTheConsolesOwnAreOffered(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(aCluster()))

	found, err := registry.ProjectKinds(t.Context(), client)
	if err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}
	got := sectionsOf(found.Kinds)
	want := map[string]string{
		// Native, beside the console's own.
		"volumeattachments.storage.k8s.io": SectionStorage,
		"controllerrevisions.apps":         SectionWorkloads,
		"podtemplates":                     SectionWorkloads,
		// Beside the APIServices: the API's own extensions.
		"customresourcedefinitions.apiextensions.k8s.io": SectionConfiguration,
	}
	for slug, section := range want {
		if got[slug] != section {
			t.Errorf("%s is filed under %q, want %q", slug, got[slug], section)
		}
	}
	for slug := range got {
		if _, ok := want[slug]; !ok {
			// pods and deployments are the console's; tokenreviews and bindings
			// cannot be listed; events.k8s.io, metrics.k8s.io and
			// componentstatuses are excluded with a reason; cluster.x-k8s.io and
			// cert-manager.io are not the project's; pods/log is a subresource.
			t.Errorf("%s is offered and should not be", slug)
		}
	}
	if len(found.Unread) != 0 {
		t.Errorf("a cluster that answered in full reports unread groups: %v", found.Unread)
	}
}

func TestANativeKindCarriesItsColumnsOnlyAtAVersionTheyWereCheckedAgainst(t *testing.T) {
	lists := aCluster()
	lists = append(lists, served("resource.k8s.io/v1alpha3", listable("resourceslices", "ResourceSlice", false)))
	snapshot := buildProject(nil, lists, nil)

	va := snapshot.bySlug["volumeattachments.storage.k8s.io"]
	if len(va.Columns) == 0 || !va.native || va.Refresh != reconcilingRefresh {
		t.Errorf("VolumeAttachments at v1 lost the table's columns or band: %+v", va)
	}
	alpha := snapshot.bySlug["resourceslices.resource.k8s.io"]
	if !alpha.native || len(alpha.Columns) != 0 {
		t.Errorf("ResourceSlices at v1alpha3 carry columns checked against other versions: %+v", alpha.Columns)
	}
}

func TestTheVersionIsTheOneTheDrawerOpens(t *testing.T) {
	if got := versionFor("v1", []string{"v1beta1", "v1"}); got != "v1" {
		t.Errorf("the preferred version serves the kind and %q was chosen", got)
	}
	// A kind the preferred version does not carry: its highest served one.
	if got := versionFor("v1", []string{"v1alpha1", "v1beta1"}); got != "v1beta1" {
		t.Errorf("want the highest served version, got %q", got)
	}
}

// A group discovery could not read is unknown, not absent.
func TestAGroupDiscoveryCouldNotReadIsNamedRatherThanTakenForAbsent(t *testing.T) {
	failed := map[schema.GroupVersion]error{
		{Group: "snapshot.storage.k8s.io", Version: "v1"}: errors.New("the backend did not answer"),
		// Excluded anyway, and the usual one to be down: not named.
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("metrics-server is down"),
		// Not the project's.
		{Group: "cert-manager.io", Version: "v1"}: errors.New("down"),
	}
	snapshot := buildProject(nil, aCluster(), failed)
	if got := snapshot.kinds.Unread; len(got) != 1 || got[0] != "snapshot.storage.k8s.io/v1" {
		t.Fatalf("Unread = %v, want the snapshot group alone", got)
	}
}

func TestADiscoveryThatWasRefusedIsAnErrorAndNotAnEmptyAnswer(t *testing.T) {
	d := fakeDiscovery(withGatewayAPI(aCluster()))
	d.AddReactor("get", "group", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("discovery is not permitted"))
	})
	registry, client := projectRegistry(t, d)

	found, err := registry.ProjectKinds(t.Context(), client)
	if err == nil {
		t.Fatalf("a refused discovery answered %d kinds and no error: the menu would read it as a cluster serving none", len(found.Kinds))
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("the refusal was not carried: %v", err)
	}
}

func TestDiscoveryIsAskedOnceForSeveralMenus(t *testing.T) {
	d := fakeDiscovery(aCluster())
	registry, client := projectRegistry(t, d)
	for range 3 {
		if _, err := registry.ProjectKinds(t.Context(), client); err != nil {
			t.Fatalf("ProjectKinds: %v", err)
		}
	}
	asked := 0
	for _, action := range d.Actions() {
		if action.GetResource().Resource == "group" {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("discovery was asked %d times for three menus within the TTL", asked)
	}
}

// --- resolving a project kind for its list -------------------------------------

const volumeAttachment = `{
  "apiVersion": "storage.k8s.io/v1",
  "kind": "VolumeAttachment",
  "metadata": {"name": "csi-abc", "creationTimestamp": "2026-10-22T08:00:00Z"},
  "spec": {"attacher": "driver.longhorn.io", "nodeName": "node-2", "source": {"persistentVolumeName": "pvc-1"}},
  "status": {"attached": true}
}`

func TestANativeKindNoCRDDefinesIsListedWithItsOwnColumns(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(aCluster()), object(t, volumeAttachment))

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "volumeattachments.storage.k8s.io", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing VolumeAttachments: %v", err)
	}
	if table.Title != "Volume attachments" || table.Namespaced {
		t.Errorf("title %q, namespaced %v", table.Title, table.Namespaced)
	}
	var titles []string
	for _, c := range table.Columns {
		titles = append(titles, c.Title)
	}
	if want := "Name,Attacher,Volume,Node,Attached,Age"; strings.Join(titles, ",") != want {
		t.Fatalf("columns %v, want %s", titles, want)
	}
	if len(table.Rows) != 1 {
		t.Fatalf("%d rows, want 1", len(table.Rows))
	}
	var cells []string
	for _, c := range table.Rows[0].Cells[:5] {
		cells = append(cells, c.Text)
	}
	if want := "csi-abc,driver.longhorn.io,pvc-1,node-2,true"; strings.Join(cells, ",") != want {
		t.Errorf("cells %v, want %s", cells, want)
	}
}

// Longhorn serves its own `volumeattachments`: the bare plural stays the CRD's,
// and the project's kind is reached by its slug.
func TestTheBarePluralStaysTheCRDsAndTheSlugReachesTheProjectsKind(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(aCluster()),
		object(t, strings.ReplaceAll(strings.ReplaceAll(policyCRD("longhorn.io"), "policies", "volumeattachments"), `"Policy"`, `"VolumeAttachment"`)))

	bare, err := registry.Definition(t.Context(), client, "volumeattachments")
	if err != nil || bare.Group != "longhorn.io" {
		t.Errorf("the bare plural resolved to %+v (%v), want Longhorn's CRD", bare, err)
	}
	slug, err := registry.Definition(t.Context(), client, "volumeattachments.storage.k8s.io")
	if err != nil || slug.Group != "storage.k8s.io" {
		t.Errorf("the slug resolved to %+v (%v), want the API server's own", slug, err)
	}
}

func TestANativeKindStillOpensWhenTheCRDsCannotBeListed(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(withGatewayAPI(aCluster())))
	refuse := func(client *cluster.Client) {
		client.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "customresourcedefinitions",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "customresourcedefinitions"}, "", errors.New("no"))
			})
	}
	refuse(client)

	if _, err := registry.Definition(t.Context(), client, "volumeattachments.storage.k8s.io"); err != nil {
		t.Errorf("a native kind needs no CRD and did not resolve: %v", err)
	}
	// A Gateway is a CRD, and the menu offers it from discovery whatever the
	// CRD list says: refused that list, it opens without printer columns
	// rather than failing on every click.
	gateway, err := registry.Definition(t.Context(), client, "gateways.gateway.networking.k8s.io")
	if err != nil || gateway.Group != "gateway.networking.k8s.io" || len(gateway.Columns) != 0 {
		t.Errorf("a Gateway with the CRD list refused resolved to %+v (%v)", gateway, err)
	}
}

// A CRD list that failed for a reason that may pass is not refused: a table
// without its columns would draw a transient failure as a permanent shape.
func TestACRDServedProjectKindWaitsForTheCRDsWhenTheirListFailed(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(withGatewayAPI(aCluster())))
	client.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "customresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("etcd is not answering")
		})

	_, err := registry.Definition(t.Context(), client, "gateways.gateway.networking.k8s.io")
	if !apierrors.IsServiceUnavailable(err) {
		t.Errorf("got %v, want the CRD list's own failure", err)
	}
}

func TestAProjectSlugWhoseDiscoveryFailedSaysSoRatherThanUnknown(t *testing.T) {
	d := fakeDiscovery(aCluster())
	d.AddReactor("get", "group", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection reset")
	})
	registry, client := projectRegistry(t, d)

	_, err := registry.Definition(t.Context(), client, "volumeattachments.storage.k8s.io")
	if err == nil || errors.Is(err, ErrUnknownKind) {
		t.Fatalf("got %v; a kind nobody could ask about is not an unknown kind", err)
	}
	// A third-party name is untouched: unknown is still the catalog's word.
	_, err = registry.Definition(t.Context(), client, "widgets.example.io")
	if !errors.Is(err, ErrUnknownKind) {
		t.Errorf("a vendor's unknown kind came back as %v", err)
	}
}

func TestOnlyTheProjectsCRDsAreTitledLikeTheConsolesKinds(t *testing.T) {
	gateway := Definition{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Plural: "httproutes"}
	vendor := Definition{Group: "cert-manager.io", Kind: "CertificateRequest", Plural: "certificaterequests"}
	if gateway.Title() != "HTTP routes" || vendor.Title() != "CertificateRequests" {
		t.Errorf("titles %q and %q", gateway.Title(), vendor.Title())
	}
}

// --- the discovery cache's guards ------------------------------------------------

// blockedDiscovery answers once release is closed, and says on entered when a
// call has started.
func blockedDiscovery(lists []*metav1.APIResourceList) (*fakediscovery.FakeDiscovery, chan struct{}, chan struct{}) {
	d := fakeDiscovery(lists)
	release, entered := make(chan struct{}), make(chan struct{}, 8)
	d.PrependReactor("get", "group", func(k8stesting.Action) (bool, runtime.Object, error) {
		entered <- struct{}{}
		<-release
		return false, nil, nil
	})
	return d, release, entered
}

func hasGateway(kinds ProjectKinds) bool {
	for _, d := range kinds.Kinds {
		if d.Group == "gateway.networking.k8s.io" {
			return true
		}
	}
	return false
}

// Every caller waits on its own context, the one that starts the fetch
// included: if that caller ran the fetch itself, the menu's three seconds would
// bound nobody who mattered and a hanging discovery would cost 30 s per draw.
func TestTheCallerThatStartsDiscoveryWaitsOnlyAsLongAsItsOwnContext(t *testing.T) {
	d, release, entered := blockedDiscovery(aCluster())
	registry, client := projectRegistry(t, d)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := registry.ProjectKinds(ctx, client)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the caller's own deadline", err)
	}
	if !errors.Is(err, ErrDiscoveryPending) {
		t.Errorf("got %v, want it told apart as pending, not as a discovery failure", err)
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Errorf("the first caller waited %v on a discovery that does not answer", waited)
	}
	<-entered
}

// A kubeconfig rebuilt while discovery was on the network: what came back
// describes the cluster the old config pointed at and is neither held nor
// handed to the caller asking through the new one.
func TestAnAnswerReadThroughAReplacedConfigIsNotServed(t *testing.T) {
	old, release, entered := blockedDiscovery(withGatewayAPI(aCluster()))
	fresh := fakeDiscovery(aCluster())
	registry, before := projectRegistry(t, nil)
	after := &cluster.Client{Context: before.Context, Config: &rest.Config{}, Dynamic: before.Dynamic}
	registry.newDiscovery = func(c *cluster.Client) (discovery.DiscoveryInterface, error) {
		if c == before {
			return old, nil
		}
		return fresh, nil
	}

	// The old config's caller starts the fetch and gives up before it lands.
	ctx, cancel := context.WithCancel(t.Context())
	gaveUp := make(chan struct{})
	go func() {
		defer close(gaveUp)
		_, _ = registry.ProjectKinds(ctx, before)
	}()
	<-entered

	answer := make(chan ProjectKinds, 1)
	go func() {
		got, err := registry.ProjectKinds(t.Context(), after)
		if err != nil {
			t.Errorf("ProjectKinds through the new config: %v", err)
		}
		answer <- got
	}()
	// Let the new caller reach the fetch in flight, then end the old one.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-gaveUp
	close(release)

	if got := <-answer; hasGateway(got) {
		t.Fatal("the new config was served what the old config's cluster serves")
	}
	if held, err := registry.ProjectKinds(t.Context(), after); err != nil || hasGateway(held) {
		t.Errorf("the old config's answer was held (%v)", err)
	}
}

// A renewal that lands after something newer is the older of two answers.
func TestARenewalOvertakenByANewerAnswerIsDropped(t *testing.T) {
	d := fakeDiscovery(aCluster())
	registry, client := projectRegistry(t, d)
	registry.renewAfter = 0 // every held answer is renewed behind its caller
	if _, err := registry.ProjectKinds(t.Context(), client); err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}

	release, entered := make(chan struct{}), make(chan struct{}, 1)
	d.PrependReactor("get", "group", func(k8stesting.Action) (bool, runtime.Object, error) {
		entered <- struct{}{}
		<-release
		return false, nil, nil
	})
	if _, err := registry.ProjectKinds(t.Context(), client); err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}
	<-entered

	// A newer answer lands while the renewal is on the network.
	newer := &projectSnapshot{bySlug: map[string]Definition{}}
	s := registry.slot(client.Context)
	s.mu.Lock()
	s.projectHeld, s.projectAt = newer, time.Now().Add(time.Millisecond)
	s.mu.Unlock()
	close(release)

	for deadline := time.Now().Add(5 * time.Second); ; {
		s.mu.Lock()
		renewing, held := s.projectRenewing, s.projectHeld
		s.mu.Unlock()
		if !renewing {
			if held != newer {
				t.Fatal("the renewal overwrote the newer answer")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the renewal never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A third-party kind cannot be in what discovery said about the project, so
// looking one up does not ask discovery: the extras look up Argo CD's and
// Flux's kinds, and each lookup would otherwise wait on a failing discovery.
func TestAThirdPartyLookupDoesNotWaitOnDiscovery(t *testing.T) {
	d := fakeDiscovery(aCluster())
	registry, client := projectRegistry(t, d)
	// A bare name is not here: it may be a core kind native.go has no row for,
	// which the menu offers and which must open, so it is asked
	// (TestABareCoreKindOpensWithoutAMenuFirst).
	for _, kind := range []string{"applications.argoproj.io", "helmreleases.helm.toolkit.fluxcd.io", "widgets.example.io"} {
		if _, err := registry.Definition(t.Context(), client, kind); !errors.Is(err, ErrUnknownKind) {
			t.Errorf("%s: %v, want unknown", kind, err)
		}
	}
	if n := len(d.Actions()); n != 0 {
		t.Errorf("discovery was asked %d times for third-party kinds", n)
	}
}

// withACoreKindNobodyTyped adds a core kind native.go has no row for, as a
// later Kubernetes release could serve one.
func withACoreKindNobodyTyped(lists []*metav1.APIResourceList) []*metav1.APIResourceList {
	for _, list := range lists {
		if list.GroupVersion == "v1" {
			list.APIResources = append(list.APIResources, listable("widgetclaims", "WidgetClaim", true))
		}
	}
	return lists
}

// Every kind the menu offers opens, the core group's
// included: a core kind native.go has no row for is offered, at its bare
// plural, and that link resolves - with Name, Namespace and Age, as any kind
// with no columns chosen for it.
func TestEveryKindTheMenuOffersResolves(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(withACoreKindNobodyTyped(withGatewayAPI(aCluster()))))

	found, err := registry.ProjectKinds(t.Context(), client)
	if err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}
	offered := map[string]bool{}
	for _, d := range found.Kinds {
		offered[d.Slug()] = true
		got, err := registry.Definition(t.Context(), client, d.Slug())
		if err != nil || got.Group != d.Group || got.Plural != d.Plural {
			t.Errorf("the menu offers %s and it resolves to %s/%s (%v)", d.Slug(), got.Group, got.Plural, err)
		}
	}
	if !offered["widgetclaims"] || !offered["podtemplates"] {
		t.Fatalf("offered %v, want both core kinds, the one native.go names and the one it does not", offered)
	}
	widgets, err := registry.Definition(t.Context(), client, "widgetclaims")
	if err != nil || len(widgets.Columns) != 0 || widgets.Kind != "WidgetClaim" {
		t.Errorf("widgetclaims resolved to %+v (%v), want the core kind with no chosen columns", widgets, err)
	}
}

// A bare core kind opens whether or not a menu has read discovery first: a
// bookmark opened as the first page after a restart resolves as the link the
// menu draws a moment later. A bare name nothing serves is still unknown.
func TestABareCoreKindOpensWithoutAMenuFirst(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(withACoreKindNobodyTyped(aCluster())))
	got, err := registry.Definition(t.Context(), client, "widgetclaims")
	if err != nil || got.Group != "" || got.Kind != "WidgetClaim" {
		t.Errorf("widgetclaims resolved to %+v (%v), want the core kind", got, err)
	}
	if _, err := registry.Definition(t.Context(), client, "widgets"); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("a bare name nothing serves: %v, want unknown", err)
	}
}

// The menu links each kind at its Slug (internal/web, projectnav.go) and the
// thirty-six at their bare plural; an extra links a kind through
// extension.KindSegment, which is consolekinds.Segment. The two must agree on
// every kind, or a row an extra draws opens a page no menu row holds - and the
// Custom Resources heading opens instead.
func TestAnExtraLinksEveryKindWhereTheMenuDoes(t *testing.T) {
	registry, client := projectRegistry(t, fakeDiscovery(withGatewayAPI(aCluster())))
	found, err := registry.ProjectKinds(t.Context(), client)
	if err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}
	for _, d := range found.Kinds {
		if got := consolekinds.Segment(d.Group, d.Plural); got != d.Slug() {
			t.Errorf("the menu links %s and an extra %s", d.Slug(), got)
		}
	}
	for _, d := range resources.Kinds() {
		q, _ := d.ListQuestion()
		if got := consolekinds.Segment(q.Attrs.Group, q.Attrs.Resource); got != d.Kind {
			t.Errorf("the menu links %s and an extra %s", d.Kind, got)
		}
	}
	// Outside the menu the spelling is no promise: an excluded project kind is
	// spelled, and Definition opens nothing there. That is why an extra asks
	// Lookup before it links a row (extras/argocd, pages.path).
	for _, k := range []schema.GroupResource{{Group: "events.k8s.io", Resource: "events"}, {Group: "metrics.k8s.io", Resource: "pods"}, {Resource: "componentstatuses"}} {
		segment := consolekinds.Segment(k.Group, k.Resource)
		if _, err := registry.Definition(t.Context(), client, segment); !errors.Is(err, ErrUnknownKind) {
			t.Errorf("%s, outside the menu, resolved (%v); an extra linking it would be told otherwise", segment, err)
		}
	}
	// And a vendor's kind, which the Custom Resources heading links by slug.
	vendor := Definition{Group: "cert-manager.io", Plural: "certificates"}
	if got := consolekinds.Segment(vendor.Group, vendor.Plural); got != vendor.Slug() {
		t.Errorf("the menu links %s and an extra %s", vendor.Slug(), got)
	}
}

// A cluster older than 1.21 serves CronJobs at
// batch/v1beta1 only, and the console reads them at batch/v1. Its menu must
// not offer a kind that would open on an error.
func TestAConsoleKindTheClusterDoesNotServeIsUnserved(t *testing.T) {
	old := append(aCluster(),
		served("batch/v1", listable("jobs", "Job", true)),
		served("batch/v1beta1", listable("cronjobs", "CronJob", true)),
	)
	snapshot := buildProject(nil, old, nil)
	if !slices.Contains(snapshot.kinds.Unserved, "cronjobs") {
		t.Errorf("Unserved = %v, want cronjobs: the cluster serves it at batch/v1beta1 only", snapshot.kinds.Unserved)
	}
	for _, kind := range []string{"jobs", "pods", "deployments", "storageclasses"} {
		if slices.Contains(snapshot.kinds.Unserved, kind) {
			t.Errorf("%s is served and still Unserved: %v", kind, snapshot.kinds.Unserved)
		}
	}
}

func TestAConsoleKindTheClusterServesIsNotUnserved(t *testing.T) {
	current := append(aCluster(),
		served("batch/v1", listable("jobs", "Job", true), listable("cronjobs", "CronJob", true)),
	)
	snapshot := buildProject(nil, current, nil)
	if slices.Contains(snapshot.kinds.Unserved, "cronjobs") || slices.Contains(snapshot.kinds.Unserved, "jobs") {
		t.Errorf("Unserved = %v: batch/v1 serves both jobs and cronjobs", snapshot.kinds.Unserved)
	}
}

// batch/v1 unread is unknown, not absent - its kinds stay offered and
// the group version is named.
func TestAConsoleKindWhoseGroupCouldNotBeReadIsNamedNotUnserved(t *testing.T) {
	failed := map[schema.GroupVersion]error{
		{Group: "batch", Version: "v1"}: errors.New("the backend did not answer"),
	}
	snapshot := buildProject(nil, aCluster(), failed)
	for _, kind := range []string{"jobs", "cronjobs"} {
		if slices.Contains(snapshot.kinds.Unserved, kind) {
			t.Errorf("%s is Unserved although batch/v1 could not be read: %v", kind, snapshot.kinds.Unserved)
		}
	}
	if !slices.Contains(snapshot.kinds.Unread, "batch/v1") {
		t.Errorf("Unread = %v, want batch/v1 named", snapshot.kinds.Unread)
	}
}

func TestADiscoveryOfAnOldClusterReportsCronJobsUnserved(t *testing.T) {
	old := append(aCluster(),
		served("batch/v1", listable("jobs", "Job", true)),
		served("batch/v1beta1", listable("cronjobs", "CronJob", true)),
	)
	registry, client := projectRegistry(t, fakeDiscovery(old))
	found, err := registry.ProjectKinds(t.Context(), client)
	if err != nil {
		t.Fatalf("ProjectKinds: %v", err)
	}
	if !slices.Contains(found.Unserved, "cronjobs") {
		t.Errorf("Unserved = %v, want cronjobs", found.Unserved)
	}
}

// A proxy that 404s /api leaves client-go with an empty core list and no
// error. The core kinds are unknown then, not absent: none is Unserved, and v1
// is named.
func TestAnEmptyCoreListIsUnreadNotUnserved(t *testing.T) {
	for name, core := range map[string][]*metav1.APIResourceList{
		"an empty v1 list": {served("v1")},
		"no v1 list":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			lists := append(core,
				served("apps/v1", listable("deployments", "Deployment", true)),
				served("batch/v1", listable("jobs", "Job", true), listable("cronjobs", "CronJob", true)),
			)
			snapshot := buildProject(nil, lists, nil)
			for _, kind := range []string{"pods", "services", "nodes", "namespaces", "events"} {
				if slices.Contains(snapshot.kinds.Unserved, kind) {
					t.Errorf("%s is Unserved on a cluster whose core group answered nothing: %v", kind, snapshot.kinds.Unserved)
				}
			}
			if !slices.Contains(snapshot.kinds.Unread, "v1") {
				t.Errorf("Unread = %v, want v1 named", snapshot.kinds.Unread)
			}
			// A group that answered keeps the rule: storage.k8s.io/v1 has no
			// list here, so StorageClasses are not served.
			if !slices.Contains(snapshot.kinds.Unserved, "storageclasses") {
				t.Errorf("Unserved = %v, want storageclasses: no storage.k8s.io/v1 list", snapshot.kinds.Unserved)
			}
		})
	}
}

// The same for a named group the console reads: a list with nothing in it is
// unread, never a group that serves none of its kinds.
func TestAnEmptyListOfAConsoleGroupIsUnreadNotUnserved(t *testing.T) {
	lists := append(aCluster(), served("batch/v1"))
	snapshot := buildProject(nil, lists, nil)
	for _, kind := range []string{"jobs", "cronjobs"} {
		if slices.Contains(snapshot.kinds.Unserved, kind) {
			t.Errorf("%s is Unserved although batch/v1 answered nothing: %v", kind, snapshot.kinds.Unserved)
		}
	}
	if !slices.Contains(snapshot.kinds.Unread, "batch/v1") {
		t.Errorf("Unread = %v, want batch/v1 named", snapshot.kinds.Unread)
	}
}
