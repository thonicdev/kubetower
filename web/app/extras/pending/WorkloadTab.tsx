import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { Failure as Failed, Pending as Waiting, focusRing, type ExtensionTabProps } from '@kubetower/ui';

import { ago, podHref, workloadQuery, type Row, type WorkloadView } from './data';
import { CategoryChip, QuotaBar, SectionNote } from './parts';

/**
 * The Pending tab of a Deployment, StatefulSet, DaemonSet or Job: its pending
 * pods, longest waiting first, each with its main reason and a link to the
 * pod's own view, and above them the refusal of its controller when a quota or
 * a policy kept a pod from being created. From one read, asked again every
 * thirty seconds only while it is on screen.
 */
export default function WorkloadTab({ cluster, kind, namespace, name }: ExtensionTabProps): ReactNode {
  const query = useQuery({ ...workloadQuery(cluster, kind, namespace ?? '', name), enabled: namespace !== undefined });
  if (namespace === undefined) return null;
  if (query.data === undefined) {
    return (
      <div data-pending-workload="">
        {query.isError ? <Failed error={query.error} onRetry={() => void query.refetch()} /> : <Waiting what="its pending pods" />}
      </div>
    );
  }
  return (
    <div data-pending-workload="" className="flex flex-col gap-3">
      <WorkloadBody cluster={cluster} view={query.data} />
    </div>
  );
}

export function WorkloadBody({ cluster, view }: { cluster: string; view: WorkloadView }): ReactNode {
  const failure = view.failure;
  const empty = view.rows.length === 0 && failure === undefined;
  return (
    <>
      <SectionNote what="Pods" section={view.pods} />
      <SectionNote what="Refusals" section={view.failuresRead} />
      {failure === undefined ? null : (
        <section data-failure={failure.category} aria-label="Cannot create pods" className="flex flex-col gap-1.5 rounded-lg border border-bad/40 bg-panel px-3.5 py-3">
          <div className="flex flex-wrap items-baseline gap-2">
            <span className="text-sm font-semibold text-bad">Cannot create pods</span>
            <span className="rounded-full border border-bad/50 px-2 text-2xs text-bad">{failure.category}</span>
            <span className="ml-auto text-2xs text-muted" title="How many times, and when last">
              ×{failure.count} · {ago(failure.last, view.read)}
            </span>
          </div>
          <p className="text-xs text-muted">{failure.message}</p>
          {failure.quota?.hard === undefined || failure.quota.used === undefined ? null : <QuotaBar quota={failure.quota} />}
        </section>
      )}
      {view.rows.length === 0 ? null : (
        <ul aria-label="Pending pods" className="flex flex-col rounded-lg border border-line bg-panel">
          {view.rows.map((row) => (
            <PodLine key={row.name} cluster={cluster} row={row} read={view.read} />
          ))}
        </ul>
      )}
      {view.capped ? <p className="text-2xs text-muted">First {view.rows.length} pods</p> : null}
      {empty && view.pods.state === 'read' ? (
        <p data-nothing-pending="" className="text-sm text-muted">
          Nothing pending
        </p>
      ) : null}
    </>
  );
}

function PodLine({ cluster, row, read }: { cluster: string; row: Row; read: string }): ReactNode {
  const first = row.verdict.reasons?.[0];
  return (
    <li data-pending-pod={row.name} className="flex flex-wrap items-center gap-2 border-t border-line px-3 py-2 text-xs first:border-t-0">
      <Link to={podHref(cluster, row.namespace, row.name)} title={row.name} className={`w-48 shrink-0 truncate font-semibold text-accent hover:underline ${focusRing}`}>
        {row.name}
      </Link>
      <CategoryChip category={row.main} main />
      {row.closest === undefined ? null : (
        <span className="text-2xs text-muted">
          {row.closest.node} · {row.closest.failing} failed
        </span>
      )}
      <span className="min-w-0 flex-1 truncate text-2xs text-muted" title={row.message}>
        {first?.text ?? row.message}
      </span>
      <span className="ml-auto text-2xs tabular-nums text-muted">{ago(row.since ?? row.created, read)}</span>
    </li>
  );
}
