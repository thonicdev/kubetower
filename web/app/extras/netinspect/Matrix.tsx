import { memo, useMemo, useState, type MouseEvent, type ReactNode } from 'react';
import { btnToggle, focusRing } from '@kubetower/ui';

import { DIAGONALS, type FlowEdge, type Graph, type GraphNode } from './data';
import { fold, ORDERS, TOPS, type CellData, type FoldAsk, type Order } from './fold';

/** What the matrix's own controls write to the address, `''` deleting a key. */
export type MatrixGo = (next: Record<string, string>) => void;

const ORDER_WORD: Record<Order, string> = { traffic: 'Traffic', rules: 'Rules', name: 'Name' };

/**
 * Every namespace against every namespace, in place of the cluster's graph:
 * one row per source, one column per destination, and in each cell what the
 * rules say - allowed, denied, or refused because the destination is isolated
 * and nothing lets the source in - and what Hubble saw. A row or column
 * heading opens that namespace.
 *
 * Only the busiest `top` are drawn, or the ones Find names; the rest are one
 * "others" row and column (`fold.ts`). Hovering a cell lights its row's and
 * its column's headings.
 *
 * Built from the overview's graph and the flows' edges: no read of its own.
 */
export function Matrix({
  graph,
  flows,
  showRules,
  showFlows,
  ask,
  onAsk,
  onNamespace,
}: {
  graph: Graph;
  flows: readonly FlowEdge[];
  showRules: boolean;
  showFlows: boolean;
  ask: FoldAsk;
  onAsk: MatrixGo;
  onNamespace: (namespace: string) => void;
}): ReactNode {
  // Folded once per answer and ask, not on every hover.
  const { find, top, order, active } = ask;
  const { nodes, cells, shown, total, folded, foldedEnds, truncated } = useMemo(
    () => fold(graph, flows, showFlows, { find, top, order, active }),
    [graph, flows, showFlows, find, top, order, active],
  );
  const [cross, setCross] = useState<{ from: string; to: string } | null>(null);
  const onOver = (event: MouseEvent<HTMLTableSectionElement>) => {
    const key = (event.target as HTMLElement).closest('td')?.getAttribute('data-cell') ?? null;
    if (key === null) return setCross(null);
    const [from = '', to = ''] = key.split('>');
    if (cross?.from !== from || cross.to !== to) setCross({ from, to });
  };
  // "others" draws the next size up, as far as the largest.
  const bigger = TOPS.find((t) => t > ask.top);
  const showMore = bigger === undefined ? undefined : () => onAsk({ top: String(bigger) });
  // Nothing to fold on a small cluster: no controls, unless an address set one.
  const tools = total > (TOPS[0] ?? 0) || ask.find !== '' || ask.active;
  return (
    <div className="flex flex-col gap-2" data-matrix="">
      {tools ? (
        <div className="flex flex-wrap items-center gap-2" data-matrix-tools="">
          <input
            type="search"
            name="mq"
            value={ask.find}
            placeholder="Find"
            aria-label="Find a namespace"
            onChange={(e) => onAsk({ mq: e.target.value })}
            className={`h-6 w-48 rounded-md border border-line bg-panel-2 px-2 text-xs text-fg placeholder:text-muted ${focusRing}`}
          />
          <div role="group" aria-label="Namespaces drawn" className="flex gap-1">
            {TOPS.map((top) => (
              <button key={top} type="button" data-top={top} aria-pressed={ask.top === top} onClick={() => onAsk({ top: String(top) })} className={`${btnToggle(ask.top === top)} tabular-nums`}>
                {top}
              </button>
            ))}
          </div>
          <label className="flex items-center gap-1.5 text-xs text-muted">
            <span>Order</span>
            <select
              data-matrix-order=""
              value={ask.order}
              onChange={(event) => onAsk({ sort: event.target.value })}
              className={`h-6 rounded-md border border-line bg-panel-2 px-1.5 text-xs text-fg ${focusRing}`}
            >
              {ORDERS.map((o) => (
                <option key={o} value={o}>
                  {ORDER_WORD[o]}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            data-matrix-active=""
            aria-pressed={ask.active}
            title="Only namespaces a rule or a flow names"
            onClick={() => onAsk({ active: ask.active ? '' : '1' })}
            className={btnToggle(ask.active)}
          >
            Active
          </button>
          <span data-matrix-count="" className="rounded-sm border border-line px-1.5 py-0.5 text-2xs text-muted tabular-nums" title={`${String(shown)} namespaces drawn of ${String(total)}${folded > 0 ? `, ${String(folded)} in others` : ''}${foldedEnds > 0 ? `, ${String(foldedEnds)} other ends in others` : ''}`}>
            {shown} / {total}
          </span>
          {truncated ? (
            <span data-matrix-cut="" className="rounded-sm border border-warn/50 px-1.5 py-0.5 text-2xs text-warn" title="Too many rules to send them all: a cell with no rule is unknown, not refused">
              rules cut
            </span>
          ) : null}
        </div>
      ) : null}
      {shown === 0 ? (
        <p data-matrix-none="" className="rounded-lg border border-line bg-panel p-3 text-sm text-muted">
          No namespace matches
        </p>
      ) : (
        <div className="overflow-auto rounded-lg border border-line bg-panel p-2">
          <table className="border-separate border-spacing-0.5 text-2xs">
            <thead>
              <tr>
                <th className="sticky top-0 left-0 z-20 bg-panel px-1 text-right font-normal text-muted">from ╲ to</th>
                {nodes.map((n) => (
                  <th key={n.id} scope="col" data-col={n.id} data-lit={cross?.to === n.id ? '' : undefined} className={`sticky top-0 z-10 h-28 w-6 rounded-sm bg-panel align-bottom font-normal ${cross?.to === n.id ? 'bg-panel-2' : ''}`}>
                    <Heading node={n} onNamespace={onNamespace} onOthers={showMore} vertical lit={cross?.to === n.id} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody onMouseOver={onOver} onMouseLeave={() => setCross(null)}>
              {nodes.map((from) => (
                <tr key={from.id}>
                  <th scope="row" data-row={from.id} data-lit={cross?.from === from.id ? '' : undefined} className={`sticky left-0 z-10 rounded-sm px-1 text-right font-normal whitespace-nowrap ${cross?.from === from.id ? 'bg-panel-2' : 'bg-panel'}`}>
                    <Heading node={from} onNamespace={onNamespace} onOthers={showMore} lit={cross?.from === from.id} />
                  </th>
                  {nodes.map((to) => (
                    <Cell key={to.id} from={from} to={to} cell={cells.get(`${from.id}>${to.id}`)} showRules={showRules} showFlows={showFlows} cut={truncated} />
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Legend cut={truncated} others={nodes.some((n) => n.kind === 'others')} diagonals={new Set(nodes.map((n) => n.diagonal))} />
    </div>
  );
}

type State = 'allowed' | 'denied' | 'partDenied' | 'unevaluated' | 'refused' | 'refusedOut' | 'open' | 'inside' | 'mixed' | 'partial' | 'folded' | 'unknown';

/** Every pod of a namespace, or of every namespace "others" holds, is
 *  isolated that way: nothing passes unless a rule of its own lets it. An
 *  "others" holding an end (a CIDR, world) is never isolated: an end never
 *  refuses. */
const grouped = (n: GraphNode) => n.kind === 'namespace' || (n.kind === 'others' && n.ends === 0);
const isolatedIn = (n: GraphNode) => grouped(n) && n.pods > 0 && n.isolatedIn >= n.pods;
const isolatedOut = (n: GraphNode) => grouped(n) && n.pods > 0 && n.isolatedOut >= n.pods;

/**
 * What the rules say of one pair. A flow passes only if **each** isolated
 * side lets it: the source's egress rules and the destination's ingress
 * rules are two halves, and an allow on one never answers for the other. A
 * deny on a pair between two namespaces wins. On a cut answer nothing but a
 * deny is concluded: a rule left out could deny or allow any pair.
 *
 * "others" stands for several namespaces, and a rule on the cell may be any
 * one member's. So a cell touching it is refused only when every member
 * refuses (all isolated on that side, and no rule of that side on the
 * cell), never allowed (what one member allows, another may refuse), and
 * never wholly denied: a deny on it is one member's. An "others" holding an
 * end (a CIDR, world), or whose ends were not counted, is never isolated.
 *
 * A namespace to itself is the policy engine's own answer (`diagonal`),
 * precedence and all: AdminNetworkPolicy priority and Pass, Calico's rule
 * order, the baseline last. Where an older server sent none, or a value this
 * page does not know, it is read as any other pair, never taken as inside.
 */
function stateOf(from: GraphNode, to: GraphNode, cell: CellData | undefined, cut: boolean): State {
  const mixed = from.kind === 'others' || to.kind === 'others';
  // The engine's own answer, precedence and all, before any count of rules.
  if (!mixed && from.id === to.id && to.diagonal !== undefined && DIAGONALS.has(to.diagonal)) return to.diagonal;
  if (cell !== undefined && cell.deny.length > 0) return mixed ? 'partDenied' : 'denied';
  if (cut) return 'unknown';
  // Rules between two folded namespaces are not sent at all (the server
  // drops a merged edge from "others" to itself): nothing to conclude.
  if (from.kind === 'others' && to.kind === 'others') return 'folded';
  const inOK = !isolatedIn(to) || cell?.allowIn === true;
  const outOK = !isolatedOut(from) || cell?.allowOut === true;
  if (inOK && outOK) {
    if (mixed) return 'folded';
    if (cell !== undefined && cell.allow.length > 0) return 'allowed';
    if (cell !== undefined && cell.unevaluated.length > 0) return 'unevaluated';
    return 'open';
  }
  if (cell !== undefined && cell.unevaluated.length > 0) return 'unevaluated';
  return inOK ? 'refusedOut' : 'refused';
}

const LOOK: Record<State, string> = {
  allowed: 'bg-accent/45',
  inside: 'bg-accent/25',
  mixed: 'border border-dashed border-muted bg-panel-2',
  partial: 'border border-dotted border-warn/60',
  denied: 'bg-bad/60',
  partDenied: 'border border-dashed border-bad/60 bg-bad/20',
  unevaluated: 'border border-dotted border-muted',
  refused: 'bg-panel-2 text-muted',
  refusedOut: 'bg-panel-2 text-muted',
  open: 'border border-line',
  folded: 'border border-dashed border-line',
  unknown: 'border border-dotted border-warn/60',
};

const WORD: Record<State, string> = {
  allowed: 'allowed by a rule',
  inside: 'allowed between its own pods',
  mixed: 'mixed: some flows between its pods pass, others are refused',
  partial: 'partial: too many pods or ports to ask every pair',
  denied: 'denied by a rule',
  partDenied: 'denied for some of the namespaces in others',
  unevaluated: 'a rule not evaluated here (FQDN, Service, layer 7)',
  refused: 'refused: the destination is isolated and no rule lets this in',
  refusedOut: 'refused: the source is isolated and no rule lets this out',
  open: 'open: no policy isolates the destination',
  folded: 'folded: the namespaces in others may answer differently',
  unknown: 'unknown: the rules were cut',
};

/** At most this many rule lines in a cell's tooltip; an "others" cell can sum hundreds. */
const TIP_LINES = 8;

function lines(prefix: string, items: readonly string[] | undefined): string[] {
  if (items === undefined) return [];
  const out = items.slice(0, TIP_LINES).map((x) => `${prefix} ${x}`);
  if (items.length > TIP_LINES) out.push(`${prefix} +${String(items.length - TIP_LINES)}`);
  return out;
}

/** Memoized: a hover redraws the headings, never the cells. */
const Cell = memo(function Cell({
  from,
  to,
  cell,
  showRules,
  showFlows,
  cut,
}: {
  from: GraphNode;
  to: GraphNode;
  cell: CellData | undefined;
  showRules: boolean;
  showFlows: boolean;
  cut: boolean;
}): ReactNode {
  const state = stateOf(from, to, cell, cut);
  const seen = showFlows && cell !== undefined && cell.forwarded + cell.dropped > 0;
  const title = [
    `${from.label} → ${to.label}: ${WORD[state]}`,
    ...lines('allow', cell?.allow),
    ...lines('deny', cell?.deny),
    ...lines('not evaluated', cell?.unevaluated),
    ...(seen ? [`seen ${String(cell.forwarded)} forwarded, ${String(cell.dropped)} dropped`] : []),
  ].join('\n');
  return (
    <td
      data-cell={`${from.id}>${to.id}`}
      data-state={state}
      data-seen={seen ? (cell.dropped > 0 ? 'dropped' : 'forwarded') : undefined}
      title={title}
      className={`relative size-6 rounded-sm text-center align-middle ${showRules ? LOOK[state] : 'border border-line'}`}
    >
      {showRules && (state === 'refused' || state === 'refusedOut') ? <span aria-hidden="true">×</span> : null}
      {seen ? (
        <span aria-hidden="true" className={`absolute right-0.5 bottom-0.5 size-1.5 rounded-full ${cell.dropped > 0 ? 'bg-bad' : 'bg-ok'}`} />
      ) : null}
    </td>
  );
});

function Heading({
  node,
  onNamespace,
  onOthers,
  vertical = false,
  lit = false,
}: {
  node: GraphNode;
  onNamespace: (namespace: string) => void;
  /** Absent at the largest size: "others" is then a plain heading. */
  onOthers: (() => void) | undefined;
  vertical?: boolean;
  lit?: boolean;
}): ReactNode {
  const text = <span className={vertical ? 'inline-block max-h-28 truncate [writing-mode:vertical-rl] rotate-180' : 'inline-block max-w-40 truncate'}>{node.label}</span>;
  if (node.kind === 'others' && onOthers === undefined) {
    return (
      <span data-heading-others="" title={`${node.label} · ${String(node.pods)} pods`} className={`italic ${lit ? 'text-accent' : 'text-muted'}`}>
        {text}
      </span>
    );
  }
  if (node.kind === 'others') {
    return (
      <button type="button" data-heading-others="" title={`${node.label} · ${String(node.pods)} pods`} onClick={onOthers} className={`italic ${lit ? 'text-accent' : 'text-muted'} hover:text-accent ${focusRing}`}>
        {text}
      </button>
    );
  }
  if (node.kind !== 'namespace' || node.namespace === undefined) {
    return (
      <span title={node.label} className={lit ? 'text-accent' : 'text-muted'}>
        {text}
      </span>
    );
  }
  const ns = node.namespace;
  return (
    <button type="button" data-heading={ns} title={`${node.label} · ${String(node.pods)} pods`} onClick={() => onNamespace(ns)} className={`${lit ? 'text-accent' : 'text-fg'} hover:text-accent ${focusRing}`}>
      {text}
    </button>
  );
}

function Legend({ cut, others, diagonals }: { cut: boolean; others: boolean; diagonals: ReadonlySet<string | undefined> }): ReactNode {
  const items: { state: State; label: string }[] = [
    { state: 'allowed', label: 'allowed' },
    { state: 'denied', label: 'denied' },
    ...(cut ? [{ state: 'unknown' as const, label: 'unknown' }] : [{ state: 'refused' as const, label: 'refused' }, { state: 'open' as const, label: 'open' }]),
    ...(others ? [{ state: 'folded' as const, label: 'folded' }, { state: 'partDenied' as const, label: 'partly denied' }] : []),
    ...(diagonals.has('mixed') ? [{ state: 'mixed' as const, label: 'mixed' }] : []),
    ...(diagonals.has('partial') ? [{ state: 'partial' as const, label: 'partial' }] : []),
  ];
  return (
    <p data-matrix-legend="" className="flex flex-wrap items-center gap-3 text-2xs text-muted">
      {items.map((i) => (
        <span key={i.state} className="flex items-center gap-1" title={WORD[i.state]}>
          <span className={`inline-block size-3 rounded-sm ${LOOK[i.state]}`} />
          {i.label}
        </span>
      ))}
      <span className="flex items-center gap-1" title="Flows seen, forwarded or dropped">
        <span className="inline-block size-1.5 rounded-full bg-ok" /> seen
        <span className="ml-1 inline-block size-1.5 rounded-full bg-bad" /> dropped
      </span>
    </p>
  );
}
