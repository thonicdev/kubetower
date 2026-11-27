import { cleanup as cleanupMounted, fireEvent, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage } from '../../api';
import type { Extension, ExtensionTabProps } from '../../extension';
import { ShellProvider } from '../../shell/ShellContext';
import { renderWithClient, stubFetch, type StubReply } from '../../testing/harness';
import type { ObjectIdentity } from './address';
import { currentTabAddress } from './extensionTabs';
import { ObjectDetail } from './ObjectDetail';

/**
 * An extension's tab in the object panel: drawn on
 * its kinds, while the server's menu lists the extension as on, and nowhere
 * else.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

function Runs({ cluster, kind, namespace, name, readOnly }: ExtensionTabProps) {
  return (
    <p data-testid="runs" data-read-only={String(readOnly)}>
      runs of {cluster}/{kind}/{namespace}/{name}
    </p>
  );
}

const kubecron: Extension = {
  key: 'kubecron',
  legacyKeys: ['scheduled'],
  nav: { label: 'KubeCron', path: 'x/kubecron' },
  routes: [],
  tabs: [{ key: 'runs', label: 'KubeCron', kinds: ['pods', 'jobs', 'cronjobs'], Component: Runs }],
};

const pod: ObjectIdentity = { cluster: 'prod', kind: 'pods', namespace: 'batch', name: 'backup-29-x7' };
const PATH = '/api/c/prod/r/pods/batch/backup-29-x7';
const page: DetailPage = { kind: 'pods', namespace: 'batch', name: 'backup-29-x7', title: 'Pod', tabs: [], sections: [] };
const ok: StubReply = { body: { state: 'ok', data: page } };

function menu(...extras: string[]): StubReply {
  return {
    body: {
      state: 'ok',
      data: {
        groups: [
          { key: 'workloads', label: 'Workloads', items: [{ text: 'Pods', href: '/c/prod/pods' }], deferred: false },
          { key: 'extra', label: 'Extra', items: extras.map((key) => ({ text: key, href: `/c/prod/x/${key}` })), deferred: false },
        ],
      },
    },
  };
}

function mount(identity: ObjectIdentity, address = '/c/prod/pods'): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ObjectDetail identity={identity} chrome="drawer" /> }], {
    initialEntries: [address],
  });
  renderWithClient(
    <ShellProvider extensions={[kubecron]}>
      <RouterProvider router={router} />
    </ShellProvider>,
  );
}

describe("an extension's tab in the object panel", () => {
  it('is drawn on its kind while the extension is on, and draws the object it is given', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('kubecron') });
    mount(pod);
    const tab = await screen.findByRole('tab', { name: 'KubeCron' });
    fireEvent.click(tab);
    expect((await screen.findByTestId('runs')).textContent).toBe('runs of prod/pods/batch/backup-29-x7');
    expect(document.querySelector('[data-extension-tab="x.kubecron.runs"]')).toBeTruthy();
  });

  it("is told whether the cluster is read-only, and drawn only once that is known (#157)", async () => {
    const clusters = (readOnly: boolean): StubReply => ({
      body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok', readOnly }] },
    });
    for (const readOnly of [true, false]) {
      stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('kubecron'), '/api/clusters': clusters(readOnly) });
      mount(pod);
      fireEvent.click(await screen.findByRole('tab', { name: 'KubeCron' }));
      expect((await screen.findByTestId('runs')).getAttribute('data-read-only')).toBe(String(readOnly));
      cleanupMounted();
    }
  });

  it('is absent while the extension is off for the cluster', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('argocd') });
    mount(pod);
    await screen.findByRole('tab', { name: 'Details' });
    // Give the menu its answer before asserting the absence.
    await waitFor(() => {
      expect(screen.queryByRole('tab', { name: 'KubeCron' })).toBeNull();
    });
    expect(screen.queryByTestId('runs')).toBeNull();
  });

  it('is absent on a kind it does not name, and the menu is not even asked', async () => {
    const fetch = vi.fn((input: RequestInfo | URL) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const body = path.startsWith('/api/c/prod/r/')
        ? { state: 'ok', data: { kind: 'services', namespace: 'batch', name: 'api', title: 'Service', tabs: [], sections: [] } }
        : menu('kubecron').body;
      return Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }));
    });
    vi.stubGlobal('fetch', fetch);
    mount({ cluster: 'prod', kind: 'services', namespace: 'batch', name: 'api' }, '/c/prod/services');
    await screen.findByRole('tab', { name: 'Details' });
    expect(screen.queryByRole('tab', { name: 'KubeCron' })).toBeNull();
    const asked = fetch.mock.calls.map(([input]) => String(input));
    expect(asked.some((url) => url.includes('/nav'))).toBe(false);
  });

  it('opens from its address, so a reload lands back on it', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('kubecron') });
    mount(pod, '/c/prod/pods?tab=x.kubecron.runs');
    expect(await screen.findByTestId('runs')).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'KubeCron' }).getAttribute('aria-selected')).toBe('true');
  });

  it('opens from an address written under its legacy key', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('kubecron') });
    mount(pod, '/c/prod/pods?tab=x.scheduled.runs');
    expect(await screen.findByTestId('runs')).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'KubeCron' }).getAttribute('aria-selected')).toBe('true');
  });
});

describe('currentTabAddress', () => {
  it('rewrites a legacy key to the current one and leaves every other value alone', () => {
    expect(currentTabAddress([kubecron], 'x.scheduled.runs')).toBe('x.kubecron.runs');
    expect(currentTabAddress([kubecron], 'x.kubecron.runs')).toBe('x.kubecron.runs');
    expect(currentTabAddress([kubecron], 'x.other.runs')).toBe('x.other.runs');
    expect(currentTabAddress([kubecron], 'x.scheduled')).toBe('x.scheduled');
    expect(currentTabAddress([kubecron], 'events')).toBe('events');
    expect(currentTabAddress([kubecron], undefined)).toBeUndefined();
  });
});

describe('a tab on every kind, drawn when the object matches', () => {
  const tracked = 'argocd.argoproj.io/tracking-id';
  const argocd: Extension = {
    key: 'argocd',
    nav: { label: 'Delivery Center', path: 'x/argocd' },
    routes: [],
    tabs: [
      {
        key: 'sync',
        label: 'Argo CD',
        kinds: ['*'],
        when: ({ annotations }) => tracked in annotations,
        Component: Runs,
      },
    ],
  };
  const service: ObjectIdentity = { cluster: 'prod', kind: 'services', namespace: 'web', name: 'api' };
  const SERVICE = '/api/c/prod/r/services/web/api';

  function serviceWith(annotations: Record<string, string> | undefined): StubReply {
    return {
      body: {
        state: 'ok',
        data: { kind: 'services', namespace: 'web', name: 'api', title: 'Service', tabs: [], sections: [], annotations },
      },
    };
  }

  function mountArgo(address = '/c/prod/services'): void {
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ObjectDetail identity={service} chrome="drawer" /> }], {
      initialEntries: [address],
    });
    renderWithClient(
      <ShellProvider extensions={[argocd]}>
        <RouterProvider router={router} />
      </ShellProvider>,
    );
  }

  it('is drawn on any kind whose object carries the mark', async () => {
    stubFetch({ [SERVICE]: serviceWith({ [tracked]: 'shop:/Service:web/api' }), '/api/c/prod/nav': menu('argocd') });
    mountArgo();
    fireEvent.click(await screen.findByRole('tab', { name: 'Argo CD' }));
    expect((await screen.findByTestId('runs')).textContent).toBe('runs of prod/services/web/api');
  });

  it('is absent on an object without the mark', async () => {
    stubFetch({ [SERVICE]: serviceWith({ owner: 'team-a' }), '/api/c/prod/nav': menu('argocd') });
    mountArgo();
    await screen.findByRole('tab', { name: 'Details' });
    await waitFor(() => {
      expect(screen.queryByRole('tab', { name: 'Argo CD' })).toBeNull();
    });
  });

  it('opens from its address once the object has been read', async () => {
    stubFetch({ [SERVICE]: serviceWith({ [tracked]: 'shop:/Service:web/api' }), '/api/c/prod/nav': menu('argocd') });
    mountArgo('/c/prod/services?tab=x.argocd.sync');
    expect(await screen.findByTestId('runs')).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Argo CD' }).getAttribute('aria-selected')).toBe('true');
  });
});

describe('a tab drawn only when its probe says yes', () => {
  const pod: ObjectIdentity = { cluster: 'prod', kind: 'pods', namespace: 'web', name: 'api-7d9-x' };
  const POD = '/api/c/prod/r/pods/web/api-7d9-x';
  const reply: StubReply = { body: { state: 'ok', data: { kind: 'pods', namespace: 'web', name: 'api-7d9-x', title: 'Pod', tabs: [], sections: [] } } };

  function mountProbed(answer: boolean, address = '/c/prod/pods'): ReturnType<typeof vi.fn> {
    const probe = vi.fn(() => Promise.resolve(answer));
    const argocd: Extension = {
      key: 'argocd',
      nav: { label: 'Delivery Center', path: 'x/argocd' },
      routes: [],
      tabs: [{ key: 'owner', label: 'Argo CD', kinds: ['pods'], probe, Component: Runs }],
    };
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ObjectDetail identity={pod} chrome="drawer" /> }], {
      initialEntries: [address],
    });
    renderWithClient(
      <ShellProvider extensions={[argocd]}>
        <RouterProvider router={router} />
      </ShellProvider>,
    );
    return probe;
  }

  it('is drawn when the probe answers true, and the probe is given the object', async () => {
    stubFetch({ [POD]: reply, '/api/c/prod/nav': menu('argocd') });
    const probe = mountProbed(true);
    fireEvent.click(await screen.findByRole('tab', { name: 'Argo CD' }));
    expect(await screen.findByTestId('runs')).toBeTruthy();
    expect(probe.mock.calls[0]?.[0]).toEqual({ cluster: 'prod', kind: 'pods', namespace: 'web', name: 'api-7d9-x' });
  });

  it('is absent when the probe answers false', async () => {
    stubFetch({ [POD]: reply, '/api/c/prod/nav': menu('argocd') });
    const probe = mountProbed(false);
    await screen.findByRole('tab', { name: 'Details' });
    await waitFor(() => {
      expect(probe).toHaveBeenCalled();
    });
    expect(screen.queryByRole('tab', { name: 'Argo CD' })).toBeNull();
  });

  it('opens from its address once the probe has answered', async () => {
    stubFetch({ [POD]: reply, '/api/c/prod/nav': menu('argocd') });
    mountProbed(true, '/c/prod/pods?tab=x.argocd.owner');
    expect(await screen.findByTestId('runs')).toBeTruthy();
  });
});
