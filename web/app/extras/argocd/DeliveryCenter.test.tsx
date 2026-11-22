import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { argocd, PAGE_PATH } from './index';
import { grantManifest, INSTALL_ARGOCD } from './NextSteps';

// The stubbed `fetch` goes with the test that stubbed it. The extra's own
// setup file did this for every test before the extras came into the core;
// the core's (`ui/src/testing/setup.ts`) unmounts but restores no global.
afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * The page, mounted the way the console mounts it: the extension's routes as
 * children of `/c/:cluster` (the core's `router/routes.tsx`), the route's own
 * `lazy`, and a fetch stub standing in for `delivery.go`.
 *
 * Every body below is the shape the Go route writes, and the first one is the
 * answer `cmd/devserve` gave on docker-desktop, verbatim.
 */

interface Reply {
  status?: number;
  contentType?: string;
  body?: unknown;
}

const PATH = '/x/docker-desktop/argocd/';
const FLUX = '/x/docker-desktop/argocd/flux';

/**
 * `reply` answers Argo CD's list. Flux CD's route is left pending unless
 * `flux` answers it, so a test about one engine reads that engine alone.
 */
function stub(reply: Reply | (() => Promise<Response>), flux?: Reply): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    if (url === FLUX) {
      if (flux === undefined) return new Promise<Response>(() => undefined);
      return Promise.resolve(
        new Response(JSON.stringify(flux.body), {
          status: flux.status ?? 200,
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
        }),
      );
    }
    if (typeof reply === 'function') return reply();
    if (url !== PATH) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    const body = typeof reply.body === 'string' ? reply.body : JSON.stringify(reply.body);
    return Promise.resolve(
      new Response(body, {
        status: reply.status ?? 200,
        headers: { 'Content-Type': reply.contentType ?? 'application/json; charset=utf-8' },
      }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(search = ''): void {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: argocd.routes }], {
    initialEntries: [`/c/docker-desktop/${PAGE_PATH}${search}`],
  });
  // No retries: the console's client retries a 5xx twice, which is its policy
  // and not this page's subject.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const refused = (code: string, status: number, message: string): Reply => ({
  status,
  body: { code, message, detail: `looking up "applications.argoproj.io": ${code}` },
});

const NOT_INSTALLED = refused(
  'kind_unknown',
  404,
  'This cluster serves no applications.argoproj.io, so Argo CD is not installed on it.',
);
const NOT_SERVED = refused(
  'kind_not_served',
  404,
  "Argo CD's custom resource definition is present on this cluster and no version of it is served, so nothing can be read.",
);
const SA = 'system:serviceaccount:kubetower:kubetower';

/** What docker-desktop answered to the console in the cluster,
 *  with the chart's `rbac.customResources` off. */
const REFUSED: Reply = {
  status: 403,
  body: {
    code: 'list_forbidden',
    message: 'Argo CD is installed on this cluster, and the account this console read it with may not list its Applications.',
    detail: `applications.argoproj.io is forbidden: User "${SA}" cannot list resource "applications" in API group "argoproj.io" at the cluster scope`,
    subject: SA,
  },
};

const AMBIGUOUS = refused(
  'kind_ambiguous',
  409,
  'Two API groups on this cluster define the same plural, so applications.argoproj.io does not name one kind.',
);

/** The Applications' tiles under their projects, in the order drawn, by
 *  `namespace/name`. Needs attention repeats some of them above: `attention`. */
function tiles(): string[] {
  const all = [...document.querySelectorAll('[data-argo-attention] [data-app], [data-argo-project] [data-app]')];
  return [...new Set(all.map((t) => t.getAttribute('data-app') ?? ''))];
}

/** The tiles under Needs attention, in the order drawn. */
function attention(): string[] {
  return [...document.querySelectorAll('[data-argo-attention] [data-app]')].map((t) => t.getAttribute('data-app') ?? '');
}

function tile(app: string): HTMLElement {
  const found =
    document.querySelector<HTMLElement>(`[data-argo-project] [data-app="${app}"]`) ??
    document.querySelector<HTMLElement>(`[data-argo-attention] [data-app="${app}"]`);
  if (found === null) throw new Error(`no tile for ${app}`);
  return found;
}

/** Waits for the Applications to be drawn. */
async function applications(): Promise<void> {
  await waitFor(() => expect(tiles().length).toBeGreaterThan(0));
}

/** One filter tile's button, by its `data-filter`. */
function filter(value: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`[data-filter="${value}"] button`);
  if (found === null) throw new Error(`no filter ${value}`);
  return found;
}

/** A row of filter tiles, by its name, as each one reads. */
function filters(row: string): string[] {
  return within(screen.getByRole('list', { name: row }))
    .getAllByRole('button')
    .map((b) => b.textContent ?? '');
}

/** The page's own region: everything under the heading, as one string. */
async function drawn(): Promise<string> {
  await screen.findByRole('heading', { name: 'Delivery Center' });
  return document.body.textContent ?? '';
}

describe('the Delivery Center', () => {
  it('lives at /c/:cluster/x/argocd and reads the Go route, not its own address', async () => {
    const fetch = stub({ body: { applications: [], complete: true } });
    mount();
    // The answer, not the heading: the heading is drawn before the query has
    // necessarily been sent, and asserting on the call then is a race.
    await screen.findByText(/lists no Application/);
    expect(argocd.key).toBe('argocd');
    expect(argocd.nav.path).toBe('x/argocd');
    expect(fetch).toHaveBeenCalled();
    expect(fetch.mock.calls.map(([input]) => String(input))).toContain(PATH);
  });

  it('draws one tile per Application, worst first, health and sync as two chips', async () => {
    stub({
      body: {
        applications: [
          { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false, project: 'default', destination: { server: 'https://kubernetes.default.svc', namespace: 'guestbook' } },
          { namespace: 'apps', name: 'cilium', sync: 'OutOfSync', health: 'Degraded', deleting: false, project: 'infra', destination: { name: 'prod-eu', namespace: 'kube-system' } },
          { namespace: 'apps', name: 'fresh', sync: '', health: '', deleting: false },
          { namespace: 'apps', name: 'broken', sync: 'Unknown', health: 'Healthy', deleting: false },
          { namespace: 'apps', name: 'odd', sync: 'SomethingNew', health: 'Healthy', deleting: false },
          { namespace: 'apps', name: 'rolling', sync: 'Synced', health: 'Progressing', deleting: false },
          { namespace: 'apps', name: 'billing', sync: 'OutOfSync', health: 'Healthy', deleting: false, destination: { server: 'https://10.0.0.9:6443', namespace: 'shop' } },
        ],
        complete: true,
      },
    });
    mount();

    await applications();
    // One tile per Application, by project, worst project first, and worst
    // first inside one: Degraded, then out of sync, then what cannot be told
    // (by sync word), then in progress, then fine.
    expect(tiles()).toEqual(['apps/cilium', 'apps/billing', 'apps/broken', 'apps/fresh', 'apps/odd', 'apps/rolling', 'argocd/guestbook']);
    expect([...document.querySelectorAll('[data-argo-project]')].map((p) => p.getAttribute('data-argo-project'))).toEqual([
      'infra',
      '(no project)',
      'default',
    ]);
    // Needs attention: the bad and the warn ones, again, worst first.
    expect(attention()).toEqual(['apps/cilium', 'apps/billing']);
    expect(tile('apps/cilium').dataset['tone']).toBe('bad');
    expect(tile('apps/billing').dataset['tone']).toBe('warn');
    expect(tile('apps/broken').dataset['tone']).toBe('off');
    expect(tile('apps/rolling').dataset['tone']).toBe('info');
    expect(tile('argocd/guestbook').dataset['tone']).toBe('ok');

    // The whole tile is one link to the Application's page.
    const open = within(tile('apps/cilium')).getByRole('link', { name: 'Open Application cilium' });
    expect(open.getAttribute('href')).toBe('/c/docker-desktop/x/argocd/applications/apps/cilium');
    expect(within(tile('apps/cilium')).getAllByRole('link')).toHaveLength(1);

    // Health then sync, in Argo's words, graded; an absent word is ours.
    const chips = (app: string) => [...tile(app).querySelectorAll('[data-pill]')].map((p) => `${p.textContent}:${p.getAttribute('data-tone')}`);
    expect(chips('apps/cilium')).toEqual(['Degraded:bad', 'Out of sync:warn']);
    expect(chips('argocd/guestbook')).toEqual(['Healthy:ok', 'Synced:ok']);
    expect(chips('apps/broken')).toEqual(['Healthy:ok', 'Unknown:muted']);
    expect(chips('apps/fresh')).toEqual(['not reported:unreported', 'not reported:unreported']);

    // One line: the destination, its cluster named only when it is not the
    // one Argo CD runs in; the project too, where no heading says it.
    const detail = (app: string) => tile(app).querySelector('[data-tile-detail]')?.textContent ?? null;
    expect(detail('argocd/guestbook')).toBe('guestbook');
    expect(detail('apps/cilium')).toBe('prod-eu/kube-system');
    expect(
      document.querySelector('[data-argo-attention] [data-app="apps/cilium"] [data-tile-detail]')?.textContent,
    ).toBe('infra · prod-eu/kube-system');
    expect(detail('apps/billing')).toBe('10.0.0.9:6443/shop');
    expect(detail('apps/fresh')).toBeNull();

    // The summary above it, as filters: all, then sync with out of sync and
    // synced always, then the health words present, worst first.
    expect(filters('Applications by sync')).toEqual(['All 7', 'Out of sync 2', 'Sync unknown 1', 'Sync not reported 1', 'Something new 1', 'Synced 2']);
    expect(filters('Applications by health')).toEqual(['Degraded 1', 'Health not reported 1', 'Progressing 1', 'Healthy 4']);

    expect(screen.queryByText('Incomplete list')).toBeNull();
    expect(document.querySelector('[data-refusal]')).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('says on the tile what is behind a word: the count out of sync, the error, the deletion', async () => {
    stub({
      body: {
        applications: [
          { namespace: 'argocd', name: 'guestbook', sync: 'OutOfSync', health: 'Healthy', deleting: false, outOfSyncCount: 12 },
          {
            namespace: 'argocd',
            name: 'demo-broken-path',
            sync: 'Unknown',
            health: 'Healthy',
            deleting: false,
            outOfSyncCount: 0,
            condition: { type: 'ComparisonError', message: 'app path does not exist', error: true },
          },
          { namespace: 'argocd', name: 'wedged', sync: 'Synced', health: 'Healthy', deleting: true },
        ],
        complete: true,
      },
    });
    mount();

    await applications();
    expect(within(tile('argocd/guestbook')).getByText('Out of sync').getAttribute('title')).toBe('12 resources out of sync');

    // The error condition: one short chip, its sentence on hover, and the
    // tile graded by it rather than by the Healthy beside it.
    const broken = tile('argocd/demo-broken-path');
    const condition = broken.querySelector('[data-condition]');
    expect(condition?.getAttribute('data-condition')).toBe('ComparisonError');
    expect(condition?.textContent).toBe('Error');
    expect(condition?.getAttribute('title')).toBe('ComparisonError: app path does not exist');
    expect(broken.dataset['tone']).toBe('bad');

    // Being deleted is said beside the health it contradicts, and keeps it.
    const wedged = tile('argocd/wedged');
    expect(wedged.querySelectorAll('[data-deleting="true"]')).toHaveLength(1);
    expect(wedged.querySelector('[data-tile-detail]')).toBeNull();
    expect([...wedged.querySelectorAll('[data-pill]')].map((p) => p.textContent)).toEqual(['Healthy', 'Synced', 'Deleting']);
    expect(wedged.dataset['tone']).toBe('warn');
    expect(document.querySelectorAll('[data-deleting="true"]')).toHaveLength(1);
    // One project, so no heading, and nothing drawn twice.
    expect(document.querySelector('[data-argo-project]')).toBeNull();
    expect(attention()).toEqual(['argocd/demo-broken-path', 'argocd/guestbook', 'argocd/wedged']);
    expect(filters('Applications by health')).toEqual(['Healthy 3', 'Deleting 1']);
    // Worst first.
    expect(tiles()).toEqual(['argocd/demo-broken-path', 'argocd/guestbook', 'argocd/wedged']);
  });

  it('narrows the tiles to a picked filter, in the address, and back through All', async () => {
    stub({
      body: {
        applications: [
          { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false },
          { namespace: 'apps', name: 'cilium', sync: 'OutOfSync', health: 'Degraded', deleting: false },
          { namespace: 'apps', name: 'billing', sync: 'OutOfSync', health: 'Healthy', deleting: false },
        ],
        complete: true,
      },
    });
    mount();

    await applications();
    const all = filter('all');
    const outOfSync = filter('argo.sync.OutOfSync');
    expect(all.getAttribute('aria-pressed')).toBe('true');
    expect(outOfSync.getAttribute('aria-pressed')).toBe('false');

    outOfSync.click();
    await waitFor(() => expect(tiles()).toEqual(['apps/cilium', 'apps/billing']));
    expect(filter('argo.sync.OutOfSync').getAttribute('aria-pressed')).toBe('true');
    expect(filter('all').getAttribute('aria-pressed')).toBe('false');
    // The counts still count everything: they are the page's answer, not the filter's.
    expect(filter('argo.sync.Synced').textContent).toBe('Synced 1');

    // Pressed again, it lets go.
    filter('argo.sync.OutOfSync').click();
    await waitFor(() => expect(tiles()).toHaveLength(3));

    filter('argo.health.Degraded').click();
    await waitFor(() => expect(tiles()).toEqual(['apps/cilium']));

    filter('all').click();
    await waitFor(() => expect(tiles()).toHaveLength(3));
    expect(filter('all').getAttribute('aria-pressed')).toBe('true');
  });

  it('keeps a picked status that has cleared drawn at 0, and All is the way back', async () => {
    // A link sent while something was Unknown, opened after it cleared.
    stub({
      body: {
        applications: [
          { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false },
          { namespace: 'apps', name: 'odd', sync: '', health: '', deleting: false },
        ],
        complete: true,
      },
    });
    mount('?status=argo.sync.Unknown');

    await screen.findByText(/None here matches the filters picked/);
    const unknown = filter('argo.sync.Unknown');
    expect(unknown.textContent).toBe('Sync unknown 0');
    expect(unknown.getAttribute('aria-pressed')).toBe('true');
    expect(tiles()).toEqual([]);
    expect(document.querySelector('[data-argo-project]')).toBeNull();

    filter('all').click();
    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(document.querySelector('[data-filter="argo.sync.Unknown"]')).toBeNull();
  });

  it('lets go of the status and the project at once from Show all', async () => {
    stub({
      body: {
        applications: [
          { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false, project: 'a' },
          { namespace: 'argocd', name: 'billing', sync: 'Synced', health: 'Healthy', deleting: false, project: 'b' },
        ],
        complete: true,
      },
    });
    mount('?status=argo.sync.OutOfSync&project=a');

    await screen.findByText(/None here matches the filters picked/);
    expect(tiles()).toEqual([]);
    (document.querySelector('[data-show-all]') as HTMLElement).click();
    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(filter('all').getAttribute('aria-pressed')).toBe('true');
    expect(document.querySelector('[data-project-filter="a"] button')?.getAttribute('aria-pressed')).toBe('false');
  });

  it('says all synced and healthy only when every one is, over the whole list', async () => {
    const synced = { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false };
    const summary = () => document.querySelector('[data-argo-summary]')?.textContent ?? '';

    stub({ body: { applications: [synced], complete: true } });
    mount();
    await applications();
    expect(document.querySelector('[data-all-well]')).not.toBeNull();
    cleanup();

    // Progressing needs no attention and is not healthy yet.
    stub({ body: { applications: [synced, { ...synced, name: 'rolling', health: 'Progressing' }], complete: true } });
    mount();
    await applications();
    expect(document.querySelector('[data-all-well]')).toBeNull();
    expect(summary()).toContain('none needs attention');
    cleanup();

    // Nor an Unknown sync, which needs no attention either.
    stub({ body: { applications: [synced, { ...synced, name: 'odd', sync: 'Unknown' }], complete: true } });
    mount();
    await applications();
    expect(document.querySelector('[data-all-well]')).toBeNull();
    cleanup();

    // Not all of them: no claim about all.
    stub({ body: { applications: [synced], complete: false, note: 'one namespace was not read' } });
    mount();
    await applications();
    expect(document.querySelector('[data-all-well]')).toBeNull();
  });

  it('draws an empty answer as empty, naming Argo CD as the one that answered', async () => {
    stub({ body: { applications: [], complete: true } });
    mount();

    const notice = await screen.findByText(/lists no Application this console may read/);
    expect(notice.closest('[data-state]')?.getAttribute('data-state')).toBe('empty');
    expect(document.querySelector('[data-argo-filters]')).toBeNull();
    expect(document.querySelector('[data-tile]')).toBeNull();
    expect(screen.queryByText('Incomplete list')).toBeNull();
  });

  it('says an incomplete list is incomplete, and still draws the rows it has', async () => {
    stub({
      body: {
        applications: [
          { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false },
        ],
        complete: false,
        note: 'The list stopped after 500 objects.',
      },
    });
    mount();

    const notice = await screen.findByText('Incomplete list');
    expect(notice.closest('[data-complete]')?.textContent).toContain('The list stopped after 500 objects.');
    expect(await screen.findByRole('link', { name: 'Open Application guestbook' })).toBeTruthy();
  });

  it('says so even when the server gives no reason', async () => {
    stub({
      body: {
        applications: [
          { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false },
        ],
        complete: false,
      },
    });
    mount();

    const notice = await screen.findByText('Incomplete list');
    expect(notice.closest('[data-complete]')?.textContent).toContain('did not say why');
  });

  it('draws an incomplete list with no rows as unreadable, never as empty', async () => {
    stub({ body: { applications: [], complete: false, note: 'The read timed out after 30s.' } });
    mount();

    const message = await screen.findByText(/not an answer about the cluster/);
    const notice = message.closest('[data-state]');
    // The state, not the wording: `data-state="empty"` is the claim that the
    // cluster has no Applications, and nothing was read.
    expect(notice?.getAttribute('data-state')).toBe('unreadable');
    expect(notice?.textContent).toContain('The read timed out after 30s.');
    expect(document.querySelector('[data-state="empty"]')).toBeNull();
    expect(screen.queryByText(/lists no Application/)).toBeNull();
    // Asking again can differ, so the notice offers it.
    expect(within(notice as HTMLElement).getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it('draws "Argo CD is not installed" as absent, never as an empty list', async () => {
    stub(NOT_INSTALLED);
    mount();

    const title = await screen.findByText('Not installed');
    const notice = title.closest('[data-state]');
    expect(notice?.getAttribute('data-state')).toBe('absent');
    expect(notice?.textContent).toContain('Argo CD is not installed on it');
    expect(notice?.textContent).toContain('cluster docker-desktop · applications.argoproj.io · verb list');
    expect(document.querySelector('[data-refusal]')?.getAttribute('data-refusal')).toBe('kind_unknown');
    expect(document.querySelector('[data-state="empty"]')).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
    // Asking again changes nothing until somebody changes the cluster.
    expect(within(notice as HTMLElement).queryByRole('button', { name: 'Try again' })).toBeNull();
    // What to do about it, in the page: Argo CD's own install, to copy.
    const next = document.querySelector('[data-refusal="kind_unknown"]') as HTMLElement;
    expect(within(next).getByRole('heading', { name: 'This page lists Argo CD Applications' })).toBeTruthy();
    expect(next.querySelector('pre')?.textContent).toBe(INSTALL_ARGOCD);
    expect(within(next).getByRole('button', { name: 'Copy the install command' })).toBeTruthy();
  });

  it('draws a refused list as Argo CD present and unreadable, with how to grant the read and no retry', async () => {
    stub(REFUSED);
    mount();

    const title = await screen.findByText('Could not be read');
    const notice = title.closest('[data-state]') as HTMLElement;
    expect(notice.getAttribute('data-state')).toBe('unreadable');
    expect(notice.textContent).toContain('Argo CD is installed on this cluster');
    expect(within(notice).queryByRole('button', { name: 'Try again' })).toBeNull();
    expect(screen.queryByText(/The request failed/)).toBeNull();
    expect(screen.queryByText('Not installed')).toBeNull();
    // Said, never drawn as zero tiles: no filters, no grid, no "0 Out of sync".
    expect(document.querySelector('[data-argo-filters]')).toBeNull();
    expect(document.querySelector('[data-tile]')).toBeNull();
    expect(screen.queryByText(/None here matches the filters picked/)).toBeNull();

    const next = document.querySelector('[data-refusal="list_forbidden"]') as HTMLElement;
    expect(next.textContent).toContain(`${SA}, the ServiceAccount this console runs as`);
    expect(next.textContent).toContain('rbac.customResources: true');
    const manifest = next.querySelector('pre')?.textContent ?? '';
    expect(manifest).toBe(grantManifest(SA));
    expect(manifest).toContain('kind: ServiceAccount\n    name: kubetower\n    namespace: kubetower');
    expect(manifest).toContain('verbs: [get, list]');
  });

  it('draws a list refused for the person as their refusal, not the console\'s, with no retry', async () => {
    stub({
      status: 403,
      body: {
        code: 'list_forbidden',
        message: 'Argo CD is installed on this cluster, and you may not list its Applications.',
        detail: 'the cluster says you may not list applications.argoproj.io',
      },
    });
    mount();

    const title = await screen.findByText('Could not be read');
    const notice = title.closest('[data-state]') as HTMLElement;
    expect(notice.getAttribute('data-state')).toBe('unreadable');
    expect(notice.textContent).toContain('you may not list its Applications');
    expect(within(notice).queryByRole('button', { name: 'Try again' })).toBeNull();

    const next = document.querySelector('[data-refusal="list_forbidden"]') as HTMLElement;
    expect(next.textContent).toContain('Argo CD is here; the read was refused');
    expect(next.textContent).toContain('refused this read for you');
    expect(next.textContent).toContain('grants your account the read');
    expect(next.textContent).not.toContain('the console may not read it');
    expect(next.textContent).not.toContain('ServiceAccount this console runs as');
    expect(next.querySelector('pre')?.textContent).toContain('kind: User');
  });

  it("grants a refused person as a User, never as the console's ServiceAccount", () => {
    const manifest = grantManifest('alice@example.com');
    expect(manifest).toContain('kind: User');
    expect(manifest).toContain('name: alice@example.com');
    expect(manifest).not.toContain('ServiceAccount');
  });

  it('draws "present and not served" as unreadable, with its own sentence and next step', async () => {
    stub(NOT_SERVED);
    mount();

    const title = await screen.findByText('Could not be read');
    const notice = title.closest('[data-state]');
    expect(notice?.getAttribute('data-state')).toBe('unreadable');
    expect(notice?.textContent).toContain('no version of it is served');
    const next = document.querySelector('[data-refusal]');
    expect(next?.getAttribute('data-refusal')).toBe('kind_not_served');
    expect(next?.textContent).toContain('served: false');
  });

  it('draws "two groups define the plural" as unreadable, with its own sentence and next step', async () => {
    stub(AMBIGUOUS);
    mount();

    const title = await screen.findByText('Could not be read');
    const notice = title.closest('[data-state]');
    expect(notice?.getAttribute('data-state')).toBe('unreadable');
    expect(notice?.textContent).toContain('does not name one kind');
    const next = document.querySelector('[data-refusal]');
    expect(next?.getAttribute('data-refusal')).toBe('kind_ambiguous');
    expect(next?.textContent).toContain('two definitions');
  });

  it('draws the four refusals as four different pages', async () => {
    const pages: string[] = [];
    for (const reply of [NOT_INSTALLED, NOT_SERVED, AMBIGUOUS, REFUSED]) {
      stub(reply);
      mount();
      await screen.findByText(/Not installed|Could not be read/);
      pages.push(await drawn());
      // Unmounted, not wiped: see ui/src/testing/setup.ts.
      cleanup();
    }
    expect(new Set(pages).size).toBe(4);
  });

  // What `delivery.go` writes for a name not in the kubeconfig
  // (extension.UnknownClusterText), on both engines' routes; and what the
  // console's gate writes for it before either route runs (`api.UnknownCluster`).
  const unknownSentence =
    'There is no cluster called "docker-desktop" in the kubeconfig this KubeTower reads. A link or bookmark may name a ' +
    'context that has since been removed or renamed, or the kubeconfig changed while this page was open: go back to ' +
    'the console to see the clusters it serves now.';
  it.each([
    ['the route', { code: 'cluster_unknown', message: unknownSentence, detail: 'kubetower: unknown cluster: "docker-desktop" is not a context in the kubeconfig' }],
    ["the console's gate", { type: 'about:blank', title: 'Not found', status: 404, code: 'not_found', detail: unknownSentence }],
  ])('draws a cluster the console does not know, answered by %s, as one answer, its sentence once, with no failure and no retry', async (_, body) => {
    const sentence = unknownSentence;
    const unknown: Reply = { status: 404, body };
    const fetch = stub(unknown, unknown);
    mount();

    const notice = (await screen.findByText('No such cluster')).closest('[data-state]') as HTMLElement;
    expect(notice.getAttribute('data-state')).toBe('absent');
    await waitFor(() => {
      expect(fetch.mock.calls.map(([input]) => String(input))).toContain(FLUX);
    });
    // The sentence once on the whole page: not twice in one notice (the
    // detail restates it), not once per engine.
    expect(document.body.textContent?.split(sentence).length).toBe(2);
    expect(document.body.textContent).not.toContain('is not a context in the kubeconfig');
    expect(screen.queryByText(/The request failed/)).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
    expect(document.querySelectorAll('[data-state]')).toHaveLength(1);
    expect(document.querySelector('[data-refusal]')?.getAttribute('data-refusal')).toBe('cluster_unknown');
    // Neither engine is drawn, nor suggested for install.
    expect(screen.queryByRole('heading', { name: 'Argo CD' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Flux CD' })).toBeNull();
    expect(document.querySelector('[data-engine-absent]')).toBeNull();
    // Nothing more is asked of a cluster that does not exist.
    expect(fetch.mock.calls.map(([input]) => String(input)).some((url) => url.endsWith('/report'))).toBe(false);
  });

  it('draws a failure to ask as a failure, with the route\'s own sentence, and never as a state', async () => {
    stub({
      status: 502,
      body: { code: 'list_failed', message: 'The Applications could not be read.', detail: 'connection refused' },
    });
    mount();

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The request failed (502)');
    expect(alert.textContent).toContain('The Applications could not be read. connection refused');
    expect(alert.textContent).toContain('code: list_failed');
    expect(document.querySelector('[data-state]')).toBeNull();
    expect(document.querySelector('[data-refusal]')).toBeNull();
  });

  it('draws the console\'s own 404 - the extra switched off here - as the gate refusing, not as "not installed"', async () => {
    // The gate answers with http.Error: plain text, no code
    // (`refuseExtraOff`, internal/web/extras.go in the core). An empty
    // body has no sentence of the gate's to show, so the page says its own.
    stub({
      status: 404,
      contentType: 'text/plain; charset=utf-8',
      body: '',
    });
    mount();

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The request failed (404)');
    expect(alert.textContent).toContain('not enabled for this cluster in this console');
    // Settings > Extra, on every profile, and in a cluster `use`.
    expect(alert.textContent).toContain('Settings > Extra');
    expect(alert.textContent).toContain('use on extras.kubetower.dev/argocd');
    expect(alert.textContent).not.toContain('not JSON');
    expect(screen.queryByText('Not installed')).toBeNull();
    expect(document.querySelector('[data-state]')).toBeNull();
  });

  it("shows the gate's own sentence, not the Settings switch, when the gate said why", async () => {
    // A switch locked by KT_EXTRAS: the Settings switch cannot help.
    stub({
      status: 404,
      contentType: 'text/plain; charset=utf-8',
      body:
        'Delivery Center is not enabled on this KubeTower: it is turned off by KT_EXTRAS=-argocd ' +
        'in the environment, which the console cannot change while it runs. ' +
        'Change KT_EXTRAS where the deployment sets it and restart KubeTower.\n',
    });
    mount();

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The request failed (404)');
    expect(alert.textContent).toContain('which the console cannot change while it runs');
    expect(alert.textContent).not.toContain('Turn it on in Settings');
    expect(screen.queryByText('Not installed')).toBeNull();
  });

  it('keeps any other answer that is not JSON a plain "not JSON" failure', async () => {
    stub({ status: 502, contentType: 'text/html', body: '<html>proxy error</html>' });
    mount();

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The answer was not JSON');
  });

  it('draws a console that could not be reached as a failure', async () => {
    stub(() => Promise.reject(new TypeError('Failed to fetch')));
    mount();

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The console could not be reached');
  });

  it('draws a 200 that is not the route\'s shape as a failure, never as an empty list', async () => {
    stub({ body: { items: [] } });
    mount();

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('answered out of contract');
    expect(document.querySelector('[data-state="empty"]')).toBeNull();
  });
});
