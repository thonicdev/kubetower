import { useEffect, useState } from 'react';

import type { ListPage } from '../../api';
import { parseChosen, rewrittenAddress } from './listFilters';
import type { ContributedColumn } from './extensionColumns';
import type { ListFilters } from './useListFilters';

/**
 * The address of a resource list rewritten from its answer, and the filters
 * that rewrite dropped, said beside the chips until dismissed.
 */
export function useDroppedFilters(
  cluster: string | undefined,
  kind: string | undefined,
  page: ListPage | undefined,
  standingIn: boolean,
  {
    search,
    settledSearch,
    chosen,
    rewrite,
    typedSearch,
  }: Pick<ListFilters, 'search' | 'settledSearch' | 'chosen' | 'rewrite' | 'typedSearch'>,
  /** The columns extensions add, whose filters are the console's own to apply. */
  contributed: readonly ContributedColumn[] & { pending?: boolean } = [],
) {
  /*
    **The address follows the answer, once the answer is to this address.**
    Two rewrites, both `listFilters.ts`'s: an old bookmark's
    `?search=node:kubeworker1` - now a plain search that finds nothing - is
    translated into the filter it meant (an address only, never what the box
    typed), and a filter the server did not apply
    (a column this kind cannot be filtered by) is dropped, so no chip claims a
    narrowing that did not happen. Only on an answer to what the address says:
    the previous filter's rows standing in, or a search still settling, would
    rewrite the address from the wrong answer.

    An effect, because it is a navigation - and a navigation is not something a
    render may do.
  */
  // The filters the server did not apply, said beside the chips until
  // dismissed - and **forgotten when the list changes**: `ResourceList` is not
  // remounted across kinds, so a notice only hidden on another list would come
  // back on a clean return to this one, about an address that no longer asked
  // for it. Reset while rendering, React's way of letting go of state that
  // belongs to the previous props.
  const list = `${cluster ?? ''}|${kind ?? ''}`;
  const [dropped, setDropped] = useState<readonly string[]>([]);
  const [droppedOn, setDroppedOn] = useState(list);
  if (droppedOn !== list) {
    setDroppedOn(list);
    setDropped([]);
  }
  // By value: the hook's array is new on every render, and an effect that
  // listed it would run - and set `dropped` - on every one.
  const pending = contributed.pending === true;
  const labelKey = JSON.stringify(contributed.map((column) => column.label));
  useEffect(() => {
    if (page === undefined || standingIn || search !== settledSearch) return;
    // A filter on an extension's column waits for the menu and the column's
    // read: judged earlier, it would be dropped for a column still arriving.
    if (pending) return;
    const next = rewrittenAddress(
      page,
      search,
      chosen,
      search !== typedSearch.current,
      JSON.parse(labelKey) as string[],
    );
    if (next === undefined) return;
    // What the second rewrite drops is said beside the chips, for this list
    // only, until dismissed: an old `Controlled by` bookmark otherwise draws
    // the whole list with nothing saying why.
    const gone = chosen.filter((one) => !next.filters.includes(one));
    // The outcome of this rewrite, known only here: the address is about to
    // lose these filters, so they cannot be read back from it at render.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (gone.length > 0) setDropped(gone);
    rewrite({ search: next.search }, next.filters);
    // `typedSearch` is a ref object whose identity never changes, so listing it re-runs nothing.
  }, [page, standingIn, search, settledSearch, chosen, rewrite, typedSearch, pending, labelKey]);
  const droppedHere = dropped.flatMap((one) => parseChosen(one) ?? []);
  const dismissDropped = (): void => {
    setDropped([]);
  };

  return { droppedHere, dismissDropped };
}
