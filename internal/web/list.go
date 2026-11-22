package web

import (
	"context"
	"net/url"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The generic list: one handler for every kind, because the descriptors in
// `internal/resources` already say what a kind is.

// refreshSecondsOf is the refresh interval clamp, in one place because the JSON
// contract carries the same number (`ListPage.refreshSeconds`, `minimum: 1`).
func refreshSecondsOf(every time.Duration) int {
	if every <= 0 {
		every = resources.DefaultRefresh
	}
	seconds := int(every.Round(time.Second) / time.Second)
	switch {
	case seconds < 1:
		seconds = 1
	case seconds > 600:
		seconds = 600
	}
	return seconds
}

// RefusalUnknownKind is a request naming a kind this cluster does not serve: an
// uninstalled CRD, one served by no version, or a bare name two groups claim.
// Not RefusalUnknownCluster: the cluster answered, and a client must tell "your
// context is gone" from "your CRD is gone". It belongs with the other words in
// refusal.go.
const RefusalUnknownKind = "unknown-kind"

// listKind is the kind a list is about, reduced to what the list does with it:
// its name, how to fetch it, and where a row leads. A closure rather than a
// second handler, because built-in and custom resource kinds differ only in
// where the rows come from.
type listKind struct {
	// slug is the URL segment: "pods", or "certificates.cert-manager.io".
	slug  string
	title string
	// fetch returns the table already filtered and ordered. nil means the kind could
	// not be resolved; reason says why.
	fetch func(ctx context.Context, client *cluster.Client, ask listAsk) (resources.Table, error)
	// href builds a row's detail address, or nil for a kind with no detail page. The
	// name is a path segment, escaped here.
	href func(cluster, namespace, name string) string
	// refresh is how often this kind's rows are worth re-reading; zero means the
	// default.
	refresh time.Duration
	reason  string
}

// The kinds whose tables hold a figure that does not come from the object, and
// which of them can still show one with no metrics backend. Two sets, because a
// Nodes list on a cluster without metrics-server still has a kubelet to ask for
// its Disk column, as the storage kinds do for theirs. Declared here, beside the
// fetches, rather than in usagecache.go.
var (
	kindsWithLiveFigures = map[string]bool{
		"pods":                   true,
		"nodes":                  true,
		"persistentvolumeclaims": true,
		"persistentvolumes":      true,
	}
	kindsNeedingNoMetricsBackend = map[string]bool{
		"nodes":                  true,
		"persistentvolumeclaims": true,
		"persistentvolumes":      true,
	}
)

// hasLiveFigures reports whether this kind's table shows anything worth fetching
// off the row path.
func hasLiveFigures(kind string) bool { return kindsWithLiveFigures[kind] }

// needsNoMetricsBackend reports whether this kind is worth asking about even on
// a cluster with no metrics-server and no Prometheus.
func needsNoMetricsBackend(kind string) bool { return kindsNeedingNoMetricsBackend[kind] }

// liveUsage fetches what the rows are using, for the kinds that show it.
//
// CPU and memory failures come back as a nil map, a dash: no metrics backend, an
// expired session or a silent Prometheus is no reason to refuse to list pods,
// and the metrics diagnostic says which it was. Never a zero, which would state a
// measurement nobody took. The storage kinds carry their reason in each entry's
// `resources.Volume` and answer a nil error. The error goes no further than the
// usage cache, which hands it to the Overview's Capacity tile.
func (s *Server) liveUsage(ctx context.Context, cluster, kind, namespace string) (map[string]resources.Usage, error) {
	switch kind {
	case "pods":
		return s.computeUsage(ctx, cluster, namespace, func() (map[string]metrics.Usage, error) {
			return s.usage.PodUsage(ctx, cluster, namespace)
		})
	case "nodes":
		return s.nodeUsage(ctx, cluster)
	case "persistentvolumeclaims":
		// Keyed as resources.usageOf looks a row up: the name alone for one namespace,
		// `namespace/name` for all, or one claim's fullness lands under another's name.
		return claimUsage(s.volumeUsage(ctx, cluster, namespace), namespace), nil
	case "persistentvolumes":
		// A PersistentVolume is cluster-scoped and found through the claim's
		// `spec.volumeName`, so the whole cluster's claims are read.
		return volumeNameUsage(ctx, s, cluster), nil
	default:
		// No usage column.
		return nil, nil
	}
}

// claimUsage keys the fullness by claim, as the claims table looks it up.
func claimUsage(volumes map[string]resources.Volume, namespace string) map[string]resources.Usage {
	if len(volumes) == 0 {
		return nil
	}
	out := make(map[string]resources.Usage, len(volumes))
	for key, volume := range volumes {
		entry := key
		if namespace != "" {
			// One namespace was asked about, so the table keys by bare name.
			entry = key[len(namespace)+1:]
		}
		out[entry] = resources.Usage{
			// A claim has no CPU or memory: the flag keeps this entry from reading as a zero
			// of those (resources.Usage.ComputeUnmeasured).
			ComputeUnmeasured: true,
			Volume: &resources.Volume{
				Measured:      volume.Measured,
				UsedBytes:     volume.UsedBytes,
				CapacityBytes: volume.CapacityBytes,
				Note:          volume.Note,
			},
		}
	}
	return out
}

// volumeNameUsage is the same reading keyed by PersistentVolume name.
func volumeNameUsage(ctx context.Context, s *Server, cluster string) map[string]resources.Usage {
	if s.clusters == nil {
		return nil
	}
	client, err := s.clusters.Client(cluster)
	if err != nil || client == nil || client.Clients == nil {
		return nil
	}
	return volumeNameUsageFrom(ctx, client.Clients)
}

// volumeNameUsageFrom is volumeNameUsage against a bare client. It needs the
// claims for `spec.volumeName` (the kubelet names the claim it mounted and
// knows nothing of the PV behind it), and lists them once for both that and
// the reading: every claim in the cluster, paged, on every render of the page.
func volumeNameUsageFrom(ctx context.Context, clients kubernetes.Interface) map[string]resources.Usage {
	claims, err := listClaims(ctx, clients, "")
	if err != nil || len(claims) == 0 {
		return nil
	}
	volumes := volumeUsageOf(ctx, clients, "", claims)
	if len(volumes) == 0 {
		return nil
	}
	out := make(map[string]resources.Usage, len(volumes))
	for key, facts := range claims {
		if facts.volume == "" {
			// An unbound claim has no volume; the PV row says so itself.
			continue
		}
		volume, ok := volumes[key]
		if !ok {
			continue
		}
		out[facts.volume] = resources.Usage{
			ComputeUnmeasured: true,
			Volume: &resources.Volume{
				Measured:      volume.Measured,
				UsedBytes:     volume.UsedBytes,
				CapacityBytes: volume.CapacityBytes,
				Note:          volume.Note,
			},
		}
	}
	return out
}

// computeUsage is the CPU and memory half, from whichever metrics backend this
// cluster has. No backend at all is no figures and no error: nothing was asked.
func (s *Server) computeUsage(ctx context.Context, cluster, namespace string,
	ask func() (map[string]metrics.Usage, error)) (map[string]resources.Usage, error) {

	if s.usage == nil {
		return nil, nil
	}
	live, err := ask()
	if err != nil {
		return nil, err
	}

	// Keys are metrics.Key's, passed through because the table looks rows up by
	// them. `From` says which backend measured them.
	usage := make(map[string]resources.Usage, len(live))
	for key, u := range live {
		usage[key] = resources.Usage{CPUMillicores: u.CPUMillicores, MemoryBytes: u.MemoryBytes, From: u.From}
	}
	return usage, nil
}

// nodeUsage is the Nodes list's three gauges, merged from two independent
// sources: CPU and memory from metrics-server or Prometheus, disk from each
// kubelet through the node proxy. Either fails on its own, often exactly one, so
// `ComputeUnmeasured` keeps them apart: a node present only for its disk must not
// read "0m" of CPU. The error is the CPU and memory read's alone.
func (s *Server) nodeUsage(ctx context.Context, cluster string) (map[string]resources.Usage, error) {
	// Safe with a nil s.usage: computeUsage checks it before calling ask, so a
	// cluster with no metrics backend still gets its Disk column.
	compute, err := s.computeUsage(ctx, cluster, "", func() (map[string]metrics.Usage, error) {
		return s.usage.NodeUsage(ctx, cluster)
	})
	return mergeNodeUsage(compute, s.nodeDisk(ctx, cluster)), err
}

// mergeNodeUsage joins the two answers, a pure function so the rule can be
// tested without a cluster.
func mergeNodeUsage(compute map[string]resources.Usage, disks map[string]resources.Disk) map[string]resources.Usage {
	if len(compute) == 0 && len(disks) == 0 {
		// Neither answered: nil, which usageFor reads as "keep what you had", where an
		// empty map would be an answer.
		return nil
	}

	usage := compute
	if usage == nil {
		usage = make(map[string]resources.Usage, len(disks))
	}
	for node, disk := range disks {
		// A node's metrics key is its name (metrics.Key).
		entry, ok := usage[node]
		if !ok {
			// Without this the entry says 0m of CPU, the zero value nobody wrote.
			entry.ComputeUnmeasured = true
		}
		entry.Disk = &resources.Disk{
			UsedBytes:     disk.UsedBytes,
			CapacityBytes: disk.CapacityBytes,
		}
		usage[node] = entry
	}
	return usage
}

// listAsk is what one list read is asked, with named fields because several are
// strings a positional call could swap.
type listAsk struct {
	// namespace is the one namespace read, or "" for the whole cluster.
	namespace string
	// within is a selection of several namespaces when namespace is "" because the
	// whole cluster is read for it (resources.Matcher.Within).
	within []string
	// selector is a label selector the API server narrows the list to
	// (resources.Query.Selector).
	selector string
	// search is the search box and filters the filter control's choices
	// (resources.Matcher).
	search  string
	filters []resources.Filter
	order   listOrder
}

// listOrder is the sort as the URL carries it: a column title and a direction.
type listOrder struct {
	sort       string
	descending bool
}

// pagePath is a list's address with none of its state on it.
func pagePath(cluster, kind string) string {
	return clusterPath(cluster) + "/" + kind
}

func pageURL(cluster, kind, namespace, query string, order listOrder) string {
	path := pagePath(cluster, kind)
	values := url.Values{}
	encoded := encode(values, namespace, query, order)
	if encoded == "" {
		return path
	}
	return path + "?" + encoded
}

// encode adds the state every list URL carries and nothing for what is unset, so
// "no filter, no namespace, default order" is a bare `/c/x/pods`. The parameter
// names are the ones the list reads, `namespace` and `search` (the SPA's
// `useUrlList('namespace')` and `FILTER_PARAMETERS`, and `listResources`), so an
// overview link really filters the list it opens
// (TestOverviewLinksSpellTheListsParameters).
func encode(values url.Values, namespace, query string, order listOrder) string {
	if namespace != "" {
		values.Set("namespace", namespace)
	}
	if query != "" {
		values.Set("search", query)
	}
	if order.sort != "" {
		values.Set("sort", order.sort)
		if order.descending {
			values.Set("dir", "desc")
		}
	}
	return values.Encode()
}

// kindNav builds the left menu from the descriptors' own groups, in their order:
// `kinds.go` alone decides the headings. A descriptor with no Group is returned
// separately, for the top beside Overview. hidden is the kinds left out by slug:
// the in-cluster console's unlisted kinds, nil elsewhere.
func (s *Server) kindNav(cluster, current string, hidden map[string]bool) ([]navLink, []navGroup) {
	// Only kinds here. Pages that are not kinds go under "Extra" at the bottom,
	// added in one place (extraNav, Server.nav).
	var top []navLink
	var groups []navGroup
	index := map[string]int{}

	for _, d := range resources.Kinds() {
		if hidden[d.Kind] {
			continue
		}
		link := navLink{
			Label: d.Title,
			// Carrying what the person last left on this kind (views.go).
			Href: s.views.remembered(
				clusterPath(cluster)+"/"+d.Kind, cluster, d.Kind),
			Current:    d.Kind == current,
			Namespaced: d.Namespaced,
			Aliases:    resources.AliasesOf(d.Kind),
		}

		// A kind with no group goes at the top, under Overview: nodes, namespaces and
		// events are the cluster itself and opened far more often than their neighbors.
		if d.Group == "" {
			top = append(top, link)
			continue
		}

		at, seen := index[d.Group]
		if !seen {
			groups = append(groups, navGroup{Label: d.Group})
			at = len(groups) - 1
			index[d.Group] = at
		}
		groups[at].Links = append(groups[at].Links, link)
		if link.Current {
			groups[at].Open = true
		}
	}
	return top, groups
}

// clusterPath is `/c/{cluster}`, the prefix of every console address on a
// cluster. Every link to a cluster's pages goes through it, so a cluster has one
// spelling: `api.ClusterPath`, `url.PathEscape` of the real name, as
// `web/ui/src/clusters/clusterPath.ts` writes it too
// (TestClusterPathSpellsAClusterAsTheFrontEndDoes). React Router compares paths
// as strings, so `%40` against `@` would leave no menu row marked current.
// `Manager.Resolve` also accepts the `cluster.MakeDNSFriendly` form, which this
// never writes.
func clusterPath(name string) string {
	return api.ClusterPath(name)
}
