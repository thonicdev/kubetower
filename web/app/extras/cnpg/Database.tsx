import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Pending, Pill, btnSecondary, focusRing } from '@kubetower/ui';

import { rowsQuery, statsQuery, tablesQuery, type Cell, type Stats, type TableRow, type Tables } from './data';
import { bytes } from './parts';
import { Problem } from './states';

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
          <TablesView cluster={cluster} namespace={namespace} name={name} tables={tables.data} />
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

const KIND_WORD: Readonly<Record<TableRow['kind'], string>> = {
  table: '',
  partitioned: 'partitioned',
  view: 'view',
  materialized: 'mat. view',
  foreign: 'foreign',
};

function TablesView({ cluster, namespace, name, tables: t }: { cluster: string; namespace: string; name: string; tables: Tables }): ReactNode {
  const [picked, setPicked] = useState<{ schema: string; table: string } | undefined>(undefined);
  if (t.tables.length === 0) return <p className="text-xs text-muted">no table you may read</p>;
  return (
    <div className="grid grid-cols-1 gap-3 xl:grid-cols-3">
      <ul className="flex max-h-96 flex-col overflow-y-auto rounded-md border border-line text-xs xl:col-span-1" data-table-list="">
        {t.tables.map((row) => {
          const id = `${row.schema}.${row.name}`;
          const on = picked?.schema === row.schema && picked.table === row.name;
          return (
            <li key={id}>
              <button
                type="button"
                data-table={id}
                onClick={() => setPicked({ schema: row.schema, table: row.name })}
                className={`flex w-full items-center gap-2 px-2 py-1 text-left hover:bg-panel-2 ${on ? 'bg-accent/15' : ''} ${focusRing}`}
              >
                <span className="min-w-0 flex-1 truncate" title={id}>
                  {t.schemas.length > 1 || row.schema !== 'public' ? <span className="text-muted">{row.schema}.</span> : null}
                  {row.name}
                </span>
                {KIND_WORD[row.kind] === '' ? null : <span className="text-2xs text-muted">{KIND_WORD[row.kind]}</span>}
                <span className="w-14 text-right text-muted tabular-nums" title="Postgres' estimate of its rows">
                  {row.rows < 0 ? '?' : row.rows.toLocaleString('en-US')}
                </span>
                <span className="w-16 text-right text-muted tabular-nums">{bytes(row.bytes)}</span>
              </button>
            </li>
          );
        })}
        {t.complete ? null : <li className="px-2 py-1 text-2xs text-muted">first {t.tables.length} tables</li>}
      </ul>
      {picked === undefined ? (
        <p className="self-center text-xs text-muted xl:col-span-2">pick a table</p>
      ) : (
        <RowsView cluster={cluster} namespace={namespace} name={name} schema={picked.schema} table={picked.table} />
      )}
    </div>
  );
}

function RowsView({ cluster, namespace, name, schema, table }: { cluster: string; namespace: string; name: string; schema: string; table: string }): ReactNode {
  const query = useQuery(rowsQuery(cluster, namespace, name, schema, table));
  if (query.isError) return <Problem error={query.error} onRetry={() => void query.refetch()} />;
  const r = query.data;
  if (r === undefined) return <Pending what={`${schema}.${table}`} />;
  return (
    <div className="flex min-w-0 flex-col gap-1 xl:col-span-2" data-rows={`${schema}.${table}`}>
      <div className="max-h-96 overflow-auto rounded-md border border-line">
        <table className="w-max min-w-full border-collapse font-mono text-xs">
          <thead className="sticky top-0 bg-panel-2">
            <tr>
              {r.columns.map((c) => (
                <th key={c.name} className="border-b border-line px-2 py-1 text-left font-sans font-medium whitespace-nowrap" title={c.type}>
                  {c.name}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {r.rows.map((row, i) => (
              <tr key={i} className="border-t border-line align-top">
                {row.map((cell, j) => (
                  <CellView key={j} cell={cell} />
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="flex gap-3 text-2xs text-muted">
        <span>{r.more ? `first ${String(r.rows.length)} rows` : `${String(r.rows.length)} rows`}</span>
        {r.columnsTotal > r.columns.length ? <span>first {r.columns.length} of {r.columnsTotal} columns</span> : null}
      </p>
    </div>
  );
}

function CellView({ cell }: { cell: Cell }): ReactNode {
  if (cell.null === true) return <td className="px-2 py-0.5 font-sans text-muted italic">null</td>;
  if (cell.bytes !== undefined) return <td className="px-2 py-0.5 font-sans text-muted">{bytes(cell.bytes)}</td>;
  const text = cell.text ?? '';
  return (
    <td className="max-w-80 truncate px-2 py-0.5" title={cell.cut === true ? `${text}…` : text.length > 40 ? text : undefined} data-cut={cell.cut === true ? '' : undefined}>
      {text}
      {cell.cut === true ? '…' : ''}
    </td>
  );
}
