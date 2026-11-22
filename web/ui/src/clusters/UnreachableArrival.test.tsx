import { focusManager } from '@tanstack/react-query';
import { act, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { lastKind, rememberKind, resetViewMemory } from '../shell/viewMemory';
import { mountApp } from '../testing/harness';

/**
 * Selecting a cluster the probe found unreachable lands on the unreachable
 * state, never on a view: not on the kind remembered from the last visit, and
 * not on a page titled Overview, both views of a cluster that cannot be
 * read. These pin the
 * arrival: the tile of an unreachable cluster links to the cluster's own
 * address, a deeper address on it is replaced by that one, the page there is
 * the warning and is not titled Overview, and a reachable cluster selected
 * afterwards opens where it was left, as before.
 */

afterEach(() => {
  vi.useRealTimers();
  focusManager.setFocused(undefined);
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetViewMemory();
  localStorage.clear();
});

const DOWN = 'docker-desktop';
const UP = 'kind-dev';
const REFUSED = 'dial tcp 127.0.0.1:6443: connect: connection refused';

interface Scene {
  /** How old the down cluster's verdict is. Five seconds is fresh: nothing is
   *  probed again. Past thirty seconds the console asks the probe first. */
  verdictAge?: number;
  /** What the probe finds when it is asked: the cluster back, or still down. */
  recovers?: boolean;
  /** The down cluster starts up, and goes down when the test says so. */
  upUntilFlipped?: boolean;
  /** The list calls the down cluster up, and its overview read answers
   *  `unreachable` - the verdict the page learns from the read itself. */
  upButOverviewDown?: boolean;
  /** `false`: the probe answers down without restamping the verdict, which is
   *  then as old as before - a verdict that has aged again by the next visit. */
  probeRestamps?: boolean;
}

interface Served {
  asked: string[];
  /** The down cluster goes down from the next read of the list. */
  flip: () => void;
  /** Probes asked from now on wait, unanswered, for the function returned:
   *  calling it answers them all. */
  hold: () => () => void;
}

const CONFIGMAP = `/api/c/${DOWN}/r/configmaps/argo/settings`;

/** The console with one cluster down and one up. Every request is recorded. */
function serve(scene: Scene = {}): Served {
  const {
    verdictAge = 5_000,
    recovers = false,
    upUntilFlipped = false,
    upButOverviewDown = false,
    probeRestamps = true,
  } = scene;
  const asked: string[] = [];
  let up = upUntilFlipped || upButOverviewDown;
  let probedAt: number | undefined;
  let held: Promise<void> | undefined;
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
  const entry = () =>
    up
      ? { name: DOWN, label: DOWN, state: 'ok', checkedAt: new Date().toISOString() }
      : {
          name: DOWN,
          label: DOWN,
          state: 'unreachable',
          reason: { code: 'cluster_unreachable', message: REFUSED, retryable: true },
          checkedAt: new Date(probedAt ?? Date.now() - verdictAge).toISOString(),
        };
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(raw, 'http://console.test').pathname;
    asked.push(`${init?.method ?? 'GET'} ${path}`);
    switch (path) {
      case '/api/session':
        return Promise.resolve(
          json({ identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' }),
        );
      case '/api/clusters':
        return Promise.resolve(
          json({
            state: 'ok',
            items: [entry(), { name: UP, label: UP, state: 'ok', checkedAt: new Date().toISOString() }],
          }),
        );
      case `/api/c/${DOWN}/probe`: {
        const answer = () => {
          up = recovers;
          if (probeRestamps) probedAt = Date.now();
          return json({ state: 'ok', data: entry() });
        };
        return held === undefined ? Promise.resolve(answer()) : held.then(answer);
      }
      case `/api/c/${DOWN}/overview`:
        return Promise.resolve(
          json(
            upButOverviewDown
              ? { state: 'unreachable', reason: { code: 'cluster_unreachable', message: REFUSED, retryable: true } }
              : { state: 'ok', data: { cluster: DOWN, counts: { state: 'ok', items: [] } } },
          ),
        );
      case CONFIGMAP:
        return Promise.resolve(
          json({
            state: 'ok',
            data: {
              kind: 'configmaps',
              namespace: 'argo',
              name: 'settings',
              title: 'ConfigMap',
              data: `/api/c/${DOWN}/data?kind=configmaps`,
              tabs: [],
              sections: [],
            },
          }),
        );
      case `/api/c/${DOWN}/data`:
        return Promise.resolve(
          json({
            state: 'ok',
            data: {
              kind: 'configmaps',
              namespace: 'argo',
              name: 'settings',
              resourceVersion: '12',
              editable: true,
              entries: [{ key: 'mode', masked: false, value: 'blue' }],
            },
          }),
        );
      case '/api/forwards':
        return Promise.resolve(json([]));
      case `/api/c/${UP}/nav`:
        return Promise.resolve(
          json({
            state: 'ok',
            data: { groups: [{ key: 'workloads', label: 'Workloads', items: [{ text: 'Pods', href: `/c/${UP}/pods` }] }] },
          }),
        );
      case `/api/c/${UP}/extensions`:
        return Promise.resolve(json({ state: 'empty', items: [] }));
      default:
        return Promise.resolve(
          new Response(JSON.stringify({ status: 404, title: 'Not Found', code: 'not_found' }), {
            status: 404,
            headers: { 'Content-Type': 'application/problem+json' },
          }),
        );
    }
  });
  return {
    asked,
    flip: () => {
      up = false;
      probedAt = Date.now();
    },
    hold: () => {
      let release = () => {};
      held = new Promise<void>((resolve) => {
        release = resolve;
      });
      return () => {
        held = undefined;
        release();
      };
    },
  };
}

function tile(name: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`#rail [data-cluster="${name}"]`);
  if (found === null) throw new Error(`no tile for ${name}`);
  return found;
}

function warning(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[data-cluster-down]');
}

describe('an unreachable cluster is arrived at on its warning, not on a view', () => {
  it('links the tile of an unreachable cluster to its own address, whatever it remembers', async () => {
    rememberKind(DOWN, 'services');
    rememberKind(UP, 'pods');
    serve();

    mountApp(`/c/${UP}/pods`);

    await waitFor(() => {
      expect(tile(DOWN).dataset['state']).toBe('unreachable');
    });
    // Not `/c/docker-desktop/services`: that list cannot be read.
    expect(tile(DOWN).getAttribute('href')).toBe(`/c/${DOWN}`);
    // The memory is kept for the day the cluster answers again.
    expect(lastKind(DOWN)).toBe('services');
  });

  it('selecting it draws the warning, titled as unreachable and not as Overview, with no menu', async () => {
    rememberKind(DOWN, 'services');
    rememberKind(UP, 'pods');
    const { asked } = serve();

    const { router } = mountApp(`/c/${UP}/pods`);
    await waitFor(() => {
      expect(tile(DOWN).getAttribute('href')).toBe(`/c/${DOWN}`);
    });
    fireEvent.click(tile(DOWN));

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    expect(warning()?.textContent).toContain('could not be reached');
    expect(warning()?.textContent).toContain(REFUSED);
    await waitFor(() => {
      expect(document.title).toContain('Unreachable');
    });
    expect(document.title).not.toContain('Overview');
    expect(document.getElementById('topbar')?.textContent ?? '').not.toContain('Overview');
    // Nothing in the menu column reads as selected: there is no menu.
    expect(document.querySelector('#menu a')).toBeNull();

    // Let any read that was going to fire, fire: none is made of the cluster.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked.filter((one) => one.includes(`/api/c/${DOWN}/`))).toEqual([]);
  });

  it('replaces a deeper address on an unreachable cluster with the cluster’s own', async () => {
    const { asked } = serve();

    const { router } = mountApp(`/c/${DOWN}/services?namespace=shop`);

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    expect(router.state.location.search).toBe('');
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked.filter((one) => one.includes(`/api/c/${DOWN}/`))).toEqual([]);
  });

  it('opens a reachable cluster selected afterwards where it was left', async () => {
    rememberKind(UP, 'pods');
    serve();

    const { router } = mountApp(`/c/${DOWN}`);
    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(tile(UP).getAttribute('href')).toBe(`/c/${UP}/pods`);
    fireEvent.click(tile(UP));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe(`/c/${UP}/pods`);
    });
    // Waited for: the router keeps the previous page on screen while the next
    // page's chunk arrives (`router/routes.tsx`), so the warning leaves a beat
    // after the address changes.
    await waitFor(() => {
      expect(warning()).toBeNull();
    });
    await waitFor(() => {
      expect(document.querySelector('#menu a[href="/c/kind-dev/pods"]')).not.toBeNull();
    });
  });

  it('leaves a page already drawn, and the value being edited, when the cluster goes down under it', async () => {
    // The list is re-read every 30 s, and a redirect when that re-read says
    // down would unmount the page and its draft.
    const { flip } = serve({ upUntilFlipped: true });
    const address = `/c/${DOWN}/configmaps/argo/settings`;
    const { router } = mountApp(address);

    const page = () => document.getElementById('page');
    await waitFor(() => {
      expect(page()?.textContent).toContain('mode');
    });
    const edit = [...(page()?.querySelectorAll('button') ?? [])].find((one) => one.textContent?.includes('Edit'));
    if (edit === undefined) throw new Error('no Edit control');
    fireEvent.click(edit);
    const area = () => page()?.querySelector('textarea');
    await waitFor(() => {
      expect(area()).not.toBeNull();
    });
    fireEvent.change(area()!, { target: { value: 'green' } });

    // The background round finds the cluster down: the list goes stale and is
    // re-read, as it is on the 30 s interval or when the window takes focus.
    flip();
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(Date.now() + 10_000);
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => {
      expect(tile(DOWN).dataset['state']).toBe('unreachable');
    });
    await new Promise((resolve) => setTimeout(resolve, 50));

    expect(router.state.location.pathname).toBe(address);
    expect(warning()).toBeNull();
    expect(area()?.value).toBe('green');
  });

  it('asks a stale verdict again before replacing a deeper address, and keeps the address when the cluster answers', async () => {
    const { asked } = serve({ verdictAge: 5 * 60_000, recovers: true });
    const { router } = mountApp(`/c/${DOWN}/pods?namespace=shop`);

    await waitFor(() => {
      expect(asked).toContain(`POST /api/c/${DOWN}/probe`);
    });
    await waitFor(() => {
      expect(tile(DOWN).dataset['state']).toBe('ok');
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(router.state.location.pathname).toBe(`/c/${DOWN}/pods`);
    expect(router.state.location.search).toBe('?namespace=shop');
    expect(warning()).toBeNull();
    expect(asked.filter((one) => one === `POST /api/c/${DOWN}/probe`)).toHaveLength(1);
  });

  it('replaces the deeper address once the stale verdict is asked again and still says down', async () => {
    const { asked } = serve({ verdictAge: 5 * 60_000, recovers: false });
    const { router } = mountApp(`/c/${DOWN}/pods?namespace=shop`);

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    // One probe: the warning's own check does not ask again, the verdict it
    // finds is the fresh one.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked.filter((one) => one === `POST /api/c/${DOWN}/probe`)).toHaveLength(1);
  });

  it('keeps the port-forwards of an unreachable cluster reachable, to list and stop them', async () => {
    serve();
    const { router } = mountApp(`/c/${DOWN}/port-forwards`);

    await waitFor(() => {
      expect(document.title).toContain('Port forwards');
    });
    await waitFor(() => {
      expect(tile(DOWN).dataset['state']).toBe('unreachable');
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(router.state.location.pathname).toBe(`/c/${DOWN}/port-forwards`);
    expect(warning()).toBeNull();
    expect(document.title).toContain('Port forwards');
  });

  it('titles the warning Unreachable when it is the overview read, not the list, that says so', async () => {
    serve({ upButOverviewDown: true });
    mountApp(`/c/${DOWN}`);

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(tile(DOWN).dataset['state']).toBe('ok');
    await waitFor(() => {
      expect(document.title).toContain('Unreachable');
    });
    expect(document.title).not.toContain('Overview');
  });

  it('forgets the page it kept once it is left, so Back to it is an arrival again', async () => {
    // The page kept under a cluster gone down must not stay kept after the
    // person leaves it, or Back would draw it again.
    const { flip } = serve({ upUntilFlipped: true });
    const address = `/c/${DOWN}/configmaps/argo/settings`;
    const { router } = mountApp(address);
    await waitFor(() => {
      expect(document.getElementById('page')?.textContent).toContain('mode');
    });

    flip();
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(Date.now() + 10_000);
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => {
      expect(tile(DOWN).dataset['state']).toBe('unreachable');
    });
    expect(router.state.location.pathname).toBe(address);

    // The tile, to the cluster's own address and its warning.
    fireEvent.click(tile(DOWN));
    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(router.state.location.pathname).toBe(`/c/${DOWN}`);

    // Back: the ConfigMap's address is an arrival on a down cluster.
    await act(async () => {
      await router.navigate(-1);
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    });
    expect(warning()).not.toBeNull();
  });

  it('asks again on a return to an address it already asked about, once the verdict has aged again', async () => {
    const { asked } = serve({ verdictAge: 5 * 60_000, recovers: false, probeRestamps: false });
    const probes = () => asked.filter((one) => one === `POST /api/c/${DOWN}/probe`).length;
    const address = `/c/${DOWN}/pods`;
    const { router } = mountApp(address);

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    // The warning's own check runs too: the verdict it finds is still stale.
    await waitFor(() => {
      expect(warning()?.querySelector('button')?.textContent).toBe('Refresh');
    });
    const before = probes();

    await act(async () => {
      await router.navigate(address);
    });
    // Two: this layout's, before deciding, and then the warning's own check
    // once it is replaced. Without the first, the return was redirected on the
    // stale verdict at once and only the warning asked.
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    });
    await waitFor(() => {
      expect(warning()?.querySelector('button')?.textContent).toBe('Refresh');
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(probes()).toBe(before + 2);
    expect(asked).not.toContain(`GET /api/c/${DOWN}/r/pods`);
  });

  it('waits for the new probe on a return to an address it already asked about, before deciding', async () => {
    // The probe answered for this address on the first visit, and that answer
    // has aged again by the return. Deciding on it at once would redirect
    // before the new probe had said anything; the return waits for it.
    const { asked, hold } = serve({ verdictAge: 5 * 60_000, recovers: false, probeRestamps: false });
    const probes = () => asked.filter((one) => one === `POST /api/c/${DOWN}/probe`).length;
    const address = `/c/${DOWN}/pods`;
    const { router } = mountApp(address);

    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
    expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    await waitFor(() => {
      expect(warning()?.querySelector('button')?.textContent).toBe('Refresh');
    });
    const before = probes();

    const release = hold();
    await act(async () => {
      await router.navigate(address);
    });
    await waitFor(() => {
      expect(probes()).toBeGreaterThan(before);
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    // The probe is unanswered: the address is the one returned to, and the
    // page's place says it is being read. Only this layout's probe has gone
    // out - the warning, which would ask its own, was never drawn.
    expect(router.state.location.pathname).toBe(address);
    expect(document.getElementById('page')?.textContent).toContain('Reading this cluster…');
    expect(warning()).toBeNull();
    expect(probes()).toBe(before + 1);

    // It answers, still down: now, and only now, the address is replaced.
    await act(async () => {
      release();
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    });
    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
  });

  it('decides a return on the probe it asked, not on one still out from an earlier visit', async () => {
    // A probe asked on the first visit, still unanswered when the person left
    // and came back, must not settle the return with its own answer before the
    // return's probe has said anything.
    const { asked, hold } = serve({ verdictAge: 5 * 60_000, recovers: false, probeRestamps: false });
    const probes = () => asked.filter((one) => one === `POST /api/c/${DOWN}/probe`).length;
    const address = `/c/${DOWN}/pods`;
    const reading = () => document.getElementById('page')?.textContent ?? '';

    const first = hold();
    const { router } = mountApp(address);
    await waitFor(() => {
      expect(probes()).toBe(1);
    });
    expect(reading()).toContain('Reading this cluster…');

    // Left, to a page under the same cluster that is not decided, and back.
    await act(async () => {
      await router.navigate(`/c/${DOWN}/port-forwards`);
    });
    await waitFor(() => {
      expect(document.title).toContain('Port forwards');
    });
    const second = hold();
    await act(async () => {
      await router.navigate(address);
    });
    await waitFor(() => {
      expect(probes()).toBe(2);
    });

    // The first visit's probe answers: the return is still waiting for its own.
    await act(async () => {
      first();
      await Promise.resolve();
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(router.state.location.pathname).toBe(address);
    expect(reading()).toContain('Reading this cluster…');
    expect(warning()).toBeNull();

    // Its own answers, still down: now the address is replaced.
    await act(async () => {
      second();
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(`/c/${DOWN}`);
    });
    await waitFor(() => {
      expect(warning()).not.toBeNull();
    });
  });
});
