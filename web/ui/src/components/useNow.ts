import { useEffect, useState } from 'react';

/**
 * A clock, so that anything drawn relative to "now" moves without the operator
 * touching anything.
 *
 * Rendering `Date.now()` and hoping a parent re-renders is how a countdown shows
 * the same number for an hour: nothing in this shell re-renders on a schedule,
 * and the one query that polls is the cluster list, which is a sibling of
 * everything that draws a time. The session tile learnt this first; the Helm
 * `Updated` cell then did exactly the same thing one file over, and a list left
 * open said "4m" indefinitely. One hook, so the next relative time cannot
 * repeat it.
 *
 * Thirty seconds, because the coarsest thing drawn from it is a minute and the
 * finest a second nobody is watching that closely.
 */
export const NOW_TICK_MS = 30_000;

export function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), NOW_TICK_MS);
    return () => clearInterval(id);
  }, []);
  return now;
}
