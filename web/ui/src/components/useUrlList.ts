import { useCallback, useMemo } from 'react';
import { useLocation, useSearchParams } from 'react-router';

/**
 * One **repeatable** query-string parameter, read and written as a selection.
 *
 * `useUrlParameter` beside it is the single-value form and every decision there
 * holds here too — `replace` and never `push`, the key is the control's own
 * name, and the value is the operator's verbatim. What this adds is the one
 * thing `?namespace=a` cannot express, which is `?namespace=a&namespace=b`.
 *
 * ## Repeated, not comma-joined, and the two are not interchangeable
 *
 * `api/openapi.yaml` declares the parameter `explode: true`, so the wire form is
 * the parameter written again. The server also parses a comma-joined value —
 * `parseNamespaces` splits on commas — but a
 * comma is a legal character in nothing here and a *repeated* parameter cannot
 * be ambiguous, so the address bar carries the contract's form. Both reach one
 * parser on the server, which is why changing the address bar's spelling is not
 * a change to what is asked.
 *
 * ## Canonical: trimmed, deduplicated, sorted
 *
 * The same three the server applies, and for the reason `parseNamespaces` gives:
 * *"two URLs naming the same two namespaces in different orders are the same
 * page, and without this they would be two different cache keys, two different
 * sort links and two different 'is this one selected' answers."* Doing it here
 * as well means the address bar, the query key and the server all agree, and a
 * selection reordered by the operator does not refetch.
 *
 * The returned array is memoized on the joined string rather than on the
 * `URLSearchParams` object, because `getAll` returns a new array on every render
 * and a new array in a dependency list is a query key that changes every render.
 */
export function useUrlList(name: string): [readonly string[], (values: readonly string[]) => void] {
  const [params, setParams] = useSearchParams();
  // Kept through the write: it is the same history entry, and its state is
  // what a page's Back reads (`back.ts`).
  const { state } = useLocation();

  // The joined string is the identity. `useSearchParams` hands back a new object
  // on every render, so anything derived from it must be keyed on its content.
  const joined = canonical(params.getAll(name)).join(',');
  const values = useMemo(() => (joined === '' ? [] : joined.split(',')), [joined]);

  const set = useCallback(
    (next: readonly string[]) => {
      setParams(
        (previous) => {
          const updated = new URLSearchParams(previous);
          updated.delete(name);
          // An empty selection is the parameter **removed**, not written empty:
          // that is the difference between asking for every namespace and asking
          // for one called "".
          for (const one of canonical(next)) updated.append(name, one);
          return updated;
        },
        { replace: true, state },
      );
    },
    [name, setParams, state],
  );

  return [values, set];
}

function canonical(values: readonly string[]): string[] {
  const seen = new Set<string>();
  for (const raw of values) {
    // A value may itself be a comma-separated list, because that is what the
    // typed box holds and what an older link may carry.
    for (const part of raw.split(',')) {
      const name = part.trim();
      if (name !== '') seen.add(name);
    }
  }
  return [...seen].sort();
}

/**
 * The selection with one name flipped — added if it is not there, removed if it
 * is.
 *
 * Named rather than inlined, because the reason matters: each row of the menu
 * is *"the selection I have now, with this one flipped"*, rather than a whole
 * new list — *"an index or a whole new list would be a claim about the page
 * the browser last drew"*. A
 * click is a statement about one namespace, resolved against what the address
 * bar already says.
 */
export function toggled(selection: readonly string[], name: string): readonly string[] {
  if (name === '') return [];
  return selection.includes(name)
    ? selection.filter((one) => one !== name)
    : canonical([...selection, name]);
}
