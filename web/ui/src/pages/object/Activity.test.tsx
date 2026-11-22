import { fireEvent, waitFor } from '@testing-library/react';
import { lazy } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Extension } from '../../extension';
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

const EVENTS_TAB = { key: 'events', label: 'Events' };
const CHANGES_TAB = { key: 'changes', label: 'Changes', endpoint: '/api/c/prod/changes?kind=pods', lazy: true };
const MANIFEST_TAB = { key: 'manifest', label: 'Manifest', endpoint: '/api/c/prod/manifest?kind=pods', lazy: true };

const backoff = {
  type: 'Warning',
  reason: 'BackOff',
  message: 'Back-off restarting failed container api',
  last: '2026-09-12T11:48:00Z',
};
const scheduled = {
  type: 'Normal',
  reason: 'Scheduled',
  message: 'Assigned argo/api-0 to node-3',
  last: '2026-09-12T09:00:00Z',
};

const changesAnswer = {
  body: {
    state: 'ok',
    data: {
      entries: [
        { at: '2026-09-12T10:30:00Z', summary: 'kubectl-edit Update' },
        { at: '2026-09-12T08:00:00Z', summary: 'kube-controller-manager Update' },
      ],
    },
  },
};

/** The routes for a pod declaring `tabs`, carrying `extra` on its payload. */
function routes(tabs: readonly object[], extra: Record<string, unknown> = {}, more: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop' } },
    [OBJECT]: {
      body: {
        state: 'ok',
        data: {
          kind: 'pods',
          namespace: 'argo',
          name: 'api-0',
          title: 'Pod',
          tabs,
          sections: [{ fields: [{ label: 'Phase', value: 'Running' }] }],
          ...extra,
        },
      },
    },
    '/api/c/prod/changes': changesAnswer,
    ...more,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

function strip(): (string | null)[] {
  return [...page().querySelectorAll('[role="tab"]')].map((one) => one.textContent);
}

/**
 * Events and Changes answer one question, and are one Activity tab.
 */
describe('the Activity tab', () => {
  it('is the one tab for events and changes: the strip is Details / Activity / Manifest', async () => {
    stubFetch(routes([EVENTS_TAB, CHANGES_TAB, MANIFEST_TAB], { events: [backoff] }));
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().querySelector('#object-tab-activity')).not.toBeNull();
    });
    expect(strip()).toEqual(['Details', 'Activity', 'Manifest']);
  });

  it('interleaves events and changes by time, newest first, each line marked with its source', async () => {
    stubFetch(routes([EVENTS_TAB, CHANGES_TAB, MANIFEST_TAB], { events: [backoff, scheduled] }));
    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().querySelector('#object-tab-activity')).not.toBeNull();
    });
    fireEvent.click(page().querySelector('#object-tab-activity')!);

    await waitFor(() => {
      expect(page().textContent).toContain('kube-controller-manager Update');
    });
    const lines = [...page().querySelectorAll('[data-activity]')];
    expect(lines.map((line) => line.getAttribute('data-activity'))).toEqual(['event', 'change', 'event', 'change']);
    expect(lines[0]?.textContent).toContain('BackOff');
    expect(lines[1]?.textContent).toContain('kubectl-edit Update');
    expect(lines[2]?.textContent).toContain('Scheduled');
    expect(lines[3]?.textContent).toContain('kube-controller-manager Update');
    // Each line says which source it is from, and an event its type, a Warning
    // standing out from a Normal.
    expect(lines.map((line) => line.querySelector('[data-activity-mark]')?.textContent)).toEqual([
      'event',
      'change',
      'event',
      'change',
    ]);
    const warning = lines[0]?.querySelector('[data-event-type]');
    expect(warning?.textContent).toBe('Warning');
    expect(warning?.className).toContain('text-warn');
    expect(lines[2]?.querySelector('[data-event-type]')?.className).not.toContain('text-warn');
  });

  /**
   * A workload's rollouts are lines of the timeline, placed by time with the
   * events and the changes, each marked as a rollout.
   */
  it('places the rollouts in the same timeline, by time, each marked as a rollout', async () => {
    stubFetch(
      routes(
        [EVENTS_TAB, CHANGES_TAB],
        { events: [backoff, scheduled] },
        {
          '/api/c/prod/changes': {
            body: {
              state: 'ok',
              data: {
                ...changesAnswer.body.data,
                rollouts: [
                  {
                    revision: '41',
                    at: '2026-09-12T11:00:00Z',
                    name: 'api-7f9c4b8d6',
                    replicas: 3,
                    image: 'registry.internal/api:v2.4.1',
                    current: true,
                    cause: 'kubectl set image deployment/api api=registry.internal/api:v2.4.1',
                  },
                  {
                    revision: '40',
                    at: '2026-09-12T07:00:00Z',
                    name: 'api-6c8b3a7f5',
                    replicas: 2,
                    image: 'registry.internal/api:v2.4.0',
                  },
                ],
                rolloutsState: 'ok',
              },
            },
          },
        },
      ),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('registry.internal/api:v2.4.0');
    });
    const lines = [...page().querySelectorAll('[data-activity]')];
    expect(lines.map((line) => line.getAttribute('data-activity'))).toEqual([
      'event',
      'rollout',
      'change',
      'event',
      'change',
      'rollout',
    ]);
    expect(lines.map((line) => line.querySelector('[data-activity-mark]')?.textContent)).toEqual([
      'event',
      'rollout',
      'change',
      'event',
      'change',
      'rollout',
    ]);
    // What a rollout line carries: the revision, the image,
    // the replicas, the cause, and `current` marked rather than inferred.
    const shipped = lines[1]?.textContent ?? '';
    expect(shipped).toContain('41');
    expect(shipped).toContain('registry.internal/api:v2.4.1');
    expect(shipped).toContain('current');
    expect(shipped).toContain('kubectl set image');
    expect(lines[1]?.querySelector('time')?.getAttribute('datetime')).toBe('2026-09-12T11:00:00Z');
    const older = lines[5]?.textContent ?? '';
    expect(older).toContain('40');
    expect(older).toContain('registry.internal/api:v2.4.0');
    expect(older).not.toContain('current');
    // No second list under the timeline.
    expect(page().querySelectorAll('ol')).toHaveLength(1);
    expect(page().textContent).not.toContain('Rollouts');
  });

  it('keeps the timeline on screen when the rollouts could not be read, and says so beside it', async () => {
    stubFetch(
      routes(
        [EVENTS_TAB, CHANGES_TAB],
        { events: [backoff] },
        {
          '/api/c/prod/changes': {
            body: {
              state: 'ok',
              data: {
                ...changesAnswer.body.data,
                rolloutsState: 'forbidden',
                rolloutsReason: {
                  code: 'verb_denied',
                  message: 'cannot list resource "replicasets" in API group "apps"',
                },
              },
            },
          },
        },
      ),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('Refused by the cluster');
    });
    const text = page().textContent ?? '';
    expect(text).toContain('replicasets');
    expect(text).toContain('BackOff');
    expect(text).toContain('kubectl-edit Update');
    expect(page().querySelectorAll('[data-activity="rollout"]')).toHaveLength(0);
    expect(text).not.toContain('Nothing here');
  });

  it('says a refused rollouts read even when nothing else has a line', async () => {
    stubFetch(
      routes(
        [EVENTS_TAB, CHANGES_TAB],
        { events: [] },
        {
          '/api/c/prod/changes': {
            body: {
              state: 'ok',
              data: {
                entries: [],
                entriesState: 'empty',
                rolloutsState: 'forbidden',
                rolloutsReason: { code: 'verb_denied', message: 'cannot list resource "replicasets"' },
              },
            },
          },
        },
      ),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('Refused by the cluster');
    });
    // A refusal is not an emptiness: *nothing here* would claim the workload
    // never shipped.
    expect(page().textContent).not.toContain('Nothing here');
  });

  it('says nothing here once when a workload that never shipped has nothing else either', async () => {
    stubFetch(
      routes(
        [EVENTS_TAB, CHANGES_TAB],
        { events: [] },
        {
          '/api/c/prod/changes': {
            body: { state: 'ok', data: { entries: [], entriesState: 'empty', rolloutsState: 'empty' } },
          },
        },
      ),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('Nothing here');
    });
    expect(page().querySelectorAll('[data-state="empty"]')).toHaveLength(1);
  });

  it.each(['events', 'changes'])('opens Activity from an address that still says tab=%s', async (old) => {
    stubFetch(routes([EVENTS_TAB, CHANGES_TAB, MANIFEST_TAB], { events: [backoff] }));
    mountApp(`/c/prod/pods/argo/api-0?tab=${old}`);

    await waitFor(() => {
      expect(page().querySelector('#object-tab-activity')?.getAttribute('aria-selected')).toBe('true');
    });
    expect(page().querySelector('#object-panel-activity')).not.toBeNull();
    await waitFor(() => {
      expect(page().textContent).toContain('kubectl-edit Update');
    });
    expect(page().textContent).toContain('BackOff');
  });

  it('draws the events alone, and asks for no changes, where the server declared only events', async () => {
    const asked: string[] = [];
    stubFetch(routes([EVENTS_TAB, MANIFEST_TAB], { events: [backoff] }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('BackOff');
    });
    expect(strip()).toEqual(['Details', 'Activity', 'Manifest']);
    expect(asked.some((url) => url.includes('/changes'))).toBe(false);
    expect(page().querySelectorAll('[data-activity="change"]')).toHaveLength(0);
  });

  it('draws the changes alone where the server declared only changes', async () => {
    stubFetch(routes([CHANGES_TAB], { events: [backoff] }));
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('kubectl-edit Update');
    });
    expect(strip()).toEqual(['Details', 'Activity']);
    expect(page().querySelectorAll('[data-activity="event"]')).toHaveLength(0);
  });

  it('draws no Activity tab where the server declared neither', async () => {
    stubFetch(routes([MANIFEST_TAB]));
    mountApp('/c/prod/pods/argo/api-0');

    await waitFor(() => {
      expect(page().querySelector('#object-tab-manifest')).not.toBeNull();
    });
    expect(strip()).toEqual(['Details', 'Manifest']);
  });

  it('keeps the events on screen when the changes could not be read, and says so', async () => {
    stubFetch(
      routes(
        [EVENTS_TAB, CHANGES_TAB],
        { events: [backoff] },
        {
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
        },
      ),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('Could not be read');
    });
    expect(page().textContent).toContain('BackOff');
    expect(page().textContent).not.toContain('Nothing here');
  });

  it('says nothing here once, when both sources answered that they have nothing', async () => {
    stubFetch(
      routes(
        [EVENTS_TAB, CHANGES_TAB],
        { events: [] },
        { '/api/c/prod/changes': { body: { state: 'ok', data: { entries: [], entriesState: 'empty' } } } },
      ),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=activity');

    await waitFor(() => {
      expect(page().textContent).toContain('Nothing here');
    });
    expect(page().querySelectorAll('[data-state="empty"]')).toHaveLength(1);
  });
});

/** An extension's view of the events replaces the core's event lines, only while it is on. */
describe('an extension\'s Activity view', () => {
  const View = lazy(() => Promise.resolve({ default: () => <div data-testid="view">the view</div> }));
  const demo: Extension = {
    key: 'demo',
    nav: { label: 'Demo', path: 'x/demo' },
    routes: [],
    activity: { kinds: ['pods'], Component: View },
  };
  const onFor = (keys: readonly string[]): StubRoutes => ({
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: { groups: [{ key: 'extra', label: 'Extra', items: keys.map((key) => ({ text: key, href: `/c/prod/x/${key}` })), deferred: false }] },
      },
    },
  });

  it('draws the view in place of the events and keeps the changes, while the extension is on', async () => {
    stubFetch(routes([EVENTS_TAB, CHANGES_TAB], { events: [backoff], eventsNote: 'capped' }, onFor(['demo'])));
    mountApp('/c/prod/pods/argo/api-0?tab=activity', [demo]);

    await waitFor(() => {
      expect(page().querySelector('[data-testid="view"]')).not.toBeNull();
    });
    await waitFor(() => {
      expect(page().textContent).toContain('kubectl-edit Update');
    });
    expect(page().querySelectorAll('[data-activity="event"]')).toHaveLength(0);
    expect(page().querySelectorAll('[data-activity="change"]')).toHaveLength(2);
    expect(page().textContent).not.toContain('capped');
  });

  it('leaves the core events as they are while the extension is off', async () => {
    stubFetch(routes([EVENTS_TAB, CHANGES_TAB], { events: [backoff] }, onFor([])));
    mountApp('/c/prod/pods/argo/api-0?tab=activity', [demo]);

    await waitFor(() => {
      expect(page().querySelectorAll('[data-activity="event"]')).toHaveLength(1);
    });
    expect(page().querySelector('[data-testid="view"]')).toBeNull();
  });

  it('leaves the core events on a kind the view does not name', async () => {
    stubFetch(routes([EVENTS_TAB, CHANGES_TAB], { events: [backoff] }, onFor(['demo'])));
    mountApp('/c/prod/pods/argo/api-0?tab=activity', [{ ...demo, activity: { kinds: ['jobs'], Component: View } }]);

    await waitFor(() => {
      expect(page().querySelectorAll('[data-activity="event"]')).toHaveLength(1);
    });
    expect(page().querySelector('[data-testid="view"]')).toBeNull();
  });
});
