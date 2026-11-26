package cnpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// The clock every test stands at.
var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// registry is a custom-resource catalog: every kind in served resolves,
// anything else is ErrUnknownKind, unless errs names it.
type registry struct {
	served map[string]bool
	errs   map[string]error
}

func (g registry) All(context.Context) ([]extension.CRD, error) { return nil, errors.New("not used") }

func (g registry) Lookup(_ context.Context, kind string) (extension.CRD, error) {
	if err := g.errs[kind]; err != nil {
		return extension.CRD{}, err
	}
	if !g.served[kind] {
		return extension.CRD{}, fmt.Errorf("%s: %w", kind, extension.ErrUnknownKind)
	}
	plural, grp, _ := strings.Cut(kind, ".")
	return extension.CRD{Group: grp, Version: "v1", Plural: plural, Namespaced: true}, nil
}

// every kind the operator serves
func allServed() map[string]bool {
	return map[string]bool{ClusterKind: true, BackupKind: true, ScheduledKind: true, PoolerKind: true,
		DatabaseKind: true, PublicationKind: true, SubscriptionKind: true}
}

// fakeHost is extension.Host with what this extra uses made real: List
// answers from the objects it holds, keyed by resource, filtered by
// namespace, metadata.name and one `key=value` label, as the console's list
// does; Definitions answers the registry. Every List is recorded.
type fakeHost struct {
	reg      registry
	defErr   error
	objects  map[string][]unstructured.Unstructured
	listErr  map[string]error // by resource
	usage    extension.Usage
	usageErr error
	listed   []extension.ListQuery
	// secrets is what Reveal answers, by "namespace/name#key"; revealErr,
	// when set, is every Reveal's refusal. revealed records what was asked.
	secrets   map[string]string
	revealErr error
	revealed  []string
	// tunnel is what Forward answers; forwardErr its refusal; forwarded
	// records the Services asked.
	tunnel     *extension.Tunnel
	forwardErr error
	forwarded  []string
	// execFn answers Exec; nil refuses it, as before this extra ran any.
	// execs records every query asked.
	execFn func(q extension.ExecQuery) (extension.ExecResult, error)
	execs  []extension.ExecQuery
}

func newHost() *fakeHost {
	return &fakeHost{reg: registry{served: allServed()}, objects: map[string][]unstructured.Unstructured{}, listErr: map[string]error{}}
}

func (h *fakeHost) add(resource string, docs ...map[string]any) {
	for _, d := range docs {
		h.objects[resource] = append(h.objects[resource], unstructured.Unstructured{Object: d})
	}
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errors.New("this extra reads nothing through Client")
}
func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	if h.defErr != nil {
		return nil, h.defErr
	}
	return h.reg, nil
}
func (h *fakeHost) Usage(_ *http.Request, _ string, nodes []string) (extension.Usage, error) {
	return h.usage, h.usageErr
}
func (h *fakeHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	return extension.LiveUsage{}, errors.New("not used")
}
func (h *fakeHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	return extension.Window{}, errors.New("not used")
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

func (h *fakeHost) VolumeFill(*http.Request, string, []extension.ClaimRef) (extension.VolumeFill, error) {
	return extension.VolumeFill{State: extension.WindowNoPrometheus}, nil
}
func (h *fakeHost) Exec(_ *http.Request, _ string, q extension.ExecQuery) (extension.ExecResult, error) {
	h.execs = append(h.execs, q)
	if h.execFn == nil {
		return extension.ExecResult{}, errors.New("this test runs no command")
	}
	return h.execFn(q)
}
func (h *fakeHost) Reveal(_ *http.Request, _ string, namespace, name, key string) (string, error) {
	h.revealed = append(h.revealed, namespace+"/"+name+"#"+key)
	if h.revealErr != nil {
		return "", h.revealErr
	}
	if h.secrets == nil {
		return "", errors.New("this extra reveals nothing")
	}
	v, ok := h.secrets[namespace+"/"+name+"#"+key]
	if !ok {
		return "", extension.ErrNoSuchKey
	}
	return v, nil
}
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error { return errors.New("not used") }
func (h *fakeHost) Forward(_ context.Context, _ string, namespace, service string, port int) (extension.Tunnel, error) {
	h.forwarded = append(h.forwarded, fmt.Sprintf("%s/%s:%d", namespace, service, port))
	if h.forwardErr != nil {
		return extension.Tunnel{}, h.forwardErr
	}
	if h.tunnel == nil {
		return extension.Tunnel{}, errors.New("this extra opens no tunnel")
	}
	return *h.tunnel, nil
}
func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("this extra opens no tunnel")
}
func (h *fakeHost) Setting(*http.Request, string, string) string { return "" }
func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string { return "" }

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.listed = append(h.listed, q)
	if err := h.listErr[q.GVR.Resource]; err != nil {
		return extension.Page{}, err
	}
	out := []unstructured.Unstructured{}
	for _, o := range h.objects[q.GVR.Resource] {
		if q.Namespace != "" && o.GetNamespace() != q.Namespace {
			continue
		}
		if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok && o.GetName() != name {
			continue
		}
		if key, value, ok := strings.Cut(q.LabelSelector, "="); ok && o.GetLabels()[key] != value {
			continue
		}
		out = append(out, o)
	}
	if q.Limit > 0 && int64(len(out)) > q.Limit {
		return extension.Page{Items: out[:q.Limit], Note: "The list was cut at its limit."}, nil
	}
	return extension.Page{Items: out, Complete: true}, nil
}

func testExtra() CNPG { return CNPG{now: func() time.Time { return testNow }} }

// serve runs one of the extra's routes as the console mounts it.
func serve(t *testing.T, h extension.Host, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, rt := range testExtra().Routes(h) {
		mux.Handle(rt.Method+" /x/{cluster}/cnpg"+rt.Pattern, rt.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %d %s: %v", rec.Code, rec.Body, err)
	}
	return out
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// pgCluster is a Cluster as the operator writes it: healthy, with its
// instances and three certificates, and the spec's extra fields merged in.
func pgCluster(ns, name string, instances int, spec map[string]any, status map[string]any) map[string]any {
	names := []any{}
	healthy := []any{}
	reported := map[string]any{}
	for i := 1; i <= instances; i++ {
		n := fmt.Sprintf("%s-%d", name, i)
		names = append(names, n)
		healthy = append(healthy, n)
		reported[n] = map[string]any{"isPrimary": i == 1, "timeLineID": int64(1)}
	}
	sp := map[string]any{"instances": int64(instances), "storage": map[string]any{"size": "1Gi"}}
	for k, v := range spec {
		sp[k] = v
	}
	cert := testNow.Add(90 * 24 * time.Hour).Format("2006-01-02 15:04:05 -0700 MST")
	st := map[string]any{
		"phase": "Cluster in healthy state", "instances": int64(instances), "readyInstances": int64(instances),
		"currentPrimary": name + "-1", "targetPrimary": name + "-1", "timelineID": int64(1),
		"currentPrimaryTimestamp": rfc(testNow.Add(-48 * time.Hour)),
		"instanceNames":           names, "instancesStatus": map[string]any{"healthy": healthy},
		"instancesReportedState": reported,
		"certificates": map[string]any{"expirations": map[string]any{
			name + "-ca": cert, name + "-server": cert, name + "-replication": cert,
		}},
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "ClusterIsReady"},
			// What the operator writes on a cluster that archives nothing.
			map[string]any{"type": "ContinuousArchiving", "status": "True", "reason": "ContinuousArchivingSuccess"},
		},
		"writeService": name + "-rw", "readService": name + "-r",
		"topology": map[string]any{"nodesUsed": int64(1)},
		"image":    "ghcr.io/cloudnative-pg/postgresql:18.6-system-trixie",
	}
	for k, v := range status {
		st[k] = v
	}
	return map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"namespace": ns, "name": name, "creationTimestamp": rfc(testNow.Add(-30 * 24 * time.Hour))},
		"spec":     sp, "status": st,
	}
}

// barman is the spec of a cluster archiving to an object store.
func barman() map[string]any {
	return map[string]any{"backup": map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://bucket/x"}}}
}

func pgBackup(ns, name, owner, phase string, stopped time.Time) map[string]any {
	st := map[string]any{}
	if phase != "" {
		st = map[string]any{"phase": phase, "method": "barmanObjectStore", "startedAt": rfc(stopped.Add(-5 * time.Minute)), "stoppedAt": rfc(stopped)}
	}
	return map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Backup",
		"metadata": map[string]any{"namespace": ns, "name": name, "creationTimestamp": rfc(stopped.Add(-6 * time.Minute))},
		"spec":     map[string]any{"cluster": map[string]any{"name": owner}, "method": "barmanObjectStore"},
		"status":   st,
	}
}

func podDoc(ns, owner string, i int, role, node string) map[string]any {
	name := fmt.Sprintf("%s-%d", owner, i)
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"namespace": ns, "name": name, "labels": map[string]any{
			"cnpg.io/cluster": owner, "cnpg.io/instanceName": name, "cnpg.io/instanceRole": role, "cnpg.io/podRole": "instance",
		}},
		"spec": map[string]any{"nodeName": node},
		"status": map[string]any{"phase": "Running", "startTime": rfc(testNow.Add(-time.Hour)),
			"conditions":        []any{map[string]any{"type": "Ready", "status": "True"}},
			"containerStatuses": []any{map[string]any{"name": "postgres", "restartCount": int64(2)}}},
	}
}

// ConfiguredSecret and ConfiguredService are the console's reads of what its
// configuration names, which this extra never makes: it names nothing.
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", extension.ErrNotConfigured
}

func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, extension.ErrNotConfigured
}
