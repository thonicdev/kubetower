import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Deployment, Member } from './data';
import { labelledRedis, podNotChecked, podShortcut, serviceShortcut } from './shortcut';

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

  it('marks an ExternalName Service to a managed Redis as external, with its address and no probe', async () => {
    const asked = stub({
      '/x/kind/redis/services/apps/sessions': { body: { namespace: 'apps', service: 'sessions', deployments: [], external: { namespace: 'apps', service: 'sessions', host: 'demo.abc123.cache.amazonaws.com', port: 6379, why: 'it is on 6379' } } },
    });
    expect(await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'apps', name: 'sessions' }, signal)).toEqual({
      title: 'sessions',
      badges: ['external'],
      detail: 'demo.abc123.cache.amazonaws.com:6379',
      to: '/c/kind/services/apps/sessions?tab=x.redis.backend',
    });
    expect(asked).toEqual(['/x/kind/redis/services/apps/sessions']);
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

  it('draws nothing for a Service that is not a Redis, and says when it could not check', async () => {
    stub({ '/x/kind/redis/services/cache/web': { status: 404, body: { code: 'not_redis', kind: 'absent', message: 'Not a Redis.' } } });
    expect(await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'web' }, signal)).toBeNull();
    const sentinelOnly = { ...sentinel, members: [sentinel.members[0] as Member, { ...(sentinel.members[1] as Member), doors: [{ namespace: 'cache', service: 'rsent-sentinel', port: 26379, why: 'sentinel' }] }] };
    stub({ '/x/kind/redis/services/cache/rsent-sentinel': { body: { namespace: 'cache', service: 'rsent-sentinel', deployments: [sentinelOnly] } } });
    expect(await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'rsent-sentinel' }, signal)).toBeNull();
    stub({ '/x/kind/redis/services/cache/web': { status: 403, body: { code: 'forbidden', kind: 'failure', message: 'The Services in cache could not be listed.' } } });
    expect(await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'web' }, signal)).toEqual({
      title: 'web',
      status: { text: 'not checked', tone: 'warn', hint: 'The Services in cache could not be listed.' },
      to: '/c/kind/services/cache/web?tab=x.redis.backend',
    });
    stub({ '/x/kind/redis/services/cache/web': { status: 404, body: { code: 'gate', kind: 'absent', message: 'Off.' } } });
    expect(await serviceShortcut({ cluster: 'kind', kind: 'services', namespace: 'cache', name: 'web' }, signal)).toBeNull();
  });

  it('says a Redis pod was not checked when its namespace could not be read, and nothing otherwise', async () => {
    const pod = { cluster: 'kind', kind: 'pods', namespace: 'cache', name: 'kv-0' };
    stub({ '/x/kind/redis/?ns=cache': { status: 403, body: { code: 'forbidden', kind: 'failure', message: 'The EndpointSlices in cache could not be listed.' } } });
    expect(await podNotChecked(pod, signal)).toEqual({
      title: 'kv-0',
      status: { text: 'not checked', tone: 'warn', hint: 'The EndpointSlices in cache could not be listed.' },
      to: '/c/kind/x/redis?ns=cache',
    });
    stub({ '/x/kind/redis/?ns=cache': { body: inventory([]) } });
    expect(await podNotChecked(pod, signal)).toBeNull();
  });

  it('takes a pod for a Redis by its labels, not for a tool beside one', () => {
    const marks = (labels: Record<string, string>) => ({ labels, annotations: {} });
    expect(labelledRedis(marks({ 'app.kubernetes.io/name': 'infra-valkey' }))).toBe(true);
    expect(labelledRedis(marks({ app: 'redis-cluster' }))).toBe(true);
    expect(labelledRedis(marks({ app: 'redis-exporter' }))).toBe(false);
    expect(labelledRedis(marks({ app: 'web' }))).toBe(false);
    expect(labelledRedis(marks({}))).toBe(false);
  });
});
