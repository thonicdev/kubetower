import type { Figures, Pod, Points, Series, Stats, Upstream } from './data';

/** The five tiles, in the order they are drawn; `?show=` carries one. */
export const TILE_KEYS = ['qps', 'nxdomain', 'servfail', 'p99', 'cache'] as const;
export type TileKey = (typeof TILE_KEYS)[number];

export const TILE_LABEL: Record<TileKey, string> = { qps: 'QPS', nxdomain: 'NXDOMAIN', servfail: 'SERVFAIL', p99: 'p99', cache: 'Cache hit' };

export type Tone = 'ok' | 'warn' | 'bad' | 'none';

/** SERVFAIL above 1 % is amber and above 5 % red; p99 above 100 ms amber and above 1 s red. */
export const SERVFAIL_WARN = 0.01;
export const SERVFAIL_BAD = 0.05;
export const P99_WARN = 0.1;
export const P99_BAD = 1;

export const TILE_HINT: Record<TileKey, string> = {
  qps: 'Queries per second over the last rate window. Click to show pods above the average.',
  nxdomain: 'Share of answers that were NXDOMAIN. Click to show pods above the cluster figure.',
  servfail: 'Share of answers that were SERVFAIL. Amber above 1 %, red above 5 %. Click to show what is above 1 %.',
  p99: '99th percentile latency. Amber above 100 ms, red above 1 s. Click to show what is above 100 ms.',
  cache: 'Share of queries served from the cache. Click to show pods below the cluster figure.',
};

export function toneOf(key: TileKey, f: Figures | undefined): Tone {
  if (f === undefined) return 'none';
  if (key === 'servfail' && f.servfail !== null) return f.servfail > SERVFAIL_BAD ? 'bad' : f.servfail > SERVFAIL_WARN ? 'warn' : 'none';
  if (key === 'p99' && f.p99 !== null) return f.p99 > P99_BAD ? 'bad' : f.p99 > P99_WARN ? 'warn' : 'none';
  return 'none';
}

/** The figure a tile shows, or null for one the window did not hold. */
export function figureOf(key: TileKey, f: Figures | null | undefined): number | null {
  if (f === null || f === undefined) return null;
  switch (key) {
    case 'qps':
      return f.qps;
    case 'nxdomain':
      return f.nxdomain;
    case 'servfail':
      return f.servfail;
    case 'p99':
      return f.p99;
    case 'cache':
      return f.cacheHit;
  }
}

export function percent(share: number | null): string {
  if (share === null || !Number.isFinite(share)) return '-';
  const pct = share * 100;
  return `${pct >= 10 || pct === 0 ? Math.round(pct) : Math.round(pct * 10) / 10} %`;
}

export function millis(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds)) return '-';
  const ms = seconds * 1000;
  if (ms >= 1000) return `${Math.round(ms / 100) / 10} s`;
  return `${ms >= 10 ? Math.round(ms) : Math.round(ms * 10) / 10} ms`;
}

export function rate(qps: number | null): string {
  if (qps === null || !Number.isFinite(qps)) return '-';
  if (qps >= 100) return `${Math.round(qps)}`;
  return `${Math.round(qps * 10) / 10}`;
}

export function format(key: TileKey, value: number | null): string {
  if (value === null) return '-';
  return key === 'qps' ? rate(value) : key === 'p99' ? millis(value) : percent(value);
}

/** What the page says in place of figures, in two or three words; the reason goes in the title. */
export function stateWord(s: Stats): string | null {
  switch (s.state) {
    case 'read':
      return null;
    case 'no-prometheus':
      return 'Prometheus absent';
    case 'unreadable':
      return 'Unreadable';
    case 'empty':
      return s.namespace === undefined || s.namespace === '' ? 'No CoreDNS' : 'No metrics';
  }
}

/** A bar: what is drawn, how long, and whether the tile's threshold is crossed. */
export interface Bar {
  id: string;
  label: string;
  value: number | null;
  text: string;
  /** 0..1 of the longest bar. */
  width: number;
  tone: Tone;
  title: string;
  /** The row says a word in place of a figure. */
  word?: string;
}

const avg = (xs: number[]) => (xs.length === 0 ? 0 : xs.reduce((a, b) => a + b, 0) / xs.length);

function podTitle(p: Pod): string {
  const f = p.figures;
  if (f === null) return `${p.pod}: Prometheus holds no series for this pod`;
  return `${p.pod}${p.node ? ` on ${p.node}` : ''}: ${rate(f.qps)} qps, NXDOMAIN ${percent(f.nxdomain)}, SERVFAIL ${percent(f.servfail)}, p50 ${millis(f.p50)}, p99 ${millis(f.p99)}, cache ${percent(f.cacheHit)}`;
}

function upstreamTitle(u: Upstream): string {
  const zones = u.zones === undefined || u.zones.length === 0 ? '' : ` (zone ${u.zones.join(', ')})`;
  if (!u.answers) return `${u.to}${zones}: no answers; health checks fail ${rate(u.healthFailPs)} per second`;
  return `${u.to}${zones}: ${rate(u.qps)} qps, ${rate(u.errQps)} errors per second, p99 ${millis(u.p99)}, health failures ${rate(u.healthFailPs)} per second`;
}

/** Whether a pod is over the tile's threshold (for qps, nxdomain and cache: against the cluster's own figure). */
function podOver(key: TileKey, p: Pod, s: Stats, meanQps: number): boolean {
  const v = figureOf(key, p.figures);
  if (v === null) return false;
  switch (key) {
    case 'qps':
      return v > meanQps;
    case 'nxdomain':
      return s.now?.nxdomain != null && v > s.now.nxdomain;
    case 'servfail':
      return v > SERVFAIL_WARN;
    case 'p99':
      return v > P99_WARN;
    case 'cache':
      return s.now?.cacheHit != null && v < s.now.cacheHit;
  }
}

function upstreamOver(key: TileKey, u: Upstream, meanQps: number): boolean {
  if (!u.answers) return key === 'servfail' || key === 'p99' || key === 'qps';
  switch (key) {
    case 'qps':
      return u.qps > meanQps;
    case 'servfail':
      return u.qps > 0 && u.errQps / u.qps > SERVFAIL_WARN;
    case 'p99':
      return u.p99 !== null && u.p99 > P99_WARN;
    default:
      return true;
  }
}

/**
 * The pods' bars under a tile (none: queries per second), narrowed to those
 * over its threshold when `filter` is on. A pod with no series is listed with
 * a word, never as an idle bar, and is dropped by a filter that needs a figure.
 */
export function podBars(s: Stats, key: TileKey, filter: boolean): Bar[] {
  const meanQps = avg(s.pods.flatMap((p) => (p.figures === null ? [] : [p.figures.qps])));
  const rows = s.pods.filter((p) => !filter || podOver(key, p, s, meanQps));
  const max = Math.max(0, ...rows.map((p) => figureOf(key, p.figures) ?? 0));
  return rows.map((p) => {
    const value = figureOf(key, p.figures);
    return {
      id: p.pod,
      label: p.pod,
      value,
      text: format(key, value),
      width: value === null || max === 0 ? 0 : Math.min(1, value / max),
      tone: toneOf(key, p.figures ?? undefined),
      title: podTitle(p),
      ...(p.figures === null ? { word: 'No metrics' } : {}),
    };
  });
}

/** The upstreams' bars: queries per second, or p99 under the p99 tile. One that never answered says "No answers". */
export function upstreamBars(s: Stats, key: TileKey, filter: boolean): Bar[] {
  const meanQps = avg(s.upstreams.filter((u) => u.answers).map((u) => u.qps));
  const rows = s.upstreams.filter((u) => !filter || upstreamOver(key, u, meanQps));
  const by = key === 'p99' ? 'p99' : 'qps';
  const val = (u: Upstream) => (by === 'p99' ? u.p99 : u.qps);
  const max = Math.max(0, ...rows.map((u) => val(u) ?? 0));
  return rows.map((u) => {
    const value = u.answers ? val(u) : null;
    const errShare = u.qps > 0 ? u.errQps / u.qps : 0;
    return {
      id: u.to,
      label: u.to,
      value,
      text: by === 'p99' ? millis(value) : rate(value),
      width: value === null || max === 0 ? 0 : Math.min(1, value / max),
      tone: !u.answers || errShare > SERVFAIL_BAD ? 'bad' : errShare > SERVFAIL_WARN ? 'warn' : 'none',
      title: upstreamTitle(u),
      ...(u.answers ? {} : { word: 'No answers' }),
    };
  });
}

/** Seconds of a series' points aligned onto one grid: the union of the times, a missing point read as 0 (rate series only). */
export function align(lines: Record<string, Points>): { t: number[]; v: Record<string, number[]> } {
  const times = [...new Set(Object.values(lines).flatMap((l) => l.map(([t]) => t)))].sort((a, b) => a - b);
  const v: Record<string, number[]> = {};
  for (const [name, l] of Object.entries(lines)) {
    const at = new Map(l);
    v[name] = times.map((t) => at.get(t) ?? 0);
  }
  return { t: times, v };
}

export const CHART_W = 600;
export const CHART_H = 120;

export interface Scale {
  x: (t: number) => number;
  y: (v: number) => number;
  max: number;
}

/** A shared scale over the points: time to x, value to y (0 at the foot), the top rounded up to the data's maximum. */
export function scaleOf(times: number[], top: number): Scale {
  const t0 = times[0] ?? 0;
  const span = (times[times.length - 1] ?? 0) - t0 || 1;
  const max = top > 0 ? top : 1;
  return { x: (t) => ((t - t0) / span) * CHART_W, y: (v) => CHART_H - (Math.min(v, max) / max) * CHART_H, max };
}

const n = (v: number) => Math.round(v * 10) / 10;

/** A polyline path through points. */
export function linePath(points: Points, sc: Scale): string {
  return points.map(([t, v], i) => `${i === 0 ? 'M' : 'L'}${n(sc.x(t))} ${n(sc.y(v))}`).join(' ');
}

/** The highest point of a series, placed on a scale. */
export function peakOf(points: Points, sc: Scale): { x: number; y: number; value: number } | null {
  let best: [number, number] | null = null;
  for (const p of points) if (best === null || p[1] > best[1]) best = p;
  return best === null ? null : { x: sc.x(best[0]), y: sc.y(best[1]), value: best[1] };
}

/** The highest point of the stacked answer codes, on the scale `stackPaths` draws them with. */
export function stackPeak(lines: Record<string, Points>, order: string[]): { x: number; y: number; value: number } | null {
  const { t, v } = align(lines);
  if (t.length < 2) return null;
  const totals: Points = t.map((time, i) => [time, order.reduce((sum, code) => sum + (v[code]?.[i] ?? 0), 0)]);
  return peakOf(totals, scaleOf(t, Math.max(...totals.map(([, x]) => x))));
}

/** Stacked areas of the rate series by answer code, bottom first: one closed path each. */
export function stackPaths(lines: Record<string, Points>, order: string[]): { code: string; d: string; max: number }[] {
  const { t, v } = align(lines);
  if (t.length < 2) return [];
  const totals = t.map((_, i) => order.reduce((sum, code) => sum + (v[code]?.[i] ?? 0), 0));
  const sc = scaleOf(t, Math.max(...totals));
  const base = t.map(() => 0);
  return order
    .filter((code) => v[code] !== undefined)
    .map((code) => {
      const low = [...base];
      const high = t.map((_, i) => (base[i] = (base[i] ?? 0) + (v[code]?.[i] ?? 0)));
      const top = t.map((time, i) => `${i === 0 ? 'M' : 'L'}${n(sc.x(time))} ${n(sc.y(high[i] ?? 0))}`).join(' ');
      const back = t
        .map((time, i) => `L${n(sc.x(time))} ${n(sc.y(low[i] ?? 0))}`)
        .reverse()
        .join(' ');
      return { code, d: `${top} ${back} Z`, max: sc.max };
    });
}

/** The answer codes in the stacking order: NOERROR at the foot, then the others by name, SERVFAIL on top. */
export function rcodeOrder(series: Series): string[] {
  const names = Object.keys(series.byRcode);
  const rest = names.filter((c) => c !== 'NOERROR' && c !== 'SERVFAIL').sort();
  return [...names.filter((c) => c === 'NOERROR'), ...rest, ...names.filter((c) => c === 'SERVFAIL')];
}

/** The tile keys whose figure has a threshold, in the order the line names them. */
const GRADED: readonly TileKey[] = ['servfail', 'p99'];

/** The worst tone any graded figure reaches, and the tile that reaches it. */
export function worstOf(f: Figures | undefined): { tone: Tone; key: TileKey | null } {
  let worst: { tone: Tone; key: TileKey | null } = { tone: 'none', key: null };
  for (const key of GRADED) {
    const tone = toneOf(key, f);
    if (tone === 'bad' || (tone === 'warn' && worst.tone === 'none')) worst = { tone, key };
  }
  return worst;
}
