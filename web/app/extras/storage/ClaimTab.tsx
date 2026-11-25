import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { Pending, Pill, focusRing, type ExtensionTabProps } from '@kubetower/ui';

import { age, bytes, claimQuery, objectHref, type Claim, type ClaimAnswer } from './data';
import { FillBar, OrphanPill, ProblemNotice, SectionBadge, SourceLine } from './parts';

/**
 * The Storage tab in a PersistentVolumeClaim's panel: how full it is, what
 * mounts it, its volume and class, and its snapshots, newest first.
 */
export default function ClaimTab({ cluster, namespace, name }: ExtensionTabProps): ReactNode {
  const query = useQuery({ ...claimQuery(cluster, namespace ?? '', name), enabled: namespace !== undefined });
  if (query.isError) return <ProblemNotice error={query.error} onRetry={() => void query.refetch()} />;
  if (query.data === undefined) return <Pending what="its storage" />;
  if (query.data === null) {
    return (
      <p role="status" data-state="none" className="text-sm text-muted">
        Not read
      </p>
    );
  }
  return <ClaimCard cluster={cluster} answer={query.data} />;
}

export function ClaimCard({ cluster, answer }: { cluster: string; answer: ClaimAnswer }): ReactNode {
  const c = answer.claim;
  return (
    <div className="flex flex-col gap-3" data-storage-claim={`${c.namespace}/${c.name}`}>
      <ClaimHead claim={c} />
      <SourceLine fill={answer.fill} />
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
        <dt className="text-muted">Size</dt>
        <dd className="tabular-nums">
          {bytes(c.capacityBytes || c.requestedBytes)} <span className="text-muted">{c.accessModes.join(' ')}</span>
        </dd>
        <dt className="text-muted">Volume</dt>
        <dd>
          {answer.volume === undefined ? (
            (c.volume ?? '—')
          ) : (
            <Link to={objectHref(cluster, 'persistentvolumes', '', answer.volume.name)} className={`hover:underline ${focusRing}`}>
              {answer.volume.name}
            </Link>
          )}
          {answer.volume?.driver === undefined ? null : <span className="text-muted"> · {answer.volume.driver}</span>}
        </dd>
        <dt className="text-muted">Class</dt>
        <dd>
          {c.class ?? '—'}
          {answer.class === undefined ? null : <span className="text-muted"> · {answer.class.provisioner}</span>}
        </dd>
        <dt className="text-muted">Reclaim</dt>
        <dd className={c.reclaim === 'Retain' ? 'text-warn' : ''}>{c.reclaim ?? '—'}</dd>
        <dt className="text-muted">Mounted by</dt>
        <dd>
          {c.mount === 'unknown' ? (
            <SectionBadge section={answer.mountsRead} />
          ) : c.pods.length === 0 ? (
            '—'
          ) : (
            <span className="flex flex-wrap gap-x-2">
              {c.pods.map((pod) => (
                <Link key={pod} to={objectHref(cluster, 'pods', c.namespace, pod)} className={`hover:underline ${focusRing}`}>
                  {pod}
                </Link>
              ))}
              {(c.morePods ?? 0) > 0 ? <span className="text-muted">+{c.morePods}</span> : null}
            </span>
          )}
        </dd>
        <dt className="text-muted">Age</dt>
        <dd className="tabular-nums">{age(c.created, answer.now)}</dd>
      </dl>
      <section aria-label="Snapshots" className="flex flex-col gap-1">
        <h3 className="flex items-center gap-2 text-xs font-medium text-muted uppercase">
          Snapshots <SectionBadge section={answer.snapshots} />
        </h3>
        {answer.snapshots.state !== 'read' ? null : answer.history.length === 0 ? (
          <p data-state="empty" className="text-xs text-muted">
            None
          </p>
        ) : (
          <ul className="flex flex-col divide-y divide-line rounded-md border border-line text-xs">
            {answer.history.map((s) => (
              <li key={s.name} data-snapshot={s.name} className="flex flex-wrap items-center gap-2 px-2 py-1.5">
                <span className="font-medium">{s.name}</span>
                <Pill tone={s.ready ? 'ok' : 'warn'}>{s.ready ? 'ready' : 'not ready'}</Pill>
                {s.sizeBytes === undefined ? null : <span className="tabular-nums text-muted">{bytes(s.sizeBytes)}</span>}
                <span className="ml-auto tabular-nums text-muted" title={s.at}>
                  {age(s.at, answer.now)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

export function ClaimHead({ claim }: { claim: Claim }): ReactNode {
  return (
    <header className="flex flex-wrap items-center gap-2">
      <span className="text-sm font-medium">{claim.name}</span>
      <Pill tone={claim.phase === 'Bound' ? 'ok' : claim.phase === 'Lost' ? 'bad' : 'warn'}>{claim.phase}</Pill>
      <OrphanPill orphan={claim.orphan} />
      <span className="ml-auto flex min-w-0 basis-40 grow">
        <FillBar fill={claim.fill} wide />
      </span>
    </header>
  );
}
