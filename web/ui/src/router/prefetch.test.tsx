import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { askedPages, forgetAskedPages, pageFor } from './pages';
import { useIntentPrefetch } from './prefetch';

/**
 * What "instant" has to mean, asserted rather than felt.
 *
 * Every test here is about the **request**, never about a rendered spinner. A
 * prefetch that did nothing would still make the page eventually correct, so a
 * test that only checked what is on screen could not tell a warm cache from a
 * cold one - which is the test that passes while the feature is absent.
 *
 * `pointerOver` and not `pointerEnter`: React derives `onPointerEnter` from the
 * bubbling `pointerover`, and `pointerenter` does not bubble, so dispatching the
 * event whose name matches the prop reaches nothing. That mistake is silent - it
 * produces a test that agrees with a component doing nothing at all.
 */

function Link({ href }: { href: string | undefined }) {
  const intent = useIntentPrefetch(href);
  return (
    <a href={href ?? '#'} {...intent}>
      {href ?? 'nowhere'}
    </a>
  );
}

let client: QueryClient;
let fetches: string[];

beforeEach(() => {
  vi.useFakeTimers();
  fetches = [];
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.spyOn(globalThis, 'fetch').mockImplementation((input: RequestInfo | URL) => {
    fetches.push(typeof input === 'string' ? input : input.toString());
    return Promise.resolve(
      new Response(JSON.stringify({ state: 'empty', reason: { code: 'nothing' } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });
});

afterEach(() => {
  // Explicit: this project does not run vitest with `globals`, so the automatic
  // unmount testing-library installs is never registered. Without it, every test
  // after the first queries a body still holding the previous test's link.
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  forgetAskedPages();
});

function draw(href: string | undefined) {
  return render(
    <QueryClientProvider client={client}>
      <Link href={href} />
    </QueryClientProvider>,
  );
}

describe('reading ahead on intent', () => {
  it('asks for a kind the pointer has rested on', async () => {
    const view = draw('/c/prod/pods');

    fireEvent.pointerOver(view.getByRole('link'));
    expect(fetches).toEqual([]);

    await vi.advanceTimersByTimeAsync(200);
    expect(fetches).toHaveLength(1);
    expect(fetches[0]).toContain('/api/c/prod/r/pods');
  });

  // The whole reason for the delay. Sweeping a pointer down a menu of dozens of
  // kinds to reach the bottom one crosses every entry above it, and firing on
  // the pointer arriving would ask the cluster for all of them.
  it('costs nothing at all when the pointer only passes over', async () => {
    const view = draw('/c/prod/pods');

    const link = view.getByRole('link');
    fireEvent.pointerOver(link);
    fireEvent.pointerOut(link);
    await vi.advanceTimersByTimeAsync(500);

    expect(fetches).toEqual([]);
  });

  // A keyboard operator states the same intent as a pointer. A console that is
  // instant with a mouse and slow with a keyboard has chosen who it is fast for.
  it('treats focus as intent', async () => {
    const view = draw('/c/prod/pods');
    fireEvent.focus(view.getByRole('link'));

    await vi.advanceTimersByTimeAsync(200);
    expect(fetches).toHaveLength(1);
  });

  // `viewMemory.remembered` appends the sort an operator last used, so a menu
  // href carries a query string as often as it does not. The sort is client-side
  // ordering and is deliberately not in the query key: warming has to land on
  // the entry the page will read, or it is a second request rather than a saved
  // one.
  it('warms the same entry whether or not the href carries a remembered sort', async () => {
    const plain = draw('/c/prod/pods');
    fireEvent.pointerOver(plain.getByRole('link'));
    await vi.advanceTimersByTimeAsync(200);
    plain.unmount();

    const sorted = draw('/c/prod/pods?sort=age');
    fireEvent.pointerOver(sorted.getByRole('link'));
    await vi.advanceTimersByTimeAsync(200);
    sorted.unmount();

    // One cache entry, and the assertion is deliberately on the cache rather
    // than on the number of requests. Counting requests would measure the test
    // client's `staleTime` - the first draft did, and failed against a client
    // configured differently from the application's while the behavior under
    // test was correct. Two hrefs landing on one entry is the claim.
    expect(client.getQueryCache().findAll({ queryKey: ['resources', 'prod', 'pods'] })).toHaveLength(
      1,
    );
  });

  // A menu href carries the namespace chosen on the
  // last list, and the namespace *is* in the key. Warmed without it, the entry
  // is "All namespaces" and the page that arrives at `?namespace=default` reads
  // none of it - a prefetch that costs a request and saves nothing.
  it('warms the namespace a carried href names, as the page will ask for it', async () => {
    const view = draw('/c/prod/configmaps?namespace=kube-system&namespace=default');
    fireEvent.pointerOver(view.getByRole('link'));
    await vi.advanceTimersByTimeAsync(200);

    expect(fetches).toHaveLength(1);
    expect(fetches[0]).toContain('namespace=default');
    expect(fetches[0]).toContain('namespace=kube-system');
    // Keyed as `ResourceList` keys it: the selection sorted and joined.
    expect(
      client
        .getQueryCache()
        .findAll({ queryKey: ['resources', 'prod', 'configmaps', 'default,kube-system'] }),
    ).toHaveLength(1);
  });

  it('asks for the overview when the path is a cluster', async () => {
    const view = draw('/c/prod');

    fireEvent.pointerOver(view.getByRole('link'));
    await vi.advanceTimersByTimeAsync(200);

    expect(fetches).toHaveLength(1);
    expect(fetches[0]).toContain('/api/c/prod/overview');
  });

  // An optimization that throws is worse than one that does not apply.
  it('warms nothing, and does not throw, for a path it has no query for', async () => {
    const view = draw('/c/prod/pods/api-1/logs');

    fireEvent.pointerOver(view.getByRole('link'));
    await vi.advanceTimersByTimeAsync(500);

    expect(fetches).toEqual([]);
  });

  it('warms nothing for a link that has no address', async () => {
    const view = draw(undefined);

    fireEvent.pointerOver(view.getByRole('link'));
    await vi.advanceTimersByTimeAsync(500);

    expect(fetches).toEqual([]);
  });

  // A menu group that closes while the pointer rests in it.
  it('sends nothing when the link goes away before the delay elapses', async () => {
    const view = draw('/c/prod/pods');

    fireEvent.pointerOver(view.getByRole('link'));
    view.unmount();
    await vi.advanceTimersByTimeAsync(500);

    expect(fetches).toEqual([]);
  });
});

/**
 * The chunk, warmed on the same intent as the data.
 *
 * Every page is a `lazy` route, so the first visit to a page has its code to
 * fetch as well as its rows, and a hover that warmed only the query would land
 * on a page whose chunk has not arrived. The assertions are on what was *asked
 * for*, because a dynamic import cannot be observed from jsdom any more than a
 * spinner can prove a warm cache.
 */
describe('warming the chunk of a page on intent', () => {
  it('asks for the chunk of a page that has no query here', async () => {
    // A detail route: nothing to prefetch for the data, still a chunk to fetch.
    const view = draw('/c/prod/pods/default/api-1');

    fireEvent.pointerOver(view.getByRole('link'));
    expect(askedPages()).toEqual([]);

    await vi.advanceTimersByTimeAsync(200);
    expect(askedPages()).toEqual(['object']);
    expect(fetches).toEqual([]);
  });

  it('asks for the chunk and the data together for a kind', async () => {
    const view = draw('/c/prod/pods?sort=age');

    fireEvent.pointerOver(view.getByRole('link'));
    await vi.advanceTimersByTimeAsync(200);

    expect(askedPages()).toEqual(['resourceList']);
    expect(fetches).toHaveLength(1);
  });

  it('asks for nothing when the pointer only passes over', async () => {
    const view = draw('/c/prod/helm');

    const link = view.getByRole('link');
    fireEvent.pointerOver(link);
    fireEvent.pointerOut(link);
    await vi.advanceTimersByTimeAsync(500);

    expect(askedPages()).toEqual([]);
  });

  it('asks for no chunk for the sign-in view, which is not lazy', async () => {
    const view = draw('/login');

    fireEvent.pointerOver(view.getByRole('link'));
    await vi.advanceTimersByTimeAsync(200);

    expect(askedPages()).toEqual([]);
  });
});

/** The address-to-chunk table, one row per shape `routes.tsx` declares. Read
 *  the two together when either changes. */
describe('which chunk an address needs', () => {
  it.each([
    ['/settings', 'settings'],
    ['/settings/general', 'settings'],
    ['/c/prod', 'overview'],
    ['/c/prod/helm', 'helmList'],
    ['/c/prod/helm/default/api', 'helmRelease'],
    ['/c/prod/port-forwards', 'forwards'],
    ['/c/prod/pods', 'resourceList'],
    ['/c/prod/nodes/node-1', 'object'],
    ['/c/prod/pods/default/api-1', 'object'],
    ['/c/my%20cluster/pods', 'resourceList'],
  ] as const)('%s needs %s', (pathname, key) => {
    expect(pageFor(pathname)).toBe(key);
  });

  it.each([
    '/',
    '/login',
    // A redirect to `/`: no page of its own to warm.
    '/health',
    '/settings/general/more',
    '/c',
    '/c/prod/helm/only-ns',
    // One page, no objects under it: a forward is not a kind.
    '/c/prod/port-forwards/argo/api-0',
    '/c/prod/pods/default/api-1/logs',
    // An extra's page: its chunk is the extra's own, mounted through
    // `extension.routes`, and the object page is the wrong download for it.
    '/c/prod/x/security',
    '/c/prod/x/security/deep',
  ])('%s needs nothing here', (pathname) => {
    expect(pageFor(pathname)).toBeUndefined();
  });
});
