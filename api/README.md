# `api/` — the contract, written before the handlers

The console's JSON API is **hand-written OpenAPI, spec-first**. This directory is
the source of truth for what leaves the process: the Go handlers are written
against it, the TypeScript types are generated from it, and a Go test puts real
responses through its schemas.

| File | What it is |
|---|---|
| `openapi.yaml` | The contract. **73** operations (counted below) |
| `errors.md` | The error envelope — the six states, why an HTTP status alone cannot carry them, and who composes a confirmation's sentence |
| `websocket.md` | The four channels OpenAPI cannot describe, and the framed log stream |

```bash
python -c "import yaml; d=yaml.safe_load(open('api/openapi.yaml')); \
print(sum(1 for p in d['paths'].values() for m in p if m in ('get','post','put','patch','delete','head')))"
# 73
```

## How paths are derived

Three rules, so that a new endpoint has an address before anyone argues about it.

1. **Every JSON endpoint is `/api` + the front-end route that needs it.** The page
   at `/c/prod/overview` is fed by `GET /api/c/prod/overview`. An endpoint serving
   no page of its own — a preference write, the session — sits directly under
   `/api/`.
2. **A segment the *cluster* names never sits beside a segment *this console*
   names.** A kind comes from discovery and can be anything, so resources live
   under a reserved one-letter segment: `/api/c/{cluster}/r/{kind}`. Extension
   routes are mounted apart, at `/x/{cluster}/{key}/`, outside `/api`: a
   top-level segment meets no console wildcard, where a method-less route under
   `/c/{cluster}/`, as a WebSocket upgrade needs, would conflict with the
   console's detail route.

   This is not tidiness. Without it, a custom resource whose plural is `metrics`
   shadows the metrics panel — and it shadows it *silently*, because a router
   prefers the literal and nothing errors. A `{kind}` segment beside a Helm path
   collides exactly this way.
3. **Probes are not under `/api/`.** `/healthz` and `/readyz` answer a kubelet, not
   a browser, and must keep their address when this contract changes.

One process serves this API and the front end from the same origin: no separate
front container, no CORS, one same-origin session cookie. Anything under `/api/`
that matches no operation answers **JSON 404**, never `index.html`.

## Two things to read before adding an endpoint

**`errors.md`.** Every 200 carries an envelope, and the envelope — not the status —
says which of *empty, unreadable, absent, unreachable, forbidden* is true. Getting
this wrong is not a bug that can be fixed in a patch release; it is the shape of
every response.

**The preference writes post one cell at a time, and that is deliberate.** A body
carrying the whole preferences object means somebody unticking one column
re-asserts every other, freezing the grid at whatever the page happened to hold.
The schemas are `additionalProperties: false` over a single cell, so the rule is
enforced rather than remembered.

## Generating the TypeScript

```bash
cd web && pnpm run gen:api    # openapi-typescript ../api/openapi.yaml -o ui/src/api/schema.ts
```

The output is committed, and CI regenerates and compares it (`pnpm run check:api`),
so an edit to `openapi.yaml` lands with its regenerated `schema.ts`. The envelope
arrives as intended — the payload
member is **optional**, so the collapse has to be opted into at the call site:

```ts
ClusterListEnvelope: components["schemas"]["Envelope"]
  & components["schemas"]["ListArms"]
  & { items?: components["schemas"]["Cluster"][] };
```

`res.items.map(…)` does not compile. That is the whole point: a developer must
decide what to show for the other four states, in the one place where deciding is
possible.

## Linting

```bash
npx @redocly/cli lint api/openapi.yaml
```

Expected output:

```
validating openapi.yaml...
Woohoo! Your API description is valid. 🎉
You have 3 warnings.
```

**The 3 warnings are all one rule** — `operation-4xx-response`, on `/healthz`,
`/readyz` and `logout`, none of which has a 4xx to declare. No configuration file
is needed and none is present: the security scheme is declared at the root.

Two other checks:

```bash
python -c "import yaml; yaml.safe_load(open('api/openapi.yaml'))"    # parses
```

and every `$ref` resolves, every `operationId` is unique, and no schema is defined
without being used — checked by script rather than by reading.

## The schema-conformance test is not optional

**Nothing regenerates this file, so the only thing between it and fiction is a
test** — `internal/api/conformance_test.go`.

It puts a real handler response through the schema for its operation. It is
strictly stronger than a regeneration diff, which can only ever confirm that a
generator ran: this can fail because the server is wrong.

The envelope rules are written as JSON Schema `if`/`then`, so the test catches the
collapse itself. Verified against the schemas in `openapi.yaml`:

```
FAIL ClusterListEnvelope {"state": "ok", "items": []}          [] should be non-empty
PASS ClusterListEnvelope {"state": "empty", "items": []}
FAIL ClusterListEnvelope {"state": "unreachable", "items": []} 'reason' is a required property
FAIL NavEnvelope        {"state": "empty"}                     should not be valid
PASS NavEnvelope        {"state": "absent", "reason": {…}}
```

`{"state":"ok","items":[]}` is a **spec violation**, not a style preference.

## Authentication, and why it is prose here

The session is an HttpOnly, SameSite=Lax, same-origin cookie set by
`POST /api/auth/login`. No token is ever readable by the browser and none is
returned in a body.

Four paths are public and nothing else is: `POST /api/auth/login`,
`POST /api/auth/logout`, `GET /healthz`, `GET /readyz`. **Static assets are behind
the guard** — stated because it is the assumption most likely to be made the other
way, and making them public breaks nothing visibly.

**An expired session answers `401 application/problem+json`, never an HTML login
page.** A `fetch` renders a login page as nothing at all: no error, no unusual
status in a log, a blank screen. The front end routes to its own login screen on
the status, which only works if the status arrives.

Every mutation additionally requires the `X-KubeTower-Request: 1` header and passes
a server-side `Origin` check. A cookie-authenticated API without both is
cross-site-forgeable; a custom header cannot be set cross-origin without a
preflight the browser then refuses.

## What is provisional, and marked as such

Honest limits, so nobody builds on them as if they were measured:

- **The per-kind cell contents.** `ListPage` is generic because one view model draws
  every kind, so the generic shape is faithful rather
  than lazy. What fills a cell per kind is real design work and no tool does it.

### Settled

- **The rail and column write bodies** follow the preference store: the rail has
  **hotbars and no groups**, so `/rail/bar` selects which bar is on screen rather
  than showing or hiding one, `/rail/place` drops a tile **beside another tile**,
  and `ColumnDensityWrite` carries no `kind`, because the store has nowhere to put
  one.
- **`SettingsWrite`** carries the fields the General tab actually writes.
- **A preview returns its sentence as well as its facts**: the server composes it,
  derived from the facts in the same call so the two cannot disagree. `sentence`
  stays optional and nullable, so the other answer is still reachable. See
  `errors.md`, and `x-open-decisions` in the spec.

## What is deliberately absent

- **Pagination for the client.** No list operation takes a page or a cursor: a
  list answers whole, or not at all. The server reads it from the API server in
  pages of 500 objects, following the continue token to the end, and stops at a
  ceiling rather than cutting the list short: past 50 000 rows, or past 10 000
  rows or 100 namespaces when the list is narrowed to the namespaces the person
  may read, the list is `unreadable` with `not_supported_here` and its sentence
  asks for a namespace. A truncated list is another state, and inventing one
  quietly is exactly what the envelope exists to prevent. Two bounded answers are
  named as such: the Overview's `events` and `restarting` carry at most 200 rows,
  with `eventsTotal` and `restartingTotal` saying how many there were; and a log's
  earlier lines are a bounded read (`GET /api/logs` with `before` and `page`), not
  a page of the stream.
- **Server-side sorting.** `Cell.sort` carries what a column sorts by, the lists
  are whole, and sorting a list already in hand is arithmetic. There is no `sort`
  parameter and there should not be one: adding it would make the order a round
  trip and make an interval refresh able to reorder the table under a reading
  finger.
- **A version segment in the path.** The front end and the API ship from one
  process, at one version, and the TypeScript package is version-locked to the Go
  module. A `/v1/` would suggest a compatibility promise that the release process
  is what actually makes.
- **Extension routes.** They mount under `/x/{cluster}/{key}/`, outside `/api`,
  and each carries its own slice of contract, except the statuses below.
- **HTML, anywhere.** Manifest coloring returns tokens; the front end renders
  spans. An API that returns markup ships an injection surface with it.

## What every extension route answers for a read refused for the person

Argo CD, KubeCron, KubeAdjust and the Redis viewer answer a read the access
review would not let through the same way (`extension/refused.go`), whatever the
route:

| Status | `code` | Means |
|---|---|---|
| 403 | `list_forbidden` | The cluster says the person may not make this read. Asking again changes nothing |
| 503 | `list_unreviewed` | The console could not ask the cluster whether the person may, so it made no read. A retry may change it |
| 503 | `console_not_granted` | Under the server profile the person's review allowed the read and the cluster refused the console's own account: a grant the installation lacks, named in `message`. Not the person's rights; whoever installed the console adds it. Asked first with `extension.ConsoleNotGranted`; the cnpg and cert-manager pages answer it |

Neither is ever the 502 of a read that failed: the cluster was not read. Each is
a problem object (`code`, `message`, `detail`); the Redis viewer's also carries
`kind`, `refusal` for the 403 and `failure` for the 503.

A cluster that is not in the kubeconfig is refused by the console's gate before
any extension route runs. Once the extra is on for some cluster, the answer has
the same shape as under `/api`: 404,
`application/problem+json`, `code` `not_found`, and the console's sentence for
an unknown cluster as `detail`. A route meets the case itself only when the
kubeconfig changes between the gate and its read, and answers 404 with its own
code then (`cluster_unknown` in Argo CD, `unknown_cluster` in the others).
