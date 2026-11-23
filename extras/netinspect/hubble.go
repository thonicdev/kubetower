package netinspect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/protobuf/encoding/protowire"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// Observed traffic, from Hubble: what Cilium saw pass and drop, read from
// Hubble Relay's Observer API (`observer.Observer/GetFlows`), never
// confused with the rules. Every flow is something that happened; every
// rule is something that would.
//
// **No Cilium module and no gRPC library.** GetFlows is one server-streaming
// gRPC call: an HTTP/2 POST whose body and answer are protobuf messages, each
// framed by five bytes. It is spoken here on golang.org/x/net/http2 and
// google.golang.org/protobuf/encoding/protowire, which the console already
// links, with the field numbers of Cilium's `api/v1/flow/flow.proto` and
// `api/v1/observer/observer.proto` (Apache-2.0); nothing of those files is
// copied.
//
// **Bounded**: at most flowCap flows, from the last flowWindow, within
// flowDeadline, through one tunnel opened and closed inside the request;
// nothing is kept between requests.
//
// **Filtered for the person** before anything is shown: a flow is served
// only when each of its ends is outside the cluster or in a namespace where
// the person may list pods, as their own reviewed listing says. That
// listing is made before the tunnel is opened.

const (
	flowCap = 2_000
	// flowWindow is how far back the flows go when the page names no
	// window; flowWindows are the ones it may name (`?window=`, seconds).
	flowWindow   = 5 * time.Minute
	flowDeadline = 10 * time.Second
	// maxMessage is the largest one GetFlows answer may be: a flow is a few
	// hundred bytes; past this the stream is not read further.
	maxMessage = 1 << 20
	// maxFlowRows caps the aggregated rows an answer holds.
	maxFlowRows = 2_000
	// whitelistCap: up to this many namespaces, Relay is asked for their
	// flows only; past it, every flow is read and filtered here.
	whitelistCap   = 100
	relayNamespace = "kube-system"
	relayService   = "hubble-relay"
)

var servicesGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}

// Flow states: each is a different answer, and "empty" is the only one that
// means Hubble was read and saw nothing.
const (
	flowsRead        = "read"
	flowsEmpty       = "empty"
	flowsAbsent      = "absent"      // no Hubble Relay Service
	flowsRefused     = "refused"     // the review refused the tunnel or the listing
	flowsTLS         = "tls"         // Relay asks for TLS, which is not spoken here
	flowsUnreachable = "unreachable" // the tunnel or the call failed
)

// Flows is `GET /flows?ns=`: observed traffic, aggregated.
type Flows struct {
	// Mode is "observed": what the network plugin saw, never a rule.
	Mode string `json:"mode"`
	// Source is who saw it: "hubble" (Cilium) or "calico" (Goldmane); empty
	// when no source was found.
	Source string `json:"source"`
	State  string `json:"state"`
	// Detail says why for any state but read and empty.
	Detail string `json:"detail,omitempty"`
	// Window is how far back the flows go, in seconds.
	Window int `json:"window"`
	// Read is how many flows were read whose every end the person may see.
	// Narrowed says flows with an end in a namespace the person may not list
	// were left out: not how many, which would tell them how busy a namespace
	// they may not read is.
	Read     int  `json:"read"`
	Narrowed bool `json:"narrowed"`
	// Truncated says flowCap was reached: there may be more.
	Truncated bool       `json:"truncated"`
	Forwarded int        `json:"forwarded"`
	Dropped   int        `json:"dropped"`
	Rows      []FlowRow  `json:"rows"`
	Edges     []FlowEdge `json:"edges"`
}

// FlowRow is the flows between two endpoints on one port with one verdict.
type FlowRow struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Port     uint32 `json:"port"`
	Protocol string `json:"protocol"`
	// Verdict is FORWARDED, DROPPED, ERROR, AUDIT…
	Verdict string `json:"verdict"`
	// Reason is the drop reason, as Hubble names it.
	Reason string `json:"reason,omitempty"`
	// DeniedBy names the policies Hubble says dropped it, when it says.
	DeniedBy []string `json:"deniedBy"`
	Count    int      `json:"count"`
	Last     string   `json:"last"`
}

// FlowEdge is the flows between two graph nodes: the overview graph's own
// node names, so the page draws them over the rules.
type FlowEdge struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	Forwarded int      `json:"forwarded"`
	Dropped   int      `json:"dropped"`
	Ports     []string `json:"ports"`
	DeniedBy  []string `json:"deniedBy"`
}

// flow is one decoded Flow, reduced to what is shown.
type flow struct {
	time        time.Time
	verdict     int
	reason      int
	srcIP       string
	dstIP       string
	srcNS       string
	srcPod      string
	dstNS       string
	dstPod      string
	port        uint32
	protocol    string
	reply       bool
	deniedBy    []string
	srcIdentity uint32
	dstIdentity uint32
	// count is how many connections this record stands for: Goldmane
	// aggregates them, Hubble sends one record per flow (0 reads as 1).
	count int
	// reporter says which end measured it (Goldmane): 1 source, 2 destination.
	reporter int
}

var verdictNames = map[int]string{0: "UNKNOWN", 1: "FORWARDED", 2: "DROPPED", 3: "ERROR", 4: "AUDIT", 5: "REDIRECTED", 6: "TRACED", 7: "TRANSLATED"}

// reasonNames are the drop reasons a policy page meets; any other is said by
// its number.
var reasonNames = map[int]string{
	133: "POLICY_DENIED", 181: "POLICY_DENY", 189: "AUTH_REQUIRED", 158: "SERVICE_BACKEND_NOT_FOUND",
	151: "STALE_OR_UNROUTABLE_IP", 165: "NO_CONFIGURATION_AVAILABLE_TO_PERFORM_POLICY_DECISION",
	137: "CT_UNKNOWN_L4_PROTOCOL", 172: "UNKNOWN_SENDER", 196: "TTL_EXCEEDED", 198: "DROP_RATE_LIMITED",
}

// relay is how to reach Hubble Relay, or why it cannot be reached.
type relay struct {
	port   int
	state  string
	detail string
}

// findRelay reads the Relay Service for the person: absent, TLS-only, or its
// plaintext port.
func findRelay(r *http.Request, h extension.Host, cluster string) relay {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR, Namespace: relayNamespace,
		FieldSelector: "metadata.name=" + relayService, Limit: 1})
	if err != nil {
		if _, refused := extension.RefusedRead(err, ""); refused {
			return relay{state: flowsRefused, detail: "You may not read the " + relayNamespace + "/" + relayService + " Service."}
		}
		return relay{state: flowsUnreachable, detail: "The " + relayNamespace + "/" + relayService + " Service could not be read: " + short(err)}
	}
	if len(page.Items) == 0 {
		return relay{state: flowsAbsent, detail: "No " + relayNamespace + "/" + relayService + " Service: Hubble Relay is not installed."}
	}
	var svc corev1.Service
	if err := convert(&page.Items[0], &svc); err != nil {
		return relay{state: flowsUnreachable, detail: "The Hubble Relay Service could not be read: " + short(err)}
	}
	for _, p := range svc.Spec.Ports {
		if p.Port == 80 || p.Name == "grpc" {
			return relay{port: int(p.Port)}
		}
	}
	for _, p := range svc.Spec.Ports {
		if p.Port == 443 || strings.Contains(p.Name, "tls") || strings.Contains(p.Name, "https") {
			return relay{state: flowsTLS, detail: "Hubble Relay serves TLS only (port " + strconv.Itoa(int(p.Port)) + "), which this version does not speak."}
		}
	}
	if len(svc.Spec.Ports) > 0 {
		return relay{port: int(svc.Spec.Ports[0].Port)}
	}
	return relay{state: flowsUnreachable, detail: "The Hubble Relay Service has no port."}
}

// flowWindows are the periods a read may ask for, as the metrics' span
// buttons offer them. Beyond an hour Hubble's ring buffer and Goldmane's
// aggregation hold little anyway, and the read is bounded by flowCap.
var flowWindows = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// windowOf is the period `?window=` names, in seconds: flowWindow when it is
// absent, and false when it names anything but one of flowWindows.
func windowOf(r *http.Request) (time.Duration, bool) {
	raw := r.URL.Query().Get("window")
	if raw == "" {
		return flowWindow, true
	}
	for _, w := range flowWindows {
		if raw == strconv.Itoa(int(w/time.Second)) {
			return w, true
		}
	}
	return 0, false
}

// fetcher reads the flows through the tunnel at addr: at most flowCap, from
// the last flowWindow, truncated when there may be more.
type fetcher func(ctx context.Context, addr string) ([]flow, bool, error)

// feed is one source of observed traffic, found and ready to be dug to: the
// Service the console's tunnel leads to, and how to read flows from it. A
// feed with a state is one that cannot be read, and says why.
type feed struct {
	source, namespace, service string
	port                       int
	state, detail              string
	// prepare makes what the read needs from the person before any tunnel is
	// dug: nothing for Hubble Relay's plaintext, a client certificate for
	// Goldmane. It answers the fetcher, or the state that says why not.
	prepare func(r *http.Request, h extension.Host, cluster string, allowed map[string]bool) (fetcher, string, string)
}

// hubbleFeed is Hubble Relay as findRelay found it.
func hubbleFeed(rl relay) feed {
	return feed{source: "hubble", namespace: relayNamespace, service: relayService, port: rl.port, state: rl.state, detail: rl.detail,
		prepare: func(r *http.Request, _ extension.Host, _ string, allowed map[string]bool) (fetcher, string, string) {
			var whitelist []string
			if ns := r.URL.Query().Get("ns"); ns != "" {
				// One namespace's read asks Relay for that namespace only:
				// otherwise the cap is spent on the rest of the cluster, and a
				// longer window reads no further back than a short one.
				whitelist = []string{ns}
			} else if len(allowed) <= whitelistCap {
				for ns := range allowed {
					whitelist = append(whitelist, ns)
				}
				sort.Strings(whitelist)
			}
			window, _ := windowOf(r)
			return func(ctx context.Context, addr string) ([]flow, bool, error) {
				return getFlows(ctx, addr, flowCap, time.Now().Add(-window), whitelist)
			}, "", ""
		}}
}

// findFeed looks for Hubble Relay first, then Calico's Goldmane when Relay
// cannot be read; both missing is one answer, absent. Hubble's own refusal or
// failure stands when Goldmane is not there either.
func findFeed(r *http.Request, h extension.Host, cluster string) feed {
	rl := findRelay(r, h, cluster)
	hubble := hubbleFeed(rl)
	if rl.state == "" || rl.state == flowsTLS {
		return hubble
	}
	gm := findGoldmane(r, h, cluster)
	if gm.state == flowsAbsent {
		if rl.state == flowsAbsent {
			hubble.detail = "No " + relayNamespace + "/" + relayService + " or " + goldmaneNamespace + "/" + goldmaneService +
				" Service: neither Hubble Relay nor Calico Goldmane is installed."
		}
		return hubble
	}
	return gm
}

// observe reads the flows for namespace ("" for the cluster), filtered for
// the person through rd, whose listing was made before this is called.
func (rd *read) observe(r *http.Request, h extension.Host, cluster, namespace string) Flows {
	window, _ := windowOf(r)
	out := Flows{Mode: "observed", Window: int(window / time.Second), Rows: []FlowRow{}, Edges: []FlowEdge{}}
	fd := findFeed(r, h, cluster)
	if fd.state != "" {
		if fd.state != flowsAbsent {
			out.Source = fd.source
		}
		out.State, out.Detail = fd.state, fd.detail
		return out
	}
	out.Source = fd.source
	allowed := map[string]bool{}
	for i := range rd.model.Pods {
		allowed[rd.model.Pods[i].Namespace] = true
	}
	if len(allowed) == 0 {
		// No flow could be shown: the console's tunnel is not dug, and the
		// source is not asked for a cluster's traffic only to drop all of it.
		out.State, out.Narrowed = flowsEmpty, !rd.complete
		return out
	}
	// What the read needs from the person is read before the tunnel: a
	// refusal costs no tunnel.
	fetch, state, detail := fd.prepare(r, h, cluster, allowed)
	if state != "" {
		out.State, out.Detail = state, detail
		return out
	}
	ctx, cancel := context.WithTimeout(r.Context(), flowDeadline)
	defer cancel()
	name := fd.namespace + "/" + fd.service
	tunnel, err := h.ForwardAsConsole(r.WithContext(ctx), cluster, fd.namespace, fd.service, fd.port)
	if err != nil {
		if errors.Is(err, extension.ErrUnknownCluster) {
			out.State, out.Detail = flowsUnreachable, extension.UnknownClusterText(cluster)
		} else if _, refused := extension.RefusedRead(err, ""); refused {
			out.State, out.Detail = flowsRefused, "The tunnel to "+name+" was refused: "+short(err)
		} else {
			out.State, out.Detail = flowsUnreachable, name+" could not be reached: "+short(err)
		}
		return out
	}
	defer tunnel.Stop()
	flows, truncated, err := fetch(ctx, tunnel.Addr)
	if err != nil {
		if fd.source == "calico" && isTLSFailure(err) {
			out.State, out.Detail = flowsTLS, "Goldmane did not accept the TLS handshake: "+short(err)
		} else {
			out.State, out.Detail = flowsUnreachable, name+" did not answer: "+short(err)
		}
		return out
	}
	out.Truncated = truncated
	rd.aggregate(&out, flows, allowed, namespace)
	if len(out.Rows) == 0 {
		out.State = flowsEmpty
	} else {
		out.State = flowsRead
	}
	return out
}

// aggregate folds flows into rows and graph edges, keeping only those whose
// every end the person may see.
func (rd *read) aggregate(out *Flows, flows []flow, allowed map[string]bool, namespace string) {
	type rowKey struct {
		from, to, proto, verdict, reason string
		port                             uint32
	}
	rows := map[rowKey]*FlowRow{}
	edges := map[[2]string]*FlowEdge{}
	edgePorts := map[[2]string]map[string]bool{}
	edgeDenied := map[[2]string]map[string]bool{}
	for i := range flows {
		f := &flows[i]
		if (f.srcNS != "" && !allowed[f.srcNS]) || (f.dstNS != "" && !allowed[f.dstNS]) {
			out.Narrowed = true
			continue
		}
		w := max(f.count, 1)
		out.Read += w
		if f.reply {
			continue
		}
		if namespace != "" && f.srcNS != namespace && f.dstNS != namespace {
			continue
		}
		verdict := verdictNames[f.verdict]
		if verdict == "" {
			verdict = strconv.Itoa(f.verdict)
		}
		switch f.verdict {
		case 1:
			out.Forwarded += w
		case 2:
			out.Dropped += w
		}
		reason := ""
		if f.verdict == 2 {
			reason = reasonNames[f.reason]
			if reason == "" && f.reason != 0 {
				reason = "reason " + strconv.Itoa(f.reason)
			}
		}
		from, to := endName(f.srcNS, f.srcPod, f.srcIP), endName(f.dstNS, f.dstPod, f.dstIP)
		k := rowKey{from, to, f.protocol, verdict, reason, f.port}
		row, ok := rows[k]
		if !ok {
			if len(rows) >= maxFlowRows {
				out.Truncated = true
				continue
			}
			row = &FlowRow{From: from, To: to, Port: f.port, Protocol: f.protocol, Verdict: verdict, Reason: reason, DeniedBy: []string{}}
			rows[k] = row
		}
		row.Count += w
		if t := f.time.UTC().Format(time.RFC3339); t > row.Last {
			row.Last = t
		}
		for _, p := range f.deniedBy {
			if !contains(row.DeniedBy, p) && len(row.DeniedBy) < labelsPerEdge {
				row.DeniedBy = append(row.DeniedBy, p)
			}
		}
		ek := [2]string{rd.flowNode(f.srcNS, f.srcPod, namespace), rd.flowNode(f.dstNS, f.dstPod, namespace)}
		if ek[0] == ek[1] {
			continue
		}
		e, ok := edges[ek]
		if !ok {
			e = &FlowEdge{From: ek[0], To: ek[1], Ports: []string{}, DeniedBy: []string{}}
			edges[ek] = e
			edgePorts[ek] = map[string]bool{}
			edgeDenied[ek] = map[string]bool{}
		}
		if f.verdict == 2 {
			e.Dropped += w
		} else {
			e.Forwarded += w
		}
		if f.port != 0 && len(edgePorts[ek]) < labelsPerEdge {
			edgePorts[ek][fmt.Sprintf("%d/%s", f.port, f.protocol)] = true
		}
		for _, p := range f.deniedBy {
			if len(edgeDenied[ek]) < labelsPerEdge {
				edgeDenied[ek][p] = true
			}
		}
	}
	for _, row := range rows {
		out.Rows = append(out.Rows, *row)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	for k, e := range edges {
		e.Ports, e.DeniedBy = sortedKeys(edgePorts[k]), sortedKeys(edgeDenied[k])
		out.Edges = append(out.Edges, *e)
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		a, b := out.Edges[i], out.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// endName is a flow end as the table shows it: namespace/pod, or the address.
func endName(ns, pod, ip string) string {
	switch {
	case ns != "" && pod != "":
		return ns + "/" + pod
	case ns != "":
		return ns
	case ip != "":
		return ip
	}
	return "unknown"
}

// flowNode is a flow end as the overview graph names its nodes: the
// namespace in the cluster's view; in one namespace's, its pod's workload,
// and other namespaces whole; outside the cluster, "world" (Cilium's name
// for it).
func (rd *read) flowNode(ns, pod, focus string) string {
	if ns == "" {
		return "entity:world"
	}
	if focus != "" && ns == focus {
		if i, ok := rd.model.podIndex[ns+"/"+pod]; ok {
			return "wl:" + ns + "/" + rd.model.Pods[i].Workload
		}
		// Goldmane names a group of pods, `<generateName>*`, not a pod.
		if w := rd.workloadOfGroup(ns, pod); w != "" {
			return "wl:" + ns + "/" + w
		}
	}
	return "ns:" + ns
}

// --- the call -----------------------------------------------------------------

// getFlows calls observer.Observer/GetFlows on addr (a plaintext HTTP/2
// listener, Hubble Relay's default) and reads at most limit flows since the
// given time; truncated says the limit was reached. namespaces, when not
// empty, asks Relay for flows touching them only.
func getFlows(ctx context.Context, addr string, limit int, since time.Time, namespaces []string) ([]flow, bool, error) {
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()
	var out []flow
	truncated, err := grpcStream(ctx, transport, "http://"+addr+"/observer.Observer/GetFlows",
		flowsRequest(uint64(limit), since, namespaces), maxMessage, func(msg []byte) bool {
			if f, ok := decodeResponse(msg); ok {
				out = append(out, f)
			}
			return len(out) < limit
		})
	return out, truncated, err
}

// grpcStream posts one gRPC request and hands each answer message to
// onMessage, until it returns false (stopped: the call is closed there and
// stopped is true), the answer ends, or something fails. A message over
// maxBytes is not read; a compressed one, which is not asked for, is an
// error; a status other than OK, in the headers or the trailers, is an error.
func grpcStream(ctx context.Context, transport *http2.Transport, url string, request []byte, maxBytes uint32, onMessage func([]byte) bool) (stopped bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(grpcFrame(request)))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if status := resp.Header.Get("Grpc-Status"); status != "" && status != "0" {
		return false, fmt.Errorf("gRPC status %s: %s", status, resp.Header.Get("Grpc-Message"))
	}
	reader := bufio.NewReader(resp.Body)
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return false, err
		}
		if header[0] != 0 {
			return false, errors.New("a compressed message, which was not asked for")
		}
		n := binary.BigEndian.Uint32(header[1:])
		if n > maxBytes {
			return false, fmt.Errorf("a message of %d bytes, over %d", n, maxBytes)
		}
		msg := make([]byte, n)
		if _, err := io.ReadFull(reader, msg); err != nil {
			return false, err
		}
		if !onMessage(msg) {
			return true, nil
		}
	}
	if status := resp.Trailer.Get("Grpc-Status"); status != "" && status != "0" {
		return false, fmt.Errorf("gRPC status %s: %s", status, resp.Trailer.Get("Grpc-Message"))
	}
	return false, nil
}

func grpcFrame(msg []byte) []byte {
	out := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))
	return append(out, msg...)
}

// flowsRequest is a GetFlowsRequest: number (1), since (7), and one
// whitelist FlowFilter (6) per namespace and side, source_pod (2) or
// destination_pod (4) set to "<namespace>/", a prefix.
func flowsRequest(number uint64, since time.Time, namespaces []string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, number)
	for _, ns := range namespaces {
		for _, field := range []protowire.Number{2, 4} {
			var filter []byte
			filter = protowire.AppendTag(filter, field, protowire.BytesType)
			filter = protowire.AppendString(filter, ns+"/")
			b = protowire.AppendTag(b, 6, protowire.BytesType)
			b = protowire.AppendBytes(b, filter)
		}
	}
	var ts []byte
	ts = protowire.AppendTag(ts, 1, protowire.VarintType)
	ts = protowire.AppendVarint(ts, uint64(since.Unix()))
	b = protowire.AppendTag(b, 7, protowire.BytesType)
	b = protowire.AppendBytes(b, ts)
	return b
}

// decodeResponse reads a GetFlowsResponse; ok is false for anything but a
// flow (field 1): a node status, lost events.
func decodeResponse(b []byte) (flow, bool) {
	var f flow
	found := false
	each(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) {
		if num == 1 && typ == protowire.BytesType {
			f, found = decodeFlow(v), true
		}
	})
	return f, found
}

// decodeFlow reads the fields of a Flow this page shows.
func decodeFlow(b []byte) flow {
	var f flow
	each(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) {
		switch {
		case num == 1 && typ == protowire.BytesType: // time
			var sec, nsec uint64
			each(v, func(num protowire.Number, _ protowire.Type, _ []byte, n uint64) {
				if num == 1 {
					sec = n
				} else if num == 2 {
					nsec = n
				}
			})
			f.time = time.Unix(int64(sec), int64(nsec))
		case num == 2 && typ == protowire.VarintType: // verdict
			f.verdict = int(n)
		case num == 25 && typ == protowire.VarintType: // drop_reason_desc
			f.reason = int(n)
		case num == 5 && typ == protowire.BytesType: // IP
			each(v, func(num protowire.Number, _ protowire.Type, s []byte, _ uint64) {
				if num == 1 {
					f.srcIP = string(s)
				} else if num == 2 {
					f.dstIP = string(s)
				}
			})
		case num == 6 && typ == protowire.BytesType: // l4
			each(v, func(num protowire.Number, typ protowire.Type, l []byte, _ uint64) {
				if typ != protowire.BytesType {
					return
				}
				switch num {
				case 1:
					f.protocol = "TCP"
				case 2:
					f.protocol = "UDP"
				case 3, 4:
					f.protocol = "ICMP"
				case 5:
					f.protocol = "SCTP"
				default:
					return
				}
				each(l, func(num protowire.Number, _ protowire.Type, _ []byte, n uint64) {
					if num == 2 && f.protocol != "ICMP" {
						f.port = uint32(n)
					}
				})
			})
		case (num == 8 || num == 9) && typ == protowire.BytesType: // source, destination
			var ns, pod string
			var id uint32
			each(v, func(num protowire.Number, _ protowire.Type, s []byte, n uint64) {
				switch num {
				case 2:
					id = uint32(n)
				case 3:
					ns = string(s)
				case 5:
					pod = string(s)
				}
			})
			if num == 8 {
				f.srcNS, f.srcPod, f.srcIdentity = ns, pod, id
			} else {
				f.dstNS, f.dstPod, f.dstIdentity = ns, pod, id
			}
		case num == 26 && typ == protowire.BytesType: // is_reply
			each(v, func(num protowire.Number, _ protowire.Type, _ []byte, n uint64) {
				if num == 1 {
					f.reply = n != 0
				}
			})
		case (num == 21004 || num == 21005) && typ == protowire.BytesType: // egress/ingress_denied_by
			var name, ns string
			each(v, func(num protowire.Number, _ protowire.Type, s []byte, _ uint64) {
				if num == 1 {
					name = string(s)
				} else if num == 2 {
					ns = string(s)
				}
			})
			if ns != "" {
				name = ns + "/" + name
			}
			if name != "" && len(f.deniedBy) < labelsPerEdge {
				f.deniedBy = append(f.deniedBy, name)
			}
		}
	})
	return f
}

// each walks a message's fields: v is a length-delimited field's bytes, n a
// varint's value. A malformed message stops the walk where it breaks.
func each(b []byte, f func(num protowire.Number, typ protowire.Type, v []byte, n uint64)) {
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return
		}
		b = b[l:]
		switch typ {
		case protowire.VarintType:
			n, l := protowire.ConsumeVarint(b)
			if l < 0 {
				return
			}
			f(num, typ, nil, n)
			b = b[l:]
		case protowire.BytesType:
			v, l := protowire.ConsumeBytes(b)
			if l < 0 {
				return
			}
			f(num, typ, v, 0)
			b = b[l:]
		default:
			l := protowire.ConsumeFieldValue(num, typ, b)
			if l < 0 {
				return
			}
			b = b[l:]
		}
	}
}
