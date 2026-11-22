import type { DataRouter } from 'react-router';

/**
 * A tab left open across an upgrade.
 *
 * The SPA's chunks are hashed, and a deployment replaces them. A tab that was
 * loaded before the upgrade still holds the old `index.html`, so the first lazy
 * route it navigates to asks for a chunk that no longer exists. The server
 * answers 404 - deliberately, and never with `index.html`, because answering
 * with HTML turns a recoverable 404 into a MIME error the SPA cannot classify.
 * When the guard is cleared is under `clearChunkReloadGuardOnceRendered`.
 *
 * The browser reports that as a `TypeError` whose message is the only thing
 * distinguishing it, and the wording differs per engine. Matching on a message
 * is fragile and is done here anyway, in one place, because the alternative is a
 * blank screen with an error in the console.
 *
 * The fourth wording is Vite's, not a browser's. Its preload helper fetches a
 * chunk's stylesheet dependencies before the chunk - `dock/terminal.ts` has one
 * - and a stylesheet that 404s rejects with `Unable to preload CSS for <url>`,
 * rethrown through the same `import()`. Same cause, same remedy, one more
 * sentence to recognize.
 */
const CHUNK_FAILURE =
  /dynamically imported module|Importing a module script failed|error loading dynamically imported module|Unable to preload CSS for/i;

export function isChunkLoadFailure(error: unknown): boolean {
  return error instanceof Error && CHUNK_FAILURE.test(error.message);
}

/**
 * Reload once, and only once. The guard is a `sessionStorage` flag rather than a
 * module variable because the reload destroys the module: without it, a chunk
 * that is genuinely missing - a bad deploy rather than a stale tab - reloads
 * forever.
 *
 * `sessionStorage` holds a boolean and never a token: no token is ever kept
 * in browser storage.
 */
const RELOADED = 'kubetower.chunk-reloaded';

/**
 * Whether this tab's one reload has been spent - or cannot be had at all,
 * because storage is unavailable. Read at render by `RouteError`, so the
 * sentence on screen says what is about to happen rather than what the
 * boundary hopes: "Reloading…" over a reload that was refused is a page that
 * looks busy and is not.
 */
export function chunkReloadSpent(): boolean {
  try {
    return sessionStorage.getItem(RELOADED) !== null;
  } catch {
    // Storage can be unavailable - a locked-down profile, private browsing in
    // some engines. Refusing to reload is the safe half of the choice.
    return true;
  }
}

export function reloadForNewBuild(): boolean {
  if (chunkReloadSpent()) return false;
  try {
    sessionStorage.setItem(RELOADED, '1');
  } catch {
    // See `chunkReloadSpent`.
    return false;
  }
  window.location.reload();
  return true;
}

/** Called once the app has rendered successfully, so the next upgrade gets its
 *  own single reload. */
export function clearChunkReloadGuard(): void {
  try {
    sessionStorage.removeItem(RELOADED);
  } catch {
    // See above.
  }
}

/**
 * Release the guard when the router has settled with nothing thrown - and not
 * before.
 *
 * Clearing the guard on the line after `render()` would be too early: with lazy
 * routes that line runs *before* the initial route's chunk has been asked for,
 * so on a build whose chunk is genuinely missing the tab would reload, clear
 * the guard, fail again and reload again - the loop the guard exists to
 * prevent. The word "rendered" has to mean the router reached an
 * idle state with no error, which is what this subscribes to.
 *
 * Every clean settle clears the guard, not only the first: a tab that survived
 * one upgrade and is left open across the next deserves its own reload too.
 *
 * Returns the unsubscribe, which `createApp` has no reason to call and the
 * tests do.
 */
export function clearChunkReloadGuardOnceRendered(router: DataRouter): () => void {
  const settled = (state: DataRouter['state']): boolean =>
    state.initialized && state.navigation.state === 'idle' && state.errors === null;

  // A tree whose initial match is static is already settled when this runs,
  // and `subscribe` does not replay the current state.
  if (settled(router.state)) clearChunkReloadGuard();
  return router.subscribe((state) => {
    if (settled(state)) clearChunkReloadGuard();
  });
}
