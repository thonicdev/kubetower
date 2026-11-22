/**
 * Saying out loud what just happened, for the person who cannot see it.
 *
 * ## Why
 *
 * Every outcome of a rearrangement (`RailArrange`) is announced: without this,
 * moving a tile up, hiding one, renaming one and grouping one all happen in
 * silence to a screen reader, because the only thing that changes is a list of
 * tiles somewhere else on the page.
 *
 * ## Why it is a module and not a context
 *
 * The region has to outlive every route, and announcing must not re-render the
 * application. A context holding the message re-renders everything under the
 * provider on every announcement; a context holding a stable `announce` needs
 * the message to live somewhere else anyway. So the message lives here, at
 * module scope - module identity is what makes state survive navigation - and
 * `LiveRegion` subscribes to it.
 *
 * It is **not** a delegated listener and nothing here reads the DOM: reading an
 * announcement off a fragment would need the attribute removed so the table
 * refresh did not announce it a second time. Announcing at the point the thing
 * happened has no such problem and needs no attribute.
 */

interface Announcement {
  readonly message: string;
  /** Bumped on every announcement, including a repeat of the same words.
   *
   *  Two identical messages in a row are otherwise one change and are announced
   *  once, and 'moved down' twice is exactly what walking a tile down a hotbar
   *  produces. In React an identical string is not even a DOM write. The
   *  sequence number is the fix: `LiveRegion` keys the text node on it, so the
   *  node is replaced rather than left alone, and the announcement lands. */
  readonly seq: number;
}

let current: Announcement = { message: '', seq: 0 };

const listeners = new Set<() => void>();

/** Say something. Empty is ignored - an empty announcement is a region being
 *  cleared, which is not a thing any caller here means. */
export function announce(message: string): void {
  if (message === '') return;
  current = { message, seq: current.seq + 1 };
  for (const listener of [...listeners]) listener();
}

export function subscribeToAnnouncements(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function currentAnnouncement(): Announcement {
  return current;
}

/** Test seam. Leaves the module as a fresh import would, so one test's sentence
 *  is not still on screen in the next. */
export function resetAnnouncements(): void {
  current = { message: '', seq: 0 };
  listeners.clear();
}
