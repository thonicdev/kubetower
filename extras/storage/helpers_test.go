package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// The clock every test stands at.
var testNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// snapshotCRDs is the catalog a cluster with the CSI snapshotter serves.
var snapshotCRDs = map[string]extension.CRD{
	SnapshotsKind: {Group: "snapshot.storage.k8s.io", Version: "v1", Plural: "volumesnapshots", Kind: "VolumeSnapshot", Namespaced: true},
	ContentsKind:  {Group: "snapshot.storage.k8s.io", Version: "v1", Plural: "volumesnapshotcontents", Kind: "VolumeSnapshotContent"},
	ClassesKind:   {Group: "snapshot.storage.k8s.io", Version: "v1", Plural: "volumesnapshotclasses", Kind: "VolumeSnapshotClass"},
}

type fakeRegistry struct{ served map[string]extension.CRD }

func (f fakeRegistry) All(context.Context) ([]extension.CRD, error) {
	var out []extension.CRD
	for _, c := range f.served {
		out = append(out, c)
	}
	return out, nil
}

func (f fakeRegistry) Lookup(_ context.Context, kind string) (extension.CRD, error) {
	c, ok := f.served[kind]
	if !ok {
		return extension.CRD{}, extension.ErrUnknownKind
	}
	return c, nil
}

// fakeHost answers List from the objects it holds, by resource, filtered by
// namespace as the console's does, and Usage from the figures it is given.
type fakeHost struct {
	mu       sync.Mutex
	registry fakeRegistry
	objects  map[string][]unstructured.Unstructured
	listErr  map[string]error
	// volumes is each node's kubelet figures, by PVCRef.
	volumes  map[string]map[string]extension.FS
	usageErr error
	defErr   error
	// silent nodes answer nothing, as a kubelet the proxy cannot reach.
	silent map[string]error
	asked  [][]string
	listed []extension.ListQuery
	// fill is the metrics backend's answer to VolumeFill (#91); fillErr its
	// refusal; filled the claims it was asked.
	fill    extension.VolumeFill
	fillErr error
	filled  [][]extension.ClaimRef
}

func newHost() *fakeHost {
	return &fakeHost{registry: fakeRegistry{served: snapshotCRDs}, objects: map[string][]unstructured.Unstructured{},
		listErr: map[string]error{}, volumes: map[string]map[string]extension.FS{}, silent: map[string]error{},
		fill: extension.VolumeFill{State: extension.WindowNoPrometheus, Detail: "no Prometheus"}}
}

func (h *fakeHost) add(resource string, objs ...unstructured.Unstructured) {
	h.objects[resource] = append(h.objects[resource], objs...)
}

// measured gives node's kubelet a figure for the claim ns/name.
func (h *fakeHost) measured(node, ref string, used, capacity int64) {
	if h.volumes[node] == nil {
		h.volumes[node] = map[string]extension.FS{}
	}
	h.volumes[node][ref] = extension.FS{UsedBytes: used, CapacityBytes: capacity}
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errors.New("this extra reads through List only")
}

func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	if h.defErr != nil {
		return nil, h.defErr
	}
	return h.registry, nil
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	h.listed = append(h.listed, q)
	h.mu.Unlock()
	if q.GVR.Group == "" && q.GVR.Resource == "secrets" {
		return extension.Page{}, extension.ErrSecretsNotListed
	}
	if err := h.listErr[q.GVR.Resource]; err != nil {
		return extension.Page{}, err
	}
	var out []unstructured.Unstructured
	for _, o := range h.objects[q.GVR.Resource] {
		if q.Namespace != "" && o.GetNamespace() != "" && o.GetNamespace() != q.Namespace {
			continue
		}
		out = append(out, *o.DeepCopy())
	}
	page := extension.Page{Items: out, Complete: true}
	if q.Limit > 0 && int64(len(out)) > q.Limit {
		page = extension.Page{Items: out[:q.Limit], Note: "cut at the limit"}
	}
	if q.Each != nil {
		for _, o := range page.Items {
			if err := q.Each(o); err != nil {
				return extension.Page{}, err
			}
		}
		page.Items = nil
	}
	if q.Columns {
		for _, o := range page.Items {
			page.Rows = append(page.Rows, extension.Row{Namespace: o.GetNamespace(), Name: o.GetName()})
		}
		page.Items = nil
	}
	return page, nil
}

func (h *fakeHost) Usage(_ *http.Request, _ string, nodes []string) (extension.Usage, error) {
	h.mu.Lock()
	h.asked = append(h.asked, append([]string{}, nodes...))
	h.mu.Unlock()
	if h.usageErr != nil {
		return extension.Usage{}, h.usageErr
	}
	out := extension.Usage{Nodes: map[string]extension.NodeUsage{}, Asked: len(nodes), Complete: true}
	for _, node := range nodes {
		if err := h.silent[node]; err != nil {
			if out.FirstErr == nil {
				out.FirstErr = err
			}
			continue
		}
		out.Answered++
		var pod extension.PodUsage
		for ref, fs := range h.volumes[node] {
			pod.Volumes = append(pod.Volumes, extension.VolumeUsage{Name: "v", FS: fs, PVCRef: ref})
		}
		out.Nodes[node] = extension.NodeUsage{Pods: []extension.PodUsage{pod}}
	}
	return out, nil
}

func (h *fakeHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	return "", errors.New("this extra reveals nothing")
}
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", extension.ErrNotConfigured
}
func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, extension.ErrNotConfigured
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("this extra runs nothing")
}
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error { return errors.New("not used") }
func (h *fakeHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	return extension.LiveUsage{}, errors.New("not used")
}
func (h *fakeHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	return extension.Window{}, errors.New("not used")
}
func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, errors.New("not used")
}
func (h *fakeHost) VolumeFill(_ *http.Request, _ string, claims []extension.ClaimRef) (extension.VolumeFill, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.filled = append(h.filled, claims)
	return h.fill, h.fillErr
}
func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("this extra opens no tunnel")
}
func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("this extra opens no tunnel")
}
func (h *fakeHost) Setting(*http.Request, string, string) string { return "" }
func (h *fakeHost) ConfirmToken(*http.Request) string            { return "" }
func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("this extra writes nothing")
}

func obj(t testing.TB, doc map[string]any) unstructured.Unstructured {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var u unstructured.Unstructured
	if err := u.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	return u
}

func created(days int) string {
	return testNow.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
}

// claim is a PersistentVolumeClaim of size bound to volume (unbound when
// volume is empty).
func claim(t testing.TB, ns, name, class, size, volume string, labels map[string]any) unstructured.Unstructured {
	meta := map[string]any{"namespace": ns, "name": name, "creationTimestamp": created(30)}
	if labels != nil {
		meta["labels"] = labels
	}
	status := map[string]any{"phase": "Pending"}
	if volume != "" {
		status = map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": size}, "accessModes": []any{"ReadWriteOnce"}}
	}
	return obj(t, map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta,
		"spec": map[string]any{"storageClassName": class, "volumeName": volume, "accessModes": []any{"ReadWriteOnce"},
			"resources": map[string]any{"requests": map[string]any{"storage": size}}},
		"status": status,
	})
}

// volume is a PersistentVolume in phase, bound to claimRef ns/name (none when
// empty).
func volume(t testing.TB, name, phase, reclaim, class, size, claimRef string, age int) unstructured.Unstructured {
	spec := map[string]any{"capacity": map[string]any{"storage": size}, "accessModes": []any{"ReadWriteOnce"},
		"persistentVolumeReclaimPolicy": reclaim, "storageClassName": class,
		"csi": map[string]any{"driver": "ebs.csi.aws.com", "volumeHandle": "vol-" + name}}
	if ns, n, ok := strings.Cut(claimRef, "/"); ok {
		spec["claimRef"] = map[string]any{"kind": "PersistentVolumeClaim", "namespace": ns, "name": n}
	}
	return obj(t, map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolume",
		"metadata": map[string]any{"name": name, "creationTimestamp": created(age)},
		"spec":     spec, "status": map[string]any{"phase": phase},
	})
}

func class(t testing.TB, name, reclaim string, isDefault bool) unstructured.Unstructured {
	meta := map[string]any{"name": name, "creationTimestamp": created(200)}
	if isDefault {
		meta["annotations"] = map[string]any{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return obj(t, map[string]any{
		"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": meta,
		"provisioner": "ebs.csi.aws.com", "reclaimPolicy": reclaim, "volumeBindingMode": "WaitForFirstConsumer",
		"allowVolumeExpansion": true,
	})
}

// pod is a pod on node, Running, mounting the claims, owned by a StatefulSet
// when set is not empty.
func pod(t testing.TB, ns, name, node, set string, claims ...string) unstructured.Unstructured {
	var volumes []any
	for i, c := range claims {
		volumes = append(volumes, map[string]any{"name": "v" + string(rune('a'+i)),
			"persistentVolumeClaim": map[string]any{"claimName": c}})
	}
	meta := map[string]any{"namespace": ns, "name": name}
	if set != "" {
		meta["ownerReferences"] = []any{map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": set,
			"uid": "u-" + set, "controller": true}}
	}
	return obj(t, map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": meta,
		"spec":   map[string]any{"nodeName": node, "containers": []any{map[string]any{"name": "app", "image": "app:1"}}, "volumes": volumes},
		"status": map[string]any{"phase": "Running"},
	})
}

func statefulSet(t testing.TB, ns, name string, templates ...string) unstructured.Unstructured {
	var vct []any
	for _, tpl := range templates {
		vct = append(vct, map[string]any{"metadata": map[string]any{"name": tpl}})
	}
	return obj(t, map[string]any{
		"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": map[string]any{"namespace": ns, "name": name},
		"spec": map[string]any{"volumeClaimTemplates": vct},
	})
}

func snapshot(t testing.TB, ns, name, claim string, at time.Time, ready bool) unstructured.Unstructured {
	return obj(t, map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot",
		"metadata": map[string]any{"namespace": ns, "name": name, "creationTimestamp": at.Format(time.RFC3339)},
		"spec": map[string]any{"volumeSnapshotClassName": "csi-snap",
			"source": map[string]any{"persistentVolumeClaimName": claim}},
		"status": map[string]any{"readyToUse": ready, "creationTime": at.Format(time.RFC3339), "restoreSize": "20Gi"},
	})
}

func snapshotClass(t testing.TB, name string) unstructured.Unstructured {
	return obj(t, map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshotClass",
		"metadata": map[string]any{"name": name}, "driver": "ebs.csi.aws.com", "deletionPolicy": "Delete",
	})
}

// serve sends target through the extra's routes, mounted as the console
// mounts them.
func serve(t testing.TB, h extension.Host, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range New().Routes(h) {
		mux.Handle(route.Method+" /x/{cluster}/"+Key+route.Pattern, route.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decode[T any](t testing.TB, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
	}
	var out T
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// at stands the package clock at testNow for one test.
func at(t testing.TB) {
	t.Helper()
	was := now
	now = func() time.Time { return testNow }
	t.Cleanup(func() { now = was })
}

const gi = int64(1) << 30
