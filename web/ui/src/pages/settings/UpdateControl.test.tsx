import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp } from '../../testing/harness';
import { INSTALL_POLL_MS, INSTALL_TIMEOUT_MS, REFUSED_MS, type UpdateInstall } from './updateInstall';

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
  /** Every GET, in order. */
  reads: string[];
  /** What GET /api/settings/update and the next settings read answer. */
  now: { install: UpdateInstall };
}

/** A console whose update is `install`; the writes answer `next(method, path)`. */
function console(
  install: UpdateInstall | undefined,
  next: (
    method: string,
    path: string,
    body: unknown,
  ) => { status: number; body: unknown; now?: UpdateInstall } | Promise<{ status: number; body: unknown; now?: UpdateInstall }> = (_m, _p) => ({
    status: 200,
    body: install,
  }),
  state = 'newer',
  /** GET answers that replace the defaults below. */
  extra: Readonly<Record<string, unknown>> = {},
): Console {
  const out: Console = { sent: [], reads: [], now: { install: install ?? idle } };
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
      return Promise.resolve(next(method, path, body)).then((answer) => {
        if (answer.now !== undefined) out.now.install = answer.now;
        else if (answer.status === 200) out.now.install = answer.body as UpdateInstall;
        return json(answer.status, answer.body);
      });
    }
    out.reads.push(path);
    if (path in extra) return json(200, extra[path]);
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
    const link = document.querySelector('a[href^="/settings"]');
    // The running build and the release it would become (#48).
    expect(link?.getAttribute('title')).toBe('Update available\nRunning: kubetower 0.1.2\nNew: 0.1.3');
    // The badge is not part of the link's name.
    expect(screen.getByRole('link', { name: 'Settings' })).toBe(link);
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
        expect(banner()?.textContent).toContain('5.0 / 20.0 MiB');
      }
      if (step === 'failed') {
        expect(banner()?.textContent).toContain('does not match its signed sha256');
        expect(banner()?.querySelector('[data-update-retry]')).not.toBeNull();
      }
      view.unmount();
      vi.unstubAllGlobals();
    }
  });

  it('offers Open folder on the verified file where the installation cannot be written, with no raw error', async () => {
    const manual: UpdateInstall = {
      ...idle,
      phase: 'manual',
      installation: 'linux-tar',
      version: '0.1.3',
      asset: 'kubetower_0.1.3_linux_amd64.tar.gz',
      folder: '/home/alice/.local/state/kubetower/update/install-1',
    };
    const c = console(manual);
    mountApp('/settings/general');
    let open: HTMLButtonElement | null = null;
    await waitFor(() => {
      open = document.querySelector<HTMLButtonElement>('[data-update-banner] [data-update-open-folder]');
      expect(open).not.toBeNull();
      expect(open?.disabled).toBe(false);
    });
    const button = open as unknown as HTMLButtonElement;
    expect(button.textContent).toBe('Open folder');
    expect(button.title).toBe(manual.folder);
    expect(banner()?.querySelector('[data-update-step="manual"]')?.textContent).toBe('Not writable');
    expect(banner()?.querySelector('[data-update-restart], [data-update-download], [data-update-retry]')).toBeNull();
    fireEvent.click(button);
    await waitFor(() => {
      expect(c.sent).toEqual([{ method: 'POST', path: '/api/settings/update/open-folder', body: undefined }]);
    });
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

describe('the poll', () => {
  it('reads a running download every INSTALL_POLL_MS, stops once it settles, and reads the settings again', async () => {
    const downloading: UpdateInstall = { ...idle, phase: 'downloading', version: '0.1.3', done: 1, total: 4 };
    const c = console(downloading);
    mountApp('/settings/general');
    await waitFor(() => {
      expect(c.reads.filter((p) => p === '/api/settings/update').length).toBeGreaterThanOrEqual(2);
    });
    expect(control()?.getAttribute('data-update-control')).toBe('downloading');

    c.now.install = { ...downloading, phase: 'ready' };
    await waitFor(() => {
      expect(control()?.getAttribute('data-update-control')).toBe('ready');
    });
    // Settled: the whole settings read again.
    await waitFor(() => {
      expect(c.reads.lastIndexOf('/api/settings')).toBeGreaterThan(c.reads.lastIndexOf('/api/settings/update'));
    });
    const polls = c.reads.filter((p) => p === '/api/settings/update').length;
    await new Promise((resolve) => setTimeout(resolve, INSTALL_POLL_MS * 3));
    expect(c.reads.filter((p) => p === '/api/settings/update').length).toBe(polls);
  });

  it('does not poll while nothing runs', async () => {
    const c = console(idle);
    mountApp('/settings/general');
    await waitFor(() => {
      expect(control()?.getAttribute('data-update-control')).toBe('idle');
    });
    await new Promise((resolve) => setTimeout(resolve, INSTALL_POLL_MS * 3));
    expect(c.reads).not.toContain('/api/settings/update');
  });

  it('draws Installing… as failed once the console has not come back in time, and Retry waits again without installing again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const c = console({ ...idle, phase: 'installing', version: '0.1.3' });
      mountApp('/settings/general');
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="installing"]')).not.toBeNull();
      });
      await vi.advanceTimersByTimeAsync(INSTALL_TIMEOUT_MS);
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="timed-out"]')?.textContent).toBe('Failed');
      });
      // The poll stopped with it.
      const polls = c.reads.filter((p) => p === '/api/settings/update').length;
      await vi.advanceTimersByTimeAsync(INSTALL_POLL_MS * 4);
      expect(c.reads.filter((p) => p === '/api/settings/update').length).toBe(polls);

      fireEvent.click(control()?.querySelector('[data-update-wait]') as Element);
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="installing"]')).not.toBeNull();
      });
      // Retry waits again on a fresh clock and reads the settings: no second
      // install, which the server refuses outside ready.
      expect(c.sent).toEqual([]);
      await waitFor(() => {
        expect(c.reads.filter((p) => p === '/api/settings/update').length).toBeGreaterThan(polls);
      });
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('one request at a time', () => {
  it('disables every Download while one is asked, and sends one POST for two clicks', async () => {
    let release: (value: { status: number; body: unknown }) => void = () => undefined;
    const c = console(idle, () => new Promise((resolve) => (release = resolve)));
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelectorAll('[data-update-download]').length).toBe(2);
    });
    const [inBanner, inLine] = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-update-download]'));
    fireEvent.click(inBanner as Element);
    await waitFor(() => {
      for (const button of document.querySelectorAll<HTMLButtonElement>('[data-update-download]')) {
        expect(button.disabled).toBe(true);
      }
    });
    fireEvent.click(inLine as Element);
    expect(c.sent.length).toBe(1);
    release({ status: 200, body: { ...idle, phase: 'downloading', done: 0, total: 0 } });
    await waitFor(() => {
      expect(control()?.getAttribute('data-update-control')).toBe('downloading');
    });
  });

  it('disables Restart to update while it is asked', async () => {
    const ready: UpdateInstall = { ...idle, phase: 'ready', version: '0.1.3' };
    console(ready, () => new Promise(() => undefined));
    mountApp('/settings/general');
    const restart = (): HTMLButtonElement | null | undefined =>
      banner()?.querySelector<HTMLButtonElement>('[data-update-restart]');
    await waitFor(() => {
      expect(restart()?.disabled).toBe(false);
    });
    fireEvent.click(restart() as Element);
    await waitFor(() => {
      expect(restart()?.disabled).toBe(true);
    });
  });
});

describe('what is drawn for odd states', () => {
  it('draws an indeterminate bar and no "0.0 / 0.0" while the size is unknown', async () => {
    console({ ...idle, phase: 'downloading', version: '0.1.3', done: 0 });
    mountApp('/settings/general');
    await waitFor(() => {
      expect(document.querySelector('[data-update-banner] [role="progressbar"]')).not.toBeNull();
    });
    expect(document.querySelector('[role="progressbar"]')?.hasAttribute('aria-valuenow')).toBe(false);
    expect(document.querySelector('[data-update-size]')).toBeNull();
    expect(banner()?.textContent).not.toContain('0.0');
  });

  it('counts what came while the size is unknown', async () => {
    console({ ...idle, phase: 'downloading', version: '0.1.3', done: 3 * 1024 * 1024 });
    mountApp('/settings/general');
    await waitFor(() => {
      expect(document.querySelector('[data-update-size]')?.textContent).toBe('3.0 MiB');
    });
  });

  it('draws nothing for a phase it does not know', async () => {
    // A newer server's phase, which this page's contract does not name yet.
    console({ ...idle, phase: 'paused' as UpdateInstall['phase'] });
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelector('[data-update="newer"]')).not.toBeNull();
    });
    expect(document.querySelector('[data-update-control]')).toBeNull();
    expect(banner()).toBeNull();
  });

  it('draws no Install updates setting where this installation cannot be updated', async () => {
    console({ ...idle, supported: false });
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelector('[data-update="newer"]')).not.toBeNull();
    });
    expect(document.querySelector('[data-install-updates]')).toBeNull();
  });

  it('reads the setting back when the server applied it but could not save it', async () => {
    const off: UpdateInstall = { ...idle, enabled: false };
    console(idle, () => ({
      status: 500,
      body: { type: 'about:blank', title: 'Internal Server Error', status: 500, code: 'not_saved', detail: 'not_saved: read-only state' },
      now: off,
    }));
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelector<HTMLInputElement>('[data-install-updates] input')?.checked).toBe(true);
    });
    fireEvent.click(document.querySelector('[data-install-updates] input') as Element);
    await waitFor(() => {
      expect(document.querySelector<HTMLInputElement>('[data-install-updates] input')?.checked).toBe(false);
    });
    expect(banner()).toBeNull();
    expect(document.querySelector('[data-update-download]')).toBeNull();
    expect(document.querySelector('[data-install-updates] [role="alert"]')?.textContent).toContain('read-only state');
  });
});

describe('the install timeout', () => {
  const installing: UpdateInstall = { ...idle, phase: 'installing', version: '0.1.3' };
  const ready: UpdateInstall = { ...idle, phase: 'ready', version: '0.1.3' };

  it('is one clock for every surface, kept across a tab switch', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      console(installing);
      const { router } = mountApp('/settings/infos');
      const line = (): Element | null => document.querySelector('[data-update="newer"] [data-update-control]');
      await waitFor(() => {
        expect(line()?.querySelector('[data-update-step="installing"]')).not.toBeNull();
      });
      await vi.advanceTimersByTimeAsync(INSTALL_TIMEOUT_MS);
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="timed-out"]')).not.toBeNull();
        expect(line()?.querySelector('[data-update-step="timed-out"]')).not.toBeNull();
      });
      await router.navigate('/settings/general');
      await waitFor(() => {
        expect(document.querySelector('[data-infos]')).toBeNull();
      });
      await router.navigate('/settings/infos');
      // A fresh line, on the same clock: no new Installing…, no new poll.
      await waitFor(() => {
        expect(line()).not.toBeNull();
      });
      expect(line()?.querySelector('[data-update-step="installing"]')).toBeNull();
      expect(line()?.querySelector('[data-update-step="timed-out"]')).not.toBeNull();
      expect(control()?.querySelector('[data-update-step="timed-out"]')).not.toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not carry over to the next install', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      // After the timeout the console says it is ready again, read on
      // Retry; the next Restart to update is a new install.
      const c = console(installing, () => ({ status: 200, body: installing }));
      mountApp('/settings/general');
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="installing"]')).not.toBeNull();
      });
      await vi.advanceTimersByTimeAsync(INSTALL_TIMEOUT_MS);
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-wait]')).not.toBeNull();
      });
      c.now.install = ready;
      fireEvent.click(control()?.querySelector('[data-update-wait]') as Element);
      await waitFor(() => {
        expect(control()?.getAttribute('data-update-control')).toBe('ready');
      });
      fireEvent.click(banner()?.querySelector('[data-update-restart]') as Element);
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="installing"]')).not.toBeNull();
      });
      const polls = c.reads.filter((p) => p === '/api/settings/update').length;
      await vi.advanceTimersByTimeAsync(INSTALL_POLL_MS * 3);
      // Still followed: no timeout, the poll runs.
      expect(control()?.querySelector('[data-update-step="timed-out"]')).toBeNull();
      expect(c.reads.filter((p) => p === '/api/settings/update').length).toBeGreaterThan(polls);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('the install timeout, once the console answers', () => {
  it('is cleared when the phase leaves installing, so the next install starts a new clock', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const installing: UpdateInstall = { ...idle, phase: 'installing', version: '0.1.3' };
      const ready: UpdateInstall = { ...idle, phase: 'ready', version: '0.1.3' };
      // The console is back, ready; its answer to the setting's write says so.
      const c = console(installing, (_m, path, body) =>
        path === '/api/settings/update'
          ? { status: 200, body: { ...ready, enabled: (body as { enabled: boolean }).enabled } }
          : { status: 200, body: installing },
      );
      mountApp('/settings/infos');
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="installing"]')).not.toBeNull();
      });
      await vi.advanceTimersByTimeAsync(INSTALL_TIMEOUT_MS);
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="timed-out"]')).not.toBeNull();
      });
      const box = (): HTMLInputElement | null => document.querySelector<HTMLInputElement>('[data-install-updates] input');
      fireEvent.click(box() as Element);
      await waitFor(() => {
        expect(box()?.checked).toBe(false);
      });
      fireEvent.click(box() as Element);
      await waitFor(() => {
        expect(control()?.getAttribute('data-update-control')).toBe('ready');
      });
      fireEvent.click(banner()?.querySelector('[data-update-restart]') as Element);
      await waitFor(() => {
        expect(control()?.querySelector('[data-update-step="installing"]')).not.toBeNull();
      });
      await vi.advanceTimersByTimeAsync(INSTALL_POLL_MS * 2);
      expect(control()?.querySelector('[data-update-step="timed-out"]')).toBeNull();
      expect(c.sent.map((one) => one.path)).toEqual([
        '/api/settings/update',
        '/api/settings/update',
        '/api/settings/update/install',
      ]);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('a refusal', () => {
  it('is drawn on every surface that offers the control', async () => {
    console(idle, () => ({
      status: 409,
      body: { type: 'about:blank', title: 'Conflict', status: 409, code: 'conflict', detail: 'conflict: an update is already in progress' },
    }));
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(document.querySelectorAll('[data-update-download]').length).toBe(2);
    });
    fireEvent.click(banner()?.querySelector('[data-update-download]') as Element);
    await waitFor(() => {
      expect(document.querySelectorAll('[data-update-refused]').length).toBe(2);
    });
  });

  it('is forgotten on another Settings tab', async () => {
    console(idle, () => ({
      status: 409,
      body: { type: 'about:blank', title: 'Conflict', status: 409, code: 'conflict', detail: 'conflict: an update is already in progress' },
    }));
    const { router } = mountApp('/settings/infos');
    await waitFor(() => {
      expect(banner()?.querySelector<HTMLButtonElement>('[data-update-download]')?.disabled).toBe(false);
    });
    fireEvent.click(banner()?.querySelector('[data-update-download]') as Element);
    await waitFor(() => {
      expect(document.querySelector('[data-update-refused]')).not.toBeNull();
    });
    await router.navigate('/settings/general');
    await waitFor(() => {
      expect(document.querySelector('[data-infos]')).toBeNull();
    });
    expect(document.querySelector('[data-update-refused]')).toBeNull();
  });

  it('is forgotten after REFUSED_MS', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      console(idle, () => ({
        status: 409,
        body: { type: 'about:blank', title: 'Conflict', status: 409, code: 'conflict', detail: 'conflict: an update is already in progress' },
      }));
      mountApp('/settings/general');
      await waitFor(() => {
        expect(banner()?.querySelector<HTMLButtonElement>('[data-update-download]')?.disabled).toBe(false);
      });
      fireEvent.click(banner()?.querySelector('[data-update-download]') as Element);
      await waitFor(() => {
        expect(document.querySelector('[data-update-refused]')).not.toBeNull();
      });
      await vi.advanceTimersByTimeAsync(REFUSED_MS / 2);
      expect(document.querySelector('[data-update-refused]')).not.toBeNull();
      await vi.advanceTimersByTimeAsync(REFUSED_MS);
      await waitFor(() => {
        expect(document.querySelector('[data-update-refused]')).toBeNull();
      });
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('the account menu trigger', () => {
  it('keeps the account as its name and describes the update', async () => {
    // One cluster and a way to sign back in: Settings is in the account menu.
    console(idle, undefined, 'newer', {
      '/api/clusters': { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] },
      '/api/auth/methods': { password: false, sso: { state: 'ready' } },
    });
    mountApp('/c/prod/x/nope');
    let trigger: HTMLElement | undefined;
    await waitFor(() => {
      trigger = screen.getByRole('button', { name: 'alice' });
      expect(trigger.getAttribute('aria-describedby')).not.toBeNull();
    });
    expect(document.getElementById(trigger?.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Update available');
  });
});
