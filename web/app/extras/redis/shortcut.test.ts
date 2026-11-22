import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Deployment, Member } from './data';
import { podShortcut, serviceShortcut } from './shortcut';

/**
 * The Redis viewer's tile on a Service's or a Pod's Details: which deployment
 * it belongs to, from discovery alone, and nothing for anything else.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const member = (key: string, service: string, pods: string[], over: Partial<Member> = {}): Member => ({
  key,
  pods,
  behind: '',
  ready: pods.length,
  reachable: pods.length > 0,
  endpointless: pods.length === 0,
  workloads: [],
  doors: [{ namespace: 'cache', service, port: 6379, why: 'the port is named redis' }],
  ...over,
});

const sentinel: Deployment = {
  key: 'cache/rsent',
  name: 'rsent-redis',
  evidence: ['release'],
  chart: 'redis-28.3.9',
  workloads: [],
  members: [
    member('n', 'rsent-redis', ['rsent-redis-node-0', 'rsent-redis-node-1']),
    member('s', 'rsent-redis', ['rsent-redis-node-0', 'rsent-redis-node-1'], {
      sentinel: true,
      role: 'sentinel',
      doors: [{ namespace: 'cache', service: 'rsent-redis', port: 26379, why: 'sentinel' }],
    }),
  ],
};

function stub(replies: Record<string, { status?: number; body: unknown }>): string[] {
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

const signal = new AbortController().signal;
const inventory = (deployments: Deployment[]) => ({
  scope: 'cache',
  complete: true,
  services: 1,
  members: 1,
  redises: deployments.length,
  namespaces: [{ name: 'cache', deployments }],
});

describe("the Redis viewer's shortcut", () => {
  it('names the deployment a Service belongs to and opens its page, from discovery only', async () => {
    const asked = stub({ '/x/kind/redis/services/cache/rsent-redis': { body: { namespace: 'cache', service: 'rsent-redis', deployments: [sentinel] } } });
    const target = await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'rsent-redis' }, signal);
    expect(target).toEqual({
      title: 'rsent-redis',
      badges: ['Redis', 'sentinel'],
      detail: 'cache · 2 nodes',
      to: '/c/kind/x/redis/cache/rsent-redis/6379',
    });
    // Nothing that dials: no instance, no probe.
    expect(asked).toEqual(['/x/kind/redis/services/cache/rsent-redis']);
  });

  it('names the deployment a Pod is behind, and nothing for a pod that is not', async () => {
    stub({ '/x/kind/redis/?ns=cache': { body: inventory([sentinel]) } });
    const target = await podShortcut({ cluster: 'kind', kind: 'pods', namespace: 'cache', name: 'rsent-redis-node-1' }, signal);
    expect(target?.title).toBe('rsent-redis');
    expect(await podShortcut({ cluster: 'kind', kind: 'pods', namespace: 'cache', name: 'web-0' }, signal)).toBeNull();
  });

  it('opens nothing for a pod behind a Sentinel port alone, which holds no keys', async () => {
    const apart: Deployment = {
      ...sentinel,
      members: [
        member('n', 'redis', ['redis-0']),
        member('s', 'redis-sentinel', ['sentinel-0'], { sentinel: true, role: 'sentinel' }),
      ],
    };
    stub({ '/x/kind/redis/?ns=cache': { body: inventory([apart]) } });
    expect(await podShortcut({ cluster: 'kind', kind: 'pods', namespace: 'cache', name: 'sentinel-0' }, signal)).toBeNull();
    expect((await podShortcut({ cluster: 'kind', kind: 'pods', namespace: 'cache', name: 'redis-0' }, signal))?.title).toBe('rsent-redis');
  });

  it('draws nothing for a Service that is not a Redis, and lets a failure through', async () => {
    stub({ '/x/kind/redis/services/cache/web': { status: 404, body: { code: 'not_redis', kind: 'absent', message: 'Not a Redis.' } } });
    expect(await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'web' }, signal)).toBeNull();
    const sentinelOnly = { ...sentinel, members: [sentinel.members[0] as Member, { ...(sentinel.members[1] as Member), doors: [{ namespace: 'cache', service: 'rsent-sentinel', port: 26379, why: 'sentinel' }] }] };
    stub({ '/x/kind/redis/services/cache/rsent-sentinel': { body: { namespace: 'cache', service: 'rsent-sentinel', deployments: [sentinelOnly] } } });
    expect(await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'rsent-sentinel' }, signal)).toBeNull();
    stub({ '/x/kind/redis/services/cache/web': { status: 502, body: { code: 'list_failed', kind: 'failure', message: 'The Services could not be read.' } } });
    await expect(serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'web' }, signal)).rejects.toBeTruthy();
  });
});
