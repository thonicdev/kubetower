# Definition of Done

What "done" means in this repository. Not a wish-list: **every line below names
the thing that went wrong without it**, and a checklist item with no failure
behind it is a preference — preferences get dropped under time pressure, and the
first thing anyone drops is the item they cannot see the cost of.

A change is done when **all** of the following hold. Where one cannot, say so
explicitly rather than quietly lowering the bar. Saying "I could not check §1
because there is no cluster on this machine" is a finished report. Omitting §1 is
not.

The items specific to this repository are marked as such; the others are
shapes that recur in any tool of this kind. A checklist item describing a
mechanism that no longer exists is read as evidence that a check happened, so an
item is removed when its mechanism is.

---

## 1. It was verified running, not just compiled

**A build that succeeds proves nothing about behavior.** The change must have
been exercised through the path a person actually takes — the browser for
anything touching the interface, a real request for anything touching the API.

- [ ] It was started, and it reported itself healthy
- [ ] The changed behavior was **observed** working, and the observation is
      stated concretely: a status code, a screenshot, a log line, a command's
      output
- [ ] Claims are quoted from what was seen, never from what was expected

> *Why:* a login page shipped rendering `%!s(MISSING)` where its error banner
> should have been. It compiled, the process was healthy, and the endpoint
> returned 200. Only opening it in a browser showed it.

**This now has a second half, and it is new.** The front end is a compiled
bundle, so it has its own version of the same lie: `tsc --noEmit` passing and
`vite build` succeeding say nothing about whether a component rendered. A green
typecheck is the TypeScript equivalent of "it compiled".

## 2. A view that answers is not the same as a view that answers correctly

**A feature that renders, returns 200 and says the wrong thing is the expensive
kind** — it is trusted, which is worse than being broken.

- [ ] The output was compared against the same fact obtained another way —
      `kubectl` by hand, the cloud provider's CLI, the object's own YAML
- [ ] An "everything is fine" answer was distrusted until something known to be
      broken produced the opposite

> *Why:* a *Changes* panel shipped saying "this object carries no
> `managedFields`" on every resource. It rendered, the endpoint returned 200, and
> the sentence was plausible. `kubectl` has simply hidden `managedFields` from
> `get -o json` since 1.24 unless `--show-managed-fields` is passed, so the panel
> was confidently reporting the absence of its own subject.

## 3. An empty answer and a failed answer are different answers

A corollary of §2, and it recurs on every new surface, so it is its own item:

- a list with nothing in it, because there is nothing — versus because the
  request failed;
- a resource that is *unreadable* rather than absent;
- a node nothing could measure, versus a node measured at zero.

- [ ] Every state that can render as "nothing" was checked to see which of the
      two it is, and the interface says which

> *Why:* a cache once reported "no pods" for a cluster running 429 of them. It
> read as true, and rather reassuring. And a usage bar drawn empty reads as 0%,
> which reads as an idle machine — the opposite of the truth when a kubelet has
> stopped answering. The fix was an em dash, not a zero.

## 4. Nothing destructive was run against a real cluster

- [ ] Guard and permission checks were exercised with `--dry-run=client`, or
      against a resource that does not exist
- [ ] No mutating verb was issued against a live cluster to "see what happens"

> *Why:* testing a read-only guard by running an actual `kubectl delete ns`
> deleted a live namespace. The guard had a hole, so the command went through.
> **A test whose expected outcome is "nothing happens" is exactly the one that
> destroys something when the assumption is wrong.**

## 5. Anything destructive is covered by a test that needs no cluster

- [ ] Every guard has a table test covering each form its input can take
- [ ] Those tests run in `go test ./...`, so CI runs them and nobody has to
      remember

> *Why:* a read-only guard looked for the verb at the first non-flag argument, so
> `kubectl --context prod delete ns X` stopped scanning at `prod`. Ten table
> cases catch that in milliseconds, without a cluster.

This covers the terminal's command shims and pod `exec`, **and the interface's
own write path** — every verb behind a confirmation, and the guarded door that
edits a manifest. That clause is explicit because a sentence such as *"the
interface has no mutating operation, so there is no guard to test"* stays true
only until the first write path ships, and then reads as an instruction **not to
test the console's only write path**.

## 6. The browser never talks to a cluster

Specific to this repository, and it is an architectural invariant rather than a
preference: **the Go backend is the only Kubernetes client.**

- [ ] No generic proxy towards an API server was introduced — no
      `httputil.ReverseProxy`, no `NewSingleHostReverseProxy`, and the test that
      walks the AST for them still passes
- [ ] Any new endpoint is a use case, not a pass-through. Generic reads through
      the dynamic client are `get` and `list` only, with the resource validated
      against discovery
- [ ] Every write goes through the one guarded door
- [ ] The front end's network calls go through the single network module; `fetch`
      and `WebSocket` are not reachable from anywhere else

> *Why this is a rule and not a design note:* a proxy means the **browser**
> decides what is called. Every guard, every read-only fence and every
> authorization check in the backend is then a suggestion, because the one
> endpoint that forwards arbitrary requests is the one nobody remembered to put
> them on.

**And the trap, which looks correct in both halves of the code:** an apply that
checks the kind from the **route** and writes the kind from the **body** is a
complete bypass. The kind that gets authorized is the one read out of the
submitted document, after parsing and before the write.

## 7. Versions and external facts were checked, not recalled

- [ ] Every version claim comes from a registry, an API, or the running process
- [ ] Every claim about somebody else's API comes from its source or its docs
- [ ] Anything uncertain is labeled uncertain

> *Why:* a tool this project once embedded rewrote kubeconfig context names —
> `/` became `--` — before putting them in its URLs. Nothing suggested it; only
> reading the source did. Assuming otherwise would have made the terminal fail on
> precisely the managed clusters that needed it.

The lesson outlives the dependency: **the surprising behavior of somebody else's
code is discovered by reading it**, not by expecting it.

## 8. Every count is produced by a command, not recalled

- [ ] Every figure in every document was produced by a command, and the command
      is quotable
- [ ] It was **re-run**, not quoted from when it was true

> *Why:* "24 tests" was written from memory. The real figure was 19 functions and
> 41 cases. A document whose own numbers are approximate cannot ask accuracy of
> anyone else.

This applies to counts in every file, not only an audit: package counts, test
counts, kind counts, line totals. All are cheap to produce and all are wrong when
recalled.

## 9. The documentation matches reality

- [ ] `README.md` matches what the binary actually does
- [ ] `docs/DEVELOPMENT.md` matches the loop that actually works
- [ ] `.env.example` lists every variable the code reads, and nothing it does not
- [ ] Ports, paths and commands were re-read against the deployment, not from
      memory

> *Why:* a port moved because the old one was taken, and three documents kept
> stating the old one for a day. **Documentation that is wrong is worse than
> absent: it is trusted.**

**One specific to this repository, and it is not a compile error.** Renaming an
environment variable renames it in the code; it does not rename it in anybody's
deployment, `.env`, compose file or shell profile. What arrives is not a build
failure — it is a variable nobody sets any more, read as its zero value, behaving
plausibly and wrongly. Check a rename by grepping the tree **and** the history,
not by reading a diff.

## 10. What is not there is said as plainly as what is

The honesty owed here is about **the distance between what is built and what a
person can reach.**

- [ ] Nothing built-but-unreachable is written up as shipped
- [ ] Anything deliberately not done is stated as a decision, not left to be
      discovered
- [ ] Wiring one of those up updates every document that currently says otherwise

> *Why:* four things once compiled, passed their own tests, and had no caller. From
> a code search each looked shipped. The sharpest was an environment variable that
> was documented, settable, accepted — and changed nothing. **A setting that
> appears to work is worse than one that is missing**, and only a line saying so
> closes the gap.

## 11. One language: English

- [ ] Code, comments, documentation and every string the interface puts on screen
      are in English
- [ ] No mixed-language sentence, and no stray label left behind a rename

> *Why:* half a convention is worse than either half. With code in one language
> and an interface in another, every new string becomes a decision, and the two
> halves drift.

## 12. It works on somebody else's machine

**This repository is not a personal configuration.** Anything naming one
environment belongs in `.env`, never in code and never in a document phrased as a
universal truth.

- [ ] No cluster name, account id, session name, internal hostname or host path
      in Go, in TypeScript, in the Dockerfile or in a compose file
- [ ] Documents phrase local facts as examples, not as truths
- [ ] Anything the machine can discover is discovered, not assumed
- [ ] **A code path this host cannot run says so, in the document its reader
      reads** — and as much of it as possible was moved somewhere this host *can*
      run

> *Why:* an SSO session name was hardcoded in three places. On any other laptop
> all three would have printed a confident instruction naming somebody else's
> session. All three now read it from the user's own configuration.

> *Why the second clause:* the installer writes a freedesktop entry on Linux and
> builds an application bundle on macOS, and this project is developed on Windows
> with neither machine to hand. Two things follow. **Say it where it will be
> read** — a colleague about to run it is not reading a package comment. **And
> shrink what is unverifiable**: the parts that are pure data — file syntax,
> plist keys, container header arithmetic — live in a file carrying **no build
> tag**, so `go test ./...` checks them on every host. What is then left
> unverified is a real answer ("the paths, and whether the desktop reacts")
> rather than "all of it". Code behind a platform build tag is code nobody here
> has ever compiled, let alone run.

## 13. There is one build, and no tag doubles it

Specific to this repository, and it is a decision rather than an accident.

- [ ] No build tag selects a variant of the product. A profile is chosen at
      **run time**
- [ ] If one is ever introduced, every gate in CI runs twice — or the variant
      ships having never been tested

> *Why:* a build tag that empties a catalog to produce a second edition means
> **`go build ./...` and `go test ./...` do not compile the thing that ships**.
> Gating the tagged build fails on every test written for the other edition, and
> tagging a file of shared helpers to fix that silently removes unrelated tests
> from the tagged run.
>
> **A build tag turns one deliberate exclusion into as many accidental ones as
> there are shared symbols**, and the only thing that makes that visible is
> running the tagged build — which is the thing nobody does.

## 14. The embedded bundle is real

Specific to this repository.

- [ ] The front-end bundle is actually embedded in the binary, asserted by a test
      rather than by the build succeeding

> *Why:* `//go:embed all:dist` is satisfied by a lone `.gitkeep`. The directory
> is committed empty on purpose, so a backend contributor can build and test
> without Node — which means the embed directive **cannot** fail when the bundle
> is missing. A binary that serves a placeholder page is a green build and a
> broken release.

## 15. Dependencies stay verifiable

- [ ] `go.sum` is updated and committed
- [ ] The image build uses `go mod download`, never `go mod tidy`
- [ ] The front end installs with `pnpm install --frozen-lockfile`, and the
      lockfile is committed
- [ ] Any newly pinned version is the current stable, checked at its source —
      base image arguments included

> *Why:* `go mod tidy` at build time rewrites `go.sum` from whatever the proxy
> serves that day, which removes the checksum verification the file exists for.
> `--frozen-lockfile` is the same argument on the other side: without it, a
> lockfile that disagrees with `package.json` resolves to something nobody has
> ever run, and it does it quietly.

## 16. Credentials stayed where they belong

- [ ] No credential, hash or key in the repository outside `.env`
- [ ] `.env` still ignored by git
- [ ] No password on a command line, where the process list would show it
- [ ] Tokens are never serialized — `json:"-"` on identity fields, plus a test
      that marshals an identity and asserts no token appears in the output
- [ ] A Secret's `data` reaches a view through one function, behind an explicit
      request to reveal it

## 17. The repository's own conventions

Each is checked by a workflow in this tree, so this item is about noticing a red
run rather than about remembering a rule:

- [ ] **Conventional Commits** on every commit **and on the pull-request title** —
      [`commitlint.config.js`](../commitlint.config.js),
      [`.github/workflows/guards.yml`](../.github/workflows/guards.yml). The title
      matters because a squash merge takes it as the commit subject, so a
      repository that lints only commits has an unenforced convention that looks
      enforced
- [ ] **No machine attribution** anywhere in the history — same workflow. Not in
      a commit message, not in a pull-request body, and not in a commit written
      to test whether the check works: a branch proving a guard can fail still
      pushed the forbidden thing, and deleting the branch afterwards does not
      un-push it
- [ ] **Every change reached `main` through a pull request**

> *Why the title clause is its own line:* the pull request that introduced this
> convention on a sibling repository was opened with a title `commitlint`
> refuses. The rule was broken by the change that introduced it, and nothing
> stopped it — because the job that catches it cannot block a merge without
> branch protection, and branch protection is not available on this plan.

## 18. What was skipped is stated

- [ ] Anything not done, or done partially, is named — with the reason
- [ ] Anything blocked on somebody else is stated as such, **with the exact
      command they need to run**
- [ ] Any check that was *not* performed is named as not performed, rather than
      omitted

> *Why:* five clusters were unreachable for an expired SSO session, and only its
> owner could refresh it. Saying so plainly is the difference between a
> blocked task and an invisible one.

> *And the general form:* a report that lists what worked and omits what was
> skipped is a report that gets acted on as if it were complete. **It is not done
> until it has run** — compiling, committing and publishing are not evidence of
> behavior. Where a real run was impossible, say so and say what remains
> unverified.

---

## 19. The contract is emitted, not only declared

Specific to this repository.

- [ ] **Every contract member a page needs is added to `api/openapi.yaml` and
      filled in the Go in the same commit**, with a test that asserts the server
      *emits* it
- [ ] **All five failure states of `api/errors.md` draw differently on the page**
      — `empty`, `unreadable`, `absent`, `unreachable`, `forbidden` — plus
      `pending`. §3 is the general rule; here it is checked per page, because a
      page that collapses two of them is the defect this product exists to
      remove
- [ ] **Each claim that a page works names something that ran** — a test
      function by name, or an observation stated concretely: page, cluster, kind,
      what was seen. "It renders" is not a proof and neither is "looks right"

> *Why the contract member and its Go go in one commit:* the response conformance
> test validates real answers against the spec, and **every optional member is
> satisfied by being absent.** So a member declared and never sent conforms: the
> client branches on it, `openapi-typescript` emits it as required, and the Go
> struct never carries it. `internal/api/listmembers_test.go` is the shape that
> catches it: read the schema's properties out of the YAML at run time and
> assert **both** directions.

A report that claims a browser check without one is the failure §1 is about. Say
*"not opened in a browser"* and the report is finished; imply otherwise and it
is not.

## 20. A green tick is read with its tier

*Specific to this repository.* Every Linux job, the release's `image` and
`draft` included, runs on one self-hosted pool (the release's own jobs, and the
three workflows it calls, only on its `release`-labeled runners, which have `gh`
and `hfsprogs`), so CI runs the strict
minimum on each change and the heavy jobs at release, and **a green run proves
what its tier ran and nothing more**. `detect` writes the tier and every gate
that did not run into the run summary; read it before reading the tick.

| Tier | When | What it proves | What it does not |
|---|---|---|---|
| **fast** | every pull request and every push to `main` | `gofmt`, `vet`, `go test -race`, the contract from outside; the front end's lint, typecheck, tests and build; `govulncheck`; R1 and R2; the six layout assertions where `web/` moved; on a pull request only, the browser path where its paths moved (below); the `docker build` on a pull request that touches what only the image build reads (`ci.yml`'s `IMAGE_INPUTS`: a Dockerfile, `.dockerignore`, `go.mod`/`go.sum`, the front end's package manifests, lockfile, Vite entry and config, `api/openapi.yaml`, the PWA's files, `cmd/ktnotices`, the license files) | the windows-gated tests (no Windows runner in CI); that every target still compiles; that the Windows setup and the macOS disk images still build; that the image still builds, when its inputs did not change; that the image is clean of advisories |
| **full** | weekly on `main`, Sunday 02:00 UTC (03:00 in Paris in winter, 04:00 in summer), and by hand: `gh workflow run ci -f full=true --ref <branch>` | the fast tier, **plus** the cross-compile of every published target, the Windows setup wizard, the macOS disk images and the `docker build` — what a tag would build | the trivy scan, which runs only in `release.yml` |
| **release** | a bare semver tag, `X.Y.Z` or `X.Y.Z-pre` (never `v`-prefixed) | `release.yml` refuses a commit with no green `ci` run (its Go and front-end jobs) and `security` run, builds every archive, the setup and the disk images through `cmd/ktbuild`, builds the image and pushes it only after trivy passed on that very image (never over an existing version tag), then drafts the release, its notes opening with `packaging/release-notes/unsigned.md` read at the tag, followed by GitHub's generated notes | anything the tests did not already prove on the commit under the tag |

`ci` has no nightly run, only the weekly full tier; `security` (govulncheck)
also runs nightly on `main`, for the advisory published with no push to meet
it.

The line that follows from the table, and the one to check before calling a
change done:

- [ ] **A pull request's green is the fast tier.** Before a squash to `main` of
      a change to a build tag, a target, `packaging/` or `cmd/ktbuild/` — and
      before any claim that "every target compiles" or "the setup builds" — the
      full tier was run on the branch by hand and its run is named in the
      report. Otherwise the first build of it is the next weekly run on `main`,
      or the tag's.
- [ ] **A job that skipped is stated.** The summary's table says *did not run*
      and why; a report that says "every target compiles" off a fast-tier run
      has read a skip as a pass (§3, §18).
- [ ] **The windows-gated tests ran somewhere.** The self-hosted pool is Linux,
      and a file behind `//go:build windows` does not skip there — it does not
      exist there. CI has no Windows job, so `task test` on a Windows machine is
      where those tests — the assistant's no-secrets suite among them — are run
      before a squash. The run summary says they did not run; the report names
      the Windows run.

> *Why the tiers exist:* every job shares one self-hosted pool, so a job
> in the queue ahead of the tests is a review that waits on it. The release's
> `image` and `draft` hold its only write scopes; the pool's runners persist, so
> each keeps its credential in a directory of its own under `runner.temp` and an
> `always()` step removes it and reads back that it is gone. What answers *did this
> change break something* runs on every change, in about six to eight minutes;
> what answers *would this ship* runs where shipping is decided, at the tag, and
> by hand before it when a change touches the build. Nothing that gates a
> release was dropped; it was moved to the tier whose question it answers.

**What CI proves of the browser path, and what it does not.**
[`.github/workflows/smoke.yml`](../.github/workflows/smoke.yml) runs the
browser path (`web/e2e/`) against the built binary on a kind cluster, on a
pull request that touches anything a page or a verb of that path goes through —
`web/`, `api/openapi.yaml`, `go.mod`/`go.sum`, and every package of the
module the smoke's binaries link, dependencies included (`go list -deps` of the
console's server packages and the commands the job builds, `cmd/kubetower`
bringing in the extras); the command and the list are in the workflow's header,
and a test holds the list to it — and by hand. It has no
schedule, and it does not run on a push to `main`. So a pull request that
changes none of those has not been through it — run
`gh workflow run smoke --ref <branch>` where it matters — the Claude terminal
case is skipped there (the self-hosted runner has no `claude`), and kind is not
a real cluster: §1 on a real one is still done by a person.

---

## Not in scope, so nobody blocks on it

- **A coverage threshold.** A percentage over a tree this size measures the wrong
  thing. The rule above is "destructive paths are tested", not a number.
- **CI being green is not the definition of done, and a green CI is not a pass on
  its own.** It is a backstop. A gate that did not run has proved nothing — which
  is why [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) reports what it
  found in the tree, and which tier it ran in, before it decides which jobs to
  run, rather than skipping quietly. **A job that skips is an empty answer
  rendered as a passing one** (§3, again; §20 for what each tier skips on
  purpose).
- **Committing and publishing.** Preparing a change is done; recording it and
  releasing it are a person's act.
