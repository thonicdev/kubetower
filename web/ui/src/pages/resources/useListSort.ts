import { useState } from 'react';

import type { GridSort } from '../../components/DataGrid';
import { lastSort, rememberSort } from '../../shell/viewMemory';

export function useListSort(
  cluster: string | undefined,
  kind: string | undefined,
  scope: string,
): [GridSort | undefined, (next: GridSort | undefined) => void] {
  /**
   * The sort, and the scope it belongs to.
   *
   * A sort is about one kind's columns. Carrying it to the next kind sorts by a
   * column that is not there, which draws every row as equal and reads as the
   * server having stopped ordering.
   *
   * Reset **during render** and not in an effect, and the lint rule that insists
   * on it is right for a reason worth keeping: a `setState` in an effect paints
   * once with the previous kind's sort key applied to the new kind's rows, and
   * that paint is a whole frame of rows in an order nothing asked for.
   */
  const [sortState, setSortState] = useState<{ scope: string; sort: GridSort | undefined }>(() => ({
    scope,
    sort: lastSort(cluster ?? '', kind ?? ''),
  }));

  let sort = sortState.sort;
  if (sortState.scope !== scope) {
    // **Reread, not carried over.** A sort belongs to one kind's columns, so
    // carrying it across is what sorts by a column that is not there.
    // `viewMemory` keys it *per kind*, so coming back to a list finds it
    // as it was left and moving to another kind still starts clean.
    sort = lastSort(cluster ?? '', kind ?? '');
    setSortState({ scope, sort });
  }
  const setSort = (next: GridSort | undefined) => {
    setSortState({ scope, sort: next });
    rememberSort(cluster ?? '', kind ?? '', next);
  };

  return [sort, setSort];
}
