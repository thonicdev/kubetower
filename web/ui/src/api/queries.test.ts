import { QueryClient } from '@tanstack/react-query';
import { describe, expect, it } from 'vitest';

import {
  MENU_FRESH_MS,
  invalidateActedObjects,
  listRefreshMs,
  navQuery,
  queryKeys,
  resourceListQuery,
  sameListAsked,
} from './queries';
import type { ListPage } from './types';

/**
 * The refresh interval, and why this is tested at the query rather than through
 * the page.
 *
 * `ResourceList.test.tsx` argues for testing at the call site rather than the
 * predicate. This is the exception, and the reason is that **the interval has no
 * rendered form at all**. Nothing about a list drawn at 60 seconds differs from
 * the same list drawn at 2 until a minute has passed, so a page-level assertion
 * would be a fake timer advancing over a polling loop, testing TanStack's
 * scheduler rather than what this console decided.
 *
 * What is worth pinning is that the figure is **read out of the answer**, which
 * is one expression: `internal/resources/kinds.go` bands it 2s live, 5s
 * reconciling, 60s declared.
 *
 * It is the one loss on this surface nobody would report, because nothing looks
 * wrong: a Secrets list at two seconds pushes megabytes of plaintext through the
 * process every minute to render a Keys column.
 */

/** What TanStack hands `refetchInterval`. Only `state.data` is read. */
function asQuery(data: unknown): never {
  return { state: { data } } as never;
}

function intervalFor(data: unknown): number {
  const option = resourceListQuery('prod', 'secrets').refetchInterval;
  if (typeof option !== 'function') throw new Error('refetchInterval is not derived from the answer');
  const answer = option(asQuery(data));
  if (typeof answer !== 'number') throw new Error(`refetchInterval returned ${String(answer)}`);
  return answer;
}

const page = (refreshSeconds: number | undefined): { state: 'ok'; data: ListPage } => ({
  state: 'ok',
  data: {
    kind: 'secrets',
    label: 'Secrets',
    namespaced: true,
    columns: [],
    rows: [],
    ...(refreshSeconds === undefined ? {} : { refreshSeconds }),
  } as ListPage,
});

describe('how often a list re-asks its cluster', () => {
  it('uses the kind’s own band and not one interval for everything', () => {
    // The three bands of kinds.go, through the member the server sends.
    expect(intervalFor(page(2))).toBe(2_000);
    expect(intervalFor(page(5))).toBe(5_000);
    // Secrets: two seconds here would push their plaintext thirty times a minute.
    expect(intervalFor(page(60))).toBe(60_000);
  });

  it('falls back to the fastest band while there is no answer to read', () => {
    /*
      The first fetch has nothing to read the band from, and being wrong in this
      direction costs one extra read of a slow kind. The other direction — the
      slowest band as the default — would leave a live kind a minute stale on
      first arrival, which is the failure that matters.
    */
    expect(intervalFor(undefined)).toBe(2_000);
  });

  it('falls back on an arm that carries no page', () => {
    // A failed read has no `data` to hold a band. `unreachable` is the case: the
    // list must keep asking, at the live rate, or a cluster that comes back stays
    // unreported until the operator navigates.
    expect(intervalFor({ state: 'unreachable', reason: { code: 'unreachable', message: 'no' } })).toBe(
      2_000,
    );
  });

  it('refuses a nonsensical band rather than polling as fast as it can', () => {
    // Zero from a kind whose refresh was never set would be an interval of 0ms,
    // which TanStack reads as "again immediately" — a request loop against a
    // cluster, from one bad field.
    expect(intervalFor(page(0))).toBe(2_000);
  });
});

describe('which list keys are the same list', () => {
  const key = (cluster: string, kind: string, namespace: string, selector = '', search = '') =>
    queryKeys.resourceList(cluster, kind, namespace, selector, search);

  it('is the same list whatever the search or the selector', () => {
    expect(sameListAsked(key('prod', 'pods', ''), key('prod', 'pods', '', '', 'ngin'))).toBe(true);
    expect(sameListAsked(key('prod', 'pods', 'a,b', 'app=api'), key('prod', 'pods', 'a,b', '', 'x'))).toBe(true);
  });

  it('is the same list whatever the filter control chose', () => {
    expect(
      sameListAsked(key('prod', 'pods', ''), queryKeys.resourceList('prod', 'pods', '', '', '', 'node:worker-2')),
    ).toBe(true);
  });

  it('is another list on another cluster, kind or namespace selection', () => {
    expect(sameListAsked(key('prod', 'pods', ''), key('staging', 'pods', ''))).toBe(false);
    expect(sameListAsked(key('prod', 'pods', ''), key('prod', 'ingresses', ''))).toBe(false);
    expect(sameListAsked(key('prod', 'pods', ''), key('prod', 'pods', 'kube-system'))).toBe(false);
    expect(sameListAsked(key('prod', 'pods', 'a'), key('prod', 'pods', 'a,b'))).toBe(false);
  });

  it('is never the same list as a key of another shape', () => {
    // A key sharing the list's four leading members but not its length is not
    // a list key at all, and its data is not a list page.
    expect(sameListAsked(['resources', 'prod', 'pods', ''], key('prod', 'pods', ''))).toBe(false);
    expect(sameListAsked(key('prod', 'pods', ''), ['resources', 'prod', 'pods', '', '', '', '', 'x'])).toBe(false);
    expect(sameListAsked(queryKeys.resourceKind('prod', 'pods'), key('prod', 'pods', ''))).toBe(false);
    expect(
      sameListAsked(queryKeys.resource('prod', 'pods', '', 'x'), queryKeys.resource('prod', 'pods', '', 'y')),
    ).toBe(false);
  });
});

describe('how often a list is asked again', () => {
  it('asks a list the console declined to narrow once, not every two seconds', () => {
    // The console's own ceiling is the same answer until the selection
    // changes, and each ask repeats the whole read behind it.
    expect(
      listRefreshMs({
        state: 'unreadable',
        reason: { code: 'not_supported_here', message: 'choose a namespace', retryable: false },
      }),
    ).toBe(false);
  });

  it('keeps asking every other answer that is not ok', () => {
    expect(
      listRefreshMs({
        state: 'unreadable',
        reason: { code: 'access_unreviewable', message: 'could not ask', retryable: true },
      }),
    ).toBe(2_000);
    expect(listRefreshMs({ state: 'forbidden', reason: { code: 'read_denied', message: 'no' } })).toBe(2_000);
  });
});

/**
 * Deleting a namespace refreshes the namespace selector: it lists the cluster's
 * namespaces under a key of its own, kept for a minute, so without this it
 * would go on offering the one just deleted.
 */
describe('acting on a namespace refreshes the namespace selector', () => {
  function seeded(): QueryClient {
    const client = new QueryClient();
    client.setQueryData(queryKeys.namespaces('prod'), { state: 'ok', items: [{ name: 'shop' }] });
    client.setQueryData(queryKeys.namespaces('other'), { state: 'ok', items: [{ name: 'shop' }] });
    return client;
  }

  it('invalidates the selector of that cluster, and only that one', async () => {
    const client = seeded();
    await invalidateActedObjects(client, 'prod', 'namespaces', [{ kind: 'namespaces', name: 'shop' }]);
    expect(client.getQueryState(queryKeys.namespaces('prod'))?.isInvalidated).toBe(true);
    expect(client.getQueryState(queryKeys.namespaces('other'))?.isInvalidated).toBe(false);
  });

  it('leaves the selector alone when a pod was acted on', async () => {
    const client = seeded();
    await invalidateActedObjects(client, 'prod', 'pods', [{ kind: 'pods', namespace: 'shop', name: 'api' }]);
    expect(client.getQueryState(queryKeys.namespaces('prod'))?.isInvalidated).toBe(false);
  });
});

/**
 * The menu carries the extras the person may use, reviewed on the server and
 * held there ten seconds when the review failed (`access.FailedTTL`). Kept
 * longer here, a grant or a revocation reaches the menu late by the difference
 * - it was sixty seconds.
 */
describe('how long the menu is believed', () => {
  it('is no longer than the server keeps its review', () => {
    expect(navQuery('prod').staleTime).toBe(MENU_FRESH_MS);
    expect(MENU_FRESH_MS).toBeLessThanOrEqual(10_000);
  });
});
