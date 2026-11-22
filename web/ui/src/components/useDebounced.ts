import { useCallback, useEffect, useState } from 'react';

/**
 * A value, settled.
 *
 * The address bar keeps up with every keystroke — `useUrlParameter` says why the
 * control must be the parameter verbatim — but the *request* must not. A list
 * request is a cluster read, and one per keystroke turns an eight-character
 * filter into eight listings of every pod in a cluster, seven of which are
 * canceled. In a router it is explicit work: nothing in the browser delays
 * the request for us.
 *
 * Deliberately **not** applied to the control's value. Debouncing what is drawn
 * is how a box ends up showing something the operator did not type.
 */
export function useDebounced<T>(value: T, delayMs: number): T {
  return useSettling(value, delayMs)[0];
}

/**
 * `useDebounced`, plus a way to settle at once.
 *
 * **The one settling every search box in the console goes through.**
 * The list's box settles at 250 ms because what it settles into is a cluster
 * read; the log pane's at 140 ms because what it settles into is one pass over a
 * buffer of up to a hundred thousand lines, and Enter there settles now. One
 * hook, because two copies of one timer are two implementations of one
 * behavior waiting to differ. The two boxes that filter
 * what is already in hand - the kinds menu's *Jump to a kind* and the namespace
 * menu - do not settle at all, because there is nothing to wait for: a menu's
 * worth of names is narrowed in the same render as the keystroke.
 *
 * The `filtering…` marker (`SettlingMark`) is drawn only where settling means
 * waiting on the cluster - the list's box. The log pane settles the same way
 * and draws none: its 140 ms is a pass over a local buffer, below what reads as
 * waiting, and a live region announcing it per keystroke would be noise.
 *
 * `settleNow` writes the value the box holds *now*, not the one it held when
 * the callback was made, so a caller may hand it to a key handler without
 * caring which render it came from.
 */
export function useSettling<T>(value: T, delayMs: number): readonly [T, () => void] {
  const [settled, setSettled] = useState(value);

  useEffect(() => {
    if (Object.is(settled, value)) return;
    const timer = setTimeout(() => {
      setSettled(value);
    }, delayMs);
    return () => {
      clearTimeout(timer);
    };
  }, [value, delayMs, settled]);

  const settleNow = useCallback(() => {
    setSettled(value);
  }, [value]);

  return [settled, settleNow] as const;
}
