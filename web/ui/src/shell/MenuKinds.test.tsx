import { cleanup, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, type StubReply } from '../testing/harness';
import { resetKindsMemory } from './kindsMemory';
import { resetMenuMode, resetOpenGroups } from './menuMemory';
import { resetNavMemory } from './navMemory';

/**
 * The kinds holding nothing the person may see are
 * left out of the column by default, a button under the jump box lists them,
 * and a favorite is listed whatever it holds. `Menu.test.tsx` holds the
 * column's other behavior, every test there without an occupancy answer -
 * which is the case "nothing is hidden" below.
 */

function forgetEverything(): void {
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  resetKindsMemory();
  localStorage.clear();
}

beforeEach(forgetEverything);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  forgetEverything();
});

const session = {
  body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
};
const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

const nav = {
  body: {
    state: 'ok',
    data: {
      groups: [
        {
          key: 'cluster',
          label: 'Cluster',
          deferred: false,
          items: [
            { text: 'Overview', href: '/c/prod' },
            { text: 'Nodes', href: '/c/prod/nodes' },
            { text: 'Helm', href: '/c/prod/helm' },
            { text: 'Port forwards', href: '/c/prod/port-forwards' },
            { text: 'An extra', href: '/c/prod/x/an-extra' },
          ],
        },
        {
          key: 'workloads',
          label: 'Workloads',
          deferred: false,
          items: [
            { text: 'Pods', href: '/c/prod/pods' },
            { text: 'Jobs', href: '/c/prod/jobs' },
            { text: 'Cron jobs', href: '/c/prod/cronjobs' },
            { text: 'Daemon sets', href: '/c/prod/daemonsets' },
          ],
        },
        {
          key: 'storage',
          label: 'Storage',
          deferred: false,
          items: [{ text: 'Persistent volume claims', href: '/c/prod/persistentvolumeclaims' }],
        },
        { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] },
      ],
    },
  },
};

const custom = {
  body: {
    state: 'ok',
    data: {
      key: 'custom-resources',
      label: 'Custom Resources',
      deferred: false,
      items: [],
      groups: [
        {
          key: 'cert-manager.io',
          label: 'cert-manager.io',
          deferred: false,
          items: [{ text: 'Certificates', href: '/c/prod/certificates.cert-manager.io' }],
        },
      ],
    },
  },
};

/** Jobs and storage and the custom kind hold nothing; Cron jobs is unknown;
 *  Nodes and Daemon sets are not named. Helm is named `none` to prove a page
 *  that is not a kind is never left out, whatever the answer says. */
const occupancy = {
  body: {
    state: 'ok',
    data: {
      kinds: {
        pods: { state: 'some' },
        jobs: { state: 'none' },
        cronjobs: { state: 'unknown', reason: 'the list was refused' },
        persistentvolumeclaims: { state: 'none' },
        helm: { state: 'none' },
        'certificates.cert-manager.io': { state: 'none' },
      },
    },
  },
};

function toResponse(reply: StubReply): Response {
  const status = reply.status ?? 200;
  if (reply.body === undefined) return new Response(null, { status });
  return new Response(JSON.stringify(reply.body), {
    status,
    headers: { 'Content-Type': reply.contentType ?? 'application/json' },
  });
}

/** `stubFetch`, plus what was `PUT` to `/api/me/kinds`, answered with itself. */
function stub(extra: Record<string, StubReply> = {}): unknown[] {
  const routes: Record<string, StubReply> = {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': nav,
    '/api/c/prod/nav/custom-resources': custom,
    '/api/c/prod/nav/occupancy': occupancy,
    ...extra,
  };
  const puts: unknown[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    const key = path.split('?')[0] ?? path;
    if (key === '/api/me/kinds' && init?.method === 'PUT') {
      const body: unknown = JSON.parse(String(init.body));
      puts.push(body);
      return Promise.resolve(toResponse({ body }));
    }
    const reply = routes[key];
    return Promise.resolve(
      toResponse(
        reply ?? {
          status: 404,
          contentType: 'application/problem+json',
          body: { status: 404, title: 'Not Found', code: 'not_found', detail: `no stub for ${key}` },
        },
      ),
    );
  });
  return puts;
}

function menu(): HTMLElement {
  const element = document.getElementById('menu');
  if (element === null) throw new Error('#menu is missing');
  return element;
}

function row(href: string): Element | null {
  return menu().querySelector(`a[href="${href}"]`);
}

function toggle(): HTMLButtonElement {
  const element = menu().querySelector<HTMLButtonElement>('button[data-unused-toggle]');
  if (element === null) throw new Error('no Show unused kinds button');
  return element;
}

function star(slug: string): HTMLButtonElement {
  const element = menu().querySelector<HTMLButtonElement>(`button[data-favourite="${slug}"]`);
  if (element === null) throw new Error(`no star on ${slug}`);
  return element;
}

/** Mounted, with the occupancy answered and the choice read: the toggle is
 *  drawn only then. */
async function arrived(at = '/c/prod'): Promise<void> {
  mountApp(at);
  await waitFor(() => {
    expect(menu().querySelector('button[data-unused-toggle]')).not.toBeNull();
  });
}

describe('kinds holding nothing are left out by default', () => {
  it('hides a kind the answer names none, and keeps some, unknown and unnamed ones', async () => {
    stub();
    await arrived();

    expect(row('/c/prod/jobs')).toBeNull();
    expect(row('/c/prod/pods')).not.toBeNull();
    // Unknown is never zero, and a kind the answer does not name is unknown.
    expect(row('/c/prod/cronjobs')).not.toBeNull();
    expect(row('/c/prod/nodes')).not.toBeNull();
    expect(row('/c/prod/daemonsets')).not.toBeNull();
  });

  it('never leaves out Overview, Helm, port-forwards or an extension page', async () => {
    stub();
    await arrived();

    expect(row('/c/prod')).not.toBeNull();
    expect(row('/c/prod/helm')).not.toBeNull();
    expect(row('/c/prod/port-forwards')).not.toBeNull();
    // Not kinds, so no star either.
    expect(menu().querySelector('button[data-favourite="helm"]')).toBeNull();
    expect(menu().querySelector('button[data-favourite="port-forwards"]')).toBeNull();
    expect(menu().querySelector('button[data-favourite="x"]')).toBeNull();
  });

  it('does not draw a group the filter emptied', async () => {
    stub();
    await arrived();

    expect(menu().querySelector('details[data-group="storage"]')).toBeNull();
    expect(menu().querySelector('details[data-group="workloads"]')).not.toBeNull();
  });

  it('hides nothing while the answer is pending', async () => {
    stub();
    // A read that never settles is the pending case, held for the whole test.
    const answering = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      if (path.startsWith('/api/c/prod/nav/occupancy')) return new Promise<Response>(() => undefined);
      return answering(input, init);
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(row('/c/prod/pods')).not.toBeNull();
    });
    expect(row('/c/prod/jobs')).not.toBeNull();
    expect(menu().querySelector('details[data-group="storage"]')).not.toBeNull();
    expect(menu().querySelector('button[data-unused-toggle]')).toBeNull();
  });

  it('hides nothing when the answer failed', async () => {
    stub({
      '/api/c/prod/nav/occupancy': {
        body: { state: 'forbidden', reason: { code: 'verb_denied', message: 'refused' } },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(row('/c/prod/pods')).not.toBeNull();
    });
    // Let the answer land before reading what it changed.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(row('/c/prod/jobs')).not.toBeNull();
    expect(menu().querySelector('button[data-unused-toggle]')).toBeNull();
  });

  it('keeps the kind on screen, even holding nothing', async () => {
    stub();
    await arrived('/c/prod/jobs');

    expect(row('/c/prod/jobs')).not.toBeNull();
  });

  it('lets the jump box reach a hidden kind', async () => {
    stub();
    await arrived();

    const box = menu().querySelector<HTMLInputElement>('#menu-jump');
    if (box === null) throw new Error('no jump box');
    fireEvent.change(box, { target: { value: 'jobs' } });

    await waitFor(() => {
      expect(menu().querySelector('a[data-kind="jobs"]')).not.toBeNull();
    });
    fireEvent.change(box, { target: { value: 'persistent' } });
    await waitFor(() => {
      expect(row('/c/prod/persistentvolumeclaims')).not.toBeNull();
    });
  });

  it('filters the custom resources once they are loaded, and says why the heading is empty', async () => {
    stub();
    await arrived();

    const summary = menu().querySelector('details[data-group="custom-resources"] > summary');
    if (summary === null) throw new Error('no custom resources heading');
    fireEvent.click(summary);

    await waitFor(() => {
      expect(menu().querySelector('[data-note="all-unused"]')).not.toBeNull();
    });
    expect(row('/c/prod/certificates.cert-manager.io')).toBeNull();
    expect(menu().querySelector('details[data-group="cert-manager.io"]')).toBeNull();

    fireEvent.click(toggle());
    await waitFor(() => {
      expect(row('/c/prod/certificates.cert-manager.io')).not.toBeNull();
    });
    expect(menu().querySelector('[data-note="all-unused"]')).toBeNull();
  });
});

describe('the Show unused kinds button', () => {
  it('lists them, says how many, and keeps the choice in this browser', async () => {
    const puts = stub();
    await arrived();

    const button = toggle();
    expect(button.getAttribute('aria-pressed')).toBe('false');
    // jobs, persistentvolumeclaims, helm and the certificate: what the answer
    // names none, custom resources included.
    expect(button.textContent).toContain('Show unused kinds');
    expect(button.textContent).toContain('4');

    fireEvent.click(button);
    await waitFor(() => {
      expect(row('/c/prod/jobs')).not.toBeNull();
    });
    expect(toggle().getAttribute('aria-pressed')).toBe('true');
    // The label says what the next press does.
    expect(toggle().textContent).toContain('Hide unused kinds');
    expect(toggle().textContent).not.toContain('Show unused kinds');
    expect(menu().querySelector('details[data-group="storage"]')).not.toBeNull();
    expect(JSON.parse(localStorage.getItem('kubetower.kinds') ?? 'null')).toEqual({
      showUnused: true,
      favourites: {},
    });
    expect(puts).toEqual([]);

    // A fresh mount reads it back.
    cleanup();
    await arrived();
    expect(row('/c/prod/jobs')).not.toBeNull();
    expect(toggle().getAttribute('aria-pressed')).toBe('true');
  });

  it('keeps the choice on the server where the server keeps it', async () => {
    localStorage.setItem('kubetower.kinds', JSON.stringify({ showUnused: true, favourites: {} }));
    const puts = stub({
      '/api/capabilities': { body: { kindPreferences: true } },
      '/api/me/kinds': { body: { showUnused: false, favourites: { prod: ['pods'] } } },
    });
    await arrived();

    // The server's copy wins over what this browser held.
    expect(row('/c/prod/jobs')).toBeNull();
    expect(star('pods').getAttribute('aria-pressed')).toBe('true');

    fireEvent.click(toggle());
    await waitFor(() => {
      expect(puts).toEqual([{ showUnused: true, favourites: { prod: ['pods'] } }]);
    });
    await waitFor(() => {
      expect(row('/c/prod/jobs')).not.toBeNull();
    });
    // The browser's copy is left alone.
    expect(JSON.parse(localStorage.getItem('kubetower.kinds') ?? 'null')).toEqual({
      showUnused: true,
      favourites: {},
    });
  });
});

describe('a favorite is listed whatever it holds', () => {
  it('keeps a starred kind holding nothing, per cluster, in this browser', async () => {
    localStorage.setItem('kubetower.kinds', JSON.stringify({ showUnused: true, favourites: {} }));
    stub();
    await arrived();

    const jobs = star('jobs');
    expect(jobs.getAttribute('aria-pressed')).toBe('false');
    expect(jobs.getAttribute('aria-label')).toBe('Favorite: Jobs');
    fireEvent.click(jobs);
    await waitFor(() => {
      expect(star('jobs').getAttribute('aria-pressed')).toBe('true');
    });

    // Unused kinds hidden again: the favorite stays, the other goes.
    fireEvent.click(toggle());
    await waitFor(() => {
      expect(menu().querySelector('details[data-group="storage"]')).toBeNull();
    });
    expect(row('/c/prod/jobs')).not.toBeNull();
    expect(JSON.parse(localStorage.getItem('kubetower.kinds') ?? 'null')).toEqual({
      showUnused: false,
      favourites: { prod: ['jobs'] },
    });
    // It is not counted among the hidden.
    expect(toggle().textContent).toContain('3');
  });

  it('lists favorites first, in the order marked, and keeps them in their groups', async () => {
    localStorage.setItem('kubetower.kinds', JSON.stringify({ showUnused: true, favourites: {} }));
    stub();
    await arrived();
    const section = (): Element | null => menu().querySelector('section[data-group="favourites"]');

    // None: the section is absent.
    expect(section()).toBeNull();

    fireEvent.click(star('jobs'));
    await waitFor(() => {
      expect(section()).not.toBeNull();
    });
    fireEvent.click(star('pods'));
    await waitFor(() => {
      expect(section()?.querySelectorAll('a').length).toBe(2);
    });

    // The first section of the column, headed Favorites, in marked order.
    const first = menu().querySelector('section, details');
    expect(first).toBe(section());
    expect(section()?.querySelector('h2')?.textContent).toBe('Favorites');
    expect([...(section()?.querySelectorAll('a') ?? [])].map((a) => a.getAttribute('href'))).toEqual([
      '/c/prod/jobs',
      '/c/prod/pods',
    ]);
    // And still in its own group.
    expect(menu().querySelector('details[data-group="workloads"] a[href="/c/prod/pods"]')).not.toBeNull();

    // Unmarking removes it from the section, and the last one removes the section.
    fireEvent.click(section()?.querySelector('button[data-favourite="jobs"]') as Element);
    await waitFor(() => {
      expect(section()?.querySelectorAll('a').length).toBe(1);
    });
    fireEvent.click(section()?.querySelector('button[data-favourite="pods"]') as Element);
    await waitFor(() => {
      expect(section()).toBeNull();
    });
    expect(menu().querySelector('details[data-group="workloads"] a[href="/c/prod/pods"]')).not.toBeNull();
  });

  it('writes a favorite to the server where the server keeps them', async () => {
    const puts = stub({
      '/api/capabilities': { body: { kindPreferences: true } },
      '/api/me/kinds': { body: { showUnused: false, favourites: { staging: ['pods'] } } },
    });
    await arrived();

    fireEvent.click(star('pods'));
    await waitFor(() => {
      expect(puts).toEqual([{ showUnused: false, favourites: { staging: ['pods'], prod: ['pods'] } }]);
    });
    await waitFor(() => {
      expect(star('pods').getAttribute('aria-pressed')).toBe('true');
    });
    expect(localStorage.getItem('kubetower.kinds')).toBeNull();
  });
});
