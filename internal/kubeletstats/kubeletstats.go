// Package kubeletstats is the one client for the kubelet's own statistics.
//
//	GET /api/v1/nodes/{node}/proxy/stats/summary
//
// That endpoint is the only source for a container's `ephemeral-storage` use
// (its writable layer and logs) and for how full the node's own filesystem is.
// Several readers share this one client so that one place knows that
// `nodes/proxy` is refused by several managed offerings, that a firewalled
// kubelet answers a bare 404, and that the capability must degrade, not fail.
//
// # It never invents a zero
//
// A node that could not be asked is absent from what comes back, never present
// with zeroes, and `Summary.NodeFSMeasured` keeps a kubelet that sent no
// `node.fs` block from reading as a node with an empty disk.
package kubeletstats

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/apiraw"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// Workers is how many kubelets are asked at once.
// Each is a connection the API server proxies, so a large cluster is not asked
// all at once.
const Workers = 5

// FS is a filesystem as the kubelet measured it.
type FS struct {
	UsedBytes int64
	// CapacityBytes is what `statfs()` said about the mount point, which is not
	// always the size of what was mounted: see Trustworthy.
	CapacityBytes int64
}

// Container is one container's disk, from its own kubelet.
type Container struct {
	Name string
	// EphemeralBytes is the writable layer plus the logs, together what the
	// `ephemeral-storage` limit is enforced against; either alone under-reports.
	EphemeralBytes int64
}

// Volume is one mounted volume, named as the pod spec names it.
type Volume struct {
	Name string
	FS   FS
	// PVCRef is the claim this volume is, `namespace/name`, empty when it is not
	// a claim. It comes from the kubelet's own `volume[].pvcRef` rather than a
	// join through the pod spec, where one wrong line would put one claim's
	// figures under another's row.
	PVCRef string
}

// Pod is one pod's entry in the summary.
type Pod struct {
	Name       string
	Namespace  string
	Containers []Container
	Volumes    []Volume
}

// Summary is one kubelet's answer, narrowed to what this console reads.
type Summary struct {
	// NodeFS is the node's root filesystem, where image layers, writable layers
	// and logs go, and the one the kubelet evicts pods to protect.
	NodeFS FS
	// NodeFSMeasured says the kubelet reported that block at all, which
	// `NodeFS == FS{}` cannot tell from an empty disk.
	NodeFSMeasured bool
	Pods           []Pod
}

// wireSummary is the summary API's shape, narrowed to what is read here. It is
// hand-written to avoid depending on `k8s.io/kubelet` for eight stable fields;
// unknown fields are ignored, which suits a kubelet newer than this build.
type wireSummary struct {
	Node struct {
		NodeName string   `json:"nodeName"`
		Fs       *fsStats `json:"fs"`
	} `json:"node"`
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"podRef"`
		Containers []struct {
			Name   string   `json:"name"`
			Rootfs *fsStats `json:"rootfs"`
			Logs   *fsStats `json:"logs"`
		} `json:"containers"`
		Volumes []struct {
			Name   string `json:"name"`
			PVCRef *struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"pvcRef"`
			fsStats
		} `json:"volume"`
	} `json:"pods"`
}

type fsStats struct {
	UsedBytes     *int64 `json:"usedBytes"`
	CapacityBytes *int64 `json:"capacityBytes"`
}

func (f *fsStats) used() int64 {
	if f == nil || f.UsedBytes == nil {
		return 0
	}
	return *f.UsedBytes
}

func (f *fsStats) capacity() int64 {
	if f == nil || f.CapacityBytes == nil {
		return 0
	}
	return *f.CapacityBytes
}

// maxSummary bounds one kubelet's stats/summary: a full node answers a few
// MiB, and a broken or hostile answer is not held whole.
var maxSummary int64 = 32 << 20

// errSummaryTooLarge is what a summary over maxSummary is refused with.
var errSummaryTooLarge = fmt.Errorf("the kubelet's summary is larger than %d MiB, which the console does not read", maxSummary>>20)

// Read asks one kubelet, through the API server's node proxy.
func Read(ctx context.Context, clients kubernetes.Interface, node string) (Summary, error) {
	// Through apiraw rather than DoRaw: for a 403 on nodes/proxy, the commonest
	// refusal here, DoRaw says "unknown" where the API server's Status explains.
	raw, err := apiraw.DoCapped(ctx, clients.CoreV1().RESTClient().Get().
		Resource("nodes").Name(node).SubResource("proxy").
		Suffix("stats", "summary"), maxSummary, errSummaryTooLarge)
	if err != nil {
		return Summary{}, fmt.Errorf("%s: %w", node, shortErr(err))
	}

	var wire wireSummary
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Summary{}, fmt.Errorf("%s: the kubelet's summary did not parse: %w", node, shortErr(err))
	}

	out := Summary{Pods: make([]Pod, 0, len(wire.Pods))}
	// A capacity of zero is not a filesystem, and would make a gauge divide by
	// nothing.
	if wire.Node.Fs != nil && wire.Node.Fs.capacity() > 0 {
		out.NodeFS = FS{UsedBytes: wire.Node.Fs.used(), CapacityBytes: wire.Node.Fs.capacity()}
		out.NodeFSMeasured = true
	}

	for _, p := range wire.Pods {
		pod := Pod{
			Name:       p.PodRef.Name,
			Namespace:  p.PodRef.Namespace,
			Containers: make([]Container, 0, len(p.Containers)),
			Volumes:    make([]Volume, 0, len(p.Volumes)),
		}
		for _, c := range p.Containers {
			pod.Containers = append(pod.Containers, Container{
				Name:           c.Name,
				EphemeralBytes: c.Rootfs.used() + c.Logs.used(),
			})
		}
		for _, v := range p.Volumes {
			volume := Volume{
				Name: v.Name,
				FS:   FS{UsedBytes: v.fsStats.used(), CapacityBytes: v.fsStats.capacity()},
			}
			// Both halves or neither: a reference with no namespace is not
			// understood, and `/name` would share a key with every such volume.
			if v.PVCRef != nil && v.PVCRef.Name != "" && v.PVCRef.Namespace != "" {
				volume.PVCRef = v.PVCRef.Namespace + "/" + v.PVCRef.Name
			}
			pod.Volumes = append(pod.Volumes, volume)
		}
		out.Pods = append(out.Pods, pod)
	}
	return out, nil
}

// Coverage is how much of what was asked actually answered.
// Both counts, so the caller's sentence can tell "3 of 40 answered" from
// "40 of 40".
type Coverage struct {
	Asked    int
	Answered int
	// FirstErr is the first failure; the rest usually repeat it.
	FirstErr error
}

// ReadNodes asks several kubelets, bounded at Workers at a time.
// A node that failed is absent from the map, so it cannot be confused with a
// node that answered.
func ReadNodes(ctx context.Context, clients kubernetes.Interface, nodes []string) (map[string]Summary, Coverage) {
	return ReadNodesWith(ctx, nodes, func(ctx context.Context, node string) (Summary, error) {
		return Read(ctx, clients, node)
	})
}

// ReadNodesWith is ReadNodes with the one-node read supplied, so a caching
// reader is bounded, counted and ordered as a fresh read is. read must answer
// like Read: a Summary, or an error naming the node.
func ReadNodesWith(ctx context.Context, nodes []string, read func(context.Context, string) (Summary, error)) (map[string]Summary, Coverage) {
	// Sorted, so a partial answer is reproducible.
	asked := append([]string(nil), nodes...)
	sort.Strings(asked)

	coverage := Coverage{Asked: len(asked)}
	if len(asked) == 0 {
		return nil, coverage
	}

	var (
		mu  sync.Mutex
		out = make(map[string]Summary, len(asked))
	)

	work := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < Workers && i < len(asked); i++ {
		wg.Add(1)
		// Through safego: a panic reading one node is that node's error, and
		// the worker goes on.
		safego.Go("kubelet stats: a worker", func() error {
			for node := range work {
				var summary Summary
				err := safego.Call("kubelet stats: the summary of a node", func() error {
					var err error
					summary, err = read(ctx, node)
					return err
				})
				mu.Lock()
				if err != nil {
					if coverage.FirstErr == nil {
						coverage.FirstErr = err
					}
					mu.Unlock()
					continue
				}
				coverage.Answered++
				out[node] = summary
				mu.Unlock()
			}
			return nil
		}, func(error) { wg.Done() })
	}
	for _, node := range asked {
		work <- node
	}
	close(work)
	wg.Wait()

	if coverage.Answered == 0 {
		return nil, coverage
	}
	return out, coverage
}

// ShortError is one line of a failure, capped, for a sentence on a page.
// A kubelet refusal arrives as a serialized `Status` of several hundred
// characters.
func ShortError(err error) string {
	if err == nil {
		return ""
	}
	return textutil.FirstLineN(err.Error(), 160)
}

// shortened is ShortError's line that still unwraps to what it shortened, so a
// caller can tell a refusal or a timeout apart with errors.Is and errors.As.
type shortened struct {
	text  string
	cause error
}

func (s shortened) Error() string { return s.text }

func (s shortened) Unwrap() error { return s.cause }

func shortErr(err error) error { return shortened{text: ShortError(err), cause: err} }
