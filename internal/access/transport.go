package access

// The runtime half of "every change to a cluster is reviewed": a transport
// that refuses a request which changes something unless Gate.Perform reviewed
// that request.
//
// choke_test.go proves from the source that every write-capable call sits in
// a closure handed to Perform, but it cannot see: a write through reflection,
// an unlisted client-go method or a file the current GOOS does not build; a
// closure whose request is not the question reviewed; a closure run after
// Perform returned; or an extension, whose Host.Client is the cluster
// manager's own client. So every client the cluster manager builds sends
// through Guard, and a changing request leaves only when its context holds
// Perform's stamp for this cluster and this request. The walk is the fast
// build-time signal; this is the backstop when it is wrong or blind.
//
// Guard reads the request's attributes from its method and path as
// kube-apiserver does (parseRequest) and lets it go only when the reviewed
// question covers them:
//
//   - the same verb, group, resource and subresource, always;
//   - the same namespace and name, or none: a question about every namespace
//     or name is the wider one. Never the other way round;
//   - the same version where the stamp names one (RBAC never reads a version).
//
// A stamp about something else (`patch` reviewed and `DELETE` sent, another
// namespace, another cluster) is refused like an unstamped request, and the
// refusal names both, because that is a bug in the console.
//
// A change is every method but GET and HEAD (an unknown method is refused,
// not guessed at), plus these GETs:
//
//   - exec, attach and portforward on a pod: a WebSocket upgrade is a GET and
//     opens a shell or a tunnel, reviewed as `create` as kubectl's are;
//   - any upgrade (`Connection: Upgrade`): nothing the console reads is one,
//     so an upgrade is a stream opened onto something;
//   - the kubelet's streams through the node proxy (`nodes/{name}/proxy/`
//     then `exec`, `attach`, `portForward`, `run` or `cri`). kube-apiserver
//     authorizes them as `get nodes/proxy`, the grant the kubelet-stats read
//     needs, through which the kubelet runs a command in any pod on the node:
//     a grant for reading figures would otherwise be a shell. The node proxy's
//     other paths are reads. The deprecated `/api/v1/proxy/` prefix, unused
//     here, is refused whatever it names.
//
// Reads go by untouched; whether a person may read is read.go's question.
//
// Named exceptions:
//
//   - the reviews themselves (SubjectAccessReview, SelfSubjectAccessReview):
//     they cannot be asked about first, and the API server stores nothing;
//   - Housekeeping: a request on objects the console created itself, with
//     nobody to review (the node shell removing its own pods). Each use is a
//     stamp that must still cover its request, and choke_test.go lists them.
//
// In-process tunnels are not an exception: they are dug inside Perform, and
// Carry takes the stamp to dialers that lose their context.
//
// It is not a sandbox: code in this process can reach anything through
// reflection or unsafe. It holds honest code, the core's and an extra's, to
// the contract, and turns a mistake into a refusal when it is made rather
// than a write found later in the API server's audit under the
// ServiceAccount's name.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/identity"
)

// stamp is what Perform (or Housekeeping) leaves on the context of the request
// it lets go: the cluster and the question that was answered.
type stamp struct {
	cluster string
	attrs   Attributes
	// housekeeping marks a stamp nobody was reviewed for. It changes nothing
	// about coverage; a refusal names it as the console's own clean-up.
	housekeeping bool
	// held is set when Guard refuses a request carrying this stamp, so that
	// Perform's outcome record says `unsent` however the closure wrapped or
	// swallowed the refusal. Nil on a Housekeeping stamp.
	held *atomic.Bool
	// wire is what the client path saw of the request on its way out
	// (wire.go). Nil on a Housekeeping stamp.
	wire *wire
}

type stampKey struct{}

// stamped is ctx carrying the question Perform answered for cluster, the
// flag Guard sets if it holds back a request carrying it, and the record of
// what became of the request it lets go.
func stamped(ctx context.Context, cluster string, attrs Attributes, held *atomic.Bool, w *wire) context.Context {
	return context.WithValue(ctx, stampKey{}, stamp{cluster: cluster, attrs: attrs, held: held, wire: w})
}

func stampOf(ctx context.Context) (stamp, bool) {
	s, ok := ctx.Value(stampKey{}).(stamp)
	return s, ok
}

// Housekeeping is ctx stamped for one request the console makes on its own
// objects, with nobody behind it to review: the attributes of that request,
// on that cluster.
//
// An exception, not a door: it reviews nothing, so every use is listed in
// choke_test.go (housekeepingWrites) with its reason, and a new one fails
// that test. Guard still lets go only the request it covers, so a clean-up
// of one pod cannot become a delete of another.
func Housekeeping(ctx context.Context, cluster string, attrs Attributes) context.Context {
	return context.WithValue(ctx, stampKey{}, stamp{cluster: cluster, attrs: attrs, housekeeping: true})
}

// ErrNotReviewed is what a request Guard refused unwraps to:
// errors.Is(err, access.ErrNotReviewed) through client-go's own wrapping.
var ErrNotReviewed = errors.New("no access review was made for this request")

// Unreviewed is Guard's refusal: a request that would have changed a cluster,
// held back before it left the process.
//
// Not a Refused, and never a 403: Refused is the cluster's answer about a
// person; this is the console refusing to send what nobody asked about (a
// defect, or an extension writing with clients it was handed for reading).
// Read as a denial, it would send the reader after a RoleBinding.
type Unreviewed struct {
	Cluster string
	// Method and Path are the request as it would have been sent, the path
	// relative to the API server's root.
	Method string
	Path   string
	// Attributes are the request's own, as kube-apiserver would read them; zero
	// for a path that is not a resource at all.
	Attributes Attributes
	// Stamp is the question that was reviewed, when there was one and it did
	// not cover this request; StampCluster is the cluster it was asked of.
	Stamp        *Attributes
	StampCluster string
}

func (u *Unreviewed) Error() string {
	what := u.Method + " " + u.Path
	if u.Attributes.Resource != "" {
		what = strings.TrimSpace(u.Attributes.Verb + " " + qualified(u.Attributes))
		if u.Attributes.Name != "" {
			what += " " + u.Attributes.Name
		}
		if u.Attributes.Namespace != "" {
			what += " in namespace " + u.Attributes.Namespace
		}
	}
	if u.Stamp == nil {
		return fmt.Sprintf("the console did not send %s to cluster %q: no access review was made for it. "+
			"Every change to a cluster goes through the console's own reviewed door; an extension asks the "+
			"console for it rather than writing with the clients it was handed", what, u.Cluster)
	}
	return fmt.Sprintf("the console did not send %s to cluster %q: the access review made for it was about "+
		"%s on cluster %q, which does not cover it", what, u.Cluster, u.Stamp.String(), u.StampCluster)
}

func (u *Unreviewed) Is(target error) bool { return target == ErrNotReviewed }

// Guard puts the check in front of every request a client built from cfg
// sends to cluster (the name Perform is handed). logf, when set, logs each
// refusal with the identity on the request.
//
// Call it last, after every other change to cfg: it reads the API server's
// base path from cfg.Host, so a server under a prefix (a proxy at
// `/k8s/clusters/<id>`) is parsed from its own root. It goes on the config,
// so every client built from it (typed, dynamic, streaming, exec, forward
// dialers) carries it.
func Guard(cfg *rest.Config, cluster string, logf func(format string, args ...any)) {
	root := ""
	if base, _, err := rest.DefaultServerUrlFor(cfg); err == nil {
		root = strings.TrimRight(base.Path, "/")
	}
	cfg.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return &guard{next: next, watchable: watchable(next), cluster: cluster, root: root, logf: logf}
	})
}

type guard struct {
	next http.RoundTripper
	// watchable is whether next is a net/http Transport, whose trace says
	// when a request obtained a connection (wire.go).
	watchable bool
	cluster   string
	root      string
	logf      func(string, ...any)
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	path, underRoot := g.relative(req.URL.Path)
	attrs, resource := parseRequest(req.Method, path)
	if !underRoot {
		resource = false
	}
	if !changes(req, path, attrs) {
		return g.next.RoundTrip(req)
	}
	if resource && isReview(attrs) {
		return g.next.RoundTrip(req)
	}
	s, ok := stampOf(req.Context())
	if ok && resource && s.cluster == g.cluster && covers(s.attrs, attrs) {
		if s.wire != nil {
			return s.wire.roundTrip(g.next, g.watchable, req)
		}
		return g.next.RoundTrip(req)
	}

	refusal := &Unreviewed{Cluster: g.cluster, Method: req.Method, Path: path}
	if resource {
		refusal.Attributes = attrs
	}
	if ok {
		reviewed := s.attrs
		refusal.Stamp, refusal.StampCluster = &reviewed, s.cluster
		if s.held != nil {
			s.held.Store(true)
		}
	}
	// A RoundTripper closes the body it was handed, on an error as on success.
	if req.Body != nil {
		_ = req.Body.Close()
	}
	// Not a `write:` line: those record writes the door reviewed, all through
	// edit's Writer, and this request no door saw. Its own prefix keeps the
	// two apart.
	if g.logf != nil {
		kind := "unreviewed"
		if ok && s.housekeeping {
			kind = "housekeeping-mismatch"
		} else if ok {
			kind = "review-mismatch"
		}
		g.logf("%s", identity.Line(req.Context(), "unsent: cluster=%q %s reason=%s",
			g.cluster, describe(refusal), kind))
	}
	return nil, refusal
}

// WrappedRoundTripper lets client-go's own helpers (closing idle connections,
// the debugging wrappers) reach the transport underneath, as every wrapper of
// its own does.
func (g *guard) WrappedRoundTripper() http.RoundTripper { return g.next }

// relative is path from the API server's root, and whether it was under it at
// all. A request outside the root is not a request for a resource.
func (g *guard) relative(path string) (string, bool) {
	if g.root == "" {
		return path, true
	}
	if path == g.root {
		return "/", true
	}
	if below, ok := strings.CutPrefix(path, g.root+"/"); ok {
		return "/" + below, true
	}
	return path, false
}

// describe is a refusal as the audit line writes it: the attributes, or the
// method and path of something that was not a resource.
func describe(u *Unreviewed) string {
	if u.Attributes.Resource != "" {
		return u.Attributes.String()
	}
	return fmt.Sprintf("method=%s path=%s", u.Method, u.Path)
}

// Carry is the wrapper for a client whose requests lose their context on the
// way to the wire: it puts the stamp ctx carries onto every request that
// carries none.
//
// client-go's SPDY dialer builds its request with `http.NewRequest` and the
// WebSocket tunnel with `context.Background()`, so a tunnel dug inside
// Perform would reach Guard unstamped and be refused. A config built inside
// the closure and wrapped with Carry(ctx) takes the stamp to them.
//
// It moves a stamp and cannot make one. Build it inside the closure and use
// it only there: a config kept afterwards outlives the review it records.
func Carry(ctx context.Context) func(http.RoundTripper) http.RoundTripper {
	s, ok := stampOf(ctx)
	return func(next http.RoundTripper) http.RoundTripper {
		if !ok {
			return next
		}
		return &carrier{next: next, stamp: s}
	}
}

type carrier struct {
	next  http.RoundTripper
	stamp stamp
}

func (c *carrier) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, has := stampOf(req.Context()); has {
		return c.next.RoundTrip(req)
	}
	return c.next.RoundTrip(req.WithContext(context.WithValue(req.Context(), stampKey{}, c.stamp)))
}

func (c *carrier) WrappedRoundTripper() http.RoundTripper { return c.next }

// changes is whether a request would change something (see the file
// comment for each case). parseRequest has already read a pod stream's verb
// as `create`.
func changes(req *http.Request, path string, attrs Attributes) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return true
	}
	// `/api/v1/proxy/...` is the node proxy by another spelling and unused
	// here, so it is held back whatever it names.
	return streams(attrs) || upgrades(req.Header) || kubeletStream(path, attrs) || attrs.Verb == "proxy"
}

// upgrades is whether a request asks to switch protocols - `Connection:
// Upgrade` or an `Upgrade` header - which is how a WebSocket or a SPDY stream
// begins.
func upgrades(header http.Header) bool {
	if header.Get("Upgrade") != "" {
		return true
	}
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// kubeletStreams are the first segments of a kubelet path, under a node's
// proxy, that run something in a container or open a stream into one. Lower
// case: the kubelet spells one `portForward`, and a comparison that missed a
// spelling would be a shell through the read grant.
var kubeletStreams = map[string]bool{"exec": true, "attach": true, "portforward": true, "run": true, "cri": true}

// kubeletStream is whether path, read as attrs, goes through a node's proxy to
// one of the kubelet's streams.
func kubeletStream(path string, attrs Attributes) bool {
	if attrs.Group != "" || attrs.Resource != "nodes" || attrs.Subresource != "proxy" {
		return false
	}
	parts := splitPath(path)
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "nodes" && parts[i+1] == attrs.Name && parts[i+2] == "proxy" {
			return kubeletStreams[strings.ToLower(parts[i+3])]
		}
	}
	return false
}

// streams is a pod's exec, attach or portforward subresource.
func streams(attrs Attributes) bool {
	if attrs.Group != "" || attrs.Resource != "pods" {
		return false
	}
	switch attrs.Subresource {
	case "exec", "attach", "portforward":
		return true
	}
	return false
}

// isReview is the one request that cannot be reviewed first: asking whether a
// request may be made (kube.go's two calls).
func isReview(attrs Attributes) bool {
	return attrs.Verb == "create" && attrs.Group == "authorization.k8s.io" && attrs.Subresource == "" &&
		attrs.Name == "" && attrs.Namespace == "" &&
		(attrs.Resource == "subjectaccessreviews" || attrs.Resource == "selfsubjectaccessreviews")
}

// covers is whether an answer to the question reviewed is an answer to the
// request made. See the file comment for why an empty namespace or name in the
// question is the wider question rather than a different one.
func covers(reviewed, request Attributes) bool {
	switch {
	case reviewed.Verb != request.Verb, reviewed.Group != request.Group,
		reviewed.Resource != request.Resource, reviewed.Subresource != request.Subresource:
		return false
	case reviewed.Version != "" && reviewed.Version != request.Version:
		return false
	case reviewed.Namespace != "" && reviewed.Namespace != request.Namespace:
		return false
	case reviewed.Name != "" && reviewed.Name != request.Name:
		return false
	}
	return true
}

// specialVerbs are the path prefixes kube-apiserver reads as a verb of their
// own: `/api/v1/watch/...` and the deprecated `/api/v1/proxy/...`.
var specialVerbs = map[string]bool{"proxy": true, "watch": true}

// parseRequest reads a request's attributes from its method and its path
// from the API server's root the way kube-apiserver's RequestInfoFactory does
// (k8s.io/apiserver/pkg/endpoints/request/requestinfo.go), so the question
// compared is the one the API server would authorize:
//
//	/api/{version}/{resource}[/{name}[/{subresource}]]
//	/api/{version}/namespaces/{namespace}/{resource}[/{name}[/{subresource}]]
//	/apis/{group}/{version}/...  (the same, with a group)
//
// It answers false for a path that is not a resource request (discovery,
// `/version`, `/healthz`, anything off `/api` and `/apis`).
//
// It keeps the API server's readings: `/api/v1/namespaces/{name}` is the
// namespace `name` in namespace `name` (RequestNamespace), and no name turns
// `delete` into `deletecollection`. And the console's own: exec, attach and
// portforward on a pod are `create` whatever the method.
func parseRequest(method, path string) (Attributes, bool) {
	parts := splitPath(path)
	if len(parts) < 3 || (parts[0] != "api" && parts[0] != "apis") {
		return Attributes{}, false
	}
	var attrs Attributes
	prefix := parts[0]
	parts = parts[1:]
	if prefix == "apis" {
		// `/apis/{group}/{version}` alone is discovery.
		if len(parts) < 3 {
			return Attributes{}, false
		}
		attrs.Group = parts[0]
		parts = parts[1:]
	}
	attrs.Version = parts[0]
	parts = parts[1:]

	if specialVerbs[parts[0]] {
		if len(parts) < 2 {
			return Attributes{}, false
		}
		attrs.Verb = parts[0]
		parts = parts[1:]
	} else {
		switch method {
		case http.MethodPost:
			attrs.Verb = "create"
		case http.MethodGet, http.MethodHead:
			attrs.Verb = "get"
		case http.MethodPut:
			attrs.Verb = "update"
		case http.MethodPatch:
			attrs.Verb = "patch"
		case http.MethodDelete:
			attrs.Verb = "delete"
		default:
			// A method the API server has no verb for: an empty verb, which no
			// review asks about, so nothing covers it.
			attrs.Verb = ""
		}
	}

	// `/namespaces/{namespace}/{resource}/...` - unless what follows the
	// namespace's name is one of the namespace's own subresources, in which
	// case the request is about the namespace itself.
	if parts[0] == "namespaces" && len(parts) > 1 {
		attrs.Namespace = parts[1]
		if len(parts) > 2 && parts[2] != "status" && parts[2] != "finalize" {
			parts = parts[2:]
		}
	}

	if len(parts) >= 3 && attrs.Verb != "proxy" {
		attrs.Subresource = parts[2]
	}
	if len(parts) >= 2 {
		attrs.Name = parts[1]
	}
	attrs.Resource = parts[0]

	if attrs.Name == "" && attrs.Verb == "delete" {
		attrs.Verb = "deletecollection"
	}
	if streams(attrs) {
		attrs.Verb = "create"
	}
	return attrs, true
}

// splitPath is the path's segments, as kube-apiserver splits them: trimmed of
// slashes at both ends, the empty path being no segments at all.
func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}
