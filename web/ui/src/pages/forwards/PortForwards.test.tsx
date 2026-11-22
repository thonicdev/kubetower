import { cleanup, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Forward } from '../../api';
import { resetKeyboardStack } from '../../keyboard/stack';
import { resetMenuMode, resetOpenGroups } from '../../shell/menuMemory';
import { resetNavMemory } from '../../shell/navMemory';
import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

/**
 * The menu's Network heading lists the open port-forwards,
 * on a page of their own at `/c/:cluster/port-forwards`.
 *
 * Whether the entry is in the menu is the server's answer (`forwardsnav.go`,
 * held by `TestThePortForwardsEntryFollowsTheCapability`); what is held here is
 * the page, and what the menu does with the entry once it is there.
 */

function forgetEverything(): void {
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  localStorage.clear();
}

beforeEach(forgetEverything);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  forgetEverything();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};
const clusters = {
  body: {
    state: 'ok',
    items: [
      { name: 'prod', label: 'prod', state: 'ok' },
      { name: 'staging', label: 'staging', state: 'ok' },
    ],
  },
};

/** The menu as the server composes it where forwards are offered: the entry
 *  last under Network, and a deferred custom resource heading. */
const nav = {
  body: {
    state: 'ok',
    data: {
      groups: [
        { key: 'cluster', label: 'Cluster', items: [{ text: 'Overview', href: '/c/prod' }] },
        {
          key: 'network',
          label: 'Network',
          items: [
            { text: 'Services', href: '/c/prod/services', namespaced: true },
            { text: 'Port forwards', href: '/c/prod/port-forwards' },
          ],
        },
        { key: 'custom-resources', label: 'Custom Resources', items: [], deferred: true },
      ],
    },
  },
};

function routes(portForward: boolean): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': nav,
    '/api/capabilities': { body: { mode: 'desktop', portForward } },
  };
}

const POD_FORWARD: Forward = {
  id: 'f-pod',
  cluster: 'prod',
  namespace: 'argo',
  pod: 'api-0',
  remotePort: 8080,
  address: '127.0.0.1:41001',
  scheme: 'http',
  openedAt: '2026-10-22T10:00:00Z',
};
const SERVICE_FORWARD: Forward = {
  id: 'f-svc',
  cluster: 'prod',
  namespace: 'shop',
  pod: 'web-7d9-abc',
  service: 'web',
  remotePort: 443,
  address: '127.0.0.1:41002',
  scheme: 'https',
  openedAt: '2026-10-22T09:00:00Z',
};
const ELSEWHERE: Forward = {
  id: 'f-staging',
  cluster: 'staging',
  namespace: 'argo',
  pod: 'api-1',
  remotePort: 9090,
  address: '127.0.0.1:41003',
  scheme: 'http',
};

/**
 * A forwarder with state over the path-only stub, as the Ports panel's tests
 * have it: the proof that Stop works is that the *list* no longer has the
 * forward, not that a request went somewhere.
 */
function stubForwarder(base: StubRoutes, live: Forward[]): { live: Forward[]; calls: string[] } {
  stubFetch(base);
  const stubbed = globalThis.fetch;
  const state = { live: [...live], calls: [] as string[] };
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
    const method = init?.method ?? 'GET';
    state.calls.push(`${method} ${path}`);
    if (path === '/api/forwards' && method === 'GET') {
      return Promise.resolve(
        new Response(JSON.stringify(state.live), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    }
    const closing = /^\/api\/forwards\/([^/]+)$/.exec(path);
    if (closing !== null && method === 'DELETE') {
      state.live = state.live.filter((one) => one.id !== closing[1]);
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    return stubbed(input, init);
  });
  return state;
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

function menu(): HTMLElement {
  const element = document.getElementById('menu');
  if (element === null) throw new Error('#menu is missing');
  return element;
}

function rows(): HTMLTableRowElement[] {
  return [...page().querySelectorAll<HTMLTableRowElement>('tbody tr')];
}

/** The row of one forward: its target cell names it. */
function row(id: string): HTMLTableRowElement | null {
  return page().querySelector(`[data-forward="${id}"]`)?.closest('tr') ?? null;
}

describe('the list of open port-forwards', () => {
  it("lists the forwards of the cluster in the address, and no other cluster's", async () => {
    stubForwarder(routes(true), [POD_FORWARD, SERVICE_FORWARD, ELSEWHERE]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(rows()).toHaveLength(2);
    });
    const text = page().textContent ?? '';
    expect(text).toContain('Port forwards');
    expect(text).toContain('8080');
    expect(text).toContain('443');
    expect(text).not.toContain('9090');
    expect(page().querySelector('[data-forward="f-staging"]')).toBeNull();
    // Every column, in the header.
    const headers = [...page().querySelectorAll('thead th [data-label]')].map((one) => one.textContent);
    expect(headers.slice(0, 5)).toEqual(['Namespace', 'Target', 'Remote port', 'Local address', 'Age']);
  });

  it("links each target to its object's page: the pod, and the service where there is one", async () => {
    stubForwarder(routes(true), [POD_FORWARD, SERVICE_FORWARD]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(rows()).toHaveLength(2);
    });
    const hrefs = (id: string): string[] =>
      [...(row(id)?.querySelectorAll('td:nth-child(2) a') ?? [])].map(
        (one) => one.getAttribute('href') ?? '',
      );
    expect(hrefs('f-pod')).toEqual(['/c/prod/pods/argo/api-0']);
    expect(hrefs('f-svc')).toEqual(['/c/prod/services/shop/web', '/c/prod/pods/shop/web-7d9-abc']);
  });

  it('opens the object when its target is clicked', async () => {
    stubForwarder(routes(true), [POD_FORWARD]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(rows()).toHaveLength(1);
    });
    const target = row('f-pod')?.querySelector<HTMLAnchorElement>('td:nth-child(2) a') ?? null;
    if (target === null) throw new Error('no target link');
    fireEvent.click(target);

    // The object page asks for the pod it was sent to.
    await waitFor(() => {
      expect(document.title).toContain('api-0');
    });
  });

  it('links the local address under the scheme the server chose, and draws it plain where it chose none', async () => {
    const unknown: Forward = { ...POD_FORWARD, id: 'f-raw', remotePort: 5432, address: '127.0.0.1:41009' };
    delete (unknown as { scheme?: string }).scheme;
    stubForwarder(routes(true), [SERVICE_FORWARD, unknown]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(rows()).toHaveLength(2);
    });
    const address = (id: string): Element | null =>
      row(id)?.querySelector('td:nth-child(4)') ?? null;
    const link = address('f-svc')?.querySelector('a');
    expect(link?.getAttribute('href')).toBe('https://127.0.0.1:41002');
    expect(link?.getAttribute('target')).toBe('_blank');
    expect(address('f-raw')?.querySelector('a')).toBeNull();
    expect(address('f-raw')?.textContent).toBe('127.0.0.1:41009');
  });

  it('stops a forward with DELETE, and the row goes away', async () => {
    const state = stubForwarder(routes(true), [POD_FORWARD, SERVICE_FORWARD]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(rows()).toHaveLength(2);
    });
    const stop = row('f-pod')?.querySelector<HTMLButtonElement>('button') ?? null;
    if (stop === null) throw new Error('no Stop on the pod forward');
    expect(stop.textContent).toBe('Stop');
    fireEvent.click(stop);

    await waitFor(() => {
      expect(page().querySelector('[data-forward="f-pod"]')).toBeNull();
    });
    expect(state.calls).toContain('DELETE /api/forwards/f-pod');
    expect(state.live.map((one) => one.id)).toEqual(['f-svc']);
    expect(page().querySelector('[data-forward="f-svc"]')).not.toBeNull();
  });

  it("renders through the lists' shared grid: panel head, headings and empty state", async () => {
    stubForwarder(routes(true), [ELSEWHERE]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(page().querySelector('table[aria-label="Port forwards on this cluster"]')).not.toBeNull();
    });
    // The panel head every list draws: the heading, the count, and the
    // search box with its funnel at the far end.
    const head = page().querySelector('header');
    expect(head?.querySelector('h1')?.textContent).toBe('Port forwards');
    expect(head?.querySelector('h1')?.className).toContain('text-lg');
    expect(head?.textContent).toContain('0 listed');
    expect(head?.querySelector('input[name="search"]')).not.toBeNull();
    expect(head?.querySelector('[data-control="filter"]')).not.toBeNull();
    // `DataGrid`'s own shape: the table straight under `#panel`'s scroll box,
    // a colgroup, a density, and headings that sort.
    const table = page().querySelector('#panel > .table-scroll > table');
    expect(table?.getAttribute('aria-label')).toBe('Port forwards on this cluster');
    expect(table?.getAttribute('data-density')).toBe('comfortable');
    expect(table?.querySelector('colgroup')).not.toBeNull();
    const heads = [...page().querySelectorAll('thead th [data-label]')].map((one) => one.textContent);
    expect(heads).toEqual(['Namespace', 'Target', 'Remote port', 'Local address', 'Age', 'Stop']);
    expect(page().querySelector('thead th[aria-sort]')).not.toBeNull();
    // The empty state is the lists' one, inside the table under its headings.
    const empty = page().querySelector('tbody [data-empty="kind"]');
    expect(empty?.textContent).toBe('No port-forwards on this cluster.');
  });

  it('narrows the rows by the search box, and says so when nothing matches', async () => {
    stubForwarder(routes(true), [POD_FORWARD, SERVICE_FORWARD]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(rows()).toHaveLength(2);
    });
    const box = page().querySelector<HTMLInputElement>('input[name="search"]');
    if (box === null) throw new Error('no search box');
    fireEvent.change(box, { target: { value: 'shop' } });
    await waitFor(() => {
      expect(row('f-pod')).toBeNull();
    });
    expect(row('f-svc')).not.toBeNull();
    expect(page().querySelector('header')?.textContent).toContain('1 of 2 listed');

    fireEvent.change(box, { target: { value: 'nothing-like-it' } });
    await waitFor(() => {
      expect(page().querySelector('tbody [data-empty="filtered"]')).not.toBeNull();
    });
    const clear = [...page().querySelectorAll('button')].find((one) => one.textContent === 'Clear the filter');
    if (clear === undefined) throw new Error('no Clear the filter');
    fireEvent.click(clear);
    await waitFor(() => {
      expect(rows()).toHaveLength(2);
    });
  });

  it('asks for nothing where the console does not open forwards, and says so', async () => {
    const state = stubForwarder(routes(false), [POD_FORWARD]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(page().querySelector('[data-note="not-offered"]')).not.toBeNull();
    });
    expect(page().textContent).toContain('kubectl port-forward');
    expect(state.calls.filter((one) => one.includes('/api/forwards'))).toEqual([]);
  });

  it('names the Details section the command is in, and why forwards are off in the profile', async () => {
    for (const [capabilities, why] of [
      [{ mode: 'shared', portForward: false }, 'it runs in the cluster for everyone who signs in'],
      [{ mode: 'container', portForward: false, replicated: true }, 'it runs as several replicas behind one address'],
    ] as const) {
      cleanup();
      stubForwarder({ ...routes(false), '/api/capabilities': { body: capabilities } }, []);
      mountApp('/c/prod/port-forwards');

      await waitFor(() => {
        expect(page().querySelector('[data-note="not-offered"]')).not.toBeNull();
      });
      const said = page().querySelector('[data-note="not-offered"]')?.textContent ?? '';
      expect(said).toContain(why);
      expect(said).toContain('The Ports section of a pod’s or a service’s Details');
      expect(said).not.toContain('Ports tab');
    }
  });

  it('says the list failed rather than that none is open', { timeout: 10_000 }, async () => {
    stubFetch({ ...routes(true), '/api/forwards': { status: 500, body: { title: 'boom' } } });
    mountApp('/c/prod/port-forwards');

    // A 5xx is asked twice more before it is an error (`query/client.ts`).
    await waitFor(
      () => {
        expect(page().textContent).toContain('The request failed (500)');
      },
      { timeout: 6_000 },
    );
    // A failed list draws no grid: an empty one would read as "none open".
    expect(page().querySelector('table')).toBeNull();
  });
});

describe('the menu entry', () => {
  it('is drawn under Network, current on its page, and opens no custom resource read', async () => {
    const state = stubForwarder(routes(true), [POD_FORWARD]);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(menu().textContent).toContain('Port forwards');
    });
    const network = menu().querySelector<HTMLDetailsElement>('[data-group="network"]');
    if (network === null) throw new Error('no Network group');
    await waitFor(() => {
      expect(network.open).toBe(true);
    });
    const entry = [...network.querySelectorAll('a')].find((one) => one.textContent === 'Port forwards');
    expect(entry?.getAttribute('aria-current')).toBe('page');
    // Not read as a custom resource page: `port-forwards` is a page under the
    // cluster, as Helm is, so the deferred heading stays shut and unasked.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(menu().querySelector<HTMLDetailsElement>('[data-group="custom-resources"]')?.open).toBe(false);
    expect(state.calls.filter((one) => one.includes('/nav/custom-resources'))).toEqual([]);
  });

  it('is a page and not a custom kind, also where the menu has no entry for it', async () => {
    // A console that opens no forwards has no entry, so no loaded group claims
    // the address; without `port-forwards` among the reserved segments the
    // menu would take it for a custom resource page and fire the read the
    // deferred heading exists to avoid (as for Helm).
    const withoutEntry = {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'network',
              label: 'Network',
              items: [{ text: 'Services', href: '/c/prod/services', namespaced: true }],
            },
            { key: 'custom-resources', label: 'Custom Resources', items: [], deferred: true },
          ],
        },
      },
    };
    const state = stubForwarder({ ...routes(false), '/api/c/prod/nav': withoutEntry }, []);
    mountApp('/c/prod/port-forwards');

    await waitFor(() => {
      expect(menu().textContent).toContain('Services');
    });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(menu().querySelector<HTMLDetailsElement>('[data-group="custom-resources"]')?.open).toBe(false);
    expect(state.calls.filter((one) => one.includes('/nav/custom-resources'))).toEqual([]);
  });

  it('warms no resource list when hovered, because it is not a kind', async () => {
    const state = stubForwarder(routes(true), []);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(menu().textContent).toContain('Port forwards');
    });
    const entry = [...menu().querySelectorAll('a')].find((one) => one.textContent === 'Port forwards');
    if (entry === undefined) throw new Error('no entry');
    fireEvent.pointerEnter(entry);
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(state.calls.filter((one) => one.startsWith('GET /api/c/prod/r/'))).toEqual([]);
  });
});
