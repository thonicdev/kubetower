import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { redis } from './index';
import RedisTab from './RedisTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

/** The tab on a Service's panel. */

function stub(replies: Record<string, { status?: number; body: unknown }>): string[] {
  const asked: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      asked.push(url);
      const reply = replies[url];
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(
        new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }),
      );
    }),
  );
  return asked;
}

function withProviders(element: ReactNode): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/*', element: <Suspense>{element}</Suspense> }], {
    initialEntries: ['/c/docker-desktop/services'],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const SERVICE = '/x/docker-desktop/redis/services/cache/redis-master';

const member = {
  key: 'cache\u0000pods=redis-master-0',
  pods: ['redis-master-0'],
  behind: 'redis-master-0',
  ready: 1,
  reachable: true,
  endpointless: false,
  role: 'primary',
  roleFrom: 'labels',
  workloads: ['StatefulSet/redis-master'],
  doors: [
    { namespace: 'cache', service: 'redis-headless', port: 6379, why: 'the port is named tcp-redis', role: 'headless', roleFrom: 'spec' },
    { namespace: 'cache', service: 'redis-master', port: 6379, why: 'the port is named tcp-redis', role: 'primary', roleFrom: 'labels' },
  ],
};

const service = {
  namespace: 'cache',
  service: 'redis-master',
  deployments: [
    {
      key: 'cache\u0000deployment=cache\u0000pods=redis-master-0',
      name: 'redis',
      evidence: [],
      release: 'cache',
      workloads: ['StatefulSet/redis-master'],
      members: [member],
    },
  ],
};

describe('the Redis tab', () => {
  it('is declared on Services, under x.redis.backend, and drawn only where the route says it is a Redis', async () => {
    const tab = redis.tabs?.[0];
    expect(tab?.kinds).toEqual(['services']);
    expect(tab?.key).toBe('backend');
    expect(tab?.label).toBe('Redis');

    stub({
      [SERVICE]: { body: service },
      '/x/docker-desktop/redis/services/cache/web': { status: 404, body: { code: 'not_redis', kind: 'absent', message: 'No port…' } },
    });
    const signal = new AbortController().signal;
    expect(await tab?.probe?.({ cluster: 'docker-desktop', kind: 'services', namespace: 'cache', name: 'redis-master' }, signal)).toBe(true);
    expect(await tab?.probe?.({ cluster: 'docker-desktop', kind: 'services', namespace: 'cache', name: 'web' }, signal)).toBe(false);
  });

  it('is drawn, and says it could not check, when discovery failed - never hidden as if not a Redis', async () => {
    const failed = { status: 502, body: { code: 'list_failed', kind: 'failure', message: 'The Services could not be read, so whether this cluster runs a Redis is unknown.' } };
    stub({ [SERVICE]: failed });
    const tab = redis.tabs?.[0];
    const signal = new AbortController().signal;
    expect(await tab?.probe?.({ cluster: 'docker-desktop', kind: 'services', namespace: 'cache', name: 'redis-master' }, signal)).toBe(true);

    withProviders(<RedisTab cluster="docker-desktop" kind="services" namespace="cache" name="redis-master" />);
    expect(await screen.findByText('Could not check whether this Service is a Redis')).toBeTruthy();
    expect(document.querySelector('[data-redis-tab="unchecked"] [data-problem="list_failed"]')?.getAttribute('data-kind')).toBe('failure');
  });

  it('draws the deployment, the doors, their reasons and the role, and dials nothing on mount', async () => {
    const asked = stub({
      [SERVICE]: { body: service },
      '/x/docker-desktop/redis/instances/cache/redis-master/6379/probe': {
        body: {
          instance: member.doors[1],
          db: 0,
          server: { engine: 'Redis', version: '7.2.4', mode: 'standalone', databases: 16 },
          overview: { keys: 3, keyspace: [] },
        },
      },
    });
    withProviders(<RedisTab cluster="docker-desktop" kind="services" namespace="cache" name="redis-master" />);
    expect(await screen.findByText('redis-master-0')).toBeTruthy();
    expect(document.querySelectorAll('[data-door]')).toHaveLength(2);
    expect(screen.getAllByText('the port is named tcp-redis')).toHaveLength(2);
    expect(document.querySelector('[data-door="redis-master:6379"]')?.getAttribute('href')).toBe(
      '/c/docker-desktop/x/redis/cache/redis-master/6379',
    );
    expect(document.querySelector('[data-deployment] [data-role]')?.getAttribute('data-role')).toBe('primary');
    expect(document.querySelector('[data-deployment]')?.textContent).toContain('release cache');
    expect(asked).toEqual([SERVICE]);

    // The probe goes to this Service's own door.
    fireEvent.click(document.querySelector('[data-probe]') as HTMLButtonElement);
    expect(await screen.findByText('Redis 7.2.4')).toBeTruthy();
    expect(asked).toEqual([SERVICE, '/x/docker-desktop/redis/instances/cache/redis-master/6379/probe']);
  });
});

/**
 * The deployment card, which the Service tab draws.
 * These lived on the inventory until it became one tile per deployment;
 * the card and its rules did not change.
 */
describe('the deployment card on the tab', () => {
  const PRIMARY = '/x/docker-desktop/redis/services/apps/cache-redis-master';
  const door = (service: string, role: string, roleFrom: string, port = 6379) => ({
    namespace: 'apps',
    service,
    port,
    why: 'the port is named tcp-redis',
    role,
    roleFrom,
  });
  const one = (key: string, pods: string[], extra: Record<string, unknown>) => ({
    key,
    pods,
    behind: pods.join(', '),
    ready: pods.length,
    reachable: true,
    endpointless: false,
    workloads: [],
    ...extra,
  });
  const replication = {
    key: 'd:cache-redis',
    name: 'cache-redis',
    evidence: ['workload', 'pods', 'release'],
    release: 'cache',
    chart: 'redis-19.6.4',
    workloads: ['StatefulSet/cache-redis-master', 'StatefulSet/cache-redis-replicas'],
    members: [
      one('master', ['cache-redis-master-0'], { role: 'primary', roleFrom: 'labels', doors: [door('cache-redis-master', 'primary', 'labels')] }),
      one('replicas', ['cache-redis-replicas-0', 'cache-redis-replicas-1'], {
        role: 'replica',
        roleFrom: 'labels',
        doors: [door('cache-redis-replicas', 'replica', 'labels')],
      }),
      one('headless', ['cache-redis-master-0'], { role: 'headless', roleFrom: 'spec', doors: [door('cache-redis-headless', 'headless', 'spec')] }),
      one('sentinels', ['cache-redis-master-0'], {
        role: 'sentinel',
        roleFrom: 'port',
        sentinel: true,
        doors: [door('cache-redis', 'sentinel', 'port', 26379)],
      }),
    ],
  };
  const answer = (deployments: unknown[]) => ({ namespace: 'apps', service: 'cache-redis-master', deployments });
  const roles = () =>
    [...document.querySelectorAll('[data-backend] [data-role]')].map((r) => `${r.getAttribute('data-role')}/${r.getAttribute('data-role-from')}`);
  const tab = () => withProviders(<RedisTab cluster="docker-desktop" kind="services" namespace="apps" name="cache-redis-master" />);

  it('draws every member with its role and source, and a Sentinel drawn but never offered', async () => {
    stub({ [PRIMARY]: { body: answer([replication]) } });
    tab();
    expect(await screen.findByText('cache-redis')).toBeTruthy();
    expect(roles()).toEqual(['primary/labels', 'replica/labels', 'headless/spec', 'sentinel/port']);
    expect(document.querySelector('[data-evidence]')?.textContent).toBe(
      'Grouped by the workloads owning their pods, pods they share, their Helm or operator labels.',
    );
    const sentinel = document.querySelector('[data-backend="sentinels"]');
    expect(sentinel?.querySelector('[data-door]')).toBeNull();
    expect(sentinel?.querySelector('[data-probe]')).toBeNull();
  });

  it('puts the server’s own role in place of the label’s once probed, and shows the guess it contradicts', async () => {
    stub({
      [PRIMARY]: { body: answer([replication]) },
      '/x/docker-desktop/redis/instances/apps/cache-redis-replicas/6379/probe': {
        body: {
          instance: door('cache-redis-replicas', 'replica', 'labels'),
          pod: 'cache-redis-replicas-1',
          db: 0,
          server: { engine: 'Redis', version: '7.4.1', mode: 'standalone', databases: 16, role: 'primary', replicas: 1 },
          overview: { keys: 1, keyspace: [] },
        },
      },
    });
    tab();
    expect(await screen.findByText('cache-redis')).toBeTruthy();
    fireEvent.click(document.querySelector('[data-backend="replicas"] [data-probe]') as HTMLButtonElement);
    await waitFor(() => expect(roles()[1]).toBe('primary/info'));
    const badge = document.querySelector('[data-backend="replicas"] [data-role]');
    expect(badge?.textContent).toContain('labels: replica');
    expect(badge?.getAttribute('title')).toContain('cache-redis-replicas-1 answers as primary');
  });

  it('says a fold made on names alone is a hint', async () => {
    stub({ [PRIMARY]: { body: answer([{ ...replication, evidence: ['name'], release: undefined, chart: undefined, workloads: [] }]) } });
    tab();
    expect(await screen.findByText('cache-redis')).toBeTruthy();
    const said = document.querySelector('[data-evidence="name"]');
    expect(said?.textContent).toContain('a hint, not a fact');
    expect(said?.className).toContain('text-warn');
  });
});

describe('a Sentinel in the same pods as its Redis (the review of da9da8f)', () => {
  it('keeps its own row and role when the Redis beside it is probed', async () => {
    const pods = ['cache-redis-node-0', 'cache-redis-node-1'];
    const base = { pods, behind: '2 pods: cache-redis-node-0, cache-redis-node-1', ready: 2, reachable: true, endpointless: false, workloads: ['StatefulSet/cache-redis-node'] };
    const redisKey = 'apps\u0000pods=cache-redis-node-0,cache-redis-node-1';
    const at = (port: number) => ({ namespace: 'apps', service: 'cache-redis', port, why: 'the port is named tcp-redis' });
    stub({
      '/x/docker-desktop/redis/services/apps/cache-redis': {
        body: {
          namespace: 'apps',
          service: 'cache-redis',
          deployments: [
            {
              key: 'd',
              name: 'cache-redis',
              evidence: ['workload', 'pods'],
              workloads: ['StatefulSet/cache-redis-node'],
              members: [
                { ...base, key: redisKey, doors: [at(6379)] },
                { ...base, key: `${redisKey}\u0000sentinel`, role: 'sentinel', roleFrom: 'port', sentinel: true, doors: [at(26379)] },
              ],
            },
          ],
        },
      },
      '/x/docker-desktop/redis/instances/apps/cache-redis/6379/probe': {
        body: { instance: at(6379), pod: 'p-0', db: 0, server: { engine: 'Redis', version: '7.4.1', mode: 'standalone', databases: 16, role: 'primary' } },
      },
    });
    withProviders(<RedisTab cluster="docker-desktop" kind="services" namespace="apps" name="cache-redis" />);
    expect(await screen.findByText('cache-redis')).toBeTruthy();
    // A NUL cannot be written in a CSS selector: rows are found by attribute.
    const row = (key: string) => [...document.querySelectorAll('[data-backend]')].find((r) => r.getAttribute('data-backend') === key);
    fireEvent.click(row(redisKey)?.querySelector('[data-probe]') as HTMLButtonElement);
    await waitFor(() => expect(row(redisKey)?.querySelector('[data-role]')?.getAttribute('data-role-from')).toBe('info'));
    const sentinel = row(`${redisKey}\u0000sentinel`)?.querySelector('[data-role]');
    expect(sentinel?.getAttribute('data-role')).toBe('sentinel');
    expect(sentinel?.getAttribute('data-role-from')).toBe('port');
    expect(document.querySelectorAll('[data-backend]')).toHaveLength(2);
  });
});
