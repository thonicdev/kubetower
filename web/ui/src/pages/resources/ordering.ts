import type { Row } from '../../api';

/**
 * Ordering the rows, on the client, by `Cell.sort`.
 *
 * ## Why the client
 *
 * `GET /api/c/{cluster}/r/{kind}` takes a namespace, a selector and a search
 * term, and **no sort parameter**. That is not an omission: the payload carries
 * `Cell.sort` beside `Cell.text` precisely *"so that a size column sorts by
 * bytes rather than by the string 9 MiB"*, which is a fact the server sends in
 * order that the client can order rows with it. There is no pagination on the
 * endpoint either, so the client holds the whole list and ordering it locally is
 * ordering all of it — the one condition under which a client-side sort is not a
 * lie.
 *
 * The order is therefore **not in the address bar** and a bookmark does not
 * carry it. Putting it in the URL is not done because the address is the record of *the request*, and a sort that
 * appears there while no request carries it invites the next reader to add a
 * parameter the server does not read.
 *
 * ## An absence is not a small value
 *
 * A row with nothing in the sorted column goes **last in both directions**.
 * Treating a missing value as zero or as the empty string puts every unmeasured
 * node at the top of an ascending sort by CPU, which reads as a row of idle
 * machines and is the one misreading this whole product is organized against.
 */

export type Direction = 'asc' | 'desc';

/** What a cell sorts by: the server's key where it sent one, the drawn text
 *  otherwise. `null` is *no value*, which is not the same as an empty string. */
function keyOf(row: Row, column: string): string | number | null {
  const cell = row.cells[column];
  if (cell === undefined) return null;
  if (cell.sort !== undefined && cell.sort !== null) return cell.sort;
  // A cell that is not `ok` has no value to sort by, whatever its text says.
  if (cell.state !== undefined && cell.state !== 'ok' && cell.state !== 'empty') return null;
  if (cell.reading !== undefined && cell.reading.value === null) return null;
  return cell.text === '' ? null : cell.text;
}

function compareKeys(a: string | number, b: string | number): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  // `numeric` so `pod-2` precedes `pod-10`, which is what an operator reading a
  // list of replicas means by alphabetical.
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
}

export function orderRows(
  rows: readonly Row[],
  column: string,
  direction: Direction,
): readonly Row[] {
  const sign = direction === 'asc' ? 1 : -1;
  // A copy: `sort` mutates, and the array in the cache is the answer the server
  // gave. Sorting it in place would reorder the cached page and make the next
  // render's "unsorted" order depend on what was last clicked.
  return [...rows].sort((left, right) => {
    const a = keyOf(left, column);
    const b = keyOf(right, column);
    if (a === null && b === null) return 0;
    if (a === null) return 1;
    if (b === null) return -1;
    return sign * compareKeys(a, b);
  });
}
