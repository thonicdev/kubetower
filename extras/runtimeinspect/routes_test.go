package runtimeinspect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// The extension as the console knows it: its key, its label, off by default
// like every extra (nothing here says otherwise), four GETs and fifteen POSTs.
func TestTheExtensionIsTheRuntimeInspector(t *testing.T) {
	x := New()
	if x.Key() != "runtime" || x.Label() != "Runtime Inspector" || !slices.Equal(x.Legacy(), []string{"jvm"}) {
		t.Errorf("key %q, label %q, legacy %v", x.Key(), x.Label(), x.Legacy())
	}
	if x.Summary() == "" || x.Needs() == "" {
		t.Error("the catalog entry is empty")
	}
	routes := x.Routes(newHost(t))
	if len(routes) != 19 {
		t.Fatalf("%d routes, want the inventory, the column, one pod, inspect, reveal, threads, history, histogram, the heap dump, the recording, the jcmd probe, the three pprof routes, the three Node.js routes and the two Python routes", len(routes))
	}
	// The reads of the spec and of metrics are GETs. Every route under a
	// container runs jcmd, opens a tunnel or both: a POST, so the console's
	// request header and origin check hold it and a cross-site link cannot
	// reach it (#82).
	for _, r := range routes {
		want := http.MethodPost
		switch r.Pattern {
		case "/{$}", "/column", "/pod/{namespace}/{name}", "/pod/{namespace}/{name}/history":
			want = http.MethodGet
		}
		if r.Method != want {
			t.Errorf("route %s answers %q, want %q", r.Pattern, r.Method, want)
		}
	}
}

func shop(t *testing.T) *fakeHost {
	return newHost(t,
		pod("shop", "billing-0",
			memory(corev1.Container{Name: "app", Image: "eclipse-temurin:21-jre", Args: []string{"-Xmx2g", "-jar", "billing.jar"}}, "1Gi", "2Gi"),
			memory(corev1.Container{Name: "proxy", Image: "envoyproxy/envoy:v1.30"}, "64Mi", "128Mi")),
		pod("shop", "web-0", corev1.Container{Name: "web", Image: "nginx:1.27"}),
		pod("data", "kafka-0", memory(corev1.Container{Name: "kafka", Image: "bitnami/kafka:3.7",
			Env: env("KAFKA_HEAP_OPTS", "-Xmx1g")}, "", "4Gi")),
	)
}

// One pod: every container, the Java one with its hints, heap and risk, the
// other with its limit and nothing invented. Read by name, through the
// reviewed list, in that namespace.
func TestOnePodAnswersEveryContainer(t *testing.T) {
	h := shop(t)
	rec := serve(t, h, "/x/kind/runtime/pod/shop/billing-0")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body)
	}
	got := decode[podAnswer](t, rec)
	if got.Cluster != "kind" || got.Namespace != "shop" || got.Name != "billing-0" || !got.Java || len(got.Containers) != 2 {
		t.Fatalf("answer = %+v", got)
	}
	app, proxy := got.Containers[0], got.Containers[1]
	if !app.Java || len(app.Hints) != 2 || app.Heap == nil || *app.Heap.Bytes != 2*gi || app.Risk.Level != riskError ||
		app.Limit.Text != "2Gi" || app.Request.Text != "1Gi" {
		t.Errorf("app = %+v", app)
	}
	if proxy.Java || proxy.Heap != nil || proxy.Risk != nil || proxy.Limit == nil || len(proxy.Hints) != 0 {
		t.Errorf("proxy = %+v", proxy)
	}
	if len(h.queries) != 1 || h.queries[0].Namespace != "shop" || h.queries[0].FieldSelector != "metadata.name=billing-0" {
		t.Errorf("queries = %+v, want one list of the pod by name in its namespace", h.queries)
	}
}

// The JSON names what the page reads: null where nothing is known, never a
// zero that reads as a measurement.
func TestThePodAnswerSaysNullForWhatIsNotKnown(t *testing.T) {
	h := newHost(t, pod("data", "zk-0", corev1.Container{Name: "zk", Image: "zookeeper:3.9"}))
	body := serve(t, h, "/x/kind/runtime/pod/data/zk-0").Body.String()
	for _, want := range []string{`"limit":null`, `"request":null`, `"bytes":null`, `"from":"nolimit"`, `"level":"nolimit"`, `"percent":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("answer does not carry %s: %s", want, body)
		}
	}
}

// A pod that is not there is a 404 naming it, never an empty answer.
func TestAPodThatIsNotThereIsNamed(t *testing.T) {
	rec := serve(t, shop(t), "/x/kind/runtime/pod/shop/gone-0")
	p := decode[problem](t, rec)
	if rec.Code != http.StatusNotFound || p.Code != CodeUnknownPod || !strings.Contains(p.Message, "gone-0") {
		t.Errorf("answered %d %+v, want 404 %s naming the pod", rec.Code, p, CodeUnknownPod)
	}
}

// An address that names no pod is refused before anything is read.
func TestAnAddressThatNamesNoPodIsRefused(t *testing.T) {
	for _, path := range []string{"/x/kind/runtime/pod/Shop/billing-0", "/x/kind/runtime/pod/shop/Billing_0", "/x/kind/runtime/?ns=No_Such"} {
		h := shop(t)
		rec := serve(t, h, path)
		if p := decode[problem](t, rec); rec.Code != http.StatusBadRequest || p.Code != CodeBadRequest {
			t.Errorf("%s: answered %d %+v, want 400 %s", path, rec.Code, p, CodeBadRequest)
		}
		if len(h.queries) != 0 {
			t.Errorf("%s: read %+v before refusing", path, h.queries)
		}
	}
}

// Every way the read did not happen is its own answer, on every route: an
// unknown cluster, a refusal for the person, one that could not be asked, a
// read that failed. None of them is an empty list.
func TestEveryFailureIsAnsweredDistinctly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
		says   string
	}{
		{"unknown cluster", fmt.Errorf("resolving: %w", extension.ErrUnknownCluster),
			http.StatusNotFound, CodeUnknownCluster, "no cluster called"},
		{"denied", fmt.Errorf("listing: %w", extension.ErrDenied),
			http.StatusForbidden, CodeListForbidden, "may not list pods"},
		{"unreviewable", fmt.Errorf("listing: %w", extension.ErrUnreviewable),
			http.StatusServiceUnavailable, CodeListUnreviewed, "could not ask the cluster whether you may"},
		{"failed", errors.New("connection refused"),
			http.StatusBadGateway, CodeListFailed, "could not be read"},
	} {
		for _, path := range []string{"/x/kind/runtime/", "/x/kind/runtime/pod/shop/billing-0", "/x/kind/runtime/column?kind=pods"} {
			t.Run(tc.name+path, func(t *testing.T) {
				h := shop(t)
				h.listErr = tc.err
				rec := serve(t, h, path)
				p := decode[problem](t, rec)
				if rec.Code != tc.status || p.Code != tc.code || !strings.Contains(p.Message, tc.says) {
					t.Errorf("answered %d %+v, want %d %s saying %q", rec.Code, p, tc.status, tc.code, tc.says)
				}
			})
		}
	}
}

// The inventory: the probable Java pods, each with its Java containers only,
// in namespace then name order, with how many pods were read.
func TestTheInventoryListsTheProbableJavaPods(t *testing.T) {
	h := shop(t)
	finished := pod("shop", "migrate-1", corev1.Container{Name: "flyway", Image: "eclipse-temurin:21-jre"})
	finished.Status.Phase = corev1.PodSucceeded
	h.pods = append(h.pods, newHost(t, finished).pods...)

	rec := serve(t, h, "/x/kind/runtime/")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body)
	}
	got := decode[inventoryAnswer](t, rec)
	if !got.Complete || got.Read != 3 || len(got.Pods) != 2 {
		t.Fatalf("answer = %+v, want 2 Java pods of the 3 still running, complete", got)
	}
	if got.Pods[0].Name != "kafka-0" || got.Pods[1].Name != "billing-0" {
		t.Errorf("order = %s, %s; want data/kafka-0 then shop/billing-0", got.Pods[0].Name, got.Pods[1].Name)
	}
	if len(got.Pods[1].Containers) != 1 || got.Pods[1].Containers[0].Name != "app" {
		t.Errorf("billing-0 containers = %+v, want the Java one only", got.Pods[1].Containers)
	}
	// One namespace.
	got = decode[inventoryAnswer](t, serve(t, h, "/x/kind/runtime/?ns=data"))
	if got.Namespace != "data" || len(got.Pods) != 1 || h.queries[len(h.queries)-1].Namespace != "data" {
		t.Errorf("?ns=data: %+v", got)
	}
}

// A narrowed list says so: an empty inventory is then not "no Java here".
func TestANarrowedInventorySaysSo(t *testing.T) {
	h := newHost(t)
	h.narrowed = true
	got := decode[inventoryAnswer](t, serve(t, h, "/x/kind/runtime/"))
	if got.Complete || got.Note == "" || got.Pods == nil {
		t.Errorf("answer = %+v, want incomplete with its note and an empty list", got)
	}
	if body := serve(t, h, "/x/kind/runtime/").Body.String(); !strings.Contains(body, `"pods":[]`) {
		t.Errorf("an empty inventory is not an empty list: %s", body)
	}
}

// The handlers read the request's own context through the Host, and a
// request the page abandoned writes nothing.
func TestAnAbandonedRequestWritesNothing(t *testing.T) {
	h := shop(t)
	h.listErr = fmt.Errorf("listing: %w", errors.Join(errors.New("x"), context.Canceled))
	rec := httptest.NewRecorder()
	New().pod(h).ServeHTTP(rec, withPath(httptest.NewRequest(http.MethodGet, "/x/kind/runtime/pod/shop/billing-0", nil)))
	if rec.Body.Len() != 0 {
		t.Errorf("wrote %q for a canceled request", rec.Body)
	}
}

func withPath(r *http.Request) *http.Request {
	r.SetPathValue("cluster", "kind")
	r.SetPathValue("namespace", "shop")
	r.SetPathValue("name", "billing-0")
	return r
}

// A plain GET - what a link on another site sends, with the Lax session
// cookie and no Origin header - reaches none of the routes under a container:
// each runs jcmd, opens a tunnel or both. It opens no tunnel and runs nothing,
// so the console writes no audit record (#82).
func TestACrossSiteGetOpensNothing(t *testing.T) {
	srv, asked := pprofServer(t, true)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	fill := strings.NewReplacer("{namespace}", "shop", "{name}", "api-0", "{container}", "app", "{profile}", "heap", "{read}", "report")
	n := 0
	for _, r := range New().Routes(h) {
		if !strings.Contains(r.Pattern, "{container}") {
			continue
		}
		n++
		path := "/x/kind/runtime" + fill.Replace(r.Pattern)
		if rec := serve(t, h, path); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s answered %d, want 405", path, rec.Code)
		}
	}
	if n != 15 {
		t.Errorf("%d routes under a container, want 15", n)
	}
	if len(h.opened) != 0 || len(h.ran) != 0 || len(*asked) != 0 {
		t.Errorf("a GET opened %v, ran %v, asked pprof %v", h.opened, h.ran, *asked)
	}
}
