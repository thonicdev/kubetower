/**
 * Live handles: one owner for anything that costs something to open.
 *
 * ## The trap this exists for
 *
 * React 19's StrictMode double-invokes effects in development: mount, run the
 * effect, run its cleanup, run the effect again. A terminal opened in a
 * `useEffect` therefore opens **two** WebSockets and two PTYs, and the backend
 * caps live terminal sessions - so what this looks like is a session limit
 * reached at half the sessions, in development only, which is where it is least
 * expected.
 *
 * Turning StrictMode off hides it. It does not fix it: React may unmount and
 * remount a subtree for its own reasons in production too, and the second PTY
 * would arrive then instead.
 *
 * ## The fix
 *
 * Key the handle and let the second mount find the first. Two parts, and the
 * second is the one that is easy to leave out:
 *
 *   1. **A module-level registry**, so a handle outlives the component that
 *      opened it - which the dock needs anyway, because a terminal has to
 *      survive navigation.
 *   2. **A deferred close.** Refcounting alone does not help: StrictMode's
 *      cleanup takes the count to zero *between* the two mounts, the handle is
 *      disposed, and the remount opens a second one exactly as before. So the
 *      last release schedules the close, and a re-acquire within the grace
 *      period cancels it.
 *
 * The grace period is the whole mechanism and it is a real cost: a handle
 * genuinely abandoned stays open for `LINGER_MS`. That is deliberate, and it is
 * why the close is scheduled rather than merely debounced - `closeHandle`
 * exists for the gesture that means "close this now".
 */

export interface OpenedHandle<T> {
  readonly value: T;
  /** Called when the last holder has released and the grace period has passed.
   *  Must be safe to call exactly once. */
  readonly dispose: () => void;
}

interface Held {
  value: unknown;
  dispose: () => void;
  holders: number;
}

const held = new Map<string, Held>();
const closing = new Map<string, ReturnType<typeof setTimeout>>();

/**
 * Long enough to span StrictMode's unmount/remount, which is synchronous, and a
 * route transition, which is not. Short enough that an abandoned PTY is not
 * still holding a slot a minute later.
 */
const LINGER_MS = 2_000;

/**
 * Get the handle for `key`, opening it if nobody holds it.
 *
 * `open` is called at most once per live handle. Every caller must pair this
 * with exactly one `releaseHandle(key)`.
 */
export function acquireHandle<T>(key: string, open: () => OpenedHandle<T>): T {
  const scheduled = closing.get(key);
  if (scheduled !== undefined) {
    clearTimeout(scheduled);
    closing.delete(key);
  }

  let entry = held.get(key);
  if (entry === undefined) {
    const opened = open();
    entry = { value: opened.value, dispose: opened.dispose, holders: 0 };
    held.set(key, entry);
  }
  entry.holders += 1;
  return entry.value as T;
}

/** Release one hold. The handle closes after the grace period if nobody has
 *  taken it back by then. */
export function releaseHandle(key: string): void {
  const entry = held.get(key);
  if (entry === undefined) return;

  entry.holders -= 1;
  if (entry.holders > 0) return;

  if (closing.has(key)) return;
  closing.set(
    key,
    setTimeout(() => {
      closing.delete(key);
      const current = held.get(key);
      if (current === undefined || current.holders > 0) return;
      held.delete(key);
      current.dispose();
    }, LINGER_MS),
  );
}

/** Close now, whatever the grace period would have said. This is the operator
 *  shutting a terminal, not a component unmounting. */
export function closeHandle(key: string): void {
  const scheduled = closing.get(key);
  if (scheduled !== undefined) {
    clearTimeout(scheduled);
    closing.delete(key);
  }
  const entry = held.get(key);
  if (entry === undefined) return;
  held.delete(key);
  entry.dispose();
}

/** The keys currently open, including ones inside their grace period. For
 *  tests and for a diagnostic panel. */
export function liveHandleKeys(): string[] {
  return [...held.keys()];
}

/** Test seam. Disposes everything and empties the registry. */
export function resetLiveHandles(): void {
  for (const scheduled of closing.values()) clearTimeout(scheduled);
  closing.clear();
  for (const entry of held.values()) entry.dispose();
  held.clear();
}
