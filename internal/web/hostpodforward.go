package web

import (
	"errors"
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/metrics"
)

var _ extension.PodForwarder = host{}

// ForwardPod is a tunnel to one port of one pod (extension.PodForwarder),
// dug by the forwarder for the person behind r: the forwarder digs it inside
// Gate.Perform, as `create pods/portforward` on the pod, so it is reviewed as
// them and recorded. Counted with Forward's tunnels (tunnelcap.go), and closed
// when r ends at the latest.
func (h host) ForwardPod(r *http.Request, name, namespace, pod string, port int) (extension.Tunnel, error) {
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
	if h.s == nil || h.s.tunnels == nil {
		return extension.Tunnel{}, errors.New("this build has no port forwarder, so nothing inside a cluster can be reached")
	}
	pods, ok := h.s.tunnels.(metrics.PodTunneller)
	if !ok {
		return extension.Tunnel{}, errNoPodTunnels
	}
	if h.s.clusters != nil {
		if _, ok := h.s.clusters.Resolve(name); !ok {
			return extension.Tunnel{}, fmt.Errorf("%w: %q is not a context in the kubeconfig", extension.ErrUnknownCluster, name)
		}
	}
	ctx := r.Context()
	tunnel, err := pods.OpenPod(ctx, name, namespace, pod, port)
	if err != nil {
		return extension.Tunnel{}, err
	}
	opened := extension.Tunnel{Addr: tunnel.Addr(), Pod: pod, PodKnown: true, Stop: tunnel.Close}
	return endsWithRequest(ctx, opened, "pod tunnel: closing it as its request ends"), nil
}
