package detail

// Following a container's references to the ConfigMaps and Secrets they name.
//
// Key names are listed for both kinds: a key name is not a secret, a value is
// (the Secrets list already prints its key names). The kind decides the rest:
//
//   - ConfigMap: values are resolved on the drawer open and drawn on the row,
//     clipped, with the rest one click away. A ConfigMap is not a credential
//     store, and hiding a log level behind a button teaches the operator to
//     press it without reading.
//   - Secret: names only. Values are never kept in a structure that outlives
//     the call; a value is revealed per key through `Reader.Data`.
//
// Two guards hold the Secret half: `entryOf` (data.go) fills no Preview for a
// Secret, and `fillFromEntry` checks the kind the pod spec names. Each is
// reachable where the other is not, so each can be tested alone.
//
// Reads on the drawer's default tab are capped by maxEnvSources, shared by
// `envFrom` and `configMapKeyRef`, and the panel says when the cap bites. There
// is no API for key names only, so a referenced Secret's values pass through
// this process for one call.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/textutil"
)

// maxEnvSources is how many distinct ConfigMaps and Secrets one drawer open will
// read to resolve a container's environment: about three containers importing
// two objects each. A ceiling, not a guess at the norm.
const maxEnvSources = 6

// envPreviewLimit is how much of a ConfigMap value one environment row carries.
// Shorter than data.go's `previewLimit` because this is a cell of the drawer's
// two-column table (about 300px): 44 characters is two lines beside the source
// label. Anything longer is one click from being shown whole.
const envPreviewLimit = 44

// envLookup reads one ConfigMap or Secret in the pod's namespace and returns its
// data block as `dataOf` measured it. A function rather than a client, so the
// passes can be tested without a cluster and can ask for nothing but a data
// block. A DataSet from a Secret carries no Preview or Value (`dataOf`).
type envLookup func(ctx context.Context, kind, name string) (*DataSet, error)

// envResolver is the read-once, capped budget both passes share: a pod naming
// three ConfigMaps costs three reads whether it names them in `envFrom`, in
// `configMapKeyRef`, or both.
type envResolver struct {
	lookup envLookup
	// held is what came back, keyed by object; a nil DataSet is recorded as a
	// failure, never here.
	held map[envKey]*DataSet
	// errs holds the error, not the sentence: the sentence depends on the
	// reference (optional, import or single key), and two references to one
	// missing object can differ in both.
	errs map[envKey]error
	// skipped is every distinct object the cap refused; the panel note counts it.
	skipped map[envKey]bool
	budget  int
}

type envKey struct{ kind, name string }

// sourceRead is one object's outcome: read, failed, or skipped by the cap. The
// panel must tell the three apart.
type sourceRead struct {
	set     *DataSet
	err     error
	skipped bool
}

// read returns what is known about one object, reading it at most once for the
// whole drawer open.
func (e *envResolver) read(ctx context.Context, kind, name string) sourceRead {
	key := envKey{kind, name}
	if set, ok := e.held[key]; ok {
		return sourceRead{set: set}
	}
	if err, ok := e.errs[key]; ok {
		return sourceRead{err: err}
	}
	if e.skipped[key] {
		return sourceRead{skipped: true}
	}
	if e.budget == 0 {
		e.skipped[key] = true
		return sourceRead{skipped: true}
	}
	e.budget--

	set, err := e.lookup(ctx, kind, name)
	switch {
	case err != nil:
		e.errs[key] = err
		return sourceRead{err: err}
	case set == nil:
		// "No error, nothing here" must not be cached as an object with no keys.
		err = fmt.Errorf("%s %s came back with no data block", kind, name)
		e.errs[key] = err
		return sourceRead{err: err}
	}
	e.held[key] = set
	return sourceRead{set: set}
}

// expandEnvFrom resolves each container's environment against the cluster, and
// returns the note that says what was left out.
//
// Containers are mutated in place. The note is empty when nothing was skipped.
func expandEnvFrom(ctx context.Context, containers []Container, lookup envLookup) string {
	res := &envResolver{
		lookup:  lookup,
		held:    map[envKey]*DataSet{},
		errs:    map[envKey]error{},
		skipped: map[envKey]bool{},
		budget:  maxEnvSources,
	}

	// Imports first: an import that misses the budget loses a whole list of
	// key names, a `configMapKeyRef` loses one value, so single-key references
	// must not displace imports.
	expandImports(ctx, containers, res)
	resolveKeyRefs(ctx, containers, res)

	if len(res.skipped) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%d more referenced object(s) were not read. This drawer stops at %d, so opening a pod "+
			"stays a handful of requests; what the rest hold is on those objects' own pages.",
		len(res.skipped), maxEnvSources)
}

// expandImports turns each container's `envFrom` into the rows it contributes.
func expandImports(ctx context.Context, containers []Container, res *envResolver) {
	for i := range containers {
		for j := range containers[i].EnvFrom {
			from := &containers[i].EnvFrom[j]
			if from.Name == "" {
				// The API rejects a nameless `envFrom`: nothing to look up.
				from.Problem = "this import names no object"
				from.Absence = AbsenceUnread
				continue
			}
			ref := envRef{
				kind: from.Kind, name: from.Name, optional: from.Optional,
				noun: "import", lost: "its keys cannot be listed",
			}

			got := res.read(ctx, from.Kind, from.Name)
			switch {
			case got.skipped:
				from.Problem = capNote("its keys are not listed here")
				from.Absence = AbsenceUnread
				continue
			case got.err != nil:
				from.Problem = lookupProblem(ref, got.err)
				from.Absence = lookupAbsence(ref, got.err)
				continue
			}

			from.Keys = len(got.set.Entries)
			for _, entry := range got.set.Entries {
				row := EnvVar{
					// The variable the container sees is PREFIX_KEY.
					Name: from.Prefix + entry.Key,
					Source: &EnvSource{
						Kind: from.Kind, Name: from.Name, Key: entry.Key,
						Optional: from.Optional,
					},
					Secret: from.Kind == "Secret",
				}
				// No further read: `dataOf` measured every value already.
				fillFromEntry(&row, from.Kind, entry)
				containers[i].Imported = append(containers[i].Imported, row)
			}
		}
	}
}

// resolveKeyRefs puts a value on every `configMapKeyRef` row.
//
// A `secretKeyRef` is deliberately not resolved and costs no read: nothing
// here could use its value.
func resolveKeyRefs(ctx context.Context, containers []Container, res *envResolver) {
	for i := range containers {
		for j := range containers[i].Env {
			v := &containers[i].Env[j]
			if v.Source == nil || v.Source.Name == "" || v.Source.Key == "" {
				continue
			}
			if !strings.EqualFold(v.Source.Kind, "ConfigMap") {
				continue
			}
			ref := envRef{
				kind: v.Source.Kind, name: v.Source.Name, optional: v.Source.Optional,
				noun: "reference", lost: "its value is not on this row",
			}

			got := res.read(ctx, v.Source.Kind, v.Source.Name)
			switch {
			case got.skipped:
				v.Problem = capNote("this value was not read")
				v.Absence = AbsenceUnread
				continue
			case got.err != nil:
				v.Problem = lookupProblem(ref, got.err)
				v.Absence = lookupAbsence(ref, got.err)
				continue
			}

			entry, found := got.set.Entry(v.Source.Key)
			if !found {
				// The object was read and lacks the key; asking again would give the
				// same answer, so the row keeps no control.
				v.Problem = missingKeyNote()
				v.Absence = AbsenceUnset
				if v.Source.Optional {
					v.Absence = AbsenceExpected
				}
				continue
			}
			fillFromEntry(v, v.Source.Kind, entry)
		}
	}
}

// fillFromEntry puts a ConfigMap's value on a row, or says why it is not there.
//
// The second of two guards on the kind: `kind` is what the pod spec says the
// reference points at, while `dataOf` read the kind off the returned object.
// Kept apart so this one can be tested with a Secret whose previews are
// filled, which no cluster produces.
func fillFromEntry(row *EnvVar, kind string, entry DataEntry) {
	if !strings.EqualFold(kind, "ConfigMap") {
		return
	}
	if entry.Binary {
		// `binaryData` a `configMapKeyRef` may name: printing it would read as
		// a corrupted value and break the rows below. Same sentence as reveal.
		row.Problem = "not text — " + textutil.Bytes(int64(entry.Stored)) +
			" that is not valid UTF-8. Read it with kubectl and a pipe."
		row.Absence = AbsenceUnprintable
		return
	}
	row.Held = true
	// `dataOf` clipped it for the ConfigMap panel; this clips it to the env
	// table's cell. `textutil.Clip` keeps the ellipsis inside the budget.
	row.Resolved = textutil.Clip(entry.Preview, envPreviewLimit)
	row.Clipped = entry.Clipped || row.Resolved != entry.Preview
}

// envRef is what a failed or skipped read has to be described in terms of.
//
// `noun` is how the pod spells the thing ("import" for `envFrom`, "reference"
// for `configMapKeyRef`), so the operator finds the panel's word in the YAML;
// `lost` is what this reader did not get (key names or one value).
type envRef struct {
	kind     string
	name     string
	optional bool
	noun     string
	lost     string
}

func (r envRef) subject() string { return r.kind + " " + r.name }

// capNote is what one unread reference says about itself; the count is in the
// panel-wide note.
func capNote(lost string) string {
	return "not read — this drawer stops after " + strconv.Itoa(maxEnvSources) +
		" referenced objects, so " + lost
}

// missingKeyNote is the object read and the key not in it. It says nothing about
// optional: the panel draws the reference itself.
func missingKeyNote() string {
	return "no such key"
}

// lookupProblem turns a failed read into the sentence the panel shows.
//
// "Could not read" must never render as "holds no keys": an operator told "no
// variables" when the truth is "you may not look" goes and debugs the workload.
func lookupProblem(ref envRef, err error) string {
	subject := ref.subject()

	switch {
	case errors.Is(err, access.ErrDenied):
		// The person's own review, not the console's credentials.
		return "you may not read " + subject + " in this namespace, so " +
			ref.lost + " — the variables are still set in the container"
	case apierrors.IsForbidden(err):
		return "this console is not allowed to read " + subject + " in this namespace, so " +
			ref.lost + " — the variables are still set in the container"
	case absentObject(ref, err):
		return "there is no " + subject + " here"
	case apierrors.IsUnauthorized(err):
		return "the credentials for this cluster were refused while reading " + subject +
			" — an expired session rather than anything about this pod"
	}
	return "reading " + subject + " failed: " + firstLine(err.Error())
}

// lookupAbsence is the kind a failed read is drawn as, beside the sentence
// lookupProblem wrote for it.
//
// A missing object is `expected` whether or not the reference is optional:
// the pod's own status and events say why a container is stuck. A refusal or
// any other failure stays `unread`. Decided per reference, never cached.
func lookupAbsence(ref envRef, err error) Absence {
	switch {
	case errors.Is(err, access.ErrDenied), apierrors.IsForbidden(err):
		return AbsenceUnread
	case absentObject(ref, err):
		return AbsenceExpected
	}
	return AbsenceUnread
}

// absentObject reports that err is the API server saying *this* object does not
// exist — a NotFound whose details name the object the reference names.
//
// The name is checked as well as the reason: only "this object does not
// exist" turns a warning into a muted line, and a 404 about anything else (a
// proxy, a missing aggregated API) is a failed read. client-go fills the
// details from the request, so a real answer about the named object passes.
func absentObject(ref envRef, err error) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	details := status.Status().Details
	return details != nil && details.Name == ref.name
}

// envLookupFor builds the reader the passes above use, against one cluster and
// one namespace.
//
// Through Locate and the dynamic client like the rest of this package, and
// through `dataOf`, the same function as the ConfigMap/Secret panel: a
// Secret's entries come back with names and sizes only, so its values are in
// no structure that outlives the call.
func (r *Reader) envLookupFor(contextName, namespace string) envLookup {
	return func(ctx context.Context, kind, name string) (*DataSet, error) {
		where, err := r.Locate(ctx, contextName, kind)
		if err != nil {
			return nil, err
		}
		// Only the two kinds with a data block; the same check `Reader.Data`
		// makes before reading a value.
		if !HasData(where.GroupVersionKind.Kind) {
			return nil, fmt.Errorf("%s has no data block", where.GroupVersionKind.Kind)
		}
		iface, err := where.Interface(namespace)
		if err != nil {
			return nil, err
		}
		// Each referenced object is reviewed by its own name: a person who may
		// read the pod and not the Secret it imports is told so.
		var raw *unstructured.Unstructured
		_, err = r.gate.Read(ctx, where.Cluster, where.Attributes("get", namespace, name), func(ctx context.Context) error {
			var err error
			raw, err = iface.Get(ctx, name, metav1.GetOptions{})
			return err
		})
		if err != nil {
			return nil, err
		}
		set := dataOf(raw, where.GroupVersionKind.Kind)
		if set == nil {
			return nil, fmt.Errorf("%s has no data block", where.GroupVersionKind.Kind)
		}
		return set, nil
	}
}
