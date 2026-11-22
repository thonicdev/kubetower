# Developing KubeTower

One Go binary serves `/api/*` and the SPA from the same process. That is the
shipped shape and it is what makes the console CORS-free, single-origin and
cookie-simple. It is also, taken literally, a development loop in which changing
a color costs a Go build.

So there are **two processes in development and one in production**, and
everything below is about keeping those two arrangements indistinguishable from
the browser's point of view.

---

## 1. What you need

| | Why | Check |
|---|---|---|
| **Go** | `go.mod`'s `toolchain` line pins the version, so the host loop builds with the same compiler the image does | `go version` |
| **Node 22+** | Vite 8 and the TypeScript toolchain | `node --version` |
| **pnpm** | the workspace is `web/` + `web/ui/`, and pnpm is what the lockfile was written by; `web/package.json` pins the version in its `packageManager` field | `corepack pnpm --version`, run in `web/` |
| **`task`** | the task runner this repository uses instead of make — §10 | `task --version` |
| **Docker** | only for the packaged back end in §7 and the in-cluster loop in §8. The fast loop needs none of it | `docker --version` |
| **a cluster** | anything your kubeconfig already names | — |
| **helm, kubectl** | only for the in-cluster loop in §8, on Docker Desktop's Kubernetes | `helm version --short`, `kubectl version --client` |

`task` is one command, and it is one every contributor to a Go repository can
already run:

```bash
go install github.com/go-task/task/v3/cmd/task@v3.45.4
```

It puts `task` in `$(go env GOPATH)/bin`, which has to be on your `PATH`.

**pnpm needs no global install.** `corepack` ships with Node and resolves it on
demand, which is what the `PNPM` variable in `Taskfile.yaml` uses by default.
Corepack reads `packageManager` from the nearest `package.json`, and there is
none at the repository root, so the pinned version is the one you get inside
`web/` — which is where every task that calls pnpm runs.

Everything else is checked in one command:

```bash
task doctor
```

It prints the version of each tool or says it is missing, the `KUBECONFIG` the
loop will use (`<unset, ~/.kube/config>` when it is unset), and where
`task build:api` writes the binary.

---

## 2. From a clone to a browsing session

**First, once per clone: choose the development password.**

```bash
task dev:password -- devpass     # or `task dev:password` alone, to be asked without echo
```

It writes the git-ignored `.env` at the root of the clone, and `task dev`
takes **these three variables from it and nothing else**. What goes in, and why,
one line printed for each:

| Variable | Written | Why |
|---|---|---|
| `KT_PASSWORD_HASH` | always, single-quoted | the bcrypt hash of the word you chose. The word is stored nowhere. The environment wins over `credentials.json`, so this is the password from now on, and it is one you can type again tomorrow |
| `KT_SESSION_SECRET` | when `.env` has none, or one shorter than the 32 characters the console uses | with a hash in the environment and no key, the console draws a key at every start, and `task dev` restarts it on every Go change: every save would sign you out |
| `KT_STATE_DIR` | only when `.env` has none | `~/.kubetower-dev`, so the development loop never writes into your own `~/.kubetower` |

Without it, the password would be drawn at random, delivered once and gone at
the first login, and every scratch state directory would hold another password
nobody wrote down.

Four things to know about it:

- **Task's `--` is not optional.** `task dev:password devpass` asks Task to run
  a task called `devpass`. The word on Task's command line is visible in the
  process list for as long as Task runs; with no word you are asked, twice,
  without echo. It is a development password, so that is your call.
- **The same `.env` is read by `task container`**, and the hash is
  single-quoted, which Compose takes literally — no `$$` doubling (§4). The
  container then takes the same password. `docker compose config` shows the
  difference: the single-quoted hash arrives whole, an unquoted one as `$2a$10`.
- **`task dev` takes nothing else from `.env`.** `cmd/devwatch -dotenv .env`
  hands the process `KT_PASSWORD_HASH`, `KT_SESSION_SECRET` and `KT_STATE_DIR`
  (`internal/devenv`), and the rest of the file does not exist for it. That is
  the point: `cp .env.example .env`, the container's setup, sets
  `KT_FORWARD_PORTS=30000-30031`, and a desktop process given that binds every
  port-forward on 0.0.0.0, reachable from your network. Read whole, that file
  starts the process with `port-forward: local ports 30000-30031, bound on
  0.0.0.0`; through devwatch, `KT_FORWARD_PORTS is not set, so the kernel picks
  each local port`. `task dev:password` warns when the file carries it.
  `KT_LISTEN`, `KT_USER` and everything else come from your shell. **An
  allow-list fails closed for exposure and open for restriction**, so devwatch
  names (never values) every other non-empty `KT_*` key it skipped: a
  `KT_READONLY_CLUSTERS` left in `.env` is a line on the terminal, not a fence
  you think you have.
- **`.env` is read literally**: no `${VAR}`, no `~`, no inline ` # comment`.
  So a `KT_STATE_DIR` that only makes sense expanded, or that is not absolute,
  is refused before anything starts, rather than becoming a directory inside the
  clone holding `credentials.json`. `task dev:password` writes an absolute one,
  and warns about one it keeps that `task dev` would refuse. It also says when
  your shell sets `KT_PASSWORD_HASH` itself, since the shell wins and the word
  you just chose would then not be the one `task dev` asks for.
- **`.env` is read when `task dev` starts**, so a running loop keeps the old
  password until it is restarted. Your own shell wins over `.env`:
  `KT_STATE_DIR=… task dev` still moves the state directory for one run.

Then seven commands, in this order.

```bash
# 1. the task runner, once per machine
go install github.com/go-task/task/v3/cmd/task@v3.45.4

# 2. the front end's dependencies, once per clone
task setup

# 3. say what is missing before anything fails obscurely
task doctor

# 4. the development password, once per clone (above)
task dev:password -- devpass

# 5. give it a cluster. KUBECONFIG is a LIST, not a path:
#    ';' on Windows, ':' everywhere else. Unset means ~/.kube/config.
export KUBECONFIG="$HOME/.kube/config:$HOME/.kube/work.yaml"   # (§5)

# 6. both processes, in one terminal
task dev

# 7. open it, and sign in as admin with the word from step 4
#    http://localhost:5173      <- NOT 127.0.0.1. See the box below.
```

When 5823 or 5173 is taken, move them:
`KT_LISTEN=127.0.0.1:5843 task dev -- --port 5183 --strictPort`. The Vite half
says where its proxy points:

```
dev:web: the proxy sends /api to http://127.0.0.1:5843
[kubetower] listening on 127.0.0.1:5843          (kubetower.log, in KT_STATE_DIR from .env)
POST http://localhost:5183/api/auth/login  admin/devpass   204
GET  http://localhost:5183/api/session                       200  {"identity":"admin","method":"password",...}
```

With `KT_DEV_API` set as well, the explicit value wins: the proxy then talks to
whatever listens there, even when the Go half is elsewhere.

> ### Open `localhost:5173`, not `127.0.0.1:5173`
>
> **Vite 8 binds the IPv6 loopback only.**
>
> ```
> $ netstat -ano | grep 5173
>   TCP    [::1]:5173    [::]:0    LISTENING
>
> $ curl -o /dev/null -w '%{http_code}' http://127.0.0.1:5173/
> (connection refused)
> $ curl -o /dev/null -w '%{http_code}' http://localhost:5173/
> 200
> ```
>
> The Go process is the other way round — `defaultListen` is the literal
> `127.0.0.1:5823`, so it binds IPv4 only. The two coexist because the proxy
> dials **outward** to `127.0.0.1:5823`, which is an IPv4 connection either way.
>
> The one combination that breaks is setting `KT_LISTEN=localhost:5823`, which
> can resolve to `::1` and leave the proxy's IPv4 target with nothing behind it.
> Write the address, not the name.

**Stop with one `Ctrl+C`.** `task dev` runs both processes as concurrent
dependencies and forwards the interrupt to both.

### When `gofmt` disagrees with itself

`gofmt -l .` can flag a file that CI and the container accept. **The `gofmt` on
`PATH` is not necessarily the one this repository builds with.**

`go.mod` pins the toolchain, so `go build` and `go test` fetch that version into
the module cache and use it. A standalone `gofmt` installed by hand is whatever
version it was, and two versions can format one construct differently — a
multi-line call as the first of two return values is one such case.

**So run the toolchain's, not the one on `PATH`:**

```
"$(go env GOROOT)/bin/gofmt" -l .
```

or `go fmt ./...`, which goes through `go` and therefore through the pin.

`ci.yml` already warns that `gofmt`'s answer "is a function of the toolchain, so
a version bump can turn this red with nobody having misformatted anything" —
this is that sentence from the other side, where the *local* tool is the stale
one and CI is right.

### When the page says the answer was not JSON

Two ways the loop breaks that have nothing to do with the code, and both look
identical in the browser: the SPA draws

> *The answer was not JSON — expected JSON, received text/plain*

which is Vite's own 502 page arriving where a cluster list was expected. The SPA
is right to refuse it — an HTML error page rendered as data is the failure
`api/errors.md` exists to prevent — but the cause is never in the console.

**Something else is holding 5823.** The packaged back end from `task container`
is the usual one. `task dev:api` refuses to start in that case and names the
task that frees it, so this should be a message rather than a mystery.

Its other way out is to move: `KT_LISTEN=127.0.0.1:5824 task dev` moves both
halves. `dev:web` derives `KT_DEV_API` from `KT_LISTEN` when `KT_DEV_API` is
unset, and prints the target it chose:

```
dev:web: the proxy sends /api to http://127.0.0.1:5824
```

Moving only the Go half would leave Vite's proxy talking to whatever had refused
to yield the port — another console's 401 then makes the sign-in page refuse a
correct password with nothing anywhere saying why.

An explicit `KT_DEV_API` still wins, which is what it is for: pointing the front
end at `task container` (§7). And `task dev -- --port 5183 --strictPort` moves
the Vite half the same way, when 5173 is the busy one.

**Vite outlived its target.** A Vite started while 5823 was busy keeps proxying
into the void after the port is freed, because the proxy resolves the target
once. The test is one command:

```
$ curl -o /dev/null -w '%{http_code}
' http://127.0.0.1:5823/kt-healthz   # 200
$ curl -o /dev/null -w '%{http_code}
' http://localhost:5173/api/v1/session  # 502
```

**5823 answering directly while 5173 answers 502 through the proxy means restart
Vite**, not the Go. Nothing about it is broken; the proxy is pointing at a
process that no longer exists.

**A Go change needs `task dev` restarted.** A `.tsx`, `.ts` or theme change does
not — Vite replaces the module in the open page. That asymmetry is the entire
point of running two processes: a Go rebuild is seconds, and paying it for a
padding value is what a development loop is supposed to remove.

### Where the start-up lives

`cmd/kubetower/main.go` is a few lines: it calls `app.Run` with the extensions
this build carries, each one listed there by name. The extras are packages of
this repository like any other; what makes one an extra is that it reaches the
console through `extension.Host` and is switched on per cluster. The start-up
itself — the flags, the environment, the profile, single sign-on, the replicas,
the archive, the server, the desktop window, the installer — is
`internal/launch`, reached through the public `app` package so that any caller
— this `main`, or the `boundary` module's test of the extension contract from
outside — starts the same console with its extensions handed to the same
function. A change to start-up goes in `internal/launch`; anything added to
`cmd/kubetower` beyond its list of extensions is start-up that every other
caller of `app.Run`, the `boundary` test included, does not get. The version
stamps follow the code:
`-X github.com/thonicdev/kubetower/internal/launch.version=…`, not `main.version`.

---

## 3. How the SPA reaches the API in development

**Through Vite's dev-server proxy, and the browser only ever knows one origin.**

```
browser  ──────────►  http://localhost:5173     Vite
                          │  /                  index.html, /@vite/client, .tsx, theme
                          │  /api/*  ──────────►  http://127.0.0.1:5823   the Go process
                          │  /kt-pwa/* ────────►  http://127.0.0.1:5823   icon, manifest, service worker
                          └─ HMR socket (direct, ws://localhost:5173)
```

`127.0.0.1:5823` is the default. Under `task dev` the target is `KT_DEV_API` when
set, and otherwise follows `KT_LISTEN` (§2); `dev:web` sets `KT_DEV_API` before
starting Vite, so the file below never sees it unset there.

`web/vite.config.ts`, in substance:

```ts
server: {
  proxy: {
    '/api': {
      target: process.env.KT_DEV_API ?? 'http://127.0.0.1:5823',
      ws: true,
      changeOrigin: false,
    },
    '/kt-pwa': {
      target: process.env.KT_DEV_API ?? 'http://127.0.0.1:5823',
      changeOrigin: false,
    },
  },
},
```

With Vite on 5173 and a Go process on 5823:

```
$ curl -s -o /dev/null -w '%{http_code}' http://localhost:5173/api/capabilities
401
$ curl -s http://localhost:5173/api/capabilities
{"error":"session missing or expired","login":"/auth/login"}

$ curl -s -o /dev/null -w '%{http_code}' http://localhost:5173/c/some-cluster/pods
200            # a deep link falls back to index.html, as the SPA needs
```

That 401 is the Go process's own JSON, returned through the proxy.

### Why the proxy and not the alternatives

**The alternative is two origins, and it does not cost a CORS header — it costs
a change to the product's cookie policy.** The session is an `HttpOnly` cookie,
so that one XSS cannot exfiltrate production credentials. With the SPA on
`localhost:5173` and the API on `127.0.0.1:5823`, every `/api` call is
cross-origin, so the cookie has to be `SameSite=None` — and a browser only
accepts `SameSite=None` together with `Secure`, which it will not set over
plain `http://`. The honest summary: **a second origin in development forces
either TLS in development or a weaker cookie in production**, and neither is a
trade worth making to avoid four lines of Vite configuration.

Three more things fall out of the proxy that would each have needed handling:

- **The CSP is unchanged.** `connect-src 'self'` holds because `self` *is*
  `localhost:5173` from the page's point of view. Beside it the backend names
  the request's own host for `ws:` and `wss:` (`connectSrc` in
  `internal/web/headers.go`), which under the proxy is `localhost:5173` — the
  HMR socket's origin. That socket is Vite's own and is **not** proxied.
- **WebSockets go through the same door.** `ws: true` upgrades in the proxy, so
  the dock terminal reaches 5823 on the one origin.
- **`changeOrigin: false` is deliberate.** Leaving `Origin` as the browser sent
  it is what lets the backend's `Origin` check on mutations see a true value.
  Rewriting it would make every mutation look same-origin to a check whose whole
  job is to notice when it is not.

### The one thing to know about ports and cookies

**A cookie does not isolate by port.** A session set by `127.0.0.1:5823` is sent
to `127.0.0.1:5173`, and one set on `localhost` is sent to `localhost` on any
port. Two consequences, and the first is useful:

- you can log in directly at `http://127.0.0.1:5823/auth/login` and the cookie
  is then presented through the proxy — provided you use the **same host
  spelling** on both, because `localhost` and `127.0.0.1` are different cookie
  origins even though they are the same machine;
- so running a second, unrelated service on another loopback port is not a
  boundary. It never was; it is worth saying because the port numbers here make
  it look like one.

### What the proxy has to cover, and does

The contract in `api/openapi.yaml` puts **everything the browser asks for** under
`/api` — `/api/auth/login`, `/api/auth/logout`, `/api/session`,
`/api/capabilities`, `/api/clusters`, `/api/c/{cluster}/…`. The paths outside it
are `/healthz` and `/readyz`, which are a probe's and not a browser's;
**`/auth/`**, the sign-in page the Go process renders and the console leaves
for when a session ends, proxied so that it arrives there through Vite too; and
**`/kt-pwa/`**, which is the browser's: `index.html` links the tab icon, the
manifest and the service worker there, and the Go process serves them from its
embedded tree. Without its proxy rule, Vite answers `/kt-pwa/icon.svg`
with its SPA fallback — `index.html`, 200, `text/html`, not a 404 — so the
browser draws its default icon and nothing logs it. In its general form: **a new
top-level path the browser asks for is a change to this file as well as to the
router.**

---

## 4. The password — what it is not

**The loopback desktop has no password at all.** A double-clicked KubeTower
listening on 127.0.0.1 opens its window signed in through a one-time launch
code, behind a Host allowlist that refuses a DNS-rebound page; a second launch,
or `kubetower open`, and the running instance prove to each other that they hold
the launcher secret in the state directory - the instance first, and neither
ever sends it - and the second launch is handed a sealed code for a window of
its own. The sign-in page there says "Open KubeTower again, or run
`kubetower open`".

**The development loop and the smoke suite keep their password, on purpose**: a
`KT_PASSWORD_HASH` set in the environment keeps the form and
`POST /api/auth/login` on the loopback desktop. That is what `task dev:password`
writes into `.env` and what `KT_E2E_PASSWORD` signs in with, so neither changes.
Vite on :5173 proxies `/api` with the browser's own `Host: localhost:5173`,
which the allowlist accepts: it checks the name, not the port. Without
`KT_PASSWORD_HASH`, `task dev` is the loopback desktop: sign in by running the
binary with `open` (`go run ./cmd/kubetower open`, or the built binary) against
the same `KT_STATE_DIR` and `KT_LISTEN`. That window is on :5823, not on Vite's
:5173, so for front-end work the password remains the way in.

Everywhere else - a container, a `KT_LISTEN` beyond loopback - what follows
holds word for word.

**There is no way to start a console reachable beyond this machine without a
password, and nothing in this repository provides a development default.**

That is not a posture: this process holds administrator credentials for
production clusters, so starting without a front door is not "less safe", it is
"anyone who can reach this socket has production". `internal/auth.New` refuses
an empty hash, and start-up turns that refusal into a fatal error.

What you do instead, and it is less work than a default would have been:

```bash
task dev:password -- <word>   # once per clone: a password you chose, hash in .env (§2)
task dev                      # signs in with that word from now on
```

That is not a default: nothing is shipped, the word is yours, and only its hash
is written, into a file git ignores. Without it — no `.env`, or no
`KT_PASSWORD_HASH` in it — the console behaves as a downloaded binary does:

```bash
task dev          # first start draws a password, prints it once
```

The first run draws 20 characters from `crypto/rand`, shows them, and keeps only
a bcrypt hash in `credentials.json` under `KT_STATE_DIR` (`~/.kubetower` on the
host). It is shown three ways at once because a launcher can throw the first two
away: the log, a box on Windows, and `PASSWORD-ONCE.txt` beside the credentials
file, which deletes itself at the first successful login.

**On Windows, a scripted first start needs `--unattended`.** The box is modal and
it opens *before* the server goroutine, so a first run with nobody at the keyboard
holds one window titled `KubeTower - first run`, answers nothing on its port and
writes nothing to stderr — its launch report goes to `kubetower.log` inside the
state directory the caller does not yet know about. Ask `/healthz` rather than
read the output to tell: the binary serves perfectly while silent.

```bash
kubetower --no-window --unattended    # no box; stderr names PASSWORD-ONCE.txt
```

The flag is only that: the box is not attempted and one line on stderr says where
the password is. A launch without it is unchanged, which is the point — a desktop
user double-clicking the shortcut has no terminal either, and a heuristic that
took the box away from them would be a lock-out dressed as a login page.

Lost it:

```bash
task password     # kubetower --reset-password: draws another, says where, exits
```

It prints one line naming `PASSWORD-ONCE.txt` and its directory — the state
directory the loop uses, so `KT_STATE_DIR` from `.env` when it is there — and
that the first successful login deletes it. The password is in that file, not in
that line, and not in the log. **On a terminal with a console attached the banner
above the line still shows the password itself**; without a console, nothing on
stderr carries it. The line is written unconditionally because everything else
on that path is not: the banner waits on a console being attached, and without
one every log line goes to `kubetower.log` inside the state directory. A
developer watching a silent screen would conclude it failed, run it again, and
lose the second password to the third.

**`task password` refuses when `KT_PASSWORD_HASH` is set**, in `.env` or in the
shell: the console then uses that hash and never reads the file a reset draws
into, so the reset would succeed and change nothing anybody signs in with. The
refusal is `--reset-password`'s own, so it holds in a container too. Run
`task dev:password` instead.

**`.env.example` ships `KT_PASSWORD_HASH=` empty, and that is not a hole.** An
empty value selects the drawn-password path above; it does not select "no
password". The file says so at length on that line, because an empty-looking
credential variable is exactly the shape somebody skims past.

**`compose.dev.yaml` uses `${KT_PASSWORD_HASH:-}` and not
`${KT_PASSWORD_HASH:?…}`**, and the reasoning is written in the file: `:?` would
force every developer to produce a bcrypt hash by hand and walk into the
`$`-doubling trap that Compose's interpolation sets for `$2a$14$…`. The reliable
outcome of a first run that fails on a credential incantation is somebody looking
for the flag that turns the check off. There is no such flag and there must not
be one.

---

## 5. Giving it a cluster

**`KUBECONFIG` is a list, not a path.** `os.PathListSeparator` — `;` on Windows,
`:` on macOS and Linux, and `:` inside the container whatever the host uses.
KubeTower reads it with `filepath.SplitList` and merges the files the way
`kubectl` does, which is `ClientConfigLoadingRules{Precedence: …}` and not our
own merge.

```powershell
# Windows
$env:KUBECONFIG = "$env:USERPROFILE\.kube\config;$env:USERPROFILE\.kube\work.yaml"
```

```bash
# macOS, Linux
export KUBECONFIG="$HOME/.kube/config:$HOME/.kube/work.yaml"
```

Three behaviors to know, all of them kubectl's:

- on a name two files declare, the **first file wins** and the loser's entry is
  discarded *whole*. Two files each holding a `prod` context give you one `prod`,
  and the other exists nowhere — not as a duplicate row, not as an error, as an
  absence. The startup log names it, because nothing else can;
- a path that is **not there** is skipped, so naming a file that lives on your
  other laptop is harmless;
- a path that **is** there and does not parse refuses the start, naming the file.

**A cluster on `127.0.0.1`** — Docker Desktop's, kind, minikube, k3d — means the
host's loopback. On the host loop that is simply true. In the container it is
not, which is why `compose.dev.yaml` carries
`extra_hosts: host.docker.internal:host-gateway` and why `KT_IN_CONTAINER` exists
to force the verdict when detection cannot.

---

## 6. What is safe to generate, and where

**If your working tree is inside a synchronized directory** — a OneDrive, Dropbox
or similar folder — files written here are really uploaded. Sync
clients commonly exclude `node_modules` and `build` by name, and not `dist` or
`*.exe`. Check your client's exclusion list before building into the tree.

| What | Where it goes | Verdict |
|---|---|---|
| `go run ./cmd/kubetower` | Go's own build cache, outside the tree | safe — **this is the development loop, and it writes nothing here** |
| `task build:api` | the temporary directory by default (`$TEMP`, else `/tmp`), never the tree | safe by construction, and `BUILD_DIR=… task build:api` moves it |
| `task setup` → `web/node_modules` | in the tree, about 200 MB | safe: commonly excluded from sync by name, and git-ignored |
| `task build:web` → `web/dist/ui` | in the tree, often **not excluded** | **the one to be careful with** |
| `go build` with no `-o` | `./kubetower(.exe)`, in the tree, ~100 MB, often not excluded | don't. Use `task build:api` |
| container state | Docker's named volumes | nothing reaches the tree |
| `task incluster:up` → `kubetower:dev` | the Docker daemon's image store; devforward's state in `$TEMP/kubetower-incluster` | nothing reaches the tree (§8) |

**`web/dist/ui` is the live hazard.** A build writes the bundle into a directory
a sync client may upload, and rewrites it on every build — with its source maps
too when `KT_SOURCEMAP=1`. The development loop never needs it — Vite serves
from memory — so run `task build:web` only when you are checking the embedded
shape, and `task clean` when you are done. Adding `dist` to the sync client's
exclusion list also works; most clients apply it only at their next restart, and
a path already synced keeps syncing.

---

## 7. The packaged back end

`compose.dev.yaml` builds the real `Dockerfile` and runs the real image. It is
the slow loop and it is the **only** one that can catch a container-only
failure. Four kinds of them, none reproducible on the host:

- a bind-mounted single **file** arriving as an empty directory from a repository
  on a network share;
- the AWS CLI from AWS being glibc-only in an Alpine/musl image;
- `~/.aws` mounted read-only, which works perfectly until an SSO session expires
  and then takes every EKS cluster at once;
- a listener on the container's own loopback, which Docker cannot route a
  published port to: the mapping exists, `docker compose ps` shows it, and a dial
  from the host is refused.

```bash
cp .env.example .env        # then set HOST_KUBE_DIR and HOST_AWS_DIR
task container:config       # interpolate and validate, building nothing
task container              # docker compose -f compose.dev.yaml up --build
task container:logs         # where the drawn password is printed
task container:down -- -v   # -v also drops the state volume and its password
```

`task container:config` prints the fully interpolated configuration: one
service, its named volumes and mounts, and the port range expanded to one
published mapping per port on `127.0.0.1`.

The `Dockerfile`'s build stage runs `go vet` and `go test` as a gate, so
`task container` fails where the tests do. Removing the gate to get a green
build would undo the thing it is there for.

### Front end against the packaged back end

The two loops compose. Leave `compose.dev.yaml` running and start Vite alone:

```bash
task dev:web
```

The proxy's target is `127.0.0.1:5823` either way, because the compose file
publishes there. Set `KT_DEV_API` only if you moved `KT_PORT`.

### Mounts, and the two that are not symmetric

- **`~/.kube` read-only, always.** KubeTower reads the file that describes your
  clusters; it never writes it. A console that could rewrite where its own
  credentials point has a blast radius that includes the next session.
- **`~/.aws` read-write, on purpose.** The AWS CLI renews the SSO session token
  inside `~/.aws/sso/cache`; read-only, renewal fails the moment a session
  expires and every EKS cluster goes dark at once.

A third one is easy to read as a mistake: `~/.aws/cli/cache` is covered by a
**named volume** so the container stops sharing the assume-role cache with the
host, and the host's own copy is re-mounted separately at `/host-aws-cache`
**read-only**. Two concurrent writers is what corrupts an entry there, and the
corruption's entire error message is a 40-character hash. The Cluster page (`/health`) must be
able to name the file to delete without becoming one of the writers.

### If the healthcheck never goes healthy

The check is `wget --spider http://127.0.0.1:5823/kt-healthz`, and
`internal/auth` exempts exactly that path from the session guard — otherwise the
check reads the login page as a failure and restarts a process that is working
perfectly, forever.

A process answering **401 on `/kt-healthz`** is not this binary, or not this
version of it: check what is listening on 5823 before suspecting the health
logic.

---

## 8. In a cluster

The console as the chart deploys it — the server profile, single sign-on through
a real identity provider, the shared account beside it — on Docker Desktop's
Kubernetes, built from this tree. It is the loop for anything the desktop
process cannot show: the OIDC flow, the server profile's refusals, the pod's
ServiceAccount, the chart's own wiring.

It needs a checkout of `kubetower-helm-charts` beside this one, which holds the
chart, `dev/values.yaml` and the Dex fixture `test/oidc/dex.yaml`. This side
reads them and writes nothing there.

```bash
task incluster:up       # build, install, then keep both forwards alive
                        #    open http://127.0.0.1:8824
task incluster:down     # from any terminal: forwards, release and Dex
task incluster:forward  # the forwards alone, after a Ctrl-C, without a rebuild
```

| Variable | Default | What it moves |
|---|---|---|
| `KT_INCLUSTER_CONTEXT` | `docker-desktop` | the kubectl context, named on every kubectl and helm call |
| `KT_INCLUSTER_CHARTS` | `../kubetower-helm-charts` | where the chart, values and fixture are read from |
| `KT_INCLUSTER_DEX_ADDRESS` | `127.0.0.1,host.docker.internal` | where Dex's forward is bound — see below |

Set one in the environment (`KT_INCLUSTER_CONTEXT=kind-dev task incluster:up`),
or pass the variable without its prefix (`task incluster:up
INCLUSTER_CONTEXT=kind-dev`). `task incluster:up KT_INCLUSTER_CONTEXT=kind-dev`
is refused: Task computes these before it reads a command-line variable, so it
would run against the default without a word. `.env` does not reach them.

### What `incluster:up` does, in order

1. **Refuses** unless the context exists, answers, and its API server is on this
   machine (`kubernetes.docker.internal` or a loopback address), and unless the
   charts checkout holds all three files. It never uses kubectl's current
   context: the dev values carry credentials that are published, and a context
   typed wrong could name a real cluster. Against a context served from another
   machine, it refuses before any request is sent.
2. **Builds `kubetower:dev`** with `docker build`. The output is an image in the
   Docker daemon; nothing is written into the tree, because the SPA and the
   binary are built inside the build. The Dockerfile's gates — lint, typecheck,
   `go vet`, `go test` — run as they do for a release, and the cache skips them
   when nothing they read changed. BuildKit is set on the command, because under
   `DOCKER_BUILDKIT=0` the first `FROM` fails.
3. **Applies the Dex fixture if it is absent**, and waits for it. Present, it is
   left as it is.
4. **`helm upgrade --install`** with the checkout's `dev/values.yaml`, and waits.
   The image's ID goes on the pod as an annotation: the tag is `dev` on every
   run and `pullPolicy` is `Never`, so without it a rebuilt image would leave the
   Deployment unchanged and the old pod running. Two installs of the same image
   make two revisions and no new pod.
5. **Supervises both forwards in the foreground** through `cmd/devforward`:
   Dex on 5556 and the console on `127.0.0.1:8824`. A forward that dies is
   restarted within a second, and the output says so, with the time.

Then sign in at `http://127.0.0.1:8824` with the shared account, `dev` / `dev`,
or through Dex — **Mock**, which carries a groups claim, or **Email**, as one of
the fixture's static users, whose password is `password`: `alice@example.com`,
or `nobody@example.com`, whom no RoleBinding names. The shared account acts with
the console's own rights (`internal/access/access.go`), so it cannot show who
may see what; `nobody@example.com` can, and is the account for checking who is
offered an extra. The header of `dev/values.yaml` lists them.

`incluster:up` applies the fixture only when it is absent, so a fixture applied
before a user was added to it does not have that user: `task incluster:down`
first, or apply `test/oidc/dex.yaml` again and
`kubectl --context docker-desktop -n dex rollout restart deployment/dex`, since
Dex reads its users at start.

### Why Dex is on the network while it runs, and the console is not

The fixture's issuer is `http://host.docker.internal:5556/dex`: the one name
both the browser and the pod reach (the top of `test/oidc/dex.yaml` has why).
On Docker Desktop for Windows:

- **the pod needs only loopback.** With Dex forwarded on `127.0.0.1` alone,
  `curl http://host.docker.internal:5556/dex/healthz` from inside the console's
  pod answers `Health check passed`;
- **the browser does not.** The host resolves `host.docker.internal` through the
  hosts file Docker Desktop writes, to the machine's own network address
  (`192.0.2.10` in the example below), and a dial there is refused with Dex on
  loopback.

So Dex is bound on loopback and on that one address, resolved again at every
restart, and `devforward` prints a line saying so. That is narrower than the
fixture's own `--address 0.0.0.0` and still reachable from the network while it
runs; what it serves is the fixture, whose users and client secret are public.
`KT_INCLUSTER_DEX_ADDRESS=127.0.0.1` keeps it on loopback, and then a browser
needs `host.docker.internal` to resolve to `127.0.0.1`. Every interface —
`0.0.0.0`, `::`, or a name resolving to one — is refused, and any address off
loopback, written out or resolved, is named in a line on every bind.

The console's forward is on `127.0.0.1` and nowhere else. Neither port moves:
5556 is in the issuer, and 8824 in the redirect the fixture registers.

### What `incluster:down` stops

The supervisor first, from any terminal, through a file it polls in
`$TEMP/kubetower-incluster`; it closes its own forwards and exits. If it is not
there to answer — killed rather than stopped — `down` closes the processes it
recorded, each only after reading its command line and finding what devforward
started it with: `port-forward`, the context and the ports for a forward, so a
`kubectl` of yours that reused a recorded PID is left alone. It reads the record
again after its wait, so a forward restarted meanwhile is closed too. Then it
checks that nothing answers on 5556 or 8824, uninstalls
the release, and deletes the fixture's objects, namespace included.

The console's state volume is kept: the chart marks it
`helm.sh/resource-policy: keep`, so the next install finds the same state.
`kubectl --context docker-desktop -n kubetower delete pvc kubetower-state` is the
deliberate act that drops it.

### Why a program supervises the forwards

`cmd/devforward`'s package comment has the long form. In short: `down` runs in
another process, and on Windows the PID Task's shell knows is not one Git Bash's
`kill` takes; and Ctrl-C reaches kubectl at the same moment as the loop, so a
loop that restarts a forward when it exits races its own shutdown. Standard
library only, for `devwatch`'s reason.

A forward that starts while the supervisor is stopping kills its own `kubectl`.
A second Ctrl-C, or a second `incluster:down`, while the forwards are closing
ends the supervisor at once, after killing every forward it knows of.

It restarts a forward when kubectl **exits**, which is what kubectl does when the
pod behind it goes. It does not probe a forward that is still running; one that
is alive and wedged is restarted by hand, by killing its `kubectl`.

### What a run looks like

From a cluster with no release and no fixture, `incluster:up` applies the
fixture, installs revision 1 and brings both forwards up. A Dex sign-in
(`/api/auth/oidc/start` → Dex → `/api/auth/oidc/callback` 303 → `/api/session`
200, `"method":"sso"`) and a shared-account sign-in (`/api/auth/login` 204 →
`/api/session` 200, `"method":"password"`) both pass. A forward killed by hand
is announced and back within a second:

```text
devforward 13:48:48: dex: the forward died after 23s (exit status 0xffffffff) - restarting in 1s
devforward 13:48:49: dex: up again, 127.0.0.1:5556, 192.0.2.10:5556 -> dex svc/dex:5556 (restart 1)
devforward 13:48:53: console: the forward died after 28s (exit status 0xffffffff) - restarting in 1s
devforward 13:48:54: console: up again, 127.0.0.1:8824 -> kubetower svc/kubetower:5823 (restart 1)
```

After `incluster:down`, no `kubectl` process is left and nothing listens on 5556
or 8824. A supervisor killed outright leaves its two `kubectl` processes behind,
as Windows does, and `incluster:down` closes both by their recorded PIDs. The
command-line check and the stop races above are covered by
`go test ./cmd/devforward/` against a fake `kubectl`.

---

## 9. What no automated check covers

- **The in-cluster loop on macOS and Linux.** There `devforward` reads a command
  line from `/proc` (Linux, also under CI's unit tests) or `ps` (macOS) in place
  of `Get-CimInstance`, and Docker Desktop may not write a hosts entry for
  `host.docker.internal` — in which case Dex is bound on loopback alone and says
  so, and a browser needs that entry.
- **A Dex sign-in in a real browser**, rather than a client following the same
  redirects, and Ctrl-C from an interactive terminal.
- **`task password` without `--unattended` on Windows**, which opens a modal
  box. Its one line on stderr is asserted by
  `TestAResetSaysWhereThePasswordWentWhateverElseReachedAnybody` with the box
  stubbed.
- **`task dev:password` with no word**, which needs a real terminal to ask
  without echo.
- **`task build:web` in a synchronized tree** — run it once when checking the
  embed (§6).

---

## 10. Why these files are named what they are

### `Taskfile.yaml` and not a `Makefile`

**`make` is not on most Windows machines.** Getting it there means Chocolatey,
MSYS2 or WSL: three different answers, none of which a contributor has because
they cloned a Go repository. `task` is `go install`, on every platform where `go`
already is.

Three smaller reasons, each of which is a real property rather than a
preference:

- **Parallelism is declarative.** The loop is two long-running processes;
  `deps:` runs them concurrently and forwards the interrupt. A Makefile does that
  with `&`, `wait` and a `trap`, which behave differently under sh, dash and
  whatever Windows supplies.
- **One shell per task, not one per line.** Make runs each recipe line in its own
  shell unless `.ONESHELL` is set, so a `cd` on one line is gone on the next.
  Here `dir:` is a property of the task.
- **The shell is embedded.** Task interprets POSIX shell itself, so a recipe does
  not depend on which `sh` is on `PATH`. Tabs are not syntax either.

What it costs, said rather than discovered: one more binary to install, present
in no base image. `task doctor` names it.

### `compose.dev.yaml` and not `docker-compose.dev.yml`

**`compose.yaml` is the Compose Specification's own canonical name** and
`docker-compose.yml` is its documented legacy spelling, so a new file takes the
modern one. The `.dev` means a bare `docker compose up` does not pick it up —
which is wanted, because there is no shipped compose file and this one must
never be mistaken for one. Every command names it with `-f`.

`compose.dev.yaml` is held to a gate: `internal/launch/envwiring_test.go` fails
when the file is missing, and checks with its own regexp that every `KT_`
variable the code reads, minus the ones it lists as deliberately not forwarded,
appears as a key in the `environment:` block. A variable the process reads and
the compose file does not pass is a knob that appears to work.

### `.env.example`, and it is not optional

`internal/launch/envwiring_test.go` reads `../../.env.example` and **fails** — not
skips — when it is absent. The contract it enforces: every variable the code
reads is listed there, and nothing that is not read is.

The variables the code reads, as the test scans for them:

```bash
find internal cmd -name '*.go' ! -name '*_test.go' -print0 \
  | xargs -0 grep -hoE '"KT_[A-Z0-9_]+"' | tr -d '"' | sort -u
```

### No `deploy/`

The Helm chart lives in its own repository, `kubetower-helm-charts`, which §8
reads beside this one. A second copy here would be a second version to keep in
step.

