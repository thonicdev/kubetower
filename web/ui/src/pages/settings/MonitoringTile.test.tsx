import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp } from '../../testing/harness';

/**
 * Settings › General's Monitoring tile: the current cluster's metrics source,
 * a badge for what answers with the discovery's line in its tooltip, and the
 * field only where nothing was found or a source is set by hand. Under the
 * server profile, the deployment's source, read only.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const SESSION = { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' };
const SETTINGS = {
  general: {},
  archive: { state: 'absent', reason: { code: 'not_installed', message: 'no archive' } },
  nodeShell: { enabled: false, available: false },
  info: { statePath: '/state', statePersistent: true, kubeconfigFiles: [], listenAddress: '127.0.0.1:5823', withoutPassword: true, version: 'kubetower test', development: false },
};

type Source = Record<string, unknown>;

const VM: Source = {
  source: 'vm/vmselect-main:8481/select/0/prometheus',
  editable: true,
  kind: 'prometheus',
  engine: 'victoriametrics',
  address: 'vm/vmselect-main:8481/select/0/prometheus',
  transport: 'proxy',
  detail: 'service vm/vmselect-main:8481/select/0/prometheus found via the source set in Settings, reached by proxy, answered by VictoriaMetrics',
};
const NONE: Source = {
  source: '',
  editable: true,
  kind: 'none',
  detail: 'the cluster answered: no Prometheus service matched, and metrics.k8s.io is not registered',
};

interface Sent {
  path: string;
  body: unknown;
}

/** Answers the page's reads, and a PUT with `onPut`. */
function serve(sources: Record<string, Source>, onPut?: (cluster: string, body: { source: string }) => { status: number; body: unknown }): Sent[] {
  const sent: Sent[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    const path = decodeURIComponent(raw.split('?')[0] ?? raw);
    const json = (status: number, body: unknown) =>
      Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' } }));
    const match = /^\/api\/c\/([^/]+)\/metrics\/source$/.exec(path);
    if (match !== null && init?.method === 'PUT') {
      const body = JSON.parse(String(init.body)) as { source: string };
      sent.push({ path, body });
      const reply = onPut?.(match[1] ?? '', body) ?? { status: 500, body: {} };
      return json(reply.status, reply.body);
    }
    if (match !== null) {
      const found = sources[match[1] ?? ''];
      return found === undefined ? json(404, { status: 404, title: 'Not Found', detail: 'unknown cluster' }) : json(200, found);
    }
    const reads: Record<string, unknown> = {
      '/api/session': SESSION,
      '/api/settings': SETTINGS,
      '/api/clusters': {
        state: 'ok',
        items: Object.keys(sources).map((name) => ({ name, label: name, state: 'ok' })),
      },
    };
    const read = reads[path];
    return read === undefined ? json(404, { status: 404, title: 'Not Found', detail: `no stub for ${path}` }) : json(200, read);
  });
  return sent;
}

function tile(): HTMLElement {
  const found = document.querySelector<HTMLElement>('[data-settings="monitoring"]');
  if (found === null) throw new Error('no Monitoring tile');
  return found;
}

function badge(): HTMLElement {
  const found = tile().querySelector<HTMLElement>('[data-source-state]');
  if (found === null) throw new Error('no badge');
  return found;
}

async function settled(state: string): Promise<void> {
  await waitFor(() => {
    expect(badge().getAttribute('data-source-state')).toBe(state);
  });
}

const PROMETHEUS: Source = {
  source: '',
  editable: true,
  kind: 'prometheus',
  engine: 'prometheus',
  address: 'monitoring/kps-prometheus:9090',
  transport: 'proxy',
  detail: 'service monitoring/kps-prometheus:9090 found via app=kube-prometheus-stack-prometheus, reached by proxy',
};

describe('the Monitoring tile', () => {
  it('is about the cluster Settings was opened from, and only that one', async () => {
    serve({ 'prod-eu': PROMETHEUS, lab: NONE });
    mountApp('/settings/general?cluster=lab');

    await settled('none');
    expect(tile().getAttribute('data-monitoring-cluster')).toBe('lab');
    expect(document.querySelectorAll('[data-settings="monitoring"]')).toHaveLength(1);
  });

  it('draws the badge alone where discovery found a source', async () => {
    serve({ 'prod-eu': PROMETHEUS });
    mountApp('/settings/general?cluster=prod-eu');

    await settled('found');
    expect(badge().textContent).toBe('Prometheus');
    expect(badge().querySelector('[title]')?.getAttribute('title')).toContain('kube-prometheus-stack-prometheus');
    expect(tile().querySelector('input')).toBeNull();
  });

  it('names VictoriaMetrics, and keeps a source set by hand in the field so it can be cleared', async () => {
    serve({ 'prod-eu': VM });
    mountApp('/settings/general?cluster=prod-eu');

    await settled('found');
    expect(badge().textContent).toBe('VictoriaMetrics');
    expect(tile().querySelector('input')?.value).toBe('vm/vmselect-main:8481/select/0/prometheus');
  });

  it('offers the field where nothing was found, with the detail in the tooltip', async () => {
    serve({ lab: NONE });
    mountApp('/settings/general?cluster=lab');

    await settled('none');
    expect(badge().textContent).toBe('No metrics available');
    expect(badge().querySelector('[title]')?.getAttribute('title')).toContain('metrics.k8s.io is not registered');
    expect(badge().querySelector('[data-no-metrics]')).not.toBeNull();
    expect(tile().querySelector('input')?.value).toBe('');
  });

  it('offers the field where only metrics-server answers, which keeps no history', async () => {
    serve({ lab: { source: '', editable: true, kind: 'metrics-server', detail: 'metrics-server answers; instant usage available, no history' } });
    mountApp('/settings/general?cluster=lab');

    await settled('metrics-server');
    expect(badge().textContent).toBe('metrics-server');
    expect(tile().querySelector('input')?.value).toBe('');
  });

  it('saves a source and draws what the cluster answers then', async () => {
    const sent = serve({ lab: NONE }, (_cluster, body) => ({
      status: 200,
      body: { ...VM, source: body.source, address: body.source, persisted: true },
    }));
    mountApp('/settings/general?cluster=lab');
    await settled('none');

    const input = tile().querySelector('input') as HTMLInputElement;
    const save = tile().querySelector('button[type="submit"]') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(input, { target: { value: ' vm/vmsingle-main:8428 ' } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);

    await waitFor(() => {
      expect(badge().textContent).toBe('VictoriaMetrics');
    });
    expect(sent).toEqual([{ path: '/api/c/lab/metrics/source', body: { source: 'vm/vmsingle-main:8428' } }]);
    expect((tile().querySelector('input') as HTMLInputElement).value).toBe('vm/vmsingle-main:8428');
  });

  it('clears a source, and the field goes once discovery finds one', async () => {
    const sent = serve({ 'prod-eu': VM }, () => ({ status: 200, body: { ...PROMETHEUS, persisted: true } }));
    mountApp('/settings/general?cluster=prod-eu');
    await settled('found');

    fireEvent.change(tile().querySelector('input') as HTMLInputElement, { target: { value: '' } });
    fireEvent.click(tile().querySelector('button[type="submit"]') as HTMLButtonElement);

    await waitFor(() => {
      expect(badge().textContent).toBe('Prometheus');
    });
    expect(sent).toEqual([{ path: '/api/c/prod-eu/metrics/source', body: { source: '' } }]);
    expect(tile().querySelector('input')).toBeNull();
  });

  it('says in one line why a source was refused, and keeps the badge', async () => {
    serve({ lab: NONE }, () => ({
      status: 400,
      body: { status: 400, title: 'Bad Request', detail: '"vmsingle" is not namespace/service:port[/path]' },
    }));
    mountApp('/settings/general?cluster=lab');
    await settled('none');

    fireEvent.change(tile().querySelector('input') as HTMLInputElement, { target: { value: 'vmsingle' } });
    fireEvent.click(tile().querySelector('button[type="submit"]') as HTMLButtonElement);

    await waitFor(() => {
      expect(tile().querySelector('[role="alert"]')?.textContent).toContain('namespace/service:port');
    });
    expect(badge().getAttribute('data-source-state')).toBe('none');
  });

  it("reads the deployment's source under the server profile, with no field", async () => {
    serve({ 'in-cluster': { ...VM, source: 'vm/vmsingle-main:8428', address: 'vm/vmsingle-main:8428', editable: false } });
    mountApp('/settings/general');

    await settled('found');
    expect(tile().querySelector('input')).toBeNull();
    expect(tile().querySelector('button')).toBeNull();
    expect(tile().textContent).toContain('vm/vmsingle-main:8428');
  });

  it('draws no metrics where the source could not be read at all', async () => {
    serve({ lab: NONE });
    vi.stubGlobal(
      'fetch',
      ((original: typeof fetch) => (input: RequestInfo | URL, init?: RequestInit) => {
        const raw = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
        if (raw.includes('/metrics/source')) {
          return Promise.resolve(
            new Response(JSON.stringify({ status: 500, title: 'x', detail: 'discovery panicked' }), {
              status: 500,
              headers: { 'Content-Type': 'application/problem+json' },
            }),
          );
        }
        return original(input, init);
      })(globalThis.fetch),
    );
    mountApp('/settings/general?cluster=lab');

    await waitFor(
      () => {
        expect(badge().getAttribute('data-source-state')).toBe('failed');
      },
      { timeout: 10_000 },
    );
    expect(badge().textContent).toBe('No metrics available');
    expect(badge().querySelector('[data-no-metrics]')).not.toBeNull();
    // A 500 says why on hover, never reads as "no backend" (#156).
    expect(badge().querySelector('[title]')?.getAttribute('title')).toContain('discovery panicked');
  });

  it('draws a read still on its way as waiting, never as no metrics', async () => {
    serve({ lab: NONE });
    vi.stubGlobal(
      'fetch',
      ((original: typeof fetch) => (input: RequestInfo | URL, init?: RequestInit) => {
        const raw = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
        if (raw.includes('/metrics/source')) return new Promise<Response>(() => undefined);
        return original(input, init);
      })(globalThis.fetch),
    );
    mountApp('/settings/general?cluster=lab');

    await waitFor(() => {
      expect(badge().getAttribute('data-source-state')).toBe('pending');
    });
    expect(badge().textContent).toBe('…');
    expect(badge().querySelector('[data-no-metrics]')).toBeNull();
  });
});
