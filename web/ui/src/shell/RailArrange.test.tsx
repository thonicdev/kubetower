import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch } from '../testing/harness';
import { resetAnnouncements } from './announce';
import { settleIcon } from './firstIcon';
import { resetOpenGroups } from './menuMemory';

// Both clusters of every fixture here were given their first icon already,
// so the writes these tests count are Arrange's alone.
beforeEach(() => {
  settleIcon('prod');
  settleIcon('staging');
});

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetAnnouncements();
  resetOpenGroups();
  localStorage.clear();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

function stub(): void {
  stubFetch({
    '/api/session': session,
    '/api/clusters': {
      body: {
        state: 'ok',
        items: [
          { name: 'prod', label: 'prod', state: 'ok', order: 0 },
          { name: 'staging', label: 'staging', state: 'ok', order: 1 },
        ],
      },
    },
    '/api/preferences': { body: { state: 'ok', data: {} } },
    '/api/preferences/rail/hide': { status: 204 },
    '/api/preferences/rail/reveal': { status: 204 },
    '/api/preferences/rail/move': { status: 204 },
  });
}

function said(): string {
  const regions = Array.from(document.querySelectorAll('[role="status"]'));
  return regions.map((region) => region.textContent ?? '').join(' ');
}

async function openArrange(): Promise<void> {
  stub();
  mountApp('/');
  await waitFor(() => {
    expect(screen.getByRole('button', { name: 'Arrange' })).toBeTruthy();
  });
  fireEvent.click(screen.getByRole('button', { name: 'Arrange' }));
  await waitFor(() => {
    expect(screen.getByRole('dialog')).toBeTruthy();
  });
}

/**
 * Every outcome of a rearrangement is announced in a live region: without
 * it every rearrangement happens in silence to a screen
 * reader - the only thing that changes is a list of tiles somewhere else on the
 * page.
 */
describe('every change to the rail says what it did', () => {
  it('announces a tile taken off the rail', async () => {
    await openArrange();

    fireEvent.click(screen.getAllByRole('button', { name: 'Hide' })[0] as HTMLElement);

    await waitFor(() => {
      expect(said()).toContain('prod taken off the rail.');
    });
  });

  it('names the neighbor a tile moved past, rather than a position', async () => {
    await openArrange();

    fireEvent.click(screen.getByRole('button', { name: 'Move staging up' }));

    // "Moved to position 1" is a number a person cannot see. The same
    // argument `hotbars.Place` makes about posting a neighbor and a side rather
    // than an index.
    await waitFor(() => {
      expect(said()).toContain('staging moved above prod.');
    });
  });

  it('says nothing when the change was refused, because the alert already does', async () => {
    stubFetch({
      '/api/session': session,
      // Two, so this is the rail's Arrange: with one cluster the rail is not
      // drawn and Arrange is in the header (`Shell.test.tsx`).
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok' },
            { name: 'staging', label: 'staging', state: 'ok' },
          ],
        },
      },
      '/api/preferences': { body: { state: 'ok', data: {} } },
      '/api/preferences/rail/hide': {
        status: 500,
        contentType: 'application/problem+json',
        body: { status: 500, title: 'Internal Server Error', code: 'internal', detail: 'disk full' },
      },
    });
    mountApp('/');
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Arrange' })).toBeTruthy();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Arrange' }));
    fireEvent.click((await screen.findAllByRole('button', { name: 'Hide' }))[0] as HTMLElement);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeTruthy();
    });
    // Saying it twice is the same defect as not saying it at all, arriving from
    // the other side.
    expect(said()).not.toContain('taken off the rail');
  });
});

/*
  A swap is two writes, and the server can apply the first and fail to write
  it down: `not_saved`, a 500 that says the change is in effect now and gone at
  the next start. Stopping after the first write would leave half a swap. The
  second write is still sent, the sentence is still shown, and the rail is read again,
  because the server's arrangement did change.
*/
describe('a swap the server applied but could not save', () => {
  it('sends both halves, says so, and draws what the server now holds', async () => {
    const notSaved = {
      status: 500,
      contentType: 'application/problem+json',
      body: {
        status: 500,
        title: 'Applied, not saved',
        code: 'not_saved',
        detail: 'not saved: in effect now, but it will not survive a restart: writing hotbars.json (disk full)',
      },
    };
    stubFetch({
      '/api/session': session,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok', order: 0 },
            { name: 'staging', label: 'staging', state: 'ok', order: 1 },
          ],
        },
      },
      '/api/preferences': { body: { state: 'ok', data: {} } },
      '/api/preferences/rail/move': notSaved,
    });
    const posted = capturePosts();
    const reads: string[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if ((init?.method ?? 'GET') === 'GET') reads.push(path);
      return stubbed(input, init);
    });
    mountApp('/');
    fireEvent.click(await screen.findByRole('button', { name: 'Arrange' }));
    const arrow = await screen.findByRole('button', { name: 'Move staging up' });
    const clusterReads = (): number => reads.filter((path) => path === '/api/clusters').length;
    const before = clusterReads();
    fireEvent.click(arrow);

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('will not survive a restart');
    });
    expect(posted).toEqual([
      { path: '/api/preferences/rail/move', body: { cluster: 'staging', order: 0 } },
      { path: '/api/preferences/rail/move', body: { cluster: 'prod', order: 1 } },
    ]);
    // Read again, because the server's arrangement did change. (The rail's own
    // interval is 30 seconds, so nothing else reads it inside this test.)
    expect(clusterReads()).toBeGreaterThan(before);
  });
});

/** `confirm.js`'s three gestures, through `components/Dialog`. */
describe('the arrange dialog behaves like a dialog', () => {
  it('closes on Escape', async () => {
    await openArrange();
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
  });

  it('gives the keyboard back to the Arrange button', async () => {
    stub();
    mountApp('/');
    const opener = await screen.findByRole('button', { name: 'Arrange' });
    // Focused explicitly: `fireEvent.click` does not move the keyboard, and the
    // behavior under test is about where the keyboard *was*.
    opener.focus();
    fireEvent.click(opener);
    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeTruthy();
    });

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    expect(document.activeElement).toBe(opener);
  });
});

/** Every POST the dialog makes, by path and body. */
function capturePosts(): { path: string; body: unknown }[] {
  const posted: { path: string; body: unknown }[] = [];
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
    if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
    return stubbed(input, init);
  });
  return posted;
}

/*
  Arrange is the nameplate editor for every cluster at once, one row each, and
  not a table - a table column is as wide as its widest cell, so one ARN would
  put a horizontal scrollbar under the dialog (the pixels are `names.spec.ts`
  case 12).
*/
describe('Arrange is the nameplate editor for every cluster', () => {
  function stubWithPlates(): void {
    stubFetch({
      '/api/session': session,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'Production', initials: 'PR', colour: 'amber', state: 'ok', order: 0 },
            { name: 'staging', label: 'staging', initials: 'S', colour: 'teal-pale', state: 'ok', order: 1 },
          ],
        },
      },
      '/api/preferences': {
        body: { rail: { barCount: 3, activeBar: 1, bars: [['prod', 'staging']], hidden: [], nameplates: { prod: { name: 'Production', colour: 'amber' } } } },
      },
      '/api/preferences/rail/nameplate': { status: 204 },
    });
  }

  async function open(): Promise<HTMLElement> {
    mountApp('/');
    fireEvent.click(await screen.findByRole('button', { name: 'Arrange' }));
    return screen.findByRole('dialog');
  }

  it('draws a row per cluster with its tile, name, initials and color, and no table', async () => {
    stubWithPlates();
    const dialog = await open();

    await waitFor(() => {
      expect(dialog.querySelectorAll('li[data-cluster]')).toHaveLength(2);
    });
    expect(dialog.querySelector('table')).toBeNull();
    const prod = dialog.querySelector<HTMLElement>('li[data-cluster="prod"]')!;
    // The fields start from what was stored: a name, no initials (so the
    // derived ones are the placeholder), and the stored swatch chosen.
    expect((screen.getByRole('textbox', { name: 'Name for prod' }) as HTMLInputElement).value).toBe('Production');
    const initials = screen.getByRole('textbox', { name: 'Initials for prod' }) as HTMLInputElement;
    expect(initials.value).toBe('');
    expect(initials.placeholder).toBe('PR');
    expect(initials.maxLength).toBe(3);
    const grid = prod.querySelector('[role="radiogroup"]')!;
    expect(grid.querySelectorAll('[role="radio"]')).toHaveLength(16);
    expect(grid.querySelector('[aria-checked="true"]')?.getAttribute('aria-label')).toBe('Amber');
  });

  it('draws the sixteen inline from md up and one color button below, both the same choice', async () => {
    stubWithPlates();
    const dialog = await open();
    await waitFor(() => {
      expect(dialog.querySelectorAll('li[data-cluster]')).toHaveLength(2);
    });
    const prod = dialog.querySelector<HTMLElement>('li[data-cluster="prod"]')!;
    // jsdom has no viewport: the pair of classes is what is pinned here, and
    // the pixels are the layout suite's.
    expect(prod.querySelector('[role="radiogroup"]')?.parentElement?.className).toContain('hidden md:inline-flex');
    const button = prod.querySelector<HTMLElement>('[data-colour-button]')!;
    expect(button.parentElement?.parentElement?.className).toContain('md:hidden');
    expect(button.getAttribute('aria-label')).toBe('Color for prod: Amber');
  });

  it('writes the color alone when a swatch is picked', async () => {
    stubWithPlates();
    const posted = capturePosts();
    const dialog = await open();
    await waitFor(() => {
      expect(dialog.querySelectorAll('li[data-cluster]')).toHaveLength(2);
    });
    const staging = dialog.querySelector<HTMLElement>('li[data-cluster="staging"]')!;
    fireEvent.click(staging.querySelector('[role="radiogroup"] [aria-label="Violet"]')!);

    await waitFor(() => {
      expect(posted).toEqual([{ path: '/api/preferences/rail/nameplate', body: { cluster: 'staging', colour: 'violet' } }]);
    });
  });

  it('moves a stored icon onto the color picked here, as the tile menu does', async () => {
    stubFetch({
      '/api/session': session,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'Production', initials: 'PR', colour: 'amber', state: 'ok', order: 0 },
            { name: 'staging', label: 'staging', initials: 'S', colour: 'teal-pale', state: 'ok', order: 1 },
          ],
        },
      },
      '/api/preferences': {
        body: {
          rail: {
            barCount: 3,
            activeBar: 1,
            bars: [['prod', 'staging']],
            hidden: [],
            nameplates: { staging: { icon: '{"v":1,"style":"kt-pod","seed":"s","background":"teal-pale"}' } },
          },
        },
      },
      '/api/preferences/rail/nameplate': { status: 204 },
    });
    const posted = capturePosts();
    const dialog = await open();
    await waitFor(() => {
      expect(dialog.querySelectorAll('li[data-cluster]')).toHaveLength(2);
    });
    const staging = dialog.querySelector<HTMLElement>('li[data-cluster="staging"]')!;
    fireEvent.click(staging.querySelector('[role="radiogroup"] [aria-label="Violet"]')!);

    await waitFor(() => {
      expect(posted).toEqual([
        {
          path: '/api/preferences/rail/nameplate',
          body: { cluster: 'staging', colour: 'violet', icon: '{"v":1,"style":"kt-pod","seed":"s","background":"violet"}' },
        },
      ]);
    });
  });

  it('opens the sixteen from the color button, writes the pick and closes', async () => {
    stubWithPlates();
    const posted = capturePosts();
    const dialog = await open();
    await waitFor(() => {
      expect(dialog.querySelectorAll('li[data-cluster]')).toHaveLength(2);
    });
    const prod = dialog.querySelector<HTMLElement>('li[data-cluster="prod"]')!;
    const button = prod.querySelector<HTMLElement>('[data-colour-button]')!;
    fireEvent.click(button);
    expect(button.getAttribute('aria-expanded')).toBe('true');
    const panel = document.getElementById(button.getAttribute('aria-controls') ?? '')!;
    fireEvent.click(panel.querySelector('[aria-label="Pale blue"]')!);

    await waitFor(() => {
      expect(posted).toEqual([{ path: '/api/preferences/rail/nameplate', body: { cluster: 'prod', colour: 'blue-pale' } }]);
    });
    expect(button.getAttribute('aria-expanded')).toBe('false');
  });

  it('writes the initials alone when they are left, and resets a stored nameplate in one write', async () => {
    stubWithPlates();
    const posted = capturePosts();
    const dialog = await open();
    await waitFor(() => {
      expect(dialog.querySelectorAll('li[data-cluster]')).toHaveLength(2);
    });

    const initials = screen.getByRole('textbox', { name: 'Initials for staging' });
    fireEvent.change(initials, { target: { value: 'ST' } });
    fireEvent.blur(initials);
    fireEvent.click(screen.getByRole('button', { name: 'Reset the nameplate of prod' }));

    await waitFor(() => {
      expect(posted).toEqual([
        { path: '/api/preferences/rail/nameplate', body: { cluster: 'staging', initials: 'ST' } },
        { path: '/api/preferences/rail/nameplate', body: { cluster: 'prod', reset: true } },
      ]);
    });
    // Nothing stored for staging, so there is nothing to reset.
    expect((screen.getByRole('button', { name: 'Reset the nameplate of staging' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('shows what Reset left, so leaving a field afterwards writes nothing back', async () => {
    /*
      A field that copied the stored name once, in a row that never remounts,
      would still read "Production" after Reset - so focusing and leaving it
      would post the name back and undo the Reset in silence. Here the server
      forgets the nameplate when it is reset, as the
      real one does, and the fields must follow it.
    */
    const routes: Record<string, { status?: number; body?: unknown }> = {
      '/api/session': session,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'Production', initials: 'PX', colour: 'amber', state: 'ok', order: 0 },
            { name: 'staging', label: 'staging', initials: 'S', colour: 'teal-pale', state: 'ok', order: 1 },
          ],
        },
      },
      '/api/preferences': {
        body: { rail: { barCount: 1, activeBar: 1, bars: [['prod', 'staging']], hidden: [], nameplates: { prod: { name: 'Production', initials: 'PX' } } } },
      },
      '/api/preferences/rail/nameplate': { status: 204 },
    };
    stubFetch(routes);
    const posted: unknown[] = [];
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as { reset?: boolean };
        posted.push(body);
        if (body.reset === true) {
          routes['/api/preferences'] = { body: { rail: { barCount: 1, activeBar: 1, bars: [['prod', 'staging']], hidden: [], nameplates: {} } } };
          routes['/api/clusters'] = {
            body: {
              state: 'ok',
              items: [
                { name: 'prod', label: 'prod', initials: 'P', colour: 'red', state: 'ok', order: 0 },
                { name: 'staging', label: 'staging', initials: 'S', colour: 'teal-pale', state: 'ok', order: 1 },
              ],
            },
          };
        }
      }
      return stubbed(input, init);
    });
    await open();
    const name = (await screen.findByRole('textbox', { name: 'Name for prod' })) as HTMLInputElement;
    const initials = screen.getByRole('textbox', { name: 'Initials for prod' }) as HTMLInputElement;
    await waitFor(() => {
      expect(name.value).toBe('Production');
    });

    fireEvent.click(screen.getByRole('button', { name: 'Reset the nameplate of prod' }));
    await waitFor(() => {
      expect(name.value).toBe('');
    });
    expect(initials.value).toBe('');

    fireEvent.focus(name);
    fireEvent.blur(name);
    fireEvent.focus(initials);
    fireEvent.keyDown(initials, { key: 'Enter' });
    fireEvent.blur(initials);
    await waitFor(() => {
      expect(said()).toContain('back to its derived name');
    });
    expect(posted).toEqual([{ cluster: 'prod', reset: true }]);
  });

  it('keeps what is being typed when the stored value moves under it', async () => {
    // Another tab renames prod while this one is typing a name for it: the
    // refetch brings the other name, and the typing is not thrown away.
    const preferences = (name: string): { body: unknown } => ({
      body: { rail: { barCount: 1, activeBar: 1, bars: [['prod', 'staging']], hidden: [], nameplates: { prod: { name } } } },
    });
    const routes: Record<string, { status?: number; body?: unknown }> = {
      '/api/session': session,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'Production', initials: 'PR', colour: 'amber', state: 'ok', order: 0 },
            { name: 'staging', label: 'staging', initials: 'S', colour: 'teal-pale', state: 'ok', order: 1 },
          ],
        },
      },
      '/api/preferences': preferences('Production'),
      '/api/preferences/rail/nameplate': { status: 204 },
    };
    stubFetch(routes);
    await open();
    const name = (await screen.findByRole('textbox', { name: 'Name for prod' })) as HTMLInputElement;
    await waitFor(() => {
      expect(name.value).toBe('Production');
    });
    fireEvent.change(name, { target: { value: 'Prod E' } });

    routes['/api/preferences'] = preferences('Elsewhere');
    const staging = document.querySelector<HTMLElement>('li[data-cluster="staging"]')!;
    fireEvent.click(staging.querySelector('[role="radiogroup"] [aria-label="Violet"]')!);
    await waitFor(() => {
      expect(said()).toContain('staging is now Violet.');
    });
    expect(name.value).toBe('Prod E');
  });

  it('stops a name where the server would cut it', async () => {
    stubWithPlates();
    await open();
    const name = (await screen.findByRole('textbox', { name: 'Name for prod' })) as HTMLInputElement;
    // `nameplates.MaxName`: a longer one would be announced whole and stored cut.
    expect(name.maxLength).toBe(32);
  });

  it('calls a color by the name its swatch has', async () => {
    stubWithPlates();
    const dialog = await open();
    await waitFor(() => {
      expect(dialog.querySelectorAll('li[data-cluster]')).toHaveLength(2);
    });
    const prod = dialog.querySelector<HTMLElement>('li[data-cluster="prod"]')!;
    fireEvent.click(prod.querySelector('[role="radiogroup"] [aria-label="Pale teal"]')!);
    await waitFor(() => {
      expect(said()).toContain('Production is now Pale teal.');
    });
  });

  it('returns the color alone to automatic, where one was chosen', async () => {
    stubWithPlates();
    const posted = capturePosts();
    await open();
    const prod = await screen.findByRole('button', { name: 'Automatic color for prod' });
    // Nothing chosen for staging: its color is already the automatic one.
    expect((screen.getByRole('button', { name: 'Automatic color for staging' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(prod);
    await waitFor(() => {
      expect(posted).toEqual([{ path: '/api/preferences/rail/nameplate', body: { cluster: 'prod', colour: '' } }]);
    });
    expect(said()).toContain('Production has its automatic color again.');
  });
});
