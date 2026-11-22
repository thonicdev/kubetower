import type { ReactNode } from 'react';

/**
 * *The answer on screen is not yet the answer to what was typed.*
 *
 * The marker for a search box whose settling is a wait on the cluster
 * - today the list's filter - drawn beside the box, never over the
 * rows. The log pane settles through the same `useSettling` and draws none:
 * `useDebounced.ts` says why. Not a spinner over the table: the rows on screen are
 * still the last true answer, and blanking them would be a worse lie than saying
 * that a newer one is on its way.
 *
 * **Its room is held whether or not it is showing.** The list's box sits in a
 * right-aligned group, so a marker that took width only while it was drawn
 * pushed the box half a word to the left on the first keystroke and back again
 * when the answer arrived - the field sliding under the caret, once per settled
 * word, which is the same flicker this marker exists to replace. The invisible
 * copy is what holds the room; the live region beside it is what is read out.
 */
export function SettlingMark({
  settling,
  word = 'filtering…',
}: {
  settling: boolean;
  word?: string;
}): ReactNode {
  return (
    <span className="relative text-xs whitespace-nowrap text-muted" data-settling={settling}>
      <span aria-hidden="true" className="invisible">
        {word}
      </span>
      <span aria-live="polite" className="absolute inset-0">
        {settling ? word : ''}
      </span>
    </span>
  );
}
