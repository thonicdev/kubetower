package cluster

import "time"

// What one request to a cluster is allowed to cost. Per cluster and short:
// several unreachable contexts is a normal state, and a page must not wait on
// the slowest of them.
const (
	// requestTimeout bounds a single API call: long enough for a large list on
	// a busy cluster (a cold EKS list can take ~17 s), short enough that an
	// unreachable one does not hold a screen.
	requestTimeout = 30 * time.Second

	// dialTimeout bounds the TCP connect to a cluster, and nothing after it.
	//
	// Without it the connect waits for the kernel or client-go's dialer (30 s);
	// on Windows the kernel gives up after ~21 s with a `connectex` sentence no
	// classifier here reads as "unreachable", so a page fanning out several
	// reads waits that long for each. Ten seconds is the readiness probe's
	// figure, one number for "this cluster does not answer", and the refusal
	// then reads `dial tcp …: i/o timeout`, which every classifier knows.
	dialTimeout = 10 * time.Second

	// clientQPS and clientBurst lift client-go's defaults (5 and 10), sized for
	// a controller, not for a UI that fans out across resource types.
	//
	// The burst is what opening a cluster costs at once: the kinds column asks
	// one review and one one-row list per kind the menu offers (navoccupancy.go
	// in internal/web), two hundred requests on a cluster serving forty CRDs,
	// and the page beside it reads too. At a burst of 100 everything past the
	// hundredth waited at 50 a second, two seconds of a first opening spent in
	// this bucket and not on the network. 300 is the burst kubectl's discovery
	// client allows; the steady rate is unchanged.
	clientQPS   = 50
	clientBurst = 300
)
