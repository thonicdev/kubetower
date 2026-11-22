import type { ReactNode } from 'react';
import { HEAD_ROW, ROW, TABLE, bodyCell, focusRing, headCell } from '@kubetower/ui';

import type { FlowEdge, Lane, NamespaceSide, NamespaceView as View, PeerView, WorkloadView } from './data';

/**
 * One namespace, read the way the pod tab reads one pod: the namespace in the
 * middle, what may come in on the left - from where, by which label - and
 * what may go out on the right, to where; then each side as a list of rules,
 * and each workload on one row. Rules are drawn dashed; what Hubble saw is
 * counted beside them, never mixed in.
 */
export function NamespaceView({
  namespace,
  view,
  flows,
  showFlows,
  onWorkload,
}: {
  namespace: string;
  view: View;
  flows: readonly FlowEdge[];
  showFlows: boolean;
  onWorkload: (name: string) => void;
}): ReactNode {
  const seen = showFlows ? observed(namespace, flows) : { in: [], out: [] };
  return (
    <div data-namespace-view={namespace} className="flex flex-col gap-3">
      <Picture namespace={namespace} view={view} />
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <SideBox title="In" word="ingress" side={view.ingress} seen={seen.in} arrow="from" />
        <SideBox title="Out" word="egress" side={view.egress} seen={seen.out} arrow="to" />
      </div>
      <Workloads view={view} onWorkload={onWorkload} />
    </div>
  );
}

// --- the picture ----------------------------------------------------------------

const W = 720;
const H = 200;
const MAX_SIDE = 6;
const LABEL_CHARS = 34;

interface Peer {
  key: string;
  label: string;
  title: string;
  tone: 'allow' | 'deny' | 'open';
}

/** A peer as one short line: its label, and where its pods are. */
function peerText(p: PeerView): string {
  if (p.kind === 'anyone') return 'anyone';
  if (p.kind !== 'pods') return p.label;
  return shortLabel(p.label);
}

/** A pods peer as `where · which`: `monitoring · app=prometheus`, the
 *  namespace's own pods by their label alone. */
function shortLabel(label: string): string {
  const text = label.replace(/^pods /, '');
  const other = /^(.*) in namespace (\S+)$/.exec(text);
  if (other?.[1] !== undefined && other[2] !== undefined) return `${other[2]} · ${other[1]}`;
  return text.replace(/ in this namespace$/, '');
}

function cut(text: string): string {
  return text.length > LABEL_CHARS ? `${text.slice(0, LABEL_CHARS - 1)}…` : text;
}

/** One side's peers, each once: allowed first, then denied; "anyone" when a
 *  workload is open on that side. At most six, the rest counted. */
function peersOf(side: NamespaceSide): { peers: Peer[]; more: number } {
  const seen = new Map<string, Peer>();
  if (side.open.length > 0) {
    seen.set('open', { key: 'open', label: 'anyone', title: `no policy: ${side.open.join(', ')}`, tone: 'open' });
  }
  const lanes = [...side.lanes].sort((a, b) => Number(a.kind !== 'allow') - Number(b.kind !== 'allow'));
  for (const lane of lanes) {
    if (lane.kind === 'unevaluated' || lane.kind === 'pass') continue;
    for (const p of lane.peers) {
      const key = `${lane.kind}:${p.kind}:${p.label}`;
      const where = `${lane.targets.join(', ')} · ${lane.ports.join(', ')} · ${lane.name}`;
      const existing = seen.get(key);
      if (existing === undefined) {
        seen.set(key, { key, label: cut(peerText(p)), title: `${p.label} → ${where}`, tone: lane.kind === 'deny' ? 'deny' : 'allow' });
      } else if (!existing.title.includes(where)) {
        existing.title = `${existing.title}\n${p.label} → ${where}`;
      }
    }
  }
  const all = [...seen.values()];
  return { peers: all.slice(0, MAX_SIDE), more: Math.max(0, all.length - MAX_SIDE) };
}

const STROKE: Record<Peer['tone'], string> = {
  allow: 'var(--color-accent)',
  deny: 'var(--color-bad)',
  open: 'var(--color-muted)',
};

function Picture({ namespace, view }: { namespace: string; view: View }): ReactNode {
  const left = peersOf(view.ingress);
  const right = peersOf(view.egress);
  const cx = W / 2;
  const cy = H / 2;
  const y = (i: number, n: number) => (n <= 1 ? cy : 24 + (i * (H - 48)) / (n - 1));
  const pods = view.workloads.reduce((n, w) => n + w.pods, 0);
  const inIsolated = view.ingress.open.length === 0 && pods > 0;
  const outIsolated = view.egress.open.length === 0 && pods > 0;
  const rx = 74;
  const ry = 28;
  const rim = (x: number, yy: number) => {
    const angle = Math.atan2(yy - cy, x - cx);
    return { x: cx + rx * Math.cos(angle), y: cy + ry * Math.sin(angle) };
  };
  const nothing = (side: NamespaceSide) => side.open.length === 0 && side.lanes.every((l) => l.kind !== 'allow');
  return (
    <svg
      viewBox={`0 0 ${String(W)} ${String(H)}`}
      role="img"
      aria-label={`What the rules let into and out of ${namespace}`}
      className="w-full max-w-4xl rounded-lg border border-line bg-panel"
      data-namespace-picture=""
    >
      <defs>
        {(['allow', 'deny', 'open'] as const).map((tone) => (
          <marker key={tone} id={`netinspect-ns-${tone}`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill={STROKE[tone]} />
          </marker>
        ))}
      </defs>
      {left.peers.map((p, i) => {
        const yy = y(i, left.peers.length);
        const end = rim(210, yy);
        return (
          <g key={`in-${p.key}`} data-peer-in={p.label} data-tone={p.tone}>
            <title>{p.title}</title>
            <line x1={210} y1={yy} x2={end.x} y2={end.y} stroke={STROKE[p.tone]} strokeWidth={1.5} strokeDasharray={p.tone === 'open' ? '2 3' : '6 4'} markerEnd={`url(#netinspect-ns-${p.tone})`} />
            <text x={204} y={yy} textAnchor="end" dominantBaseline="central" fontSize="11" fill={p.tone === 'deny' ? 'var(--color-bad)' : 'var(--color-fg)'}>
              {p.label}
            </text>
          </g>
        );
      })}
      {right.peers.map((p, i) => {
        const yy = y(i, right.peers.length);
        const start = rim(W - 210, yy);
        return (
          <g key={`out-${p.key}`} data-peer-out={p.label} data-tone={p.tone}>
            <title>{p.title}</title>
            <line x1={start.x} y1={start.y} x2={W - 210} y2={yy} stroke={STROKE[p.tone]} strokeWidth={1.5} strokeDasharray={p.tone === 'open' ? '2 3' : '6 4'} markerEnd={`url(#netinspect-ns-${p.tone})`} />
            <text x={W - 204} y={yy} dominantBaseline="central" fontSize="11" fill={p.tone === 'deny' ? 'var(--color-bad)' : 'var(--color-fg)'}>
              {p.label}
            </text>
          </g>
        );
      })}
      {nothing(view.ingress) && pods > 0 ? (
        <text x={204} y={cy} textAnchor="end" dominantBaseline="central" fontSize="11" fill="var(--color-bad)">
          nothing allowed
        </text>
      ) : null}
      {nothing(view.egress) && pods > 0 ? (
        <text x={W - 204} y={cy} dominantBaseline="central" fontSize="11" fill="var(--color-bad)">
          nothing allowed
        </text>
      ) : null}
      {left.more > 0 ? (
        <text x={204} y={H - 6} textAnchor="end" fontSize="10" fill="var(--color-muted)">
          +{left.more} more
        </text>
      ) : null}
      {right.more > 0 ? (
        <text x={W - 204} y={H - 6} fontSize="10" fill="var(--color-muted)">
          +{right.more} more
        </text>
      ) : null}
      <ellipse cx={cx} cy={cy} rx={rx - 4} ry={ry - 4} fill="var(--color-panel-2)" stroke="var(--color-line)" strokeWidth={1.5} />
      <path d={`M ${String(cx)} ${String(cy - ry)} A ${String(rx)} ${String(ry)} 0 0 0 ${String(cx)} ${String(cy + ry)}`} fill="none" stroke={inIsolated ? 'var(--color-ok)' : 'transparent'} strokeWidth={3} data-ring-in={inIsolated ? 'isolated' : 'open'} />
      <path d={`M ${String(cx)} ${String(cy - ry)} A ${String(rx)} ${String(ry)} 0 0 1 ${String(cx)} ${String(cy + ry)}`} fill="none" stroke={outIsolated ? 'var(--color-ok)' : 'transparent'} strokeWidth={3} data-ring-out={outIsolated ? 'isolated' : 'open'} />
      <text x={cx} y={cy - 6} textAnchor="middle" dominantBaseline="central" fontSize="12" fill="var(--color-fg)">
        {cut(namespace)}
      </text>
      <text x={cx} y={cy + 10} textAnchor="middle" dominantBaseline="central" fontSize="10" fill="var(--color-muted)">
        {pods} pods · {view.workloadsTotal} workloads
      </text>
      <text x={8} y={14} fontSize="9" fill="var(--color-muted)">
        IN
      </text>
      <text x={W - 8} y={14} textAnchor="end" fontSize="9" fill="var(--color-muted)">
        OUT
      </text>
    </svg>
  );
}

// --- each side as a list ----------------------------------------------------------

/** A rule's row: an allow dashed in the accent, a deny in the refusal color,
 *  one not evaluated dotted and faint - the pod tab's looks. */
const LANE_LOOK: Record<Lane['kind'], string> = {
  allow: 'border-dashed border-accent/50',
  deny: 'border-dashed border-bad/60',
  pass: 'border-dashed border-line text-muted',
  unevaluated: 'border-dotted border-line text-muted',
};

interface Seen {
  peer: string;
  forwarded: number;
  dropped: number;
}

function SideBox({ title, word, side, seen, arrow }: { title: string; word: string; side: NamespaceSide; seen: readonly Seen[]; arrow: 'from' | 'to' }): ReactNode {
  return (
    <section data-side={word} className="flex flex-col gap-1.5 rounded-lg border border-line bg-panel p-3 text-xs">
      <h3 className="text-sm font-medium">{title}</h3>
      <ul className="flex flex-col gap-1">
        {side.lanes.map((lane) => (
          <li
            key={`${lane.namespace}/${lane.name}/${String(lane.rule)}`}
            data-lane={`${lane.name}/${String(lane.rule)}`}
            data-kind={lane.kind}
            title={`${lane.kind === 'allow' ? '' : `${lane.kind === 'unevaluated' ? 'not evaluated' : lane.kind} · `}${lane.namespace}/${lane.name} · rule ${String(lane.rule)} · ${lane.text}`}
            className={`flex flex-col gap-0.5 rounded-md border px-2 py-1 ${LANE_LOOK[lane.kind]}`}
          >
            <span className="flex flex-wrap items-baseline gap-x-1.5">
              <span className="text-muted">{arrow}</span>
              <span className={lane.kind === 'deny' ? 'text-bad' : 'text-fg'}>{lane.peers.map(peerText).join(' or ') || '—'}</span>
              <span className="text-muted">·</span>
              <span>{lane.ports.join(', ')}</span>
            </span>
            <span className="truncate text-2xs text-muted">
              {arrow === 'from' ? '→ ' : '← '}
              {lane.targets.join(', ')} · {lane.name}
            </span>
          </li>
        ))}
        {side.open.length === 0 ? null : (
          <li data-lane="open" title="No policy selects these workloads in this direction." className="flex flex-col gap-0.5 rounded-md border border-dotted border-line px-2 py-1 text-muted">
            <span>{arrow} anyone · every port</span>
            <span className="truncate text-2xs">
              {arrow === 'from' ? '→ ' : '← '}
              {side.open.join(', ')}
            </span>
          </li>
        )}
        {side.closed.length === 0 ? null : (
          <li data-lane="closed" title="Isolated, and no rule allows anything in this direction." className="rounded-md border border-bad/40 px-2 py-1 text-bad">
            nothing · {side.closed.join(', ')}
          </li>
        )}
      </ul>
      {side.lanesTotal > side.lanes.length ? <p className="text-2xs text-muted">+{side.lanesTotal - side.lanes.length} rules</p> : null}
      {seen.length === 0 ? null : (
        <p data-seen={word} className="flex flex-wrap items-center gap-1.5 text-2xs text-muted">
          <span>seen</span>
          {seen.map((s) => (
            <span key={s.peer} title={`${String(s.forwarded)} forwarded, ${String(s.dropped)} dropped`} className="rounded-sm bg-panel-2 px-1.5 py-0.5 text-fg">
              {s.peer} {s.forwarded}
              {s.dropped > 0 ? <span className="text-bad"> · {s.dropped}</span> : null}
            </span>
          ))}
        </p>
      )}
    </section>
  );
}

/** What Hubble saw cross the namespace's edge, by the peer on the other side:
 *  `ns:ops` → `ops`, `wl:shop/api` inside. At most eight a side. */
function observed(namespace: string, edges: readonly FlowEdge[]): { in: Seen[]; out: Seen[] } {
  const inside = (id: string) => id.startsWith(`wl:${namespace}/`) || id === `ns:${namespace}`;
  const name = (id: string) => id.replace(/^(ns|wl):/, '');
  const add = (into: Map<string, Seen>, peer: string, e: FlowEdge) => {
    const s = into.get(peer) ?? { peer, forwarded: 0, dropped: 0 };
    s.forwarded += e.forwarded;
    s.dropped += e.dropped;
    into.set(peer, s);
  };
  const ins = new Map<string, Seen>();
  const outs = new Map<string, Seen>();
  for (const e of edges) {
    const from = inside(e.from);
    const to = inside(e.to);
    if (!from && to) add(ins, name(e.from), e);
    if (from && !to) add(outs, name(e.to), e);
  }
  const top = (m: Map<string, Seen>) => [...m.values()].sort((a, b) => b.forwarded + b.dropped - (a.forwarded + a.dropped)).slice(0, 8);
  return { in: top(ins), out: top(outs) };
}

// --- each workload on one row -----------------------------------------------------

function isolation(isolated: number, pods: number): { text: string; tone: string } {
  if (pods === 0) return { text: 'host', tone: 'text-muted' };
  if (isolated === 0) return { text: 'open', tone: 'text-muted' };
  if (isolated === pods) return { text: 'isolated', tone: 'text-ok' };
  return { text: `${String(isolated)}/${String(pods)}`, tone: 'text-warn' };
}

function Workloads({ view, onWorkload }: { view: View; onWorkload: (name: string) => void }): ReactNode {
  return (
    <section aria-label="Workloads" className="flex flex-col gap-1.5">
      <div className="overflow-x-auto rounded-lg border border-line">
        <table className={`w-full ${TABLE}`} data-workloads="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Workload</th>
              <th className={headCell()}>Pods</th>
              <th className={headCell()}>In</th>
              <th className={headCell()}>From</th>
              <th className={headCell()}>Out</th>
              <th className={headCell()}>To</th>
            </tr>
          </thead>
          <tbody>
            {view.workloads.map((w) => (
              <WorkloadRow key={w.name} w={w} onWorkload={onWorkload} />
            ))}
          </tbody>
        </table>
      </div>
      {view.workloadsTotal > view.workloads.length ? <p className="text-2xs text-muted">+{view.workloadsTotal - view.workloads.length} workloads</p> : null}
    </section>
  );
}

function WorkloadRow({ w, onWorkload }: { w: WorkloadView; onWorkload: (name: string) => void }): ReactNode {
  const net = w.pods - w.hostNetwork;
  const inn = isolation(w.isolatedIn, net);
  const out = isolation(w.isolatedOut, net);
  const peers = (list: readonly string[], state: { text: string }) =>
    list.length > 0 ? list.map(shortLabel).join(', ') : state.text === 'open' ? 'anyone' : state.text === 'isolated' ? 'nothing' : '';
  return (
    <tr data-workload={w.name} className={ROW}>
      <td className={bodyCell(true)}>
        <button type="button" onClick={() => onWorkload(w.name)} className={`text-accent hover:underline ${focusRing}`}>
          {w.name}
        </button>
      </td>
      <td className={`${bodyCell(true)} tabular-nums`}>{w.pods}</td>
      <td className={`${bodyCell(true)} ${inn.tone}`}>{inn.text}</td>
      <td className={`${bodyCell(true)} max-w-64 truncate`} title={w.from.join('\n')}>
        {peers(w.from, inn)}
      </td>
      <td className={`${bodyCell(true)} ${out.tone}`}>{out.text}</td>
      <td className={`${bodyCell(true)} max-w-64 truncate`} title={w.to.join('\n')}>
        {peers(w.to, out)}
      </td>
    </tr>
  );
}
