import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp } from '../testing/harness';
import { RELEASES, replaceReleases, type Release } from './changelog';
import { resetWhatsNew } from './WhatsNew';

// A changelog of fake releases, in place of this repository's.
const real = RELEASES;
const FAKE: Release[] = [
  { version: '0.3.0', highlights: [{ title: 'Saved views', kind: 'added' }, { title: 'Pod logs across restarts', kind: 'fixed' }] },
  { version: '0.2.1', highlights: [] },
  { version: '0.2.0', highlights: [{ title: "What's new", kind: 'added' }] },
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
function console(release: string | undefined, where: { desktop: boolean; seen?: string; refuse?: boolean }): Console {
  const out: Console = { sent: [] };
  let seen = where.seen;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url).split('?')[0] ?? '';
    const json = (status: number, body: unknown): Promise<Response> =>
      Promise.resolve(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
    if ((init?.method ?? 'GET') !== 'GET') {
      const body: unknown = init?.body === undefined ? undefined : JSON.parse(String(init.body));
      out.sent.push({ method: init?.method ?? '', path, body });
      if (path === '/api/preferences/seen-version') {
        if (where.refuse === true) {
          return json(500, { status: 500, title: 'Internal Server Error', code: 'not_saved', detail: 'state directory not writable' });
        }
        seen = (body as { version: string }).version;
      }
      return json(204, null);
    }
    switch (path) {
      case '/api/session':
        return json(200, { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' });
      case '/api/clusters':
        // prod is the second cluster: Settings, asked for none, would take dev.
        return json(200, {
          state: 'ok',
          items: [
            { name: 'dev', label: 'dev', state: 'ok' },
            { name: 'prod', label: 'prod', state: 'ok' },
          ],
        });
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
      case '/api/c/prod/extensions':
        return json(200, {
          state: 'ok',
          items: [
            { key: 'events', label: 'Event Intelligence', enabled: true, pinned: false, writable: true },
            { key: 'netinspect', label: 'Network Inspector', enabled: false, pinned: false, writable: true },
            { key: 'runtime', label: 'Runtime Inspector', enabled: true, pinned: false, writable: true },
          ],
        });
      default:
        return json(404, { status: 404, title: 'Not Found', code: 'not_found', detail: `no stub for ${path}` });
    }
  });
  return out;
}

/** The dialog's content box, marked with what it shows. */
const content = (): HTMLElement | null => document.querySelector<HTMLElement>('[data-whats-new]');
/** The dialog itself: the one an extra's About opens. */
const dialog = (): HTMLElement | null => content()?.closest<HTMLElement>('[role="dialog"]') ?? null;

async function shown(kind: string): Promise<HTMLElement> {
  await waitFor(() => {
    expect(content()?.dataset['whatsNew']).toBe(kind);
  });
  return content() as HTMLElement;
}

/** Long enough for every read to answer: then an absent dialog is a finding. */
async function settled(): Promise<void> {
  await waitFor(() => {
    expect(screen.getAllByText('prod').length).toBeGreaterThan(0);
  });
  await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
}

const titleOf = (): string | null | undefined => dialog()?.querySelector('h2')?.textContent;

describe("What's new", () => {
  it('welcomes a first visit in a centred dialog, with the mark and a card per view', async () => {
    console('0.3.0', { desktop: false });
    mountApp('/');
    const box = await shown('welcome');
    // The shared Dialog: modal, named by its heading, and no top band in the page.
    expect(dialog()?.getAttribute('aria-modal')).toBe('true');
    expect(screen.getByRole('dialog', { name: 'Welcome to KubeTower' })).toBe(dialog());
    expect(box.querySelector('[data-kubetower-mark]')).not.toBeNull();
    const cards = [...box.querySelectorAll<HTMLElement>('[data-whats-new-card]')];
    expect(cards.map((card) => card.querySelector('.font-medium')?.textContent)).toEqual([
      'Clusters',
      'Overview',
      'Network Inspector',
      'Runtime Inspector',
      'Event Intelligence',
      'Extras',
    ]);
    // Under the mark, the site's tagline and intro.
    expect(box.querySelector('[data-whats-new-tagline]')?.textContent).toBe('The free Kubernetes console.');
    expect(box.querySelector('[data-whats-new-intro]')?.textContent).not.toBe('');
    // What each holds is its line, drawn and describing the link: no title.
    for (const card of cards) {
      const tip = document.getElementById(card.getAttribute('aria-describedby') ?? '');
      expect(tip?.textContent).not.toBe('');
      expect(card.contains(tip)).toBe(true);
      expect(tip?.className).not.toContain('sr-only');
      expect(card.hasAttribute('title')).toBe(false);
    }
    expect(screen.getByRole('button', { name: 'Get started' })).toBeDefined();
    // The focus goes into the dialog.
    await waitFor(() => {
      expect(dialog()?.contains(document.activeElement)).toBe(true);
    });
  });

  it('leads each card to its view on the cluster in the address, or to Settings › Extra where the extra is off', async () => {
    console('0.3.0', { desktop: false });
    const { router } = mountApp('/c/prod');
    const box = await shown('welcome');
    const href = (key: string): string | null | undefined => box.querySelector(`[data-whats-new-card="${key}"]`)?.getAttribute('href');
    await waitFor(() => {
      expect(href('events')).toBe('/c/prod/x/events');
    });
    expect(href('clusters')).toBe('/');
    expect(href('overview')).toBe('/c/prod');
    expect(href('runtime')).toBe('/c/prod/x/runtime');
    // Settings › Extra for this cluster, the second listed, never the first.
    expect(href('netinspect')).toBe('/settings/extra?cluster=prod');
    expect(href('extras')).toBe('/settings/extra?cluster=prod');
    fireEvent.click(box.querySelector('[data-whats-new-card="events"]') as HTMLElement);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/prod/x/events');
    });
    // A card closes it for the release, as Get started does.
    expect(content()).toBeNull();
    expect(localStorage.getItem('kubetower.seenVersion')).toBe('0.3.0');
  });

  it('opens Settings › Extra on the cluster in the address, with its way back', async () => {
    console('0.3.0', { desktop: false });
    const { router } = mountApp('/c/prod');
    const box = await shown('welcome');
    const card = (): HTMLElement | null => box.querySelector<HTMLElement>('[data-whats-new-card="netinspect"]');
    await waitFor(() => {
      expect(card()?.getAttribute('href')).toBe('/settings/extra?cluster=prod');
    });
    fireEvent.click(card() as HTMLElement);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/extra');
    });
    expect(router.state.location.search).toBe('?cluster=prod');
    // Opened the console's way: the entry carries where Settings came from.
    expect(router.state.location.state).not.toBeNull();
    expect(content()).toBeNull();
  });

  it('leaves the dialog and the release alone on a click meant for another tab', async () => {
    console('0.3.0', { desktop: false });
    const { router } = mountApp('/c/prod');
    const box = await shown('welcome');
    const card = box.querySelector<HTMLElement>('[data-whats-new-card="overview"]') as HTMLElement;
    for (const modifier of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { button: 1 }]) {
      fireEvent.click(card, modifier);
    }
    expect(content()).not.toBeNull();
    expect(localStorage.getItem('kubetower.seenVersion')).toBeNull();
    expect(router.state.location.pathname).toBe('/c/prod');
  });

  it("gives What's new its version, an icon and a badge per highlight, and the changelog", async () => {
    localStorage.setItem('kubetower.seenVersion', '0.1.2');
    console('0.3.0', { desktop: false });
    mountApp('/');
    const box = await shown('news');
    expect(titleOf()).toBe("What's new 0.3.0");
    expect([...box.querySelectorAll('[data-whats-new-release]')].map((one) => one.getAttribute('data-whats-new-release'))).toEqual([
      '0.3.0',
      '0.2.0',
    ]);
    const items = [...box.querySelectorAll<HTMLElement>('[data-highlight]')];
    expect(items.map((one) => [one.dataset['highlight'], one.textContent])).toEqual([
      ['added', 'Saved viewsAdded'],
      ['fixed', 'Pod logs across restartsFixed'],
      ['added', "What's newAdded"],
    ]);
    // Added and Fixed differ by their glyph, not by a sentence.
    expect(items.map((one) => one.querySelector('use')?.getAttribute('href'))).toEqual(['#i-plus', '#i-check', '#i-plus']);
    await waitFor(() => {
      expect(box.querySelector('[data-whats-new-changelog]')?.getAttribute('href')).toBe(
        'https://github.com/thonicdev/kubetower/blob/main/CHANGELOG.md',
      );
    });
    expect(screen.getByRole('button', { name: 'Close' })).toBeDefined();
  });

  it('shows nothing on the version already seen', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.3.0');
    console('0.3.0', { desktop: false });
    mountApp('/');
    await settled();
    expect(content()).toBeNull();
  });

  it('falls back to the cards when nothing notable came since', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.2.0');
    console('0.2.1', { desktop: false });
    mountApp('/');
    const box = await shown('tour');
    expect(titleOf()).toBe('KubeTower 0.2.1');
    expect(box.querySelectorAll('[data-whats-new-card]')).toHaveLength(6);
  });

  it('shows nothing on a development build', async () => {
    console(undefined, { desktop: false });
    mountApp('/');
    await settled();
    expect(content()).toBeNull();
  });

  it('closes with Get started and stays closed in this browser', async () => {
    console('0.3.0', { desktop: false });
    const first = mountApp('/');
    await shown('welcome');
    fireEvent.click(screen.getByRole('button', { name: 'Get started' }));
    expect(content()).toBeNull();
    expect(localStorage.getItem('kubetower.seenVersion')).toBe('0.3.0');
    first.view.unmount();
    resetWhatsNew();
    mountApp('/');
    await settled();
    expect(content()).toBeNull();
  });

  it('closes on a click on the veil, and not on one inside the box', async () => {
    console('0.3.0', { desktop: false });
    mountApp('/');
    const box = await shown('welcome');
    fireEvent.mouseDown(box);
    fireEvent.click(box);
    expect(content()).not.toBeNull();
    const veil = dialog()?.parentElement as HTMLElement;
    fireEvent.mouseDown(veil);
    fireEvent.click(veil);
    expect(content()).toBeNull();
    expect(localStorage.getItem('kubetower.seenVersion')).toBe('0.3.0');
  });

  it('keeps the release seen on the desktop, through the preferences, closed by Escape', async () => {
    const c = console('0.3.0', { desktop: true, seen: '0.2.0' });
    const first = mountApp('/');
    await shown('news');
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
    expect(content()).toBeNull();
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
    expect(content()).toBeNull();
  });

  it('opens again from Settings › Infos, takes the focus, and gives it back on close', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.3.0');
    console('0.3.0', { desktop: false });
    mountApp('/settings/infos');
    const open = await openButton();
    expect(content()).toBeNull();
    open.focus();
    fireEvent.click(open);
    const box = await shown('news');
    expect([...box.querySelectorAll<HTMLElement>('[data-highlight]')].map((one) => one.dataset['highlight'])).toEqual(['added', 'fixed']);
    await waitFor(() => {
      expect(dialog()?.contains(document.activeElement)).toBe(true);
    });
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(content()).toBeNull();
    await waitFor(() => {
      expect(document.activeElement).toBe(open);
    });
  });

  it('records nothing when an older build, opened from Infos, is closed', async () => {
    localStorage.setItem('kubetower.seenVersion', '0.3.0');
    console('0.2.0', { desktop: false });
    mountApp('/settings/infos');
    fireEvent.click(await openButton());
    await shown('news');
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(content()).toBeNull();
    expect(localStorage.getItem('kubetower.seenVersion')).toBe('0.3.0');
  });

  it('comes back saying so when the desktop could not keep it', async () => {
    console('0.3.0', { desktop: true, seen: '0.2.0', refuse: true });
    mountApp('/');
    await shown('news');
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    const box = await shown('news');
    await waitFor(() => {
      expect(box.querySelector('[data-whats-new-failed]')?.textContent).toBe('Not saved');
    });
    // Inside a status region that was there before it, so it is announced.
    expect(box.querySelector('[data-whats-new-failed]')?.closest('[role="status"]')).not.toBeNull();
  });
});

async function openButton(): Promise<HTMLElement> {
  let open: HTMLElement | null = null;
  await waitFor(() => {
    open = document.querySelector<HTMLElement>('[data-whats-new-open]');
    expect(open).not.toBeNull();
  });
  return open as unknown as HTMLElement;
}
