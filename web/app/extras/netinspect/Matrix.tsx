import { memo, useState, type MouseEvent, type ReactNode } from 'react';
import { focusRing } from '@kubetower/ui';

import type { FlowEdge, Graph, GraphNode } from './data';
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
  const { nodes, cells, shown, total, folded } = fold(graph, flows, showFlows, ask);
  const [cross, setCross] = useState<{ from: string; to: string } | null>(null);
  const onOver = (event: MouseEvent<HTMLTableSectionElement>) => {
    const key = (event.target as HTMLElement).closest('td')?.getAttribute('data-cell') ?? null;
    if (key === null) return setCross(null);
    const [from = '', to = ''] = key.split('>');
    if (cross?.from !== from || cross.to !== to) setCross({ from, to });
  };
  const showAll = () => onAsk({ top: '0', mq: '', active: '' });
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
            className={`h-8 w-48 rounded-md border border-line bg-panel-2 px-2 text-xs text-fg placeholder:text-muted ${focusRing}`}
          />
          <div role="group" aria-label="Namespaces drawn" className="flex">
            {TOPS.map((top, i) => (
              <button
                key={top}
                type="button"
                data-top={top}
                aria-pressed={ask.top === top}
                onClick={() => onAsk({ top: String(top) })}
                className={`h-8 border border-line px-2 text-xs tabular-nums ${i === 0 ? 'rounded-l-md' : '-ml-px'} ${i === TOPS.length - 1 ? 'rounded-r-md' : ''} ${ask.top === top ? 'relative border-accent bg-accent/15 text-fg' : 'text-muted hover:text-fg'} ${focusRing}`}
              >
                {top === 0 ? 'All' : top}
              </button>
            ))}
          </div>
          <label className="flex items-center gap-1.5 text-xs text-muted">
            <span>Order</span>
            <select
              data-matrix-order=""
              value={ask.order}
              onChange={(event) => onAsk({ sort: event.target.value })}
              className={`h-8 rounded-md border border-line bg-panel-2 px-2 text-xs text-fg ${focusRing}`}
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
            className={`h-8 rounded-md border px-2 text-xs ${ask.active ? 'border-accent bg-accent/15 text-fg' : 'border-line text-muted hover:text-fg'} ${focusRing}`}
          >
            Active
          </button>
          <span data-matrix-count="" className="rounded-sm border border-line px-1.5 py-0.5 text-2xs text-muted tabular-nums" title={`${String(shown)} drawn of ${String(total)}${folded > 0 ? `, ${String(folded)} in others` : ''}`}>
            {shown} / {total}
          </span>
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
                    <Heading node={n} onNamespace={onNamespace} onOthers={showAll} vertical lit={cross?.to === n.id} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody onMouseOver={onOver} onMouseLeave={() => setCross(null)}>
              {nodes.map((from) => (
                <tr key={from.id}>
                  <th scope="row" data-row={from.id} data-lit={cross?.from === from.id ? '' : undefined} className={`sticky left-0 z-10 rounded-sm px-1 text-right font-normal whitespace-nowrap ${cross?.from === from.id ? 'bg-panel-2' : 'bg-panel'}`}>
                    <Heading node={from} onNamespace={onNamespace} onOthers={showAll} lit={cross?.from === from.id} />
                  </th>
                  {nodes.map((to) => (
                    <Cell key={to.id} from={from} to={to} cell={cells.get(`${from.id}>${to.id}`)} showRules={showRules} showFlows={showFlows} />
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Legend />
    </div>
  );
}

type State = 'allowed' | 'denied' | 'unevaluated' | 'refused' | 'open' | 'inside' | 'folded';

/** What the rules say of one pair. A namespace to itself is allowed when a
 *  rule speaks between its own pods (`internal`). */
function stateOf(from: GraphNode, to: GraphNode, cell: CellData | undefined): State {
  if (cell !== undefined && cell.deny.length > 0) return 'denied';
  if (cell !== undefined && cell.allow.length > 0) return 'allowed';
  if (cell !== undefined && cell.unevaluated.length > 0) return 'unevaluated';
  if (from.id === to.id && to.internal > 0) return 'inside';
  if (to.kind === 'others') return 'folded';
  if (to.kind === 'namespace' && to.pods > 0 && to.isolatedIn >= to.pods) return 'refused';
  return 'open';
}

const LOOK: Record<State, string> = {
  allowed: 'bg-accent/45',
  inside: 'bg-accent/25',
  denied: 'bg-bad/60',
  unevaluated: 'border border-dotted border-muted',
  refused: 'bg-panel-2 text-muted',
  open: 'border border-line',
  folded: 'border border-dashed border-line',
};

const WORD: Record<State, string> = {
  allowed: 'allowed by a rule',
  inside: 'allowed between its own pods',
  denied: 'denied by a rule',
  unevaluated: 'a rule not evaluated here (FQDN, Service, layer 7)',
  refused: 'refused: the destination is isolated and no rule lets this in',
  open: 'open: no policy isolates the destination',
  folded: 'no rule toward the folded namespaces',
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
}: {
  from: GraphNode;
  to: GraphNode;
  cell: CellData | undefined;
  showRules: boolean;
  showFlows: boolean;
}): ReactNode {
  const state = stateOf(from, to, cell);
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
      {showRules && state === 'refused' ? <span aria-hidden="true">×</span> : null}
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
  onOthers: () => void;
  vertical?: boolean;
  lit?: boolean;
}): ReactNode {
  const text = <span className={vertical ? 'inline-block max-h-28 truncate [writing-mode:vertical-rl] rotate-180' : 'inline-block max-w-40 truncate'}>{node.label}</span>;
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

function Legend(): ReactNode {
  const items: { state: State; label: string }[] = [
    { state: 'allowed', label: 'allowed' },
    { state: 'denied', label: 'denied' },
    { state: 'refused', label: 'refused' },
    { state: 'open', label: 'open' },
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
