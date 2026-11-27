import { cleanup, fireEvent, render } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it } from 'vitest';

import type { FlowEdge, Graph, GraphNode } from './data';
import { foldAskOf } from './fold';
import { Matrix } from './Matrix';

afterEach(cleanup);

const ns = (i: number): GraphNode => {
  const name = `team-${String(i).padStart(3, '0')}`;
  return { id: `ns:${name}`, label: name, kind: 'namespace', namespace: name, pods: 2, isolatedIn: 0, isolatedOut: 0, internal: 0 };
};
const graph: Graph = { scope: 'cluster', nodes: Array.from({ length: 120 }, (_, i) => ns(i)), edges: [], folded: 0, edgesTotal: 0 };
const flows: FlowEdge[] = [{ from: 'ns:team-077', to: 'ns:team-000', forwarded: 40, dropped: 2, ports: [], deniedBy: [] }];

/** The matrix with its ask held as the page holds it, in an address. */
function Harness(): React.ReactNode {
  const [params, setParams] = useState(new URLSearchParams());
  return (
    <Matrix
      graph={graph}
      flows={flows}
      showRules
      showFlows
      ask={foldAskOf(params)}
      onAsk={(next) =>
        setParams((current) => {
          const out = new URLSearchParams(current);
          for (const [k, v] of Object.entries(next)) {
            if (v === '') out.delete(k);
            else out.set(k, v);
          }
          return out;
        })
      }
      onNamespace={() => undefined}
    />
  );
}

const rows = () => [...document.querySelectorAll('tbody th[data-row]')].map((th) => th.getAttribute('data-row'));

describe('the namespace matrix on 120 namespaces', () => {
  it('draws the busiest 24 and others, and counts them', () => {
    render(<Harness />);
    expect(rows()).toHaveLength(25);
    expect(rows()[0]).toBe('ns:team-000');
    expect(rows()[1]).toBe('ns:team-077');
    expect(rows().at(-1)).toBe('others');
    expect(document.querySelector('[data-matrix-count]')?.textContent).toBe('24 / 120');
    expect(document.querySelector('[data-cell="ns:team-077>ns:team-000"]')?.getAttribute('data-seen')).toBe('dropped');
  });

  it('draws the next size up when others is pressed, as far as the largest', () => {
    render(<Harness />);
    fireEvent.click(document.querySelector('[data-heading-others]') as Element);
    expect(rows()).toHaveLength(49);
    expect(document.querySelector('[data-top="48"]')?.getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(document.querySelector('[data-heading-others]') as Element);
    expect(rows()).toHaveLength(97);
    // At the largest size "others" is a heading, not a button.
    expect(document.querySelector('button[data-heading-others]')).toBeNull();
  });

  it('keeps only what Find names', () => {
    render(<Harness />);
    fireEvent.change(document.querySelector('input[name="mq"]') as Element, { target: { value: 'team-11' } });
    expect(rows()).toEqual(['ns:team-110', 'ns:team-111', 'ns:team-112', 'ns:team-113', 'ns:team-114', 'ns:team-115', 'ns:team-116', 'ns:team-117', 'ns:team-118', 'ns:team-119', 'others']);
    fireEvent.change(document.querySelector('input[name="mq"]') as Element, { target: { value: 'nothing-here' } });
    expect(document.querySelector('[data-matrix-none]')).not.toBeNull();
  });

  it('lights the row and column of the hovered cell', () => {
    render(<Harness />);
    fireEvent.mouseOver(document.querySelector('[data-cell="ns:team-077>ns:team-000"]') as Element);
    expect(document.querySelector('th[data-row="ns:team-077"]')?.hasAttribute('data-lit')).toBe(true);
    expect(document.querySelector('th[data-col="ns:team-000"]')?.hasAttribute('data-lit')).toBe(true);
    expect(document.querySelectorAll('th[data-lit]')).toHaveLength(2);
  });

  it('orders by name on request', () => {
    render(<Harness />);
    fireEvent.change(document.querySelector('[data-matrix-order]') as Element, { target: { value: 'name' } });
    expect(rows().slice(0, 2)).toEqual(['ns:team-000', 'ns:team-001']);
  });
});

/** 120 namespaces, each isolated and letting every namespace in, as the
 *  server sends them: folded until every edge between what is kept fits. */
function dense(kept: number, truncated: boolean): Graph {
  // Each lets its own pods in too: the server counts that rule on the node
  // (`internal`) and sends the engine's answer for it (`diagonal`).
  const nodes: GraphNode[] = Array.from({ length: kept }, (_, i) => ({ ...ns(i), isolatedIn: 2, internal: 1, diagonal: 'inside' as const }));
  nodes.push({ id: 'others', label: `${String(120 - kept)} others`, kind: 'others', pods: 2 * (120 - kept), isolatedIn: 2 * (120 - kept), isolatedOut: 0, internal: 0 });
  const edges = nodes.flatMap((a) =>
    nodes.filter((b) => b.id !== a.id).map((b) => ({ kind: 'allow' as const, from: a.id, to: b.id, side: 'ingress' as const, ports: ['every port'], policies: ['from-all'], rules: 1 })),
  );
  return { scope: 'cluster', nodes, edges: truncated ? edges.slice(0, 500) : edges, folded: 120 - kept, edgesTotal: 120 * 119, truncated };
}

describe('a cluster where every namespace lets every namespace in', () => {
  const mountGraph = (g: Graph) =>
    render(<Matrix graph={g} flows={[]} showRules showFlows ask={foldAskOf(new URLSearchParams('top=96'))} onAsk={() => undefined} onNamespace={() => undefined} />);

  it('draws no refused cell', () => {
    mountGraph(dense(44, false));
    const states = [...document.querySelectorAll('[data-cell]')].map((c) => c.getAttribute('data-state'));
    expect(states.length).toBeGreaterThan(44 * 44);
    expect(states).not.toContain('refused');
    expect(document.querySelector('[data-matrix-cut]')).toBeNull();
  });

  it('draws a cell a cut answer has no rule for as unknown, never refused, and says the rules were cut', () => {
    mountGraph(dense(44, true));
    const states = [...document.querySelectorAll('[data-cell]')].map((c) => c.getAttribute('data-state'));
    expect(states).not.toContain('refused');
    expect(states).not.toContain('open');
    expect(states).toContain('unknown');
    expect(document.querySelector('[data-matrix-cut]')).not.toBeNull();
  });
});

describe('a namespace open to everyone', () => {
  it('reads refused from no namespace', () => {
    const isolated = (i: number): GraphNode => ({ ...ns(i), isolatedIn: 2 });
    const g: Graph = {
      scope: 'cluster',
      nodes: [isolated(0), isolated(1), isolated(2), { id: 'anyone', label: 'Anyone', kind: 'anyone', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 }],
      edges: [{ kind: 'allow', from: 'anyone', to: 'ns:team-001', side: 'ingress', ports: ['every port'], policies: ['open-to-all'], rules: 1 }],
      folded: 0,
      edgesTotal: 1,
    };
    render(<Matrix graph={g} flows={[]} showRules showFlows ask={foldAskOf(new URLSearchParams())} onAsk={() => undefined} onNamespace={() => undefined} />);
    const column = [...document.querySelectorAll('[data-cell$=">ns:team-001"]')];
    expect(column).toHaveLength(4);
    expect(column.map((c) => c.getAttribute('data-state'))).toEqual(['allowed', 'allowed', 'allowed', 'allowed']);
    // A column no rule opens is still refused.
    expect(document.querySelector('[data-cell="ns:team-000>ns:team-002"]')?.getAttribute('data-state')).toBe('refused');
  });
});

describe('each isolated side needs its own allow', () => {
  const node = (i: number, inIso: boolean, outIso: boolean): GraphNode => ({ ...ns(i), isolatedIn: inIso ? 2 : 0, isolatedOut: outIso ? 2 : 0 });
  const anyone: GraphNode = { id: 'anyone', label: 'Anyone', kind: 'anyone', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 };
  const edge = (from: string, to: string, side: 'ingress' | 'egress', policy: string) => ({ kind: 'allow' as const, from, to, side, ports: ['every port'], policies: [policy], rules: 1 });
  const show = (g: Graph) => render(<Matrix graph={g} flows={[]} showRules showFlows ask={foldAskOf(new URLSearchParams())} onAsk={() => undefined} onNamespace={() => undefined} />);
  const state = (from: number, to: number) => document.querySelector(`[data-cell="ns:team-${String(from).padStart(3, '0')}>ns:team-${String(to).padStart(3, '0')}"]`)?.getAttribute('data-state');

  it('reads every pair refused under default-deny ingress and allow-all egress in every namespace', () => {
    const nodes = [0, 1, 2, 3].map((i) => node(i, true, true));
    const edges = nodes.map((n) => edge(n.id, 'anyone', 'egress', 'allow-all-egress'));
    show({ scope: 'cluster', nodes: [...nodes, anyone], edges, folded: 0, edgesTotal: edges.length });
    for (const a of [0, 1, 2, 3]) for (const b of [0, 1, 2, 3]) if (a !== b) expect(state(a, b), `${String(a)}→${String(b)}`).toBe('refused');
  });

  it('reads an egress allow toward an open destination as allowed', () => {
    show({ scope: 'cluster', nodes: [node(0, false, true), node(1, false, false), anyone], edges: [edge('ns:team-000', 'anyone', 'egress', 'out')], folded: 0, edgesTotal: 1 });
    expect(state(0, 1)).toBe('allowed');
  });

  it('reads an ingress allow from everyone as refused from a source isolated for egress', () => {
    show({ scope: 'cluster', nodes: [node(0, false, true), node(1, true, false), node(2, false, false), anyone], edges: [edge('anyone', 'ns:team-001', 'ingress', 'in')], folded: 0, edgesTotal: 1 });
    expect(state(0, 1)).toBe('refusedOut');
    expect(state(2, 1)).toBe('allowed');
  });

  it('reads the pair allowed when both halves are there', () => {
    show({
      scope: 'cluster',
      nodes: [node(0, true, true), node(1, true, true), anyone],
      edges: [edge('ns:team-000', 'anyone', 'egress', 'out'), edge('anyone', 'ns:team-001', 'ingress', 'in')],
      folded: 0,
      edgesTotal: 2,
    });
    expect(state(0, 1)).toBe('allowed');
    expect(state(1, 0)).toBe('refused');
  });

  it('never reads allowed on a cut answer, a rule open to everyone or not', () => {
    show({ scope: 'cluster', nodes: [node(0, false, false), node(1, true, false), anyone], edges: [edge('anyone', 'ns:team-001', 'ingress', 'in')], folded: 0, edgesTotal: 9, truncated: true });
    expect(state(0, 1)).toBe('unknown');
  });
});

describe('a cell touching others', () => {
  // Twenty namespaces drawn twelve at a time: eight are in "others".
  const many = (inIso: boolean, outIso: boolean): GraphNode[] => Array.from({ length: 20 }, (_, i) => ({ ...ns(i), isolatedIn: inIso ? 2 : 0, isolatedOut: outIso ? 2 : 0 }));
  const anyone: GraphNode = { id: 'anyone', label: 'Anyone', kind: 'anyone', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 };
  const edge = (from: string, to: string, side: 'ingress' | 'egress') => ({ kind: 'allow' as const, from, to, side, ports: ['every port'], policies: ['p'], rules: 1 });
  const show = (g: Graph) =>
    render(<Matrix graph={g} flows={[]} showRules showFlows ask={foldAskOf(new URLSearchParams('top=12&sort=name'))} onAsk={() => undefined} onNamespace={() => undefined} />);
  const states = (selector: string) => [...document.querySelectorAll(selector)].map((c) => c.getAttribute('data-state'));

  it('is never allowed under default-deny ingress and allow-all egress, and refused where every member refuses', () => {
    const nodes = many(true, true);
    show({ scope: 'cluster', nodes: [...nodes, anyone], edges: nodes.map((n) => edge(n.id, 'anyone', 'egress')), folded: 0, edgesTotal: 20 });
    expect(document.querySelector('[data-heading-others]')).not.toBeNull();
    const toOthers = states('[data-cell$=">others"]');
    const fromOthers = states('[data-cell^="others>"]');
    expect([...toOthers, ...fromOthers]).not.toContain('allowed');
    expect(states('[data-cell="ns:team-000>others"]')).toEqual(['refused']);
    expect(states('[data-cell="others>ns:team-000"]')).toEqual(['refused']);
  });

  it('is refused from others into an open-to-all namespace when every member is isolated for egress', () => {
    const nodes = many(false, true);
    show({ scope: 'cluster', nodes: [...nodes, anyone], edges: [edge('anyone', 'ns:team-001', 'ingress')], folded: 0, edgesTotal: 1 });
    expect(states('[data-cell="others>ns:team-001"]')).toEqual(['refusedOut']);
  });

  it('is folded, not allowed, where a member may allow', () => {
    const nodes = many(true, false);
    show({ scope: 'cluster', nodes, edges: [edge('ns:team-000', 'ns:team-015', 'ingress')], folded: 0, edgesTotal: 1 });
    expect(states('[data-cell="ns:team-000>others"]')).toEqual(['folded']);
  });
});

describe('an others holding an end', () => {
  const many = (inIso: boolean, outIso: boolean): GraphNode[] => Array.from({ length: 20 }, (_, i) => ({ ...ns(i), isolatedIn: inIso ? 2 : 0, isolatedOut: outIso ? 2 : 0 }));
  const end = (id: string, label: string, kind: GraphNode['kind']): GraphNode => ({ id, label, kind, pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 });
  const edge = (from: string, to: string, side: 'ingress' | 'egress') => ({ kind: 'allow' as const, from, to, side, ports: ['443/TCP'], policies: ['p'], rules: 1 });
  // Find keeps twelve namespaces: the other eight and the end are in "others".
  const show = (g: Graph) =>
    render(<Matrix graph={g} flows={[]} showRules showFlows ask={foldAskOf(new URLSearchParams('top=12&sort=name&mq=team'))} onAsk={() => undefined} onNamespace={() => undefined} />);
  const state = (key: string) => document.querySelector(`[data-cell="${key}"]`)?.getAttribute('data-state');

  it('is not refused toward it from a source whose egress rule reaches the folded CIDR', () => {
    const nodes = many(true, true);
    show({ scope: 'cluster', nodes: [...nodes, end('cidr:203.0.113.0/24', '203.0.113.0/24', 'cidr')], edges: [edge('ns:team-000', 'cidr:203.0.113.0/24', 'egress')], folded: 0, edgesTotal: 1 });
    expect(state('ns:team-000>others')).toBe('folded');
  });

  it('is not refused from it toward a namespace that lets world in, world folded with egress-isolated namespaces', () => {
    const nodes = many(true, true);
    show({ scope: 'cluster', nodes: [...nodes, end('entity:world', 'world', 'entity')], edges: [edge('entity:world', 'ns:team-000', 'ingress')], folded: 0, edgesTotal: 1 });
    expect(state('others>ns:team-000')).toBe('folded');
  });

  it('is still refused when it holds namespaces only, all refusing', () => {
    show({ scope: 'cluster', nodes: many(true, true), edges: [], folded: 0, edgesTotal: 0 });
    expect(state('ns:team-000>others')).toBe('refused');
    expect(document.querySelector('[data-matrix-legend]')?.textContent).toContain('folded');
  });

  it("takes the server's others as holding an end when it says so", () => {
    const nodes = many(true, true).slice(0, 5);
    const serverOthers: GraphNode = { id: 'others', label: '9 others', kind: 'others', pods: 16, isolatedIn: 16, isolatedOut: 16, internal: 0, ends: 1 };
    show({ scope: 'cluster', nodes: [...nodes, serverOthers], edges: [edge('ns:team-000', 'others', 'egress')], folded: 9, edgesTotal: 1 });
    expect(state('ns:team-000>others')).toBe('folded');
  });
});

describe('cells the policy engine would answer otherwise', () => {
  const node = (i: number, inIso: boolean, outIso: boolean, extra: Partial<GraphNode> = {}): GraphNode => ({ ...ns(i), isolatedIn: inIso ? 2 : 0, isolatedOut: outIso ? 2 : 0, ...extra });
  const show = (g: Graph, query = '') =>
    render(<Matrix graph={g} flows={[]} showRules showFlows ask={foldAskOf(new URLSearchParams(query))} onAsk={() => undefined} onNamespace={() => undefined} />);
  const state = (key: string) => document.querySelector(`[data-cell="${key}"]`)?.getAttribute('data-state');
  const rule = (kind: 'allow' | 'deny', from: string, to: string, side: 'ingress' | 'egress', baseline = false) => ({
    kind,
    from,
    to,
    side,
    ports: ['every port'],
    policies: [baseline ? 'default' : 'p'],
    rules: 1,
    ...(baseline ? { baseline: true } : {}),
  });

  it("sets a baseline's rule aside where a NetworkPolicy isolates that side", () => {
    // team-001 is isolated on ingress: the baseline's allow is not consulted.
    show({ scope: 'cluster', nodes: [node(0, false, false), node(1, true, false)], edges: [rule('allow', 'ns:team-000', 'ns:team-001', 'ingress', true)], folded: 0, edgesTotal: 1 });
    expect(state('ns:team-000>ns:team-001')).toBe('refused');
    cleanup();
    // Nor is its deny: the NetworkPolicy's allow decides.
    show({
      scope: 'cluster',
      nodes: [node(0, false, false), node(1, true, false)],
      edges: [rule('deny', 'ns:team-000', 'ns:team-001', 'ingress', true), rule('allow', 'ns:team-000', 'ns:team-001', 'ingress')],
      folded: 0,
      edgesTotal: 2,
    });
    expect(state('ns:team-000>ns:team-001')).toBe('allowed');
    cleanup();
    // Where no NetworkPolicy isolates the side, the baseline speaks.
    show({ scope: 'cluster', nodes: [node(0, false, false), node(1, false, false)], edges: [rule('deny', 'ns:team-000', 'ns:team-001', 'ingress', true)], folded: 0, edgesTotal: 1 });
    expect(state('ns:team-000>ns:team-001')).toBe('denied');
  });

  it("draws a namespace's own cell as the engine answered it, whatever the rules counted", () => {
    // The engine's word wins over any count: an admin allow above a deny, a
    // pass above a deny, a rule it cannot evaluate.
    for (const diagonal of ['inside', 'open', 'mixed', 'denied', 'refused', 'refusedOut', 'unevaluated', 'partial'] as const) {
      show({ scope: 'cluster', nodes: [node(0, true, true, { internal: 2, diagonal })], edges: [], folded: 0, edgesTotal: 0 });
      expect(state('ns:team-000>ns:team-000'), diagonal).toBe(diagonal);
      cleanup();
    }
    // Even beside a deny drawn on the cell by a rule open to everyone.
    show({
      scope: 'cluster',
      nodes: [node(0, false, false, { internal: 1, diagonal: 'inside' }), { id: 'anyone', label: 'Anyone', kind: 'anyone', pods: 0, isolatedIn: 0, isolatedOut: 0, internal: 0 }],
      edges: [rule('deny', 'anyone', 'ns:team-000', 'ingress')],
      folded: 0,
      edgesTotal: 1,
    });
    expect(state('ns:team-000>ns:team-000')).toBe('inside');
  });

  it('reads a diagonal it does not know as no answer, and names mixed and partial in the legend only when drawn', () => {
    show({ scope: 'cluster', nodes: [node(0, true, true, { internal: 2, diagonal: 'bogus' as 'inside' })], edges: [], folded: 0, edgesTotal: 0 });
    expect(state('ns:team-000>ns:team-000')).toBe('refused');
    const legend = (): string => document.querySelector('[data-matrix-legend]')?.textContent ?? '';
    expect(legend()).not.toContain('mixed');
    expect(legend()).not.toContain('partial');
    cleanup();
    show({ scope: 'cluster', nodes: [node(0, false, false, { diagonal: 'mixed' }), node(1, false, false, { diagonal: 'partial' })], edges: [], folded: 0, edgesTotal: 0 });
    expect(state('ns:team-000>ns:team-000')).toBe('mixed');
    expect(state('ns:team-001>ns:team-001')).toBe('partial');
    expect(document.querySelector('[data-cell="ns:team-000>ns:team-000"]')?.getAttribute('title')).toBe('team-000 → team-000: mixed: some flows between its pods pass, others are refused');
    expect(legend()).toContain('mixed');
    expect(legend()).toContain('partial');
  });

  it('never takes a namespace with rules between its own pods as inside when the server sent no answer for it', () => {
    show({ scope: 'cluster', nodes: [node(0, true, false, { internal: 1 })], edges: [], folded: 0, edgesTotal: 0 });
    expect(state('ns:team-000>ns:team-000')).toBe('refused');
  });

  it('reads one deny among the namespaces in others as partly denied, not denied', () => {
    const nodes = Array.from({ length: 20 }, (_, i) => node(i, false, false));
    show({ scope: 'cluster', nodes, edges: [rule('deny', 'ns:team-000', 'ns:team-015', 'ingress')], folded: 0, edgesTotal: 1 }, 'top=12&sort=name');
    expect(state('ns:team-000>others')).toBe('partDenied');
    expect(document.querySelector('[data-matrix-legend]')?.textContent).toContain('partly denied');
  });

  it("never takes the server's others as isolated when it did not count its ends", () => {
    const nodes = Array.from({ length: 5 }, (_, i) => node(i, true, false));
    const serverOthers: GraphNode = { id: 'others', label: '9 others', kind: 'others', pods: 16, isolatedIn: 16, isolatedOut: 16, internal: 0 };
    show({ scope: 'cluster', nodes: [...nodes, serverOthers], edges: [], folded: 9, edgesTotal: 0 });
    expect(state('ns:team-000>others')).toBe('folded');
    cleanup();
    show({ scope: 'cluster', nodes: [...nodes, { ...serverOthers, ends: 0 }], edges: [], folded: 9, edgesTotal: 0 });
    expect(state('ns:team-000>others')).toBe('refused');
  });
});
