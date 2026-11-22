import type { ReactNode } from 'react';
import { Pill } from '@kubetower/ui';

import type { BackupMark, Health, Instance, InstanceState, ReplicaLag, Summary } from './data';

/**
 * The drawings the pages share: the topology, the ready ring, the backup
 * timeline, the bars, and the words. Hand-made SVG on the theme's tokens
 * (`stroke-ok`, `fill-bad/20`…): every class is written out whole, since
 * Tailwind finds utilities by scanning source text.
 */

// --- words ------------------------------------------------------------------

export const HEALTH_LABEL: Readonly<Record<Health, string>> = {
  healthy: 'Healthy',
  switchover: 'Switchover',
  degraded: 'Degraded',
  failing: 'Failing',
  progressing: 'In progress',
};

export const HEALTH_TONE: Readonly<Record<Health, 'ok' | 'warn' | 'bad' | 'info'>> = {
  healthy: 'ok',
  switchover: 'info',
  degraded: 'warn',
  failing: 'bad',
  progressing: 'info',
};

/** A pill's grade for a health: the switchover and the work in progress draw
 *  in the info tone, in the same pill as every other state. */
export const healthPill = (h: Health): string => HEALTH_TONE[h];

const STATE_WORD: Readonly<Record<InstanceState, string>> = {
  healthy: 'streaming',
  replicating: 'catching up',
  failed: 'failed',
  unknown: 'not reported',
};

export function instanceWords(i: Instance): string {
  if (i.role === 'primary') return i.state === 'failed' ? 'primary, failed' : 'primary';
  return `replica, ${STATE_WORD[i.state]}`;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** "3 min", "5 h", "12 d": how long ago, short. */
export function ago(iso: string | undefined, now: number): string {
  if (iso === undefined) return 'never';
  const ms = now - Date.parse(iso);
  if (Number.isNaN(ms)) return 'unknown';
  if (ms < 0) return 'just now';
  if (ms < HOUR) return `${String(Math.max(1, Math.round(ms / MINUTE)))} min ago`;
  if (ms < 2 * DAY) return `${String(Math.round(ms / HOUR))} h ago`;
  return `${String(Math.round(ms / DAY))} d ago`;
}

/** "in 12 d", "in 5 h", "expired 2 d ago". */
export function until(iso: string, now: number): string {
  const ms = Date.parse(iso) - now;
  if (Number.isNaN(ms)) return 'unknown';
  if (ms <= 0) return `expired ${ago(iso, now)}`;
  if (ms < 2 * DAY) return `in ${String(Math.round(ms / HOUR))} h`;
  return `in ${String(Math.round(ms / DAY))} d`;
}

export function bytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u += 1;
  }
  return `${v >= 10 || u === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[u] ?? 'B'}`;
}

/** The Postgres major version, from the image's tag ("…/postgresql:18.6-…"). */
export function major(image: string | undefined): string | undefined {
  const tag = image?.split(':').pop();
  const m = tag === undefined ? null : /^(\d+)/.exec(tag);
  return m?.[1];
}

// --- the backup and archive chips ---------------------------------------------

export function BackupPill({ s, now }: { s: Summary; now: number }): ReactNode {
  const b = s.backup;
  switch (b.state) {
    case 'none':
      return (
        <Pill neutral="muted" title="Nothing backs this cluster up: no backup configured, scheduled or taken.">
          not backed up
        </Pill>
      );
    case 'never':
      return (
        <Pill tone="bad" title="Backups are configured or asked for, and none has completed.">
          no backup yet
        </Pill>
      );
    case 'failing':
      return (
        <Pill tone="bad" title={b.message === undefined || b.message === '' ? 'The last backup failed.' : `The last backup failed: ${b.message}`}>
          backup failed
        </Pill>
      );
    case 'late':
      return (
        <Pill tone="warn" title="The last successful backup is older than 24 h.">
          backup {ago(b.lastSuccess, now)}
        </Pill>
      );
    default:
      return <Pill tone="ok">backup {ago(b.lastSuccess, now)}</Pill>;
  }
}

export function ArchivePill({ s }: { s: Summary }): ReactNode {
  switch (s.archive.state) {
    case 'none':
      return (
        <Pill neutral="muted" title="No WAL archiving is configured: no point-in-time recovery.">
          no WAL archive
        </Pill>
      );
    case 'failing':
      return (
        <Pill tone="bad" title={s.archive.message ?? 'WAL archiving is failing.'}>
          WAL archive failing
        </Pill>
      );
    case 'pending':
      return <Pill title="Configured; the operator has not reported on it yet.">WAL archive pending</Pill>;
    default:
      return <Pill tone="ok">WAL archived</Pill>;
  }
}

export function CertPill({ s, now }: { s: Summary; now: number }): ReactNode {
  const c = s.certs;
  if (c.state === 'unknown' || c.earliest === undefined) return null;
  const tone = c.state === 'ok' ? undefined : c.state;
  return (
    <Pill tone={tone} neutral="muted" title="The earliest of this cluster's certificates to expire.">
      certs {until(c.earliest, now)}
    </Pill>
  );
}

// --- the topology ---------------------------------------------------------------

const NODE_STROKE: Readonly<Record<InstanceState, string>> = {
  healthy: 'stroke-ok',
  replicating: 'stroke-warn',
  failed: 'stroke-bad',
  unknown: 'stroke-muted',
};
const NODE_FILL: Readonly<Record<InstanceState, string>> = {
  healthy: 'fill-ok/25',
  replicating: 'fill-warn/25',
  failed: 'fill-bad/25',
  unknown: 'fill-muted/20',
};

/**
 * Primary and replicas as a hand-made graph: the primary on top, filled; one
 * replica per disc below, joined to it by an edge colored by what the
 * operator says of the replica - streaming, catching up, failed. An instance on
 * another timeline than the cluster's is drawn dashed. The instance a
 * switchover is promoting carries an accent ring.
 *
 * **Lag in seconds is drawn only when it was read** (`lag`, from the
 * button's one query): no resource of the operator carries it. An edge with no
 * figure on it says nothing false; one whose standby reported none says so
 * with a dash, never 0.
 */
export function Topology({
  instances,
  timeline,
  size = 'small',
  highlight,
  lag,
}: {
  instances: readonly Instance[];
  timeline?: number | undefined;
  size?: 'small' | 'large';
  highlight?: string | undefined;
  /** What the lag button read: the standbys the primary streams to. */
  lag?: readonly ReplicaLag[] | undefined;
}): ReactNode {
  const large = size === 'large';
  const r = large ? 16 : 7;
  const gap = large ? 150 : 24;
  const primary = instances.find((i) => i.role === 'primary');
  const replicas = instances.filter((i) => i !== primary);
  const columns = Math.max(1, replicas.length);
  const width = columns * gap;
  const top = r + 6;
  // A row nothing sits in is not drawn: no primary leaves no top row, and no
  // replica leaves no bottom row.
  const bottom = primary === undefined || replicas.length === 0 ? top : top + (large ? 84 : 26);
  const height = bottom + r + (large ? 34 : 4);
  const px = width / 2;
  const x = (i: number) => gap / 2 + i * gap;
  const off = (i: Instance) => timeline !== undefined && i.timeline !== undefined && i.timeline !== 0 && i.timeline !== timeline;
  const label = instances.map(instanceWords).join('; ');

  const disc = (i: Instance, cx: number, cy: number) => (
    <g key={i.name} data-instance={i.name} data-role={i.role} data-state={i.state}>
      <title>{`${i.name}: ${instanceWords(i)}${i.timeline === undefined ? '' : `, timeline ${String(i.timeline)}`}`}</title>
      {i.target === true ? <circle cx={cx} cy={cy} r={r + 4} className="fill-none stroke-accent" strokeWidth={2} strokeDasharray="3 2" /> : null}
      {highlight === i.name ? <circle cx={cx} cy={cy} r={r + 4} className="fill-none stroke-fg" strokeWidth={1.5} /> : null}
      <circle
        cx={cx}
        cy={cy}
        r={r}
        className={`${NODE_STROKE[i.state]} ${i.role === 'primary' ? NODE_FILL[i.state] : 'fill-panel'}`}
        strokeWidth={large ? 2.5 : 2}
        strokeDasharray={off(i) ? '3 2' : undefined}
      />
      {large ? (
        <>
          <text x={cx} y={cy + 4} textAnchor="middle" className="fill-fg text-2xs font-semibold">
            {i.role === 'primary' ? 'P' : 'R'}
          </text>
          <text x={i.role === 'primary' ? cx + r + 10 : cx} y={i.role === 'primary' ? cy - 2 : cy + r + 14} textAnchor={i.role === 'primary' ? 'start' : 'middle'} className="fill-fg text-2xs">
            {i.name}
          </text>
          <text x={i.role === 'primary' ? cx + r + 10 : cx} y={i.role === 'primary' ? cy + 11 : cy + r + 27} textAnchor={i.role === 'primary' ? 'start' : 'middle'} className="fill-muted text-2xs">
            {i.timeline === undefined ? STATE_WORD[i.state] : `${STATE_WORD[i.state]} · TL ${String(i.timeline)}`}
          </text>
        </>
      ) : null}
    </g>
  );

  return (
    <svg
      role="img"
      aria-label={`Topology: ${label}`}
      viewBox={`0 0 ${String(width)} ${String(height)}`}
      width={width}
      height={height}
      className={large ? 'max-w-full shrink-0 overflow-visible' : 'shrink-0'}
      data-topology={size}
    >
      {primary === undefined
        ? null
        : replicas.map((rep, n) => {
            const row = lag?.find((l) => l.name === rep.name);
            return (
              <g key={`edge-${rep.name}`}>
                <line
                  x1={px}
                  y1={top + r}
                  x2={x(n)}
                  y2={bottom - r}
                  className={NODE_STROKE[rep.state]}
                  strokeWidth={large ? 2 : 1.5}
                  strokeDasharray={rep.state === 'replicating' ? '4 3' : rep.state === 'failed' ? '1 3' : undefined}
                  data-edge={rep.name}
                />
                {large && lag !== undefined ? (
                  <text
                    x={(px + x(n)) / 2}
                    y={(top + r + bottom - r) / 2 - 3}
                    textAnchor={x(n) < px ? 'end' : x(n) > px ? 'start' : 'middle'}
                    className={`${row?.replay === undefined ? 'fill-muted' : 'fill-fg'} text-2xs tabular-nums`}
                    data-edge-lag={rep.name}
                  >
                    <title>{row === undefined ? `${rep.name} is not in the primary's replication list` : row.replay === undefined ? 'Replay lag not measured: nothing was replayed lately' : `Replay lag ${seconds(row.replay)}`}</title>
                    {row?.replay === undefined ? '—' : seconds(row.replay)}
                  </text>
                ) : null}
              </g>
            );
          })}
      {primary === undefined ? null : disc(primary, px, top)}
      {replicas.map((rep, n) => disc(rep, x(n), primary === undefined ? top : bottom))}
    </svg>
  );
}

/** A lag in seconds, as a person reads it. */
export function seconds(v: number): string {
  if (v < 1) return `${String(Math.round(v * 1000))} ms`;
  if (v < 60) return `${v.toFixed(1)} s`;
  if (v < 3600) return `${(v / 60).toFixed(1)} min`;
  return `${(v / 3600).toFixed(1)} h`;
}

/** Ready over expected, as a ring. */
export function ReadyRing({ ready, expected }: { ready: number; expected: number }): ReactNode {
  const r = 15;
  const c = 2 * Math.PI * r;
  const share = expected <= 0 ? 0 : Math.min(1, ready / expected);
  const tone = expected > 0 && ready >= expected ? 'stroke-ok' : ready === 0 ? 'stroke-bad' : 'stroke-warn';
  return (
    <svg role="img" aria-label={`${String(ready)} of ${String(expected)} instances ready`} viewBox="0 0 40 40" className="size-10 shrink-0" data-ring="">
      <circle cx="20" cy="20" r={r} className="fill-none stroke-line" strokeWidth={4} />
      <circle
        cx="20"
        cy="20"
        r={r}
        className={`fill-none ${tone}`}
        strokeWidth={4}
        strokeDasharray={`${String(c * share)} ${String(c)}`}
        transform="rotate(-90 20 20)"
      />
      <text x="20" y="24" textAnchor="middle" className="fill-fg text-2xs font-semibold">
        {`${String(ready)}/${String(expected)}`}
      </text>
    </svg>
  );
}

// --- the backup timeline ------------------------------------------------------

const PHASE_FILL: Readonly<Record<string, string>> = {
  completed: 'fill-ok',
  failed: 'fill-bad',
  walArchivingFailing: 'fill-bad',
  'invalid backup definition': 'fill-bad',
  running: 'fill-accent',
  started: 'fill-accent',
  finalizing: 'fill-accent',
};

const markTime = (m: BackupMark) => Date.parse(m.stoppedAt ?? m.startedAt ?? m.createdAt ?? '');

/**
 * Thirty days of backups on one axis, now at the right: one mark per Backup,
 * colored by its phase, and a rule at 24 h - the age past which a cluster's
 * last success counts as late.
 */
export function BackupTimeline({ backups, now, days = 30 }: { backups: readonly BackupMark[]; now: number; days?: number }): ReactNode {
  const width = 600;
  const height = 46;
  const span = days * DAY;
  const left = 8;
  const right = width - 8;
  const xOf = (t: number) => right - ((now - t) / span) * (right - left);
  const marks = backups.filter((m) => {
    const t = markTime(m);
    return !Number.isNaN(t) && now - t <= span && t <= now + HOUR;
  });
  const older = backups.length - marks.length;
  return (
    <figure className="flex flex-col gap-1" data-backup-timeline="">
      <svg role="img" aria-label={`${String(marks.length)} backups in the last ${String(days)} days${older > 0 ? `, ${String(older)} older` : ''}`} viewBox={`0 0 ${String(width)} ${String(height)}`} className="h-auto w-full">
        <line x1={left} y1={22} x2={right} y2={22} className="stroke-line" strokeWidth={2} />
        {[7, 14, 21, 28].map((d) => (
          <g key={d}>
            <line x1={xOf(now - d * DAY)} y1={17} x2={xOf(now - d * DAY)} y2={27} className="stroke-line" strokeWidth={1} />
            <text x={xOf(now - d * DAY)} y={42} textAnchor="middle" className="fill-muted text-2xs">{`${String(d)} d`}</text>
          </g>
        ))}
        <line x1={xOf(now - DAY)} y1={6} x2={xOf(now - DAY)} y2={32} className="stroke-warn" strokeWidth={1} strokeDasharray="2 2" />
        <text x={xOf(now - DAY) - 4} y={12} textAnchor="end" className="fill-warn text-2xs">
          24 h
        </text>
        {marks.map((m) => (
          <circle key={m.name} cx={xOf(markTime(m))} cy={22} r={5} className={`${PHASE_FILL[m.phase] ?? 'fill-muted'} stroke-bg`} strokeWidth={1.5} data-mark={m.phase}>
            <title>{`${m.name}: ${m.phase}${m.error === undefined ? '' : ` - ${m.error}`}`}</title>
          </circle>
        ))}
      </svg>
      {marks.length === 0 ? <figcaption className="text-2xs text-muted">{`No backup in ${String(days)} d`}</figcaption> : null}
    </figure>
  );
}

/** A used-over-capacity bar. Untrustworthy capacity is said, not drawn. */
export function Bar({ used, total, label, warnAt = 0.8, badAt = 0.9 }: { used: number; total: number; label: string; warnAt?: number; badAt?: number }): ReactNode {
  const share = total <= 0 ? 0 : Math.min(1, used / total);
  const tone = share >= badAt ? 'bg-bad' : share >= warnAt ? 'bg-warn' : 'bg-ok';
  return (
    <div className="flex min-w-0 flex-col gap-0.5" data-bar="">
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-line" role="img" aria-label={label}>
        <div className={`h-full rounded-full ${tone}`} style={{ width: `${(share * 100).toFixed(1)}%` }} />
      </div>
      <span className="text-2xs text-muted tabular-nums">{label}</span>
    </div>
  );
}
