package storage

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

var (
	claimsGVR  = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	volumesGVR = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	classesGVR = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
	podsGVR    = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	setsGVR    = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
)

// listLimit is the most objects of one kind a read holds. Past it the answer
// says the read was cut rather than reading on.
const listLimit = 5000

// maxRows is the most claims and the most volumes an answer carries; the
// counts are over every one read, and the rows are sorted with the orphans
// and the fullest first, so a cut drops only rows with nothing to say.
const maxRows = 2000

// maxPods is how many of a claim's pods its row names.
const maxPods = 3

// now is the clock; tests move it.
var now = time.Now

// Section states, for the reads beside the claims.
const (
	StateRead    = "read"
	StateAbsent  = "absent"  // the kind is not served: not installed
	StateRefused = "refused" // the person may not list it, or it could not be asked
	StateFailed  = "failed"  // the read failed
)

// Section is how one read beside the claims went.
type Section struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// The states of the fill source.
const (
	SourceRead       = "read"       // every kubelet asked answered
	SourcePartial    = "partial"    // some did
	SourceUnreadable = "unreadable" // none could be asked, or none answered
	SourceNone       = "none"       // no running pod mounts a claim: nothing to ask
)

// FillSource is where the fill figures come from, and how far they reach.
type FillSource struct {
	State    string `json:"state"`
	Asked    int    `json:"asked"`
	Answered int    `json:"answered"`
	// Message is one line, set when the figures are not all there: what is
	// missing and what grants it.
	Message string `json:"message,omitempty"`
	// Detail is the error behind it, for a hover.
	Detail string `json:"detail,omitempty"`
	// Via names what gave the figures, in the order asked:
	// extension.EnginePrometheus or extension.EngineVictoriaMetrics for the
	// cluster's metrics backend (#91), then ViaKubelet for the claims it did
	// not cover. Empty when nothing gave one.
	Via []string `json:"via,omitempty"`
}

// ViaKubelet: figures read from the kubelets, through nodes/proxy.
const ViaKubelet = "kubelet"

// Counts are the tiles.
type Counts struct {
	Claims        int `json:"claims"`
	Bound         int `json:"bound"`
	Pending       int `json:"pending"`
	Lost          int `json:"lost"`
	Volumes       int `json:"volumes"`
	Classes       int `json:"classes"`
	Orphans       int `json:"orphans"`
	OrphanClaims  int `json:"orphanClaims"`
	OrphanVolumes int `json:"orphanVolumes"`
	OrphanClasses int `json:"orphanClasses"`
	// RetainedOrphans counts the orphaned volumes whose policy is Retain: the
	// disk is kept, and billed, until somebody deletes it.
	RetainedOrphans int `json:"retainedOrphans"`
	Measured        int `json:"measured"`
	// Shared counts the claims on a share: measured, with no percentage.
	Shared int `json:"shared"`
	// Unread counts the mounted claims no kubelet gave a figure for.
	Unread           int   `json:"unread"`
	Warn             int   `json:"warn"`
	Bad              int   `json:"bad"`
	Snapshotted      int   `json:"snapshotted"`
	RequestedBytes   int64 `json:"requestedBytes"`
	ProvisionedBytes int64 `json:"provisionedBytes"`
}

// Answer is the page's one answer.
type Answer struct {
	Namespace string   `json:"namespace"`
	Now       string   `json:"now"`
	Complete  bool     `json:"complete"`
	Notes     []string `json:"notes"`
	Counts    Counts   `json:"counts"`
	Claims    []Claim  `json:"claims"`
	// Shown is len(Claims); Counts.Claims is how many were read.
	Shown       int             `json:"shown"`
	Volumes     []Volume        `json:"volumes"`
	VolumesRead Section         `json:"volumesRead"`
	Classes     []Class         `json:"classes"`
	ClassesRead Section         `json:"classesRead"`
	MountsRead  Section         `json:"mountsRead"`
	Fill        FillSource      `json:"fill"`
	Snapshots   SnapshotSection `json:"snapshots"`
}

// podInfo is what a pod tells this page: where it runs and what it mounts.
type podInfo struct {
	namespace, name, node string
	uid                   string
	running               bool
	// claims are the claims its persistentVolumeClaim volumes name, and the
	// `<pod>-<volume>` claims of its generic ephemeral volumes; ephemeral says
	// which are the latter, mounted only by the pod that owns them.
	claims    []string
	ephemeral map[string]bool
	workload  *Workload
}

// podOwner is the pod a claim's ownerReference names.
type podOwner struct{ name, uid string }

// set is a StatefulSet, reduced to what names its claims.
type set struct {
	name      string
	templates []string
}

// reading is one read's whole result, the parts the tabs need beside the
// answer.
type reading struct {
	answer Answer
	pods   map[string]podInfo // by namespace/name; nil when not read
	// mounts are the claims each pod mounts, by namespace/name of the pod,
	// under the same rule as the claims' Mount: an ephemeral volume's claim
	// only when the pod owns it.
	mounts  map[string][]string
	history map[string][]Snapshot
}

// failure is a read the request cannot answer without: the claims.
type failure struct {
	step string
	err  error
}

func (f *failure) Error() string { return f.step + ": " + f.err.Error() }
func (f *failure) Unwrap() error { return f.err }

func sectionOf(err error) Section {
	state := StateFailed
	if errors.Is(err, extension.ErrDenied) || errors.Is(err, extension.ErrUnreviewable) {
		state = StateRefused
	}
	s := Section{State: state, Detail: shortError(err)}
	switch {
	case state == StateRefused && errors.Is(err, extension.ErrUnreviewable):
		s.Message = "Not asked"
	case state == StateRefused:
		s.Message = "Refused"
	default:
		s.Message = "Unreadable"
	}
	return s
}

func noteOr(what, note string) string {
	if note == "" {
		note = "the read stopped before the end."
	}
	return what + ": " + note
}

// read reads everything in scope: ns, or every namespace when empty.
func read(r *http.Request, h extension.Host, cluster, ns string, at time.Time) (*reading, error) {
	out := &reading{answer: Answer{Namespace: ns, Now: at.UTC().Format(time.RFC3339), Complete: true, Notes: []string{},
		Claims: []Claim{}, Volumes: []Volume{}, Classes: []Class{},
		VolumesRead: Section{State: StateRead}, ClassesRead: Section{State: StateRead}, MountsRead: Section{State: StateRead}}}
	a := &out.answer
	incomplete := func(what, note string) {
		a.Complete = false
		a.Notes = append(a.Notes, noteOr(what, note))
	}

	page, err := h.List(r, cluster, extension.ListQuery{GVR: claimsGVR, Namespace: ns, Limit: listLimit})
	if err != nil {
		return nil, &failure{step: stepClaims, err: err}
	}
	if !page.Complete {
		incomplete("Claims", page.Note)
	}
	// Every claim in the cluster was read: only then may a volume name a claim
	// that was not, and only then is a class nothing uses known to be unused.
	everyClaim := ns == "" && page.Complete
	claims := make([]Claim, 0, len(page.Items))
	labelled := map[string]bool{}
	ownedBySet := map[string]string{}
	ownedByPod := map[string]podOwner{}
	for i := range page.Items {
		var c corev1.PersistentVolumeClaim
		if runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, &c) != nil {
			continue
		}
		claim := readClaim(&c)
		key := claim.Namespace + "/" + claim.Name
		labelled[key] = len(c.Labels) > 0
		for _, owner := range c.OwnerReferences {
			switch owner.Kind {
			case "StatefulSet":
				ownedBySet[key] = owner.Name
			case "Pod":
				// A generic ephemeral volume's claim, owned by its pod.
				ownedByPod[key] = podOwner{name: owner.Name, uid: string(owner.UID)}
			}
		}
		claims = append(claims, claim)
	}
	index := map[string]int{}
	for i, c := range claims {
		index[c.Namespace+"/"+c.Name] = i
	}

	// The volumes: cluster-scoped, so in a namespace only those whose claim is
	// there.
	volumes := []Volume{}
	byName := map[string]Volume{}
	volumesComplete := false
	if page, err := h.List(r, cluster, extension.ListQuery{GVR: volumesGVR, Limit: listLimit}); err != nil {
		a.VolumesRead = sectionOf(err)
	} else {
		volumesComplete = page.Complete
		if !page.Complete {
			incomplete("Volumes", page.Note)
		}
		for i := range page.Items {
			var v corev1.PersistentVolume
			if runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, &v) != nil {
				continue
			}
			vol := readVolume(&v)
			if ref := v.Spec.ClaimRef; ref != nil {
				key := ref.Namespace + "/" + ref.Name
				if ns != "" && ref.Namespace != ns {
					continue
				}
				if _, ok := index[key]; ok || everyClaim {
					vol.Claim = key
				}
			} else if ns != "" {
				continue
			}
			byName[vol.Name] = vol
			volumes = append(volumes, vol)
		}
	}
	for i := range claims {
		if v, ok := byName[claims[i].Volume]; ok {
			claims[i].Reclaim = v.Reclaim
		}
	}

	classes := []Class{}
	if page, err := h.List(r, cluster, extension.ListQuery{GVR: classesGVR, Limit: listLimit}); err != nil {
		a.ClassesRead = sectionOf(err)
	} else {
		if !page.Complete {
			incomplete("Storage classes", page.Note)
		}
		for i := range page.Items {
			var s storagev1.StorageClass
			if runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, &s) != nil {
				continue
			}
			classes = append(classes, readClass(&s))
		}
	}

	// The pods, for who mounts what. Kept small: only what this page reads.
	pods, mountsKnown := readPods(r, h, cluster, ns, a, incomplete)
	out.pods = pods
	out.mounts = map[string][]string{}
	mounting := map[string][]podInfo{}
	for _, p := range pods {
		for _, c := range p.claims {
			key := p.namespace + "/" + c
			// An ephemeral volume's claim is the one Kubernetes made for this
			// pod: owned by it, UID and all. A claim that only shares the name is
			// somebody else's, and the pod cannot start on it.
			if p.ephemeral[c] && ownedByPod[key] != (podOwner{name: p.name, uid: p.uid}) {
				continue
			}
			mounting[key] = append(mounting[key], p)
			out.mounts[p.namespace+"/"+p.name] = append(out.mounts[p.namespace+"/"+p.name], c)
		}
	}
	sets, setsKnown := readSets(r, h, cluster, ns, incomplete)

	for i := range claims {
		c := &claims[i]
		key := c.Namespace + "/" + c.Name
		users := mounting[key]
		switch {
		case !mountsKnown:
			c.Mount = MountUnknown
		case len(users) == 0:
			c.Mount = MountUnmounted
			c.Orphan = OrphanUnmounted
			if setsKnown && gone(c.Name, ownedBySet[key], labelled[key], sets[c.Namespace]) {
				c.Orphan = OrphanWorkloadGone
			}
		default:
			c.Mount = MountMounted
		}
		for j, p := range users {
			if j < maxPods {
				c.Pods = append(c.Pods, p.name)
			}
			if c.Workload == nil && p.workload != nil {
				c.Workload = p.workload
			}
		}
		if len(users) > maxPods {
			c.MorePods = len(users) - maxPods
		}
	}

	a.Fill = fill(r, h, cluster, claims, mounting, incomplete)
	a.Snapshots, out.history = snapshots(r, h, cluster, ns, claims, incomplete)

	// The classes' use, over what was read.
	use := map[string][2]int{}
	for _, c := range claims {
		u := use[c.Class]
		u[0]++
		use[c.Class] = u
	}
	for _, v := range volumes {
		u := use[v.Class]
		u[1]++
		use[v.Class] = u
	}
	classesKnown := everyClaim && a.VolumesRead.State == StateRead && volumesComplete
	for i := range classes {
		u := use[classes[i].Name]
		classes[i].Claims, classes[i].Volumes = u[0], u[1]
		if classesKnown && ns == "" && u[0] == 0 && u[1] == 0 {
			classes[i].Orphan = OrphanUnusedClass
		}
	}

	tally(a, claims, volumes, classes)
	sortClaims(claims)
	sortVolumes(volumes)
	sortClasses(classes)
	if len(claims) > maxRows {
		claims = claims[:maxRows]
		a.Notes = append(a.Notes, fmt.Sprintf("First %d claims", maxRows))
	}
	if len(volumes) > maxRows {
		volumes = volumes[:maxRows]
		a.Notes = append(a.Notes, fmt.Sprintf("First %d volumes", maxRows))
	}
	a.Claims, a.Volumes, a.Classes = claims, volumes, classes
	a.Shown = len(claims)
	return out, nil
}

func tally(a *Answer, claims []Claim, volumes []Volume, classes []Class) {
	n := &a.Counts
	n.Claims, n.Volumes, n.Classes = len(claims), len(volumes), len(classes)
	for _, c := range claims {
		switch c.Phase {
		case string(corev1.ClaimBound):
			n.Bound++
		case string(corev1.ClaimLost):
			n.Lost++
		default:
			n.Pending++
		}
		n.RequestedBytes += c.RequestedBytes
		n.ProvisionedBytes += c.CapacityBytes
		if c.Orphan != "" {
			n.OrphanClaims++
		}
		if c.Fill.State == FillShared {
			n.Shared++
		}
		if c.Fill.State == FillUnread {
			n.Unread++
		}
		if c.Fill.State == FillMeasured {
			n.Measured++
			switch c.Fill.Level {
			case LevelWarn:
				n.Warn++
			case LevelBad:
				n.Bad++
			}
		}
		if c.Snapshots > 0 {
			n.Snapshotted++
		}
	}
	for _, v := range volumes {
		if v.Orphan != "" {
			n.OrphanVolumes++
			if v.Reclaim == string(corev1.PersistentVolumeReclaimRetain) {
				n.RetainedOrphans++
			}
		}
	}
	for _, c := range classes {
		if c.Orphan != "" {
			n.OrphanClasses++
		}
	}
	n.Orphans = n.OrphanClaims + n.OrphanVolumes + n.OrphanClasses
}

// readPods reads the pods in scope, one page at a time, keeping only those
// that mount a claim: a `persistentVolumeClaim` volume, or a generic ephemeral
// volume, whose claim Kubernetes names `<pod>-<volume>`. mountsKnown is false
// when they could not all be read: then no claim is called unmounted.
func readPods(r *http.Request, h extension.Host, cluster, ns string, a *Answer, incomplete func(string, string)) (map[string]podInfo, bool) {
	pods := map[string]podInfo{}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: ns, Each: func(o unstructured.Unstructured) error {
		var p corev1.Pod
		if runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &p) != nil {
			return nil
		}
		info := podInfo{namespace: p.Namespace, name: p.Name, node: p.Spec.NodeName, uid: string(p.UID),
			running: p.Status.Phase == corev1.PodRunning, workload: workloadOf(&p)}
		for _, v := range p.Spec.Volumes {
			switch {
			case v.PersistentVolumeClaim != nil:
				// One claim may back two volumes of a pod: listed once (#182).
				if !slices.Contains(info.claims, v.PersistentVolumeClaim.ClaimName) {
					info.claims = append(info.claims, v.PersistentVolumeClaim.ClaimName)
				}
			case v.Ephemeral != nil:
				if info.ephemeral == nil {
					info.ephemeral = map[string]bool{}
				}
				info.claims = append(info.claims, p.Name+"-"+v.Name)
				info.ephemeral[p.Name+"-"+v.Name] = true
			}
		}
		if len(info.claims) > 0 {
			pods[p.Namespace+"/"+p.Name] = info
		}
		return nil
	}})
	if err != nil {
		a.MountsRead = sectionOf(err)
		a.Complete = false
		return nil, false
	}
	if !page.Complete {
		incomplete("Pods", page.Note)
		return pods, false
	}
	return pods, true
}

// workloadOf is the pod's controller, a Deployment's named through its
// ReplicaSet's pod-template-hash suffix rather than by reading ReplicaSets.
func workloadOf(p *corev1.Pod) *Workload {
	for _, o := range p.OwnerReferences {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			if hash := p.Labels["pod-template-hash"]; hash != "" {
				if name, ok := strings.CutSuffix(o.Name, "-"+hash); ok {
					return &Workload{Kind: "Deployment", Name: name}
				}
			}
		}
		return &Workload{Kind: o.Kind, Name: o.Name}
	}
	return nil
}

// readSets reads the StatefulSets in scope, by namespace. known is false when
// they could not all be read: then no claim is called the claim of a gone
// StatefulSet.
func readSets(r *http.Request, h extension.Host, cluster, ns string, incomplete func(string, string)) (map[string][]set, bool) {
	sets := map[string][]set{}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: setsGVR, Namespace: ns, Each: func(o unstructured.Unstructured) error {
		s := set{name: o.GetName()}
		templates, _, _ := unstructured.NestedSlice(o.Object, "spec", "volumeClaimTemplates")
		for _, t := range templates {
			if m, ok := t.(map[string]any); ok {
				if name, _, _ := unstructured.NestedString(m, "metadata", "name"); name != "" {
					s.templates = append(s.templates, name)
				}
			}
		}
		sets[o.GetNamespace()] = append(sets[o.GetNamespace()], s)
		return nil
	}})
	if err != nil {
		incomplete("StatefulSets", sectionOf(err).Message)
		return nil, false
	}
	if !page.Complete {
		incomplete("StatefulSets", page.Note)
		return sets, false
	}
	return sets, true
}

// ordinal is the shape a StatefulSet gives its claims: `<template>-<set>-<n>`,
// an ordinal of at most four digits - a dated name such as `backup-20241101`
// is not one.
var ordinal = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?-[0-9]{1,4}$`)

// gone answers whether an unmounted claim is the claim of a StatefulSet that
// no longer exists: owned by one that is not there, or named the way a
// StatefulSet names its claims, carrying the labels it copies onto them, with
// no StatefulSet in its namespace that would own the name. A claim a live
// StatefulSet would own - one scaled down - is not gone.
func gone(claim, owner string, labelled bool, live []set) bool {
	for _, s := range live {
		if s.name == owner {
			return false
		}
		for _, t := range s.templates {
			if rest, ok := strings.CutPrefix(claim, t+"-"+s.name+"-"); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
				return false
			}
		}
	}
	if owner != "" {
		return true
	}
	return labelled && ordinal.MatchString(claim)
}
