import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import type { Row, WorkloadView } from './data';
import { WorkloadBody } from './WorkloadTab';

const row = (name: string, main: Row['main'], text: string, since: string): Row => ({
  namespace: 'shop', name, created: since, stage: 'unschedulable', main, categories: [main], message: `0/3 nodes are available: ${text}`,
  verdict: { total: 3, reasons: [{ text, nodes: 3, category: main }] }, request: { cpu: 1000 },
});

const view = (over: Partial<WorkloadView>): WorkloadView => ({
  cluster: 'prod', kind: 'Deployment', namespace: 'shop', name: 'api', read: '2026-11-11T14:30:00Z',
  pods: { state: 'read', complete: true }, capped: false, pending: 0, rows: [], failuresRead: { state: 'read', complete: true }, ...over,
});

function draw(v: WorkloadView) {
  return render(
    <MemoryRouter>
      <WorkloadBody cluster="prod" view={v} />
    </MemoryRouter>,
  );
}

describe('the workload tab', () => {
  it('lists the pending pods, each with its reason and a link to its own view', () => {
    draw(view({ pending: 2, rows: [row('api-1', 'resources', 'Insufficient cpu', '2026-11-11T14:10:00Z'), row('api-2', 'taints', '3 untolerated taint', '2026-11-11T14:20:00Z')] }));
    const first = screen.getByText('api-1').closest('li');
    expect(first?.getAttribute('data-pending-pod')).toBe('api-1');
    expect(screen.getByText('api-1').getAttribute('href')).toBe('/c/prod/x/pending/shop/api-1');
    expect(first?.textContent).toContain('Resources');
    // The name is cut to a fixed column, so it carries its whole text.
    expect(screen.getByText('api-1').getAttribute('title')).toBe('api-1');
    expect(first?.textContent).toContain('20m');
    expect(screen.getByText('api-2').closest('li')?.textContent).toContain('Taints');
  });

  it('shows the refusal of a controller that could not create a pod, with its quota', () => {
    draw(
      view({
        failure: {
          namespace: 'shop', kind: 'ReplicaSet', name: 'api-1', category: 'quota', message: 'exceeded quota: small', count: 4, last: '2026-11-11T14:29:00Z',
          quota: { name: 'small', resource: 'requests.cpu', requested: '3', used: 2000, hard: 2000, room: false },
        },
      }),
    );
    expect(screen.getByText('Cannot create pods')).toBeTruthy();
    expect(document.querySelector('[data-quota="small"]')).not.toBeNull();
    expect(document.querySelector('[data-nothing-pending]')).toBeNull();
  });

  it('says nothing is pending only when the pods were read; a refused read is not that answer', () => {
    const { unmount } = draw(view({}));
    expect(document.querySelector('[data-nothing-pending]')).not.toBeNull();
    unmount();
    draw(view({ pods: { state: 'denied', complete: false, note: 'no' } }));
    expect(document.querySelector('[data-nothing-pending]')).toBeNull();
    expect(document.querySelector('[data-section="Pods"][data-state="denied"]')).not.toBeNull();
  });
});
