import { describe, expect, it } from 'vitest';

import type { FlowEdge, Graph, GraphEdge, GraphNode } from './data';
import { DEFAULT_TOP, fold, foldAskOf, type FoldAsk } from './fold';

const ns = (i: number, extra: Partial<GraphNode> = {}): GraphNode => {
  const name = `team-${String(i).padStart(3, '0')}`;
  return { id: `ns:${name}`, label: name, kind: 'namespace', namespace: name, pods: 2, isolatedIn: 0, isolatedOut: 0, internal: 0, ...extra };
};
const allow = (from: string, to: string, rules = 1, policy = 'p'): GraphEdge => ({ kind: 'allow', from, to, side: 'ingress', ports: ['8080/TCP'], policies: [policy], rules });
const flow = (from: string, to: string, forwarded: number, dropped = 0): FlowEdge => ({ from, to, forwarded, dropped, ports: [], deniedBy: [] });

/** 120 namespaces; team-000..009 joined by rules, team-050 the busiest by traffic. */
function graph(): Graph {
  const nodes = Array.from({ length: 120 }, (_, i) => ns(i));
  const edges = Array.from({ length: 10 }, (_, i) => allow(`ns:team-${String(i).padStart(3, '0')}`, 'ns:team-000', 10 - i));
  return { scope: 'cluster', nodes, edges, folded: 0, edgesTotal: edges.length };
}
const flows: FlowEdge[] = [flow('ns:team-050', 'ns:team-051', 900, 3), flow('ns:team-052', 'ns:team-050', 50), flow('ns:team-060', 'entity:world', 7)];
const ask = (over: Partial<FoldAsk> = {}): FoldAsk => ({ find: '', top: DEFAULT_TOP, order: 'traffic', active: false, ...over });

describe('fold', () => {
  it('draws the 24 busiest and folds the rest into others', () => {
    const f = fold(graph(), flows, true, ask());
    // world, which only a flow names, ranks among the 24 but is not a namespace.
    expect(f.shown).toBe(23);
    expect(f.total).toBe(120);
    expect(f.folded).toBe(97);
    expect(f.foldedEnds).toBe(0);
    expect(f.nodes).toHaveLength(25);
    expect(f.nodes.at(-1)).toMatchObject({ id: 'others', label: '97 others', kind: 'others' });
  });

  it('orders by traffic, then by rules, then by name', () => {
    const byTraffic = fold(graph(), flows, true, ask()).nodes.map((n) => n.label);
    expect(byTraffic.slice(0, 3)).toEqual(['team-050', 'team-051', 'team-052']);
    const byRules = fold(graph(), flows, true, ask({ order: 'rules' })).nodes.map((n) => n.label);
    expect(byRules[0]).toBe('team-000');
    const byName = fold(graph(), flows, true, ask({ order: 'name', top: 96 })).nodes.map((n) => n.label);
    expect(byName.slice(0, 2)).toEqual(['team-000', 'team-001']);
    // 24 namespaces and world folded: the label counts the namespaces.
    expect(byName.at(-1)).toBe('24 others');
  });

  it('keeps only what Find names, case-insensitive', () => {
    const f = fold(graph(), flows, true, ask({ find: 'TEAM-05' }));
    expect(f.nodes.filter((n) => n.kind !== 'others').map((n) => n.label)).toEqual(
      expect.arrayContaining(['team-050', 'team-051', 'team-052', 'team-053', 'team-059']),
    );
    expect(f.shown).toBe(10);
    expect(f.folded).toBe(110);
    expect(f.foldedEnds).toBe(1);
  });

  it('draws at most the largest size, the rest in others', () => {
    const f = fold(graph(), flows, true, ask({ top: 96 }));
    expect(f.nodes).toHaveLength(97);
    expect(f.nodes.at(-1)?.kind).toBe('others');
  });

  it('leaves out the namespaces nothing names when asked for the active ones', () => {
    const f = fold(graph(), flows, true, ask({ active: true, top: 96 }));
    // team-000..009 by rules, team-050/051/052/060 by flows; world too, but it is not a namespace.
    expect(f.shown).toBe(14);
    expect(f.nodes.filter((n) => n.kind !== 'others')).toHaveLength(15);
  });

  it('sums the folded namespaces rules and flows in others', () => {
    const f = fold(graph(), flows, true, ask({ top: 2, order: 'rules' }));
    // team-000 and team-001 kept; team-002..009 allow into team-000 through others.
    const intoZero = f.cells.get('others>ns:team-000');
    expect(intoZero?.allow).toEqual(['in · 8080/TCP · p']);
    const traffic = f.cells.get('others>others');
    expect(traffic?.forwarded).toBe(957);
    expect(traffic?.dropped).toBe(3);
  });

  it('counts a flow to a namespace the graph folded in others, never as a column', () => {
    const g = graph();
    g.nodes = [...g.nodes.slice(0, 3), { id: 'others', label: '117 others', kind: 'others', pods: 234, isolatedIn: 0, isolatedOut: 0, internal: 0 }];
    g.folded = 117;
    const f = fold(g, [flow('ns:team-000', 'ns:team-099', 12)], true, ask());
    expect(f.nodes.map((n) => n.id)).toEqual(['ns:team-000', 'ns:team-001', 'ns:team-002', 'others']);
    expect(f.total).toBe(120);
    expect(f.cells.get('ns:team-000>others')?.forwarded).toBe(12);
  });

  it('draws no flow when the flows are hidden', () => {
    const f = fold(graph(), flows, false, ask());
    expect(f.nodes.some((n) => n.id === 'entity:world')).toBe(false);
    expect([...f.cells.values()].every((c) => c.forwarded + c.dropped === 0)).toBe(true);
  });
});

describe('a cut answer', () => {
  it('says so', () => {
    expect(fold({ ...graph(), truncated: true }, flows, true, ask()).truncated).toBe(true);
    expect(fold(graph(), flows, true, ask()).truncated).toBe(false);
  });
});

describe('foldAskOf', () => {
  it('reads the address and defaults what is absent or wrong', () => {
    expect(foldAskOf(new URLSearchParams(''))).toEqual({ find: '', top: 24, order: 'traffic', active: false });
    expect(foldAskOf(new URLSearchParams('mq=pay&top=96&sort=name&active=1'))).toEqual({ find: 'pay', top: 96, order: 'name', active: true });
    for (const bad of ['top=7', 'top=0', 'top=', 'top=-0', 'top=0x30', 'top=48.0']) {
      expect(foldAskOf(new URLSearchParams(`${bad}&sort=size`)), bad).toMatchObject({ top: 24, order: 'traffic' });
    }
  });
});

describe('a rule open to everyone', () => {
  const anyoneNode: GraphNode = { id: 'anyone', label: 'Anyone', kind: 'anyone', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 };
  const isolated = (i: number) => ns(i, { isolatedIn: 2 });
  const g = (edges: GraphEdge[], extra: GraphNode[] = [anyoneNode]): Graph => ({
    scope: 'cluster',
    nodes: [isolated(0), isolated(1), isolated(2), ...extra],
    edges,
    folded: 0,
    edgesTotal: edges.length,
  });

  it('allows every source into its namespace, its own included', () => {
    const f = fold(g([allow('anyone', 'ns:team-001', 1, 'open-to-all')]), [], true, ask());
    for (const from of ['ns:team-000', 'ns:team-001', 'ns:team-002', 'anyone']) {
      expect(f.cells.get(`${from}>ns:team-001`)?.allow, from).toEqual(['in · 8080/TCP · open-to-all']);
      expect(f.cells.get(`${from}>ns:team-001`), from).toMatchObject({ allowIn: true, allowOut: false });
    }
    expect(f.cells.get('ns:team-000>ns:team-002')).toBeUndefined();
  });

  it("carries an egress rule to every destination as the source's half only", () => {
    const f = fold(g([{ ...allow('ns:team-000', 'anyone', 1, 'out-to-all'), side: 'egress' }]), [], true, ask());
    for (const to of ['ns:team-001', 'ns:team-002']) {
      expect(f.cells.get(`ns:team-000>${to}`)?.allow, to).toEqual(['out · 8080/TCP · out-to-all']);
      expect(f.cells.get(`ns:team-000>${to}`), to).toMatchObject({ allowIn: false, allowOut: true });
    }
  });

  it('denies every source for a deny rule', () => {
    const f = fold(g([{ ...allow('anyone', 'ns:team-002', 1, 'deny-all'), kind: 'deny' }]), [], true, ask());
    expect(f.cells.get('ns:team-000>ns:team-002')?.deny).toEqual(['in · 8080/TCP · deny-all']);
  });

  it('counts the cluster entity for the namespaces, not for an end outside the cluster', () => {
    const world: GraphNode = { id: 'entity:world', label: 'world', kind: 'entity', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 };
    const cluster: GraphNode = { id: 'entity:cluster', label: 'cluster', kind: 'entity', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 };
    const f = fold(g([allow('entity:cluster', 'ns:team-001', 1, 'from-cluster')], [cluster, world]), [], true, ask());
    expect(f.cells.get('ns:team-000>ns:team-001')?.allow).toEqual(['in · 8080/TCP · from-cluster']);
    expect(f.cells.get('entity:world>ns:team-001')).toBeUndefined();
  });
});
