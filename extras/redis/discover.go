package redis

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/svcgroup"
)

// The port every Redis and Valkey listens on unless somebody moved it.
const defaultPort = 6379

var (
	servicesGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	slicesGVR   = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	podsGVR     = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// looksLikeRedis is the guess, and the sentence explaining it.
//
// A guess, said on every row: no label is common to every chart, so this
// matches a port named `redis`, `valkey` or their `-tcp` spellings, or the
// number 6379. A Service it takes nothing of may still say what it fronts, by
// its labels or its pods' image (evidence.go). Which of the two engines it is comes from the server when
// dialled.
//
// The name is tried before the number, as the stronger signal. A port named
// `metrics` (an exporter) or for a Sentinel (no keys) is not offered, whatever
// its number.
func looksLikeRedis(port corev1.ServicePort) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(port.Name))
	switch {
	case name == "redis", name == "valkey", name == "redis-tcp", name == "tcp-redis",
		name == "valkey-tcp", name == "tcp-valkey":
		return "the port is named " + port.Name, true
	case name == "metrics", name == "http-metrics", name == "exporter", strings.Contains(name, "sentinel"):
		// Named as an exporter or a Sentinel, whatever the number says.
		return "", false
	case port.Port == defaultPort:
		return "it is on 6379", true
	}
	return "", false
}

// looksLikeSentinel is the Sentinel ports looksLikeRedis refuses, so the
// inventory can draw a Sentinel beside the deployment it watches. The instance
// routes look only among the Redis ports, so a Sentinel is never dialled.
func looksLikeSentinel(port corev1.ServicePort) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(port.Name))
	switch {
	case strings.Contains(name, "sentinel"):
		return "the port is named " + port.Name, true
	case name == "metrics", name == "http-metrics", name == "exporter":
		return "", false
	case port.Port == 26379:
		return "it is on 26379", true
	}
	return "", false
}

// guessedServices is the Services with a port the port guess takes, as a
// Redis or a Sentinel: the evidence leaves them to it.
func guessedServices(services []corev1.Service) map[string]bool {
	out := map[string]bool{}
	for i := range services {
		for _, port := range services[i].Spec.Ports {
			_, redis := looksLikeRedis(port)
			_, sentinel := looksLikeSentinel(port)
			if redis || sentinel {
				out[services[i].Namespace+"/"+services[i].Name] = true
			}
		}
	}
	return out
}

// discovery is what one inventory read found.
type discovery struct {
	Instances []svcgroup.Instance
	// Sentinels are the Sentinel ports, for the inventory's grouping only.
	Sentinels []svcgroup.Instance
	// Services are every Service read, by "namespace/name", for the labels
	// and selectors the grouping reads.
	Services map[string]*corev1.Service
	// Complete is false when the Services listing stopped before its end - a
	// narrowed answer - and Note says why.
	Complete bool
	Note     string
	// SlicesNote says why what is behind each Service could not be read, when
	// the EndpointSlice listing failed. Empty when it answered.
	SlicesNote string
	// External are the ExternalName Services to a Redis outside the cluster:
	// listed, never dialled (external.go).
	External []externalDoor
	// ScopePods are the pods of a one-namespace read, when they were listed to
	// stand in for the EndpointSlices (podsBySelector); nil otherwise.
	ScopePods map[string]*corev1.Pod
}

// discover lists the Services that look like a Redis or a Valkey, for the
// person behind r. namespace scopes it; empty means the whole cluster.
//
// The Services listing failing is an error, and the caller draws it as one: a
// cluster that runs no Redis is an empty answer, not a failure. The
// EndpointSlices failing is not: the list stands, every Service kept to itself,
// and SlicesNote says what could not be read.
func discover(r *http.Request, h extension.Host, cluster, namespace string) (discovery, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR, Namespace: namespace})
	if err != nil {
		return discovery{}, err
	}
	services := make([]corev1.Service, 0, len(page.Items))
	for i := range page.Items {
		var service corev1.Service
		if err := fromUnstructured(&page.Items[i], &service); err != nil {
			return discovery{}, fmt.Errorf("a Service the cluster returned could not be read: %w", err)
		}
		services = append(services, service)
	}
	out := discovery{Complete: page.Complete, Note: page.Note, External: externals(services)}

	var slices []discoveryv1.EndpointSlice
	slicesRead := false
	perNamespace := false
	if sp, err := h.List(r, cluster, extension.ListQuery{GVR: slicesGVR, Namespace: namespace}); err != nil {
		out.SlicesNote = "what is behind each Service could not be read: " + shortError(err)
	} else {
		slicesRead = true
		for i := range sp.Items {
			var slice discoveryv1.EndpointSlice
			if err := fromUnstructured(&sp.Items[i], &slice); err != nil {
				slicesRead = false
				slices = nil
				out.SlicesNote = "what is behind each Service could not be read: " + shortError(err)
				break
			}
			slices = append(slices, slice)
		}
		if slicesRead && !sp.Complete {
			// A partial slice listing cannot say "nothing is behind" a Service:
			// not established. On a cluster-wide read it can still be
			// established namespace by namespace, below.
			slicesRead = false
			slices = nil
			out.SlicesNote = "what is behind each Service was only partly read: " + sp.Note
			perNamespace = namespace == ""
		}
	}
	// What the port guess leaves, a Service may still say it is a Redis
	// (evidence.go). Its pods are read only for one namespace.
	taken := guessedServices(services)
	var pods []*corev1.Pod
	if namespace != "" && len(taken) < len(services) {
		pods = podsIn(r, h, cluster, namespace)
	}
	chosen := evidence(services, taken, pods)
	if perNamespace {
		out.Instances = discoverPerNamespace(r, h, cluster, services, looksLikeRedis, chosen)
		out.Sentinels = discoverPerNamespace(r, h, cluster, services, looksLikeSentinel, nil)
	} else {
		out.Instances = withEvidence(svcgroup.Discover(services, slices, slicesRead, looksLikeRedis), services, slices, slicesRead, chosen)
		out.Sentinels = svcgroup.Discover(services, slices, slicesRead, looksLikeSentinel)
	}
	out.Services = make(map[string]*corev1.Service, len(services))
	for i := range services {
		out.Services[services[i].Namespace+"/"+services[i].Name] = &services[i]
	}
	if !slicesRead && !perNamespace && namespace != "" && len(out.Instances)+len(out.Sentinels) > 0 {
		out.ScopePods = podsBySelector(r, h, cluster, namespace, out.Services, out.Instances, out.Sentinels)
	}
	return out, nil
}

// discoverPerNamespace is svcgroup.Discover for a cluster-wide Services
// listing whose EndpointSlice listing came back partial (the person may list
// slices in some namespaces only). It lists the slices of each namespace that
// holds a Service, and attaches pods only in a namespace whose own listing
// was whole; any other namespace keeps its pods unknown. Each namespace's
// instances are sorted by Discover, and the namespaces are walked in order,
// so the result keeps Discover's namespace, service, port order.
func discoverPerNamespace(r *http.Request, h extension.Host, cluster string, services []corev1.Service, match svcgroup.Match, chosen map[string]chosenPort) []svcgroup.Instance {
	byNamespace := map[string][]corev1.Service{}
	var namespaces []string
	for i := range services {
		ns := services[i].Namespace
		if _, ok := byNamespace[ns]; !ok {
			namespaces = append(namespaces, ns)
		}
		byNamespace[ns] = append(byNamespace[ns], services[i])
	}
	sort.Strings(namespaces)
	var out []svcgroup.Instance
	for _, ns := range namespaces {
		slices, read := readSlicesIn(r, h, cluster, ns)
		out = append(out, withEvidence(svcgroup.Discover(byNamespace[ns], slices, read, match), byNamespace[ns], slices, read, chosen)...)
	}
	return out
}

// readSlicesIn lists one namespace's EndpointSlices; read is true only when
// the listing answered whole and decoded.
func readSlicesIn(r *http.Request, h extension.Host, cluster, namespace string) ([]discoveryv1.EndpointSlice, bool) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: slicesGVR, Namespace: namespace})
	if err != nil || !page.Complete {
		return nil, false
	}
	var slices []discoveryv1.EndpointSlice
	for i := range page.Items {
		var slice discoveryv1.EndpointSlice
		if err := fromUnstructured(&page.Items[i], &slice); err != nil {
			return nil, false
		}
		slices = append(slices, slice)
	}
	return slices, true
}

// podsRead is what readPods found: the pods by "namespace/name", and a
// sentence per namespace whose pods could not be read whole.
type podsRead struct {
	Pods  map[string]*corev1.Pod
	Notes []string
}

// readPods lists the pods of each namespace that holds something the
// inventory found, through Host.List, for the person: one listing per such
// namespace, since a cluster-wide pod listing is the largest read a console
// makes.
//
// A namespace whose pods could not be read whole is grouped without its
// workloads and pod labels, and Notes says so.
func readPods(r *http.Request, h extension.Host, cluster string, found discovery) podsRead {
	out := podsRead{Pods: map[string]*corev1.Pod{}}
	seen := map[string]bool{}
	var namespaces []string
	for _, list := range [][]svcgroup.Instance{found.Instances, found.Sentinels} {
		for _, instance := range list {
			if len(instance.Pods) > 0 && !seen[instance.Namespace] {
				seen[instance.Namespace] = true
				namespaces = append(namespaces, instance.Namespace)
			}
		}
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		if found.ScopePods != nil {
			// Already listed, whole, to stand in for the EndpointSlices.
			for k, v := range found.ScopePods {
				if v.Namespace == namespace {
					out.Pods[k] = v
				}
			}
			continue
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace})
		if err != nil {
			out.Notes = append(out.Notes, "the pods in "+namespace+" could not be read ("+shortError(err)+
				"), so its Redises are grouped without their workloads and pod labels")
			continue
		}
		if !page.Complete {
			out.Notes = append(out.Notes, "the pods in "+namespace+" were only partly read ("+page.Note+
				"), so its Redises are grouped without their workloads and pod labels")
			continue
		}
		pods := map[string]*corev1.Pod{}
		failed := false
		for i := range page.Items {
			var pod corev1.Pod
			if err := fromUnstructured(&page.Items[i], &pod); err != nil {
				failed = true
				break
			}
			pods[pod.Namespace+"/"+pod.Name] = &pod
		}
		if failed {
			out.Notes = append(out.Notes, "a pod in "+namespace+" could not be decoded, so its Redises are "+
				"grouped without their workloads and pod labels")
			continue
		}
		for k, v := range pods {
			out.Pods[k] = v
		}
	}
	return out
}

// readPod is one pod, by name, read for the person through Host.List. An
// absent pod is (nil, nil): the tunnel's pod went between the forward and now.
func readPod(r *http.Request, h extension.Host, cluster, namespace, name string) (*corev1.Pod, error) {
	page, err := h.List(r, cluster, extension.ListQuery{
		GVR: podsGVR, Namespace: namespace, FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return nil, err
	}
	for i := range page.Items {
		if page.Items[i].GetName() != name || page.Items[i].GetNamespace() != namespace {
			continue
		}
		var pod corev1.Pod
		if err := fromUnstructured(&page.Items[i], &pod); err != nil {
			return nil, fmt.Errorf("pod %s/%s could not be read: %w", namespace, name, err)
		}
		return &pod, nil
	}
	return nil, nil
}

func fromUnstructured(u *unstructured.Unstructured, into any) error {
	if u == nil {
		return errors.New("an empty object")
	}
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into)
}
