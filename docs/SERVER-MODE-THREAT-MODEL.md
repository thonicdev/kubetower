# Server mode: the threat model

The console has two runtime profiles. This document exists because the second
one changes who the adversary is, and that change is not visible in any diff:
most of the code that becomes dangerous under it is code that is already
written, already tested, and already correct for the profile it was written for.

**It is a prerequisite, not a document.** Server-profile code is written against
it, for the same reason the read-only guardrail exists: it is far cheaper to
decide that a route is never registered than to find out later that a condition
guarding it has a path around it.

> **How to read it.** Every count is produced by a command, and the command is
> quoted beside the figure; re-run it rather than trusting the figure. A claim
> about what the code does names the file or the test that holds it. What is a
> requirement rather than an observation is said to be one, and §17 lists what
> could not be determined at all.

---

## 1. The profile being modeled

Two profiles, selected by a flag. Desktop is the default; `--incluster` selects
the server profile, and it is the only way to select it. There is no third, and
a container running the desktop profile is still the desktop profile.

| | Desktop (default) | Server (`--incluster`) |
|---|---|---|
| Reached by | loopback, one browser | a TLS ingress, many browsers |
| Who signs in | one operator: on loopback, a one-time sign-in code and no password; otherwise a bcrypt password | anybody the identity provider vouches for, and the shared password account where it is kept |
| Kubernetes credentials | the operator's own kubeconfig | one ServiceAccount, every action gated by a review naming the person (§5) |
| Clusters | all of them | **only the one it runs in** |
| Local shell, assistant | yes | see §9 and §10 |

"Only the one it runs in" is doing more work in this document than it looks. It
removes an entire class of threat — the console holding long-lived credentials
for clusters it is not in — and §12 is written about what returns if that is
ever revisited rather than about anything shipping now.

## 2. The inversion, stated once

In the desktop profile the operator **is** the trust boundary. The session
defends a loopback socket against another program on the same machine; the
kubeconfig is theirs; a shell the console opens runs as them and can do nothing
they could not do from their own terminal. Nearly every guardrail in the tree is
therefore a *safety* limit — it protects the operator from a mistake, not from
an adversary.

In the server profile the same guardrails are asked to be **isolation**, between
people who are not each other, none of whom is the process. Three consequences
run through everything below:

1. **A process-wide limit becomes a shared resource.** §7.
2. **A guardrail that refuses in words becomes a control that must refuse in
   structure.** §9.
3. **Anything the process holds, every signed-in person effectively holds**,
   because the process is the one making every call. §14.

The last is the whole of the confused-deputy problem, and asking the cluster
about each person does not solve it. What a person may do is the cluster's RBAC,
asked per person by a SubjectAccessReview: that makes the *policy* the cluster's
rather than the console's; it does not stop the console being the one that
acts. §5 says what that buys and what it does not.

## 3. What is worth taking

In rough order of how much an attacker wants it:

- **Every `Secret` in the cluster**, because the ServiceAccount can read them in
  order to show them (§14). This is the headline and everything else is smaller.
- **The pod's ServiceAccount token**, at
  `/var/run/secrets/kubernetes.io/serviceaccount/token`, which is the same thing
  without going through the console at all.
- **A live session cookie**, which is a working identity for up to the cap (§8).
- **The archive on disk**, which is a persistent copy of cluster state collected
  with the ServiceAccount's rights and not re-authorized on read (§6.2).
- **An open port-forward**, which is a raw TCP path into a workload with no
  front door of its own (§7.3).
- **The audit log**, for the same reason anyone wants an audit log: to not be
  in it (§13).

---

## 4. Identity: the OIDC flow and the session store

### 4.1 The session machinery, read from the code

"The session is a stateless cookie" is true of the shared password's session and
is a much smaller description than the thing. `internal/auth/auth.go`:

- **HMAC-SHA256 over a versioned payload.** `mint` writes `2:<issued>:<expires>`
  and appends `|` plus the signature; `parseToken` verifies the signature with
  `subtle.ConstantTimeCompare` **before** reading a single field, and refuses a
  v1 payload rather than interpreting it charitably.
- **A sliding idle window with an absolute cap**, and both are re-derived per
  request. `expiryFor` returns `now + idle`, clamped to `issued + max`; `live`
  then checks the cap **against the current configuration** rather than trusting
  the stamp in the cookie. Defaults: `DefaultSessionIdle = 30 * 24 * time.Hour`,
  `DefaultSessionMax = 90 * 24 * time.Hour`.
- **A rate limit on the `Set-Cookie` itself.** `refreshEvery = time.Hour`, and
  `refreshDue` compares the deadline the token *has* against the one a token
  minted now *would* have, so at the cap it stops writing on its own.
- **No refresh on a protocol upgrade.** `keepAlive` returns early for a
  WebSocket, because gorilla's upgrader writes the 101 from scratch and a header
  set there is discarded — a refresh that appeared to happen and did not.
- **The cookie flags are written in one place**, `sessionCookie`, so a refresh
  cannot silently drop `HttpOnly` an hour after a correct login.

Two properties of that machinery matter more in a shared console than in a
single-user one, and neither is obvious from "stateless":

- **Lowering `KT_SESSION_MAX` is a real revocation lever**, fleet-wide, without
  rotating the signing key — because `live` re-derives the cap. It ends every
  session older than the new cap, which is coarser than key rotation in one way
  (it is time-based) and identical in the way that matters here: it is not
  per-person.
- **A stream is authenticated at its upgrade.** In the desktop profile nothing
  more is needed. In a shared console a session revoked by any mechanism short
  of killing the pod would keep whatever sockets it already had, so a pod exec
  and a log follow ask again while they run (§8.3).

### 4.2 What the server profile adds

`internal/auth/oidc.go` implements the authorization-code flow with `state`,
`nonce` and PKCE (S256). The console verifies signature, `iss`, `aud`,
`exp`/`nbf` and the nonce itself, and takes no identity from a header. No token
is kept after verification, and the console asks for no refresh token, so "the
refresh token never leaves the server" holds because there is none. Every person
the provider signs in is reviewed under their own name (§5).

Five properties of the flow:

- **The server profile only.** The process refuses to start with
  `KT_OIDC_ISSUER` and no `--incluster`. The desktop profile serves the local
  shell, the node shell and the assistant as the process (§9), and behind a
  provider that would be every person the provider accepts
  (`internal/launch/ssoprofile_test.go`).
- **There is no allow-list of users or groups.** Who may sign in is decided at
  the provider. With a broad one - Google, a whole tenant, GitHub through Dex
  without an organization restriction - that can be anyone.
- **`system:` is refused** in the name and in every group after the prefix, and
  as the prefix itself. kube-apiserver does not refuse it by default; its
  structured authentication documents the rule as a `userValidationRules`
  expression an operator writes. The console is what asserts the groups to the
  review, so it applies the rule itself.
- **The flow is a sealed cookie** (AES-256-GCM, key derived from the session
  key) carrying `state`, nonce, PKCE verifier, `next` and expiry. The server
  keeps nothing between the click and the return, so the public start route
  holds nothing a flood can evict. **Single use is the console's own**: once
  the code is exchanged and the ID token verified, and before a session is
  opened, a hash of `state` is recorded in the session store, set-if-absent,
  until the flow would have expired. A second callback with the same state is
  refused even against a provider that redeems a code twice. Recording after
  the exchange is what keeps the public start route from writing into the
  store (`TestAForgedOrReplayedCallbackIsRefused`). **Its limit under
  Valkey:** at Valkey's memory ceiling `volatile-ttl` evicts the keys nearest
  their expiry first, these short-lived markers among them, so against a
  provider that honors a code twice the replay protection weakens while
  Valkey is full. The chart sets that ceiling (`valkey.maxmemory`) but has
  not sized it against a number of sign-ins, so this is a stated limit, not a
  guarantee.
- **Login CSRF by cookie planting.** The flow cookie binds the callback to
  the browser that clicked, but only if nobody else can write that cookie. An
  attacker who can set a cookie for this host - from a sibling subdomain, or
  by injecting one into a plain-http response on the same name - can plant
  their own sealed flow, send the victim to the callback with their own code
  and state, and sign the victim in as the attacker. Behind https the cookie
  is named `__Host-kubetower_sso` with `Path=/`, `Secure` and no `Domain`,
  which a browser accepts only from this host over TLS, so it cannot be
  planted. **Over plain http it cannot be closed**: the prefix needs a secure
  origin, so a development console keeps the unprefixed name and is exposed
  to anybody who can write its cookies
  (`TestCookiesAreSecureWhereTheBrowserArrivesOverTLS`).
- **The public sign-in state names no address.** `/api/auth/methods` and the
  sign-in page say which kind of failure it was; the log has the error.

**The name is an authorization input, not a label**, because the review names
the person (§5.2): the API server answers about whoever a RoleBinding names with
that string. Two risks follow.

- **A collision with Kubernetes' other users.** Asserted bare, the provider's
  `alice` is the `alice` of a client certificate or of another authenticator,
  and a RoleBinding written for one grants the other. So the name carries
  `KT_OIDC_USERNAME_PREFIX` before it is reviewed, logged or stored -
  kube-apiserver's `--oidc-username-prefix`, **with its default**: the issuer
  and `#` for any claim but `email`, which stays bare; `-` for no prefix, typed
  out. Mirroring the default is what lets one RoleBinding mean the same person
  to `kubectl` against a cluster that trusts the same issuer and to the
  console. **That default is the `--oidc-*` flags' only**: a cluster set up
  through structured authentication (`AuthenticationConfiguration`) has none,
  its `claimMappings.username.prefix` is whatever its operator wrote, and
  `KT_OIDC_USERNAME_PREFIX` has to be set to the same string. The start-up log
  prints the prefix in force, because it is the string every RoleBinding has
  to name. A name that is `system:` after the prefix, or that holds a
  character drawing as nothing - a control or format character, a
  default-ignorable one, a line separator - is refused at sign-in, and so is
  such a group; a prefix that is either is refused at start-up
  (`TestANameIsCheckedAfterItsPrefix`, `TestWhatANameMayHold`,
  `TestThePrefixedNameIsWhatIsReviewedAndWhatTheWriteLineSays`). Real names
  keep what they carry: the direction marks, the soft hyphen, and the
  zero-width joiners inside a word of a script that shapes with them (Persian,
  Malayalam). **Not caught**, and not claimed: spaces of any kind and
  homoglyphs. What keeps one person from looking like another is the prefix
  and a claim they cannot edit, not a character class.
- **A session outlives the configuration that named it, unless it is
  refused.** The store keeps a session for up to the single sign-on cap,
  twelve hours by default, and survives a rollout under Valkey. Each record
  carries a fingerprint of the issuer, the two claims and the two prefixes it
  was derived under, and a record whose fingerprint is not the running one is
  no session: the person signs in again and gets the name this configuration
  gives. Without it, setting the prefix - or moving the claim from `email` to
  `sub` - would leave everybody already signed in reviewed under their old
  name until the cap. A record with no fingerprint lapses the same way
  (`TestASessionFromAnotherMappingIsNoSession`).
- **A claim the person can edit is a person they can become.** Which claim
  names the person is the operator's (`KT_OIDC_USERNAME_CLAIM`), and the
  console cannot tell a safe one from an unsafe one. **Safe**: `sub`, which the
  provider assigns and nobody edits, and an `email` the provider verifies.
  **Not safe**: `preferred_username`, `name`, `upn`, and an email the
  provider's users can set themselves - at some providers each of those is a
  field the user types, and the console would assert whoever they typed. The
  Azure AD "nOAuth" case is the known instance. An `email` with
  `email_verified: false` is refused; **one with no `email_verified` claim at
  all is accepted**, as kube-apiserver accepts it, because some providers never
  send the claim and refusing would make `email` unusable there - and the log
  says once that nothing vouched for the address. That acceptance is the
  residual risk, and it is the operator's to close by choosing `sub`.

The requirements those properties answer:

- **Authorization-code flow with PKCE**, server-side. The session cookie is
  `HttpOnly`, so no token is readable by the browser; the refresh token must
  never leave the server at all.
- **The console verifies the ID token itself** — issuer, audience, signature
  against the provider's JWKS, `exp`, `nbf`, and the nonce it issued — and does
  not accept an identity asserted by an ingress header. An ingress-asserted
  identity is a header, and a header is whatever reaches the pod; in a cluster,
  what reaches the pod is not only the ingress (§14.3).
- **The identity mapping is the console's assertion.** This is the part to be
  frightened of. The console tells the API server *"alice, in these groups, is
  asking"*, and the API server answers faithfully about alice. A bug in token
  verification, in the claim-to-username mapping, or in the groups claim, does
  not produce a denied request — it produces an **allowed** request about the
  wrong person, with the API server's authority behind it and an audit record
  naming the victim. Every other control in this document assumes this one is
  right.
- **The session cookie stays the same cookie.** One cookie shape, one guard, one
  place the flags are written. What changes is what the payload carries — an
  opaque reference to server-side state (§8).

### 4.3 What an attacker with a session cookie gets

Concretely, under the server profile:

- Every object the person may read, as the review answers it (§5) — and, through
  the reads the review does not cover (§5, *What does not go through it*), what
  the ServiceAccount may read. Not "information disclosure" — a `Secret`'s value
  is one more click where the person may `get` it.
- A pod exec on anything the review allows, which under a permissive
  `RoleBinding` is a shell in a production workload.
- Nothing on the machine the console runs on, **if** §9 holds.

### 4.4 How each of those is tested

| Requirement | Test |
|---|---|
| A token with a bad signature is refused before any field is read | `parseToken`'s `ConstantTimeCompare` precedes the `Cut`; assert with a token whose payload is valid and whose signature is one byte off |
| A v1 cookie is refused rather than guessed | `TestASessionCookieFrom051IsRefusedRatherThanTrusted` |
| Shortening the cap ends old sessions | `live` re-derives; assert by minting at cap A and reading at cap B |
| An ID token with the wrong audience, issuer or nonce is refused | `TestEveryWrongClaimIsARefusalAndNeverAFallback` in `internal/auth/oidc_test.go`. One row per claim (issuer, audience, authorized party, expiry, `nbf`, nonce, signature, username claim, `email_verified`, groups shape), and each row asserts the refusal **and** that no session came out of it |
| The name reviewed and the name on the write line are the same prefixed string, and one that is `system:` once prefixed or holds a character drawing as nothing is refused | `TestThePrefixedNameIsWhatIsReviewedAndWhatTheWriteLineSays`, `TestANameIsCheckedAfterItsPrefix`, `TestWhatANameMayHold` and `TestTheUsernamePrefixFollowsKubeApiserversRule` in `internal/auth/oidc_test.go` |
| A session signed in under another prefix, claim or issuer is no session | `TestASessionFromAnotherMappingIsNoSession` |
| The console never reads identity from a request header | **to write**, and it is a walk-shaped assertion: grep the handler chain for the header names the ingress would set, the same way `internal/launch/envwiring_test.go` greps for `KT_` variables |
| A revoked session cannot open a **new** socket | **to write**, §8 |

### 4.5 The transport: HSTS, sent by the binary

The server profile is reached through a TLS ingress (§1), and that is also the
assumption the session cookie's `Secure` flag rests on. The flag stops a browser
sending the cookie over plain http; it does not stop the browser *going* to plain
http. A typed `kubetower.example.com`, an old `http://` bookmark or a link
somebody wrote out by hand makes a first request in the clear, and whoever is on
the path can answer it instead of the ingress's redirect: a sign-in page served
over http, and the password typed into it (an SSL-strip).

**The binary sends `Strict-Transport-Security`.** Under `--incluster`, and only
there, every response carries `Strict-Transport-Security: max-age=31536000`
(`web.StrictTransport`, applied by `frontHandler` in `internal/launch/run.go`).
The chart does not own it, so it holds whichever ingress is in front and there
is nothing to keep in step.

- **This host only.** No `includeSubDomains`: the ingress name may share a parent
  domain with other services, and the directive would make every name below it
  https-only for a year. No `preload`: a preload list is a decision about a whole
  domain, its owner's to take.
- **A year**, which covers the gap between visits of somebody who opens the
  console once a quarter and is longer than a password session's default 90-day
  cap.
- **Sent without looking at the request's scheme.** Behind the ingress the
  process listens on plain HTTP and cannot see the browser's transport. Where the
  profile is reached over plain http anyway - a development ingress, a
  `kubectl port-forward` to the pod - the browser receives the header over a
  non-secure transport and discards it (RFC 6797 §8.1), so nothing is pinned.
- **Not on the desktop**, which listens on plain-HTTP loopback. Nothing about the
  desktop's responses changes.
- **An ingress that adds its own** (some do by default) may send it beside this
  one; a browser processes only the first (RFC 6797 §8.1). `includeSubDomains`
  or `preload`, where an operator wants them, are set at the ingress, checking
  that its header replaces this one.

**What it does not cover.** The very first visit from a browser that has never
seen the header, and a visit after it has lapsed: HSTS is trust on first use.
Nor any other name for the same pod - an IP, a second ingress host - until that
name has itself been answered over https. And it is no substitute for the
`Secure` cookie: under `--incluster` the session cookie is `Secure` even
without single sign-on. The two are companions: `Secure` keeps the cookie off
plain http on the first visit too, HSTS keeps the browser off plain http on every
visit after it.

Held by `TestTheServerProfileSendsHSTSAndTheDesktopDoesNot`
(`internal/launch/fronthandler_test.go`: the header on every server-profile
response, none on either desktop's, their 421 refusal included) and
`TestStrictTransportIsThisHostOnlyForAYear` (`internal/web/headers_test.go`).
Not observed through a real ingress or in a browser.

---

## 5. Kubernetes credentials: one ServiceAccount and one review

What a person may do is the cluster's RBAC, asked per person by a
SubjectAccessReview. `internal/access` is the choke point: `Gate.Perform` sends a
`SubjectAccessReview` naming the signed-in person and their groups (plus
`system:authenticated`) and runs the request only on `allowed`. Every write in
`internal/edit` (patch, create, delete, eviction), the pod exec and the node
shell in `internal/terminal`, the forward manager and the reveal of a masked
value (a Secret's key, a Helm release's supplied values, or its rendered notes)
ask it; the pages ask it before they offer a verb. The shared password account
and the desktop are reviewed as the console's own credentials (a
`SelfSubjectAccessReview`); a request no identity reached is refused under the
server profile. A review that cannot be made refuses - except on the desktop,
for the console's own credentials, where the API server checks that same
identity and the request goes through with `decision=unreviewable` on its line;
a denial refuses everywhere.

**Reads go through the same gate** (`internal/access/read.go`), with a read
verb and the same held answers:

- **`Gate.Read`** for one thing: an object by name - reviewed **with the name**,
  so a Role granting `get` on one named Secret opens that Secret and refuses its
  neighbor (§5.3) - a list in one namespace, a log (`get pods/log`, by pod).
- **`access.List`** for a list that may cross namespaces: one review for the
  whole list; when that is denied and the kind is namespaced, the list is read
  once with the ServiceAccount and **narrowed to the namespaces the person may
  list**, one review per namespace the rows are in. A list of one namespace the
  person may not list, a cluster-scoped list they may not make, and a list no
  review could be made for are refused - and not read.
- **An empty answer and a narrowed one are different answers**: a narrowed list
  carries `accessNote`, and the dashboard too. The sentence is built from the
  question, never from the rows, so it does not say how much was left out. The
  Helm release list has no member to say so; it refuses instead - a selected
  namespace the person may not list is named, and the unselected page is
  refused to a person who may not list Secrets everywhere.
- **A hold shared by every person is filled as the console** (`AsConsole`) and
  reviewed for each person on the way out: the namespace dropdown (and the
  "no such namespace" note, which would otherwise let a person probe for
  names), the usage gauges, the custom-resource catalog (discovery, which
  every authenticated identity may read), the console's own Prometheus. Each is
  listed, with its reason, in `choke_test.go`.
- **The Prometheus panel reads under the console's rights**, because its series
  come from the console's own monitoring backend and Prometheus has no RBAC of
  its own to ask. What a person is shown out of it is reviewed: an object's
  metrics panel is `get` on that pod or node, by name; the hottest-pods
  rankings are narrowed by `list pods` per namespace; the cluster's own history
  is shown with the page whose node list the person was allowed.
- **Who chooses that backend is a trust boundary of its own, and the
  deployment holds it.** Reviewing who *sees* the series says nothing about
  where they come from. A Prometheus found by ranking Service labels across
  every namespace is chosen by whoever may create a Service: a person who may
  create one in a namespace of their own could label it to rank first, and
  every chart, Overview figure and hottest-pods ranking on the shared console
  would be drawn from their pod - which could hide saturation or an attack in
  progress - and every query, naming the pods and nodes people were looking at,
  would go to it. So under `--incluster` the target is `KT_PROMETHEUS_SERVICE`
  (`namespace/service:port[/path]`), set by whoever installs the console, and
  **nothing is discovered**: unset is no Prometheus, never a fall back to
  labels, and a configured target that does not answer is reported unreachable
  under its own name rather than replaced (`internal/launch/prometheus.go`,
  `metrics.Options.Configured`). Whether a hostile Service could be reached at
  all is the ServiceAccount's `services/proxy` and `pods/portforward` grants
  (§5.1); the chart grants neither by default, and narrows the first by Service
  name only - in a ClusterRole a name matches in every namespace, so a
  narrowing to `prometheus:9090` also admits `team-a/prometheus:9090`.
  Configuration is what closes it; RBAC alone does not. The desktop is one
  person's console over their own kubeconfig, and keeps looking: there it
  refuses to choose when Services in more than one namespace claim to be
  Prometheus, and says which - and the same across engines (#133): Services
  claiming to be Prometheus in one namespace and VictoriaMetrics in another are
  refused alike, since a Service labelled `app=prometheus` in a tenant's
  namespace beside a real vmsingle elsewhere is the same decoy; the two in one
  namespace keep Prometheus. A source set by hand in Settings › Monitoring
  replaces discovery on that cluster, as the server profile's configuration
  does, and is kept in the desktop's state directory under the resolved
  context's name. An answer larger than 16 MiB is refused on both
  transports.
- **A tunnel is reviewed as the request it is**: `terminal.Forwarder.Forward`
  reads the Service and its pods for whoever the context carries and digs the
  tunnel inside `Gate.Perform` as `create pods/portforward` on the pod it lands
  on - so an extra's `Host.Forward` made with a person's request is reviewed for
  that person, and the console's own tunnels to Prometheus say `AsConsole`.
  `Host.Forward` names the pod the tunnel reached, so a credential read for a
  pod is sent only down a tunnel to that pod.
- **A tunnel the console digs for an extra is the console's, served per
  person.** `Host.ForwardAsConsole` is `Host.Forward` dug `AsConsole`: the
  Delivery Center's diff reaches argocd-server through it, and KubeCron its
  collector, so a person needs no `pods/portforward` in either namespace -
  a right that would also reach argocd-repo-server and argocd-redis. The
  ServiceAccount needs it instead, and the chart does not grant it per
  namespace: `pods/portforward` comes with `pods/exec`, both `create` and
  `get`, in its ClusterRole, behind `rbac.exec`, which is off by default.
  Naming an extra in `extras` never grants it. So with the chart's defaults
  neither tunnel can be dug; with `rbac.exec`
  on, the ServiceAccount may open a shell in and a tunnel to every pod in the
  cluster, not only in those two namespaces. The tunnel is
  recorded as the console's act with the person as `onBehalfOf`, refused for
  a request with nobody on it, counted against the same ceiling and the same
  per-person share as `Host.Forward`, and closed when the request ends. What
  comes down it is the ServiceAccount's read, so the extra asks the person's
  own reviewed reads first and serves only what they returned: the diff only
  after the person's `Host.List` of the object and of its Application, and
  only argocd-server's item for that object; the collector's history only for
  the CronJobs the person's `Host.List` returned, a run's log only after
  `Host.MayReadLogs`. A person who may list no CronJob causes no tunnel.
  The Redis viewer keeps `Host.Forward`: it reaches the person's own
  workload.
- **An extra reads a Secret's value through `Host.Reveal` and nowhere else on
  the contract**: the console's own reveal door, reviewed for the person as
  `get secrets/<name>` by name, recorded as a reveal naming the key and the
  extension's key (attempt before the read, outcome after; an attempt that
  cannot be written refuses the read), and refused on a review that could not
  be made, on both profiles. The ServiceAccount never reads it for a person the
  review did not allow - **for an SSO identity**: a session signed in with the
  shared password is reviewed as the console itself, so through it
  `Host.Reveal` reads what the ServiceAccount may read, as the console's own
  `RevealEnv` does. `Host.List` refuses `v1/secrets`, and any group, version,
  resource or namespace that is not one path segment (client-go cleans the URL
  it joins, so `secrets/` would be read as `secrets`), before reading anything.
- **A runtime backstop holds the writes behind the walk**
  (`internal/access/transport.go`). Every client the cluster manager builds -
  typed, dynamic, streaming, an exec's executor, a forward's dialers - sends
  through `access.Guard`, which refuses a request that would change the cluster
  (POST, PUT, PATCH, DELETE, any method it does not know, exec, attach and
  portforward on a pod whatever the method, any upgrade request, and the
  kubelet's own streams through a node's proxy - `nodes/{name}/proxy/` followed
  by `exec`, `attach`, `portForward`, `run` or `cri`) unless its context carries
  the stamp `Gate.Perform` put there: for this cluster, about a question that
  covers the request - the same verb, group, resource and subresource, and the
  same namespace and name or none. The request's attributes are read from its
  path as kube-apiserver reads them. It is refused before it leaves the
  process, as an error and never as the cluster's 403, with an `unsent:` log
  line naming who was behind it. Two exceptions, named in `choke_test.go` like
  the walk's: the reviews themselves, and the node shell's clean-up of its own
  pods (`access.Housekeeping`, a stamp that still has to cover its one pod).
  Both profiles carry it, the desktop included.
- **The node proxy is the grant to watch.** kube-apiserver authorizes
  everything under `nodes/{name}/proxy/` as `get nodes/proxy` - the grant the
  console's kubelet-stats read needs, so a ServiceAccount set up for the disk
  and usage figures holds it. Through it the kubelet runs a command in any pod
  on that node, with no `pods/exec` check at all - so a grant meant for reading
  figures is a shell. The guard holds those paths, and any upgrade, as changes
  no review stamps; the figures (`stats/summary`, `metrics`) and
  `containerLogs` stay reads.
- **An extra's own clients answer discovery and nothing else.**
  `extension.Host.Client` hands an extra a typed and a dynamic client built over
  the cluster manager's connection, behind a transport
  (`access.DiscoveryOnly`, `internal/access/discovery.go`) that sends only
  `GET`/`HEAD` of `/api`, `/api/{version}`, `/apis`, `/apis/{group}`,
  `/apis/{group}/{version}` and `/version` - which kinds the cluster serves,
  what every authenticated identity may read. A get, a list or a watch of any
  object, a raw REST call, an upgrade and every change are refused before they
  leave the process, as an error and never as the cluster's 403. So an extra
  reads no object with the ServiceAccount's credentials through `Client`, and a
  change it sends there is refused twice: by that transport, and by the guard
  above, since an extra is handed `extension.Host`, not `Gate.Perform`, and its
  request carries no stamp. A write an extra needs is a core route it leads to,
  through the one guarded door. The boundary module proves both from an extra's
  side, against the real console: a typed delete and a dynamic create, a typed
  get and a dynamic list through `Host.Client` are refused, discovery is
  answered, and the cluster receives none of the four. `extras/boundary_test.go`
  also refuses `k8s.io/client-go` in an extra's non-test files, so an extra
  builds no client of its own.

  **What keeps an extra from reaching `Gate.Perform` is a test, not the
  compiler.** Every extra is a package of this module, and Go's `internal/`
  rule is path-based: inside one module it lets `extras/` import
  `internal/access` as freely as any core package. `extras/boundary_test.go`
  parses every Go file under `extras/`, whatever its build constraints, and
  fails on any import of a package of this module other than `extension`,
  `schedule`, `textutil`, `svcgroup` and the extra's own (its tests may also
  import `app`), so one extra cannot import another either. It sees **direct
  imports only**: a public package of the core that imported `internal/` and
  handed it on would not be caught there, and review is what stands behind
  that.

**What does not go through it, and must not be read as covered:**

- **What an extra reads with the console's credentials: what the
  configuration names, and nothing else.** Every object an extra lists goes
  through `Host.List`, reviewed for the person as the console's own lists are
  (narrowed to the namespaces they may list, a read by name reviewed with the
  name), and `Host.Usage`, `Host.ContainerUsage`, `Host.UsageWindow`,
  `Host.VolumeFill`, `Host.Reveal`, `Host.Exec`, `Host.Forward` and
  `Host.MayReadLogs` are reviewed for the person too - the two usage doors as
  `list pods` and the volume-fill door as `list persistentvolumeclaims`,
  because neither metrics-server read with these credentials nor Prometheus,
  which has no RBAC, knows who is asking. **Two doors are not reviewed for the person, by
  design**: `Host.ConfiguredSecret` and `Host.ConfiguredService` read, as the
  console (`AsConsole`, the ServiceAccount here), one Secret's key or one
  Service that the extension's `extension.Configured` names **from the
  configuration alone** - the environment, the cluster's setting (written on
  the desktop and in a container only), the extension's defaults - and refuse
  any other namespace or name with `ErrNotConfigured` before anything is
  reviewed or read (`internal/web/hostconfigured.go`). That is Argo CD's
  read-only token and argocd-server's Service (`KT_ARGOCD_TOKEN_SECRET`,
  `KT_ARGOCD_SERVER_SERVICE`), and the KubeCron collector's Service and token
  when `KT_KUBECRON_COLLECTOR` (or the setting) names it. Reviewed for the
  person, the token would be handed to whoever may read it, who could then use
  it directly; read as the console, the person never sees it. A Secret's read
  is recorded as a reveal by the console with the person as `onBehalfOf`,
  naming the key and the extension (attempt before the read, an attempt that
  cannot be written refuses it); its value is in no record, log line, error or
  answer, only in the request the extra sends to that backend. A request with
  nobody signed in is refused. The ServiceAccount needs `get` on those Secrets
  and Services; what the person is then shown out of the backend stays their
  own reviewed reads (above, the tunnel the console digs for an extra).
  `Host.MayReadLogs` reads nothing: it is the review `get pods/log` for the
  person, which KubeCron asks before it shows a run's log its collector
  stored, so a person `kubectl logs` refuses is refused that copy too.
- **A command an extra runs in a pod.** `Host.Exec` is the console's exec door
  handed to an extra, for the JVM Inspector's `jcmd`. It is reviewed for the
  person as `create pods/exec` on the pod by name, through the same
  `Gate.Perform` as a shell, so it reaches no pod the person could not open a
  shell in; its attempt and outcome records name the extension and **the argv**,
  so an extra must never put a secret in one. It runs the argv with no shell, no
  stdin and no terminal, keeps 4 MiB of output, stops at a deadline (30 s by
  default, 2 minutes at most), and counts sixteen at once with a share of four
  per person. **The review says the person may exec in the pod, not that the
  command is harmless**: which commands a page runs is the extra's to hold to a
  fixed list, as the Redis viewer holds its own.
- **A credential sent to a Service the cluster names.** No extra does this.
  The Delivery Center reads Argo CD's token through `Host.ConfiguredSecret`
  and sends it only to the Service `KT_ARGOCD_SERVER_SERVICE` names
  (`argocd/argocd-server` when unset), read by name, over TLS whose
  certificate must be valid for `KT_ARGOCD_SERVER_NAME` (the Service's
  `<name>.<namespace>.svc` when unset) and chain to the `ca.crt` beside the
  token, or to the system's roots; never over plain HTTP, and no redirect is
  followed. KubeCron's collector token is read from one Secret -
  `KT_KUBECRON_TOKEN_SECRET`, the cluster's setting, or `kubecron-api` - **in
  the collector's own namespace and no other**, and sent only to that
  collector: the collector is the Service `KT_KUBECRON_COLLECTOR` or the
  setting names, read as the console through `Host.ConfiguredService` with its
  token through `Host.ConfiguredSecret`; or, where nothing names it, the one
  Service labeled `kubecron.io/collector` **among those the person may list**
  (`Host.List`), never the first of several - and then the token, in a
  namespace the cluster chose, is read **for the person** through
  `Host.Reveal`, or not at all. The header is added only when the Secret's
  namespace is the Service's. The setting is written on the desktop and in a
  container only; under the server profile the chart's values decide. A
  tenant who labels a Service in their own namespace is read only where it is
  the only collector the person may list, with that namespace's Secret, which
  the person must be allowed to read. **The Redis viewer is not in
  this class**: it reads no stored token. A password is read only after the
  instance has refused an unauthenticated connection, only from the pod the
  tunnel reached (`Host.Forward` names it), through `Host.Reveal` - reviewed
  for the person as `get secrets/<name>` and recorded - and sent only down that
  tunnel, which a port-forward pins to that pod; a tunnel that cannot name its
  pod is sent none (`extras/redis/session.go`). The routes that dial are POSTs,
  so a cross-site link - a GET with the session cookie under `SameSite=Lax` -
  cannot make the console dial an instance or record a reveal in the person's
  name.
- **A hostile extra.** The guard holds honest code to the contract - the
  core's own and an extra's - and turns a mistake into a refusal. It is not a
  boundary against an extra that means harm: one is compiled into this
  process, so it can read the ServiceAccount's token from the pod or reach
  past the transport by reflection. An extra is trusted code, reviewed like
  the core; "an extra cannot write through its own clients" above means it
  cannot by using them as handed.
- **The object a write is built on** (`edit.live`) is read inside the write
  path: what reaches the person from it is the write's own answer, reviewed by
  `Perform` with the write's verb, or an action's preview, which is shown only
  once the verb's own questions were allowed.
- **A log stream or an exec already open** is reviewed again while it runs,
  every fifteen seconds from the gate's held answers: a RoleBinding removed
  ends it, in a refusal frame, at most `access.TTL` plus fifteen seconds later.
  A review that could not be made does not end it.

### 5.1 The ServiceAccount's RBAC, and the honest shape of it

The console needs `create` on `subjectaccessreviews.authorization.k8s.io` and
**nothing else that is an authorization verb** — no `impersonate`, no `escalate`,
no `bind`. That part is small and checkable.

The uncomfortable part is everything else. Because the console is the one
reading, the ServiceAccount needs read access to every resource the console
offers, cluster-wide — including `secrets`, because the console shows them
masked; `pods/log`, because it streams logs; `pods/exec`, because the dock execs;
`pods/portforward`, because tunnels are dug through the API server; the metrics
API; events; and discovery. Because the console is the one writing, it needs the
write verbs on everything it may be asked to apply.

**So the ServiceAccount is, in practice, close to cluster-admin minus
impersonation.** That is not a flaw in reviewing per person; it is what it
costs, and the alternative — impersonation — is worse: a ServiceAccount allowed
to impersonate can become anyone, `system:masters` included (§14.1). It is
stated here so that nobody reads "one ServiceAccount plus a review" as though
the account were small.

Two requirements follow, and both are the charts repository's:

- **The list is derived, not written by hand.** A hand-written `ClusterRole`
  drifts the first time a page is added, and it drifts in the direction of a
  `*`. Derive it from the call sites or from a recorded session, and commit the
  derivation, not just its output.
- **A check on the shipped `ClusterRole`** that fails on any of: `impersonate`,
  `escalate`, `bind`, a `*` in `verbs`, a `*` in `resources`. That is a
  four-line test over a YAML file and it is the part of §5.1 that can be
  asserted mechanically.

**RBAC writes are refused under the server profile in this version.** A
person allowed to write a Role or a binding is not thereby allowed to grant
what it names: kube-apiserver checks `escalate` on a Role that grants more than
its author holds, and `bind` on a binding to a role the author does not hold -
**against the identity that sends the request**, which here is the
ServiceAccount. A review of `create rolebindings` for the person asks neither,
so through the console a person with that one right could bind themselves to
`cluster-admin` whenever the ServiceAccount holds it. Until the console reviews
`bind` and `escalate` for the person (planned for 0.2), `access.Gate.Review`
answers every verb but `get`, `list` and `watch` on the group
`rbac.authorization.k8s.io` (roles, clusterroles, rolebindings,
clusterrolebindings) with `not_in_profile` under `--incluster`, before any
review is asked: `Perform` refuses it, so no request is stamped and the
transport guard would refuse one too; the attempt is recorded in the audit log
with that decision and the outcome `refused`. The API answers 403
`rbac_not_in_profile`, and the pages draw the verbs disabled with the same
sentence, "RBAC changes are not available in the server profile in this
version". Creating, editing, applying, patching, deleting and bulk deleting are
all covered, the create and the apply on the kind read from the submitted
document. The desktop profiles are unchanged: there the kubeconfig's own
identity sends the request, and the API server's `bind` and `escalate` checks
are about that same person.

**Not derived here.** The list has to be generated from the call sites rather
than read: `find . -name '*.go' -not -name '*_test.go' | wc -l` gives the number
of files to derive it from.

### 5.2 One choke point, and what "one" has to mean

The review happens at **a single choke point**, never per handler, and
`internal/edit`'s one guarded door is the precedent — that door exists because a
check repeated in forty places is a check missing from the forty-first.

The size of the surface it has to cover: `grep -c "m\.HandleFunc("
internal/api/router.go` → **66** registrations, of which
`grep -c "a\.guard(" internal/api/router.go` → **35** are mutations. Forty-first
is not hypothetical at that width.

**The trap, which looks correct in both halves:** an `apply` that reviews the
kind from the **route** and writes the kind from the **body** is a full bypass.
The reviewed kind is read out of the parsed document.

**The walk:** `internal/access/choke_test.go` type-checks every package of the
module (`go/types`, against `go list -export` data) and fails on a
write-capable call - a client-go method such as `Create`, `Update`, `Patch`,
`Delete`, `Evict`, `UpdateScale`, `Bind`, an exec `Stream`, `ForwardPorts`,
called or taken as a method value; a method of the same name taking the API's
write options (a local interface narrowing a client); a raw REST request sent
with a mutating or non-literal verb - outside a closure handed to
`(*access.Gate).Perform`, identified by type rather than by name (or to a
function that only forwards its closure there, or inside a function only ever
used from one). What it deliberately leaves out is listed in the test with a
reason each: the review itself, the console's own session store and the node
shell's clean-up of its own pods. The in-process tunnels are not among them:
the tunnel is dug inside `Gate.Perform`. **What the walk cannot see** - a write
made through reflection or a method name missing from its list, a file the
current GOOS does not build, a closure whose question is not the request it
sends, and above all another module - **is held at runtime by
`access.Guard`** (§5): a change no `Perform` stamped, or stamped about something
else, is refused before it is sent.

**The same walk holds the reads**: a client call that reads a cluster's objects
- `List`, `Get`, `Watch`, `GetLogs`, a raw REST `GET` - called or taken as a
method value, must sit inside a closure handed to `Gate.Read`, `access.List` or
`access.Each`, or be handed to one in place through a function that only
forwards it (`resources.Listed`). The reads made with the console's own rights
are listed with a reason each - the catalog of kinds, the event archive's
collector, the monitoring backend and the usage gauges' holds, the forward
manager's sweep, the object a write is built on - and every `access.AsConsole`
is listed beside them, so a new one fails until it is named.

**Why the walk and not a test of the gate.** A behavioral test of the choke
point proves the door works; only a structural one — find the calls, assert they
all sit behind the one function — proves there is no second door.

### 5.3 The `resourceNames` gap, narrowed

Filtering by (verb, group, resource) over-grants where a `Role` carries
`resourceNames`. The gap is narrower than it reads, and the narrowing changes
what has to be built:

**RBAC's `resourceNames` does not apply to `list`, `watch`, `create` or
`deletecollection`** — the API server cannot express "may list only these named
objects", so a role restricted by name gives no list access at all. A list
filtered by triple is therefore faithful: there is no role it under-represents.

The gap is real for **`get` on a named object**, where `resourceNames` does
apply and a triple-level yes is too generous. So the requirement is bounded: the
detail read — one object, by name — carries its own review with the name in it,
and the list does not. That is roughly one extra review per drawer opening, not
400 per list.

**Built**: every object read by name - the drawer, the data panel, an
environment variable's ConfigMap or Secret, the Changes panel, a custom
resource - is reviewed with its name, and a list is reviewed without one.

> **Asserted from the RBAC documentation, and the half this tree relies on was
> measured** against docker-desktop with a binding the cluster already has (a
> Role granting `get` on the one ConfigMap `kubelet-config` in `kube-system`):
> the review answers yes for that name, no for another name in the same
> namespace, and no for `list`.

### 5.4 The authorization cache's TTL is a revocation delay

Say it in the operator-facing documentation in these words: **removing a
`RoleBinding` takes effect when the entry expires, not when it is removed.** An
operator revoking access during an incident needs to know whether they have to
wait, and a minute is an acceptable answer only if it is written down somewhere
they will read it.

**Written down:** `access.TTL` is one minute and `access.FailedTTL` ten seconds
(a review that could not be made is held that long and refuses), and
`.env.example`'s single sign-on section says it in these words. One gate serves
the whole process, so the page that offers a verb and the write that performs it
share the held answer - the delay is the same minute for both. The chart's
values documentation is the other place an operator will read it, and is the
charts repository's to write.

---

## 6. The cache — including the two that already exist

### 6.1 There is no object cache; its filter exists

**There is no object cache.** Reads go to the API server's watch cache on every
request (`ResourceVersion: "0"`), with the ServiceAccount.

**The filter does exist**: it is applied to each answer on its way out, by the
same review and the same held answers as a write (§5). So a cache added later is
a change to *where* a list is read from, not to whether it is filtered - the
doors in `internal/access/read.go` take a read closure, and the structural test
fails on a read made outside one. A cache filled with one person's answer and
served to the next is the one shape to avoid: a shared hold is filled as the
console (`AsConsole`) and reviewed per person on the way out, as the namespace
dropdown already is.

### 6.2 The archive is a cache, and nothing calls it one

`internal/archive` keeps "a window of what Kubernetes forgets": events, and
optionally pod logs, written to disk under `KT_STATE_DIR`, seven days by default.
`Defaults()` is `{Enabled: true, Logs: false, RetentionDays: DefaultRetentionDays}`
— so events are archived by default and logs are not.

In the desktop profile this is a feature with no authorization question: the
operator recorded their own reads. In a shared console it is **a persistent,
on-disk copy of cluster state collected with the ServiceAccount's rights, served
back to whoever asks**, and it has three properties that make it harder than the
object cache:

- **It survives the object.** A review answers about a pod that no longer exists
  by evaluating the triple, which is the best that can be done — so an archived
  record of a deleted pod's logs is authorized by *namespace access*, not by
  access to the pod.
- **It survives a `RoleBinding` change** by the full retention window, not by a
  minute.
- **It is the one part of the console's state that an attacker can read without
  the console**, if they get a shell in the pod (§14.2).

**The decision this document asks for and does not make:** in the server profile,
is the archive off, on-and-filtered, or on-and-namespace-scoped? It is not a
detail of a cache; it is a data-retention decision with an operator's name on it.
Until it is answered, the safe default this document asks for is **off under
`--incluster`**, because a safe state should need nothing running to hold; the
code keeps it on where the state directory persists (§15). Under the profile the archive's
settings are the deployment's (`KT_ARCHIVE_*`): no person can change them or
empty the archive through the console (§11).

### 6.3 The AWS credential cache

`internal/cluster/awscache.go` gives each context its own AWS CLI credential
cache, and `~/.aws` is mounted read-write on purpose on the desktop, because the
CLI renews SSO credentials there. The server profile keeps this out — the
console manages only the cluster it runs in and needs no cloud credential to do
it. §12 is where it comes back.

---

## 7. Assumption 1 — the caps are per process, and there are two of them

### 7.1 What the code says

```
$ grep -n "maxOpen = " internal/forward/forward.go
105:	maxOpen = 16
$ grep -n "const maxSessions" internal/terminal/limit.go
35:const maxSessions = 16
```

The first caps port-forwards. The second has the same shape:
`internal/terminal/limit.go` caps terminals and pod execs together at sixteen
for the whole process, deliberately sharing one count between them because they
are the same resource — a WebSocket, a goroutine copying it, and a process or an
SPDY stream on the far side. Sixteen is generous for one operator. It is not
generous divided by ten.

### 7.2 Divide by identity; do not raise

**Divide, do not raise.** Raising the cap to make ten users comfortable would
quietly remove the property it exists for: a stuck loop of clicks ends in a
sentence rather than in forty processes.

So the counter is per-identity, with the process-wide ceiling above it — not one
or the other. Per-identity alone would let ten users open 160.

**Built for terminals and pod execs.** Under `--incluster` each person's share
is `PerPersonSessions` (four) of the sixteen; the shared password account has no
share, because its name is everybody who holds the password, and counts against
the sixteen alone. Port-forwards need no share: the server profile offers none
(§7.3).

**The refusal message changes with the counter.** *"16 terminals are already
open, which is the limit"* told to a user whose panel is empty because the
sixteen belong to somebody else sends them to look at the wrong thing. So there
are two sentences: the ceiling's, and *"you already have 4 terminals open, which
is one person's share of this shared console"*.

**Tested by:** a table-driven test that opens the per-identity limit as alice,
asserts bob is unaffected, then opens the process ceiling across identities and
asserts *both* refusals carry their own sentence. `internal/terminal/limit_test.go`
has the counter's fixture.

### 7.3 The part the cap does not cover: where a forward listens

The package comment in `internal/forward/forward.go` is direct about it:

- with `KT_FORWARD_PORTS` set, a forward listens **on 0.0.0.0**, not on
  loopback, because a published container port cannot be routed to the
  container's own loopback;
- **"every local user on that host can dial an open forward … with no session and
  no password, because a raw TCP tunnel has no front door of its own"**;
- and so can any other container on the same network.

Transposed into a pod, "any other container on the same network" is **any pod in
the cluster that can reach this pod's IP**, unless a NetworkPolicy says
otherwise. A forward opened by alice to a workload alice may reach would be, for
as long as it is open, an unauthenticated TCP path into that workload for
anything in the cluster network.

And the feature would not work anyway: the dock draws `http://127.0.0.1:<port>`
as a clickable link, which resolves in the user's browser to *their own* machine.
In server mode that link points at nothing.

**So under `--incluster` no forward is made** and the three forward operations
are not registered; `capabilities.portForward` is false, and the Ports panel
draws `kubectl port-forward -n <ns> <pod|svc>/<name> <local>:<remote>` to copy,
so the tunnel opens on the viewer's machine with the viewer's own credentials
and the cluster checks the right. The in-process tunnels the extensions and the
Prometheus panel dig are untouched; each is reviewed as `create
pods/portforward` for whoever asked (§5).

**The three options, kept because the other two are the ones to reopen it
with:**

1. **Port-forwards are off under `--incluster`**, `capabilities.portForward`
   false, the control replaced by the command. The one taken: it removes the
   *user-facing* forward and keeps the in-process one.
2. **Forwards bind loopback and the console proxies them**, per session, through
   the authenticated origin. This keeps the feature and puts it behind the front
   door, at the cost of a new proxying path — which brushes against the ban on a
   generic reverse proxy (`docs/DEFINITION-OF-DONE.md` §6) and would have to be
   written so that the browser names a tunnel id, never a host and port.
3. **Forwards bind loopback and the link is honest about being unreachable.**
   Cheap, and it ships a control that visibly does nothing, which is worse than
   no control.

Whichever is taken, **`KT_FORWARD_PORTS` must not be set in the chart's default
values**, and a check on the chart asserting that is the test.

### 7.4 The desktop's cookie, and every other port on loopback

A desktop section in a server-mode document, because it is the forward's other
half and because the server profile does not have it.

**A loopback session cookie is shared across ports.** RFC 6265 §8.5: cookies do
not isolate by port, so the session cookie the desktop console sets on
`127.0.0.1:5823` is sent by the browser to **every** service on `127.0.0.1` -
another developer server, a port-forward the console itself opened, anything
another local user started. Whatever answers there receives a cookie that opens
every cluster in the operator's kubeconfig. The cookie is `HttpOnly` and
`SameSite=Lax`, which stops a page's script from reading it and a cross-site
POST from carrying it; neither stops a browser sending it to a different port on
the same host, because to a cookie that *is* the same host.

What is done, and what is not:

- **The console's own forwards are written under the other spelling.** The
  Ports panel links a forward as `localhost:<port>` when the console is on
  `127.0.0.1`, and as `127.0.0.1:<port>` when it is on `localhost`, so the click
  the operator makes to look at a pod does not hand that pod the session. Held
  by `web/ui/src/pages/object/Ports.loopback.test.tsx` and its counterpart in
  `Ports.test.tsx`.
- **A service the operator opens by hand on the console's own host still gets
  the cookie**, and so does anything another local user runs on a port of
  `127.0.0.1`. On a single-user machine that is the operator's own software. On a
  multi-user host it is somebody else's, and the cookie then crosses a user
  boundary. **An accepted risk: neither fix is built.** The two that would close
  it are binding the session to the connecting OS user (on Linux, the peer's uid
  read through `/proc/net/tcp*`) and asking for a per-tab secret beside the
  cookie. The reasoning: loopback cookies are shared across ports, and so across
  local users, by the browser's own rules (RFC 6265 §8.5), not by anything the
  console does; and the desktop profile is treated as single-user - one
  operator, their own kubeconfig, their own machine (§1). Somebody running the
  desktop console on a host shared with other local users is outside what it
  defends against, and the server profile is the one built for several people.
- **Not observed in a real browser.** The behavior above is RFC 6265's and Go's
  `cookiejar`'s; Chrome, Safari, Firefox and the two native windows have not
  been checked.

---

## 8. Assumption 2 — the session is stateless, so nothing revokes one

### 8.1 The exact problem

The shared password's session is a signed assertion and nothing else. There is
no store, so there is nothing to delete. The levers that exist are:

| Lever | Effect | Who it hits |
|---|---|---|
| Rotate `SessionKey` | immediate | **everybody** |
| Change `KT_PASSWORD_HASH` (or `--reset-password` on the desktop) | immediate: a shared-account session is signed with a key derived from the session key and the password hash | everybody signed in **with the shared password or a launch code**; single sign-on sessions are untouched |
| Lower `KT_SESSION_MAX` | ends sessions older than the new cap | everybody, by age |
| Wait | up to 30 days idle, 90 days absolute, on the defaults | nobody |
| `POST /api/auth/logout` | clears the cookie in **that** browser | the cooperative user |

None of those is "sign alice out". In a shared console that is not an
inconvenience; it is the missing half of offboarding. Somebody leaves, their
`RoleBinding` is removed, and — after the authorization cache's minute — they can
still reach the console and still hold every open socket; what they can still
read is whatever the console serves that is not behind a fresh review. Which is
exactly why §5.2's choke point is on every read path and not only on writes.

### 8.2 What the server profile needs

A **server-side session store**, where the refresh token, if there were one,
would never leave it.

**Built for single sign-on sessions** (`internal/auth/store.go`): in memory for
one replica, and in the shared, sealed Valkey store under several (§8.3). The
shared password's session stays the stateless cookie of §8.1, deliberately: it
is the desktop's, and a desktop signed out by every restart would be worse.
Revocation is per session id, and a person's own sign-out ends their session and
nobody else's (`TestSigningOnePersonOutLeavesTheOtherSignedIn`). **Not built:**
an operator-facing "sign alice out". The refusal frame inside an open stream is
built (§8.3).

- The cookie carries an opaque session id, signed as before. The machinery
  stays: the version prefix, the constant-time compare, the sliding window with
  an absolute cap, the one place the flags are written, the rate-limited
  `Set-Cookie`. **All of it is still needed** — a store makes revocation
  possible, it does not make an unsigned cookie safe, and the rate limit is worth
  more with a store behind it, not less, because each refresh costs a write.
- The store holds the subject, the groups as of sign-in, and the fingerprint of
  the mapping that named them (§4.2).
- **Revocation is per session id**, and there must be an operator-facing way to
  use it. A revocation mechanism nobody can invoke is a field in a struct.

### 8.3 The three parts that will be got wrong

- **Where the store lives.** In memory means a pod restart signs everybody out
  and two replicas disagree about who is signed in; and the charts repository is
  where replicas are decided. **The console cannot promise revocation across
  replicas without knowing what the chart deploys.**

  So the store is an interface with two implementations
  (`internal/sharedstate`): this process's memory, for one replica, and
  **Valkey**, which the chart deploys whenever `replicaCount` is above one. It
  carries the sessions and the confirmation plans. Sign-ins in progress are not
  in it: they are a sealed cookie in the browser (§4.2), because a public route
  writing into the shared store would let an anonymous flood evict the plans and
  the sessions beside them. A record expires in the store when its cookie
  would: the idle window from the last refresh, never past the cap. **Every
  value is sealed** with a key derived from the session key
  (`sharedstate.Sealed`), because the store's own password is not the boundary:
  whoever could write a record could otherwise rewrite their own groups, and the
  review would carry them to every question. A store that does not answer is a
  503 naming it, never "sign in again" - **and so is a sign-out it did not
  record**: the answer says the session is still live, and the cookie is
  **kept** so the browser can sign out again once the store answers; a cleared
  cookie on a live session would be a browser that looks signed out with nothing
  left to retry with. Lifetimes are sent to Valkey as durations (`PX`,
  `PEXPIRE`), never instants: its clock is not the pod's, and a difference
  between them would expire keys before their time. A password Valkey refuses
  stops the process at start-up, naming `KT_VALKEY_PASSWORD`, rather than
  reading as a store that "did not answer yet".

  **The plans have a ceiling.** A plan keeps the request it was minted from - a
  manifest, whole - for two minutes, so a signed-in person previewing in a loop
  could fill the store the sessions live in. Each replica counts the plans it
  put there and refuses past 512 plans or 64 MiB, and under the server profile
  past one person's share of 32 plans or 16 MiB (none for the shared password
  account, whose name is everybody holding it), with a 409 `limit_reached`. The
  store therefore holds at most replicas x the ceiling in plans. The values are
  the operator's to confirm.

  **What several replicas do not share, and so do not offer**, decided at
  registration rather than refused in a handler: the preference writes, the
  per-cluster extension switches and the settings writes, each a file in one
  pod's state directory; port-forwards, each a listener in one pod; and the
  archive, which would be two halves. `KT_REPLICATED` is how the deployment
  says there are several, and the process refuses to start without the store,
  the server profile, `KT_SESSION_SECRET`, and - where the shared password is
  offered - `KT_PASSWORD_HASH`, each of which would otherwise differ per pod.
  The archive is forced off there **whatever a settings file on the volume
  says** (`archive.Options.ForceOff`), and the settings page draws none of
  the writes that are not registered.
- **Groups go stale.** The groups the console asserts to the API server come
  from the token at sign-in. A group removed at the provider keeps working until
  the session ends and the person signs in again — a second revocation delay,
  longer than §5.4's minute and independent of it: at most the single sign-on
  cap, twelve hours by default.
- **Revocation does not close a socket by itself.** A stream is authenticated at
  the upgrade (§4.1). Tearing a WebSocket down from inside the auth middleware
  closes it with no status the browser can report — a terminal that dies with
  no status at all. So the check sits *inside* the stream loop, where a refusal
  can be sent as a frame the pane can render.

  **Built for the pod exec and the log follow.** The front door gives every
  request a way to ask later whether its sign-in still stands (`auth.Standing`,
  `identity.Standing`): the store's record and the cap, not the opening
  cookie's idle deadline, which the browser's other requests have moved on
  since. An open exec or follow asks it every fifteen seconds, with the review
  of its own question, and ends in its own frame on a no - a `state` frame on
  the terminal socket, an `error` event or a `forbidden` state on the log
  stream. A store or a review that did not answer is not a no. Not covered: a
  node shell, a local shell and the assistant, none of which the server profile
  registers.

**Tested by:** revoke a session id, then assert three separate things — a new
request with that cookie is refused; a new upgrade with that cookie is refused;
and an already-open stream receives a refusal frame within one interval. Three
assertions because the third is the one that will regress silently. The third
is `TestAnOpenExecEndsWhenItsSignInEnds` and `TestAFollowEndsWhenItsSignInEnds`,
over `TestASignedOutSessionNoLongerStands` at the front door.

---

## 9. Assumption 3 — the local shell, and what "off" has to mean

### 9.1 Two guarantees that are not the same guarantee

There are two ways to turn a route off:

- **Not registering the route** is a property of the router. It is true for every
  method, every header, every path-matching subtlety, and it is **visible by
  walking the router** — `mux.Handler(req)` returns the pattern that would serve
  a request, and an empty pattern is a statement about the whole router.
- **Refusing inside the handler** is a property of one code path. It is true only
  where the condition is evaluated, it has to be re-established for every early
  return added later, and no walk of the router can see it. A test of it is a
  behavioral test that must never regress, and a behavioral test passes
  vacuously the day somebody adds a second path into the same handler.

**The server profile uses the first form.** Under `--incluster`, the local-shell
routes are never registered, because that is the only one of the two that a test
can establish negatively: *no pattern serves this address* is a statement about
the whole router; *this handler refuses* is a statement about one function.

Where the code is:

- `internal/launch/shellupgrades.go` registers the three `/kt-api/` upgrades,
  which the SPA does not use - `/kt-api/terminal`, `/kt-api/exec`,
  `/kt-api/claude` - and `mountShellUpgrades` returns before registering any of
  them under `--incluster`. It is a function rather than lines in `main` so that
  a test can walk what it mounted (`TestTheServerProfileMountsNoShellUpgrade`).
- The node shell is the socket channel `/api/c/{cluster}/ws/nodeshell`,
  registered where `Channels.Offers(ChannelNodeShell)` says so, and its preview
  and setting are registered under the same condition — `offersNodeShell` in
  `internal/api/handlers_channels.go`
  (`TestTheNodeShellPreviewAndSettingFollowTheChannel`). No `/kt-api/nodeshell`
  is registered on either profile.
- `POST /api/dock/terminals` is registered only when `Deps.InCluster` is false
  (`TestTheServerProfileRegistersNoLocalShell`). On a desktop that cannot open a
  pseudo-terminal it still exists and refuses, deliberately: the profile removes
  routes and never changes the desktop.
- `grep -rn 'Handle("/kt-api' --include=*.go . | grep -v _test | wc -l` → **3**.

### 9.2 What the condition is standing between

`/kt-api/terminal` serves a pseudo-terminal **on the machine this process runs
on**. In a pod, that machine is the pod, and the shell it opens inherits the
process's environment — the mounted ServiceAccount token, the kubeconfig built
from it, the state directory, the archive, and whatever
`internal/terminal/readonly.go`'s `kubectl` shim has or has not managed to put on
the PATH. Measured on a real deployment: a shell opened this way read every
Secret in the cluster while the chart's `rbac.exec` and `rbac.write` were both
false. RBAC bounds what the pod may do; this shell *is* the pod.

The read-only fence is a `#!/bin/sh` file named `kubectl` on the session PATH,
and the package comment records that Windows walks straight past it via
`PATHEXT` — a guardrail that is known to be bypassable on one platform is not
the thing to put between a shared console and cluster-admin. The test of that
condition would be what stands between a shared console and a shell holding the
pod's ServiceAccount, so §9.1's answer is to remove the condition rather than to
test it harder.

### 9.3 The three tests, and why three

1. **Router walk.** Build the process's router under `--incluster` and assert
   that `mux.Handler` returns an empty pattern for `/kt-api/terminal`,
   `/kt-api/exec`, `/kt-api/nodeshell` and `POST /api/dock/terminals`, for
   **every** method, and for the trailing-slash and case variants. This is the
   load-bearing one.
2. **Structural.** A `go/ast` test asserting that the constructor for the shell
   layer is called from exactly one place and that place is behind the profile
   switch — the same shape as the ban on `httputil.ReverseProxy`
   (`docs/DEFINITION-OF-DONE.md` §6). Without it, the router walk stays green
   while the layer is constructed and reachable some other way.
3. **Capability.** `GET /api/capabilities` reports `localShell: false`, so the
   SPA draws no control. This one is cosmetic and is listed third on purpose: a
   capability flag is what the UI reads, never what makes a thing safe.

**What none of the three covers**, said rather than discovered: `ErrNotInProfile`
stays in `internal/api` because the desktop profile still needs a handler that
refuses when there is no terminal layer at all. A reviewer seeing that path can
conclude the profile is defended by it. It is not. The router walk is.

---

## 10. Assumption 4 — the assistant control

**The server profile ships no assistant control at all. Absent, not disabled.**

So: **what an attacker gains through this path is nothing, because the path is
not built.** The thing to protect is that it stays unbuilt, and that is a
router-walk assertion — `/kt-api/claude` and `/api/ws/assistant` return an empty
pattern under `--incluster`, asserted the same way as §9.3's first test — rather
than a behavioral test of a condition that must never regress.

### Why this one got absence and the local shell got a condition

They are defended differently on purpose, and the difference is worth a
paragraph because it will otherwise read as inconsistency.

The local shell **exists in the desktop profile and must keep existing**.
Whatever the server profile does, `internal/terminal`'s shell code stays in the
binary, stays constructed on one path, and stays reachable on that path — so the
server profile's guarantee is *"this router does not reach it"*, which is §9.3's
three tests and a standing obligation.

The assistant control has a second reason to be absent that has nothing to do
with credentials: **it runs on one subscription.** `internal/terminal/claude.go`
locates the binary with `findClaude`, hands out workspaces from `claudeSlots`,
and reads its credentials from `CLAUDE_CONFIG_DIR` — one directory, one account,
one set of credentials on the pod's filesystem. In a shared console that is one
person's account serving everybody: their quota, their rate limit, their
conversation history, their terms of use, and their name on whatever anybody
asks it to do. No technical guard fixes that. A per-user credential would, and
that is a different product decision rather than a mitigation.

The absence can be revisited. The shape that would make it safe is an assistant
that runs with *the viewer's own* credentials and *the viewer's own*
subscription. That is not a smaller version of what exists; it is a different
feature, and it would need its own pass of this document.

---

## 11. Per-user state: there is none, and most of it does not want to be

The console's own state — rail order, hidden clusters, column widths and
densities, nameplates, hotbars, the drawer, the archive's settings, and which
additive pages are ticked per cluster — lives under one `KT_STATE_DIR`
(`internal/statefile/statefile.go`), written by one process, with no notion of
who asked. `grep -c 'HandleFunc("POST /api/preferences' internal/api/router.go`
→ **12** preference writes.

Three different answers are needed and they are not the same question:

- **Per-user, because it is a preference.** Column widths, rail order, density,
  hidden clusters. Shared, these are ten people fighting over one layout.
- **Shared, because it is configuration.** The archive's retention, the node
  shell's image. Shared is correct; **who may change it** is not a preference
  question, and in a shared console it is an administrative action. Under
  `--incluster` neither `PUT /api/settings` nor `POST /api/settings/clear` is
  registered: the deployment's `KT_ARCHIVE_*` decides the archive's settings,
  and a person who must empty the archive is an operator with the volume.
- **Shared, and load-bearing.** Which additive pages are ticked per cluster
  (`on[cluster][extra]`; a new context starts with nothing enabled, on every
  profile). This is a per-cluster fact, not a per-person one, and it stays
  shared. **It has an administrative role behind it**: under `--incluster` the
  write is registered with one replica and its state volume, reviewed as the
  verb `use` on `extras.kubetower.dev/<key>` for the person signed in, refused
  without it, and recorded in the audit log (act `extension`, with `change`
  saying on, off or which setting, and what it was); the chart's values
  (`KT_EXTRAS`) say which extras may be turned on at all. An extra's own
  settings are written on the same terms. **The same `use` reaches an extra
  that is on**: under `--incluster` every route of an extra is reviewed for it
  after the per-cluster switch, so a person without it is answered 403
  `rbac_denied` naming the permission (503 `access_unreviewable` where the
  review could not be made), and the menu leaves the extra out for them. The
  route is the control, the menu the courtesy; the shared password account is
  reviewed as the console's ServiceAccount, as for the switch.

**Several replicas.** Where the console runs as more than one replica
(`KT_REPLICATED`), the preference writes, the extension switches and the
settings writes are **not registered**, because each is a file in one pod's
state directory and a width saved through one pod would be drawn one request in
two. Every replica draws the defaults, `capabilities.preferences` says so, and
the front end draws no rail arrangement and keeps each person's columns in their
own browser.

**The server profile, on one replica too.** Under `--incluster` the twelve
preference writes are not registered either - the chart's default is one
replica - because there they would be the first bucket shared: one person's
nameplates and hidden tiles would become everybody's rail, and one person's
column layout everybody's table (`TestTheServerProfileRegistersNoPreferenceWrite`).
`capabilities.preferences` is false, so the rail draws no *Arrange* and no bar
switcher (the profile serves one cluster), and the tables keep each person's
columns and density **in that browser's own storage**, sending no request.
`capabilities.replicated` is what the settings page reads. **And the preference
files are not read** under the profile or under several replicas: the rail,
nameplate and column stores are the process's own memory there, so a file left
on the volume by a console that had the writes is not frozen into everybody's
defaults. The files stay on disk, unread; nothing rewrites them.

**The decision this document does not make:** whether the server profile gets
per-person preferences kept by the server, keyed by subject. That is still the
feature this section describes, and it is not built.

**Tested by:** a test that writes a preference as alice and reads it as bob,
asserting bob sees the default, if per-person preferences are built; and a
router walk asserting the administrative routes are absent under `--incluster`
(`internal/api/profileroutes_test.go`).

---

## 12. Remote-cluster credentials

**Out of scope**: the server profile manages only the cluster it runs in, so
there is no kubeconfig, no cloud credential, and no exec credential plugin in the
pod. This section exists so that the decision is not silently reversed.

If multi-cluster comes to the server profile, three things come with it:

- **A kubeconfig in the pod is a credential at rest**, readable by anything with
  a shell there (§14.2) and by anything that can read the Secret it is mounted
  from. The console's *own* cluster is reached with a ServiceAccount and needs
  no such file; a second cluster reached with a static token or a client
  certificate has no review behind it at all, and the whole of §5 evaporates for
  that cluster.
- **The AWS CLI's credential cache is written at runtime**: `~/.aws` is mounted
  read-write on purpose, and the assume-role cache corrupts itself, silently,
  under two concurrent writers. A writable credential directory in a shared pod
  is a shared, writable credential directory.
- **Loopback clusters** become meaningless and possibly dangerous: a context
  pointing at `127.0.0.1` in a pod is the pod's own loopback, not the operator's
  cluster.

The requirement, for when it comes: **either every cluster is reached with a
credential that supports a review, or the profile says out loud which clusters
are unreviewed and refuses to offer writes on them.** Offering the same UI over
both is the failure.

---

## 13. The audit log

The record is written at the same choke point as the review, because that is the
only place holding the identity, the verb, the resource and the decision at
once. Repeated here only where the threat model adds something:

> **The identity on the process log.** Every `write:` line ends with
> `who="<name>" via=sso`, `who="<name>" via=password` (the shared account, which
> names nobody), or `who=- via=unattributed` where no session reached the write.
> The name comes from the session the guard verified, and never from a header.
> `internal/edit/writelines_test.go` walks the tree and fails on a `write:`
> literal that is not the format of the Writer's `Logf(ctx, …)` - which proves
> no `write:` line is written another way, and not that every change writes one.
> **The whole rendered line has its control characters escaped**
> (`identity.Line`), because the name is not the only thing on it a person
> chooses: a manifest's field paths are the keys of the YAML they sent, and a
> key holding a newline would otherwise forge a second line attributed to
> somebody else. **Exec and port-forward are attributed too** - `exec
> opened`/`exec closed` and both `port-forward … open` lines end with the same
> fields - since a pod shell is the most dangerous thing the profile hands out.
>
> **The record itself is not those lines.** `internal/audit` writes it to
> **stdout, one JSON object per line**, and `access.Gate.Perform` writes it - so
> every write, exec and port-forward the choke point lets through or refuses is
> recorded without a caller remembering to. **Two records per act**, sharing a
> random `id`: the `attempt`, after the review and before the request is sent,
> naming who (`who`, `via`), the question (`verb`, `group`, `resource`,
> `subresource`, `namespace`, `name`) and the review's `decision`; and the
> `outcome` - `applied`, `refused` or `failed` with a one-word `reason` -
> repeating every field, so either half answers *who did what* alone. The
> `reason` is one of the API server's own StatusReasons (`Conflict`, `NotFound`
> …), `unsent` for a request the guard held back because no review covered it -
> **only that**, so it stays usable as a security signal - `held` for a
> reviewed request that never left for an ordinary reason (its time ran out
> before it was handed to the client, the cluster's rate limit held it, no
> connection was made; `access.Delivery`), `unrecorded`, `timed-out` for one the
> API server answered without finishing it (a 504 `Timeout`, a 500
> `ServerTimeout`, or any 5xx carrying `Retry-After` that client-go asked again
> after, even with something else back after it) - answered, not refused: the
> change may still happen; on a dry run, `dryRun: true` beside it says nothing
> changed - `cancelled` for one sent and cut by a context of the console's
> before its answer - its own deadline, or the request it serves ending, a
> browser that went away - `unanswered` for one sent whose connection ended
> before an answer, or `error` - never a webhook's free-text reason, which can
> quote the value it refused. A dry run is recorded with `dryRun: true`. **A
> forced delete** - a delete sent with a grace period of zero, the console's
> "Force delete", so the object's own `terminationGracePeriodSeconds` is not
> waited for - is recorded with `force: true` on both records, and the field is
> absent on every other act; the process log's `write:` line for it, applied,
> refused or failed, carries `force=true` the same way. **An act the console
> makes for itself** (`AsConsole`: its own Prometheus tunnel) is reviewed as the
> console and recorded as `via: console`, the person whose page caused it as
> `onBehalfOf` and never as `who`. **An attempt that cannot be written refuses
> the act** (`access.Unrecorded`, a 503 titled "Audit record could not be
> written") and nothing is sent; an outcome that cannot be written is said in
> the process log and changes nothing, because the request has been made. An
> attempt with no outcome is an act whose end was not recorded, and a reader
> must assume the request was sent. A reveal is recorded the same way, its `key`
> named and the attempt written before the value is read. A Helm release's
> supplied values are such a reveal: the release page carries only whether there
> are any, and `GET .../helm/{namespace}/{name}/values` reviews and records
> `get secrets/sh.helm.release.v1.<name>.v<revision>`, key `values`. Its
> rendered notes are the same reveal under the key `notes` (`GET .../notes`):
> charts print generated passwords in their `NOTES.txt`, so the page carries
> only whether there are any. A server-profile gate wired without a log refuses
> every act. **A debug container** is recorded as the write it is (`patch
> pods/ephemeralcontainers`, on the pod) with its `image`, its `target` and the
> `container` name it was given, and the shell opened in it as an `exec`
> carrying the same three; a cluster in `KT_READONLY_CLUSTERS` refuses it before
> anything is sent, recorded with decision `read_only`.
>
> **A write that fails, comes back short, or has not returned within five
> seconds breaks the log for the rest of the process**: it may have left half
> a line the next record would be glued to, or still be in flight, so nothing
> is written after it and every act is refused, each refusal saying why in the
> process log. **The replica is then not ready** - `/readyz` and `/kt-healthz`
> answer 503 naming the broken log - so its Service sends traffic to replicas
> that still record; `/healthz`, the liveness probe, is untouched, because a
> restart recovers the log and whether to restart is the operator's call.
> **Every field is bounded** - a name, a namespace or a key at 253 bytes,
> anything else at a kibibyte, a cut value marked with how much was cut - so one
> request cannot write a record a container runtime splits into partial lines.
> The C1 controls, the bidirectional controls and the direction marks are
> escaped, so `kubectl logs` shows a record in the order it was written.
>
> The `write:`, `exec` and `port-forward` lines stay in the process log, for
> an operator reading along; they are not the record. A refused or failed
> `write:` line names its record (`audit=<id>`); the `applied` line, written
> by the caller, does not.
>
> **What the record does not cover**: a tunnel's close (the outcome is whether
> it opened), the node shell's clean-up of its own pods (housekeeping, §5.2),
> an exec refused for the session ceiling rather than by the review, and the
> desktop, which keeps no record - one operator on their own kubeconfig, whose
> stdout under `-H windowsgui` fails every write. **Not checked on a bench**:
> that the deployment's log collection reads the container's stdout. JSON lines
> on stdout is what every common collector reads by default; whether *this*
> cluster's does is the operator's to confirm.

- **It is what pays for reviewing rather than impersonating.** The API server's
  audit sees `system:serviceaccount:…` for every write and cannot say who asked.
  That record lives in this process because impersonation is not used.
- **Same choke point as the review**, because that is the only place holding the
  identity, the verb, the resource and the decision at once.
- **The record carries the outcome, not only the intent.** A log saying *alice
  asked to delete* without saying whether it happened does not answer the
  question it will be asked.
- **Never a manifest body and never a secret value.** `internal/edit` masks
  secret values behind an explicit gesture and an audit that copies them defeats
  that masking through the back door. The reveal endpoint deliberately takes the
  *key* in the query and returns the *value* only in the body, because *"a secret
  in a URL is a secret in an access log"* — the audit log must not undo that by
  recording query strings wholesale.
- **stdout, structured**, because an audit log inside the audited process on a
  volume that process can rewrite is evidence held by the accused.
- **A failed write refuses the action**, which makes the logging path a hard
  dependency of every write. That is accepted knowingly.

**What it does not do, stated because an audit log invites over-confidence:** a
compromised process does not write records it does not want written. stdout
shortens the window — the records already emitted have left the pod — but the
log is a record of what the console did while it was still the console. It is
detection, not prevention, and §14 is about the case where it has stopped being
either.

**Tested by** (`internal/access/record_test.go`, `internal/audit/audit_test.go`,
`internal/web/revealaudit_test.go`, `internal/web/helmreveal_test.go`): a run
over an allowed, a denied, a failed and a dry-run act asserting one attempt and
one outcome each, joined by an id no other act has, with the decision *and* the
outcome, and no trace of the API server's message; a write whose attempt record
fails is refused and the fake API server sees nothing; a reveal whose record
fails reads nothing, and a recorded one names the key and never the value; a
name holding a newline or a bidirectional control stays one record; a console
act recorded as the console's; a webhook's free-text reason kept off the record;
a 409 on a patch recorded as `Conflict`; a megabyte name cut at its bound; a
stalled, a failed and a short write each breaking the log; C1 controls and
direction marks escaped; an exec refused for its record never logged as opened;
a node-shell pod not created when its record fails. What is not tested is a
record built from an object carrying secret data, because `audit.Record` has no
field a value could be put in.

---

## 14. What an attacker gains by compromising the pod

"Compromising the pod" means: arbitrary code execution inside the console's
container, however arrived at — a dependency, a bug in the console, or a shell
the profile forgot to turn off.

### 14.1 The ServiceAccount, immediately

`/var/run/secrets/kubernetes.io/serviceaccount/token`, mounted by default, no
console code involved. With it, everything §5.1 lists: read every `Secret` in
every namespace, read and write every resource the console can apply to, exec
into pods, read logs. **This is not mediated by the review**, because the review
is a thing the console chooses to do — the token works against the API server
directly.

So the blast radius of pod compromise is *the ServiceAccount's RBAC*, and
nothing in the console reduces it. Two consequences:

- **The `ClusterRole` is the boundary** — the only one. §5.1's derivation is
  therefore not tidiness; it is the size of this paragraph.
- **`automountServiceAccountToken` cannot be turned off**, because the console
  needs the token. The mitigation that exists elsewhere does not apply here.

What it does **not** get: `impersonate`. Reviewing rather than impersonating has
one real defensive win: a compromised pod cannot become `system:masters`. It can
do a great deal as itself; it cannot become anyone else, and it cannot mint
credentials for a third party that outlive the pod. The check in §5.1 that fails
on `impersonate` in the shipped `ClusterRole` is the test of that.

### 14.2 Everything on the pod's filesystem

- **The archive** (§6.2): up to seven days of events, and pod logs if they were
  turned on, for the whole cluster, already collected.
- **The session signing key**, in the process and wherever the deployment keeps
  it (`KT_SESSION_SECRET`, or the state directory). With it, mint a valid
  cookie for any subject — which means *asking the API server about any
  subject*, and the API server answering faithfully. This is the one place where pod compromise gives more than the
  ServiceAccount does: it gives the ability to act as a named user, with that
  user's name in the audit log.
- **The state directory** generally: preferences, settings, which pages are
  ticked.
- **The assistant's credentials**, if the control were present. It is not (§10),
  and this is one of the reasons.
- **A kubeconfig and a cloud credential cache**, if multi-cluster ever comes to
  the profile (§12).

### 14.3 The network position

A pod inside the cluster is a better place to stand than the ingress. From it:

- every in-cluster service the NetworkPolicy allows, which by default is all of
  them;
- **every open port-forward**, which listens on 0.0.0.0 when `KT_FORWARD_PORTS`
  is set (§7.3) — though from inside the pod that is redundant;
- the ability to **assert an identity header**, which is why §4.2 requires the
  console to verify the ID token itself rather than trust a header the ingress
  was supposed to be the only source of.

### 14.4 What limits it, honestly

Three things, and they are smaller than one would like:

1. **No impersonation** (§14.1).
2. **The audit records already emitted have left the pod** (§13).
3. **The session cap is real.** A cookie stolen before the compromise still dies
   at `issued + max`, and lowering `KT_SESSION_MAX` ends it sooner. A cookie
   *minted* after the signing key is taken does not — which is why key rotation
   is the recovery step, and why it signing everybody out is the correct
   behavior rather than a defect.

**Recovery, so it is written down before it is needed:** delete the pod, rotate
the ServiceAccount token, rotate the session signing key, rotate anything in the
state directory, and treat every `Secret` the `ClusterRole` could read as
disclosed. The last is the expensive one and it is the reason §5.1 asks for a
derived rather than a hand-written role.

---

## 15. What the server profile does not offer, and how each is enforced

The addresses below are the ones the binary registers.

| | Under `--incluster` | Enforced by | Tested by |
|---|---|---|---|
| Local shell, `/kt-api/terminal` | **absent** | route not registered — `mountShellUpgrades` returns before registering any of the three | `TestTheServerProfileMountsNoShellUpgrade` |
| Local shell, SPA (`POST /api/dock/terminals`) | **absent** | route not registered — `if !a.deps.InCluster`, `internal/api/router.go` | `TestTheServerProfileRegistersNoLocalShell` |
| Local shell, socket (`/api/c/{cluster}/ws/terminal`) | **absent** | channel not registered — `Offers` is false for the profile | router walk |
| Pod exec, `/kt-api/exec` | **absent** | route not registered — it goes with the other two, and §9.2 says why | `TestTheServerProfileMountsNoShellUpgrade` |
| Pod exec, socket (`/api/c/{cluster}/ws/exec`) | **present**, reviewed for the person as `create pods/exec` on that pod; **Shell drawn only where the review says yes** | the review (§5), and the API server's own refusal of the ServiceAccount's `pods/exec`; the control is drawn from the pod's page (`DetailPage.shell`, `internal/web/access.go`) | `TestExecIsNotDrawnWhereTheChannelIsNotRegistered`, `TestTheReviewAsksTheAPIServerAboutThePerson` |
| Debug container, socket (`/api/c/{cluster}/ws/debug`) | **present** wherever exec is, reviewed for the person as `patch pods/ephemeralcontainers` **and** `create pods/exec` on that pod before anything is added; refused before the upgrade, with its record, on a cluster in `KT_READONLY_CLUSTERS`; **Debug drawn only where both reviews say yes** | the two reviews (§5), the PATCH through `Gate.Perform` and the shell through the exec path, and the API server's own refusal of the ServiceAccount's `pods/ephemeralcontainers`; the control is drawn from the pod's page (`DetailPage.debug`, `internal/web/access.go`) | `TestBothDebugReviewsAreAskedAndARefusalIsRecorded`, `TestADebugOnAReadOnlyClusterIsRefusedWithNoCall`, `TestTheDebugOfferIsReviewedOnBothQuestions` |
| Node shell, socket (`/api/c/{cluster}/ws/nodeshell`), its preview and its setting | **absent** | channel not registered — `Offers` is false for the profile; the preview and the setting follow it | `TestTheNodeShellPreviewAndSettingFollowTheChannel` |
| Assistant control (`/api/ws/assistant`, and `/kt-api/claude`) | **absent** (§10) | neither route registered | router walk |
| Port-forward (`/api/forwards`, all three operations) | **absent**, and the page shows the `kubectl port-forward` command instead (§7.3, option 1) | routes not registered — `if !a.deps.InCluster`, `internal/api/router.go` | router walk, `TestTheServerProfileSaysShowTheCommandForPortForward` |
| Multi-cluster | **absent** | one context, `in-cluster`, from `rest.InClusterConfig()` — `KUBECONFIG` is not read (`internal/cluster/incluster.go`) | `TestTheServerProfileReachesItsOwnClusterWithoutAKubeconfig` |
| Settings writes and clear | **absent** — the deployment's `KT_ARCHIVE_*` decides (§11) | routes not registered | `internal/api/profileroutes_test.go` |
| Preference writes | **absent** — columns kept in each browser (§11) | routes not registered | `TestTheServerProfileRegistersNoPreferenceWrite` |
| Archive | **on, events only, where the state directory persists** (`archive in /var/lib/kubetower/archive (events true, logs false, 7 days)` on a pod's start-up log). **Absent where the deployment says the directory does not persist.** Whether it should be on under the profile at all is §6.2's open decision | `KT_STATE_PERSISTENT=false`: the archive is not opened, `archive` answers `absent` | `internal/api/profileroutes_test.go`, `internal/web/persistence_test.go` |

### The probe that does not establish absence

**On this surface a 200 does not mean a route exists.** The SPA is served from a
catch-all, so every unregistered address under `/kt-api/` returns the
single-page document — byte for byte, including a control nobody ever wrote a
handler for:

```console
$ for p in exec terminal claude nodeshell nonsense-xyz; do
    curl -s -b c.txt "http://127.0.0.1:8824/kt-api/$p" | sha256sum; done
b8625d36baab6baa8ecb9b38a53301a299d34c3df10c0714f6298a918016c2d2  # exec
b8625d36baab6baa8ecb9b38a53301a299d34c3df10c0714f6298a918016c2d2  # terminal
b8625d36baab6baa8ecb9b38a53301a299d34c3df10c0714f6298a918016c2d2  # claude
b8625d36baab6baa8ecb9b38a53301a299d34c3df10c0714f6298a918016c2d2  # nodeshell
b8625d36baab6baa8ecb9b38a53301a299d34c3df10c0714f6298a918016c2d2  # nonsense-xyz
```

All five equal `GET /`. A **registered** address answers with its own handler
instead, and an unregistered one under `/api/` answers the contract's 404:

```console
$ curl -s -b c.txt 'http://127.0.0.1:8824/api/c/in-cluster/ws/exec'
namespace and pod must be Kubernetes names              # 400, its own handler

$ curl -s -X POST -b c.txt -H 'X-KubeTower-Request: 1' \
    'http://127.0.0.1:8824/api/dock/terminals'
{"title":"Not found","status":404,
 "detail":"no operation at POST /api/dock/terminals","code":"not_found"}
```

**So §9.3's router walk is the check and curl is not.** That is §9.1's argument
about handlers, one level further out: a probe that cannot distinguish *absent*
from *served by something else* establishes nothing, and reasoning from the code
gets rows wrong that a curl against the pod would agree with.

The third and cosmetic check of §9.3, on a pod on the chart's defaults:

```console
$ curl -s -b c.txt http://127.0.0.1:8851/api/capabilities
{"mode":"shared","localShell":false,"podExec":false,"nodeShell":false,
 "assistant":false,"portForward":false,...}
$ curl -s -X POST -H 'X-KubeTower-Request: 1' -b c.txt http://127.0.0.1:8851/api/forwards
{"title":"Not found","status":404,"detail":"no operation at POST /api/forwards",...}
```

`podExec` says only whether the build can open a shell in a pod at all — with
the chart's `rbac.exec: false` the ServiceAccount cannot
(`kubectl auth can-i create pods --subresource=exec --as=system:serviceaccount:…
-A` → `no`). Whether Shell is drawn on a pod is that pod's page
(`DetailPage.shell`), asked for the person and, where they may, for the
console's own ServiceAccount: with `rbac.exec: false` a person who may exec sees
Shell disabled with a sentence naming that switch, instead of no Shell at all.

One row in that table is an open decision, and it is what §16 is for. A table
with a plausible answer in every row would be the more comfortable document and
the less useful one.

---

## 16. Open, and whose they are

| Question | Where it is decided |
|---|---|
| First user: one operator on their own cluster, or a team? Decides how far per-identity isolation goes | the operator. **§7, §8 and §11 are all sized by this answer** |
| Is the assistant absent, or replaced by something using the viewer's own credentials? | **Answered: absent**, revisitable (§10) |
| Which additive pages a deployment offers | **Answered: there is one image and it carries every one of them, every page off until somebody turns it on.** The chart's `extras` value reaches the console as `KT_EXTRAS` and names the pages this installation *may* turn on; a person holding `use` on one turns it on per cluster in Settings → Extra, reviewed and recorded (§11). One replica and its state volume: with several replicas, or none, the per-cluster write is not registered and every page stays off. An extra's own settings are changed on the same terms; `KT_EXTRA_SETTINGS`, rendered by the chart, pins one for every cluster |
| Port-forward: off, proxied, or honest (§7.3) | **Answered: off, with the `kubectl port-forward` command shown instead** (§7.3) |
| Archive: off, filtered, or namespace-scoped under `--incluster` (§6.2) | the operator — it is a retention decision, not a cache detail |
| Per-person preferences kept by the server (§11) | the console |
| Session store: where it lives, and how many replicas (§8.3) | **Answered**: memory for one replica, Valkey for several, sealed with the session key; the chart deploys Valkey when `replicaCount` is above one. See §8.3 |
| An operator-facing "sign alice out" (§8.2) | the console |
| The exact `ClusterRole` (§5.1) | the charts repository, derived and committed with its derivation |
| `Strict-Transport-Security`: sent by the binary or by the chart (§4.5) | **Answered: the binary**, under `--incluster` only, `max-age=31536000`, this host only |
| Desktop on a multi-user host: bind the session to the OS user, a per-tab secret, or neither (§7.4) | **Answered: neither**, an accepted risk; the desktop is treated as single-user |

---

## 17. What could not be determined from the code as it stands

Listed because a threat model that reads as complete gets used as though it were.

- **The `ClusterRole` is not derived here** (§5.1). What the chart ships is the
  charts repository's, and the size of §14.1 is whatever it grants.
- **Whether the deployment's log collection reads the container's stdout**
  (§13). JSON lines on stdout is the common default; a given cluster's collector
  is the operator's to confirm.
- **Valkey's memory ceiling against a number of sign-ins** (§4.2): not sized, so
  replay protection under a full store is a stated limit.
- **HSTS through a real ingress and in a browser** (§4.5), and the loopback
  cookie's behavior in real browsers and the native windows (§7.4): the tests
  hold the headers and the links, not what a browser does with them.
- **Facts about the API server have to be measured rather than reasoned
  about** — event ordering, `resourceNames`, what the service proxy strips.
  Treat every unmeasured claim above the same way.

---

## 18. The checks this document asks for, collected

So that "the threat model says so" has somewhere to point.

**Router walks** (the load-bearing ones, §9.3, §10, §15) — under `--incluster`,
`mux.Handler` returns an empty pattern for `/kt-api/terminal`, `/kt-api/exec`,
`/kt-api/nodeshell`, `/kt-api/claude` and `POST /api/dock/terminals`, for every
method and for path variants.

**Structural, `go/ast`** — the shell layer is constructed in exactly one place
and that place is behind the profile switch (§9.3); no handler reaches a
Kubernetes client except through the choke point (§5.2); no generic reverse proxy
(`docs/DEFINITION-OF-DONE.md` §6).

**On the chart** — the `ClusterRole` contains no `impersonate`, `escalate`,
`bind`, or `*` (§5.1); `KT_FORWARD_PORTS` is unset by default (§7.3).

**Behavioral** — per-identity caps refuse the right person with the right
sentence (§7.2); a revoked session fails a request, fails an upgrade, and ends an
open stream (§8.3); an ID token with a wrong claim is refused and does not fall
back (§4.4); a preference written by one user is not read by another (§11); an
audit record carries the outcome and no secret value (§13); every server-profile
response carries `Strict-Transport-Security` and no desktop response does (§4.5).

**Documented rather than tested**, because they are delays and not defects: the
authorization cache's TTL is a revocation delay of about a minute (§5.4); group
membership is stale until the person signs in again (§8.3); an archived record
outlives the object it describes (§6.2).
