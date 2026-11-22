import { columnWidth, type GridColumn } from '../../components/DataGrid';

/**
 * The status column's floor: wide enough for `CrashLoopBackOff` in full
 * (answer 1), the longest of the ordinary pod statuses. The pill still
 * truncates as a last resort (`Pill.tsx`), with the word on its `title`.
 *
 * **No column gives way for it, and it gives way last.** Every column the
 * operator has stays drawn however narrow the panel - a 900px window, an open
 * drawer, a long status - and the table never scrolls sideways: a table nobody
 * has arranged narrows its other columns first (`DataGrid`'s `fitToRoom`,
 * where this column `holds`); an arranged one (`.sized`) keeps its shares and
 * fits its panel, so there every column is narrower rather than any of them
 * gone (`theme.css`, the sized table's rule).
 * Restarts, CPU and Age used to drop off one by one, and a column that
 * vanished on its own read as a bug.
 */
export const STATUS_FLOOR_TEXT = 'CrashLoopBackOff';

/**
 * A column's key without its position: the server keys a list's columns as
 * the label and the index (`status-3`, `columnKey` in `internal/web`), and
 * the index moves with the kind's descriptor, so the status column is named
 * by the label alone.
 */
function bareKey(key: string): string {
  return key.replace(/-\d+$/, '');
}

/** The status column with its floor, any other column as it is. */
export function withStatusFloor<T>(column: GridColumn<T>): GridColumn<T> {
  if (bareKey(column.key) !== 'status') return column;
  const floor = columnWidth({ ...column, reserve: 0 }, [STATUS_FLOOR_TEXT]);
  // A held column never yields as free text too.
  const held: GridColumn<T> = { ...column, reserve: Math.max(column.reserve ?? 0, floor), holds: true };
  delete held.yields;
  return held;
}
