import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { BulkAffordance, Row } from '../../api';
import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';
import { actingRows, withoutSettledVerbs } from './ListSelectionBar';

afterEach(() => {
  vi.unstubAllGlobals();
});

/*
  A toggle verb is offered where a ticked row would be changed by it, and sent
  only those rows. `Row.unchanged` is the server's answer per row (the half of
  a toggle the object is already on); the bar never reads a cell for it.
*/

const bulk: BulkAffordance = {
  scope: 'prod|cronjobs',
  verbs: [
    { verb: 'suspend', label: 'Suspend', permitted: true },
    { verb: 'resume', label: 'Resume', permitted: true },
    { verb: 'delete', label: 'Delete', permitted: true, destructive: true },
  ],
};

const row = (name: string, unchanged?: string[]): Row => ({
  id: `jobs/${name}`,
  name,
  namespace: 'jobs',
  cells: { name: { text: name } },
  ...(unchanged === undefined ? {} : { unchanged }),
});

const suspended = row('nightly', ['suspend']);
const active = row('hourly', ['resume']);

const verbsOf = (offered: BulkAffordance | undefined): string[] => (offered?.verbs ?? []).map((one) => one.verb);

describe('withoutSettledVerbs', () => {
  it('offers Resume and not Suspend when every ticked CronJob is suspended', () => {
    expect(verbsOf(withoutSettledVerbs(bulk, [suspended, active], new Set([suspended.id])))).toEqual([
      'resume',
      'delete',
    ]);
  });

  it('offers Suspend and not Resume when no ticked CronJob is suspended', () => {
    expect(verbsOf(withoutSettledVerbs(bulk, [suspended, active], new Set([active.id])))).toEqual([
      'suspend',
      'delete',
    ]);
  });

  it('offers both halves over a mixed selection', () => {
    expect(verbsOf(withoutSettledVerbs(bulk, [suspended, active], new Set([suspended.id, active.id])))).toEqual([
      'suspend',
      'resume',
      'delete',
    ]);
  });

  it('keeps every verb on a row that says nothing', () => {
    const plain = row('unknown');
    expect(withoutSettledVerbs(bulk, [plain], new Set([plain.id]))).toBe(bulk);
  });

  it('keeps every verb while no ticked row is still listed', () => {
    expect(withoutSettledVerbs(bulk, [suspended], new Set(['jobs/gone']))).toBe(bulk);
  });
});

describe('actingRows', () => {
  it('sends a toggle only the rows it would change', () => {
    const picked = new Set([suspended.id, active.id]);
    expect(actingRows([suspended, active], picked, 'suspend').map((one) => one.name)).toEqual(['hourly']);
    expect(actingRows([suspended, active], picked, 'resume').map((one) => one.name)).toEqual(['nightly']);
    expect(actingRows([suspended, active], picked, 'delete').map((one) => one.name)).toEqual(['nightly', 'hourly']);
  });
});

const session = {
  body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
};

function routes(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop' } },
    '/api/c/prod/r/cronjobs': {
      body: {
        state: 'ok',
        data: {
          kind: 'cronjobs',
          label: 'CronJobs',
          namespaced: true,
          columns: [{ key: 'name', label: 'Name', visible: true, order: 0, sortable: true, role: 'name' }],
          rows: [suspended, active],
          bulk,
        },
      },
    },
    ...extra,
  };
}

function barControls(): string[] {
  const bar = document.getElementById('bulk-bar');
  if (bar === null) throw new Error('#bulk-bar is missing');
  return [...bar.querySelectorAll('button')].map((one) => one.textContent ?? '');
}

async function tick(name: string): Promise<void> {
  await waitFor(() => {
    expect(document.querySelector('tbody td')).not.toBeNull();
  });
  const line = [...document.querySelectorAll('tbody tr')].find((one) => one.textContent.includes(name));
  const box = line?.querySelector('input[type="checkbox"]');
  if (box === null || box === undefined) throw new Error(`no checkbox on ${name}`);
  fireEvent.click(box);
  await waitFor(() => {
    expect(document.getElementById('bulk-bar')).not.toBeNull();
  });
}

describe('the selection bar on CronJobs', () => {
  it('offers only Resume on a suspended CronJob', async () => {
    stubFetch(routes());
    mountApp('/c/prod/cronjobs');
    await tick('nightly');
    expect(barControls()).toEqual(['Clear', 'Resume', 'Delete']);
  });

  it('offers only Suspend on an active CronJob', async () => {
    stubFetch(routes());
    mountApp('/c/prod/cronjobs');
    await tick('hourly');
    expect(barControls()).toEqual(['Clear', 'Suspend', 'Delete']);
  });

  it('offers both over a mixed selection, and Suspend previews the active one alone', async () => {
    const posted: unknown[] = [];
    stubFetch(
      routes({
        '/api/c/prod/bulk/preview': {
          body: {
            token: 'plan-1',
            expiresAt: '2999-01-01T00:00:00Z',
            sentence: 'Suspend cronjob jobs/hourly.',
            facts: {
              verb: 'suspend',
              button: 'Suspend 1 CronJob',
              count: 1,
              targets: [{ kind: 'cronjobs', namespace: 'jobs', name: 'hourly' }],
              confirmation: { kind: 'acknowledge' },
            },
          },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') posted.push(JSON.parse(String(init.body)));
      return stubbed(input, init);
    });

    mountApp('/c/prod/cronjobs');
    await tick('nightly');
    await tick('hourly');
    expect(barControls()).toEqual(['Clear', 'Suspend', 'Resume', 'Delete']);

    const suspend = [...(document.getElementById('bulk-bar')?.querySelectorAll('button') ?? [])].find(
      (one) => one.textContent === 'Suspend',
    );
    expect(suspend?.title).toBe('Suspend 1 of the 2 selected: the others would not change');
    if (suspend === undefined) throw new Error('no Suspend');
    fireEvent.click(suspend);

    await waitFor(() => {
      expect(document.body.textContent).toContain('Suspend cronjob jobs/hourly.');
    });
    expect(posted).toEqual([
      { verb: 'suspend', kind: 'cronjobs', targets: [{ kind: 'cronjobs', namespace: 'jobs', name: 'hourly' }] },
    ]);
    // The dialog counts what the verb acts on, not the selection.
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Suspend 1 object?');
  });
});
