import { useId, useState, type ReactNode } from 'react';

import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { focusRing } from '../focus';
import { toolbarSquare } from './toolbar';
import { MenuScrim } from './MenuScrim';

/**
 * What is done **to** the table rather than read from it: row height, and
 * putting this table's columns back.
 *
 * ## Why there are two menus and not one
 *
 * Row height and Reset sit behind a `⋯` at the end of the column controls,
 * not as loose buttons in the middle of the panel head, which is where the eye
 * goes to find out *what* the table is showing; they are not that. One is a preference about the whole
 * console and the other is an undo, and neither is touched twice in an
 * afternoon.
 *
 * So the row reads left to right as the two questions an operator asks of a
 * table. **Which rows** — the namespace and the filter, together at the near
 * end. **Which columns** — the Columns menu, a later question and a rarer one.
 * Then the `⋯`, holding what is done to the table.
 *
 * **Reset is not in the Columns menu**: it is done to the table, not a choice
 * of which columns to read.
 *
 * ## Compact rows is drawn always, and Reset only sometimes
 *
 * The asymmetry is deliberate on both sides.
 * The toggle is a preference and there is nothing to undo, and **a preference
 * that only appears once you have already changed it is one nobody finds**.
 * Reset is an undo: offered only when there is something to go back from, which
 * `ListPage.columnsCustomised` is the server's answer to.
 *
 * ## The dot on the `⋯`, and why the remedy is not moved out from behind it
 *
 * A stored width is a share of the table, so a sized table always fills its
 * panel and never looks broken for want of a Reset. What Reset is for is
 * *forget what I arranged* - and somebody who wants that knows he arranged
 * something.
 *
 * So the remedy is **not** moved out from behind the `⋯`, not drawn disabled on
 * a table with nothing stored, and not announced in a banner. Each of those pays
 * a permanent piece of furniture on every table for a case that now arrives
 * rarely, and the second breaks the rule *a table with nothing stored offers
 * nothing*.
 *
 * What is added is the smallest thing that answers the honest remainder - **an
 * operator cannot tell, from a table, that this one is not the default one.**
 * The `⋯` carries a dot exactly when the server says `columnsCustomised`, and
 * an `sr-only` sentence beside it says the same thing to a reader. It appears
 * only where there is something to undo, so it is a property of the table rather
 * than furniture.
 *
 * **The sentence is a description and not the name.** `aria-label` stays
 * `More table options` in both states: a control whose *name* changes with its
 * state is one a screen-reader user has to learn twice, and the name is also
 * what every test in this tree finds this button by.
 *
 * ## The item says what it forgets
 *
 * `Reset columns` forgets the widths, the column order **and** which columns
 * are hidden, so the menu names all three under the label, before the click
 * rather than after it.
 *
 * **Choosing between the three is not offered here yet**, and that is a wiring
 * fact rather than a decision: the store and `POST /api/preferences/columns/reset`
 * both take a scope (`internal/columns.Scope`), but the click has to
 * travel through `ResourceList`'s mutation to carry one. Three items that all did
 * the same thing would be a worse lie than one honest item.
 *
 * It is one setting for the whole console and not one per kind
 * (`internal/columns/columns.go:758`): a statement about the operator's screen,
 * and somebody who wants more rows at once wants them everywhere.
 *
 * ## The keyboard, and why not a `document` listener
 *
 * Escape closes it, claimed at `KeyLayer.Menu` from the one keyboard stack, and
 * a click elsewhere closes it through a real element in the tree - `MenuScrim`,
 * the one copy every menu shares. Both for `ColumnMenu`'s reason: React
 * attaches its own listeners at the root, and a hand-written `document.addEventListener` runs in
 * a different phase and fires for elements React is mid-unmount.
 */
export interface TableMenuProps {
  /** Whether rows are currently drawn compact. `aria-pressed` is this, so a
   *  reader announces the state rather than only the label. */
  compact: boolean;
  /** Given the density to write. The whole console's, not this kind's. */
  onDensity: (compact: boolean) => void;
  /** Forget every preference for this kind: the widths, the column order and
   *  which columns are hidden. Three arrangements behind one verb - the store
   *  and the endpoint take a scope, this does not carry one yet, and the item
   *  below says all three out loud so the price is read before the click. */
  onReset: () => void;
  /** Whether the server says anything is stored for this kind, which is the
   *  only thing that decides whether Reset is offered. */
  arranged: boolean;
}

export function TableMenu({ compact, onDensity, onReset, arranged }: TableMenuProps): ReactNode {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const arrangedId = useId();

  useEscape(
    KeyLayer.Menu,
    () => {
      setOpen(false);
    },
    open,
  );

  return (
    <div className="relative">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        aria-label="More table options"
        // The state is a *description* and not part of the name. A name that
        // changes with the state is a control a screen-reader user has to learn
        // twice, and it is what every test in the tree finds this button by.
        aria-describedby={arranged ? arrangedId : undefined}
        title={
          arranged
            ? "More: row height, and putting this table's columns back - you have arranged this one"
            : "More: row height, and putting this table's columns back"
        }
        onClick={() => {
          setOpen((was) => !was);
        }}
        className={`relative ${toolbarSquare} text-muted ${focusRing}`}
      >
        {/* U+22EF, a midline horizontal ellipsis. */}
        <span aria-hidden="true">⋯</span>

        {/* The dot is the whole of the discoverability half, and it says nothing
            to a screen reader: a colored circle is not a sentence. The sentence
            is the `sr-only` span beside it, referenced from `aria-describedby`
            above, so both readings carry the same fact once. */}
        {arranged ? (
          <>
            <span
              aria-hidden="true"
              data-testid="table-arranged"
              className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-accent"
            />
            <span id={arrangedId} className="sr-only">
              this table is arranged
            </span>
          </>
        ) : null}
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
            <button
              type="button"
              aria-pressed={compact}
              title={compact ? 'Give the rows their usual height' : 'Fit more rows on the screen'}
              onClick={() => {
                onDensity(!compact);
                setOpen(false);
              }}
              className={`flex w-full items-center gap-2 rounded-sm px-1 py-1 text-left text-sm ${focusRing}`}
            >
              {/* A tick and not a checkbox: it is a button that acts, and the
                  mark says what the state is now. The tick keeps its width
                  when empty so the labels stay aligned. */}
              <span aria-hidden="true" className="inline-block w-3 shrink-0">
                {compact ? '✓' : ' '}
              </span>
              <span>Compact rows</span>
            </button>

            {arranged ? (
              <button
                type="button"
                title="Forget this table's column widths, its column order and which columns are hidden"
                onClick={() => {
                  onReset();
                  setOpen(false);
                }}
                className={`flex w-full items-start gap-2 rounded-sm px-1 py-1 text-left text-sm ${focusRing}`}
              >
                <span aria-hidden="true" className="inline-block w-3 shrink-0">
                  {' '}
                </span>
                <span>
                  <span className="block">Reset columns</span>
                  {/* What it costs, read before the click: one word would cover
                      three acts, and cost a column order nobody had asked about.

                      Hidden from a reader because the `title` above already says
                      the same thing and says it in full. Two announcements of one
                      fact is the noise that gets a reader turned off - and it
                      would also fold this line into the control's *name*, which
                      is what a reader identifies the item by. */}
                  <span aria-hidden="true" className="block text-xs text-muted">
                    widths, order and hidden columns
                  </span>
                </span>
              </button>
            ) : null}
          </div>
        </>
      ) : null}
    </div>
  );
}
