import { waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

afterEach(() => {
  vi.unstubAllGlobals();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };

const OBJECT = '/api/c/prod/r/pods/argo/api-0';

/** What the server answers for a pod, trimmed. The `tabs` array is the part
 *  under test. */
const detail = {
  body: {
    state: 'ok',
    data: {
      kind: 'pods',
      namespace: 'argo',
      name: 'api-0',
      title: 'Pod',
      tabs: [
        { key: 'manifest', label: 'Manifest', endpoint: '/api/c/prod/manifest?kind=pods', lazy: true },
        { key: 'changes', label: 'Changes', endpoint: '/api/c/prod/changes?kind=pods', lazy: true },
      ],
      // Not a tab. The charts are a member carrying their own address, and the
      // details panel draws them above the facts.
      metrics: '/api/c/prod/metrics?kind=pods&namespace=argo&name=api-0',
      sections: [{ fields: [{ label: 'Phase', value: 'Running' }] }],
    },
  },
};

function shell(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop' } },
    [OBJECT]: detail,
    ...extra,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

/**
 * The defect these are written against: the metrics charts missing when a
 * resource is clicked.
 *
 * The object endpoint declares each tab with its address, so the client's tab
 * list must follow it rather than being hard-coded.
 */
describe('the strip is what the server declared, not what the client decided', () => {
  it('draws a tab for every declared panel it can render', async () => {
    stubFetch(shell());
    mountApp(`/c/prod/pods/argo/api-0`);

    await waitFor(() => {
      expect(page().querySelector('#object-tab-activity')).not.toBeNull();
    });
    expect(page().querySelector('#object-tab-manifest')).not.toBeNull();
    // Details is not declared by the server - it is the object itself - and is
    // always first. Metrics is not here at all: it is a member, drawn inside
    // Details.
    const labels = [...page().querySelectorAll('[role="tab"]')].map((one) => one.textContent);
    expect(labels).toEqual(['Details', 'Activity', 'Manifest']);
  });

  it('drops a declared key it has no panel for, rather than drawing an empty tab', async () => {
    stubFetch(
      shell({
        [OBJECT]: {
          body: {
            state: 'ok',
            data: {
              ...detail.body.data,
              tabs: [{ key: 'kubecron', label: 'KubeCron' }, { key: 'changes', label: 'Changes' }],
            },
          },
        },
      }),
    );
    mountApp(`/c/prod/pods/argo/api-0`);

    await waitFor(() => {
      expect(page().querySelector('#object-tab-activity')).not.toBeNull();
    });
    // The closed half of the vocabulary: an extra ships its own panel, and until
    // it does, a tab over an empty box is worse than no tab.
    expect(page().textContent).not.toContain('KubeCron');
  });

  it('keeps the server label where it differs from ours', async () => {
    stubFetch(
      shell({
        [OBJECT]: {
          body: {
            state: 'ok',
            data: {
              ...detail.body.data,
              tabs: [{ key: 'manifest', label: 'Source', endpoint: '/api/c/prod/manifest?kind=pods', lazy: true }],
            },
          },
        },
      }),
    );
    mountApp(`/c/prod/pods/argo/api-0`);

    await waitFor(() => {
      expect(page().querySelector('#object-tab-manifest')?.textContent).toBe('Source');
    });
  });
});

/**
 * Metrics are visible directly, not on a separate tab: the charts are drawn on
 * the panel that opens, above the facts, and there is no Metrics tab to click.
 */
describe('the metrics panel, on the panel that opens', () => {
  const metrics = {
    body: {
      state: 'ok',
      data: {
        series: [
          {
            key: 'cpu',
            label: 'CPU',
            unit: 'mcores',
            data: {
              startedAt: '2026-09-12T10:51:08Z',
              stepSeconds: 30,
              values: [264.2, 324.8, 372.7, 324.6],
            },
          },
        ],
        metrics: [
          { key: 'cpu', label: 'CPU', available: true, selected: true },
          {
            key: 'network',
            label: 'Network',
            available: false,
            reason: { code: 'absent', message: 'no network samples in this source' },
          },
        ],
        windows: [
          { key: '1h', label: '1h', selected: true },
          { key: '24h', label: '24h' },
        ],
        caption: 'One reading every 30s.',
      },
    },
  };

  it('draws the series, the peak and where it was read from', async () => {
    stubFetch(shell({ '/api/c/prod/metrics': metrics }));
    mountApp(`/c/prod/pods/argo/api-0`);

    await waitFor(() => {
      expect(page().querySelector('svg[role="img"]')).not.toBeNull();
    });
    const text = page().textContent ?? '';
    expect(text).toContain('peak 373');
    // No sentence says where it was read from.
    expect(text).not.toContain('prometheus in monitoring');
    expect(text).toContain('One reading every 30s.');
    // The line itself, from the samples, drawn on a 0-to-peak scale.
    const points = page().querySelector('polyline')?.getAttribute('points') ?? '';
    expect(points.split(' ')).toHaveLength(4);
    expect(points.startsWith('0,')).toBe(true);
  });

  it('draws an unreadable measurement as a disabled control carrying why', async () => {
    stubFetch(shell({ '/api/c/prod/metrics': metrics }));
    mountApp(`/c/prod/pods/argo/api-0`);

    await waitFor(() => {
      expect(page().querySelector('svg[role="img"]')).not.toBeNull();
    });
    const network = [...page().querySelectorAll('button')].find((one) =>
      one.textContent?.includes('Network'),
    );
    // Never a hidden control, and never a chart drawing a flat line at zero: the
    // contract calls that "a confident wrong answer".
    expect(network?.hasAttribute('disabled')).toBe(true);
    expect(network?.getAttribute('title')).toContain('no network samples in this source');
  });

  it('asks for the span the operator picked, and not before', async () => {
    const asked: string[] = [];
    stubFetch(shell({ '/api/c/prod/metrics': metrics }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });

    mountApp(`/c/prod/pods/argo/api-0`);
    await waitFor(() => {
      expect(asked.some((one) => one.includes('/metrics'))).toBe(true);
    });
    // The first read carries no range: which span is the default is the server's
    // to say, and a client that sent one would be overriding an answer it asked
    // for.
    expect(asked.filter((one) => one.includes('/metrics') && one.includes('range='))).toEqual([]);
  });

  it('draws the facts even when the charts cannot be read', async () => {
    // The other half of "its own address": the object's payload and the charts
    // are two reads, and the second one failing is a notice inside the block
    // rather than a panel that does not open. Folded into the object, a
    // Prometheus that is down would take the fact table with it.
    stubFetch(
      shell({
        '/api/c/prod/metrics': {
          body: {
            state: 'unreachable',
            reason: { code: 'source_unreachable', message: 'prometheus did not answer' },
          },
        },
      }),
    );
    mountApp(`/c/prod/pods/argo/api-0`);

    await waitFor(() => {
      expect(page().textContent).toContain('prometheus did not answer');
    });
    expect(page().textContent).toContain('Running');
  });

  it('is not read at all while another tab is open', async () => {
    const asked: string[] = [];
    stubFetch(shell({ '/api/c/prod/metrics': metrics }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });

    mountApp(`/c/prod/pods/argo/api-0?tab=manifest`);
    await waitFor(() => {
      expect(page().querySelector('#object-tab-manifest')?.getAttribute('aria-selected')).toBe('true');
    });
    // The charts live on Details. A user reading a manifest is not asking
    // Prometheus anything, and the panel that is not on screen costs nothing.
    expect(asked.filter((one) => one.includes('/metrics'))).toEqual([]);
  });
});

describe('the changes panel', () => {
  it('draws what the operation returns, newest first', async () => {
    stubFetch(
      shell({
        '/api/c/prod/changes': {
          body: {
            state: 'ok',
            data: {
              entries: [
                { at: '2026-09-07T23:08:55+02:00', summary: 'kubelet Update (status)' },
                { at: '2026-09-07T23:08:41+02:00', summary: 'kube-controller-manager Update' },
              ],
            },
          },
        },
      }),
    );
    mountApp(`/c/prod/pods/argo/api-0?tab=changes`);

    await waitFor(() => {
      expect(page().textContent).toContain('kubelet Update (status)');
    });
    expect(page().querySelectorAll('li')).toHaveLength(2);
  });

  /**
   * The fixture must be one a server can actually produce: an
   * `entriesState: 'unreadable'` with `code: 'archive_unreadable'` is not, since
   * that code is not in the contract's `ReasonCode` enum - a green test
   * asserting its own invention, over a branch nothing could reach.
   *
   * What is sent below is what `internal/web`'s `entriesAnswer` actually
   * answers, arm for arm, and `internal/api/changesmembers_test.go` is what keeps
   * the two from drifting apart.
   */
  it('tells an object that records no history from one that has not changed', async () => {
    stubFetch(
      shell({
        '/api/c/prod/changes': {
          body: {
            state: 'ok',
            data: {
              entries: [],
              entriesState: 'absent',
              entriesReason: {
                code: 'not_supported_here',
                message:
                  'This object carries no managedFields. Objects created before server-side apply, or by a controller that does not set them, record no history.',
              },
            },
          },
        },
      }),
    );
    mountApp(`/c/prod/pods/argo/api-0?tab=changes`);

    // The founding defect at panel scale. "Nothing here" about an object that
    // has never been able to say whether anything changed is the console
    // confidently reporting the absence of its own subject.
    await waitFor(() => {
      expect(page().textContent).toContain('Not installed');
    });
    expect(page().textContent).toContain('no managedFields');
    expect(page().textContent).not.toContain('Nothing here');
  });

  it('draws a read that failed as a failure and not as an empty list', async () => {
    stubFetch(
      shell({
        '/api/c/prod/changes': {
          body: {
            state: 'ok',
            data: {
              entries: [],
              entriesState: 'unreadable',
              entriesReason: { code: 'undecodable', message: 'the managedFields could not be decoded' },
            },
          },
        },
      }),
    );
    mountApp(`/c/prod/pods/argo/api-0?tab=changes`);

    await waitFor(() => {
      expect(page().textContent).toContain('Could not be read');
    });
    expect(page().textContent).not.toContain('Nothing here');
  });

  /**
   * The regression the member test cannot see. `entriesState` reaches the client
   * as an **absent** JSON member on any panel the server answers `ok` with
   * entries, and an absent member is indistinguishable from the state this whole
   * finding is about - so the fold to `empty` is only correct while a server that
   * has something to say says it. This pins the fold itself.
   */
  it('says nothing happened only when that is what the server said', async () => {
    stubFetch(
      shell({
        '/api/c/prod/changes': {
          body: { state: 'ok', data: { entries: [], entriesState: 'empty' } },
        },
      }),
    );
    mountApp(`/c/prod/pods/argo/api-0?tab=changes`);

    await waitFor(() => {
      expect(page().textContent).toContain('Nothing here');
    });
  });
});

/**
 * *What shipped, and when* — a different question from *who wrote which field*,
 * sharing a panel with it and nothing else. `changes.Report.Rollouts` is
 * filled by the producer and the mapper must walk it as well as `Changes`.
 */
describe('the changes panel draws the rollouts', () => {
  const rollouts = {
    body: {
      state: 'ok',
      data: {
        entries: [{ at: '2026-09-07T23:08:55+02:00', summary: 'kubelet Update (status)' }],
        rollouts: [
          {
            revision: '41',
            at: '2026-09-07T22:00:00+02:00',
            name: 'api-7f9c4b8d6',
            replicas: 3,
            image: 'registry.internal/api:v2.4.1',
            current: true,
            cause: 'kubectl set image deployment/api api=registry.internal/api:v2.4.1',
          },
          {
            revision: '40',
            at: '2026-09-06T10:00:00+02:00',
            name: 'api-6c8b3a7f5',
            replicas: 3,
            image: 'registry.internal/api:v2.4.0',
          },
        ],
      },
    },
  };

  it('draws which image was running, and marks the one that is', async () => {
    stubFetch(shell({ '/api/c/prod/changes': rollouts }));
    mountApp('/c/prod/pods/argo/api-0?tab=changes');

    await waitFor(() => {
      expect(page().textContent).toContain('registry.internal/api:v2.4.1');
    });
    const text = page().textContent ?? '';
    expect(text).toContain('registry.internal/api:v2.4.0');
    // Marked, never inferred from the order: the highest revision is not always
    // the deployed one, which is the state a failed rollout leaves behind.
    expect(text).toContain('current');
    // And the change cause, where anybody set it.
    expect(text).toContain('kubectl set image');
    // Both lists are on the panel, under their own headings.
    expect(text).toContain('kubelet Update (status)');
  });

  /**
   * `rolloutsState` reaching the screen. A refusal on `replicasets` - ordinary
   * under tight RBAC while the Deployment itself reads fine - must not render as
   * a workload that had never shipped.
   */
  it('says a rollout history was refused rather than drawing nothing', async () => {
    stubFetch(
      shell({
        '/api/c/prod/changes': {
          body: {
            state: 'ok',
            data: {
              entries: [{ at: '2026-09-07T23:08:55+02:00', summary: 'kubelet Update' }],
              rolloutsState: 'forbidden',
              rolloutsReason: {
                code: 'verb_denied',
                message: 'cannot list resource "replicasets" in API group "apps"',
              },
            },
          },
        },
      }),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=changes');

    await waitFor(() => {
      expect(page().textContent).toContain('Refused by the cluster');
    });
    const text = page().textContent ?? '';
    expect(text).toContain('replicasets');
    // The other half still reads, which is the whole reason the two states are
    // separate: an error page over a working answer is the worse trade.
    expect(text).toContain('kubelet Update');
  });

  it('says a workload has never shipped rather than drawing nothing', async () => {
    stubFetch(
      shell({
        '/api/c/prod/changes': {
          body: {
            state: 'ok',
            data: {
              entries: [{ at: '2026-09-07T23:08:55+02:00', summary: 'kubelet Update' }],
              rolloutsState: 'empty',
            },
          },
        },
      }),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=changes');

    await waitFor(() => {
      expect(page().textContent).toContain('Rollouts');
    });
    expect(page().textContent).toContain('Nothing here');
  });

  it('draws no rollouts heading on a kind that has none', async () => {
    stubFetch({
      ...shell(),
      '/api/c/prod/changes': {
        body: {
          state: 'ok',
          data: { entries: [{ at: '2026-09-07T23:08:55+02:00', summary: 'kubelet Update' }] },
        },
      },
    });
    mountApp('/c/prod/pods/argo/api-0?tab=changes');

    await waitFor(() => {
      expect(page().textContent).toContain('kubelet Update');
    });
    // A ConfigMap has no revisions, and an empty list under a heading reads as
    // one that lost them.
    expect(page().textContent).not.toContain('Rollouts');
  });
});

/**
 * A Secret's keys are drawn once. The server's field set for a Secret is what
 * is *about* the object, and the keys are in the data section: the payload here
 * is the shape `detailPageOf` sends, with one summary section and no section
 * named after the data block.
 *
 * Details and data are one page, so duplication would be *within a single
 * view*: the fact table and the key list sit close together, and a Secret
 * restating its keys as held-back facts would visibly say everything twice.
 * What these tests pin is that the keys appear once, in the data section, and
 * the facts around them are about the object.
 */
describe('a Secret draws its keys once, and says something else in its facts', () => {
  const SECRET = '/api/c/prod/r/secrets/argo/creds';
  const secret = {
    body: {
      state: 'ok',
      data: {
        kind: 'secrets',
        namespace: 'argo',
        name: 'creds',
        title: 'Secret',
        // The keys as an address, which is what replaced the Data tab.
        data: '/api/c/prod/data?kind=secrets',
        tabs: [{ key: 'manifest', label: 'Manifest', endpoint: '/api/c/prod/manifest?kind=secrets' }],
        sections: [
          {
            key: 'summary',
            label: 'Summary',
            state: 'ok',
            fields: [
              { label: 'Kind', value: 'Secret' },
              { label: 'Name', value: 'creds' },
              { label: 'Type', value: 'Opaque' },
              { label: 'Keys', value: '2' },
              { label: 'Immutable', value: 'no' },
            ],
          },
        ],
        labels: { 'app.kubernetes.io/managed-by': 'Helm' },
      },
    },
  };
  const data = {
    body: {
      state: 'ok',
      data: {
        kind: 'secrets',
        namespace: 'argo',
        name: 'creds',
        resourceVersion: '4821',
        editable: true,
        entries: [
          { key: 'password', masked: true },
          { key: 'username', masked: true },
        ],
      },
    },
  };

  /** The page with the data section cut out of it, which is how this file asks
   *  *where* something is drawn rather than merely whether it is. Reading the
   *  whole `textContent` cannot tell a fact table restating the keys from the
   *  key list itself, and that is precisely the confusion to rule out. */
  function outsideTheKeys(): string {
    const clone = page().cloneNode(true) as HTMLElement;
    clone.querySelector('[data-section="data"]')?.remove();
    return clone.textContent ?? '';
  }

  it('says what is about the object in its facts, and no key among them', async () => {
    stubFetch(shell({ [SECRET]: secret, '/api/c/prod/data': data }));
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('Opaque');
    });
    // Wait for the keys too: the assertion below is that they are *not* in the
    // rest of the page, which would pass trivially against a page where they
    // have not arrived anywhere yet.
    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });

    const elsewhere = outsideTheKeys();
    expect(elsewhere).toContain('Keys');
    expect(elsewhere).toContain('Immutable');
    expect(elsewhere).toContain('Helm');
    // The keys are the data section's and only its. A fact table restating them
    // is the duplication to rule out, and on one page it would sit right above
    // the list.
    expect(elsewhere).not.toContain('password');
    expect(elsewhere).not.toContain('username');
    expect(elsewhere).not.toContain('held back');
  });

  it('lists every key, on the page it opens on and with no tab named', async () => {
    stubFetch(shell({ [SECRET]: secret, '/api/c/prod/data': data }));
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    expect(page().textContent).toContain('username');
    // Inside the section, not merely somewhere on the page.
    const block = page().querySelector('[data-section="data"]');
    expect(block?.textContent).toContain('password');
    expect(block?.textContent).toContain('username');
  });
});
