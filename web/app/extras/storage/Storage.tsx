import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import {
  FilterTile,
  HEAD_ROW,
  Pending,
  Pill,
  ROW,
  TABLE,
  TileGrid,
  bodyCell,
  btnSecondary,
  focusRing,
  headCell,
  useDeclarePageFacts,
  type TileTone,
} from '@kubetower/ui';

import {
  age,
  bytes,
  fillKnown,
  objectHref,
  overviewQuery,
  passes,
  showOf,
  type Claim,
  type Class,
  type Overview,
  type Show,
  type Volume,
} from './data';
import { FillBar, FillVia, OrphanPill, ProblemNotice, SectionBadge, SnapshotBadge, SourceLine } from './parts';

/** Rows drawn at once; "Show more" adds as many again. */
export const PAGE_ROWS = 100;

/**
 * The Storage Inspector page, at `/c/:cluster/x/storage`: what storage there
 * is, what is orphaned, what is filling up, and what a snapshot backs.
 *
 * Tiles that filter (`?show=`), a strip of the claims by fill level, then the
 * list the tile picked. Read-only: an orphan links to its own page, where the
 * console's verbs are. One read per scope, no interval.
 */
export function Storage(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  const [params, setParams] = useSearchParams();
  const scope = (params.get('ns') ?? '').trim();
  const show = showOf(params.get('show'));
  useDeclarePageFacts({ title: 'Storage Inspector' });
  const overview = useQuery(overviewQuery(cluster, scope));

  const pick = (next: Show) =>
    setParams(
      (p) => {
        const out = new URLSearchParams(p);
        if (next === 'all') out.delete('show');
        else out.set('show', next);
        return out;
      },
      { replace: true },
    );

  const data = overview.data;
  return (
    <div className="flex flex-col gap-4 p-4" data-storage-page="">
      <header className="flex flex-wrap items-center gap-2">
        <h1 className="text-xl font-medium">Storage Inspector</h1>
        <p className="text-xs text-muted" data-counts="">
          {cluster}
          {scope === '' ? null : <> · {scope}</>}
          {data === undefined ? null : (
            <>
              {' '}
              · {bytes(data.counts.provisionedBytes)} provisioned
            </>
          )}
        </p>
        <span className="flex-1" />
        {scope === '' ? null : (
          <Link to={`/c/${encodeURIComponent(cluster)}/x/storage`} className={`text-xs text-accent underline ${focusRing}`}>
            All namespaces
          </Link>
        )}
        <button type="button" onClick={() => void overview.refetch()} disabled={overview.isFetching} className={btnSecondary}>
          {overview.isFetching ? 'Reading…' : 'Refresh'}
        </button>
      </header>

      {data === undefined ? (
        overview.isError ? (
          <ProblemNotice error={overview.error} onRetry={() => void overview.refetch()} />
        ) : (
          <Pending what="the cluster's storage" />
        )
      ) : (
        <Answer cluster={cluster} data={data} show={show} pick={pick} />
      )}
    </div>
  );
}

interface TileSpec {
  show: Show;
  label: string;
  count: number;
  /** The count is a floor: claims it may hold were not read. */
  floor?: boolean;
  tone: TileTone;
  hint: string;
}

function tiles(data: Overview): TileSpec[] {
  const c = data.counts;
  const full = c.warn + c.bad;
  // No claim measured: how many are full is unknown, never 0.
  const known = fillKnown(data);
  // Claims with no figure may be full: the count is then a floor, never a 0.
  const unread = c.unread;
  const unsnapshotted = data.snapshots.state === 'read' ? c.claims - c.snapshotted : -1;
  return [
    { show: 'all', label: 'Claims', count: c.claims, tone: c.lost > 0 ? 'bad' : c.pending > 0 ? 'warn' : 'off', hint: `${String(c.bound)} bound · ${String(c.pending)} pending · ${String(c.lost)} lost` },
    { show: 'orphans', label: 'Orphans', count: c.orphans, tone: c.orphans > 0 ? 'warn' : 'off', hint: `${String(c.orphanClaims)} claims · ${String(c.orphanVolumes)} volumes (${String(c.retainedOrphans)} Retain) · ${String(c.orphanClasses)} classes` },
    {
      show: 'full',
      label: '≥ 80 %',
      count: known ? full : -1,
      floor: unread > 0,
      tone: c.bad > 0 ? 'bad' : full > 0 ? 'warn' : 'off',
      hint: unread > 0 ? `${String(c.bad)} at 90 % and over · ${String(unread)} unread` : `${String(c.bad)} at 90 % and over`,
    },
    { show: 'unmeasured', label: 'Unread', count: unread, tone: unread > 0 ? 'warn' : 'off', hint: 'Mounted, and no kubelet figure' },
    { show: 'unsnapshotted', label: 'No snapshot', count: unsnapshotted, tone: 'off', hint: 'Claims with no VolumeSnapshot' },
    { show: 'volumes', label: 'Volumes', count: data.volumesRead.state === 'read' ? c.volumes : -1, tone: 'off', hint: 'PersistentVolumes' },
    { show: 'classes', label: 'Classes', count: data.classesRead.state === 'read' ? c.classes : -1, tone: 'off', hint: 'StorageClasses' },
  ];
}

function Answer({ cluster, data, show, pick }: { cluster: string; data: Overview; show: Show; pick: (s: Show) => void }): ReactNode {
  return (
    <>
      {data.notes.length === 0 ? null : (
        <div role="status" data-state="narrowed" className="flex flex-col gap-0.5 text-xs text-warn">
          {data.notes.map((note) => (
            <p key={note}>{note}</p>
          ))}
        </div>
      )}

      <div role="group" aria-label="Filters" className="flex flex-wrap items-center gap-2">
        <TileGrid density="compact" label="What to show">
          {tiles(data).map((t) => (
            <FilterTile
              key={t.show}
              data-filter={t.show}
              data-count={String(t.count)}
              count={t.count < 0 ? '–' : data.complete && t.floor !== true ? t.count : `${String(t.count)}+`}
              label={t.label}
              tone={t.tone}
              pressed={show === t.show}
              onClick={() => pick(show === t.show && t.show !== 'all' ? 'all' : t.show)}
              hint={t.count < 0 ? 'Not read' : t.hint}
            />
          ))}
        </TileGrid>
      </div>

      <section aria-label="At a glance" className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border border-line bg-panel p-3">
        <LevelStrip data={data} />
        <span className="flex items-center gap-2 text-xs" data-glance="snapshots">
          <span className="text-muted">Snapshots</span>
          {data.snapshots.state === 'read' ? (
            <span className="tabular-nums">
              {data.snapshots.ready}/{data.snapshots.total}
            </span>
          ) : null}
          <SnapshotBadge snapshots={data.snapshots} />
        </span>
        <span className="flex items-center gap-2 text-xs" data-glance="mounts">
          <span className="text-muted">Mounts</span>
          {data.mountsRead.state === 'read' ? <span>read</span> : <SectionBadge section={data.mountsRead} />}
        </span>
        {(data.fill.via ?? []).length === 0 ? null : (
          <span className="flex items-center gap-2 text-xs" data-glance="fill">
            <span className="text-muted">Fill</span>
            <FillVia fill={data.fill} />
          </span>
        )}
        <SourceLine fill={data.fill} />
      </section>

      {show === 'orphans' ? (
        <Orphans cluster={cluster} data={data} />
      ) : show === 'volumes' ? (
        <Volumes cluster={cluster} data={data} rows={data.volumes} />
      ) : show === 'classes' ? (
        <Classes cluster={cluster} data={data} rows={data.classes} />
      ) : (
        <Claims cluster={cluster} data={data} rows={data.claims.filter((c) => passes(c, show))} />
      )}
    </>
  );
}

/**
 * The claims laid end to end by level: red at 90 % and over, amber from 80,
 * green under, grey where no figure came. A reading; the tiles filter.
 */
export function LevelStrip({ data }: { data: Overview }): ReactNode {
  const parts = [
    { key: 'bad', count: data.counts.bad, fill: 'bg-bad', label: '≥ 90 %' },
    { key: 'warn', count: data.counts.warn, fill: 'bg-warn', label: '80–90 %' },
    { key: 'ok', count: data.counts.measured - data.counts.bad - data.counts.warn, fill: 'bg-ok', label: '< 80 %' },
    { key: 'none', count: data.counts.claims - data.counts.measured, fill: 'bg-muted', label: 'No figure' },
  ];
  const total = Math.max(1, data.counts.claims);
  return (
    <figure className="flex min-w-48 flex-1 flex-col gap-1" data-level-strip="">
      <div role="img" aria-label="Claims by fill" className="flex h-2.5 overflow-hidden rounded-full bg-panel-2">
        {parts.map((p) =>
          p.count <= 0 ? null : (
            <span key={p.key} data-part={p.key} className={`h-full ${p.fill}`} style={{ width: `${String((p.count / total) * 100)}%` }} title={`${p.label}: ${String(p.count)}`} />
          ),
        )}
      </div>
      <figcaption className="flex flex-wrap gap-3 text-2xs text-muted">
        {parts.map((p) => (
          <span key={p.key} className="flex items-center gap-1">
            <span className={`inline-block size-2 rounded-full ${p.fill}`} />
            {p.label} <span className="tabular-nums text-fg">{p.count}</span>
          </span>
        ))}
      </figcaption>
    </figure>
  );
}

function Empty({ what }: { what: string }): ReactNode {
  return (
    <p role="status" data-state="empty" className="text-xs text-muted">
      {what}
    </p>
  );
}

function More({ shown, total, onMore }: { shown: number; total: number; onMore: () => void }): ReactNode {
  if (shown >= total) return null;
  return (
    <button type="button" onClick={onMore} className={`self-start ${btnSecondary}`}>
      Show more ({total - shown})
    </button>
  );
}

const PHASE_TONE: Readonly<Record<string, string>> = { Bound: 'ok', Pending: 'warn', Lost: 'bad', Released: 'warn', Available: 'warn', Failed: 'bad' };

export function Claims({ cluster, data, rows }: { cluster: string; data: Overview; rows: readonly Claim[] }): ReactNode {
  const [limit, setLimit] = useState(PAGE_ROWS);
  if (data.counts.claims === 0) return <Empty what="No claim" />;
  if (rows.length === 0) return <Empty what="No match" />;
  const drawn = rows.slice(0, limit);
  return (
    <section aria-label="Claims" className="flex flex-col gap-2">
      <div className="overflow-x-auto rounded-lg border border-line">
        <table className={`${TABLE} w-full`} data-rows={String(drawn.length)}>
          <thead className="bg-panel">
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Claim</th>
              <th className={headCell()}>Status</th>
              <th className={headCell()}>Used</th>
              <th className={headCell()}>Size</th>
              <th className={headCell()}>Class</th>
              <th className={headCell()}>Mounted by</th>
              <th className={headCell()}>Last snapshot</th>
              <th className={headCell()}>Age</th>
            </tr>
          </thead>
          <tbody>
            {drawn.map((c) => (
              <tr key={`${c.namespace}/${c.name}`} className={ROW} data-claim={`${c.namespace}/${c.name}`}>
                <td className={bodyCell()}>
                  <Link to={objectHref(cluster, 'persistentvolumeclaims', c.namespace, c.name)} className={`hover:underline ${focusRing}`}>
                    {c.name}
                  </Link>
                  <span className="block text-2xs text-muted">{c.namespace}</span>
                </td>
                <td className={bodyCell()}>
                  <span className="flex flex-wrap items-center gap-1">
                    <Pill tone={PHASE_TONE[c.phase]}>{c.phase}</Pill>
                    <OrphanPill orphan={c.orphan} />
                    {c.mount === 'unknown' ? <Pill title="The pods could not all be read">mount ?</Pill> : null}
                  </span>
                </td>
                <td className={bodyCell()}>
                  <FillBar fill={c.fill} />
                </td>
                <td className={`${bodyCell()} tabular-nums`} title={`requested ${bytes(c.requestedBytes)}`}>
                  {bytes(c.capacityBytes || c.requestedBytes)}
                  <span className="block text-2xs text-muted">{c.accessModes.join(' ')}</span>
                </td>
                <td className={bodyCell()}>
                  {c.class ?? '—'}
                  {c.reclaim === undefined ? null : (
                    <span className={`block text-2xs ${c.reclaim === 'Retain' ? 'text-warn' : 'text-muted'}`}>{c.reclaim}</span>
                  )}
                </td>
                <td className={bodyCell()}>
                  {c.workload !== undefined ? (
                    <span title={c.pods.join(', ')}>
                      {c.workload.name} <span className="text-2xs text-muted">{c.workload.kind}</span>
                    </span>
                  ) : c.pods.length > 0 ? (
                    <span>{c.pods[0]}</span>
                  ) : (
                    <span className="text-muted">—</span>
                  )}
                  {c.pods.length + (c.morePods ?? 0) > 1 ? <span className="block text-2xs text-muted">{c.pods.length + (c.morePods ?? 0)} pods</span> : null}
                </td>
                <td className={bodyCell()}>
                  {c.snapshot === undefined ? (
                    <span className="text-muted">—</span>
                  ) : (
                    <span title={`${c.snapshot.name} · ${String(c.snapshots)} snapshots`}>
                      {age(c.snapshot.at, data.now)}
                      {c.snapshot.ready ? null : (
                        <>
                          {' '}
                          <Pill tone="warn">not ready</Pill>
                        </>
                      )}
                    </span>
                  )}
                </td>
                <td className={`${bodyCell()} tabular-nums text-muted`}>{age(c.created, data.now)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <More shown={drawn.length} total={rows.length} onMore={() => setLimit((n) => n + PAGE_ROWS)} />
    </section>
  );
}

export function Volumes({ cluster, data, rows }: { cluster: string; data: Overview; rows: readonly Volume[] }): ReactNode {
  const [limit, setLimit] = useState(PAGE_ROWS);
  if (data.volumesRead.state !== 'read') return <SectionBadge section={data.volumesRead} />;
  if (rows.length === 0) return <Empty what="No volume" />;
  const drawn = rows.slice(0, limit);
  return (
    <section aria-label="Volumes" className="flex flex-col gap-2">
      <div className="overflow-x-auto rounded-lg border border-line">
        <table className={`${TABLE} w-full`} data-rows={String(drawn.length)}>
          <thead className="bg-panel">
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Volume</th>
              <th className={headCell()}>Status</th>
              <th className={headCell()}>Size</th>
              <th className={headCell()}>Reclaim</th>
              <th className={headCell()}>Class</th>
              <th className={headCell()}>Claim</th>
              <th className={headCell()}>Age</th>
            </tr>
          </thead>
          <tbody>
            {drawn.map((v) => (
              <tr key={v.name} className={ROW} data-volume={v.name}>
                <td className={bodyCell()}>
                  <Link to={objectHref(cluster, 'persistentvolumes', '', v.name)} className={`hover:underline ${focusRing}`}>
                    {v.name}
                  </Link>
                  {v.driver === undefined ? null : <span className="block text-2xs text-muted">{v.driver}</span>}
                </td>
                <td className={bodyCell()}>
                  <span className="flex flex-wrap items-center gap-1">
                    <Pill tone={PHASE_TONE[v.phase]}>{v.phase}</Pill>
                    <OrphanPill orphan={v.orphan} />
                  </span>
                </td>
                <td className={`${bodyCell()} tabular-nums`}>
                  {bytes(v.capacityBytes)}
                  <span className="block text-2xs text-muted">{v.accessModes.join(' ')}</span>
                </td>
                <td className={`${bodyCell()} ${v.reclaim === 'Retain' && v.orphan !== undefined ? 'text-warn' : ''}`}>{v.reclaim}</td>
                <td className={bodyCell()}>{v.class ?? '—'}</td>
                <td className={bodyCell()}>{v.claim ?? <span className="text-muted">—</span>}</td>
                <td className={`${bodyCell()} tabular-nums text-muted`}>{age(v.created, data.now)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <More shown={drawn.length} total={rows.length} onMore={() => setLimit((n) => n + PAGE_ROWS)} />
    </section>
  );
}

export function Classes({ cluster, data, rows }: { cluster: string; data: Overview; rows: readonly Class[] }): ReactNode {
  if (data.classesRead.state !== 'read') return <SectionBadge section={data.classesRead} />;
  if (rows.length === 0) return <Empty what="No class" />;
  return (
    <section aria-label="Classes" className="overflow-x-auto rounded-lg border border-line">
      <table className={`${TABLE} w-full`} data-rows={String(rows.length)}>
        <thead className="bg-panel">
          <tr className={HEAD_ROW}>
            <th className={headCell()}>Class</th>
            <th className={headCell()}>Provisioner</th>
            <th className={headCell()}>Reclaim</th>
            <th className={headCell()}>Binding</th>
            <th className={headCell()}>Claims</th>
            <th className={headCell()}>Volumes</th>
            <th className={headCell()}>Age</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((k) => (
            <tr key={k.name} className={ROW} data-class={k.name}>
              <td className={bodyCell()}>
                <span className="flex flex-wrap items-center gap-1">
                  <Link to={objectHref(cluster, 'storageclasses', '', k.name)} className={`hover:underline ${focusRing}`}>
                    {k.name}
                  </Link>
                  {k.default ? <Pill tone="info">default</Pill> : null}
                  {k.expansion ? <Pill title="allowVolumeExpansion">expand</Pill> : null}
                  <OrphanPill orphan={k.orphan} />
                </span>
              </td>
              <td className={bodyCell()}>{k.provisioner}</td>
              <td className={bodyCell()}>{k.reclaim}</td>
              <td className={bodyCell()}>{k.binding}</td>
              <td className={`${bodyCell()} tabular-nums`}>{k.claims}</td>
              <td className={`${bodyCell()} tabular-nums`}>{k.volumes}</td>
              <td className={`${bodyCell()} tabular-nums text-muted`}>{age(k.created, data.now)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

/** Every orphan, by kind: the claims, the volumes, the classes. */
function Orphans({ cluster, data }: { cluster: string; data: Overview }): ReactNode {
  const claims = data.claims.filter((c) => c.orphan !== undefined);
  const volumes = data.volumes.filter((v) => v.orphan !== undefined);
  const classes = data.classes.filter((k) => k.orphan !== undefined);
  if (claims.length + volumes.length + classes.length === 0) return <Empty what="No orphan" />;
  return (
    <div className="flex flex-col gap-4" data-orphans="">
      {claims.length === 0 ? null : <Claims cluster={cluster} data={data} rows={claims} />}
      {volumes.length === 0 ? null : <Volumes cluster={cluster} data={data} rows={volumes} />}
      {classes.length === 0 ? null : <Classes cluster={cluster} data={data} rows={classes} />}
    </div>
  );
}
