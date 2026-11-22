import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Suspense } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import ActivityView from './ActivityView';
import { events } from './extension';
import { OBJECT_KINDS, type Answer, type Change, type Section } from './data';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const READ: Section = { state: 'read', complete: true };
const NOW = Date.parse('2026-11-15T12:00:00Z');
const at = (minutesAgo: number): string => new Date(NOW - minutesAgo * 60_000).toISOString();
const object = { cluster: 'prod', kind: 'deployments', namespace: 'shop', name: 'api' };
const ROLLOUT: Change = { id: 'rollout/shop/api/7', kind: 'rollout', objectKind: 'Deployment', namespace: 'shop', name: 'api', time: at(3), revision: 7, referenced: true };
const ROUTE = '/x/prod/events/object/deployments/shop/api?window=1h';

const answer: Answer = {
  namespace: 'shop',
  window: '1h',
  since: at(60),
  generatedAt: new Date(NOW).toISOString(),
  object: { kind: 'Deployment', namespace: 'shop', name: 'api' },
  tiles: {
    warnings: { count: 268, state: 'read' },
    crashLoop: { count: 0, state: 'read' },
    oomKilled: { count: 0, state: 'read' },
    failedScheduling: { count: 0, state: 'read' },
    objectsTouched: { count: 1, state: 'read' },
  },
  causes: [
    {
      reason: 'BackOff',
      message: 'Back-off',
      type: 'Warning',
      count: 268,
      objects: 1,
      kinds: ['Pod'],
      firstSeen: at(10),
      lastSeen: at(1),
      examples: [],
      since: ROLLOUT.id,
      groups: [{ kind: 'Pod', namespace: 'shop', name: 'api-7d9f-x1', count: 268, firstSeen: at(10), lastSeen: at(1), message: 'm', since: ROLLOUT.id }],
    },
  ],
  changes: [ROLLOUT],
  pods: [],
  sources: { events: READ, replicasets: READ, pods: READ, configmaps: READ, nodes: READ },
  truncated: false,
  complete: true,
};

function stub(status: number, body: unknown) {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? `${input.pathname}${input.search}` : input.url;
    const ok = url.includes('window=1h') && status === 200;
    return Promise.resolve(
      new Response(ok || status !== 200 ? JSON.stringify(body) : `no stub for ${url}`, {
        status: ok || status !== 200 ? status : 599,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(): void {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <Suspense>
          <ActivityView {...object} />
        </Suspense>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('the extension offers its view as the Activity of an object', () => {
  it('on the kinds it names, with no tab and no shortcut of its own', () => {
    expect(events.activity?.kinds).toEqual(OBJECT_KINDS);
    expect(events.tabs).toBeUndefined();
    expect(events.shortcuts).toBeUndefined();
  });
});

describe('the Activity view', () => {
  it('draws the timeline with its change and the aggregated cause with its count, asked from the object route', async () => {
    const fetch = stub(200, answer);
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    expect(screen.getByRole('button', { name: /BackOff/ }).textContent).toContain('× 268');
    expect(document.querySelectorAll('[data-bar]')).toHaveLength(1);
    expect(document.querySelector(`[data-change="${ROLLOUT.id}"]`)).not.toBeNull();
    // Only the window is a control: no tiles, no filters.
    expect(document.querySelector('[data-tile]')).toBeNull();
    expect(document.querySelector('select')).toBeNull();
    expect(fetch.mock.calls.map(([u]) => String(u))).toEqual([ROUTE]);
    fireEvent.click(screen.getByRole('button', { name: '15 min' }));
    await waitFor(() => expect(fetch.mock.calls.some(([u]) => String(u).includes('window=15m'))).toBe(true));
  });

  it('says a source it could not read, and nothing about the ones it read', async () => {
    stub(200, { ...answer, sources: { ...answer.sources, events: { state: 'denied', complete: false, note: 'no' } } });
    mount();
    await screen.findByRole('button', { name: /BackOff/ });
    expect(document.querySelector('[data-source="Events"]')?.textContent).toBe('Events: refused');
    expect(document.querySelector('[data-source="Pods"]')).toBeNull();
  });

  it('says it failed when the read fails', async () => {
    stub(500, { code: 'internal', message: 'the events could not be read' });
    mount();
    await waitFor(() => expect(document.querySelector('[data-activity-view]')?.textContent).toMatch(/could not be read|Retry/i));
    expect(document.querySelector('[data-bar]')).toBeNull();
  });
});
