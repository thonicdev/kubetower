package drift_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/thonicdev/kubetower/extras/argocd/drift"
)

// The applied manifest is guestbook-ui's own, as Argo CD wrote it on
// docker-desktop; the live object is that manifest after the API
// server defaulted it, then changed by hand.

const applied = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"annotations":{"argocd.argoproj.io/tracking-id":"guestbook:apps/Deployment:guestbook/guestbook-ui"},"name":"guestbook-ui","namespace":"guestbook","creationTimestamp":null},"spec":{"replicas":1,"revisionHistoryLimit":3,"selector":{"matchLabels":{"app":"guestbook-ui"}},"template":{"metadata":{"labels":{"app":"guestbook-ui"}},"spec":{"containers":[{"image":"gcr.io/google-samples/gb-frontend:v5","name":"guestbook-ui","ports":[{"containerPort":80}],"resources":{"limits":{"cpu":"1","memory":"128Mi"}},"emptyDirLike":{}}]}}}}`

func decoded(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// live is the applied manifest with the API server's defaults added, the
// values as an unstructured object holds them (int64, not float64).
func live(t *testing.T) map[string]any {
	t.Helper()
	obj := decoded(t, applied)
	meta := obj["metadata"].(map[string]any)
	meta["uid"] = "0b1c"
	meta["creationTimestamp"] = "2026-09-24T10:00:00Z"
	meta["annotations"].(map[string]any)[drift.LastApplied] = applied
	spec := obj["spec"].(map[string]any)
	spec["replicas"] = int64(1)
	spec["revisionHistoryLimit"] = int64(3)
	spec["progressDeadlineSeconds"] = int64(600)
	container := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["terminationMessagePath"] = "/dev/termination-log"
	container["ports"] = []any{map[string]any{"containerPort": int64(80), "protocol": "TCP"}}
	container["resources"] = map[string]any{"limits": map[string]any{"cpu": "1000m", "memory": "128Mi"}}
	delete(container, "emptyDirLike")
	obj["status"] = map[string]any{"replicas": int64(1)}
	return obj
}

func TestDefaultsAndRespellingsAreNotDrift(t *testing.T) {
	if got := drift.Compare(decoded(t, applied), live(t), drift.Options{}); len(got) != 0 {
		t.Errorf("got %+v, want nothing: defaults, int64, 1000m and an empty map are not drift", got)
	}
}

func TestAHandMadeScaleAndImageAreDrift(t *testing.T) {
	now := live(t)
	spec := now["spec"].(map[string]any)
	spec["replicas"] = int64(3)
	container := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["image"] = "gcr.io/google-samples/gb-frontend:v6"

	got := drift.Compare(decoded(t, applied), now, drift.Options{})
	want := []drift.Field{
		{Path: "spec.replicas", Applied: "1", Live: "3", Change: drift.Changed},
		{Path: "spec.template.spec.containers[name=guestbook-ui].image", Applied: "gcr.io/google-samples/gb-frontend:v5", Live: "gcr.io/google-samples/gb-frontend:v6", Change: drift.Changed},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestAContainerMatchedByNameNotByPosition(t *testing.T) {
	now := live(t)
	podSpec := now["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	// A sidecar injected in front: by position everything would differ.
	podSpec["containers"] = append([]any{map[string]any{"name": "istio-proxy", "image": "proxy"}}, podSpec["containers"].([]any)...)
	if got := drift.Compare(decoded(t, applied), now, drift.Options{}); len(got) != 0 {
		t.Errorf("got %+v, want nothing: an added container is somebody else's", got)
	}
}

func TestARemovedFieldIsMissing(t *testing.T) {
	now := live(t)
	delete(now["spec"].(map[string]any), "revisionHistoryLimit")
	got := drift.Compare(decoded(t, applied), now, drift.Options{})
	if len(got) != 1 || got[0].Path != "spec.revisionHistoryLimit" || got[0].Change != drift.Missing || got[0].Applied != "3" {
		t.Errorf("got %+v, want spec.revisionHistoryLimit missing", got)
	}
}

// A plain omitempty field applied at its zero value is left out of the stored
// object: kube-state-metrics' chart applies `hostNetwork: false`, and the live
// Deployment has no such key. A pointer field is not: its false is kept, and
// its absence live is drift.
func TestAZeroValueTheServerDroppedIsNotMissing(t *testing.T) {
	want := decoded(t, applied)
	podSpec := want["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	podSpec["hostNetwork"] = false
	podSpec["serviceAccountName"] = ""
	if got := drift.Compare(want, live(t), drift.Options{}); len(got) != 0 {
		t.Errorf("got %+v, want no drift for zero values the server omits", got)
	}
	// A pointer field keeps its false when stored: gone live, it defaults to
	// true, and that is drift - a security one here.
	delete(podSpec, "serviceAccountName")
	podSpec["automountServiceAccountToken"] = false
	got := drift.Compare(want, live(t), drift.Options{})
	if len(got) != 1 || got[0].Path != "spec.template.spec.automountServiceAccountToken" || got[0].Change != drift.Missing {
		t.Errorf("got %+v, want a pointer field's false gone live as missing", got)
	}
	delete(podSpec, "automountServiceAccountToken")
	// A map key spelled like one of those fields is a key, not the field.
	want["metadata"].(map[string]any)["labels"] = map[string]any{"tty": ""}
	got = drift.Compare(want, live(t), drift.Options{})
	if len(got) != 1 || got[0].Change != drift.Missing {
		t.Errorf("got %+v, want a label keyed tty gone live as missing", got)
	}
	delete(want["metadata"].(map[string]any), "labels")
	podSpec["hostNetwork"] = true
	got = drift.Compare(want, live(t), drift.Options{})
	if len(got) != 1 || got[0].Path != "spec.template.spec.hostNetwork" || got[0].Change != drift.Missing {
		t.Errorf("got %+v, want a non-zero value still missing", got)
	}
}

func TestALabelKeyIsQuotedAndAPlainListIsPositional(t *testing.T) {
	want := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app.kubernetes.io/name": "shop"}},
		"spec":     map[string]any{"args": []any{"--port=80"}},
	}
	now := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app.kubernetes.io/name": "shop2"}},
		"spec":     map[string]any{"args": []any{"--port=80", "--debug"}},
	}
	got := drift.Compare(want, now, drift.Options{})
	paths := []string{}
	for _, f := range got {
		paths = append(paths, f.Path+":"+string(f.Change))
	}
	expected := []string{`metadata.labels["app.kubernetes.io/name"]:changed`, "spec.args[1]:added"}
	if !reflect.DeepEqual(paths, expected) {
		t.Errorf("got %v, want %v", paths, expected)
	}
}

func TestASecretShowsPathsAndNeverValues(t *testing.T) {
	want := map[string]any{"kind": "Secret", "data": map[string]any{"password": "b2xk"}}
	now := map[string]any{"kind": "Secret", "data": map[string]any{"password": "bmV3"}}
	got := drift.Compare(want, now, drift.Options{Mask: true})
	if len(got) != 1 || got[0].Path != "data.password" || got[0].Applied != drift.Masked || got[0].Live != drift.Masked {
		t.Errorf("got %+v, want data.password changed with both values masked", got)
	}
}
