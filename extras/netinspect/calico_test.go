package netinspect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
)

// --- a CA, and what it signs ------------------------------------------------

type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newAuthority(t testing.TB, name string) authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return authority{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// issue signs a certificate for cn, valid as a server for dns names and as a
// client; it answers the PEM pair and the parsed one.
func (a authority) issue(t testing.TB, cn string, dns ...string) (crt, key string, pair tls.Certificate) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, DNSNames: dns,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, a.cert, &k.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(k)
	crt = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	key = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	pair, err = tls.X509KeyPair([]byte(crt), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return crt, key, pair
}

// --- a Goldmane --------------------------------------------------------------

// gmFlow is one Flow the fake Goldmane holds.
type gmFlow struct {
	srcNS, srcName string
	srcType        uint64 // 1 workload (the default), 4 network
	dstNS, dstName string
	dstType        uint64
	port           uint64
	proto          string
	reporter       uint64 // 1 src, 2 dst
	action         uint64 // 1 allow, 2 deny
	conns          int64
	end            int64
	hits           []gmHit
}

type gmHit struct {
	kind         uint64
	ns, name     string
	action       uint64
	tier, policy string
}

func (f gmFlow) encode() []byte {
	str := func(b []byte, n protowire.Number, s string) []byte {
		if s == "" {
			return b
		}
		b = protowire.AppendTag(b, n, protowire.BytesType)
		return protowire.AppendString(b, s)
	}
	num := func(b []byte, n protowire.Number, v uint64) []byte {
		if v == 0 {
			return b
		}
		b = protowire.AppendTag(b, n, protowire.VarintType)
		return protowire.AppendVarint(b, v)
	}
	typeOf := func(t uint64) uint64 {
		if t == 0 {
			return 1
		}
		return t
	}
	var key []byte
	key = str(key, 1, f.srcName)
	key = str(key, 2, f.srcNS)
	key = num(key, 3, typeOf(f.srcType))
	key = str(key, 4, f.dstName)
	key = str(key, 5, f.dstNS)
	key = num(key, 6, typeOf(f.dstType))
	key = num(key, 7, f.port)
	key = str(key, 12, f.proto)
	key = num(key, 13, f.reporter)
	key = num(key, 14, f.action)
	var trace []byte
	for _, h := range f.hits {
		var hit []byte
		hit = num(hit, 1, h.kind)
		hit = str(hit, 2, h.ns)
		hit = str(hit, 3, h.name)
		hit = num(hit, 5, h.action)
		trace = protowire.AppendTag(trace, 1, protowire.BytesType)
		trace = protowire.AppendBytes(trace, hit)
	}
	if len(trace) > 0 {
		key = protowire.AppendTag(key, 15, protowire.BytesType)
		key = protowire.AppendBytes(key, trace)
	}
	var flow []byte
	flow = protowire.AppendTag(flow, 1, protowire.BytesType)
	flow = protowire.AppendBytes(flow, key)
	flow = num(flow, 2, uint64(f.end-300))
	flow = num(flow, 3, uint64(f.end))
	flow = num(flow, 10, uint64(f.conns))
	var result []byte
	result = num(result, 1, 1)
	result = protowire.AppendTag(result, 2, protowire.BytesType)
	return protowire.AppendBytes(result, flow)
}

// listResult is a FlowListResult holding flows, claiming total results.
func listResult(flows []gmFlow, total int) []byte {
	var meta []byte
	meta = protowire.AppendTag(meta, 1, protowire.VarintType)
	meta = protowire.AppendVarint(meta, 1)
	meta = protowire.AppendTag(meta, 2, protowire.VarintType)
	meta = protowire.AppendVarint(meta, uint64(total))
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, meta)
	for _, f := range flows {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, f.encode())
	}
	return b
}

// fakeGoldmane is Goldmane's goldmane.Flows/List over HTTP/2 in TLS, asking
// for a client certificate signed by its CA, as the real one does.
type fakeGoldmane struct {
	mu     sync.Mutex
	flows  []gmFlow
	total  int // 0: len(flows)
	asked  [][]byte
	status string
	block  chan struct{} // when set, the call waits here for the client to go
	gone   chan struct{} // closed when a blocked call saw its client go
	server *httptest.Server
	peers  int // calls that presented a verified client certificate
}

func startGoldmane(t *testing.T, ca authority, serverCA authority, flows ...gmFlow) *fakeGoldmane {
	t.Helper()
	fg := &fakeGoldmane{flows: flows, status: "0", gone: make(chan struct{})}
	_, _, serverPair := serverCA.issue(t, "goldmane", goldmaneServerName)
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/goldmane.Flows/List" || r.Header.Get("Content-Type") != "application/grpc" {
			http.Error(w, "not List", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		fg.mu.Lock()
		fg.asked = append(fg.asked, body)
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			fg.peers++
		}
		status, block, total, flows := fg.status, fg.block, fg.total, fg.flows
		fg.mu.Unlock()
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
		w.WriteHeader(http.StatusOK)
		if block != nil {
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				close(fg.gone)
			case <-block:
			}
			return
		}
		if total == 0 {
			total = len(flows)
		}
		msg := listResult(flows, total)
		head := make([]byte, 5)
		binary.BigEndian.PutUint32(head[1:], uint32(len(msg)))
		_, _ = w.Write(append(head, msg...))
		w.Header().Set("Grpc-Status", status)
		w.Header().Set("Grpc-Message", "")
	})
	fg.server = httptest.NewUnstartedServer(handler)
	fg.server.TLS = &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	fg.server.EnableHTTP2 = true
	fg.server.StartTLS()
	t.Cleanup(fg.server.Close)
	return fg
}

// set changes what the next call answers.
func (fg *fakeGoldmane) set(flows []gmFlow, total int, status string) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.flows, fg.total, fg.status = flows, total, status
}

func (fg *fakeGoldmane) addr() string { return strings.TrimPrefix(fg.server.URL, "https://") }

// scene is the shop with Calico: Goldmane's Service, its CA ConfigMap, the
// Secret the console reads for the person, and a tunnel to fg.
type scene struct {
	*fakeHost
	ca  authority
	crt string
	key string
}

func calicoScene(t *testing.T, build func(ca authority) *fakeGoldmane) (*scene, *fakeGoldmane) {
	t.Helper()
	ca := newAuthority(t, "tigera-operator-signer")
	crt, key, _ := ca.issue(t, "127.0.0.1", "whisker-backend")
	h := shop(t)
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "calico-system", Name: "goldmane"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 7443}}},
	})
	h.add(t, "configmaps", &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "calico-system", Name: "goldmane-ca-bundle"},
		Data:       map[string]string{"ca.crt": ca.pem, "ca-bundle.crt": ""},
	})
	h.secrets = map[string]string{
		"calico-system/whisker-backend-key-pair/tls.crt": crt,
		"calico-system/whisker-backend-key-pair/tls.key": key,
	}
	fg := build(ca)
	if fg != nil {
		h.tunnelTo = fg.addr()
	}
	return &scene{fakeHost: h, ca: ca, crt: crt, key: key}, fg
}

func goldmaneOf(t *testing.T, flows ...gmFlow) func(authority) *fakeGoldmane {
	return func(ca authority) *fakeGoldmane { return startGoldmane(t, ca, ca, flows...) }
}

var recent = time.Now().Unix()

func TestCalicoFlowsAreReadAggregatedAndFilteredForThePerson(t *testing.T) {
	deny := []gmHit{{kind: 9, action: 2}}
	s, fg := calicoScene(t, goldmaneOf(t,
		// web → api, seen by both ends: one connection count, not two.
		gmFlow{srcNS: "shop", srcName: "web-5cd4b6b569-*", dstNS: "shop", dstName: "api-6b4696b557-*", port: 80, proto: "tcp", reporter: 1, action: 1, conns: 21, end: recent},
		gmFlow{srcNS: "shop", srcName: "web-5cd4b6b569-*", dstNS: "shop", dstName: "api-6b4696b557-*", port: 80, proto: "tcp", reporter: 2, action: 1, conns: 21, end: recent,
			hits: []gmHit{{kind: 6, ns: "shop", name: "api-from-web", action: 1}}},
		// ops → api, refused at the destination by a named policy: the
		// source's own Allow of the same connection is not a second fact.
		gmFlow{srcNS: "ops", srcName: "probe-7f84c65549-*", dstNS: "shop", dstName: "api-6b4696b557-*", port: 80, proto: "tcp", reporter: 2, action: 2, conns: 12, end: recent,
			hits: []gmHit{{kind: 1, ns: "shop", name: "deny-ops", action: 2}, {kind: 9, action: 2}}},
		gmFlow{srcNS: "ops", srcName: "probe-7f84c65549-*", dstNS: "shop", dstName: "api-6b4696b557-*", port: 80, proto: "tcp", reporter: 1, action: 1, conns: 12, end: recent},
		// shop → outside, and a flow from a namespace the person may not list.
		gmFlow{srcNS: "shop", srcName: "web-5cd4b6b569-*", dstName: "pub", dstType: 4, dstNS: "-", port: 443, proto: "tcp", reporter: 1, action: 1, conns: 3, end: recent},
		gmFlow{srcNS: "secret", srcName: "vault-*", dstNS: "shop", dstName: "api-6b4696b557-*", port: 80, proto: "tcp", reporter: 2, action: 1, conns: 99, end: recent},
		// An egress refusal reads Deny at the source, with no named policy.
		gmFlow{srcNS: "shop", srcName: "stranger-1-*", dstNS: "ops", dstName: "probe-7f84c65549-*", port: 5432, proto: "tcp", reporter: 1, action: 2, conns: 4, end: recent, hits: deny},
	))
	// A pod the Goldmane name stands for, so that a namespace's view names its workload.
	s.pod(t, "shop", "web-5cd4b6b569-x7k2p", "10.0.0.30", map[string]string{"app": "web"})
	got := flowsOf(t, s.fakeHost, "")
	if got.Mode != "observed" || got.Source != "calico" || got.State != flowsRead || !got.Narrowed || got.Truncated {
		t.Fatalf("flows: %+v", got)
	}
	// 21 + 12 + 3 + 4 connections the person may see; 99 from "secret" are not counted.
	if got.Read != 40 || got.Forwarded != 24 || got.Dropped != 16 {
		t.Errorf("read %d, forwarded %d, dropped %d: %+v", got.Read, got.Forwarded, got.Dropped, got.Rows)
	}
	rows := map[string]FlowRow{}
	for _, r := range got.Rows {
		rows[r.From+">"+r.To+":"+r.Verdict] = r
		if strings.Contains(r.From, "secret") {
			t.Errorf("a flow from a namespace the person may not list is shown: %+v", r)
		}
	}
	if r := rows["shop/web-5cd4b6b569-*>shop/api-6b4696b557-*:FORWARDED"]; r.Count != 21 || r.Port != 80 || r.Protocol != "TCP" {
		t.Errorf("web → api: %+v", got.Rows)
	}
	if r := rows["ops/probe-7f84c65549-*>shop/api-6b4696b557-*:DROPPED"]; r.Count != 12 || r.Reason != "POLICY_DENIED" || strings.Join(r.DeniedBy, ",") != "shop/deny-ops" {
		t.Errorf("ops → api refusal: %+v", r)
	}
	if _, twice := rows["ops/probe-7f84c65549-*>shop/api-6b4696b557-*:FORWARDED"]; twice {
		t.Errorf("a connection refused at its destination is also drawn as forwarded: %+v", got.Rows)
	}
	if r := rows["shop/stranger-1-*>ops/probe-7f84c65549-*:DROPPED"]; r.Count != 4 || len(r.DeniedBy) != 0 {
		t.Errorf("egress refusal: %+v", r)
	}
	if r := rows["shop/web-5cd4b6b569-*>public network:FORWARDED"]; r.Count != 3 {
		t.Errorf("outside the cluster: %+v", got.Rows)
	}
	edges := map[string]FlowEdge{}
	for _, e := range got.Edges {
		edges[e.From+">"+e.To] = e
	}
	if e := edges["ns:ops>ns:shop"]; e.Dropped != 12 || strings.Join(e.DeniedBy, ",") != "shop/deny-ops" {
		t.Errorf("ops → shop edge: %+v", edges)
	}
	if e := edges["ns:shop>entity:world"]; e.Forwarded != 3 {
		t.Errorf("shop → world edge: %+v", edges)
	}
	// In one namespace's view a Goldmane group of pods is its workload.
	ns := flowsOf(t, s.fakeHost, "?ns=shop")
	found := false
	for _, e := range ns.Edges {
		if e.From == "wl:shop/web" && e.To == "ns:shop" && e.Forwarded == 21 {
			found = true
		}
	}
	if !found {
		t.Errorf("shop's edges name no workload for web-*: %+v", ns.Edges)
	}
	// What was asked: the last five minutes, one record per flow, at most flowCap.
	fg.mu.Lock()
	asked := fg.asked[0][5:]
	peers := fg.peers
	fg.mu.Unlock()
	var start, pageSize, interval uint64
	each(asked, func(num protowire.Number, _ protowire.Type, _ []byte, n uint64) {
		switch num {
		case 1:
			start = n
		case 4:
			pageSize = n
		case 7:
			interval = n
		}
	})
	if int64(start) != -300 || pageSize != flowCap || interval != 300 {
		t.Errorf("asked start %d, page_size %d, interval %d", int64(start), pageSize, interval)
	}
	if peers != 2 {
		t.Errorf("%d calls carried a verified client certificate, want 2", peers)
	}
	if s.tunnels != s.stopped || s.tunnels != 2 {
		t.Errorf("%d tunnels opened, %d stopped", s.tunnels, s.stopped)
	}
	// The person's own reads come before the tunnel, each time.
	if strings.Join(s.events[:3], " ") != "reveal:calico-system/whisker-backend-key-pair/tls.crt reveal:calico-system/whisker-backend-key-pair/tls.key tunnel:goldmane" {
		t.Errorf("order: %v", s.events)
	}
}

func TestEachWayGoldmaneFailsIsItsOwnState(t *testing.T) {
	ask := func(t *testing.T, s *scene) Flows {
		t.Helper()
		return flowsOf(t, s.fakeHost, "")
	}
	t.Run("neither hubble nor goldmane", func(t *testing.T) {
		h := shop(t)
		got := flowsOf(t, h, "")
		if got.State != flowsAbsent || got.Source != "" || !strings.Contains(got.Detail, "Goldmane") || !strings.Contains(got.Detail, "Hubble") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("hubble wins where both are installed", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		fr := startRelay(t, testFlow{srcNS: "shop", srcPod: "web", dstNS: "shop", dstPod: "api", port: 80, verdict: 1})
		s.add(t, "services", &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "hubble-relay"},
			Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "grpc", Port: 80}}}})
		s.tunnelTo = fr.addr()
		if got := ask(t, s); got.Source != "hubble" || got.State != flowsRead {
			t.Errorf("%+v", got)
		}
	})
	t.Run("goldmane where hubble is absent", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t, gmFlow{srcNS: "shop", srcName: "web-*", dstNS: "shop", dstName: "api-*", port: 80, proto: "tcp", reporter: 2, action: 1, conns: 1, end: recent}))
		if got := ask(t, s); got.Source != "calico" || got.State != flowsRead {
			t.Errorf("%+v", got)
		}
	})
	t.Run("the Secret refused", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.revealErr = fmt.Errorf("reveal: %w", extension.ErrRevealDenied)
		got := ask(t, s)
		if got.State != flowsRefused || got.Source != "calico" || !strings.Contains(got.Detail, "whisker-backend-key-pair") {
			t.Errorf("%+v", got)
		}
		if s.tunnels != 0 {
			t.Errorf("%d tunnels opened for a person refused the credential", s.tunnels)
		}
	})
	t.Run("the Secret could not be reviewed", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.revealErr = extension.ErrRevealUnreviewed
		if got := ask(t, s); got.State != flowsRefused || s.tunnels != 0 {
			t.Errorf("%+v, %d tunnels", got, s.tunnels)
		}
	})
	t.Run("the Secret is not there", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.revealErr = extension.ErrNoSuchSecret
		if got := ask(t, s); got.State != flowsTLS || s.tunnels != 0 {
			t.Errorf("%+v, %d tunnels", got, s.tunnels)
		}
	})
	t.Run("the Secret read failed", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.revealErr = fmt.Errorf("the api server is gone")
		if got := ask(t, s); got.State != flowsUnreachable || strings.Contains(got.Detail, "gone") {
			// The cause is not echoed: it may carry what the Secret held.
			t.Errorf("%+v", got)
		}
	})
	t.Run("the CA ConfigMap refused", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.listErr["configmaps"] = fmt.Errorf("list: %w", extension.ErrDenied)
		if got := ask(t, s); got.State != flowsRefused || s.tunnels != 0 || len(s.events) != 0 {
			t.Errorf("%+v, %d tunnels, %v", got, s.tunnels, s.events)
		}
	})
	t.Run("no CA ConfigMap", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.objects["configmaps"] = nil
		if got := ask(t, s); got.State != flowsTLS || s.tunnels != 0 {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a client certificate that is no pair", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.secrets["calico-system/whisker-backend-key-pair/tls.key"] = "not a key"
		got := ask(t, s)
		if got.State != flowsTLS || s.tunnels != 0 || strings.Contains(got.Detail, "not a key") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("the server's certificate is not signed by the CA the person reads", func(t *testing.T) {
		s, _ := calicoScene(t, func(ca authority) *fakeGoldmane {
			return startGoldmane(t, ca, newAuthority(t, "someone-else"))
		})
		got := ask(t, s)
		if got.State != flowsTLS || s.tunnels != s.stopped {
			t.Errorf("%+v", got)
		}
	})
	t.Run("goldmane refuses the client certificate", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		other := newAuthority(t, "someone-else")
		s.secrets["calico-system/whisker-backend-key-pair/tls.crt"], s.secrets["calico-system/whisker-backend-key-pair/tls.key"], _ = other.issue(t, "x")
		got := ask(t, s)
		if got.State != flowsTLS || s.tunnels != s.stopped || s.tunnels != 1 {
			t.Errorf("%+v, %d tunnels, %d stopped", got, s.tunnels, s.stopped)
		}
	})
	t.Run("plaintext where TLS is asked", func(t *testing.T) {
		s, _ := calicoScene(t, func(authority) *fakeGoldmane { return nil })
		s.tunnelTo = startRelay(t).addr() // a plaintext h2c server
		if got := ask(t, s); got.State != flowsTLS && got.State != flowsUnreachable {
			t.Errorf("%+v", got)
		}
	})
	t.Run("the tunnel refused", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.tunnelErr = fmt.Errorf("portforward: %w", extension.ErrDenied)
		if got := ask(t, s); got.State != flowsRefused {
			t.Errorf("%+v", got)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.tunnelTo = "127.0.0.1:1"
		if got := ask(t, s); got.State != flowsUnreachable || s.tunnels != s.stopped {
			t.Errorf("%+v, %d tunnels, %d stopped", got, s.tunnels, s.stopped)
		}
	})
	t.Run("a gRPC error", func(t *testing.T) {
		s, fg := calicoScene(t, goldmaneOf(t))
		fg.set(nil, 0, "13")
		if got := ask(t, s); got.State != flowsUnreachable || !strings.Contains(got.Detail, "status 13") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		if got := ask(t, s); got.State != flowsEmpty || got.Read != 0 || got.Source != "calico" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("the page itself refused", func(t *testing.T) {
		s, _ := calicoScene(t, goldmaneOf(t))
		s.listErr["pods"] = fmt.Errorf("list: %w", extension.ErrDenied)
		w := serve(t, s.fakeHost, "/x/kind/netinspect/flows")
		if w.Code != http.StatusForbidden || s.tunnels != 0 || len(s.events) != 0 {
			t.Errorf("answered %d after %d tunnels and %v: the person's listing comes before the credential and the tunnel", w.Code, s.tunnels, s.events)
		}
	})
}

// A person who may list no pod is asked nothing of Goldmane: no credential is
// read for them, no tunnel is dug.
func TestNoPodToShowReadsNoCredentialAndOpensNoTunnel(t *testing.T) {
	s, fg := calicoScene(t, goldmaneOf(t, gmFlow{srcNS: "secret", srcName: "vault-*", dstNS: "secret", dstName: "db-*", port: 5432, proto: "tcp", reporter: 2, action: 1, conns: 1, end: recent}))
	s.narrowed["pods"] = true
	s.objects["pods"] = nil
	got := flowsOf(t, s.fakeHost, "")
	if got.State != flowsEmpty || got.Read != 0 || !got.Narrowed || len(got.Rows) != 0 {
		t.Errorf("flows: %+v", got)
	}
	if s.tunnels != 0 || len(s.events) != 0 || len(fg.asked) != 0 {
		t.Errorf("%d tunnels, events %v, %d calls", s.tunnels, s.events, len(fg.asked))
	}
}

// Goldmane holds more than the cap: the answer says so, and reads no more
// than the cap.
func TestCalicoReadStopsAtTheCapAndSaysSo(t *testing.T) {
	flows := make([]gmFlow, flowCap+500)
	for i := range flows {
		flows[i] = gmFlow{srcNS: "shop", srcName: fmt.Sprintf("web-%d-*", i), dstNS: "shop", dstName: "api-*", port: 80, proto: "tcp", reporter: 2, action: 1, conns: 1, end: recent}
	}
	s, fg := calicoScene(t, goldmaneOf(t, flows...))
	fg.set(flows, len(flows), "0")
	got := flowsOf(t, s.fakeHost, "")
	if !got.Truncated || got.Read != flowCap || len(got.Rows) > maxFlowRows {
		t.Errorf("read %d, truncated %v, %d rows", got.Read, got.Truncated, len(got.Rows))
	}
	// The same, from the total alone: Goldmane says there are more than it sent.
	fg.set(flows[:10], 5000, "0")
	if got := flowsOf(t, s.fakeHost, ""); !got.Truncated || got.Read != 10 {
		t.Errorf("read %d, truncated %v", got.Read, got.Truncated)
	}
}

// A call whose context ends is closed at once: the server sees its client go
// (and goleak, in TestMain, sees no goroutine left behind).
func TestCancellingTheRequestClosesTheCall(t *testing.T) {
	s, fg := calicoScene(t, goldmaneOf(t))
	fg.block = make(chan struct{})
	t.Cleanup(func() { close(fg.block) })
	pair, err := tls.X509KeyPair([]byte(s.crt), []byte(s.key))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(s.ca.pem))
	cfg := &tls.Config{RootCAs: pool, ServerName: goldmaneServerName, Certificates: []tls.Certificate{pair}, NextProtos: []string{"h2"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := listGoldmane(ctx, fg.addr(), cfg, flowCap, flowWindow)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a canceled call answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not return after its context ended")
	}
	select {
	case <-fg.gone:
	case <-time.After(5 * time.Second):
		t.Error("Goldmane never saw its client go: the call was not closed")
	}
}

// A message over the ceiling is not read.
func TestAGoldmaneAnswerOverTheCeilingIsNotRead(t *testing.T) {
	ca := newAuthority(t, "tigera-operator-signer")
	_, _, serverPair := ca.issue(t, "goldmane", goldmaneServerName)
	_, _, pair := ca.issue(t, "client")
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		head := make([]byte, 5)
		binary.BigEndian.PutUint32(head[1:], maxGoldmaneMessage+1)
		w.Header().Set("Content-Type", "application/grpc")
		_, _ = w.Write(head)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	cfg := &tls.Config{RootCAs: pool, ServerName: goldmaneServerName, Certificates: []tls.Certificate{pair}, NextProtos: []string{"h2"}}
	_, _, err := listGoldmane(context.Background(), strings.TrimPrefix(srv.URL, "https://"), cfg, flowCap, flowWindow)
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Errorf("err = %v", err)
	}
}

// Real bytes: the answer of `goldmane.Flows/List` from Calico v3.33.0 on a
// kind cluster, captured with start_time_gte=-300 and
// aggregation_interval=300 (see testdata). Decoded by the code that reads the
// live answer.
func TestTheDecoderReadsWhatGoldmaneSent(t *testing.T) {
	data, err := os.ReadFile("testdata/goldmane-list.bin")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 5 || data[0] != 0 || int(binary.BigEndian.Uint32(data[1:5])) != len(data)-5 {
		t.Fatalf("not one gRPC message: %d bytes", len(data))
	}
	flows, total := decodeList(data[5:], nil, flowCap)
	if total != 25 || len(flows) != 25 {
		t.Fatalf("%d flows of %d", len(flows), total)
	}
	var deny, allow *flow
	for i := range flows {
		f := &flows[i]
		if f.srcNS == "clients" && strings.HasPrefix(f.srcPod, "stranger-") && f.dstNS == "shop" && f.port == 80 {
			if f.verdict == 2 && f.reporter == 2 {
				deny = f
			}
			if f.verdict == 1 && f.reporter == 1 {
				allow = f
			}
		}
	}
	if deny == nil || allow == nil {
		t.Fatalf("stranger → api not found in %d flows", len(flows))
	}
	if deny.protocol != "TCP" || deny.count < 12 || len(deny.deniedBy) != 0 || !strings.HasPrefix(deny.dstPod, "api-") || !strings.HasSuffix(deny.dstPod, "-*") {
		t.Errorf("deny: %+v", deny)
	}
	// Two reporters, one connection: after folding, the refusal stands alone.
	folded := dedupeReporters(flows)
	refused, forwarded := 0, 0
	for _, f := range folded {
		if f.srcNS == "clients" && strings.HasPrefix(f.srcPod, "stranger-") && f.dstNS == "shop" && f.port == 80 {
			if f.verdict == 2 {
				refused++
			} else {
				forwarded++
			}
		}
	}
	if refused != 1 || forwarded != 0 {
		t.Errorf("stranger → api after folding: %d refused, %d forwarded", refused, forwarded)
	}
	// The world is named, never a pod.
	for _, f := range flows {
		if f.srcNS == "" && (f.srcIP == "" && f.srcPod != "") {
			t.Errorf("a network end named as a pod: %+v", f)
		}
	}
	if !strings.Contains(fmt.Sprint(flows), "private network") {
		t.Errorf("no end outside the cluster in the capture")
	}
}

// 10 000 flows sent where 2 000 were asked for: the read stops at the cap, and
// stays bounded in time and memory.
func TestLoadTenThousandGoldmaneFlows(t *testing.T) {
	if testing.Short() {
		t.Skip("sends 10 000 flows")
	}
	flows := make([]gmFlow, 10_000)
	for i := range flows {
		flows[i] = gmFlow{srcNS: "shop", srcName: fmt.Sprintf("web-%d-*", i%300), dstNS: "ops", dstName: fmt.Sprintf("probe-%d-*", i%40),
			port: uint64(8000 + i%50), proto: "tcp", reporter: 2, action: uint64(1 + i%2), conns: int64(1 + i%7), end: recent,
			hits: []gmHit{{kind: 1, ns: "ops", name: "deny", action: 2}}}
	}
	s, fg := calicoScene(t, goldmaneOf(t, flows...))
	fg.set(flows, len(flows), "0")
	limit := loadTime
	allocLimit := uint64(loadAllocated)
	if raceEnabled {
		limit *= raceSlowdown
		allocLimit *= raceAllocation
	}
	took, allocated, retained, size := measureRequest(t, s.fakeHost, "/x/kind/netinspect/flows")
	t.Logf("10 000 flows sent: %v, %.1f MiB allocated, %.2f MiB retained, %d KiB answer", took, float64(allocated)/(1<<20), float64(retained)/(1<<20), size>>10)
	if took > limit || allocated > allocLimit || retained > loadRetained || size > loadAnswerBytes {
		t.Errorf("over a ceiling")
	}
	got := flowsOf(t, s.fakeHost, "")
	if !got.Truncated || len(got.Rows) > maxFlowRows || len(got.Rows) == 0 {
		t.Errorf("truncated %v, %d rows", got.Truncated, len(got.Rows))
	}
	if s.tunnels != s.stopped {
		t.Errorf("%d tunnels, %d stopped", s.tunnels, s.stopped)
	}
}

func TestReportersAreFoldedOnce(t *testing.T) {
	a := flow{srcNS: "a", srcPod: "x-*", dstNS: "b", dstPod: "y-*", port: 80, protocol: "TCP", verdict: 1, count: 5}
	srcSide, dstSide := a, a
	srcSide.reporter, dstSide.reporter = 1, 2
	dstSide.count = 7
	got := dedupeReporters([]flow{srcSide, dstSide})
	if len(got) != 1 || got[0].count != 7 {
		t.Errorf("both ends saw one connection: %+v", got)
	}
	// Two time buckets of one reporter add up; the other reporter's total is compared.
	got = dedupeReporters([]flow{srcSide, srcSide, dstSide})
	if len(got) != 1 || got[0].count != 10 {
		t.Errorf("%+v", got)
	}
	// A refusal at the destination removes the source's Allow of it, not a refusal at the source.
	denied := a
	denied.verdict, denied.reporter = 2, 2
	got = dedupeReporters([]flow{srcSide, denied})
	if len(got) != 1 || got[0].verdict != 2 {
		t.Errorf("%+v", got)
	}
	// A Pass or an unknown action is not a decision.
	if _, ok := decodeFlowResult(gmFlow{srcNS: "a", srcName: "x-*", dstNS: "b", dstName: "y-*", port: 80, proto: "tcp", reporter: 1, action: 3}.encode()); ok {
		t.Errorf("a pass was read as a decision")
	}
}
