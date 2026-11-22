import type { Column, ListPage } from '../../api';
import { cellKey, type ContributedColumn } from './extensionColumns';
import { columnOf, parseChosen } from './listFilters';

/** One filter in effect, as a chip beside the box. */
export interface FilterChip {
  /** The address's own spelling of the choice, which removing the chip drops. */
  parameter: string;
  column: string;
  value: string;
}

/* What the filter control offers, and the chips: both from the columns the
   server marks filterable, and the chips from the address so a tick shows at
   once rather than when the cluster answers. */

/**
 * The columns the filter control offers, each with the values ticked in it:
 * **every column on screen and no hidden one**. The server's filterable columns
 * that are shown (`shown`, the arrangement as drawn, where it is known), then
 * the columns the extras add, whose values come from their cells on this page.
 */
export function filterColumnsOf(
  page: ListPage | undefined,
  chosen: readonly string[],
  shown?: readonly Pick<Column, 'key' | 'visible'>[],
  contributed: readonly ContributedColumn[] = [],
) {
  const visible = new Map((shown ?? page?.columns ?? []).map((column) => [column.key, column.visible]));
  const ticked = (label: string) =>
    chosen.flatMap((one) => {
      const parsed = parseChosen(one);
      return parsed !== undefined && parsed.column.toLowerCase() === label.toLowerCase() ? [parsed.value] : [];
    });
  const own = (page?.columns ?? [])
    .filter((column) => column.filterable && visible.get(column.key) !== false)
    .map((column) => ({
      label: column.label,
      values: column.filterValues ?? [],
      chosen: chosen.flatMap((one) => {
        const parsed = parseChosen(one);
        return parsed !== undefined && columnOf([column], parsed) !== undefined ? [parsed.value] : [];
      }),
    }));
  const rows = page?.rows ?? [];
  const added = contributed
    .filter((column) => rows.some((row) => column.cells[cellKey(row.namespace, row.name ?? '')] !== undefined))
    .map((column) => ({
      label: column.label,
      values: [
        ...new Set(rows.flatMap((row) => column.cells[cellKey(row.namespace, row.name ?? '')]?.text ?? [])),
      ].sort((a, b) => a.localeCompare(b)),
      chosen: ticked(column.label),
    }));
  return [...own, ...added];
}

/**
 * The rows an extension column's filters leave: values of one column are
 * OR-ed, columns AND-ed, as the server does for its own. A filter naming no
 * extension column narrows nothing here (the server narrowed it, or dropped it).
 */
export function narrowByExtensionColumns<T extends { namespace?: string | undefined; name?: string | undefined }>(
  rows: readonly T[],
  chosen: readonly string[],
  contributed: readonly ContributedColumn[],
): readonly T[] {
  const wanted = new Map<ContributedColumn, Set<string>>();
  for (const one of chosen) {
    const parsed = parseChosen(one);
    if (parsed === undefined) continue;
    const column = contributed.find((each) => each.label.toLowerCase() === parsed.column.toLowerCase());
    if (column === undefined) continue;
    const values = wanted.get(column) ?? new Set<string>();
    values.add(parsed.value.toLowerCase());
    wanted.set(column, values);
  }
  if (wanted.size === 0) return rows;
  return rows.filter((row) =>
    [...wanted].every(([column, values]) => values.has((column.cells[cellKey(row.namespace, row.name ?? '')]?.text ?? '').toLowerCase())),
  );
}

/** The chips, read from the address: a column the page does not know yet
 *  keeps the name the address gave it. */
export function chipsOf(page: ListPage | undefined, chosen: readonly string[]): FilterChip[] {
  return chosen.flatMap((one) => {
    const parsed = parseChosen(one);
    if (parsed === undefined) return [];
    const column = page === undefined ? undefined : columnOf(page.columns, parsed);
    return [{ parameter: one, column: column?.label ?? parsed.column, value: parsed.value }];
  });
}
