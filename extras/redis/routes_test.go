package redis

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/svcgroup"
)

// The server-side half of the prototype's `internal/web/redisview_test.go`,
// ported to JSON routes. The other half - the scan box's URL, the replace URL,
// the way back's `scope`, the filter box's name, the probe being a button - is
// the pages' now, and is ported with them.

type problemBody struct {
	Code    string `json:"code"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

type inventoryBody struct {
	Scope      string `json:"scope"`
	Complete   bool   `json:"complete"`
	SlicesNote string `json:"slicesNote"`
	Services   int    `json:"services"`
	Redises    int    `json:"redises"`
	Namespaces []struct {
		Name        string `json:"name"`
		Deployments []struct {
			Members []backendBody `json:"members"`
		} `json:"deployments"`
	} `json:"namespaces"`
}

type backendBody struct {
	Pods         []string `json:"pods"`
	Behind       string   `json:"behind"`
	Ready        int      `json:"ready"`
	Reachable    bool     `json:"reachable"`
	Endpointless bool     `json:"endpointless"`
	Doors        []struct {
		Service string `json:"service"`
		Port    int    `json:"port"`
		Why     string `json:"why"`
	} `json:"doors"`
}

// The three answers, and the reason there are three rather than two: "nothing
// is behind this Service" and "what is behind it could not be read" are
// different facts, and folding the second into the first reports a healthy
// Redis as down on the strength of a permission the person lacks.
func TestABackendSaysWhichOfNothingAndCouldNotLookItMeans(t *testing.T) {
	one := svcgroup.Behind(svcgroup.Backend{Pods: []string{"redis-0"}})
	if one != "redis-0" {
		t.Fatalf("one pod = %q, want its name", one)
	}
	several := svcgroup.Behind(svcgroup.Backend{Pods: []string{"redis-0", "redis-1"}})
	if !strings.Contains(several, "redis-0") || !strings.Contains(several, "redis-1") {
		t.Fatalf("several pods = %q, want both named", several)
	}

	// And through the route: one Service with nothing behind it, read; then
	// the same cluster with the EndpointSlices refused.
	h := newHost()
	// No pods: the slice listing answers, and names no slice for it.
	h.redisService(t, "cache", "empty", corev1.ServicePort{Name: "redis", Port: 6379})
	answered := decode[inventoryBody](t, serve(t, h, "/x/kind/redis/"))
	none := answered.Namespaces[0].Deployments[0].Members[0]

	h.listErr["endpointslices"] = errors.New(`endpointslices.discovery.k8s.io is forbidden`)
	refused := decode[inventoryBody](t, serve(t, h, "/x/kind/redis/"))
	unknown := refused.Namespaces[0].Deployments[0].Members[0]

	if !none.Endpointless || unknown.Endpointless {
		t.Fatalf("endpointless = %v / %v, want true for the read one and false for the refused one",
			none.Endpointless, unknown.Endpointless)
	}
	if none.Behind == unknown.Behind {
		t.Fatalf("a Service with nothing behind it and one that could not be read both say %q", none.Behind)
	}
	if strings.Contains(unknown.Behind, "nothing") {
		t.Fatalf("%q claims nothing is there, which is exactly what was not established", unknown.Behind)
	}
	if refused.SlicesNote == "" {
		t.Fatal("the inventory did not say that what is behind each Service could not be read")
	}
}

// **The inventory reaches no instance until it is asked to.** The tunnels every
// extension holds are capped together; an inventory that probed each row would
// exhaust the cap and put this console in every Redis's connection log on every
// glance.
func TestTheInventoryDialsNothingUntilTheOperatorAsks(t *testing.T) {
	h := newHost()
	h.redisService(t, "cache", "redis-master", corev1.ServicePort{Name: "tcp-redis", Port: 6379}, "redis-master-0")
	h.redisService(t, "cache", "redis-headless", corev1.ServicePort{Name: "tcp-redis", Port: 6379}, "redis-master-0")
	h.redisService(t, "apps", "valkey", corev1.ServicePort{Name: "valkey", Port: 6379}, "valkey-0")
	h.redisService(t, "apps", "web", corev1.ServicePort{Name: "http", Port: 8080}, "web-0")

	w := serve(t, h, "/x/kind/redis/")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if h.forwards != 0 || len(h.reveals) != 0 {
		t.Fatalf("the inventory opened %d tunnel(s) and read %d Secret key(s); it must open none",
			h.forwards, len(h.reveals))
	}
	body := decode[inventoryBody](t, w)
	if body.Services != 3 || body.Redises != 2 {
		t.Fatalf("counts = %d Services, %d Redises; want 3 and 2 - two doors onto one Redis are one Redis",
			body.Services, body.Redises)
	}
	if len(body.Namespaces) != 2 || body.Namespaces[0].Name != "apps" || body.Namespaces[1].Name != "cache" {
		t.Fatalf("namespaces = %+v, want apps then cache: the namespace is the organizing axis", body.Namespaces)
	}
	cache := body.Namespaces[1].Deployments
	if len(cache) != 1 || len(cache[0].Members) != 1 || len(cache[0].Members[0].Doors) != 2 || cache[0].Members[0].Doors[0].Why == "" {
		t.Fatalf("cache = %+v, want one Redis with two doors, each with its reason", cache)
	}
}

// An inventory that could not list Services is a failure, not an empty cluster.
func TestAnInventoryThatCouldNotListSaysSoRatherThanNone(t *testing.T) {
	h := newHost()
	h.listErr["services"] = errors.New("services is forbidden")
	w := serve(t, h, "/x/kind/redis/")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if p := decode[problemBody](t, w); p.Code != CodeListFailed || p.Kind != kindFailure {
		t.Fatalf("problem = %+v, want list_failed, a failure", p)
	}

	h.listErr["services"] = extension.ErrUnknownCluster
	if w := serve(t, h, "/x/gone/redis/"); w.Code != http.StatusNotFound || decode[problemBody](t, w).Code != CodeUnknownCluster {
		t.Fatalf("an unknown cluster answered %d %s", w.Code, w.Body.String())
	}

	empty := newHost()
	w = serve(t, empty, "/x/kind/redis/")
	if body := decode[inventoryBody](t, w); w.Code != http.StatusOK || body.Services != 0 || !body.Complete {
		t.Fatalf("a cluster with no Redis answered %d %+v, want 200, complete, none", w.Code, body)
	}
}

// A bookmark outlives the thing it points at, and "gone" is not "unreachable".
// Discovery is consulted first, and a port that is not offered is a different
// instance, not the same one on another port.
func TestAnInstanceThatIsNoLongerDiscoveredIsFoundToBeGone(t *testing.T) {
	h := newHost()
	h.redisService(t, "kt-redis-demo", "redis-demo", corev1.ServicePort{Name: "redis", Port: 6379}, "redis-0")

	for _, target := range []string{
		"/x/kind/redis/instances/kt-redis-demo/redis-demo/6380",
		"/x/kind/redis/instances/other/redis-demo/6379",
		"/x/kind/redis/instances/kt-redis-demo/uninstalled/6379/probe",
	} {
		w := serve(t, h, target)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", target, w.Code)
		}
		if p := decode[problemBody](t, w); p.Code != CodeInstanceGone || p.Kind != kindAbsent {
			t.Fatalf("%s: problem = %+v, want instance_gone, an absence", target, p)
		}
	}
	if h.forwards != 0 {
		t.Fatalf("%d tunnel(s) opened to instances discovery does not find", h.forwards)
	}
}

// **The needle is a phrase, not the word "password".** This package builds
// sentences out of these errors, and every one contains the word.
func TestWantsPasswordMatchesRedissCodesAndNotThisPagesOwnProse(t *testing.T) {
	for _, wants := range []string{
		"NOAUTH Authentication required.",
		"WRONGPASS invalid username-password pair or user is disabled.",
		"ERR Client sent AUTH, but no password is set",
	} {
		if !wantsPassword(errors.New(wants)) {
			t.Errorf("%q was not recognized as a demand for credentials", wants)
		}
	}
	for _, other := range []string{
		"dial tcp 127.0.0.1:34567: connect: connection refused",
		"could not reach cache/redis: no pod is ready",
		"reading key auth:password:reset failed",
	} {
		if wantsPassword(errors.New(other)) {
			t.Errorf("%q was read as a demand for credentials; a broad needle here makes the "+
				"console look up a Secret for a failure that is not about authentication", other)
		}
	}
	if wantsPassword(nil) {
		t.Error("no error is not a demand for credentials")
	}
}

// **A sentence this package already wrote is a conclusion, not evidence.** The
// credential refusals quote Redis's words back at the reader, so they match the
// very needles that produced them.
func TestARefusalThisFileWroteIsNotReadAsAFreshDemand(t *testing.T) {
	written := &refusal{code: CodePasswordRefused, kind: kindRefusal,
		message: "This instance refused the password in Secret redis, key redis-password - " +
			"read from the REDIS_PASSWORD variable of container redis in pod redis-0. WRONGPASS."}

	if wantsPassword(written) {
		t.Error("the page's own refusal was read as a demand for credentials, which sends " +
			"the credential lookup round again on its own output")
	}
	if got := dialRefusal(t.Context(), svcgroup.Instance{}, 0, written, ""); got != error(written) {
		t.Errorf("dialRefusal rewrote a sentence that was already the answer: %v", got)
	}
}

// **No password ever comes from the request.** The instance routes read an
// allowlist of query parameters (db, match, cursor, and key or raw on the key
// route); a `password` riding on the address is never read, never sent and
// never echoed.
func TestTheInstanceRouteReadsOnlyTheParametersOnTheAllowlist(t *testing.T) {
	server := newFakeRedis(t, "Redis")
	server.set(0, "session:1", value{kind: "string", s: "x"})
	h := newHost()
	h.forwardTo = server.addr()
	h.redisService(t, "cache", "redis", corev1.ServicePort{Name: "redis", Port: 6379}, "redis-0")

	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379?password=s3cret-in-url&auth=s3cret-in-url&match=session:*")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "s3cret-in-url") {
		t.Fatalf("a parameter off the allowlist was echoed: %s", w.Body.String())
	}
	if server.sent("s3cret-in-url") {
		t.Fatal("a password from the address reached the server")
	}
	for _, cmd := range server.seen() {
		if strings.EqualFold(cmd[0], "AUTH") || len(cmd) > 2 && strings.EqualFold(cmd[2], "AUTH") {
			t.Fatalf("an instance that wants no password was sent %v", cmd)
		}
	}

	// And on an instance that does want one, a password in the address does
	// not stand in for the pod's: the person may not read the Secret, so the
	// answer is that refusal, whatever the URL says.
	locked := newFakeRedis(t, "Redis")
	locked.password = "s3cret-in-url"
	h.forwardTo = locked.addr()
	h.add(t, "pods", pod("cache", "redis-0", corev1.Container{Name: "redis",
		Env: []corev1.EnvVar{fromSecret("REDIS_PASSWORD", "redis", "redis-password")}}))
	h.revealErr = extension.ErrRevealDenied
	w = serve(t, h, "/x/kind/redis/instances/cache/redis/6379?password=s3cret-in-url")
	if w.Code != http.StatusForbidden || decode[problemBody](t, w).Code != CodeSecretDenied {
		t.Fatalf("answered %d %s, want the Secret refusal", w.Code, w.Body.String())
	}
	if locked.sent("s3cret-in-url") {
		t.Fatal("the password from the address was sent to the server")
	}
}

// A bad address is a bad request, said before anything is listed or dialled.
func TestAnAddressThatNamesNoInstanceIsRefusedBeforeAnything(t *testing.T) {
	h := newHost()
	for _, target := range []string{
		"/x/kind/redis/instances/cache/redis/notaport",
		"/x/kind/redis/instances/cache/redis/0",
		"/x/kind/redis/instances/cache/redis/6379?db=-1",
		"/x/kind/redis/instances/cache/redis/6379?cursor=abc",
		"/x/kind/redis/instances/cache/redis/6379/key",
		"/x/kind/redis/instances/cache/redis/6379/key?raw=%%%",
	} {
		w := serve(t, h, target)
		if w.Code != http.StatusBadRequest || decode[problemBody](t, w).Code != CodeBadRequest {
			t.Fatalf("%s answered %d %s, want 400 bad_request", target, w.Code, w.Body.String())
		}
	}
	if h.forwards != 0 {
		t.Fatalf("%d tunnel(s) opened for addresses that name no instance", h.forwards)
	}
}

// The tunnel cap's own sentence reaches the page, as a failure: the instance is
// not refusing anybody, the console has no slot left.
func TestATunnelThatCouldNotOpenSaysWhy(t *testing.T) {
	h := newHost()
	h.redisService(t, "cache", "redis", corev1.ServicePort{Name: "redis", Port: 6379}, "redis-0")
	h.forwardErr = errors.New("the extensions already hold 16 tunnels, the most this console opens for them at once")

	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/probe")
	p := decode[problemBody](t, w)
	if w.Code != http.StatusBadGateway || p.Code != CodeTunnelFailed || p.Kind != kindFailure {
		t.Fatalf("answered %d %+v, want 502 tunnel_failed, a failure", w.Code, p)
	}
	if !strings.Contains(p.Detail, "16 tunnels") {
		t.Fatalf("the cap's sentence was lost: %+v", p)
	}
}

// "Connection refused" behind an open tunnel is most often an exporter's port,
// and the answer says so with the reason the port was offered.
func TestNothingListeningBehindTheTunnelPointsAtTheReasonItWasOffered(t *testing.T) {
	server := newFakeRedis(t, "Redis")
	addr := server.addr()
	_ = server.ln.Close()

	h := newHost()
	h.forwardTo = addr
	h.redisService(t, "cache", "redis", corev1.ServicePort{Port: 6379}, "redis-0")
	w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/probe")
	p := decode[problemBody](t, w)
	if w.Code != http.StatusBadGateway || p.Code != CodeUnreachable || p.Kind != kindFailure {
		t.Fatalf("answered %d %+v, want 502 unreachable", w.Code, p)
	}
	if !strings.Contains(p.Message, "it is on 6379") {
		t.Fatalf("the answer does not repeat why the port was offered: %q", p.Message)
	}
	if h.stopped != h.forwards {
		t.Fatalf("%d tunnel(s) opened and %d stopped: a failed dial must give its slot back", h.forwards, h.stopped)
	}
}

// The tab on a Service's panel asks whether the
// Service is one the inventory would offer, by the same guess, and dials
// nothing: a Redis Service answers its backend and every door onto it, a
// Service that is not answers `not_redis` - an absence, so the tab is not drawn.
func TestTheServiceTabAnswersFromDiscoveryAndDialsNothing(t *testing.T) {
	h := newHost()
	h.redisService(t, "cache", "redis-master", corev1.ServicePort{Name: "tcp-redis", Port: 6379}, "redis-master-0")
	h.redisService(t, "cache", "redis-headless", corev1.ServicePort{Name: "tcp-redis", Port: 6379}, "redis-master-0")
	h.redisService(t, "cache", "web", corev1.ServicePort{Name: "http", Port: 8080}, "web-0")

	w := serve(t, h, "/x/kind/redis/services/cache/redis-master")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := decode[struct {
		Deployments []struct {
			Members []struct {
				Behind string `json:"behind"`
				Role   string `json:"role"`
				Doors  []struct {
					Service string `json:"service"`
					Why     string `json:"why"`
				} `json:"doors"`
			} `json:"members"`
		} `json:"deployments"`
	}](t, w)
	if len(body.Deployments) != 1 || len(body.Deployments[0].Members) != 1 {
		t.Fatalf("deployments = %+v, want the one Redis the Service belongs to", body.Deployments)
	}
	if m := body.Deployments[0].Members[0]; len(m.Doors) != 2 || m.Behind != "redis-master-0" || m.Role != rolePrimary {
		t.Fatalf("member = %+v, want the primary behind redis-master-0 with both doors", m)
	}

	// A Service reached on a Sentinel port only is drawn in the inventory and
	// is not a Redis to open: no tab.
	h.redisService(t, "cache", "redis-sentinel", corev1.ServicePort{Name: "tcp-sentinel", Port: 26379}, "redis-master-0")
	w = serve(t, h, "/x/kind/redis/services/cache/redis-sentinel")
	if p := decode[problemBody](t, w); w.Code != http.StatusNotFound || p.Code != CodeNotRedis ||
		!strings.Contains(p.Message, "Sentinel port only") {
		t.Fatalf("a Sentinel-only Service answered %d %+v, want 404 not_redis saying why", w.Code, p)
	}

	w = serve(t, h, "/x/kind/redis/services/cache/web")
	if p := decode[problemBody](t, w); w.Code != http.StatusNotFound || p.Code != CodeNotRedis || p.Kind != kindAbsent {
		t.Fatalf("a non-Redis Service answered %d %+v, want 404 not_redis", w.Code, p)
	}
	if h.forwards != 0 || len(h.reveals) != 0 {
		t.Fatalf("the tab's route opened %d tunnel(s) and read %d key(s)", h.forwards, len(h.reveals))
	}
}

// The routes that open a tunnel - and may read and record the reveal of a
// password - are POSTs: a GET to one, which
// is what a cross-site link sends, is refused before the handler runs, and
// nothing is dialled or revealed. The two that read discovery alone stay GETs.
func TestARouteThatDialsIsNotAGet(t *testing.T) {
	dialling := map[string]bool{
		"/instances/{namespace}/{service}/{port}":       true,
		"/instances/{namespace}/{service}/{port}/probe": true,
		"/instances/{namespace}/{service}/{port}/key":   true,
	}
	for _, route := range New().Routes(newHost()) {
		want := http.MethodGet
		if dialling[route.Pattern] {
			want = http.MethodPost
		}
		if route.Method != want {
			t.Errorf("%s is a %s, want %s", route.Pattern, route.Method, want)
		}
	}

	h := newHost()
	h.redisService(t, "cache", "redis", corev1.ServicePort{Name: "redis", Port: 6379}, "redis-0")
	mux := http.NewServeMux()
	for _, route := range New().Routes(h) {
		mux.Handle(route.Method+" /x/{cluster}/"+Key+route.Pattern, route.Handler)
	}
	for _, target := range []string{
		"/x/kind/redis/instances/cache/redis/6379",
		"/x/kind/redis/instances/cache/redis/6379/probe",
		"/x/kind/redis/instances/cache/redis/6379/key?key=greeting",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s answered %d, want 405", target, w.Code)
		}
	}
	if h.forwards != 0 || len(h.reveals) != 0 {
		t.Errorf("GETs to the dialling routes opened %d tunnels and %d reveals, want none", h.forwards, len(h.reveals))
	}
}
