// Package edit is the only thing in KubeTower that changes a cluster.
//
// Everything else reads, which is what makes the `KT_READONLY_CLUSTERS`
// guardrail coherent and the console safe to leave pointed at production:
//
//   - Nothing sends a whole object. Every write is a JSON merge patch
//     (RFC 7386) of only what the operator changed; a whole-object update
//     would send back `status` and every unseen field, and delete what the
//     drawer had trimmed.
//   - Every write carries `metadata.resourceVersion` as a precondition, so a
//     save built on a stale panel loses, and is told so in words.
//   - Every write is dry-run first (`DryRun: ["All"]`), so a rejection comes
//     with its reason before anything is committed.
//   - A cluster in `KT_READONLY_CLUSTERS` is refused here, in the code that
//     would send the request, not only by a button the UI hides.
package edit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/textutil"
)

// Location is where a kind lives on one cluster, with the client to reach it.
// It is `detail.Location` reduced to what a write needs; see Resolver.
type Location struct {
	// Cluster is the real context name as the kubeconfig spells it, not the
	// URL's: the name KT_READONLY_CLUSTERS is matched against and every
	// refusal names.
	Cluster    string
	Resource   schema.GroupVersionResource
	Kind       string // "ConfigMap", as a person writes it
	Namespaced bool
	Dynamic    dynamic.Interface

	// guarded records that this Location came out of locate(), the only place
	// KT_READONLY_CLUSTERS is consulted. The other fields are exported for
	// Resolvers outside this package, so a Location can be assembled by hand;
	// this unexported flag tells a fabricated one from a resolved one before
	// the request leaves.
	guarded bool
	// door is the Writer that stamped it: the access gate and the audit line
	// every request on this Location goes through. Set with guarded by
	// locate(); door.go refuses a Location without it.
	door *Writer
}

// Resolver turns a cluster name and a kind into an endpoint.
//
// A local interface rather than an import of the cluster manager and a
// second REST mapper: internal/web adapts detail.Reader, which already holds
// one mapper per cluster.
type Resolver interface {
	Locate(ctx context.Context, cluster, kind string) (Location, error)
}

// Options are what a Writer needs. A nil ReadOnly marks no cluster, as an
// empty KT_READONLY_CLUSTERS does.
type Options struct {
	Resolve  Resolver
	ReadOnly func(string) bool
	Logf     func(string, ...any)
	// Access is the choke point every request is reviewed at before it is
	// sent. Nil refuses every write as unreviewable (fail closed), never a
	// way to write without a review.
	Access *access.Gate
}

type Writer struct {
	resolve  Resolver
	readOnly func(string) bool
	logf     func(string, ...any)
	access   *access.Gate
}

func New(opts Options) *Writer {
	w := &Writer{resolve: opts.Resolve, readOnly: opts.ReadOnly, logf: opts.Logf, access: opts.Access}
	if w.readOnly == nil {
		w.readOnly = func(string) bool { return false }
	}
	if w.logf == nil {
		w.logf = func(string, ...any) {}
	}
	return w
}

// Target is one object.
type Target struct {
	Cluster   string
	Kind      string
	Namespace string
	Name      string
}

// Applied is what a write did, for the panel to say and the log to record.
type Applied struct {
	Target
	// ResourceVersion is what the object is at now. The panel re-arms with
	// it, so a second edit is not a conflict against its first version.
	ResourceVersion string
	// Fields are the dotted paths the patch touched.
	Fields []string
}

// --- refusals ----------------------------------------------------------------

// RefusalKind says why a write was not sent. The HTTP status is
// internal/web's; this package carries the sentence.
type RefusalKind int

const (
	// RefusedInput is a request that does not describe a change this panel makes.
	RefusedInput RefusalKind = iota
	// RefusedReadOnly is the KT_READONLY_CLUSTERS guardrail.
	RefusedReadOnly
	// RefusedImmutable is a change the API would refuse and this could predict.
	RefusedImmutable
	// RefusedConflict is a save built on a version of the object that has moved.
	RefusedConflict
	// RefusedByAPI is what the dry run came back with.
	RefusedByAPI
	// RefusedNoChange is an editor saved without an edit.
	RefusedNoChange
	// RefusedByAccess is the access review saying the person behind the
	// request may not do it: the cluster's own RBAC, asked before sending.
	RefusedByAccess
	// RefusedUnreviewable is an access review that could not be made. It
	// refuses like a denial and is not one - see internal/access.
	RefusedUnreviewable
	// RefusedUnsent is a request the console's own access guard held back
	// because no review covered it: the closure asked about one thing and sent
	// another, or sent on a context that was not the review's. A defect in the
	// console, never the person's rights: a 500, not a 403, so nobody goes to
	// an administrator about a RoleBinding.
	RefusedUnsent
	// RefusedUnrecorded is a write not sent because its audit record could not
	// be written first (access.ErrUnrecorded), named apart from
	// RefusedUnreviewable so nobody checks the cluster's access reviews when
	// it is the console's stdout that stopped.
	RefusedUnrecorded
	// RefusedNotSent is a write that never left the console (a *NotSent).
	// Nobody said no and nothing changed. Never RefusedByAPI, which would say
	// the cluster refused a request it never saw.
	RefusedNotSent
	// RefusedUnanswered is a write whose outcome is not known: it left and no
	// answer came back (an *Unanswered), or the API server answered that it
	// timed out (a *TimedOut), so the change may still happen. Never
	// RefusedByAPI. One kind for both, because the person's next step is the
	// same: look before doing it again.
	RefusedUnanswered
	// RefusedNotInProfile is a write the running profile does not make
	// (access.NotInProfile): a change to RBAC under the server profile. A 403
	// with its own sentence; never RefusedByAccess, which would send the person
	// to an administrator for a RoleBinding that changes nothing.
	RefusedNotInProfile
)

// Refusal is a write that was not sent, with the sentence to put on screen.
type Refusal struct {
	Kind   RefusalKind
	Reason string
	// cause is the API server's error a refusal was made from, nil for one
	// this package decided on its own.
	cause error
}

func (r *Refusal) Error() string { return r.Reason }

// Unwrap is the error the refusal was made from, so the outcome record
// names its reason (`Conflict` for a 409) rather than `error`. Readers ask
// AsRefusal first, which finds the refusal before anything it wraps.
func (r *Refusal) Unwrap() error { return r.cause }

func refuse(kind RefusalKind, format string, args ...any) error {
	return &Refusal{Kind: kind, Reason: fmt.Sprintf(format, args...)}
}

// refuseFrom is refuse, wrapping the API server's error it was made from.
func refuseFrom(cause error, kind RefusalKind, format string, args ...any) error {
	return &Refusal{Kind: kind, Reason: fmt.Sprintf(format, args...), cause: cause}
}

// AsRefusal reports whether an error is one of this package's refusals.
func AsRefusal(err error) (*Refusal, bool) {
	var refusal *Refusal
	if err == nil {
		return nil, false
	}
	if ok := asRefusal(err, &refusal); ok {
		return refusal, true
	}
	return nil, false
}

func asRefusal(err error, out **Refusal) bool {
	for err != nil {
		if refusal, ok := err.(*Refusal); ok {
			*out = refusal
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// conflictSentence is what an operator can act on when a save loses a race:
// not "409", but look again before deciding.
const conflictSentence = "this %s changed while you had it open — reload the panel and make the change again, so you are editing what is there now"

// --- the shared front half ----------------------------------------------------

// locate resolves the target and applies the guardrail.
//
// The read-only check is on the one path every write goes through, against
// the resolved context name: MakeDNSFriendly turns an EKS ARN into
// something that never matches KT_READONLY_CLUSTERS, and a guardrail that
// silently stops matching on the clusters it protects is worse than none.
func (w *Writer) locate(ctx context.Context, target Target) (Location, error) {
	where, err := w.resolve.Locate(ctx, target.Cluster, target.Kind)
	if err != nil {
		return Location{}, refuse(RefusedInput, "%s", textutil.FirstLineN(err.Error(), refusalLine))
	}
	if w.readOnly(where.Cluster) {
		return Location{}, refuse(RefusedReadOnly,
			"%s is marked read-only in KT_READONLY_CLUSTERS; changes are refused there",
			where.Cluster)
	}
	if target.Name == "" {
		return Location{}, refuse(RefusedInput, "no object name was given")
	}
	where.guarded = true
	where.door = w
	return where, nil
}

// live fetches the object a write is about to be built on.
func live(ctx context.Context, where Location, target Target) (*unstructured.Unstructured, error) {
	// A Location that did not come from locate() has not been through the
	// read-only guardrail: this makes the guard impossible to skip, not just
	// called by every path today.
	if !where.guarded {
		return nil, refuse(RefusedInput,
			"this write was built on a location that never went through the read-only "+
				"guardrail; call Locate rather than assembling a Location")
	}
	iface, err := resourceOf(where, target)
	if err != nil {
		return nil, err
	}
	// Part of the write, not a read of its own: every caller has already
	// asked the person's review (reviewBeforeReading, actions.Needs, a plan
	// Preview minted). Reviewing it again as a `get` would ask a question
	// the write's own review does not, and pages predict a write's questions
	// from the write's.
	return iface.Get(ctx, target.Name, metav1.GetOptions{})
}

// resourceOf is the endpoint a target lives at. Only live and the closures
// in door.go call it: it can write, and a write through it outside
// Gate.Perform is what internal/access's structural test refuses.
func resourceOf(where Location, target Target) (dynamic.ResourceInterface, error) {
	if !where.Namespaced {
		return where.Dynamic.Resource(where.Resource), nil
	}
	if target.Namespace == "" {
		return nil, refuse(RefusedInput, "%s is namespaced and no namespace was given", where.Resource.Resource)
	}
	return where.Dynamic.Resource(where.Resource).Namespace(target.Namespace), nil
}

// send patches, either as a dry run or for real.
//
// `DryRun: ["All"]` runs validation, admission webhooks and quota and changes
// nothing, so a patch a controller will refuse is refused with its own
// sentence before the operator is told it succeeded. Each call is one
// request, so a caller that already validated pays for no third.
//
// Reviewed first, dry run included: `patch` on this name for the person
// behind ctx. A dry run the person may not make previews nothing they can do.
func send(ctx context.Context, where Location, target Target, patch []byte, dryRun bool) (*unstructured.Unstructured, access.Decision, error) {
	options := metav1.PatchOptions{}
	if dryRun {
		options.DryRun = []string{metav1.DryRunAll}
	}
	var applied *unstructured.Unstructured
	decision, err := perform(ctx, where, target, "patch", "", dryRun, func(ctx context.Context) error {
		iface, err := resourceOf(where, target)
		if err != nil {
			return err
		}
		applied, err = iface.Patch(ctx, target.Name, types.MergePatchType, patch, options)
		if err != nil {
			err = onTheWire(ctx, changesObject, err)
			if dryRun {
				if incomplete := checkIncomplete(err); incomplete != nil {
					return incomplete
				}
			}
			return apiRefusal(err)
		}
		return nil
	})
	if err != nil {
		return nil, decision, err
	}
	return applied, decision, nil
}

// apiRefusal turns what the API said into what the panel says.
func apiRefusal(err error) error {
	// The guard's refusal first: it arrives as a transport error, and the
	// fallthrough would call a request the cluster never saw "refused".
	if unsent := asUnsent(err); unsent != nil {
		return unsent
	}
	// Nor what was never answered no (onTheWire): its audit record says
	// `held`, `timed-out` or `unanswered`.
	if outcome := notARefusal(err); outcome != nil {
		return outcome
	}
	if apierrors.IsConflict(err) {
		return refuseFrom(err, RefusedConflict, conflictSentence, "object")
	}
	// A 400 says the request itself was malformed (a manifest that does not
	// decode into its kind, an admission webhook's bad request): nobody's
	// rights said no, so it is the caller's input and a 400, never a 403.
	if apierrors.IsBadRequest(err) {
		return refuseFrom(err, RefusedInput, "the cluster refused the change: %s", strings.TrimSpace(err.Error()))
	}
	return refuseFrom(err, RefusedByAPI, "the cluster refused the change: %s", strings.TrimSpace(err.Error()))
}

// notARefusal is err as the sentence for a write the API server did not
// refuse (never sent, timed out, or unanswered), and nil otherwise. An
// eviction has sentences of its own (internal/actions).
func notARefusal(err error) error {
	if held, ok := AsNotSent(err); ok {
		return refuseFrom(err, RefusedNotSent, "the change was not sent: %s (%s). Nothing changed; make it again",
			held.At, textutil.FirstLineN(held.Err.Error(), refusalLine))
	}
	if timedOut, ok := AsTimedOut(err); ok {
		return refuseFrom(err, RefusedUnanswered, "the API server %s the change (%s), so it may "+
			"still happen; reload to see whether it did before making it again", timedOut.What(), timedOut.Answer())
	}
	if unanswered, ok := AsUnanswered(err); ok {
		return refuseFrom(err, RefusedUnanswered, "the change was sent and no answer came back (%s), so "+
			"whether it happened is not known; reload to see whether it did before making it again",
			textutil.FirstLineN(unanswered.Err.Error(), refusalLine))
	}
	return nil
}

// checkIncomplete is err as the sentence for a dry run that did not complete -
// never sent, unfinished, or with no answer back - and nil for anything else.
//
// Not "may still happen": a dry run changes nothing. Every create and
// patch runs one first, so this is what a person meets on a slow cluster.
// RefusedNotSent (a retryable 503 `not_sent`), never RefusedUnanswered,
// whose `not_answered` tells a client not to retry on its own. The audit
// record keeps what became of the request beside `dryRun: true`.
func checkIncomplete(err error) error {
	var why string
	if held, ok := AsNotSent(err); ok {
		why = held.At.String()
	} else if timedOut, ok := AsTimedOut(err); ok {
		why = "the API server " + timedOut.What() + " it, " + timedOut.Answer()
	} else if unanswered, ok := AsUnanswered(err); ok {
		why = "no answer came back: " + textutil.FirstLineN(unanswered.Err.Error(), refusalLine)
	} else {
		return nil
	}
	return refuseFrom(err, RefusedNotSent, "the check of this change - a dry run, which changes nothing - did "+
		"not complete (%s). Nothing changed; try again", why)
}

// asUnsent is err as RefusedUnsent when it is the access guard's refusal, and
// nil otherwise, in the guard's own sentence without client-go's
// `Patch "https://...":` prefix.
func asUnsent(err error) error {
	var unreviewed *access.Unreviewed
	if !errors.As(err, &unreviewed) {
		return nil
	}
	return refuseFrom(err, RefusedUnsent, "%s", textutil.FirstLineN(unreviewed.Error(), refusalLine))
}

// --- stage one: one key of a ConfigMap or a Secret ----------------------------

// KeyChange is one value, typed in the panel.
//
// Value is always plaintext, on a Secret too: the operator types the value
// and this package does the base64. Pasted base64 is unreadable, which is
// how the wrong one gets pasted.
type KeyChange struct {
	Target
	Key   string
	Value string
	// ResourceVersion is what the panel was drawn from.
	ResourceVersion string
}

// SetKey changes exactly one value.
//
// The patch carries that key and the precondition and nothing else: that is
// the whole blast radius, and a test pins its literal bytes.
func (w *Writer) SetKey(ctx context.Context, change KeyChange) (Applied, error) {
	ready, err := w.prepareKey(ctx, change)
	if err != nil {
		return Applied{}, err
	}

	// prepareKey has already dry-run this exact body; this is the commit.
	applied, decision, err := send(ctx, ready.where, change.Target, ready.body, false)
	if err != nil {
		return Applied{}, err
	}

	// `bytes=` and never the value: this is as often a Secret, and a log
	// line is where a credential would outlive the process. A refused or
	// failed commit writes its own line in door.go.
	w.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s key=%s bytes=%d decision=%s outcome=applied",
		ready.where.Cluster, ready.where.Resource.Resource, change.Namespace, change.Name, change.Key, len(change.Value), decision)

	return Applied{
		Target:          change.Target,
		ResourceVersion: applied.GetResourceVersion(),
		Fields:          []string{"data." + change.Key},
	}, nil
}

// KeyPreview is one value's change, as the operator confirms it before it is
// sent.
//
// For a ConfigMap it carries the before/after lines. For a Secret only sizes
// and line counts: the plaintext is already in the editor the dialog opened
// over, and a second copy on the most screenshot-prone surface is a
// decision nobody made.
type KeyPreview struct {
	Target
	// Title is the kind as a person writes it: "ConfigMap".
	Title string
	Key   string
	// Secret decides what may be shown. See the type comment.
	Secret bool
	// ResourceVersion is the version this was computed against, and what the
	// apply must be given back, so a dialog left open while somebody saves fails.
	ResourceVersion string
	// Value is the plaintext as typed, carried back so the commit re-derives
	// everything rather than trusting a payload that went through a browser.
	Value string

	// Diff is before → after, filled only when Secret is false.
	Diff    []DiffLine
	Added   int
	Removed int

	// Sizes: the whole recap on a Secret, context on a ConfigMap. Bytes are
	// of the plaintext; StoredBytes is what the API will hold (base64 on a
	// Secret, a third larger).
	BeforeBytes, AfterBytes int
	BeforeLines, AfterLines int
	StoredBytes             int

	// Unchanged says the typed value is byte-for-byte what is already there.
	//
	// Never on a Secret: type a guess, preview, and "unchanged" would read the
	// Secret one guess at a time with no audited reveal.
	Unchanged bool

	// Fields are the dotted paths the patch touches, named rather than
	// counted.
	Fields []string
	// Validated is what the dry run said.
	Validated string
}

// PreviewKey computes the change to one value and validates it, and commits
// nothing.
//
// Like PreviewManifest, the dry run means the confirmation shows a change
// the cluster has already agreed to accept.
func (w *Writer) PreviewKey(ctx context.Context, change KeyChange) (KeyPreview, error) {
	ready, err := w.prepareKey(ctx, change)
	if err != nil {
		return KeyPreview{}, err
	}

	preview := KeyPreview{
		Target:          change.Target,
		Title:           ready.where.Kind,
		Key:             change.Key,
		Secret:          ready.secret,
		ResourceVersion: ready.object.GetResourceVersion(),
		Value:           change.Value,
		BeforeBytes:     len(ready.before),
		AfterBytes:      len(change.Value),
		BeforeLines:     countLines(ready.before),
		AfterLines:      countLines(change.Value),
		StoredBytes:     len(ready.stage),
		Unchanged:       !ready.secret && ready.before == change.Value,
		Fields:          []string{"data." + change.Key, "metadata.resourceVersion"},
		Validated: fmt.Sprintf("The API server was asked to apply this as a dry run and accepted it. "+
			"One key is sent — data.%s — with metadata.resourceVersion as the precondition, and nothing else.",
			change.Key),
	}

	// On a Secret this would be a second copy of a credential.
	if !ready.secret {
		lines, stat := diffLines(ready.before, change.Value)
		preview.Diff, preview.Added, preview.Removed = lines, stat.Added, stat.Removed
	}
	return preview, nil
}

// countLines is how many lines a value has, 0 for an empty one: the count
// internal/detail puts beside the key.
func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

// preparedKey is everything the preview and the commit need, computed once.
type preparedKey struct {
	where  Location
	object *unstructured.Unstructured
	secret bool
	// before is the current value as plaintext: decoded on a Secret, as stored on
	// a ConfigMap.
	before string
	// stage is what goes into the patch: base64 on a Secret, the plaintext on a
	// ConfigMap.
	stage string
	body  []byte
}

// prepareKey does every check there is, builds the payload, and validates it.
//
// The only path to a key write: preview and commit both run all of it
// (guardrail, key, precondition, dry run), because a stale browser tab can
// carry back anything carried across the confirmation.
func (w *Writer) prepareKey(ctx context.Context, change KeyChange) (*preparedKey, error) {
	where, err := w.locate(ctx, change.Target)
	if err != nil {
		return nil, err
	}
	if !isDataKind(where.Kind) {
		return nil, refuse(RefusedInput,
			"only a ConfigMap or a Secret has values that can be edited here; %s does not", where.Kind)
	}
	if change.Key == "" {
		return nil, refuse(RefusedInput, "no key was given")
	}

	// Reviewed before the object is read: every check below answers from it.
	if err := reviewBeforeReading(ctx, where, change.Target); err != nil {
		return nil, err
	}

	object, err := live(ctx, where, change.Target)
	if err != nil {
		return nil, wrapGet(err, where.Kind)
	}
	secret := strings.EqualFold(where.Kind, "Secret")

	if immutable, _, _ := unstructured.NestedBool(object.Object, "immutable"); immutable {
		return nil, refuse(RefusedImmutable,
			"%s %s is declared immutable: true, so the API refuses every change to its data. "+
				"An immutable object has to be replaced, not edited.", where.Kind, change.Name)
	}

	// The key has to exist: adding and deleting keys is not offered (see
	// SetKey on blast radius), and a typo would otherwise create a second key.
	stored, found, err := unstructured.NestedString(object.Object, "data", change.Key)
	if err != nil || !found {
		if _, binary, _ := unstructured.NestedString(object.Object, "binaryData", change.Key); binary {
			return nil, refuse(RefusedInput,
				"%q is binary data; it is not editable here", change.Key)
		}
		return nil, refuse(RefusedInput,
			"%s %s has no key %q. This panel changes values that are already there; "+
				"adding and removing keys is not offered.", where.Kind, change.Name, change.Key)
	}
	if !textValue(stored, secret) {
		return nil, refuse(RefusedInput,
			"the current value of %q is not text; editing it here would corrupt it", change.Key)
	}

	// An absent resourceVersion is refused: the patch below puts the version
	// this call just read in the precondition, so without the check here the
	// API server could not refuse a stale save either, and whatever landed
	// while the panel was open would be overwritten silently. The manifest
	// path refuses it the same way.
	if change.ResourceVersion == "" {
		return nil, refuse(RefusedInput,
			"this editor was opened without a resourceVersion, so a save could not be made safe. Reload the panel")
	}
	if object.GetResourceVersion() != change.ResourceVersion {
		return nil, refuse(RefusedConflict, conflictSentence, strings.ToLower(where.Kind))
	}

	// The current value as the operator sees it: decoded on a Secret,
	// verbatim on a ConfigMap (textValue established that it decodes).
	current, _ := decode(stored, secret)

	stage := change.Value
	if secret {
		stage = encode(change.Value)
	}

	// The whole payload: a field added here would be one the operator never
	// saw and never asked to change.
	patch := map[string]any{
		"metadata": map[string]any{"resourceVersion": object.GetResourceVersion()},
		"data":     map[string]any{change.Key: stage},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}

	// Dry run first: a 1 MiB ceiling or an admission webhook answers before
	// anything is stored.
	if _, _, err := send(ctx, where, change.Target, body, true); err != nil {
		return nil, err
	}

	return &preparedKey{
		where: where, object: object,
		secret: secret, before: string(current), stage: stage, body: body,
	}, nil
}

func isDataKind(kind string) bool {
	return strings.EqualFold(kind, "ConfigMap") || strings.EqualFold(kind, "Secret")
}

// wrapGet keeps a "not found" readable rather than letting it arrive as an
// unexplained failure of a save.
func wrapGet(err error, kind string) error {
	if apierrors.IsNotFound(err) {
		return refuse(RefusedInput, "this %s no longer exists", strings.ToLower(kind))
	}
	return err
}

// refusalLine bounds the console's own sentences about a write not answered
// with a refusal (never sent, never answered, held by the guard).
//
// The cluster's own refusal is not bounded (apiRefusal): a validation answer
// names every field it refused, the explaining one often last, so it
// reaches the person whole.
const refusalLine = 300
