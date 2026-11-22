import { useEffect, type Dispatch, type RefObject, type SetStateAction, type UIEvent } from 'react';

import { LOG_CHUNK, LOG_WINDOW } from './logBuffer';

/**
 * Following the newest line, and keeping the reader's place when the window
 * grows upward.
 *
 * Whether the reader is at the newest line (`pinnedRef`) is **remembered rather
 * than measured**, and this is the measurement that decides the whole
 * architecture: reading `scrollHeight`, `scrollTop` and `clientHeight` as each
 * line arrives is 3.4 ms per line on a two-thousand line window - five thousand
 * lines took seventeen seconds and the tab was unusable throughout. So the
 * scroll listener this returns is the only place that measures, the arrival
 * path only appends, and the geometry happens once per repaint.
 */
export function useLogScroll({
  hostRef, pinnedRef, grewRef, held, windowSize, setWindowSize, atWindowTop, loadEarlier,
}: {
  hostRef: RefObject<HTMLDivElement | null>;
  pinnedRef: RefObject<boolean>;
  /** The distance to the bottom before the window grew upward, or `null`. */
  grewRef: RefObject<number | null>;
  /** Whether a selection holds the window still. */
  held: boolean;
  windowSize: number;
  setWindowSize: Dispatch<SetStateAction<number>>;
  atWindowTop: boolean;
  loadEarlier: () => void;
}): (event: UIEvent<HTMLDivElement>) => void {
  // The scroll compensation, after the prepended rows are in the document.
  //
  // Runs before the pinning effect below and returns early there, because
  // `pinned` was set false by the fetch: the two must not both write
  // `scrollTop` in one paint, and the order is what decides which one wins.
  useEffect(() => {
    const before = grewRef.current;
    if (before === null) return;
    grewRef.current = null;
    const element = hostRef.current;
    if (element === null || element.clientHeight === 0) return;
    // Not "add the height of what arrived", which needs a measurement per row.
    // The distance from the reader to the **bottom** is invariant under a
    // prepend, so restoring it puts the same line under the same pixel.
    element.scrollTop = element.scrollHeight - before;
  });

  // Back to the newest line, once per repaint and never per line. Without it a
  // followed panel silently stops moving and reads as a stream that ended -
  // which is the mirror of the failure the marker exists to prevent.
  useEffect(() => {
    // Not while a selection is held: scrolling would carry it out of sight,
    // and a drag still in progress would grow over the lines moving under it.
    if (!pinnedRef.current || held) return;
    const element = hostRef.current;
    if (element === null) return;
    // A hidden pane measures 0x0, so every scroll number is zero, "already at
    // the bottom" is trivially true, and writing scrollTop pins the view to the
    // **top**. Every geometry read needs this guard.
    if (element.clientHeight === 0) return;
    element.scrollTop = element.scrollHeight;
  });

  return (event) => {
    // The one place that measures. Two pixels of slack, because a
    // fractional scroll height never lands exactly on the bottom.
    const element = event.currentTarget;
    pinnedRef.current =
      element.scrollHeight - element.scrollTop - element.clientHeight <= 2;
    // Back at the newest line: the window that grew upward goes back to
    // its size. Grown, it would keep every row a page backwards brought
    // in - thousands - in the document for the life of the pane, each
    // re-rendered on every arrival. What was read is still in the
    // buffer, and scrolling up draws it again.
    if (pinnedRef.current && windowSize > LOG_WINDOW) {
      setWindowSize(LOG_WINDOW);
      return;
    }
    // Near the top: grow the drawn window upward first, and only ask the
    // server once there is nothing left in the buffer to draw. Two
    // distinct gestures that feel like one, and the reason `loadEarlier` is not called on every
    // scroll to the top of a hundred-thousand-line buffer.
    if (element.scrollTop > 64) return;
    if (!atWindowTop) {
      setWindowSize((was) => was + LOG_CHUNK);
      grewRef.current = element.scrollHeight - element.scrollTop;
      return;
    }
    loadEarlier();
  };
}
