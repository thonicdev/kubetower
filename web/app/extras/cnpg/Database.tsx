import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Pending, Pill, btnSecondary, focusRing } from '@kubetower/ui';

import { statsQuery, tablesQuery, type Stats } from './data';
import { bytes } from './parts';
import { Problem } from './states';
import { TableBrowser } from './TableBrowser';

/**
 * What is inside the cluster: its statistics and its tables, read with the
 * cluster's own password through a tunnel (`extras/cnpg/sqlconn.go`) - so
 * only once asked, by Connect, and once each: no interval.
 */
export function Database({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }): ReactNode {
  const [on, setOn] = useState(false);
  const SECTION = 'flex flex-col gap-2 rounded-lg border border-line bg-panel p-3';
  if (!on) {
    return (
      <section className={SECTION} aria-label="Database" data-database="off">
        <div className="flex items-center gap-3">
          <h2 className="text-sm font-medium">Database</h2>
          <button
            type="button"
            className={btnSecondary}
            onClick={() => setOn(true)}
            data-connect=""
            title={`Port-forward to ${namespace}/${name}-rw and read the password of its application Secret, for you; statements are read-only.`}
          >
            Connect
          </button>
          <span className="text-xs text-muted">statistics · tables</span>
        </div>
      </section>
    );
  }
  return <Connected cluster={cluster} namespace={namespace} name={name} section={SECTION} />;
}

function Connected({ cluster, namespace, name, section }: { cluster: string; namespace: string; name: string; section: string }): ReactNode {
  const stats = useQuery(statsQuery(cluster, namespace, name));
  const tables = useQuery(tablesQuery(cluster, namespace, name));
  return (
    <>
      <section className={section} aria-label="Statistics" data-stats="">
        <h2 className="flex items-center gap-2 text-sm font-medium">
          Statistics
          {stats.data !== undefined ? <Verified verified={stats.data.verified} /> : null}
          <button type="button" className={`ml-auto text-xs text-accent hover:underline ${focusRing}`} onClick={() => void stats.refetch()} disabled={stats.isFetching}>
            {stats.isFetching ? 'Reading…' : 'Read again'}
          </button>
        </h2>
        {stats.data !== undefined ? (
          <StatsView stats={stats.data} />
        ) : stats.isError ? (
          <Problem error={stats.error} onRetry={() => void stats.refetch()} />
        ) : (
          <Pending what="the statistics" />
        )}
      </section>
      <section className={section} aria-label="Tables" data-tables="">
        <h2 className="text-sm font-medium">Tables{tables.data !== undefined ? <span className="ml-2 text-xs font-normal text-muted">{tables.data.database}</span> : null}</h2>
        {tables.data !== undefined ? (
          <TableBrowser cluster={cluster} namespace={namespace} name={name} first={tables.data} />
        ) : tables.isError ? (
          <Problem error={tables.error} onRetry={() => void tables.refetch()} />
        ) : (
          <Pending what="the tables" />
        )}
      </section>
    </>
  );
}

function Verified({ verified }: { verified: boolean }): ReactNode {
  return verified ? (
    <Pill tone="ok" title="The server's certificate was checked against the cluster's CA.">
      TLS
    </Pill>
  ) : (
    <Pill tone="warn" title="Encrypted, and the certificate was not checked: the cluster's CA Secret could not be read. The tunnel ends on the instance the port-forward chose.">
      TLS unverified
    </Pill>
  );
}

// --- statistics ---------------------------------------------------------------------

function Figure({ label, value, title, tone }: { label: string; value: string; title?: string | undefined; tone?: string | undefined }): ReactNode {
  return (
    <div className="flex min-w-28 flex-col gap-0.5 rounded-md border border-line bg-panel-2 px-3 py-2" title={title} data-figure={label}>
      <span className="text-2xs tracking-eyebrow text-muted uppercase">{label}</span>
      <span className={`text-lg tabular-nums ${tone ?? 'text-fg'}`}>{value}</span>
    </div>
  );
}

function percent(ratio: number): string {
  return `${(ratio * 100).toFixed(1)}%`;
}

function StatsView({ stats: s }: { stats: Stats }): ReactNode {
  const used = s.maxConnections > 0 ? s.connections / s.maxConnections : 0;
  const largest = Math.max(1, ...s.databases.map((d) => d.bytes));
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-2">
        <Figure
          label="Connections"
          value={`${String(s.connections)} / ${String(s.maxConnections)}`}
          title="Client backends over max_connections"
          tone={used >= 0.9 ? 'text-bad' : used >= 0.75 ? 'text-warn' : undefined}
        />
        <Figure
          label="Cache hit"
          value={s.cacheHit === undefined ? '—' : percent(s.cacheHit)}
          title={s.cacheHit === undefined ? 'Nothing read yet' : 'Blocks found in shared buffers over blocks asked, every database'}
          tone={s.cacheHit !== undefined && s.cacheHit < 0.9 ? 'text-warn' : undefined}
        />
        <Figure label="Commits" value={s.commits.toLocaleString('en-US')} title="Transactions committed since the statistics were reset" />
        <Figure label="Rollbacks" value={s.rollbacks.toLocaleString('en-US')} title="Transactions rolled back since the statistics were reset" />
        <Figure label="Deadlocks" value={String(s.deadlocks)} tone={s.deadlocks > 0 ? 'text-warn' : undefined} />
        <Figure label="Version" value={s.version} title={s.startedAt === undefined ? undefined : `Started ${s.startedAt}`} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <div className="flex flex-col gap-1" data-db-sizes="">
          <span className="text-2xs tracking-eyebrow text-muted uppercase">Databases</span>
          {s.databases.map((d) => (
            <div key={d.name} className="flex items-center gap-2 text-xs" data-db={d.name}>
              <span className="w-32 truncate" title={d.name}>
                {d.name}
              </span>
              <span className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-white/10">
                <span className="block h-full rounded-full bg-accent" style={{ width: `${String(Math.max(1, (d.bytes / largest) * 100))}%` }} />
              </span>
              <span className="w-20 text-right text-muted tabular-nums">{bytes(d.bytes)}</span>
            </div>
          ))}
        </div>
        <div className="flex flex-col gap-1" data-replicas="">
          <span className="text-2xs tracking-eyebrow text-muted uppercase">Standbys</span>
          {s.replicas.length === 0 ? <span className="text-xs text-muted">none streaming</span> : null}
          {s.replicas.map((r) => (
            <div key={r.name} className="flex items-center gap-2 text-xs" data-replica={r.name}>
              <span className="w-32 truncate">{r.name}</span>
              <span className="text-muted">{r.state}</span>
              <span className="ml-auto tabular-nums" title={r.lagBytes === undefined ? 'The lag needs the pg_monitor role, which the application user does not hold.' : 'WAL not yet replayed'}>
                {r.lagBytes === undefined ? '—' : bytes(r.lagBytes)}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// --- tables ---------------------------------------------------------------------------
