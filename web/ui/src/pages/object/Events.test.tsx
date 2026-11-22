import { fireEvent, waitFor } from '@testing-library/react';
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

/** What the server answers for a pod, trimmed to the part under test: the events
 *  ride **with** the object, and their tab carries no endpoint. */
function object(extra: Record<string, unknown>): StubRoutes {
  return {
    [OBJECT]: {
      body: {
        state: 'ok',
        data: {
          kind: 'pods',
          namespace: 'argo',
          name: 'api-0',
          title: 'Pod',
          tabs: [
            { key: 'events', label: 'Events' },
            { key: 'manifest', label: 'Manifest', endpoint: '/api/c/prod/manifest?kind=pods', lazy: true },
          ],
          sections: [{ fields: [{ label: 'Phase', value: 'Running' }] }],
          ...extra,
        },
      },
    },
  };
}

function shell(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop' } },
    ...extra,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

const backoff = {
  type: 'Warning',
  reason: 'BackOff',
  from: 'kubelet on node-3',
  message: 'Back-off restarting failed container api in pod api-0_argo(8f2c)',
  count: 340,
  first: '2026-09-12T09:12:00Z',
  last: '2026-09-12T11:48:00Z',
};

/**
 * The panel a user opens first when something is wrong.
 */
describe('the events panel', () => {
  it('draws what the cluster still remembers, whole', async () => {
    stubFetch(shell(object({ events: [backoff] })));
    mountApp('/c/prod/pods/argo/api-0?tab=events');

    await waitFor(() => {
      expect(page().textContent).toContain('BackOff');
    });
    const text = page().textContent ?? '';
    // Never clipped: the message is the entire content of an event, and the
    // answer is at the end as often as at the start.
    expect(text).toContain('Back-off restarting failed container api in pod api-0_argo(8f2c)');
    expect(text).toContain('kubelet on node-3');
    // A BackOff that fired 340 times and one that fired once are the same row
    // without this, and the first is an incident.
    expect(text).toContain('×340');
  });

  it('folds an event whose message runs to several lines, and keeps a one-line one standing', async () => {
    const mount = {
      ...backoff,
      reason: 'FailedMount',
      count: 1,
      message: 'MountVolume.SetUp failed for volume "data" : mount failed: exit status 32\nOutput: mount.nfs: access denied by server',
    };
    stubFetch(shell(object({ events: [backoff, mount] })));
    mountApp('/c/prod/pods/argo/api-0?tab=events');

    await waitFor(() => {
      expect(page().textContent).toContain('FailedMount');
    });
    const folded = page().querySelectorAll('details[data-folded-message]');
    expect(folded).toHaveLength(1);
    expect(folded[0]?.hasAttribute('open')).toBe(false);
    expect(folded[0]?.querySelector('[data-folded-message-control]')?.textContent).toContain('show error');
    expect(folded[0]?.querySelector('[data-folded-message-line]')?.textContent).toBe(
      'Output: mount.nfs: access denied by server',
    );
    // The one-line BackOff is still a sentence, with no control of its own.
    const sentence = [...page().querySelectorAll('p')].find((p) => p.textContent.startsWith('Back-off restarting'));
    expect(sentence?.closest('details')).toBeNull();
  });

  it('asks the server for nothing at all', async () => {
    const asked: string[] = [];
    stubFetch(shell(object({ events: [backoff] })));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url));
      return stubbed(input, init);
    });

    mountApp('/c/prod/pods/argo/api-0');
    await waitFor(() => {
      expect(page().querySelector('#object-tab-activity')).not.toBeNull();
    });
    const before = asked.length;
    fireEvent.click(page().querySelector('#object-tab-activity')!);

    await waitFor(() => {
      expect(page().textContent).toContain('BackOff');
    });
    // The rows came with the object. A tab that fetched would be a second round
    // trip for bytes already in hand, which is why its `Tab` carries no
    // endpoint.
    expect(asked.slice(before)).toEqual([]);
  });

  it.each([
    // The server's own sentences, from `internal/detail/detail.go`.
    ['the ordinary quiet object', 'No events. Kubernetes keeps them for about an hour, so a quiet object has none.'],
    [
      'a person who may not list every namespace',
      'No events in the namespaces you may list; events in the others are not shown to you.',
    ],
    ['a read that failed', 'Events could not be read: events is forbidden'],
  ])("says why an empty list is empty, once, in the server's words: %s", async (_what, note) => {
    stubFetch(shell(object({ events: [], eventsNote: note })));
    mountApp('/c/prod/pods/argo/api-0?tab=events');

    // The founding collapse in the one place a user is looking for a
    // cause: "nothing recently" drawn as a blank panel reads as "nothing ever".
    await waitFor(() => {
      expect(page().textContent).toContain(note);
    });
    // One notice, and the server's sentence is the whole of it: no generic sentence above it, no "No events" title
    // repeating the note's first words, and no title calling a failed read
    // empty.
    const notices = page().querySelectorAll('[data-state="empty"]');
    expect(notices).toHaveLength(1);
    expect(notices[0]?.textContent).toBe(note);
    expect(page().textContent).not.toContain('there is nothing to show');
    expect(page().textContent?.split('No events').length ?? 0).toBeLessThanOrEqual(2);
  });

  it("writes the stamp in the console's one date format", async () => {
    // Built from local components: the reader's zone is kept, so the figures
    // are the same on every machine, and the shape is the UI's - not the
    // machine's French or American locale.
    const at = new Date(2026, 9, 10, 14, 3, 5).toISOString();
    stubFetch(
      shell(object({ events: [{ type: 'Warning', reason: 'BackOff', message: 'Back-off', last: at }] })),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=events');
    await waitFor(() => {
      expect(page().querySelector('time')?.textContent).toBe('Oct 10, 14:03:05');
    });
  });

  it('draws the span where a series has one, and no stamp where there is none', async () => {
    stubFetch(
      shell(
        object({
          events: [backoff, { type: 'Normal', reason: 'Scheduled', message: 'Assigned to node-3' }],
        }),
      ),
    );
    mountApp('/c/prod/pods/argo/api-0?tab=events');

    await waitFor(() => {
      expect(page().textContent).toContain('Scheduled');
    });
    // One arrow, from the one event that has two different instants: *since
    // 09:12, still firing at 11:48* is a different sentence from *at 11:48*.
    expect(page().querySelectorAll('time')).toHaveLength(1);
    expect(page().querySelector('time')?.textContent).toContain('→');
  });
});

/**
 * A controller's panel carries the events of what it owns:
 * a Deployment's reason is usually on its pods, and a row about a pod has to say
 * so, or a pod's BackOff reads as the Deployment's own.
 */
describe("a controller's events", () => {
  const DEPLOYMENT = '/api/c/prod/r/deployments/argo/api';

  function deployment(extra: Record<string, unknown>): StubRoutes {
    return shell({
      [DEPLOYMENT]: {
        body: {
          state: 'ok',
          data: {
            kind: 'deployments',
            namespace: 'argo',
            name: 'api',
            title: 'Deployment',
            tabs: [{ key: 'events', label: 'Events' }],
            sections: [],
            ...extra,
          },
        },
      },
    });
  }

  it('names the owned object a row is about, as a link to it, and nothing on its own rows', async () => {
    const own = { type: 'Normal', reason: 'ScalingReplicaSet', message: 'Scaled up replica set api-7d9f to 1' };
    const pods = { ...backoff, about: { kind: 'pods', title: 'Pod', name: 'api-7d9f-x2k' } };
    stubFetch(deployment({ events: [pods, own] }));
    mountApp('/c/prod/deployments/argo/api?tab=events');

    await waitFor(() => {
      expect(page().textContent).toContain('ScalingReplicaSet');
    });
    const named = page().querySelectorAll('[data-event-about]');
    expect(named).toHaveLength(1);
    expect(named[0]?.textContent).toBe('Pod api-7d9f-x2k');
    // On the object's own page the link is the owned object's own page, in the
    // namespace of the one on screen.
    expect(named[0]?.getAttribute('href')).toBe('/c/prod/pods/argo/api-7d9f-x2k');
  });

  it("draws one empty state for the whole family, in the server's words", async () => {
    const note =
      'No events for this Deployment or the ReplicaSets and pods it owns. ' +
      'Kubernetes keeps them for about an hour, so a quiet object has none.';
    stubFetch(deployment({ events: [], eventsNote: note }));
    mountApp('/c/prod/deployments/argo/api?tab=events');

    await waitFor(() => {
      expect(page().textContent).toContain(note);
    });
    expect(page().querySelectorAll('[data-state="empty"]')).toHaveLength(1);
  });
});

/**
 * An Event is the one object that *is* a sentence, and the panel showed
 * everything except it.
 */
describe('what an event itself says', () => {
  const EVENT = '/api/c/prod/r/events/argo/api.17f';

  function eventObject(extra: Record<string, unknown>): StubRoutes {
    return {
      '/api/session': session,
      '/api/clusters': clusters,
      '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
      '/api/capabilities': { body: { mode: 'desktop' } },
      [EVENT]: {
        body: {
          state: 'ok',
          data: {
            kind: 'events',
            namespace: 'argo',
            name: 'api.17f',
            title: 'Event',
            tabs: [],
            sections: [],
            ...extra,
          },
        },
      },
    };
  }

  it('draws the message whole, with who said it and about what', async () => {
    stubFetch(
      eventObject({
        event: {
          message: 'Back-off pulling image "registry.internal/api:v2"',
          reason: 'BackOff',
          type: 'Warning',
          source: 'kubelet',
          node: 'node-3',
          count: 47,
          object: 'Pod/api-7d9f',
          objectField: 'spec.containers{istio-proxy}',
          first: '2026-09-12T09:12:00Z',
          last: '2026-09-12T11:48:00Z',
        },
      }),
    );
    mountApp('/c/prod/events/argo/api.17f');

    await waitFor(() => {
      expect(page().textContent).toContain('Back-off pulling image');
    });
    const text = page().textContent ?? '';
    // Never clipped: the list cuts at 140 characters because a table cell
    // holding a paragraph is a column nobody can read. This is the whole thing.
    expect(text).toContain('registry.internal/api:v2');
    expect(text).toContain('kubelet');
    // Which node refused to pull this is the next question.
    expect(text).toContain('node-3');
    expect(text).toContain('Pod/api-7d9f');
    // On a pod with a sidecar, the difference between the app failing and the
    // mesh failing.
    expect(text).toContain('spec.containers{istio-proxy}');
    expect(text).toContain('×47');
  });

  it('folds a message that runs to several lines, as the Events tab does', async () => {
    stubFetch(
      eventObject({
        event: {
          message: 'Error: UPGRADE FAILED: pre-upgrade hooks failed\nError: job failed: BackoffLimitExceeded',
          reason: 'UpgradeFailed',
          type: 'Warning',
        },
      }),
    );
    mountApp('/c/prod/events/argo/api.17f');

    await waitFor(() => {
      expect(page().querySelector('details[data-folded-message]')).not.toBeNull();
    });
    const folded = page().querySelector('details[data-folded-message]');
    expect(folded?.hasAttribute('open')).toBe(false);
    expect(folded?.querySelector('[data-folded-message-line]')?.textContent).toBe(
      'Error: job failed: BackoffLimitExceeded',
    );
    // A Warning's output is an error, as on the Events tab.
    expect(folded?.querySelector('[data-folded-message-control]')?.textContent).toContain('show error');
    expect(folded?.querySelector('[data-folded-message-control]')?.textContent).not.toContain('show message');
  });

  it('calls a Normal event\'s folded output a message', async () => {
    stubFetch(
      eventObject({
        event: { message: 'Pulled image\nDigest: sha256:abc', reason: 'Pulled', type: 'Normal' },
      }),
    );
    mountApp('/c/prod/events/argo/api.17f');

    await waitFor(() => {
      expect(page().querySelector('details[data-folded-message]')).not.toBeNull();
    });
    expect(page().querySelector('[data-folded-message-control]')?.textContent).toContain('show message');
  });

  it('draws nothing at all on an object that is not an event', async () => {
    stubFetch(
      eventObject({
        kind: 'pods',
        title: 'Pod',
        sections: [{ fields: [{ label: 'Phase', value: 'Running' }] }],
      }),
    );
    mountApp('/c/prod/events/argo/api.17f');

    await waitFor(() => {
      expect(page().textContent).toContain('Running');
    });
    // No heading over a blank: the member is absent and so is the panel.
    expect(page().textContent).not.toContain('Back-off');
  });
});
