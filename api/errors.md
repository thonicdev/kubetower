# The envelope, and why an HTTP status alone cannot carry it

This is the most consequential decision in `openapi.yaml`, and it is at a boundary
that cannot be changed later without a breaking release. It is written down here
rather than only in the spec because the spec says *what* the shape is and this
says *why*, and the why is what stops it being tidied away.

## Why

A backend cache that reports "no pods" for a cluster running 429 of them raises
no error. The page renders. An empty list is a valid, successful, well-formed
answer: it type-checks, it draws, and it looks calm.

That is why the console distinguishes **six** states, and why all six have to
survive into JSON:

| State | What the operator must be told |
|---|---|
| **empty** | there is nothing here |
| **pending** | nobody has asked yet |
| **unreadable** | it exists and could not be read |
| **absent** | the thing that serves this is not installed |
| **unreachable** | the cluster could not be asked at all |
| **forbidden** | the API server refused this specific verb |

`{"items": []}` says one thing where the console says six. **It destroys the
distinction at exactly the boundary that cannot be changed later**, because by
then a front end, a generated type package and any third-party client are all
written against it.

### `pending` is not a failed read

The other five are the answers to a read that happened. This console probes
clusters on its own loop and answers a request out of what it already holds, so
"nobody has asked this one yet" is a sixth answer — and it is a fact about the
**server**, not a fetch the browser has not resolved. A client cannot derive it,
and must not invent it.

It cannot be folded into the other five, because they are the ways a *read* can
fail and this is not one of them. Reported as `unreadable`, it would assert that
the thing exists and could not be read, when nothing was tried. A console that
distinguishes empty from failed and then renders "nobody asked" as either of them
has not finished the argument: a health page that says *0 of 14 answering* while
it is still asking is a wall of red invented out of not having looked.

`pending` carries a `reason` and no payload, exactly like the four failure arms —
there is nothing to draw. What differs is what a client should draw: a failure
invites a retry, `pending` invites waiting, and a count of how many are still
pending is the honest headline while a first round is in flight.

## The split: status answers one question, the envelope answers the other

> **The HTTP status answers "did this request get an answer?"**
> **The envelope's `state` answers "what is the answer?"**

A request that reaches a handler and produces a considered answer is **200**,
including when the considered answer is that the data could not be obtained. A 200
with an empty list and a 200 with an unreadable resource are different answers and
both are 200s, because the request succeeded and the answer is that the data did
not.

A request that never produced an answer — no session, unknown route, malformed
body, refused at the door — carries a 4xx or 5xx and an RFC 9457 **problem
object**, never an envelope. Two body shapes, one distinction, no overlap.

### The case that looks like an exception and is not

`forbidden` appears in both vocabularies, and the difference is *whose* refusal:

- **HTTP 403** — this console refuses **the caller** this operation. A read-only
  fence, a profile that disables the feature, an authorization decision about the
  whole request. There is no answer to give.
- **`state: forbidden`** — the API server refused **the datum's** verb. The
  console asked and got a definite answer, and that answer is part of the page: a
  column the caller may not read, a verb the drawer must not offer. The request
  succeeded.

The test is not the status code the cluster returned. It is whether the response
still has something to say.

One 403 is the profile's rather than anybody's rights: **`rbac_not_in_profile`**.
Under the server profile this version does not create, change or delete RBAC
objects (Role, ClusterRole, RoleBinding, ClusterRoleBinding), because the console
does not yet review `bind` and `escalate`. Every preview and act of such a change
(create, edit, apply, patch, delete, bulk delete) is refused before any review,
nothing reaches the cluster, and `detail` reads "RBAC changes are not available in
the server profile in this version". It is never `rbac_denied`: no RoleBinding
changes the answer. The desktop profiles are unchanged. A bulk row refused for
this reason carries `reason.code` `rbac_not_in_profile`, never `verb_denied`.

A write the cluster answers 400 (a manifest that does not decode into its kind)
is a 400 `invalid_request`, never a 403: no right was refused.

## The envelope is a value wrapper, not a response wrapper

This is the property most easily lost, and losing it is silent.

A dashboard reads capacity from one place, usage from another and events from a
third. Enveloping only the response means the whole page is unreadable when one of
the three is, or — much worse, because it looks fine — the page is `ok` and usage
is drawn as zero beside a correct capacity.

So the envelope **composes**. `Overview` has nine panels and each is an envelope.
`Cell` carries its own optional `state`, so a usage column with no metrics source
is `absent` in every row instead of `0`. `Cluster` carries its own state, so one
unreachable cluster leaves the other nine `ok`.

Getting this back after flattening the responses would be a rewrite of every page.

## The arms, and why they are machine-checked

```yaml
state: ok         → items, with at least one element
state: empty      → items, empty
otherwise         → reason, and NO items member at all
```

Three deliberate choices:

**`{"state":"ok","items":[]}` is a spec violation.** Zero elements read
successfully is `empty`. Allowing both spellings would put the collapse straight
back in. The rule is a JSON
Schema `if/then`, so the conformance test fails on it rather than a reviewer
having to notice.

**The failed arms omit `items` entirely** — not `[]`, not `null`. In the generated
TypeScript the member is optional, so `res.items.map(…)` does not compile until
the caller has dealt with the other four states. **The collapse becomes opt-in and
visible at the call site**, which is the only place anyone can make the right
decision about it.

**A value envelope has no `empty`.** A single thing that is not there is `absent`.
Two spellings of one fact is how two clients end up disagreeing about which to
check.

Verified, not asserted — these are real runs against the schemas in `openapi.yaml`:

```
FAIL ClusterListEnvelope {"state": "ok", "items": []}          [] should be non-empty
PASS ClusterListEnvelope {"state": "empty", "items": []}
FAIL ClusterListEnvelope {"state": "unreachable", "items": []} 'reason' is a required property
FAIL NavEnvelope        {"state": "empty"}                     should not be valid
PASS NavEnvelope        {"state": "absent", "reason": {...}}
```

## `reason.code` is protocol; `reason.message` is prose

A client branches on `code`. `message` is one sentence for an operator and may be
reworded in any release — a client that branches on it is branching on
copy-editing. `scope` says *what* was asked, because "unreachable" that does not
name what could not be reached is not usable in a support conversation.

## A number with no companion flag is a complete-looking lie

`Reading` carries `trustworthy` beside `value`, and `value` is nullable.

An untrustworthy reading drawn as a number is the empty-versus-failed collapse in
a gauge, where it looks entirely fine. A `float64` on its own is a complete-looking
API and that is exactly the problem.

Nullability is not stylistic either. `strconv.ParseFloat` accepts `"NaN"` and
`"+Inf"`, and upstream sources encode a gap as literal `NaN` — but `encoding/json`
**refuses to marshal a non-finite float**, so one bad sample fails the whole
response. A gap should cost one missing pixel, not the panel, and this failure
would pass in development against a cluster that has no gaps and appear in
production against one that does. Hence: gaps are `null`, and they are never
omitted, because omitting one shifts every later sample onto the wrong moment and
the chart still draws.

## Confirm-then-act: the server composes the sentence

Five route pairs are confirm-then-act, and someone has to write the sentence a
person reads before agreeing to a destructive action. There are two options:

- **(a) the server sends the sentence** — it keeps authorship of what was agreed
  to; the front end renders a string it did not write. Costs a string.
- **(b) facts only** — cleaner as an API, and it moves a safety-critical sentence
  into TypeScript, where a phrasing bug confirms the wrong thing against perfectly
  correct data and no Go test in the suite can see it.

**This server does (a).** The deciding argument is the one under (b): the sentence is composed in
the same call that resolved the facts, out of those facts, so the two cannot
disagree and one test covers both. A sentence composed elsewhere from correct
facts can still be wrong, and nothing in this repository would see it.

**`ConfirmationPlan.sentence` stays optional and nullable**, which is not
indecision: a client must handle its absence — an endpoint that has not composed
one is conforming rather than broken — and (b) remains reachable without a
breaking change. Which of the two this server does is recorded at
`x-open-decisions` in the spec so a reader and a generator both see it.

Three things hold either way:

1. **`facts` is always present and is always the authority.** A sentence that
   disagrees with the facts is a defect in the sentence.
2. **When `sentence` is present, the client renders it and does not compose its
   own.** Two authors for one sentence is worse than either alone.
3. **The act is bound to the plan by `token`, and the server re-derives the facts
   before acting.** Between a preview and a click, a cluster changes; a
   confirmation about a set that is no longer the set being acted on is not a
   confirmation. That is what the 409 with a fresh plan is for.

The third closes a gap that exists whenever the confirmation and the act are two
independent requests with nothing tying them together. Whichever way the sentence
goes, the token closes it.

`facts.confirmation` carries the gesture too (`count`, `name`, `acknowledge`,
`number`),
because "a deletion is confirmed by count rather than by typing a name" is a
decision already taken here, and a front end free to choose its own gesture will
choose the easier one.

## What still has no answer

**Nothing in the spec forces a front end to render `reason.message` rather than
its own guess at what a state means.** Six states with a sentence each is a
contract the API can offer and cannot impose. The parity checklist per page — every
page in every one of the six states — is where that gets caught, and it is a
front-end test, not a schema.

**And a code is not a remedy.** `reason.code` says which failure it is; it does
not say whose it is, and for a cluster that did not answer those are different
questions with different audiences behind them. `HealthCheck.remedy` is the one
place the contract carries the second, deliberately, rather than putting it on
`Reason` where every handler would be invited to compose one it is in no position
to write.
