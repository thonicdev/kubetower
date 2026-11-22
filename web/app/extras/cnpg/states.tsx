import type { ReactNode } from 'react';
import { CopyButton, Failure } from '@kubetower/ui';

import { CnpgProblem, type Source } from './data';

/**
 * The three answers that are not a page: CloudNativePG is not here, it is
 * here and you may not read it, or the read failed. Drawn three ways, never
 * one as another - an empty page for a refusal tells the reader the cluster
 * runs no Postgres.
 */

/** What an administrator grants for a person to read the operator's
 *  resources: Kubernetes' own view role does not. */
export const GRANT = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cnpg-reader
rules:
  - apiGroups: [postgresql.cnpg.io]
    resources: [clusters, backups, scheduledbackups, poolers, databases, publications, subscriptions]
    verbs: [get, list]`;

export function Problem({ error, onRetry }: { error: unknown; onRetry?: () => void }): ReactNode {
  if (!(error instanceof CnpgProblem) || error.kind === 'failure') {
    return <Failure error={error} onRetry={onRetry} />;
  }
  if (error.kind === 'absent') {
    const notInstalled = error.code === 'kind_unknown';
    return (
      <section className="flex flex-col gap-2 rounded-lg border border-line bg-panel px-4 py-3 text-sm" data-state="absent" data-code={error.code}>
        <p className="font-medium" title={notInstalled ? 'No custom resource kind answers to clusters.postgresql.cnpg.io.' : undefined}>
          {notInstalled ? 'CloudNativePG is not installed.' : error.message}
        </p>
      </section>
    );
  }
  return (
    <section className="flex flex-col gap-2 rounded-lg border border-warn/45 bg-warn/10 px-4 py-3 text-sm" data-state="refused" data-code={error.code}>
      <p className="font-medium" title={error.detail}>
        {error.message}
      </p>
      {error.code === 'list_forbidden' && error.message.includes('Clusters') ? (
        <>
          <div className="flex items-start gap-2" title="Kubernetes' view role does not cover postgresql.cnpg.io: a ClusterRole like this one, bound to you, does.">
            <pre className="min-w-0 flex-1 overflow-x-auto rounded border border-line bg-bg p-2 text-xs">{GRANT}</pre>
            <CopyButton value={GRANT} label="Copy the ClusterRole" />
          </div>
        </>
      ) : null}
    </section>
  );
}

const KIND_WORD: Readonly<Record<string, string>> = {
  'backups.postgresql.cnpg.io': 'Backups',
  'scheduledbackups.postgresql.cnpg.io': 'Scheduled backups',
  'poolers.postgresql.cnpg.io': 'Poolers',
  'databases.postgresql.cnpg.io': 'Databases',
  'publications.postgresql.cnpg.io': 'Publications',
  'subscriptions.postgresql.cnpg.io': 'Subscriptions',
  pods: 'Pods',
  events: 'Events',
};

/** The notes under the tiles: a list cut short, and every secondary read
 *  that was refused or failed. A kind not served here says nothing. */
export function Sources({ overview }: { overview: { complete: boolean; note?: string | undefined; sources: readonly Source[] } }): ReactNode {
  const notes: string[] = [];
  if (!overview.complete) notes.push(overview.note ?? 'The list of clusters was cut short: the counts are floors.');
  for (const s of overview.sources) {
    const word = KIND_WORD[s.kind] ?? s.kind;
    if (s.state === 'refused' || s.state === 'failed') notes.push(`${word}: ${s.note ?? s.state}`);
    else if (s.state === 'read' && !s.complete && s.note !== undefined && s.note !== '') notes.push(`${word}: ${s.note}`);
  }
  if (notes.length === 0) return null;
  return (
    <ul className="flex flex-col gap-0.5 text-xs text-warn" data-notes="">
      {notes.map((n) => (
        <li key={n}>{n}</li>
      ))}
    </ul>
  );
}
