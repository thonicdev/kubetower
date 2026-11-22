import { QueryObserver } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { createQueryClient } from '../query/client';

import { SHARED_CAPABILITIES_MS, resourceDetailQuery } from './queries';

/**
 * Whether an open object page follows what the person may do.
 *
 * The page's `actions` are the server's answer to *may this person perform this
 * verb on this object* - a review of the cluster's own RBAC, held a minute on
 * the server. Under the shared profile that answer changes without the object
 * changing: a RoleBinding added in one namespace should bring Delete onto a pod
 * page there within the minute, without a reload, and one removed should take
 * it away. On the desktop the page is read once.
 */

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** A pod page whose offered verbs can be changed under the tab. */
function serve(): { granted: { delete: boolean }; reads: () => number } {
  const granted = { delete: false };
  let reads = 0;
  vi.stubGlobal('fetch', () => {
    reads += 1;
    const actions = [
      { verb: 'evict', label: 'Evict', permitted: false, reason: { code: 'access_unreviewable', message: 'x' } },
      ...(granted.delete ? [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true }] : []),
    ];
    const body = { state: 'ok', data: { kind: 'pods', namespace: 'shop', name: 'api-1', title: 'api-1', tabs: [], actions } };
    return Promise.resolve(
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
  });
  return { granted, reads: () => reads };
}

function open(shared: boolean) {
  const observer = new QueryObserver(createQueryClient(), resourceDetailQuery('prod', 'pods', 'shop', 'api-1', shared));
  const close = observer.subscribe(() => undefined);
  const verbs = (): string[] => {
    const result = observer.getCurrentResult().data;
    if (result === undefined || result.state !== 'ok') return [];
    return (result.data.actions ?? []).map((action) => action.verb);
  };
  return { verbs, close };
}

describe('an open object page, as grants change', () => {
  it('shows a newly granted verb within the minute under the shared profile', async () => {
    vi.useFakeTimers();
    const server = serve();
    const tab = open(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(tab.verbs()).toEqual(['evict']);

    // `delete pods` granted in `shop`, and the server's minute passes.
    server.granted.delete = true;
    await vi.advanceTimersByTimeAsync(SHARED_CAPABILITIES_MS);
    expect(tab.verbs()).toEqual(['evict', 'delete']);

    // And revoked again.
    server.granted.delete = false;
    await vi.advanceTimersByTimeAsync(SHARED_CAPABILITIES_MS);
    expect(tab.verbs()).toEqual(['evict']);
    tab.close();
  });

  it('is read once on the desktop', async () => {
    vi.useFakeTimers();
    const server = serve();
    const tab = open(false);
    await vi.advanceTimersByTimeAsync(10 * SHARED_CAPABILITIES_MS);
    expect(server.reads()).toBe(1);
    tab.close();
  });
});
