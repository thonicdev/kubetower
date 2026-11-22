import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { Suspense } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import ArgoTab, { comparedAgo } from './ArgoTab';
import { argocd } from './index';
import type { Tracked } from './tracked';

/**
 * The Argo CD tab in an object's panel: offered on
 * any kind whose object carries Argo's mark. It says Synced, Not synced or Unknown in one line, and draws one diff at once.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const URL = '/x/docker-desktop/argocd/resource?kind=deployments&name=guestbook-ui&namespace=guestbook';

function stub(status: number, body: unknown): void {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof globalThis.URL ? input.pathname + input.search : input.url;
      if (url !== URL) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      return Promise.resolve(
        new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json; charset=utf-8' } }),
      );
    }),
  );
}

function mount(): void {
  const router = createMemoryRouter(
    [
      {
        path: '/',
        element: (
          <Suspense>
            <ArgoTab cluster="docker-desktop" kind="deployments" namespace="guestbook" name="guestbook-ui" />
          </Suspense>
        ),
      },
    ],
    { initialEntries: ['/'] },
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const scaled: Tracked = {
  kind: 'Deployment',
  namespace: 'guestbook',
  name: 'guestbook-ui',
  managed: true,
  via: 'annotation',
  mark: 'guestbook:apps/Deployment:guestbook/guestbook-ui',
  copied: false,
  application: {
    namespace: 'argocd',
    name: 'guestbook',
    deleting: false,
    source: {},
    destination: {},
    sync: 'OutOfSync',
    syncRevision: 'bbbbbbbbbb',
    health: 'Healthy',
    automatedSync: false,
    history: [],
    conditions: [],
    resources: [],
  },
  resource: { group: 'apps', kind: 'Deployment', namespace: 'guestbook', name: 'guestbook-ui', status: 'OutOfSync', requiresPruning: false },
  syncedRevision: 'aaaaaaaaaa',
  drift: { compared: true, masked: false, fields: [{ path: 'spec.replicas', applied: '1', live: '3', change: 'changed' }] },
  note: 'Argo CD finds this resource different from git, and the fields below changed on it since Argo CD last applied it.',
};

const OWN_DIFF = {
  compared: true,
  masked: false,
  setup: false,
  fields: [{ path: 'spec.template.spec.containers[name=web].image', applied: 'web:v6', live: 'web:v5', change: 'changed' as const }],
};

const SETUP = { compared: false, masked: false, setup: true, fields: [], secret: 'kubetower/kubetower-argocd-token', note: 'Argo CD own diff is off.' };

const tab = () => document.querySelector('[data-argo-tab]') as HTMLElement;
const verdict = () => document.querySelector('[data-argo-verdict]')?.textContent;
const tables = () => document.querySelectorAll('table');

async function found(selector: string): Promise<Element> {
  return waitFor(() => {
    const el = document.querySelector(selector);
    if (el === null) throw new Error(`no ${selector} yet`);
    return el;
  });
}

describe('the Argo CD tab', () => {
  it('is offered on every kind, only on an object carrying the mark', () => {
    const tab = argocd.tabs?.find((one) => one.key === 'sync');
    expect(tab?.kinds).toEqual(['*']);
    expect(tab?.when?.({ labels: {}, annotations: { 'argocd.argoproj.io/tracking-id': 'a:/Service:ns/x' } })).toBe(true);
    expect(tab?.when?.({ labels: { 'app.kubernetes.io/instance': 'shop' }, annotations: {} })).toBe(true);
    expect(tab?.when?.({ labels: { app: 'shop' }, annotations: { owner: 'me' } })).toBe(false);
    // Helm's own release carries the same label, and is not Argo CD's.
    expect(tab?.when?.({ labels: { 'app.kubernetes.io/instance': 'kubetower' }, annotations: { 'meta.helm.sh/release-name': 'kubetower' } })).toBe(false);
  });

  it("says OutOfSync and draws Argo CD's own diff at once, and only that one", async () => {
    // Both comparisons answered, as on docker-desktop: drawing both would show
    // the same field twice, in two tables.
    stub(200, { ...scaled, argoDiff: OWN_DIFF });
    mount();
    const row = await screen.findByText('spec.template.spec.containers[name=web].image');
    expect(verdict()).toBe('Out of sync');
    expect(tab().dataset['verdict']).toBe('outofsync');
    expect(tables()).toHaveLength(1);
    expect(row.closest('tr')?.textContent).toBe('spec.template.spec.containers[name=web].imageweb:v6web:v5');
    expect(screen.getByRole('table', { name: 'Git against live' })).toBeTruthy();
    expect(screen.queryByText('spec.replicas')).toBeNull();
    // The Application, as a link, and no second set of sync words.
    expect(screen.getByRole('link', { name: 'guestbook' }).getAttribute('href')).toBe('/c/docker-desktop/x/argocd/applications/argocd/guestbook');
    expect(screen.queryAllByText('Out of sync')).toHaveLength(1);
    expect(screen.queryByText('Healthy')).toBeNull();
    // Nothing to set up: the token answered.
    expect(document.querySelector('[data-argo-diff-setup]')).toBeNull();
  });

  it('falls back to the drift since the last sync, says which it is, and shows no setup or command', async () => {
    stub(200, { ...scaled, argoDiff: SETUP });
    mount();
    const row = await screen.findByText('spec.replicas');
    expect(row.closest('tr')?.textContent).toBe('spec.replicas13');
    expect(screen.getByRole('table', { name: 'Last applied against live' })).toBeTruthy();
    expect(tables()).toHaveLength(1);
    // No "compare with git" fold, no command: only the sync word and its reason.
    expect(document.querySelector('[data-argo-diff-setup]')).toBeNull();
    expect(tab().textContent).not.toMatch(/kubectl|argocd |Compare with git/);
  });

  it('offers Refresh for the Application, and only for a managed object', async () => {
    stub(200, scaled);
    mount();
    await found('[data-argo-refresh]');
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeTruthy();
  });

  it('says when Argo CD last compared the Application', async () => {
    stub(200, { ...scaled, application: { ...scaled.application, reconciledAt: new Date(Date.now() - 125_000).toISOString() } });
    mount();
    const el = await found('[data-argo-compared]');
    expect(el.textContent).toBe('compared 2m ago');
    expect(comparedAgo('2026-11-16T10:00:00Z', Date.parse('2026-11-16T10:00:30Z'))).toBe('30s ago');
    expect(comparedAgo('2026-11-16T10:00:00Z', Date.parse('2026-11-16T13:00:00Z'))).toBe('3h ago');
  });

  it('says Synced and nothing else when nothing differs', async () => {
    stub(200, {
      ...scaled,
      resource: { ...scaled.resource, status: 'Synced' },
      drift: { compared: true, masked: false, fields: [] },
      argoDiff: { ...OWN_DIFF, fields: [] },
      note: undefined,
    });
    mount();
    await waitFor(() => expect(verdict()).toBe('Synced'));
    expect(tab().textContent).toBe('SyncedRefreshguestbook');
    expect(tables()).toHaveLength(0);
  });

  it('says the difference is a commit not yet applied when nothing changed on the object', async () => {
    stub(200, { ...scaled, drift: { compared: true, masked: false, fields: [] }, argoDiff: SETUP });
    mount();
    const line = await found('[data-drift="in-git"]');
    expect(verdict()).toBe('Out of sync');
    expect(line.textContent).toContain('(git bbbbbbb, applied aaaaaaa)');
    expect(tables()).toHaveLength(0);
  });

  it("says why Argo CD's own diff failed, in warn, and still draws the drift", async () => {
    stub(200, { ...scaled, argoDiff: { compared: false, masked: false, setup: false, fields: [], note: 'argocd-server refused the token (401).' } });
    mount();
    await screen.findByText('spec.replicas');
    const failed = document.querySelector('[data-argo-diff="failed"]');
    expect(failed?.textContent).toBe('Argo CD diff unavailable');
    expect(failed?.getAttribute('title')).toBe('argocd-server refused the token (401).');
    expect(failed?.className).toContain('text-warn');
    expect(document.querySelector('[data-argo-diff-setup]')).toBeNull();
  });

  it('says Unknown when the Applications are refused, in a short state, and still draws the drift', async () => {
    stub(200, {
      ...scaled,
      application: undefined,
      resource: undefined,
      note: undefined,
      applicationCode: 'list_forbidden',
      applicationNote: 'The account this console read it with may not read Argo CD\u2019s Applications.',
      subject: 'system:serviceaccount:kubetower:kubetower',
    });
    mount();
    expect(await screen.findByText('spec.replicas')).toBeTruthy();
    expect(verdict()).toBe('Unknown');
    expect(document.querySelector('[data-argo-why]')?.getAttribute('data-code')).toBe('list_forbidden');
    expect(document.querySelector('[data-argo-why]')?.textContent).toBe('Application not readable');
    expect(screen.queryByText('How to grant the read')).toBeNull();
  });

  it("says Unknown when the Application is read and does not list the object, never the Application's own word", async () => {
    stub(200, {
      ...scaled,
      resource: undefined,
      note: undefined,
      applicationNote: 'The Application argocd/guestbook does not list this Deployment among its resources.',
    });
    mount();
    await screen.findByText('spec.replicas');
    // The Application says OutOfSync; that is about all of its resources.
    expect(verdict()).toBe('Unknown');
    const why = document.querySelector('[data-argo-why]');
    expect(why?.textContent).toBe('Application not found');
    expect(why?.getAttribute('title')).toContain('does not list this Deployment');
  });

  it("draws the drift Argo CD does not count when its own diff finds nothing", async () => {
    stub(200, { ...scaled, resource: { ...scaled.resource, status: 'Synced' }, note: undefined, argoDiff: { ...OWN_DIFF, fields: [] } });
    mount();
    await screen.findByText('spec.replicas');
    expect(verdict()).toBe('Synced');
    expect(screen.getByRole('table', { name: 'Last applied against live' })).toBeTruthy();
    expect(screen.getByText(/Ignored by Argo CD/)).toBeTruthy();
    expect(screen.queryByText('Argo CD shows no field difference.')).toBeNull();
  });

  it('says an object git no longer has as an answer, once, and never as a failed diff', async () => {
    stub(200, {
      ...scaled,
      argoDiff: { compared: false, masked: false, setup: false, fields: [], notInGit: true, note: 'Git no longer has this object: Argo CD would delete it on a sync with pruning.' },
    });
    mount();
    await screen.findByText('spec.replicas');
    expect(document.querySelector('[data-not-in-git]')?.textContent).toBe('Not in git');
    expect(document.querySelector('[data-argo-diff="failed"]')).toBeNull();
    expect(screen.queryByText(/Git no longer has this object/)).toBeNull();
  });

  it('draws an unmanaged object as not managed, with its reason', async () => {
    stub(200, { kind: 'Service', name: 'x', managed: false, copied: true, drift: { compared: false, masked: false, fields: [] }, note: 'The tracking annotation names another object.' });
    mount();
    expect(await screen.findByText('Not managed by Argo CD')).toBeTruthy();
    expect(screen.getByText('Not managed by Argo CD').closest('[title]')?.getAttribute('title')).toBe('The tracking annotation names another object.');
  });

  it('on a Pod, is offered through a probe and names the owner Argo CD manages', async () => {
    const tab = argocd.tabs?.find((one) => one.key === 'owner');
    expect(tab?.kinds).toEqual(['pods', 'replicasets']);
    expect(tab?.probe).toBeTypeOf('function');
    expect(tab?.when?.({ labels: {}, annotations: {} })).toBe(true);
    stub(200, {
      ...scaled,
      of: { kind: 'Pod', namespace: 'guestbook', name: 'guestbook-ui-5f9-x7' },
      through: [
        { kind: 'ReplicaSet', namespace: 'guestbook', name: 'guestbook-ui-5f9', path: 'replicasets' },
        { kind: 'Deployment', namespace: 'guestbook', name: 'guestbook-ui', path: 'deployments' },
      ],
    });
    mount();
    const link = await screen.findByRole('link', { name: 'Deployment guestbook-ui' });
    expect(link.getAttribute('href')).toBe('/c/docker-desktop/deployments/guestbook/guestbook-ui?tab=x.argocd.sync');
    expect(link.getAttribute('title')).toBe('ReplicaSet guestbook-ui-5f9 → Deployment guestbook-ui');
  });

  it('draws a refusal as an answer, with no retry, and a failure as a failure', async () => {
    stub(403, { code: 'list_forbidden', message: 'The account this console read it with may not read this Deployment.', detail: 'forbidden' });
    mount();
    const refused = await found('[data-argo-tab="refused"]');
    expect(refused.textContent).toBe('The account this console read it with may not read this Deployment.');
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    cleanup();

    stub(502, { code: 'list_failed', message: 'The Deployment could not be read.', detail: 'connection refused' });
    mount();
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The Deployment could not be read. connection refused');
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
    expect(document.querySelector('[data-argo-tab]')).toBeNull();
  });

  it('draws a Secret as "Secret differs" and its keys, never a value or a table', async () => {
    stub(200, {
      ...scaled,
      kind: 'Secret',
      secret: true,
      resource: { ...scaled.resource, kind: 'Secret', status: 'OutOfSync' },
      drift: { compared: false, masked: true, fields: [] },
      argoDiff: {
        compared: true,
        masked: true,
        setup: false,
        fields: [
          { path: 'data.password', change: 'changed' },
          { path: 'data["tls.crt"]', change: 'changed' },
        ],
      },
    });
    mount();
    const line = await found('[data-drift="secret"]');
    expect(line.textContent).toBe('Secret differspasswordtls.crt');
    expect(tables()).toHaveLength(0);
    expect(document.querySelector('[data-argo-diff-setup]')).toBeNull();
  });

  it('draws an OutOfSync Secret with no diff to read as "Secret differs" alone', async () => {
    stub(200, {
      ...scaled,
      kind: 'Secret',
      secret: true,
      resource: { ...scaled.resource, kind: 'Secret', status: 'OutOfSync' },
      drift: { compared: false, masked: true, fields: [] },
      argoDiff: SETUP,
    });
    mount();
    const line = await found('[data-drift="secret"]');
    expect(line.textContent).toBe('Secret differs');
    expect(document.querySelector('[data-argo-diff-setup]')).toBeNull();
  });

  it('declares the Argo CD column on every list', () => {
    const column = argocd.columns?.[0];
    expect(column?.kinds).toEqual(['*']);
    expect(column?.label).toBe('Argo CD');
  });
});
