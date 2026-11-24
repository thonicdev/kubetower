package netinspect

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/encoding/protowire"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
)

// testFlow is one flow the fake Relay sends.
type testFlow struct {
	srcNS, srcPod, dstNS, dstPod string
	srcIP, dstIP                 string
	port                         uint32
	udp                          bool
	verdict                      int
	reason                       int
	reply                        bool
	deniedBy                     string // "namespace/name"
}

func (t testFlow) encode() []byte {
	end := func(ns, pod string) []byte {
		var b []byte
		if ns != "" {
			b = protowire.AppendTag(b, 3, protowire.BytesType)
			b = protowire.AppendString(b, ns)
		}
		if pod != "" {
			b = protowire.AppendTag(b, 5, protowire.BytesType)
			b = protowire.AppendString(b, pod)
		}
		return b
	}
	var f []byte
	var ts []byte
	ts = protowire.AppendTag(ts, 1, protowire.VarintType)
	ts = protowire.AppendVarint(ts, uint64(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC).Unix()))
	f = protowire.AppendTag(f, 1, protowire.BytesType)
	f = protowire.AppendBytes(f, ts)
	f = protowire.AppendTag(f, 2, protowire.VarintType)
	f = protowire.AppendVarint(f, uint64(t.verdict))
	var ip []byte
	ip = protowire.AppendTag(ip, 1, protowire.BytesType)
	ip = protowire.AppendString(ip, t.srcIP)
	ip = protowire.AppendTag(ip, 2, protowire.BytesType)
	ip = protowire.AppendString(ip, t.dstIP)
	f = protowire.AppendTag(f, 5, protowire.BytesType)
	f = protowire.AppendBytes(f, ip)
	var ports []byte
	ports = protowire.AppendTag(ports, 2, protowire.VarintType)
	ports = protowire.AppendVarint(ports, uint64(t.port))
	var l4 []byte
	which := protowire.Number(1)
	if t.udp {
		which = 2
	}
	l4 = protowire.AppendTag(l4, which, protowire.BytesType)
	l4 = protowire.AppendBytes(l4, ports)
	f = protowire.AppendTag(f, 6, protowire.BytesType)
	f = protowire.AppendBytes(f, l4)
	f = protowire.AppendTag(f, 8, protowire.BytesType)
	f = protowire.AppendBytes(f, end(t.srcNS, t.srcPod))
	f = protowire.AppendTag(f, 9, protowire.BytesType)
	f = protowire.AppendBytes(f, end(t.dstNS, t.dstPod))
	if t.reason != 0 {
		f = protowire.AppendTag(f, 25, protowire.VarintType)
		f = protowire.AppendVarint(f, uint64(t.reason))
	}
	if t.reply {
		var bv []byte
		bv = protowire.AppendTag(bv, 1, protowire.VarintType)
		bv = protowire.AppendVarint(bv, 1)
		f = protowire.AppendTag(f, 26, protowire.BytesType)
		f = protowire.AppendBytes(f, bv)
	}
	if t.deniedBy != "" {
		ns, name, _ := strings.Cut(t.deniedBy, "/")
		var p []byte
		p = protowire.AppendTag(p, 1, protowire.BytesType)
		p = protowire.AppendString(p, name)
		p = protowire.AppendTag(p, 2, protowire.BytesType)
		p = protowire.AppendString(p, ns)
		f = protowire.AppendTag(f, 21005, protowire.BytesType)
		f = protowire.AppendBytes(f, p)
	}
	var resp []byte
	resp = protowire.AppendTag(resp, 1, protowire.BytesType)
	resp = protowire.AppendBytes(resp, f)
	return resp
}

// fakeRelay is Hubble Relay's GetFlows over plaintext HTTP/2: it records
// the request and streams the flows, framed as gRPC does, with a status in
// the trailers.
type fakeRelay struct {
	mu      sync.Mutex
	flows   []testFlow
	asked   [][]byte
	status  string
	server  *httptest.Server
	streams int
}

func startRelay(t *testing.T, flows ...testFlow) *fakeRelay {
	t.Helper()
	fr := &fakeRelay{flows: flows, status: "0"}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
		fr.streams++
		fr.mu.Unlock()
		if r.URL.Path != "/observer.Observer/GetFlows" || r.Header.Get("Content-Type") != "application/grpc" {
			http.Error(w, "not GetFlows", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		fr.mu.Lock()
		fr.asked = append(fr.asked, body)
		status := fr.status
		fr.mu.Unlock()
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
		w.WriteHeader(http.StatusOK)
		for _, f := range fr.flows {
			msg := f.encode()
			head := make([]byte, 5)
			binary.BigEndian.PutUint32(head[1:], uint32(len(msg)))
			if _, err := w.Write(append(head, msg...)); err != nil {
				return // the client went away: what a cap or a cancel does
			}
		}
		w.Header().Set("Grpc-Status", status)
		w.Header().Set("Grpc-Message", "")
	})
	fr.server = httptest.NewUnstartedServer(h2c.NewHandler(handler, &http2.Server{}))
	fr.server.Start()
	t.Cleanup(fr.server.Close)
	return fr
}

func (fr *fakeRelay) addr() string { return strings.TrimPrefix(fr.server.URL, "http://") }

// withRelay is the shop scene with a Hubble Relay Service and a tunnel to fr.
func withRelay(t *testing.T, fr *fakeRelay) *fakeHost {
	h := shop(t)
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "hubble-relay"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "grpc", Port: 80}}},
	})
	if fr != nil {
		h.tunnelTo = fr.addr()
	}
	return h
}

func flowsOf(t *testing.T, h *fakeHost, query string) Flows {
	t.Helper()
	w := serve(t, h, "/x/kind/netinspect/flows"+query)
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body.String())
	}
	return decode[Flows](t, w)
}

func TestObservedFlowsAreAggregatedAndSaid(t *testing.T) {
	fr := startRelay(t,
		testFlow{srcNS: "shop", srcPod: "web", dstNS: "shop", dstPod: "api", port: 80, verdict: 1},
		testFlow{srcNS: "shop", srcPod: "web", dstNS: "shop", dstPod: "api", port: 80, verdict: 1},
		testFlow{srcNS: "shop", srcPod: "api", dstNS: "shop", dstPod: "web", port: 41000, verdict: 1, reply: true},
		testFlow{srcNS: "ops", srcPod: "probe", dstNS: "shop", dstPod: "api", port: 80, verdict: 2, reason: 133, deniedBy: "shop/default-deny-ingress"},
		testFlow{srcNS: "shop", srcPod: "web", dstIP: "198.51.100.7", port: 443, verdict: 1},
		testFlow{srcNS: "secret", srcPod: "vault", dstNS: "shop", dstPod: "api", port: 80, verdict: 1},
	)
	h := withRelay(t, fr)
	got := flowsOf(t, h, "")
	// The flow from "secret" is neither counted nor read: the answer says only
	// that it was narrowed, never how much traffic the person may not see.
	if got.Mode != "observed" || got.State != flowsRead || got.Read != 5 || !got.Narrowed || got.Forwarded != 3 || got.Dropped != 1 {
		t.Fatalf("flows: %+v", got)
	}
	var dropped *FlowRow
	for i := range got.Rows {
		if got.Rows[i].Verdict == "DROPPED" {
			dropped = &got.Rows[i]
		}
		if strings.Contains(got.Rows[i].From, "secret") {
			t.Errorf("a flow from a namespace the person may not list is shown: %+v", got.Rows[i])
		}
	}
	if dropped == nil || dropped.Reason != "POLICY_DENIED" || strings.Join(dropped.DeniedBy, ",") != "shop/default-deny-ingress" || dropped.From != "ops/probe" {
		t.Errorf("dropped row: %+v", dropped)
	}
	edges := map[string]FlowEdge{}
	for _, e := range got.Edges {
		edges[e.From+">"+e.To] = e
	}
	if e := edges["ns:ops>ns:shop"]; e.Dropped != 1 || strings.Join(e.DeniedBy, ",") != "shop/default-deny-ingress" {
		t.Errorf("ops → shop edge: %+v", edges)
	}
	if e := edges["ns:shop>entity:world"]; e.Forwarded != 1 {
		t.Errorf("shop → world edge: %+v", edges)
	}
	// One namespace's view names its workloads as the rule graph does.
	ns := flowsOf(t, h, "?ns=shop")
	found := false
	for _, e := range ns.Edges {
		if e.From == "wl:shop/web" && e.To == "wl:shop/api" && e.Forwarded == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("shop's edges: %+v", ns.Edges)
	}
	// The request: a cap, a window, and the person's namespaces.
	fr.mu.Lock()
	asked := fr.asked[0][5:] // past the gRPC frame header
	fr.mu.Unlock()
	var number uint64
	var filters int
	each(asked, func(num protowire.Number, _ protowire.Type, _ []byte, n uint64) {
		switch num {
		case 1:
			number = n
		case 6:
			filters++
		}
	})
	if number != flowCap || filters != 4 { // shop and ops, each as source and as destination
		t.Errorf("asked number %d with %d filters", number, filters)
	}
	if h.tunnels != h.stopped || h.tunnels != 2 {
		t.Errorf("%d tunnels opened, %d stopped", h.tunnels, h.stopped)
	}
}

func TestEachWayRelayFailsIsItsOwnState(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		h := shop(t)
		if got := flowsOf(t, h, ""); got.State != flowsAbsent || got.Detail == "" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("tls", func(t *testing.T) {
		h := shop(t)
		h.add(t, "services", &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "hubble-relay"},
			Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "grpc-tls", Port: 443}}}})
		if got := flowsOf(t, h, ""); got.State != flowsTLS {
			t.Errorf("%+v", got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		h := withRelay(t, nil)
		h.tunnelErr = fmt.Errorf("portforward: %w", extension.ErrDenied)
		if got := flowsOf(t, h, ""); got.State != flowsRefused {
			t.Errorf("%+v", got)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		h := withRelay(t, nil)
		h.tunnelTo = "127.0.0.1:1"
		if got := flowsOf(t, h, ""); got.State != flowsUnreachable || h.tunnels != h.stopped {
			t.Errorf("%+v, %d tunnels, %d stopped", got, h.tunnels, h.stopped)
		}
	})
	t.Run("grpc error", func(t *testing.T) {
		fr := startRelay(t)
		fr.status = "7"
		h := withRelay(t, fr)
		if got := flowsOf(t, h, ""); got.State != flowsUnreachable || !strings.Contains(got.Detail, "status 7") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		h := withRelay(t, startRelay(t))
		if got := flowsOf(t, h, ""); got.State != flowsEmpty || got.Read != 0 {
			t.Errorf("%+v", got)
		}
	})
	t.Run("the page itself refused", func(t *testing.T) {
		h := withRelay(t, startRelay(t))
		h.listErr["pods"] = fmt.Errorf("list: %w", extension.ErrDenied)
		w := serve(t, h, "/x/kind/netinspect/flows")
		if w.Code != http.StatusForbidden || h.tunnels != 0 {
			t.Errorf("answered %d after %d tunnels: the person's listing comes before the tunnel", w.Code, h.tunnels)
		}
	})
}

// 10 000 flows streamed: the cap stops the read at flowCap, the stream is
// closed, and the answer is bounded in time and memory.
func TestLoadTenThousandFlows(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 10 000 flows")
	}
	flows := make([]testFlow, 10_000)
	for i := range flows {
		flows[i] = testFlow{srcNS: "shop", srcPod: fmt.Sprintf("web-%d", i%300), dstNS: "ops", dstPod: fmt.Sprintf("probe-%d", i%40),
			port: uint32(8000 + i%50), verdict: 1 + i%2, reason: 133 * (i % 2), deniedBy: map[bool]string{true: "ops/deny", false: ""}[i%2 == 1]}
	}
	h := withRelay(t, startRelay(t, flows...))
	limit := loadTime
	allocLimit := uint64(loadAllocated)
	if raceEnabled {
		limit *= raceSlowdown
		allocLimit *= raceAllocation
	}
	took, allocated, retained, size := measureRequest(t, h, "/x/kind/netinspect/flows")
	t.Logf("10 000 flows streamed: %v, %.1f MiB allocated, %.2f MiB retained, %d KiB answer", took, float64(allocated)/(1<<20), float64(retained)/(1<<20), size>>10)
	if took > limit || allocated > allocLimit || retained > loadRetained || size > loadAnswerBytes {
		t.Errorf("over a ceiling")
	}
	got := flowsOf(t, h, "")
	if got.Read != flowCap || !got.Truncated || len(got.Rows) > maxFlowRows {
		t.Errorf("read %d, truncated %v, %d rows", got.Read, got.Truncated, len(got.Rows))
	}
	if h.tunnels != h.stopped {
		t.Errorf("%d tunnels, %d stopped", h.tunnels, h.stopped)
	}
}

// A person who may list no pod is asked nothing of Hubble: the tunnel is the
// console's, and every flow it brought back would be dropped for them.
func TestNoPodToShowOpensNoTunnel(t *testing.T) {
	fr := startRelay(t, testFlow{srcNS: "secret", srcPod: "vault", dstNS: "secret", dstPod: "db", port: 5432, verdict: 1})
	h := newHost()
	h.namespace(t, "secret", nil)
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "hubble-relay"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "grpc", Port: 80}}},
	})
	h.tunnelTo = fr.addr()
	h.narrowed["pods"] = true
	got := flowsOf(t, h, "")
	if got.State != flowsEmpty || got.Read != 0 || !got.Narrowed || len(got.Rows) != 0 {
		t.Errorf("flows: %+v", got)
	}
	if h.tunnels != 0 {
		t.Errorf("%d tunnels opened to Hubble Relay for a person who may see no pod", h.tunnels)
	}
}

// The page may ask for one of four periods; five minutes when it names none,
// and anything else is refused before a tunnel is dug.
func TestFlowsTakeTheWindowAsked(t *testing.T) {
	fr := startRelay(t, testFlow{srcNS: "shop", srcPod: "web", dstNS: "shop", dstPod: "api", port: 80, verdict: 1})
	h := withRelay(t, fr)
	sinceOf := func(i int) int64 {
		t.Helper()
		fr.mu.Lock()
		asked := fr.asked[i][5:]
		fr.mu.Unlock()
		var since int64
		each(asked, func(num protowire.Number, _ protowire.Type, b []byte, _ uint64) {
			if num == 7 {
				each(b, func(n protowire.Number, _ protowire.Type, _ []byte, v uint64) {
					if n == 1 {
						since = int64(v)
					}
				})
			}
		})
		return since
	}
	if got := flowsOf(t, h, ""); got.Window != 300 || got.State != flowsRead {
		t.Fatalf("no window: %d (%s)", got.Window, got.State)
	}
	if ago := time.Now().Unix() - sinceOf(0); ago < 295 || ago > 305 {
		t.Errorf("no window asked Relay for %d s back, want 300", ago)
	}
	for i, window := range []int{60, 900, 3600} {
		got := flowsOf(t, h, fmt.Sprintf("?ns=shop&window=%d", window))
		if got.Window != window || got.State != flowsRead {
			t.Errorf("window %d: answered %d (%s)", window, got.Window, got.State)
		}
		if ago := time.Now().Unix() - sinceOf(i+1); ago < int64(window)-5 || ago > int64(window)+5 {
			t.Errorf("window %d asked Relay for %d s back", window, ago)
		}
	}
	before := h.tunnels
	for _, bad := range []string{"7", "-60", "1h", "86400"} {
		if w := serve(t, h, "/x/kind/netinspect/flows?window="+bad); w.Code != http.StatusBadRequest {
			t.Errorf("window=%s: %d, want 400", bad, w.Code)
		}
	}
	if h.tunnels != before {
		t.Errorf("a refused window dug %d tunnels", h.tunnels-before)
	}
}

// One namespace's read asks Relay for that namespace only, as source and as
// destination; the cluster's read asks for every namespace the person sees.
func TestRelayIsAskedForTheNamespaceRead(t *testing.T) {
	fr := startRelay(t,
		testFlow{srcNS: "shop", srcPod: "web", dstNS: "shop", dstPod: "api", port: 80, verdict: 1},
		testFlow{srcNS: "ops", srcPod: "probe", dstNS: "ops", dstPod: "db", port: 5432, verdict: 1})
	h := withRelay(t, fr)
	filters := func(i int) []string {
		t.Helper()
		fr.mu.Lock()
		asked := fr.asked[i][5:]
		fr.mu.Unlock()
		var out []string
		each(asked, func(num protowire.Number, _ protowire.Type, b []byte, _ uint64) {
			if num == 6 {
				each(b, func(n protowire.Number, _ protowire.Type, v []byte, _ uint64) {
					out = append(out, fmt.Sprintf("%d:%s", n, v))
				})
			}
		})
		return out
	}
	flowsOf(t, h, "")
	if got := strings.Join(filters(0), ","); got != "2:ops/,4:ops/,2:shop/,4:shop/" {
		t.Errorf("cluster read filters %s", got)
	}
	flowsOf(t, h, "?ns=shop&window=3600")
	if got := strings.Join(filters(1), ","); got != "2:shop/,4:shop/" {
		t.Errorf("shop read filters %s, want shop only", got)
	}
}
