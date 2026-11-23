import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp } from '../../testing/harness';
import type { UpdateInstall } from './updateInstall';

/**
 * The update's badge, banner and steps (#48): what is drawn for each state of
 * `info.update.install`, and that Download and Restart to update are two
 * separate requests.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const idle: UpdateInstall = {
  phase: 'idle',
  enabled: true,
  supported: true,
  installation: 'linux-appimage',
  ends: { terminals: 0, forwards: 0 },
};

function settings(install: UpdateInstall | undefined, state = 'newer'): unknown {
  return {
    general: { archiveEnabled: false, captureLogs: false, retentionDays: 7 },
    retentionChoices: [{ days: 7, label: '7 days' }],
    archive: { state: 'empty' },
    nodeShell: { enabled: false, available: false, image: '', defaultImage: 'alpine:3.24' },
    info: {
      statePath: '/state',
      statePersistent: true,
      kubeconfigFiles: [],
      listenAddress: '127.0.0.1:5823',
      withoutPassword: true,
      version: 'kubetower 0.1.2',
      development: false,
      repositoryUrl: 'https://github.com/thonicdev/kubetower',
      documentationUrl: 'https://example.com/docs',
      update: {
        state,
        latest: '0.1.3',
        releaseUrl: 'https://github.com/thonicdev/kubetower/releases/tag/0.1.3',
        checking: false,
        ...(install === undefined ? {} : { install }),
      },
    },
  };
}

interface Console {
  sent: { method: string; path: string; body: unknown }[];
  /** What GET /api/settings/update and the next settings read answer. */
  now: { install: UpdateInstall };
}

/** A console whose update is `install`; the writes answer `next(method, path)`. */
function console(
  install: UpdateInstall | undefined,
  next: (method: string, path: string, body: unknown) => { status: number; body: unknown } = (_m, _p) => ({
    status: 200,
    body: install,
  }),
  state = 'newer',
): Console {
  const out: Console = { sent: [], now: { install: install ?? idle } };
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url).split('?')[0] ?? '';
    const method = init?.method ?? 'GET';
    const json = (status: number, body: unknown): Promise<Response> =>
      Promise.resolve(
        new Response(JSON.stringify(body), {
          status,
          headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
        }),
      );
    if (method !== 'GET') {
      const body: unknown = init?.body === undefined ? undefined : JSON.parse(String(init.body));
      out.sent.push({ method, path, body });
      const answer = next(method, path, body);
      if (answer.status === 200) out.now.install = answer.body as UpdateInstall;
      return json(answer.status, answer.body);
    }
    switch (path) {
      case '/api/session':
        return json(200, { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' });
      case '/api/clusters':
        return json(200, { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }, { name: 'staging', label: 'staging', state: 'ok' }] });
      case '/api/settings':
        return json(200, settings(install === undefined ? undefined : out.now.install, state));
      case '/api/settings/update':
        return json(200, out.now.install);
      default:
        return json(404, { status: 404, title: 'Not Found', code: 'not_found', detail: `no stub for ${path}` });
    }
  });
  return out;
}

const banner = (): HTMLElement | null => document.querySelector<HTMLElement>('[data-update-banner]');
const control = (): HTMLElement | null => document.querySelector<HTMLElement>('[data-update-banner] [data-update-control]');

describe('the badge on Settings', () => {
  it('is drawn while a newer release can be installed, with the version in its tooltip', async () => {
    console(idle);
    mountApp('/c/prod/x/nope');
    await waitFor(() => {
      expect(document.querySelector('a[href^="/settings"] [data-count-badge]')?.textContent).toBe('1');
    });
    expect(document.querySelector('a[href^="/settings"]')?.getAttribute('title')).toBe('Update available: 0.1.3');
  });

  for (const [name, install, state] of [
    ['installing turned off', { ...idle, enabled: false }, 'newer'],
    ['an installation that cannot be updated', { ...idle, supported: false }, 'newer'],
    ['the latest release', idle, 'current'],
    ['no installer at all', undefined, 'newer'],
  ] as const) {
    it(`is not drawn for ${name}`, async () => {
      console(install, undefined, state);
      mountApp('/c/prod/x/nope');
      await waitFor(() => {
        expect(document.querySelector('a[href^="/settings"]')).not.toBeNull();
      });
      // Give the settings read its turn.
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(document.querySelector('[data-count-badge]')).toBeNull();
    });
  }
});

describe('the banner and its two clicks', () => {
  it('downloads on the first click and installs nothing until the second', async () => {
    const ready: UpdateInstall = {
      ...idle,
      phase: 'ready',
      version: '0.1.3',
      asset: 'kubetower-0.1.3-x86_64.AppImage',
      ends: { terminals: 2, forwards: 1 },
    };
    const c = console(idle, (_method, path) =>
      path === '/api/settings/update/download'
        ? { status: 200, body: ready }
        : { status: 200, body: { ...ready, phase: 'installing' } },
    );
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(banner()?.textContent).toContain('Update available');
    });
    expect(banner()?.textContent).toContain('0.1.3');
    fireEvent.click(banner()?.querySelector('[data-update-download]') as Element);
    await waitFor(() => {
      expect(control()?.getAttribute('data-update-control')).toBe('ready');
    });
    expect(c.sent.map((one) => one.path)).toEqual(['/api/settings/update/download']);
    // What the restart ends, before it is pressed.
    expect(banner()?.querySelector('[data-update-ends]')?.textContent).toBe('2 terminals · 1 forward');

    fireEvent.click(banner()?.querySelector('[data-update-restart]') as Element);
    await waitFor(() => {
      expect(control()?.getAttribute('data-update-control')).toBe('installing');
    });
    expect(c.sent.map((one) => one.path)).toEqual(['/api/settings/update/download', '/api/settings/update/install']);
  });

  it('draws each step in place', async () => {
    for (const [install, step] of [
      [{ ...idle, phase: 'downloading', version: '0.1.3', done: 5 * 1024 * 1024, total: 20 * 1024 * 1024 }, 'downloading'],
      [{ ...idle, phase: 'verifying', version: '0.1.3' }, 'verifying'],
      [{ ...idle, phase: 'installing', version: '0.1.3' }, 'installing'],
      [{ ...idle, phase: 'failed', reason: 'kubetower-0.1.3-x86_64.AppImage does not match its signed sha256' }, 'failed'],
    ] as const) {
      console(install);
      const { view } = mountApp('/settings/general');
      await waitFor(() => {
        expect(document.querySelector(`[data-update-banner] [data-update-step="${step}"]`)).not.toBeNull();
      });
      if (step === 'downloading') {
        expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('25');
        expect(banner()?.textContent).toContain('5.0 / 20.0 MB');
      }
      if (step === 'failed') {
        expect(banner()?.textContent).toContain('does not match its signed sha256');
        expect(banner()?.querySelector('[data-update-retry]')).not.toBeNull();
      }
      view.unmount();
      vi.unstubAllGlobals();
    }
  });

  it("says a refused download in the server's words", async () => {
    console(idle, () => ({
      status: 409,
      body: { type: 'about:blank', title: 'Conflict', status: 409, code: 'conflict', detail: 'conflict: an update is already in progress' },
    }));
    mountApp('/settings/general');
    let download: Element | null = null;
    await waitFor(() => {
      download = document.querySelector('[data-update-banner] [data-update-download]');
      expect(download).not.toBeNull();
    });
    fireEvent.click(download as unknown as Element);
    await waitFor(() => {
      expect(document.querySelector('[data-update-refused]')?.textContent).toContain('an update is already in progress');
    });
  });
});

describe('Settings › Infos', () => {
  it('draws Download beside View release and Check now, and the Install updates setting', async () => {
    console(idle);
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelector('[data-update="newer"] [data-update-download]')).not.toBeNull();
    });
    const line = document.querySelector('[data-update="newer"]');
    expect(line?.textContent).toContain('View release');
    expect(document.querySelector('[data-update-check] button')?.textContent).toBe('Check now');
    expect(document.querySelector<HTMLInputElement>('[data-install-updates] input')?.checked).toBe(true);
  });

  it('turns installing off: the banner and Download go, the check stays', async () => {
    const c = console(idle, (_m, _p, body) => ({ status: 200, body: { ...idle, enabled: (body as { enabled: boolean }).enabled } }));
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelector('[data-install-updates] input')).not.toBeNull();
    });
    fireEvent.click(document.querySelector('[data-install-updates] input') as Element);
    await waitFor(() => {
      expect(banner()).toBeNull();
    });
    expect(c.sent).toEqual([{ method: 'PUT', path: '/api/settings/update', body: { enabled: false } }]);
    expect(document.querySelector('[data-update-download]')).toBeNull();
    expect(document.querySelector('[data-update-check] button')?.textContent).toBe('Check now');
  });

  it('draws no setting where nothing installs', async () => {
    console(undefined);
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelector('[data-update="newer"]')).not.toBeNull();
    });
    expect(document.querySelector('[data-install-updates]')).toBeNull();
    expect(document.querySelector('[data-update-download]')).toBeNull();
    expect(banner()).toBeNull();
  });
});
