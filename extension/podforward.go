package extension

import "net/http"

// PodForwarder opens a tunnel to a port of one pod, where Forward reaches a
// Service: a diagnostics endpoint a container listens on - Go's pprof, the
// Node.js inspector - is the pod's, not a Service's. The Host the core hands an
// extension implements it; an extension asks with a type assertion, so an
// extension that needs none compiles unchanged and Host gains no method.
type PodForwarder interface {
	// ForwardPod opens a local address that reaches port on the pod
	// namespace/name in cluster, and hands back the address and the teardown.
	// Tunnel.Pod is that pod, PodKnown true.
	//
	//   - **Reviewed for the person behind r**, as `create pods/portforward`
	//     on that pod, and recorded: the audit names the person, the pod and
	//     the act. A refusal is classifiable with errors.Is: ErrDenied when
	//     the cluster says no, ErrUnreviewable when the console could not ask.
	//     An unknown cluster is ErrUnknownCluster, wrapped.
	//   - **Counted with Forward's tunnels**, against the same ceiling and the
	//     same per-person share.
	//   - **Closed when r ends, at the latest.** Stop gives the slot back
	//     sooner, and a page calls it when it is done.
	//   - **Whether something listens on port is not known when it opens**: a
	//     tunnel to a port nothing listens on opens, and its first connection
	//     fails. The extension reads that from its own first request.
	//
	// port is 1 to 65535; the namespace and the pod are Kubernetes names.
	ForwardPod(r *http.Request, cluster, namespace, pod string, port int) (Tunnel, error)
}
