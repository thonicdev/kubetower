package storage

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// The qualified snapshot kinds `Host.Definitions` resolves. They are the CSI
// external-snapshotter's CRDs, which a cluster has only when somebody
// installed them.
const (
	SnapshotsKind = "volumesnapshots.snapshot.storage.k8s.io"
	ContentsKind  = "volumesnapshotcontents.snapshot.storage.k8s.io"
	ClassesKind   = "volumesnapshotclasses.snapshot.storage.k8s.io"
)

// maxHistory is how many of a claim's snapshots its tab lists.
const maxHistory = 10

// SnapshotClass is one VolumeSnapshotClass.
type SnapshotClass struct {
	Name           string `json:"name"`
	Driver         string `json:"driver"`
	DeletionPolicy string `json:"deletionPolicy"`
	Default        bool   `json:"default"`
}

// SnapshotSection is the snapshot kinds. Its State is StateAbsent when the
// cluster does not serve VolumeSnapshots, StateRefused or StateFailed when it
// does and they could not be read: three answers, drawn three ways.
type SnapshotSection struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Total and Ready count the VolumeSnapshots read.
	Total int `json:"total"`
	Ready int `json:"ready"`
	// Contents counts the VolumeSnapshotContents, a cluster-scoped kind: -1
	// in a namespace, and when it could not be read.
	Contents int             `json:"contents"`
	Classes  []SnapshotClass `json:"classes"`
}

// snapshots reads the snapshot kinds and puts each claim's last snapshot on
// its row. history is every claim's snapshots, newest first, for its tab.
func snapshots(r *http.Request, h extension.Host, cluster, ns string, claims []Claim, incomplete func(string, string)) (SnapshotSection, map[string][]Snapshot) {
	s := SnapshotSection{State: StateRead, Contents: -1, Classes: []SnapshotClass{}}
	history := map[string][]Snapshot{}
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		sec := sectionOf(err)
		s.State, s.Message, s.Detail = sec.State, sec.Message, sec.Detail
		return s, history
	}
	lookup := func(kind string) (extension.CRD, error) { return registry.Lookup(r.Context(), kind) }

	crd, err := lookup(SnapshotsKind)
	switch {
	case absent(err):
		s.State, s.Message = StateAbsent, "Not installed"
		return s, history
	case err != nil:
		sec := sectionOf(err)
		s.State, s.Message, s.Detail = sec.State, sec.Message, sec.Detail
		return s, history
	}
	scope := ns
	if !crd.Namespaced {
		scope = ""
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: scope, Limit: listLimit})
	if err != nil {
		sec := sectionOf(err)
		s.State, s.Message, s.Detail = sec.State, sec.Message, sec.Detail
		return s, history
	}
	if !page.Complete {
		incomplete("Snapshots", page.Note)
	}
	for i := range page.Items {
		claim, snap := readSnapshot(&page.Items[i])
		s.Total++
		if snap.Ready {
			s.Ready++
		}
		if claim == "" {
			continue
		}
		key := page.Items[i].GetNamespace() + "/" + claim
		history[key] = append(history[key], snap)
	}
	for key := range history {
		sort.SliceStable(history[key], func(i, j int) bool { return history[key][i].At > history[key][j].At })
	}
	for i := range claims {
		key := claims[i].Namespace + "/" + claims[i].Name
		if list := history[key]; len(list) > 0 {
			last := list[0]
			claims[i].Snapshot = &last
			claims[i].Snapshots = len(list)
		}
	}

	if crd, err := lookup(ClassesKind); err == nil {
		if page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: listLimit}); err != nil {
			incomplete("Snapshot classes", sectionOf(err).Message)
		} else {
			for i := range page.Items {
				s.Classes = append(s.Classes, readSnapshotClass(&page.Items[i]))
			}
			sort.SliceStable(s.Classes, func(i, j int) bool { return s.Classes[i].Name < s.Classes[j].Name })
		}
	}
	// Contents are cluster-scoped and name the snapshot they hold: counted
	// for the whole cluster only.
	if ns == "" {
		if crd, err := lookup(ContentsKind); err == nil {
			if page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: listLimit, Columns: true}); err != nil {
				incomplete("Snapshot contents", sectionOf(err).Message)
			} else {
				s.Contents = len(page.Rows)
			}
		}
	}
	return s, history
}

// absent is a kind the cluster does not serve: not installed, an answer about
// the cluster rather than a failure.
func absent(err error) bool {
	return errors.Is(err, extension.ErrUnknownKind) || errors.Is(err, extension.ErrNotServed)
}

// readSnapshot is one VolumeSnapshot and the claim it was taken of, empty for
// one made from an existing content.
func readSnapshot(o *unstructured.Unstructured) (string, Snapshot) {
	claim, _, _ := unstructured.NestedString(o.Object, "spec", "source", "persistentVolumeClaimName")
	s := Snapshot{Name: o.GetName()}
	s.Ready, _, _ = unstructured.NestedBool(o.Object, "status", "readyToUse")
	s.Class, _, _ = unstructured.NestedString(o.Object, "spec", "volumeSnapshotClassName")
	at, _, _ := unstructured.NestedString(o.Object, "status", "creationTime")
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		s.At = stamp(t)
	} else {
		s.At = stamp(o.GetCreationTimestamp().Time)
	}
	if size, _, _ := unstructured.NestedString(o.Object, "status", "restoreSize"); size != "" {
		s.Size = parseBytes(size)
	}
	return claim, s
}

func readSnapshotClass(o *unstructured.Unstructured) SnapshotClass {
	c := SnapshotClass{Name: o.GetName()}
	c.Driver, _, _ = unstructured.NestedString(o.Object, "driver")
	c.DeletionPolicy, _, _ = unstructured.NestedString(o.Object, "deletionPolicy")
	c.Default = o.GetAnnotations()["snapshot.storage.kubernetes.io/is-default-class"] == "true"
	return c
}
