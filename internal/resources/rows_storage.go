package resources

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

// --- storage -------------------------------------------------------------------

// storageClassRow: provisioner, reclaim policy and binding mode decide whether
// a claim binds and what happens to its data. Default shows at a glance
// whether exactly one class is the default, as it should be.
func storageClassRow(c *storagev1.StorageClass, now time.Time) []Cell {
	reclaim := ""
	if c.ReclaimPolicy != nil {
		reclaim = string(*c.ReclaimPolicy)
	}
	binding := ""
	if c.VolumeBindingMode != nil {
		binding = string(*c.VolumeBindingMode)
	}

	return []Cell{
		text(c.Name),
		text(c.Provisioner),
		text(reclaim),
		text(binding),
		defaultClass(c.Annotations,
			"storageclass.kubernetes.io/is-default-class",
			// The beta spelling, still written by some older charts and
			// provisioners.
			"storageclass.beta.kubernetes.io/is-default-class"),
		age(c.CreationTimestamp, now),
	}
}

func pvcRow(p *corev1.PersistentVolumeClaim, now time.Time, usage *Usage) []Cell {
	class, rank := gradePhase(string(p.Status.Phase), map[string]string{
		string(corev1.ClaimBound):   ClassOK,
		string(corev1.ClaimPending): ClassWarn,
		string(corev1.ClaimLost):    ClassBad,
	})

	capacity := quantityCell(p.Status.Capacity, corev1.ResourceStorage)
	if absent(capacity) {
		// Not bound yet: the request is the only figure.
		capacity = quantityCell(p.Spec.Resources.Requests, corev1.ResourceStorage)
	}

	return []Cell{
		text(p.Name),
		text(p.Namespace),
		graded(string(p.Status.Phase), class, rank),
		capacity,
		claimUsed(usage, p.Status.Phase == corev1.ClaimBound),
		text(accessModes(p.Spec.AccessModes)),
		text(p.Spec.VolumeName),
		text(storageClass(p.Spec.StorageClassName)),
		age(p.CreationTimestamp, now),
	}
}

// claimUsed is the Used column on a claim, with the sentence explaining an em
// dash. How full a claim is exists nowhere in the API: only the kubelet that
// mounted it knows, through `nodes/proxy`, which may be refused, and a kubelet
// may measure a shared export rather than the claim. So em dashes are ordinary,
// and never drawn as empty bars.
//
// Unbound and unreported claims are explained here; the kubelet's reasons are
// written in internal/web/volumeusage.go and arrive as Volume.Note.
func claimUsed(usage *Usage, bound bool) Cell {
	cell := volumeCell(usage)
	if cell.Text != Missing || cell.Hint != "" {
		return cell
	}
	if !bound {
		// Nothing can have mounted it.
		cell.Hint = "This claim is not bound, so no kubelet has mounted its " +
			"filesystem and nothing has measured it. How full a volume is exists " +
			"nowhere in the Kubernetes API."
		return cell
	}
	cell.Hint = unmeasuredClaim
	return cell
}

// unmeasuredClaim is what a bound claim with no reading says. It names all
// three causes, since they cannot be told apart from here.
const unmeasuredClaim = "No kubelet reported this volume. Either no running pod " +
	"has it mounted — the kubelet only knows about the filesystems it mounted — " +
	"or the kubelet on the node holding it could not be asked. That read needs " +
	"the `nodes/proxy` permission and an API server that can reach the kubelet " +
	"on 10250; several managed offerings refuse the first, and a firewalled " +
	"kubelet answers the second with a 404 that names neither."

func pvRow(p *corev1.PersistentVolume, now time.Time, usage *Usage) []Cell {
	class, rank := gradePhase(string(p.Status.Phase), map[string]string{
		string(corev1.VolumeBound):     ClassOK,
		string(corev1.VolumeAvailable): ClassNone,
		string(corev1.VolumeReleased):  ClassWarn,
		string(corev1.VolumePending):   ClassWarn,
		string(corev1.VolumeFailed):    ClassBad,
	})

	claim := ""
	if p.Spec.ClaimRef != nil {
		claim = p.Spec.ClaimRef.Namespace + "/" + p.Spec.ClaimRef.Name
	}

	return []Cell{
		text(p.Name),
		graded(string(p.Status.Phase), class, rank),
		quantityCell(p.Spec.Capacity, corev1.ResourceStorage),
		volumeUsed(usage, claim),
		text(accessModes(p.Spec.AccessModes)),
		text(string(p.Spec.PersistentVolumeReclaimPolicy)),
		text(claim),
		text(p.Spec.StorageClassName),
		age(p.CreationTimestamp, now),
	}
}

// volumeUsed is the Used column on a PersistentVolume: its claim's reading,
// since a PV and its PVC are one filesystem. An unclaimed volume is mounted
// nowhere, and says so rather than blaming the kubelet.
func volumeUsed(usage *Usage, claim string) Cell {
	cell := volumeCell(usage)
	if cell.Text != Missing || cell.Hint != "" {
		return cell
	}
	if claim == "" {
		cell.Hint = "Nothing has claimed this volume, so no kubelet has mounted " +
			"it and nothing has measured it."
		return cell
	}
	cell.Hint = unmeasuredClaim
	return cell
}
