# The socket surface — what OpenAPI cannot describe

**OpenAPI 3.1 cannot describe a WebSocket.** There is no verb, no request body, no
response object; an upgrade is a handshake followed by a conversation. Five
channels of this product's surface are exactly that, and one more is a stream.

They are **not** omitted from `openapi.yaml`: they are listed there under
`x-channels`, and the log stream is a full operation in it. This file is the rest
of their contract, and it is written to the same standard — if it is thinner than
the spec, that is a defect in this file and not a property of sockets.

## The six, and why each is here

| Channel | Path | Why it cannot be an operation |
|---|---|---|
| **terminal** | `/api/c/{cluster}/ws/terminal` | a shell **on the machine running the console**, scoped to a cluster's context: bidirectional, long-lived, byte-oriented |
| **exec** | `/api/c/{cluster}/ws/exec` | an interactive shell **inside a pod**, through the API server's `exec` subresource |
| **debug** | `/api/c/{cluster}/ws/debug` | a shell in a **debug container** the console adds to a running pod, then exec-es into |
| **nodeshell** | `/api/c/{cluster}/ws/nodeshell` | a shell on a node, after a plan has been agreed |
| **assistant** | `/api/ws/assistant` | a long-lived control channel |
| **logs** | `GET /api/logs` | a stream, and it **is** in the spec, as `text/event-stream` |

`terminal` is the **host** shell: it resolves a kubeconfig context and runs a
command line on the machine running the console, which is the session
`capabilities.localShell` gates and `POST /api/dock/terminals` opens. `exec` is
the **pod** shell, interactive rather than one command, through client-go. Both
are mutating doors, which is why the read-only fence refuses both: a guardrail
that refuses a gesture through one door and permits it through the next is worse
than none.

The four upgrades are registered **without a method** — `"/kt-api/terminal"`,
not `"GET /kt-api/terminal"` — because an upgrade is not a verb. Any route table
regenerated from the spec has to preserve that, and a generator that emits `GET`
for these has produced a router that rejects the handshake.

## Why the log stream is framed rather than raw

It would have been simpler to hand back the log bytes. It is `text/event-stream`
instead, and the reason is the same one that produced the envelope:

**A raw stream cannot say why it stopped.** A log that ends because the pod was
deleted, a log that ends because the caller may not read it, and a log that is
simply quiet are three different answers, and a raw byte stream renders all three
as a screen that stopped changing. A framed stream carries a `state` event with
the same five-state vocabulary mid-stream.

```
event: message
data: {"type":"line","at":"2026-09-11T09:31:02Z","container":"app","text":"listening on :8080"}

event: message
data: {"type":"state","state":"forbidden","reason":{"code":"verb_denied","message":"…"}}

event: message
data: {"type":"end","atStart":true}
```

One frame is not an ending. **`state: pending` with `container_not_started`** is a
follow opened on a container that has not started yet - `ContainerCreating`,
`PodInitializing`, or a pod still waiting for a node ("waiting for the pod to be
scheduled", with the scheduler's reason when it has one) - which the server waits
for on the same stream: it says so, a new frame when the reason changes, and the
container's lines follow once it runs. Every other `state` frame says why the
stream is stopping and is followed by `end`. The wait ends with `state:
unreadable` and the same code when the container is not going to start on its own
or the pod goes away, and on the stream's own deadline as any follow does. A read
that does not follow does not wait: it answers the API server's refusal at once.

```
event: message
data: {"type":"state","state":"pending","reason":{"code":"container_not_started","message":"waiting for the container to start: ContainerCreating"}}

event: message
data: {"type":"line","container":"app","text":"listening on :8080"}
```

Two inherited properties:

- **It has no cursor, and paging back is a different shape on the same address.**
  `internal/logs/earlier.go` pages back, but there is no cursor to page with —
  `corev1.PodLogOptions` offers no offset and no continue token — so `before` is
  how many lines the caller already holds, the server grows the tail and keeps the
  front of it, and `tailLines` is untouched. With `before` the route answers
  `application/json` and does not stream, which is the whole asymmetry: a `fetch`
  may read a status **and** a sentence, and an `EventSource` may read neither.
  Three stop reasons come back on the page — `beginning`, `ceiling`, `dense` —
  each with its sentence written server-side, because *there is nothing older*,
  *there is more and this console will not fetch it* and *this log is too dense to
  page through* are three answers and only the first is an ending.
- **`atStart` is a fact, and the sentence about it is the front end's.** The
  last line says it has reached the beginning of *the log the cluster still holds*
  — not the beginning of time. That rule has to be tested where the sentence
  lives, in the front end: a Go test over Go strings would keep passing over a
  string nobody reads.

## The handshake

### Authentication is the same cookie, and 401 still means 401

The session cookie is sent with the upgrade request because it is same-origin.
A lapsed session is refused **before** the upgrade, with an ordinary HTTP
`401 application/problem+json` — not a socket that opens and immediately closes.

That matters more here than anywhere: a socket that opens and closes is
indistinguishable from a container that exited, and a front end will retry it.

### Origin is checked on the upgrade whenever it is sent

`Sec-WebSocket-Protocol` and the cookie are not enough. The same-origin policy does
**not** apply to WebSocket handshakes — a page on any origin can open one at this
console and the browser will attach the cookie. So the server checks the `Origin`
header on every channel's upgrade, after the session (a lapsed one is the 401
above), and the cases are these:

- **`Origin` present and naming this console** — its host, scheme stripped, equals
  the request's `Host`: the upgrade goes on.
- **`Origin` present and naming anything else** (or a request with no `Host`):
  refused with `403 application/problem+json`, `origin_rejected`, before anything
  is allocated.
- **`Origin` absent:** accepted. A browser always sends one on a WebSocket
  handshake, so its absence means a non-browser client (curl, wscat), which a
  hostile page cannot drive and which still needed a live session.

The channel's own upgrader (`internal/terminal`) makes the same comparison again,
ignoring case, with the same rule for an absent `Origin`.

This is the socket half of the custom-header check the JSON mutations use, and it
is the half people forget, because the JSON side is the one the framework warns
about.

### The content policy has to permit it

`connect-src 'self'` covers same-origin `ws:`/`wss:` — the socket inherits the
page's origin. This is stated because it is worth verifying rather than assuming,
particularly in an embedded web view, where a same-origin socket has been observed
to be treated differently from a same-origin fetch.

## Per channel

### terminal, exec

**Opened from an explicit gesture, never from a bare effect.** React's strict mode
double-invokes effects in development, so a socket opened in `useEffect` opens
**two** sockets and two pseudo-terminals. The session cap then reports itself as
reached at half the sessions — in development only, which is where it is least
expected and hardest to attribute.

Either open from a click, or key the session so the second mount reuses the first.
The cap is real and shared; burning it on a duplicate is a bug that looks like a
limit.

Frames are byte-oriented after the handshake, with a small control channel for
resize:

```
→ {"type":"resize","cols":120,"rows":40}
→ {"type":"data","data":"<base64>"}
← {"type":"data","stream":"stdout","data":"<base64>"}
← {"type":"exit"}
← {"type":"state","state":"forbidden","reason":{"code":…,"message":…}}
```

`exec` differs from `terminal` in that it terminates, so `exit` is expected rather
than exceptional.

Notes on that vocabulary:

1. **It is what the server sends**, base64 inside JSON in both directions. The
   template console speaks a second framing on the same session - binary frames
   out, `{"type":"in"}` in - chosen at the upgrade: `internal/terminal/stream.go`.

2. **A refusal really is a frame, and that is what this wire is for.** A refusal
   written as a red sentence into the terminal output can be read by a person but
   a client cannot branch on it, so the session cap and the read-only fence would
   be two sentences that happen to differ rather than two answers. The `state`
   frame carries the envelope's own vocabulary, and `reason.code` is one of
   `api/openapi.yaml`'s `ReasonCode` values - a test reads that enum at run time
   and fails on a name invented in the terminal layer.

3. **`exit` is the last frame before the server closes, on every channel.** A
   client therefore never sees a bare close (1006) after an ordinary `exit` and
   does not draw it as the connection going away.

4. **`exec` and `nodeshell` carry a `code` when the API server reported one.** A
   command inside a pod that ends with a non-zero status is
   `{"type":"exit","code":3}` - client-go reports that status - not a failure
   written into the output. A zero is absent: nothing reports it as a number.
   `terminal` and `assistant` carry no `code`: the pseudo-terminal seam these
   sessions are built on (`internal/terminal/pty.go`) exposes Read, Write, Close,
   Resize, Kill and Wait, and none of them carries a status - on Windows there is
   no `*exec.Cmd` to ask, because ConPTY needs a `CreateProcess` `os/exec` cannot
   make. A zero written there would be a client reading "it succeeded" off a
   number nobody measured. `code` stays optional in the grammar.

### debug

**A write, then an exec.** The upgrade carries `namespace`, `pod`, `image` and
optionally `target` (the container whose processes the debug container joins).
In order:

1. A cluster in `KT_READONLY_CLUSTERS` is refused with 403 before the upgrade,
   and the refusal is recorded (decision `read_only`, outcome `refused`): an
   ephemeral container cannot be removed once added. Nothing reaches the cluster.
2. The names are Kubernetes names and the image a plausible reference, or 400.
3. After the upgrade, two reviews for the person: `patch pods/ephemeralcontainers`
   and `create pods/exec` on that pod. Either refused is a `state` frame with
   `verb_denied`, and its audit record; nothing is added.
4. The pod is read: one that is not there is `absent` / `no_such_object`, as is a
   `target` the pod does not declare.
5. A strategic-merge PATCH on the pod's `ephemeralcontainers` subresource adds
   `kt-debug-` and five random characters, running the image's `sh` on stdin and
   a terminal so it stays up, with `targetContainerName` when a target was given.
   It is audited with the image, the target and the container's name.
6. The server waits for it to run, at most two minutes, narrating the kubelet's
   reasons in the output. An image that cannot be pulled, or a container that
   ended, is `unreadable` / `container_not_started` at once; the bound is
   `timeout`.
7. A shell is exec-ed into that container, exactly as on `exec` (`create
   pods/exec`, the same frames, the same `exit`), with the container's own `sh`
   from its PATH rather than `exec`'s `/bin/sh`. `pods/attach` is never asked.

The PATCH's refusals keep their cause apart, because each has a different owner:

| Code | The API server said |
|---|---|
| `verb_denied` | 403, RBAC |
| `pod_security` | 403 carrying "violates PodSecurity": the namespace's level forbids it |
| `admission_denied` | an admission webhook "denied the request" |
| `not_supported_here` | 404 or 405 on the subresource: the cluster serves no ephemeral containers |
| `unclassified` | anything else, its sentence in `message` |

On `exec` and `debug` alike, **a shell program the container runtime did not
find in the image** is `unreadable` / `no_shell`, not `unclassified`: the remedy
is a container that has one, which is what `debug` adds.

### nodeshell

**This channel is the act half of a confirm-then-act pair whose preview is an
ordinary operation** (`POST /api/c/{cluster}/nodeshell/preview`). It is the one
place where the plan token crosses protocols.

The upgrade carries the token from the plan, and the server re-derives the facts
before doing anything — the same rule as every other act, and it is the only thing
binding the confirmation to what actually happens. **An upgrade with no token, or
with an expired one, is refused with 403 and no socket is opened.**

### assistant

A long-lived control channel. Disabled where `capabilities.assistant` is false,
and the refusal is at the upgrade, not in the front end: in a shared profile this
would run on one person's account for everybody, and hiding the button is not a
control.

**No session is opened about a Secret.** The upgrade is refused with
`forbidden` / `verb_denied` and a sentence saying why, on any of `Secret`,
`secret`, `Secrets` or `secrets` in the `kind` query — the console has two names
for a kind (the payload's `title` and the route's discovery plural) and the
query takes either. The console also withholds the control on a Secret's page,
and that is the courtesy; this is the contract.

**What the refusal buys, and what it does not.** It stops a session being
addressed to a Secret. It does not, by itself, stop a session opened on some
other object from reading one: the read-only fence denies mutating verbs, and
`get` is not one. That is the job of the session's `kubectl` and `helm`
overrides, read-only or not (`KT_CLAUDE_READONLY=false` included): they refuse
every spelling of a Secret read, `helm get manifest|values|all|hooks`, and every
kubectl plugin reached through `kubectl`, since a plugin (krew's `view-secret`,
or `kubectl krew` itself) can read a Secret by a door no pattern names; a plugin
binary called by its own name (`kubectl-view_secret x`) never meets the
override. They are a guardrail, not a boundary: a guarantee that the assistant cannot read a Secret at all would have
to be a credential that cannot, evaluated at the API server.

With `KT_CLAUDE_READONLY=false` the assistant's `kubectl` keeps, among others,
`exec`, `attach`, `cp` and `debug` (a read-only session denies them), so it can
read what is mounted in a pod, Secrets included.

**The conversation comes back after a reload, and the server decides how.** The
upgrade may carry `session=<uuid>` — lower-case, the shape `crypto.randomUUID()`
produces — drawn by the browser when the tab was first opened and stored with
the tab, so a reopened tab sends the same one. What it means is not the
browser's to say: the server looks for the conversation's transcript under the
CLI's own configuration and starts Claude Code with `--resume <uuid>` when there
is one and `--session-id <uuid>` when there is not, because a resume of an id
that never got as far as a transcript is a refusal from the CLI, and the browser
cannot know whether the process it started before the reload wrote one.

A value that is not a UUID is **dropped, not refused**: the session opens with
no conversation, and the log says why.
It is one flag and one file name, and a value of that shape did not come from
the dock.

**What is and is not promised.** The conversation is what comes back — the
terminal's scrollback is not, and the process is a new one. Conversations live
per working directory on the CLI's side, and this console runs each session in
a workspace of its own that is reused; the server looks across all of them and
resumes in the one that holds the transcript. When that workspace is out to
another live session, the conversation is **still resumed**, in a free
workspace: the CLI finds a transcript kept under another directory's name
(`claude --resume <id>` from one workspace was measured resuming a transcript
kept under another's), so nothing is lost by running elsewhere, while running
in the busy workspace would be two sessions writing one workspace's notes,
which the workspaces exist to prevent. Where two workspaces hold a transcript
under one id, the one written last is resumed. Whoever opens the CLI's transcripts for a
session that is not this console's is outside this contract.

## What is common to all of them

- **The capability flag is the front end's hint; the refusal is the contract.**
  Every channel checks its own condition on the upgrade. A profile that disables a
  channel must be tested at the upgrade, because the code is still in the binary —
  a condition is all that stops it starting, and that condition is what stands
  between a shared console and a shell holding the process's own credentials.
- **Session limits are per process.** They are a safety limit for one
  operator and a shared resource for several; a shared profile divides them by
  identity rather than raising them.
- **A refusal is always framed or HTTP, never a silent close.** A close with no
  reason is the socket-shaped version of `{"items": []}`.

## What gates `terminal`

`capabilities.localShell` reports `PTYSupported()`:
a pane in the page, served wherever a pseudo-terminal can be opened beside the
process, which is the condition the channel itself checks. A terminal *window on
the desktop the process runs on* is a different thing, false in a container, and
is reported separately as `capabilities.desktopTerminal`.

## What has not been decided here

- **Whether this file eventually becomes AsyncAPI.** It is prose because prose is
  what one reader needs now; `x-channels` in the spec carries the machine-readable
  index so that converting later does not mean taking the inventory again.
