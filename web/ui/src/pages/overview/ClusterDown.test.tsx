import { act, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp } from '../../testing/harness';

/**
 * An unreachable cluster shows one warning and a Refresh that works, and nothing
 * else.
 *
 * Without it, a cluster the probe found unreachable moments ago would draw a
 * "could not be asked" notice with *Try again*, a pointer to Health, eleven
 * panels repeating the refusal and an inert kinds menu - after dialling the
 * cluster again for up to ten seconds to learn what the probe already knew.
 * These pin the shape: with a fresh red verdict in the clusters list,
 * no overview and no menu are asked for; the page is the warning and Refresh;
 * Refresh says *Checking…*, re-probes this cluster once, and a green answer
 * takes the page to the dashboard.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  localStorage.clear();
});

const TIMEOUT = 'Get "https://10.255.255.1:6443/version": context deadline exceeded';

interface Served {
  asked: string[];
  /** Release the held probe answer, as the cluster that has come back. */
  release: () => void;
}

interface Scene {
  /** What the probe finds when it is asked: the cluster back, or still down. */
  recovers?: boolean;
  /** How old the list's verdict is, until a probe restamps it. 5 s is the
   *  fresh case; the background round re-probes a failing cluster at most
   *  once a minute, backing off to once every ten, so minutes is the ordinary
   *  age of a cluster that has been down a while. */
  verdictAge?: number;
  /** `true`: the verdict's instant is fixed when the scene starts, so it ages
   *  with the clock rather than being re-stamped on every read. */
  fixed?: boolean;
  /** The list says the cluster is up, and the overview read answers this. */
  upButOverview?: unknown;
}

/** The console, with prod down until the probe says otherwise. The probe's
 *  answer is held until the test releases it, so "Checking…" can be seen. */
function serve(scene: boolean | Scene): Served {
  const {
    recovers = false,
    verdictAge = 5_000,
    fixed = false,
    upButOverview,
  } = typeof scene === 'boolean' ? { recovers: scene } : scene;
  const asked: string[] = [];
  let up = upButOverview !== undefined;
  const started = Date.now();
  let probedAt: number | undefined;
  let release: () => void = () => undefined;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
  const entry = () =>
    up
      ? { name: 'prod', label: 'prod', state: 'ok', checkedAt: new Date().toISOString() }
      : {
          name: 'prod',
          label: 'prod',
          state: 'unreachable',
          reason: { code: 'timeout', message: TIMEOUT, retryable: true },
          checkedAt: new Date(probedAt !== undefined ? probedAt : (fixed ? started : Date.now()) - verdictAge).toISOString(),
        };

  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(raw, 'http://console.test').pathname;
    asked.push(`${init?.method ?? 'GET'} ${path}`);
    switch (path) {
      case '/api/session':
        return json({ identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' });
      case '/api/clusters':
        return json({ state: 'ok', items: [entry()] });
      case '/api/c/prod/probe':
        await held;
        up = recovers;
        // The probe restamps the verdict: fresh from here on.
        probedAt = Date.now();
        return json({ state: 'ok', data: entry() });
      case '/api/c/prod/nav':
        return json({ state: 'ok', data: { groups: [{ key: 'workloads', label: 'Workloads', items: [{ text: 'Pods', href: '/c/prod/pods' }] }] } });
      case '/api/c/prod/extensions':
        return json({ state: 'empty', items: [] });
      case '/api/c/prod/overview':
        if (upButOverview !== undefined) return json(upButOverview);
        return json({ state: 'ok', data: { cluster: 'prod', counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 1 }] } } });
      default:
        return new Response(JSON.stringify({ status: 404, title: 'Not Found', code: 'not_found' }), {
          status: 404,
          headers: { 'Content-Type': 'application/problem+json' },
        });
    }
  });
  return { asked, release: () => release() };
}

function warning(): HTMLElement | null {
  const found = document.querySelector('[data-cluster-down]');
  return found instanceof HTMLElement ? found : null;
}

function refresh(): HTMLButtonElement {
  const found = warning()?.querySelector('button');
  if (!(found instanceof HTMLButtonElement)) throw new Error('no Refresh in the warning');
  return found;
}

describe('a cluster the probe found down moments ago', () => {
  it('renders the warning and Refresh, and asks for no overview and no menu', async () => {
    const { asked } = serve(false);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    // The connection problem, in the server's words, and one button saying
    // Refresh - not Try again.
    expect(warning()?.textContent).toContain('could not be reached');
    expect(warning()?.textContent).toContain(TIMEOUT);
    expect(refresh().textContent).toBe('Refresh');
    expect(document.body.textContent).not.toContain('Try again');

    // Nothing else on the page: no panels, no pointer, no head.
    const page = document.getElementById('page');
    expect(page?.querySelectorAll('[role="status"][data-state]')).toHaveLength(0);
    expect(page?.querySelector('a[href="/health"]')).toBeNull();

    // And the menu has no groups.
    expect(document.querySelector('#menu [data-group], #menu details, #menu a')).toBeNull();
    // Nor a Kinds button in the top bar: it would show and hide a column that
    // is not drawn here.
    expect(document.querySelector('button[aria-controls="menu"]')).toBeNull();
    // Nor is Ctrl+B claimed for it: nothing is written, and the key is left to
    // the browser (not prevented).
    expect(fireEvent.keyDown(window, { key: 'b', ctrlKey: true })).toBe(true);
    expect(localStorage.getItem('kubetower.menu.mode')).toBeNull();

    // Let any query that was going to fire, fire.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked.filter((one) => one.endsWith('/overview'))).toEqual([]);
    expect(asked.filter((one) => one.includes('/nav'))).toEqual([]);
  });

  it('Refresh says Checking…, probes this cluster once, and a green verdict loads the page', async () => {
    const { asked, release } = serve(true);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });

    fireEvent.click(refresh());

    await waitFor(() => {
      expect(refresh().textContent).toBe('Checking…');
    });
    expect(refresh().disabled).toBe(true);
    // A second press while it runs is not a second probe.
    fireEvent.click(refresh());

    release();

    await waitFor(() => {
      expect(warning()).toBeNull();
    });
    expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toHaveLength(1);
    // The page and the menu are read once the verdict is green.
    await waitFor(() => {
      expect(asked.some((one) => one.endsWith('/overview'))).toBe(true);
    });
    await waitFor(() => {
      expect(asked.some((one) => one.endsWith('/nav'))).toBe(true);
    });
  });

  it('a cluster still down after Refresh keeps the warning, and the button comes back', async () => {
    const { asked, release } = serve(false);
    mountApp('/c/prod');
    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });

    fireEvent.click(refresh());
    release();

    await waitFor(() => {
      expect(refresh().textContent).toBe('Refresh');
    });
    expect(warning()?.textContent).toContain(TIMEOUT);
    expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toHaveLength(1);
    expect(asked.filter((one) => one.endsWith('/overview'))).toEqual([]);
  });
});

/**
 * The warning is held for a verdict of any age, not only thirty seconds after a
 * probe. The background round re-probes a failing cluster at most once a minute,
 * backing off to once every ten, so a cluster that has been down a while carries
 * an older verdict nearly always - and its page must not fall back to a loading
 * dashboard, a read dialling for ten seconds, and the warning beside an inert
 * menu.
 */
describe('a cluster the probe found down minutes ago', () => {
  it('draws the warning at once, with no menu and no read, and checks the verdict with one probe', async () => {
    const { asked, release } = serve({ verdictAge: 5 * 60_000 });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(warning()?.textContent).toContain(TIMEOUT);
    // Checked on arrival, with the warning already up.
    await waitFor(() => {
      expect(refresh().textContent).toBe('Checking…');
    });
    expect(document.querySelector('#menu [data-group], #menu details, #menu a')).toBeNull();

    release();
    await waitFor(() => {
      expect(refresh().textContent).toBe('Refresh');
    });
    // Still down: the warning stays, the menu stays away, and nothing was read.
    expect(warning()?.textContent).toContain(TIMEOUT);
    expect(document.querySelector('#menu [data-group], #menu details, #menu a')).toBeNull();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toHaveLength(1);
    expect(asked.filter((one) => one.endsWith('/overview'))).toEqual([]);
    expect(asked.filter((one) => one.includes('/nav'))).toEqual([]);
  });

  it('loads the page when that one probe finds the cluster back', async () => {
    const { asked, release } = serve({ recovers: true, verdictAge: 10 * 60_000 });
    mountApp('/c/prod');
    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    release();

    await waitFor(() => {
      expect(warning()).toBeNull();
    });
    await waitFor(() => {
      expect(asked.some((one) => one.endsWith('/overview'))).toBe(true);
    });
    await waitFor(() => {
      expect(asked.some((one) => one.endsWith('/nav'))).toBe(true);
    });
    expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toHaveLength(1);
  });

  it('keeps the warning and the hidden menu as a fresh verdict ages past thirty seconds, and asks nothing', async () => {
    // Only the clock the page reads is moved: `useNow`'s interval and Date.
    // Every other timer stays real, so the waits below still run.
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] });
    try {
      const { asked } = serve({ fixed: true });
      mountApp('/c/prod');
      await waitFor(() => {
        expect(warning()).not.toBeNull();
      });
      expect(refresh().textContent).toBe('Refresh');

      // Two minutes on: `useNow` has ticked four times, the list has been
      // polled, and the verdict it holds is two minutes old.
      for (let tick = 0; tick < 4; tick += 1) {
        await act(async () => {
          vi.advanceTimersByTime(30_000);
          await Promise.resolve();
        });
      }
      await new Promise((resolve) => setTimeout(resolve, 50));

      expect(warning()?.textContent).toContain(TIMEOUT);
      expect(document.querySelector('#menu [data-group], #menu details, #menu a')).toBeNull();
      expect(asked.filter((one) => one.endsWith('/overview'))).toEqual([]);
      expect(asked.filter((one) => one.includes('/nav'))).toEqual([]);
      // Decided on arrival: a verdict that ages on screen is not a second probe.
      expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });
});

/**
 * Only an `unreachable` refusal collapses to the warning: a person without
 * `list nodes` must not be offered a Refresh that re-probes connectivity, which
 * cannot change an RBAC answer.
 */
describe('an overview refused as forbidden', () => {
  it('keeps the permission notice and the menu, and is not drawn as a down cluster', async () => {
    const denied = 'nodes is forbidden: User "alice" cannot list resource "nodes" in API group "" at the cluster scope';
    const { asked } = serve({
      upButOverview: {
        state: 'forbidden',
        reason: { code: 'verb_denied', message: denied, retryable: false },
      },
    });
    mountApp('/c/prod');

    await waitFor(() => {
      expect(document.body.textContent).toContain(denied);
    });
    expect(warning()).toBeNull();
    expect(document.body.textContent).not.toContain('Checking…');
    // The notice, the line under it, the menu. The line carries no link.
    expect(document.querySelector('#page')?.textContent).toContain('never a cluster');
    await waitFor(() => {
      expect(document.querySelector('#menu [data-group], #menu details, #menu a')).not.toBeNull();
    });
    // The menu is drawn, so the button that hides it is there too, and Ctrl+B
    // hides it - the other side of the down cluster's check above.
    expect(document.querySelector('button[aria-controls="menu"]')).not.toBeNull();
    expect(fireEvent.keyDown(window, { key: 'b', ctrlKey: true })).toBe(false);
    expect(localStorage.getItem('kubetower.menu.mode')).toBe('hidden');
    expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toEqual([]);
  });
});
