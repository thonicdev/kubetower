import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { REFRESH_MS, clusterQuery, overviewQuery, type Overview, type Summary } from './data';
import { PAGE_SIZE } from './CnpgPage';
import { cnpg } from './index';
import { roleCells } from './extension';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/**
 * The page mounted the way the console mounts it, with a fetch stub standing
 * in for `extras/cnpg`. Every body is the shape the Go route writes.
 */
type Replies = Record<string, { status?: number; body: unknown }>;

function stub(replies: Replies): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const reply = replies[url];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }));
    }),
  );
  return asked;
}

function mount(path = 'x/cnpg') {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: cnpg.routes }], { initialEntries: [`/c/kind/${path}`] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchInterval: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const OVERVIEW = '/x/kind/cnpg/';
const NOW = '2026-11-11T12:00:00Z';

function summary(name: string, over: Partial<Summary> = {}): Summary {
  return {
    namespace: 'db',
    name,
    phase: 'Cluster in healthy state',
    health: 'healthy',
    expected: 3,
    ready: 3,
    primary: `${name}-1`,
    timeline: 1,
    instances: [
      { name: `${name}-1`, role: 'primary', state: 'healthy', timeline: 1 },
      { name: `${name}-2`, role: 'replica', state: 'healthy', timeline: 1 },
      { name: `${name}-3`, role: 'replica', state: 'healthy', timeline: 1 },
    ],
    archive: { state: 'ok' },
    backup: { state: 'ok', expected: true, late: false, lastSuccess: '2026-11-11T10:00:00Z', scheduled: 1, recent: [], total: 1 },
    certs: { state: 'ok', earliest: '2027-01-01T00:00:00Z', list: [] },
    poolers: 0,
    ...over,
  };
}

function overview(clusters: Summary[], over: Partial<Overview> = {}): Overview {
  return {
    now: NOW,
    complete: true,
    counts: {
      clusters: clusters.length,
      healthy: clusters.filter((c) => c.health === 'healthy').length,
      degraded: clusters.filter((c) => c.health === 'degraded' || c.health === 'failing').length,
      switchover: clusters.filter((c) => c.health === 'switchover').length,
      archiveFailing: clusters.filter((c) => c.archive.state === 'failing').length,
      backupLate: clusters.filter((c) => c.backup.late).length,
      notBackedUp: 0,
      certsSoon: 0,
    },
    clusters,
    sources: [],
    staleAfterHours: 24,
    certWarnDays: 7,
    ...over,
  };
}

describe('the Postgres (CNPG) page', () => {
  it('draws not installed, refused, failed and empty four different ways', async () => {
    stub({ [OVERVIEW]: { status: 404, body: { code: 'kind_unknown', message: 'CloudNativePG is not installed on this cluster: …' } } });
    mount();
    expect((await screen.findByText('CloudNativePG is not installed.')).closest('[data-state]')?.getAttribute('data-state')).toBe('absent');
    cleanup();

    stub({ [OVERVIEW]: { status: 403, body: { code: 'list_forbidden', message: 'CloudNativePG is installed on this cluster, and you may not list its Clusters.' } } });
    mount();
    const refused = await screen.findByText(/you may not list its Clusters/);
    expect(refused.closest('[data-state]')?.getAttribute('data-state')).toBe('refused');
    expect(screen.getByText(/apiGroups: \[postgresql.cnpg.io\]/)).toBeTruthy();
    cleanup();

    stub({ [OVERVIEW]: { status: 502, body: { code: 'list_failed', message: "CloudNativePG's Clusters could not be listed." } } });
    mount();
    await screen.findByText(/could not be listed/);
    expect(document.querySelector('[data-state]')).toBeNull();
    cleanup();

    stub({ [OVERVIEW]: { body: overview([]) } });
    mount();
    await screen.findByText(/^No Postgres cluster/);
    expect(document.querySelector('[data-cluster-card]')).toBeNull();
  });

  it('reads a refused kind resolution as refused, the API server message in the tooltip', async () => {
    stub({
      [OVERVIEW]: {
        status: 403,
        body: {
          code: 'list_forbidden',
          message: 'You may not read the custom resource definitions, so whether CloudNativePG is installed is unknown.',
          detail: 'customresourcedefinitions is forbidden: User "x" cannot list',
        },
      },
    });
    mount();
    const line = await screen.findByText(/You may not read the custom resource definitions/);
    expect(line.closest('[data-state]')?.getAttribute('data-state')).toBe('refused');
    expect(line.getAttribute('title')).toContain('is forbidden');
    expect(screen.queryByText(/apiGroups: \[postgresql.cnpg.io\]/)).toBeNull();
  });

  it('filters the cards from a tile, and back', async () => {
    stub({
      [OVERVIEW]: {
        body: overview([
          summary('sick', { health: 'degraded', ready: 2 }),
          summary('broken-archive', { archive: { state: 'failing', message: 'exit status 2' } }),
          summary('fine'),
        ]),
      },
    });
    const router = mount();
    await screen.findByText('fine');
    expect(document.querySelectorAll('[data-cluster-card]')).toHaveLength(3);

    fireEvent.click(screen.getByRole('button', { name: /Degraded/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-cluster-card]')).toHaveLength(1));
    expect(router.state.location.search).toBe('?show=degraded');
    expect(document.querySelector('[data-cluster-card="db/sick"]')).not.toBeNull();

    fireEvent.click(screen.getByRole('button', { name: /Archive failing/ }));
    await waitFor(() => expect(document.querySelector('[data-cluster-card="db/broken-archive"]')).not.toBeNull());
    expect(document.querySelectorAll('[data-cluster-card]')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: /Archive failing/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-cluster-card]')).toHaveLength(3));
  });

  it(`draws at most ${String(PAGE_SIZE)} cards of a thousand, then more on request`, async () => {
    const many = Array.from({ length: 1000 }, (_, i) => summary(`pg-${String(i).padStart(4, '0')}`));
    stub({ [OVERVIEW]: { body: overview(many) } });
    mount();
    await screen.findByText('pg-0000');
    expect(document.querySelectorAll('[data-cluster-card]')).toHaveLength(PAGE_SIZE);
    expect(document.querySelectorAll('[data-topology="small"]')).toHaveLength(PAGE_SIZE);
    // A card link carries the shared focus ring, drawn on the stretched box.
    expect(document.querySelector('[data-cluster-card] [data-tile-open]')?.getAttribute('class')).toContain('focus-visible:after:outline-solid');
    fireEvent.click(screen.getByRole('button', { name: /Show 120 more of 880/ }));
    await waitFor(() => expect(document.querySelectorAll('[data-cluster-card]')).toHaveLength(2 * PAGE_SIZE));
    // A thousand cards' worth of data on a loaded runner: the bound is what
    // is asserted, not the speed.
  }, 20_000);

  it('marks the primary, a failed replica and one catching up', async () => {
    stub({
      [OVERVIEW]: {
        body: overview([
          summary('pg', {
            health: 'degraded',
            instances: [
              { name: 'pg-1', role: 'primary', state: 'healthy' },
              { name: 'pg-2', role: 'replica', state: 'failed' },
              { name: 'pg-3', role: 'replica', state: 'replicating' },
            ],
          }),
        ]),
      },
    });
    mount();
    await screen.findByText('pg');
    const topology = screen.getByRole('img', { name: /Topology: primary; replica, failed; replica, catching up/ });
    expect(topology.querySelector('[data-instance="pg-1"]')?.getAttribute('data-role')).toBe('primary');
    // Every drawn disc is an instance: no empty slot is drawn.
    expect(topology.querySelectorAll('[data-instance]')).toHaveLength(3);
    expect(topology.querySelector('[data-edge="pg-2"]')?.getAttribute('class')).toContain('stroke-bad');
    expect(topology.querySelector('[data-edge="pg-3"]')?.getAttribute('stroke-dasharray')).toBe('4 3');
  });

  it('draws a lone primary at the size of any other node, with no row below it', async () => {
    stub({
      [OVERVIEW]: {
        body: overview([
          summary('solo', { instances: [{ name: 'solo-1', role: 'primary', state: 'healthy' }] }),
          summary('pair', {
            instances: [
              { name: 'pair-1', role: 'primary', state: 'healthy' },
              { name: 'pair-2', role: 'replica', state: 'healthy' },
            ],
          }),
        ]),
      },
    });
    mount();
    await screen.findByText('solo');
    const [solo, pair] = [...document.querySelectorAll('[data-topology="small"]')] as SVGSVGElement[];
    // Drawn at its own size: a node's radius is the same number of pixels in both.
    expect(solo?.getAttribute('height')).toBe(solo?.getAttribute('viewBox')?.split(' ')[3]);
    expect(pair?.getAttribute('height')).toBe(pair?.getAttribute('viewBox')?.split(' ')[3]);
    expect(Number(solo?.getAttribute('height'))).toBeLessThan(Number(pair?.getAttribute('height')));
  });

  it("says a list cut short and a refused secondary read", async () => {
    stub({
      [OVERVIEW]: {
        body: overview([summary('pg')], {
          complete: false,
          note: 'The list was cut at its limit.',
          sources: [{ kind: 'backups.postgresql.cnpg.io', state: 'refused', note: 'You may not list backups.postgresql.cnpg.io.', complete: false }],
        }),
      },
    });
    mount();
    await screen.findByText('The list was cut at its limit.');
    expect(screen.getByText(/Backups: You may not list/)).toBeTruthy();
  });

  it('reads again every 30 s, only while the page is shown', () => {
    for (const q of [overviewQuery('kind'), clusterQuery('kind', 'db', 'pg')]) {
      expect(q.refetchInterval).toBe(REFRESH_MS);
      expect(q.refetchIntervalInBackground).toBe(false);
    }
  });
});

describe('a cluster page', () => {
  it('draws the topology, the members, the timeline and the events', async () => {
    const s = summary('pg');
    stub({
      '/x/kind/cnpg/clusters/db/pg': {
        body: {
          now: NOW,
          summary: s,
          members: s.instances.map((i) => ({ ...i, pod: { phase: 'Running', ready: true, restarts: 0, node: 'w1' } })),
          conditions: [{ type: 'Ready', status: 'True' }],
          events: [{ type: 'Normal', reason: 'FailoverTarget', message: 'Failing over from pg-2 to pg-1', at: '2026-11-11T11:40:00Z' }],
          backups: [{ name: 'b1', phase: 'completed', stoppedAt: '2026-11-11T09:00:00Z' }, { name: 'b0', phase: 'failed', stoppedAt: '2026-11-08T09:00:00Z' }],
          scheduled: [],
          poolers: [],
          declared: [],
          diskNote: 'Disks not measured: you may not list nodes, which the kubelets\' figures need.',
          sources: [],
          commands: [],
        },
      },
    });
    mount('x/cnpg/clusters/db/pg');
    await screen.findByText('Primary and replicas');
    expect(document.querySelector('[data-topology="large"]')).not.toBeNull();
    expect(document.querySelectorAll('[data-member]')).toHaveLength(3);
    expect(document.querySelectorAll('[data-mark="completed"]')).toHaveLength(1);
    expect(document.querySelectorAll('[data-mark="failed"]')).toHaveLength(1);
    expect(screen.getByText('Failing over from pg-2 to pg-1')).toBeTruthy();
    expect(screen.getByText(/Disks not measured/)).toBeTruthy();
  });
});

describe("a cluster page's replication lag", () => {
  const LAG = '/x/kind/cnpg/clusters/db/pg/lag';
  const body = () => {
    const s = summary('pg');
    return { now: NOW, summary: s, members: [], conditions: [], events: [], backups: [], scheduled: [], poolers: [], declared: [], sources: [] };
  };
  const open = async (lag: { status?: number; body: unknown }) => {
    const asked = stub({ '/x/kind/cnpg/clusters/db/pg': { body: { ...body(), commands: [] } }, [LAG]: lag });
    mount('x/cnpg/clusters/db/pg');
    const button = await waitFor(() => {
      const found = document.querySelector<HTMLButtonElement>('[data-lag-read]');
      expect(found).not.toBeNull();
      return found!;
    });
    return { asked, button };
  };

  it('reads nothing until the button is pressed, then draws each lag on its edge', async () => {
    const { asked, button } = await open({ body: { primary: 'pg-1', replicas: [{ name: 'pg-2', state: 'streaming', sync: 'async', write: 0.001, flush: 0.004, replay: 0.0124 }, { name: 'pg-3', state: 'streaming', sync: 'async', replay: 12.6 }] } });
    expect(asked).not.toContain(LAG);
    fireEvent.click(button);
    await waitFor(() => expect(document.querySelector('[data-lag-replica="pg-2"]')).not.toBeNull());
    expect(asked.filter((u) => u === LAG)).toHaveLength(1);
    expect(document.querySelector('[data-edge-lag="pg-2"]')?.textContent).toMatch(/12 ms$/);
    expect(document.querySelector('[data-edge-lag="pg-3"]')?.textContent).toMatch(/12.6 s$/);
    // A lag Postgres did not report is a dash, not 0.
    expect(document.querySelector('[data-lag-replica="pg-3"]')?.textContent).toContain('—');
    expect(document.querySelector('[data-lag-replica="pg-3"]')?.textContent).not.toContain('0 ms');
  });

  it.each([
    [403, 'exec_forbidden', 'Refused: no exec on the primary'],
    [422, 'no_psql', 'No psql in the image'],
    [504, 'exec_timed_out', 'Timed out'],
    [502, 'lag_truncated', 'Answer cut'],
  ])('names a %i %s as its own answer, never as 0 s', async (status, code, label) => {
    const { button } = await open({ status, body: { code, message: 'said' } });
    fireEvent.click(button);
    await waitFor(() => expect(document.querySelector(`[data-lag-problem="${code}"]`)).not.toBeNull());
    expect(screen.getByText(label)).toBeTruthy();
    expect(document.querySelector('[data-lag-replica]')).toBeNull();
    expect(document.querySelector('[data-edge-lag]')).toBeNull();
  });

  it('says an empty answer is nobody streaming, not a failure', async () => {
    const { button } = await open({ body: { primary: 'pg-1', replicas: [] } });
    fireEvent.click(button);
    await screen.findByText('no standby streaming');
    expect(document.querySelector('[data-lag-problem]')).toBeNull();
  });
});

describe("a cluster page's commands", () => {
  it('draws the lines to copy and a copy sends no request', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const s = summary('pg');
    const commands = [
      { act: 'backup', line: 'kubectl cnpg backup pg -n db' },
      { act: 'promote', instance: "x'y", line: `kubectl cnpg promote pg 'x'\\''y' -n db` },
    ];
    const asked = stub({
      '/x/kind/cnpg/clusters/db/pg': {
        body: { now: NOW, summary: s, members: [], conditions: [], events: [], backups: [], scheduled: [], poolers: [], declared: [], sources: [], commands },
      },
    });
    mount('x/cnpg/clusters/db/pg');
    await screen.findByText('Back up now');
    expect(document.querySelectorAll('[data-command]')).toHaveLength(2);
    const before = asked.length;
    fireEvent.click(document.querySelectorAll('[data-command] button')[1]!);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(commands[1]!.line));
    expect(asked.length).toBe(before);
    Reflect.deleteProperty(navigator, 'clipboard');
  });
});

describe("a cluster page's way back", () => {
  const detail = () => {
    const s = summary('pg');
    return {
      now: NOW, summary: s, members: [], conditions: [], events: [], backups: [], scheduled: [], poolers: [], declared: [], sources: [], commands: [],
    };
  };

  it('links to the list when the page was the first opened', async () => {
    stub({ '/x/kind/cnpg/clusters/db/pg': { body: detail() } });
    mount('x/cnpg/clusters/db/pg');
    const back = await waitFor(() => {
      const found = document.querySelector('[data-back]');
      expect(found).not.toBeNull();
      return found;
    });
    expect(back?.getAttribute('href')).toBe('/c/kind/x/cnpg');
  });

  it('steps back to the list, its filter kept, when the page was opened from it', async () => {
    stub({ [OVERVIEW]: { body: overview([summary('pg')]) }, '/x/kind/cnpg/clusters/db/pg': { body: detail() } });
    const router = mount('x/cnpg?filter=degraded');
    await waitFor(() => expect(router.state.location.pathname).toBe('/c/kind/x/cnpg'));
    await router.navigate('/c/kind/x/cnpg/clusters/db/pg');
    const back = await screen.findByRole('button', { name: 'Back' });
    fireEvent.click(back);
    await waitFor(() => expect(router.state.location.pathname).toBe('/c/kind/x/cnpg'));
    expect(router.state.location.search).toBe('?filter=degraded');
  });
});

describe('the Pods column', () => {
  it('draws each instance pod with its role, and nothing on another kind', async () => {
    const asked = stub({
      '/x/kind/cnpg/pods': { body: { pods: { 'db/pg-1': { role: 'primary', cluster: 'pg' }, 'db/pg-2': { role: 'replica', cluster: 'pg' } }, complete: true } },
    });
    const signal = new AbortController().signal;
    const cells = await roleCells({ cluster: 'kind', kind: 'pods' }, signal);
    expect(cells['db/pg-1']?.text).toBe('primary');
    expect(cells['db/pg-1']?.tone).toBe('ok');
    expect(cells['db/pg-2']?.tone).toBeUndefined();
    expect(cells['db/pg-2']?.href).toBe('/c/kind/x/cnpg/clusters/db/pg');
    await expect(roleCells({ cluster: 'kind', kind: 'services' }, signal)).resolves.toEqual({});
    expect(asked).toEqual(['/x/kind/cnpg/pods']);
  });
});
