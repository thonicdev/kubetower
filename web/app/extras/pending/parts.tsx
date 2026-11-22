import { useState, type ReactNode } from 'react';
import { focusRing } from '@kubetower/ui';

import { CATEGORY_HINT, CATEGORY_LABEL, ago, format, type Category, type Column, type Failure, type Fit, type NodeBar, type Preemption, type Reason, type Section, type TimelineEvent } from './data';

/** Each category's color: a categorical palette, never a grade - a taint is
 *  not worse than a missing volume, only different. */
export const CATEGORY_DOT: Readonly<Record<Category, string>> = {
  resources: 'bg-plate-red',
  taints: 'bg-plate-amber',
  nodeAffinity: 'bg-plate-violet',
  podAffinity: 'bg-plate-pink',
  spread: 'bg-plate-teal',
  volumes: 'bg-plate-blue',
  cordon: 'bg-plate-lime',
  ports: 'bg-plate-green',
  gated: 'bg-muted',
  noScheduler: 'bg-warn',
  starting: 'bg-accent',
  other: 'bg-muted',
};

const percent = (v: number) => `${Math.max(0, Math.min(100, v))}%`;

/** One category as a chip: its color and its name. */
export function CategoryChip({ category, main = false }: { category: Category; main?: boolean }): ReactNode {
  return (
    <span
      data-category={category}
      title={CATEGORY_HINT[category]}
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-px text-2xs ${main ? 'border-fg/40 text-fg' : 'border-line text-muted'}`}
    >
      <span className={`size-1.5 rounded-full ${CATEGORY_DOT[category]}`} />
      {CATEGORY_LABEL[category]}
    </span>
  );
}

/**
 * A section that was not read whole, said in one line: a refusal for this
 * person, a read nobody could review, a failure, or a narrowed answer. A read
 * whole section says nothing.
 */
export function SectionNote({ what, section }: { what: string; section: Section | undefined }): ReactNode {
  if (section === undefined || (section.state === 'read' && section.complete)) return null;
  const tone = section.state === 'read' ? 'text-muted' : 'text-warn';
  const head =
    section.state === 'denied'
      ? `${what}: not readable`
      : section.state === 'unreviewable'
        ? `${what}: not reviewed`
        : section.state === 'failed'
          ? `${what}: read failed`
          : `${what}: partial`;
  return (
    <p data-section={what} data-state={section.state} className={`text-xs ${tone}`}>
      <span className="font-semibold" title={section.note ?? ''}>
        {head}
      </span>
    </p>
  );
}

/**
 * Why the nodes were refused, as the scheduler counted them: one bar per
 * reason, its length the share of the nodes it refused. A reason with no count
 * refused them all.
 */
export function ReasonBars({ reasons, total }: { reasons: readonly Reason[]; total: number }): ReactNode {
  return (
    <ul data-reasons="" className="flex flex-col gap-1.5">
      {reasons.map((r, i) => {
        const all = r.nodes < 0;
        const share = all || total <= 0 ? 100 : (r.nodes / total) * 100;
        return (
          <li key={i} className="flex flex-col gap-0.5 text-xs">
            <span className="flex items-baseline gap-2">
              <span className={`size-2 shrink-0 rounded-full ${CATEGORY_DOT[r.category]}`} />
              <span className="min-w-0 flex-1">{r.text}</span>
              <span className="shrink-0 tabular-nums text-muted">{all ? 'every node' : total > 0 ? `${r.nodes} of ${total} nodes` : `${r.nodes} nodes`}</span>
            </span>
            <span className="relative block h-1.5 rounded-full bg-white/10">
              <span className={`absolute inset-y-0 left-0 rounded-full ${CATEGORY_DOT[r.category]}`} style={{ width: percent(share) }} />
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/** What each preemption verdict is called, and what it says on hover. */
export function preemptionLabel(p: Preemption): { label: string; hint: string; tone: 'accent' | 'warn' | 'muted' } {
  switch (p.verdict) {
    case 'nominated':
      return { label: `Nominated ${p.nominated ?? ''}`.trim(), hint: 'The scheduler found lower-priority pods to evict and is clearing this node for the pod', tone: 'accent' };
    case 'waiting':
      return { label: 'Victims leaving', hint: 'The nominated node still holds a pod that is terminating', tone: 'accent' };
    case 'never':
      return { label: 'Never preempts', hint: 'The pod’s preemptionPolicy is Never: it waits for room rather than evicting', tone: 'warn' };
    case 'unhelpful':
      return { label: 'Eviction would not help', hint: 'On every node the refusal is not about room, so evicting pods would not fit this one', tone: 'muted' };
    case 'noVictims':
      return { label: 'No victims', hint: 'No pod of lower priority runs on the nodes where room is the matter', tone: 'muted' };
    case 'mixed':
      return { label: 'Eviction would not help', hint: 'Where room is the matter nothing of lower priority runs; elsewhere the refusal is not about room', tone: 'muted' };
    case 'unsaid':
      return { label: 'Preemption not said', hint: 'The scheduler’s message carries no preemption verdict', tone: 'muted' };
    default:
      return { label: 'Preemption', hint: p.text ?? '', tone: 'muted' };
  }
}

const TONE_CHIP = { accent: 'border-accent/60 text-accent', warn: 'border-warn/60 text-warn', muted: 'border-line text-muted' } as const;

/**
 * Priority and preemption of one pod: its class and priority, whether it may
 * preempt, the scheduler's verdict, and - where it counted - how many nodes
 * said "not helpful" and how many "no victims".
 */
export function PreemptionStrip({ preemption: p }: { preemption: Preemption }): ReactNode {
  const verdict = preemptionLabel(p);
  const named = (n: number) => n !== 0;
  const share = (n: number) => (n < 0 || p.total <= 0 ? 100 : (n / p.total) * 100);
  const counted = p.total > 0 && ((p.unhelpful > 0) || (p.noVictims > 0));
  return (
    <div data-preemption={p.verdict} className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-1.5 text-2xs">
        <span data-chip="verdict" title={verdict.hint} className={`inline-flex items-center gap-1 rounded-full border px-2 py-px ${TONE_CHIP[verdict.tone]}`}>
          <span aria-hidden="true">{p.verdict === 'nominated' || p.verdict === 'waiting' ? '◆' : '○'}</span>
          {verdict.label}
        </span>
        {p.priority === undefined && p.class === undefined ? null : (
          <span data-chip="priority" title={p.class === undefined || p.class === '' ? 'No PriorityClass: the default priority' : `PriorityClass ${p.class}`} className="inline-flex items-center gap-1 rounded-full border border-line px-2 py-px text-muted">
            <span aria-hidden="true">▲</span>
            {p.class === undefined || p.class === '' ? '' : `${p.class} · `}
            <span className="tabular-nums">{p.priority ?? 0}</span>
          </span>
        )}
        {p.policy === 'Never' && p.verdict !== 'never' ? (
          <span data-chip="policy" title="preemptionPolicy: Never" className={`rounded-full border px-2 py-px ${TONE_CHIP.warn}`}>
            Never preempts
          </span>
        ) : null}
      </div>
      {counted ? (
        <div data-preemption-counts="" className="flex flex-col gap-0.5">
          <span className="flex h-1.5 overflow-hidden rounded-full bg-white/10" aria-hidden="true">
            {named(p.unhelpful) ? <span className="bg-muted" style={{ width: percent(share(p.unhelpful)) }} /> : null}
            {named(p.noVictims) ? <span className="bg-plate-blue" style={{ width: percent(share(p.noVictims)) }} /> : null}
          </span>
          <span className="flex gap-3 text-2xs tabular-nums text-muted">
            {named(p.unhelpful) ? (
              <span data-count="unhelpful" title="Evicting could not make the pod fit on these nodes">
                <span className="mr-1 inline-block size-1.5 rounded-full bg-muted" />
                {p.unhelpful < 0 ? 'all' : p.unhelpful} not helpful
              </span>
            ) : null}
            {named(p.noVictims) ? (
              <span data-count="noVictims" title="Nothing of lower priority runs on these nodes">
                <span className="mr-1 inline-block size-1.5 rounded-full bg-plate-blue" />
                {p.noVictims < 0 ? 'all' : p.noVictims} no victims
              </span>
            ) : null}
          </span>
        </div>
      ) : null}
    </div>
  );
}

/**
 * One node's requested against allocatable, for CPU and memory, with - when
 * a pod is picked - its request laid after what is already reserved: green
 * where it would fit, red where it would not.
 */
export function NodeBars({ bars, request, limit = 40 }: { bars: readonly NodeBar[]; request?: Readonly<Record<string, number>>; limit?: number }): ReactNode {
  const [all, setAll] = useState(false);
  const shown = all ? bars : bars.slice(0, limit);
  return (
    <div data-node-bars="" className="flex flex-col gap-2">
      <div className="flex gap-x-3 text-2xs uppercase tracking-wide text-muted">
        <span className="w-40 shrink-0">Node</span>
        <span className="min-w-0 flex-1">CPU requested</span>
        <span className="min-w-0 flex-1">Memory requested</span>
      </div>
      <ul className="flex flex-col gap-1.5">
        {shown.map((b) => (
          <li key={b.name} data-node={b.name} className="flex items-center gap-x-3 text-xs">
            <span className="flex w-40 min-w-0 shrink-0 items-center gap-1" title={b.name}>
              <span className="truncate">{b.name}</span>
              {b.cordoned ? <span className="shrink-0 text-2xs text-warn">cordoned</span> : null}
              {!b.ready ? <span className="shrink-0 text-2xs text-bad">not ready</span> : null}
              {b.taints > 0 ? (
                <span className="shrink-0 text-2xs text-muted" title={`${b.taints} taint(s) a pod must tolerate`}>
                  ⚑{b.taints}
                </span>
              ) : null}
            </span>
            <Track resource="cpu" gauge={b.cpu} add={request?.['cpu']} />
            <Track resource="memory" gauge={b.memory} add={request?.['memory']} />
          </li>
        ))}
      </ul>
      {bars.length > limit ? (
        <button type="button" className={`self-start text-xs text-accent underline ${focusRing}`} onClick={() => setAll((v) => !v)}>
          {all ? `Show the first ${limit}` : `Show all ${bars.length} nodes`}
        </button>
      ) : null}
    </div>
  );
}

function Track({ resource, gauge, add }: { resource: string; gauge: { allocatable: number; requested: number }; add: number | undefined }): ReactNode {
  if (gauge.allocatable <= 0) return <span className="text-2xs text-muted">none allocatable</span>;
  const used = (gauge.requested / gauge.allocatable) * 100;
  const extra = add === undefined || add <= 0 ? 0 : (add / gauge.allocatable) * 100;
  const fits = add === undefined || gauge.requested + add <= gauge.allocatable;
  const free = gauge.allocatable - gauge.requested;
  return (
    <span className="flex min-w-0 flex-1 items-center gap-1.5" title={`${format(resource, gauge.requested)} of ${format(resource, gauge.allocatable)} requested; ${format(resource, Math.max(free, 0))} free`}>
      <span className="relative block h-2.5 min-w-0 flex-1 overflow-hidden rounded-full bg-white/10">
        <span data-fill="requested" className={`absolute inset-y-0 left-0 ${used >= 90 ? 'bg-warn' : 'bg-accent'} opacity-60`} style={{ width: percent(used) }} />
        {extra > 0 ? (
          <span
            data-fill="pod"
            data-fits={fits}
            className={`absolute inset-y-0 ${fits ? 'bg-ok' : 'bg-bad'}`}
            style={{ left: percent(used), width: percent(Math.min(extra, 100 - Math.min(used, 100)) || 1) }}
          />
        ) : null}
      </span>
      <span className="w-10 shrink-0 text-right text-2xs tabular-nums text-muted">{Math.round(used)}%</span>
    </span>
  );
}

/** What a column's heading says on hover: how it was answered, or why it was not. */
export function columnTitle(c: Column): string {
  const placement = c.key === 'podAffinity' || c.key === 'spread';
  if (c.computed) return placement ? 'Computed from the pods placed on the nodes' : 'Computed from the node and the pod';
  const counted = `${c.scheduler < 0 ? 'every node' : `${c.scheduler} node(s)`} refused per the scheduler`;
  return c.why === undefined || c.why === '' ? `Not computed here: ${counted}` : `Not computed here, ${c.why}: ${counted}`;
}

/**
 * The node-fit matrix: nodes down, checks across, closest node first. Green
 * passes, red refuses (its hover says why), and a gray dashed cell is a check
 * nothing here computes - the scheduler's word only, never a pass.
 */
export function Matrix({ columns, fits, total, limit = 30, nominated }: { columns: readonly Column[]; fits: readonly Fit[]; total: number; limit?: number; nominated?: string }): ReactNode {
  const [all, setAll] = useState(false);
  const shown = all ? fits : fits.slice(0, limit);
  return (
    <div data-matrix="" className="flex flex-col gap-2">
      {/* A grid of colored cells, not a list's rows: drawn with ARIA's table
          roles so it reads as one, without the lists' table look. */}
      <div className="overflow-x-auto">
        <div role="table" aria-label="Node fit" className="flex w-max flex-col gap-0.5 text-2xs">
          <div role="row" className="flex gap-0.5">
            <span role="columnheader" className="sticky left-0 w-32 shrink-0 bg-panel px-1 text-muted">
              Node
            </span>
            {columns.map((c) => (
              <span
                key={c.key}
                role="columnheader"
                className="w-14 shrink-0 truncate text-center text-muted"
                title={columnTitle(c)}
              >
                {c.label}
                {c.computed ? '' : ' *'}
              </span>
            ))}
          </div>
          {shown.map((f, row) => (
            <div role="row" key={f.node} data-fit={f.node} data-failing={f.failing} className="flex gap-0.5">
              <span role="rowheader" className="sticky left-0 w-32 shrink-0 truncate bg-panel px-1" title={f.node}>
                {row === 0 ? <span className="text-accent" title="Closest: fewest failed checks, a node short of room before one a rule keeps the pod off">★ </span> : null}
                {f.node}
                {nominated !== undefined && f.node === nominated ? (
                  <span data-nominated="" className="ml-1 text-accent" title="The node the scheduler nominated for the pod">
                    ◆
                  </span>
                ) : null}
              </span>
              {f.cells.map((cell) => (
                <span
                  key={cell.key}
                  role="cell"
                  data-cell={cell.key}
                  data-ok={cell.ok === null ? 'unknown' : String(cell.ok)}
                  title={cell.detail ?? (cell.ok === true ? 'passes' : '')}
                  className={`flex h-5 w-14 shrink-0 items-center justify-center rounded-sm ${
                    cell.ok === null ? 'border border-dashed border-line text-muted' : cell.ok ? 'bg-ok/20 text-ok' : 'bg-bad/25 text-bad'
                  }`}
                >
                  {cell.ok === null ? '?' : cell.ok ? '✓' : '✗'}
                </span>
              ))}
            </div>
          ))}
        </div>
      </div>
      {total > fits.length ? <p className="text-2xs text-muted">{fits.length} of {total} nodes</p> : null}
      {fits.length > limit ? (
        <button type="button" className={`self-start text-xs text-accent underline ${focusRing}`} onClick={() => setAll((v) => !v)}>
          {all ? `Show the ${limit} closest` : `Show all ${fits.length}`}
        </button>
      ) : null}
    </div>
  );
}

/**
 * The pod's events on one time axis, from its creation to the read: a dot
 * for an event seen once, a bar from its first to its last sighting for one
 * that repeated, one line per reason.
 */
export function Timeline({ events, created, read }: { events: readonly TimelineEvent[]; created: string; read: string }): ReactNode {
  if (events.length === 0) return <p className="text-xs text-muted">No events</p>;
  const start = Math.min(Date.parse(created), ...events.map((e) => Date.parse(e.first)));
  const end = Math.max(Date.parse(read), ...events.map((e) => Date.parse(e.last)));
  const span = Math.max(end - start, 1);
  const at = (t: string) => ((Date.parse(t) - start) / span) * 100;
  const lanes = new Map<string, TimelineEvent[]>();
  for (const e of events) {
    const key = `${e.reason}`;
    lanes.set(key, [...(lanes.get(key) ?? []), e]);
  }
  return (
    <div data-timeline="" className="flex flex-col gap-1.5">
      {[...lanes.entries()].map(([reason, list]) => (
        <div key={reason} className="flex items-center gap-2 text-xs">
          <span className={`w-36 shrink-0 truncate ${list.some((e) => e.type === 'Warning') ? 'text-warn' : 'text-muted'}`} title={reason}>
            {reason}
          </span>
          <span className="relative block h-3 min-w-0 flex-1 rounded-full bg-white/5">
            {list.map((e, i) => {
              const left = at(e.first);
              const width = Math.max(at(e.last) - left, 0);
              const tone = e.type === 'Warning' ? 'bg-warn' : 'bg-accent';
              const title = `${e.reason} ×${e.count} — ${e.message} (${e.source}, last ${new Date(e.last).toISOString().slice(11, 19)} UTC)`;
              return width < 1 ? (
                <span key={i} data-event={e.reason} title={title} className={`absolute top-0.5 -ml-1 size-2 rounded-full ${tone}`} style={{ left: percent(left) }} />
              ) : (
                <span key={i} data-event={e.reason} title={title} className={`absolute inset-y-0.5 rounded-full ${tone}`} style={{ left: percent(left), width: percent(width) }} />
              );
            })}
          </span>
        </div>
      ))}
      <div className="flex gap-2 text-2xs text-muted">
        <span className="w-36 shrink-0" />
        <span className="flex min-w-0 flex-1 justify-between">
          <span>created, {ago(created, read)} ago</span>
          <span>now</span>
        </span>
      </div>
      <ul className="mt-1 flex flex-col gap-0.5 text-2xs text-muted">
        {events.slice(-5).map((e, i) => (
          <li key={i} className="truncate" title={e.message}>
            <span className={e.type === 'Warning' ? 'text-warn' : ''}>{e.reason}</span>
            {e.count > 1 ? ` ×${e.count}` : ''} · {ago(e.last, read)} ago · {e.message}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** A quota's use against its limit, with what the refused pod asked for. */
export function QuotaBar({ quota }: { quota: NonNullable<Failure['quota']> }): ReactNode {
  const hard = quota.hard ?? 0;
  const used = quota.used ?? 0;
  const unit = quota.resource.endsWith('cpu') ? 'cpu' : quota.resource.endsWith('memory') ? 'memory' : quota.resource;
  const pct = hard > 0 ? (used / hard) * 100 : 100;
  return (
    <div data-quota={quota.name} className="flex items-center gap-2">
      <span className="shrink-0 text-2xs text-muted">
        quota {quota.name}, {quota.resource}
      </span>
      <span className="relative block h-2 min-w-0 flex-1 overflow-hidden rounded-full bg-white/10">
        <span className={`absolute inset-y-0 left-0 ${pct >= 100 ? 'bg-bad' : 'bg-warn'}`} style={{ width: `${Math.min(pct, 100)}%` }} />
      </span>
      <span className="shrink-0 text-2xs tabular-nums">
        {format(unit, used)} of {format(unit, hard)} used · asked {quota.requested}
      </span>
      {quota.room === true ? <span className="shrink-0 text-2xs text-ok">room now</span> : null}
    </div>
  );
}
