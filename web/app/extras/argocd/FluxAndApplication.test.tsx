import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor as settle, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { argocd, PAGE_PATH } from './index';
import { grantManifest, INSTALL_FLUX } from './NextSteps';

// The stubbed `fetch` goes with the test that stubbed it. The extra's own
// setup file did this for every test before the extras came into the core;
// the core's (`ui/src/testing/setup.ts`) unmounts but restores no global.
afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * Flux CD beside Argo CD and one Application's page, mounted the
 * way the console mounts them, with each Go route's body stubbed.
 */

type Replies = Record<string, { status?: number; body: unknown }>;

function stub(replies: Replies): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    const reply = replies[url];
    if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    return Promise.resolve(
      new Response(JSON.stringify(reply.body), {
        status: reply.status ?? 200,
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
      }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(path: string): void {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: argocd.routes }], {
    initialEntries: [`/c/docker-desktop/${path}`],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const ARGO = '/x/docker-desktop/argocd/';
const FLUX = '/x/docker-desktop/argocd/flux';
const SA = 'system:serviceaccount:kubetower:kubetower';

const argoRows = {
  body: { applications: [{ namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false }], complete: true },
};
const argoAbsent = {
  status: 404,
  body: { code: 'kind_unknown', message: 'This cluster serves no applications.argoproj.io, so Argo CD is not installed on it.' },
};
const fluxAbsent = {
  status: 404,
  body: { code: 'kind_unknown', message: 'This cluster serves neither Kustomizations nor HelmReleases, so Flux CD is not installed on it.' },
};

describe('Flux CD in the Delivery Center', () => {
  it("lists Kustomizations and HelmReleases in Flux's own words, beside Argo CD", async () => {
    stub({
      [ARGO]: argoRows,
      [FLUX]: {
        body: {
          objects: [
            { kind: 'Kustomization', namespace: 'flux-system', name: 'apps', ready: 'True', reason: 'ReconciliationSucceeded', reconciling: false, stalled: false, suspended: false, revision: 'main@sha1:abc', deleting: false },
            { kind: 'HelmRelease', namespace: 'podinfo', name: 'podinfo', ready: 'False', reason: 'InstallFailed', reconciling: false, stalled: true, suspended: true, deleting: false },
          ],
          complete: true,
        },
      },
    });
    mount(PAGE_PATH);
    const table = await screen.findByRole('table', { name: 'Flux CD Kustomizations and HelmReleases on this cluster' });
    expect(within(table).getByText('apps')).toBeTruthy();
    expect(within(table).getByText('True').getAttribute('data-tone')).toBe('ok');
    expect(within(table).getByText('False').getAttribute('data-tone')).toBe('bad');
    expect(table.querySelector('[data-suspended="true"]')).toBeTruthy();
    expect(table.querySelector('[data-stalled="true"]')).toBeTruthy();
    // Argo CD's tiles are still there, untouched by the second engine.
    expect(await screen.findByRole('link', { name: 'Open Application guestbook' })).toBeTruthy();
  });

  it('says nothing of an engine that is absent while the other one is there', async () => {
    stub({ [ARGO]: argoRows, [FLUX]: fluxAbsent });
    mount(PAGE_PATH);
    await screen.findByRole('link', { name: 'Open Application guestbook' });
    // Let Flux's answer land before asserting its absence from the page.
    await settle(() => expect(document.querySelector('[data-flux-refusal]')).toBeNull());
    expect(screen.queryByText(/Flux CD is not installed/)).toBeNull();
    expect(document.querySelector('[data-engine-absent]')).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Flux CD' })).toBeNull();
    expect(screen.queryByText(INSTALL_FLUX)).toBeNull();
  });

  it('says nothing of Argo CD when only Flux is there', async () => {
    stub({
      [ARGO]: argoAbsent,
      [FLUX]: {
        body: {
          objects: [{ kind: 'Kustomization', namespace: 'flux-system', name: 'apps', ready: 'True', reconciling: false, stalled: false, suspended: false, deleting: false }],
          complete: true,
        },
      },
    });
    mount(PAGE_PATH);
    await screen.findByRole('table', { name: 'Flux CD Kustomizations and HelmReleases on this cluster' });
    expect(screen.queryByText(/Argo CD is not installed/)).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Argo CD' })).toBeNull();
    expect(document.querySelector('[data-refusal]')).toBeNull();
  });

  it('draws one short state, and no install hints, when neither engine is there', async () => {
    stub({ [ARGO]: argoAbsent, [FLUX]: fluxAbsent });
    mount(PAGE_PATH);
    const state = await waitFor(() => document.querySelector('[data-neither]'));
    expect(state.textContent).toBe('No Argo CD or Flux on this cluster.');
    expect(document.querySelector('[data-flux-refusal]')).toBeNull();
    expect(document.querySelector('[data-refusal]')).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Argo CD' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Flux CD' })).toBeNull();
    expect(screen.queryByText(INSTALL_FLUX)).toBeNull();
  });

  it("draws Flux refused with Flux's own grant, never Argo CD's", async () => {
    stub({
      [ARGO]: argoRows,
      [FLUX]: { status: 403, body: { code: 'list_forbidden', message: 'Flux CD is installed on this cluster, and the account this console read it with may not list its kustomizations.', subject: SA } },
    });
    mount(PAGE_PATH);
    const next = await waitFor(() => document.querySelector('[data-flux-refusal="list_forbidden"]'));
    const manifest = next.querySelector('pre')?.textContent ?? '';
    expect(manifest).toBe(grantManifest(SA, 'flux'));
    expect(manifest).toContain('resources: [kustomizations, helmreleases]');
    expect(manifest).not.toContain('argoproj.io');
  });
});

describe('the overview', () => {
  const apps = {
    body: {
      applications: [
        { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false },
        { namespace: 'argocd', name: 'billing', sync: 'OutOfSync', health: 'Degraded', deleting: false },
        { namespace: 'argocd', name: 'old', sync: 'OutOfSync', health: 'Healthy', deleting: true },
      ],
      complete: true,
    },
  };

  const REPORT = '/x/docker-desktop/argocd/report';
  const tile = (label: string, count: number) => ({ label, key: label.toLowerCase().replace(/[^a-z0-9]/g, ''), count });
  const view = {
    collected: '2026-10-20T18:00:00Z',
    state: 'present',
    summary: '2 of 3 Applications need attention (not both Healthy and Synced)',
    headline: 'Degraded 1',
    headlineKey: 'degraded',
    allWell: false,
    sync: [tile('Synced', 1), tile('OutOfSync', 2), tile('Unknown', 0)],
    health: [tile('Healthy', 2), tile('Progressing', 0), tile('Degraded', 1), tile('Suspended', 0), tile('Missing', 0), tile('Unknown', 0)],
    areas: [
      { name: 'Applications', kind: 'applications.argoproj.io', count: 3, summary: '3 objects read' },
      { name: 'ApplicationSets', kind: 'applicationsets.argoproj.io', count: 0, summary: '0 objects read' },
      { name: 'AppProjects', kind: 'appprojects.argoproj.io', unavailable: 'appprojects.argoproj.io is forbidden', count: 0 },
    ],
    offenders: [
      {
        namespace: 'argocd', name: 'billing', project: 'default', projectNamespace: 'argocd',
        sync: 'OutOfSync', syncKey: 'outofsync', health: 'Degraded', healthKey: 'degraded', healthMessage: '0 of 1 pods available',
        since: [
          { of: 'health', value: '4d', hint: 'h', missing: false },
          { of: 'sync', value: 'not recorded by Argo', hint: 's', missing: true },
          { of: 'as of', value: '2m ago', hint: 'a', missing: false },
        ],
        source: 'billing', revision: '53e28ff', operation: 'Failed, by automated, 1h ago', operationTone: 'bad',
        conditions: [{ type: 'SyncError', error: true, message: 'admission webhook denied' }], reason: 'Degraded — broken now',
      },
    ],
    causes: [{ type: 'SyncError', error: true, message: 'admission webhook denied', failing: 1, objects: [{ namespace: 'argocd', name: 'billing' }] }],
    sets: [], setTotal: 0, setFailing: 0,
    projectsUnavailable: 'appprojects.argoproj.io is forbidden', projects: [], projectTotal: 0,
    notes: ["This page reads no Application's resource tree."],
  };

  const projects = {
    ...view,
    projectsUnavailable: undefined,
    projectTotal: 2,
    projects: [
      { namespace: 'argocd', name: 'default', applications: 2, offending: 2, description: 'The default project' },
      { namespace: 'argocd', name: 'idle', applications: 0, offending: 0 },
    ],
    setTotal: 3,
    setFailing: 1,
    sets: [{ namespace: 'argocd', name: 'clusters', type: 'ErrorOccurred', status: 'True', message: 'generator failed' }],
  };

  it('draws the projects and the failing ApplicationSets as tiles under the Applications, read with the page', async () => {
    const fetch = stub({ [ARGO]: apps, [FLUX]: fluxAbsent, [REPORT]: { body: projects } });
    mount(PAGE_PATH);
    const project = (await waitFor(() => document.querySelector('[data-project="default"]'))) as HTMLElement;
    expect(fetch.mock.calls.map((call) => String(call[0]))).toContain(REPORT);
    // No fold, no tables: the report's offenders and causes are not drawn.
    expect(document.querySelector('details[data-argo-more]')).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
    // After the Applications.
    const first = document.querySelector('[data-app]') as HTMLElement;
    expect(first.compareDocumentPosition(project) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    expect([...project.querySelectorAll('[data-pill]')].map((p) => p.textContent)).toEqual(['2 applications', '2 need attention']);
    expect(project.dataset['tone']).toBe('warn');
    expect(within(project).getByRole('link', { name: 'AppProject argocd/default' }).getAttribute('href')).toBe(
      '/c/docker-desktop/appprojects.argoproj.io/argocd/default',
    );
    expect(document.querySelector<HTMLElement>('[data-project="idle"]')?.dataset['tone']).toBe('off');

    const set = document.querySelector<HTMLElement>('[data-set="argocd/clusters"]');
    expect(set?.dataset['tone']).toBe('bad');
    expect(within(set as HTMLElement).getByRole('link', { name: 'Open ApplicationSet clusters' }).getAttribute('href')).toBe(
      '/c/docker-desktop/applicationsets.argoproj.io/argocd/clusters',
    );
    expect(screen.getByText('What this page did not read')).toBeTruthy();

    // A project tile filters the page to that project, in the address.
    within(project).getByRole('button', { name: 'Show project default only' }).click();
    await settle(() => expect(document.querySelector('[data-project-filter="default"] button')?.getAttribute('aria-pressed')).toBe('true'));
  });

  it('says a refused AppProjects read, never zero projects', async () => {
    stub({ [ARGO]: argoRows, [FLUX]: fluxAbsent, [REPORT]: { body: view } });
    mount(PAGE_PATH);
    expect(await screen.findByText('appprojects.argoproj.io is forbidden')).toBeTruthy();
    expect(screen.queryByText('No AppProject on this cluster.')).toBeNull();
    expect(screen.getByText('No ApplicationSet on this cluster.')).toBeTruthy();
  });

  it('searches every Application, keeps the words in the address, and narrows Flux too', async () => {
    stub({
      [ARGO]: {
        body: {
          applications: [
            { namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false, project: 'team-a', source: { repoURL: 'https://github.com/argoproj/argocd-example-apps.git', path: 'guestbook' } },
            { namespace: 'argocd', name: 'podinfo', sync: 'Synced', health: 'Healthy', deleting: false, project: 'platform', source: { repoURL: 'https://stefanprodan.github.io/podinfo', chart: 'podinfo', targetRevision: '6.7.1' } },
            { namespace: 'argocd', name: 'web', sync: 'OutOfSync', health: 'Healthy', deleting: false, project: 'team-a', destination: { namespace: 'shop' } },
          ],
          complete: true,
        },
      },
      [FLUX]: fluxAbsent,
      [REPORT]: { body: view },
    });
    mount(PAGE_PATH);
    await settle(() => expect(document.querySelectorAll('[data-argo-project] [data-app]')).toHaveLength(3));
    const box = screen.getByRole('searchbox', { name: 'Search applications' });
    const drawn = () => [
      ...new Set([...document.querySelectorAll('[data-app]')].map((t) => t.getAttribute('data-app'))),
    ];

    fireEvent.change(box, { target: { value: 'PODINFO 6.7' } });
    await settle(() => expect(drawn()).toEqual(['argocd/podinfo']));
    // The chart and its version on the tile.
    expect(document.querySelector('[data-app="argocd/podinfo"] [data-app-source]')?.textContent).toBe('podinfo 6.7.1');

    // A namespace, then a project: every word must be found.
    fireEvent.change(box, { target: { value: 'team-a shop' } });
    await settle(() => expect(drawn()).toEqual(['argocd/web']));
    // The counts count what the search leaves.
    expect(document.querySelector('[data-filter="all"]')?.textContent).toBe('All 1');

    fireEvent.change(box, { target: { value: 'nothing-like-this' } });
    // Nothing found by the search alone: said as such, and no Show all - the
    // box itself is the way back.
    await screen.findAllByText('Nothing here matches the search.');
    expect(document.querySelector('[data-show-all]')).toBeNull();
    fireEvent.change(box, { target: { value: '' } });
    await settle(() => expect(drawn()).toHaveLength(3));
  });
});

describe("one Application's page", () => {
  const APP = '/x/docker-desktop/argocd/applications/argocd/guestbook';
  const detail = {
    namespace: 'argocd',
    name: 'guestbook',
    project: 'default',
    deleting: false,
    source: { repoURL: 'https://github.com/argoproj/argocd-example-apps.git', path: 'guestbook', targetRevision: 'HEAD' },
    destination: { server: 'https://kubernetes.default.svc', namespace: 'guestbook' },
    sync: 'OutOfSync',
    syncRevision: '53e28ff20cc530b9ada2173fbbd64d48338583ba',
    health: 'Degraded',
    healthMessage: 'Deployment has 0 of 1 pods available',
    automatedSync: true,
    operation: {
      phase: 'Failed',
      message: 'one or more objects failed to apply',
      automated: true,
      results: [{ kind: 'Deployment', namespace: 'guestbook', name: 'guestbook-ui', status: 'SyncFailed', message: 'admission webhook denied' }],
    },
    history: [{ id: 2, revision: '53e28ff20cc530b9ada2173fbbd64d48338583ba', deployedAt: '2026-10-20T10:00:00Z', source: { path: 'guestbook' } }],
    conditions: [{ type: 'SyncError', message: 'one or more objects failed to apply', error: true }],
    resources: [
      { group: 'apps', version: 'v1', kind: 'Deployment', namespace: 'guestbook', name: 'guestbook-ui', status: 'OutOfSync', health: 'Degraded', requiresPruning: false, path: 'deployments' },
      { version: 'v1', kind: 'Service', namespace: 'guestbook', name: 'guestbook-ui', status: 'Synced', health: 'Healthy', requiresPruning: false, path: 'services' },
      { group: 'example.io', version: 'v1', kind: 'Gone', name: 'stale', status: 'OutOfSync', requiresPruning: true },
    ],
    resourcesNote: "Some resources' kinds are not served by this cluster's API, so those rows open nothing here.",
  };

  it('opens from its tile, the whole tile one link', async () => {
    stub({ [ARGO]: argoRows });
    mount(PAGE_PATH);
    const link = await screen.findByRole('link', { name: 'Open Application guestbook' });
    expect(link.textContent).toBe('guestbook');
    expect(link.closest('[data-app]')?.getAttribute('data-app')).toBe('argocd/guestbook');
    expect(link.getAttribute('href')).toBe('/c/docker-desktop/x/argocd/applications/argocd/guestbook');
  });

  it('opens on the resources, what is out of sync first, with the history and the last sync behind tabs', async () => {
    stub({ [APP]: { body: detail } });
    mount(`${PAGE_PATH}/applications/argocd/guestbook`);

    const resources = await screen.findByRole('table', { name: 'Resources this Application manages' });
    // The head: the two words, the facts in one row, the error condition.
    const head = document.querySelector('[data-app-head]') as HTMLElement;
    expect([...head.querySelectorAll('[data-pill]')].map((p) => p.textContent)).toEqual(['Degraded', 'Out of sync']);
    expect(head.querySelector('[data-app-facts]')?.textContent).toContain('auto-sync');
    expect(head.querySelector('[data-last-sync="Failed"]')?.textContent).toContain('sync failed');
    expect(head.querySelector('[data-condition="SyncError"]')).toBeTruthy();

    // Out of sync first, each with its reason in a word.
    const rows = [...document.querySelectorAll('[data-outofsync-row]')];
    expect(rows.map((r) => r.getAttribute('data-outofsync-row'))).toEqual(['Deployment/guestbook/guestbook-ui', 'Gone/stale']);
    expect(rows.map((r) => r.querySelector('[data-reason]')?.textContent)).toEqual([
      'differs from git',
      'not in git - would be pruned',
    ]);

    // The table: the OutOfSync ones first; each opens in the console, an
    // OutOfSync one on its Argo CD tab.
    const opened = within(resources)
      .getAllByRole('link', { name: 'guestbook/guestbook-ui' })
      .map((a) => a.getAttribute('href'));
    expect(opened).toEqual([
      '/c/docker-desktop/deployments/guestbook/guestbook-ui?tab=x.argocd.sync',
      '/c/docker-desktop/services/guestbook/guestbook-ui',
    ]);
    const manifests = within(resources).getAllByRole('link', { name: 'Manifest' }).map((a) => a.getAttribute('href'));
    expect(manifests).toContain('/c/docker-desktop/services/guestbook/guestbook-ui?tab=manifest');
    expect(within(resources).getByText('stale')).toBeTruthy();
    expect(screen.getByText(/so those rows open nothing here/)).toBeTruthy();
    expect(within(resources).getByText('prune')).toBeTruthy();

    // History and the last sync are not drawn until asked for.
    expect(screen.queryByRole('table', { name: 'Deployments of this Application, newest first' })).toBeNull();
    expect(document.querySelector('[data-operation]')).toBeNull();
    screen.getByRole('tab', { name: /History/ }).click();
    const history = await screen.findByRole('table', { name: 'Deployments of this Application, newest first' });
    expect(within(history).getByText('53e28ff')).toBeTruthy();
    screen.getByRole('tab', { name: /Last sync/ }).click();
    await settle(() => expect(document.querySelector('[data-operation="Failed"]')?.textContent).toContain('by the sync policy'));
    // Read-only: nothing on the page offers to sync.
    expect(screen.queryByRole('button', { name: /^sync/i })).toBeNull();
  });

  it('opens why a resource is out of sync under its row: the diff, or missing from the cluster', async () => {
    const DEPLOYMENT = '/x/docker-desktop/argocd/resource?kind=deployments&name=guestbook-ui&namespace=guestbook';
    const MISSING = '/x/docker-desktop/argocd/resource?kind=configmaps&name=settings&namespace=guestbook';
    const fetch = stub({
      [APP]: {
        body: {
          ...detail,
          resources: [
            ...detail.resources,
            { version: 'v1', kind: 'ConfigMap', namespace: 'guestbook', name: 'settings', status: 'OutOfSync', requiresPruning: false, path: 'configmaps' },
          ],
        },
      },
      [DEPLOYMENT]: {
        body: {
          kind: 'Deployment', namespace: 'guestbook', name: 'guestbook-ui', managed: true, copied: false,
          resource: { kind: 'Deployment', name: 'guestbook-ui', namespace: 'guestbook', status: 'OutOfSync', requiresPruning: false },
          drift: { compared: true, masked: false, fields: [{ path: 'spec.replicas', applied: '1', live: '3', change: 'changed' }] },
        },
      },
      [MISSING]: { status: 404, body: { code: 'object_not_found', message: 'There is no ConfigMap guestbook/settings on this cluster that this console may read.' } },
    });
    mount(`${PAGE_PATH}/applications/argocd/guestbook`);
    // The first ones are open with the page: the difference under the row,
    // and the row's word following what the read found.
    const diff = await screen.findByRole('table', { name: 'Last applied against live' });
    expect(within(diff).getByText('spec.replicas')).toBeTruthy();
    expect(within(diff).getByText('3')).toBeTruthy();
    expect((await waitFor(() => document.querySelector('[data-why="missing"]'))).textContent).toBe('Missing');
    const reason = (row: string) =>
      document.querySelector(`[data-outofsync-row="${row}"] [data-reason]`)?.textContent;
    await settle(() => expect(reason('ConfigMap/guestbook/settings')).toBe('missing from the cluster'));
    expect(reason('Deployment/guestbook/guestbook-ui')).toBe('1 field differs');
    expect(reason('Gone/stale')).toBe('not in git - would be pruned');

    // Folded on demand; each resource is read once.
    screen.getByRole('button', { name: 'Hide why Deployment guestbook/guestbook-ui is out of sync' }).click();
    await settle(() => expect(screen.queryByRole('table', { name: 'Last applied against live' })).toBeNull());
    expect(fetch.mock.calls.filter((call) => String(call[0]) === DEPLOYMENT)).toHaveLength(1);
  });

  it('opens the first five out-of-sync resources only, and reads the others when opened', async () => {
    const many = Array.from({ length: 7 }, (_, i) => ({
      version: 'v1', kind: 'ConfigMap', namespace: 'guestbook', name: `cm-${i}`, status: 'OutOfSync', requiresPruning: false, path: 'configmaps',
    }));
    const fetch = stub({ [APP]: { body: { ...detail, resources: many } } });
    mount(`${PAGE_PATH}/applications/argocd/guestbook`);
    await screen.findByRole('table', { name: 'Resources this Application manages' });
    const read = () => fetch.mock.calls.map((call) => String(call[0])).filter((u) => u.includes('/argocd/resource'));
    await settle(() => expect(read()).toHaveLength(5));
    expect(read().some((u) => u.includes('cm-5') || u.includes('cm-6'))).toBe(false);
    screen.getByRole('button', { name: 'Show why ConfigMap guestbook/cm-6 is out of sync' }).click();
    await settle(() => expect(read().some((u) => u.includes('cm-6'))).toBe(true));
  });

  it('opens its project in the console and draws its Events, newest first', async () => {
    stub({
      [APP]: {
        body: {
          ...detail,
          projectNamespace: 'argocd',
          events: [
            { type: 'Warning', reason: 'OperationCompleted', message: 'Sync operation failed', at: '2026-10-20T11:00:00Z' },
            { type: 'Normal', reason: 'OperationStarted', message: 'initiated automated sync', at: '2026-10-20T10:59:00Z', count: 2 },
          ],
        },
      },
    });
    mount(`${PAGE_PATH}/applications/argocd/guestbook`);
    const project = await screen.findByRole('link', { name: 'default' });
    expect(project.getAttribute('href')).toBe('/c/docker-desktop/appprojects.argoproj.io/argocd/default');
    const events = screen.getByRole('list', { name: 'Events of this Application' });
    expect([...events.querySelectorAll('[data-event]')].map((e) => e.getAttribute('data-event'))).toEqual(['OperationCompleted', 'OperationStarted']);
    expect(within(events).getByText('×2')).toBeTruthy();
  });

  it("hides the health column when Argo CD keeps no resource's health, and says why", async () => {
    stub({
      [APP]: {
        body: {
          ...detail,
          resources: detail.resources.map((r) => ({ ...r, health: undefined })),
        },
      },
    });
    mount(`${PAGE_PATH}/applications/argocd/guestbook`);
    const resources = await screen.findByRole('table', { name: 'Resources this Application manages' });
    expect(within(resources).queryByText('not reported')).toBeNull();
    expect(within(resources).queryByRole('columnheader', { name: 'Health' })).toBeNull();
    expect(document.querySelector('[data-health-note]')?.textContent).toContain('Argo CD 3 default');
  });

  it('draws a refused read as how to grant it, not as a failure to retry', async () => {
    stub({ [APP]: { status: 403, body: { code: 'list_forbidden', message: 'Argo CD is installed on this cluster, and the account this console read it with may not read its Applications.', subject: SA } } });
    mount(`${PAGE_PATH}/applications/argocd/guestbook`);
    const next = await waitFor(() => document.querySelector('[data-refusal="list_forbidden"]'));
    expect(next.querySelector('pre')?.textContent).toBe(grantManifest(SA));
    expect(screen.queryByText(/The request failed/)).toBeNull();
  });
});

async function waitFor(find: () => Element | null): Promise<Element> {
  for (let i = 0; i < 100; i++) {
    const found = find();
    if (found !== null) return found;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error('not found in time');
}
