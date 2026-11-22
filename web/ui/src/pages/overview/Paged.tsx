import { useState, type ReactNode } from "react";

import { btnGhost } from "../../components/buttons";
import { LIST_PAGE } from '../../components/Windowed';

/** Rows a list on the overview shows at once. */
export const PAGE_SIZE = LIST_PAGE;

/**
 * One page of a list, and the controls to move between pages.
 *
 * The overview's attention lists - what is not in the state it should be in,
 * what is restarting, the recent warnings - are the server's list cut to the
 * worst first; on a bad cluster that is hundreds of rows. They draw 100 at a
 * time. Nothing is drawn below the table when 100 rows or fewer exist: a pager
 * with one page is noise.
 *
 * The page is clamped at draw time rather than reset by an effect, so a refetch
 * that shortens the list lands on the last page that still exists instead of an
 * empty one.
 *
 * **A list the server cut says so.** The server sends at most 200 rows
 * of the restarting list and the warning feed, with how many there were
 * beside them. Without `total` the last page of 900 crash loops read "181-200
 * of 200", which is the end of the list; with it, every page reads "of 900, the
 * first 200 shown". A `total` no larger than the rows carried is no cut, and
 * draws as before.
 */
export function Paged<T>({
  items,
  total,
  what,
  children,
}: {
  items: readonly T[];
  /** How many rows there were before the server cut the list to `items`.
   *  Absent where the server did not say, which draws as an uncut list. */
  total?: number | undefined;
  /** What the rows are, for the controls' accessible name. */
  what: string;
  children: (page: readonly T[]) => ReactNode;
}): ReactNode {
  const [asked, setAsked] = useState(0);
  const pages = Math.max(1, Math.ceil(items.length / PAGE_SIZE));
  const page = Math.min(asked, pages - 1);
  const from = page * PAGE_SIZE;
  const shown = items.slice(from, from + PAGE_SIZE);
  const cut = total !== undefined && total > items.length;

  return (
    <>
      {children(shown)}
      {(pages > 1 || cut) && (
        <nav
          aria-label={`${what}, pages`}
          className="mt-2 flex items-center justify-end gap-2 text-xs"
        >
          <button
            type="button"
            disabled={page === 0}
            title={page === 0 ? 'This is the first page' : undefined}
            onClick={() => setAsked(page - 1)}
            className={btnGhost}
          >
            Previous
          </button>
          <span className="text-muted tabular-nums" aria-live="polite">
            {from + 1}&ndash;{from + shown.length} of{' '}
            {cut ? `${String(total)}, the first ${String(items.length)} shown` : items.length}
          </span>
          <button
            type="button"
            disabled={page === pages - 1}
            title={page === pages - 1 ? 'This is the last page' : undefined}
            onClick={() => setAsked(page + 1)}
            className={btnGhost}
          >
            Next
          </button>
        </nav>
      )}
    </>
  );
}
