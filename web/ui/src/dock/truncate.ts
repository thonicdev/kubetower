/**
 * Keep both ends of a name, because both carry meaning.
 *
 * `internal/web/rail.go`'s `middleTruncate`, rune for rune. These names are
 * `FAMILY-EKS-N` and `FAMILY-EKS-N_OWNER`, and a pod is `api-7f9c4b8d6-x2kqp`.
 * Cutting the tail leaves three labels reading `STAGING-E…`; cutting the head
 * leaves three reading `…KS-1_OWNER`. **Both are wrong.**
 *
 * One function, because hand-rolled copies drift: different ceilings, and a
 * byte-index slice that cuts a multi-byte character in half. Five fits go through this one: a tab name at 22,
 * a Claude tab at 20, a log tab at 26, a scope-menu row, and the rail's own 11.
 *
 * ## The rune-for-rune requirement is the load-bearing half
 *
 * `String.prototype.slice` is a **UTF-16 code-unit** slice and will cut an
 * astral character in half - an emoji in a namespace name, a CJK name - leaving
 * a lone surrogate that renders as a replacement character. Go's version slices
 * `[]rune`. `[...s]` is the JavaScript equivalent: the string iterator yields
 * code points, so a surrogate pair is one element.
 *
 * The arithmetic is Go's exactly: a 2/5 head and a 3/5 tail around a
 * single-rune ellipsis, with the tail computed first so the rounding falls the
 * same way.
 */
export function middleTruncate(s: string, fit: number): string {
  const runes = [...s];
  if (runes.length <= fit) return s;
  // `(fit - 1) * 3 / 5` in Go is integer division. `Math.floor` is the same for
  // the non-negative values this is ever called with, and `fit` below 1 would
  // be a caller error rather than a case to handle.
  const tail = Math.floor(((fit - 1) * 3) / 5);
  const head = fit - 1 - tail;
  return runes.slice(0, head).join('') + '…' + runes.slice(runes.length - tail).join('');
}

/**
 * A cluster name shortened for a tab.
 *
 * Cut an ARN at its last `/` first, then middle-
 * truncate. An EKS context name is
 * `arn:aws:eks:eu-west-1:123456789012:cluster/example-prod-eks`, and middle-
 * truncating that whole string keeps the account number and loses the cluster.
 */
export function shortCluster(name: string, fit = 24): string {
  const slash = name.lastIndexOf('/');
  const tail = slash >= 0 ? name.slice(slash + 1) : name;
  return middleTruncate(tail, fit);
}
