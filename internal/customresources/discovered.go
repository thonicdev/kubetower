package customresources

// Which of the Kubernetes project's kinds this cluster serves.
//
// Discovery decides. The CRD list cannot (the API server serves some project
// kinds itself) and neither can a fixed list of names (a cluster without the
// Gateway API CRDs must show no Gateways). Discovery is also far cheaper than
// the CRD catalog and carries what the menu needs: plural, kind, scope and
// verbs. Printer columns come from the catalog when a list is opened.
//
// A failed read is not an absence: a discovery that failed is the caller's
// error, and a group that alone could not be read (an aggregated API whose
// backend is down) is named in Unread.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/safego"
)

// ProjectKinds is what one cluster serves of the Kubernetes project's kinds,
// beyond the console's own typed kinds.
type ProjectKinds struct {
	// Kinds are the served, listable, offered kinds, each with its Section,
	// in menu order: the native table's order, then by API group and title.
	Kinds []Definition
	// Unread names the project group versions discovery could not read, as
	// sorted `group/version`: their kinds are unknown, not absent. Groups the
	// console excludes anyway are not named.
	Unread []string
	// Unserved are the console's own typed kinds (kinds.go), by slug, that
	// this cluster does not serve at the group version the console reads them
	// at, such as `cronjobs` where only `batch/v1beta1` exists. A kind whose
	// group version could not be read is never here; Unread names it.
	Unserved []string
}

// projectSnapshot is one cluster's answer, held. Built whole and never mutated.
type projectSnapshot struct {
	kinds  ProjectKinds
	bySlug map[string]Definition
}

// ProjectKinds is the project's kinds this cluster serves. Held per cluster
// for the catalog's TTL and renewed in the background at the same age; a
// failure is never held, so the next caller gets the real error.
func (r *Registry) ProjectKinds(ctx context.Context, client *cluster.Client) (ProjectKinds, error) {
	snapshot, err := r.project(ctx, client)
	if err != nil {
		return ProjectKinds{}, err
	}
	return snapshot.kinds, nil
}

// projectKind resolves a slug against what discovery said, for a kind no CRD
// defines. Exact slugs only: the menu spells a project kind `plural.group`,
// and its bare plural is free for a CRD to claim (Longhorn serves its own
// `volumeattachments`).
func (r *Registry) projectKind(ctx context.Context, client *cluster.Client, kind string) (Definition, bool, error) {
	snapshot, err := r.project(ctx, client)
	if err != nil {
		return Definition{}, false, err
	}
	d, ok := snapshot.bySlug[strings.ToLower(strings.TrimSpace(kind))]
	return d, ok, nil
}

// projectFetch is one discovery in flight for one cluster, and its answer.
// Every waiter, the starter included, reads the answer here, so a failure
// reaches all of them once.
type projectFetch struct {
	done     chan struct{}
	snapshot *projectSnapshot
	err      error
	// superseded means it was read through a config that stopped being this
	// context's while on the network: neither stored nor returned.
	superseded bool
}

// ErrDiscoveryPending is the error of a caller whose own wait ended while the
// detached discovery was still running. It says nothing about what the cluster
// serves: the answer lands for the next caller.
var ErrDiscoveryPending = errors.New("discovery is still running")

// discoveryBound is how long one detached discovery may run: a large cluster
// serves hundreds of group versions.
const discoveryBound = 60 * time.Second

// project returns the held snapshot, or waits for one.
//
// The fetch runs in its own goroutine, detached from every request and
// bounded at discoveryBound, and every caller, the starter
// included, only waits on its own context. That lets the menu give up after
// its own short bound (projectNavWait) and draw without these kinds.
func (r *Registry) project(ctx context.Context, client *cluster.Client) (*projectSnapshot, error) {
	if client == nil || client.Config == nil {
		return nil, errors.New("no client for this cluster")
	}
	s := r.slot(client.Context)

	for {
		s.mu.Lock()
		if s.projectFor != client.Config {
			// A rebuilt kubeconfig: the held answer belongs to the cluster
			// the old config pointed at.
			s.projectHeld, s.projectAt, s.projectFor = nil, time.Time{}, client.Config
		}
		if s.projectHeld != nil && time.Since(s.projectAt) < r.ttl {
			if time.Since(s.projectAt) >= r.renewAfter {
				r.renewProject(s, client)
			}
			held := s.projectHeld
			s.mu.Unlock()
			return held, nil
		}
		fetch := s.projectFetching
		if fetch == nil {
			fetch = &projectFetch{done: make(chan struct{})}
			s.projectFetching = fetch
			config := s.projectFor
			safego.Go("custom resources: discovering what "+client.Context+" serves", func() error {
				r.fetchProject(s, client, config, fetch)
				return nil
			}, nil)
		}
		s.mu.Unlock()

		// Something a canceled request can walk away from.
		select {
		case <-ctx.Done():
			// We stopped waiting; that says nothing about what the cluster
			// serves. The fetch lands for the next caller.
			return nil, fmt.Errorf("what %q serves was still being discovered when the request stopped waiting: %w: %w",
				client.Context, ErrDiscoveryPending, ctx.Err())
		case <-fetch.done:
		}
		switch {
		case fetch.err != nil:
			return nil, fmt.Errorf("discovering what %q serves: %w", client.Context, fetch.err)
		case fetch.superseded:
			// Read through a config that is no longer this context's: ask again.
			continue
		}
		return fetch.snapshot, nil
	}
}

// fetchProject runs one discovery for the slot and lands its answer.
func (r *Registry) fetchProject(s *slot, client *cluster.Client, config *rest.Config, fetch *projectFetch) {
	ctx, cancel := context.WithTimeout(context.Background(), discoveryBound)
	// A panic in discovery becomes this fetch's error, so `done` is still
	// closed and its waiters released.
	var snapshot *projectSnapshot
	err := safego.Call("custom resources: discovery of "+client.Context, func() error {
		var err error
		snapshot, err = r.discoverProject(ctx, client)
		return err
	})
	cancel()

	s.mu.Lock()
	fetch.snapshot, fetch.err = snapshot, err
	fetch.superseded = err == nil && s.projectFor != config
	if err == nil && !fetch.superseded {
		s.projectHeld, s.projectAt = snapshot, time.Now()
	}
	s.projectFetching = nil
	s.mu.Unlock()
	// Released whatever happened; a failure is not held, so the next caller
	// starts its own fetch.
	close(fetch.done)
}

// renewProject refreshes a held snapshot in the background. The caller holds
// the slot's lock.
func (r *Registry) renewProject(s *slot, client *cluster.Client) {
	if s.projectRenewing {
		return
	}
	s.projectRenewing = true
	since, config := s.projectAt, s.projectFor
	safego.Go("custom resources: renewing what "+client.Context+" serves", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), discoveryBound)
		defer cancel()
		// A panic becomes this renewal's error: the flag is still cleared and
		// the held answer expires on schedule.
		var snapshot *projectSnapshot
		err := safego.Call("custom resources: renewal of "+client.Context, func() error {
			var err error
			snapshot, err = r.discoverProject(ctx, client)
			return err
		})

		s.mu.Lock()
		defer s.mu.Unlock()
		s.projectRenewing = false
		// A failed renewal changes nothing: the held answer expires on
		// schedule. One overtaken by a newer answer (a synchronous fetch, a
		// rebuilt config) is dropped, as in refreshInBackground.
		if err != nil || !s.projectAt.Equal(since) || s.projectFor != config {
			return nil
		}
		s.projectHeld, s.projectAt = snapshot, time.Now()
		return nil
	}, nil)
}

// discoverProject asks the cluster what it serves and keeps the project's part.
func (r *Registry) discoverProject(ctx context.Context, client *cluster.Client) (*projectSnapshot, error) {
	build := r.newDiscovery
	if build == nil {
		build = defaultDiscovery
	}
	d, err := build(client)
	if err != nil {
		return nil, err
	}
	groups, lists, err := discovery.ToDiscoveryInterfaceWithContext(d).ServerGroupsAndResourcesWithContext(ctx)
	failed, partial := discovery.GroupDiscoveryFailedErrorGroups(err)
	if err != nil && (!partial || groups == nil) {
		// No answer at all, not one failing group: the caller reports it
		// rather than drawing a menu without these kinds.
		return nil, err
	}
	return buildProject(groups, lists, failed), nil
}

func defaultDiscovery(client *cluster.Client) (discovery.DiscoveryInterface, error) {
	if client.Clients == nil {
		return nil, fmt.Errorf("no client for %q", client.Context)
	}
	return client.Clients.Discovery(), nil
}

// buildProject reduces discovery's answer to the project's offered kinds.
func buildProject(groups []*metav1.APIGroup, lists []*metav1.APIResourceList, failed map[schema.GroupVersion]error) *projectSnapshot {
	preferred := map[string]string{}
	for _, g := range groups {
		if g != nil {
			preferred[g.Name] = g.PreferredVersion.Version
		}
	}
	console := consoleKinds()

	// Every version serving each kind, with discovery's description of it.
	type served struct {
		resource metav1.APIResource
		versions []string
	}
	found := map[schema.GroupResource]*served{}
	// Every resource served, per version, to find which of the console's own
	// kinds are not served (unservedConsoleKinds).
	all := map[schema.GroupVersionResource]bool{}
	// Resources per group version list: an empty one where the console
	// expects kinds is unread, not empty (emptyConsoleGroups).
	listed := map[schema.GroupVersion]int{}
	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		listed[gv] += len(list.APIResources)
		for _, resource := range list.APIResources {
			all[gv.WithResource(resource.Name)] = true
		}
		if !ProjectGroup(gv.Group) {
			continue
		}
		for _, resource := range list.APIResources {
			key := schema.GroupResource{Group: gv.Group, Resource: resource.Name}
			if !offered(key, resource, console) {
				continue
			}
			if found[key] == nil {
				found[key] = &served{resource: resource}
			}
			found[key].versions = append(found[key].versions, gv.Version)
		}
	}

	snapshot := &projectSnapshot{bySlug: map[string]Definition{}}
	for key, s := range found {
		version := versionFor(preferred[key.Group], s.versions)
		d := Definition{
			Group:      key.Group,
			Version:    version,
			Kind:       s.resource.Kind,
			Plural:     key.Resource,
			Singular:   s.resource.SingularName,
			ShortNames: s.resource.ShortNames,
			Namespaced: s.resource.Namespaced,
			Served:     true,
			Section:    sectionOf(key.Group, key.Resource),
		}
		if native, _, ok := nativeAt(key.Group, key.Resource); ok {
			d.native = true
			d.Refresh = native.refresh
			if slices.Contains(native.versions, version) {
				d.Columns = native.columns
			}
		}
		snapshot.kinds.Kinds = append(snapshot.kinds.Kinds, d)
		snapshot.bySlug[strings.ToLower(d.Slug())] = d
	}
	sort.SliceStable(snapshot.kinds.Kinds, func(i, j int) bool {
		return projectOrder(snapshot.kinds.Kinds[i], snapshot.kinds.Kinds[j])
	})

	// What discovery could not read, plus empty answers where the console's
	// own kinds must be: unknown, not absent.
	unread := make(map[schema.GroupVersion]error, len(failed))
	for gv, err := range failed {
		unread[gv] = err
	}
	for _, gv := range emptyConsoleGroups(listed) {
		if _, known := unread[gv]; !known {
			unread[gv] = errors.New("discovery answered no resources")
		}
	}
	for gv := range unread {
		if !ProjectGroup(gv.Group) {
			continue
		}
		if _, out := excluded(gv.Group, ""); out {
			continue
		}
		snapshot.kinds.Unread = append(snapshot.kinds.Unread, gv.String())
	}
	sort.Strings(snapshot.kinds.Unread)
	snapshot.kinds.Unserved = unservedConsoleKinds(all, unread)
	return snapshot
}

// unservedConsoleKinds is the console's own kinds this cluster does not serve
// at the group version kinds.go reads them at, in kinds.go's order.
//
// Only what discovery stated: a kind whose group version is unread (failed,
// or empty per emptyConsoleGroups) stays in the menu and its group version is
// named in Unread. A wholly failed discovery is an error and hides nothing.
func unservedConsoleKinds(served map[schema.GroupVersionResource]bool, unread map[schema.GroupVersion]error) []string {
	var out []string
	for _, d := range resources.Kinds() {
		q, ok := d.ListQuestion()
		if !ok {
			continue
		}
		gvr := schema.GroupVersionResource{Group: q.Attrs.Group, Version: q.Attrs.Version, Resource: q.Attrs.Resource}
		if _, unknown := unread[gvr.GroupVersion()]; unknown {
			continue
		}
		if !served[gvr] {
			out = append(out, d.Kind)
		}
	}
	return out
}

// emptyConsoleGroups is the group versions the console reads its own kinds at
// that discovery answered with no resources, treated as unread.
//
// client-go does not fail on these: a proxy that 404s `/api` yields no core
// list and no error, and a 404 on `/api/v1` yields an empty one. Either would
// make every core kind read as not served. A real API server always serves
// core `v1`, so a missing or empty core list is unread; any other group
// version with an empty list is unread too, while a missing one is simply not
// served.
func emptyConsoleGroups(listed map[schema.GroupVersion]int) []schema.GroupVersion {
	seen := map[schema.GroupVersion]bool{}
	var out []schema.GroupVersion
	for _, d := range resources.Kinds() {
		q, ok := d.ListQuestion()
		if !ok {
			continue
		}
		gv := schema.GroupVersion{Group: q.Attrs.Group, Version: q.Attrs.Version}
		if seen[gv] {
			continue
		}
		seen[gv] = true
		count, answered := listed[gv]
		if (answered && count == 0) || (!answered && gv.Group == "") {
			out = append(out, gv)
		}
	}
	return out
}

// offered is whether a discovered project resource gets a menu entry.
func offered(key schema.GroupResource, resource metav1.APIResource, console map[schema.GroupResource]bool) bool {
	switch {
	case strings.Contains(key.Resource, "/"):
		// A subresource (`pods/log`) is not a kind.
		return false
	case !slices.Contains(resource.Verbs, "list"):
		// Create-only kinds (TokenReview, SubjectAccessReview, Binding): an
		// entry could only open onto a refusal.
		return false
	case console[key]:
		// One of the console's typed kinds, listed already.
		return false
	}
	_, out := excluded(key.Group, key.Resource)
	return !out
}

// versionFor is the version a project kind is read at: the one the drawer
// opens it at, so list and drawer agree. client-go's RESTMapper tries the
// group's preferred version first, then the rest highest first.
func versionFor(preferred string, versions []string) string {
	if slices.Contains(versions, preferred) {
		return preferred
	}
	best := ""
	for _, v := range versions {
		if best == "" || kubeversion.CompareKubeAwareVersionStrings(v, best) > 0 {
			best = v
		}
	}
	return best
}

// projectOrder is the menu's order within a section: the native table's
// order, then the kinds the CRDs serve by API group and title.
func projectOrder(a, b Definition) bool {
	_, ai, aNative := nativeAt(a.Group, a.Plural)
	_, bi, bNative := nativeAt(b.Group, b.Plural)
	switch {
	case aNative && bNative:
		return ai < bi
	case aNative != bNative:
		return aNative
	case a.Group != b.Group:
		return a.Group < b.Group
	default:
		return foldedLess(a.Title(), b.Title())
	}
}

// foldedLess orders two names as a reader does, ignoring case (byte order
// would put "IPAddressPools" before "Ingresses"); the names as written break a
// tie, so the order stays total.
func foldedLess(a, b string) bool {
	if al, bl := strings.ToLower(a), strings.ToLower(b); al != bl {
		return al < bl
	}
	return a < b
}

// consoleKinds is the console's typed kinds (kinds.go) by group and resource,
// which the menu must not offer twice.
func consoleKinds() map[schema.GroupResource]bool {
	out := map[schema.GroupResource]bool{}
	for _, d := range resources.Kinds() {
		if q, ok := d.ListQuestion(); ok {
			out[schema.GroupResource{Group: q.Attrs.Group, Resource: q.Attrs.Resource}] = true
		}
	}
	return out
}
