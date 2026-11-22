package access

// The clients an extension is handed through Host.Client answer discovery and
// nothing else.
//
// Everything an extension reads about the objects of a cluster goes through
// the console's reviewed doors (Host.List, Host.Reveal and the others), which
// review it for the person behind the request. A client that could read an
// object would be a read the console makes with its own credentials -
// under the server profile its ServiceAccount's - for whoever reached the
// page. So the transport behind those clients lets through only what tells
// which kinds the cluster serves, and refuses the rest before it leaves the
// process: an object, a list, a watch, a log, a proxy, and every change
// (which Guard would refuse too).
//
// What is discovery, relative to the API server's root:
//
//	/api            /api/{version}
//	/apis           /apis/{group}   /apis/{group}/{version}
//	/version
//
// GET or HEAD only, and never an upgrade. One segment more is a resource
// (`/api/v1/pods`, `/apis/apps/v1/deployments`), and is refused.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/client-go/rest"
)

// ErrNotDiscovery is what a request refused by DiscoveryOnly unwraps to, through
// client-go's own wrapping.
var ErrNotDiscovery = errors.New("the clients handed to an extension answer discovery only")

// NotDiscovery is DiscoveryOnly's refusal: a request for something other than
// which kinds the cluster serves, held back before it left the process. Never a
// 403: it is not the cluster's answer about anyone, and read as one it would
// send the reader after a RoleBinding.
type NotDiscovery struct {
	Cluster string
	Method  string
	// Path is relative to the API server's root.
	Path string
}

func (n *NotDiscovery) Error() string {
	return fmt.Sprintf("the console did not send %s %s to cluster %q: the clients an extension is handed answer "+
		"which kinds the cluster serves and nothing else. An extension reads objects through Host.List, which "+
		"reviews what it reads for the person", n.Method, n.Path, n.Cluster)
}

func (n *NotDiscovery) Is(target error) bool { return target == ErrNotDiscovery }

// DiscoveryOnly wraps the transport of a client built from cfg for cluster so
// that it sends discovery and refuses everything else. cfg is read for the API
// server's base path, as Guard reads it, so a server under a prefix is parsed
// from its own root.
func DiscoveryOnly(cfg *rest.Config, cluster string) func(http.RoundTripper) http.RoundTripper {
	root := ""
	if cfg != nil {
		if base, _, err := rest.DefaultServerUrlFor(cfg); err == nil {
			root = strings.TrimRight(base.Path, "/")
		}
	}
	return func(next http.RoundTripper) http.RoundTripper {
		return &discoveryOnly{next: next, cluster: cluster, root: root}
	}
}

type discoveryOnly struct {
	next    http.RoundTripper
	cluster string
	root    string
}

func (d *discoveryOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	path, underRoot := (&guard{root: d.root}).relative(req.URL.Path)
	// An escaped spelling (RawPath set) is refused whatever it decodes to: the
	// API server reads the path as sent.
	if underRoot && req.URL.RawPath == "" && IsDiscovery(req.Method, req.Header, path) {
		return d.next.RoundTrip(req)
	}
	// A RoundTripper closes the body it was handed, on an error as on success.
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, &NotDiscovery{Cluster: d.cluster, Method: req.Method, Path: path}
}

// WrappedRoundTripper lets client-go's own helpers reach the transport
// underneath, as Guard's does.
func (d *discoveryOnly) WrappedRoundTripper() http.RoundTripper { return d.next }

// IsDiscovery is whether a request, its path relative to the API server's
// root, asks only which kinds the cluster serves (see the file comment).
func IsDiscovery(method string, header http.Header, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(header.Get("Connection")), "upgrade") {
		return false
	}
	if strings.Contains(path, "%") || strings.Contains(path, "//") {
		// An escaped or doubled separator is not a spelling discovery uses, and
		// the API server may read it as one segment more.
		return false
	}
	parts := splitPath(path)
	for _, part := range parts {
		if part == "." || part == ".." {
			return false
		}
	}
	switch {
	case len(parts) == 1:
		return parts[0] == "api" || parts[0] == "apis" || parts[0] == "version"
	case len(parts) == 2:
		return parts[0] == "api" || parts[0] == "apis"
	case len(parts) == 3:
		return parts[0] == "apis"
	}
	return false
}
