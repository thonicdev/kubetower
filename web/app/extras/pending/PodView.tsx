import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Failure, GoneNotice, isGoneProblem, Pending as Waiting } from '@kubetower/ui';

import { ago, format, podQuery, type PodView as PodViewData } from './data';
import { CategoryChip, Matrix, NodeBars, PreemptionStrip, ReasonBars, SectionNote, Timeline } from './parts';

/**
 * One pod: why it waits, what it would take, which nodes come closest and on
 * what, and its events. Drawn by the pod's own page and by the tab in its
 * panel, from one read (`GET /pod/{namespace}/{name}`), asked again every
 * thirty seconds only while it is on screen.
 */
export function PodView({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }): ReactNode {
  const query = useQuery(podQuery(cluster, namespace, name));
  if (query.data === undefined) {
    if (query.isError && isGoneProblem(query.error, ['no_such_pod'])) {
      return <GoneNotice what="Pod" detail={query.error instanceof Error ? query.error.message : undefined} />;
    }
    return query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : <Waiting what="why this pod waits" />;
  }
  return <PodBody view={query.data} />;
}

export const STAGE_TEXT = {
  unschedulable: 'Waiting for a node',
  gated: 'Held by scheduling gates',
  unseen: 'Not seen by a scheduler',
  starting: 'Placed, not started',
} as const;

export function PodBody({ view }: { view: PodViewData }): ReactNode {
  const row = view.row;
  if (!view.pending || row === undefined) {
    return (
      <p data-not-pending="" className="rounded-lg border border-line bg-panel px-3.5 py-2.5 text-sm">
        Not pending · <strong>{view.phase || 'gone'}</strong>
      </p>
    );
  }
  const reasons = row.verdict.reasons ?? [];
  const request = Object.entries(row.request).filter(([, v]) => v > 0);
  return (
    <div data-pod-view={row.stage} className="flex flex-col gap-4">
      <section aria-label="Why it waits" className="flex flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-semibold">{STAGE_TEXT[row.stage]}</span>
          <CategoryChip category={row.main} main />
          {row.categories
            .filter((c) => c !== row.main)
            .map((c) => (
              <CategoryChip key={c} category={c} />
            ))}
          <span className="ml-auto text-xs text-muted">for {ago(row.since ?? row.created, view.read)}</span>
        </div>
        {reasons.length > 0 && row.stage === 'unschedulable' ? (
          <ReasonBars reasons={reasons} total={row.verdict.total} />
        ) : (
          <p className="text-xs">{row.message}</p>
        )}
        {request.length > 0 ? (
          <p className="text-2xs text-muted">
            Request {request.map(([k, v]) => `${format(k, v)} ${k === 'cpu' ? 'CPU' : k}`).join(', ')}
            {row.owner === undefined ? '' : ` · ${row.owner.kind} ${row.owner.name}`}
            {row.node === undefined || row.node === '' ? '' : ` · on ${row.node}`}
          </p>
        ) : null}
        {row.preemption === undefined ? null : <PreemptionStrip preemption={row.preemption} />}
      </section>

      <section aria-label="What it would take" data-would-take="" className="flex flex-col gap-1.5 rounded-lg border border-accent/40 bg-panel-2 px-3.5 py-3">
        <h3 className="text-sm font-semibold" title="Inferred from the specs and the scheduler's message, not a simulation of the scheduler">
          What it would take
        </h3>
        <ul className="flex list-disc flex-col gap-1 pl-4 text-xs">
          {view.wouldTake.map((line, i) => (
            <li key={i}>{line}</li>
          ))}
        </ul>
      </section>

      <SectionNote what="Nodes" section={view.nodes} />
      <SectionNote what="Pods on the nodes" section={view.placed} />
      <SectionNote what="Claims" section={view.claimsRead} />

      {view.fits.length > 0 ? (
        <div className="grid gap-4 xl:grid-cols-2">
          <section aria-label="Node fit" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
            <h3 className="text-sm font-semibold">Node fit</h3>
            <Matrix columns={view.columns} fits={view.fits} total={view.nodesTotal} {...(row.preemption?.nominated === undefined || row.preemption.nominated === '' ? {} : { nominated: row.preemption.nominated })} />
          </section>
          <section aria-label="Room on the closest nodes" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
            <h3 className="text-sm font-semibold">Room on the closest nodes</h3>
            <NodeBars bars={view.bars} request={row.request} />
          </section>
        </div>
      ) : null}

      <section aria-label="Events" className="flex flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
        <h3 className="text-sm font-semibold">Events</h3>
        <SectionNote what="Events" section={view.eventsRead} />
        <Timeline events={view.events} created={row.created} read={view.read} />
        {view.eventsCapped ? <p className="text-2xs text-muted">First 200 events</p> : null}
      </section>
    </div>
  );
}
