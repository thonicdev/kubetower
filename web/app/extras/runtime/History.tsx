import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Failure, Pending, Pill, btnSecondary } from '@kubetower/ui';

import { HISTORY_RETRIES, HISTORY_RETRY_MS, InspectProblem, getHistory, type History, type HistoryLine } from './data';

/**
 * A pod's memory history: seven days of each container's working set, from the
 * cluster's Prometheus, read on a click of its own and never when the tab
 * opens. The route reads it through the console's one usage-window door
 * (`extras/runtimeinspect/history.go`) and computes the scale and the points; this draws
 * them as given.
 *
 * The four answers are four drawings and never one for another: no Prometheus,
 * a Prometheus that could not be asked, nothing held for this pod, and the
 * lines. A history still being read is asked again every few seconds.
 */

const FAILURE_TITLE: Record<string, string> = {
  list_forbidden: 'You may not list pods here',
  list_unreviewed: 'Your access could not be checked',
  history_failed: 'The history could not be read',
  unknown_pod: 'Pod no longer exists',
  unknown_cluster: 'Unknown cluster',
};

/** The color of each container's line, in the order the server sorts them (largest first). */
const LINE = ['stroke-plate-blue', 'stroke-plate-green', 'stroke-plate-amber', 'stroke-plate-violet', 'stroke-plate-teal', 'stroke-plate-pink'];
const SWATCH = ['bg-plate-blue', 'bg-plate-green', 'bg-plate-amber', 'bg-plate-violet', 'bg-plate-teal', 'bg-plate-pink'];

export function HistorySection({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }): ReactNode {
  const [asked, setAsked] = useState(false);
  const query = useQuery({
    queryKey: ['jvm', 'history', cluster, namespace, name] as const,
    queryFn: ({ signal }) => getHistory(cluster, namespace, name, signal),
    enabled: asked,
    retry: false,
    staleTime: 60_000,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
    refetchInterval: (q) =>
      q.state.data?.state === 'pending' && q.state.dataUpdateCount < HISTORY_RETRIES ? HISTORY_RETRY_MS : false,
  });
  const data = query.data;
  return (
    <section data-jvm-history="" className="flex flex-col gap-3 border-t border-line pt-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold">Memory history</h3>
        <button
          type="button"
          data-jvm-history-start=""
          className={btnSecondary}
          disabled={query.isFetching}
          title="Reads the last seven days of this pod’s containers’ working set from the cluster’s Prometheus (no command runs in the container)"
          onClick={() => (asked ? void query.refetch() : setAsked(true))}
        >
          {query.isFetching ? 'Reading…' : asked ? 'Read again' : '7 days'}
        </button>
        {data?.state === 'read' && data.partial !== undefined && data.partial !== '' ? (
          <Pill tone="warn" title={data.partial}>
            partial
          </Pill>
        ) : null}
        {data?.state === 'read' && data.cut === true ? (
          <Pill neutral="muted" title="The pod has more containers than are drawn">
            first {data.series.length}
          </Pill>
        ) : null}
      </div>
      {query.isError ? (
        query.error instanceof InspectProblem ? (
          <div data-jvm-failure={query.error.code} className="flex flex-col gap-1">
            <p className="font-medium text-warn" title={query.error.message}>
              {FAILURE_TITLE[query.error.code] ?? 'The history failed'}
            </p>
          </div>
        ) : (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        )
      ) : null}
      {asked && data === undefined && !query.isError ? <Pending what="Prometheus" /> : null}
      {data === undefined ? null : <HistoryView history={data} />}
    </section>
  );
}

/** Each state's pill: what it is, as a word, with the core's line on hover. */
const STATE_PILL: Record<Exclude<History['state'], 'read' | 'pending'>, { tone: string | undefined; label: string }> = {
  absent: { tone: undefined, label: 'No Prometheus' },
  unreadable: { tone: 'bad', label: 'Prometheus unreadable' },
  empty: { tone: undefined, label: 'No series for this pod' },
};

/** A history as the tab draws it. Exported for its test and the picture. */
export function HistoryView({ history }: { history: History }): ReactNode {
  if (history.state === 'pending') {
    return (
      <div data-jvm-history-state="pending">
        <Pending what="the week from Prometheus" />
      </div>
    );
  }
  if (history.state !== 'read') {
    const pill = STATE_PILL[history.state];
    return (
      <div data-jvm-history-state={history.state}>
        <Pill tone={pill.tone} neutral="muted" title={history.detail}>
          {pill.label}
        </Pill>
      </div>
    );
  }
  return (
    <div data-jvm-history-result="" className="flex flex-col gap-2">
      <div className="flex gap-2">
        <div className="relative h-40 w-16 shrink-0 text-2xs text-muted tabular-nums">
          {(history.ticks ?? []).map((t) => (
            <span key={t.y} className="absolute right-0 -translate-y-1/2" style={{ top: `${String(t.y)}%` }}>
              {t.text}
            </span>
          ))}
        </div>
        <svg
          viewBox="0 0 100 100"
          preserveAspectRatio="none"
          className="h-40 min-w-0 flex-1 rounded bg-white/5"
          role="img"
          aria-label="Memory over the last seven days"
        >
          {(history.ticks ?? []).map((t) => (
            <line key={t.y} x1="0" x2="100" y1={t.y} y2={t.y} vectorEffect="non-scaling-stroke" strokeWidth="1" className="stroke-white/10" />
          ))}
          {history.series.map((l, i) =>
            l.limitAt === null ? null : (
              <line
                key={`limit-${l.container}`}
                data-jvm-history-limit={l.container}
                x1="0"
                x2="100"
                y1={l.limitAt}
                y2={l.limitAt}
                vectorEffect="non-scaling-stroke"
                strokeWidth="1"
                strokeDasharray="4 3"
                className="stroke-bad"
                opacity={i === 0 ? 1 : 0.6}
              />
            ),
          )}
          {history.series.map((l, i) => (
            <polyline
              key={l.container}
              data-jvm-history-line={l.container}
              points={l.points.map(([x, y]) => `${String(x)},${String(y)}`).join(' ')}
              fill="none"
              vectorEffect="non-scaling-stroke"
              strokeWidth="1.5"
              strokeLinejoin="round"
              className={LINE[i % LINE.length] ?? 'stroke-plate-blue'}
            />
          ))}
        </svg>
      </div>
      <div className="flex justify-between pl-18 text-2xs text-muted tabular-nums">
        <span>{day(history.from)}</span>
        <span>{day(history.to)}</span>
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-2xs tabular-nums">
        {history.series.map((l, i) => (
          <Legend key={l.container} line={l} swatch={SWATCH[i % SWATCH.length] ?? 'bg-plate-blue'} />
        ))}
      </ul>
    </div>
  );
}

function Legend({ line, swatch }: { line: HistoryLine; swatch: string }): ReactNode {
  return (
    <li className="flex items-center gap-1" title={`min ${line.min.text} · max ${line.max.text}${line.limit === null ? '' : ` · limit ${line.limit.text}`}`}>
      <span className={`inline-block h-2 w-2 rounded-sm ${swatch}`} />
      {line.container}
      <span className="text-muted">
        {line.last.text} now · max {line.max.text}
      </span>
      {line.limit === null ? null : <span className="text-bad">limit {line.limit.text}</span>}
    </li>
  );
}

const DAY = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'UTC' });

function day(unix: number | undefined): string {
  return unix === undefined ? '' : `${DAY.format(new Date(unix * 1000))} UTC`;
}
