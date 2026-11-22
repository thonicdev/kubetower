import type { UseQueryResult } from '@tanstack/react-query';

import type { ListResult, ValueResult } from '../api';
import type { CapacityCell, MetricsPanel, OverviewPage } from '../pages/overview/data';
import { measured } from '../pages/overview/Reading';

/**
 * The chooser's arithmetic, kept out of the components so that it can be read
 * against its tests rather than against a screenshot.
 *
 * **Nothing here invents a figure.** Every number is the server's: a usage is
 * one cluster's `used` over its allocatable, taken only where it measured
 * both; a count is a panel's own. Where nothing measured, the answer
 * is `undefined` and the page draws nothing, or a dash where a read failed -
 * never a zero, which on this page would read as an idle fleet.
 */

/** One cluster's overview as the chooser sees it: still being read, read, or not. */
export type OverviewRead =
  | { readonly kind: 'waiting' }
  | { readonly kind: 'failed'; readonly why: string | undefined }
  | {
      readonly kind: 'ok';
      readonly page: OverviewPage;
      /** A later refetch failed over this answer: why, or `''` with no words. */
      readonly stale: string | undefined;
    };

function messageOf(error: unknown): string | undefined {
  return error instanceof Error && error.message !== '' ? error.message : undefined;
}

/**
 * One query's answer, with the two ways it can be wrong kept apart: a read
 * that never answered, and **a refetch that failed over an answer already
 * held**. The second keeps its figures on screen - blanking a card because
 * one poll failed is worse than the staleness - and says so, as `ListView`
 * does with *Not refreshed* (`state/Result.tsx`).
 */
export function readOf(query: UseQueryResult<ValueResult<OverviewPage>> | undefined): OverviewRead {
  if (query === undefined || query.data === undefined) {
    return query?.isError === true ? { kind: 'failed', why: messageOf(query.error) } : { kind: 'waiting' };
  }
  const result = query.data;
  if (result.state !== 'ok') return { kind: 'failed', why: result.reason.message };
  return {
    kind: 'ok',
    page: result.data,
    stale: query.isError ? (messageOf(query.error) ?? '') : undefined,
  };
}

/** The capacity panel's cell for `label` ("CPU", "Memory"), where the panel answered. */
export function capacityCell(page: OverviewPage, label: string): CapacityCell | undefined {
  const panel = page.capacity;
  if (panel?.state !== 'ok') return undefined;
  return panel.items.find((cell) => cell.label === label);
}

/** One usage tile's figures: `used/whole unit`, and how full. */
export interface Usage {
  readonly used: string;
  readonly whole: string;
  readonly unit: string;
  /** 0-100, used over allocatable. */
  readonly percent: number;
}

/**
 * `40`, `11.2`, `188.1`, `0.25`: one place, two under one, never a trailing
 * zero - the server writes `40.0 cores` and `16.0 GiB` (`resources.CPUSize`,
 * `MemorySize`), and `10/40.0` is not the figure wanted. A real
 * figure that rounds to nothing is `<0.01`, never `0`, which reads as idle.
 *
 * **The same rule as the server's `resources.Fraction`** (`fractionFigure`),
 * deliberately: every other usage hover in the console is written there, both
 * halves in the whole's unit, and this tile is the one hover the browser
 * composes (`components/Reading.tsx` part 2). Change one, change both.
 */
function figure(n: number): string {
  const places = n >= 1 ? 1 : 2;
  const shown = Number(n.toFixed(places));
  if (shown === 0 && n > 0) return '<0.01';
  return String(shown);
}

/** The server's rendered figure split into its number and its unit: `48 cores`, `500m`, `188.1 GiB`. */
function split(text: string | undefined): { n: number; unit: string } | undefined {
  const match = /^\s*(\d+(?:\.\d+)?)\s*([A-Za-z]*)\s*$/.exec(text ?? '');
  if (match === null) return undefined;
  const n = Number(match[1]);
  return Number.isFinite(n) ? { n, unit: match[2] ?? '' } : undefined;
}

/** What one unit the server names is worth in the wire's own unit: CPU is
 *  millicores on the wire (`internal/overview`, `UnitCores`), memory bytes. */
const SCALE: Readonly<Record<string, number>> = {
  cores: 1000,
  core: 1000,
  m: 1000, // said in cores, as the larger figure is
  KiB: 2 ** 10,
  MiB: 2 ** 20,
  GiB: 2 ** 30,
  TiB: 2 ** 40,
};

/**
 * Used over allocatable, **in the allocatable's own unit**: `10 / 40 cores`,
 * and `97.8 / 188.1 GiB` the same way.
 *
 * **The unit word is the server's; the figures are the wire's values.** The
 * unit is read from the server's rendered capacity (`Reading.text`, its one
 * formatter); both halves are then the exact `value`s divided by what that unit
 * is worth (`SCALE`). They are not the rounded text times a ratio: `1.9 cores`
 * is 1930m printed, and rescaling from it would read `0.98 / 1.9 cores` where
 * the server's `resources.Fraction` reads `1 / 1.9 cores` for the same cluster.
 * This is the one browser-composed hover (`components/Reading.tsx` part 2), so
 * it has to agree with the server figure for figure. CPU in millicores (`500m`)
 * is said in cores. The ratio is **not** clamped for the figure: 42 used of 40
 * is `42 / 40`, a measurement; the bar clamps.
 *
 * Nothing at all where either half was not measured, or the unit is one this
 * does not know: no tile is drawn for it.
 */
export function usage(page: OverviewPage, label: string): Usage | undefined {
  const cell = capacityCell(page, label);
  if (!measured(cell?.used) || !measured(cell?.capacity) || cell.capacity.value <= 0) return undefined;

  const ratio = Math.max(0, cell.used.value / cell.capacity.value);
  // No figure to read the unit from: CPU is millicores on the wire; anything
  // else is not guessed.
  const named = split(cell.capacity.text)?.unit ?? (label === 'CPU' ? 'cores' : undefined);
  const scale = named === undefined ? undefined : SCALE[named];
  if (named === undefined || scale === undefined) return undefined;
  const unit = named === 'core' || named === 'm' ? 'cores' : named;

  return {
    used: figure(cell.used.value / scale),
    whole: figure(cell.capacity.value / scale),
    unit,
    percent: Math.min(100, ratio * 100),
  };
}

/** The ok half of a list panel, or nothing: `empty` is zero items, a failure is not a count. */
export function itemsOf<T>(panel: ListResult<T> | undefined): readonly T[] | undefined {
  if (panel === undefined) return undefined;
  if (panel.state === 'ok' || panel.state === 'empty') return panel.items;
  return undefined;
}

/**
 * The pods tile of the counts panel: the count, `'failed'` where the panel
 * failed, or `undefined` where it answered with no pods entry - nothing to
 * draw, which is not a failure either.
 */
export function podCount(page: OverviewPage): { count: number; note: string | undefined } | 'failed' | undefined {
  const items = itemsOf(page.counts);
  if (items === undefined) return page.counts === undefined ? undefined : 'failed';
  const entry = items.find((item) => item.kind === 'pods');
  return entry === undefined ? undefined : { count: entry.count, note: entry.note };
}

/**
 * The cluster's own CPU line over the last hour, **in cores**. The server keys
 * it from its title (`seriesKey("CPU used")`, `internal/web/apioverview.go`);
 * the contract fixes neither, so either one finds it. It sends millicores and
 * no unit (`internal/overview/metrics.go`, `scale: 1000`); a line that says
 * `cores` is taken at its word. Converted once here, so the card's caption
 * and its CPU tile name the same unit.
 */
export function cpuHistory(page: OverviewPage): MetricsPanel | undefined {
  const history = page.history;
  if (history?.state !== 'ok') return undefined;
  const series = history.data.series.find((one) => one.key === 'cpu-used' || one.label === 'CPU used');
  if (series === undefined) return undefined;
  const per = series.unit === 'cores' ? 1 : 1 / 1000;
  const values = series.data.values.map((value) => (value === null ? null : value * per));
  return { ...history.data, series: [{ ...series, unit: 'cores', data: { ...series.data, values } }] };
}

/**
 * Why the history was not read, where it failed rather than being absent.
 *
 * `absent` with `not_installed` is a cluster with no series source, and draws
 * nothing, as its overview does. Anything else that is not `ok` is a read that
 * failed, which the server sends as a failure precisely so that it stays drawn
 * (`internal/web/apioverview.go`, `clusterHistory`).
 */
export function historyFailure(page: OverviewPage): string | undefined {
  const history = page.history;
  if (history === undefined || history.state === 'ok') return undefined;
  if (history.state === 'absent' && history.reason.code === 'not_installed') return undefined;
  return history.reason.message;
}
