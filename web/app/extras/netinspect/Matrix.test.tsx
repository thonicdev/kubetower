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
  // Each lets its own pods in too: the server counts that rule on the node (`internal`).
  const nodes: GraphNode[] = Array.from({ length: kept }, (_, i) => ({ ...ns(i), isolatedIn: 2, internal: 1 }));
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
