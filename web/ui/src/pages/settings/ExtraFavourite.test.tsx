import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { ExtensionInfo } from '../../api';
import { resetKindsMemory } from '../../shell/kindsMemory';
import { ExtraTab } from './ExtraTab';

/** The star on each card of Settings > Extra (#111). */

const row = (key: string, label: string): ExtensionInfo => ({ key, label, enabled: true, pinned: false, writable: true, settings: [] });
const items = [row('storage', 'Storage Inspector'), row('argocd', 'Delivery Center'), row('pending', 'Pod Debugging')];

beforeEach(() => {
  resetKindsMemory();
  localStorage.clear();
  vi.stubGlobal('fetch', () => Promise.resolve(new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } })));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetKindsMemory();
  localStorage.clear();
});

function draw() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <ExtraTab cluster="prod" clusters={[]} items={items} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const star = (key: string): HTMLButtonElement => document.querySelector<HTMLButtonElement>(`[data-extra-favourite="${key}"]`) as HTMLButtonElement;
const order = (): (string | null)[] => [...document.querySelectorAll('[data-extra]')].map((one) => one.getAttribute('data-extra'));

describe('Settings > Extra', () => {
  it('marks and unmarks a favourite of this cluster, kept with the kinds, and leaves the cards in their order', async () => {
    draw();
    await waitFor(() => {
      expect(star('pending').disabled).toBe(false);
    });
    expect(star('pending').getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(star('pending'));
    fireEvent.click(star('storage'));
    await waitFor(() => {
      expect(star('pending').getAttribute('aria-pressed')).toBe('true');
    });
    expect(JSON.parse(localStorage.getItem('kubetower.kinds') ?? '{}')).toMatchObject({ favourites: { prod: ['extra:pending', 'extra:storage'] } });
    expect(order()).toEqual(['argocd', 'pending', 'storage']);

    fireEvent.click(star('pending'));
    await waitFor(() => {
      expect(star('pending').getAttribute('aria-pressed')).toBe('false');
    });
    expect(JSON.parse(localStorage.getItem('kubetower.kinds') ?? '{}')).toMatchObject({ favourites: { prod: ['extra:storage'] } });
  });

  it('reads the favourites back', async () => {
    localStorage.setItem('kubetower.kinds', JSON.stringify({ showUnused: false, favourites: { prod: ['extra:argocd'], staging: ['extra:storage'] } }));
    draw();
    await waitFor(() => {
      expect(star('argocd').getAttribute('aria-pressed')).toBe('true');
    });
    expect(star('storage').getAttribute('aria-pressed')).toBe('false');
  });
});
