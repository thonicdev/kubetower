import type { ReactNode } from 'react';
import { Pill, btnToggle, focusRing } from '@kubetower/ui';

import { SPANS, type Figures, type Points, type Series, type Span } from './data';
import {
  CHART_H,
  CHART_W,
  TILE_HINT,
  TILE_KEYS,
  TILE_LABEL,
  format,
  figureOf,
  linePath,
  millis,
  peakOf,
  percent,
  rate,
  rcodeOrder,
  scaleOf,
  stackPaths,
  stackPeak,
  toneOf,
  type Bar,
  type TileKey,
  type Tone,
} from './model';

const TONE_TEXT: Record<Tone, string> = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', none: '' };
const TONE_BAR: Record<Tone, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', none: 'bg-accent' };

/** The five tiles. A tile filters the bars under it; pressed again, it lets them go. */
export function Tiles({ now, show, onShow }: { now: Figures | undefined; show: TileKey | ''; onShow: (key: TileKey | '') => void }): ReactNode {
  return (
    <ul aria-label="Tiles" className="flex flex-wrap gap-2">
      {TILE_KEYS.map((key) => {
        const value = figureOf(key, now);
        const on = show === key;
        return (
          <li key={key}>
            <button
              type="button"
              data-tile={key}
              data-tone={toneOf(key, now)}
              aria-pressed={on}
              title={TILE_HINT[key]}
              onClick={() => onShow(on ? '' : key)}
              className={`flex items-center gap-2 rounded-md border px-3 py-1.5 ${on ? 'border-accent bg-panel-2' : 'border-line bg-panel hover:bg-panel-2'} ${focusRing}`}
            >
              <span className={`text-2xl font-semibold tabular-nums ${value === null ? 'text-muted' : TONE_TEXT[toneOf(key, now)]}`}>{format(key, value)}</span>
              <span className="text-xs text-muted">{TILE_LABEL[key]}</span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

export function SpanControl({ span, onSpan }: { span: Span; onSpan: (s: Span) => void }): ReactNode {
  const label: Record<Span, string> = { '15m': '15 min', '1h': '1 h', '6h': '6 h' };
  return (
    <div role="group" aria-label="Span" className="flex gap-1">
      {SPANS.map((s) => (
        <button key={s} type="button" aria-pressed={s === span} data-span={s} onClick={() => onSpan(s)} className={`${btnToggle(s === span)} ${focusRing}`}>
          {label[s]}
        </button>
      ))}
    </div>
  );
}

function Frame({ title, max, children }: { title: string; max: string; children: ReactNode }): ReactNode {
  return (
    <figure className="m-0 min-w-0 flex-1 basis-72 rounded-md border border-line bg-panel p-2">
      <figcaption className="mb-1 flex items-baseline justify-between text-xs text-muted">
        <span>{title}</span>
        <span className="tabular-nums">{max}</span>
      </figcaption>
      {children}
    </figure>
  );
}

function Empty({ title }: { title: string }): ReactNode {
  return (
    <Frame title={title} max="">
      <p data-state="empty" className="py-8 text-center text-xs text-muted">
        No metrics
      </p>
    </Frame>
  );
}

const usable = (p: Points | undefined): p is Points => p !== undefined && p.length >= 2;

type Peak = { x: number; y: number; value: number } | null;

/** The plot, with the value of its peak written beside the peak point. */
function Plot({ children, label, peak, text }: { children: ReactNode; label: string; peak?: Peak; text?: (v: number) => string }): ReactNode {
  const left = peak ? (peak.x / CHART_W) * 100 : 0;
  const align = left < 12 ? '' : left > 88 ? '-translate-x-full' : '-translate-x-1/2';
  return (
    <div className="relative">
      <svg viewBox={`0 0 ${CHART_W} ${CHART_H}`} role="img" aria-label={label} preserveAspectRatio="none" className="h-28 w-full overflow-visible">
        {children}
      </svg>
      {peak && text ? (
        <span
          data-peak=""
          className={`pointer-events-none absolute translate-y-1 whitespace-nowrap rounded-sm bg-panel px-0.5 text-2xs tabular-nums text-muted ${align}`}
          style={{ left: `${left}%`, top: `${(peak.y / CHART_H) * 100}%` }}
        >
          {text(peak.value)}
        </span>
      ) : null}
    </div>
  );
}

const RCODE_FILL: Record<string, string> = { NOERROR: 'fill-ok/50', NXDOMAIN: 'fill-warn/60', SERVFAIL: 'fill-bad/70' };

/** Queries per second stacked by answer code. */
export function RcodeChart({ series }: { series: Series }): ReactNode {
  const paths = stackPaths(series.byRcode, rcodeOrder(series));
  if (paths.length === 0) return <Empty title="QPS by answer" />;
  const top = paths[0]?.max ?? 0;
  return (
    <Frame title="QPS by answer" max={rate(top)}>
      <Plot label="Queries per second stacked by answer code" peak={stackPeak(series.byRcode, rcodeOrder(series))} text={rate}>
        {paths.map((p) => (
          <path key={p.code} d={p.d} data-code={p.code} className={RCODE_FILL[p.code] ?? 'fill-muted/50'}>
            <title>{p.code}</title>
          </path>
        ))}
      </Plot>
    </Frame>
  );
}

function lastOf(p: Points): number {
  return p[p.length - 1]?.[1] ?? 0;
}

/** p50 and p99 on one scale. */
export function LatencyChart({ series }: { series: Series }): ReactNode {
  const lines: [string, Points | undefined, string][] = [
    ['p50', series.p50, 'stroke-ok'],
    ['p99', series.p99, 'stroke-warn'],
  ];
  const drawn = lines.filter((l): l is [string, Points, string] => usable(l[1]));
  if (drawn.length === 0) return <Empty title="Latency" />;
  const times = drawn.flatMap(([, p]) => p.map(([t]) => t));
  const sc = scaleOf([Math.min(...times), Math.max(...times)], Math.max(...drawn.flatMap(([, p]) => p.map(([, v]) => v))));
  return (
    <Frame title="p50 / p99" max={millis(sc.max)}>
      <Plot label="Median and 99th percentile latency" peak={peakOf(drawn[drawn.length - 1]?.[1] ?? [], sc)} text={millis}>
        {drawn.map(([name, p, cls]) => (
          <path key={name} d={linePath(p, sc)} data-line={name} className={`fill-none stroke-2 ${cls}`} vectorEffect="non-scaling-stroke">
            <title>{`${name} now ${millis(lastOf(p))}`}</title>
          </path>
        ))}
      </Plot>
    </Frame>
  );
}

/** The cache hit share, 0 to 100 %. */
export function CacheChart({ series }: { series: Series }): ReactNode {
  if (!usable(series.cacheHit)) return <Empty title="Cache hit" />;
  const times = series.cacheHit.map(([t]) => t);
  const sc = scaleOf(times, 1);
  return (
    <Frame title="Cache hit" max={percent(1)}>
      <Plot label="Cache hit rate" peak={peakOf(series.cacheHit, sc)} text={percent}>
        <path d={linePath(series.cacheHit, sc)} data-line="cache" className="fill-none stroke-2 stroke-accent" vectorEffect="non-scaling-stroke">
          <title>{`now ${percent(lastOf(series.cacheHit))}`}</title>
        </path>
      </Plot>
    </Frame>
  );
}

/** One bar per row; a row with a word (No metrics, No answers) draws the word in place of a bar. */
export function Bars({ title, rows, none }: { title: string; rows: Bar[]; none: string }): ReactNode {
  return (
    <section aria-label={title} data-bars={title} className="min-w-0 flex-1 basis-96">
      <h2 className="mb-1 text-sm font-medium">
        {title} <span className="text-xs font-normal text-muted">{rows.length}</span>
      </h2>
      {rows.length === 0 ? (
        <p className="text-xs text-muted">{none}</p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {rows.map((row) => (
            <li key={row.id} data-bar={row.id} data-tone={row.tone} title={row.title} className="flex items-center gap-2 text-xs">
              <span title={row.label} className="w-1/3 shrink-0 truncate font-mono">{row.label}</span>
              <span className="h-2 flex-1 overflow-hidden rounded-sm bg-panel-2">
                {row.word === undefined ? <span className={`block h-full ${TONE_BAR[row.tone]}`} style={{ width: `${Math.round(row.width * 100)}%` }} /> : null}
              </span>
              <span className={`flex min-w-20 shrink-0 justify-end tabular-nums ${row.word === undefined ? TONE_TEXT[row.tone] : 'text-muted'}`}>{row.word === 'No answers' ? <Pill tone="bad">{row.word}</Pill> : (row.word ?? row.text)}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
