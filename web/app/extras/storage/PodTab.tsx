import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { Pending, focusRing, type ExtensionTabProps } from '@kubetower/ui';

import { age, bytes, objectHref, podQuery } from './data';
import { ClaimHead } from './ClaimTab';
import { ProblemNotice, SourceLine } from './parts';

/** The Storage tab in a Pod's panel: each claim it mounts, and how full. */
export default function PodTab({ cluster, namespace, name }: ExtensionTabProps): ReactNode {
  const query = useQuery({ ...podQuery(cluster, namespace ?? '', name), enabled: namespace !== undefined });
  if (query.isError) return <ProblemNotice error={query.error} onRetry={() => void query.refetch()} />;
  if (query.data === undefined) return <Pending what="its claims" />;
  const answer = query.data;
  if (answer.claims.length === 0) {
    return (
      <p role="status" data-state="none" className="text-sm text-muted">
        No claim
      </p>
    );
  }
  return (
    <div className="flex flex-col gap-3" data-storage-pod="">
      <SourceLine fill={answer.fill} />
      {answer.claims.map((c) => (
        <section key={c.name} aria-label={`Claim ${c.name}`} className="flex flex-col gap-1 rounded-lg border border-line bg-panel p-3">
          <ClaimHead claim={c} />
          <p className="flex flex-wrap gap-x-3 text-xs text-muted">
            <Link to={objectHref(cluster, 'persistentvolumeclaims', c.namespace, c.name)} className={`text-accent hover:underline ${focusRing}`}>
              Open claim
            </Link>
            <span className="tabular-nums">{bytes(c.capacityBytes || c.requestedBytes)}</span>
            {c.class === undefined ? null : <span>{c.class}</span>}
            {c.reclaim === undefined ? null : <span className={c.reclaim === 'Retain' ? 'text-warn' : ''}>{c.reclaim}</span>}
            <span title={c.snapshot?.name}>{c.snapshot === undefined ? 'no snapshot' : `snapshot ${age(c.snapshot.at, answer.now)}`}</span>
          </p>
        </section>
      ))}
    </div>
  );
}
