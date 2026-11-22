import { useRef, useState, type ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useLocation, useNavigate, useParams } from 'react-router';
import { BackArrow, CopyButton, Pending, Pill, trail, useDeclarePageFacts } from '@kubetower/ui';

import { PAGE_PATH, clusterQuery, lagQuery, type Command, type Detail, type Member } from './data';
import {
  ArchivePill,
  BackupPill,
  BackupTimeline,
  Bar,
  CertPill,
  HEALTH_LABEL,
  ReadyRing,
  Topology,
  ago,
  bytes,
  healthPill,
  instanceWords,
  major,
  until,
} from './parts';
import { Database } from './Database';
import { LagBar } from './Lag';
import { Problem } from './states';

/**
 * One CloudNativePG cluster: its topology, its instances and their disks,
 * its backups on a timeline, its certificates, its promotions, and what
 * names it. From `GET /x/{cluster}/cnpg/clusters/{namespace}/{name}`.
 */
export function ClusterPage(): ReactNode {
  const { cluster = '', namespace = '', name = '' } = useParams<{ cluster: string; namespace: string; name: string }>();
  useDeclarePageFacts(trail({ label: 'Postgres (CNPG)', to: `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}` }, { label: namespace }, { label: name }));
  const query = useQuery(clusterQuery(cluster, namespace, name));
  return (
    <div className="flex flex-col gap-4 p-4" data-cnpg-cluster="" aria-busy={query.isFetching}>
      {query.data !== undefined ? (
        <ClusterView detail={query.data} cluster={cluster} back={<Back cluster={cluster} />} />
      ) : query.isError ? (
        <Problem error={query.error} onRetry={() => void query.refetch()} />
      ) : (
        <Pending what="this Postgres cluster" />
      )}
    </div>
  );
}

/**
 * The way back to the list, as the Redis viewer's: the step back in this tab
 * when the page was opened from the console - so the list comes back with
 * its filter - and a link to the list when the page was the tab's first.
 */
function Back({ cluster }: { cluster: string }): ReactNode {
  const navigate = useNavigate();
  const arrival = useLocation().key;
  const [inConsole] = useState(() => {
    const idx: unknown = (window.history.state as { idx?: unknown } | null)?.idx;
    return typeof idx === 'number' ? idx > 0 : arrival !== 'default';
  });
  return inConsole ? (
    <BackArrow onClick={() => void navigate(-1)} data-back="" />
  ) : (
    <BackArrow to={`/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`} label="Back to Postgres (CNPG)" data-back="" />
  );
}

const SECTION = 'flex flex-col gap-2 rounded-lg border border-line bg-panel p-3';
const HEADING = 'text-sm font-medium';

export function ClusterView({
  detail: d,
  cluster,
  highlight,
  compact = false,
  back,
}: {
  detail: Detail;
  cluster: string;
  highlight?: string | undefined;
  compact?: boolean;
  /** The way back to the list, before the title: on the page only. */
  back?: ReactNode;
}): ReactNode {
  const s = d.summary;
  const now = Date.parse(d.now);
  const version = major(s.image);
  const [lagOn, setLagOn] = useState(false);
  const lag = useQuery({ ...lagQuery(cluster, s.namespace, s.name), enabled: lagOn });
  return (
    <>
      <header className="flex items-center gap-3">
        {back}
        <ReadyRing ready={s.ready} expected={s.expected} />
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className={compact ? 'text-base font-medium' : 'text-xl font-medium'}>{s.name}</h1>
          <div className="flex flex-wrap items-center gap-1 text-xs text-muted">
            <Pill tone={healthPill(s.health)} title={s.phaseReason ?? s.phase}>
              {HEALTH_LABEL[s.health]}
            </Pill>
            <span>{s.phase}</span>
            <span>· {s.namespace}</span>
            {version === undefined ? null : <span>· PostgreSQL {version}</span>}
            {d.writeService === undefined ? null : <span>· write {d.writeService}</span>}
            {d.readService === undefined ? null : <span>· read {d.readService}</span>}
          </div>
        </div>
      </header>
      {s.phaseReason !== undefined && s.phaseReason !== '' ? <p className="text-sm text-muted">{s.phaseReason}</p> : null}

      <section className={SECTION} aria-label="Topology">
        <h2 className={HEADING} title="Dashed: another timeline than the cluster's.">
          Primary and replicas
        </h2>
        <Topology instances={s.instances} timeline={s.timeline} size="large" highlight={highlight} lag={lag.data?.replicas} />
        {compact || s.expected < 2 ? null : (
          <LagBar
            lag={lag}
            started={lagOn}
            onRead={() => {
              if (lagOn) void lag.refetch();
              else setLagOn(true);
            }}
          />
        )}
        {(s.nodesUsed ?? 0) === 1 && s.expected > 1 ? (
          <Pill tone="warn" title="Every instance runs on one node: losing it loses them all.">
            one node
          </Pill>
        ) : null}
        <Members members={d.members} note={d.diskNote} compact={compact} />
      </section>

      {compact || d.commands.length === 0 ? null : <Commands commands={d.commands} />}

      {compact ? null : <Database cluster={cluster} namespace={s.namespace} name={s.name} />}

      <div className={compact ? 'flex flex-col gap-4' : 'grid grid-cols-1 gap-4 xl:grid-cols-2'}>
        <section className={SECTION} aria-label="Backups">
          <h2 className={HEADING}>Backups</h2>
          <div className="flex flex-wrap gap-1">
            <BackupPill s={s} now={now} />
            <ArchivePill s={s} />
            {s.backup.scheduled > 0 ? <Pill neutral="muted">{`${String(s.backup.scheduled)} scheduled`}</Pill> : null}
          </div>
          <BackupTimeline backups={d.backups} now={now} />
          {d.scheduled.length > 0 ? (
            <ul className="flex flex-col gap-0.5 text-xs">
              {d.scheduled.map((sc) => (
                <li key={sc.name} className="flex flex-wrap gap-2">
                  <span className="font-medium">{sc.name}</span>
                  <code className="text-muted">{sc.schedule}</code>
                  {sc.suspended ? <Pill tone="warn">suspended</Pill> : null}
                  {sc.last === undefined ? null : <span className="text-muted">last {ago(sc.last, now)}</span>}
                </li>
              ))}
            </ul>
          ) : null}
          {d.backups.length > 0 ? (
            <ul className="flex flex-col gap-0.5 text-xs" data-backups="">
              {d.backups.slice(0, 8).map((b) => (
                <li key={b.name} className="flex min-w-0 flex-wrap items-center gap-2">
                  <Pill tone={b.phase === 'completed' ? 'ok' : b.phase === 'failed' ? 'bad' : undefined}>{b.phase}</Pill>
                  <span className="truncate">{b.name}</span>
                  <span className="text-muted">{ago(b.stoppedAt ?? b.startedAt ?? b.createdAt, now)}</span>
                  {b.error === undefined ? null : <span className="truncate text-bad" title={b.error}>{b.error}</span>}
                </li>
              ))}
            </ul>
          ) : null}
        </section>

        <section className={SECTION} aria-label="Certificates">
          <h2 className={HEADING}>Certificates</h2>
          <div className="flex flex-wrap gap-1">
            <CertPill s={s} now={now} />
          </div>
          {s.certs.list.length === 0 ? <p className="text-xs text-muted">No expiry reported</p> : null}
          {s.certs.list.map((c) => {
            const left = Math.max(0, (Date.parse(c.expiresAt) - now) / 86_400_000);
            return <Bar key={c.secret} used={90 - Math.min(90, left)} total={90} warnAt={83 / 90} badAt={1} label={`${c.secret} - expires ${until(c.expiresAt, now)}`} />;
          })}
        </section>

        <section className={SECTION} aria-label="Promotions">
          <h2 className={HEADING}>Switchovers and failovers</h2>
          <div className="flex flex-wrap gap-1 text-xs" data-promotions="">
            <Pill neutral="muted">{`timeline ${String(s.timeline ?? '?')}`}</Pill>
            {s.timeline === undefined || s.timeline < 1 ? null : (
              <Pill neutral="muted" title="At least: counted from the timeline since the cluster was created">
                {`${String(s.timeline - 1)} promotion${s.timeline - 1 === 1 ? '' : 's'}`}
              </Pill>
            )}
            {s.promotedAt === undefined ? null : <Pill neutral="muted">{`last ${ago(s.promotedAt, now)}`}</Pill>}
          </div>
          {d.events.length === 0 ? (
            <p className="text-xs text-muted" title="The API server keeps Events an hour by default.">
              No recent events
            </p>
          ) : (
            <ol className="flex flex-col gap-1 text-xs" data-events="">
              {d.events.map((e, i) => (
                <li key={`${e.reason ?? ''}-${String(i)}`} className="flex min-w-0 gap-2">
                  <span className="shrink-0 text-muted tabular-nums">{ago(e.at, now)}</span>
                  <Pill tone={e.type === 'Warning' ? 'warn' : undefined} neutral="muted">
                    {e.reason ?? 'Event'}
                  </Pill>
                  <span className="min-w-0 truncate" title={e.message}>
                    {e.message}
                  </span>
                </li>
              ))}
            </ol>
          )}
        </section>

        <section className={SECTION} aria-label="Poolers and declared objects">
          <h2 className={HEADING}>Poolers, databases, publications</h2>
          {d.poolers.length === 0 && d.declared.length === 0 ? <p className="text-xs text-muted">None</p> : null}
          <ul className="flex flex-col gap-1 text-xs">
            {d.poolers.map((p) => (
              <li key={`p-${p.name}`} className="flex flex-wrap items-center gap-2">
                <Pill tone={p.phase === 'active' ? 'ok' : p.phase === 'failed' ? 'bad' : p.phase === 'inactive' ? 'warn' : undefined}>{p.phase ?? 'no phase'}</Pill>
                <span className="font-medium">Pooler {p.name}</span>
                <span className="text-muted">
                  {p.type ?? ''} · {String(p.instances)} pgbouncer
                </span>
                {p.reason === undefined || p.reason === '' ? null : <span className="text-warn">{p.reason}</span>}
              </li>
            ))}
            {d.declared.map((o) => (
              <li key={`${o.kind}-${o.name}`} className="flex flex-wrap items-center gap-2">
                <Pill tone={o.applied === true ? 'ok' : o.applied === false ? 'bad' : undefined}>{o.applied === true ? 'applied' : o.applied === false ? 'not applied' : 'pending'}</Pill>
                <span className="font-medium">
                  {o.kind} {o.pgName ?? o.name}
                </span>
                {o.message === undefined || o.message === '' ? null : <span className="text-muted">{o.message}</span>}
              </li>
            ))}
          </ul>
        </section>
      </div>

      {compact ? null : (
        <section className={SECTION} aria-label="Conditions">
          <h2 className={HEADING}>Conditions</h2>
          <ul className="flex flex-col gap-0.5 text-xs">
            {d.conditions.map((c) => (
              <li key={c.type} className="flex flex-wrap gap-2">
                <Pill tone={c.status === 'True' ? 'ok' : c.status === 'False' ? 'bad' : undefined}>{c.type}</Pill>
                <span className="text-muted">{c.reason}</span>
                <span>{c.message}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      {compact ? (
        <Link className="text-xs text-accent hover:underline" to={`/c/${encodeURIComponent(cluster)}/${PAGE_PATH}/clusters/${encodeURIComponent(s.namespace)}/${encodeURIComponent(s.name)}`}>
          Open the cluster&apos;s page
        </Link>
      ) : null}
    </>
  );
}

function Members({ members, note, compact }: { members: readonly Member[]; note?: string | undefined; compact: boolean }): ReactNode {
  return (
    <div className="flex flex-col gap-1">
      <ul className={compact ? 'grid grid-cols-1 gap-2' : 'grid grid-cols-1 gap-2 md:grid-cols-2 xl:grid-cols-3'} data-members="">
        {members.map((m) => (
          <li key={m.name} className="flex flex-col gap-1 rounded border border-line bg-panel-2 px-2 py-1.5 text-xs" data-member={m.name}>
            <div className="flex items-center gap-2">
              <span className="font-medium">{m.name}</span>
              <Pill tone={m.role === 'primary' ? 'ok' : undefined} neutral="muted">
                {instanceWords(m)}
              </Pill>
            </div>
            {m.pod === undefined ? (
              <span className="text-muted">no pod</span>
            ) : (
              <span className="text-muted">
                {m.pod.ready ? 'ready' : 'not ready'} · {m.pod.phase}
                {m.pod.restarts > 0 ? ` · ${String(m.pod.restarts)} restart${m.pod.restarts === 1 ? '' : 's'}` : ''}
                {m.pod.node === undefined ? '' : ` · ${m.pod.node}`}
                {m.pod.roleLabel !== undefined && m.pod.roleLabel !== '' && m.pod.roleLabel !== m.role ? ` · labeled ${m.pod.roleLabel}` : ''}
              </span>
            )}
            {m.data === undefined ? null : m.data.trustworthy ? (
              <Bar used={m.data.usedBytes} total={m.data.capacityBytes} label={`data ${bytes(m.data.usedBytes)} of ${bytes(m.data.capacityBytes)}`} />
            ) : (
              <span className="text-muted" title="The filesystem's capacity is not the claim's: a network share's whole size.">
                data {bytes(m.data.usedBytes)}
              </span>
            )}
            {m.wal === undefined ? null : (
              <Bar used={m.wal.usedBytes} total={m.wal.capacityBytes} label={`WAL ${bytes(m.wal.usedBytes)} of ${bytes(m.wal.capacityBytes)}`} />
            )}
          </li>
        ))}
      </ul>
      {note === undefined || note === '' ? null : (
        <p className="text-2xs text-warn" title={note}>
          {note.split(':')[0]}
        </p>
      )}
    </div>
  );
}

const ACT_LABEL: Record<Command['act'], string> = { backup: 'Back up now', restart: 'Restart', reload: 'Reload', promote: 'Promote' };

/**
 * The `kubectl cnpg` lines for the usual acts on this cluster, to copy.
 * Nothing here sends a request: a button copies text.
 */
function Commands({ commands }: { commands: readonly Command[] }): ReactNode {
  return (
    <section className={SECTION} aria-label="Commands" data-commands="">
      <h2 className={HEADING} title="Copied, never run by KubeTower">
        Commands
      </h2>
      <ul className="flex flex-col gap-1">
        {commands.map((c) => (
          <CommandRow key={c.line} command={c} />
        ))}
      </ul>
    </section>
  );
}

function CommandRow({ command: c }: { command: Command }): ReactNode {
  const block = useRef<HTMLElement>(null);
  return (
    <li className="flex min-w-0 items-center gap-2 text-xs" data-command={c.act}>
      <span className="w-44 shrink-0 truncate">
        <Pill neutral="muted">{c.instance === undefined ? ACT_LABEL[c.act] : `${ACT_LABEL[c.act]} ${c.instance}`}</Pill>
      </span>
      <code ref={block} className="min-w-0 flex-1 truncate font-mono text-2xs select-all" title={c.line}>
        {c.line}
      </code>
      <CopyButton value={c.line} label="Copy" selectRef={block} />
    </li>
  );
}
