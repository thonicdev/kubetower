import type { KeyLayer } from './layers';

/**
 * A claimant. Return `true` to say the key was consumed - the stack then stops
 * and no lower layer sees it. Return `false` and the next layer down is asked.
 *
 * Returning `true` is a claim about *this* key, not about the layer: a dialog
 * that handles Escape and ignores everything else returns `false` for
 * everything else, and typing still reaches the page beneath it.
 */
export type KeyClaim = (event: KeyboardEvent) => boolean;

interface Entry {
  readonly layer: number;
  readonly claim: KeyClaim;
}

/**
 * Module level, and that is not an accident. The registry has to outlive every
 * component in it - a claimant registered by a route component is gone on
 * navigation, and the stack itself must not be. The module graph makes "runs
 * once" true for free.
 *
 * Ordered on insert: descending layer, and within a layer, most recently
 * registered first. Since entries are only ever appended at the head of their
 * own layer group, insertion is a single scan and dispatch is a plain walk.
 */
const entries: Entry[] = [];

/** Refcount rather than a boolean: React 19's StrictMode mounts, unmounts and
 *  remounts, so a boolean would be flipped off by the first unmount and the
 *  listener would be installed twice or not at all. */
let installs = 0;

function dispatch(event: KeyboardEvent): void {
  // Something closer to the element already dealt with it - a text input
  // swallowing its own Escape, say. Nothing above the page overrides that.
  if (event.defaultPrevented) return;

  // Iterate over a snapshot: a claimant that closes a dialog unregisters
  // another claimant from inside this loop, and splicing the live array under
  // the iterator skips the next entry.
  for (const entry of entries.slice()) {
    if (!entry.claim(event)) continue;
    event.preventDefault();
    // Not arbitration - there is only one listener here, so there is nothing to
    // out-shout. This keeps a consumed key from also reaching React's own
    // handlers at the root container.
    event.stopPropagation();
    return;
  }
}

/**
 * Install the single listener. Idempotent under StrictMode's double mount, and
 * the returned release is idempotent too, because React calls a cleanup exactly
 * once but nothing here should depend on that being true.
 */
export function installKeyboardStack(target: Window = window): () => void {
  if (installs++ === 0) {
    // Capture, so the stack is asked before anything inside the page. React's
    // own listeners sit at the root container, which is below `window`.
    target.addEventListener('keydown', dispatch, true);
  }
  let released = false;
  return () => {
    if (released) return;
    released = true;
    if (--installs === 0) target.removeEventListener('keydown', dispatch, true);
  };
}

/**
 * Register a claimant at a layer. Returns its release.
 *
 * Prefer `useKeyLayer` from a component; this is the primitive underneath it,
 * exported for the places that are not components.
 */
export function claimKey(layer: KeyLayer | number, claim: KeyClaim): () => void {
  const entry: Entry = { layer, claim };

  // Descending layer; newest first within a layer. `entry` is the newest thing
  // in existence, so it belongs before the first entry whose layer is not
  // strictly higher than its own.
  let at = entries.findIndex((e) => e.layer <= layer);
  if (at < 0) at = entries.length;
  entries.splice(at, 0, entry);

  let released = false;
  return () => {
    if (released) return;
    released = true;
    const i = entries.indexOf(entry);
    if (i >= 0) entries.splice(i, 1);
  };
}

/** For tests and for a diagnostic panel. The layers currently claiming, highest
 *  first. Never use it to decide anything at runtime: if a component needs to
 *  know whether it is on top, it is asking the wrong question - it should claim
 *  a layer and be told. */
export function claimedLayers(): number[] {
  return entries.map((e) => e.layer);
}

/** Test seam. Leaves the registry as a fresh module would. */
export function resetKeyboardStack(): void {
  entries.length = 0;
  installs = 0;
}
