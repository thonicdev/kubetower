import { act, cleanup, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch } from '../testing/harness';
import { NARROW_QUERY, resetMenuMode, resetOpenGroups } from './menuMemory';
import { resetNavMemory } from './navMemory';

/**
 * At 390px the kinds column beside the rail would leave the page about 150px,
 * so on a phone-width window it is closed by default and opens over
 * the page, and leaves once used.
 */

function forgetEverything(): void {
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  localStorage.clear();
}

let width = { narrow: false };
const changes = new Set<() => void>();

function windowWidth(narrow: boolean): void {
  width = { narrow };
  vi.stubGlobal('matchMedia', (query: string) => ({
    get matches() {
      return width.narrow && query === NARROW_QUERY;
    },
    media: query,
    addEventListener: (_: string, listener: () => void) => changes.add(listener),
    removeEventListener: (_: string, listener: () => void) => changes.delete(listener),
  }));
}

/** The window crossing the breakpoint, as the browser reports it. */
function resize(narrow: boolean): void {
  width.narrow = narrow;
  act(() => {
    for (const listener of [...changes]) listener();
  });
}

beforeEach(forgetEverything);
afterEach(() => {
  cleanup();
  changes.clear();
  vi.unstubAllGlobals();
  forgetEverything();
});

function serve(extra: Record<string, unknown> = {}): void {
  stubFetch({
    '/api/session': {
      body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
    },
    '/api/clusters': {
      body: {
        state: 'ok',
        items: [
          { name: 'prod', label: 'prod', state: 'ok' },
          { name: 'dev', label: 'dev', state: 'ok' },
        ],
      },
    },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: { groups: [{ key: 'workloads', label: 'Workloads', items: [{ text: 'Pods', href: '/c/prod/pods' }] }] },
      },
    },
    ...extra,
  } as never);
}

function menu(): HTMLElement {
  const element = document.getElementById('menu');
  if (element === null) throw new Error('#menu is missing');
  return element;
}

function kinds(): HTMLButtonElement {
  const found = document.querySelector('button[aria-controls="menu"]');
  if (!(found instanceof HTMLButtonElement)) throw new Error('no Kinds button');
  return found;
}

const backdrop = (): Element | null => document.querySelector('[data-menu-backdrop]');

async function open(): Promise<void> {
  await waitFor(() => {
    expect(kinds()).toBeDefined();
  });
  expect(menu().hasAttribute('data-hidden')).toBe(true);
  fireEvent.click(kinds());
  await waitFor(() => {
    expect(menu().hasAttribute('data-hidden')).toBe(false);
  });
}

describe('the kinds column on a phone-width window', () => {
  it('is closed by default, even where the wide window keeps it open', async () => {
    windowWidth(true);
    localStorage.setItem('kubetower.menu.mode', 'shown');
    serve();
    mountApp('/c/prod');
    await waitFor(() => {
      expect(kinds().getAttribute('aria-expanded')).toBe('false');
    });
    expect(menu().hasAttribute('data-hidden')).toBe(true);
  });

  it('opens over the page, and following a kind closes it and navigates', async () => {
    windowWidth(true);
    serve();
    const { router } = mountApp('/c/prod');
    await open();
    expect(menu().className).toContain('absolute');
    expect(backdrop()).not.toBeNull();
    await waitFor(() => {
      expect(menu().querySelector('a[href="/c/prod/pods"]')).not.toBeNull();
    });

    fireEvent.click(menu().querySelector('a[href="/c/prod/pods"]') as Element);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/pods');
    });
    expect(menu().hasAttribute('data-hidden')).toBe(true);
    expect(backdrop()).toBeNull();
  });

  it('closes on Escape and on a tap beside it', async () => {
    windowWidth(true);
    serve();
    mountApp('/c/prod');
    await open();
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(menu().hasAttribute('data-hidden')).toBe(true);
    });

    fireEvent.click(kinds());
    await waitFor(() => {
      expect(backdrop()).not.toBeNull();
    });
    fireEvent.click(backdrop() as Element);
    await waitFor(() => {
      expect(menu().hasAttribute('data-hidden')).toBe(true);
    });
  });

  it('is unchanged on a wide window: beside the page, open, no backdrop', async () => {
    windowWidth(false);
    serve();
    mountApp('/c/prod');
    await waitFor(() => {
      expect(menu().querySelector('a[href="/c/prod/pods"]')).not.toBeNull();
    });
    expect(menu().hasAttribute('data-hidden')).toBe(false);
    expect(menu().className).not.toContain('absolute');
    expect(backdrop()).toBeNull();
  });

  it('leaves Escape to the jump box while it is being typed in', async () => {
    windowWidth(true);
    serve();
    mountApp('/c/prod');
    await open();
    const box = menu().querySelector('input');
    if (!(box instanceof HTMLInputElement)) throw new Error('no jump box');
    box.focus();
    fireEvent.change(box, { target: { value: 'po' } });
    fireEvent.keyDown(box, { key: 'Escape' });
    expect(menu().hasAttribute('data-hidden')).toBe(false);
  });

  it('starts closed again after the window was widened with it open', async () => {
    windowWidth(true);
    serve();
    mountApp('/c/prod');
    await open();
    resize(false);
    resize(true);
    await waitFor(() => {
      expect(menu().hasAttribute('data-hidden')).toBe(true);
    });
  });

  // The in-cluster console has its Kinds button in the bottom left corner,
  // so the phone-width overlay applies there too.
  it('is closed by default on the in-cluster console too, with Kinds in the corner', async () => {
    windowWidth(true);
    serve({
      '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
      '/api/capabilities': {
        body: { mode: 'shared', localShell: false, podExec: true, nodeShell: false, assistant: false, portForward: false },
      },
    });
    mountApp('/c/prod');
    await waitFor(() => {
      expect(kinds().dataset['kindsAt']).toBe('corner');
    });
    expect(menu().hasAttribute('data-hidden')).toBe(true);
    fireEvent.click(kinds());
    await waitFor(() => {
      expect(menu().className).toContain('absolute');
    });
  });
});
