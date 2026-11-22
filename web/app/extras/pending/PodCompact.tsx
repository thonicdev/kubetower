import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Dialog, Failure, GoneNotice, isGoneProblem, KeyLayer, Pending as Waiting, btnSecondary } from '@kubetower/ui';

import { ago, format, podQuery, type Fit, type PodView as PodViewData, type Reason } from './data';
import DebugWindow from './DebugWindow';
import { CATEGORY_DOT } from './parts';
import { STAGE_TEXT } from './PodView';

/** How many reasons the compact tile draws. */
export const TOP_REASONS = 3;

/**
 * A pending pod's tile in its panel: the state, its top reasons as bars and
 * the node that comes closest with what it lacks. Everything else - Node fit,
 * the room on the closest nodes, events, what it would take - is in the
 * window the Expand button opens, which is the pod's full view.
 */
export function PodCompact({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }): ReactNode {
  const query = useQuery(podQuery(cluster, namespace, name));
  const [open, setOpen] = useState(false);
  if (query.data === undefined) {
    if (query.isError && isGoneProblem(query.error, ['no_such_pod'])) {
      return <GoneNotice what="Pod" detail={query.error instanceof Error ? query.error.message : undefined} />;
    }
    return query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : <Waiting what="why this pod waits" />;
  }
  const view = query.data;
  const row = view.row;
  if (!view.pending || row === undefined) {
    return (
      <p data-not-pending="" className="rounded-lg border border-line bg-panel px-3.5 py-2.5 text-sm">
        Not pending · <strong>{view.phase || 'gone'}</strong>
      </p>
    );
  }
  const reasons = row.stage === 'unschedulable' ? (row.verdict.reasons ?? []).slice(0, TOP_REASONS) : [];
  const state = `${STAGE_TEXT[row.stage]} · for ${ago(row.since ?? row.created, view.read)}`;
  const lacks = closestLacks(view);
  return (
    <section data-pod-compact={row.stage} aria-label="Why it waits" className="flex min-w-0 flex-col gap-3 overflow-hidden rounded-lg border border-line bg-panel px-3.5 py-3">
      <div className="flex min-w-0 items-start gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-semibold" title={state}>
          {state}
        </span>
        <button
          type="button"
          aria-label="Expand"
          title="Expand"
          data-expand=""
          onClick={() => setOpen(true)}
          className={`${btnSecondary} shrink-0 px-0 w-8`}
        >
          <span aria-hidden="true">⤢</span>
        </button>
      </div>
      {reasons.length > 0 ? (
        <CompactReasons reasons={reasons} total={row.verdict.total} />
      ) : (
        <p className="truncate text-xs" title={row.message}>
          {row.message}
        </p>
      )}
      {lacks === undefined ? null : (
        <p data-closest="" className="flex min-w-0 items-baseline gap-2 text-xs">
          <span className="min-w-0 shrink truncate font-medium" title={lacks.node}>
            {lacks.node}
          </span>
          <span className="min-w-0 flex-1 truncate text-muted" title={lacks.text}>
            {lacks.text}
          </span>
        </p>
      )}
      {open ? (
        <Dialog title={`Pod Debugging · ${name}`} size="viewport" layer={KeyLayer.Dialog} onClose={() => setOpen(false)}>
          <DebugWindow cluster={cluster} kind="pods" namespace={namespace} name={name} />
        </Dialog>
      ) : null}
    </section>
  );
}

function CompactReasons({ reasons, total }: { reasons: readonly Reason[]; total: number }): ReactNode {
  return (
    <ul data-reasons="" className="flex min-w-0 flex-col gap-1.5">
      {reasons.map((r, i) => {
        const all = r.nodes < 0;
        const share = all || total <= 0 ? 100 : Math.max(0, Math.min(100, (r.nodes / total) * 100));
        const count = all ? 'all' : total > 0 ? `${r.nodes}/${total}` : String(r.nodes);
        return (
          <li key={i} className="flex min-w-0 flex-col gap-0.5 text-xs">
            <span className="flex min-w-0 items-baseline gap-2">
              <span className={`size-2 shrink-0 rounded-full ${CATEGORY_DOT[r.category]}`} />
              <span className="min-w-0 flex-1 truncate" title={r.text}>
                {r.text}
              </span>
              <span className="shrink-0 tabular-nums text-muted">{count}</span>
            </span>
            <span className="relative block h-1.5 rounded-full bg-white/10">
              <span className={`absolute inset-y-0 left-0 rounded-full ${CATEGORY_DOT[r.category]}`} style={{ width: `${share}%` }} />
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/** The closest node and what it fails: room as `CPU 2 · 1.4 free`, any other check by name. */
export function closestLacks(view: PodViewData): { node: string; text: string } | undefined {
  const row = view.row;
  const fit: Fit | undefined = view.fits[0];
  if (row === undefined) return undefined;
  if (fit === undefined) return row.closest === undefined ? undefined : { node: row.closest.node, text: `${row.closest.failing} failed` };
  const bar = view.bars.find((b) => b.name === fit.node);
  const parts: string[] = [];
  for (const cell of fit.cells) {
    if (cell.ok !== false) continue;
    const column = view.columns.find((c) => c.key === cell.key);
    const label = column?.label ?? cell.key;
    const resource = cell.key.startsWith('resource:') ? cell.key.slice('resource:'.length) : '';
    const need = resource === '' ? undefined : row.request[resource];
    const gauge = resource === 'cpu' ? bar?.cpu : resource === 'memory' ? bar?.memory : undefined;
    if (need === undefined || gauge === undefined) {
      parts.push(label);
    } else {
      parts.push(`${label} ${format(resource, need)} · ${format(resource, Math.max(gauge.allocatable - gauge.requested, 0))} free`);
    }
  }
  return { node: fit.node, text: parts.slice(0, 2).join(' · ') };
}
