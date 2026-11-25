package web

import (
	"errors"
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/metrics"
)

var _ extension.PodForwarder = host{}

// ForwardPod is a tunnel to one port of one pod (extension.PodForwarder),
// dug by the forwarder for the person behind r: the forwarder digs it inside
// Gate.Perform, as `create pods/portforward` on the pod, so it is reviewed as
// them and recorded with the extension that asked, as Exec is: a tunnel to any
// port of a pod reaches about as far. Counted with Forward's tunnels
// (tunnelcap.go), and closed when r ends at the latest.
//
// A cluster in KT_READONLY_CLUSTERS refuses it (refuseReadOnly), as the
// console's own forward is refused there: a tunnel to a pod's diagnostics port
// can take a dump or attach a debugger, which the marking is about.
func (h host) ForwardPod(r *http.Request, name, namespace, pod string, port int) (extension.Tunnel, error) {
	if h.ext == "" {
		return extension.Tunnel{}, errors.New("ForwardPod is answered only to a mounted extension, so the record " +
			"can name who asked; this door was handed to none")
	}
	if r == nil {
		return extension.Tunnel{}, errors.New("a tunnel to a pod needs the request it is opened for")
	}
	if port < 1 || port > 65535 {
		return extension.Tunnel{}, fmt.Errorf("a port is between 1 and 65535, not %d", port)
	}
	if len(validation.IsDNS1123Label(namespace)) > 0 {
		return extension.Tunnel{}, fmt.Errorf("%q is not a namespace name", namespace)
	}
	if len(validation.IsDNS1123Subdomain(pod)) > 0 {
		return extension.Tunnel{}, fmt.Errorf("%q is not a pod name", pod)
	}
	// The server's tunnels are always counted (tunnelcap.go), and counted
	// tunnels say errNoPodTunnels for a forwarder that opens none.
	var pods metrics.PodTunneller
	if h.s != nil {
		pods, _ = h.s.tunnels.(metrics.PodTunneller)
	}
	if pods == nil {
		return extension.Tunnel{}, errors.New("this build has no port forwarder, so nothing inside a cluster can be reached")
	}
	resolved := name
	if h.s.clusters != nil {
		var ok bool
		if resolved, ok = h.s.clusters.Resolve(name); !ok {
			return extension.Tunnel{}, fmt.Errorf("%w: %q is not a context in the kubeconfig", extension.ErrUnknownCluster, name)
		}
	}
	ctx := r.Context()
	noted := access.WithNote(ctx, access.Note{Extension: h.ext})
	attrs := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "portforward",
		Namespace: namespace, Name: pod}
	if err := h.refuseReadOnly(noted, resolved, audit.PortForward, attrs, "opening a tunnel to a pod"); err != nil {
		return extension.Tunnel{}, err
	}
	tunnel, err := pods.OpenPod(noted, name, namespace, pod, port)
	if err != nil {
		return extension.Tunnel{}, err
	}
	opened := extension.Tunnel{Addr: tunnel.Addr(), Pod: pod, PodKnown: true, Stop: tunnel.Close}
	return endsWithRequest(ctx, opened, "pod tunnel: closing it as its request ends"), nil
}
