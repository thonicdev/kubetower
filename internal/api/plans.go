package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// Confirm-then-act.
//
// A preview returns the sentence the operator reads before a destructive
// action, composed here from the facts: a phrasing bug in the client would
// confirm the wrong thing against correct facts, out of reach of Go tests.
// `sentence` stays optional in the schema; what holds either way:
//
//  1. `facts` is always present and is the authority; the sentence is derived
//     from it, so the two cannot disagree.
//  2. A client that receives a sentence renders it rather than composing one.
//  3. The act is bound to the plan by `token`, and the server re-derives the
//     facts before acting: a cluster that changed between preview and click
//     gets a 409 with a fresh plan instead of an act nobody confirmed.

// planLife is how long a plan is good for: short, because it is a promise about
// a changing cluster, long enough to read and decide. The re-derivation at the
// act makes the exact value unimportant.
const planLife = 2 * time.Minute

// planKind says which pair a plan belongs to, so a token issued for a manifest
// cannot be spent on a bulk delete.
type planKind string

const (
	planAction    planKind = "action"
	planBulk      planKind = "bulk"
	planData      planKind = "data"
	planManifest  planKind = "manifest"
	planCreate    planKind = "create"
	planNodeShell planKind = "nodeshell"
	planClear     planKind = "clear"
	planPrereq    planKind = "prereq"
)

// plan is what was agreed to, and everything needed to re-derive it.
type plan struct {
	kind      planKind
	cluster   string
	expiresAt time.Time
	facts     PlanFacts

	// Exactly one of these is set, matching kind. The request is kept, not only
	// its facts, because the act re-runs the preview from it. manifest serves
	// both planManifest and planCreate.
	action   *ActionRequest
	bulk     *BulkRequest
	data     *DataWrite
	manifest *ManifestWrite
	clear    string
	// prereq is the key of the prerequisite a planPrereq installs.
	prereq string
}

// planStore holds the plans between a preview and its act.
//
// It lives in the shared store because the preview and the confirmation may
// land on two replicas; with one replica the store is in memory. A plan carries
// the request it was minted from (a manifest, a Secret's values), so in Valkey
// it is sealed (sharedstate.Sealed): the store alone neither reveals it nor
// can forge one.
type planStore struct {
	now func() time.Time
	kv  sharedstate.Store

	// What this replica has put in the store and not seen spent, by token.
	mu     sync.Mutex
	held   map[string]heldPlan
	limits planCeiling
}

// planCeiling bounds how much of the shared store this replica's plans may
// take, and one person's share of it.
//
// A plan holds its request (a manifest up to the schema's four megabytes) for
// planLife, beside the sessions, so without a bound one person previewing in a
// loop could fill the store everyone's session lives in.
//
// It is counted by the issuing replica in its own memory, not in the store,
// to avoid a shared write per preview: the store holds at most replicas x
// plans and replicas x bytes. A plan is released when spent here or when it
// expires; one spent on another replica stays counted until it lapses, which
// errs on the side of refusing. Bytes are the encoding before sealing.
//
// Shares apply under the server profile only (on the desktop there is one
// person), and never to the shared password account, whose name is everybody
// holding the password (see terminal.PerPersonSessions). The ceilings sit far
// above what clicking produces.
type planCeiling struct {
	plans, bytes                   int
	perPersonPlans, perPersonBytes int
}

// The defaults; New sets the share under --incluster.
const (
	maxPlans       = 512
	maxPlanBytes   = 64 << 20
	perPersonPlans = 32
	perPersonBytes = 16 << 20
)

// heldPlan is one plan this replica issued: whose it is, whether it counts
// against their share, its size and when it lapses.
type heldPlan struct {
	who     string
	share   bool
	size    int
	expires time.Time
}

// planRefused is a plan the ceiling would not store: a 409 `limit_reached`,
// the contract's answer for a full console-wide limit, with the sentence
// alone as its text.
type planRefused struct{ sentence string }

func (r *planRefused) Error() string        { return r.sentence }
func (r *planRefused) Is(target error) bool { return target == ErrLimitReached }

// planPrefix is where plans live in the store, beside the sessions.
const planPrefix = "plan:"

func newPlanStore(now func() time.Time, kv sharedstate.Store) *planStore {
	if kv == nil {
		kv = sharedstate.NewMemory(now)
	}
	return &planStore{now: now, kv: kv, held: map[string]heldPlan{},
		limits: planCeiling{plans: maxPlans, bytes: maxPlanBytes}}
}

// shared gives each person a share of the ceiling, under the server profile.
func (s *planStore) shared() *planStore {
	s.limits.perPersonPlans, s.limits.perPersonBytes = perPersonPlans, perPersonBytes
	return s
}

// hold counts a plan against the ceiling before it is stored, or says why it
// cannot be. The console's ceiling and a person's share have distinct
// sentences, and each says what frees a place: a plan lapses two minutes after
// its preview, and confirming one spends it.
func (s *planStore) hold(ctx context.Context, token string, size int, expires time.Time) error {
	who, _ := identity.From(ctx)
	share := s.limits.perPersonPlans > 0 && !who.Shared()
	key := who.Key()

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var plans, bytes, mine, mineBytes int
	for t, h := range s.held {
		if !now.Before(h.expires) {
			delete(s.held, t)
			continue
		}
		plans++
		bytes += h.size
		if share && h.share && h.who == key {
			mine++
			mineBytes += h.size
		}
	}
	const lapse = "; each lapses two minutes after its preview, or when it is confirmed - try again in a moment"
	switch {
	case plans >= s.limits.plans:
		return &planRefused{fmt.Sprintf("%s already waiting on this console, which is its limit, so this "+
			"preview was not kept"+lapse, confirmations(plans, "is", "are"))}
	case bytes+size > s.limits.bytes:
		return &planRefused{fmt.Sprintf("the confirmations waiting on this console already hold %d MiB, "+
			"and this one would take it past its limit of %d MiB, so this preview was not kept"+lapse,
			bytes>>20, s.limits.bytes>>20)}
	case share && mine >= s.limits.perPersonPlans:
		return &planRefused{fmt.Sprintf("you already have %s waiting, which is one person's share of this "+
			"shared console, so this preview was not kept"+lapse, confirmations(mine, "", ""))}
	case share && mineBytes+size > s.limits.perPersonBytes:
		return &planRefused{fmt.Sprintf("your confirmations waiting already hold %d MiB, and this one would "+
			"take them past one person's share of this shared console, %d MiB, so this preview was not kept"+
			lapse, mineBytes>>20, s.limits.perPersonBytes>>20)}
	}
	s.held[token] = heldPlan{who: key, share: share, size: size, expires: expires}
	return nil
}

// confirmations counts plans in the sentence's own words, with the verb that
// follows the count where there is one: "1 confirmation is", "3 are".
func confirmations(n int, one, many string) string {
	text := strconv.Itoa(n) + " confirmation"
	verb := one
	if n != 1 {
		text, verb = text+"s", many
	}
	if verb == "" {
		return text
	}
	return text + " " + verb
}

// release forgets a plan's place, once it is spent or could not be stored. A
// token this replica never issued is not an error.
func (s *planStore) release(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.held, token)
}

// planRecord is a plan as the store holds it, every field included. An
// ActionRequest's precondition and stage are `json:"-"` because a client must
// not supply them, yet the act re-derives against them, so they are copied out
// by name here.
type planRecord struct {
	Kind      planKind  `json:"kind"`
	Cluster   string    `json:"cluster"`
	ExpiresAt time.Time `json:"expiresAt"`
	Facts     PlanFacts `json:"facts"`

	Action                *ActionRequest `json:"action,omitempty"`
	ActionPrecondition    string         `json:"actionPrecondition,omitempty"`
	ActionPreconditionUID string         `json:"actionPreconditionUid,omitempty"`
	ActionStage           string         `json:"actionStage,omitempty"`
	Bulk                  *BulkRequest   `json:"bulk,omitempty"`
	Data                  *DataWrite     `json:"data,omitempty"`
	Manifest              *ManifestWrite `json:"manifest,omitempty"`
	Clear                 string         `json:"clear,omitempty"`
	Prereq                string         `json:"prereq,omitempty"`
}

func recordOf(p *plan) planRecord {
	r := planRecord{Kind: p.kind, Cluster: p.cluster, ExpiresAt: p.expiresAt, Facts: p.facts,
		Action: p.action, Bulk: p.bulk, Data: p.data, Manifest: p.manifest, Clear: p.clear, Prereq: p.prereq}
	if p.action != nil {
		r.ActionPrecondition, r.ActionStage = p.action.Precondition, p.action.Stage
		r.ActionPreconditionUID = p.action.PreconditionUID
	}
	return r
}

func (r planRecord) plan() *plan {
	p := &plan{kind: r.Kind, cluster: r.Cluster, expiresAt: r.ExpiresAt, facts: r.Facts,
		action: r.Action, bulk: r.Bulk, data: r.Data, manifest: r.Manifest, clear: r.Clear, prereq: r.Prereq}
	if p.action != nil {
		p.action.Precondition, p.action.Stage = r.ActionPrecondition, r.ActionStage
		p.action.PreconditionUID = r.ActionPreconditionUID
	}
	return p
}

// issue mints an opaque, single-use, short-lived token.
func (s *planStore) issue(ctx context.Context, p *plan) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("a plan token could not be minted: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	p.expiresAt = s.now().Add(planLife)

	body, err := json.Marshal(recordOf(p))
	if err != nil {
		return "", fmt.Errorf("a plan could not be encoded: %w", err)
	}
	if err := s.hold(ctx, token, len(body), p.expiresAt); err != nil {
		return "", err
	}
	if err := s.kv.Put(ctx, planPrefix+token, body, p.expiresAt); err != nil {
		s.release(token)
		return "", err
	}
	return token, nil
}

// take spends a token. It is gone whether or not the act succeeds, and in the
// same step it is read, so a confirmation sent twice to two replicas acts once.
// An error means the store did not answer, which the caller reports as such
// rather than as an expired plan.
func (s *planStore) take(ctx context.Context, token string, kind planKind) (*plan, bool, error) {
	body, ok, err := s.kv.Take(ctx, planPrefix+token)
	if err == nil {
		// Gone from the store, so gone from the ceiling, lapsed or not.
		s.release(token)
	}
	if err != nil || !ok {
		return nil, false, err
	}
	return s.open(body, kind)
}

// peek reads a plan without spending it, to check the gesture first: a refused
// gesture agreed to nothing and must not use the plan up, or the corrected
// value would get "already spent" with no fresh plan. The act still goes
// through take, which keeps a plan single-use.
func (s *planStore) peek(ctx context.Context, token string, kind planKind) (*plan, bool, error) {
	body, ok, err := s.kv.Get(ctx, planPrefix+token)
	if err != nil || !ok {
		return nil, false, err
	}
	return s.open(body, kind)
}

// open reads a stored plan, answering "no such plan" for another kind or one
// past its expiry.
func (s *planStore) open(body []byte, kind planKind) (*plan, bool, error) {
	var r planRecord
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, false, nil
	}
	if r.Kind != kind || !s.now().Before(r.ExpiresAt) {
		return nil, false, nil
	}
	return r.plan(), true, nil
}

// whyChanged is the sentence on the 409. When the pin moved it names the
// values, since "no longer what the plan described" alone would not tell a
// vanished target from an added taint. The pin is what the act depends on
// (`actions.PinOf`), so a node's status write does not trigger it.
//
// A different UID is said first: a recreated object's generation restarts at 1
// and can match on both sides.
func whyChanged(agreed, now PlanFacts, otherwise string) string {
	if agreed.PreconditionUID != "" && agreed.PreconditionUID != now.PreconditionUID {
		return fmt.Sprintf("the object changed while you had it open: it was deleted and created again "+
			"under the same name, so it is not the object the plan was made on - its metadata.uid was %s "+
			"and is %s now. Read the fresh plan and confirm it again", agreed.PreconditionUID, now.PreconditionUID)
	}
	if agreed.Precondition != "" && agreed.Precondition != now.Precondition {
		on := preconditionOn(agreed)
		verb, nowVerb := "was", "is"
		if strings.Contains(on, " and ") {
			verb, nowVerb = "were", "are"
		}
		return fmt.Sprintf("the object changed since the plan was made: its %s %s %s and %s %s now. "+
			"Read the fresh plan and confirm it again", on, verb, agreed.Precondition, nowVerb, now.Precondition)
	}
	return otherwise
}

// preconditionOn is what a plan's precondition is a value of; empty means the
// resourceVersion.
func preconditionOn(f PlanFacts) string {
	if f.PreconditionOn == "" {
		return "metadata.resourceVersion"
	}
	return f.PreconditionOn
}

// planFrom assembles the response for a preview.
func (a *API) planFrom(ctx context.Context, p *plan) (ConfirmationPlan, error) {
	token, err := a.plans.issue(ctx, p)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	out := ConfirmationPlan{
		Token:     token,
		ExpiresAt: p.expiresAt,
		Facts:     p.facts,
		Sentence:  strPtr(sentenceFor(p.facts)),
	}
	if p.kind == planNodeShell {
		// Escaped: an EKS context is an ARN holding `/`. net/http decodes it once
		// on the way back in.
		out.Channel = "/api" + ClusterPath(p.cluster) + "/ws/nodeshell"
	}
	return out, nil
}

// sentenceFor composes what the operator reads, from the facts alone, so it
// cannot describe a different set from the one `facts` names. A plan's
// Headline (a single verb's line read off the object, such as who owns it) is
// part of the facts and wins when present.
func sentenceFor(f PlanFacts) string {
	if f.Headline != "" {
		return f.Headline
	}
	verb := f.Verb
	if verb == "" {
		verb = "act on"
	}
	switch {
	case f.Count == 0:
		return fmt.Sprintf("Nothing matches: %s would touch no object.", verb)
	case f.Count == 1 && len(f.Targets) == 1:
		t := f.Targets[0]
		where := t.Name
		if t.Namespace != "" {
			where = t.Namespace + "/" + t.Name
		}
		return fmt.Sprintf("%s %s %s.", strings.ToUpper(verb[:1])+verb[1:], KindWord(t.Kind, 1), where)
	default:
		kind := "object"
		if len(f.Targets) > 0 {
			kind = f.Targets[0].Kind
		}
		return fmt.Sprintf("%s %d %s. This cannot be undone.",
			strings.ToUpper(verb[:1])+verb[1:], f.Count, KindWord(kind, f.Count))
	}
}

// KindWord names a kind as a sentence needs it: one "pod", several "pods".
//
// `PlanTarget.Kind` is the plural resource slug of the URL, which would read
// "Delete pods shop/api-0." as is. The console's display names live in
// `internal/resources`, which this package must not reach (it owns the
// contract only, see backend.go) and which holds no singular form anyway, so
// the word is derived from the slug by rule. A two-word Title therefore reads
// as `configmap` here. Exported so `internal/web` uses the same word.
func KindWord(kind string, count int) string {
	if count == 1 {
		return singular(kind)
	}
	return plural(kind)
}

// singular turns a plural resource slug back into one object's word. The arms
// cover the shapes the slugs in `internal/resources` take, not English in
// general: `networkpolicies`, `ingresses`, `priorityclasses`, `endpointslices`.
func singular(kind string) string {
	if kind == "" {
		return "object"
	}
	if qualified(kind) {
		return kind
	}
	switch {
	case strings.HasSuffix(kind, "ies"):
		return strings.TrimSuffix(kind, "ies") + "y"
	case strings.HasSuffix(kind, "sses"), strings.HasSuffix(kind, "shes"),
		strings.HasSuffix(kind, "ches"), strings.HasSuffix(kind, "xes"):
		return strings.TrimSuffix(kind, "es")
	case strings.HasSuffix(kind, "ss"):
		// Already singular, such as `ingress`: trimming would make a non-word.
		return kind
	case strings.HasSuffix(kind, "s"):
		return strings.TrimSuffix(kind, "s")
	}
	return kind
}

func plural(kind string) string {
	if kind == "" {
		return "objects"
	}
	if qualified(kind) {
		return kind
	}
	if strings.HasSuffix(kind, "s") {
		return kind
	}
	return kind + "s"
}

// qualified reports a slug carrying its group, such as
// `applicationsets.argoproj.io`: neither plural nor singular may touch it, or
// it would read `argoproj.ios` or `argoproj.i`.
func qualified(kind string) bool { return strings.Contains(kind, ".") }

// sameFacts reports whether a re-derived plan still describes what was agreed:
// verb, count, destructiveness, targets, precondition and UID. Reworded
// warnings are not a different act.
//
// The precondition makes a change of state count, not only a change of set;
// it is the pin (what the act depends on), so heartbeats and status writes do
// not count. A pin on a different field also differs, so two binaries sharing
// a plan store across an upgrade refuse rather than compare a generation with
// a resourceVersion. The UID catches a recreated object whose generation
// restarted. Plans with no precondition or UID (bulk runs, a drain's second
// stage) compare equal on them.
func sameFacts(agreed, now PlanFacts) bool {
	if agreed.Verb != now.Verb || agreed.Count != now.Count || agreed.Destructive != now.Destructive {
		return false
	}
	if agreed.Precondition != now.Precondition || preconditionOn(agreed) != preconditionOn(now) {
		return false
	}
	if agreed.PreconditionUID != now.PreconditionUID {
		return false
	}
	if len(agreed.Targets) != len(now.Targets) {
		return false
	}
	key := func(t PlanTarget) string { return t.Kind + "\x00" + t.Namespace + "\x00" + t.Name }
	left := make([]string, 0, len(agreed.Targets))
	right := make([]string, 0, len(now.Targets))
	for _, t := range agreed.Targets {
		left = append(left, key(t))
	}
	for _, t := range now.Targets {
		right = append(right, key(t))
	}
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// gestureSatisfied checks what the operator typed against what the plan asked
// for. The plan carries the gesture so the front end cannot pick an easier one.
func gestureSatisfied(f PlanFacts, confirmed string) (bool, string) {
	if f.Confirmation == nil {
		return true, ""
	}
	switch f.Confirmation.Kind {
	case "acknowledge":
		return true, ""
	case "count", "name":
		if strings.TrimSpace(confirmed) == f.Confirmation.Expected {
			return true, ""
		}
		return false, fmt.Sprintf("this plan is confirmed by %s, and %q is not it",
			f.Confirmation.Kind, confirmed)
	case "number":
		return numberSatisfied(f.Confirmation, confirmed)
	}
	// Fail closed: the plan chose this kind, so an unknown one is the server
	// asking for a check it cannot make.
	return false, fmt.Sprintf("this plan asks to be confirmed by %q, which this server "+
		"does not know how to check, so nothing was done",
		f.Confirmation.Kind)
}

// numberSatisfied checks a `number` gesture, where the typed value is the value
// the act uses. No default: an empty box is refused. The bounds are checked
// here for the sentence; the act re-checks them as the guarantee.
func numberSatisfied(c *Confirmation, confirmed string) (bool, string) {
	text := strings.TrimSpace(confirmed)
	if text == "" {
		return false, "this plan is confirmed by typing the number it should be set to, and nothing was typed"
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return false, fmt.Sprintf("%q is not a whole number", confirmed)
	}
	if c.Minimum != nil && value < *c.Minimum {
		return false, fmt.Sprintf("%d is below the lowest this accepts, %d", value, *c.Minimum)
	}
	if c.Maximum != nil && value > *c.Maximum {
		return false, fmt.Sprintf("%d is above the highest this accepts, %d", value, *c.Maximum)
	}
	return true, ""
}
