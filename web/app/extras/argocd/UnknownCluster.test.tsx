import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider, type RouteObject } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import ArgoTab, { ResourceWhy } from './ArgoTab';
import { argocd, PAGE_PATH } from './index';

/**
 * A cluster the console does not know, on every Argo CD surface that reads
 * one thing: the Application page, the Argo CD tab, and the difference under
 * a resource's row. Each route answers 404 `cluster_unknown` with the
 * console's sentence (`extension.UnknownClusterText`); the console's gate,
 * which refuses the name before the route runs, answers it as the console's
 * API does (404 `not_found`, the sentence as `detail`). Each surface draws
 * either as the Delivery Center does: one notice, the sentence once, no failure,
 * no alert, no "Try again".
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const CLUSTER = 'gone-cluster';

// What the routes write for a name not in the kubeconfig (`unknownCluster`,
// `extras/argocd/delivery.go`): the sentence, and the Go error as detail.
const SENTENCE =
  `There is no cluster called "${CLUSTER}" in the kubeconfig this KubeTower reads. A link or bookmark may name a ` +
  'context that has since been removed or renamed, or the kubeconfig changed while this page was open: go back to ' +
  'the console to see the clusters it serves now.';
const UNKNOWN = {
  code: 'cluster_unknown',
  message: SENTENCE,
  detail: `kubetower: unknown cluster: "${CLUSTER}" is not a context in the kubeconfig`,
};
// What the console's gate writes for it (`api.UnknownCluster`), before any route.
const GATE = { type: 'about:blank', title: 'Not found', status: 404, code: 'not_found', detail: SENTENCE };

const ANSWERS = [
  ['the route', UNKNOWN, 'application/json; charset=utf-8'],
  ["the console's gate", GATE, 'application/problem+json'],
] as const;
let answer: { body: unknown; contentType: string } = { body: UNKNOWN, contentType: ANSWERS[0][2] };

const APP = `/x/${CLUSTER}/argocd/applications/argocd/guestbook`;
const RESOURCE = `/x/${CLUSTER}/argocd/resource?kind=deployments&name=guestbook-ui&namespace=guestbook`;

/** Every address in `urls` answers the unknown cluster; anything else is a 599. */
function stub(...urls: string[]): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    if (!urls.includes(url)) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    return Promise.resolve(new Response(JSON.stringify(answer.body), { status: 404, headers: { 'Content-Type': answer.contentType } }));
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(routes: RouteObject[], at: string): void {
  const router = createMemoryRouter(routes, { initialEntries: [at] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

function alone(element: ReactNode): RouteObject[] {
  return [{ path: '/', element: <Suspense>{element}</Suspense> }];
}

/** One answer: one notice, `absent`, about the cluster, the sentence once. */
async function expectOneAnswer(fetch: ReturnType<typeof vi.fn>, url: string): Promise<void> {
  const notice = (await screen.findByText('No such cluster')).closest('[data-state]') as HTMLElement;
  expect(fetch.mock.calls.map(([input]) => String(input))).toContain(url);
  expect(notice.getAttribute('data-state')).toBe('absent');
  expect(notice.closest('[data-refusal]')?.getAttribute('data-refusal')).toBe('cluster_unknown');
  expect(document.querySelectorAll('[data-state]')).toHaveLength(1);
  // The sentence once on the whole page, and not the Go error that restates it.
  expect(document.body.textContent?.split(SENTENCE).length).toBe(2);
  expect(document.body.textContent).not.toContain('is not a context in the kubeconfig');
  expect(screen.queryByText(/The request failed/)).toBeNull();
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
}

describe.each(ANSWERS)('a cluster the console does not know, answered by %s', (_, body, contentType) => {
  beforeEach(() => {
    answer = { body, contentType };
  });

  it("on an Application's page, is one answer with no failure and no retry", async () => {
    const fetch = stub(APP);
    mount([{ path: 'c/:cluster', children: argocd.routes }], `/c/${CLUSTER}/${PAGE_PATH}/applications/argocd/guestbook`);
    await expectOneAnswer(fetch, APP);
    // The page's own Refresh stays: it is how a reader asks again once the
    // kubeconfig has changed.
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeTruthy();
  });

  it('in the Argo CD tab, is one answer with no failure and no retry', async () => {
    const fetch = stub(RESOURCE);
    mount(alone(<ArgoTab cluster={CLUSTER} kind="deployments" namespace="guestbook" name="guestbook-ui" />), '/');
    await expectOneAnswer(fetch, RESOURCE);
    expect(document.querySelector('[data-argo-tab]')).toBeNull();
  });

  it("under a resource's row, is one answer with no failure and no retry", async () => {
    const fetch = stub(RESOURCE);
    mount(alone(<ResourceWhy cluster={CLUSTER} kind="deployments" namespace="guestbook" name="guestbook-ui" />), '/');
    await expectOneAnswer(fetch, RESOURCE);
    expect(document.querySelector('[data-why]')).toBeNull();
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
  });
});
