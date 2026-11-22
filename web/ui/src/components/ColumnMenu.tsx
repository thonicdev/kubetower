import { useId, useState, type ReactNode } from 'react';

import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { focusRing } from '../focus';
import { toolbarControl } from './toolbar';
import { arrangeAll, isLastVisible, type Arrangeable } from './columns';
import { MenuScrim } from './MenuScrim';

/**
 * Which columns are drawn, edited one column at a time.
 *
 * ## One cell per request, and it is the reason this component exists
 *
 * `api/openapi.yaml` keeps five separate column-preference routes and says why
 * on `columns/show`: *"a body carrying every column's visibility means that
 * unticking one re-asserts the rest from whatever the page happened to hold,
 * which freezes a grid that has since gained a column. The schema refuses the
 * second field."* That is **a design comment rather than a line of code** — a
 * JSON API accepting the whole preferences object is tidier, passes review,
 * and reintroduces the exact bug the shape prevents.
 *
 * So `onShow` takes one key and one boolean. There is no shape here that could
 * carry a second column, which is the only version of this rule that survives
 * somebody tidying the component.
 *
 * ## The keyboard
 *
 * Escape closes it, claimed at `KeyLayer.Menu` from the one keyboard stack —
 * **not** a `document` listener: React attaches its own listeners at the root
 * container, so a hand-written `document.addEventListener` runs in a different
 * phase from every React handler and fires for elements React is mid-unmount.
 *
 * A click elsewhere closes it through a real element in the tree rather than a
 * global handler, for the same reason - `MenuScrim`, which is the one copy of
 * that element every menu in the console shares.
 *
 * ## What it does not do
 *
 * **Reset.** It is in `TableMenu`, behind the `⋯` at the far right, beside
 * Compact rows, because neither is a question about *which columns are shown*.
 * This menu answers that one question and has no second shape in it.
 *
 * Reordering and resizing. Those are done on the headings themselves
 * (`DataGrid`).
 */

export interface ColumnMenuProps<C extends Arrangeable> {
  columns: readonly C[];
  /** The operator's word for each column. */
  labelOf: (column: C) => string;
  /** One column, one call. Never a batch. */
  onShow: (key: string, visible: boolean) => void;
}

export function ColumnMenu<C extends Arrangeable>({
  columns,
  labelOf,
  onShow,
}: ColumnMenuProps<C>): ReactNode {
  const [open, setOpen] = useState(false);
  const panelId = useId();

  useEscape(
    KeyLayer.Menu,
    () => {
      setOpen(false);
    },
    open,
  );

  return (
    // Joined to the funnel on its left: one border between them (`-ml-px`) and
    // no rounding on the shared edge, so the field, the funnel and Columns read
    // as one segmented control.
    <div className="relative -ml-px">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => {
          setOpen((was) => !was);
        }}
        className={`${toolbarControl} rounded-l-none text-muted ${focusRing}`}
      >
        Columns
      </button>

      {open ? (
        <>
          <MenuScrim
            onDismiss={() => {
              setOpen(false);
            }}
          />

          <div
            id={panelId}
            className="absolute right-0 z-20 mt-1 w-56 rounded-lg border border-line bg-panel-2 p-2 shadow-lg"
          >
            <ul>
              {arrangeAll(columns).map((column) => {
                /*
                  Two different reasons a column cannot be hidden, and they are
                  not interchangeable.

                  `lockedNote` is the **server's**, about this column in
                  particular, and it sends the sentence. `api/openapi.yaml` says
                  how to draw it: "the column menu draws it as a fact rather
                  than as a control that refuses; a toggle that does nothing is
                  worse than an explained absence."

                  `isLastVisible` is **this client's**: a table with no columns
                  is not a narrower table, it is a page that has lost its
                  contents to a checkbox. It gets its own sentence rather than
                  silently borrowing the server's, because "the last one
                  standing" and "this one never moves" send an operator to two
                  different places.
                */
                const last = isLastVisible(columns, column.key);
                const locked = column.lockedNote;
                const note =
                  locked ?? (last ? 'The last column shown. A table needs one.' : undefined);
                return (
                  <li key={column.key}>
                    <label className="flex items-center gap-2 rounded-sm px-1 py-1 text-sm">
                      <input
                        type="checkbox"
                        // The box is the control, not the row, and a box has
                        // room outside it for the ring.
                        className={focusRing}
                        checked={column.visible}
                        disabled={last || locked !== undefined}
                        onChange={(event) => {
                          onShow(column.key, event.target.checked);
                        }}
                      />
                      <span className={`min-w-0 flex-1 ${column.visible ? '' : 'text-muted'}`}>
                        {labelOf(column)}
                      </span>
                      {/* Why a column is unticked on a console nobody has
                          arranged. Without it an operator reads a decision of
                          their own into a default they never made. */}
                      {column.secondary === true && !column.visible ? (
                        <span className="shrink-0 text-xs text-muted">off by default</span>
                      ) : null}
                    </label>
                    {note === undefined ? null : (
                      <p className="mb-1 ml-6 text-xs text-muted">{note}</p>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        </>
      ) : null}
    </div>
  );
}
