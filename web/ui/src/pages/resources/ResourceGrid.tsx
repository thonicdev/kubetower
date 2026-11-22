import type { ReactNode } from 'react';

import type { Cell, Column, ListPage, Row } from '../../api';
import { arrange } from '../../components/columns';
import { DataGrid, type GridColumn, type GridSort, type RowMenuAnchor } from '../../components/DataGrid';
import type { useSelection } from '../../components/useSelection';
import { extensionGridColumns, type ContributedColumn } from './extensionColumns';
import { withStatusFloor } from './statusFloor';
import { orderRows, type Direction } from './ordering';
import { ResourceCell } from './ResourceCell';

/** Ascending, then descending, then unsorted. The third click is not a
 *  flourish: without it there is no way back to the order the server sent, and
 *  that order is the one that carries the server's own opinion of importance. */
export function nextSort(current: GridSort | undefined, key: string): GridSort | undefined {
  if (current === undefined || current.key !== key) return { key, direction: 'asc' };
  if (current.direction === 'asc') return { key, direction: 'desc' };
  return undefined;
}

/**
 * What a column's late values cost, in pixels, from its declaration alone.
 *
 * **A width must never outlive the answer it was computed from.** Measuring a
 * cell is fine (`defaultWidths` measures); what is not is letting a width
 * computed from one answer stand for the next. Its declarative half is that **a
 * column that will hold a gauge reserves the bar and floors the reading whether
 * or not this render has one.** Without that, `GridColumn.reserve` goes unset
 * and the nodes list repeats the defect: usage lookups do not block, so CPU,
 * Memory and Disk first paint as three em dashes, three columns are sized for an
 * em dash, and the readings arrive two seconds later overflowing their own
 * column.
 *
 * The figures are what `ResourceCell` actually draws, and they are deliberately
 * coarse — the point is that they are a function of the **declaration** and not
 * of this second's cells:
 *
 * - a gauge is a track and its percentage, side by side
 *   (`components/Reading.tsx`: the percentage in a fixed
 *   `w-10` box at the bar's end, so the bar is the column less a constant);
 * - a copy control is a bordered button of about 22px, plus its own gap -
 *   added to the text rather than reserved under it, because it is drawn beside
 *   the value (`padFor`, below).
 *
 * A column that is neither gets nothing, and `DataGrid` then sizes it from its
 * heading and its cells, the wider of the two.
 *
 * **The gauge's figure is the span the line is drawn across**, not a sum of a
 * track, a gap and a reading. Reducing it to `max(track, reading)` would be the
 * tidy edit and would halve every usage bar in the console. The line is a bar
 * and a 40px percentage, so at the floor the bar keeps 80px of the 126.
 */
// The bar at the column's floor, and the percentage's fixed `w-10` box beside
// it (`components/Reading.tsx` part 4). 80 + 6 + 40 is the 126 reserved.
const GAUGE_TRACK = 80;
const GAUGE_PERCENT = 40;
const GAP = 6;
const COPY_CONTROL = 22;

/** How long a usage bar is drawn, and therefore how wide its column must be. */
const GAUGE_SPAN = GAUGE_TRACK + GAP + GAUGE_PERCENT;

function reserveFor(column: Column): number {
  return column.gauged ? GAUGE_SPAN : 0;
}

/**
 * What a pill draws around its text: `Pill`'s `px-2` either side and its
 * border, 18px.
 */
const PILL_CHROME = 18;

/**
 * And what its text costs beyond the grid's `CH`, **per character**: a pill is
 * `font-semibold`, and `CH` is a regular character. Proportional because the
 * overshoot is: a fixed 8px is not enough for `CreateContainerConfigError` (26
 * characters) even if it clears `Running` (7). A pixel a character covers the
 * 0.7px the layout runner's font was measured to need, with room - limit 4 in
 * `measure/main.tsx`: text advance is the runner's font, so this is a margin and
 * not a measurement of every font.
 */
const PILL_PER_CHARACTER = 1;

/**
 * What a cell draws **around or beside** its text, added to the text's width
 * (`GridColumn.pad`, `GridColumn.perCharacter`)
 * rather than floored under it.
 *
 * **The copy control is not a floor, because a floor of 28 is nothing**: every
 * column is floored at 64 already, so the control beside a Nodes IP would be
 * reserved by nobody and `192.168….` is what would be read at 1024. It
 * sits beside the value - `ResourceCell` draws them side by side - so it adds.
 * The pill is the same shape: drawn wherever the column is graded, around the
 * text, so a column of `Warning` sized for the word clipped every one of them.
 * The namespace arm is never a pill (`ResourceCell` says why), so it adds none.
 */
function padFor(column: Column, graded: boolean): { pad?: number; perCharacter?: number } {
  const pill = graded && column.role !== 'namespace';
  const pad = (pill ? PILL_CHROME : 0) + (column.copyable ? COPY_CONTROL + GAP : 0);
  return { ...(pad === 0 ? {} : { pad }), ...(pill ? { perCharacter: PILL_PER_CHARACTER } : {}) };
}

/**
 * What hovering a cell says — two different hovers, in one function: the cell's
 * hint if it has one, else its own text when that is long.
 *
 * **`Cell.hint` is for a cell that states a number the reader then has to go and
 * look up.** The node table's taint column is the one that prompted it: a column
 * wide enough for two taints pushes CPU and Memory off the screen, so the cell
 * says `2` and the hover says which two. Without it *the column
 * degrades to a bare number with no way to learn what it counts, and nothing
 * fails*. It is not a rare member either: every gauged cell
 * carries one, and for a gauge it is the cell's quantity itself,
 * `used / total` (`resources.Fraction`, "1.2 / 8 cores allocatable"),
 * because the cell draws only the bar and its percentage.
 *
 * **The 28-character arm is the fallback for a clipped value**, and it is the
 * half that CSS does not answer. `theme.css`
 * clips an overlong cell with an ellipsis rather than painting it over the
 * column beside it; this is what makes the clipped half recoverable instead of
 * merely tidy. Twenty-eight is a threshold for *offering* a hover, never a truncation:
 * nothing here shortens what is drawn.
 *
 * `else if` and not both, because a cell with a hint has something better to say
 * than its own text repeated back.
 */
const HOVER_FROM = 28;

function titleOf(cell: Cell | undefined): string | undefined {
  if (cell === undefined) return undefined;
  if (cell.hint !== undefined && cell.hint !== '') return cell.hint;
  return cell.text.length > HOVER_FROM ? cell.text : undefined;
}

/** The listed rows as a table: the server's columns sized, graded and
 *  fitted to the panel, then the extensions' columns after them. */
export function Grid({
  page,
  compact,
  onResize,
  onPlace,
  sort,
  onSort,
  selection,
  empty,
  selected,
  onPickNamespace,
  rowAction,
  rowMenu,
  rowMenuOpen,
  contributed = [],
}: {
  page: ListPage;
  /** The columns extensions add to this list, after the
   *  server's own. */
  contributed?: readonly ContributedColumn[];
  compact: boolean;
  /** Where the gesture is kept - the console's store, or this browser's where
   *  the console keeps none (`columnMemory`). Absent is the grid's own "not
   *  resizable, not draggable". */
  onResize?: ((sizes: readonly { key: string; width: number | null }[]) => void) | undefined;
  onPlace?: ((key: string, target: string, edge: 'before' | 'after') => void) | undefined;
  /** The namespaces this list is already narrowed to, so a cell that would ask
   *  for what is already on screen is drawn as text. */
  selected: readonly string[];
  sort: GridSort | undefined;
  onSort: (key: string) => void;
  /** Absent where the kind offers no bulk verb, which is what withholds the
   *  checkboxes from a table where ticking a row would lead nowhere. */
  selection?: ReturnType<typeof useSelection> | undefined;
  empty: ReactNode;
  onPickNamespace: (namespace: string) => void;
  rowAction: (row: Row) => (() => void) | undefined;
  rowMenu: (row: Row) => ((anchor: RowMenuAnchor) => void) | undefined;
  rowMenuOpen: string | undefined;
}): ReactNode {
  const usage = usageNote(page);
  const columns: GridColumn<Row>[] = arrange(page.columns).map((column: Column) => {
    const reserve = reserveFor(column);
    /*
      Does anything in this column carry a grade?

      An ungraded value rendering as bare text beside graded ones reads as a
      rendering fault. That is a rule about a **column**, and a cell cannot see
      its neighbors. So the question is answered here, once per column per
      render, and handed down.

      Over the page the browser is holding, which is the set the user is
      looking at: a Restarts column is graded on this page or it is not, and
      asking the server would be a second opinion about a question the rows in
      front of us already answer. O(rows) per column, on a render that is already
      mapping every row.
    */
    const gradedColumn = page.rows.some((row: Row) => {
      const tone = row.cells[column.key]?.tone;
      return tone === 'ok' || tone === 'warn' || tone === 'bad';
    });
    return {
      key: column.key,
      label: column.label,
      // `width` and `sortable` are sent on every column; `align` is not, and
      // spreading `undefined` into an exact-optional property is a type error
      // rather than a shrug, which is what keeps this honest.
      ...(column.align === undefined ? {} : { align: column.align }),
      ...(column.width === undefined ? {} : { width: column.width }),
      sortable: column.sortable,
      /*
        The tick travels with the **name**, and with the declared name rather
        than the leftmost column: the checkbox is in the name cell, so that
        dragging Name to the far right takes the checkboxes with it. The tick
        belongs to the object, not to a position.
      */
      ...(column.role === 'name' ? { leads: true } : {}),
      // Who gives up width where the columns need more than the panel: the
      // name and the namespace first, then any other free text. A number, a
      // gauge or a graded pill keeps its width.
      ...(column.role === 'name' || column.role === 'namespace'
        ? { yields: 1 as const }
        : column.role === 'other' && column.align !== 'end' && reserve === 0 && !gradedColumn
          ? { yields: 2 as const }
          : {}),

      // What the column holds, for sizing. The default width arbitrates between
      // a heading and the values under it; a column measured by its heading
      // alone gives the widest pixels of the table to its narrowest data.
      measure: (row: Row) => row.cells[column.key]?.text ?? '',
      // The cell's own hover, or the whole of a value its column will clip.
      title: (row: Row) => titleOf(row.cells[column.key]),
      // **What marks a column as a sentence is a property of the view model,
      // not of its label.** That property is `Column.prose`; re-deciding it
      // here from the heading text is what must not be done.
      ...(column.prose ? { wrap: 'prose' as const } : {}),
      ...(reserve === 0 ? {} : { reserve }),
      ...padFor(column, gradedColumn),
      ...(usage !== undefined && emptyColumn(page, column) ? { headTitle: usage } : {}),
      cell: (row: Row) => {
        /*
          **A namespace that is already the whole selection is not a link.**
          Filtering to what is already the whole of the table changes nothing,
          and a control that cannot change anything is never drawn.

          Decided per cell rather than per column, because it is a fact about
          this row's value against this page's question.
        */
        const cell = row.cells[column.key];
        const inert =
          column.role === 'namespace' &&
          selected.length === 1 &&
          cell !== undefined &&
          selected[0] === cell.text;
        return (
          <ResourceCell
            cell={cell}
            column={column}
            gradedColumn={gradedColumn}
            {...(inert ? {} : { onPickNamespace })}
          />
        );
      },
    };
  });

  columns.push(...extensionGridColumns(contributed, page.rows));

  const rows =
    sort === undefined ? page.rows : orderRows(page.rows, sort.key, sort.direction as Direction);

  // Every column stays drawn however narrow the panel: an unarranged table
  // fits by narrowing the columns that yield (`fitToRoom`), an arranged one
  // fits its shares to the panel. The status column keeps room for
  // `CrashLoopBackOff`. See `statusFloor.ts`.
  const floored = columns.map(withStatusFloor);

  return (
    <DataGrid
      columns={floored}
      rows={rows}
      rowKey={(row) => row.id}
      label={page.label}
      compact={compact}
      onResize={onResize}
      onPlace={onPlace}
      stickyHead
      sort={sort}
      onSort={onSort}
      {...(selection === undefined ? {} : { selection })}
      empty={empty}
      rowAction={rowAction}
      rowMenu={rowMenu}
      rowMenuOpen={rowMenuOpen}
    />
  );
}

/**
 * Why this table's usage columns are empty, on a kind that has them, or
 * `undefined` - the server's `measurementNote`, read only where
 * `measurementState` says which of its two answers it is.
 *
 * ## It is a heading's hover, not a line above the table
 *
 * A line above every list whose usage columns were empty would be redundant:
 * the dashes already say there is no figure. The difference between an empty
 * answer and a failed one is kept one hover away instead: the sentence is the
 * `title` of each empty usage heading, and a `pending` read says it fills in
 * when that read returns while an `unmeasured` one says there are no
 * measurements.
 */
function usageNote(page: ListPage): string | undefined {
  const note = page.measurementNote;
  return page.measurementState === undefined || note === undefined || note === '' ? undefined : note;
}

/**
 * Whether every row of this column is the em dash - the shape the usage
 * columns take when `measurementState` is set. Read off the cells because the
 * contract marks no column as a usage column, and recognizing one by its title
 * is not done; the name column is never one. A table with no
 * rows has nothing empty to explain.
 */
function emptyColumn(page: ListPage, column: Column): boolean {
  if (column.role === 'name' || page.rows.length === 0) return false;
  return page.rows.every((row: Row) => {
    const text = row.cells[column.key]?.text;
    return text === undefined || text === '' || text === '—';
  });
}
