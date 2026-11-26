package netinspect

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/protobuf/encoding/protowire"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// Observed traffic, from Calico: what Goldmane, the flow aggregator of
// Calico Open Source 3.30 and later (installed by the Tigera operator), says
// passed and was refused. It is the same mode as Hubble's, with the same
// bounds and the same filter for the person: this file is only how Goldmane
// is found, authenticated to and read.
//
// **No Calico module and no gRPC library.** `goldmane.Flows/List` is one
// unary gRPC call, spoken on the same hand-written HTTP/2 client as Hubble's
// (grpcStream), with the field numbers of Calico's `goldmane/proto/api.proto`
// (Apache-2.0); nothing of that file is copied.
//
// **mTLS, with the person's own reading of the credential.** Goldmane serves
// TLS only and asks for a client certificate signed by the Tigera CA - and
// asks Kubernetes nothing, so whoever holds such a certificate reads every
// flow. The console therefore uses the pair Whisker itself uses, the Secret
// `calico-system/whisker-backend-key-pair`, read through Host.Reveal: reviewed
// for the person and recorded, never with the console's own account on their
// behalf. A person who may not read that Secret has no Calico flows (state
// refused). The Tigera CA that signs Goldmane's certificate is public, in the
// ConfigMap `calico-system/goldmane-ca-bundle`, read through Host.List. Both
// are read before the tunnel is dug.
//
// **One read.** `start_time_gte` is relative (-300: the last five minutes),
// `aggregation_interval` is the whole window so that a flow is one record, and
// `page_size` is flowCap. Goldmane's per-field filters are ANDed, so the
// person's filter is applied here to what comes back, as for Hubble past
// whitelistCap. One namespace's read also asks for that namespace at either
// end (`Filter.endpoints`, `either`), so the cap is not spent on the rest of
// the cluster; a Goldmane too old to know that field ignores it, and the
// filter here still holds.
const (
	goldmaneNamespace    = "calico-system"
	goldmaneService      = "goldmane"
	goldmanePort         = 7443
	goldmaneClientSecret = "whisker-backend-key-pair"
	goldmaneCAMap        = "goldmane-ca-bundle"
	// goldmaneServerName is in the certificate's names, whatever address the
	// tunnel is dialled on.
	goldmaneServerName = "goldmane.calico-system.svc"
	// maxGoldmaneMessage is the largest List answer read: flowCap flows with
	// their labels and policy traces.
	maxGoldmaneMessage = 16 << 20
)

var configMapsGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// findGoldmane reads the Goldmane Service for the person: absent, or the port
// its gRPC is served on.
func findGoldmane(r *http.Request, h extension.Host, cluster string) feed {
	fd := feed{source: "calico", namespace: goldmaneNamespace, service: goldmaneService, prepare: prepareGoldmane}
	name := goldmaneNamespace + "/" + goldmaneService
	page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR, Namespace: goldmaneNamespace,
		FieldSelector: "metadata.name=" + goldmaneService, Limit: 1})
	if err != nil {
		if _, refused := extension.RefusedRead(err, ""); refused {
			fd.state, fd.detail = flowsRefused, "You may not read the "+name+" Service."
		} else {
			fd.state, fd.detail = flowsUnreachable, "The "+name+" Service could not be read: "+short(err)
		}
		return fd
	}
	if len(page.Items) == 0 {
		fd.state, fd.detail = flowsAbsent, "No "+name+" Service: Calico Goldmane is not installed."
		return fd
	}
	var svc corev1.Service
	if err := convert(&page.Items[0], &svc); err != nil {
		fd.state, fd.detail = flowsUnreachable, "The Goldmane Service could not be read: "+short(err)
		return fd
	}
	fd.port = goldmanePort
	found := false
	for _, p := range svc.Spec.Ports {
		if p.Port == goldmanePort {
			found = true
		}
	}
	if !found {
		if len(svc.Spec.Ports) == 0 {
			fd.state, fd.detail = flowsUnreachable, "The Goldmane Service has no port."
			return fd
		}
		fd.port = int(svc.Spec.Ports[0].Port)
	}
	return fd
}

// prepareGoldmane reads, for the person, the CA and the client certificate,
// and answers the call that uses them - or the state that says which of them
// could not be had.
func prepareGoldmane(r *http.Request, h extension.Host, cluster string, _ map[string]bool) (fetcher, string, string) {
	secret := goldmaneNamespace + "/" + goldmaneClientSecret
	page, err := h.List(r, cluster, extension.ListQuery{GVR: configMapsGVR, Namespace: goldmaneNamespace,
		FieldSelector: "metadata.name=" + goldmaneCAMap, Limit: 1})
	if err != nil {
		if errors.Is(err, extension.ErrUnknownCluster) {
			return nil, flowsUnreachable, extension.UnknownClusterText(cluster)
		}
		if _, refused := extension.RefusedRead(err, ""); refused {
			return nil, flowsRefused, "You may not read the " + goldmaneNamespace + "/" + goldmaneCAMap + " ConfigMap, which holds the CA Goldmane's certificate is checked against."
		}
		return nil, flowsUnreachable, "The Goldmane CA could not be read: " + short(err)
	}
	if len(page.Items) == 0 {
		return nil, flowsTLS, "No " + goldmaneNamespace + "/" + goldmaneCAMap + " ConfigMap: Goldmane's CA is not where it is looked for."
	}
	var cm corev1.ConfigMap
	if err := convert(&page.Items[0], &cm); err != nil {
		return nil, flowsUnreachable, "The Goldmane CA could not be read: " + short(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cm.Data["ca.crt"])) {
		return nil, flowsTLS, "The " + goldmaneNamespace + "/" + goldmaneCAMap + " ConfigMap holds no certificate under ca.crt."
	}
	crt, state, detail := revealKey(r, h, cluster, "tls.crt", secret)
	if state != "" {
		return nil, state, detail
	}
	key, state, detail := revealKey(r, h, cluster, "tls.key", secret)
	if state != "" {
		return nil, state, detail
	}
	pair, err := tls.X509KeyPair([]byte(crt), []byte(key))
	if err != nil {
		// The error names no part of the key.
		return nil, flowsTLS, "The client certificate in " + secret + " is not a usable pair: " + short(err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: goldmaneServerName,
		Certificates: []tls.Certificate{pair}, NextProtos: []string{"h2"}}
	window, _ := windowOf(r)
	ns := r.URL.Query().Get("ns")
	return func(ctx context.Context, addr string) ([]flow, bool, error) {
		return listGoldmane(ctx, addr, cfg, flowCap, window, ns)
	}, "", ""
}

// revealKey is one key of Goldmane's client Secret, through the person's own
// reviewed, recorded read. Its sentence never holds the value.
func revealKey(r *http.Request, h extension.Host, cluster, key, secret string) (string, string, string) {
	value, err := h.Reveal(r, cluster, goldmaneNamespace, goldmaneClientSecret, key)
	switch {
	case err == nil:
		return value, "", ""
	case errors.Is(err, extension.ErrUnknownCluster):
		return "", flowsUnreachable, extension.UnknownClusterText(cluster)
	case errors.Is(err, extension.ErrRevealDenied):
		return "", flowsRefused, "You may not read the Secret " + secret + ", Goldmane's client certificate."
	case errors.Is(err, extension.ErrRevealUnreviewed):
		return "", flowsRefused, "The console could not review and record reading the Secret " + secret + ", so it read nothing."
	case errors.Is(err, extension.ErrNoSuchSecret):
		return "", flowsTLS, "No Secret " + secret + ": Goldmane's client certificate is not where it is looked for."
	case errors.Is(err, extension.ErrNoSuchKey):
		return "", flowsTLS, "The Secret " + secret + " holds no " + key + "."
	}
	return "", flowsUnreachable, "The Secret " + secret + " could not be read."
}

// isTLSFailure says a call failed in the TLS handshake or because the server
// refused the client's certificate: a TLS or x509 error, which Go words with
// "tls:" or "x509:" whether it is a local check or a remote alert.
func isTLSFailure(err error) bool {
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:")
}

// listGoldmane calls goldmane.Flows/List on addr through the tunnel, in TLS
// with the client certificate in cfg, and reads at most limit flows from the
// last window. truncated says Goldmane holds more than were read.
func listGoldmane(ctx context.Context, addr string, cfg *tls.Config, limit int, window time.Duration, namespace string) ([]flow, bool, error) {
	var seen readFailure
	transport := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			raw, err := d.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			conn := tls.Client(raw, cfg)
			if err := conn.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			heard, err := firstWord(ctx, conn)
			if err != nil {
				_ = raw.Close()
				return nil, err
			}
			return &watchedConn{Conn: heard, seen: &seen}, nil
		},
	}
	defer transport.CloseIdleConnections()
	var out []flow
	var total int64
	_, err := grpcStream(ctx, transport, "https://"+addr+"/goldmane.Flows/List", listRequest(limit, window, namespace), maxGoldmaneMessage,
		func(msg []byte) bool {
			var t int64
			out, t = decodeList(msg, out, limit)
			total = max(total, t)
			return true
		})
	if err != nil {
		// TLS 1.3 tells a client its certificate was refused after its own
		// handshake is over, and the HTTP/2 transport then reports only "client
		// conn could not be established": the connection's own word is the one
		// that names the cause.
		if cause := seen.get(); cause != nil && isTLSFailure(cause) {
			return nil, false, cause
		}
		return nil, false, err
	}
	return dedupeReporters(out), total > int64(len(out)) || len(out) >= limit, nil
}

// firstWordWait bounds how long a client waits for the server to speak first
// after a TLS 1.3 handshake. A variable so a test can shorten it.
var firstWordWait = 2 * time.Second

// firstWord reads the server's first word on a TLS 1.3 connection before the
// client writes anything (#141).
//
// In TLS 1.3 the client's handshake is over before the server has checked the
// client certificate. A server that refuses it sends an alert and closes; if
// the HTTP/2 preface the client wrote in the meantime reached it unread, the
// close is a TCP reset, which discards the alert still waiting to be read, and
// the call fails as "connection reset by peer" or "broken pipe": a refused
// certificate read as an unreachable Goldmane. With nothing written yet the
// close is a plain FIN, so the alert is read, every time.
//
// An HTTP/2 server speaks first (its SETTINGS frame; grpc-go writes it before it
// reads the client's preface), so the wait is short. It is bounded all the
// same, by firstWordWait alone: a server that waits for the client is still
// asked, two seconds later. The call's own deadline does not reach here, since
// net/http dials with context.WithoutCancel of the request's context, which
// carries neither a deadline nor a cancellation; the context is still read
// after the wait, for a caller that dials with its own. What was read stays in
// front of the connection's reads; a reset or a closed connection is returned
// as it is, never reworded.
func firstWord(ctx context.Context, conn *tls.Conn) (net.Conn, error) {
	if conn.ConnectionState().Version != tls.VersionTLS13 {
		return conn, nil // TLS 1.2 checks the client certificate inside the handshake.
	}
	if err := conn.SetReadDeadline(time.Now().Add(firstWordWait)); err != nil {
		return nil, err
	}
	// No goroutine watches the context: a cancelled call waits at most
	// firstWordWait, and is told so just below.
	r := bufio.NewReader(conn)
	_, err := r.Peek(1)
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	var timeout net.Error
	if err != nil && (!errors.As(err, &timeout) || !timeout.Timeout()) {
		return nil, err
	}
	// A read timeout leaves a TLS connection usable: crypto/tls keeps only a
	// failure that is not temporary.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &heardConn{Conn: conn, r: r}, nil
}

// heardConn is a connection whose first bytes were already read into r.
type heardConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *heardConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// decodeList reads a FlowListResult - meta (1) with the total number of
// results (2), flows (2) as FlowResults - adding to out at most limit flows
// in all, and says the total Goldmane holds.
func decodeList(msg []byte, out []flow, limit int) ([]flow, int64) {
	var total int64
	each(msg, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		switch {
		case num == 1 && typ == protowire.BytesType: // meta
			each(v, func(num protowire.Number, _ protowire.Type, _ []byte, n uint64) {
				if num == 2 {
					total = int64(n)
				}
			})
		case num == 2 && typ == protowire.BytesType && len(out) < limit: // FlowResult
			if f, ok := decodeFlowResult(v); ok {
				out = append(out, f)
			}
		}
	})
	return out, total
}

// readFailure keeps the first error a connection's read gave, other than the
// end of the stream.
type readFailure struct {
	mu  sync.Mutex
	err error
}

func (f *readFailure) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err == nil {
		f.err = err
	}
}

func (f *readFailure) get() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// watchedConn is a connection that remembers why a read of it failed.
type watchedConn struct {
	net.Conn
	seen *readFailure
}

func (c *watchedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil && !errors.Is(err, io.EOF) {
		c.seen.set(err)
	}
	return n, err
}

// listRequest is a FlowListRequest: start_time_gte (1), a negative number of
// seconds before now; page_size (4); aggregation_interval (7), the window, so
// that a flow is one record.
func listRequest(limit int, window time.Duration, namespace string) []byte {
	secs := int64(window / time.Second)
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(-secs))
	b = protowire.AppendTag(b, 4, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(limit))
	if namespace != "" {
		// filter (6) { endpoints (11) { either (3) { namespace (1) { value (1) } } } }:
		// the namespace at either end, matched exactly.
		var match, end, endpoints, filter []byte
		match = protowire.AppendTag(match, 1, protowire.BytesType)
		match = protowire.AppendString(match, namespace)
		end = protowire.AppendTag(end, 1, protowire.BytesType)
		end = protowire.AppendBytes(end, match)
		endpoints = protowire.AppendTag(endpoints, 3, protowire.BytesType)
		endpoints = protowire.AppendBytes(endpoints, end)
		filter = protowire.AppendTag(filter, 11, protowire.BytesType)
		filter = protowire.AppendBytes(filter, endpoints)
		b = protowire.AppendTag(b, 6, protowire.BytesType)
		b = protowire.AppendBytes(b, filter)
	}
	b = protowire.AppendTag(b, 7, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(secs))
	return b
}

// decodeFlowResult reads a FlowResult {id (1), flow (2)}; ok is false for a
// flow that is neither allowed nor denied (a pass is not a decision) or that
// has no key.
func decodeFlowResult(b []byte) (flow, bool) {
	var f flow
	ok := false
	each(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num == 2 && typ == protowire.BytesType {
			f, ok = decodeGoldmaneFlow(v)
		}
	})
	return f, ok
}

// decodeGoldmaneFlow reads the fields of a Flow this page shows.
func decodeGoldmaneFlow(b []byte) (flow, bool) {
	var f flow
	haveKey := false
	var started, live int64
	each(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) {
		switch {
		case num == 1 && typ == protowire.BytesType: // key
			haveKey = true
			decodeGoldmaneKey(v, &f)
		case num == 3 && typ == protowire.VarintType: // end_time
			f.time = time.Unix(int64(n), 0)
		case num == 10 && typ == protowire.VarintType: // num_connections_started
			started = int64(n)
		case num == 12 && typ == protowire.VarintType: // num_connections_live
			live = int64(n)
		}
	})
	switch {
	case started > 0:
		f.count = int(min(started, 1<<30))
	case live > 0:
		f.count = int(min(live, 1<<30))
	default:
		f.count = 1
	}
	return f, haveKey && f.verdict != 0
}

func decodeGoldmaneKey(b []byte, f *flow) {
	var srcType, dstType uint64
	var srcName, srcNS, dstName, dstNS string
	each(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) {
		switch {
		case num == 1 && typ == protowire.BytesType:
			srcName = string(v)
		case num == 2 && typ == protowire.BytesType:
			srcNS = string(v)
		case num == 3 && typ == protowire.VarintType:
			srcType = n
		case num == 4 && typ == protowire.BytesType:
			dstName = string(v)
		case num == 5 && typ == protowire.BytesType:
			dstNS = string(v)
		case num == 6 && typ == protowire.VarintType:
			dstType = n
		case num == 7 && typ == protowire.VarintType:
			f.port = uint32(n)
		case num == 12 && typ == protowire.BytesType:
			f.protocol = strings.ToUpper(string(v))
		case num == 13 && typ == protowire.VarintType:
			f.reporter = int(n)
		case num == 14 && typ == protowire.VarintType: // action
			switch n {
			case 1:
				f.verdict = 1 // Allow: forwarded
			case 2:
				f.verdict = 2 // Deny: dropped
				f.reason = 133
			}
		case num == 15 && typ == protowire.BytesType: // policies
			each(v, func(num protowire.Number, typ protowire.Type, hit []byte, _ uint64) {
				if num == 1 && typ == protowire.BytesType { // enforced_policies
					if name, ok := deniedBy(hit); ok && len(f.deniedBy) < labelsPerEdge {
						f.deniedBy = append(f.deniedBy, name)
					}
				}
			})
		}
	})
	f.srcNS, f.srcPod, f.srcIP = goldmaneEnd(srcType, srcNS, srcName)
	f.dstNS, f.dstPod, f.dstIP = goldmaneEnd(dstType, dstNS, dstName)
}

// goldmaneEnd names a flow end as the rest of the page does: a workload is its
// namespace and group of pods; anything else is outside the cluster, named by
// what Goldmane calls it (a private or public network, a host, a network set).
func goldmaneEnd(typ uint64, ns, name string) (string, string, string) {
	if typ == 1 && ns != "" && ns != "-" {
		return ns, name, ""
	}
	switch name {
	case "pvt":
		return "", "", "private network"
	case "pub":
		return "", "", "public network"
	case "", "-":
		return "", "", ""
	}
	return "", "", name
}

// deniedBy is the name of a policy hit that refused: a Deny by a policy that
// has a name. A Profile or an end-of-tier Deny, which is how a
// NetworkPolicy's isolation reads, has none.
func deniedBy(hit []byte) (string, bool) {
	var kind, action uint64
	var ns, name string
	each(hit, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) {
		switch {
		case num == 1 && typ == protowire.VarintType:
			kind = n
		case num == 2 && typ == protowire.BytesType:
			ns = string(v)
		case num == 3 && typ == protowire.BytesType:
			name = string(v)
		case num == 5 && typ == protowire.VarintType:
			action = n
		}
	})
	if action != 2 || name == "" || kind == 9 || kind == 10 { // Profile, EndOfTier
		return "", false
	}
	if ns != "" {
		name = ns + "/" + name
	}
	return name, true
}

// dedupeReporters folds what Goldmane says twice. A connection is measured at
// its source and at its destination, so each shows up once per reporter; a
// refusal at the destination is also an Allow at the source, which left
// nothing wrong. Per (source, destination, port, protocol, verdict) the larger
// of the two reporters' counts stands, and an Allow the source reported is
// dropped where the destination refused the same connection.
func dedupeReporters(flows []flow) []flow {
	type key struct {
		srcNS, srcPod, srcIP, dstNS, dstPod, dstIP, protocol string
		port                                                 uint32
	}
	type verdictKey struct {
		key
		verdict int
	}
	of := func(f *flow) key {
		return key{f.srcNS, f.srcPod, f.srcIP, f.dstNS, f.dstPod, f.dstIP, f.protocol, f.port}
	}
	refusedAtDestination := map[key]bool{}
	for i := range flows {
		if f := &flows[i]; f.verdict == 2 && f.reporter == 2 {
			refusedAtDestination[of(f)] = true
		}
	}
	type slot struct {
		f        flow
		reporter map[int]int
	}
	slots := map[verdictKey]*slot{}
	var order []verdictKey
	for i := range flows {
		f := flows[i]
		if f.verdict == 1 && f.reporter == 1 && refusedAtDestination[of(&f)] {
			continue
		}
		k := verdictKey{of(&f), f.verdict}
		s, ok := slots[k]
		if !ok {
			s = &slot{f: f, reporter: map[int]int{}}
			s.f.count = 0
			slots[k] = s
			order = append(order, k)
		}
		s.reporter[f.reporter] += max(f.count, 1)
		if f.time.After(s.f.time) {
			s.f.time = f.time
		}
		for _, p := range f.deniedBy {
			if !contains(s.f.deniedBy, p) && len(s.f.deniedBy) < labelsPerEdge {
				s.f.deniedBy = append(s.f.deniedBy, p)
			}
		}
	}
	out := make([]flow, 0, len(order))
	for _, k := range order {
		s := slots[k]
		for _, n := range s.reporter {
			s.f.count = max(s.f.count, n)
		}
		out = append(out, s.f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].count > out[j].count })
	return out
}

// workloadOfGroup is the workload of the pods a Goldmane name stands for
// (`<generateName>*`): the first pod of that namespace whose name starts so.
// Remembered for the request.
func (rd *read) workloadOfGroup(ns, group string) string {
	prefix, ok := strings.CutSuffix(group, "*")
	if !ok || prefix == "" {
		return ""
	}
	key := ns + "/" + group
	if w, done := rd.groups[key]; done {
		return w
	}
	if rd.groups == nil {
		rd.groups = map[string]string{}
	}
	w := ""
	for i := range rd.model.Pods {
		if p := &rd.model.Pods[i]; p.Namespace == ns && strings.HasPrefix(p.Name, prefix) {
			w = p.Workload
			break
		}
	}
	rd.groups[key] = w
	return w
}
