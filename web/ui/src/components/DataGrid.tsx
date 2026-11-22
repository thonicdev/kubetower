import {
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';

import { focusRing, focusRingClear, focusRingFlush, focusRingTight } from '../focus';
import { Icon } from './Icon';
import { bodyCell, headCell, HEAD_ROW, ROW, TABLE } from './tableLook';
import { useRowWindow, WINDOW_AFTER } from './useRowWindow';

/**
 * The grid every list in this console is drawn with.
 *
 * ## The class is `kt-grid`, and that is not cosmetic
 *
 * Tailwind emits `.grid { display: grid }`. A `<table class="grid">` is
 * therefore taken **out of table layout entirely**, and the two rules
 * `theme.css` carries for this element have nothing left to act on: the
 * inherited `white-space` and `table-layout: fixed`. Both fail silently, on a
 * table that still looks like a table. The name is recorded in `theme.css` and
 * repeated here because this is the file that would undo it.
 *
 * ## The two rules this component is built around
 *
 * **The wrap goes on the cell, never on a descendant.** `white-space` inherits,
 * so `overflow-wrap: anywhere` on a `<span>` inside a `nowrap` cell is inert,
 * and reads like a working declaration. Here the vocabulary is closed at the
 * three names `theme.css` defines — `prose`, `message`, `wrap` — they are put
 * on the `<td>` by this component and by nothing else, and `GridColumn.wrap` is
 * the only way to ask for one. A wrapper `<div>` carrying a wrap class is just
 * as inert.
 *
 * **A width derived from data that has not arrived is a guess.** The precise
 * statement matters, because the loose one produces a different bug. Sizing a
 * column from **the cells in front of the renderer** is correct and is what
 * `defaultWidths` does; three Go tests defend it and one of them defends it
 * against exactly the over-correction:
 *
 * - `TestAListWithNoRowsStillDrawsItsHeadingsWhole` — with nothing under it, a
 *   column gets its heading's full room, or the heading draws ellipsized on a
 *   list where it is the only thing on screen.
 * - `TestAColumnThatIsBlankOnEveryRowIsStillSizedByItsHeading` — the same defect
 *   one column wide, and not the rare case: a column blank on every row is 100%
 *   heading.
 * - `TestAHeadingStillGivesWayToTheValuesUnderIt` — **and this is the one a
 *   heading-only rule breaks.** `RESTARTS` over a column of `883` would claim
 *   118px for three digits while `STATUS` beside it needs 174px for
 *   `CrashLoopBackOff`: the widest pixels of the table spent on its narrowest
 *   data.
 *
 * So the rule is not *never measure a cell*. It is **never let a width outlive
 * the answer it was computed from**. The nodes list is the instance: usage
 * lookups deliberately do not block, so CPU, Memory and Disk first render as
 * three em dashes, and a colgroup sized then and never recomputed would leave
 * the readings overflowing their own columns when they arrive.
 *
 * **React closes that by construction:** the `<colgroup>` below is rendered
 * from the same `rows` as the `<tbody>`, in the same render, so a colgroup
 * describing an older answer than the rows it sizes cannot exist. A late value
 * widens its column when it arrives instead of overflowing it for ever.
 *
 * What that does not buy is stillness — a value gaining a digit moves its
 * column — and the answer to that is the declarative one:
 * `GridColumn.reserve` is what a column says a late value costs, read whether or
 * not this render has one. No resource column declares a reserve today, and that
 * is a gap in the contract rather than an oversight here: `Column` carries no
 * statement of what a column draws. Recorded in `ResourceCell`.
 *
 * ## What it deliberately does not do
 *
 * It does not fetch, it does not order rows, and it does not know what a `Cell`
 * is. Ordering is the caller's, because the caller is what knows how to compare
 * two values — a size column sorts by bytes and not by the string "9 MiB",
 * which is why the payload carries `Cell.sort` beside `Cell.text`. This
 * component owns the heading affordance and the arrow, and nothing behind them.
 */

export interface GridColumn<T> {
  key: string;
  label: string;
  /** `end` for a number. Anything else reads as a typo rather than a choice. */
  align?: 'start' | 'end';
  /**
   * The width the operator stored, or `null` for automatic.
   *
   * **It is a share of the table and not a number of pixels**: every column
   * always fills the whole page. The unit is the pixel because that is what a
   * pointer produces and what the store clamps, and the number is read against
   * the other columns': a set summing to 1266 fills a 1266px panel at the
   * numbers stored, and fills a 1676px panel with every column 1.324x wider.
   * One stored width anywhere in the set puts the whole table into the sized
   * arrangement, where the colgroup is authoritative about the ratios and a cell
   * too long for its column is clipped instead of painting over the next one.
   *
   * Two things this depends on, neither obvious:
   *
   * - The rule laying a sized table out must not be outranked — see the `w-full`
   *   note on the `<table>` below and the two selectors in `theme.css`.
   * - A colgroup in pixels is only ever scaled up, so a set summing to **more**
   *   than the panel would be drawn at its literal pixels and scroll sideways.
   *   `declareWidth` converts nothing: this number is untouched, and what
   *   reaches the colgroup is its share of the set rather than its pixels.
   * So the table is exactly its panel at every width, and the store's floor of
   * 56 is a floor on the **gesture** rather than on the screen: a narrow enough
   * panel draws a column under it. An unsized list fits too, by another road:
   * there the arithmetic below produces what each column needs, and where they
   * need more than the panel, `fitToRoom` decides which give way.
   */
  width?: number | null;
  /**
   * What this column costs when its value arrives late, in pixels.
   *
   * Declared by the column and read on every render, including the first, when
   * there is nothing in the column to measure. A column that will hold a gauge
   * reserves the bar **and** floors the reading here, or it is sized for the em
   * dash that stood in for it.
   */
  reserve?: number;
  /**
   * What the cell draws **around or beside** its text, in pixels, added to the
   * text's own width wherever there is text.
   *
   * Not `reserve`, which is a floor: a pill's border and padding and a copy
   * control beside an address are drawn *with* the value, so a floor of either
   * is room the value then has to share, and a Warning pill would read
   * `Warni…`. `PICK`'s reasoning, for the cell.
   */
  pad?: number;
  /**
   * What each character of the cell's text costs beyond `CH`, for a cell that
   * draws its text heavier than the grid's regular weight - a pill's semibold.
   * Per character because the overshoot grows with the text.
   */
  perCharacter?: number;
  /**
   * The text this column's cell holds, for sizing only.
   *
   * Separate from `cell`, which returns a node and cannot be measured. A column
   * with no `measure` is sized by its heading alone — which is right for a
   * column of icons and wrong for a column of `CrashLoopBackOff`, so it is a
   * choice each column makes rather than a default nobody notices.
   */
  measure?: (row: T) => string;
  /**
   * One of `theme.css`'s three wrap names, or nothing.
   *
   * `prose` and `message` mark a column that is a **sentence**; `wrap` marks a
   * single unbreakable token that may be broken anywhere. What makes a column
   * one of those is a property of the view model and never of the heading text —
   * the client must not re-decide it from the label.
   */
  wrap?: 'prose' | 'message' | 'wrap';
  /**
   * What hovering this cell says, or nothing.
   *
   * On the `<td>` and not on anything inside it, the only place that works for
   * both things it is for: a cell
   * that states a *number the reader then has to go and look up*, and a value
   * clipped by its own column. A `title` on a `<span>` inside the cell covers the
   * span's box, and the box a clipped value overflows is the cell's.
   *
   * Returning `undefined` rather than `''` is the contract, because the two
   * differ on screen: `title=""` hovers as a blank box.
   */
  title?: (row: T) => string | undefined;
  /**
   * What hovering the **heading** says, or nothing: why a whole column is
   * empty, where one sentence explains every dash under it. On the label, so it
   * does not cover the resize grip's own hover.
   */
  headTitle?: string;
  sortable?: boolean;
  /**
   * This column carries the row's tick, and the select-all in its heading.
   *
   * **The checkboxes are not a column** (`TestTheCheckboxesAddNoColumnToTheTable`
   * holds it): **the tick belongs to the object, not to a position.** Beside
   * the name, because the name is what identifies the row — and if the operator
   * drags Name to the far right, the checkboxes go with it. A dedicated leading
   * column cannot do that, and it also puts 34px between the tick and the
   * namespace, too far for the eye to pair them.
   *
   * At most one column may declare it; the first that does wins, so a payload
   * with two `name` roles cannot draw two ticks per row.
   */
  leads?: boolean;
  /**
   * When this column gives up width, in a list whose columns need more than
   * its panel: `1` first (the name and the namespace), then `2` (any other
   * free text), each cut with an ellipsis and offered whole on hover. A column
   * without it keeps its width; see `fitToRoom`.
   */
  yields?: 1 | 2;
  /**
   * This column gives up its width last, after every other one: the status,
   * whose value is what a row is read for (`statusFloor.ts`).
   */
  holds?: boolean;
  cell: (row: T) => ReactNode;
}

export interface GridSort {
  key: string;
  direction: 'asc' | 'desc';
}

export interface GridSelection {
  picked: ReadonlySet<string>;
  toggle: (id: string) => void;
  /** Every row currently listed, ticked or cleared in one gesture. */
  setAll: (picked: boolean) => void;
  allPicked: boolean;
  somePicked: boolean;
}

/**
 * Where a row's menu was asked for: the point to draw it at, in the viewport's
 * coordinates, and the element the focus goes back to when it closes.
 */
export interface RowMenuAnchor {
  x: number;
  y: number;
  from: HTMLElement;
}

export interface DataGridProps<T> {
  /**
   * The headings stay at the top of the box that scrolls the rows.
   *
   * Only for a table whose **parent** is the vertical scroller - the list's
   * `#panel` - and that is why it is a prop rather than the default: the heading
   * can stick only to its nearest scrollport, and `.table-scroll`'s own
   * `overflow-x-auto` made that box the scrollport in both axes (one axis not
   * `visible` computes the other to `auto`), the height of the whole table, so
   * the heading never moved. With this set the box overflows nothing and the
   * parent scrolls in both directions; a table in a drawer or a card, which
   * has no such parent, keeps its own sideways scroll.
   */
  stickyHead?: boolean;
  columns: readonly GridColumn<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  /** Named for a screen reader, because a table of 400 pods with no accessible
   *  name is 400 rows of unattributed strings. */
  label: string;
  sort?: GridSort | undefined;
  onSort?: ((key: string) => void) | undefined;
  selection?: GridSelection | undefined;
  /**
   * Drawn in one full-width cell **inside the table**, so the headings stay on
   * screen. That is not decoration: an operator looking at an empty list needs
   * to see which columns they asked for, and a notice that replaces the table
   * answers a question they did not ask.
   */
  empty: ReactNode;
  /**
   * What opening **this** row does, or `undefined` where this row opens nothing.
   *
   * **The row is the control, and the name inside it is not.** A name that
   * linked to the object's page inside a row that opens the drawer would fire
   * **both** on a click - the drawer opens and the page navigates out from
   * under it. One row, one action.
   *
   * **Per row, and returning the action rather than answering a predicate
   * beside it.** An `onOpen(row)` that silently did nothing for a row it could
   * not address would leave that row with `tabIndex=0`, a pointer cursor and a
   * hover, advertising an action it does not have. A row that does not name
   * exactly one object opens nothing, and that is an ordinary case rather than
   * a corner, so a shape where the affordance and the action can disagree is a
   * shape that will disagree. Here there is one answer and the tabstop is
   * derived from it.
   */
  rowAction?: ((row: T) => (() => void) | undefined) | undefined;
  /**
   * What opening **this** row's menu does, or `undefined` where the row has
   * none. Right-clicking a row has to be discoverable.
   *
   * Three ways in, one menu: a right-click on the row, the keyboard's menu key
   * (or Shift+F10) on it, and a `⋯` drawn in the cell that carries the tick, so
   * the gesture can be found by looking. The browser's own menu is suppressed
   * on a row that has one and nowhere else - not on the headings, not on an
   * empty list, not on a row that addresses no single object, and not on a
   * link inside a row, whose own menu is what a right-click on it is for.
   *
   * The grid draws the ways in and says where the menu was asked for; what the
   * menu holds, and drawing it, are the caller's. Drawn by the caller because a
   * list's cells are `overflow: hidden` (`theme.css`) and would clip a menu
   * drawn inside one.
   */
  rowMenu?: ((row: T) => ((anchor: RowMenuAnchor) => void) | undefined) | undefined;
  /** The row whose menu is open, by `rowKey`, for its `⋯`'s `aria-expanded`. */
  rowMenuOpen?: string | undefined;
  /**
   * The widths this gesture decided — **every column, every time**, the dragged
   * one first.
   *
   * ## Why every column: a stored width is a share
   *
   * A stored width is a **share of the table**, not a number of pixels. The
   * store keeps integers and the wire carries integers, and what they mean is
   * *this column against the others*: a set summing to 1266 in a 1266px panel
   * draws at 1266, and the same set in a 1676px panel draws at 1676 with every
   * column 1.324x wider. `theme.css` carries the rendering half — a sized table
   * is `width: 100%`, exactly as an unsized one is, and a fixed table shares the
   * surplus over its columns in proportion to what they declare.
   *
   * So a gesture cannot decide one column. Widening Name by 120px means the
   * columns after it give up 120px between them, or the table would have to grow
   * past its panel; **the set is the answer and each number in it is only
   * meaningful beside the others**: the columns always fill the whole page,
   * adapting to each other.
   *
   * Posting every column on **every** gesture also keeps two properties:
   *
   * - **The freeze.** `startResize` reads every column's drawn width, so the sum
   *   of the colgroup *is* what is on screen and one pixel of travel is one
   *   pixel of column. Without it a number written into the colgroup mid-drag is
   *   re-stretched by whatever the surplus then is, and the column runs away
   *   from the pointer.
   * - **No column is ever re-derived from content on a table the operator has
   *   sized.** Posting the whole set is what guarantees it: a column left
   *   unstored would be re-measured from the cells in front of the renderer, and
   *   its share of the table would then move on its own when the data changed.
   *
   * Freezing the other columns would clip a column that was already too narrow
   * for as long as the layout lived, if a frozen number were pixels. Here it is
   * a *share*: a column too narrow is too narrow as a fraction of the table, and
   * it widens with the window.
   */
  onResize?: ((sizes: readonly { key: string; width: number | null }[]) => void) | undefined;
  /**
   * A column was dropped beside another, on the named side.
   *
   * Beside a neighbor and not at an index, because **an index is a fact about
   * the list the browser last saw** and this panel is redrawn every two seconds.
   * The server owns the order and resolves the name against it, which is also
   * why nothing here reorders the DOM.
   */
  onPlace?: ((key: string, target: string, edge: 'before' | 'after') => void) | undefined;
  /**
   * Row height, for every table in the console.
   *
   * The toggle is drawn **always**, unlike Reset columns, and the server keeps
   * one value for the whole console rather than one per kind: it is a statement
   * about the operator's screen, and somebody who wants more rows at once wants
   * them everywhere.
   *
   * A prop and not a class on an ancestor, because the padding is the whole of
   * it here and a padding is a utility. Compact mode does not shrink the gauge
   * track or a pill's padding: both would be descendant rules over components
   * this grid does not know about, and threading density into every cell
   * renderer to save four pixels is a worse trade.
   */
  compact?: boolean;
}

/**
 * Controls inside a row that decline to open it.
 *
 * `data-no-open` is read from the event's target upwards: the row is the
 * element that has to not act, so the row is where it is said. Without it the
 * namespace link would open the drawer as well as filtering, which looks like
 * the filter being ignored.
 *
 * A closed attribute rather than a list of class names, so a control added later
 * opts out by saying so instead of by being remembered here.
 *
 * `CopyButton` additionally stops propagation on its own, and that stays: it is
 * the same rule from the other end, and two guards on the one control that is
 * repeated eight hundred times is the cheap direction to be wrong in.
 */
function declinesToOpen(target: EventTarget | null): boolean {
  return target instanceof Element && target.closest('[data-no-open]') !== null;
}

/**
 * A row menu asked for without a pointer - by the `⋯` or by the keyboard - is
 * drawn under the row's `⋯`, from its left edge: one place for the three ways
 * in that are not a pointer.
 */
function atMenuButton(within: HTMLElement, from: HTMLElement): RowMenuAnchor {
  const button = within.matches('[data-row-menu]') ? within : within.querySelector('[data-row-menu]');
  const box = (button ?? within).getBoundingClientRect();
  return { x: box.left, y: box.bottom, from };
}

/** Whether an event landed on a link, whose own menu is the browser's. */
function onALink(target: EventTarget | null): boolean {
  return target instanceof Element && target.closest('a[href]') !== null;
}

/**
 * The arithmetic. It is arithmetic and not styling, which is why it is here and
 * not in a stylesheet — `theme.css` says the same of its own half.
 *
 * `7` is an approximate advance for the console's UI face at the grid's step
 * (13px): a constant, not a measurement, so it does NOT follow the theme, and
 * it is close rather than generous. `40` is what a heading draws beside its
 * label - its two paddings, the sort mark and its gap, the resize grip and its
 * gap, 37px measured - and `20` a cell's two paddings.
 * They are deliberately coarse. The numbers do not have to be right; they have
 * to be **a function of the answer on screen**, which is the property the
 * colgroup and the rows being rendered together already guarantees.
 *
 * `HEAD_CH` is a heading's own advance: headings are drawn uppercase and
 * tracked (`tracking-heading`), wider per character than a value - `CONTAINERS`
 * measures 72px, `RESTARTS` 54, in the layout harness - with the margin the CI
 * runner's wider face needs (about 5%, `drawerWidth.ts`).
 *
 * **A column is never narrower than its heading.** A heading that gave way to
 * the values under it read `RE…` over Ready and Restarts both at 900px; a list
 * whose columns need more than its panel narrows the ones that yield
 * (`fitToRoom`), and no column is dropped to avoid it (`statusFloor.ts`). `CrashLoopBackOff` keeps
 * its room through the status column's own floor there, not by taking the
 * heading's.
 *
 * The floor and the ceiling stop the two failures either side: a one-character
 * heading over a column holding a timestamp, and one long string pushing every
 * other column off a laptop screen.
 */
const CH = 7;
const HEAD_CH = 8;
const HEAD_PAD = 40;
const CELL_PAD = 20;
const FLOOR = 64;
const CEILING = 320;

/**
 * What the tick costs the column that carries it.
 *
 * `defaultWidths` counts a pill, a gauge and a copy control; the checkbox is
 * reserved here, because under `table-layout: fixed` the name would otherwise
 * be clipped by whatever the box takes. A width derived from the text alone, in
 * a cell that also holds a control, is a width for less than the cell holds.
 */
const PICK = 22;

/**
 * What a row's `⋯` costs the column that carries it, for `PICK`'s reason: it
 * sits beside the value rather than instead of it, so a width derived from the
 * text alone is a width for less than the cell holds. Added only where the grid
 * draws one: the glyph's `--icon` (15px), its `px-0.5`
 * either side and the cell's `gap-2`, 27px, rounded up.
 */
const MENU = 28;

/**
 * The bounds a width is held to while it is being dragged.
 *
 * The same pair `internal/columns` clamps to, duplicated here so the column
 * stops under the pointer rather than snapping back after the round trip. The
 * server clamps regardless and the server is what decides.
 */
const MIN_WIDTH = 56;
const MAX_WIDTH = 720;
/** What one press of an arrow key is worth, and what Shift makes it. */
const STEP = 16;
const FINE = 4;

function clampWidth(width: number): number {
  return Math.round(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, width)));
}

/**
 * Takes `give` pixels off `pool`, in proportion to what each of them is drawn
 * at, and answers how much it managed to take. A negative `give` hands the
 * pixels back the same way.
 *
 * **In proportion to their own width and not in equal shares**: leaving the
 * untouched columns to share the difference equally draws Age as wide as
 * Namespace while Namespace shrinks, content-blind. Scaling them all by one
 * factor is the only redistribution that leaves the arrangement *between* them
 * exactly as the operator left it.
 *
 * **The floor wins over the proportion.** A column at `MIN_WIDTH` has nothing
 * left to give and is taken out of the pool, and the passes below hand what it
 * could not give to the columns that still can. When nobody can, the answer is
 * less than was asked for and the dragged column stops there — which is what
 * stops a drag from pushing three columns to nothing to satisfy a fourth.
 *
 * Mutates `widths` in place; the caller owns the copy.
 */
function spread(widths: Record<string, number>, pool: readonly string[], give: number): number {
  let left = give;
  const free = new Set(pool.filter((key) => widths[key] !== undefined));
  // At most one pass per column that can hit the floor, plus one to settle.
  for (let pass = 0; pass <= pool.length && Math.abs(left) > 0.01 && free.size > 0; pass++) {
    let total = 0;
    for (const key of free) total += widths[key] as number;
    if (total <= 0) break;
    const part = left / total;
    let took = 0;
    for (const key of [...free]) {
      const width = widths[key] as number;
      const want = width - width * part;
      if (want < MIN_WIDTH) {
        took += width - MIN_WIDTH;
        widths[key] = MIN_WIDTH;
        free.delete(key);
      } else {
        took += width - want;
        widths[key] = want;
      }
    }
    left -= took;
  }
  return give - left;
}

/**
 * The arrangement after the dragged column is taken to `want`: it moves, the
 * others absorb the difference, and **the total is unchanged**.
 *
 * The total is the whole point. It is the table's width, the table fills its
 * panel, and a gesture that changed the total would move the table's right edge
 * instead of the boundary the operator has hold of.
 *
 * **The columns *after* the dragged one absorb it**, so that everything to the
 * left of the grip stays where it is and the grip stays under the pointer. The
 * last column has nothing after it, so there the columns before it absorb and
 * the table shifts under the gesture — unavoidable once the total is fixed, and
 * better than a last column that cannot be resized at all.
 *
 * A column with no entry in `drawn` is not in the pool: that is a column with no
 * element to measure — every render before the first paint, and every test in
 * jsdom, which lays nothing out.
 */
export function absorb(
  order: readonly string[],
  drawn: Readonly<Record<string, number>>,
  key: string,
  want: number,
): Record<string, number> {
  const next = { ...drawn };
  const here = drawn[key];
  const at = order.indexOf(key);
  if (here === undefined || at < 0) {
    next[key] = want;
    return next;
  }
  const after = order.slice(at + 1).filter((each) => drawn[each] !== undefined);
  const pool = after.length > 0 ? after : order.slice(0, at).filter((each) => drawn[each] !== undefined);
  next[key] = here + spread(next, pool, want - here);
  return next;
}

/**
 * The set to post, from the set on screen.
 *
 * Only the ratios are kept, so the numbers may be scaled — and they have to be,
 * because the store clamps every width to [`MIN_WIDTH`, `MAX_WIDTH`] and a
 * clamped number is a *changed ratio*. On a wide enough panel a column is drawn
 * past 720px, and posting that would have the store keep 720 and quietly
 * narrow that column's share on the next reload. Scaling the whole set instead
 * changes nothing about the arrangement, because the arrangement is the ratios.
 *
 * **Where both bounds cannot hold at once the floor wins**: an arrangement wider
 * than 720/56 = 12.86 to 1 cannot be expressed in the store's bounds at all, and
 * a column under the floor is one nobody can grab again, while a column at the
 * ceiling is merely compressed.
 *
 * Integers, because the wire is integers. The rounding is worth at most half a
 * pixel on the narrowest column, which is a fifth of a percent of its share.
 */
export function postable(
  order: readonly string[],
  drawn: Readonly<Record<string, number>>,
): { key: string; width: number | null }[] {
  const keys = order.filter((key) => drawn[key] !== undefined);
  if (keys.length === 0) return [];
  let smallest = Infinity;
  let largest = 0;
  for (const key of keys) {
    const width = drawn[key] as number;
    smallest = Math.min(smallest, width);
    largest = Math.max(largest, width);
  }
  const up = MIN_WIDTH / smallest;
  const down = MAX_WIDTH / largest;
  const scale = up > 1 ? up : down < 1 ? down : 1;
  return keys.map((key) => ({ key, width: clampWidth((drawn[key] as number) * scale) }));
}

/**
 * What a column that yields keeps while the others still have room to give:
 * its heading, its tick and `⋯`, and about a dozen characters of its value - or
 * its own width, where that is less.
 */
const KEEPS = 'x'.repeat(12);

export function yieldFloor<T>(column: GridColumn<T>, own: number, menu = false): number {
  return Math.min(own, columnWidth(column, [KEEPS], menu));
}

/** What any column keeps before the table falls back to shares: its heading
 *  whole, with its tick and `⋯` - or its own width, where that is less. */
export function headFloor<T>(column: GridColumn<T>, own: number, menu = false): number {
  return Math.min(own, columnWidth({ ...column, reserve: 0 }, [], menu));
}

/** One column, as `fitToRoom` reads it. */
export interface Fitting {
  width: number;
  /** `GridColumn.yields`. */
  yields: 1 | 2 | undefined;
  /** `yieldFloor`, for a column that yields. */
  floor: number;
  /** `headFloor`. */
  head: number;
  /** `GridColumn.holds`. */
  holds?: boolean | undefined;
  /**
   * For a column that holds, what the values in front of the renderer need,
   * without its reserve. A function, because it walks every row and is only
   * asked where the four steps were not enough.
   */
  need?: (() => number) | undefined;
}

/** What a column is left with, at the least, before the held ones give way. */
const SQUEEZED = 40;

/**
 * The widths an unarranged list draws at, so that it fits `room`, its panel's
 * width, and never scrolls sideways.
 *
 * The arithmetic's widths are what each column needs. Where they sum to more
 * than the panel, the difference is taken in steps, each only while the one
 * before could not cover it, and within a step from every column it names in
 * proportion to what that column has above the step's floor:
 *
 * 1. the name and the namespace (`yields: 1`), down to `yieldFloor`;
 * 2. any other free text (`yields: 2`), down to `yieldFloor`;
 * 3. every other column - a count, a gauge - down to its heading;
 * 4. the columns that yield, down to their heading.
 *
 * A column that `holds` (the status) keeps its width through all four, then
 * gives back its reserve, down to what its values need (`need`). A value
 * cut on the way is ellipsized and offered whole on hover. No column is
 * dropped. `squeezed` is the last resort, for a panel narrower than every
 * heading together: every column but the held ones is narrowed in proportion,
 * the held ones too only where the rest would be left under `SQUEEZED` each,
 * and the widths are declared as shares of the table (`declareWidth`), so the
 * table is never wider than its panel.
 */
export function fitToRoom(columns: readonly Fitting[], room: number): { widths: number[]; squeezed: boolean } {
  const out = columns.map((column) => column.width);
  const sum = (): number => out.reduce((total, each) => total + each, 0);
  const steps: ((column: Fitting) => number | undefined)[] = [
    (column) => (column.yields === 1 ? column.floor : undefined),
    (column) => (column.yields === 2 ? column.floor : undefined),
    (column) => (column.yields === undefined && column.holds !== true ? column.head : undefined),
    (column) => (column.yields === undefined ? undefined : column.head),
    (column) => (column.holds === true ? Math.max(column.head, column.need?.() ?? column.width) : undefined),
  ];
  for (const step of steps) {
    const over = sum() - room;
    if (over <= 0) break;
    const pool = columns.flatMap((column, at) => {
      const to = step(column);
      const slack = to === undefined ? 0 : (out[at] as number) - to;
      return slack > 0 && to !== undefined ? [{ at, slack, to }] : [];
    });
    const slack = pool.reduce((total, each) => total + each.slack, 0);
    if (slack <= 0) continue;
    const take = Math.min(over, slack);
    for (const each of pool) {
      out[each.at] = Math.max(each.to, (out[each.at] as number) - Math.ceil((take * each.slack) / slack));
    }
  }
  const total = sum();
  if (total <= room) return { widths: out, squeezed: false };
  const held = columns.reduce((kept, column, at) => kept + (column.holds === true ? (out[at] as number) : 0), 0);
  const others = columns.filter((column) => column.holds !== true).length;
  const keep = room - held >= SQUEEZED * others && total > held;
  const scale = keep ? (room - held) / (total - held) : room / total;
  return {
    widths: out.map((width, at) => (keep && columns[at]?.holds === true ? width : width * scale)),
    squeezed: true,
  };
}

/**
 * The width a list's table has to fit, read before paint and again whenever
 * the panel changes width - a window resized, the drawer opened or dragged, a
 * vertical scroll bar appearing. `null` where nothing is laid out (jsdom) or
 * for a table that is not a list.
 */
function useRoom(box: { readonly current: HTMLElement | null }, on: boolean): number | null {
  const [room, setRoom] = useState<number | null>(null);
  useLayoutEffect(() => {
    const node = box.current;
    if (!on || node === null || typeof ResizeObserver === 'undefined') return;
    const read = (): void => {
      const width = node.clientWidth;
      setRoom(width > 0 ? width : null);
    };
    read();
    const observer = new ResizeObserver(read);
    observer.observe(node);
    return () => {
      observer.disconnect();
    };
  }, [box, on]);
  return room;
}

/** What a heading needs to draw whole. */
export function headingRoom(label: string): number {
  return label.length * HEAD_CH + HEAD_PAD;
}

/**
 * The width of one column, from its definition and the rows in front of it.
 *
 * `samples` is what this answer holds. An empty `samples` — no rows at all, or a
 * column blank on every row — is the case the heading wins outright.
 */
export function columnWidth<T>(column: GridColumn<T>, samples: readonly string[], menu = false): number {
  const head = headingRoom(column.label);
  const widest = samples.reduce((longest, text) => Math.max(longest, text.length), 0);
  const values = widest === 0 ? 0 : widest * (CH + (column.perCharacter ?? 0)) + CELL_PAD + (column.pad ?? 0);
  const want = Math.max(values, head);
  // The tick is **added** to whatever the column needs, not floored against it:
  // it sits beside the value rather than instead of it, so it is the *whole*
  // control that is reserved, not whatever the heading happened to leave over.
  const tick = column.leads === true ? PICK : 0;
  return Math.min(CEILING, Math.max(FLOOR, column.reserve ?? 0, want) + tick + (menu ? MENU : 0));
}

/**
 * What this column declares, which is the operator's number wherever they have
 * one and the arithmetic above where they do not.
 *
 * **It is a weight and not a width.** A fixed table is laid out to
 * the *ratios* of its colgroup — it fills its panel and shares the surplus out
 * in proportion — so the number here decides this column's share of whatever
 * room there is, and equals its drawn width only when the panel happens to be
 * the sum of them all. The unit is still the pixel because that is what a
 * pointer produces and what the store clamps, and mixing a stored number with a
 * content-derived one is meaningful for the same reason: both are the width that
 * column would take if the table were exactly wide enough.
 */
function widthOf<T>(column: GridColumn<T>, rows: readonly T[], menu = false): number {
  if (typeof column.width === 'number') return column.width;
  const measure = column.measure;
  const samples = measure === undefined ? [] : rows.map((row) => measure(row));
  return columnWidth(column, samples, menu);
}

/**
 * What one `<col>` declares, out of the set the table is drawing.
 *
 * **A share is written as a share.** A stored number left in the colgroup as
 * pixels is a share in one direction only: a fixed table shares a *surplus* out
 * in proportion, but CSS 2.1 §17.5.2.1 makes the table's width the **greater**
 * of its own `width` and the sum of its columns — so an arrangement summing to
 * more than the panel is not scaled down at all. It is drawn at its literal
 * pixels and the panel scrolls sideways (ten Nodes columns summing to 2 284 in
 * a 1 292px panel overflow by 992px, before any gesture).
 *
 * A percentage has no such asymmetry. 13.57% of a table that is itself
 * `width: 100%` is that fraction of the panel whatever the panel is, narrower
 * and wider alike, so the table is always exactly its panel and the proportions
 * are exactly the ones that were dragged. **Nothing is converted**: the stored
 * numbers are untouched on the wire and on disk; they are only read
 * differently.
 *
 * For a sized table, and for a list `fitToRoom` had to squeeze past every
 * heading. Otherwise the arithmetic's pixels, fitted to the panel.
 *
 * `total` cannot be zero here — every column declares at least `FLOOR` — and is
 * guarded anyway, because a zero would make every width `NaN%` and lose the
 * colgroup altogether rather than mis-size it.
 */
export function declareWidth(width: number, total: number, sized: boolean): string {
  if (!sized || total <= 0) return `${String(width)}px`;
  // Four decimals is 0.0001% of a 1 292px panel: thirteen ten-thousandths of a
  // pixel, against a colgroup the browser re-normalizes anyway.
  return `${String(Math.round((width / total) * 1e6) / 1e4)}%`;
}

/** From the operator's first drag onwards the table lays itself out to the
 *  stored shares — the only way a column can be made narrower than its content,
 *  and therefore the only way resizing means anything at all. It still fills its
 *  panel; what `.sized` decides is whose numbers the ratios come from, and that
 *  a cell too long for its column is clipped rather than painted over the next
 *  one. */
export function isSized<T>(columns: readonly GridColumn<T>[]): boolean {
  return columns.some((column) => typeof column.width === 'number');
}

export function DataGrid<T>({
  columns,
  rows,
  rowKey,
  label,
  sort,
  onSort,
  selection,
  empty,
  rowAction,
  rowMenu,
  rowMenuOpen,
  compact = false,
  onResize,
  onPlace,
  stickyHead = false,
}: DataGridProps<T>): ReactNode {
  /*
    The width a column is being dragged to, before it is committed.

    **The one thing drawn locally during a gesture**, the exception to "the
    server holds the arrangement": the whole point of a resize is watching the
    column follow the pointer. What is drawn is not what is kept — the number
    goes to the server on release, is clamped there, and comes back in the
    answer.

    Keyed by column, so a drag on one says nothing about the others. Cleared when
    the pointer is released, at which point the payload is the authority again.
  */
  const [dragWidth, setDragWidth] = useState<Readonly<Record<string, number>>>({});
  /*
    **Every row is in the list; only the ones in view are in the page.** Past
    `WINDOW_AFTER` rows the body draws the visible rows, an overscan, and the
    one row that holds the focus (or whose menu is open), with a spacer row
    standing in for each gap. Widths, sort, filter, the count and select-all
    all read `rows`, the whole list, so none of them depends on what is drawn.
  */
  const bodyRef = useRef<HTMLTableSectionElement>(null);
  const [heldId, setHeldId] = useState<string | null>(null);
  const rowHeight = compact ? 28 : 36;
  const holding = [heldId, rowMenuOpen].filter((id): id is string => typeof id === 'string');
  const pinned =
    rows.length > WINDOW_AFTER && holding.length > 0
      ? holding.map((id) => rows.findIndex((row) => rowKey(row) === id))
      : [];
  const spans = useRowWindow(bodyRef, rows.length, rowHeight, pinned);
  const entries: ({ row: T } | { gap: number; key: string })[] = [];
  let walked = 0;
  for (const span of spans) {
    if (span.from > walked) entries.push({ gap: span.from - walked, key: `gap-${String(walked)}` });
    for (let at = span.from; at < span.to; at += 1) entries.push({ row: rows[at] as T });
    walked = span.to;
  }
  if (walked < rows.length) entries.push({ gap: rows.length - walked, key: `gap-${String(walked)}` });
  const sizingRef = useRef<{
    key: string;
    startX: number;
    startWidth: number;
    /** Every column's width when the pointer went down. See `startResize`. */
    frozen: Readonly<Record<string, number>>;
  } | null>(null);
  /*
    Whether a resize is in progress, as **state** and not only as the ref above.

    `draggable` is turned off on the heading for the duration, and it is the
    attribute rather than `preventDefault` because whether a press cancels the
    drag depends on the browser deriving mousedown from pointerdown, which they
    do not all do the same way. An attribute has to be rendered, and a ref may
    not be read during render, so the fact is held twice: the ref for the
    handlers, this for the markup.
  */
  const [resizing, setResizing] = useState(false);
  const [dropping, setDropping] = useState<{ key: string; edge: 'before' | 'after' } | null>(null);
  const draggedRef = useRef<string | null>(null);

  /*
    Sized by the payload, **or for the duration of a gesture**.

    The second half is not a convenience: the sized arrangement is the one where
    the colgroup is authoritative, and a drag is exactly when the colgroup has to
    be. On a table nobody has resized yet the surplus is still being shared out,
    so a width written mid-drag comes back re-inflated and the column runs away
    from the pointer. `Heading.freeze` puts every column's drawn width in
    `dragWidth` at pointerdown, so the sum is what is already on screen and this
    flag costs nothing visible at the moment it flips.

    Both are needed. `isSized` alone leaves the first drag on every table wrong;
    this alone would lose the arrangement the moment the pointer came up.
  */
  const sized = isSized(columns) || resizing;
  /*
    What each column declares, and what they declare between them — the second
    being what each one's share is a share **of**. See `declareWidth`.

    **Held as an array and summed from it, rather than measured twice.** Each
    `widthOf` walks every row through `GridColumn.measure`, so a second pass to
    total them is a second pass over the whole table; `DataGrid.perf.test.tsx`
    counts those calls.
  */
  const carrier = columns.find((column) => column.leads) ?? columns[0];
  const declaredEach = columns.map(
    (column) =>
      dragWidth[column.key] ?? widthOf(column, rows, rowMenu !== undefined && column === carrier),
  );
  /*
    An unarranged list fits its panel: the columns that yield give up what the
    others need (`fitToRoom`), so the table never scrolls sideways. An arranged
    one already fits, by its shares.
  */
  const scrollRef = useRef<HTMLDivElement>(null);
  const room = useRoom(scrollRef, stickyHead);
  const fit =
    sized || room === null
      ? undefined
      : fitToRoom(
          columns.map((column, at) => {
            const width = declaredEach[at] ?? 0;
            const menu = rowMenu !== undefined && column === carrier;
            return {
              width,
              yields: column.yields,
              floor: yieldFloor(column, width, menu),
              head: headFloor(column, width, menu),
              holds: column.holds,
              need: () => {
                const measure = column.measure;
                const samples = measure === undefined ? [] : rows.map((row) => measure(row));
                return Math.min(width, columnWidth({ ...column, reserve: 0 }, samples, menu));
              },
            };
          }),
          room,
        );
  const drawnEach = fit?.widths ?? declaredEach;
  const declared = drawnEach.reduce((total, each) => total + each, 0);
  const shares = sized || fit?.squeezed === true;
  // No column is added for the ticks, so the notice spans exactly the columns
  // the operator asked for.
  const span = columns.length;
  /*
    The column that carries the tick.

    At most one: a payload declaring two `name` roles would otherwise draw two
    checkboxes on every row. And **the first column where none declares it** —
    a grid given a selection and no `leads` would otherwise render no checkbox
    anywhere, so forgetting the flag would silently remove the ticks rather than
    misplace them. The server names the descriptor's first column `name` for the
    same reason.
  */
  const leading = selection === undefined ? undefined : carrier;
  /* The row menu's `⋯` sits in the same cell as the tick, for the tick's
     reason: it is about the object, and the name is what identifies it. */
  const menuCell = rowMenu === undefined ? undefined : carrier;
  /* The column held at the left edge should a list scroll sideways (a list
     fits its panel, `fitToRoom`, so only a table held wider): the one
     that carries the ticks and the `⋯` (the column that `leads`, the name in
     a resource list, or the first column where none leads), and only where
     it is drawn first. Held anywhere else, `left-0` would lift it over the
     columns before it. Its tick and `⋯` are inside its cell, so they are held
     with it. A list only, as `stickyHead` says: a fact table or a card sizes
     to itself and keeps no edge. `data-held` on its heading tells the list's
     sideways fade where the held column ends. */
  const held = stickyHead && columns[0] === carrier ? carrier : undefined;

  return (
    // `table-scroll` is a selector `theme.css` matches on, not a bundle of
    // utilities: `#panel > .table-scroll > .kt-grid` is how the fixed layout is
    // scoped to the list and kept off the drawer's fact tables and the overview
    // cards, which are content that should size to itself.
    <div ref={scrollRef} className={`table-scroll min-w-0${stickyHead ? '' : ' overflow-x-auto'}`}>
      {/*
        `w-full` on every table **except a sized one**.

        Tailwind 4 emits its utilities into `@layer utilities`, declared after
        `@layer components` — and **layer order beats specificity**. So
        `w-full`'s `width: 100%` would win over `theme.css`'s
        `.kt-grid.sized`'s own width whatever either selector scores, and a width
        the operator dragged would not be laid out the way the stylesheet says it
        is. `theme.css` carries the other half of this, an id selector.

        A `w-full` written as a `:not(.sized)` variant would read as the same
        statement and is not one — it is still a utility fighting a component
        rule, and the next person to add a width utility here reopens this. The
        class is simply not emitted where the component rule owns the width.

        Both rules ask for `width: 100%` today, and the class stays off a sized
        table anyway: the stylesheet owns the width of a table the operator has
        arranged, and a utility that happens to agree with it today is a utility
        that will be edited by somebody who has not read `theme.css`.
      */}
      <table
        data-density={compact ? 'compact' : 'comfortable'}
        className={`${TABLE}${sized ? ' sized' : ' w-full'}`}
        aria-label={label}
      >
        <colgroup>
          {columns.map((column, at) => (
            <col
              key={column.key}
              style={{ width: declareWidth(drawnEach[at] ?? 0, declared, shares) }}
            />
          ))}
        </colgroup>

        <thead>
          {/*
            The headings' rhythm: `tracking-heading` (.04em), wider than
            Tailwind's `tracking-wide`.

            The rule under the headings and their surface are on the `<th>` and
            not here, because a border on a `<tr>` does not travel with a sticky
            cell: whether the heading sticks is decided by the scrollport, and a
            heading that ever sticks with its rule left behind on the row is a
            line that scrolls away from what it underlines.
          */}
          <tr className={HEAD_ROW}>
            {columns.map((column) => (
              <Heading
                key={column.key}
                sticky={stickyHead}
                pinned={column === held}
                column={column}
                columns={columns}
                rows={rows}
                sort={sort}
                {...(onSort === undefined ? {} : { onSort })}
                {...(selection !== undefined && column === leading ? { selection } : {})}
                {...(onResize === undefined ? {} : { onResize })}
                {...(onPlace === undefined ? {} : { onPlace })}
                dragWidth={dragWidth}
                setDragWidth={setDragWidth}
                sizingRef={sizingRef}
                resizing={resizing}
                setResizing={setResizing}
                draggedRef={draggedRef}
                dropping={dropping}
                setDropping={setDropping}
              />
            ))}
          </tr>
        </thead>

        <tbody
          ref={bodyRef}
          onFocus={(event) => {
            const at = (event.target as HTMLElement).closest('tr[data-rowid]');
            setHeldId(at?.getAttribute('data-rowid') ?? null);
          }}
          onBlur={(event) => {
            const to = event.relatedTarget;
            if (!(to instanceof Node) || !event.currentTarget.contains(to)) setHeldId(null);
          }}
        >
          {rows.length === 0 ? (
            <tr>
              <td colSpan={span} className="prose p-6 text-center align-top">
                {empty}
              </td>
            </tr>
          ) : (
            entries.map((entry) => {
              if ('gap' in entry) {
                return (
                  <tr key={entry.key} aria-hidden="true" data-spacer="">
                    <td colSpan={span} className="p-0" style={{ height: entry.gap * rowHeight }} />
                  </tr>
                );
              }
              const row = entry.row;
              const id = rowKey(row);
              const picked = selection !== undefined && selection.picked.has(id);
              const action = rowAction?.(row);
              const menu = rowMenu?.(row);
              const open =
                action === undefined
                  ? undefined
                  : (event: { target: EventTarget | null }) => {
                      if (declinesToOpen(event.target)) return;
                      action();
                    };

              return (
                <tr
                  key={id}
                  data-rowid={id}
                  data-picked={picked ? 'on' : undefined}
                  {...(menu === undefined
                    ? {}
                    : {
                        /*
                          **Where the menu opens**: a right-click opens it at
                          the pointer; the `⋯`, the menu key and Shift+F10 open
                          it under the `⋯`. They are told apart by `button ===
                          2`, not by a contextmenu at (0,0): real Chromium sends
                          the menu key's at the focused element's coordinates.

                          A link inside the row keeps the browser's menu, from
                          the pointer and from the keyboard alike - open in a new
                          tab and copy the address are what it is for.
                        */
                        onContextMenu: (event: ReactMouseEvent<HTMLTableRowElement>) => {
                          if (onALink(event.target)) return;
                          event.preventDefault();
                          const at = event.currentTarget;
                          menu(
                            event.button === 2
                              ? { x: event.clientX, y: event.clientY, from: at }
                              : atMenuButton(at, at),
                          );
                        },
                        onKeyDown: (event: ReactKeyboardEvent<HTMLTableRowElement>) => {
                          if (event.key !== 'ContextMenu' && !(event.shiftKey && event.key === 'F10')) return;
                          if (onALink(event.target)) return;
                          event.preventDefault();
                          menu(atMenuButton(event.currentTarget, event.currentTarget));
                        },
                      })}
                  // A tabstop only where there is something to open, and Enter
                  // on keyup and not keydown: keydown on a row that is about to
                  // be replaced fires the action of the row underneath.
                  {...(open === undefined
                    ? {}
                    : {
                        tabIndex: 0,
                        onClick: open,
                        onKeyUp: (event: { key: string; target: EventTarget | null }) => {
                          if (event.key !== 'Enter') return;
                          open(event);
                        },
                      })}
                  // The ring only where the row is a tabstop. `.pickable` is
                  // flush with the scroll region on both sides, so it is drawn
                  // inside.
                  /*
                    **Hover only, and no zebra**, for a sober, dark table. A
                    striped table draws a rhythm the data does not have, and it
                    fights the one thing this table does color - a row that is
                    picked, and a row under the pointer, both `bg-panel-2`.

                    The separator is `line` at half strength. At full strength on
                    forty rows the rules read as the grid and the names read as
                    what is inside it; halved, the row is the unit and the line is
                    only where one ends.
                  */
                  // `group`: the row's `⋯` is drawn while the row is hovered or
                  // holds the focus.
                  className={`group ${ROW} ${picked ? 'bg-panel-2' : ''}${
                    open === undefined ? '' : ` cursor-pointer hover:bg-panel-2 ${focusRingFlush}`
                  }`}
                >
                  {columns.map((column) => {
                    // The wrap name goes on the cell and only on the cell: on
                    // anything this cell contains it is inert. `title` is on the
                    // cell for the neighboring reason: it is the element whose
                    // box a clipped value overflows.
                    const title = column.title?.(row);
                    return (
                      <td
                        key={column.key}
                        className={`${cellClass(column, compact)}${
                          column === held ? pinCell(picked, open !== undefined) : ''
                        }`}
                        {...(title === undefined || title === ''
                          ? { onPointerEnter: offerWhole(() => column.measure?.(row) ?? '') }
                          : { title })}
                      >
                        {(selection !== undefined && column === leading) ||
                        (menu !== undefined && column === menuCell) ? (
                          // In the name's own cell, beside the value. Not
                          // rendered by anything that knows what a `Cell` is:
                          // the tick and the `⋯` are about the row, and the
                          // cell renderer is about one column of it.
                          <span className="flex min-w-0 items-center gap-2">
                            {selection !== undefined && column === leading ? (
                              <input
                                type="checkbox"
                                // **Declines to open the row**, like every other
                                // control inside one. Without it, ticking a box
                                // opens the drawer as well - so building a
                                // selection of six pods opens and replaces the
                                // drawer six times.
                                data-no-open
                                className={`shrink-0 ${focusRing}`}
                                aria-label={`Select ${id}`}
                                checked={picked}
                                onChange={() => {
                                  selection.toggle(id);
                                }}
                              />
                            ) : null}
                            <span className="min-w-0 flex-1 truncate">{column.cell(row)}</span>
                            {menu !== undefined && column === menuCell ? (
                              <button
                                type="button"
                                // The row's menu, found by looking. Declines to
                                // open the row, for the tick's reason.
                                data-no-open
                                data-row-menu
                                aria-haspopup="menu"
                                aria-expanded={rowMenuOpen === id}
                                aria-label={`Actions on ${id}`}
                                title="Actions (or right-click the row)"
                                onClick={(event) => {
                                  menu(atMenuButton(event.currentTarget, event.currentTarget));
                                }}
                                /*
                                  Drawn only on the row under the pointer or
                                  holding the focus, its own included, and while
                                  its menu is open. **Opacity and not
                                  `invisible`**: its room stays reserved, so
                                  nothing in the row moves when it appears, and it
                                  stays in the tab order, which a hidden element
                                  leaves.
                                  A 24px box, its -4px margins keeping a compact
                                  row as tall as it was.
                                */
                                className={`-my-1 flex size-6 shrink-0 items-center justify-center rounded-sm text-muted hover:text-fg ${
                                  rowMenuOpen === id
                                    ? 'opacity-100'
                                    : 'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100'
                                } ${focusRingTight}`}
                              >
                                {/* A shape and not the `⋯` character: the row's
                                    text is what is read, selected and copied,
                                    and the glyph is not part of it. */}
                                <Icon name="more" />
                              </button>
                            ) : null}
                          </span>
                        ) : (
                          column.cell(row)
                        )}
                      </td>
                    );
                  })}
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}

/**
 * The heading cell's own classes, in one place because there are two `<th>`
 * arms and they drifted by a utility the first time somebody edited one.
 *
 * ## The surface, and the sticky that is claimed rather than measured
 *
 * The headings sit on the sticky `bg-panel` surface. **`sticky` is added only by
 * `stickyHead`**, and the reason it is not here unconditionally is this: the
 * nearest scrollport to a `<th>` in this table is
 * `.table-scroll`, which is `overflow-x-auto` — and a box with one axis not
 * `visible` computes the other to `auto`, so the heading's sticky container is a
 * box the height of the whole table that never scrolls vertically. `position:
 * sticky` there is inert. The box that does scroll is `#panel`, two levels up.
 *
 * Adding it anyway would be a class that reads as a working declaration and does
 * nothing. Making it work means changing what `.table-scroll` overflows, and
 * that box is what the fixed-layout arithmetic is scoped to
 * (`#panel > .table-scroll > .kt-grid`) — a geometry change, not a look.
 *
 * The rule under the headings is on the cell for the same reason it is not on
 * the row: when the sticky does arrive, a border left on the `<tr>` scrolls away
 * from what it underlines.
 */
const HEAD = `${headCell()} bg-panel`;

/**
 * Whether anything in this box is drawn shorter than its text.
 *
 * The box itself, or anything in it: a pill truncates inside a cell that does
 * not, and a heading's label inside a button that does not. An inline element
 * measures 0 by 0 and is skipped by the comparison rather than by a rule.
 */
function clipsText(box: HTMLElement): boolean {
  if (box.scrollWidth > box.clientWidth + 1) return true;
  for (const one of box.querySelectorAll('*')) {
    if (one instanceof HTMLElement && one.scrollWidth > one.clientWidth + 1) return true;
  }
  return false;
}

/**
 * The whole of a clipped text, as the hover of the box that clips it - asked
 * on the way in, so the answer is the one for what is on screen now.
 *
 * **Measured, and only where it clips**, so a clipped `RE…` or `Warni…` can
 * be read whole. A title on every cell would repeat what is already legible in
 * each of them, and a length rule guesses wrong for a short word in a narrow
 * pill. Whether
 * a box clips is a fact of the layout, so it is read off the layout, at the
 * moment the hover is wanted. The text itself is in the DOM whole either way,
 * so a screen reader was never short of it; this is the eye's equivalent.
 *
 * Only on a box React gives no title of its own: a cell's `hint` or a heading's
 * `headTitle` says something better than the text repeated, and an attribute
 * set here would race React's for the same name.
 */
function offerWhole(textOf: () => string) {
  return (event: ReactPointerEvent<HTMLElement>): void => {
    const box = event.currentTarget;
    // Read on the way in and not during the render: `measure` walks a cell,
    // and `DataGrid.perf.test.tsx` counts how often a render does that.
    const text = textOf();
    if (text !== '' && clipsText(box)) box.setAttribute('title', text);
    else box.removeAttribute('title');
  };
}

/**
 * `stickyHead`'s half on the cell. `z-1` lifts it over the rows' own positioned
 * content - a row's tick, a copy control - as they slide under it.
 *
 * **`top-0`, because the list panel has no padding.** A sticky box is held
 * inside its scrollport's *content* box, so a padded `#panel` would stop a
 * `top-0` heading below the panel's edge with the rows scrolling through that
 * band. Padding put on `#panel` needs a matching negative offset here.
 */
const STICKY = ' sticky top-0 z-1';

/**
 * The held column's heading (the name's in a resource list), held at the left
 * edge as well. Above every other
 * heading and above the held cells below it, which pass under it as the rows
 * scroll up.
 */
function pinHead(sticky: boolean): string {
  return sticky ? ' sticky top-0 left-0 z-3' : ' sticky left-0 z-3';
}

/**
 * The held column's cell (the name's in a resource list), held at the left
 * edge. Above the other cells and their own positioned content as they pass
 * under it, and **opaque**: a row draws no surface of its own, so a held cell
 * left transparent shows the cells passing under it. The surface follows the
 * row's: `bg-panel-2` when picked or under the pointer, and the list's own
 * `bg` otherwise.
 *
 * **It draws its share of the row's focus ring.** The ring is an outline on the
 * `<tr>` (`focusRingFlush`, inside the row by 2px), and an opaque positioned
 * cell paints over the row's outline where it sits. So while the row holds the
 * keyboard focus, the cell draws the same 2px accent on its left, top and
 * bottom, inside itself, where the row's would have been: the ring reads whole.
 */
function pinCell(picked: boolean, opens: boolean): string {
  const base = ` sticky left-0 z-2${opens ? HELD_RING : ''}`;
  if (picked) return `${base} bg-panel-2`;
  return `${base} bg-bg${opens ? ' group-hover:bg-panel-2' : ''}`;
}

/** `pinCell`'s share of the row's ring: the row's 2px accent, without its right edge. */
const HELD_RING =
  ' group-focus-visible:before:pointer-events-none group-focus-visible:before:absolute group-focus-visible:before:inset-0' +
  ' group-focus-visible:before:border-2 group-focus-visible:before:border-r-0 group-focus-visible:before:border-accent';

/**
 * The flex box inside a heading, aligned with its column.
 *
 * `text-right` on the `<th>` would move nothing: the label sits in a flex box,
 * and a flex box's children follow `justify-content`, never `text-align`. So an
 * end-aligned column would draw its value at the right edge and its heading at
 * the left.
 */
function headBox(column: { align?: 'start' | 'end' | undefined }): string {
  return `flex min-w-0 items-center gap-2${column.align === 'end' ? ' justify-end' : ''}`;
}

function cellClass<T>(column: GridColumn<T>, compact: boolean): string {
  const wrap = column.wrap === undefined ? '' : ` ${column.wrap}`;
  const align = column.align === 'end' ? ' text-right' : '';
  // The padding, the row height and the density are every table's, in
  // `tableLook.ts`.
  return `${bodyCell(compact)}${wrap}${align}`;
}

/**
 * One heading: the label, the sort control, the select-all where it belongs, and
 * the grip.
 *
 * ## The grip is a real button, and that is the rule rather than a detail
 *
 * **It adds a way, it replaces none.** The arrow keys resize the column and Alt
 * with them moves it — the path that works from the keyboard, and the one that
 * works when a drag on a trackpad does not.
 *
 * ## The three traps in a column drag
 *
 * 1. **A press that moved nothing stores nothing.** The grip sits inside the
 *    heading, so a click landing on it is an ordinary click to the operator. A
 *    post there is not a no-op: the server stores what it is sent and a stored
 *    width is **sticky**, so a column never gets sized from its contents again.
 *    On a list with no rows every column is at its floor, and one stray click
 *    would freeze the floor into the layout for good. The comparison is against
 *    the width at **pointerdown** rather than counting pixels moved,
 *    because the question is whether the gesture changed anything and a drag out
 *    and back has not.
 * 2. **`pointercancel` commits too.** A touch turning into a scroll, or a window
 *    losing focus mid-drag, must still keep what is on screen — otherwise the
 *    next refresh silently undoes the resize, which reads as a feature that works
 *    only sometimes.
 * 3. **The heading is made undraggable for the duration.** `preventDefault` on
 *    the press keeps the text from selecting, and it is *not* what stops the
 *    reorder drag starting: whether it does depends on the browser deriving
 *    mousedown from pointerdown, which they do not all do the same way. Turning
 *    `draggable` off, which no browser has an opinion about, is what stops it.
 *
 * ## And the fourth: every column is posted, never one alone
 *
 * See `DataGridProps.onResize`.
 *
 * ## Reordering never touches the DOM
 *
 * A drop sends "beside that one, on this side" and the answer redraws the table.
 * `draggable` is on the `<th>` and explicitly **off** the controls inside it: an
 * `<a>` or a `<button>` is a drag source in its own right and would carry
 * something else into the drop. The rail learnt this on its tiles.
 */
function Heading<T>({
  column,
  columns,
  rows,
  sort,
  onSort,
  selection,
  onResize,
  onPlace,
  dragWidth,
  setDragWidth,
  sizingRef,
  resizing,
  setResizing,
  draggedRef,
  dropping,
  setDropping,
  sticky,
  pinned,
}: {
  sticky: boolean;
  /** The name's heading, held at the left edge (`pinHead`). */
  pinned: boolean;
  column: GridColumn<T>;
  columns: readonly GridColumn<T>[];
  rows: readonly T[];
  sort?: GridSort | undefined;
  onSort?: ((key: string) => void) | undefined;
  /** Given only to the column that carries the ticks, which draws the select-all
   *  inside its own heading rather than in a column of its own. */
  selection?: GridSelection | undefined;
  onResize?: ((sizes: readonly { key: string; width: number | null }[]) => void) | undefined;
  onPlace?: ((key: string, target: string, edge: 'before' | 'after') => void) | undefined;
  dragWidth: Readonly<Record<string, number>>;
  setDragWidth: (next: (previous: Readonly<Record<string, number>>) => Record<string, number>) => void;
  sizingRef: {
    current: {
      key: string;
      startX: number;
      startWidth: number;
      frozen: Readonly<Record<string, number>>;
    } | null;
  };
  /** True while any column is being dragged wider, which is what takes
   *  `draggable` off the heading for the duration. */
  resizing: boolean;
  setResizing: (busy: boolean) => void;
  draggedRef: { current: string | null };
  dropping: { key: string; edge: 'before' | 'after' } | null;
  setDropping: (next: { key: string; edge: 'before' | 'after' } | null) => void;
}): ReactNode {
  /** The heading itself, so a gesture can start from the width on screen rather
   *  than from the one in the colgroup. See `current` below for the difference. */
  const headRef = useRef<HTMLTableCellElement>(null);
  const sortable = column.sortable !== false && onSort !== undefined;
  const active = sort !== undefined && sort.key === column.key;
  const align = column.align === 'end' ? ' text-right' : '';
  const resizable = onResize !== undefined;
  const movable = onPlace !== undefined;

  const all =
    selection === undefined ? null : (
      <input
        type="checkbox"
        // Not in a row, so nothing would open - carried for the same reason the
        // row's is, which is that the next person to move this element should
        // not have to rediscover the rule.
        data-no-open
        className={`shrink-0 ${focusRing}`}
        aria-label="Select every row listed"
        title="Select every row this filter lists, drawn or not. The bar says how many that is."
        checked={selection.allPicked}
        ref={(box) => {
          // The third state: some rows picked, not all. `indeterminate` is a DOM
          // property with no attribute behind it, so it cannot be written in JSX.
          // no attribute behind it, so it cannot be written in JSX.
          if (box !== null) {
            box.indeterminate = selection.somePicked && !selection.allPicked;
          }
        }}
        onChange={(event) => {
          selection.setAll(event.target.checked);
        }}
      />
    );

  /**
   * What this column is drawn at right now, which is what a resize starts from.
   *
   * **Off the element, not off the colgroup.** A table nobody has sized yet is
   * stretched to its panel (`min-width: 100%`), and a fixed-layout table shares
   * that surplus back out over its columns — so the number in the `<col>` and
   * the number on screen differ: in a 1290px panel a Name column declaring
   * 203px is drawn at 345.9px.
   *
   * Reading the colgroup would start both paths from somewhere the operator is
   * not looking at: one press of ArrowRight — the key that widens — would take
   * Name from 345.9px to 219px, because 203 + 16 is less than 345.9.
   *
   * `getBoundingClientRect` and not `offsetWidth`, for the fractional pixel: the
   * widths that come back out of a stretch are not integers, and rounding each
   * gesture's start is how a column creeps on repeated drags. Falls back to the
   * colgroup where there is no element yet, which is every render before the
   * first paint.
   */
  const current = (): number => {
    const held = dragWidth[column.key];
    if (held !== undefined) return held;
    const box = headRef.current?.getBoundingClientRect().width;
    return box === undefined || box === 0 ? widthOf(column, rows) : box;
  };

  /** The columns in the order they are drawn, which is the order the absorption
   *  is decided in: what follows the grip gives, what precedes it stays. */
  const order = columns.map((each) => each.key);

  /**
   * The whole arrangement this gesture decides, the dragged one first.
   *
   * **Every column, every time** — `DataGridProps.onResize` carries the reason.
   * `drawn` is the set `absorb` produced from the freeze, which is where every
   * column *is* on screen; posting it is what makes the shares on disk the
   * arrangement the operator is looking at.
   *
   * The dragged column first, so a caller that posts sequentially and is refused
   * partway has at least applied the one the operator asked for.
   *
   * A column with no entry in `drawn` is left out rather than sent as `null`:
   * that is a column with no element to measure — every render before the first
   * paint, and every test in jsdom, which lays nothing out — and `null` there
   * would post "automatic" for a column nobody touched.
   */
  const sizes = (drawn: Readonly<Record<string, number>>): { key: string; width: number | null }[] => {
    const all = postable(order, drawn);
    return [...all.filter((each) => each.key === column.key), ...all.filter((each) => each.key !== column.key)];
  };

  /** Commit, but only if the gesture changed anything. Trap 1. */
  const commit = (): void => {
    const held = sizingRef.current;
    const reached = dragWidth[column.key];
    // Read before the freeze is dropped below: it is the arrangement on screen —
    // the dragged column and everything that absorbed it — and after
    // `setDragWidth(() => ({}))` there is nowhere else to get it, since the
    // headings are re-laid out by the same release.
    const frozen = dragWidth;
    sizingRef.current = null;
    setResizing(false);
    /*
      The whole freeze goes, and **before the refusals below rather than after
      them**. `startResize` puts every column in `dragWidth`, so an early return
      that skipped this would leave the table pinned to the arrangement it
      happened to have when the gesture began, with `resizing` already false —
      a colgroup holding widths for a drag that is over, in a table that is
      stretching them again. The old shape deleted one key and could afford to
      return first; this one cannot, and that is the cost of freezing the set.
    */
    setDragWidth(() => ({}));
    if (held === null || held.key !== column.key || onResize === undefined) return;
    const drawn = clampWidth(reached ?? held.startWidth);
    // The whole set as it stands, and not the dragged number written back into
    // the freeze: what the other columns gave up is as much a part of this
    // gesture's answer as what the pointer moved.
    if (drawn !== clampWidth(held.startWidth)) onResize(sizes({ ...frozen, [column.key]: drawn }));
  };

  /**
   * Every column's drawn width, read off the headings in one pass.
   *
   * **The freeze is what makes the column stop under the pointer**, and it is the
   * gesture's whole mechanism rather than an optimization. A table nobody has
   * sized is stretched to its panel, and a fixed table shares that surplus back
   * out over its columns — so writing one number into the colgroup mid-drag has
   * out over its columns — so writing one number into the colgroup mid-drag has
   * it re-inflated by whatever the surplus then is: without the freeze, pressing
   * the Name grip and not moving at all took the column from 345.9px to 495.9px
   * in a 1290px panel.
   *
   * With every column frozen the sum *is* the table's current width, so the
   * stretch factor is exactly 1, the press moves nothing, and one pixel of
   * travel is one pixel of column. `absorb` keeps the sum where the freeze put
   * it for the whole drag: the table fills its panel, so the arrangement can
   * only be a redistribution.
   *
   * Read by position, because the headings are rendered from `columns` in order
   * and there is exactly one `<th>` per column — the checkboxes are not a
   * column, so a resize never lands one column to the side.
   */
  const freeze = (): Record<string, number> => {
    const heads = headRef.current?.closest('table')?.querySelectorAll('thead th');
    const widths: Record<string, number> = {};
    if (heads === undefined) return widths;
    columns.forEach((each, at) => {
      const box = heads[at]?.getBoundingClientRect().width;
      if (box !== undefined && box > 0) widths[each.key] = box;
    });
    return widths;
  };

  const startResize = (event: ReactPointerEvent<HTMLButtonElement>): void => {
    if (event.button !== 0) return;
    // Keeps the press from selecting the heading text as the pointer travels.
    // It is not what stops the reorder drag — trap 3.
    event.preventDefault();
    const held = freeze();
    sizingRef.current = {
      key: column.key,
      startX: event.clientX,
      startWidth: held[column.key] ?? current(),
      // Kept for the whole gesture, so every move redistributes from where the
      // table was when the pointer went down rather than from the previous
      // move's answer. Absorbing from the answer compounds the rounding, and a
      // drag out and back would not return the columns to where they started.
      frozen: held,
    };
    setDragWidth(() => held);
    setResizing(true);
    try {
      event.currentTarget.setPointerCapture(event.pointerId);
    } catch {
      // Not every browser, and the drag still works without it.
    }
  };

  const moveResize = (event: ReactPointerEvent<HTMLButtonElement>): void => {
    const held = sizingRef.current;
    if (held === null || held.key !== column.key) return;
    const width = clampWidth(held.startWidth + (event.clientX - held.startX));
    // The whole arrangement, not the one column: the table is filling its panel
    // and it has to go on filling it while the pointer moves, so what this
    // column takes the ones after it give.
    setDragWidth(() => absorb(order, held.frozen, column.key, width));
  };

  /** One arrow press. Shift is the fine step, Alt moves the column instead. */
  const onKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>): void => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
    const back = event.key === 'ArrowLeft';

    if (event.altKey) {
      if (onPlace === undefined) return;
      const at = columns.indexOf(column);
      const neighbour = columns[back ? at - 1 : at + 1];
      if (neighbour === undefined) return;
      event.preventDefault();
      // Expressed as its new neighbor and a side, like every other drop.
      onPlace(column.key, neighbour.key, back ? 'before' : 'after');
      return;
    }

    if (onResize === undefined) return;
    event.preventDefault();
    // `freeze()` and not `dragWidth`: an arrow press is not a gesture, so there
    // is nothing frozen — and it redistributes exactly as a drag does, because
    // the table has to keep filling its panel however the width was asked for.
    onResize(
      sizes(
        absorb(
          order,
          freeze(),
          column.key,
          clampWidth(current() + (back ? -STEP : STEP) * (event.shiftKey ? FINE / STEP : 1)),
        ),
      ),
    );
  };

  const grip = !resizable ? null : (
    <button
      type="button"
      // Off: a control inside a draggable heading must not become the drag
      // source itself.
      draggable={false}
      data-grip=""
      aria-label={`Resize the ${column.label} column`}
      title={`Drag to resize ${column.label}. Drag the heading to move it. Arrow keys resize; Shift for a finer step; Alt with them moves the column.`}
      onPointerDown={startResize}
      onPointerMove={moveResize}
      onPointerUp={commit}
      // A pointer that is canceled must still commit what is on screen. Trap 2.
      onPointerCancel={commit}
      onKeyDown={onKeyDown}
      // Only the focus ring marks a focused grip; it has no opacity to raise.
      className={`ml-auto h-4 w-1.5 shrink-0 cursor-col-resize rounded-full bg-line ${focusRingTight}`}
    />
  );

  /*
    Where a dragged heading would land. A class rather than a moved element: this
    file never reorders the DOM, and the marker is drawn on the heading the
    pointer is over.
  */
  const marker =
    dropping !== null && dropping.key === column.key
      ? dropping.edge === 'before'
        ? ' border-l-2 border-l-accent'
        : ' border-r-2 border-r-accent'
      : '';

  const dragProps = !movable
    ? {}
    : {
        draggable: !resizing,
        onDragStart: (event: React.DragEvent<HTMLTableCellElement>) => {
          if (sizingRef.current !== null) return;
          draggedRef.current = column.key;
          event.dataTransfer.effectAllowed = 'move';
          // Firefox starts no drag at all without data on the transfer, and the
          // label is the only sensible thing to leave anywhere else it is dropped.
          event.dataTransfer.setData('text/plain', column.label);
        },
        onDragOver: (event: React.DragEvent<HTMLTableCellElement>) => {
          const from = draggedRef.current;
          if (from === null || from === column.key) return;
          event.preventDefault();
          const box = event.currentTarget.getBoundingClientRect();
          const edge: 'before' | 'after' =
            event.clientX < box.left + box.width / 2 ? 'before' : 'after';
          if (dropping === null || dropping.key !== column.key || dropping.edge !== edge) {
            setDropping({ key: column.key, edge });
          }
        },
        onDragLeave: () => {
          if (dropping !== null && dropping.key === column.key) setDropping(null);
        },
        onDrop: (event: React.DragEvent<HTMLTableCellElement>) => {
          event.preventDefault();
          const from = draggedRef.current;
          const where = dropping;
          draggedRef.current = null;
          setDropping(null);
          if (from === null || from === column.key || where === null || onPlace === undefined) return;
          onPlace(from, column.key, where.edge);
        },
        onDragEnd: () => {
          draggedRef.current = null;
          setDropping(null);
        },
      };

  const label = (
    <span
      data-label=""
      className="min-w-0 truncate"
      {...(column.headTitle === undefined ? {} : { title: column.headTitle })}
    >
      {column.label}
    </span>
  );

  // The heading's whole label where its column cuts it - `RE…` over Ready
  // and Restarts both - unless it already carries a sentence.
  const whole = column.headTitle === undefined ? { onPointerEnter: offerWhole(() => column.label) } : {};

  if (!sortable) {
    return (
      <th
        ref={headRef}
        scope="col"
        className={`${HEAD}${align}${marker}${pinned ? pinHead(sticky) : sticky ? STICKY : ''}`}
        {...(pinned ? { 'data-held': '' } : {})}
        {...whole}
        {...dragProps}
      >
        <span className={headBox(column)}>
          {all}
          {label}
          {grip}
        </span>
      </th>
    );
  }

  // `aria-sort` on the header and not a glyph alone: the arrow is what a sighted
  // operator reads and is invisible to everything else.
  const ariaSort = active ? (sort.direction === 'asc' ? 'ascending' : 'descending') : 'none';

  return (
    <th
      ref={headRef}
      scope="col"
      aria-sort={ariaSort}
      className={`${HEAD}${align}${marker}${pinned ? pinHead(sticky) : sticky ? STICKY : ''}`}
      {...(pinned ? { 'data-held': '' } : {})}
      {...whole}
      {...dragProps}
    >
      {/* The box sits beside the sort control and not inside it: a click meant
          for the tick must not also reorder the table. The grip is a third
          sibling for the same reason: a press on it must not sort. */}
      <span className={headBox(column)}>
        {all}
        <button
          type="button"
          draggable={false}
          onClick={() => {
            onSort(column.key);
          }}
          // `+3` and not `+2`: the control is inline inside a padded `<th>`,
          // and at `+2` its ring sits closer to the heading beside it than to
          // its own column.
          className={`inline-flex min-w-0 items-center gap-1 uppercase ${active ? 'text-fg' : 'text-muted'} ${focusRingClear}`}
        >
          {label}
          <span aria-hidden="true" className="text-2xs">
            {active ? (sort.direction === 'asc' ? '↑' : '↓') : '·'}
          </span>
        </button>
        {grip}
      </span>
    </th>
  );
}
