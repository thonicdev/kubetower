import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Extension } from '../../extension';
import { favouriteExtraKeys } from '../../shell/favouriteExtras';
import { resetKindsMemory } from '../../shell/kindsMemory';
import { ShellProvider } from '../../shell/ShellContext';
import { FavouriteExtras } from './FavouriteExtras';

/** The shortcuts to this cluster's favourite extras, under the Overview's head (#111). */

const nav = {
  state: 'ok',
  data: {
    groups: [
      { key: 'cluster', label: 'Cluster', deferred: false, items: [{ text: 'Overview', href: '/c/prod' }] },
      {
        key: 'extra',
        label: 'Extra',
        deferred: false,
        items: [
          { text: 'Delivery Center', href: '/c/prod/x/argocd' },
          { text: 'Pod Debugging', href: '/c/prod/x/pending' },
          { text: 'Storage Inspector', href: '/c/prod/x/storage' },
        ],
      },
    ],
  },
};

beforeEach(() => {
  resetKindsMemory();
  localStorage.clear();
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url).split('?')[0];
    const body = path === '/api/c/prod/nav' ? nav : {};
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetKindsMemory();
  localStorage.clear();
});

/** Pod Debugging, as the build ships it: reached from a Pending pod, no menu entry of its own. */
const contextual: Extension[] = [{ key: 'pending', icon: 'hourglass', nav: { label: 'Pod Debugging', path: 'x/pending', menu: false }, routes: [] }];

function draw(favourites: string[], extensions?: Extension[]) {
  localStorage.setItem('kubetower.kinds', JSON.stringify({ showUnused: false, favourites: { prod: favourites } }));
  const inside = <FavouriteExtras cluster="prod" />;
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>{extensions === undefined ? inside : <ShellProvider extensions={extensions}>{inside}</ShellProvider>}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const links = (): [string | null, string | null][] =>
  [...document.querySelectorAll('[data-favourite-extras] a')].map((a) => [a.getAttribute('href'), a.textContent]);

describe('the favourite extras on the Overview', () => {
  it('links each favourite extra the menu offers, in the order marked, and no kind', async () => {
    draw(['extra:storage', 'pods', 'extra:argocd', 'extra:gone']);
    await waitFor(() => {
      expect(links()).toEqual([
        ['/c/prod/x/storage', 'Storage Inspector'],
        ['/c/prod/x/argocd', 'Delivery Center'],
      ]);
    });
  });

  it('draws nothing without a favourite extra', async () => {
    draw(['pods']);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(document.querySelector('[data-favourite-extras]')).toBeNull();
  });

  it('leaves out an extra the menu does not list, as the menu does', async () => {
    draw(['extra:pending', 'extra:storage'], contextual);
    await waitFor(() => {
      expect(links()).toEqual([['/c/prod/x/storage', 'Storage Inspector']]);
    });
  });

  it('reads only the extras among the favourites, whatever a kind is called', () => {
    expect(favouriteExtraKeys(['pods', 'extra:storage', 'storage', 'extra:argocd'])).toEqual(['storage', 'argocd']);
  });
});
