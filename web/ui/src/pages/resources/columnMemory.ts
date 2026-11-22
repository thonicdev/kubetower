import type { Column } from '../../api';
import { arrangeAll } from '../../components/columns';

/**
 * What a list's columns arrive as: a resource list's `ListPage`, or the Helm
 * list's answer, which carries the same two members beside its releases.
 */
export interface ArrangedColumns {
  columns: Column[];
  columnsCustomised?: boolean;
}

/**
 * A person's column layout, kept in their own browser - where the console keeps
 * none of its own.
 *
 * ## Why this exists
 *
 * The twelve preference writes are not registered where `capabilities
 * .preferences` is false: under the server profile, one replica or several, and
 * wherever several replicas share one address. On a shared console what the
 * server keeps is the **console's** arrangement, so one person dragging a column
 * narrow narrowed it for everybody who signed in after. The answer is *per browser*: each person's widths, order, hidden columns and
 * density live here, and no request is sent for any of them.
 *
 * Per-identity preferences stored on the server are a feature of their own - they
 * need the session store - and not this. On the desktop nothing here is read or
 * written: `GET /api/preferences` and the list payload carry the arrangement, as
 * they always did.
 *
 * ## The same rules as the server's store, applied to what the server sent
 *
 * `internal/columns` is the reference, and this follows it rather than inventing
 * a second set of semantics for the same gestures:
 *
 * - a width is a share of the table, clamped to 56..720, and `null` (or zero) is
 *   "automatic", which removes it;
 * - a column is placed **beside another**, resolved against the order on screen,
 *   and the keys this table does not have are kept at the end - a CRD may declare
 *   different printer columns on two clusters;
 * - a visibility is stored only where it differs from what the server sent for
 *   that column - the default, since the server reads no stored arrangement where
 *   this module is used - so ticking a column back leaves the memory as it was
 *   found;
 * - Reset forgets this kind's arrangement, and a kind that holds nothing leaves
 *   the memory, which is what takes the reset dot off the table.
 *
 * What the server sent is the base: a column with no remembered order keeps its
 * place after the remembered ones, in the server's order, so a column added by a
 * release is drawn rather than lost.
 *
 * ## Storage may not be there at all
 *
 * The bargain `menuMemory.ts` strikes, unchanged: private windows, blocked site
 * data and a locked-down profile make every read and write throw, and then the
 * layout lasts as long as the tab. That costs the memory and never the table.
 * A store that still reads but refuses writes - a full quota - is held to the
 * same bargain by the `unwritable` latch below.
 *
 * ## Two tabs
 *
 * Every gesture reads storage again before it writes (read, modify, write), so a
 * second tab arranging another table does not wipe the first's with a snapshot
 * taken when it opened; and a `storage` event from another tab drops the cached
 * snapshot, so the table on screen follows. Two tabs dragging the same column at
 * the same instant still race, and the last write wins - the server store's
 * answer too.
 */
const KEY = 'kubetower.columns';

/** The server's bounds, `columns.MinWidth` and `columns.MaxWidth`. */
export const MIN_WIDTH = 56;
export const MAX_WIDTH = 720;

export interface KindMemory {
  /** Keys in the order drawn, ghosts from other tables kept at the end. */
  readonly order?: readonly string[];
  /** Key to share of the table. */
  readonly widths?: Readonly<Record<string, number>>;
  /** Key to hidden: `true` hidden on purpose, `false` shown on purpose. Absent
   *  is the column's own default. */
  readonly hidden?: Readonly<Record<string, boolean>>;
}

export interface ColumnMemory {
  readonly density?: 'comfortable' | 'compact';
  readonly kinds: Readonly<Record<string, KindMemory>>;
}

const NOTHING: ColumnMemory = { kinds: {} };

/** The snapshot, cached for `useSyncExternalStore`, which compares by identity.
 *  `undefined` is "not read from storage yet". */
let snapshot: ColumnMemory | undefined;

/** Whether the last write was refused while storage still reads - a full quota
 *  is the ordinary case. Until a write is accepted again this tab's snapshot is
 *  the arrangement, because storage does not hold it: re-reading storage before
 *  a gesture would snap every earlier gesture of this tab back.
 *  `menuMemory.ts` meets the same case and this is its latch. Cleared by
 *  the next accepted write (`commit`). */
let unwritable = false;

const listeners = new Set<() => void>();

export function columnMemory(): ColumnMemory {
  if (snapshot === undefined) snapshot = readStorage();
  return snapshot;
}

export function subscribeToColumnMemory(listener: () => void): () => void {
  listeners.add(listener);
  if (listeners.size === 1) window.addEventListener('storage', fromAnotherTab);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) window.removeEventListener('storage', fromAnotherTab);
  };
}

/** Another tab wrote: forget the snapshot, and redraw from storage. */
function fromAnotherTab(event: StorageEvent): void {
  if (event.key !== KEY && event.key !== null) return;
  // Storage refused this tab's writes, so it does not hold what this tab
  // arranged; following it would throw that away for the life of the tab.
  if (unwritable) return;
  snapshot = undefined;
  for (const listener of [...listeners]) listener();
}

/**
 * What is stored now, for a gesture about to write. Storage itself where it
 * answers - another tab may have written since this one last read - and this
 * tab's own snapshot where it does not, or where it has refused this tab's
 * writes: either way that snapshot is the only record of what this tab
 * arranged, so a blocked store still keeps the tab's own gestures.
 */
function latest(): ColumnMemory {
  if (unwritable) return columnMemory();
  try {
    const raw = localStorage.getItem(KEY);
    snapshot = raw === null ? NOTHING : parse(JSON.parse(raw));
  } catch {
    // Blocked, or nonsense: keep what this tab already holds.
  }
  return columnMemory();
}

/** One resize gesture: every width the grid says it decided. */
export function rememberWidths(
  kind: string,
  sizes: readonly { key: string; width: number | null }[],
): void {
  const entry = latest().kinds[kind] ?? {};
  const widths: Record<string, number> = { ...entry.widths };
  for (const { key, width } of sizes) {
    const clamped = clampWidth(width);
    if (clamped === undefined) delete widths[key];
    else widths[key] = clamped;
  }
  commitKind(kind, { ...entry, widths });
}

/**
 * Put one column immediately before or after another. `onScreen` is every key
 * of this table in the order it is drawn now, hidden ones included - the order
 * the gesture was made against.
 */
export function rememberPlace(
  kind: string,
  onScreen: readonly string[],
  column: string,
  target: string,
  edge: 'before' | 'after',
): void {
  if (!onScreen.includes(column) || !onScreen.includes(target)) return;
  const entry = latest().kinds[kind] ?? {};
  let order = [...onScreen];
  if (column !== target) {
    order.splice(order.indexOf(column), 1);
    const at = order.indexOf(target) + (edge === 'after' ? 1 : 0);
    order.splice(at, 0, column);
  }
  const live = new Set(onScreen);
  order = [...order, ...(entry.order ?? []).filter((key) => !live.has(key))];
  commitKind(kind, { ...entry, order });
}

/**
 * Show or hide one column. `sent` is whether the server drew it - the default,
 * since nothing stored is read where this is used - and a visibility equal to it
 * is no visibility to remember.
 *
 * It does not compare against `secondary`: where the two differ (a server that
 * sent a secondary column visible), hiding it would compare equal to
 * `secondary`'s "shown" and write nothing.
 */
export function rememberShown(
  kind: string,
  column: string,
  visible: boolean,
  sent: boolean,
): void {
  const entry = latest().kinds[kind] ?? {};
  const hidden: Record<string, boolean> = { ...entry.hidden };
  if (visible !== sent) hidden[column] = !visible;
  else delete hidden[column];
  commitKind(kind, { ...entry, hidden });
}

/** Reset: forget this kind's arrangement. Density is every table's and stays. */
export function forgetColumns(kind: string): void {
  commitKind(kind, {});
}

export function rememberDensity(density: 'comfortable' | 'compact'): void {
  commit({ ...latest(), density });
}

/**
 * The page with this browser's arrangement applied over the server's columns.
 *
 * `columnsCustomised` is this memory's answer, not the server's: here the server
 * keeps nothing a person can undo, so a Reset offered on its word would forget
 * nothing.
 */
export function withRemembered<P extends ArrangedColumns>(page: P, kind: string, memory: ColumnMemory): P {
  const entry = memory.kinds[kind];
  if (entry === undefined) return { ...page, columnsCustomised: false };

  const rank = new Map<string, number>();
  if (entry.order !== undefined && entry.order.length > 0) {
    const present = new Set(page.columns.map((column) => column.key));
    for (const key of entry.order) {
      if (present.has(key) && !rank.has(key)) rank.set(key, rank.size);
    }
    for (const column of arrangeAll(page.columns)) {
      if (!rank.has(column.key)) rank.set(column.key, rank.size);
    }
  }

  return {
    ...page,
    columnsCustomised: true,
    columns: page.columns.map((column) => {
      const hidden = entry.hidden?.[column.key];
      const width = entry.widths?.[column.key];
      return {
        ...column,
        // A column the server says cannot be hidden is drawn whatever was
        // remembered against it.
        visible: hidden === undefined || column.lockedNote !== undefined ? column.visible : !hidden,
        ...(width === undefined ? {} : { width }),
        order: rank.get(column.key) ?? column.order,
      };
    }),
  };
}

function clampWidth(width: number | null): number | undefined {
  if (width === null || !Number.isFinite(width) || width <= 0) return undefined;
  return Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, Math.round(width)));
}

function empty(entry: KindMemory): boolean {
  return (
    (entry.order?.length ?? 0) === 0 &&
    Object.keys(entry.widths ?? {}).length === 0 &&
    Object.keys(entry.hidden ?? {}).length === 0
  );
}

function commitKind(kind: string, entry: KindMemory): void {
  const current = latest();
  const kinds: Record<string, KindMemory> = { ...current.kinds };
  if (empty(entry)) delete kinds[kind];
  else kinds[kind] = entry;
  commit({ ...current, kinds });
}

function commit(next: ColumnMemory): void {
  snapshot = next;
  try {
    localStorage.setItem(KEY, JSON.stringify(next));
    // Storage holds exactly this tab's arrangement again, so it is the record:
    // re-reading it before a gesture, and following other tabs, are right once
    // more. A latch that never cleared would have this tab write its stale
    // layout over every other tab's for the rest of its life.
    unwritable = false;
  } catch {
    // Storage refused: the layout lasts as long as the tab. See the header.
    unwritable = true;
  }
  for (const listener of [...listeners]) listener();
}

function readStorage(): ColumnMemory {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw === null) return NOTHING;
    return parse(JSON.parse(raw));
  } catch {
    return NOTHING;
  }
}

/** Whatever is in storage, reduced to what this module could have written.
 *  Anything else - a hand edit, an older shape - is dropped, never thrown. */
function parse(value: unknown): ColumnMemory {
  if (!isRecord(value)) return NOTHING;
  const kinds: Record<string, KindMemory> = {};
  if (isRecord(value.kinds)) {
    for (const [kind, raw] of Object.entries(value.kinds)) {
      if (!isRecord(raw)) continue;
      const entry: KindMemory = {
        ...(Array.isArray(raw.order)
          ? { order: raw.order.filter((key): key is string => typeof key === 'string') }
          : {}),
        ...(isRecord(raw.widths) ? { widths: numbers(raw.widths) } : {}),
        ...(isRecord(raw.hidden) ? { hidden: booleans(raw.hidden) } : {}),
      };
      if (!empty(entry)) kinds[kind] = entry;
    }
  }
  const density =
    value.density === 'compact' || value.density === 'comfortable' ? value.density : undefined;
  return density === undefined ? { kinds } : { density, kinds };
}

function numbers(record: Record<string, unknown>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [key, width] of Object.entries(record)) {
    const clamped = typeof width === 'number' ? clampWidth(width) : undefined;
    if (clamped !== undefined) out[key] = clamped;
  }
  return out;
}

function booleans(record: Record<string, unknown>): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  for (const [key, hidden] of Object.entries(record)) {
    if (typeof hidden === 'boolean') out[key] = hidden;
  }
  return out;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Test seam. Leaves the module as a fresh import would. */
export function resetColumnMemory(): void {
  snapshot = undefined;
  unwritable = false;
  listeners.clear();
  window.removeEventListener('storage', fromAnotherTab);
}
