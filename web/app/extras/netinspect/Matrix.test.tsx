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

  it('shows every namespace when others is pressed', () => {
    render(<Harness />);
    fireEvent.click(document.querySelector('[data-heading-others]') as Element);
    expect(rows()).toHaveLength(120);
    expect(document.querySelector('[data-top="0"]')?.getAttribute('aria-pressed')).toBe('true');
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
