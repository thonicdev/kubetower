import { QueryClient } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { queryKeys } from '../api';
import { AGAIN_MS, QUIET_MS, credentialFailureOf, watchCredentials } from './credentialWatch';

afterEach(() => {
  vi.unstubAllGlobals();
});

const lapsed = (cluster: string, code = 'credentials_expired') => ({
  state: 'unreadable',
  reason: { code, message: 'listing pods: getting credentials: …', scope: { cluster, resource: 'pods', verb: 'list' } },
});

function probes(answer: Record<string, unknown>): string[] {
  const asked: string[] = [];
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    asked.push(`${init?.method ?? 'GET'} ${new URL(raw, 'http://console.test').pathname}`);
    return new Response(JSON.stringify(answer), { status: 200, headers: { 'Content-Type': 'application/json' } });
  });
  return asked;
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

describe('credentialFailureOf', () => {
  it('names the cluster of a read that failed at the credential step', () => {
    expect(credentialFailureOf(lapsed('prod'))).toBe('prod');
    expect(credentialFailureOf(lapsed('prod', 'credentials_refused'))).toBe('prod');
  });

  it('is nothing for any other answer', () => {
    expect(credentialFailureOf(undefined)).toBeUndefined();
    expect(credentialFailureOf({ state: 'ok', items: [1] })).toBeUndefined();
    expect(
      credentialFailureOf({ state: 'unreachable', reason: { code: 'timeout', message: 'x', scope: { cluster: 'prod' } } }),
    ).toBeUndefined();
    expect(
      credentialFailureOf({ state: 'forbidden', reason: { code: 'verb_denied', message: 'x', scope: { cluster: 'prod' } } }),
    ).toBeUndefined();
    // A check about this machine, with no cluster in its scope (the health
    // page's SSO row), asks nothing.
    expect(credentialFailureOf({ state: 'unreadable', reason: { code: 'credentials_expired', message: 'x' } })).toBeUndefined();
  });
});

describe('watchCredentials', () => {
  it('asks the probe once for a cluster the list still calls ok, then reads the list again', async () => {
    const asked = probes({ state: 'ok', data: { name: 'prod', label: 'prod', state: 'unreadable', reason: { code: 'credentials_expired', message: 'x' }, remedy: { who: 'You', what: 'x', actionable: true, command: 'aws sso login' } } });
    const client = new QueryClient();
    client.setQueryData(queryKeys.clusters, { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] });
    const invalidate = vi.spyOn(client, 'invalidateQueries');

    watchCredentials(client, lapsed('prod'), 0);
    watchCredentials(client, lapsed('prod'), 1); // a second panel of the same page
    await settle();
    await settle();

    expect(asked).toEqual(['POST /api/c/prod/probe']);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.clusters });
  });

  it('asks again only after a while, and after longer when the probe found the cluster fine', async () => {
    const asked = probes({ state: 'ok', data: { name: 'prod', label: 'prod', state: 'ok' } });
    const client = new QueryClient();
    const now = Date.now();
    watchCredentials(client, lapsed('prod'), now);
    await settle();
    await settle();
    expect(asked).toHaveLength(1);

    // The probe said the cluster answers: one aggregated API refusing does not
    // keep it busy.
    watchCredentials(client, lapsed('prod'), now + AGAIN_MS + 1);
    await settle();
    expect(asked).toHaveLength(1);
    watchCredentials(client, lapsed('prod'), now + QUIET_MS + AGAIN_MS);
    await settle();
    expect(asked).toHaveLength(2);
  });

  it('asks nothing for a cluster already drawn as one message', async () => {
    const asked = probes({});
    const client = new QueryClient();
    client.setQueryData(queryKeys.clusters, {
      state: 'ok',
      items: [{ name: 'prod', label: 'prod', state: 'unreadable', reason: { code: 'credentials_refused', message: 'x' } }],
    });
    watchCredentials(client, lapsed('prod', 'credentials_refused'), 0);
    await settle();
    expect(asked).toEqual([]);
  });
});
