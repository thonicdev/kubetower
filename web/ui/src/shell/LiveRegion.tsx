import { useSyncExternalStore, type ReactNode } from 'react';

import { currentAnnouncement, subscribeToAnnouncements } from './announce';

/**
 * The one polite live region, mounted in the Shell.
 *
 * `announce.ts` holds the message and says why it is a module rather than a
 * context. This is the element, and it is deliberately thin: it subscribes,
 * it renders, and nothing in the application re-renders when it does.
 *
 * **One region for the whole console, and that is not a simplification.** A
 * live region that is unmounted and remounted by a route change is a
 * new region with nothing to compare against, and the Shell is the one place
 * guaranteed to stay mounted.
 *
 * `sr-only` and not inline styles: the stylesheet is the bundle, and a page
 * whose CSS did not arrive has no rail to announce.
 */
export function LiveRegion(): ReactNode {
  const { message, seq } = useSyncExternalStore(
    subscribeToAnnouncements,
    currentAnnouncement,
    currentAnnouncement,
  );

  return (
    <p role="status" aria-live="polite" className="sr-only">
      {/* Keyed on the sequence number, so saying the same words twice replaces
          the text node instead of leaving it untouched. See `announce.ts`. */}
      <span key={seq}>{message}</span>
    </p>
  );
}
