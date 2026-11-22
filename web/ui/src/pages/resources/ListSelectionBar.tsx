import type { ReactNode } from 'react';

import type { BulkAffordance, Column, ListPage, Row } from '../../api';
import { SelectionBar } from '../../components/SelectionBar';
import type { useSelection } from '../../components/useSelection';

/**
 * A namespace already Terminating offers no Delete, and the
 * bar's verbs are per kind, so the one thing the kind's answer cannot say is
 * decided here from the rows: **Delete is withheld from the bar while any ticked
 * row is a Terminating namespace**. Every other verb and every other kind keeps
 * the server's list untouched.
 */
function withoutDeleteOnTerminating(
  bulk: BulkAffordance | undefined,
  columns: readonly Column[] | undefined,
  rows: readonly Row[] | undefined,
  picked: ReadonlySet<string>,
  kind: string,
): BulkAffordance | undefined {
  if (bulk === undefined || kind !== 'namespaces') return bulk;
  // The cell is found through its column: the server keys a cell by
  // `columnKey(title, index)`, so the key is `status-1` and never `status`.
  const status = columns?.find((column) => column.label === 'Status')?.key;
  if (status === undefined) return bulk;
  const terminating = (rows ?? []).some(
    (row) => picked.has(row.id) && row.cells[status]?.text === 'Terminating',
  );
  if (!terminating) return bulk;
  return { ...bulk, verbs: bulk.verbs.filter((one) => one.verb !== 'delete') };
}

/**
 * **A toggle is offered where a ticked row needs it, and acts only there.**
 * The kind's verbs come in pairs - Suspend and Resume, Cordon and Uncordon -
 * and each row says which of its kind's verbs would change nothing on it
 * (`Row.unchanged`: the half of a toggle it is already on). A verb stays on the
 * bar while at least one ticked row is not listed under it, and is withheld
 * once every ticked row is; `actingRows` then sends that verb only the rows it
 * would change. A row that says nothing keeps every verb, as before.
 */
export function withoutSettledVerbs(
  bulk: BulkAffordance | undefined,
  rows: readonly Row[] | undefined,
  picked: ReadonlySet<string>,
): BulkAffordance | undefined {
  if (bulk === undefined) return bulk;
  const ticked = (rows ?? []).filter((row) => picked.has(row.id));
  // Nothing ticked is still on the list: the bar's own `dropped` line says so,
  // and there is no row to judge a verb by.
  if (ticked.length === 0) return bulk;
  const verbs = bulk.verbs.filter((one) => ticked.some((row) => !(row.unchanged ?? []).includes(one.verb)));
  return verbs.length === bulk.verbs.length ? bulk : { ...bulk, verbs };
}

/** The ticked rows one verb would change: every ticked row, less those that
 *  list the verb in `unchanged`. */
export function actingRows(rows: readonly Row[] | undefined, picked: ReadonlySet<string>, verb: string): Row[] {
  return (rows ?? []).filter((row) => picked.has(row.id) && !(row.unchanged ?? []).includes(verb));
}

/** The bar the ticked rows put up, with the verbs the kind offers on them. */
export function ListSelectionBar({
  selection,
  cluster,
  kind,
  page,
}: {
  selection: ReturnType<typeof useSelection>;
  cluster: string;
  kind: string;
  page: ListPage | undefined;
}): ReactNode {
  return (
    <SelectionBar
      selection={selection}
      cluster={cluster}
      kind={kind}
      bulk={withoutSettledVerbs(
        withoutDeleteOnTerminating(page?.bulk, page?.columns, page?.rows, selection.picked, kind),
        page?.rows,
        selection.picked,
      )}
      /* The mapping from a ticked id to a target lives here, because the
         rows live here. The bar never parses an id: `Row.id` is the
         server's and its shape is not this client's to know - a bar that
         split it on a slash would be a second, weaker copy of a decision
         made in Go. A picked id whose row has gone is dropped rather than
         guessed at; `useSelection` has already reported how many. */
      targetsFor={(picked, verb) =>
        actingRows(page?.rows, picked, verb).map((row) => ({
            kind,
            ...(row.namespace === undefined ? {} : { namespace: row.namespace }),
            name: row.name ?? row.id,
          }))
      }
    />
  );
}
