import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp, stubFetch, type StubReply, type StubRoutes } from '../../testing/harness';

/**
 * The dashboard, at the altitude of the page.
 *
 * Every assertion here goes through the mounted application and a real
 * `Response`, because the claim being made is about what a reader sees. A
 * unit test of the narrowing would pass while the page rendered three panels
 * and swallowed the fourth - the kind of defect that survives a covered
 * predicate and an uncovered call site.
 *
 * The assertions that matter are the ones where two answers must not look
 * alike: a panel that failed beside three that did not, a leaderboard that is
 * `absent` rather than zero, and a reading that cannot be believed rather than
 * one that is low.
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

const clusters = {
  body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] },
};

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

/** A dashboard where every panel answered. */
const wholeCluster = {
  state: 'ok',
  data: {
    cluster: 'prod',
    counts: {
      state: 'ok',
      items: [
        { kind: 'nodes', label: 'Nodes', count: 6 },
        { kind: 'nodes', label: 'Healthy nodes', count: 6 },
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
        },
      ],
    },
    top: {
      state: 'absent',
      reason: {
        code: 'not_installed',
        message: 'no metrics backend was found on this cluster',
        retryable: true,
      },
    },
    events: { state: 'empty', items: [] },
  },
};

describe('a dashboard the access review narrowed says so', () => {
  it('draws the server’s sentence above the panels, and nothing on a whole answer', async () => {
    // Every count below is about the namespaces the person may list, and a
    // dashboard that says "6 nodes, 3 pods" without saying so is an empty answer
    // and a narrowed one drawn alike.
    const note =
      'You may not list everything this page counts in every namespace, so its pods, events and workloads are those of the namespaces where you may.';
    stubFetch(withOverview({ body: { ...wholeCluster, data: { ...wholeCluster.data, accessNote: note } } }));
    mountApp('/c/prod');
    await waitFor(() => {
      expect(page().querySelector('[data-note="access"]')?.textContent).toBe(note);
    });
  });

  it('draws no note on a dashboard that is the cluster’s whole answer', async () => {
    stubFetch(withOverview({ body: wholeCluster }));
    mountApp('/c/prod');
    await waitFor(() => {
      expect(page().textContent).toContain('Nodes');
    });
    expect(page().querySelector('[data-note="access"]')).toBeNull();
  });
});

describe('a person who may not list nodes', () => {
  it('reads the refusal once, on the capacity, and the rest of the page as it answered', async () => {
    const refusal = {
      code: 'verb_denied',
      message: 'the cluster says you may not list nodes',
      scope: { cluster: 'prod', resource: 'nodes', verb: 'list' },
      retryable: false,
    };
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            cluster: 'prod',
            version: 'v1.31.4',
            counts: { state: 'ok', items: [{ kind: 'deployments', label: 'Deployments', count: 3 }] },
            capacity: { state: 'forbidden', reason: refusal },
            alerts: { state: 'forbidden', reason: refusal },
            history: { state: 'forbidden', reason: refusal },
            top: {
              state: 'absent',
              reason: { code: 'not_installed', message: 'no metrics backend was found' },
            },
            events: { state: 'empty', items: [] },
          },
        },
      }),
    );
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Deployments');
    });
    const text = page().textContent ?? '';
    expect(text.split('the cluster says you may not list nodes').length - 1).toBe(1);
    expect(text).toContain('Kubernetes v1.31.4');
    expect(text).not.toContain('Over time');
  });
});

describe('the composed envelope survives the page', () => {
  it('draws the panels that answered beside the one that did not', async () => {
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            cluster: 'prod',
            counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 6 }] },
            capacity: {
              state: 'unreachable',
              reason: {
                code: 'cluster_unreachable',
                message: 'nothing answered in time',
                retryable: true,
              },
            },
            top: {
              state: 'absent',
              reason: { code: 'not_installed', message: 'no metrics backend was found' },
            },
            events: { state: 'empty', items: [] },
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Nodes');
    });

    // The whole point of four envelopes: the count is on screen, and the
    // capacity panel says what happened to it, in the server's words.
    const text = page().textContent ?? '';
    expect(text).toContain('Nodes');
    expect(text).toContain('nothing answered in time');
    expect(text).toContain('Could not be asked');

    // And the failure is confined to its own panel: the other three drew.
    expect(page().querySelector('[data-kind="nodes"]')).not.toBeNull();
    expect(text).toContain('Recent warningsNothing here');
  });

  it('draws a panel the payload said nothing about as neither a state nor a blank', async () => {
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            cluster: 'prod',
            counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 1 }] },
            // capacity, top and events omitted entirely, which the contract
            // permits and the implementation never does.
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Not answered');
    });
    const text = page().textContent ?? '';
    // Not "Not installed", not "Nothing here": a missing panel is a fault in
    // this product and must not be dressed as a fact about a cluster.
    expect(text).not.toContain('Not installed');
    expect(text).toContain('carried nothing at all about capacity');
  });

  it('refuses an ok panel with an empty items array rather than drawing it calmly', async () => {
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            cluster: 'prod',
            counts: { state: 'ok', items: [] },
            capacity: { state: 'empty', items: [] },
            top: { state: 'empty', items: [] },
            events: { state: 'empty', items: [] },
          },
        },
      }),
    );

    mountApp('/c/prod');

    // `{"state":"ok","items":[]}` is the shape that would report "no pods" for a
    // cluster running 429 of them. It reaches the UI as a failure, because it is
    // a bug in this product and not a fact about a cluster.
    await waitFor(() => {
      expect(page().textContent).toContain("The console's own API answered out of contract");
    });
  });
});

describe('a leaderboard that is absent is not a leaderboard of zeroes', () => {
  it('draws no leaderboard panel, no notice and no rows where there is no metrics source', async () => {
    stubFetch(withOverview({ body: wholeCluster }));

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-percent]')).not.toBeNull();
    });

    // No metrics source, no panel - not a "Not installed" notice where a
    // leaderboard would be.
    const text = page().textContent ?? '';
    const headings = [...page().querySelectorAll('h2')].map((heading) => heading.textContent);
    expect(headings).not.toContain('Most used');
    expect(text).not.toContain('Not installed');
    expect(text).not.toContain('no metrics backend was found on this cluster');
    // Nothing that could be read as a measurement of nothing.
    expect(text).not.toContain('This board was produced and has no rows');
    expect(page().querySelectorAll('[data-percent]')).toHaveLength(1); // the CPU gauge only
  });

  it('draws no leaderboard panel at all where the leaderboard is empty', async () => {
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            cluster: 'prod',
            counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 1 }] },
            capacity: { state: 'empty', items: [] },
            top: { state: 'empty', items: [] },
            events: { state: 'empty', items: [] },
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('h1')?.textContent).toBe('prod');
    });
    // An empty leaderboard is not a panel with "Nothing here" in it, it is no
    // panel.
    const headings = [...page().querySelectorAll('h2')].map((heading) => heading.textContent);
    expect(headings).not.toContain('Most used');
    expect(page().textContent).not.toContain('produced no leaderboard');
    expect(page().textContent).not.toContain('Not installed');
  });
});

describe('a reading that cannot be believed is not drawn as a number', () => {
  it('draws no bar and no figure where the cluster reported no capacity', async () => {
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            cluster: 'prod',
            counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 1 }] },
            capacity: {
              state: 'ok',
              items: [
                {
                  label: 'Memory',
                  requested: { value: null, trustworthy: false, text: 'the cluster reported no capacity' },
                  capacity: { value: null, trustworthy: false, text: 'the cluster reported no capacity' },
                },
              ],
            },
            top: { state: 'empty', items: [] },
            events: { state: 'empty', items: [] },
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('the cluster reported no capacity');
    });

    // An empty track reads as 0%, which reads as an idle machine, which is the
    // opposite of the truth when nothing could be measured.
    expect(page().querySelectorAll('[data-percent]')).toHaveLength(0);
    expect(page().textContent).toContain('—');
    expect(page().textContent).not.toContain('0%');
  });

  it('treats a non-finite value as no value, even though JSON cannot carry one', async () => {
    // Sent as a raw body: `JSON.stringify` turns Infinity into null, so the only
    // way to reproduce what `strconv.ParseFloat` accepts and a hand-written
    // encoder might emit is to write the literal.
    stubFetch(
      withOverview({
        body: `{"state":"ok","data":{"cluster":"prod",
          "counts":{"state":"ok","items":[{"kind":"nodes","label":"Nodes","count":1}]},
          "capacity":{"state":"ok","items":[{"label":"CPU",
            "requested":{"value":1,"trustworthy":true,"text":"1 core"},
            "capacity":{"value":8,"trustworthy":true,"text":"8 cores"},
            "used":{"value":1e999,"trustworthy":true,"text":"Infinity"}}]},
          "top":{"state":"empty","items":[]},
          "events":{"state":"empty","items":[]}}}`,
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().querySelector('[data-capacity="CPU"]')).not.toBeNull();
    });

    // The ring is `requested / allocatable` - the figure that decides whether
    // the next pod schedules and the only one available without a metrics
    // backend. So a ring is drawn here, correctly, from two finite numbers, and
    // what must not be drawn is the **used** figure.
    const cell = page().querySelector('[data-capacity="CPU"]');
    const ring = cell?.querySelector('[data-percent]');
    expect(ring?.getAttribute('data-percent')).toBe('13'); // 1 of 8 cores, requested

    // Drawn as a gap. A number would be the claim that this cluster is using
    // Infinity cores, made from a value that is not a reading.
    //
    // `ReadingText` prints the dash and *not measured*, and puts the server's
    // own `text` on hover - so the literal "Infinity" is
    // there only as the server's words, in the title. What is asserted is that
    // it went down the unmeasured path: one ring on the panel, and the used
    // figure is a gap rather than a value.
    const used = cell?.querySelector('[data-unmeasured]');
    expect(used?.textContent).toContain('—');
    expect(used?.textContent).toContain('not measured');
    expect(used?.getAttribute('title')).toBe('Infinity');
    expect(cell?.querySelectorAll('[data-percent]')).toHaveLength(1);
  });
});

describe('the cluster itself refusing', () => {
  it('says what the cluster said and whose refusal it is, with no Cluster page to point at', async () => {
    stubFetch(
      withOverview({
        // `unreadable`: the cluster answered and refused the read. An
        // `unreachable` one is a different page - see the block on the probe's
        // verdict.
        body: {
          state: 'unreadable',
          reason: {
            code: 'read_denied',
            message: 'listing the nodes of prod: Unauthorized',
            retryable: true,
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('listing the nodes of prod: Unauthorized');
    });
    // The line under the sentence says whose refusal it is, and carries no link
    // to a Cluster page.
    expect(page().textContent).toContain('signing in here grants the console, never a cluster.');
    expect(page().querySelector('a[href="/health"]')).toBeNull();
    expect(page().textContent).not.toContain('Cluster page');
  });

  it('tells a context this console does not serve from one that did not answer', async () => {
    stubFetch(
      withOverview({
        status: 404,
        contentType: 'application/problem+json',
        body: { status: 404, title: 'Not Found', code: 'not_found', detail: 'no such cluster' },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('This console is not configured for that context.');
    });
    expect(page().textContent).not.toContain('Could not be asked');
  });
});

/**
 * On a cluster that refuses, the notice comes first, and below it the panels
 * still say what they could not read - a blank would read as a dashboard with
 * nothing to say rather than one whose ten panels could not be read.
 */
describe('on a refusing cluster the refusal comes first, and the panels still say what they could not read', () => {
  // A refusal the cluster *answered* with. An unreachable cluster is one
  // warning and a Refresh, pinned in its own block below; these hold the shape
  // for every other refusal.
  const refusal = withOverview({
    body: {
      state: 'unreadable',
      reason: {
        code: 'read_denied',
        message: 'dial tcp 10.0.0.1:6443: i/o timeout',
        retryable: true,
      },
    },
  });

  it('draws the notice before the heading', async () => {
    stubFetch(refusal);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('dial tcp 10.0.0.1:6443: i/o timeout');
    });

    const notice = page().querySelector('[role="status"][data-state="unreadable"]');
    const heading = page().querySelector('h1');
    if (notice === null || heading === null) throw new Error('the notice or the heading is missing');
    // The notice precedes the heading in the document, which is the order the
    // eye reads the page in.
    expect(notice.compareDocumentPosition(heading) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(heading.textContent).toBe('prod');
    // And the line saying whose refusal it is, between the two.
    const note = [...page().querySelectorAll('p')].find((p) => p.textContent.includes('never a cluster'));
    if (note === undefined) throw new Error('no line under the refusal');
    expect(notice.compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(note.compareDocumentPosition(heading) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });

  it('draws every panel in the refusal’s own state rather than hiding them', async () => {
    stubFetch(refusal);
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('dial tcp 10.0.0.1:6443: i/o timeout');
    });

    // The notice at the top, and then one per panel: capacity, the history,
    // the alerts, the kind tiles, the phase bar, trouble, restarting, the
    // leaderboards and the warnings - nine, each saying the same sentence about
    // its own subject. Ten with the notice.
    const notices = page().querySelectorAll('[role="status"][data-state="unreadable"]');
    expect(notices.length).toBe(10);
    for (const one of notices) {
      expect(one.textContent).toContain('dial tcp 10.0.0.1:6443: i/o timeout');
    }
    // None of them reads as a cluster with nothing on it.
    expect(page().textContent).not.toContain('answered and had none');
    expect(page().textContent).not.toContain('nothing needs acting on');
    // One retry, in the notice at the top: nine more that re-read the same page
    // would be nine copies of one control.
    expect(page().querySelectorAll('button').length).toBe(1);
    expect(notices[0]?.querySelector('button')?.textContent).toBe('Try again');
  });

  it('is unchanged on a cluster that answered: the head first, no pointer', async () => {
    stubFetch(withOverview({ body: wholeCluster }));
    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Kubernetes');
    });
    expect(page().querySelector('h1')?.textContent).toBe('prod');
    expect(page().querySelector('a[href="/health"]')).toBeNull();
    expect(page().querySelector('[role="status"][data-state="unreadable"]')).toBeNull();
  });
});

describe('a list the server cut says so on its last page', () => {
  // With 450 restarting containers the server sends the worst 400 and
  // `restartingTotal: 450`; the pager must not end on "of 400".
  it('draws the restarting pager as cut, from the payload’s own total', async () => {
    const rows = Array.from({ length: 400 }, (_, index) => ({
      object: { text: `pod-${String(index)}` },
      container: 'app',
      init: false,
      restarts: 450 - index,
    }));
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            ...wholeCluster.data,
            restarting: { state: 'ok', items: rows },
            restartingTotal: 450,
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('pod-0');
    });
    const pager = page().querySelector('nav[aria-label="Containers that keep exiting, pages"]');
    if (pager === null) throw new Error('the restarting panel drew no pager');
    const next = Array.from(pager.querySelectorAll('button')).find((button) => button.textContent === 'Next');
    if (next === undefined) throw new Error('no Next button');
    for (let step = 0; step < 3; step += 1) fireEvent.click(next);

    await waitFor(() => {
      expect(pager.textContent).toContain('301–400 of 450, the first 400 shown');
    });
    expect(page().textContent).toContain('pod-399');
  });
  // The same for the warning feed: the newest 400 of 450 events, and
  // `eventsTotal: 450`.
  it('draws the warnings pager as cut, from the payload’s own total', async () => {
    const rows = Array.from({ length: 400 }, (_, index) => ({
      text: `event-${String(index)}`,
      reason: 'BackOff',
      message: `event-${String(index)}`,
      count: 1,
      tone: 'warn' as const,
    }));
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            ...wholeCluster.data,
            events: { state: 'ok', items: rows },
            eventsTotal: 450,
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('event-0');
    });
    const pager = page().querySelector('nav[aria-label="Recent warning events, pages"]');
    if (pager === null) throw new Error('the warnings panel drew no pager');
    const next = Array.from(pager.querySelectorAll('button')).find((button) => button.textContent === 'Next');
    if (next === undefined) throw new Error('no Next button');
    for (let step = 0; step < 3; step += 1) fireEvent.click(next);

    await waitFor(() => {
      expect(pager.textContent).toContain('301–400 of 450, the first 400 shown');
    });
    expect(page().textContent).toContain('event-399');
  });
});

describe('a metrics backend found and not reached (#127)', () => {
  const dashboard = (history: unknown): StubReply => ({
    body: {
      state: 'ok',
      data: {
        cluster: 'prod',
        counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 6 }] },
        history,
        events: { state: 'empty', items: [] },
      },
    },
  });

  /** The overview's own reads, counted. */
  function counted(reply: StubReply): string[] {
    const asked: string[] = [];
    stubFetch(withOverview(reply));
    const answer = globalThis.fetch;
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      if (url.includes('/overview')) asked.push(url);
      return answer(input, init);
    });
    return asked;
  }

  it('reads the overview again when the small retry is pressed', async () => {
    const asked = counted(
      dashboard({ state: 'unreachable', reason: { code: 'cluster_unreachable', message: 'Prometheus not reached', retryable: true } }),
    );
    mountApp('/c/prod');
    const retry = await waitFor(() => {
      const found = page().querySelector('[data-no-metrics] button[aria-label="Try again"]');
      if (!(found instanceof HTMLButtonElement)) throw new Error('no retry yet');
      return found;
    });
    const before = asked.length;
    fireEvent.click(retry);
    await waitFor(() => {
      expect(asked.length).toBe(before + 1);
    });
  });

  it('never draws a pending history as No metrics available', async () => {
    counted(
      dashboard({ state: 'pending', reason: { code: 'not_probed_yet', message: 'the first round has not reached this cluster' } }),
    );
    mountApp('/c/prod');
    await waitFor(() => {
      expect(page().querySelector('[data-state="pending"]')).not.toBeNull();
    });
    expect(page().querySelector('[data-no-metrics]')).toBeNull();
    expect(page().textContent).not.toContain('No metrics available');
  });

  it('draws Over time as one quiet line, the sentence on hover, with a small retry', async () => {
    const said = 'service monitoring/prometheus:9090 found, but neither the API service proxy nor a port-forward reached it';
    stubFetch(
      withOverview({
        body: {
          state: 'ok',
          data: {
            cluster: 'prod',
            counts: { state: 'ok', items: [{ kind: 'nodes', label: 'Nodes', count: 6 }] },
            history: {
              state: 'unreachable',
              reason: { code: 'cluster_unreachable', message: said, detail: said, retryable: true },
            },
            top: { state: 'unreadable', reason: { code: 'unreadable', message: 'the leaderboard query answered garbage' } },
            events: { state: 'empty', items: [] },
          },
        },
      }),
    );

    mountApp('/c/prod');

    await waitFor(() => {
      expect(page().textContent).toContain('Over time');
    });
    const text = page().textContent ?? '';
    expect(text).not.toContain('Could not be asked');
    expect(text).not.toContain(said);
    const lines = Array.from(page().querySelectorAll('[data-no-metrics]'));
    expect(lines.map((line) => line.getAttribute('data-state'))).toEqual(['unreachable', 'unreadable']);
    expect(lines[0]?.getAttribute('title')).toBe(said);
    expect(lines[1]?.getAttribute('title')).toBe('the leaderboard query answered garbage');
    // Retry only where the reason allows it: the history, not the leaderboard.
    expect(lines[0]?.querySelector('button[aria-label="Try again"]')).not.toBeNull();
    expect(lines[1]?.querySelector('button')).toBeNull();
    expect(page().querySelector('[data-no-metrics].font-mono, [data-no-metrics] .font-mono')).toBeNull();
  });
});

