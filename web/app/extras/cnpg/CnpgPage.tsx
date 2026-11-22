import { useState, type ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useParams, useSearchParams } from 'react-router';
import { FilterTile, Pending, Pill, Tile, TileGrid, btnSecondary, useDeclarePageFacts } from '@kubetower/ui';

import { clusterHref, overviewQuery, type Overview, type Summary } from './data';
import { ArchivePill, BackupPill, CertPill, HEALTH_LABEL, HEALTH_TONE, ReadyRing, Topology, healthPill, major } from './parts';
import { Problem, Sources } from './states';

/**
 * The Postgres (CNPG) page: one card per CloudNativePG cluster, the worst
 * first, under a row of tiles that each filter the cards.
 *
 * Read from `GET /x/{cluster}/cnpg/`, again every 30 s while the page is open
 * and the browser tab visible.
 */

/** The filters, as the tiles and the address name them. */
export const FILTERS = ['all', 'degraded', 'archive', 'backup', 'switchover', 'certs'] as const;
export type Filter = (typeof FILTERS)[number];

/** How many cards are drawn before "Show more": a page of 300 clusters is
 *  read whole and drawn in steps, so the browser lays out a bounded number. */
export const PAGE_SIZE = 120;

export function matches(s: Summary, f: Filter): boolean {
  switch (f) {
    case 'degraded':
      return s.health === 'degraded' || s.health === 'failing';
    case 'archive':
      return s.archive.state === 'failing';
    case 'backup':
      return s.backup.late;
    case 'switchover':
      return s.health === 'switchover';
    case 'certs':
      return s.certs.state === 'warn' || s.certs.state === 'bad';
    default:
      return true;
  }
}

export function CnpgPage(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'Postgres (CNPG)' });
  const query = useQuery(overviewQuery(cluster));

  return (
    <div className="flex flex-col gap-4 p-4" data-cnpg-page="" aria-busy={query.isFetching}>
      <h1 className="text-xl font-medium">Postgres (CNPG)</h1>
      {query.data !== undefined ? (
        <Board overview={query.data} cluster={cluster} />
      ) : query.isError ? (
        <Problem error={query.error} onRetry={() => void query.refetch()} />
      ) : (
        <Pending what="CloudNativePG's clusters" />
      )}
    </div>
  );
}

function Board({ overview, cluster }: { overview: Overview; cluster: string }): ReactNode {
  const [params, setParams] = useSearchParams();
  const raw = params.get('show') ?? 'all';
  const filter: Filter = (FILTERS as readonly string[]).includes(raw) ? (raw as Filter) : 'all';
  const [shown, setShown] = useState(PAGE_SIZE);
  const now = Date.parse(overview.now);
  const c = overview.counts;
  const floor = overview.complete ? '' : '≥ ';

  if (overview.clusters.length === 0) {
    return (
      <section className="flex flex-col gap-2" data-empty="">
        <p className="rounded-lg border border-line bg-panel px-4 py-3 text-sm">No Postgres cluster{overview.complete ? '' : ' you may read'}.</p>
        <Sources overview={overview} />
      </section>
    );
  }

  const choose = (f: Filter) => () => {
    const next = new URLSearchParams(params);
    if (f === 'all' || f === filter) next.delete('show');
    else next.set('show', f);
    setParams(next, { replace: true });
    setShown(PAGE_SIZE);
  };
  const tile = (f: Filter, count: number, label: string, tone: 'ok' | 'warn' | 'bad' | 'info' | undefined, hint: string) => (
    <FilterTile
      key={f}
      data-filter={f}
      count={`${count > 0 ? floor : ''}${String(count)}`}
      label={label}
      tone={count === 0 && f !== 'all' ? undefined : tone}
      pressed={filter === f}
      onClick={choose(f)}
      hint={hint}
    />
  );
  const list = overview.clusters.filter((s) => matches(s, filter));

  return (
    <>
      <TileGrid density="compact" label="Filter the clusters">
        {tile('all', c.clusters, 'Clusters', undefined, 'Every cluster')}
        {tile('degraded', c.degraded, 'Degraded', 'bad', 'Fewer instances ready than asked for, an instance failed, or the operator cannot proceed')}
        {tile('archive', c.archiveFailing, 'Archive failing', 'bad', 'WAL archiving is configured and failing')}
        {tile('backup', c.backupLate, 'Backup > 1 day', 'warn', `Backed up, and no successful backup in the last ${String(overview.staleAfterHours)} h (a fixed threshold)`)}
        {tile('switchover', c.switchover, 'Switchover', 'info', 'A switchover or a failover is under way')}
        {tile('certs', c.certsSoon, `Certs < ${String(overview.certWarnDays)} days`, 'warn', `A certificate expired or expiring within ${String(overview.certWarnDays)} days: the operator renews its own 7 days ahead`)}
      </TileGrid>
      <Sources overview={overview} />
      {list.length === 0 ? (
        <p className="text-sm text-muted" data-none="">
          No match.
        </p>
      ) : (
        <TileGrid label="CloudNativePG clusters">
          {list.slice(0, shown).map((s) => (
            <ClusterCard key={`${s.namespace}/${s.name}`} s={s} cluster={cluster} now={now} />
          ))}
        </TileGrid>
      )}
      {list.length > shown ? (
        <button type="button" className={`${btnSecondary} self-start`} onClick={() => setShown((n) => n + PAGE_SIZE)} data-more="">
          Show {String(Math.min(PAGE_SIZE, list.length - shown))} more of {String(list.length - shown)}
        </button>
      ) : null}
    </>
  );
}

/** One cluster: its topology, its ready ring and its chips. */
export function ClusterCard({ s, cluster, now }: { s: Summary; cluster: string; now: number }): ReactNode {
  const version = major(s.image);
  return (
    <Tile
      data-cluster-card={`${s.namespace}/${s.name}`}
      title={s.name}
      to={clusterHref(cluster, s.namespace, s.name)}
      tone={HEALTH_TONE[s.health]}
      badges={
        <>
          <Pill tone={healthPill(s.health)} title={s.phaseReason ?? s.phase}>
            {HEALTH_LABEL[s.health]}
          </Pill>
          {version === undefined ? null : <Pill neutral="muted">PG {version}</Pill>}
        </>
      }
      detail={`${s.namespace}${s.primary === undefined ? '' : ` · primary ${s.primary}`}`}
    >
      <div className="flex items-center gap-3">
        <ReadyRing ready={s.ready} expected={s.expected} />
        <Topology instances={s.instances} timeline={s.timeline} />
        <div className="flex min-w-0 flex-wrap gap-1">
          <BackupPill s={s} now={now} />
          <ArchivePill s={s} />
          <CertPill s={s} now={now} />
        </div>
      </div>
    </Tile>
  );
}
