import { cleanup, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Extension, ExtensionShortcutTarget } from '../../extension';
import { ShellProvider } from '../../shell/ShellContext';
import { renderWithClient, stubFetch, type StubReply } from '../../testing/harness';
import { ExtraTiles } from './ExtraTiles';

/**
 * An extension's tile on the Overview (`Extension.overview`): drawn only while
 * the extension is on for the cluster, once it has answered - nothing over a
 * refusal, a null or an extension that is off.
 */

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function menu(...extras: string[]): StubReply {
  return {
    body: {
      state: 'ok',
      data: { groups: [{ key: 'extra', label: 'Extra', items: extras.map((key) => ({ text: key, href: `/c/prod/x/${key}` })), deferred: false }] },
    },
  };
}

const target: ExtensionShortcutTarget = {
  title: '42 claims',
  badges: ['3 orphans'],
  detail: 'Fill read',
  to: '/c/prod/x/storage',
  status: { text: '1 ≥ 90%', tone: 'bad', hint: 'claims at 90 % and over' },
};

function mount(resolve: (cluster: string, signal: AbortSignal) => Promise<ExtensionShortcutTarget | null>): ReturnType<typeof vi.fn> {
  const spy = vi.fn(resolve);
  const storage: Extension = { key: 'storage', nav: { label: 'Storage Inspector', path: 'x/storage' }, routes: [], overview: { resolve: spy } };
  const router = createMemoryRouter([{ path: '*', element: <ExtraTiles cluster="prod" /> }], { initialEntries: ['/c/prod'] });
  renderWithClient(
    <ShellProvider extensions={[storage]}>
      <RouterProvider router={router} />
    </ShellProvider>,
  );
  return spy;
}

describe("an extension's tile on the Overview", () => {
  it('is drawn with its status, title, badges and link while the extension is on', async () => {
    stubFetch({ '/api/c/prod/nav': menu('storage') });
    const spy = mount(() => Promise.resolve(target));
    const link = await screen.findByRole('link', { name: 'Storage Inspector: 42 claims' });
    expect(link.getAttribute('href')).toBe('/c/prod/x/storage');
    expect(screen.getByText('1 ≥ 90%').closest('[data-tone]')?.getAttribute('data-tone')).toBe('bad');
    expect(screen.getByText('3 orphans')).toBeTruthy();
    expect(document.querySelector('[data-extra-tile="storage"]')?.getAttribute('data-tone')).toBe('bad');
    expect(spy).toHaveBeenCalledWith('prod', expect.anything());
  });

  it('is not asked for, nor drawn, while the extension is off', async () => {
    stubFetch({ '/api/c/prod/nav': menu('redis') });
    const spy = mount(() => Promise.resolve(target));
    await new Promise((settle) => setTimeout(settle, 50));
    expect(spy).not.toHaveBeenCalled();
    expect(document.querySelector('[data-extra-tiles]')).toBeNull();
  });

  it('draws no panel when the extension answers nothing or fails', async () => {
    for (const resolve of [() => Promise.resolve(null), () => Promise.reject(new Error('refused'))]) {
      stubFetch({ '/api/c/prod/nav': menu('storage') });
      const spy = mount(resolve);
      await waitFor(() => expect(spy).toHaveBeenCalled());
      await new Promise((settle) => setTimeout(settle, 20));
      expect(document.querySelector('[data-extra-tiles]')).toBeNull();
      cleanup();
    }
  });

  it('does not read the menu when no extension offers a tile', async () => {
    const fetches = vi.fn(() => Promise.reject(new Error('no read expected')));
    vi.stubGlobal('fetch', fetches);
    const router = createMemoryRouter([{ path: '*', element: <ExtraTiles cluster="prod" /> }], { initialEntries: ['/c/prod'] });
    renderWithClient(
      <ShellProvider extensions={[{ key: 'redis', nav: { label: 'Redis', path: 'x/redis' }, routes: [] }]}>
        <RouterProvider router={router} />
      </ShellProvider>,
    );
    await new Promise((settle) => setTimeout(settle, 20));
    expect(fetches).not.toHaveBeenCalled();
  });
});
