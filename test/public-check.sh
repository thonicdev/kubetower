#!/usr/bin/env bash
# Refuse a reference that a reader of this public repository cannot follow.
#
#   bash test/public-check.sh tree              every tracked file, this one
#                                               included - its name and its content
#   bash test/public-check.sh commits <range>   every commit message in a git range
#   bash test/public-check.sh message <label>   the text on stdin - a pull
#                                               request's title and description
#   bash test/public-check.sh --self-test       prove every scan can fail, then stop
#
# What it refuses: the names of the organization's private repositories and
# documents, and the name of the prototype this console was migrated from -
# spelt with a hyphen, an underscore, a space or nothing between their words,
# in any case - and identifiers from its private decision and fix registers: a
# D, O or FX, a hyphen and a number, in any case. A reference nobody outside
# can open is worse than none, because it reads like evidence. The prototype's
# name is refused because this console carries none of it: the rule is this
# repository's own, so it is built in rather than left to a variable.
#
# Two names are deliberately not refused here. The extras' contract is named
# `kubetower-extra` on legitimate lines of the code, so refusing it would refuse
# the contract itself. And the server-mode threat model is a public document of
# this tree, so its name is an ordinary link; if that document ever moves out,
# its name goes back into the list below and into the self-test's.
#
# One allowance, in messages only: a line that does nothing but close an issue
# on the private documentation repository - `Closes <that repository>#58`,
# optionally with the organization in front. That is the one place a private
# issue may be named, because it is how the loop back to it closes. A file in
# the tree gets no allowance, and neither does a Closes line carrying anything
# else.
#
# Why messages as well as the tree: this repository squash-merges with the pull
# request's title and description as the commit message, so the description is
# published on `main` exactly as a file is. The commit messages of the branch
# do not reach `main`, but they stay readable on the pull request for good.
#
# The names are assembled from pieces below, so that this file does not spell
# what it refuses - the tree scan reads it like any other file and excludes
# nothing. A pattern the operator does not want to publish at all can be added
# through the PUBLIC_CHECK_EXTRA environment variable, which the workflow fills
# from a repository secret of the same name when one exists (a secret, so the
# patterns are masked in public logs; a fork's pull request runs without them).
#
# A binary file - one git marks binary, or whose first 8000 bytes hold a NUL,
# the test git itself uses - is checked by its path, not by its bytes: the
# compressed data of a screenshot spells a short identifier by chance often
# enough to fail a pull request for nothing. What a binary file can say in
# words is read instead where the format keeps words: a PNG's tEXt and
# uncompressed iTXt chunks are scanned like a file, and a PNG carrying text this
# script cannot read (zTXt, a compressed iTXt, or a chunk that runs past the end)
# is refused. Other binary formats are taken on their path alone.
#
# Exit status: 0 clean, 1 a reference was found, 2 the scan itself failed. The
# last is not a pass: a scan that could not read is a scan that saw nothing.
set -u

self="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"

# Each private name with its words joined by ':', which no separator below
# matches - so neither this list nor the pattern built from it is a hit.
words='kubetower:docs claude:work claude:plugins claude:global claude:kubetower kube:deck'
sep='[-_ ]?'
names=''
for w in $words; do
  p="${w//:/$sep}"
  # The docs repository's old name, but not the assistant's own working
  # directory, `claude-workspace`, a name the console uses on purpose.
  [ "$w" = claude:work ] && p="$p([^s]|$)"
  names="${names:+$names|}$p"
done
ids='\b(D|O|FX)-[0-9]+\b'
extra="${PUBLIC_CHECK_EXTRA:-}"

# The allowed Closes line, lower case because it is compared lower-cased.
docs_repo="${words%% *}"; docs_repo="${docs_repo//:/-}"
closes="^[[:space:]]*closes[[:space:]]+(thonicdev/)?${docs_repo}#[0-9]+[[:space:]]*\$"

# Blank every line that only closes a documentation issue, keeping the line
# numbers of the rest.
allow_closes() {
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    if [[ ${line,,} =~ $closes ]]; then echo; else printf '%s\n' "$line"; fi
  done
}

# Print every matching line of stdin, prefixed with $1. With $2 = closes, a
# line that only closes a documentation issue is not read. Returns what grep
# returns: 0 when there was a match, 1 when there was none, 2 when it failed.
match() {
  local label=$1 allow=${2:-} input found=1 rc
  input=$(cat) || return 2
  [ "$allow" = closes ] && input=$(printf '%s\n' "$input" | allow_closes)
  for spec in "i:$names" "i:$ids" ${extra:+"s:$extra"}; do
    local flags=-nE
    [ "${spec%%:*}" = i ] && flags=-niE
    printf '%s\n' "$input" | grep $flags -e "${spec#*:}" | sed "s|^|$label:|"
    rc=${PIPESTATUS[1]}
    case $rc in
      0) found=0 ;;
      1) ;;
      *) echo "grep failed ($rc) on $label" >&2; return 2 ;;
    esac
  done
  return $found
}

# Fill `binary` with the tracked paths git calls binary: by its own content
# test (`i/-text`) or by the attribute (`binary`, `-text`) of .gitattributes.
declare -A binary=()
binary_paths() {
  local entry path attr value
  while IFS= read -r -d '' entry; do
    path=${entry#*$'\t'}
    [[ $entry == i/-text* ]] && binary[$path]=1
  done < <(git ls-files --eol -z)
  while IFS= read -r -d '' path && IFS= read -r -d '' attr && IFS= read -r -d '' value; do
    [ "$value" = set ] && binary[$path]=1
  done < <(git ls-files -z | git check-attr -z --stdin binary)
  return 0
}

# Whether the first 8000 bytes of $1 hold a NUL: git's own test, for a file
# that is not in the index as binary yet.
has_nul() {
  local all kept
  all=$(head -c 8000 "$1" | wc -c) || return 1
  kept=$(head -c 8000 "$1" | tr -d '\000' | wc -c) || return 1
  [ "$all" -ne "$kept" ]
}

# Print the words a PNG keeps: each tEXt and uncompressed iTXt chunk, its NUL
# separators as new lines. Returns 0 when every text chunk was read, 3 when one
# could not be (zTXt, a compressed iTXt, a chunk running past the end), 4 when
# $1 is not a PNG, 2 when it could not be read.
png_text() {
  local f=$1 size off=8 len type b kw flag
  size=$(wc -c < "$f") || return 2
  [ "$(head -c 8 "$f" | od -An -tx1 | tr -d ' \n')" = 89504e470d0a1a0a ] || return 4
  while [ "$off" -lt "$size" ]; do
    read -r -a b <<< "$(od -An -tu1 -j "$off" -N 4 "$f")"
    [ "${#b[@]}" -eq 4 ] || return 3
    len=$(( (b[0] << 24) | (b[1] << 16) | (b[2] << 8) | b[3] ))
    [ $((off + 12 + len)) -le "$size" ] || return 3
    type=$(od -An -c -j $((off + 4)) -N 4 "$f" | tr -d ' \n')
    case $type in
      tEXt) tail -c +$((off + 9)) "$f" | head -c "$len" | tr '\000' '\n'; echo ;;
      iTXt)
        # keyword NUL, then the compression flag.
        kw=$(tail -c +$((off + 9)) "$f" | head -c "$len" | tr '\000' '\n' | head -n 1 | wc -c)
        [ "$kw" -lt "$len" ] || return 3
        flag=$(od -An -tu1 -j $((off + 8 + kw)) -N 1 "$f" | tr -d ' \n')
        [ "$flag" = 0 ] || return 3
        tail -c +$((off + 9)) "$f" | head -c "$len" | tr '\000' '\n'; echo ;;
      zTXt) return 3 ;;
      IEND) return 0 ;;
    esac
    off=$((off + 12 + len))
  done
  return 0
}

tree() {
  local files hit=0 rc skipped=0
  # Every tracked file, with no path left out: a file the scan skips is a file
  # that publishes whatever it says.
  mapfile -d '' files < <(git ls-files -z) || return 2
  binary_paths || return 2
  if [ "${#files[@]}" -eq 0 ]; then
    echo "git ls-files returned no files - nothing was scanned" >&2
    return 2
  fi
  for f in "${files[@]}"; do
    # The path first: a file can publish a name by being called it.
    printf '%s\n' "$f" | match "file name $f"
    rc=$?
    [ $rc -eq 2 ] && return 2
    [ $rc -eq 0 ] && hit=1
    [ -f "$f" ] || continue
    if [ -n "${binary[$f]:-}" ] || has_nul "$f"; then
      skipped=$((skipped + 1))
      png_text "$f" | match "$f (PNG text)"
      rc=("${PIPESTATUS[@]}")
      case ${rc[0]} in
        0|4) ;;
        3) echo "$f: a PNG carrying text this scan cannot read (compressed or cut short)"; hit=1; continue ;;
        *) echo "could not read $f" >&2; return 2 ;;
      esac
      [ "${rc[1]}" -eq 2 ] && return 2
      [ "${rc[1]}" -eq 0 ] && hit=1
      continue
    fi
    match "$f" < "$f"
    rc=$?
    [ $rc -eq 2 ] && return 2
    [ $rc -eq 0 ] && hit=1
  done
  echo "scanned ${#files[@]} tracked files: every name, the content of the text files," \
    "and the PNG text of the $skipped binary ones" >&2
  return $hit
}

commits() {
  local range=$1 shas hit=0 rc
  shas=$(git rev-list "$range") || { echo "git rev-list $range failed" >&2; return 2; }
  if [ -z "$shas" ]; then
    # Empty is not clean: a range with nothing in it is usually a range that
    # was computed against a history the checkout does not have.
    echo "no commits in $range - nothing was scanned" >&2
    return 2
  fi
  for c in $shas; do
    git log -1 --format=%B "$c" > /dev/null || return 2
    git log -1 --format=%B "$c" | match "commit $(git rev-parse --short "$c")" closes
    rc=${PIPESTATUS[1]}
    [ $rc -eq 2 ] && return 2
    [ $rc -eq 0 ] && hit=1
  done
  echo "scanned $(printf '%s\n' "$shas" | wc -l | tr -d ' ') commit messages in $range" >&2
  return $hit
}

message() {
  local label=$1 text rc
  text=$(cat) || return 2
  # A title is never empty, so an empty message is a message that did not
  # arrive - an unset variable, a wrong event field - not a clean one.
  if [ -z "${text//[[:space:]]/}" ]; then
    echo "the $label is empty - nothing was scanned" >&2
    return 2
  fi
  printf '%s\n' "$text" | match "$label" closes
  rc=${PIPESTATUS[1]}
  [ $rc -eq 2 ] && return 2
  echo "scanned the $label, $(printf '%s\n' "$text" | wc -l | tr -d ' ') lines" >&2
  # match answers "found", so its 0 is this scan's 1.
  [ $rc -eq 1 ] && return 0
  return 1
}

# planted_png [TYPE DATA]...: a PNG's bytes on stdout - signature, IHDR, each
# chunk given (DATA in printf's escapes), IEND. The CRCs are left at zero: the
# scan never checks them.
png_chunk() {
  local data len
  data=$(mktemp) || return 2
  printf "$2" > "$data"
  len=$(wc -c < "$data")
  local size
  size=$(printf '\\%03o\\%03o\\%03o\\%03o' $((len >> 24 & 255)) $((len >> 16 & 255)) $((len >> 8 & 255)) $((len & 255)))
  printf "$size"
  printf '%s' "$1"
  cat "$data"
  printf '\0\0\0\0'
  rm -f "$data"
}
planted_png() {
  printf '\211PNG\r\n\032\n'
  png_chunk IHDR '\0\0\0\1\0\0\0\1\010\0\0\0\0'
  while [ $# -ge 2 ]; do png_chunk "$1" "$2"; shift 2; done
  png_chunk IEND ''
}

self_test() {
  local tmp rc n=0 w s planted
  local docs="${docs_repo}"
  # Every refused name, in every spelling of its separator and once in
  # capitals - all built at run time.
  for w in $words; do
    for s in - _ ' ' ''; do
      planted="${w//:/$s}"
      printf 'see %s for why\n' "$planted" | match planted > /dev/null
      [ $? -eq 0 ] || { echo "self-test: '$planted' was not caught" >&2; return 2; }
      n=$((n + 1))
    done
    planted="${w//:/-}"
    printf 'see %s for why\n' "${planted^^}" | match planted > /dev/null
    [ $? -eq 0 ] || { echo "self-test: '${planted^^}' was not caught" >&2; return 2; }
    n=$((n + 1))
  done
  # The assistant's working directory shares a prefix with a refused name and
  # is not one.
  for planted in "claude""-workspace" "claude"" workspace"; do
    printf 'in the %s directory\n' "$planted" | match planted > /dev/null 2>&1
    [ $? -eq 1 ] || { echo "self-test: '$planted' was caught" >&2; return 2; }
    n=$((n + 1))
  done
  # And a list of its own, spelt apart from $words, so that a name dropped from
  # that list fails here rather than silently stops being asked about.
  local required=("kubetower""-docs" "claude""-work" "claude""-plugins" "Claude""-global"
    "Claude""-Kubetower" "Kube""Deck" "kube"" deck" "KUBE""DECK"
    "kubetower"" docs" "kubetower""_docs" "kubetower""docs")
  for planted in "${required[@]}"; do
    printf 'moved to %s\n' "$planted" | match planted > /dev/null
    [ $? -eq 0 ] || { echo "self-test: '$planted' was not caught" >&2; return 2; }
    n=$((n + 1))
  done
  # One of each identifier, in capitals and in lower case.
  for id in D O FX d o fx; do
    printf 'decided in %s-%d\n' "$id" 35 | match planted > /dev/null
    [ $? -eq 0 ] || { echo "self-test: an $id identifier was not caught" >&2; return 2; }
    n=$((n + 1))
  done
  # And the things that look close and are ordinary.
  local ordinary=('sha-256' 'ISO-8601' 'a D-day' 'kubetower-helm-charts#2' 'FX rates'
    'covid-19' 'x-1' 'demo-1' 'utf-8' 'the KubeTower console' 'Closes #5'
    'the kubetower-extra contract' 'docs/SERVER-MODE-THREAT-MODEL.md')
  printf '%s\n' "${ordinary[@]}" | match clean > /dev/null
  [ $? -eq 1 ] || { echo "self-test: an ordinary line was refused" >&2; return 2; }

  # The Closes allowance: a line that only closes a documentation issue passes
  # a message, and nothing else rides on it.
  printf 'feat: a change\n\nCloses thonicdev/%s#58\r\ncloses %s#59\n' "$docs" "$docs" \
    | bash "$self" message "planted description" 2> /dev/null > /dev/null
  [ $? -eq 0 ] || { echo "self-test: a Closes line was refused" >&2; return 2; }
  for planted in "see $docs#58" "Closes $docs#58 as decided in D-$((30 + 5))" \
                 "Closes $docs#58, and $docs has the rest" "Fixes $docs#58"; do
    printf 'feat: a change\n\n%s\n' "$planted" | bash "$self" message "planted description" > /dev/null 2>&1
    [ $? -eq 1 ] || { echo "self-test: '$planted' passed a message" >&2; return 2; }
    n=$((n + 1))
  done
  printf '' | bash "$self" message "planted description" > /dev/null 2>&1
  [ $? -eq 2 ] || { echo "self-test: an empty message read as clean" >&2; return 2; }

  # Then the scans end to end, in a throwaway repository.
  tmp=$(mktemp -d) || return 2
  (
    set -e
    cd "$tmp"
    git init -q
    git config user.name self-test
    git config user.email self-test@example.invalid
    git config core.autocrlf false
    printf 'clean\n' > a.txt
    git add a.txt
    git commit -q -m 'chore: a clean start'
    git commit -q --allow-empty -m 'chore: a clean message'
  ) || { rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" tree ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 0 ] || { echo "self-test: a clean tree was refused ($rc)" >&2; rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" commits HEAD~1..HEAD ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 0 ] || { echo "self-test: a clean message was refused ($rc)" >&2; rm -rf "$tmp"; return 2; }
  # A squash of a pull request closing a documentation issue lands on main.
  ( cd "$tmp" && git commit -q --allow-empty -m "feat: a change" -m "Closes thonicdev/$docs#58" ) \
    || { rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" commits HEAD~1..HEAD ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 0 ] || { echo "self-test: a squash closing an issue was refused ($rc)" >&2; rm -rf "$tmp"; return 2; }
  ( cd "$tmp" \
    && git commit -q --allow-empty -m "feat: a change" -m "as decided in d-$((30 + 5))" ) \
    || { rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" commits HEAD~3..HEAD ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 1 ] || { echo "self-test: a planted commit message passed ($rc)" >&2; rm -rf "$tmp"; return 2; }
  # A file named after a private repository, with nothing wrong inside it.
  ( cd "$tmp" && printf 'clean\n' > "${docs}-notes.md" && git add "${docs}-notes.md" ) \
    || { rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" tree ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 1 ] || { echo "self-test: a planted file name passed ($rc)" >&2; rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && git rm -q --cached "${docs}-notes.md" && rm "${docs}-notes.md" ) \
    || { rm -rf "$tmp"; return 2; }
  # And a Closes line in a file, which gets no allowance.
  ( cd "$tmp" && printf 'Closes %s#58\n' "$docs" > b.txt && git add b.txt ) || { rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" tree ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 1 ] || { echo "self-test: a planted file passed ($rc)" >&2; rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && git rm -q --cached b.txt && rm b.txt ) || { rm -rf "$tmp"; return 2; }
  # A binary file is read by its words, not its bytes: an identifier the
  # compressed data of a PNG spells by chance passes, the same identifier in a
  # text file fails, and a PNG saying a private name in a text chunk fails, as
  # does one carrying text the scan cannot read.
  local by_chance="x\0\1d-$((30 + 5))\0\2y"
  ( cd "$tmp" && planted_png IDAT "$by_chance" > shot.png && git add shot.png ) || { rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" tree ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 0 ] || { echo "self-test: a PNG whose bytes spell an identifier was refused ($rc)" >&2; rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && printf "$by_chance" | tr '\000\001\002' '   ' > c.txt && git add c.txt ) || { rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && bash "$self" tree ) > /dev/null 2>&1
  rc=$?
  [ $rc -eq 1 ] || { echo "self-test: the same identifier in a text file passed ($rc)" >&2; rm -rf "$tmp"; return 2; }
  ( cd "$tmp" && git rm -q --cached c.txt && rm c.txt ) || { rm -rf "$tmp"; return 2; }
  local chunk
  for chunk in "tEXt:Comment\0see $docs" "iTXt:Comment\0\0\0en\0\0see $docs" "zTXt:Comment\0\0xyz"; do
    ( cd "$tmp" && planted_png IDAT "$by_chance" "${chunk%%:*}" "${chunk#*:}" > shot.png && git add shot.png ) \
      || { rm -rf "$tmp"; return 2; }
    ( cd "$tmp" && bash "$self" tree ) > /dev/null 2>&1
    rc=$?
    [ $rc -eq 1 ] || { echo "self-test: a PNG with a planted ${chunk%%:*} chunk passed ($rc)" >&2; rm -rf "$tmp"; return 2; }
    n=$((n + 1))
  done
  rm -rf "$tmp"
  echo "ok    self-test - $n planted references caught, ${#ordinary[@]} ordinary lines and two"
  echo "      Closes lines passed, and a planted commit message, file name, file and"
  echo "      PNG text chunk each failed a real scan while a squash closing an issue"
  echo "      and an identifier spelt by a PNG's compressed bytes did not"
}

case "${1:-}" in
  --self-test) self_test; exit $? ;;
  tree) tree; rc=$? ;;
  commits)
    [ -n "${2:-}" ] || { echo "usage: $0 commits <range>" >&2; exit 2; }
    commits "$2"; rc=$? ;;
  message)
    [ -n "${2:-}" ] || { echo "usage: $0 message <label> < text" >&2; exit 2; }
    message "$2"; rc=$? ;;
  *) echo "usage: $0 tree | commits <range> | message <label> | --self-test" >&2; exit 2 ;;
esac

case $rc in
  0) echo "no private references" ;;
  1) echo "::error::a private reference would be published - state the reasoning in plain words instead" ;;
  *) echo "::error::the scan could not run, so it saw nothing" ;;
esac
exit $rc
