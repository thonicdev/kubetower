package storage

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

// The thresholds a claim's fill is graded at, in percent of the filesystem the
// kubelet measured.
const (
	WarnPercent = 80
	BadPercent  = 90
)

// The levels a measured fill is graded in.
const (
	LevelOK   = "ok"
	LevelWarn = "warn"
	LevelBad  = "bad"
)

// level grades a percentage: 90 and over is bad, 80 and over warn.
func level(percent float64) string {
	switch {
	case percent >= BadPercent:
		return LevelBad
	case percent >= WarnPercent:
		return LevelWarn
	}
	return LevelOK
}

// The states one claim's fill is in.
const (
	// FillMeasured: a kubelet reported the claim's filesystem.
	FillMeasured = "measured"
	// FillShared: a kubelet reported it, and the filesystem is not the claim's
	// size - a share (NFS, CephFS) reports the whole export. Used is shown, no
	// percentage.
	FillShared = "shared"
	// FillUnmounted: no running pod mounts it, so no kubelet has a figure.
	FillUnmounted = "unmounted"
	// FillUnread: a pod mounts it and its node's kubelet gave no figure for it
	// - refused, unreachable, or not reporting that volume.
	FillUnread = "unread"
)

// Fill is how full one claim is.
type Fill struct {
	State         string  `json:"state"`
	UsedBytes     int64   `json:"usedBytes,omitempty"`
	CapacityBytes int64   `json:"capacityBytes,omitempty"`
	Percent       float64 `json:"percent,omitempty"`
	Level         string  `json:"level,omitempty"`
}

// The mount states of a claim.
const (
	MountMounted   = "mounted"
	MountUnmounted = "unmounted"
	// MountUnknown: the pods could not all be read, so whether one mounts the
	// claim is not known - never drawn as unmounted.
	MountUnknown = "unknown"
)

// The reasons something is an orphan.
const (
	OrphanUnmounted    = "unmounted"    // a claim no pod mounts
	OrphanWorkloadGone = "workloadGone" // a claim no pod mounts, whose StatefulSet is gone
	OrphanReleased     = "released"     // a volume whose claim was deleted
	OrphanAvailable    = "available"    // a volume no claim was ever bound to
	OrphanUnusedClass  = "unusedClass"  // a class no claim and no volume uses
)

// Workload is the controller of the pods that mount a claim.
type Workload struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Snapshot is a claim's last VolumeSnapshot.
type Snapshot struct {
	Name  string `json:"name"`
	At    string `json:"at,omitempty"`
	Ready bool   `json:"ready"`
	Class string `json:"class,omitempty"`
	Size  int64  `json:"sizeBytes,omitempty"`
}

// Claim is one PersistentVolumeClaim, as the wire carries it.
type Claim struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Phase is Bound, Pending or Lost.
	Phase  string `json:"phase"`
	Class  string `json:"class,omitempty"`
	Volume string `json:"volume,omitempty"`
	// RequestedBytes is spec.resources.requests.storage; CapacityBytes is
	// status.capacity.storage, what was provisioned, zero while unbound.
	RequestedBytes int64    `json:"requestedBytes"`
	CapacityBytes  int64    `json:"capacityBytes"`
	AccessModes    []string `json:"accessModes"`
	VolumeMode     string   `json:"volumeMode,omitempty"`
	// Reclaim is the bound volume's reclaim policy, empty when it was not read.
	Reclaim string `json:"reclaim,omitempty"`
	Created string `json:"created"`
	Mount   string `json:"mount"`
	// Pods are the pods that mount it, the first few.
	Pods     []string  `json:"pods"`
	MorePods int       `json:"morePods,omitempty"`
	Workload *Workload `json:"workload,omitempty"`
	// Orphan is empty, OrphanUnmounted or OrphanWorkloadGone.
	Orphan   string    `json:"orphan,omitempty"`
	Fill     Fill      `json:"fill"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	// Snapshots counts the claim's VolumeSnapshots.
	Snapshots int `json:"snapshots"`
}

// Volume is one PersistentVolume.
type Volume struct {
	Name string `json:"name"`
	// Phase is Available, Bound, Released, Failed or Pending.
	Phase         string   `json:"phase"`
	CapacityBytes int64    `json:"capacityBytes"`
	AccessModes   []string `json:"accessModes"`
	Reclaim       string   `json:"reclaim"`
	Class         string   `json:"class,omitempty"`
	Driver        string   `json:"driver,omitempty"`
	// Claim is `namespace/name`, named only when that claim was read or every
	// claim was: a volume never names a claim in a namespace the person may
	// not list.
	Claim   string `json:"claim,omitempty"`
	Created string `json:"created"`
	// Orphan is empty, OrphanReleased or OrphanAvailable.
	Orphan string `json:"orphan,omitempty"`
}

// Class is one StorageClass.
type Class struct {
	Name        string `json:"name"`
	Provisioner string `json:"provisioner"`
	Reclaim     string `json:"reclaim"`
	Binding     string `json:"binding"`
	Expansion   bool   `json:"expansion"`
	Default     bool   `json:"default"`
	Created     string `json:"created"`
	Claims      int    `json:"claims"`
	Volumes     int    `json:"volumes"`
	// Orphan is OrphanUnusedClass when nothing uses it. Never set when the
	// claims could not all be read: an unread claim may use it.
	Orphan string `json:"orphan,omitempty"`
}

// bytesOf is a quantity's value in bytes, zero when absent.
func bytesOf(list corev1.ResourceList) int64 {
	q, ok := list[corev1.ResourceStorage]
	if !ok {
		return 0
	}
	return q.Value()
}

// modes spells access modes as kubectl does.
func modes(in []corev1.PersistentVolumeAccessMode) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		switch m {
		case corev1.ReadWriteOnce:
			out = append(out, "RWO")
		case corev1.ReadOnlyMany:
			out = append(out, "ROX")
		case corev1.ReadWriteMany:
			out = append(out, "RWX")
		case corev1.ReadWriteOncePod:
			out = append(out, "RWOP")
		default:
			out = append(out, string(m))
		}
	}
	return out
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func readClaim(c *corev1.PersistentVolumeClaim) Claim {
	out := Claim{
		Namespace: c.Namespace, Name: c.Name, Phase: string(c.Status.Phase),
		Volume:         c.Spec.VolumeName,
		RequestedBytes: bytesOf(c.Spec.Resources.Requests),
		CapacityBytes:  bytesOf(c.Status.Capacity),
		AccessModes:    modes(c.Spec.AccessModes),
		Created:        stamp(c.CreationTimestamp.Time),
		Pods:           []string{},
	}
	if out.Phase == "" {
		out.Phase = string(corev1.ClaimPending)
	}
	if c.Spec.StorageClassName != nil {
		out.Class = *c.Spec.StorageClassName
	}
	if c.Spec.VolumeMode != nil {
		out.VolumeMode = string(*c.Spec.VolumeMode)
	}
	return out
}

func readVolume(v *corev1.PersistentVolume) Volume {
	out := Volume{
		Name: v.Name, Phase: string(v.Status.Phase),
		CapacityBytes: bytesOf(v.Spec.Capacity),
		AccessModes:   modes(v.Spec.AccessModes),
		Reclaim:       string(v.Spec.PersistentVolumeReclaimPolicy),
		Class:         v.Spec.StorageClassName,
		Created:       stamp(v.CreationTimestamp.Time),
	}
	if v.Spec.CSI != nil {
		out.Driver = v.Spec.CSI.Driver
	}
	switch v.Status.Phase {
	case corev1.VolumeReleased:
		out.Orphan = OrphanReleased
	case corev1.VolumeAvailable:
		if v.Spec.ClaimRef == nil {
			out.Orphan = OrphanAvailable
		}
	}
	return out
}

// defaultClassAnnotations mark the cluster's default class, the beta spelling
// included: both are still written.
var defaultClassAnnotations = []string{
	"storageclass.kubernetes.io/is-default-class",
	"storageclass.beta.kubernetes.io/is-default-class",
}

func readClass(s *storagev1.StorageClass) Class {
	out := Class{Name: s.Name, Provisioner: s.Provisioner, Reclaim: string(corev1.PersistentVolumeReclaimDelete),
		Binding: string(storagev1.VolumeBindingImmediate), Created: stamp(s.CreationTimestamp.Time)}
	if s.ReclaimPolicy != nil {
		out.Reclaim = string(*s.ReclaimPolicy)
	}
	if s.VolumeBindingMode != nil {
		out.Binding = string(*s.VolumeBindingMode)
	}
	out.Expansion = s.AllowVolumeExpansion != nil && *s.AllowVolumeExpansion
	for _, a := range defaultClassAnnotations {
		if s.Annotations[a] == "true" {
			out.Default = true
		}
	}
	return out
}

// measure grades one claim's kubelet figures. declared is the claim's own
// capacity: a filesystem far from it is a share, whose figures are about
// somebody else's disk.
func measure(used, capacity, declared int64, trustworthy func(declared, measured int64) bool) Fill {
	f := Fill{State: FillMeasured, UsedBytes: used, CapacityBytes: capacity}
	if capacity <= 0 || (declared > 0 && !trustworthy(declared, capacity)) {
		f.State = FillShared
		f.CapacityBytes = 0
		return f
	}
	f.Percent = float64(used) * 100 / float64(capacity)
	f.Level = level(f.Percent)
	return f
}

// sortClaims puts the orphans and the fullest first, then namespace and name,
// so two answers to one cluster draw one order.
func sortClaims(cs []Claim) {
	rank := func(c Claim) int {
		switch {
		case c.Fill.Level == LevelBad:
			return 0
		case c.Fill.Level == LevelWarn:
			return 1
		case c.Orphan != "":
			return 2
		case c.Phase != string(corev1.ClaimBound):
			return 3
		}
		return 4
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if a.Fill.Percent != b.Fill.Percent {
			return a.Fill.Percent > b.Fill.Percent
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
}

func sortVolumes(vs []Volume) {
	sort.SliceStable(vs, func(i, j int) bool {
		a, b := vs[i], vs[j]
		if (a.Orphan != "") != (b.Orphan != "") {
			return a.Orphan != ""
		}
		return a.Name < b.Name
	})
}

func sortClasses(cs []Class) {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
}
