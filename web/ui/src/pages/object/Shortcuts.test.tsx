import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import type { ComponentType } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage } from '../../api';
import type { Extension, ExtensionShortcutTarget, ExtensionTabProps } from '../../extension';
import { ShellProvider } from '../../shell/ShellContext';
import { renderWithClient, stubFetch, type StubReply } from '../../testing/harness';
import type { ObjectIdentity } from './address';
import { ObjectDetail } from './ObjectDetail';

/**
 * An extension's shortcut on an object's Details (`Extension.shortcuts`): a
 * tile to the extension's page about the object, drawn on its kinds while the
 * extension is on, once it has said where to go - and nothing otherwise.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

const service: ObjectIdentity = { cluster: 'prod', kind: 'services', namespace: 'cache', name: 'redis-master' };
const PATH = '/api/c/prod/r/services/cache/redis-master';
const page: DetailPage = { kind: 'services', namespace: 'cache', name: 'redis-master', title: 'Service', tabs: [], sections: [] };
const ok: StubReply = { body: { state: 'ok', data: page } };

function menu(...extras: string[]): StubReply {
  return {
    body: {
      state: 'ok',
      data: { groups: [{ key: 'extra', label: 'Extra', items: extras.map((key) => ({ text: key, href: `/c/prod/x/${key}` })), deferred: false }] },
    },
  };
}

const target: ExtensionShortcutTarget = {
  title: 'redis',
  badges: ['Redis', 'replication'],
  detail: 'cache · 1 primary · 2 replicas',
  to: '/c/prod/x/redis/cache/redis-master/6379',
};

function mount(
  resolve: () => Promise<ExtensionShortcutTarget | null>,
  kinds = ['services'],
  dialog?: ComponentType<ExtensionTabProps>,
): ReturnType<typeof vi.fn> {
  const spy = vi.fn(resolve);
  const redis: Extension = {
    key: 'redis',
    nav: { label: 'Redis viewer', path: 'x/redis' },
    routes: [],
    shortcuts: [{ key: 'deployment', kinds, resolve: spy, ...(dialog === undefined ? {} : { dialog }) }],
  };
  const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ObjectDetail identity={service} chrome="drawer" /> }], {
    initialEntries: ['/c/prod/services'],
  });
  renderWithClient(
    <ShellProvider extensions={[redis]}>
      <RouterProvider router={router} />
    </ShellProvider>,
  );
  return spy;
}

describe("an extension's shortcut on Details", () => {
  it("is one line in an Extras section just above Related, and opens the extension's tab on this object when it draws one", async () => {
    const related: DetailPage = {
      ...page,
      relations: [{ role: 'Selects', kind: 'pods', name: 'redis-master-0', namespace: 'cache', linkable: true } as never],
    };
    stubFetch({ [PATH]: { body: { state: 'ok', data: related } }, '/api/c/prod/nav': menu('redis') });
    const redis: Extension = {
      key: 'redis',
      nav: { label: 'Redis viewer', path: 'x/redis' },
      routes: [],
      shortcuts: [{ key: 'deployment', kinds: ['services'], resolve: () => Promise.resolve(target) }],
      tabs: [{ key: 'backend', label: 'Redis', kinds: ['services'], Component: () => <p data-redis-tab="">the tab</p> }],
    };
    const router = createMemoryRouter([{ path: '/c/:cluster/:kind', element: <ObjectDetail identity={service} chrome="drawer" /> }], {
      initialEntries: ['/c/prod/services'],
    });
    renderWithClient(
      <ShellProvider extensions={[redis]}>
        <RouterProvider router={router} />
      </ShellProvider>,
    );
    const line = await waitFor(() => {
      const found = document.querySelector('[data-shortcut="x.redis.deployment"]');
      expect(found).not.toBeNull();
      return found as HTMLElement;
    });
    const section = line.closest('[data-shortcuts]') as HTMLElement;
    expect(section.querySelector('h3')?.textContent).toBe('Extras');
    expect(line.textContent).toContain('Redis viewer');
    expect(line.textContent).toContain('cache · 1 primary · 2 replicas');
    // Above Related, which follows it.
    const headings = [...document.querySelectorAll('h3')].map((h) => h.textContent);
    expect(headings.indexOf('Extras')).toBeLessThan(headings.indexOf('Related'));
    expect(headings.indexOf('Extras')).toBeGreaterThan(-1);
    // A tab of the same extension on this object: the line opens it in place.
    expect(line.querySelector('a')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Open redis in Redis viewer' }));
    await waitFor(() => expect(router.state.location.search).toBe('?tab=x.redis.backend'));
  });

  it('draws a line that opens the page it names, with the extension named on it', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('redis') });
    const spy = mount(() => Promise.resolve(target));
    await waitFor(() => expect(document.querySelector('[data-shortcut="x.redis.deployment"]')).toBeTruthy());
    const tile = document.querySelector('[data-shortcut="x.redis.deployment"]') as HTMLElement;
    expect(tile.querySelector('a')?.getAttribute('href')).toBe('/c/prod/x/redis/cache/redis-master/6379');
    expect(tile.querySelector('a')?.getAttribute('aria-label')).toBe('Open redis in Redis viewer');
    expect(tile.textContent).toContain('Redis viewer');
    expect(tile.getAttribute('title')).toBe('Redis · replication');
    expect(tile.textContent).toContain('cache · 1 primary · 2 replicas');
    expect(spy).toHaveBeenCalledWith({ cluster: 'prod', kind: 'services', namespace: 'cache', name: 'redis-master' }, expect.any(AbortSignal));
  });

  it('opens its dialog for the same object when it declares one, instead of following the link', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('redis') });
    const seen: unknown[] = [];
    const Window = (props: ExtensionTabProps) => {
      seen.push(props);
      return <p data-window="">debug {props.name}</p>;
    };
    mount(() => Promise.resolve(target), ['services'], Window);
    await waitFor(() => expect(document.querySelector('[data-shortcut="x.redis.deployment"]')).toBeTruthy());
    const tile = document.querySelector('[data-shortcut="x.redis.deployment"]') as HTMLElement;
    expect(tile.querySelector('a')).toBeNull();
    expect(screen.queryByRole('dialog')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Open redis in Redis viewer' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog.querySelector('[data-window]')?.textContent).toBe('debug redis-master');
    expect(seen[0]).toEqual({ cluster: 'prod', kind: 'services', namespace: 'cache', name: 'redis-master' });
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('draws nothing when the extension says there is nowhere to go, or could not say', async () => {
    for (const resolve of [() => Promise.resolve(null), () => Promise.reject(new Error('the route failed'))]) {
      stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('redis') });
      const spy = mount(resolve);
      await screen.findByRole('tab', { name: 'Details' });
      await waitFor(() => expect(spy).toHaveBeenCalled());
      await screen.findByText('Service', { selector: 'p' });
      expect(document.querySelector('[data-shortcut]')).toBeNull();
      cleanup();
    }
  });

  it('is not asked while the extension is off, nor on a kind it does not name', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('argocd') });
    let spy = mount(() => Promise.resolve(target));
    await screen.findByRole('tab', { name: 'Details' });
    await new Promise((r) => setTimeout(r, 20));
    expect(spy).not.toHaveBeenCalled();
    expect(document.querySelector('[data-shortcut]')).toBeNull();
    cleanup();

    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('redis') });
    spy = mount(() => Promise.resolve(target), ['pods']);
    await screen.findByRole('tab', { name: 'Details' });
    await new Promise((r) => setTimeout(r, 20));
    expect(spy).not.toHaveBeenCalled();
  });
});

describe("a shortcut's status and meter", () => {
  it('draws the graded word in its tone, the tile in the same tone, and the meter as given', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('redis') });
    mount(() =>
      Promise.resolve({
        ...target,
        status: { text: 'tight', tone: 'warn', hint: 'above 75 %' },
        meter: {
          label: 'maximum heap against the memory limit',
          segments: [
            { width: 80, tone: 'warn', label: 'max heap' },
            { width: 140, label: 'clamped' },
          ],
          threshold: { at: 75, label: '75 %' },
          text: '1.6 GiB of 2Gi',
        },
      }),
    );
    await waitFor(() => expect(document.querySelector('[data-shortcut-meter]')).toBeTruthy());
    const tile = document.querySelector('[data-shortcut="x.redis.deployment"]') as HTMLElement;
    expect(tile.getAttribute('data-tone')).toBe('warn');
    expect(tile.querySelector('[data-pill="warn"]')?.textContent).toBe('tight');
    const parts = tile.querySelectorAll<HTMLElement>('[data-meter-part]');
    expect([...parts].map((p) => p.style.width)).toEqual(['80%', '100%']);
    expect(tile.querySelector<HTMLElement>('[data-meter-threshold]')?.style.left).toBe('75%');
    expect(tile.textContent).toContain('1.6 GiB of 2Gi');
    // The reading is cut to the room left, so it carries its whole text.
    expect(screen.getByText('1.6 GiB of 2Gi').getAttribute('title')).toBe('1.6 GiB of 2Gi');
  });

  it('is drawn as before without them', async () => {
    stubFetch({ [PATH]: ok, '/api/c/prod/nav': menu('redis') });
    mount(() => Promise.resolve(target));
    await waitFor(() => expect(document.querySelector('[data-shortcut="x.redis.deployment"]')).toBeTruthy());
    const tile = document.querySelector('[data-shortcut="x.redis.deployment"]') as HTMLElement;
    expect(tile.getAttribute('data-tone')).toBe('info');
    expect(tile.querySelector('[data-shortcut-meter]')).toBeNull();
  });
});
