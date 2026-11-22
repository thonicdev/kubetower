import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { KeyboardStack } from '../../../ui/src/keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../../ui/src/keyboard/stack';

import PodTab from './PodTab';

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const READ = { state: 'read', complete: true };
const NOW = '2026-03-11T09:40:00Z';
const gauge = (allocatable: number, requested: number) => ({ allocatable, requested });

const VIEW = {
  cluster: 'prod',
  pending: true,
  phase: 'Pending',
  read: NOW,
  row: {
    namespace: 'shop',
    name: 'api-1',
    created: '2026-03-11T08:58:00Z',
    since: '2026-03-11T08:58:00Z',
    stage: 'unschedulable',
    main: 'resources',
    categories: ['resources', 'taints'],
    message: '0/12 nodes are available',
    verdict: {
      total: 12,
      reasons: [
        { text: 'Insufficient cpu', nodes: 10, category: 'resources' },
        { text: 'node(s) had untolerated taint dedicated=gpu', nodes: 2, category: 'taints' },
        { text: 'Insufficient memory', nodes: 1, category: 'resources' },
        { text: 'a fourth reason that must not be drawn', nodes: 1, category: 'other' },
      ],
    },
    request: { cpu: 2000 },
  },
  columns: [{ key: 'resource:cpu', label: 'CPU', computed: true, scheduler: 0 }],
  fits: [
    { node: 'node-pool-b4', failing: 1, excluded: 0, deficit: 0, cells: [{ key: 'resource:cpu', ok: false }] },
    { node: 'node-pool-a2', failing: 1, excluded: 0, deficit: 0, cells: [{ key: 'resource:cpu', ok: false }] },
  ],
  nodesTotal: 12,
  nodes: READ,
  placed: READ,
  bars: [{ name: 'node-pool-b4', ready: true, cordoned: false, taints: 0, cpu: gauge(7800, 6400), memory: gauge(1, 0), pods: gauge(110, 40) }],
  wouldTake: ['Free some CPU on node-pool-b4'],
  claims: [],
  claimsRead: READ,
  events: [{ type: 'Warning', reason: 'FailedScheduling', message: 'nope', first: '2026-03-11T08:58:00Z', last: NOW, count: 3, source: 'default-scheduler' }],
  eventsRead: READ,
  eventsCapped: false,
};

function mount(): void {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify(VIEW), { status: 200, headers: { 'Content-Type': 'application/json' } }))));
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <KeyboardStack>
        <MemoryRouter>
          <PodTab cluster="prod" kind="pods" namespace="shop" name="api-1" />
        </MemoryRouter>
      </KeyboardStack>
    </QueryClientProvider>,
  );
}

describe("a pending pod's compact tile", () => {
  it('holds the state, at most three reasons and the closest node with what it lacks', async () => {
    mount();
    const tile = await screen.findByRole('region', { name: 'Why it waits' });
    expect(tile.textContent).toContain('Waiting for a node · for 42m');
    expect(tile.querySelectorAll('[data-reasons] li')).toHaveLength(3);
    expect(tile.textContent).toContain('Insufficient cpu');
    expect(tile.textContent).toContain('10/12');
    expect(tile.textContent).not.toContain('a fourth reason');
    const closest = tile.querySelector('[data-closest]');
    expect(closest?.textContent).toBe('node-pool-b4CPU 2 · 1.4 free');
  });

  it('draws no Node fit, no Room and no Events, and no sentence of its own', async () => {
    mount();
    await screen.findByRole('region', { name: 'Why it waits' });
    expect(screen.queryByText('Node fit')).toBeNull();
    expect(screen.queryByText('Room on the closest nodes')).toBeNull();
    expect(screen.queryByText('Events')).toBeNull();
    expect(screen.queryByText('What it would take')).toBeNull();
    expect(document.querySelector('[data-matrix], [data-node-bars], [data-timeline]')).toBeNull();
  });

  it('opens the full view in the window from Expand, and Escape and the backdrop close it', async () => {
    mount();
    const expand = await screen.findByRole('button', { name: 'Expand' });
    expect(expand.getAttribute('title')).toBe('Expand');
    fireEvent.click(expand);
    const dialog = await screen.findByRole('dialog', { name: 'Pod Debugging · api-1' });
    expect(await within(dialog).findByText('Node fit')).not.toBeNull();
    expect(within(dialog).getByText('Room on the closest nodes')).not.toBeNull();
    expect(within(dialog).getByText('Events')).not.toBeNull();
    expect(within(dialog).getByText('What it would take')).not.toBeNull();

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Expand' }));
    const veil = (await screen.findByRole('dialog')).parentElement as HTMLElement;
    fireEvent.mouseDown(veil);
    fireEvent.click(veil);
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
