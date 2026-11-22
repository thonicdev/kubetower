import { useState, type ReactNode } from 'react';

import { btnGhost } from './buttons';

/** How many rows a `Windowed` list draws at once, and how many each "Show more"
 *  adds. Only the extras' long lists use it; `DataGrid` shows every row through
 *  a virtual scroll instead. */
export const LIST_PAGE = 100;

/**
 * The first `LIST_PAGE` items of a list, and a button for the next `LIST_PAGE`.
 *
 * For the large extras' lists (tiles, bars, hand-built tables), which are not a
 * `DataGrid`: a list that can grow draws a bounded number of rows however long the answer is.
 * The answer is still held whole; only the drawing is cut. Nothing is drawn
 * below a list of `LIST_PAGE` items or fewer.
 *
 * `more` places the button inside the caller's own markup when it must be a
 * `<li>` or a `<tr>`; the default is a bare centered button below the list.
 */
export function Windowed<T>({
  items,
  children,
  more,
}: {
  items: readonly T[];
  children: (shown: readonly T[]) => ReactNode;
  more?: (button: ReactNode) => ReactNode;
}): ReactNode {
  const [drawn, setDrawn] = useState(LIST_PAGE);
  const left = items.length - drawn;
  const button =
    left > 0 ? (
      <button
        type="button"
        data-more=""
        className={btnGhost}
        onClick={() => {
          setDrawn((n) => n + LIST_PAGE);
        }}
      >
        Show {String(Math.min(LIST_PAGE, left))} more of {String(left)}
      </button>
    ) : null;
  return (
    <>
      {children(left > 0 ? items.slice(0, drawn) : items)}
      {button === null ? null : more === undefined ? <div className="mt-2 text-center">{button}</div> : more(button)}
    </>
  );
}
