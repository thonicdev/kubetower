import type { Direction, PeerView, RuleKind, RuleView } from './data';
import { shortLabel } from './text';

/**
 * What a pod's Network tab draws, worked out from one direction's rules:
 * flows (one per rule and peer) for the rows, and at most five arrows for
 * the graph, aggregated by namespace when there are more peers than that.
 */

export type Side = 'ingress' | 'egress';

export const EVERY_PORT = 'every port';
/** Arrows a side of the graph draws, the `+N` one included. */
export const MAX_ARROWS = 5;
/** Rows a direction shows before `Show all`, and the count that brings a filter. */
export const MAX_ROWS = 8;
/** Rows past which an expanded list scrolls inside a bounded height. */
export const SCROLL_ROWS = 40;

const PROTO = /^(.*)\/(TCP|UDP|SCTP)$/;

function splitPort(p: string): { port: string; proto: string } {
  const m = PROTO.exec(p);
  return m === null ? { port: p, proto: '' } : { port: m[1] ?? p, proto: m[2] ?? '' };
}

/** Ports as the union of several lists: one `every port` makes the union that. */
export function unionPorts(lists: readonly (readonly string[])[]): string[] {
  const out: string[] = [];
  for (const list of lists) {
    if (list.length === 0 || list.includes(EVERY_PORT)) return [EVERY_PORT];
    for (const p of list) if (!out.includes(p)) out.push(p);
  }
  return out.length === 0 ? [EVERY_PORT] : out;
}

/**
 * Ports in a few characters: `TCP 80, 443` for up to `max` ports of one
 * protocol, `80, 443 +2` beyond that, `every port` for no filter. The whole
 * list belongs in the tooltip.
 */
export function portsLabel(ports: readonly string[], max = 2): string {
  const list = unionPorts([ports]);
  if (list.length === 1 && list[0] === EVERY_PORT) return EVERY_PORT;
  const shown = list.slice(0, max);
  const rest = list.length - shown.length;
  const parts = shown.map(splitPort);
  const proto = parts[0]?.proto ?? '';
  const uniform = parts.every((p) => p.proto === proto);
  const words = parts.map((p, i) => (uniform ? p.port : (shown[i] ?? p.port))).join(', ');
  if (rest > 0) return `${words} +${String(rest)}`;
  return uniform && proto !== '' ? `${proto} ${words}` : words;
}

export interface PeerText {
  /** The selector, or the whole label for a peer that is not pods. */
  selector: string;
  /** The namespace the pods are in; empty for any other peer. */
  ns: string;
  /** `3 pods`; empty for any other peer. */
  sub: string;
}

const POD_LABEL = /^(?:pods|endpoints) (.+?) in (this namespace|namespaces? .+|any namespace)$/;

/** Who a peer is: its selector and namespace first, its pod count second. */
export function peerText(p: PeerView, podNamespace: string): PeerText {
  if (p.kind === 'anyone') return { selector: 'anyone', ns: '', sub: '' };
  if (p.kind !== 'pods') return { selector: p.label, ns: '', sub: '' };
  const sub = p.pods === 0 ? 'no pod now' : `${String(p.pods)} ${p.pods === 1 ? 'pod' : 'pods'}`;
  const m = POD_LABEL.exec(p.label);
  let selector = m?.[1] ?? p.label;
  let where = m?.[2] ?? '';
  if (m === null) {
    const every = /^every pod in (.+)$/.exec(p.label);
    if (every !== null) {
      selector = 'all pods';
      where = every[1] ?? '';
    } else if (p.label.startsWith('every pod')) {
      selector = 'all pods';
      where = 'any namespace';
    }
  }
  let ns: string;
  if (p.namespaces.length === 1) ns = p.namespaces[0] ?? '';
  else if (p.namespaces.length > 1) ns = `${String(p.namespaces.length)} namespaces`;
  else if (where === 'this namespace') ns = podNamespace;
  else ns = where.replace(/^namespaces? /, '');
  return { selector, ns, sub };
}

/** `selector · namespace` in at most `max` characters, the namespace cut first. */
export function peerName(t: PeerText, max: number): string {
  if (t.ns === '') return shortLabel(t.selector, max);
  const whole = `${t.selector} · ${t.ns}`;
  if ([...whole].length <= max) return whole;
  const ns = shortLabel(t.ns, Math.max(8, Math.floor(max / 3)));
  return `${shortLabel(t.selector, Math.max(6, max - [...ns].length - 3))} · ${ns}`;
}

export interface Flow {
  id: string;
  side: Side;
  kind: RuleKind;
  peer: PeerView;
  peerKey: string;
  text: PeerText;
  ports: string[];
  policy: string;
  ruleNo: number;
  /** What the filter matches. */
  search: string;
}

const KIND_ORDER: Record<RuleKind, number> = { deny: 0, allow: 1, pass: 2, unevaluated: 3 };
const EVERYONE: PeerView = { kind: 'anyone', label: 'everyone', pods: 0, namespaces: [] };

/** One flow per rule and peer: deny first, then allow, then not evaluated;
 *  within each, by namespace then peer. */
export function flowsOf(direction: Direction, side: Side, podNamespace: string): Flow[] {
  const out: Flow[] = [];
  for (const r of direction.rules) {
    const peers = r.peers.length === 0 ? [EVERYONE] : r.peers;
    peers.forEach((peer, i) => out.push(flowOf(r, peer, `${side}:${r.name}/${String(r.rule)}:${String(i)}`, side, podNamespace)));
  }
  return out
    .map((f, i) => ({ f, i }))
    .sort((a, b) => KIND_ORDER[a.f.kind] - KIND_ORDER[b.f.kind] || a.f.text.ns.localeCompare(b.f.text.ns) || a.f.text.selector.localeCompare(b.f.text.selector) || a.i - b.i)
    .map((x) => x.f);
}

function flowOf(r: RuleView, peer: PeerView, id: string, side: Side, podNamespace: string): Flow {
  const text = peerText(peer, podNamespace);
  const ports = r.ports.length === 0 ? [EVERY_PORT] : r.ports;
  return {
    id,
    side,
    kind: r.kind,
    peer,
    peerKey: `${peer.kind}:${peer.label}`,
    text,
    ports,
    policy: r.name,
    ruleNo: r.rule,
    search: [text.selector, text.ns, text.sub, peer.label, ports.join(' '), r.name, `rule ${String(r.rule)}`, r.kind].join(' ').toLowerCase(),
  };
}

export interface Arrow {
  key: string;
  /** The whole name, for the tooltip. */
  name: string;
  text: PeerText;
  ports: string[];
  flowIds: string[];
  /** Stands for several peers: pressing it filters the rows. */
  aggregate: boolean;
  /** The `+N` arrow. */
  more: boolean;
  /** What the tooltip lists besides the ports. */
  members: string[];
}

function groupOf(f: Flow): { key: string; name: string; plural: string } {
  switch (f.peer.kind) {
    case 'pods':
      return { key: `ns:${f.text.ns}`, name: f.text.ns, plural: '' };
    case 'cidr':
      return { key: 'cidr', name: 'CIDRs', plural: 'CIDRs' };
    case 'entity':
      return { key: 'entity', name: 'entities', plural: 'entities' };
    default:
      return { key: 'anyone', name: 'anyone', plural: '' };
  }
}

/**
 * What the graph draws for one side: the allow flows' peers, one arrow each
 * up to five; beyond that one arrow per namespace, and past five of those
 * the four largest and a `+N` for the rest.
 */
export function arrowsOf(flows: readonly Flow[]): Arrow[] {
  const allowed = flows.filter((f) => f.kind === 'allow');
  const peers = new Map<string, Flow[]>();
  for (const f of allowed) peers.set(f.peerKey, [...(peers.get(f.peerKey) ?? []), f]);
  if (peers.size <= MAX_ARROWS) {
    return [...peers.entries()].map(([key, fs]) => {
      const first = fs[0] as Flow;
      return {
        key,
        name: first.text.ns === '' ? first.text.selector : `${first.text.selector} · ${first.text.ns}`,
        text: first.text,
        ports: unionPorts(fs.map((f) => f.ports)),
        flowIds: fs.map((f) => f.id),
        aggregate: false,
        more: false,
        members: [],
      };
    });
  }
  const groups = new Map<string, { name: string; plural: string; flows: Flow[]; peers: Map<string, number> }>();
  for (const f of allowed) {
    const g = groupOf(f);
    const entry = groups.get(g.key) ?? { name: g.name, plural: g.plural, flows: [], peers: new Map<string, number>() };
    entry.flows.push(f);
    entry.peers.set(f.peerKey, f.peer.pods);
    groups.set(g.key, entry);
  }
  const sorted = [...groups.entries()].sort((a, b) => b[1].peers.size - a[1].peers.size || a[1].name.localeCompare(b[1].name));
  const toArrow = ([key, g]: [string, (typeof sorted)[number][1]]): Arrow => {
    const pods = [...g.peers.values()].reduce((n, x) => n + x, 0);
    const count = g.peers.size;
    const name = g.plural === '' ? (pods > 0 ? `${g.name} · ${String(pods)} ${pods === 1 ? 'pod' : 'pods'}` : g.name) : `${String(count)} ${count === 1 ? g.plural.replace(/ies$/, 'y').replace(/s$/, '') : g.plural}`;
    return {
      key,
      name,
      text: { selector: name, ns: '', sub: '' },
      ports: unionPorts(g.flows.map((f) => f.ports)),
      flowIds: g.flows.map((f) => f.id),
      aggregate: true,
      more: false,
      members: [...g.peers.keys()].map((k) => k.replace(/^[a-z]+:/, '')),
    };
  };
  if (sorted.length <= MAX_ARROWS) return sorted.map(toArrow);
  const head = sorted.slice(0, MAX_ARROWS - 1).map(toArrow);
  const rest = sorted.slice(MAX_ARROWS - 1);
  const restFlows = rest.flatMap(([, g]) => g.flows);
  return [
    ...head,
    {
      key: 'more',
      name: `+${String(rest.length)}`,
      text: { selector: `+${String(rest.length)} more`, ns: '', sub: '' },
      ports: unionPorts(restFlows.map((f) => f.ports)),
      flowIds: restFlows.map((f) => f.id),
      aggregate: true,
      more: true,
      members: rest.map(([, g]) => g.name),
    },
  ];
}

/** Rows matching `query` against peer, ports and policy. */
export function filterFlows(flows: readonly Flow[], query: string): Flow[] {
  const q = query.trim().toLowerCase();
  return q === '' ? [...flows] : flows.filter((f) => f.search.includes(q));
}
