import { useEffect, useRef, type Dispatch, type RefObject, type SetStateAction } from 'react';

import type { LogBuffer, LogLine } from './logBuffer';

/**
 * Holds the window still while a selection is inside the pane (see `LogPane`).
 *
 * `setHeld` is given the newest line drawn when a selection inside the pane
 * begins, and `null` once nothing in it is selected.
 */
export function useLogSelection({
  hostRef, buffer, rows, setHeld,
}: {
  hostRef: RefObject<HTMLDivElement | null>;
  buffer: LogBuffer;
  /** The buffer indexes drawn, oldest first. */
  rows: readonly number[];
  setHeld: Dispatch<SetStateAction<LogLine | null>>;
}): void {
  /** The newest line drawn, written after each paint, so the selection
   *  listener knows where to hold without reading the render's values. */
  const newestDrawn = useRef<LogLine | null>(null);

  useEffect(() => {
    const last = rows[rows.length - 1];
    newestDrawn.current = last === undefined ? null : buffer.lines[last] ?? null;
  });

  // Whether a selection is inside this pane. **On `document`, because that is
  // the only place a browser reports a selection changing** - there is no
  // per-element event for it, for a drag or for a script. It decides nothing
  // about keys: copying stays the browser's, and this only says whether the
  // rows under a selection must be kept still. A hidden pane holds no
  // selection, so it lets go when the reader looks at another tab and selects
  // there.
  useEffect(() => {
    const onChange = (): void => {
      const selection = document.getSelection();
      const element = hostRef.current;
      const inside =
        selection !== null &&
        !selection.isCollapsed &&
        element !== null &&
        (element.contains(selection.anchorNode) || element.contains(selection.focusNode));
      setHeld((was) => (inside ? was ?? newestDrawn.current : null));
    };
    document.addEventListener('selectionchange', onChange);
    return () => document.removeEventListener('selectionchange', onChange);
  }, [hostRef, setHeld]);
}
