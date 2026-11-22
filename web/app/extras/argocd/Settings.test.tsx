import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, createMemoryRouter, RouterProvider } from 'react-router';
import type { ExtensionInfo } from '../../../ui/src/api';
import { ExtraTab } from '../../../ui/src/pages/settings/ExtraTab';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { argocd, PAGE_PATH } from './index';
import { argoLink, fluxLink } from './settings';

afterEach(() => {
  vi.unstubAllGlobals();
});

const ARGO = '/x/docker-desktop/argocd/';
const FLUX = '/x/docker-desktop/argocd/flux';
const SETTINGS = '/x/docker-desktop/argocd/settings';

const argoRows = {
  applications: [{ namespace: 'argocd', name: 'guestbook', sync: 'Synced', health: 'Healthy', deleting: false }],
  complete: true,
};
const fluxRows = {
  objects: [
    { kind: 'Kustomization', namespace: 'flux-system', name: 'apps', ready: 'True', reconciling: false, stalled: false, suspended: false, deleting: false },
    { kind: 'HelmRelease', namespace: 'podinfo', name: 'podinfo', ready: 'True', reconciling: false, stalled: false, suspended: false, deleting: false },
  ],
  complete: true,
};
const fluxAbsent = { status: 404, body: { code: 'kind_unknown', message: 'Flux CD is not installed on it.' } };

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

function mount(): void {
  const router = createMemoryRouter([{ path: 'c/:cluster', children: argocd.routes }], {
    initialEntries: [`/c/docker-desktop/${PAGE_PATH}`],
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('the links to the tools\' own UIs', () => {
  it('builds the Argo CD and the Weave GitOps addresses', () => {
    expect(argoLink('https://argo.test', 'argocd', 'guest book')).toBe('https://argo.test/applications/argocd/guest%20book');
    expect(fluxLink('https://gitops.test', 'Kustomization', 'flux-system', 'apps')).toBe(
      'https://gitops.test/kustomization/details?clusterName=Default&name=apps&namespace=flux-system',
    );
    expect(fluxLink('https://gitops.test', 'HelmRelease', 'podinfo', 'podinfo')).toBe(
      'https://gitops.test/helm_release/details?clusterName=Default&name=podinfo&namespace=podinfo',
    );
    expect(fluxLink('https://gitops.test', 'Other', 'a', 'b')).toBeUndefined();
  });

  it('puts Open in Argo CD on each Application, in a new tab, once the address is set', async () => {
    stub({
      [ARGO]: { body: argoRows },
      [FLUX]: fluxAbsent,
      [SETTINGS]: { body: { tool: 'auto', argoUrl: 'https://argo.test' } },
    });
    mount();
    const link = await screen.findByRole('link', { name: 'Open guestbook in Argo CD' });
    expect(link.getAttribute('href')).toBe('https://argo.test/applications/argocd/guestbook');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toBe('noreferrer');
  });

  it('draws no link while no address is set', async () => {
    stub({ [ARGO]: { body: argoRows }, [FLUX]: fluxAbsent, [SETTINGS]: { body: { tool: 'auto' } } });
    mount();
    await screen.findByRole('link', { name: 'Open Application guestbook' });
    await waitFor(() => expect(screen.queryByText('Open in Argo CD')).toBeNull());
    expect(document.querySelector('[data-external]')).toBeNull();
  });

  it('puts Open in Flux on each Kustomization and HelmRelease', async () => {
    stub({
      [ARGO]: { status: 404, body: { code: 'kind_unknown', message: 'Argo CD is not installed on it.' } },
      [FLUX]: { body: fluxRows },
      [SETTINGS]: { body: { tool: 'auto', fluxUrl: 'https://gitops.test' } },
    });
    mount();
    const link = await screen.findByRole('link', { name: 'Open apps in Flux' });
    expect(link.getAttribute('href')).toBe('https://gitops.test/kustomization/details?clusterName=Default&name=apps&namespace=flux-system');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(screen.getByRole('link', { name: 'Open podinfo in Flux' }).getAttribute('href')).toContain('/helm_release/details?');
  });

  it('refuses to link an address that is not http or https', async () => {
    stub({
      [ARGO]: { body: argoRows },
      [FLUX]: fluxAbsent,
      [SETTINGS]: { body: { tool: 'auto', argoUrl: 'javascript:alert(1)' } },
    });
    mount();
    await screen.findByRole('link', { name: 'Open Application guestbook' });
    await waitFor(() => expect(document.querySelector('[data-external]')).toBeNull());
  });
});

describe('the Tool setting', () => {
  it('draws Argo CD alone when the tool is argocd', async () => {
    stub({ [ARGO]: { body: argoRows }, [SETTINGS]: { body: { tool: 'argocd' } } });
    mount();
    await screen.findByRole('link', { name: 'Open Application guestbook' });
    expect(screen.queryByRole('heading', { name: 'Flux CD' })).toBeNull();
  });

  it('draws Flux alone when the tool is flux', async () => {
    stub({ [FLUX]: { body: fluxRows }, [SETTINGS]: { body: { tool: 'flux' } } });
    mount();
    await screen.findByRole('table', { name: 'Flux CD Kustomizations and HelmReleases on this cluster' });
    expect(screen.queryByRole('heading', { name: 'Argo CD' })).toBeNull();
  });

  it('draws both sections when the tool is both, even where one is absent', async () => {
    stub({ [ARGO]: { body: argoRows }, [FLUX]: fluxAbsent, [SETTINGS]: { body: { tool: 'both' } } });
    mount();
    await screen.findByRole('link', { name: 'Open Application guestbook' });
    expect(await screen.findByRole('heading', { name: 'Flux CD' })).toBeTruthy();
  });
});

describe("the settings window of the extra's tile", () => {
  it("keeps the window open with the console's 400 for an address that is not http or https", async () => {
    const info: ExtensionInfo = {
      key: 'argocd',
      label: 'Delivery Center',
      enabled: true,
      pinned: false,
      writable: true,
      settings: [
        {
          key: 'tool', label: 'Tool', kind: 'choice', value: false, default: false, placeholder: 'Auto-detect',
          options: [
            { value: 'auto', label: 'Auto-detect' },
            { value: 'argocd', label: 'Argo CD' },
            { value: 'flux', label: 'Flux' },
            { value: 'both', label: 'Both' },
          ],
        },
        { key: 'argocd-url', label: 'Argo CD URL', kind: 'text', value: false, default: false, placeholder: 'https://argocd.example.com' },
      ],
    };
    vi.stubGlobal('fetch', (_input: RequestInfo | URL, init?: RequestInit) => {
      if ((init?.method ?? 'GET') === 'PUT') {
        return Promise.resolve(
          new Response(JSON.stringify({ title: 'Bad Request', status: 400, detail: 'the address must start with http:// or https://' }), {
            status: 400,
            headers: { 'Content-Type': 'application/problem+json' },
          }),
        );
      }
      return Promise.resolve(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
    });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <ExtraTab cluster="prod" clusters={[]} items={[info]} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Edit Delivery Center' }));
    // The tool is a list of its four choices, not a line of text.
    const select = document.querySelector('[role="dialog"] [data-extra-texts] select') as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(['Auto-detect', 'Argo CD', 'Flux', 'Both']);
    expect(select.value).toBe('auto');
    const input = document.querySelector('[role="dialog"] [data-extra-texts] input') as HTMLInputElement;
    expect(input.placeholder).toBe('https://argocd.example.com');
    fireEvent.change(input, { target: { value: 'javascript:alert(1)' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect((await screen.findByRole('alert')).textContent).toContain('http:// or https://');
    expect(document.querySelector('[role="dialog"] [data-extra-modal="argocd"]')).not.toBeNull();
  });
});
