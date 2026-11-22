import { useMemo } from 'react';

import type { ListPage } from '../../api';
import { useSelection } from '../../components/useSelection';

/** The ticked rows of a resource list, the scope they belong to, and whether the kind offers ticks. */
export function useListSelection(
  cluster: string | undefined,
  kind: string | undefined,
  page: ListPage | undefined,
) {
  /**
   * The ids currently listed, or `undefined` when the list did not answer.
   *
   * This one expression draws an empty answer and a failed answer differently: `useSelection`
   * prunes ticks against what is listed, and pruning against a **failed** read
   * would empty a person's selection because a cluster hiccuped. Here the
   * failed arms have no rows to pass, so the wrong version cannot be written.
   */
  const listed = useMemo(() => page?.rows.map((row) => row.id), [page]);

  /**
   * The scope a selection belongs to, and **the server names it**.
   *
   * `api/openapi.yaml` on `BulkAffordance.scope` says why, and says it against
   * exactly what stood here: *"a client composing it from the route would not
   * notice a namespace selection changing under it."* It is a safety property
   * rather than a cache key — a set of pod names carried across to another
   * cluster's node list is a set of objects that do not exist, and worse, might.
   * The server's is `cluster + "|" + kind`.
   *
   * The route composition survives as the value used **before the first answer**,
   * when there is no server scope to read and no row to tick either. It uses the
   * server's separator so the two agree wherever the resolved kind is the
   * requested one; where a CRD alias resolves to another slug they differ once,
   * on the first answer, and the reset that follows empties a selection that is
   * necessarily empty.
   */
  const scope = page?.bulk?.scope ?? `${cluster ?? ''}|${kind ?? ''}`;
  const selection = useSelection(scope, listed);

  /**
   * Whether this kind offers a selection at all.
   *
   * `bulkAffordance` returns nil where the kind has no bulk verb, and that nil is
   * load-bearing rather than an empty list: it is what keeps the checkboxes off a
   * table where ticking a row would lead nowhere.
   */
  const offersSelection = page?.bulk !== undefined;

  return { scope, selection, offersSelection };
}
