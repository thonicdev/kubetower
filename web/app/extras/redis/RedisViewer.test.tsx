import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { redis } from './index';

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * The inventory, mounted the way the console mounts it, with a fetch stub
 * standing in for `extras/redis`. Every body is the shape the Go route writes,
 * and the stub records every address asked for - which is what the claim
 * "the inventory dials nothing until asked" is checked on.
 */

type Replies = Record<string, { status?: number; body: unknown; contentType?: string }>;

function stub(replies: Replies): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const reply = replies[url];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      const body = typeof reply.body === 'string' ? reply.body : JSON.stringify(reply.body);
      return Promise.resolve(
        new Response(body, { status: reply.status ?? 200, headers: { 'Content-Type': reply.contentType ?? 'application/json' } }),
      );
    }),
  );
  return asked;
}

function mount(path: string) {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: redis.routes }], {
    initialEntries: [`/c/docker-desktop/${path}`],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const INVENTORY = '/x/docker-desktop/redis/';

const EXTERNAL_DOOR = { namespace: 'apps', service: 'sessions', host: 'demo.abc123.cache.amazonaws.com', port: 6379, why: 'it is on 6379' };

const door = (namespace: string, service: string, why = 'the port is named redis') => ({
  namespace,
  service,
  port: 6379,
  why,
});

const backend = (key: string, namespace: string, services: string[], over: Record<string, unknown> = {}) => ({
  key,
  pods: [`${key}-0`],
  behind: `${key}-0`,
  ready: 1,
  reachable: true,
  endpointless: false,
  doors: services.map((s) => door(namespace, s)),
  ...over,
});

/** A deployment of one member: the shape of a Redis nothing else folds with. */
const alone = (member: ReturnType<typeof backend>) => ({
  key: `d:${member.key}`,
  name: member.key,
  evidence: [],
  workloads: [],
  members: [{ workloads: [], ...member }],
});

type Namespace = { name: string; backends?: ReturnType<typeof backend>[]; deployments?: unknown[] };

/**
 * The inventory the route writes. A namespace given as `backends` is the
 * older shape, before deployments, and is folded here into deployments of one
 * member each, which is what the route answers for Redises nothing groups.
 */
const inventory = (over: Record<string, unknown> = {}) => {
  const body: Record<string, unknown> = {
    scope: '',
    complete: true,
    services: 3,
    members: 2,
    redises: 2,
    namespaces: [
      { name: 'apps', backends: [backend('valkey', 'apps', ['valkey'])] },
      { name: 'cache', backends: [backend('redis', 'cache', ['redis-master', 'redis-headless'])] },
    ],
    ...over,
  };
  body['namespaces'] = (body['namespaces'] as Namespace[]).map((ns) =>
    ns.deployments !== undefined ? ns : { name: ns.name, deployments: (ns.backends ?? []).map(alone) },
  );
  return body;
};

const answered = (engine: string, version: string, mode = 'standalone', keys = 4) => ({
  instance: door('x', 'y'),
  pod: 'p-0',
  db: 0,
  server: { engine, version, mode, databases: 16 },
  overview: { keys, usedMemory: '1.0 MiB', keyspace: [] },
});

const PROBE_REDIS = '/x/docker-desktop/redis/instances/cache/redis-master/6379/probe';


const sel = (namespace: string, service: string, role: string, roleFrom: string) => ({
  ...door(namespace, service, 'the port is named tcp-redis'),
  role,
  roleFrom,
});
const replication = {
  key: 'd:cache-redis',
  name: 'cache-redis',
  evidence: ['workload', 'pods', 'release'],
  release: 'cache',
  chart: 'redis-19.6.4',
  workloads: ['StatefulSet/cache-redis-master', 'StatefulSet/cache-redis-replicas'],
  members: [
    {
      ...backend('master', 'apps', []),
      doors: [sel('apps', 'cache-redis-master', 'primary', 'labels')],
      role: 'primary',
      roleFrom: 'labels',
      workloads: ['StatefulSet/cache-redis-master'],
    },
    {
      ...backend('replicas', 'apps', []),
      pods: ['cache-redis-replicas-0', 'cache-redis-replicas-1'],
      behind: '2 pods: cache-redis-replicas-0, cache-redis-replicas-1',
      ready: 2,
      doors: [sel('apps', 'cache-redis-replicas', 'replica', 'labels')],
      role: 'replica',
      roleFrom: 'labels',
      workloads: ['StatefulSet/cache-redis-replicas'],
    },
    {
      ...backend('headless', 'apps', []),
      doors: [sel('apps', 'cache-redis-headless', 'headless', 'spec')],
      role: 'headless',
      roleFrom: 'spec',
      workloads: [],
    },
    {
      ...backend('sentinels', 'apps', []),
      doors: [{ ...sel('apps', 'cache-redis', 'sentinel', 'port'), port: 26379 }],
      role: 'sentinel',
      roleFrom: 'port',
      sentinel: true,
      workloads: [],
    },
  ],
};
const body = (deployments: unknown[]) =>
  inventory({ services: 3, members: 3, redises: deployments.length, namespaces: [{ name: 'apps', deployments }] });

const tileOf = (key: string) => [...document.querySelectorAll('[data-deployment]')].find((t) => t.getAttribute('data-deployment') === key);
const tiles = () => [...document.querySelectorAll('[data-deployment]')].map((t) => t.getAttribute('data-deployment'));
const filter = (f: string) => document.querySelector(`[data-filter="${f}"]`);
const count = (f: string) => filter(f)?.getAttribute('data-count');
const press = (f: string) => fireEvent.click(filter(f)?.querySelector('button') as HTMLButtonElement);
const pressed = (f: string) => filter(f)?.querySelector('button')?.getAttribute('aria-pressed');

describe('the inventory: one tile per deployment', () => {
  it('opens no connection until a probe is pressed, and then exactly one', async () => {
    const asked = stub({ [INVENTORY]: { body: inventory() }, [PROBE_REDIS]: { body: answered('Redis', '7.2.4') } });
    mount('x/redis');

    await waitFor(() => expect(tiles()).toEqual(['d:valkey', 'd:redis']));
    expect(asked).toEqual([INVENTORY]);

    fireEvent.click(tileOf('d:redis')?.querySelector('[data-probe]') as HTMLButtonElement);
    await waitFor(() => expect(tileOf('d:redis')?.getAttribute('data-status')).toBe('reachable'));
    expect(asked.filter((u) => u.includes('/instances/'))).toEqual([PROBE_REDIS]);
    // The tile now reads what the server said, and offers the address to copy.
    const tile = tileOf('d:redis');
    expect(tile?.getAttribute('data-engine')).toBe('Redis');
    expect(tile?.getAttribute('data-tone')).toBe('ok');
    expect(tile?.querySelector('[data-tile-detail]')?.textContent).toBe('cache · 1 pod · 4 keys');
    expect(tile?.querySelector('[data-probe]')).toBeNull();
    expect(tile?.querySelector('[aria-label="Copy address"], [title="Copy address"]')).toBeTruthy();
  });

  it('opens the instance page from the whole tile, carrying the scope as `scope`', async () => {
    stub({ ['/x/docker-desktop/redis/?ns=cache']: { body: inventory({ scope: 'cache', redises: 1, services: 2, namespaces: [(inventory()['namespaces'] as Namespace[])[1]] }) } });
    mount('x/redis?ns=cache');
    await waitFor(() => expect(tiles()).toEqual(['d:redis']));
    const open = tileOf('d:redis')?.querySelector('[data-tile-open]');
    expect(open?.getAttribute('href')).toBe('/c/docker-desktop/x/redis/cache/redis-master/6379?scope=cache');
    expect(open?.textContent).toBe('redis');
    expect(document.querySelector('[data-counts]')?.textContent).toContain('1 deployment, 2 Services');
  });

  it('groups a primary, its replicas, the headless Service and the Sentinels into one tile', async () => {
    const asked = stub({ [INVENTORY]: { body: body([replication]) } });
    mount('x/redis');
    await waitFor(() => expect(tiles()).toEqual(['d:cache-redis']));
    expect(asked).toEqual([INVENTORY]);

    const tile = tileOf('d:cache-redis');
    expect(tile?.querySelector('[data-tile-open]')?.textContent).toBe('cache-redis');
    // master + replicas + headless + sentinel: one tile, the primary opened.
    expect(tile?.querySelector('[data-tile-open]')?.getAttribute('href')).toBe('/c/docker-desktop/x/redis/apps/cache-redis-master/6379');
    expect(tile?.querySelector('[data-tile-open]')?.getAttribute('class')).toContain('focus-visible:after:outline-solid');
    expect(tile?.querySelector('[data-tile-detail]')?.textContent).toBe('apps · 1 primary · 2 replicas');
    expect(tile?.getAttribute('data-mode')).toBe('sentinel');
    // The engine from the chart its Helm labels name, said as such until probed.
    expect(tile?.getAttribute('data-engine')).toBe('Redis');
    expect(tile?.querySelector('[data-pill]')?.getAttribute('title')).toBe('Redis, from the chart its Helm labels name.');
    expect(tile?.getAttribute('data-status')).toBe('unprobed');
    expect(count('state.unprobed')).toBe('1');
  });

  it('draws one Service in front of the same Redis as one tile, not two', async () => {
    stub({ [INVENTORY]: { body: inventory() } });
    mount('x/redis');
    await waitFor(() => expect(tiles()).toHaveLength(2));
    // redis-master and redis-headless reach one backend: one tile.
    expect(document.querySelectorAll('[data-tile-grid="regular"] > li')).toHaveLength(2);
  });

  it('tells Valkey from Redis: by the server once probed, by the chart before, never by a name', async () => {
    const valkeyChart = { key: 'd:vk', name: 'redis-lookalike', evidence: ['release'], release: 'vk', chart: 'valkey-2.1.0', workloads: [], members: [{ workloads: [], ...backend('vk', 'apps', ['redis-lookalike']) }] };
    const named = { key: 'd:nm', name: 'valkey', evidence: [], workloads: [], members: [{ workloads: [], ...backend('nm', 'apps', ['valkey']) }] };
    stub({
      [INVENTORY]: { body: body([valkeyChart, named]) },
      '/x/docker-desktop/redis/instances/apps/valkey/6379/probe': { body: answered('Valkey', '8.0.1') },
    });
    mount('x/redis');
    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(tileOf('d:vk')?.getAttribute('data-engine')).toBe('Valkey');
    // A Service called valkey is not a Valkey until it says so.
    expect(tileOf('d:nm')?.getAttribute('data-engine')).toBe('unknown');
    expect(count('engine.Valkey')).toBe('1');
    expect(count('engine.unknown')).toBe('1');

    fireEvent.click(tileOf('d:nm')?.querySelector('[data-probe]') as HTMLButtonElement);
    await waitFor(() => expect(tileOf('d:nm')?.getAttribute('data-engine')).toBe('Valkey'));
    expect(count('engine.Valkey')).toBe('2');
    expect(filter('engine.unknown')).toBeNull();
  });

  it('draws none found, refused, could not list, narrowed and the list as different things', async () => {
    stub({ [INVENTORY]: { body: inventory({ services: 0, redises: 0, namespaces: [] }) } });
    mount('x/redis');
    const empty = await screen.findByText('No Redis found');
    expect(empty.closest('[data-state]')?.getAttribute('data-state')).toBe('empty');
    cleanup();

    stub({ [INVENTORY]: { status: 403, body: { code: 'list_refused', kind: 'refusal', message: 'You may not list Services on this cluster.' } } });
    mount('x/redis');
    const refused = await screen.findByText('You may not list Services on this cluster.');
    expect(refused.closest('[data-problem]')?.getAttribute('data-kind')).toBe('refusal');
    expect(screen.getByText('Refused')).toBeTruthy();
    expect(screen.queryByText('No Redis found')).toBeNull();
    expect(document.querySelector('[data-filters]')).toBeNull();
    cleanup();

    stub({
      [INVENTORY]: {
        status: 502,
        body: { code: 'list_failed', kind: 'failure', message: 'The Services could not be read, so whether this cluster runs a Redis is unknown.' },
      },
    });
    mount('x/redis');
    const failed = await screen.findByText(/whether this cluster runs a Redis is unknown/);
    expect(failed.closest('[data-problem]')?.getAttribute('data-kind')).toBe('failure');
    cleanup();

    // A namespace this person may not list is said, and the counts are lower
    // bounds - never a plain number that would read as all there is.
    stub({ [INVENTORY]: { body: inventory({ complete: false, note: 'the Services in payments could not be listed (forbidden)' }) } });
    mount('x/redis');
    const narrowed = await screen.findByText('Incomplete list');
    expect(narrowed.closest('[data-state]')?.getAttribute('data-state')).toBe('narrowed');
    expect(narrowed.closest('[data-state]')?.textContent).toContain('payments could not be listed (forbidden)');
    expect(tiles()).toHaveLength(2);
    expect(filter('state.unprobed')?.textContent).toContain('2+');
    cleanup();

    // An empty body has no sentence of the gate's to show, so the page says its own.
    stub({ [INVENTORY]: { body: '', status: 404, contentType: 'text/plain' } });
    mount('x/redis');
    expect(await screen.findByText(/not enabled for this cluster/)).toBeTruthy();
  });

  it("shows the gate's own sentence, not the Settings switch, when the gate said why", async () => {
    stub({
      [INVENTORY]: {
        status: 404,
        contentType: 'text/plain; charset=utf-8',
        body:
          'Redis viewer is not enabled on this KubeTower: it is turned off by KT_EXTRAS=-redis ' +
          'in the environment, which the console cannot change while it runs. ' +
          'Change KT_EXTRAS where the deployment sets it and restart KubeTower.\n',
      },
    });
    mount('x/redis');
    expect(await screen.findByText(/which the console cannot change while it runs/)).toBeTruthy();
    expect(screen.queryByText(/Turn it on in Settings/)).toBeNull();
  });

  it("draws the gate's unknown cluster as not here, with its sentence and no retry", async () => {
    // What the console's gate writes for a cluster the kubeconfig does not
    // hold, before the route runs (`api.UnknownCluster`).
    const sentence = 'There is no cluster called "docker-desktop" in the kubeconfig this KubeTower reads.';
    stub({
      [INVENTORY]: {
        status: 404,
        contentType: 'application/problem+json',
        body: { type: 'about:blank', title: 'Not found', status: 404, code: 'not_found', detail: sentence },
      },
    });
    mount('x/redis');
    const notice = (await screen.findByText(sentence)).closest('[data-problem]');
    expect(notice?.getAttribute('data-problem')).toBe('unknown_cluster');
    expect(notice?.getAttribute('data-kind')).toBe('absent');
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });

  it('lists an ExternalName Service to a managed Redis apart, opening its Service, with no probe', async () => {
    const asked = stub({
      [INVENTORY]: { body: inventory({ services: 0, members: 0, redises: 0, namespaces: [], external: [{ namespace: 'apps', service: 'sessions', host: 'demo.abc123.cache.amazonaws.com', port: 6379, why: 'it is on 6379' }] }) },
    });
    mount('x/redis');
    await waitFor(() => expect(document.querySelector('[data-external="apps/sessions"]')).not.toBeNull());
    const tile = document.querySelector('[data-external="apps/sessions"]');
    expect(tile?.querySelector('a')?.getAttribute('href')).toBe('/c/docker-desktop/services/apps/sessions?tab=x.redis.backend');
    expect(tile?.querySelector('[data-tile-detail]')?.textContent).toBe('apps · demo.abc123.cache.amazonaws.com:6379');
    expect(tile?.querySelector('[data-probe]')).toBeNull();
    expect(document.querySelector('[data-state="empty"]')).toBeNull();
    expect(document.querySelector('[data-counts]')?.textContent).toContain('1 external');
    expect(asked).toEqual([INVENTORY]);
  });

  it('hides the external tiles and their count while a filter narrows the grid', async () => {
    stub({ [INVENTORY]: { body: inventory({ external: [EXTERNAL_DOOR] }) } });
    mount('x/redis?show=engine.Valkey');
    await waitFor(() => expect(document.querySelector('[data-counts]')?.textContent).toContain('2 deployments'));
    expect(document.querySelector('[data-external]')).toBeNull();
    expect(document.querySelector('[data-counts]')?.textContent).not.toContain('external');
  });

  it('draws a tile with nothing behind it as not serving, with no probe and no link', async () => {
    stub({
      [INVENTORY]: {
        body: inventory({
          namespaces: [
            {
              name: 'cache',
              backends: [
                backend('a', 'cache', ['a']),
                backend('b', 'cache', ['b'], { pods: [], reachable: false, endpointless: true, behind: 'nothing is behind this Service' }),
              ],
            },
          ],
        }),
      },
    });
    mount('x/redis');
    await waitFor(() => expect(tiles()).toEqual(['d:a', 'd:b']));
    const b = tileOf('d:b');
    expect(b?.getAttribute('data-status')).toBe('notserving');
    expect(b?.getAttribute('data-tone')).toBe('off');
    expect(b?.querySelector('[data-probe]')).toBeNull();
    expect(b?.querySelector('a')).toBeNull();
    expect(b?.querySelector('[data-tile-detail]')?.textContent).toBe('cache · no pods');
    expect(count('state.notserving')).toBe('1');
  });
});

describe('the filter tiles', () => {
  it('give every status its count, and each narrows the grid to exactly its tiles', async () => {
    const ns = 'cache';
    const rows = [
      backend('ok', ns, ['ok']),
      backend('down', ns, ['down']),
      backend('locked', ns, ['locked']),
      backend('sharded', ns, ['sharded']),
      backend('empty', ns, ['empty'], { pods: [], reachable: false, endpointless: true, behind: 'nothing is behind this Service' }),
      backend('later', ns, ['later']),
    ];
    const probeOf = (s: string) => `/x/docker-desktop/redis/instances/cache/${s}/6379/probe`;
    stub({
      [INVENTORY]: { body: inventory({ services: 6, redises: 6, namespaces: [{ name: ns, backends: rows }] }) },
      [probeOf('ok')]: { body: answered('Redis', '7.2.4') },
      [probeOf('down')]: { status: 502, body: { code: 'unreachable', kind: 'failure', message: 'The tunnel opened and nothing answered.' } },
      [probeOf('locked')]: { status: 409, body: { code: 'no_credential', kind: 'refusal', message: 'This instance asked for a password.' } },
      [probeOf('sharded')]: { body: answered('Valkey', '8.0.1', 'cluster') },
    });
    const router = mount('x/redis');
    await waitFor(() => expect(tiles()).toHaveLength(6));
    expect(count('state.notserving')).toBe('1');
    expect(count('state.unprobed')).toBe('5');

    for (const key of ['ok', 'down', 'locked', 'sharded']) {
      fireEvent.click(tileOf(`d:${key}`)?.querySelector('[data-probe]') as HTMLButtonElement);
    }
    await waitFor(() => expect(count('state.unprobed')).toBe('1'));

    const expected: Record<string, string[]> = {
      reachable: ['d:ok'],
      unreachable: ['d:down'],
      password: ['d:locked'],
      unhandled: ['d:sharded'],
      notserving: ['d:empty'],
      unprobed: ['d:later'],
    };
    for (const [state, keys] of Object.entries(expected)) {
      expect(count(`state.${state}`)).toBe(String(keys.length));
      press(`state.${state}`);
      expect(pressed(`state.${state}`)).toBe('true');
      expect(tiles()).toEqual(keys);
      expect(router.state.location.search).toBe(`?show=state.${state}`);
      press(`state.${state}`);
      expect(tiles()).toHaveLength(6);
    }
    expect(count('engine.Redis')).toBe('1');
    expect(count('engine.Valkey')).toBe('1');
    // A Cluster node reads as a cluster, by the server's word.
    expect(tileOf('d:sharded')?.getAttribute('data-mode')).toBe('cluster');
    expect(screen.queryByText(/0 keys/)).toBeNull();
    expect(tileOf('d:locked')?.querySelector('[data-problem="no_credential"]')?.getAttribute('data-kind')).toBe('refusal');
    expect(tileOf('d:down')?.querySelector('[data-problem="unreachable"]')?.getAttribute('data-kind')).toBe('failure');
    expect(tileOf('d:down')?.getAttribute('data-tone')).toBe('bad');
  });

  it('combine across engine, mode and status, from the address and back to it', async () => {
    const replicated = { ...replication, key: 'd:rep', chart: 'valkey-2.1.0', members: replication.members.filter((m) => !('sentinel' in m)) };
    stub({ [INVENTORY]: { body: body([replication, replicated, { ...alone(backend('solo', 'apps', ['solo'])), chart: 'valkey-2.1.0' }]) } });
    const router = mount('x/redis?show=engine.Valkey');
    await waitFor(() => expect(tiles()).toEqual(['d:rep', 'd:solo']));
    expect(pressed('engine.Valkey')).toBe('true');
    expect(count('mode.replication')).toBe('1');
    expect(count('mode.sentinel')).toBe('1');
    expect(count('mode.standalone')).toBe('1');

    press('mode.replication');
    expect(tiles()).toEqual(['d:rep']);
    expect(router.state.location.search).toBe('?show=engine.Valkey&show=mode.replication');
    expect(router.state.historyAction).toBe('REPLACE');

    press('engine.Valkey');
    expect(tiles()).toEqual(['d:rep']);
    expect(router.state.location.search).toBe('?show=mode.replication');

    fireEvent.click(screen.getByText('All'));
    expect(tiles()).toHaveLength(3);
    expect(router.state.location.search).toBe('');
  });

  it('probe the rest one tunnel at a time, and stop when told', async () => {
    const asked: string[] = [];
    const pending: ((r: Response) => void)[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        asked.push(url);
        const json = (b: unknown) => new Response(JSON.stringify(b), { headers: { 'Content-Type': 'application/json' } });
        if (url === INVENTORY) return Promise.resolve(json(inventory()));
        return new Promise<Response>((resolve) => pending.push(() => resolve(json(answered('Redis', '7.2.4')))));
      }),
    );
    mount('x/redis');
    await waitFor(() => expect(tiles()).toHaveLength(2));
    const probes = () => asked.filter((u) => u.endsWith('/probe'));
    expect(document.querySelector('[data-probe-each]')?.textContent).toBe('Probe all (2)');

    fireEvent.click(document.querySelector('[data-probe-each]') as HTMLButtonElement);
    await waitFor(() => expect(probes()).toHaveLength(1));
    // The second waits for the first: never two tunnels at once.
    await new Promise((r) => setTimeout(r, 20));
    expect(probes()).toHaveLength(1);
    pending.shift()?.(new Response());
    await waitFor(() => expect(probes()).toHaveLength(2));
    expect(count('state.reachable')).toBe('1');

    fireEvent.click(document.querySelector('[data-probe-stop]') as HTMLButtonElement);
    await waitFor(() => expect(document.querySelector('[data-probe-stop]')).toBeNull());
    // The probe in flight was abandoned, and its tile is back to not probed.
    expect(count('state.unprobed')).toBe('1');
    expect(count('state.reachable')).toBe('1');
  });
});

describe('what the inventory could not read', () => {
  it('says which namespaces were grouped without their pods', async () => {
    stub({
      [INVENTORY]: {
        body: {
          ...body([replication]),
          podsNotes: ['the pods in apps could not be read (forbidden), so its Redises are grouped without their workloads and pod labels'],
        },
      },
    });
    mount('x/redis');
    await waitFor(() => expect(tiles()).toEqual(['d:cache-redis']));
    expect(document.querySelector('[data-pods="unread"]')?.textContent).toContain('The pods in apps could not be read');
  });
});
