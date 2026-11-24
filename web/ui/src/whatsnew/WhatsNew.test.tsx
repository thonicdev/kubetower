import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp } from '../testing/harness';
import { RELEASES, replaceReleases, type Release } from './changelog';
import { resetWhatsNew } from './WhatsNew';

// A changelog of fake releases, in place of this repository's.
const real = RELEASES;
const FAKE: Release[] = [
  { version: '0.3.0', highlights: ['Saved views', 'Pod logs across restarts'] },
  { version: '0.2.1', highlights: [] },
  { version: '0.2.0', highlights: ["What's new"] },
  { version: '0.1.2', highlights: [] },
];
beforeEach(() => {
  replaceReleases(FAKE);
});

afterEach(() => {
  replaceReleases(real);
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetWhatsNew();
});

interface Console {
  sent: { method: string; path: string; body: unknown }[];
}

/** A signed-in console on `release`, the desktop (`seen` kept by the server) or a server one. */
function console(release: string | undefined, where: { desktop: boolean; seen?: string }): Console {
  const out: Console = { sent: [] };
  let seen = where.seen;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url).split('?')[0] ?? '';
    const json = (status: number, body: unknown): Promise<Response> =>
      Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
    if ((init?.method ?? 'GET') !== 'GET') {
      const body: unknown = init?.body === undefined ? undefined : JSON.parse(String(init.body));
      out.sent.push({ method: init?.method ?? '', path, body });
      if (path === '/api/preferences/seen-version') seen = (body as { version: string }).version;
      return json(204, null);
    }
    switch (path) {
      case '/api/session':
        return json(200, { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' });
      case '/api/clusters':
        return json(200, { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] });
      case '/api/capabilities':
        return json(200, {
          mode: where.desktop ? 'desktop' : 'shared',
          preferences: where.desktop,
          replicated: false,
          kindPreferences: false,
          podExec: true,
          nodeShell: false,
          assistant: false,
          portForward: true,
          ...(release === undefined ? {} : { release }),
        });
      case '/api/preferences':
        return json(200, {
          rail: { barCount: 9, activeBar: 1, bars: [[]] },
          columns: { density: 'comfortable', kinds: {} },
          ...(seen === undefined ? {} : { seenVersion: seen }),
        });
      case '/api/settings':
        return json(200, { info: { repositoryUrl: 'https://github.com/thonicdev/kubetower' } });
      default:
        return json(404, { status: 404, title: 'Not Found', code: 'not_found', detail: `no stub for ${path}` });
    }
  });
  return out;
}

const banner = (): HTMLElement | null => document.querySelector<HTMLElement>('[data-whats-new]');

async function shown(kind: string): Promise<HTMLElement> {
  await waitFor(() => {
    expect(banner()?.dataset['whatsNew']).toBe(kind);
  });
  return banner() as HTMLElement;
}

/** Long enough for every read to answer: then an absent banner is a finding. */
async function settled(): Promise<void> {
  await waitFor(() => {
    expect(screen.getAllByText('prod').length).toBeGreaterThan(0);
  });
  await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
}

describe("What's new", () => {
  it('welcomes a first visit with the tour', async () => {
    console('0.3.0', { desktop: false });
    mountApp('/');
    const box = await shown('welcome');
    expect(box.querySelector('h2')?.textContent).toBe('Welcome to KubeTower');
    expect(box.querySelectorAll('[data-whats-new-tour] li')).toHaveLength(5);
  });

  it('aggregates the highlights of every release since the one seen, with the changelog', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.1.2');
    console('0.3.0', { desktop: false });
    mountApp('/');
    const box = await shown('news');
    expect(box.querySelector('h2')?.textContent).toBe("What's new");
    expect([...box.querySelectorAll('[data-whats-new-release]')].map((one) => one.getAttribute('data-whats-new-release'))).toEqual([
      '0.3.0',
      '0.2.0',
    ]);
    expect([...box.querySelectorAll('li')].map((one) => one.textContent)).toEqual(['Saved views', 'Pod logs across restarts', "What's new"]);
    expect(box.querySelector('[data-whats-new-changelog]')?.getAttribute('href')).toBe(
      'https://github.com/thonicdev/kubetower/blob/main/CHANGELOG.md',
    );
  });

  it('shows nothing on the version already seen', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.3.0');
    console('0.3.0', { desktop: false });
    mountApp('/');
    await settled();
    expect(banner()).toBeNull();
  });

  it('falls back to the tour when nothing notable came since', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.2.0');
    console('0.2.1', { desktop: false });
    mountApp('/');
    const box = await shown('tour');
    expect(box.querySelector('h2')?.textContent).toBe('KubeTower');
  });

  it('shows nothing on a development build', async () => {
    console(undefined, { desktop: false });
    mountApp('/');
    await settled();
    expect(banner()).toBeNull();
  });

  it('closes with one click and stays closed in this browser', async () => {
    console('0.3.0', { desktop: false });
    const first = mountApp('/');
    await shown('welcome');
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(banner()).toBeNull();
    expect(localStorage.getItem('kubetower.seenVersion')).toBe('0.3.0');
    first.view.unmount();
    resetWhatsNew();
    mountApp('/');
    await settled();
    expect(banner()).toBeNull();
  });

  it('keeps the release seen on the desktop, through the preferences', async () => {
    const c = console('0.3.0', { desktop: true, seen: '0.2.0' });
    const first = mountApp('/');
    const box = await shown('news');
    fireEvent.keyDown(box, { key: 'Escape' });
    expect(banner()).toBeNull();
    await waitFor(() => {
      // Only the What's new write: the desktop's rail writes its own.
      expect(c.sent.filter((one) => one.path === '/api/preferences/seen-version')).toEqual([
        { method: 'POST', path: '/api/preferences/seen-version', body: { version: '0.3.0' } },
      ]);
    });
    expect(localStorage.getItem('kubetower.seenVersion')).toBeNull();
    first.view.unmount();
    resetWhatsNew();
    mountApp('/');
    await settled();
    expect(banner()).toBeNull();
  });

  it('opens again from Settings › Infos, on this release, and takes the focus', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.3.0');
    console('0.3.0', { desktop: false });
    mountApp('/settings/infos');
    let open: HTMLElement | null = null;
    await waitFor(() => {
      open = document.querySelector<HTMLElement>('[data-whats-new-open]');
      expect(open).not.toBeNull();
    });
    expect(banner()).toBeNull();
    fireEvent.click(open as unknown as HTMLElement);
    const box = await shown('news');
    expect([...box.querySelectorAll('li')].map((one) => one.textContent)).toEqual(['Saved views', 'Pod logs across restarts']);
    await waitFor(() => {
      expect(document.activeElement).toBe(box);
    });
  });
});
