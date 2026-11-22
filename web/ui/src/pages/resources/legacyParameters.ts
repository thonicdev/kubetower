import { useEffect } from 'react';
import { useSearchParams } from 'react-router';

/**
 * The older names for a list's namespace and search (`?ns=` and `?q=`), and the
 * names that replaced them.
 *
 * Bookmarks and pasted links may still carry the old names. Left unread, such a
 * link would open the whole list with "All namespaces" and an empty box, which
 * is a filter that appears to work and is discarded on arrival. They are read
 * once, on arrival, and the address is rewritten to the names the page reads -
 * so the address bar teaches the current spelling rather than keeping the old.
 *
 * **Only where the current name is absent.** A link carrying both was written
 * by something that knew the new name, and the new name is what it meant.
 */
const LEGACY: readonly (readonly [legacy: string, current: string])[] = [
  ['ns', 'namespace'],
  ['q', 'search'],
];

/** The address with the old names moved to the new, or undefined where there is nothing to move. */
export function modernised(params: URLSearchParams): URLSearchParams | undefined {
  if (!LEGACY.some(([legacy]) => params.has(legacy))) return undefined;
  const next = new URLSearchParams(params);
  for (const [legacy, current] of LEGACY) {
    if (!next.has(legacy)) continue;
    const values = next.getAll(legacy);
    next.delete(legacy);
    if (next.has(current)) continue;
    // `ns` was comma-joined and `namespace` repeats; `useUrlList` reads both
    // spellings, so the value is carried as it was written.
    for (const value of values) if (value !== '') next.append(current, value);
  }
  return next;
}

/** Rewrites an arriving address that uses the old names, replacing the history entry. */
export function useLegacyListParameters(): void {
  const [params, setParams] = useSearchParams();
  const next = modernised(params);
  const wanted = next?.toString();
  useEffect(() => {
    if (wanted === undefined) return;
    setParams(new URLSearchParams(wanted), { replace: true });
  }, [wanted, setParams]);
}
