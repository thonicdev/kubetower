import { QueryObserver } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { createQueryClient } from '../query/client';

import { SHARED_CAPABILITIES_MS, capabilitiesQuery } from './queries';
import type { Capabilities } from './types';

/**
 * How long an open tab believes what it was told about itself.
 *
 * At the query and not through a page, for `queries.test.ts`'s reason: the
 * interval has no rendered form until time passes. What is pinned is that a
 * **revocation reaches a tab that stays open** - `podExec` under the shared
 * profile is the cluster's answer, held a minute on the server, and the query
 * read it once with `staleTime: Infinity`, so *Open a shell* stayed on the page
 * after `rbac.exec` was taken away, for as long as nobody reloaded.
 */

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** A server whose `podExec` can be changed under the tab, counting the reads. */
function serve(mode: Capabilities['mode']): { grant: { podExec: boolean }; reads: () => number } {
  const grant = { podExec: true };
  let reads = 0;
  vi.stubGlobal('fetch', () => {
    reads += 1;
    const body: Partial<Capabilities> = { mode, podExec: grant.podExec, portForward: false };
    return Promise.resolve(
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
  });
  return { grant, reads: () => reads };
}

/** An open tab: one subscribed observer of the query, as a mounted page holds. */
function open() {
  const observer = new QueryObserver(createQueryClient(), capabilitiesQuery());
  const close = observer.subscribe(() => undefined);
  return { now: () => observer.getCurrentResult(), close };
}

describe('capabilities, under a tab that stays open', () => {
  it('re-reads under the shared profile, so a revoked exec grant leaves the page', async () => {
    vi.useFakeTimers();
    const server = serve('shared');
    const tab = open();
    await vi.advanceTimersByTimeAsync(0);
    expect(tab.now().data?.podExec).toBe(true);

    // `helm upgrade --set rbac.exec=false`, and the server's minute passes.
    server.grant.podExec = false;
    await vi.advanceTimersByTimeAsync(SHARED_CAPABILITIES_MS);

    expect(tab.now().data?.podExec).toBe(false);
    expect(server.reads()).toBe(2);
    tab.close();
  });

  it('re-reads on the server’s own minute and not faster', async () => {
    // Faster reads the same cached review; the audit log pays for each one.
    vi.useFakeTimers();
    const server = serve('shared');
    const tab = open();
    await vi.advanceTimersByTimeAsync(SHARED_CAPABILITIES_MS - 1);
    expect(server.reads()).toBe(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(server.reads()).toBe(2);
    tab.close();
  });

  it.each(['desktop', 'container'] as const)(
    'reads once under %s, where the answer describes the build',
    async (mode) => {
      vi.useFakeTimers();
      const server = serve(mode);
      const tab = open();
      await vi.advanceTimersByTimeAsync(10 * SHARED_CAPABILITIES_MS);
      expect(server.reads()).toBe(1);
      expect(tab.now().isStale).toBe(false);
      tab.close();
    },
  );
});
