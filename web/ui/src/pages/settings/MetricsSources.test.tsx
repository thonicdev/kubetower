import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp } from '../../testing/harness';

/**
 * Settings › General's metrics source per cluster: a field on the desktop, the
 * deployment's source read only under the server profile, and a badge for what
 * answers, with the discovery's line in its tooltip.
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

function row(cluster: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`[data-metrics-source="${cluster}"]`);
  if (found === null) throw new Error(`no row for ${cluster}`);
  return found;
}

function badge(cluster: string): HTMLElement {
  const found = row(cluster).querySelector<HTMLElement>('[data-source-state]');
  if (found === null) throw new Error(`no badge for ${cluster}`);
  return found;
}

describe('the metrics source per cluster', () => {
  it('draws one row per cluster, with the engine that answers or no metrics', async () => {
    serve({ 'prod-eu': VM, lab: NONE });
    mountApp('/settings/general');

    await waitFor(() => {
      expect(badge('prod-eu').getAttribute('data-source-state')).toBe('found');
      expect(badge('lab').getAttribute('data-source-state')).toBe('none');
    });
    expect(badge('prod-eu').textContent).toBe('VictoriaMetrics');
    expect(badge('lab').textContent).toBe('No metrics available');
    // The detail is in the tooltip, not on the page.
    expect(badge('lab').querySelector('[title]')?.getAttribute('title')).toContain('metrics.k8s.io is not registered');
    expect(badge('prod-eu').querySelector('[title]')?.getAttribute('title')).toContain('answered by VictoriaMetrics');
    expect(row('prod-eu').querySelector('input')?.value).toBe('vm/vmselect-main:8481/select/0/prometheus');
    expect(row('lab').querySelector('input')?.value).toBe('');
  });

  it('saves a source and draws what the cluster answers then', async () => {
    const sent = serve({ lab: NONE }, (_cluster, body) => ({
      status: 200,
      body: { ...VM, source: body.source, address: body.source, persisted: true },
    }));
    mountApp('/settings/general');
    await waitFor(() => {
      expect(badge('lab').getAttribute('data-source-state')).toBe('none');
    });

    const input = row('lab').querySelector('input') as HTMLInputElement;
    const save = row('lab').querySelector('button[type="submit"]') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(input, { target: { value: ' vm/vmsingle-main:8428 ' } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);

    await waitFor(() => {
      expect(badge('lab').textContent).toBe('VictoriaMetrics');
    });
    expect(sent).toEqual([{ path: '/api/c/lab/metrics/source', body: { source: 'vm/vmsingle-main:8428' } }]);
    expect(input.value).toBe('vm/vmsingle-main:8428');
  });

  it('says in one line why a source was refused, and keeps the badge', async () => {
    serve({ lab: NONE }, () => ({
      status: 400,
      body: { status: 400, title: 'Bad Request', detail: '"vmsingle" is not namespace/service:port[/path]' },
    }));
    mountApp('/settings/general');
    await waitFor(() => {
      expect(badge('lab').getAttribute('data-source-state')).toBe('none');
    });

    fireEvent.change(row('lab').querySelector('input') as HTMLInputElement, { target: { value: 'vmsingle' } });
    fireEvent.click(row('lab').querySelector('button[type="submit"]') as HTMLButtonElement);

    await waitFor(() => {
      expect(row('lab').querySelector('[role="alert"]')?.textContent).toContain('namespace/service:port');
    });
    expect(badge('lab').getAttribute('data-source-state')).toBe('none');
  });

  it('reads the deployment\'s source under the server profile, with no field', async () => {
    serve({ 'in-cluster': { ...VM, source: 'vm/vmsingle-main:8428', address: 'vm/vmsingle-main:8428', editable: false } });
    mountApp('/settings/general');

    await waitFor(() => {
      expect(badge('in-cluster').getAttribute('data-source-state')).toBe('found');
    });
    expect(row('in-cluster').querySelector('input')).toBeNull();
    expect(row('in-cluster').querySelector('button')).toBeNull();
    expect(row('in-cluster').textContent).toContain('vm/vmsingle-main:8428');
  });

  it('draws no metrics where the source could not be read at all', async () => {
    serve({ lab: NONE });
    vi.stubGlobal(
      'fetch',
      ((original: typeof fetch) => (input: RequestInfo | URL, init?: RequestInit) => {
        const raw = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
        if (raw.includes('/metrics/source')) return Promise.resolve(new Response(JSON.stringify({ status: 500, title: 'x' }), { status: 500, headers: { 'Content-Type': 'application/problem+json' } }));
        return original(input, init);
      })(globalThis.fetch),
    );
    mountApp('/settings/general');

    await waitFor(
      () => {
        expect(badge('lab').getAttribute('data-source-state')).toBe('failed');
      },
      { timeout: 10_000 },
    );
    expect(badge('lab').textContent).toBe('No metrics available');
  });
});
