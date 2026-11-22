import { waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp } from '../testing/harness';

/**
 * A cluster whose AWS SSO session has expired is one
 * message with the command to copy, on every page of it - not the kinds menu's
 * error, half a list and "could not be asked" scattered over the page.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
  localStorage.clear();
});

const COMMAND = 'aws sso login --profile prod-devops';

function serve(remedy: Record<string, unknown> | undefined, checkedAt = new Date().toISOString()): string[] {
  const asked: string[] = [];
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(raw, 'http://console.test').pathname;
    asked.push(`${init?.method ?? 'GET'} ${path}`);
    switch (path) {
      case '/api/session':
        return json({ identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' });
      case '/api/clusters':
        return json({
          state: 'ok',
          items: [
            {
              name: 'prod',
              label: 'prod',
              state: 'unreadable',
              reason: { code: 'credentials_expired', message: 'SSO session expired — sign in again on the host', retryable: true },
              ...(remedy === undefined ? {} : { remedy }),
              checkedAt,
            },
            { name: 'dev', label: 'dev', state: 'ok', checkedAt: new Date().toISOString() },
          ],
        });
      default:
        return new Response(JSON.stringify({ status: 404, title: 'Not Found', code: 'not_found' }), {
          status: 404,
          headers: { 'Content-Type': 'application/problem+json' },
        });
    }
  });
  return asked;
}

function panels(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('[data-cluster-down]')];
}

describe('a cluster whose SSO session has expired', () => {
  it.each(['/c/prod', '/c/prod/pods', '/c/prod/pods/default/api-1'])(
    'is one message with the command on %s, and nothing of the cluster is read',
    async (address) => {
      const asked = serve({ who: 'You', what: 'Sign in again', actionable: true, command: COMMAND });
      const { router } = mountApp(address);

      await waitFor(() => {
        expect(panels()).toHaveLength(1);
      });
      const panel = panels()[0];
      expect(panel?.dataset.clusterDown).toBe('expired');
      expect(panel?.textContent).toContain('AWS SSO session');
      expect(panel?.querySelector('[data-command]')?.textContent).toBe(COMMAND);
      expect(panel?.querySelector('button[aria-label="Copy the command"], button[title="Copy the command"]')).not.toBeNull();
      // Said once: the page does not repeat the server's sentence beside the
      // command, and nothing else on the page says "could not".
      expect(document.body.textContent?.split('expired')).toHaveLength(3); // the menu's line and the panel's title
      expect(document.body.textContent).not.toContain('could not be asked');

      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(asked.filter((one) => one.includes('/api/c/prod/') && !one.endsWith('/probe'))).toEqual([]);
      // The address is kept: the panel is drawn where the person arrived, so
      // the deep link is still there once they sign in again. The redirect an
      // unreachable cluster gets would move it to `/c/prod` and draw the same
      // panel, which is why the address is asserted and not only the panel.
      expect(router.state.location.pathname).toBe(address);
    },
  );

  // A 401 on a context that is not AWS is `credentials_expired` too, with no
  // command: it may be one API that refuses while the rest answers, so the
  // pages keep drawing what they can.
  it('without a command, is not collapsed: the page draws what it can', async () => {
    const asked = serve({ who: 'You', what: 'Sign in again', actionable: true });
    mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(asked.some((one) => one.startsWith('GET /api/c/prod/'))).toBe(true);
    });
    expect(panels()).toHaveLength(0);
  });

  it('asks again on arrival when the verdict is old', async () => {
    const asked = serve(
      { who: 'You', what: 'Sign in again', actionable: true, command: COMMAND },
      new Date(Date.now() - 10 * 60_000).toISOString(),
    );
    const { router } = mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(asked).toContain('POST /api/c/prod/probe');
    });
    expect(panels()).toHaveLength(1);
    expect(router.state.location.pathname).toBe('/c/prod/pods');
  });
});

/**
 * The ordinary case: the console was opened while the session was good, and
 * it lapsed since. The list still says the cluster answers - its verdict is
 * the last probe's - and every read of it is refused at the credential step.
 * The first such read asks the probe, and the page becomes the one screen;
 * once the session is renewed, Retry brings the page back.
 */
describe('a session that lapses while the cluster is open', () => {
  const expiredEntry = {
    name: 'prod',
    label: 'prod',
    state: 'unreadable',
    reason: { code: 'credentials_expired', message: 'SSO session expired — sign in again on the host', retryable: true },
    remedy: { who: 'You', what: 'Sign in again', actionable: true, command: COMMAND },
    checkedAt: new Date().toISOString(),
  };
  const okEntry = { name: 'prod', label: 'prod', state: 'ok', checkedAt: new Date().toISOString() };

  function serveLapse(
    capabilities: Record<string, unknown> = {},
    hold?: Promise<void>,
  ): { asked: string[]; renew: () => void } {
    const asked: string[] = [];
    let verdict: Record<string, unknown> = okEntry; // what the last probe found
    let session: 'lapsed' | 'renewed' = 'lapsed'; // what the reads would find
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, 'http://console.test').pathname;
      const method = init?.method ?? 'GET';
      asked.push(`${method} ${path}`);
      if (path === '/api/session')
        return json({ identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' });
      if (path === '/api/capabilities') return json({ mode: 'desktop', podExec: true, ...capabilities });
      if (path === '/api/clusters') return json({ state: 'ok', items: [verdict] });
      if (path === '/api/c/prod/probe' && method === 'POST') {
        // Held only once the screen is up, so the probe that draws it answers.
        if (hold !== undefined && verdict === expiredEntry) await hold;
        verdict = session === 'lapsed' ? expiredEntry : okEntry;
        return json({ state: 'ok', data: verdict });
      }
      if (path === '/api/c/prod/r/pods') {
        return session === 'lapsed'
          ? json({
              state: 'unreadable',
              reason: {
                code: 'credentials_refused',
                message: 'listing pods: getting credentials: exec: executable aws failed with exit code 255',
                scope: { cluster: 'prod', resource: 'pods', verb: 'list' },
                retryable: true,
              },
            })
          : json({ state: 'ok', items: [{ name: 'api-1', namespace: 'default' }] });
      }
      return new Response(JSON.stringify({ status: 404, title: 'Not Found', code: 'not_found' }), {
        status: 404,
        headers: { 'Content-Type': 'application/problem+json' },
      });
    });
    return { asked, renew: () => (session = 'renewed') };
  }

  it('becomes the one screen once a read is refused, and Retry brings the page back', async () => {
    const { asked, renew } = serveLapse();
    const { router } = mountApp('/c/prod/pods');

    await waitFor(() => {
      expect(panels()).toHaveLength(1);
    });
    expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toHaveLength(1);
    const panel = panels()[0];
    expect(panel?.querySelector('h1')?.textContent).toBe('The AWS SSO session behind this cluster has expired');
    expect(panel?.querySelector('[data-command]')?.textContent).toBe(COMMAND);
    // The list's own failure is gone with the page it was on.
    expect(document.body.textContent).not.toContain('failed with exit code');
    // Without a terminal here, the command is the host's to run.
    expect(panel?.textContent).toContain('Run this where KubeTower runs');
    expect(panel?.querySelector('button[title^="Open a shell"]')).toBeNull();

    renew();
    const retry = [...(panel?.querySelectorAll('button') ?? [])].find((button) => button.textContent === 'Retry');
    expect(retry).toBeDefined();
    retry?.click();
    await waitFor(() => {
      expect(panels()).toHaveLength(0);
    });
    expect(router.state.location.pathname).toBe('/c/prod/pods');
  });

  it('Retry says Checking…, then that it looked and the session is still lapsed', async () => {
    let answer: () => void = () => undefined;
    const held = new Promise<void>((resolve) => {
      answer = resolve;
    });
    const { asked } = serveLapse({}, held);
    mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(panels()).toHaveLength(1);
    });
    const panel = (): HTMLElement | undefined => panels()[0];
    const button = (): HTMLButtonElement | undefined =>
      [...(panel()?.querySelectorAll('button') ?? [])].find((one) => one.textContent === 'Retry' || one.textContent === 'Checking…');
    const still = (): Element | null | undefined => panel()?.querySelector('[data-still-lapsed]');
    expect(still()).toBeNull();
    const probes = asked.filter((one) => one === 'POST /api/c/prod/probe').length;

    button()?.click();
    await waitFor(() => {
      expect(button()?.textContent).toBe('Checking…');
    });
    expect(button()?.disabled).toBe(true);
    expect(button()?.getAttribute('aria-busy')).toBe('true');
    expect(still()).toBeNull();

    answer();
    await waitFor(() => {
      expect(button()?.textContent).toBe('Retry');
    });
    expect(asked.filter((one) => one === 'POST /api/c/prod/probe')).toHaveLength(probes + 1);
    expect(panels()).toHaveLength(1);
    await waitFor(() => {
      expect(still()?.textContent).toMatch(/^Checked at \d{2}:\d{2}:\d{2}: still signed out\.$/);
    });
    expect(still()?.getAttribute('role')).toBe('status');
  });

  it('offers the dock terminal where this console has one', async () => {
    serveLapse({ localShell: true });
    mountApp('/c/prod/pods');
    await waitFor(
      () => {
        expect(panels()).toHaveLength(1);
      },
      { timeout: 4000 },
    );
    const panel = panels()[0];
    expect(panel?.querySelector('button[title^="Open a shell"]')).not.toBeNull();
    expect(panel?.textContent).toContain('Run this in a terminal on this machine');
  });
});

describe('a credential plugin that refused', () => {
  it('is one screen with the server sentence, and no command', async () => {
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, 'http://console.test').pathname;
      if (path === '/api/session')
        return json({ identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' });
      if (path === '/api/clusters')
        return json({
          state: 'ok',
          items: [
            {
              name: 'prod',
              label: 'prod',
              state: 'unreadable',
              reason: { code: 'credentials_refused', message: 'the credential plugin ran and refused', retryable: false },
              checkedAt: new Date().toISOString(),
            },
          ],
        });
      return new Response(JSON.stringify({ status: 404, title: 'Not Found', code: 'not_found' }), {
        status: 404,
        headers: { 'Content-Type': 'application/problem+json' },
      });
    });
    mountApp('/c/prod/pods');
    await waitFor(() => {
      expect(panels()).toHaveLength(1);
    });
    const panel = panels()[0];
    expect(panel?.querySelector('h1')?.textContent).toBe('The credentials behind this cluster were refused');
    expect(panel?.textContent).toContain('the credential plugin ran and refused');
    expect(panel?.querySelector('[data-command]')).toBeNull();
  });
});
