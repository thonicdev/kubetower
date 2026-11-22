import { act, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp, stubFetch, type StubReply, type StubRoutes } from '../../testing/harness';
import { formatInstant } from '../../components/instant';

/**
 * The six panels the dashboard declared and drew none of, and the two things a
 * browser measures that a compiler cannot.
 *
 * ## What it pins
 *
 * Two behaviors, both found in a headless-browser sweep against a six-node
 * cluster running 195 pods:
 *
 *   - the capacity badge is placed by its column and not by its reading: a
 *     gauge and its reading are one flex row, badge first, so a column of badges
 *     is not ragged; and an unmeasured cell draws the em dash and no bar at all.
 *   - no table on the page is clipped away by its panel: every table is inside
 *     a `.table-scroll`, counted rather than named, so a table added later is
 *     covered too.
 *
 * ## What it cannot measure
 *
 * jsdom computes no layout: `getBoundingClientRect` is all zeroes, so a pixel
 * spread cannot be re-measured here by anything. What is asserted is the
 * **mechanism** that removes it - the flex row, the order, and the reading being
 * an element of its own rather than an anonymous text node no rule can align.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

function withOverview(reply: StubReply): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/overview': reply,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

/**
 * A cluster with something wrong with it.
 *
 * Deliberately not a healthy one: four of the six panels below draw nothing at
 * all on a quiet cluster, so a healthy fixture would exercise none of them and
 * pass against a page that had not been written.
 */
const brokenCluster = {
  state: 'ok',
  data: {
    cluster: 'prod',
    version: 'v1.29.4',
    collectedAt: '2026-09-12T09:00:00Z',
    allWell: false,
    nodesSummary: { text: '2 of 3 nodes healthy', href: '/c/prod/nodes' },
    nodesTone: 'warn',
    workloadsSummary: { text: '429 pods', href: '/c/prod/pods' },
    counts: {
      state: 'ok',
      items: [
        {
          kind: 'deployments',
          label: 'Deployments',
          count: 3,
          healthy: 2,
          note: '1 not ready',
          tone: 'warn',
          ref: { text: 'Deployments', href: '/c/prod/deployments' },
        },
        { kind: 'daemonsets', label: 'DaemonSets', count: 4, healthy: 4, tone: 'ok' },
      ],
    },
    capacity: {
      state: 'ok',
      items: [
        {
          label: 'CPU',
          requested: { value: 2.3, trustworthy: true, text: '2.3 cores' },
          capacity: { value: 8, trustworthy: true, text: '8 cores' },
          used: { value: 2.3, trustworthy: true, text: '2.3 cores' },
          ref: { text: 'CPU requested', href: '/c/prod/nodes' },
          tone: 'ok',
        },
      ],
    },
    top: { state: 'empty', items: [] },
    events: {
      state: 'ok',
      items: [
        {
          text: 'Back-off restarting failed container',
          age: '4m',
          at: '2026-09-12T08:56:00Z',
          scope: { text: 'default', href: '/c/prod/events?namespace=default' },
          object: { text: 'Pod/api-0', href: '/c/prod/pods?search=api-0&open=api-0' },
          reason: 'BackOff',
          message: 'Back-off restarting failed container',
          count: 340,
          tone: 'warn',
        },
      ],
    },
    alerts: {
      state: 'ok',
      items: [
        {
          subject: { text: 'node-2', href: '/c/prod/nodes?search=node-2&open=node-2' },
          summary: 'under memory pressure',
          detail: 'kubelet has observed memory pressure',
          tone: 'bad',
        },
      ],
    },
    trouble: {
      state: 'ok',
      items: [
        {
          kind: { text: 'Deployment', href: '/c/prod/deployments' },
          scope: { text: 'default', href: '/c/prod/deployments?namespace=default' },
          object: { text: 'api', href: '/c/prod/deployments?search=api&open=api' },
          detail: '1 of 3 replicas ready',
          since: '2026-09-09T09:00:00Z',
          age: '3d',
          tone: 'bad',
        },
        // A kind with no record of when it left its state: listed, undated.
        {
          kind: { text: 'Pod', href: '/c/prod/pods' },
          scope: { text: 'default', href: '/c/prod/pods?namespace=default' },
          object: { text: 'worker-7', href: '/c/prod/pods?search=worker-7&open=worker-7' },
          detail: 'CrashLoopBackOff',
          tone: 'bad',
        },
      ],
    },
    restarting: {
      state: 'ok',
      items: [
        {
          scope: { text: 'default', href: '/c/prod/pods?namespace=default' },
          object: { text: 'api-0', href: '/c/prod/pods?search=api-0&open=api-0' },
          container: 'api',
          init: false,
          restarts: 12,
          reason: 'OOMKilled',
          lastExitAt: '2026-09-12T08:56:00Z',
          age: '4m',
          tone: 'bad',
        },
        // A container the kubelet no longer holds an exit for: listed, and
        // undated. The server sends its own em dash in `age` and no
        // `lastExitAt` at all, which is the shape the server is pinned to
        // produce.
        {
          scope: { text: 'default', href: '/c/prod/pods?namespace=default' },
          object: { text: 'worker-7', href: '/c/prod/pods?search=worker-7&open=worker-7' },
          container: 'worker',
          init: false,
          restarts: 900,
          reason: '—',
          age: '—',
          tone: 'bad',
        },
        // An init container, and it is third because the server sorts by count
        // and does not promote it. Rows like this are on the panel at all because
        // the server reads init containers too; the mark says which kind they are.
        {
          scope: { text: 'default', href: '/c/prod/pods?namespace=default' },
          object: { text: 'web-3', href: '/c/prod/pods?search=web-3&open=web-3' },
          container: 'wait-for-db',
          init: true,
          restarts: 6,
          reason: 'Error',
          lastExitAt: '2026-09-12T08:30:00Z',
          age: '30m',
          tone: 'warn',
        },
      ],
    },
    phases: {
      state: 'ok',
      items: [
        { phase: 'Running', count: 420, tone: 'ok' },
        { phase: 'Failed', count: 9, tone: 'bad' },
      ],
    },
    history: {
      state: 'ok',
      data: {
        caption: 'prometheus, last hour',
        zoomable: false,
        series: [
          {
            key: 'cpu-used',
            label: 'CPU used',
            unit: 'cores',
            data: {
              startedAt: '2026-09-12T08:00:00Z',
              stepSeconds: 60,
              // A gap in the middle: it must break the line, never be
              // interpolated across.
              values: [1, null, 3, 2],
            },
          },
        ],
      },
    },
    // `panel` is the server's prose and `affects` is what a client keys on:
    // this page titles the same panel "Recent warnings", and matching those two
    // strings would be re-deriving an identity from printed text.
    notShown: [{ panel: 'Warning events', reason: 'events is forbidden', affects: ['events'] }],
  },
};

describe('the half an operator opens the dashboard for', () => {
  it('draws the panels that were declared and never filled', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('under memory pressure');
    });
    const text = page().textContent ?? '';

    // Alerts, lifted out of the table so a red pill in the twenty-third row is
    // not the only sign.
    expect(text).toContain('under memory pressure');
    expect(text).toContain('kubelet has observed memory pressure');

    // The panel the dashboard exists for.
    expect(text).toContain('Not in the state it should be in');
    expect(text).toContain('1 of 3 replicas ready');

    // Restarting, and the pod opens.
    expect(text).toContain('Restarting');
    expect(text).toContain('OOMKilled');
    expect(page().querySelector('a[href="/c/prod/pods?search=api-0&open=api-0"]')).not.toBeNull();

    // The phase breakdown, with the figures beside the shape.
    expect(text).toContain('Running');
    expect(text).toContain('420');

    // The cluster's own series.
    expect(page().querySelector('svg[aria-label^="CPU used"]')).not.toBeNull();
  });

  it('draws the head an operator reads before deciding not to read the rest', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('2 of 3 nodes healthy');
    });
    const text = page().textContent ?? '';

    expect(text).toContain('Kubernetes v1.29.4');
    // The version line carries no read time - no stamp, no tooltip.
    const version = [...page().querySelectorAll('span')].find(
      (el) => el.childElementCount === 0 && el.textContent?.startsWith('Kubernetes '),
    );
    expect(version?.textContent).toBe('Kubernetes v1.29.4');
    expect(version?.closest('[title]')).toBeNull();
    expect(text).not.toContain('read at');
    expect(text).toContain('429 pods');
    // The badge is the server's and this cluster is not well.
    expect(text).not.toContain('nothing needs acting on');
  });

  it('never raises the badge on its own judgment', async () => {
    // `allWell` absent. A client computing it from whichever panels it happened
    // to receive would raise it on a page with two panels missing, which is the
    // one sentence that stops an operator reading the rest.
    const silent = structuredClone(brokenCluster) as { data: Record<string, unknown> };
    delete silent.data.allWell;
    delete silent.data.alerts;
    delete silent.data.trouble;
    delete silent.data.restarting;

    stubFetch(withOverview({ body: silent }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('2 of 3 nodes healthy');
    });
    expect(page().textContent).not.toContain('nothing needs acting on');
  });

  // The "nothing needs acting on" pill is not drawn even where the server raised
  // it. A quiet cluster says so by drawing nothing that needs acting on; the
  // nodes pill and the alerts stay.
  it('draws no all-well pill even when the server raised it', async () => {
    const well = structuredClone(brokenCluster) as { data: Record<string, unknown> };
    well.data.allWell = true;

    stubFetch(withOverview({ body: well }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('2 of 3 nodes healthy');
    });
    expect(page().textContent).not.toContain('nothing needs acting on');
    expect(page().textContent).toContain('under memory pressure');
  });
});

/**
 * A metrics source with nothing to say draws nothing - no heading, no "Nothing to
 * chart.", no caption, no notice - on a cluster whose Prometheus holds no
 * container or pod cAdvisor series and which has no metrics-server, and the same
 * of *Most used*'s "Nothing here". What stays drawn, and short, is what is not
 * that: a read that failed, and a window that is empty over series that exist.
 */
describe('a metrics source with nothing to say is not drawn', () => {
  const NO_SHAPE =
    "this cluster's Prometheus has no per-container container_cpu_usage_seconds_total series and no pod cgroup's either";

  function unchartable(key: string, label: string) {
    return {
      key,
      label,
      data: { startedAt: '0001-01-01T00:00:00Z', stepSeconds: 60, values: [], trustworthy: false },
      reason: { code: 'not_installed', message: NO_SHAPE },
    };
  }

  function withMetrics(history: unknown, top?: unknown) {
    const body = structuredClone(brokenCluster) as { data: Record<string, unknown> };
    body.data.history = history;
    if (top !== undefined) body.data.top = top;
    return body;
  }

  function headings(): string[] {
    return [...page().querySelectorAll('h2')].map((heading) => heading.textContent?.trim() ?? '');
  }

  async function drawn(body: unknown): Promise<string> {
    stubFetch(withOverview({ body }));
    mountApp('/c/prod');
    await waitFor(() => {
      expect(page().textContent).toContain('under memory pressure');
    });
    return page().textContent ?? '';
  }

  it('draws no Over time section over a Prometheus with no usable series for either line', async () => {
    const text = await drawn(
      withMetrics({
        state: 'ok',
        data: {
          series: [unchartable('cpu-used', 'CPU used'), unchartable('memory-used', 'Memory used')],
          caption: `CPU used: ${NO_SHAPE}; Memory used: ${NO_SHAPE}`,
          zoomable: false,
        },
      }),
    );

    expect(headings()).not.toContain('Over time');
    expect(text).not.toContain('Over time');
    expect(text).not.toContain('Nothing to chart');
    expect(text).not.toContain('not a claim');
    expect(text).not.toContain(NO_SHAPE);
    // The rest of the head is still there.
    expect(headings()).toContain('Capacity');
  });

  it('draws no Over time section where there is no Prometheus at all', async () => {
    const text = await drawn(
      withMetrics({
        state: 'absent',
        reason: { code: 'not_installed', message: 'this cluster keeps no series', detail: 'no Prometheus was found' },
      }),
    );

    expect(headings()).not.toContain('Over time');
    expect(text).not.toContain('this cluster keeps no series');
    expect(text).not.toContain('Not installed');
  });

  it('keeps a failed history read visible: failed is not absent', async () => {
    await drawn(
      withMetrics({
        state: 'unreachable',
        reason: { code: 'cluster_unreachable', message: 'Post "http://prometheus:9090": i/o timeout', retryable: true },
      }),
    );

    expect(headings()).toContain('Over time');
    expect(page().querySelector('[role="status"][data-state="unreachable"]')?.textContent).toContain('i/o timeout');
  });

  // A read that panicked reaches the page as `unreadable`/`unclassified`, and it
  // is drawn - never hidden as if there were no metrics source.
  it('keeps a history read that panicked visible', async () => {
    await drawn(
      withMetrics({
        state: 'unreadable',
        reason: { code: 'unclassified', message: "reading this cluster's history panicked: a nil map", retryable: true },
      }),
    );

    expect(headings()).toContain('Over time');
    expect(page().querySelector('[role="status"][data-state="unreadable"]')?.textContent).toContain('panicked');
  });

  it('keeps a usage read that panicked visible', async () => {
    await drawn(
      withMetrics(brokenCluster.data.history, {
        state: 'unreadable',
        reason: { code: 'unclassified', message: 'the pod usage read panicked: a nil map', retryable: true },
      }),
    );

    expect(headings()).toContain('Most used');
    expect(page().textContent).toContain('the pod usage read panicked: a nil map');
  });

  it('keeps an empty window over series that exist, short, with the caption on hover', async () => {
    const WHY = 'no samples in this window - the cluster may be younger than the window';
    const empty = (key: string, label: string) => ({
      key,
      label,
      data: { startedAt: '2026-10-20T09:00:00Z', stepSeconds: 60, values: [], trustworthy: false },
      reason: { code: 'empty_scope', message: WHY },
    });
    const text = await drawn(
      withMetrics({
        state: 'ok',
        data: { series: [empty('cpu-used', 'CPU used'), empty('memory-used', 'Memory used')], caption: WHY, zoomable: false },
      }),
    );

    expect(headings()).toContain('Over time');
    expect(text).toContain('No metrics');
    // The caption is a sentence about lines that drew nothing, and the lines
    // already say so: it is on hover, not a paragraph under them.
    expect(text).not.toContain(WHY);
    const heading = [...page().querySelectorAll('h2')].find((h) => h.textContent === 'Over time');
    expect(heading?.getAttribute('title')).toBe(WHY);
  });

  it('draws a line that reads beside one this Prometheus cannot chart, and not the other', async () => {
    const text = await drawn(
      withMetrics({
        state: 'ok',
        data: {
          series: [
            {
              key: 'cpu-used',
              label: 'CPU used',
              unit: 'cores',
              data: { startedAt: '2026-10-20T09:00:00Z', stepSeconds: 60, values: [1, 2, 3] },
            },
            unchartable('memory-used', 'Memory used'),
          ],
          caption: 'from pod cgroups: this Prometheus has no per-container series',
          zoomable: false,
        },
      }),
    );

    expect(headings()).toContain('Over time');
    expect(page().querySelector('svg[aria-label^="CPU used"]')).not.toBeNull();
    expect(text).not.toContain('Nothing to chart');
    // The source of a drawn line stays in view: it is what stops a
    // pod-cgroup sum being read as the container sum.
    expect(text).toContain('from pod cgroups: this Prometheus has no per-container series');
  });

  it('draws no Most used panel where the leaderboard is empty', async () => {
    const text = await drawn(
      withMetrics(brokenCluster.data.history, {
        state: 'empty',
        reason: {
          code: 'empty_scope',
          message: 'the metrics backend answered and no pod on this cluster is using anything measurable',
        },
        items: [],
      }),
    );

    expect(headings()).not.toContain('Most used');
    expect(text).not.toContain('produced no leaderboard');
    expect(text).not.toContain('not a claim');
    expect(text).not.toContain('Nothing here');
  });

  it('draws no Most used panel where there is no metrics source', async () => {
    await drawn(
      withMetrics(brokenCluster.data.history, {
        state: 'absent',
        reason: { code: 'not_installed', message: 'no metrics backend was found on this cluster' },
      }),
    );

    expect(headings()).not.toContain('Most used');
    expect(page().textContent).not.toContain('no metrics backend was found');
  });

  it('draws no Most used panel where the metrics source did not answer', async () => {
    await drawn(
      withMetrics(brokenCluster.data.history, {
        state: 'unreachable',
        reason: { code: 'cluster_unreachable', message: "this cluster's metrics backend did not answer", retryable: true },
      }),
    );

    expect(headings()).not.toContain('Most used');
    expect(page().textContent).not.toContain("this cluster's metrics backend did not answer");
  });

  it('keeps Most used visible where the metrics source refused the read', async () => {
    await drawn(
      withMetrics(brokenCluster.data.history, {
        state: 'forbidden',
        reason: { code: 'read_denied', message: 'you may not read pods.metrics.k8s.io', retryable: false },
      }),
    );

    expect(headings()).toContain('Most used');
    expect(page().textContent).toContain('you may not read pods.metrics.k8s.io');
  });
});

// The machines one by one are the Nodes page's, and no table here repeats it.
// What makes a sick machine impossible to miss from this page stays: the pill
// that leads to the Nodes page, and the node alerts.
describe('the machines are the Nodes page, not a panel here', () => {
  it('draws no Nodes panel, and keeps the pill and the node alert', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('under memory pressure');
    });

    const headings = [...page().querySelectorAll('h2')].map((heading) => heading.textContent?.trim());
    expect(headings).not.toContain('Nodes');
    expect(page().querySelector('table[aria-label="The machines in this cluster"]')).toBeNull();

    // The pill, leading to the Nodes page.
    const pill = [...page().querySelectorAll<HTMLAnchorElement>('a[href="/c/prod/nodes"]')].find(
      (link) => link.textContent === '2 of 3 nodes healthy',
    );
    expect(pill).toBeDefined();

    // The node alert, opening the node it is about.
    const alert = page().querySelector<HTMLAnchorElement>('a[href="/c/prod/nodes?search=node-2&open=node-2"]');
    expect(alert?.textContent).toBe('node-2');
    expect(page().textContent).toContain('kubelet has observed memory pressure');
  });
});

describe('a trouble row says since when', () => {
  /**
   * *What* is a sentence about the present and nothing else on the row says how
   * long it has been so. The fifth column is the server's own age with the
   * instant on hover, and a row whose kind keeps no record of the transition
   * draws the em dash - never "0s", which would read a Deployment broken since
   * Tuesday as one mid-rollout.
   */
  function troubleRows(): HTMLTableRowElement[] {
    const table = page().querySelector('table[aria-label="Workloads not in the state they declare"]');
    if (table === null) throw new Error('the trouble table is missing');
    return Array.from(table.querySelectorAll('tbody tr'));
  }

  it('draws the column, the age, and the instant on hover', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('1 of 3 replicas ready');
    });

    const table = page().querySelector('table[aria-label="Workloads not in the state they declare"]');
    const heads = Array.from(table?.querySelectorAll('thead th') ?? []).map((th) => th.textContent);
    expect(heads).toEqual(['Kind', 'Namespace', 'Name', 'What', 'Since']);

    const [dated] = troubleRows();
    const cell = dated?.querySelectorAll('td')[4];
    expect(cell?.textContent).toBe('3d');
    expect(cell?.getAttribute('title')).toBe(formatInstant('2026-09-09T09:00:00Z', { year: true, seconds: true }));
    // Muted, the same as every other age on the page: the name and the fault
    // are what the eye lands on, and the age is what it reads next.
    expect(cell?.className).toContain('text-muted');
  });

  it('draws the em dash, and says why, on a row whose kind has no record', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('CrashLoopBackOff');
    });

    const [, undated] = troubleRows();
    const cell = undated?.querySelectorAll('td')[4];
    expect(cell?.textContent).toBe('—');
    expect(cell?.textContent).not.toBe('0s');
    // An em dash with no explanation is indistinguishable from a broken cell.
    expect(cell?.getAttribute('title')).toMatch(/no record/);
  });
});

describe('a restarting row says when it last exited', () => {
  /**
   * `lastExitAt` is declared in the contract and drawn by this cell. A row with
   * no recorded termination hovers as a termination the kubelet no longer holds.
   * That is a real state, and it is the one a reader uses to decide there is no
   * record to go and look for.
   *
   * The two arms are asserted against each other rather than one at a time:
   * if the two rendered identically, a test that only looked at
   * the dated row would have passed on a page where every row was undated.
   */
  function restartRows(): HTMLTableRowElement[] {
    const table = page().querySelector('table[aria-label="Containers that keep exiting"]');
    if (table === null) throw new Error('the restarting table is missing');
    return Array.from(table.querySelectorAll('tbody tr'));
  }

  it('draws the age with the exact instant on hover', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('OOMKilled');
    });

    const [dated] = restartRows();
    const cell = dated?.querySelectorAll('td')[5];
    expect(cell?.textContent).toBe('4m');
    // The follow-up question is "was that during the 03:00 incident", and the
    // cell answers "recently or not".
    expect(cell?.getAttribute('title')).toBe(formatInstant('2026-09-12T08:56:00Z', { year: true, seconds: true }));
  });

  it('says there is no record rather than drawing one, when the exit is not held', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('OOMKilled');
    });

    const [dated, undated] = restartRows();
    const cell = undated?.querySelectorAll('td')[5];
    expect(cell?.textContent).toBe('—');
    expect(cell?.textContent).not.toBe('0s');
    expect(cell?.getAttribute('title')).toMatch(/no longer holds/);

    // And the two rows are distinguishable, which is the whole defect: they were
    // not, because the server sent no instant on either.
    const first = dated?.querySelectorAll('td')[5];
    expect(first?.getAttribute('title')).not.toBe(cell?.getAttribute('title'));
  });
});

describe('a restarting row says which kind of container it is', () => {
  /**
   * The server reads `initContainerStatuses` as well, so init containers appear
   * on this panel and must not be drawn exactly like the app containers beside
   * them. `kubectl logs -c` goes somewhere else for an init container, and a
   * plain one that will not start is why a pod never reaches Running at all.
   *
   * Asserted as a pair, for the reason the `lastExitAt` block above gives: a
   * test that only looked at the marked row would pass on a page that marked
   * every row.
   */
  function restartRows(): HTMLTableRowElement[] {
    const table = page().querySelector('table[aria-label="Containers that keep exiting"]');
    if (table === null) throw new Error('the restarting table is missing');
    return Array.from(table.querySelectorAll('tbody tr'));
  }

  it('marks the init container and leaves the app containers unmarked', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('OOMKilled');
    });

    const rows = restartRows();
    expect(rows).toHaveLength(3);

    const marked = rows[2];
    expect(marked?.querySelectorAll('td')[2]?.textContent).toContain('wait-for-db');
    const mark = marked?.querySelector('[data-init-mark]');
    expect(mark).not.toBeNull();
    expect(mark?.textContent).toBe('init');
    // The mark is on the Container cell and not in a column of its own: the
    // Restarting table is one of the tables that lose width at 1024px, and a
    // column carrying one boolean is the first thing that reflow should not have
    // to spend.
    expect(marked?.querySelectorAll('td')[2]?.querySelector('[data-init-mark]')).not.toBeNull();
    expect(marked?.querySelectorAll('td')).toHaveLength(6);

    // Nothing is drawn for an app container. The absence is the unmarked case,
    // which holds because the contract makes `init` required - a blank cell
    // cannot mean "the server did not say".
    expect(rows[0]?.querySelector('[data-init-mark]')).toBeNull();
    expect(rows[1]?.querySelector('[data-init-mark]')).toBeNull();
    expect(rows[0]?.getAttribute('data-init')).toBe('false');
    expect(marked?.getAttribute('data-init')).toBe('true');
  });

  it('marks it without a tone, because being an init container is not a fault', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('OOMKilled');
    });

    // A cell's tone says whether this is a fault, and conflating that with
    // anything else is a defect (see `components/Reading.tsx`). Which of the
    // kubelet's two lists a row came from is a taxonomy, and the row's one graded
    // thing is the Restarts pill - which still carries the server's tone, one
    // cell away.
    const marked = restartRows()[2];
    const mark = marked?.querySelector('[data-init-mark]');
    expect(mark?.className).toContain('text-muted');
    expect(mark?.className).not.toMatch(/text-(warn|bad|ok)/);
    expect(marked?.querySelector('[data-tone]')?.getAttribute('data-tone')).toBe('warn');
  });

  it('does not promote the init row, whatever it may be worth', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('OOMKilled');
    });

    // The server sorts by restart count and refuses to promote an init row -
    // "arguably more urgent" is not a rule a reader can predict from a screen.
    // The client draws the order it is given, and a sort added here would be a
    // second opinion about ranking that nothing on the server would notice.
    const containers = restartRows().map((row) => row.querySelectorAll('td')[2]?.textContent?.trim());
    expect(containers).toEqual(['api', 'worker', 'initwait-for-db']);
  });
});

describe('a warning event is six facts and not a sentence', () => {
  it('carries its age, its count and both links', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Recent warnings');
    });
    const text = page().textContent ?? '';

    expect(text).toContain('4m');
    // 340 against 1. Without it a BackOff that has fired all morning and one
    // that fired once are the same row, and the first is an incident.
    expect(text).toContain('340');
    expect(text).toContain('BackOff');
    expect(page().querySelector('a[href="/c/prod/events?namespace=default"]')).not.toBeNull();
  });
});

describe('what the page did not look at', () => {
  it('names every panel that was never asked, and links none of them', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Not shown');
    });
    const text = page().textContent ?? '';
    expect(text).toContain('events is forbidden');

    // Nothing here leads anywhere, on purpose: the subject is a panel that did
    // not load, and the only useful destination would be a kubeconfig or an IAM
    // role rather than a page.
    const notShown = [...page().querySelectorAll('table')].find((table) =>
      (table.getAttribute('aria-label') ?? '').includes('could not read'),
    );
    expect(notShown).toBeDefined();
    expect(notShown?.querySelectorAll('a')).toHaveLength(0);
  });

  it('draws nothing at all where there is nothing to say', async () => {
    const clean = structuredClone(brokenCluster) as { data: Record<string, unknown> };
    clean.data.notShown = [];

    stubFetch(withOverview({ body: clean }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Recent warnings');
    });
    expect(page().textContent).not.toContain('Not shown');
  });
});

/**
 * Without a scrolling wrapper, a table wider than its panel (at a 1280px window
 * the hottest-pods memory table, at 1024px Recent warnings and Restarting) has
 * columns outside it with no scrollbar anywhere - not merely off to the side, but
 * unreachable by scrolling, selecting or tabbing.
 *
 * Counted rather than named, so a table added later is covered too.
 */
describe('no table on the dashboard is clipped away by its panel', () => {
  it('has every table inside a scrolling wrapper', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('node-2');
    });

    const tables = [...page().querySelectorAll('table')];
    expect(tables.length).toBeGreaterThan(0);

    for (const table of tables) {
      // `kt-grid` and not `grid`: Tailwind emits `.grid { display: grid }`,
      // which would take the table out of table layout entirely and leave
      // theme.css's table rules with nothing to act on.
      expect(table.classList.contains('kt-grid')).toBe(true);
      expect(table.classList.contains('grid')).toBe(false);

      const wrapper = table.parentElement;
      expect(wrapper?.classList.contains('table-scroll')).toBe(true);
      expect(wrapper?.className).toContain('overflow-x-auto');
    }
  });

  it('marks the cell that holds a sentence, on the cell and not on a child', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Back-off restarting failed container');
    });

    // `white-space` is inherited into every element inside the cell, so an
    // `overflow-wrap` on a span in there is inert and defeats the obvious fix.
    // The class goes on the cell.
    const message = [...page().querySelectorAll('td')].find(
      (cell) => cell.textContent === 'Back-off restarting failed container',
    );
    expect(message?.classList.contains('message')).toBe(true);
  });
});

describe("the cluster's own series", () => {
  it('breaks the line on a gap rather than drawing through it', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('svg[aria-label^="CPU used"]')).not.toBeNull();
    });

    // Four values with a null second. A null must break the run: omitting a
    // sample shifts every later one onto the wrong moment and the chart still
    // draws, and interpolating across a five-minute hole in the scrape draws it
    // as a steeper line rather than as a hole.
    const svg = page().querySelector('svg[aria-label^="CPU used"]');
    const lines = svg?.querySelectorAll('polyline') ?? [];
    expect(lines).toHaveLength(1);
    // The run after the gap: indices 2 and 3 only.
    expect(lines[0]?.getAttribute('points')?.split(' ')).toHaveLength(2);
  });

  it('offers no overlay while the server says the series cannot be zoomed', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('svg[aria-label^="CPU used"]')).not.toBeNull();
    });

    // There is no way to re-ask for a cluster's own series at a wider window
    // today (`getMetricsPanel` requires `kind` and `name`), so the server sends
    // `zoomable: false` and the page honors it rather than offering an overlay
    // that would open onto nothing.
    const svg = page().querySelector('svg[aria-label^="CPU used"]');
    expect(svg?.closest('a')).toBeNull();
    expect(svg?.closest('button')).toBeNull();
  });
});

/**
 * The capacity ring.
 *
 * It is the dashboard's capacity donut and nothing else. A ring around a count
 * in a table is never drawn; the list's equivalent is a bar, and that belongs
 * to another surface.
 */
describe('the capacity ring', () => {
  it('draws requested against allocatable, graded by the server', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-donut="CPU"]')).not.toBeNull();
    });

    // 2.3 of 8 cores. The ring is **requested**, which is what decides whether
    // the next pod schedules and is the only figure available without a metrics
    // backend at all.
    const ring = page().querySelector('[data-donut="CPU"] [data-percent]');
    expect(ring?.getAttribute('data-percent')).toBe('29');
    // The grading is the server's `tone`, never re-derived: the thresholds are a
    // scheduling fact and they live where the figures are computed.
    expect(ring?.getAttribute('class')).toContain('text-ok');

    // The reading beside the shape, because nothing on this page may depend on
    // the reader's ability to judge an angle.
    expect(page().querySelector('[data-donut="CPU"]')?.textContent).toContain('2.3 cores');
  });

  it('says which arc is which, and draws used as a second arc inside it', async () => {
    /*
      Nothing on the tile may leave the reader guessing which of `requested` and
      `used` the ring is.

      `requested` and `used` are different quantities from different sources: the
      first is what decides whether the next pod schedules and is available with
      no metrics backend at all, the second comes from the metrics backend's own
      series. Drawing one shape for both would read as one figure under two
      labels.

      So: the outer ring is `requested`, a thin inner arc is `used`, and both are
      named underneath with the denominator they share.
    */
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-donut="CPU"]')).not.toBeNull();
    });

    const tile = page().querySelector('[data-donut="CPU"]');
    // Both quantities named in words. A swatch matches each to its arc and is
    // never the only distinction - color alone is one some readers
    // cannot see.
    expect(tile?.textContent).toContain('requested');
    expect(tile?.textContent).toContain('used');
    // Each against the same whole, because a pair with two denominators is not
    // a pair - and each line is its percentage, with the quantity on hover as
    // `used / total`, both halves the server's texts.
    const lines = [...(tile?.querySelectorAll('figcaption [data-measure]') ?? [])];
    expect(lines).toHaveLength(2);
    for (const line of lines) {
      expect(line.querySelector('[data-share]')?.textContent).toBe('29%');
      // This fixture sends no `requestedOf` / `usedOf`, so the line falls back
      // to joining the two readings' own server texts.
      expect(line.getAttribute('title')).toBe('2.3 cores / 8 cores');
      expect(line.querySelector('.sr-only')?.textContent).toBe('2.3 cores / 8 cores');
      // No bar under a line: the ring above is its shape.
      expect(line.querySelector('[data-gauged]')).toBeNull();
      // No quantity drawn on the line itself.
      const drawn = line.cloneNode(true) as HTMLElement;
      drawn.querySelectorAll('.sr-only').forEach((node) => {
        node.remove();
      });
      expect(drawn.textContent).not.toContain('cores');
    }

    // The graded arc is `requested` and it is the **last** circle in the group -
    // the over-commit test below reads `circles.at(-1)`, so drawing `used`
    // after it would silently move that assertion onto a different arc.
    const circles = [...(tile?.querySelectorAll('circle') ?? [])];
    expect(circles.at(-1)?.getAttribute('data-arc')).toBe('requested');
    expect(circles.some((circle) => circle.getAttribute('data-arc') === 'used')).toBe(true);
    // Inside the ring and thinner: a second series on one tile, not a second
    // ring.
    const used = circles.find((circle) => circle.getAttribute('data-arc') === 'used');
    const requested = circles.at(-1);
    expect(Number(used?.getAttribute('r'))).toBeLessThan(Number(requested?.getAttribute('r')));
    expect(Number(used?.getAttribute('stroke-width'))).toBeLessThan(
      Number(requested?.getAttribute('stroke-width')),
    );
  });

  it("puts the server's used / total on each line's hover, both halves in the capacity's unit", async () => {
    // One unit per hover, the whole's. The server writes it (`resources.Fraction`), so a used
    // figure under one core reads `0.37 / 8 cores`, never `368m / 8.0 cores`,
    // and the line takes the string as sent.
    const sent = structuredClone(brokenCluster) as { data: { capacity: { items: unknown[] } } };
    sent.data.capacity.items = [
      {
        label: 'CPU',
        requested: { value: 2300, trustworthy: true, text: '2.3 cores' },
        capacity: { value: 8000, trustworthy: true, text: '8.0 cores' },
        used: { value: 368, trustworthy: true, text: '368m' },
        requestedOf: '2.3 / 8 cores',
        usedOf: '0.37 / 8 cores',
        tone: 'ok',
      },
    ];

    stubFetch(withOverview({ body: sent }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-donut="CPU"]')).not.toBeNull();
    });

    const lines = [...(page().querySelectorAll('[data-donut="CPU"] figcaption [data-measure]') ?? [])];
    expect(lines.map((line) => line.getAttribute('title'))).toEqual(['2.3 / 8 cores', '0.37 / 8 cores']);
    expect(lines.map((line) => line.querySelector('[data-share]')?.textContent)).toEqual(['29%', '5%']);
  });

  it('draws no inner arc where the metrics backend said nothing', async () => {
    // An empty answer is not a zero, on the second arc too. `used` absent is not `used` at zero, and
    // an arc at 0% inside a ring reads as a cluster requesting a lot and using
    // none of it - which is a finding, made up.
    const noMetrics = structuredClone(brokenCluster) as { data: { capacity: { items: unknown[] } } };
    noMetrics.data.capacity.items = [
      {
        label: 'CPU',
        requested: { value: 2.3, trustworthy: true, text: '2.3 cores' },
        capacity: { value: 8, trustworthy: true, text: '8 cores' },
        used: { value: null, trustworthy: false, text: 'no metrics source on this cluster' },
        tone: 'ok',
      },
    ];

    stubFetch(withOverview({ body: noMetrics }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-donut="CPU"]')).not.toBeNull();
    });

    const tile = page().querySelector('[data-donut="CPU"]');
    expect(tile?.querySelector('[data-arc="used"]')).toBeNull();
    // The ring itself is still drawn - `requested` needs no metrics backend -
    // and the used line says the dash and a short word, with the server's
    // sentence on hover rather than beside it: a hundred characters under a ring
    // would run past the tile.
    expect(tile?.querySelector('[data-arc="requested"]')).not.toBeNull();
    const used = tile?.querySelector('[data-unmeasured]');
    expect(used?.textContent).toContain('—');
    expect(used?.textContent).toContain('not measured');
    expect(used?.getAttribute('title')).toBe('no metrics source on this cluster');
    // And no percentage on that line: `0%` would be the same made-up finding
    // as an arc at 0%. The requested line,
    // measured, keeps its own.
    const shares = [...(tile?.querySelectorAll('[data-share]') ?? [])].map((one) => one.textContent);
    expect(shares).toEqual(['29%']);
  });

  it('draws used from the reading of now where the history says it has none', async () => {
    // A cluster with metrics-server and no Prometheus has a `used` figure and no
    // series: the tile must not draw "used - not measured: ... so it holds no
    // history" while "Most used" lists every pod's live CPU. Usage is a question
    // about now; only the series panel says there is no history.
    const NO_HISTORY = 'docker-desktop has metrics-server and no Prometheus, so it holds no history to query';
    const instant = structuredClone(brokenCluster) as {
      data: { capacity: { items: unknown[] }; history: unknown };
    };
    instant.data.capacity.items = [
      {
        label: 'CPU',
        requested: { value: 2300, trustworthy: true, text: '2.3 cores' },
        capacity: { value: 8000, trustworthy: true, text: '8 cores' },
        used: { value: 230, trustworthy: true, text: '230m' },
        tone: 'ok',
      },
    ];
    instant.data.history = {
      state: 'absent',
      reason: { code: 'not_installed', message: 'this cluster keeps no series', detail: NO_HISTORY, retryable: true },
    };

    stubFetch(withOverview({ body: instant }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-donut="CPU"]')).not.toBeNull();
    });

    const tile = page().querySelector('[data-donut="CPU"]');
    expect(tile?.querySelector('[data-arc="used"]')).not.toBeNull();
    expect(tile?.textContent).toContain('230m');
    expect(tile?.textContent).not.toContain('not measured');
    expect(tile?.textContent).not.toContain('no history');
    // And nowhere else: a cluster with no Prometheus draws no history section
    // at all, so the sentence is not on the page.
    expect(page().textContent.split(NO_HISTORY).length - 1).toBe(0);
  });

  it('sends the caption to the machines, and not the ring', async () => {
    stubFetch(withOverview({ body: brokenCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-capacity="CPU"]')).not.toBeNull();
    });

    const cell = page().querySelector('[data-capacity="CPU"]');
    // An arc 13 units thick is not a pointer target.
    expect(cell?.querySelector('svg a')).toBeNull();
    // And the destination is the node list, for all three: they are sums over
    // the machines, and the Nodes page is where the machines are, one by one.
    expect(cell?.querySelector('a')?.getAttribute('href')).toBe('/c/prod/nodes');
  });

  it('fills the ring at a fraction over one and still prints the real percentage', async () => {
    const over = structuredClone(brokenCluster) as { data: { capacity: { items: unknown[] } } };
    over.data.capacity.items = [
      {
        label: 'CPU',
        requested: { value: 9.4, trustworthy: true, text: '9.4 cores' },
        capacity: { value: 8, trustworthy: true, text: '8 cores' },
        used: { value: 2.3, trustworthy: true, text: '2.3 cores' },
        ref: { text: 'CPU requested', href: '/c/prod/nodes' },
        tone: 'bad',
      },
    ];

    stubFetch(withOverview({ body: over }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-donut="CPU"]')).not.toBeNull();
    });

    // An over-committed cluster is a normal state, and capping the *number* as
    // well as the shape would hide the only reading that matters.
    const ring = page().querySelector('[data-donut="CPU"] [data-percent]');
    expect(ring?.getAttribute('data-percent')).toBe('118');
    expect(page().querySelector('[data-donut="CPU"]')?.textContent).toContain('118%');
    // The requested line under it says the same real figure, never a clamped
    // 100%, with the quantities on hover.
    const requestedLine = page().querySelector('[data-donut="CPU"] figcaption [data-measure]');
    expect(requestedLine?.querySelector('[data-share]')?.textContent).toBe('118%');
    expect(requestedLine?.getAttribute('title')).toBe('9.4 cores / 8 cores');

    // The arc itself is clamped: it cannot be drawn outside the track.
    const arc = [...(ring?.querySelectorAll('circle') ?? [])].at(-1);
    const dash = (arc?.getAttribute('stroke-dasharray') ?? '').split(' ');
    expect(Number(dash[1])).toBeCloseTo(0, 5);
  });

  it('draws no ring at all where nothing was measured, and says why inside the frame', async () => {
    const unmeasured = structuredClone(brokenCluster) as { data: { capacity: { items: unknown[] } } };
    unmeasured.data.capacity.items = [
      {
        label: 'Memory',
        requested: { value: null, trustworthy: false, text: 'the pod list could not be read' },
        capacity: { value: null, trustworthy: false, text: 'the cluster reported no capacity' },
      },
    ];

    stubFetch(withOverview({ body: unmeasured }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-donut-empty="true"]')).not.toBeNull();
    });

    // A full-size ring at 0% reads as an idle cluster, which is the opposite of
    // the truth when nothing could be measured.
    //
    // Scoped to the capacity panel: counting `[data-percent]` across the page
    // would measure other gauges too.
    expect(
      page().querySelectorAll('[data-capacity="Memory"] [data-percent]'),
    ).toHaveLength(0);
    // And the reason is **inside** the frame: a chart-shaped blank with an
    // explanation somewhere below it is how "this cluster has no capacity
    // reading" gets read as "this chart is broken".
    expect(page().querySelector('[data-donut-empty="true"]')?.textContent).toContain(
      'the cluster reported no capacity',
    );
  });
});

/**
 * A panel whose read failed, an empty one, and a whole one are three different
 * things and must be drawn as three.
 *
 * With `listWarningEvents` made to panic, the page answers `allWell: false` and
 * names the feed in `notShown`; the warnings panel must not draw *"Nothing here -
 * No warning events. This cluster answered and had none."* with the correction
 * at the foot of the page in another component. Nor may the heading say
 * *incomplete* while the body still opens with *"Nothing here"* in bold and
 * `data-state="empty"`, the same state and the same first word as a real empty.
 * So the failed case is asserted on the DOM state and on the absence of the empty
 * word, not on a sentence that can be reworded.
 *
 * The three cases are asserted **against each other** rather than one at a time:
 * what matters is that no two of the three say the same thing, and a test that
 * only looks at the failed case passes on a page that marks everything.
 */
describe('a failed panel, an empty one and a whole one read differently', () => {
  /** The panel by its heading, because that is how a reader finds it. */
  function panelNamed(title: string): HTMLElement {
    const found = [...page().querySelectorAll('section')].find(
      (section) => section.querySelector('h2')?.textContent === title,
    );
    if (found === undefined) throw new Error(`no panel titled ${title}`);
    return found;
  }

  function withEvents(events: unknown, notShown: unknown): { data: Record<string, unknown> } {
    const fixture = structuredClone(brokenCluster) as { data: Record<string, unknown> };
    fixture.data.events = events;
    fixture.data.notShown = notShown;
    return fixture;
  }

  async function draw(body: unknown): Promise<HTMLElement> {
    stubFetch(withOverview({ body }));
    mountApp('/c/prod');
    await waitFor(() => {
      expect(page().textContent).toContain('Recent warnings');
    });
    return panelNamed('Recent warnings');
  }

  it('says nothing at all about a panel that is whole', async () => {
    const warnings = await draw(withEvents(brokenCluster.data.events, []));

    expect(warnings.querySelector('[data-panel-mark]')).toBeNull();
    expect(warnings.hasAttribute('data-incomplete')).toBe(false);
    expect(warnings.querySelector('[role="status"]')).toBeNull();
    expect(warnings.textContent).toContain('Back-off restarting failed container');
  });

  it('draws a warning event cleaned of escapes, as every other events surface does', async () => {
    const [first] = brokenCluster.data.events.items;
    const warnings = await draw(
      withEvents(
        {
          state: 'ok',
          items: [
            { ...first, message: '\x1b[31mprobe failed\x1b[0m: [33mrate(x[5m]) over budget' },
          ],
        },
        [],
      ),
    );
    const cell = warnings.querySelector('td.message');
    expect(cell?.textContent).toBe('probe failed: rate(x[5m]) over budget');
  });

  it('lets an empty panel say it is empty, and no more, when that is true', async () => {
    const warnings = await draw(withEvents({ state: 'empty', items: [] }, []));

    expect(warnings.querySelector('[data-panel-mark]')).toBeNull();
    expect(warnings.hasAttribute('data-incomplete')).toBe(false);
    expect(warnings.querySelector('[role="status"]')?.getAttribute('data-state')).toBe('empty');
    expect(warnings.textContent).toContain('Nothing here');
    expect(warnings.textContent).not.toContain('answered');
  });

  it('marks the panel whose read failed and withdraws that claim', async () => {
    const warnings = await draw(
      withEvents({ state: 'empty', items: [] }, [
        {
          panel: 'Warning events',
          reason: 'the warning event feed panicked: boom',
          affects: ['events'],
        },
      ]),
    );

    // Distinguishable from whole: there is a mark, and the section says so.
    const mark = warnings.querySelector('[data-panel-mark]');
    expect(mark).not.toBeNull();
    expect(mark?.textContent).toBe('incomplete');
    expect(warnings.hasAttribute('data-incomplete')).toBe(true);

    // Distinguishable from empty, in the DOM and not only in prose: the body is
    // not the empty state, and it does not open with the empty state's word.
    const body = warnings.querySelector('[role="status"]');
    expect(body?.getAttribute('data-state')).toBe('incomplete');
    expect(warnings.querySelector('[data-state="empty"]')).toBeNull();
    expect(warnings.textContent).not.toContain('Nothing here');
    expect(warnings.textContent).not.toContain('This cluster answered and had none');

    // What is there is a dash and two words - never a count, never a zero, and
    // never a second copy of the reason, which lives once under "Not shown".
    expect(body?.textContent).toBe('— not read');
    expect(warnings.textContent).not.toContain('panicked');

    // And it points at the entry that explains it, rather than repeating a
    // Kubernetes paragraph in a panel heading.
    expect(mark?.getAttribute('href')).toBe('#not-shown');
    expect(mark?.getAttribute('title')).toContain('the warning event feed panicked');
    expect(document.getElementById('not-shown')).not.toBeNull();
  });

  /**
   * The mark is keyed on `affects` and never on `panel`.
   *
   * The server writes *"Warning events"* and this page titles the same panel
   * *"Recent warnings"*. A client matching those two strings would work on the
   * day it was written and stop the day either is improved, saying nothing when
   * it does.
   */
  it('marks the panel the failure names, not the one whose title looks like it', async () => {
    const warnings = await draw(
      withEvents({ state: 'empty', items: [] }, [
        { panel: 'Recent warnings', reason: 'a name that matches nothing', affects: ['trouble'] },
      ]),
    );

    expect(warnings.querySelector('[data-panel-mark]')).toBeNull();
    expect(panelNamed('Not in the state it should be in').querySelector('[data-panel-mark]')).not.toBeNull();
  });
});

// The attention lists show 100 rows a page.
describe('the attention lists page at 100', () => {
  function withRestarting(count: number): { data: Record<string, unknown> } {
    const fixture = structuredClone(brokenCluster) as { data: Record<string, unknown> };
    fixture.data.restarting = {
      state: 'ok',
      items: Array.from({ length: count }, (_, index) => ({
        scope: { text: 'default', href: '/c/prod/pods?namespace=default' },
        object: { text: `pod-${String(index)}`, href: `/c/prod/pods?search=pod-${String(index)}` },
        container: 'app',
        init: false,
        restarts: 100 - index,
        reason: 'Error',
        age: '1m',
        tone: 'bad',
      })),
    };
    return fixture;
  }

  async function drawRestarting(count: number): Promise<void> {
    stubFetch(withOverview({ body: withRestarting(count) }));
    mountApp('/c/prod');
    await waitFor(() => {
      expect(page().querySelector('table[aria-label="Containers that keep exiting"]')).not.toBeNull();
    });
  }

  const rows = (): number =>
    page().querySelectorAll('table[aria-label="Containers that keep exiting"] tbody tr').length;
  const button = (name: string): HTMLButtonElement => {
    const found = Array.from(page().querySelectorAll('nav[aria-label$=", pages"] button')).find(
      (candidate) => candidate.textContent === name,
    );
    if (found === undefined) throw new Error(`no ${name} button`);
    return found as HTMLButtonElement;
  };

  it('shows 100, 100 and 45 of 245 across three pages', async () => {
    await drawRestarting(245);
    expect(rows()).toBe(100);
    expect(page().textContent).toContain('1–100 of 245');
    expect(page().textContent).toContain('pod-0');

    fireEvent.click(button('Next'));
    expect(rows()).toBe(100);
    expect(page().textContent).toContain('101–200 of 245');
    expect(page().textContent).toContain('pod-100');

    fireEvent.click(button('Next'));
    expect(rows()).toBe(45);
    expect(page().textContent).toContain('201–245 of 245');
    expect(button('Next').disabled).toBe(true);

    fireEvent.click(button('Previous'));
    expect(rows()).toBe(100);
  });

  it('draws no pagination controls at 100 rows or fewer', async () => {
    await drawRestarting(100);
    expect(rows()).toBe(100);
    expect(page().querySelector('nav[aria-label$=", pages"]')).toBeNull();
  });
});

describe('every attention list pages, and the page belongs to the cluster', () => {
  type Fixture = { data: Record<string, unknown> };
  const many = (source: { items: unknown[] }, count: number): unknown[] =>
    Array.from({ length: count }, (_, index) => {
      const row = structuredClone(source.items[0]) as Record<string, unknown>;
      const name = `row-${String(index)}`;
      if (typeof row.text === 'string') row.text = `${name} message`;
      if (typeof row.object === 'object' && row.object !== null) (row.object as { text: string }).text = name;
      return row;
    });
  const withMany = (key: 'trouble' | 'events', count: number): Fixture => {
    const fixture = structuredClone(brokenCluster) as Fixture;
    fixture.data[key] = {
      state: 'ok',
      items: many(brokenCluster.data[key] as { items: unknown[] }, count),
    };
    return fixture;
  };
  const rowsOf = (label: string): number =>
    page().querySelectorAll(`table[aria-label="${label}"] tbody tr`).length;
  const next = (): void => {
    const found = Array.from(page().querySelectorAll('nav[aria-label$=", pages"] button')).find(
      (candidate) => candidate.textContent === 'Next',
    );
    if (found === undefined) throw new Error('no Next button');
    fireEvent.click(found);
  };

  it.each([
    ['trouble', 'Workloads not in the state they declare'],
    ['events', 'Recent warning events'],
  ] as const)('pages the %s list at 100: 100, 100 and 45 of 245', async (key, label) => {
    stubFetch(withOverview({ body: withMany(key, 245) }));
    mountApp('/c/prod');
    await waitFor(() => {
      expect(rowsOf(label)).toBe(100);
    });
    next();
    expect(rowsOf(label)).toBe(100);
    next();
    expect(rowsOf(label)).toBe(45);
  });

  it('starts at the first page when the cluster changes', async () => {
    const label = 'Workloads not in the state they declare';
    const body = withMany('trouble', 245);
    const routes = withOverview({ body });
    stubFetch({
      ...routes,
      '/api/clusters': {
        body: {
          state: 'ok',
          items: [
            { name: 'prod', label: 'prod', state: 'ok' },
            { name: 'stage', label: 'stage', state: 'ok' },
          ],
        },
      },
      '/api/c/stage/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/c/stage/extensions': { body: { state: 'empty', items: [] } },
      '/api/c/stage/overview': { body },
    });
    // Both clusters are read once first, so that the second arrival finds its
    // answer cached and the dashboard is never unmounted by a loading state.
    const { router } = mountApp('/c/stage');
    await waitFor(() => {
      expect(rowsOf(label)).toBe(100);
    });
    await act(async () => {
      await router.navigate('/c/prod');
    });
    await waitFor(() => {
      expect(rowsOf(label)).toBe(100);
    });
    next();
    next();
    expect(rowsOf(label)).toBe(45);

    await act(async () => {
      await router.navigate('/c/stage');
    });

    await waitFor(() => {
      expect(rowsOf(label)).toBe(100);
    });
  });
});

describe('Most used keeps its notice where the source answered unreadably', () => {
  it('draws the panel and the reason for an unreadable leaderboard', async () => {
    const fixture = structuredClone(brokenCluster) as { data: Record<string, unknown> };
    fixture.data.top = {
      state: 'unreadable',
      reason: { code: 'unclassified', message: 'the metrics reply could not be read', retryable: true },
    };
    stubFetch(withOverview({ body: fixture }));
    mountApp('/c/prod');
    await waitFor(() => {
      expect(page().textContent).toContain('Recent warnings');
    });

    const titles = [...page().querySelectorAll('h2')].map((heading) => heading.textContent?.trim());
    expect(titles).toContain('Most used');
    expect(page().textContent).toContain('the metrics reply could not be read');
  });
});
