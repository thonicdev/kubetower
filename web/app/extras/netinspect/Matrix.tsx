import type { ReactNode } from 'react';
import { focusRing } from '@kubetower/ui';

import type { FlowEdge, Graph, GraphNode } from './data';

/**
 * Every namespace against every namespace, in place of the cluster's graph:
 * one row per source, one column per destination, and in each cell what the
 * rules say - allowed, denied, or refused because the destination is isolated
 * and nothing lets the source in - and what Hubble saw. A row or column
 * heading opens that namespace.
 *
 * Built from the overview's graph (the rules, already bounded at 40 nodes
 * with the rest folded) and the flows' edges: no read of its own.
 */
export function Matrix({
  graph,
  flows,
  showRules,
  showFlows,
  onNamespace,
}: {
  graph: Graph;
  flows: readonly FlowEdge[];
  showRules: boolean;
  showFlows: boolean;
  onNamespace: (namespace: string) => void;
}): ReactNode {
  const nodes = [...graph.nodes.filter((n) => n.kind !== 'workload'), ...(showFlows ? flowOnly(graph, flows) : [])];
  const cells = cellsOf(nodes, graph, flows);
  return (
    <div className="flex flex-col gap-2" data-matrix="">
      <div className="overflow-auto rounded-lg border border-line bg-panel p-2">
        <table className="border-separate border-spacing-0.5 text-2xs">
          <thead>
            <tr>
              <th className="sticky left-0 bg-panel px-1 text-right font-normal text-muted">from ╲ to</th>
              {nodes.map((n) => (
                <th key={n.id} scope="col" className="h-28 w-6 align-bottom font-normal">
                  <Heading node={n} onNamespace={onNamespace} vertical />
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {nodes.map((from) => (
              <tr key={from.id}>
                <th scope="row" className="sticky left-0 bg-panel px-1 text-right font-normal whitespace-nowrap">
                  <Heading node={from} onNamespace={onNamespace} />
                </th>
                {nodes.map((to) => (
                  <Cell key={to.id} from={from} to={to} cell={cells.get(`${from.id}>${to.id}`)} showRules={showRules} showFlows={showFlows} />
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Legend />
    </div>
  );
}

/** What Hubble saw at an end no rule names (`entity:world`): a row and a
 *  column of its own, at most ten. */
function flowOnly(graph: Graph, flows: readonly FlowEdge[]): GraphNode[] {
  const known = new Set(graph.nodes.map((n) => n.id));
  const extra = new Map<string, GraphNode>();
  for (const e of flows) {
    for (const id of [e.from, e.to]) {
      if (known.has(id) || extra.has(id) || extra.size >= 10 || id.startsWith('wl:')) continue;
      extra.set(id, { id, label: id.replace(/^[a-z]+:/, ''), kind: 'entity', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 });
    }
  }
  return [...extra.values()];
}

interface CellData {
  allow: string[];
  deny: string[];
  unevaluated: string[];
  forwarded: number;
  dropped: number;
}

/** The rules and the flows of every pair, keyed `from>to` on the graph's ids. */
function cellsOf(nodes: readonly GraphNode[], graph: Graph, flows: readonly FlowEdge[]): Map<string, CellData> {
  const out = new Map<string, CellData>();
  const at = (key: string) => {
    let c = out.get(key);
    if (c === undefined) {
      c = { allow: [], deny: [], unevaluated: [], forwarded: 0, dropped: 0 };
      out.set(key, c);
    }
    return c;
  };
  for (const e of graph.edges) {
    const c = at(`${e.from}>${e.to}`);
    const what = `${e.ports.join(', ')} · ${e.policies.join(', ')}`;
    if (e.kind === 'deny') c.deny.push(what);
    else if (e.kind === 'allow') c.allow.push(what);
    else c.unevaluated.push(what);
  }
  const known = new Set(nodes.map((n) => n.id));
  for (const e of flows) {
    if (!known.has(e.from) || !known.has(e.to)) continue;
    const c = at(`${e.from}>${e.to}`);
    c.forwarded += e.forwarded;
    c.dropped += e.dropped;
  }
  return out;
}

type State = 'allowed' | 'denied' | 'unevaluated' | 'refused' | 'open' | 'inside';

/** What the rules say of one pair. A namespace to itself is allowed when a
 *  rule speaks between its own pods (`internal`). */
function stateOf(from: GraphNode, to: GraphNode, cell: CellData | undefined): State {
  if (cell !== undefined && cell.deny.length > 0) return 'denied';
  if (cell !== undefined && cell.allow.length > 0) return 'allowed';
  if (cell !== undefined && cell.unevaluated.length > 0) return 'unevaluated';
  if (from.id === to.id && to.internal > 0) return 'inside';
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
};

const WORD: Record<State, string> = {
  allowed: 'allowed by a rule',
  inside: 'allowed between its own pods',
  denied: 'denied by a rule',
  unevaluated: 'a rule not evaluated here (FQDN, Service, layer 7)',
  refused: 'refused: the destination is isolated and no rule lets this in',
  open: 'open: no policy isolates the destination',
};

function Cell({
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
    ...(cell?.allow.map((a) => `allow ${a}`) ?? []),
    ...(cell?.deny.map((d) => `deny ${d}`) ?? []),
    ...(cell?.unevaluated.map((u) => `not evaluated ${u}`) ?? []),
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
}

function Heading({ node, onNamespace, vertical = false }: { node: GraphNode; onNamespace: (namespace: string) => void; vertical?: boolean }): ReactNode {
  const text = <span className={vertical ? 'inline-block max-h-28 truncate [writing-mode:vertical-rl] rotate-180' : 'inline-block max-w-40 truncate'}>{node.label}</span>;
  if (node.kind !== 'namespace' || node.namespace === undefined) {
    return (
      <span title={node.label} className="text-muted">
        {text}
      </span>
    );
  }
  const ns = node.namespace;
  return (
    <button type="button" data-heading={ns} title={`${node.label} · ${String(node.pods)} pods`} onClick={() => onNamespace(ns)} className={`text-fg hover:text-accent ${focusRing}`}>
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
