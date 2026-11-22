import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { kubecron, PAGE_PATH } from './index';
import { CELL } from './Heatmap';
import { yearWindow } from './PageHeatmap';
import { backHere, backOf } from './data';
import { installCommand, sentence, settingsHref } from './InstallCollector';

// The stubbed `fetch` goes with the test that stubbed it. The extra's own
// setup file did this for every test before the extras came into the core;
// the core's (`ui/src/testing/setup.ts`) unmounts but restores no global.
afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * The pages, mounted the way the console mounts them, with a fetch stub
 * standing in for `kubecron.go`. Every body is the shape the Go route writes.
 */

type Replies = Record<string, { status?: number; body: unknown; contentType?: string }>;

function stub(replies: Replies): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const reply = replies[url];
    if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    const body = typeof reply.body === 'string' ? reply.body : JSON.stringify(reply.body);
    return Promise.resolve(
      new Response(body, { status: reply.status ?? 200, headers: { 'Content-Type': reply.contentType ?? 'application/json' } }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function mount(path: string, from?: string): ReturnType<typeof createMemoryRouter> {
  // `from`: the page the reader came from, one entry back in the history.
  const entries = from === undefined ? [path] : [from, path];
  const router = createMemoryRouter([{ path: 'c/:cluster', children: kubecron.routes }], {
    initialEntries: entries.map((p) => `/c/docker-desktop/${p}`),
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

/** The size utilities on each heatmap square and legend swatch, as drawn: the
 *  page's, a CronJob's and the tab's are one size. */
function drawnSizes(): Set<string> {
  const drawn = [...document.querySelectorAll('[data-day] > *, [data-legend] span.inline-block')];
  return new Set(drawn.flatMap((e) => [...e.classList].filter((c) => /^(size|w|h|aspect|min-w|basis)-/.test(c))));
}

const BOARD = '/x/docker-desktop/kubecron/';

const cronJob = (over: Record<string, unknown> = {}) => ({
  namespace: 'kc-test',
  name: 'always-fails',
  schedule: '*/3 * * * *',
  suspended: false,
  verdict: 'ok',
  activeCount: 0,
  runs: [],
  ...over,
});

const board = (over: Record<string, unknown> = {}) => ({
  collectedAt: '2026-10-20T12:00:00Z',
  complete: true,
  retained: { ok: true },
  history: { ok: true, detail: 'a KubeCron collector at kubecron/kubecron, watching since 20 Oct 2026' },
  collector: { state: 'ok', where: 'kubecron/kubecron', runDays: 90, logDays: 14, metricsEnabled: true },
  counts: { total: 1, missed: 0, unknown: 0, suspended: 0, active: 0, failing: 1, namespaces: 1 },
  cronJobs: [cronJob({ history: { runs: 5, failed: 5, lastFailure: '2026-10-20T11:57:00Z', avgDurationMs: 5000 } })],
  ...over,
});

describe('KubeCron', () => {
  it('lives at x/kubecron and draws each CronJob with its verdict and its failures', async () => {
    stub({ [BOARD]: { body: board() } });
    mount(PAGE_PATH);
    expect(await screen.findByText('5 of 5 failed')).toBeTruthy();
    expect(screen.getByText('On time')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'always-fails' }).getAttribute('href')).toBe(
      '/c/docker-desktop/x/kubecron/kc-test/always-fails',
    );
    expect(kubecron.key).toBe('kubecron');
  });

  it('draws "cannot say" in its own word with its reason, never as on time', async () => {
    stub({
      [BOARD]: {
        body: board({
          cronJobs: [cronJob({ verdict: 'unknown', unresolved: 'spec.timeZone is "Mars/Olympus", which is not an IANA zone name' })],
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText('Cannot say')).toBeTruthy();
    expect(screen.getByText(/Mars\/Olympus/)).toBeTruthy();
    expect(screen.queryByText('On time')).toBeNull();
  });

  it('with no collector, keeps the verdicts and says what to install, with the command for this cluster', async () => {
    stub({
      [BOARD]: {
        body: board({
          collector: { state: 'absent', detail: 'no KubeCron collector runs in this cluster.' },
          history: { ok: false, detail: 'no KubeCron collector runs in this cluster.' },
          cronJobs: [cronJob()],
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByRole('heading', { name: 'Run history needs the KubeCron collector' })).toBeTruthy();
    expect(screen.getByText('On time')).toBeTruthy();
    const command = document.querySelector('[data-collector="absent"] pre')?.textContent ?? '';
    expect(command).toBe(installCommand('docker-desktop'));
    expect(command).toContain('--set mode=server');
    expect(command).toContain('--set clusterID=docker-desktop');
    // Nothing to configure for the chart's names: no
    // variable to set and no restart, and where to set another.
    const configure = document.querySelector('[data-install-step="configure"]')?.textContent ?? '';
    expect(configure).toContain('Nothing to configure');
    expect(configure).toContain('kubecron-api');
    expect(configure).not.toContain('KT_KUBECRON_COLLECTOR=');
    expect(document.querySelector('[data-collector="absent"]')?.textContent).not.toMatch(/and restart/i);
    expect(
      document.querySelector('[data-install-step="configure"] [data-collector-settings]')?.getAttribute('href'),
    ).toBe(settingsHref('docker-desktop'));
    expect(screen.getByRole('link', { name: 'KubeCron project' }).getAttribute('href')).toBe(
      'https://github.com/schmitech-fr/kubecron',
    );
    expect(screen.getByText(/^No KubeCron collector runs in this cluster\. The verdicts/)).toBeTruthy();
  });

  it("opens a paragraph with the route's lower-case sentence, capitalized and terminated", () => {
    expect(sentence('no collector here')).toBe('No collector here.');
    expect(sentence('no collector here.')).toBe('No collector here.');
  });

  it('says a collector that cannot be read in its own words, and does not offer to install one', async () => {
    stub({
      [BOARD]: {
        body: board({
          collector: { state: 'unusable', detail: 'its token could not be read from Secret kubecron/kubecron-api' },
          history: { ok: false },
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/could not be read: its token could not be read/)).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Run history needs the KubeCron collector' })).toBeNull();
  });

  it('draws each step of a collector that cannot be read, and where to set it, with no restart', async () => {
    stub({
      [BOARD]: {
        body: board({
          collector: {
            state: 'unusable',
            detail:
              'the token\'s Secret kubecron/kubecron-api: access refused (403): the console\'s credentials may not get secrets/kubecron-api in namespace "kubecron"',
            via: 'label',
            checks: [
              { step: 'collector', state: 'ok', detail: 'the collector\'s Service kubecron/kubecron, the one labeled kubecron.io/collector=true', value: 'kubecron/kubecron', source: 'default' },
              {
                step: 'secret',
                state: 'denied',
                detail: 'the token\'s Secret kubecron/kubecron-api: access refused (403): the console\'s credentials may not get secrets/kubecron-api in namespace "kubecron"',
                value: 'kubecron/kubecron-api',
                source: 'default',
              },
            ],
          },
          history: { ok: false },
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/^The KubeCron collector could not be read: the token's Secret/)).toBeTruthy();
    const help = document.querySelector('[data-collector-help="unusable"]');
    expect(help?.querySelector('[data-check="collector"]')?.getAttribute('data-check-state')).toBe('ok');
    expect(help?.querySelector('[data-check="secret"]')?.getAttribute('data-check-state')).toBe('denied');
    expect(help?.querySelector('[data-check="secret"]')?.textContent).toContain('may not get secrets/kubecron-api');
    expect(help?.querySelector('[data-check="secret"] [data-check-source]')?.textContent).toBe('(default)');
    expect(help?.querySelector('[data-collector-settings]')?.getAttribute('href')).toBe(settingsHref('docker-desktop'));
    expect(help?.textContent).toContain('with no restart');
    expect(help?.textContent).not.toMatch(/and restart/i);
  });

  it('names every labeled collector when there are several, and installs nothing', async () => {
    stub({
      [BOARD]: {
        body: board({
          collector: {
            state: 'unusable',
            detail: '2 Services are labeled kubecron.io/collector=true (a/kubecron, b/kubecron), and none is chosen: name the collector in Settings › Extra › KubeCron',
            candidates: ['a/kubecron', 'b/kubecron'],
            checks: [{ step: 'collector', state: 'several', detail: '2 Services are labeled kubecron.io/collector=true (a/kubecron, b/kubecron), and none is chosen', source: 'default' }],
          },
          history: { ok: false },
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/and none is chosen: name the collector/)).toBeTruthy();
    expect(document.querySelector('[data-check="collector"]')?.getAttribute('data-check-state')).toBe('several');
    expect(screen.queryByRole('heading', { name: 'Run history needs the KubeCron collector' })).toBeNull();
  });

  it('says a configuration that cannot be used as such, never as a collector that is here', async () => {
    stub({
      [BOARD]: {
        body: board({
          collector: {
            state: 'misconfigured',
            detail: 'KT_KUBECRON_COLLECTOR="kubecron" is not <namespace>/<service>, so no collector is read',
            checks: [{ step: 'collector', state: 'failed', detail: 'KT_KUBECRON_COLLECTOR="kubecron" is not <namespace>/<service>, so no collector is read' }],
          },
          history: { ok: false },
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/KubeCron configuration is invalid: KT_KUBECRON_COLLECTOR="kubecron" is not/)).toBeTruthy();
    expect(screen.queryByText(/The KubeCron collector could not be read/)).toBeNull();
    expect(document.querySelector('[data-collector-help="misconfigured"] [data-collector-settings]')).toBeTruthy();
  });

  it('does not ask for a variable already set when the Service it names is not in this cluster', async () => {
    stub({
      [BOARD]: {
        body: board({
          collector: {
            state: 'absent',
            configured: 'kubecron/kubecron',
            detail: 'KT_KUBECRON_COLLECTOR names kubecron/kubecron, which does not exist in this cluster, and no Service here is labeled kubecron.io/collector=true',
            checks: [
              { step: 'collector', state: 'missing', detail: 'KT_KUBECRON_COLLECTOR names kubecron/kubecron, which does not exist in this cluster; looking for the one labeled kubecron.io/collector=true', value: 'kubecron/kubecron', source: 'environment' },
              { step: 'collector', state: 'missing', detail: 'no KubeCron collector runs in this cluster', source: 'default' },
            ],
          },
          history: { ok: false },
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/^KT_KUBECRON_COLLECTOR names kubecron\/kubecron, which does not exist in this cluster/)).toBeTruthy();
    expect(document.querySelector('[data-install-step="configure"]')).toBeNull();
    expect(document.querySelector('[data-install-step="configured"]')?.textContent).toContain('kubecron/kubecron');
    // The variable decides it, and the Settings page would refuse the write.
    expect(document.querySelector('[data-install-step="configured"]')?.textContent).toContain('change KT_KUBECRON_COLLECTOR');
    expect(document.querySelector('[data-install-step="configured"] [data-collector-settings]')).toBeNull();
  });

  it('says which way a collector was reached when the label found it', async () => {
    stub({
      [BOARD]: {
        body: board({
          collector: {
            state: 'ok',
            where: 'ops/cron',
            via: 'label',
            source: 'KT_KUBECRON_COLLECTOR names kubecron/kubecron, which does not exist in this cluster; the collector at ops/cron was found by its label kubecron.io/collector=true, and is read without a token',
          },
        }),
      },
    });
    mount(PAGE_PATH);
    expect((await screen.findByLabelText('Summary')).getAttribute('title')).toMatch(/was found by its label/);
  });

  it('draws a CronJob list that failed as a failure, never as an empty page', async () => {
    stub({
      [BOARD]: {
        status: 502,
        body: { code: 'list_failed', message: 'The CronJobs could not be read, so whether anything is late on this cluster is unknown.' },
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/could not be read, so whether anything is late/)).toBeTruthy();
    expect(screen.queryByText(/no CronJob this console may read/)).toBeNull();
  });

  it("shows the gate's own sentence, not the Settings switch, when the gate said why", async () => {
    // A switch locked by KT_EXTRAS: the Settings switch cannot help.
    stub({
      [BOARD]: {
        status: 404,
        contentType: 'text/plain; charset=utf-8',
        body:
          'KubeCron is not enabled on this KubeTower: it is turned off by KT_EXTRAS=-kubecron ' +
          'in the environment, which the console cannot change while it runs. ' +
          'Change KT_EXTRAS where the deployment sets it and restart KubeTower.\n',
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/which the console cannot change while it runs/)).toBeTruthy();
    expect(screen.queryByText(/Turn it on in Settings/)).toBeNull();
  });

  it('says the console turned the page off when the gate sent no sentence', async () => {
    stub({ [BOARD]: { status: 404, contentType: 'text/plain', body: '' } });
    mount(PAGE_PATH);
    expect(await screen.findByText(/KubeCron is not enabled for this cluster/)).toBeTruthy();
  });
});

describe("one CronJob's page", () => {
  const DETAIL = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';

  it('draws days before the collector watched as not recorded, and the runs with their exit codes', async () => {
    const today = new Date().toISOString().slice(0, 10);
    stub({
      [DETAIL]: {
        body: {
          cronJob: cronJob(),
          collector: { state: 'ok', where: 'kubecron/kubecron' },
          daily: { windowDays: 90, observedSince: `${today}T08:00:00Z`, days: [{ day: today, total: 4, succeeded: 0, running: 0, failed: 4 }] },
          runs: { runs: [{ id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status: 'failed', exitCode: 3, retryCount: 0, logSizeBytes: 40, durationMs: 5000 }], nextCursor: '' },
        },
      },
    });
    mount(`${PAGE_PATH}/kc-test/always-fails`);
    expect(await screen.findByRole('heading', { name: 'always-fails' })).toBeTruthy();
    await screen.findByText('5s');
    expect(document.querySelector(`[data-day="${today}"]`)?.getAttribute('data-square')).toBe('failed');
    const squares = [...document.querySelectorAll('[data-square]')].map((s) => s.getAttribute('data-square'));
    // The page's year at the page's size, every day before the collector
    // watched not recorded.
    expect(squares).toHaveLength(yearWindow(today));
    expect(squares.filter((s) => s === 'unrecorded')).toHaveLength(yearWindow(today) - 1);
    expect(squares).not.toContain('none');
    // The same squares as the page's year, and its legend.
    expect(drawnSizes()).toEqual(new Set([CELL]));
    expect(document.querySelector('[data-legend]')).not.toBeNull();
    expect(screen.getByText('3')).toBeTruthy();
  });

  it("opens one run in the list's place, keeping the header and the calendar, and goes back to all runs", async () => {
    const today = new Date().toISOString().slice(0, 10);
    stub({
      [DETAIL]: {
        body: {
          cronJob: cronJob(),
          collector: { state: 'ok', where: 'kubecron/kubecron' },
          daily: { windowDays: 90, observedSince: `${today}T08:00:00Z`, days: [{ day: today, total: 1, succeeded: 0, running: 0, failed: 1 }] },
          runs: { runs: [{ id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status: 'failed', exitCode: 3, retryCount: 0, logSizeBytes: 40, durationMs: 5000 }], nextCursor: '' },
        },
      },
      '/x/docker-desktop/kubecron/runs/r1': {
        body: {
          run: { id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status: 'failed', retryCount: 0, logSizeBytes: 40, durationMs: 5000, exitCode: 3 },
          namespace: 'kc-test',
          cronJob: 'always-fails',
          collector: { state: 'ok' },
          log: { lines: [{ at: `${today}T09:00:01Z`, text: 'ERROR: connection refused' }], sizeBytes: 40, expired: false, truncated: false, running: false },
          samples: { samples: [], intervalSeconds: 15, metricsEnabled: true },
        },
      },
    });
    const router = mount(`${PAGE_PATH}/kc-test/always-fails`);
    await screen.findByRole('region', { name: 'Runs' });
    // Each row's Log unfolds the log in the page, beside the runs, which
    // stay; full-page is the log's own button.
    fireEvent.click(document.querySelector('[data-run="r1"] [data-log-toggle]') as HTMLElement);
    expect(await screen.findByText('connection refused', { exact: false })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Runs' })).toBeTruthy();
    expect(document.querySelector('[data-log-layout="open"] [data-inline-log="r1"]')).toBeTruthy();
    expect(router.state.location.pathname).toBe(`/c/docker-desktop/${PAGE_PATH}/kc-test/always-fails`);
    expect(new URLSearchParams(router.state.location.search).get('log')).toBe('r1');
    expect(document.querySelector('[data-log-full]')?.getAttribute('href')).toBe('/c/docker-desktop/x/kubecron/runs/r1/log');
    fireEvent.click(screen.getByRole('button', { name: 'Close the log' }));
    await waitFor(() => expect(document.querySelector('[data-inline-log]')).toBeNull());
    fireEvent.click(document.querySelector('[data-run="r1"] button') as HTMLElement);
    expect(await screen.findByText('Exit code')).toBeTruthy();
    // No log is drawn until Log is pressed.
    expect(screen.queryByText('ERROR: connection refused')).toBeNull();
    // The list is gone; the header and the calendar are still there.
    expect(screen.queryByRole('region', { name: 'Runs' })).toBeNull();
    expect(document.querySelector(`[data-day="${today}"]`)).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'always-fails' })).toBeTruthy();
    // The run's own Log unfolds beside it, and folds again.
    fireEvent.click(document.querySelector('[data-run-body] [data-log-toggle]') as HTMLElement);
    expect(await screen.findByText('connection refused', { exact: false })).toBeTruthy();
    expect(document.querySelector('[data-run-body] [data-log-toggle]')?.getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(document.querySelector('[data-run-body] [data-log-toggle]') as HTMLElement);
    await waitFor(() => expect(document.querySelector('[data-inline-log]')).toBeNull());
    fireEvent.click(screen.getByRole('button', { name: '← All runs' }));
    expect(await screen.findByRole('region', { name: 'Runs' })).toBeTruthy();
    expect(document.querySelector('[data-run-body]')).toBeNull();
  });

  it('answers a CronJob the person may not see as absent', async () => {
    stub({
      [DETAIL]: {
        status: 404,
        body: { code: 'cronjob_not_found', message: 'There is no CronJob kc-test/always-fails on this cluster that this console may read.' },
      },
    });
    mount(`${PAGE_PATH}/kc-test/always-fails`);
    expect(await screen.findByText(/that this console may read/)).toBeTruthy();
  });

  // A CronJob reached through KubeCron must be deletable: drawing its
  // history and none of its verbs left no way to act on it. It draws the
  // console's own, from the object's page, and Delete previews through the
  // console's plan rather than anything of KubeCron's.
  it("offers the CronJob's own verbs, Delete among them, through the console's plan", async () => {
    const fetch = stub({
      [DETAIL]: {
        body: { cronJob: cronJob(), collector: { state: 'ok' }, daily: { windowDays: 90, days: [] }, runs: { runs: [] } },
      },
      '/api/c/docker-desktop/r/cronjobs/kc-test/always-fails': {
        body: {
          state: 'ok',
          data: {
            kind: 'cronjobs',
            namespace: 'kc-test',
            name: 'always-fails',
            title: 'CronJob',
            tabs: [],
            actions: [
              { verb: 'trigger', label: 'Run now', permitted: true },
              { verb: 'suspend', label: 'Suspend', permitted: true },
              { verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true },
            ],
          },
        },
      },
    });
    mount(`${PAGE_PATH}/kc-test/always-fails`);
    const group = await screen.findByRole('group', { name: 'Actions' });
    expect([...group.querySelectorAll('button')].map((b) => b.textContent?.trim())).toEqual(['Run now', 'Suspend', 'Delete']);
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    await waitFor(() => {
      const posted = fetch.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'POST');
      expect(posted.map(([input]) => String(input))).toContain('/api/c/docker-desktop/actions/preview');
    });
    const preview = fetch.mock.calls.find(([input]) => String(input) === '/api/c/docker-desktop/actions/preview');
    expect(JSON.parse(String((preview?.[1] as RequestInit).body))).toMatchObject({
      verb: 'delete',
      target: { kind: 'cronjobs', namespace: 'kc-test', name: 'always-fails' },
    });
  });

  // After a Delete the CronJob is gone and its page has nothing to show. The
  // stubs answer the CronJob as present until the Delete is sent, then absent;
  // `deleteFrom` presses Delete on the page and returns its router.
  const deleteFrom = async (back?: string): Promise<ReturnType<typeof createMemoryRouter>> => {
    let gone = false;
    const page = {
      kind: 'cronjobs',
      namespace: 'kc-test',
      name: 'always-fails',
      title: 'CronJob',
      tabs: [],
      actions: [{ verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true }],
    };
    const json = (body: unknown): Promise<Response> =>
      Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        if (url === DETAIL) {
          return json({ cronJob: cronJob(), collector: { state: 'ok' }, daily: { windowDays: 90, days: [] }, runs: { runs: [] } });
        }
        if (url === '/api/c/docker-desktop/r/cronjobs/kc-test/always-fails') {
          return json(gone ? { state: 'absent', reason: { code: 'not_found', message: 'gone' } } : { state: 'ok', data: page });
        }
        if (url === '/api/c/docker-desktop/actions/preview') {
          return json({
            token: 'plan-1',
            expiresAt: '2999-01-01T00:00:00Z',
            sentence: 'Delete the CronJob always-fails.',
            facts: {
              verb: 'delete',
              button: 'Delete',
              count: 1,
              destructive: true,
              targets: [{ kind: 'cronjobs', namespace: 'kc-test', name: 'always-fails' }],
              confirmation: { kind: 'acknowledge', expected: 'confirm' },
            },
          });
        }
        if (url === '/api/c/docker-desktop/actions') {
          gone = true;
          return json({ succeeded: 1, failed: 0, results: [], summary: 'Deleted CronJob always-fails.' });
        }
        return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
      }),
    );
    const path = `/c/docker-desktop/${PAGE_PATH}/kc-test/always-fails`;
    const router = createMemoryRouter([{ path: 'c/:cluster', children: kubecron.routes }], {
      initialEntries: [back === undefined ? path : { pathname: path, state: backHere({ pathname: back, search: '' }) }],
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    const group = await screen.findByRole('group', { name: 'Actions' });
    fireEvent.click(within(group).getByRole('button', { name: 'Delete' }));
    const commit = await waitFor(() => {
      const button = document.querySelector('[data-commit]');
      if (button === null) throw new Error('no plan dialog yet');
      return button as HTMLButtonElement;
    });
    fireEvent.click(commit);
    return router;
  };

  // It goes back to KubeCron, as the reader came from nowhere else.
  it('leaves for KubeCron once its CronJob is deleted', async () => {
    const router = await deleteFrom();
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(`/c/docker-desktop/${PAGE_PATH}`);
    });
  });

  // Opened from another page of the console, it goes back there.
  it('leaves for the page it was opened from once its CronJob is deleted', async () => {
    const router = await deleteFrom('/c/docker-desktop/jobs/kc-test');
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/c/docker-desktop/jobs/kc-test');
    });
  });

  // Opened from the CronJob's own KubeCron tab, going back would land on that
  // CronJob's page, gone as well: it goes to KubeCron instead.
  it('does not go back to the deleted CronJob own page in the console', async () => {
    const router = await deleteFrom('/c/docker-desktop/cronjobs/kc-test/always-fails');
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(`/c/docker-desktop/${PAGE_PATH}`);
    });
  });

  it('says why a run has no CPU or memory figure rather than drawing 0', async () => {
    stub({
      [`${DETAIL}`]: {
        body: { cronJob: cronJob(), collector: { state: 'ok' }, daily: { windowDays: 90, days: [] }, runs: { runs: [] } },
      },
      '/x/docker-desktop/kubecron/runs/r1': {
        body: {
          run: { id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: '2026-10-20T09:00:00Z', status: 'failed', retryCount: 0, logSizeBytes: 2048, durationMs: 5000, exitCode: 3 },
          namespace: 'kc-test',
          cronJob: 'always-fails',
          collector: { state: 'ok' },
          log: { lines: [], sizeBytes: 2048, expired: true, truncated: false, running: false },
          samples: { samples: [], intervalSeconds: 15, metricsEnabled: true },
        },
      },
    });
    mount(`${PAGE_PATH}/kc-test/always-fails?run=r1`);
    expect(await screen.findByText(/ended inside one sampling interval \(15s\)/)).toBeTruthy();
    expect([...document.querySelectorAll('[data-missing]')].map((e) => e.textContent)).toEqual(['not recorded', 'not recorded']);
    expect(document.querySelector('[data-run-stats]')?.textContent).not.toMatch(/\b0m\b|\b0 B\b/);
  });

  it('draws the next occurrence of a suspended CronJob as one that will not fire, under a Firing column', async () => {
    stub({
      [BOARD]: {
        body: board({
          cronJobs: [cronJob({ name: 'paused-migration', suspended: true, verdict: 'suspended', nextAt: '2026-10-21T02:30:00Z' })],
        }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText('none, suspended')).toBeTruthy();
    expect(screen.getByText(/would be/)).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: /Firing/ })).toBeTruthy();
  });

  it('marks a CronJob whose last three runs failed as Failing, beside Firing', async () => {
    stub({
      [BOARD]: {
        body: board({ cronJobs: [cronJob({ history: { runs: 5, failed: 5, failing: true } })] }),
      },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText('Failing')).toBeTruthy();
    expect(screen.getByText('On time')).toBeTruthy();
  });

  it('keeps the header and the heatmap on screen while a day is read: no reload on a click', async () => {
    const today = new Date().toISOString().slice(0, 10);
    const DETAIL = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';
    const one = { id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status: 'failed', exitCode: 3, retryCount: 0, logSizeBytes: 40, durationMs: 5000 };
    const body = JSON.stringify({
      cronJob: cronJob(),
      collector: { state: 'ok', where: 'kubecron/kubecron' },
      daily: { windowDays: 90, observedSince: `${today}T00:00:00Z`, days: [{ day: today, total: 1, succeeded: 0, running: 0, failed: 1 }] },
      runs: { runs: [one], nextCursor: '' },
    });
    const reply = () => new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } });
    // The day's answer is held until the test lets it go.
    let release: () => void = () => undefined;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        return url === DETAIL ? Promise.resolve(reply()) : held.then(reply);
      }),
    );
    mount(`${PAGE_PATH}/kc-test/always-fails`);
    await screen.findByRole('region', { name: 'Runs' });
    const heatmap = document.querySelector('[data-heatmap-scroll]');
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }));
    // While the day is read, the page stays as it was: the same heatmap
    // element, the header, the runs dimmed, and no "Loading" in their place.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(document.querySelector('[data-heatmap-scroll]')).toBe(heatmap);
    expect(screen.getByRole('heading', { name: 'always-fails' })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Runs' }).closest('[aria-busy]')?.getAttribute('aria-busy')).toBe('true');
    release();
    expect(await screen.findByText(`Runs of ${today} (UTC)`)).toBeTruthy();
    expect(document.querySelector('[data-heatmap-scroll]')).toBe(heatmap);
  });

  it("never draws one CronJob's runs under another's name while the other is read", async () => {
    const today = new Date().toISOString().slice(0, 10);
    const A = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';
    const one = { id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status: 'failed', exitCode: 3, retryCount: 0, logSizeBytes: 40, durationMs: 5000 };
    const body = JSON.stringify({
      cronJob: cronJob(),
      collector: { state: 'ok', where: 'kubecron/kubecron' },
      daily: { windowDays: 90, observedSince: `${today}T00:00:00Z`, days: [{ day: today, total: 1, succeeded: 0, running: 0, failed: 1 }] },
      runs: { runs: [one], nextCursor: '' },
    });
    // The other CronJob's answer never comes.
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
        return url === A ? Promise.resolve(new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } })) : new Promise<Response>(() => undefined);
      }),
    );
    const router = mount(`${PAGE_PATH}/kc-test/always-fails`);
    await screen.findByRole('region', { name: 'Runs' });
    expect(document.querySelector('[data-run="r1"]')).toBeTruthy();
    await router.navigate(`/c/docker-desktop/${PAGE_PATH}/kc-test/other`);
    expect(await screen.findByRole('heading', { name: 'other' })).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(document.querySelector('[data-run="r1"]')).toBeNull();
    expect(document.querySelector('[data-heatmap-scroll]')).toBeNull();
  });

  it('moves an open log to the run picked, and closes it on another page of runs', async () => {
    const today = new Date().toISOString().slice(0, 10);
    const DETAIL = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';
    const run = (id: string, hour: string) => ({ id, pod: `always-fails-${id}`, trigger: 'scheduled', startedAt: `${today}T${hour}:00:00Z`, status: 'failed', exitCode: 3, retryCount: 0, logSizeBytes: 40, durationMs: 5000 });
    const answer = (id: string, hour: string) => ({
      body: {
        run: run(id, hour),
        namespace: 'kc-test',
        cronJob: 'always-fails',
        collector: { state: 'ok' },
        log: { lines: [{ at: `${today}T${hour}:00:01Z`, text: `line of ${id}` }], sizeBytes: 40, expired: false, truncated: false, running: false },
        samples: { samples: [], intervalSeconds: 15, metricsEnabled: true },
      },
    });
    const detail = {
      body: {
        cronJob: cronJob(),
        collector: { state: 'ok', where: 'kubecron/kubecron' },
        daily: { windowDays: 90, observedSince: `${today}T00:00:00Z`, days: [{ day: today, total: 2, succeeded: 0, running: 0, failed: 2 }] },
        runs: { runs: [run('r2', '10'), run('r1', '09')], nextCursor: 'c1' },
      },
    };
    stub({
      [DETAIL]: detail,
      [`${DETAIL}?before=c1`]: detail,
      '/x/docker-desktop/kubecron/runs/r1': answer('r1', '09'),
      '/x/docker-desktop/kubecron/runs/r2': answer('r2', '10'),
    });
    const router = mount(`${PAGE_PATH}/kc-test/always-fails`);
    await screen.findByRole('region', { name: 'Runs' });
    fireEvent.click(document.querySelector('[data-run="r1"] [data-log-toggle]') as HTMLElement);
    expect(await screen.findByText('line of r1')).toBeTruthy();
    // Picking r2 opens r2 with its own log beside it, never r1's.
    fireEvent.click(document.querySelector('[data-run="r2"] button') as HTMLElement);
    expect(await screen.findByText('line of r2')).toBeTruthy();
    expect(screen.queryByText('line of r1')).toBeNull();
    expect(document.querySelector('[data-run-body] [data-log-toggle]')?.getAttribute('aria-pressed')).toBe('true');
    // Back to the list, a log open, then Older: the log closes.
    fireEvent.click(screen.getByRole('button', { name: '← All runs' }));
    await screen.findByRole('region', { name: 'Runs' });
    fireEvent.click(document.querySelector('[data-run="r1"] [data-log-toggle]') as HTMLElement);
    expect(await screen.findByText('line of r1')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Older' }));
    await waitFor(() => expect(document.querySelector('[data-inline-log]')).toBeNull());
    expect(new URLSearchParams(router.state.location.search).get('log')).toBeNull();
  });

  it('narrows the runs to a day of the calendar, and All runs clears it', async () => {
    const today = new Date().toISOString().slice(0, 10);
    const DETAIL = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';
    const body = (runs: unknown[]) => ({
      body: {
        cronJob: cronJob(),
        collector: { state: 'ok', where: 'kubecron/kubecron' },
        daily: { windowDays: 90, observedSince: `${today}T00:00:00Z`, days: [{ day: today, total: 1, succeeded: 0, running: 0, failed: 1 }] },
        runs: { runs, nextCursor: '' },
      },
    });
    const one = { id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status: 'failed', exitCode: 3, retryCount: 0, logSizeBytes: 40, durationMs: 5000 };
    const fetch = stub({ [DETAIL]: body([one]), [`${DETAIL}?day=${today}`]: body([one]) });
    mount(`${PAGE_PATH}/kc-test/always-fails`);
    await screen.findByRole('region', { name: 'Runs' });
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }));
    expect(await screen.findByText(`Runs of ${today} (UTC)`)).toBeTruthy();
    expect(fetch.mock.calls.map(([input]) => String(input))).toContain(`${DETAIL}?day=${today}`);
    fireEvent.click(screen.getByRole('button', { name: 'All runs' }));
    await screen.findByRole('region', { name: 'Runs' });
    expect(screen.queryByText(`Runs of ${today} (UTC)`)).toBeNull();
  });
});

describe("the page's heatmap", () => {
  const HEAT = '/x/docker-desktop/kubecron/daily';
  const today = new Date().toISOString().slice(0, 10);
  const yesterday = new Date(Date.now() - 86_400_000).toISOString().slice(0, 10);
  const heat = (over: Record<string, unknown> = {}) => ({
    collector: { state: 'ok', where: 'kubecron/kubecron', runDays: 90 },
    complete: true,
    windowDays: 90,
    runDays: 90,
    observedSince: '2020-01-01T00:00:00Z',
    cronJobs: [
      { namespace: 'kc-test', name: 'always-fails', days: [{ day: today, total: 3, succeeded: 0, running: 0, failed: 3 }] },
      {
        namespace: 'ops',
        name: 'nightly',
        days: [
          { day: yesterday, total: 1, succeeded: 1, running: 0, failed: 0 },
          { day: today, total: 2, succeeded: 2, running: 0, failed: 0 },
        ],
      },
    ],
    unread: [],
    ...over,
  });
  const twoCronJobs = () =>
    board({ cronJobs: [cronJob(), cronJob({ namespace: 'ops', name: 'nightly' })], counts: { ...board().counts, total: 2 } });
  const total = () => document.querySelector('[data-heatmap-total]')?.getAttribute('data-heatmap-total');

  it("opens a CronJob with the page's heatmap and its outcome filter, no CronJob pickers, and an arrow back to the page as it was", async () => {
    const DETAIL = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';
    const r = (id: string, status: string) => ({ id, pod: `p-${id}`, trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status, exitCode: status === 'failed' ? 1 : 0, retryCount: 0, logSizeBytes: 4, durationMs: 1000 });
    const detail = () => ({
      body: {
        cronJob: cronJob(),
        collector: { state: 'ok', where: 'kubecron/kubecron' },
        daily: { windowDays: 90, observedSince: '2020-01-01T00:00:00Z', days: [{ day: today, total: 3, succeeded: 1, running: 0, failed: 2 }] },
        runs: { runs: [r('f1', 'failed'), r('s1', 'succeeded'), r('f2', 'failed')], nextCursor: '' },
      },
    });
    stub({ [BOARD]: { body: twoCronJobs() }, [HEAT]: { body: heat() }, [DETAIL]: detail(), [`${DETAIL}?day=${today}`]: detail() });
    const router = mount(`${PAGE_PATH}?ns=kc-test&state=failed`);
    await screen.findByRole('region', { name: 'Heatmap' });
    fireEvent.click(await screen.findByRole('link', { name: 'always-fails' }));
    expect(await screen.findByRole('heading', { name: 'always-fails' })).toBeTruthy();
    await screen.findByRole('region', { name: 'Runs' });
    // The page's toolbar, less what picks a namespace or a CronJob.
    expect(screen.queryByRole('combobox', { name: 'Namespace' })).toBeNull();
    expect(screen.queryByRole('combobox', { name: 'CronJob' })).toBeNull();
    expect(screen.getByRole('group', { name: 'Outcome' })).toBeTruthy();
    expect(total()).toBe('3');
    expect(document.querySelectorAll('[data-run]')).toHaveLength(3);
    // Failed: the heatmap counts and the runs listed follow.
    fireEvent.click(screen.getByRole('button', { name: 'failed' }));
    await waitFor(() => expect(total()).toBe('2'));
    expect([...document.querySelectorAll('[data-run]')].map((e) => e.getAttribute('data-run'))).toEqual(['f1', 'f2']);
    expect(document.querySelector(`[data-day="${today}"]`)?.getAttribute('data-square')).toBe('failed');
    fireEvent.click(screen.getByRole('button', { name: 'running' }));
    expect(await screen.findByText('No running run in this page of runs.')).toBeTruthy();
    // A day picked keeps the way back; the arrow returns to the page as it was left.
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }));
    await screen.findByText(`Runs of ${today} (UTC)`);
    const back = screen.getByRole('link', { name: 'Back' });
    expect(back.getAttribute('href')).toBe(`/c/docker-desktop/${PAGE_PATH}?ns=kc-test&state=failed`);
    fireEvent.click(back);
    await screen.findByRole('region', { name: 'Heatmap' });
    expect(router.state.location.search).toBe('?ns=kc-test&state=failed');
  });

  it('returns only to an address inside the console', () => {
    expect(backOf(backHere({ pathname: '/c/a/x/kubecron', search: '?ns=b' }))).toBe('/c/a/x/kubecron?ns=b');
    for (const bad of ['//evil.example/x', '/\\evil.example/x', 'https://evil.example/', 'c/a', 42]) {
      expect(backOf({ 'kubecron.back': bad })).toBeUndefined();
    }
    expect(backOf(undefined)).toBeUndefined();
  });

  it('closes a run in focus that the outcome filter hides', async () => {
    const DETAIL = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';
    const r = (id: string, status: string) => ({ id, pod: `p-${id}`, trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status, exitCode: 0, retryCount: 0, logSizeBytes: 4, durationMs: 1000 });
    stub({
      [DETAIL]: {
        body: {
          cronJob: cronJob(),
          collector: { state: 'ok', where: 'kubecron/kubecron' },
          daily: { windowDays: 90, observedSince: '2020-01-01T00:00:00Z', days: [{ day: today, total: 2, succeeded: 1, running: 0, failed: 1 }] },
          runs: { runs: [r('s1', 'succeeded'), r('f1', 'failed')], nextCursor: '' },
        },
      },
      '/x/docker-desktop/kubecron/runs/s1': {
        body: { run: r('s1', 'succeeded'), namespace: 'kc-test', cronJob: 'always-fails', collector: { state: 'ok' }, samples: { samples: [], intervalSeconds: 15, metricsEnabled: true } },
      },
    });
    const router = mount(`${PAGE_PATH}/kc-test/always-fails?run=s1`);
    expect(await screen.findByText('Exit code')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'succeeded' }));
    await waitFor(() => expect(new URLSearchParams(router.state.location.search).get('state')).toBe('succeeded'));
    expect(document.querySelector('[data-run-body]')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'failed' }));
    expect(await screen.findByRole('region', { name: 'Runs' })).toBeTruthy();
    expect(new URLSearchParams(router.state.location.search).get('run')).toBeNull();
  });

  it('sends a CronJob opened from nowhere in the console back to KubeCron', async () => {
    const DETAIL = '/x/docker-desktop/kubecron/cronjobs/kc-test/always-fails';
    stub({
      [DETAIL]: {
        body: { cronJob: cronJob(), collector: { state: 'ok', where: 'kubecron/kubecron' }, daily: { windowDays: 90, days: [] }, runs: { runs: [], nextCursor: '' } },
      },
    });
    mount(`${PAGE_PATH}/kc-test/always-fails`);
    expect((await screen.findByRole('link', { name: 'Back to KubeCron' })).getAttribute('href')).toBe(`/c/docker-desktop/${PAGE_PATH}`);
    expect(screen.queryByRole('link', { name: 'KubeCron' })).toBeNull();
  });

  it('draws a year of weeks summed over every CronJob, and filters by namespace and outcome', async () => {
    stub({ [BOARD]: { body: twoCronJobs() }, [HEAT]: { body: heat() } });
    mount(PAGE_PATH);
    await screen.findByRole('region', { name: 'Heatmap' });
    expect(total()).toBe('6');
    expect(drawnSizes()).toEqual(new Set([CELL]));
    expect(document.querySelector('[data-legend-scale]')?.getAttribute('data-legend-scale')).toBe('ok');
    expect(document.querySelector(`[data-day="${today}"]`)?.getAttribute('data-square')).toBe('failed');
    expect(document.querySelector(`[data-day="${yesterday}"]`)?.getAttribute('data-square')).toBe('ok');
    // Fifty-two whole weeks and this one; older than the collector keeps is not recorded, not empty.
    const squares = [...document.querySelectorAll('[data-square]')].map((s) => s.getAttribute('data-square'));
    expect(squares.length).toBeGreaterThanOrEqual(365);
    expect(squares.length).toBeLessThanOrEqual(371);
    expect(squares.filter((s) => s === 'unrecorded')).toHaveLength(squares.length - 90);

    fireEvent.change(screen.getByRole('combobox', { name: 'Namespace' }), { target: { value: 'ops' } });
    expect(total()).toBe('3');
    expect(document.querySelector(`[data-day="${today}"]`)?.getAttribute('data-square')).toBe('ok');
    // The list below follows the same filter.
    expect(screen.queryByRole('link', { name: 'always-fails' })).toBeNull();
    expect(screen.getByRole('link', { name: 'nightly' })).toBeTruthy();

    fireEvent.change(screen.getByRole('combobox', { name: 'Namespace' }), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'failed' }));
    expect(total()).toBe('3');
    // The legend follows the filter's scale.
    expect(document.querySelector('[data-legend-scale]')?.getAttribute('data-legend-scale')).toBe('failed');
    expect(document.querySelector(`[data-day="${yesterday}"]`)?.getAttribute('data-square')).toBe('none');
  });

  it('opens a day as its runs across the CronJobs that ran, a run as its log beside them, and full-page only when asked', async () => {
    const r1 = { id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: `${today}T09:00:00Z`, status: 'failed', exitCode: 3, retryCount: 0, logSizeBytes: 40, durationMs: 5000 };
    const DAY = `/x/docker-desktop/kubecron/days/${today}?cronjob=kc-test%2Falways-fails&cronjob=ops%2Fnightly`;
    const fetch = stub({
      [BOARD]: { body: twoCronJobs() },
      [HEAT]: { body: heat() },
      [DAY]: {
        body: { day: today, collector: { state: 'ok' }, runs: [{ ...r1, namespace: 'kc-test', cronJob: 'always-fails' }], partial: [], unread: [] },
      },
      '/x/docker-desktop/kubecron/runs/r1': {
        body: {
          run: r1,
          namespace: 'kc-test',
          cronJob: 'always-fails',
          collector: { state: 'ok' },
          log: { lines: [{ at: `${today}T09:00:01Z`, text: 'ERROR: connection refused' }], sizeBytes: 40, expired: false, truncated: false, running: false },
          samples: { samples: [], intervalSeconds: 15, metricsEnabled: true },
        },
      },
    });
    mount(PAGE_PATH);
    await screen.findByRole('region', { name: 'Heatmap' });
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }));
    await screen.findByRole('region', { name: `Runs of ${today}` });
    const row = await waitFor(() => {
      const r = document.querySelector('[data-day-runs] tr[data-run="r1"]');
      if (r === null) throw new Error('no row yet');
      return r as HTMLElement;
    });
    expect(fetch.mock.calls.map(([input]) => String(input))).toContain(DAY);
    // A run unfolds its log in the page, beside the day's runs; the page is not left.
    fireEvent.click(row);
    expect(await screen.findByText('connection refused', { exact: false })).toBeTruthy();
    expect(document.querySelector('[data-kubecron-log]')).toBeNull();
    expect(document.querySelector('[data-log-layout="open"] [data-inline-log="r1"]')).toBeTruthy();
    expect(screen.getByRole('region', { name: `Runs of ${today}` })).toBeTruthy();
    // Full page only from the log's own button; its × lands on the view it
    // was opened from, as it was: the same day picked, the same log open.
    fireEvent.click(screen.getByRole('link', { name: /Full page/ }));
    expect(await screen.findByRole('button', { name: 'Close the full page' })).toBeTruthy();
    expect(document.querySelector('[data-kubecron-log="r1"]')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Close the full page' }));
    expect(await screen.findByRole('region', { name: `Runs of ${today}` })).toBeTruthy();
    expect(document.querySelector(`[data-day="${today}"] button`)?.getAttribute('aria-pressed')).toBe('true');
    expect(await waitFor(() => document.querySelector('[data-inline-log="r1"]'))).toBeTruthy();
    // And the row folds it again.
    fireEvent.click(document.querySelector('[data-day-runs] [data-log-toggle="r1"]') as HTMLElement);
    await waitFor(() => expect(document.querySelector('[data-inline-log]')).toBeNull());
    // An outcome filter closes an open log: its row may be filtered out.
    fireEvent.click(document.querySelector('[data-day-runs] [data-log-toggle="r1"]') as HTMLElement);
    expect(await waitFor(() => document.querySelector('[data-inline-log="r1"]'))).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'succeeded' }));
    await waitFor(() => expect(document.querySelector('[data-inline-log]')).toBeNull());
  });

  it('a day no CronJob in the filter ran is said without asking', async () => {
    const fetch = stub({ [BOARD]: { body: twoCronJobs() }, [HEAT]: { body: heat() } });
    mount(`${PAGE_PATH}?ns=kc-test&day=${yesterday}`);
    expect(await screen.findByText(`No run on ${yesterday}.`)).toBeTruthy();
    expect(fetch.mock.calls.map(([input]) => String(input)).some((u) => u.includes('/days/'))).toBe(false);
  });

  it('tells a heatmap that failed from one with nothing to draw', async () => {
    stub({
      [BOARD]: { body: twoCronJobs() },
      [HEAT]: { status: 502, body: { code: 'list_failed', message: 'The CronJobs could not be read, so whether anything is late on this cluster is unknown.' } },
    });
    mount(PAGE_PATH);
    expect(await screen.findByText(/could not be read, so whether anything is late/)).toBeTruthy();
    expect(screen.queryByRole('region', { name: 'Heatmap' })).toBeNull();
  });

  it('names every calendar that could not be read, and says a picked one is unread, not a non-match', async () => {
    stub({
      [BOARD]: { body: twoCronJobs() },
      [HEAT]: {
        body: heat({
          unread: [
            { namespace: 'ops', name: 'backup', detail: 'the collector answered 500' },
            { namespace: 'web', name: 'sitemap', detail: 'not asked: one page load reads the first 500 CronJobs, by namespace and name' },
          ],
        }),
      },
    });
    mount(PAGE_PATH);
    await screen.findByRole('region', { name: 'Heatmap' });
    const unread = document.querySelector('[data-heatmap-unread]');
    expect(unread?.getAttribute('data-heatmap-unread')).toBe('2');
    expect(unread?.textContent).toContain('2 calendars could not be read and are not counted');
    expect(unread?.textContent).toContain('ops/backup: the collector answered 500');
    expect(unread?.textContent).toContain('web/sitemap: not asked');

    fireEvent.change(screen.getByRole('combobox', { name: 'CronJob' }), { target: { value: 'ops/backup' } });
    expect(document.querySelector('[data-heatmap="unread"]')?.textContent).toBe('ops/backup: its calendar could not be read.');
    expect(document.querySelector('[data-heatmap="no-match"]')).toBeNull();
  });

  it('says a filter nothing matches as no match', async () => {
    stub({ [BOARD]: { body: twoCronJobs() }, [HEAT]: { body: heat() } });
    mount(`${PAGE_PATH}?cronjob=ops%2Fgone`);
    await screen.findByRole('region', { name: 'Heatmap' });
    expect(document.querySelector('[data-heatmap="no-match"]')).toBeTruthy();
  });

  it("draws a day outside the server's window as not recorded, never as no run", async () => {
    stub({ [BOARD]: { body: twoCronJobs() }, [HEAT]: { body: heat({ windowDays: 30, runDays: undefined }) } });
    mount(PAGE_PATH);
    await screen.findByRole('region', { name: 'Heatmap' });
    const squares = [...document.querySelectorAll('[data-square]')].map((s) => s.getAttribute('data-square'));
    expect(squares.filter((s) => s === 'unrecorded')).toHaveLength(squares.length - 30);
  });

  it('asks a busy day without naming every CronJob, which an ingress would refuse', async () => {
    const many = Array.from({ length: 60 }, (_, i) => ({
      namespace: i % 2 === 0 ? 'a' : 'b',
      name: `job-${i}`,
      days: [{ day: today, total: 1, succeeded: 1, running: 0, failed: 0 }],
    }));
    const fetch = stub({
      [BOARD]: { body: twoCronJobs() },
      [HEAT]: { body: heat({ cronJobs: many }) },
      [`/x/docker-desktop/kubecron/days/${today}`]: { body: { day: today, collector: { state: 'ok' }, runs: [], partial: [], unread: [] } },
    });
    mount(`${PAGE_PATH}?day=${today}`);
    await screen.findByText(`No run on ${today}.`);
    const asked = fetch.mock.calls.map(([input]) => String(input)).filter((u) => u.includes('/days/'));
    expect(asked).toEqual([`/x/docker-desktop/kubecron/days/${today}`]);
  });

  it('draws no heatmap without a collector: the install block says why', async () => {
    stub({
      [BOARD]: { body: board({ collector: { state: 'absent', detail: 'no KubeCron collector runs in this cluster.' }, history: { ok: false } }) },
      [HEAT]: { body: heat({ collector: { state: 'absent' }, cronJobs: [] }) },
    });
    mount(PAGE_PATH);
    expect(await screen.findByRole('heading', { name: 'Run history needs the KubeCron collector' })).toBeTruthy();
    expect(screen.queryByRole('region', { name: 'Heatmap' })).toBeNull();
  });
});

describe("a run's log, full-page", () => {
  const RUN = '/x/docker-desktop/kubecron/runs/r1';
  const answer = (log: unknown, over: Record<string, unknown> = {}) => ({
    run: { id: 'r1', pod: 'always-fails-1', trigger: 'scheduled', startedAt: '2026-10-20T09:00:00Z', status: 'failed', retryCount: 0, logSizeBytes: 2048, durationMs: 5000, exitCode: 3 },
    namespace: 'kc-test',
    cronJob: 'always-fails',
    collector: { state: 'ok' },
    ...(log === undefined ? {} : { log }),
    samples: { samples: [], intervalSeconds: 15, metricsEnabled: true },
    ...over,
  });

  it('numbers and colors each line, with one header line and nothing else', async () => {
    stub({
      [RUN]: {
        body: answer({
          lines: [
            { at: '2026-10-20T09:00:01Z', text: '2026-10-20T09:00:01Z ERROR connection refused' },
            { at: '2026-10-20T09:00:02Z', text: '{"level":"warn","msg":"slow","ms":1200,"ok":false}' },
            { at: '2026-10-20T09:00:03Z', text: 'done' },
          ],
          sizeBytes: 120,
          expired: false,
          truncated: true,
          running: false,
        }),
      },
    });
    mount(`${PAGE_PATH}/runs/r1/log`);
    await screen.findByText('connection refused', { exact: false });
    expect(document.querySelector('[data-line="1"]')?.getAttribute('data-level')).toBe('error');
    expect(document.querySelector('[data-line="2"]')?.getAttribute('data-level')).toBe('warn');
    expect(document.querySelector('[data-line="3"]')?.getAttribute('data-level')).toBe('');
    // The text is the log's, cut and never changed.
    expect(document.querySelector('[data-line="2"] > span:last-child')?.textContent).toBe('{"level":"warn","msg":"slow","ms":1200,"ok":false}');
    expect([...document.querySelectorAll('[data-line="2"] .text-y-key')].map((e) => e.textContent)).toEqual(['"level"', '"msg"', '"ms"', '"ok"']);
    expect(document.querySelector('[data-line="1"] .text-bad')?.textContent).toBe('ERROR');
    expect(document.querySelector('[data-log-truncated]')?.textContent).toBe('Last 3 lines.');
    // The CronJob's runs of the run's own UTC day; opened from nowhere in the
    // console, the × closes onto them rather than stepping out of it.
    expect(screen.getByRole('link', { name: 'kc-test/always-fails' }).getAttribute('href')).toBe(
      '/c/docker-desktop/x/kubecron/kc-test/always-fails?day=2026-10-20',
    );
    expect(screen.queryByRole('button', { name: 'Close the full page' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Close the full page' }).getAttribute('href')).toBe(
      '/c/docker-desktop/x/kubecron/kc-test/always-fails?day=2026-10-20',
    );
  });

  it('never steps Back out of the console from an entry it did not open, such as the one signing in replaces', async () => {
    stub({ [RUN]: { body: answer({ lines: [{ at: '2026-10-20T09:00:01Z', text: 'done' }], sizeBytes: 4, expired: false, truncated: false, running: false }) } });
    // An entry behind, with a key of its own, but not opened by the console.
    mount(`${PAGE_PATH}/runs/r1/log`, 'signin');
    await screen.findByText('done');
    expect(screen.queryByRole('button', { name: 'Close the full page' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Close the full page' }).getAttribute('href')).toBe(
      '/c/docker-desktop/x/kubecron/kc-test/always-fails?day=2026-10-20',
    );
  });

  it('says an aged-out log as such, never as a silent run', async () => {
    stub({ [RUN]: { body: answer({ lines: [], sizeBytes: 2048, expired: true, truncated: false, running: false }) } });
    mount(`${PAGE_PATH}/runs/r1/log`);
    expect((await screen.findByText(/Aged out of the collector's log retention \(2\.0 KiB printed\)/)).getAttribute('data-log-state')).toBe('expired');
  });

  it('says a silent run printed nothing', async () => {
    stub({ [RUN]: { body: answer({ lines: [], sizeBytes: 0, expired: false, truncated: false, running: false }) } });
    mount(`${PAGE_PATH}/runs/r1/log`);
    expect((await screen.findByText('No output.')).getAttribute('data-log-state')).toBe('empty');
  });

  it('says a log the collector could not give, in its words', async () => {
    stub({ [RUN]: { body: answer(undefined, { logNote: 'the collector at kubecron/kubecron answered 500' }) } });
    mount(`${PAGE_PATH}/runs/r1/log`);
    expect((await screen.findByText('the collector at kubecron/kubecron answered 500')).getAttribute('data-log-state')).toBe('failed');
  });

  it('says a log the person may not read as a refusal, apart from a failure', async () => {
    const note = "You may not read this run's log: the cluster says you may not get pods/log in namespace ops.";
    stub({ [RUN]: { body: answer(undefined, { logNote: note, logState: 'refused' }) } });
    mount(`${PAGE_PATH}/runs/r1/log`);
    expect((await screen.findByText(note)).getAttribute('data-log-state')).toBe('refused');
  });

  it('titles a run the collector no longer holds Not found', async () => {
    stub({ [RUN]: { status: 404, body: { code: 'run_not_found', message: 'This run is not one this console may show.' } } });
    mount(`${PAGE_PATH}/runs/r1/log`);
    expect(await screen.findByText('Not found')).toBeTruthy();
  });
});
