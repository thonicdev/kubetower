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
    expect(f.shown).toBe(24);
    // 120 namespaces and world, which only a flow names.
    expect(f.total).toBe(121);
    expect(f.folded).toBe(97);
    expect(f.nodes).toHaveLength(25);
    expect(f.nodes.at(-1)).toMatchObject({ id: 'others', label: '97 others', kind: 'others' });
  });

  it('orders by traffic, then by rules, then by name', () => {
    const byTraffic = fold(graph(), flows, true, ask()).nodes.map((n) => n.label);
    expect(byTraffic.slice(0, 3)).toEqual(['team-050', 'team-051', 'team-052']);
    const byRules = fold(graph(), flows, true, ask({ order: 'rules' })).nodes.map((n) => n.label);
    expect(byRules[0]).toBe('team-000');
    const byName = fold(graph(), flows, true, ask({ order: 'name', top: 0 })).nodes.map((n) => n.label);
    expect(byName.slice(0, 2)).toEqual(['team-000', 'team-001']);
    expect(byName).not.toContain('97 others');
  });

  it('keeps only what Find names, case-insensitive', () => {
    const f = fold(graph(), flows, true, ask({ find: 'TEAM-05' }));
    expect(f.nodes.filter((n) => n.kind !== 'others').map((n) => n.label)).toEqual(
      expect.arrayContaining(['team-050', 'team-051', 'team-052', 'team-053', 'team-059']),
    );
    expect(f.shown).toBe(10);
    expect(f.folded).toBe(111);
  });

  it('draws every namespace when top is 0', () => {
    const f = fold(graph(), flows, true, ask({ top: 0 }));
    expect(f.shown).toBe(121);
    expect(f.nodes.some((n) => n.kind === 'others')).toBe(false);
  });

  it('leaves out the namespaces nothing names when asked for the active ones', () => {
    const f = fold(graph(), flows, true, ask({ active: true, top: 0 }));
    // team-000..009 by rules, team-050/051/052/060 and world by flows.
    expect(f.shown).toBe(15);
  });

  it('sums the folded namespaces rules and flows in others', () => {
    const f = fold(graph(), flows, true, ask({ top: 2, order: 'rules' }));
    // team-000 and team-001 kept; team-002..009 allow into team-000 through others.
    const intoZero = f.cells.get('others>ns:team-000');
    expect(intoZero?.allow).toEqual(['8080/TCP · p']);
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

describe('foldAskOf', () => {
  it('reads the address and defaults what is absent or wrong', () => {
    expect(foldAskOf(new URLSearchParams(''))).toEqual({ find: '', top: 24, order: 'traffic', active: false });
    expect(foldAskOf(new URLSearchParams('mq=pay&top=0&sort=name&active=1'))).toEqual({ find: 'pay', top: 0, order: 'name', active: true });
    expect(foldAskOf(new URLSearchParams('top=7&sort=size'))).toMatchObject({ top: 24, order: 'traffic' });
  });
});
