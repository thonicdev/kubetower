import type { FlowEdge, Graph, GraphNode } from './data';

/**
 * The namespace matrix's folding: which rows and columns it draws, and what
 * the rest add up to.
 *
 * The server sends the cluster's graph whole up to its bound (`view.go`
 * `maxNodes`); a matrix of every one of them is unreadable past a few dozen.
 * So the page keeps the busiest `top` - or the ones a search names - and folds
 * the rest into one "others" row and column, which carries their rules and
 * their flows summed. Nothing is lost: "others" says how many it holds, and
 * pressing it shows them all.
 */

export type Order = 'traffic' | 'rules' | 'name';

export const ORDERS: readonly Order[] = ['traffic', 'rules', 'name'];

/** The sizes offered. The largest is a bound, not "all": a few hundred
 *  namespaces squared is tens of thousands of cells. Find reaches the rest
 *  of what the server sent; a namespace the server folded into its own
 *  "others" (a dense cluster, `maxNodes` in `extras/netinspect/view.go`)
 *  has no row to find. */
export const TOPS: readonly number[] = [12, 24, 48, 96];

export const DEFAULT_TOP = 24;

export const OTHERS = 'others';

/** At most this many ends no rule names (`entity:world`) get a column. */
const ENTITY_COLUMNS = 10;

/** The peers that name every pod of the cluster: a rule's empty peer, and Cilium's `all` and `cluster` entities. */
const EVERYONE = new Set(['anyone', 'entity:all', 'entity:cluster']);

/** Whether a rule whose peer is `everyone` covers the node: `cluster` names the cluster's pods only. */
function covers(everyone: string, n: GraphNode): boolean {
  if (everyone !== 'entity:cluster') return true;
  return n.kind === 'namespace' || n.kind === 'others';
}

export interface FoldAsk {
  /** Only the namespaces whose name holds this, case-insensitive. */
  find: string;
  /** How many to draw, one of TOPS. */
  top: number;
  order: Order;
  /** Leave out the namespaces no rule and no flow names. */
  active: boolean;
}

export interface CellData {
  allow: string[];
  deny: string[];
  unevaluated: string[];
  /** An allow from the destination's ingress rules covers the pair. */
  allowIn: boolean;
  /** An allow from the source's egress rules covers the pair. */
  allowOut: boolean;
  forwarded: number;
  dropped: number;
}

export interface Folded {
  /** The rows and columns, "others" last when anything was folded. */
  nodes: GraphNode[];
  /** Keyed `from>to` on the drawn ids. */
  cells: Map<string, CellData>;
  /** Namespaces drawn. */
  shown: number;
  /** Every namespace the answer names, those the server folded included. */
  total: number;
  /** Namespaces "others" holds. */
  folded: number;
  /** Ends that are not namespaces (world, a CIDR) "others" holds. */
  foldedEnds: number;
  /** The server left edges out: a pair with no rule is not known to be refused. */
  truncated: boolean;
}

/** Reads the address's spelling of the ask, defaulting what is absent or wrong. */
export function foldAskOf(params: URLSearchParams): FoldAsk {
  const raw = params.get('top') ?? '';
  const top = /^[0-9]+$/.test(raw) ? Number(raw) : DEFAULT_TOP;
  const order = params.get('sort') as Order | null;
  return {
    find: params.get('mq') ?? '',
    top: TOPS.includes(top) ? top : DEFAULT_TOP,
    order: order !== null && ORDERS.includes(order) ? order : 'traffic',
    active: params.get('active') === '1',
  };
}

interface Weight {
  traffic: number;
  rules: number;
}

export function fold(graph: Graph, flows: readonly FlowEdge[], showFlows: boolean, ask: FoldAsk): Folded {
  // Every row the answer could draw: the graph's namespaces and ends, then the
  // ends only a flow names. The server's own "others" is not one: what it
  // holds is folded here too.
  const base = graph.nodes.filter((n) => n.kind !== 'workload' && n.kind !== 'others');
  const serverOthers = graph.nodes.find((n) => n.kind === 'others');
  const known = new Map(base.map((n) => [n.id, n]));
  const seen = showFlows ? flows : [];
  let entities = 0;
  for (const e of seen) {
    for (const id of [e.from, e.to]) {
      if (known.has(id) || id.startsWith('wl:') || id.startsWith('ns:') || entities >= ENTITY_COLUMNS) continue;
      const node: GraphNode = { id, label: id.replace(/^[a-z]+:/, ''), kind: 'entity', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 };
      known.set(id, node);
      base.push(node);
      entities++;
    }
  }
  // A namespace a flow names and the graph does not was folded by the server:
  // it belongs to "others", never to a column of its own.
  const home = (id: string): string | undefined => (known.has(id) ? id : id === OTHERS || id.startsWith('ns:') ? OTHERS : undefined);

  const weight = new Map<string, Weight>(base.map((n) => [n.id, { traffic: 0, rules: 0 }]));
  for (const e of graph.edges) {
    for (const id of new Set([e.from, e.to])) {
      const w = weight.get(id);
      if (w !== undefined) w.rules += e.rules;
    }
  }
  for (const e of seen) {
    for (const id of new Set([e.from, e.to])) {
      const w = weight.get(id);
      if (w !== undefined) w.traffic += e.forwarded + e.dropped;
    }
  }

  const needle = ask.find.trim().toLowerCase();
  const candidates = base.filter((n) => {
    if (needle !== '' && !n.label.toLowerCase().includes(needle)) return false;
    const w = weight.get(n.id);
    return !ask.active || n.internal > 0 || (w !== undefined && w.traffic + w.rules > 0);
  });
  const w = (n: GraphNode) => weight.get(n.id) ?? { traffic: 0, rules: 0 };
  const byName = (a: GraphNode, b: GraphNode) => a.label.localeCompare(b.label) || a.id.localeCompare(b.id);
  candidates.sort((a, b) => {
    if (ask.order === 'name') return byName(a, b);
    const [x, y] = [w(a), w(b)];
    const first = ask.order === 'traffic' ? y.traffic - x.traffic || y.rules - x.rules : y.rules - x.rules || y.traffic - x.traffic;
    return first || b.pods - a.pods || byName(a, b);
  });
  const kept = candidates.slice(0, ask.top);
  const keptIds = new Set(kept.map((n) => n.id));

  const others: GraphNode = { id: OTHERS, label: '', kind: 'others', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 };
  // The server's "others" is counted as namespaces: it folds the least
  // connected nodes, which are namespaces but for the rarest cluster.
  let folded = serverOthers === undefined ? 0 : graph.folded;
  let foldedEnds = 0;
  if (serverOthers !== undefined) {
    others.pods += serverOthers.pods;
    others.isolatedIn += serverOthers.isolatedIn;
    others.isolatedOut += serverOthers.isolatedOut;
  }
  for (const n of base) {
    if (keptIds.has(n.id)) continue;
    if (n.kind === 'namespace') folded++;
    else foldedEnds++;
    others.pods += n.pods;
    others.isolatedIn += n.isolatedIn;
    others.isolatedOut += n.isolatedOut;
  }
  others.label = `${String(folded > 0 ? folded : foldedEnds)} others`;
  // The ends it holds, the server's included: an end never refuses, so an
  // "others" holding one is never taken as isolated (Matrix). A server that
  // did not count them may hold one.
  others.ends = foldedEnds + (serverOthers === undefined ? 0 : (serverOthers.ends ?? 1));
  const any = folded + foldedEnds > 0;
  const nodes = any ? [...kept, others] : kept;
  const drawn = (id: string): string | undefined => {
    const at = home(id);
    if (at === undefined) return undefined;
    if (at === OTHERS) return any ? OTHERS : undefined;
    return keptIds.has(at) ? at : any ? OTHERS : undefined;
  };

  const cells = new Map<string, CellData>();
  const at = (key: string) => {
    let c = cells.get(key);
    if (c === undefined) {
      c = { allow: [], deny: [], unevaluated: [], allowIn: false, allowOut: false, forwarded: 0, dropped: 0 };
      cells.set(key, c);
    }
    return c;
  };
  // Each rule is kept with the side it belongs to: an egress rule of the
  // source and an ingress rule of the destination are two halves, and a
  // pair is allowed only where each isolated side has its own (Matrix).
  const add = (key: string, e: (typeof graph.edges)[number]) => {
    const c = at(key);
    const side = e.side === 'both' ? 'in and out' : e.side === 'ingress' ? 'in' : 'out';
    const what = `${side} · ${e.ports.join(', ')} · ${e.policies.join(', ')}`;
    const list = e.kind === 'deny' ? c.deny : e.kind === 'allow' ? c.allow : c.unevaluated;
    if (!list.includes(what)) list.push(what);
    if (e.kind === 'allow') {
      c.allowIn ||= e.side !== 'egress';
      c.allowOut ||= e.side !== 'ingress';
    }
  };
  // A baseline's rule is not consulted on a side a NetworkPolicy isolates:
  // the policy engine reads it only for a pod no NetworkPolicy selects.
  const isolatedSide = (id: string, n: (g: GraphNode) => number): boolean => {
    const node = known.get(id) ?? (id === OTHERS ? serverOthers : undefined);
    return node !== undefined && node.pods > 0 && n(node) >= node.pods;
  };
  const setAside = (e: (typeof graph.edges)[number]): boolean =>
    e.baseline === true &&
    (e.side === 'ingress' ? isolatedSide(e.to, (n) => n.isolatedIn) : e.side === 'egress' ? isolatedSide(e.from, (n) => n.isolatedOut) : isolatedSide(e.to, (n) => n.isolatedIn) && isolatedSide(e.from, (n) => n.isolatedOut));
  for (const e of graph.edges) {
    const from = drawn(e.from);
    const to = drawn(e.to);
    if (from === undefined || to === undefined || setAside(e)) continue;
    add(`${from}>${to}`, e);
    // A rule whose peer is everyone speaks for every pair it covers, not
    // only for the Anyone row or column: without this, every other cell
    // toward an isolated namespace would read refused. It is one side's
    // half only: an ingress one never answers for the source's egress, nor
    // an egress one for the destination's ingress.
    // Who it covers is read from the rule's own peer, not from where it is
    // drawn: a cluster entity folded into "others" still covers only the
    // namespaces.
    const covered = (drawnAt: string, peer: string) => nodes.filter((n) => n.id !== drawnAt && covers(peer, n));
    if (EVERYONE.has(e.from)) for (const n of covered(from, e.from)) add(`${n.id}>${to}`, e);
    if (EVERYONE.has(e.to)) for (const n of covered(to, e.to)) add(`${from}>${n.id}`, e);
  }
  for (const e of seen) {
    const from = drawn(e.from);
    const to = drawn(e.to);
    if (from === undefined || to === undefined) continue;
    const c = at(`${from}>${to}`);
    c.forwarded += e.forwarded;
    c.dropped += e.dropped;
  }
  const isNamespace = (n: GraphNode) => n.kind === 'namespace';
  return {
    nodes,
    cells,
    shown: kept.filter(isNamespace).length,
    total: base.filter(isNamespace).length + (serverOthers === undefined ? 0 : graph.folded),
    folded,
    foldedEnds,
    truncated: graph.truncated === true,
  };
}
