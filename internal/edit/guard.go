package edit

import "strings"

// What the API server will refuse, said before it is asked.
//
// The dry run catches these too, but the API's message ("field is
// immutable", no field named) is baffling; a predictable refusal costs no
// round trip here, names the field and says what to do instead.
//
// The list is short on purpose: immutable fields on kinds an operator opens.
// An over-broad guard refuses edits that would have worked, with no way to
// argue with it.

// forbidden is one path a patch must not touch, and why.
type forbidden struct {
	// kinds is empty for a rule that applies to everything.
	kinds []string
	path  string
	why   string
}

var forbiddenPaths = []forbidden{
	{path: "apiVersion", why: "the apiVersion of an existing object cannot be changed; create the object under the other version instead"},
	{path: "kind", why: "an object cannot change kind"},
	{path: "metadata.name", why: "an object cannot be renamed. Create the new one and delete the old"},
	{path: "metadata.namespace", why: "an object cannot be moved between namespaces. Create it in the other namespace and delete this one"},
	{path: "metadata.uid", why: "the uid is the cluster's identity for this object and is not yours to set"},
	{path: "metadata.creationTimestamp", why: "the creation timestamp is the cluster's record of when this object appeared"},
	{path: "metadata.generation", why: "the generation is maintained by the API server"},
	{
		path: "metadata.managedFields",
		why: "managedFields is the record of who wrote which field, and the drawer takes it out of the " +
			"manifest before showing it — which is why writing it back would be adding it rather than " +
			"keeping it. The Activity tab is where that information is readable",
	},
	{path: "metadata.resourceVersion", why: "the resourceVersion is the precondition this save carries; editing it by hand would either do nothing or overwrite whatever changed underneath you. Leave it alone"},
	{
		path: "status",
		why: "status is written by controllers, not by hand. It is a separate subresource on most kinds, " +
			"so a change to it here would be dropped without a word — which is worse than being refused",
	},

	{kinds: []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job"},
		path: "spec.selector",
		why:  "the selector of a controller is immutable: it is what already binds it to its pods. Changing it means creating a new object"},
	{kinds: []string{"Service"}, path: "spec.clusterIP",
		why: "the cluster IP is assigned once and cannot be changed. Recreate the Service to move it"},
	{kinds: []string{"Service"}, path: "spec.clusterIPs",
		why: "the cluster IPs are assigned once and cannot be changed"},
	{kinds: []string{"Job"}, path: "spec.template",
		why: "a Job's pod template is immutable once it has been created. Delete the Job and create it again"},
	{kinds: []string{"Job"}, path: "spec.completions",
		why: "a Job's completion count is immutable"},
	{kinds: []string{"PersistentVolumeClaim"}, path: "spec.accessModes",
		why: "the access modes of a bound claim are immutable"},
	{kinds: []string{"PersistentVolumeClaim"}, path: "spec.storageClassName",
		why: "the storage class of a claim is immutable"},
	{kinds: []string{"PersistentVolumeClaim"}, path: "spec.volumeName",
		why: "the volume a claim is bound to is not yours to reassign"},
	{kinds: []string{"PersistentVolumeClaim"}, path: "spec.volumeMode",
		why: "the volume mode of a claim is immutable"},
}

// predictRefusal reports the first change in this patch that the API is certain
// to refuse.
func predictRefusal(kind string, immutable bool, patch map[string]any) error {
	// An immutable ConfigMap or Secret refuses a whole block, not a field,
	// and is common on config deployed by a pipeline: exactly the config
	// somebody opens wanting to change.
	if immutable && isDataKind(kind) {
		for _, block := range []string{"data", "binaryData", "stringData", "immutable"} {
			if hasPath(patch, block) {
				return refuse(RefusedImmutable,
					"this %s is declared immutable: true, so the API refuses every change to %s. "+
						"An immutable object has to be replaced, not edited.", kind, block)
			}
		}
	}

	for _, rule := range forbiddenPaths {
		if !rule.applies(kind) || !hasPath(patch, rule.path) {
			continue
		}
		return refuse(RefusedImmutable, "%s cannot be changed: %s", rule.path, rule.why)
	}
	return nil
}

func (f forbidden) applies(kind string) bool {
	if len(f.kinds) == 0 {
		return true
	}
	for _, candidate := range f.kinds {
		if strings.EqualFold(candidate, kind) {
			return true
		}
	}
	return false
}
