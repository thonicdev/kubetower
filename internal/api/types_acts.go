package api

import (
	"time"
)

// --- Edit -------------------------------------------------------------------

// DataEntry is one key of a ConfigMap or a Secret.
//
// Value is a pointer so that an empty value and no value differ on the wire:
//
//	nil      nothing to draw: Masked (a Secret's key, revealed through the one
//	         door), Binary (not text), or a ConfigMap's binaryData key, which
//	         this panel does not decode.
//	non-nil  the value, and `""` is a value.
type DataEntry struct {
	Key    string  `json:"key"`
	Value  *string `json:"value,omitempty"`
	Masked bool    `json:"masked"`
	Binary *bool   `json:"binary,omitempty"`
}

// DataPanel is the editable data of one object, masked.
type DataPanel struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// ResourceVersion is carried back on the write as a precondition, so two
	// tabs cannot overwrite each other.
	ResourceVersion string      `json:"resourceVersion"`
	Entries         []DataEntry `json:"entries"`
	// Carried as false rather than omitted, for the reason on ManifestPanel.
	Editable bool `json:"editable"`
	// EditableNote says which cause refused the write, since each is someone
	// else's to fix: an immutable object, a cluster guarded by
	// KT_READONLY_CLUSTERS, or a ServiceAccount not granted patch.
	EditableNote string `json:"editableNote,omitempty"`
}

// DataWriteEntry is one key being written.
type DataWriteEntry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Delete bool   `json:"delete,omitempty"`
}

// DataWrite is the submitted data.
type DataWrite struct {
	Kind            string           `json:"kind"`
	Namespace       string           `json:"namespace,omitempty"`
	Name            string           `json:"name"`
	ResourceVersion string           `json:"resourceVersion"`
	Entries         []DataWriteEntry `json:"entries"`
}

// ManifestToken is coloring done server-side and rendered client-side. The API
// returns tokens, never HTML, so it ships no injection surface.
type ManifestToken struct {
	Text  string `json:"text"`
	Class string `json:"class"`
}

// ManifestLine is one numbered line of tokens.
type ManifestLine struct {
	Number int             `json:"number"`
	Tokens []ManifestToken `json:"tokens"`
}

// ManifestPanel is the object's manifest.
type ManifestPanel struct {
	Lines []ManifestLine `json:"lines"`
	// Raw is the document for the editor, Secret values masked by
	// detail.Trimmed, which is also the base of the edit diff, so a mask cannot
	// be written back over a value.
	Raw string `json:"raw,omitempty"`
	// MaskedKeys are a Secret's text `data` keys whose value in Raw is the mask,
	// in the Data tab's order; empty on any other kind. The editor reveals each
	// through the one door, one request (and one audit line) per key. Binary
	// values are left out: the door reveals text only. The server lists them so
	// the masking rule has one copy.
	MaskedKeys      []string `json:"maskedKeys,omitempty"`
	ResourceVersion string   `json:"resourceVersion,omitempty"`
	// Editable carries `false` rather than omitting it, so "cannot be edited"
	// never reads as "never said".
	Editable bool `json:"editable"`
	// EditableNote is which setting refuses the write, when one does.
	// `editable: false` with a note draws the control disabled with the note as
	// its reason; with no note the person may not patch at all, and no control
	// is drawn.
	EditableNote string `json:"editableNote,omitempty"`
}

// ManifestWrite is the submitted document. The kind that is authorized is read
// out of this, after parsing — never from a route or from a sibling field.
type ManifestWrite struct {
	Document        string `json:"document"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// CreateTemplate is what the list's "+" opens: a document for a new object of
// one kind, `apiVersion` and `kind` resolved by the server from the list's slug.
type CreateTemplate struct {
	// Document is the YAML for the editor, ending in a newline.
	Document string `json:"document"`
	// Kind is the API's own spelling, "Deployment", for the editor's title.
	Kind string `json:"kind"`
	// Generic is true where the kind has no written template and Document is
	// the skeleton of apiVersion, kind and metadata.
	Generic bool `json:"generic,omitempty"`
}

// CreateTemplateQuery is what getCreateTemplate was asked: a kind as the
// list's slug spells it, and the namespace in view.
type CreateTemplateQuery struct {
	Cluster   string
	Kind      string
	Namespace string
}

// --- Confirm-then-act -------------------------------------------------------

// PlanTarget is one object an act would touch.
type PlanTarget struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Note      string `json:"note,omitempty"`
	// Refused is set on a selection's preview for a target the person may not
	// act on; Note then carries the refusal's reason.
	Refused bool `json:"refused,omitempty"`
}

// Confirmation is how the operator is asked to agree, carried so that the front
// end cannot quietly choose an easier gesture.
type Confirmation struct {
	Kind     string `json:"kind"`
	Expected string `json:"expected,omitempty"`
	// Label is the sentence above the box, giving the reason for what is typed.
	Label string `json:"label,omitempty"`
	// Value, Minimum and Maximum belong to the `number` gesture, where what is
	// typed is the value the act needs. Pointers, because zero is a meaningful
	// replica count, floor and ceiling.
	Value   *int64 `json:"value,omitempty"`
	Minimum *int64 `json:"minimum,omitempty"`
	Maximum *int64 `json:"maximum,omitempty"`
}

// PlanFacts is the structured truth about what would happen, and the
// authority over any sentence.
type PlanFacts struct {
	Verb    string       `json:"verb"`
	Targets []PlanTarget `json:"targets"`
	// Count is the number of targets the server resolved, which a client
	// compares with its own selection.
	Count        int   `json:"count"`
	Destructive  bool  `json:"destructive"`
	Irreversible *bool `json:"irreversible,omitempty"`
	// Explanation, Warning and Button are the verb's own words per (verb, kind),
	// from `internal/web`'s `actionSpec`, so each act's confirmation reads
	// differently. The server writes them; a plan with no verb spec carries none.
	// The dialog folds Explanation and Warning under "Details" and leads with
	// Headline.
	Explanation string `json:"explanation,omitempty"`
	// Warning is a property of the verb; Warnings are read off this object, such
	// as a pod a drain would leave alone.
	Warning string `json:"warning,omitempty"`
	// Button names the act and, on a plan about one object, the object:
	// "Delete api-0", "Run nightly now".
	Button   string   `json:"button,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Headline is the line a single verb's plan leads with ("Deletes pod api-0.
	// ReplicaSet api-7f9c creates a new one."), composed from what the preview
	// read; `sentenceFor` returns it as the plan's sentence. Absent with no verb
	// spec. On a selection it is the set's sentence. Not compared by sameFacts:
	// a change in what it reads is the precondition's to catch.
	Headline string `json:"headline,omitempty"`
	// Recap is the change as fields, keeping `absent` and `unchanged`, which a
	// sentence would drop.
	Recap *PlanRecap `json:"recap,omitempty"`
	Diff  string     `json:"diff,omitempty"`
	// Fields are the paths the merge patch touches: what the request asks for,
	// which a diff of a reindented block does not show.
	Fields []string `json:"fields,omitempty"`
	// Request is the literal body that will be sent, not a second marshalling
	// of the same value: a confirmation cannot show one thing and send another.
	Request string `json:"request,omitempty"`
	// Precondition is the value the act is pinned to, stated so a 409 reads as
	// the guard working.
	Precondition string `json:"precondition,omitempty"`
	// PreconditionOn names what Precondition is a value of. Absent means
	// `metadata.resourceVersion`; a verb on a node is pinned to
	// `spec.unschedulable and spec.taints`, and on a workload to
	// `metadata.generation`, so a status write does not refuse the act.
	PreconditionOn string `json:"preconditionOn,omitempty"`
	// PreconditionUID is the `metadata.uid` the act is also pinned to: a
	// recreated object restarts its generation and a re-registered node keeps
	// its taints, so the value alone cannot tell an object from its successor.
	// A delete sends it as its UID precondition.
	PreconditionUID string        `json:"preconditionUid,omitempty"`
	Confirmation    *Confirmation `json:"confirmation,omitempty"`
	// Alternative is another plan the operator may ask for instead, such as a
	// drain that also evicts pods nothing owns. Not compared by sameFacts: it is
	// an offer, not part of what is agreed to.
	Alternative *PlanAlternative `json:"alternative,omitempty"`
	// Forceable says the act may carry ActRequest's `force` (grace period zero).
	// A single delete's plan only; runAction refuses `force` without it. Not
	// compared by sameFacts: a delete's re-derived plan always says it.
	Forceable bool `json:"forceable,omitempty"`
}

// PlanAlternative is a plan the operator may ask for in place of this one.
type PlanAlternative struct {
	Label   string             `json:"label"`
	Request AlternativeRequest `json:"request"`
}

// AlternativeRequest is an ActionRequest as a plan offers it: only the members
// a client may send, so a server-held precondition or stage cannot be put in an
// offer and silently lost in the plan store's JSON.
type AlternativeRequest struct {
	Verb           string     `json:"verb"`
	Target         PlanTarget `json:"target"`
	IncludeUnowned bool       `json:"includeUnowned,omitempty"`
}

// PlanRecap is the one field a verb will change, before and after.
//
//   - Absent: the field is not written on the object; Before carries the value
//     in effect.
//   - Unchanged: the act is a no-op, such as uncordoning a schedulable node,
//     and must not be confirmed as a change.
type PlanRecap struct {
	Field  string `json:"field,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Absent and Unchanged are plain bools: false is the ordinary case.
	Absent    bool `json:"absent,omitempty"`
	Unchanged bool `json:"unchanged,omitempty"`
	// Note is the sentence for a verb where neither a field change nor a target
	// set describes what happens — a delete, an evict, a restart.
	Note string `json:"note,omitempty"`
}

// ConfirmationPlan is what a preview returns. `Sentence` is optional in the
// contract; this server fills it, so it stays the author of what the operator
// agrees to (plans.go).
type ConfirmationPlan struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Sentence  *string   `json:"sentence,omitempty"`
	Facts     PlanFacts `json:"facts"`
	// Channel is where the act happens when it is not an operation in this
	// document — the node shell's WebSocket path.
	Channel string `json:"channel,omitempty"`
}

// ActRequest is the act half, bound to the plan by its token.
type ActRequest struct {
	Token     string `json:"token"`
	Confirmed string `json:"confirmed,omitempty"`
	// Force is the "Force delete" box: grace period zero. Accepted only against
	// a plan that says `forceable`, refused before the token is spent elsewhere.
	Force bool `json:"force,omitempty"`
}

// ApplyRequest is the same with no gesture.
type ApplyRequest struct {
	Token string `json:"token"`
}

// ActionRequest previews one verb on one object. A replica count arrives as the
// `number` gesture at the act, not here.
type ActionRequest struct {
	Verb   string     `json:"verb"`
	Target PlanTarget `json:"target"`
	// Precondition is the pin the act carries (`PlanFacts.Precondition`). The
	// handler fills it from the agreed facts; off the wire, so a body naming one
	// is refused as an unknown member: a client-chosen pin is no pin.
	Precondition string `json:"-"`
	// PreconditionUID is `PlanFacts.PreconditionUID`, server-held for the same
	// reason.
	PreconditionUID string `json:"-"`
	// Stage is which step of a staged verb this is, server-held so a client
	// cannot skip a stage: a drain's eviction is reachable only through the plan
	// minted from its cordon's outcome. Empty is the first stage; the values are
	// the backend's (`actions.StageEvict`).
	Stage string `json:"-"`
	// IncludeUnowned is a drain's `--force`: also evict pods nothing owns. The
	// operator's choice, sent from a plan's Alternative; refused on any verb but
	// drain. It carries on to the eviction stage.
	IncludeUnowned bool `json:"includeUnowned,omitempty"`
	// Force is the act's `ActRequest.Force`, set by runAction after the plan
	// said `forceable`. Off the wire: it is chosen at the click.
	Force bool `json:"-"`
}

// BulkRequest previews one verb on a selection.
type BulkRequest struct {
	Verb    string       `json:"verb"`
	Kind    string       `json:"kind"`
	Targets []PlanTarget `json:"targets"`
}

// NodeShellRequest previews opening a shell on a node.
type NodeShellRequest struct {
	Node string `json:"node"`
}

// ActResult is one target's outcome.
type ActResult struct {
	Target  PlanTarget `json:"target"`
	OK      bool       `json:"ok"`
	Message string     `json:"message,omitempty"`
	Reason  *Reason    `json:"reason,omitempty"`
}

// ActOutcome is per target, always: a bulk action that half worked is the
// normal case.
type ActOutcome struct {
	Succeeded int         `json:"succeeded"`
	Failed    int         `json:"failed"`
	Results   []ActResult `json:"results"`
	// Summary is the sentence the act left behind, composed by the backend so it
	// agrees with the figures, and says what a count cannot (a drain's cordon
	// has evicted nothing yet).
	Summary string `json:"summary,omitempty"`
	// Next is where to go now that it has happened: the object a create made.
	Next []Ref `json:"next,omitempty"`
	// NextPlan is the plan for the next stage, such as a drain's eviction after
	// its cordon, confirmed on its own like any plan. The handler mints it from
	// NextRequest through the ordinary preview path, so the plan the operator
	// reads is the one the act re-derives.
	NextPlan *ConfirmationPlan `json:"nextPlan,omitempty"`
	// NextRequest is what the backend hands the handler to mint NextPlan from.
	// Off the wire: a client is given the plan, never the request behind it.
	NextRequest *ActionRequest `json:"-"`
	// Alternative is a plan the operator may ask for now this act is done,
	// where there is no next stage, such as a drain including the unowned pods a
	// cordon left. An offer only, as in PlanFacts.Alternative.
	Alternative *PlanAlternative `json:"alternative,omitempty"`
}
