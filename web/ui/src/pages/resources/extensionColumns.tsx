import { useQueries } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';

import { useExtensions, useNav, type ExtensionInfo, type ListResult, type Row } from '../../api';
import { Pill } from '../../components/Pill';
import type { GridColumn } from '../../components/DataGrid';
import { focusRingTight } from '../../focus';
import type { Extension, ExtensionCell } from '../../extension';
import { useShellIfPresent } from '../../shell/ShellContext';
import { enabledExtensions } from '../object/extensionTabs';
import { useExtraColumnsShown } from './extraColumnMemory';

/**
 * The columns extensions add to the console's lists: offered on the kinds each
 * one names, read once per list from the extension's own route, drawn only while
 * the extension is on for the cluster - the menu's Extra group, as for tabs -
 * and only when some row on the page has an answer. A row with no answer is an empty cell, never an error; a
 * column whose read failed is not drawn at all.
 *
 * **A column gated on a setting** (`ExtensionColumn.setting`) is drawn only once the cluster's extension listing says that setting is true
 * there. Not while the listing is in flight - a column that appears and is then
 * taken away is worse than one that arrives a moment late - and, where the
 * listing failed or does not name the setting, as the column's declared
 * default says (`ExtensionColumn.settingDefault`, false when absent), so "could not tell"
 * draws what the default draws.
 *
 * **Every other column is hidden until ticked** under its extra in Settings >
 * Extra, per browser (`extraColumnMemory.ts`).
 */
export interface ContributedColumn {
  /** The extension that adds it. */
  extension: string;
  /** `x.<extension>.<column>`, the grid's key, apart from the server's own. */
  id: string;
  label: string;
  cells: Readonly<Record<string, ExtensionCell>>;
}

/** The answer's key for a row: `namespace/name`, or the name alone on a
 *  cluster-scoped kind. */
export function cellKey(namespace: string | undefined, name: string): string {
  return namespace === undefined || namespace === '' ? name : `${namespace}/${name}`;
}

/**
 * Whether a column gated on `setting` of `extension` is wanted on this cluster:
 * `undefined` while the listing is in flight, then the setting's value - or the
 * declared `fallback` (the column's `settingDefault`) where the listing failed
 * or does not name it.
 */
export function settingWanted(
  listing: ListResult<ExtensionInfo> | undefined,
  failed: boolean,
  extension: string,
  setting: string,
  fallback = false,
): boolean | undefined {
  if (failed) return fallback;
  if (listing === undefined) return undefined;
  if (listing.state !== 'ok') return fallback;
  const row = listing.items.find((one) => one.key === extension);
  // `settings` is absent from a server older than the member, which reads as none.
  const found = (row?.settings as ExtensionInfo['settings'] | undefined)?.find((one) => one.key === setting);
  return found === undefined ? fallback : found.value;
}

function offered(extensions: readonly Extension[], kind: string): { extension: string; column: NonNullable<Extension['columns']>[number] }[] {
  const wanted = kind.toLowerCase();
  const out: { extension: string; column: NonNullable<Extension['columns']>[number] }[] = [];
  for (const extension of extensions) {
    for (const column of extension.columns ?? []) {
      if (column.kinds.some((one) => one === '*' || one.toLowerCase() === wanted)) out.push({ extension: extension.key, column });
    }
  }
  return out;
}

export function useExtensionColumns(
  cluster: string | undefined,
  kind: string | undefined,
): ContributedColumn[] & { pending?: boolean } {
  const shell = useShellIfPresent();
  const candidates = cluster === undefined || kind === undefined ? [] : offered(shell?.extensions ?? [], kind);
  const nav = useNav(cluster, candidates.length > 0);
  const on = enabledExtensions(nav.data);
  const shownByChoice = useExtraColumnsShown();
  const gated = candidates.some((one) => one.column.setting !== undefined);
  const listing = useExtensions(cluster, gated);
  const live = candidates.filter(
    (one) =>
      on.has(one.extension) &&
      (one.column.setting === undefined
        ? shownByChoice(one.extension, one.column.key)
        : settingWanted(listing.data, listing.isError, one.extension, one.column.setting, one.column.settingDefault ?? false) ===
          true),
  );
  const answers = useQueries({
    queries: live.map((one) => ({
      queryKey: ['extension-column', one.extension, one.column.key, cluster ?? '', kind ?? ''] as const,
      queryFn: ({ signal }: { signal: AbortSignal }) => one.column.cells({ cluster: cluster ?? '', kind: kind ?? '' }, signal),
      retry: false,
      // The cells are one live read of the cluster; without an interval a
      // column read once stays as it was while the list beside it moves.
      staleTime: 10_000,
      refetchInterval: 15_000,
    })),
  });
  const out: ContributedColumn[] & { pending?: boolean } = [];
  live.forEach((one, i) => {
    const data = answers[i]?.data;
    if (data !== undefined) {
      out.push({ extension: one.extension, id: `x.${one.extension}.${one.column.key}`, label: one.column.label, cells: data });
    }
  });
  // Not settled: the menu has not said which extras are on, or a column is
  // still being read. A filter on such a column is not dropped meanwhile.
  out.pending = (candidates.length > 0 && nav.isPending) || answers.some((one) => one.isPending);
  return out;
}

/** The grid's columns for what the extensions answered, each drawn only when a
 *  row on this page has an answer. */
export function extensionGridColumns(columns: readonly ContributedColumn[], rows: readonly Row[]): GridColumn<Row>[] {
  return columns
    .filter((c) => rows.some((row) => c.cells[cellKey(row.namespace, row.name ?? '')] !== undefined))
    .map((c) => ({
      key: c.id,
      label: c.label,
      sortable: false,
      measure: (row: Row) => c.cells[cellKey(row.namespace, row.name ?? '')]?.text ?? '',
      cell: (row: Row): ReactNode => <ExtensionCellView cell={c.cells[cellKey(row.namespace, row.name ?? '')]} />,
    }));
}

function ExtensionCellView({ cell }: { cell: ExtensionCell | undefined }): ReactNode {
  if (cell === undefined) return null;
  const pill = (
    <Pill tone={cell.tone} neutral="muted" title={cell.hint}>
      {cell.text}
    </Pill>
  );
  if (cell.href === undefined) return pill;
  return (
    <Link to={cell.href} className={focusRingTight} onClick={(e) => e.stopPropagation()}>
      {pill}
    </Link>
  );
}
