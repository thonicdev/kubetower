/**
 * Where to go back to after signing in.
 *
 * ## In the URL, not in router state
 *
 * `navigate(..., { state })` loses the destination on a reload, and reloading
 * the sign-in page is the single most likely thing an operator does there. The
 * query parameter survives it.
 *
 * ## And it is validated, because it is attacker-controllable
 *
 * A `next` that is not checked is an open redirect: `/login?next=https://…`
 * turns this console's own sign-in page into a credible hop to somewhere else.
 * The rule is the narrow one - a single leading `/`, nothing that could be read
 * as a scheme or as a host - rather than a list of things to reject, because a
 * denylist here is wrong in the direction that matters.
 */
export const NEXT_PARAM = 'next';

/** The single-page route that hands over to the server's sign-in page. */
export const SIGN_IN_PATH = '/login';

/**
 * The sign-in page itself, which the server renders. It is the one a browser
 * with no session is sent to by any navigation (the session guard redirects
 * there), so it is also the one the console leaves for: two sign-in screens
 * would be two places to keep saying the same things.
 */
export const SERVER_SIGN_IN_PATH = '/auth/login';

export function safeNext(raw: string | null | undefined): string {
  if (raw === null || raw === undefined || raw === '') return '/';
  // A protocol-relative URL (`//evil.example`) is a host, and a backslash is
  // treated as a slash by several engines' URL parsers.
  if (!raw.startsWith('/') || raw.startsWith('//')) return '/';
  if (/[\\\n\r\t]/.test(raw)) return '/';
  // Signing in to arrive back at the sign-in page is a loop.
  for (const path of [SIGN_IN_PATH, SERVER_SIGN_IN_PATH]) {
    if (raw === path || raw.startsWith(`${path}?`) || raw.startsWith(`${path}/`)) return '/';
  }
  return raw;
}

/** The sign-in address that will come back here. */
export function signInPath(from: { pathname: string; search: string; hash: string }): string {
  const next = `${from.pathname}${from.search}${from.hash}`;
  if (safeNext(next) === '/') return SERVER_SIGN_IN_PATH;
  return `${SERVER_SIGN_IN_PATH}?${NEXT_PARAM}=${encodeURIComponent(next)}`;
}
