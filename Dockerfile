# KubeTower — one binary, and the four command-line tools it genuinely needs.
#
# The console talks to Kubernetes through client-go, so it needs no command-line
# tool to do its own work. Four are here anyway, and it is worth being precise
# about which and why — this list is what somebody trying to shrink the image
# will read:
#
#   aws     — not for us to call. client-go runs the kubeconfig's `exec` block,
#             and on an EKS context that block *is* `aws eks get-token`. Without
#             the binary, every EKS cluster fails authentication from inside the
#             container while working perfectly on the host.
#   kubectl — for the operator, in the terminal. Nothing in the server calls it.
#   helm    — likewise, for the operator in the terminal, where
#             internal/terminal installs a read-only override for it.
#   claude  — Claude Code, started by the Claude control in the drawer. It runs
#             on the operator's own subscription, authenticated once through
#             /login into a named volume; there is no API key in this project
#             and no model call leaves their plan. Opt-in: left out unless the
#             build sets CLAUDE_CODE_VERSION (compose.dev.yaml does, for local
#             development). The released image never carries it (release.yml):
#             it is what a cluster runs, and the server profile never offers
#             the control.

# The two base images, pinned together on purpose.
#
# GO_VERSION carries its Alpine explicitly rather than a floating `1.27-alpine`.
# The builder and the runtime are the same distribution at the
# same release, so a CGO_ENABLED=0 binary cannot be linked against one musl and
# run against another — and, more practically, the two cannot drift apart
# silently when Docker Hub repoints the floating tag.
#
# The Alpine release decides the kubectl version, and internal/terminal's skew
# check warns when kubectl is minors away from the cluster: Alpine 3.24 ships
# kubectl 1.36, matching the Kubernetes release the console targets.
ARG GO_VERSION=1.27-alpine3.24
ARG ALPINE_VERSION=3.24

# ---------------------------------------------------------------------------
# The console itself.
#
# **Without this stage the image ships no front end at all**, and nothing says
# so: `//go:embed all:dist` is satisfied by the committed `web/dist/.gitkeep`, so
# the Go build succeeds, the image builds, `docker run --version` answers - and
# `/` serves the "UI not built" page.
#
# `--platform=$BUILDPLATFORM` because this stage produces a directory of static
# files: cross-building it under QEMU costs minutes to produce identical bytes.
FROM --platform=$BUILDPLATFORM node:24-alpine AS console

WORKDIR /web
RUN corepack enable
# The manifest and the lockfile alone first, so a change to a component does not
# re-resolve the dependency graph.
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
COPY api/openapi.yaml /api/openapi.yaml
# The same gates CI runs, in the same order, because an image built from a tree
# that fails them is an image nobody should be able to produce by accident.
RUN pnpm run lint && pnpm run typecheck && pnpm run check:styles \
    && pnpm run check:api && pnpm run build
# An empty answer and a failed answer, one layer down: `pnpm build` exits 0 for
# reasons that do not include "the bundle exists". And no sourcemap: the bundle
# is embedded whole, and a map's sourcesContent is every comment of the source
# (web/vite.config.ts builds none unless KT_SOURCEMAP=1 asks).
RUN test -f dist/ui/index.html || { echo "the build produced no dist/ui/index.html"; exit 1; }
RUN if [ -n "$(find dist -name '*.map')" ]; then echo "the bundle holds sourcemaps:"; find dist -name '*.map'; exit 1; fi

# ---------------------------------------------------------------------------
# The application icon, rasterized.
#
# Chromium will not use an SVG for the icon it puts in the Windows taskbar; it
# wants 192 and 512 pixel bitmaps. Those are generated here rather than committed,
# so there is no exported PNG in the repository quietly drifting from the vector
# it came from — change icon.svg and the taskbar follows on the next build.
#
# The package is `rsvg-convert`, not `librsvg`: the latter exists on Alpine and
# does not provide the binary, which costs a build to discover.
FROM alpine:${ALPINE_VERSION} AS icons

RUN apk add --no-cache rsvg-convert

WORKDIR /icons
COPY internal/web/static/pwa/icon.svg internal/web/static/pwa/icon-maskable.svg ./

RUN rsvg-convert -w 192 -h 192 icon.svg -o icon-192.png \
    && rsvg-convert -w 512 -h 512 icon.svg -o icon-512.png \
    && rsvg-convert -w 512 -h 512 icon-maskable.svg -o icon-maskable-512.png

# ---------------------------------------------------------------------------
FROM golang:${GO_VERSION} AS build

WORKDIR /src

# Dependencies first, so a code change does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Before `go build`, because the embed happens at compile time. The `.gitkeep`
# that `COPY . .` just brought in is replaced by the real bundle here.
COPY --from=console /web/dist ./web/dist

# The bitmaps land in the source tree *before* the build, because they are
# served by `go:embed` like every other asset. Nothing is bind-mounted at run
# time: Docker Desktop can expose a mounted directory on a network share to a
# container as an empty directory without erroring. Baking them in always works.
COPY --from=icons /icons/icon-192.png /icons/icon-512.png /icons/icon-maskable-512.png \
     ./internal/web/static/pwa/

# What this image is.
#
# **There is no edition build tag.** This repository is the console and its
# additive pages (`extras/`): `cmd/kubetower` registers them, which is *wiring*
# in `main` rather than a different compilation, so the image is built and
# tested once.
#
# **`KT_VERSION` and `KT_BUILT`** are what `--version` prints, and they default
# to exactly what an unstamped build should say. `docker compose up --build`
# passes neither and the image reports `dev (built unknown, ...)`, which is
# true: an image built from a working tree has no release identity.
#
# `release.yml` passes both, the same values `cmd/ktbuild` links into the
# archives of the same tag, because an image built from a tag does have one and
# the two have to agree. There is no edition argument: one product with one
# license.
ARG KT_VERSION="dev"
ARG KT_BUILT="unknown"

# The bundle must be embedded (docs/DEFINITION-OF-DONE.md §14). Asserted here,
# so it holds for every build of this image and not only a tagged
# one - and before the compile, so the failure names the cause rather than
# arriving as a 503 in production.
RUN test -f web/dist/ui/index.html || { echo "no front-end bundle to embed - the console stage produced nothing"; exit 1; }

# The third-party notices: the Go modules this binary links and the npm packages
# the console stage bundled, each with its license text (cmd/ktnotices).
# Written into the bundle before the compile, so the binary embeds and serves
# it, and copied into the image beside the license below.
COPY --from=console /web/build/third-party-npm.json ./web/build/third-party-npm.json
RUN go run ./cmd/ktnotices -npm web/build/third-party-npm.json -o web/dist/ui/THIRD_PARTY_NOTICES.txt \
    && test -s web/dist/ui/THIRD_PARTY_NOTICES.txt

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/thonicdev/kubetower/internal/launch.version=${KT_VERSION} -X github.com/thonicdev/kubetower/internal/launch.built=${KT_BUILT}" \
    -o /out/kubetower ./cmd/kubetower

# vet and the tests gate the image. A binary that does not pass its own tests
# must not reach a cluster: a read-only guard with a hole can delete a live
# namespace. One compilation, so one run gates it.
#
# `-p 2`, as ci.yml's `go` job: without it go test runs one test binary per CPU
# at once, which can run a shared build machine out of memory.
#
# **KT_GATE=scan skips it**, for an image built only to be scanned and thrown
# away. Nothing that ships sets it (release.yml scans the image it ships, which
# runs the gate), and
# any value but empty or `scan` is refused, so a typo cannot skip the gate.
#
# **After the compile, and last in this stage**, so that ARG changes no earlier
# layer: the scan-only build reuses the compiled binary from ci's layer cache
# instead of compiling it again. The image still cannot exist without it - the
# final stage copies from this stage's last layer, which is this one.
ARG KT_GATE=""
RUN case "${KT_GATE}" in \
        "") go vet ./... && go test -p 2 ./... ;; \
        scan) echo "KT_GATE=scan: vet and tests NOT run - an image to scan, never to ship" ;; \
        *) echo "KT_GATE must be empty or 'scan', not '${KT_GATE}'"; exit 1 ;; \
    esac

# ---------------------------------------------------------------------------
#
# Named `runtime` so a build can refuse the cache for this stage alone
# (`no-cache-filters: runtime`).
# Nothing selects it with --target; it is the last stage, so a plain build
# still produces it.
FROM alpine:${ALPINE_VERSION} AS runtime

# The Alpine aws-cli package, not the installer from AWS: the official v2 build
# is glibc-only and this is musl, and presents as `aws: not found` inside a container where /usr/local/bin/aws exists.
# `apk upgrade` before `apk add`, and it is the difference between a green
# vulnerability scan and a red one. The `alpine:3.24` tag is rebuilt far less
# often than its package index moves, so a base image pulled today ships
# yesterday's openssl while the fix sits in the index the very next line reads.
# Upgrading costs one layer and removes the whole class - but only when the
# layer is built. Restored from a build cache, it is the index of whatever day
# the cache was written, which is why the runtime stage can refuse the cache.
RUN apk upgrade --no-cache \
    && apk add --no-cache \
        aws-cli \
        kubectl \
        helm \
        bash \
        ca-certificates \
        coreutils \
        tini \
    && update-ca-certificates

# A fixed uid, 100, so a bind-mounted ~/.aws and ~/.kube keep working without a
# chown.
#
# /var/lib/kubetower is KT_STATE_DIR: the rail's hotbars, and the only thing this
# process writes. It is created here rather than at run time because Docker
# copies the ownership of an existing mount point onto a new named volume — the
# directory has to exist, owned by uid 100, *before* the volume is created, or
# the volume arrives owned by root and every write fails on a process that has
# dropped privileges.
#
# /home/kubetower/.aws/cli/cache exists here for the same reason, and it is the
# subtle one: it sits *inside* the bind-mounted ~/.aws, and docker-compose.yml
# puts a named volume on top of it so the container stops sharing that one
# directory with the host. The comment on the `aws-cache` volume there says what
# that prevents. The mount point still has to exist in the image and be owned by
# 100 — the bind mount underneath does not help, because the ownership Docker
# copies onto a new volume is read from the image layer.
RUN mkdir -p /home/kubetower/.aws/cli/cache /home/kubetower/.kube /home/kubetower/.claude \
        /tmp/kubetower /var/lib/kubetower \
    && addgroup -g 101 -S kubetower \
    && adduser -u 100 -S -G kubetower -h /home/kubetower kubetower \
    && chown -R 100:101 /home/kubetower /tmp/kubetower /var/lib/kubetower

# The fourth binary: Claude Code, for the Claude terminal.
#
# The native build rather than the npm package — it needs no Node runtime, which
# keeps Node, npm and a whole package manager out of an image that holds
# production credentials. Anthropic's installer detects musl and fetches the
# musl build, so unlike the AWS CLI this one needs no special
# handling; it does need libgcc and libstdc++, which Alpine does not ship by
# default.
#
# **Pinned by its checksum, and fetched without the installer.** The install
# script is not versioned, and it downloads and *runs* the newest release to
# install the one asked for, so a build piping it into bash executes whatever is
# latest that day. Here the binary of the named version is downloaded from the
# same release bucket the installer reads, and refused unless its SHA-256 is the
# one written here.
#
# The two checksums below (linux-x64-musl, linux-arm64-musl) are read from
# https://downloads.claude.ai/claude-code-releases/<version>/manifest.json.
# **A version bump is three lines**: the version and both checksums, from that
# version's manifest.json. Renovate does not move them.
#
# **Left out by default**: CLAUDE_CODE_VERSION is empty unless the build sets it
# (`--build-arg CLAUDE_CODE_VERSION=2.1.226`, the version the checksums below
# are for; compose.dev.yaml passes it). Without it the server detects the
# absence at startup, the drawer stops drawing the control, and /kt-api/claude
# refuses in words.
ARG CLAUDE_CODE_VERSION=
ARG CLAUDE_CODE_SHA256_X64=d199d62f2ce2fca6138256f788ecd6157cacc40edb3b50ce22b8f974f816111a
ARG CLAUDE_CODE_SHA256_ARM64=8c58e37c14e09f0be1b5b42e1fc4f409f1124ccc584a8633b99b7e8e63d79bd0
RUN set -eux; \
    if [ -n "${CLAUDE_CODE_VERSION}" ]; then \
        case "$(uname -m)" in \
            x86_64) platform=linux-x64-musl; sum="${CLAUDE_CODE_SHA256_X64}" ;; \
            aarch64) platform=linux-arm64-musl; sum="${CLAUDE_CODE_SHA256_ARM64}" ;; \
            *) echo "no pinned Claude Code build for $(uname -m)"; exit 1 ;; \
        esac; \
        apk add --no-cache libgcc libstdc++ curl; \
        mkdir -p /opt/claude; \
        # Bounded: a stalled runner network fails the build in words within
        # about 30 minutes instead of holding the job to its timeout. A
        # transfer under 1 KiB/s for 60 s is cut, each try stops at 10
        # minutes, and no try starts after 20.
        curl -fsSL --connect-timeout 30 --max-time 600 \
            --speed-limit 1024 --speed-time 60 \
            --retry 3 --retry-all-errors --retry-max-time 1200 \
            -o /opt/claude/claude \
            "https://downloads.claude.ai/claude-code-releases/${CLAUDE_CODE_VERSION}/${platform}/claude"; \
        echo "${sum}  /opt/claude/claude" | sha256sum -c -; \
        # Readable and runnable by the runtime user, which is not this one.
        chmod 755 /opt/claude/claude; \
        ln -s /opt/claude/claude /usr/local/bin/claude; \
        # Bounded too: on some runners the binary starts and never answers,
        # and the build waited for the job's timeout. It answers in well
        # under a second where it works.
        timeout 60 claude --version \
            || { echo "claude --version gave no answer within 60 s on this build host"; exit 1; }; \
    fi

COPY --from=build /out/kubetower /usr/local/bin/kubetower
# The license and the notices of what the binary is built from,
# travel with the image as they do with every archive.
COPY --from=build /src/LICENSE /src/LICENSING.md /src/web/dist/ui/THIRD_PARTY_NOTICES.txt \
     /usr/share/licenses/kubetower/

USER 100:101
ENV HOME=/home/kubetower
# The pinned Claude Code above stays the one that runs. Without this the native
# binary may update itself into $HOME at run time, which would undo the
# checksum pin exactly where it matters. Bumping it is
# the three lines above, in a reviewed change.
ENV DISABLE_AUTOUPDATER=1
# The default matches the directory created above. docker-compose.yml sets it
# too, and mounts a volume there; this is what makes `docker run` of the bare
# image write somewhere sensible rather than into $HOME.
ENV KT_STATE_DIR=/var/lib/kubetower

# Every interface, and only here.
#
# The binary defaults to loopback, because running it on a laptop must not put a
# console holding production credentials on the local network. Inside a container
# that reasoning is inverted: the container is the boundary, and Docker cannot
# route a published port to a socket bound to the container's own loopback — so
# the image says so explicitly rather than the code guessing where it is running.
#
# docker-compose.yml sets this too, and deliberately: an operator reading the
# compose file should see where this listens without opening the Dockerfile.
ENV KT_LISTEN=:5823
WORKDIR /home/kubetower

EXPOSE 5823

# tini reaps the shells the terminal starts. Without it they linger as zombies:
# `exit` in a terminal would leave a process behind every time.
# The binary is part of the ENTRYPOINT and not the CMD, deliberately. With it
# in CMD, any argument at all replaces the whole command - so `docker run
# <image> --version` would ask tini to execute a file called `--version`.
#
# What this gives up: `docker run <image> sh` no longer opens a shell, it
# passes `sh` to the console. Use `--entrypoint sh` for that.
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/kubetower"]
