#!/usr/bin/env bash
# What guards.yml and ci.yml check on a pull request, run on this machine
# first, so a pull request leaves draft already green.
#
#   task ci                      every check below, then a summary
#   task ci -- --force           even above KT_CI_MAX_LOAD
#   bash test/ci-local.sh --list the jobs and steps it replays, and what it leaves to the runners
#
# It is restated here rather than read out of the workflows: their steps lean
# on what only a runner has (job outputs, GITHUB_ENV, a pull request's event).
# What keeps the two from drifting is a test, cmd/ktbuild/localci_test.go: every
# job of guards.yml and ci.yml, and every step of a job replayed here, must be
# named below, by a `# step:` line where it is replayed or a `# runner-only:`
# line saying why it is not.
#
# **One run at a time on this machine** (flock), and **none above a load of
# KT_CI_MAX_LOAD** (6 by default): `go test -race` and a Vite build beside
# somebody else's suite is a machine that swaps for an hour. KT_CI_MAX_LOAD=off
# or --force runs anyway.
#
# Every check runs even after one failed, as CI's `!cancelled()` steps do, and
# the exit status is non-zero when any did. A check that could not run is a
# failure, never a pass: a skipped Valkey test reads green and checks nothing.
#
# Never run by CI: it is the local half.

set -uo pipefail
root=$(git rev-parse --show-toplevel) || exit 2
cd "$root" || exit 2

force=no
case "${1:-}" in
  --force) force=yes ;;
  --list)
    grep -E '^[[:space:]]*# (step|runner-only):' "$0" | sed -E 's/^[[:space:]]*# //'
    exit 0 ;;
  "") ;;
  *) echo "usage: $0 [--force | --list]" >&2; exit 2 ;;
esac

# --- the machine ------------------------------------------------------------

limit=${KT_CI_MAX_LOAD:-6}
load=$(cut -d' ' -f1 /proc/loadavg 2>/dev/null || echo 0)
if [ "$force" = no ] && [ "$limit" != off ] && awk -v l="$load" -v m="$limit" 'BEGIN { exit !(l > m) }'; then
  echo "task ci: the load average is $load, above $limit. It will be quicker once the machine is quieter;"
  echo "         KT_CI_MAX_LOAD=<n> raises the limit, KT_CI_MAX_LOAD=off or --force runs it now."
  exit 75
fi

lock="${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}/kubetower-ci.lock"
exec 9>"$lock" || { echo "task ci: cannot open the lock $lock" >&2; exit 2; }
if ! flock -n 9; then
  echo "task ci: another run holds $lock on this machine; it runs one at a time. Try again when it ends."
  exit 75
fi

base=${KT_CI_BASE:-origin/main}
git fetch -q origin main 2>/dev/null || echo "note: git fetch origin main failed; comparing with the $base already here"
range="$base..HEAD"

results=()
failed=0
# run <label> <command...>: one check, timed, its outcome kept for the summary.
run() {
  local label=$1; shift
  local start=$SECONDS
  echo
  echo "=== $label"
  if "$@"; then
    results+=("ok    $label ($((SECONDS - start))s)")
  else
    results+=("FAIL  $label ($((SECONDS - start))s)")
    failed=1
  fi
}

# The open pull request of this branch, when there is one and gh can say.
pr_json=$(gh pr view --json title,body,commits 2>/dev/null || true)

commitlint_ready() {
  [ -x node_modules/.bin/commitlint ] && [ -d node_modules/@commitlint/config-conventional ] && return 0
  npm install --no-save --no-audit --no-fund --silent @commitlint/cli @commitlint/config-conventional
}

# --- guards.yml -------------------------------------------------------------

# step: guards/attribution: Establish the commit range
# step: guards/attribution: Refuse machine attribution in the commit range
r1_messages() {
  git log --oneline "$range"
  if git log --format='%B' "$range" | grep -inE 'co-authored-by:.*(claude|anthropic)|generated with .*claude'; then
    echo "R1 - machine attribution in a commit message"; return 1
  fi
  echo "no machine attribution in any commit message"
}
run "R1 - no machine attribution in $range" r1_messages

# step: guards/attribution: Refuse a machine as author or committer, and report every identity
r1_identities() {
  local who
  who=$(git log --format='%an <%ae>%n%cn <%ce>' "$range" | sort -u) || return 1
  printf '%s\n' "$who"
  if printf '%s\n' "$who" | grep -inE '(claude|anthropic)'; then
    echo "R1 - a machine is named as author or committer"; return 1
  fi
}
run "R1 - authors and committers" r1_identities

# step: guards/commits: npm install --no-save @commitlint/cli @commitlint/config-conventional
# step: guards/commits: Lint the commit range
r2_commits() {
  commitlint_ready || return 1
  npx --no-install commitlint --from "$base" --to HEAD --verbose
}
run "R2 - conventional commits" r2_commits

# step: guards/pr-title: npm install --no-save @commitlint/cli @commitlint/config-conventional
# step: guards/pr-title: Lint the pull-request title
r2_title() {
  local count header title
  count=$(git rev-list --count "$range") || return 1
  if [ "$count" = 1 ]; then
    # A one-commit pull request squashes under its commit's header.
    header=$(git show -s --format=%B HEAD | sed -n 1p)
    [ -n "$header" ] || { echo "the head commit's header could not be read"; return 1; }
    TEXT="$header" node -e 'const n=[...process.env.TEXT].length; console.log(`the only commit header is ${n} characters, within 92?`); process.exit(n>92?1:0)' || return 1
  fi
  if [ -z "$pr_json" ]; then
    echo "no pull request for this branch yet: its title is checked once it exists"
    return 0
  fi
  title=$(printf '%s' "$pr_json" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>process.stdout.write(JSON.parse(s).title))')
  TEXT="$title" node -e 'const n=[...process.env.TEXT].length; console.log(`the title is ${n} characters, within 92?`); process.exit(n>92?1:0)' || return 1
  commitlint_ready || return 1
  printf '%s' "$title" | npx --no-install commitlint --verbose
}
run "R2 - conventional PR title" r2_title

# step: guards/public-tree: The check refuses what it is meant to refuse
run "public - the check refuses what it is meant to refuse" bash test/public-check.sh --self-test

# step: guards/public-tree: No private reference in the tree
run "public - no private reference in the tree" bash test/public-check.sh tree

# step: guards/public-text: No private reference in the title or description
public_text() {
  if [ -z "$pr_json" ]; then
    echo "no pull request for this branch yet: its title and description are checked once it exists"
    return 0
  fi
  printf '%s' "$pr_json" \
    | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const p=JSON.parse(s);process.stdout.write(`${p.title}\n\n${p.body}\n`)})' \
    | bash test/public-check.sh message "pull request description"
}
run "public - no private reference in the title or description" public_text

# step: guards/public-text: No private reference in a commit message
run "public - no private reference in a commit message" bash test/public-check.sh commits "$range"

# --- ci.yml: go -------------------------------------------------------------

# step: ci/detect: Detect and report
# Locally everything runs: detect only decides which jobs a pull request needs.

# step: ci/go: gofmt
gofmt_tree() {
  local gofmt unformatted
  gofmt="$(go env GOROOT)/bin/gofmt"
  echo "using $gofmt ($(go version))"
  unformatted=$("$gofmt" -l .) || return 1
  [ -z "$unformatted" ] || { echo "These files are not gofmt'd:"; echo "$unformatted"; return 1; }
}
run "gofmt" gofmt_tree

# step: ci/go: go vet
run "go vet" go vet ./...

# step: ci/go: go vet, Windows, tests compiled
run "go vet, Windows" env CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...

# step: ci/go: start valkey on the host network
# step: ci/go: remove valkey
# The image is ci.yml's own pin, read from it, so Renovate moves both.
valkey_name=""
start_valkey() {
  local image password port candidate
  image=$(sed -nE 's/^[[:space:]]*VALKEY_IMAGE:[[:space:]]*([^[:space:]]+).*/\1/p' .github/workflows/ci.yml | head -1)
  [ -n "$image" ] || { echo "no VALKEY_IMAGE in ci.yml"; return 1; }
  command -v docker >/dev/null || { echo "docker is not on PATH: the Valkey tests would skip, which is not a pass"; return 1; }
  password=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
  for _ in $(seq 1 20); do
    candidate=$(( 20000 + RANDOM % 20000 ))
    if ! timeout 1 bash -c "exec 3<>/dev/tcp/127.0.0.1/$candidate" 2>/dev/null; then port=$candidate; break; fi
  done
  [ -n "${port:-}" ] || { echo "no free port for valkey"; return 1; }
  valkey_name="kt-ci-local-valkey-$$"
  docker run -d --rm --name "$valkey_name" --network host "$image" \
    valkey-server --bind 127.0.0.1 --port "$port" --requirepass "$password" --save "" --appendonly no >/dev/null || return 1
  for _ in $(seq 1 30); do
    if docker exec "$valkey_name" valkey-cli -h 127.0.0.1 -p "$port" -a "$password" --no-auth-warning ping 2>/dev/null | grep -q PONG; then
      export KT_TEST_VALKEY_ADDR="127.0.0.1:$port" KT_TEST_VALKEY_PASSWORD="$password"
      echo "valkey answers at 127.0.0.1:$port"
      return 0
    fi
    sleep 1
  done
  echo "valkey did not answer within 30s"; return 1
}
stop_valkey() { [ -z "$valkey_name" ] || docker rm -f "$valkey_name" >/dev/null 2>&1 || true; }
trap stop_valkey EXIT
run "start valkey" start_valkey

# step: ci/go: go test -race
run "go test -race" go test -race -p 2 -count=1 ./...

# step: ci/go: the valkey tests ran against valkey
valkey_ran() {
  local out want
  out=$(go test -race -count=1 -v -run 'TestValkeyKeepsTheContract|TestAWrongValkeyPasswordRefusesToStart' \
    ./internal/sharedstate/ ./internal/launch/ 2>&1) || { echo "$out"; return 1; }
  echo "$out" | grep -E '^(=== RUN|--- |    --- |ok|FAIL|PASS)'
  if echo "$out" | grep -q -- '--- SKIP'; then echo "a valkey test skipped"; return 1; fi
  for want in TestValkeyKeepsTheContract TestAWrongValkeyPasswordRefusesToStart; do
    echo "$out" | grep -q -- "^--- PASS: $want " || { echo "$want did not pass"; return 1; }
  done
}
run "the valkey tests ran against valkey" valkey_ran
stop_valkey

# step: ci/go: the extension contract compiles from outside the core module
run "the extension contract from outside the core" bash -c 'cd boundary && go vet ./... && go test -race -count=1 ./...'

# step: ci/go: the core does not import the boundary module
not_boundary() {
  if go list -deps ./cmd/kubetower | grep '^boundary\.kubetower\.invalid'; then
    echo "cmd/kubetower imports the boundary module"; return 1
  fi
  echo "cmd/kubetower does not reach boundary.kubetower.invalid"
}
run "the core does not import the boundary module" not_boundary

# step: ci/go: no database driver and no kubetower-extra module in the build
no_drivers() {
  local deps
  deps=$(go list -deps ./cmd/kubetower) || return 1
  if echo "$deps" | grep -E 'go-redis|go-sql-driver'; then echo "a database driver reaches cmd/kubetower"; return 1; fi
  if echo "$deps" | grep -E 'pgx' \
     | grep -vxE 'github.com/jackc/pgx/v5/(pgconn|pgconn/ctxwatch|pgconn/internal/bgreader|pgproto3|internal/iobufpool|internal/pgio)'; then
    echo "a part of pgx other than pgconn reaches cmd/kubetower"; return 1
  fi
  if go list -deps -test ./... | grep -i 'kubetower-extra'; then echo "an import names a kubetower-extra module"; return 1; fi
  echo "no database driver but pgconn, no kubetower-extra import"
}
run "no database driver and no kubetower-extra module" no_drivers

# --- ci.yml: web ------------------------------------------------------------

PNPM=${PNPM:-corepack pnpm}
web() { (cd web && $PNPM "$@"); }

# step: ci/web: pnpm install --frozen-lockfile
run "pnpm install --frozen-lockfile" web install --frozen-lockfile
# step: ci/web: pnpm run lint
run "pnpm run lint" web run lint
# step: ci/web: pnpm run typecheck
run "pnpm run typecheck" web run typecheck
# step: ci/web: pnpm run check:styles
run "pnpm run check:styles" web run check:styles
# step: ci/web: pnpm run check:api
run "pnpm run check:api" web run check:api
# step: ci/web: pnpm run test
# Two workers, as Go runs two packages at a time: Vitest's default of one per
# core beside another suite turns its 5 s timeouts and time ceilings red on a
# loaded machine without any test being wrong. KT_CI_WEB_WORKERS changes it.
run "pnpm run test" web run test --maxWorkers="${KT_CI_WEB_WORKERS:-2}"
# step: ci/web: pnpm run build
run "pnpm run build" web run build

# step: ci/web: the bundle carries the extras' pages
extras_chunks() {
  local page n
  for page in DeliveryCenter KubeCron; do
    n=$(find web/dist/ui/assets -maxdepth 1 -name "${page}-*.js" | wc -l)
    [ "$n" -eq 1 ] || { echo "web/dist/ui/assets holds $n $page chunks, want 1"; return 1; }
    echo "ok   $page"
  done
}
run "the bundle carries the extras' pages" extras_chunks

# runner-only: ci/web: hand the console to the jobs that embed it - an artifact for the full tier's jobs of the same run

# --- left to the runners ----------------------------------------------------
#
# runner-only: ci/platforms - the full tier: every published target cross-compiled (manual run, Sundays, release)
# runner-only: ci/dmg - the full tier: the macOS disk images, libdmg-hfsplus on the pool
# runner-only: ci/appimage - the full tier: the AppImage
# runner-only: ci/setup - the full tier: NSIS and the Windows setup wizard
# runner-only: ci/packages - the full tier: the .deb and .rpm
# runner-only: ci/image - the docker build and its smoke, on the pool's docker
#
# And the other workflows, not replayed: layout.yml (Playwright in its own
# network namespace, under sudo), smoke.yml (the built binary on a kind
# cluster), security.yml, release.yml and the full tier's workflows.

echo
echo "=== summary"
printf '%s\n' "${results[@]}"
if [ "$failed" != 0 ]; then
  echo "task ci: FAILED - the lines marked FAIL above say which check and why"
  exit 1
fi
echo "task ci: every check passed. Not run here: the full tier, the docker build, layout, smoke, security (runner-only)."
