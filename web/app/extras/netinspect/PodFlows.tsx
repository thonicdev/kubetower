import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Failure, HEAD_ROW, Pending, Pill, ROW, TABLE, bodyCell, btnToggle, focusRing, formatClock, headCell } from '@kubetower/ui';

import { FLOW_WINDOWS, DEFAULT_FLOW_WINDOW, flowsQuery, type FlowRow } from './data';
import { FLOW_STATE } from './parts';

/** The rows the enlarged view draws before saying how many it left out. */
const MAX_ROWS = 200;

const WINDOW_WORD: Record<number, string> = { 60: '1 min', 300: '5 min', 900: '15 min', 3600: '1 h' };

/**
 * What the flow source saw of one pod, in the enlarged view: its namespace's
 * flows over the period picked, kept where the pod is an end. The period is
 * a read of its own (`?window=`), like a metrics span; nothing is drawn where
 * the cluster has no flow source.
 */
export function PodFlows({ cluster, namespace, pod, query }: { cluster: string; namespace: string; pod: string; query: string }): ReactNode {
  const [window, setWindow] = useState(DEFAULT_FLOW_WINDOW);
  const [picked, setPicked] = useState<string | null>(null);
  const flows = useQuery({ ...flowsQuery(cluster, namespace, window), placeholderData: (previous) => previous });
  const id = `${namespace}/${pod}`;
  const data = flows.data;
  if (data?.state === 'absent') return null;

  const periods = (
    <div role="group" aria-label="Period" className="flex gap-1" data-flow-windows="">
      {FLOW_WINDOWS.map((w) => (
        <button key={w} type="button" data-window={w} aria-pressed={window === w} onClick={() => setWindow(w)} className={`${btnToggle(window === w)} tabular-nums`}>
          {WINDOW_WORD[w] ?? `${String(w)} s`}
        </button>
      ))}
    </div>
  );

  let body: ReactNode;
  let counts: ReactNode = null;
  if (data === undefined) {
    body = flows.isError ? <Failure error={flows.error} onRetry={() => void flows.refetch()} /> : <Pending what="the flows" />;
  } else if (data.state !== 'read' && data.state !== 'empty') {
    body = (
      <p role="status" data-pod-flows-state={data.state} className="text-xs text-warn" title={data.detail}>
        {data.state === 'tls' && data.source === 'calico' ? 'TLS failed' : FLOW_STATE[data.state]}
      </p>
    );
  } else {
    const mine = data.rows.filter((r) => r.from === id || r.to === id);
    const q = query.trim().toLowerCase();
    const rows = q === '' ? mine : mine.filter((r) => searchOf(r).includes(q));
    const forwarded = mine.filter((r) => r.verdict === 'FORWARDED').reduce((n, r) => n + r.count, 0);
    const dropped = mine.filter((r) => r.verdict === 'DROPPED').reduce((n, r) => n + r.count, 0);
    counts = (
      <span className="flex items-center gap-2 text-xs tabular-nums" data-pod-flow-counts="">
        <span className="flex items-center gap-1" title="forwarded">
          <span aria-hidden="true" className="inline-block size-1.5 rounded-full bg-ok" />
          {forwarded}
        </span>
        <span className="flex items-center gap-1" title="dropped">
          <span aria-hidden="true" className="inline-block size-1.5 rounded-full bg-bad" />
          {dropped}
        </span>
        {data.truncated ? (
          <span className="text-warn" title="The source held more flows than were read">
            +
          </span>
        ) : null}
      </span>
    );
    body =
      mine.length === 0 ? (
        <p data-pod-flows-none="" className="text-xs text-muted">
          no flow
        </p>
      ) : rows.length === 0 ? (
        <p data-no-match="" className="text-xs text-muted">
          no match
        </p>
      ) : (
        <>
          <table className={`${TABLE} w-full`} data-pod-flows="">
            <thead>
              <tr className={HEAD_ROW}>
                <th className={headCell()} aria-label="Direction" />
                <th className={headCell()}>Peer</th>
                <th className={headCell()}>Port</th>
                <th className={headCell()}>Verdict</th>
                <th className={headCell()}>Dropped by</th>
                <th className={headCell()}>Flows</th>
                <th className={headCell()}>Last</th>
              </tr>
            </thead>
            <tbody>
              {rows.slice(0, MAX_ROWS).map((r) => {
                const key = keyOf(r);
                const out = r.from === id;
                const on = picked === key;
                return (
                  <tr
                    key={key}
                    data-pod-flow={key}
                    data-verdict={r.verdict}
                    aria-selected={on}
                    tabIndex={0}
                    onClick={() => setPicked(on ? null : key)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        setPicked(on ? null : key);
                      }
                    }}
                    className={`${ROW} cursor-pointer ${on ? 'bg-accent/15' : ''} ${focusRing}`}
                  >
                    <td className={`${bodyCell()} text-muted`} title={out ? 'egress' : 'ingress'}>
                      {out ? '→' : '←'}
                    </td>
                    <td className={bodyCell()}>{out ? r.to : r.from}</td>
                    <td className={`${bodyCell()} tabular-nums`}>{r.port === 0 ? r.protocol : `${String(r.port)}/${r.protocol}`}</td>
                    <td className={bodyCell()}>
                      <span title={r.reason}>
                        <Pill tone={r.verdict === 'DROPPED' ? 'bad' : r.verdict === 'FORWARDED' ? 'ok' : undefined}>{r.verdict.toLowerCase()}</Pill>
                      </span>
                    </td>
                    <td className={`${bodyCell()} text-muted`}>{r.deniedBy.join(', ')}</td>
                    <td className={`${bodyCell()} tabular-nums`}>{r.count}</td>
                    <td className={`${bodyCell()} text-muted tabular-nums`} title={r.last}>
                      {r.last === '' ? '' : formatClock(r.last)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {rows.length > MAX_ROWS ? (
            <p className="text-xs text-muted tabular-nums">
              {MAX_ROWS} / {rows.length}
            </p>
          ) : null}
        </>
      );
  }

  return (
    <section aria-label="Observed flows" data-pod-flows-panel="" className="flex flex-col gap-2 rounded-lg border border-line bg-panel p-3" aria-busy={flows.isFetching}>
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-medium">Observed</h3>
        {data?.source === undefined || data.source === '' ? null : <Pill>{data.source === 'calico' ? 'Calico' : 'Hubble'}</Pill>}
        {counts}
        <span className="ml-auto">{periods}</span>
      </div>
      <div className={flows.isPlaceholderData ? 'opacity-60' : undefined}>{body}</div>
    </section>
  );
}

function keyOf(r: FlowRow): string {
  return `${r.from}>${r.to}:${String(r.port)}/${r.protocol}:${r.verdict}:${r.reason ?? ''}`;
}

function searchOf(r: FlowRow): string {
  return [r.from, r.to, String(r.port), r.protocol, r.verdict, r.reason ?? '', ...r.deniedBy].join(' ').toLowerCase();
}
