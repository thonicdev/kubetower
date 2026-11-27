import { QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, waitFor } from '@testing-library/react';
import type { ReactElement } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailPage, EnvVar, PodContainer } from '../../api';
import { createQueryClient } from '../../query/client';
import { stubFetch, type StubRoutes } from '../../testing/harness';
import { Containers, ENV_FOLD_AFTER } from './Containers';
import { resetEnvFolds } from './envFolds';
import { ObjectDetail } from './ObjectDetail';

/**
 * What a pod is made of, and the one door that reveals.
 *
 * Through the real panel.
 *
 * Two of these are about a **value that must not be on screen** and one is about
 * an absence that must not be colored: an ordinary healthy Argo CD server pod
 * carries thirty `optional: true` references to keys nobody sets, and drawing
 * each with a warning pill reading "not read" would be wrong twice - the object
 * *had* been read, and nothing about it is a warning - and a color spent on the
 * normal case is a color the user stops seeing.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetEnvFolds();
});

const OBJECT = '/api/c/prod/r/pods/shop-prod/api-1';

const containers: PodContainer[] = [
  {
    name: 'wait-for-db',
    image: 'busybox:1.36',
    init: true,
    // Nothing declared and nothing reported, which is what every container of a
    // Pending pod looks like. Both absences sit on this container so that the
    // one below can carry the opposite of each.
    probes: [
      { kind: 'liveness', state: 'absent' },
      { kind: 'readiness', state: 'absent' },
      { kind: 'startup', state: 'absent' },
    ],
    status: {
      state: 'unreported',
      ready: false,
      problem: 'the kubelet has reported nothing about this container yet',
    },
  },
  {
    name: 'api',
    image: 'shop/api:1.4.2',
    init: false,
    pullPolicy: 'Always',
    probes: [
      {
        kind: 'liveness',
        state: 'declared',
        handler: 'httpGet',
        target: '/healthz:8080',
        initialDelaySeconds: 15,
        periodSeconds: 10,
        timeoutSeconds: 1,
        failureThreshold: 3,
      },
      {
        kind: 'readiness',
        state: 'declared',
        handler: 'tcpSocket',
        target: ':8080',
        periodSeconds: 5,
        failureThreshold: 2,
      },
      // Declared on the other two and absent here, on one container, so the
      // distinction is drawn side by side rather than across two fixtures.
      { kind: 'startup', state: 'absent' },
    ],
    status: { state: 'running', ready: true, restarts: 2 },
    requests: [{ name: 'cpu', value: '100m' }],
    limits: [{ name: 'memory', value: '512Mi' }],
    env: [
      { name: 'LOG_LEVEL', value: 'info', secret: false, held: false, clipped: false },
      { name: 'NODE_NAME', from: 'field spec.nodeName', secret: false, held: false, clipped: false },
      {
        name: 'DB_PASSWORD',
        secret: true,
        source: { kind: 'Secret', name: 'db-credentials', key: 'password', optional: false },
        held: false,
        clipped: false,
      },
      {
        name: 'NGINX_CONF',
        held: true,
        clipped: true,
        resolved: 'worker_processes auto;',
        secret: false,
        source: { kind: 'ConfigMap', name: 'nginx', key: 'nginx.conf', optional: false },
      },
      {
        name: 'MISSING',
        problem: 'the ConfigMap was not read within this panel’s budget',
        absence: 'unread',
        secret: false,
        held: false,
        clipped: false,
        source: { kind: 'ConfigMap', name: 'extras', key: 'missing', optional: false },
      },
      {
        name: 'OPTIONAL',
        problem: 'the ConfigMap does not carry that key, and the reference is optional',
        absence: 'expected',
        secret: false,
        held: false,
        clipped: false,
        source: { kind: 'ConfigMap', name: 'params', key: 'tuning', optional: true },
      },
      {
        name: 'NOT_SET',
        problem: 'the ConfigMap does not carry that key',
        absence: 'unset',
        secret: false,
        held: false,
        clipped: false,
        source: { kind: 'ConfigMap', name: 'params', key: 'required', optional: false },
      },
    ],
    envFrom: [
      { kind: 'ConfigMap', name: 'app-settings', prefix: 'APP_', keys: 4, optional: false },
      {
        kind: 'Secret',
        name: 'unreadable',
        optional: true,
        problem: 'this account may not read that Secret',
      },
    ],
    imported: [
      {
        name: 'APP_TIMEOUT',
        held: true,
        clipped: false,
        secret: false,
        resolved: '30s',
        source: { kind: 'ConfigMap', name: 'app-settings', key: 'TIMEOUT', optional: false },
      },
    ],
  },
];

const page: DetailPage = {
  kind: 'pods',
  namespace: 'shop-prod',
  name: 'api-1',
  title: 'Pod',
  tabs: [],
  sections: [],
  containers,
  envNote: '2 of 5 envFrom imports were not read: this panel stops after 3.',
};

/** Every address the panel asked for, in order. Reset by `mount`, and recorded
 *  by wrapping the harness's stub rather than replacing it - the harness builds a
 *  real `Response`, and one test here is about what a real one carries. */
let asked: string[] = [];

async function mount(over: Partial<DetailPage> = {}, extra: StubRoutes = {}): Promise<HTMLElement> {
  stubFetch({ [OBJECT]: { body: { state: 'ok', data: { ...page, ...over } } }, ...extra });
  asked = [];
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    asked.push(typeof input === 'string' ? input : String(input));
    return stubbed(input, init);
  });
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster/:kind',
        element: (
          <ObjectDetail
            identity={{ cluster: 'prod', kind: 'pods', namespace: 'shop-prod', name: 'api-1' }}
            chrome="drawer"
          />
        ),
      },
      { path: '/health', element: <p>health</p> },
    ],
    { initialEntries: ['/c/prod/pods'] },
  );
  const view = render(
    <QueryClientProvider client={createQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  await waitFor(() => {
    expect(view.container.querySelector('[data-section="containers"]')).not.toBeNull();
  });
  return view.container;
}

/** Opens every tile's Probes section, which is closed by default. */
function openProbes(root: HTMLElement): void {
  for (const button of root.querySelectorAll<HTMLButtonElement>(
    '[data-tile-section="probes"] button[data-fold-control][aria-expanded="false"]',
  )) {
    fireEvent.click(button);
  }
}

describe('the containers panel', () => {
  it('draws init containers first and marks them', async () => {
    // An init container that will not start is the reason the pod is stuck, and
    // it looks identical to a crashing app container if nothing distinguishes
    // them.
    const container = await mount();
    const drawn = [...container.querySelectorAll('[data-container]')];
    expect(drawn.map((node) => node.getAttribute('data-container'))).toEqual([
      'wait-for-db',
      'api',
    ]);
    expect(drawn[0]?.getAttribute('data-init')).toBe('true');
    expect(drawn[0]?.querySelector('[data-init-mark]')).not.toBeNull();
    expect(drawn[1]?.querySelector('[data-init-mark]')).toBeNull();
  });

  it('says "none declared" where a container asked for nothing', async () => {
    // BestEffort decides what the scheduler does with the pod under pressure and
    // is exactly what nobody scrolls a manifest for. A missing row reads as a
    // panel that did not look.
    const container = await mount();
    const init = container.querySelector('[data-container="wait-for-db"]');
    expect(init?.querySelector('[data-qos="besteffort"]')?.textContent).toBe('BestEffort');
    // In the Resources section, which is open by default.
    expect(init?.querySelector('[data-tile-section="resources"] [data-qos="besteffort"]')).not.toBeNull();
    // And the container that did declare something says what, rather than that.
    const app = container.querySelector('[data-container="api"]');
    expect(app?.querySelector('[data-qos="besteffort"]')).toBeNull();
    expect(app?.querySelector('[data-resource="cpu"] [data-amounts="requests"]')?.textContent).toBe('req 100m');
    expect(app?.querySelector('[data-resource="memory"] [data-amounts="limits"]')?.textContent).toBe('lim 512Mi');
  });

  it('draws the pull policy only where the server sent one', async () => {
    const container = await mount();
    expect(
      container.querySelector('[data-container="api"] [data-pull-policy]')?.textContent,
    ).toContain('Always');
    expect(
      container.querySelector('[data-container="wait-for-db"] [data-pull-policy]'),
    ).toBeNull();
  });

  it('keeps a container’s own env apart from what its imports contributed', async () => {
    // The two are not equally knowable: one is written in the pod spec and is a
    // click away on the Manifest tab, the other was learned by reading another
    // object.
    const container = await mount();
    const app = container.querySelector('[data-container="api"]');
    expect(app?.querySelector('[data-env="environment"]')).not.toBeNull();
    const imported = app?.querySelector('[data-env="imported"]');
    expect(imported?.textContent).toContain('APP_TIMEOUT');
    expect(app?.querySelector('[data-env="environment"]')?.textContent).not.toContain(
      'APP_TIMEOUT',
    );
  });

  it('tells a read import from a refused one', async () => {
    // Three answers look identical on a panel that only lists rows: an object
    // with no keys, one this console may not read, and one nobody looked at.
    const container = await mount();
    expect(container.querySelector('[data-import-keys]')?.textContent).toContain('4 keys');
    expect(container.querySelector('[data-import-problem]')?.textContent).toContain(
      'may not read',
    );
  });

  it('says the import list was capped', async () => {
    const container = await mount();
    expect(container.querySelector('[data-env-note]')?.textContent).toContain('stops after 3');
  });
});

describe('a ConfigMap value is drawn and a Secret value is asked for', () => {
  it('draws a resolved ConfigMap value on the row, with no control', async () => {
    // "It is not a credential store... and a control that has to be pressed to
    // see a log level is one that gets pressed without reading the row."
    const container = await mount();
    const row = rowFor(container, 'NGINX_CONF');
    expect(row.querySelector('[data-held="true"]')?.textContent).toContain('worker_processes');
    expect(row.querySelector('[data-reveal]')).toBeNull();
  });

  it('never draws a Secret value, and offers exactly one control for it', async () => {
    const container = await mount();
    const row = rowFor(container, 'DB_PASSWORD');
    expect(row.textContent).not.toContain('hunter2');
    expect(row.querySelectorAll('[data-reveal]')).toHaveLength(1);
    // Decode and not Show: the stored form is base64, and a control whose label
    // does not name its operation is one people write a second one beside.
    expect(row.querySelector('[data-reveal]')?.textContent).toBe('Decode');
  });

  it('keeps the control on a secret-sourced row whatever absence rides with it', async () => {
    // Four absences exist, three of which withdraw the control, but a
    // `valueFrom.secretKeyRef` whose Secret was refused must still show a button.
    // `row.secret` decides on its own, and it has to: the absences are a report
    // of a read, and a Secret is not read to draw this panel.
    //
    // The server cannot produce this row today, and that is the reason to pin it
    // here rather than to leave it to the server. If a Secret ever is read on a
    // drawer open, the control must not be lost in a second place as well - a
    // secret-sourced row with no control and no way to ask is the failure.
    const container = await mount({
      containers: containers.map((one) =>
        one.name === 'api'
          ? {
              ...one,
              env: [
                {
                  name: 'DB_PASSWORD',
                  secret: true,
                  absence: 'unset',
                  problem: 'the Secret does not carry that key',
                  source: { kind: 'Secret', name: 'db-credentials', key: 'password', optional: false },
                  held: false,
                  clipped: false,
                },
              ],
            }
          : one,
      ),
    });

    const row = rowFor(container, 'DB_PASSWORD');
    expect(row.querySelector('[data-reveal]')).not.toBeNull();
    // And the reason rides with it: control or reason is the invariant, and both
    // together is not a contradiction of it.
    expect(row.textContent).toContain('does not carry that key');
  });

  it('has no control anywhere that opens more than one value', async () => {
    // The gesture is per row because the exposure is per row. There is no
    // "reveal all", and there is nothing to add one to.
    const container = await mount();
    const controls = [...container.querySelectorAll('[data-reveal]')];
    // The Secret, and the one `unread` absence. Nothing else.
    expect(controls).toHaveLength(2);
  });

  it('asks the source object by its own address, never the workload', async () => {
    // The row names a key of a ConfigMap or a Secret and the reveal reads *that*
    // object. Several containers import the same ConfigMap under different
    // prefixes, so the variable's name as a container sees it is not the key in
    // the source; addressing the workload would mean re-deriving that mapping on
    // every reveal.
    const container = await mount(
      {},
      {
        '/api/c/prod/env': {
          body: { state: 'ok', data: { key: 'password', value: 'hunter2', truncated: false } },
        },
      },
    );
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);

    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').querySelector('[data-revealed="true"]')).not.toBeNull();
    });

    const reveal = asked.find((path) => path.startsWith('/api/c/prod/env'));
    expect(reveal).toBeDefined();
    const query = new URLSearchParams((reveal ?? '').split('?')[1] ?? '');
    // The source object, and the key **in the source** - not `DB_PASSWORD`,
    // which is the name the container sees.
    expect(query.get('kind')).toBe('secrets');
    expect(query.get('name')).toBe('db-credentials');
    expect(query.get('key')).toBe('password');
    expect(query.get('namespace')).toBe('shop-prod');
    // For the audit line and the labeling only.
    expect(query.get('container')).toBe('api');
    expect(query.get('variable')).toBe('DB_PASSWORD');
    // And the value is in the body, never in the address: a secret in a URL is a
    // secret in an access log.
    expect(reveal).not.toContain('hunter2');
    expect(rowFor(container, 'DB_PASSWORD').textContent).toContain('hunter2');
  });

  it('says a revealed empty value rather than drawing a blank', async () => {
    // An `ok` carrying the empty string is a revealed empty value, and the
    // envelope is the only thing that says so. Reading it as "nothing came back"
    // is the collapse this contract exists to remove, on the one payload where
    // being wrong hides or exposes a credential.
    const container = await mount(
      {},
      { '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: '', truncated: false } } } },
    );
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').textContent).toContain('the value is empty');
    });
  });

  it('draws a binary value as a refusal, never as an empty value', async () => {
    // The door reveals text. A key whose value is not text must be refused, not
    // answered with `{key, value: ''}` - otherwise a keystore arrives here as
    // an `ok` and this row draws *the value is empty*, which is what a key
    // somebody emptied looks like. The refusal is `unreadable` /
    // `undecodable`, no data, and the sentence says what to do instead.
    //
    // This row cannot know in advance: a Secret's values are not in the panel at
    // all, so unlike the Data tab it has no `binary` flag to read and the door's
    // answer is the only thing that can tell it.
    const container = await mount(
      {},
      {
        '/api/c/prod/env': {
          body: {
            state: 'unreadable',
            reason: {
              code: 'undecodable',
              message:
                '"password" is not text — 8 B that does not decode to UTF-8. This console reveals text; read it with kubectl and a pipe.',
            },
          },
        },
      },
    );
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').textContent).toContain('is not text');
    });

    const row = rowFor(container, 'DB_PASSWORD');
    expect(row.textContent).toContain('kubectl');
    // The two answers that must not be confused with it: a revealed empty value,
    // and a value drawn at all.
    expect(row.textContent).not.toContain('the value is empty');
    expect(row.querySelector('[data-revealed="true"]')).toBeNull();
    // And the control is gone. The bytes will not be text on the next click -
    // the server says `retryable: false` - and this file's own rule is that a
    // control which can only repeat itself is one people stop reading.
    expect(row.querySelector('[data-reveal]')).toBeNull();
  });

  it('draws a refusal that arrived without words, rather than a row that says nothing', async () => {
    // An invariant rather than one more branch: a secret-sourced row carries a control or a reason, **never neither**. The
    // one branch that can produce neither is the one that withdraws the control
    // - `undecodable`, which is final - arriving with no message to put in its
    // place. `Reason.message` is required by the contract and *required* is not
    // *non-empty*, so this is a row that says `held back` with nothing to press
    // and nothing to read: a value that will not decode and does not say why.
    const container = await mount(
      {},
      {
        '/api/c/prod/env': {
          body: { state: 'unreadable', reason: { code: 'undecodable', message: '' } },
        },
      },
    );
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]')).toBeNull();
    });

    const row = rowFor(container, 'DB_PASSWORD');
    expect(row.textContent).toContain('refused');
    // And not the row's resting state, which is what "no control and no reason"
    // looks like on a Secret: a cell reading `held back` for a value the console
    // has already refused to hand over.
    expect(row.textContent).not.toContain('held back');
  });

  it('says why a reveal was refused, and keeps the value off the row', async () => {
    const container = await mount(
      {},
      {
        '/api/c/prod/env': {
          body: {
            state: 'forbidden',
            reason: { code: 'read_denied', message: 'secrets is forbidden for this account.' },
          },
        },
      },
    );
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').textContent).toContain('forbidden for this account');
    });
    expect(rowFor(container, 'DB_PASSWORD').querySelector('[data-revealed="true"]')).toBeNull();
  });
});

/**
 * Once shown, a value must not stay on screen until the drawer closes. The
 * gesture is one-per-key, so the way back has to exist - someone who checks a
 * password and then shares their screen needs a control to press.
 *
 * The switching cases render the panel directly and re-render it in place,
 * which is what the drawer does when it is re-targeted: `ObjectDetail` is the
 * same component at the same position, and a row that keeps its state keeps the
 * previous pod's credential.
 */
describe('a revealed value can be hidden again', () => {
  const hunter2 = {
    '/api/c/prod/env': {
      body: { state: 'ok', data: { key: 'password', value: 'hunter2', truncated: false } },
    },
  };

  it('takes the value off the row on Hide, and asks nothing', async () => {
    const container = await mount({}, hunter2);
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').textContent).toContain('hunter2');
    });

    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-hide]') as Element);

    const row = rowFor(container, 'DB_PASSWORD');
    expect(row.textContent).not.toContain('hunter2');
    expect(row.querySelector('[data-revealed="true"]')).toBeNull();
    expect(row.querySelector('[data-hide]')).toBeNull();
    // The door is offered again, and nothing was asked for by hiding: the audit
    // line was written on Decode and that is the record.
    expect(row.querySelector('[data-reveal]')).not.toBeNull();
    expect(asked.filter((path) => path.startsWith('/api/c/prod/env'))).toHaveLength(1);
  });

  it('clears the value when the row moves to another container', async () => {
    const { container, redraw } = await draw({ namespace: 'shop-prod', containers });
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').textContent).toContain('hunter2');
    });

    redraw({
      namespace: 'shop-prod',
      containers: containers.map((one) => (one.name === 'api' ? { ...one, name: 'api-canary' } : one)),
    });

    const row = rowFor(container, 'DB_PASSWORD');
    expect(row.closest('[data-container]')?.getAttribute('data-container')).toBe('api-canary');
    expect(row.textContent).not.toContain('hunter2');
    expect(row.querySelector('[data-revealed="true"]')).toBeNull();
  });

  it('clears the value when the same row is drawn for another pod', async () => {
    // The same container name, the same variable, another namespace: the drawer
    // re-targeted to a same-named pod elsewhere. No key changes, so the row is
    // the same instance - and the value it held belongs to the other pod.
    const { container, redraw } = await draw({ namespace: 'shop-prod', containers });
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(container, 'DB_PASSWORD').textContent).toContain('hunter2');
    });

    redraw({ namespace: 'shop-staging', containers });

    const row = rowFor(container, 'DB_PASSWORD');
    expect(row.textContent).not.toContain('hunter2');
    expect(row.querySelector('[data-revealed="true"]')).toBeNull();
    expect(row.querySelector('[data-reveal]')).not.toBeNull();
  });

  /** The panel on its own, with a client and nothing else, and a way to draw it
   *  again in place with other props - the same component at the same position,
   *  which is what a re-targeted drawer does. */
  async function draw(props: {
    namespace: string;
    containers: readonly PodContainer[];
  }): Promise<{ container: HTMLElement; redraw: (next: typeof props) => void }> {
    stubFetch(hunter2);
    asked = [];
    const client = createQueryClient();
    const panel = (at: typeof props): ReactElement => (
      <QueryClientProvider client={client}>
        <Containers cluster="prod" kind="pods" namespace={at.namespace} name="api-1" containers={at.containers} />
      </QueryClientProvider>
    );
    const view = render(panel(props));
    await waitFor(() => {
      expect(view.container.querySelector('[data-section="containers"]')).not.toBeNull();
    });
    return { container: view.container, redraw: (next) => view.rerender(panel(next)) };
  }
});

describe('the four absences', () => {
  it('draws them apart, and colors only the one a person can retry', async () => {
    const container = await mount();

    const unread = rowFor(container, 'MISSING').querySelector('[data-absence]');
    const unset = rowFor(container, 'NOT_SET').querySelector('[data-absence]');
    const expected = rowFor(container, 'OPTIONAL').querySelector('[data-absence]');

    expect(unread?.getAttribute('data-absence')).toBe('unread');
    expect(unset?.getAttribute('data-absence')).toBe('unset');
    expect(expected?.getAttribute('data-absence')).toBe('expected');

    // A read that failed is a warning; a key or an object that is not there is
    // drawn as a reference and says nothing about it.
    expect(unread?.className).toContain('text-warn');
    expect(unset?.className).not.toContain('text-warn');
    expect(expected?.className).not.toContain('text-warn');
    expect(unset?.textContent).toBe('—');
    expect(expected?.textContent).toBe('—');
  });

  it('keeps a control only on the absence where asking again could answer differently', async () => {
    const container = await mount();
    expect(rowFor(container, 'MISSING').querySelector('[data-reveal]')).not.toBeNull();
    expect(rowFor(container, 'NOT_SET').querySelector('[data-reveal]')).toBeNull();
    expect(rowFor(container, 'OPTIONAL').querySelector('[data-reveal]')).toBeNull();
  });
});

/** Words that say whether a reference resolves, or whether it may not. None is
 *  drawn anywhere on the panel, in its text or in a title. */
const EXISTENCE_WORDS = [
  'must exist',
  'may exist',
  'may be absent',
  'optional',
  'absent (optional)',
  'not set (optional)',
];

function saysNone(container: HTMLElement): void {
  // The variable names are the fixture's own and are not drawn by this panel's wording.
  const copy = container.cloneNode(true) as HTMLElement;
  copy.querySelectorAll('th').forEach((cell) => {
    cell.remove();
  });
  const text = copy.textContent.toLowerCase();
  const titles = [...container.querySelectorAll('[title]')]
    .map((node) => node.getAttribute('title') ?? '')
    .join(' | ')
    .toLowerCase();
  for (const word of EXISTENCE_WORDS) {
    expect(text, `text says "${word}"`).not.toContain(word);
    expect(titles, `a title says "${word}"`).not.toContain(word);
  }
}

describe('a reference to a ConfigMap or a Secret is a link, and says nothing about whether it exists', () => {
  const controller: PodContainer = {
    name: 'argocd-application-controller',
    image: 'quay.io/argoproj/argocd:v3.1.0',
    init: false,
    probes: [],
    status: { state: 'running', ready: true, restarts: 0 },
    env: [
      {
        name: 'ABSENT_OPTIONAL',
        problem: 'no such key, and the reference is optional',
        absence: 'expected',
        secret: false,
        held: false,
        clipped: false,
        source: { kind: 'ConfigMap', name: 'argocd-cmd-params-cm', key: 'controller.status', optional: true },
      },
      {
        name: 'ABSENT_REQUIRED',
        problem: 'no such key, and the reference is not optional',
        absence: 'unset',
        secret: false,
        held: false,
        clipped: false,
        source: { kind: 'ConfigMap', name: 'argocd-cmd-params-cm', key: 'repo.server', optional: false },
      },
      {
        name: 'PRESENT_OPTIONAL',
        held: true,
        clipped: false,
        secret: false,
        resolved: 'info',
        source: { kind: 'ConfigMap', name: 'argocd-cmd-params-cm', key: 'log.level', optional: true },
      },
      {
        name: 'TOKEN',
        secret: true,
        held: false,
        clipped: false,
        source: { kind: 'Secret', name: 'api', key: 'token', optional: true },
      },
    ],
    envFrom: [
      { kind: 'ConfigMap', name: 'absent', optional: true, problem: 'there is no ConfigMap absent here', absence: 'expected' },
      { kind: 'Secret', name: 'creds', prefix: 'DB_', keys: 2, optional: false },
    ],
  };

  it('links the object of an absent optional key, with the key, and no existence wording', async () => {
    const container = await mount({ containers: [controller] });
    const row = rowFor(container, 'ABSENT_OPTIONAL');

    const link = row.querySelector('[data-source] a');
    expect(link?.textContent).toBe('ConfigMap/argocd-cmd-params-cm');
    expect(link?.getAttribute('href')).toBe('/c/prod/configmaps?namespace=shop-prod&open=argocd-cmd-params-cm');
    expect(row.querySelector('[data-source-key]')?.textContent).toBe(':controller.status');
    expect(row.querySelector('[data-absence]')?.textContent).toBe('—');
    expect(row.textContent).not.toContain('no such key');
  });

  it('draws a required key that is missing the same way, and a present one as before', async () => {
    const container = await mount({ containers: [controller] });
    const missing = rowFor(container, 'ABSENT_REQUIRED');
    // The second row of a run names the object and links it, like the first.
    expect(missing.querySelector('[data-source] a')?.textContent).toBe('ConfigMap/argocd-cmd-params-cm');
    expect(missing.querySelector('[data-source] a')?.getAttribute('href')).toBe(
      '/c/prod/configmaps?namespace=shop-prod&open=argocd-cmd-params-cm',
    );
    expect(missing.querySelector('[data-absence]')?.textContent).toBe('—');
    expect(missing.querySelector('[data-absence]')?.className).not.toContain('text-warn');

    const present = rowFor(container, 'PRESENT_OPTIONAL');
    expect(present.querySelector('[data-held]')?.textContent).toBe('info');
    expect(present.querySelector('[data-source] a')).not.toBeNull();
  });

  it('links a Secret by name, with its key, and keeps the reveal control', async () => {
    const container = await mount({ containers: [controller] });
    const row = rowFor(container, 'TOKEN');
    const link = row.querySelector('[data-source] a');
    expect(link?.textContent).toBe('Secret/api');
    expect(link?.getAttribute('href')).toBe('/c/prod/secrets?namespace=shop-prod&open=api');
    expect(row.querySelector('[data-source-key]')?.textContent).toBe(':token');
    expect(row.querySelector('[data-reveal]')).not.toBeNull();
  });

  it('links each envFrom import, existing or not, and draws no sentence for the absent one', async () => {
    const container = await mount({ containers: [controller] });
    const lines = [...container.querySelectorAll('[data-imports] li')];
    expect(lines).toHaveLength(2);
    expect(lines[0]?.querySelector('a')?.getAttribute('href')).toBe(
      '/c/prod/configmaps?namespace=shop-prod&open=absent',
    );
    expect(lines[0]?.textContent).toBe('ConfigMap/absent');
    expect(lines[1]?.querySelector('a')?.getAttribute('href')).toBe(
      '/c/prod/secrets?namespace=shop-prod&open=creds',
    );
    expect(lines[1]?.textContent).toContain('as DB_*');
    expect(lines[1]?.textContent).toContain('2 keys');
  });

  it('says none of the existence words anywhere, in text or in a title', async () => {
    saysNone(await mount({ containers: [controller] }));
    saysNone(await mount());
  });

  it('leads to the object page when drawn on a page', () => {
    stubFetch({});
    const view = render(
      <QueryClientProvider client={createQueryClient()}>
        <RouterProvider
          router={createMemoryRouter([
            {
              path: '/',
              element: (
                <Containers
                  cluster="prod"
                  kind="pods"
                  namespace="shop-prod"
                  name="api-1"
                  containers={[controller]}
                  chrome="page"
                />
              ),
            },
          ])}
        />
      </QueryClientProvider>,
    );
    expect(view.container.querySelector('[data-source] a')?.getAttribute('href')).toBe(
      '/c/prod/configmaps/shop-prod/argocd-cmd-params-cm',
    );
  });

  it('offers a break after every separator in a name and a key, and adds no text', async () => {
    const container = await mount({ containers: [controller] });
    const row = rowFor(container, 'ABSENT_OPTIONAL');

    const name = row.querySelector('th');
    expect(name?.textContent).toBe('ABSENT_OPTIONAL');
    expect(name?.querySelectorAll('wbr')).toHaveLength(1);

    const key = row.querySelector('[data-source-key]');
    expect(key?.textContent).toBe(':controller.status');
    expect(key?.querySelectorAll('wbr')).toHaveLength(1);
  });

  it('sets the tables at the size of the resource lists', async () => {
    const container = await mount({ containers: [controller] });
    expect(container.querySelector('[data-env="environment"] table')?.className).toContain('text-sm');
    expect(container.querySelector('[data-env="environment"] table')?.className).not.toContain('text-xs');
    expect(container.querySelector('[data-imports] ul')?.className).toContain('text-sm');
    expect(container.querySelector('[data-source]')?.className).toContain('text-sm');
  });
});

describe('a missing object is drawn as its reference, and a refused read still warns', () => {
  const worker: PodContainer = {
    name: 'worker',
    image: 'shop/worker:2.0.0',
    init: false,
    probes: [],
    status: { state: 'running', ready: true, restarts: 0 },
    envFrom: [
      { kind: 'ConfigMap', name: 'absent', optional: true, problem: 'there is no ConfigMap absent here', absence: 'expected' },
      { kind: 'Secret', name: 'locked', optional: false, problem: 'this console is not allowed to read it', absence: 'unread' },
      { kind: 'ConfigMap', name: 'gone', optional: false, problem: 'there is no ConfigMap gone here', absence: 'expected' },
    ],
    env: [
      {
        name: 'ENDPOINT',
        problem: 'the read failed',
        absence: 'unread',
        secret: false,
        held: false,
        clipped: false,
        source: { kind: 'ConfigMap', name: 'gone', key: 'endpoint', optional: false },
      },
    ],
  };

  it('draws the absent import as a link alone, and the refused one with its sentence', async () => {
    const container = await mount({ containers: [worker] });
    const lines = [...container.querySelectorAll('[data-imports] li')];
    expect(lines[0]?.textContent).toBe('ConfigMap/absent');
    expect(lines[0]?.querySelector('[data-import-problem]')).toBeNull();
    expect(lines[1]?.querySelector('[data-import-problem]')?.className).toContain('text-warn');
  });

  it('draws a required import of a missing object as the link alone, with no warning', async () => {
    const container = await mount({ containers: [worker] });
    const line = [...container.querySelectorAll('[data-imports] li')][2];
    expect(line?.textContent).toBe('ConfigMap/gone');
    expect(line?.querySelector('a')?.getAttribute('href')).toBe(
      '/c/prod/configmaps?namespace=shop-prod&open=gone',
    );
    expect(line?.querySelector('[data-import-problem]')).toBeNull();
    expect(line?.querySelector('.text-warn')).toBeNull();
  });

  it('keeps the warning and the retry on a row whose read failed', async () => {
    const container = await mount({ containers: [worker] });
    const row = rowFor(container, 'ENDPOINT');
    expect(row.querySelector('[data-absence]')?.className).toContain('text-warn');
    expect(row.querySelector('[data-reveal]')).not.toBeNull();
    expect(row.querySelector('[data-source] a')?.textContent).toBe('ConfigMap/gone');
  });
});

/**
 * Whether the containers are healthy.
 *
 * Three answers a blank cell folds into one, and one test per branch, because
 * the failure this panel is guarding against is never "nothing is drawn" - it is
 * two different things drawn the same way.
 */
describe('whether a container is healthy', () => {
  it('names each declared probe, what it checks and the timings that decide behavior', async () => {
    const container = await mount();
    openProbes(container);
    const api = container.querySelector('[data-container="api"]');

    const liveness = api?.querySelector('[data-probe="liveness"]');
    expect(liveness?.getAttribute('data-probe-state')).toBe('declared');
    expect(liveness?.textContent).toContain('httpGet');
    expect(liveness?.textContent).toContain('/healthz:8080');

    // All four, not one standing for the rest: periodSeconds and
    // failureThreshold are between them how long a container has to recover,
    // and neither number means anything alone.
    const timings = [...(liveness?.querySelectorAll('[data-probe-timing]') ?? [])].map((part) => part.textContent);
    expect(timings).toEqual(['after 15s', 'every 10s', '1s timeout', '×3']);

    const readiness = api?.querySelector('[data-probe="readiness"]');
    expect(readiness?.textContent).toContain('tcpSocket');
    expect(readiness?.textContent).toContain(':8080');

    // Each timing says what it is on hover, since the chip draws only the figure.
    expect(liveness?.querySelector('[data-probe-timing]')?.getAttribute('title')).toBe('initial delay');
  });

  it('draws the startup probe beside the other two rather than folded into them', async () => {
    // It is the one that explains a container that never gets as far as failing
    // liveness: while it runs the kubelet holds the other two off entirely. A
    // panel that drew only liveness and readiness would be showing two probes
    // that are not being evaluated.
    const container = await mount();
    openProbes(container);
    const api = container.querySelector('[data-container="api"]');
    const drawn = [...(api?.querySelectorAll('[data-probe]') ?? [])];
    expect(drawn.map((node) => node.getAttribute('data-probe'))).toEqual([
      'liveness',
      'readiness',
      'startup',
    ]);
  });

  it('writes out the probes a container does not declare, because an empty cell is not that sentence', async () => {
    // "No readiness probe" is a finding - a Service can be sending traffic to a
    // container nothing has ever asked whether it is ready - and a row that is
    // simply missing says the panel did not look.
    const container = await mount();
    openProbes(container);

    const startup = container
      .querySelector('[data-container="api"]')
      ?.querySelector('[data-probe="startup"]');
    expect(startup?.getAttribute('data-probe-state')).toBe('absent');
    expect(startup?.querySelector('[data-probe-absent]')?.textContent).toBe('none');

    const init = container.querySelector('[data-container="wait-for-db"]');
    expect([...(init?.querySelectorAll('[data-probe-absent]') ?? [])]).toHaveLength(3);
    expect(init?.querySelector('[data-probe="readiness"]')?.textContent).toBe('readinessnone');

    // Stated, not colored. Most containers declare no readiness probe, so a
    // warning tone here would light up most panels and mean nothing.
    expect(startup?.querySelector('[data-probe-absent]')?.className).not.toContain('text-warn');
  });

  it('keeps "not declared" and "could not be read" apart, in the tone as well as the words', async () => {
    // The finest-grained instance of the empty-versus-failed rule on this
    // surface. `absent` is a fact about the pod; `unreadable` is a fact about
    // this console, and it is the one of the two that is a fault - drawing them
    // alike sends somebody to fix the wrong one.
    const container = await mount({ containers: unreadable });
    openProbes(container);
    const drawn = container.querySelector('[data-container="odd"]');

    const liveness = drawn?.querySelector('[data-probe="liveness"]');
    expect(liveness?.getAttribute('data-probe-state')).toBe('unreadable');
    const problem = liveness?.querySelector('[data-probe-problem]');
    expect(problem?.textContent).toContain('a shape this console cannot read');
    expect(liveness?.className).toContain('text-warn');

    // And on the same container, the other answer, so the pair is measured
    // rather than each half asserted against a fixture of its own.
    const readiness = drawn?.querySelector('[data-probe="readiness"]');
    expect(readiness?.getAttribute('data-probe-state')).toBe('absent');
    expect(readiness?.querySelector('[data-probe-problem]')).toBeNull();
    expect(readiness?.querySelector('[data-probe-absent]')).not.toBeNull();
  });

  it('names an unreadable probe under "unreadable" in the folded line, not under "none"', async () => {
    // The same pair on the closed section, which is how the tile first draws
    // A liveness probe the console could not read is not
    // one the pod leaves out, so the line must not fold the two into one word.
    const container = await mount({ containers: unreadable });
    const probes = container.querySelector('[data-container="odd"] [data-tile-section="probes"]');
    expect(probes?.querySelector('button[data-fold-control]')?.getAttribute('aria-expanded')).toBe('false');
    expect(probes?.querySelector('[data-fold-summary]')?.textContent).toBe(
      'unreadable: liveness · none: readiness, startup',
    );
  });

  it('carries the state, the reason and the restart count of a container that is failing', async () => {
    const container = await mount({ containers: unreadable });
    const drawn = container.querySelector('[data-container="odd"]');

    const status = drawn?.querySelector('[data-status]');
    expect(status?.getAttribute('data-status')).toBe('waiting');
    // The reason is the word a user searches for, so it is in the pill and
    // not only in the message.
    expect(status?.textContent).toBe('Waiting: CrashLoopBackOff');
    expect(drawn?.querySelector('[data-restarts]')?.textContent).toBe('340 restarts');
    expect(drawn?.querySelector('[data-ready]')?.getAttribute('data-ready')).toBe('false');
    // The message beside the reason rather than instead of it.
    expect(drawn?.querySelector('[data-status-message]')?.textContent).toContain('back-off 5m0s');

  });

  it('says only the state where the kubelet had no reason to give', async () => {
    // A running container carries no reason, which is the ordinary case and not
    // an absence: the pill reads `Running` and does not manufacture a colon and
    // an empty word after it.
    const container = await mount();
    const healthy = container
      .querySelector('[data-container="api"]')
      ?.querySelector('[data-status]');
    expect(healthy?.getAttribute('data-status')).toBe('running');
    expect(healthy?.textContent).toBe('Running');
    expect(healthy?.closest('[data-pill]')?.getAttribute('data-pill')).toBe('ok');
    expect(
      container.querySelector('[data-container="api"]')?.querySelector('[data-restarts]')
        ?.textContent,
    ).toBe('2 restarts');
  });

  it('never draws a container nobody reported as one that is not ready', async () => {
    // Every container of a Pending pod is in this state, and so is every
    // container of a pod that will never be scheduled. Nothing has run a
    // readiness check, so there is no readiness answer to draw - and `ready` in
    // the payload says nothing under `unreported`.
    const container = await mount();
    const init = container.querySelector('[data-container="wait-for-db"]');

    expect(init?.querySelector('[data-status]')?.getAttribute('data-status')).toBe('unreported');
    expect(init?.querySelector('[data-ready]')).toBeNull();
    // The pill says `Unreported`, and the server's reason is its title.
    expect(init?.querySelector('[data-pill]')?.getAttribute('title')).toContain(
      'reported nothing about this container',
    );
    // And no restart count, because zero and unknown are different answers: the
    // payload omits `restarts` when it is zero, so a client reading it without
    // the state would put "0 restarts" on a container nobody has spoken about.
    expect(init?.querySelector('[data-restarts]')).toBeNull();
  });
});

describe('a container that has stopped says how', () => {
  // A pod whose container was OOM-killed six times read
  // "TERMINATED: OOMKILLED, 6 restarts" with no exit code, though the payload
  // carries one: `OOMKilled` and a 137 are the same event said two ways.
  const stopped = (exitCode: number, reason: string): PodContainer[] => [
    {
      name: 'worker',
      image: 'shop/worker:2.0',
      init: false,
      status: { state: 'terminated', ready: false, reason, exitCode, restarts: 6 },
    },
  ];

  it('draws the exit code beside the reason, in the failing tone', async () => {
    const container = await mount({ containers: stopped(137, 'OOMKilled') });
    const drawn = container.querySelector('[data-container="worker"]');
    expect(drawn?.querySelector('[data-status]')?.textContent).toBe('Terminated: OOMKilled');
    const code = drawn?.querySelector('[data-exit-code]');
    expect(code?.textContent).toBe('exit code 137');
    expect(code?.className).toContain('text-bad');
  });

  it('draws no exit code on a container that is not terminated', async () => {
    const container = await mount({ containers: unreadable });
    expect(container.querySelector('[data-container="odd"] [data-exit-code]')).toBeNull();
  });

  /**
   * A Trivy container, `Terminated: Error`, `FallbackToLogsOnError`, must not
   * have its fifteen-line log tail pasted whole under the exit code. The fixture
   * is fabricated.
   */
  const TAIL = [
    ...Array.from({ length: 12 }, (_, at) => `2026-10-15T08:00:${String(at).padStart(2, '0')}Z\tINFO\t[vulndb] step ${String(at)}`),
    '\x1b[34mNotices:\x1b[0m',
    '[34mNotices:[0m 🎉',
    '2026-10-15T08:00:13Z\tFATAL\tFatal error\tinit error: DB error: failed to download vulnerability DB',
  ].join('\n');
  // `exitCode` is omitted from the payload when it is 0, as the server does:
  // `null` here stands for that absence.
  const failed = (message?: string, reason = 'Error', exitCode: number | null = 1): PodContainer[] => [
    {
      name: 'grafana-sc-datasources',
      image: 'mirror.gcr.io/aquasec/trivy:0.75.0',
      init: false,
      status: {
        state: 'terminated',
        ready: false,
        reason,
        ...(exitCode === null ? {} : { exitCode }),
        ...(message === undefined ? {} : { message }),
      },
    },
  ];

  it('folds its termination message behind "show error", with only the line that failed beside it', async () => {
    const container = await mount({ containers: failed(TAIL) });
    const drawn = container.querySelector('[data-container="grafana-sc-datasources"]');
    // Nothing of the tail is drawn as a sentence any more.
    expect(drawn?.querySelector('[data-status-message]')).toBeNull();
    const details = drawn?.querySelector('details[data-folded-message][data-message-of="state"]');
    expect(details).not.toBeNull();
    expect(details?.hasAttribute('open')).toBe(false);
    expect(details?.querySelector('[data-folded-message-control]')?.textContent).toContain('show error');
    expect(details?.querySelector('[data-folded-message-line]')?.textContent).toContain('FATAL');
    // The facts beside it are unchanged.
    expect(drawn?.querySelector('[data-status]')?.textContent).toBe('Terminated: Error');
    expect(drawn?.querySelector('[data-exit-code]')?.textContent).toBe('exit code 1');
  });

  it('opens it on a click, cleaned of every escape, with its newlines', async () => {
    const container = await mount({ containers: failed(TAIL) });
    const details = container.querySelector('details[data-message-of="state"]');
    const summary = details?.querySelector('summary');
    if (summary === null || summary === undefined) throw new Error('no control');
    fireEvent.click(summary);
    expect(details?.hasAttribute('open')).toBe(true);
    const body = details?.querySelector('[data-folded-message-body]')?.textContent ?? '';
    expect(body.split('\n')).toHaveLength(15);
    expect(body).not.toContain('\x1b');
    expect(body).not.toContain('[34m');
    expect(body).not.toContain('[0m');
    expect(body).toContain('Notices: 🎉');
  });

  it('labels it by the exit code: a run that exited 0 left a message, any other an error', async () => {
    const control = '[data-message-of="state"] [data-folded-message-control]';
    // Exit 0 is omitted from the payload, whatever the reason says.
    let container = await mount({ containers: failed('done\nall good', 'Completed', null) });
    expect(container.querySelector(control)?.textContent).toContain('show message');
    container = await mount({ containers: failed('done\nall good', 'Error', null) });
    expect(container.querySelector(control)?.textContent).toContain('show message');
    // A non-zero code is an error even under a reason that reads clean.
    container = await mount({ containers: failed('half done\nkilled', 'Completed', 143) });
    expect(container.querySelector(control)?.textContent).toContain('show error');
  });

  it('draws no control at all where the kubelet wrote no message', async () => {
    const container = await mount({ containers: failed() });
    expect(container.querySelector('[data-container="grafana-sc-datasources"] [data-folded-message]')).toBeNull();
    expect(container.querySelector('[data-container="grafana-sc-datasources"] summary')).toBeNull();
  });
});

/**
 * A container that was OOM-killed and restarted is running again, and its panel
 * has to say why it restarted, which is in `lastState` rather than `state`.
 */
describe('a restarted container says how its last run ended', () => {
  const finished = new Date(Date.now() - 3 * 86_400_000);
  const started = new Date(finished.getTime() - 41 * 60_000);
  const restarted = (lastTermination?: NonNullable<PodContainer['status']>['lastTermination']): PodContainer[] => [
    {
      name: 'worker',
      image: 'shop/worker:2.0',
      init: false,
      status: {
        state: 'running',
        ready: true,
        restarts: 4,
        ...(lastTermination === undefined ? {} : { lastTermination }),
      },
    },
  ];

  it('draws the exit code and its meaning, OOMKilled highlighted, when it ended and how long it ran', async () => {
    const container = await mount({
      containers: restarted({
        exitCode: 137,
        reason: 'OOMKilled',
        startedAt: started.toISOString(),
        finishedAt: finished.toISOString(),
      }),
    });
    const drawn = container.querySelector('[data-container="worker"]');
    // The current state is unchanged: running, with the restart count beside it.
    expect(drawn?.querySelector('[data-status]')?.textContent).toBe('Running');
    expect(drawn?.querySelector('[data-restarts]')?.textContent).toBe('4 restarts');

    const last = drawn?.querySelector('[data-last-termination]');
    expect(last).not.toBeNull();
    expect(last?.querySelector('[data-last-exit-code]')?.textContent).toBe('exit code 137 (SIGKILL, often OOM)');
    const reason = last?.querySelector('[data-last-reason]');
    expect(reason?.textContent).toBe('OOMKilled');
    expect(reason?.className).toContain('text-bad');
    // Relative, with the absolute instant on hover however old it is.
    const ended = last?.querySelector('[data-last-ended]');
    expect(ended?.textContent).toBe('ended 3d ago');
    expect(ended?.getAttribute('title')).toContain(String(finished.getFullYear()));
    expect(last?.textContent).toContain('after running 41m');
  });

  it('names a clean exit as one, without the failing tone', async () => {
    const container = await mount({ containers: restarted({ exitCode: 0, reason: 'Completed' }) });
    const code = container.querySelector('[data-last-exit-code]');
    expect(code?.textContent).toBe('exit code 0 (clean exit)');
    expect(code?.className).not.toContain('text-bad');
    expect(container.querySelector('[data-last-reason]')?.className).not.toContain('text-bad');
  });

  it('labels a last run by the same rule: exit 0 left a message, any other an error', async () => {
    const control = '[data-message-of="last"] [data-folded-message-control]';
    let container = await mount({ containers: restarted({ exitCode: 0, reason: 'Completed', message: 'done\nbye' }) });
    expect(container.querySelector(control)?.textContent).toContain('show message');
    expect(container.querySelector(control)?.textContent).not.toContain('show error');
    container = await mount({ containers: restarted({ exitCode: 3, reason: 'Error', message: 'boom\nbye' }) });
    expect(container.querySelector(control)?.textContent).toContain('show error');
  });

  it.each([
    [143, 'exit code 143 (SIGTERM)'],
    [1, 'exit code 1 (application error)'],
    [139, 'exit code 139 (signal 11)'],
    [3, 'exit code 3'],
  ])('names exit code %i by what it means, where it means something', async (exitCode, drawn) => {
    const container = await mount({ containers: restarted({ exitCode, reason: 'Error' }) });
    const code = container.querySelector('[data-last-exit-code]');
    expect(code?.textContent).toBe(drawn);
    expect(code?.className).toContain('text-bad');
    // Only OOMKilled is boxed: an ordinary Error is the muted word.
    expect(container.querySelector('[data-last-reason]')?.className).not.toContain('text-bad');
  });

  it('draws the line for an init container as well - a restartable sidecar restarts like any other', async () => {
    const container = await mount({
      containers: [
        {
          name: 'proxy',
          image: 'shop/proxy:1.0',
          init: true,
          status: { state: 'running', ready: true, restarts: 1, lastTermination: { exitCode: 143, reason: 'Error' } },
        },
      ],
    });
    const drawn = container.querySelector('[data-container="proxy"]');
    expect(drawn?.getAttribute('data-init')).toBe('true');
    expect(drawn?.querySelector('[data-restarts]')?.textContent).toBe('1 restart');
    expect(drawn?.querySelector('[data-last-exit-code]')?.textContent).toBe('exit code 143 (SIGTERM)');
  });

  it('draws the signal where the runtime reported one, and nothing where it did not', async () => {
    let container = await mount({ containers: restarted({ exitCode: 137, signal: 9, reason: 'Error' }) });
    expect(container.querySelector('[data-last-signal]')?.textContent).toBe('signal 9');
    container = await mount({ containers: restarted({ exitCode: 137, reason: 'Error' }) });
    expect(container.querySelector('[data-last-signal]')).toBeNull();
  });

  it('folds the message behind "show error" with its telling line, then shows all of it with its newlines, wrapped and bounded', async () => {
    const message = '{"level":"error","msg":"dial tcp"}\npanic: connection refused\n\tat main.go:42';
    const container = await mount({ containers: restarted({ exitCode: 2, reason: 'Error', message }) });
    const details = container.querySelector('[data-last-termination] details[data-folded-message][data-message-of="last"]');
    expect(details).not.toBeNull();
    expect(details?.hasAttribute('open')).toBe(false);
    expect(details?.querySelector('[data-folded-message-control]')?.textContent).toContain('show error');
    // The last line that says it failed, not the oldest line of the tail.
    const line = details?.querySelector('[data-folded-message-line]');
    expect(line?.textContent).toBe('panic: connection refused');
    expect(line?.className).toContain('truncate');
    const body = details?.querySelector('[data-folded-message-body]');
    expect(body?.textContent).toBe(message);
    // Newlines kept, broken anywhere, and a height past which it scrolls.
    expect(body?.className).toContain('whitespace-pre-wrap');
    expect(body?.className).toContain('wrap-anywhere');
    expect(body?.className).toContain('max-h-48');
  });

  it('draws no line for a container the kubelet keeps no previous run for - not "never"', async () => {
    const container = await mount({ containers: restarted() });
    expect(container.querySelector('[data-last-termination]')).toBeNull();
    expect(container.textContent).not.toMatch(/never/i);
  });
});

describe('where a value is read out of', () => {
  it('names the object behind a row and the key in that object, and nothing where there is no source', async () => {
    // `EnvSource` is drawn: a user looking at `DB_PASSWORD` has to be able to
    // tell a literal from a Secret key from a ConfigMap key, and those are
    // three different things to go and change.
    const container = await mount();

    const secret = rowFor(container, 'DB_PASSWORD').querySelector('[data-source]');
    expect(secret?.getAttribute('data-source')).toBe('Secret/db-credentials:password');
    expect(secret?.querySelector('[data-source-object]')?.textContent).toBe('Secret/db-credentials');
    // The key **in the source object**, which the contract warns is not the
    // variable's name as the container sees it.
    expect(secret?.querySelector('[data-source-key]')?.textContent).toBe(':password');

    const configmap = rowFor(container, 'NGINX_CONF').querySelector('[data-source]');
    expect(configmap?.getAttribute('data-source')).toBe('ConfigMap/nginx:nginx.conf');

    // A literal is in the pod spec and comes out of no object; a field reference
    // names what decides the value instead. Neither has a source and neither
    // gets an empty one, which would be a fourth thing to read.
    expect(rowFor(container, 'LOG_LEVEL').querySelector('[data-source]')).toBeNull();
    expect(rowFor(container, 'NODE_NAME').querySelector('[data-source]')).toBeNull();
  });

  it('names and links the object on every row, including a second row reading the same one', async () => {
    // One object read by adjacent rows is named on each of them: nothing is
    // elided to "same object".
    const container = await mount();
    const env = container.querySelector('[data-container="api"] [data-env="environment"]');

    // OPTIONAL and NOT_SET both come out of `ConfigMap/params` and are adjacent.
    for (const [name, key] of [
      ['OPTIONAL', ':tuning'],
      ['NOT_SET', ':required'],
    ] as const) {
      const source = rowFor(container, name).querySelector('[data-source]');
      expect(source?.querySelector('[data-source-object]')?.textContent).toBe('ConfigMap/params');
      const link = source?.querySelector('a');
      expect(link?.textContent).toBe('ConfigMap/params');
      expect(link?.getAttribute('href')).toBe('/c/prod/configmaps?namespace=shop-prod&open=params');
      expect(source?.querySelector('[data-source-key]')?.textContent).toBe(key);
      expect(source?.getAttribute('data-source')).toBe(`ConfigMap/params${key}`);
    }
    expect(env?.textContent).not.toContain('same');
    expect(env?.querySelectorAll('[data-source] a')).toHaveLength(5);
  });

  it('does not say on a row whether the reference may be absent', async () => {
    // Required and optional references are drawn alike: the object, the key,
    // and nothing about what an absence would mean.
    const container = await mount();
    expect(container.querySelector('[data-source-optional]')).toBeNull();
    saysNone(container);
  });

  it('draws the source plainly, because a reference that is working is the normal case', async () => {
    // The same lesson the four absences carry: a color spent on the normal case
    // is a color the user stops seeing. Many sourced rows are references that
    // must exist and do; the fault when the key really
    // is gone is `unset`, which is already a warning on the value beside it.
    const container = await mount();
    for (const name of ['DB_PASSWORD', 'NGINX_CONF', 'OPTIONAL', 'NOT_SET']) {
      const source = rowFor(container, name).querySelector('[data-source]');
      expect(source?.className).toContain('text-muted');
      expect(source?.className).not.toContain('warn');
      expect(source?.className).not.toContain('text-bad');
    }
  });

  it('keeps the source on the row once the value has been revealed', async () => {
    // The source is beside the variable and not inside the value, so the branch
    // that swaps a control for a credential cannot take it away - which is when
    // the user is most likely to want to know which object they are looking
    // at the inside of.
    const container = await mount(
      {},
      {
        '/api/c/prod/env': {
          body: { state: 'ok', data: { key: 'password', value: 'hunter2', truncated: false } },
        },
      },
    );
    fireEvent.click(rowFor(container, 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(
        rowFor(container, 'DB_PASSWORD').querySelector('[data-revealed="true"]'),
      ).not.toBeNull();
    });
    expect(
      rowFor(container, 'DB_PASSWORD').querySelector('[data-source]')?.getAttribute('data-source'),
    ).toBe('Secret/db-credentials:password');
  });

  it('names the key in the source on an imported row, where the variable carries the prefix', async () => {
    // The case the contract warns about in as many words: `APP_TIMEOUT` is what
    // the container sees and `TIMEOUT` is what is in the ConfigMap, and an
    // user going to change it needs the second.
    const container = await mount();
    const source = rowFor(container, 'APP_TIMEOUT').querySelector('[data-source]');
    expect(source?.getAttribute('data-source')).toBe('ConfigMap/app-settings:TIMEOUT');
    expect(source?.querySelector('[data-source-key]')?.textContent).toBe(':TIMEOUT');
  });
});

/** A container carrying the two answers the base fixture cannot: a declaration
 *  this console could not read, beside one that is genuinely not there, and a
 *  reported state that is a failure. */
const unreadable: PodContainer[] = [
  {
    name: 'odd',
    image: 'shop/api:1.4.2',
    init: false,
    probes: [
      {
        kind: 'liveness',
        state: 'unreadable',
        problem: 'the pod carries a liveness probe in a shape this console cannot read',
      },
      { kind: 'readiness', state: 'absent' },
      { kind: 'startup', state: 'absent' },
    ],
    status: {
      state: 'waiting',
      ready: false,
      reason: 'CrashLoopBackOff',
      message: 'back-off 5m0s restarting failed container odd',
      restarts: 340,
    },
  },
];

/** The `<tr>` for one variable, as the user sees it. */
function rowFor(container: HTMLElement, name: string): HTMLElement {
  const head = [...container.querySelectorAll('[data-env] th')].find(
    (cell) => cell.textContent === name,
  );
  const row = head?.closest('tr');
  if (row === null || row === undefined) throw new Error(`no row for ${name}`);
  return row as HTMLElement;
}

/**
 * A long list folded behind its count.
 *
 * A pod whose variables push the rest of the panel off the screen is the case.
 * The fixture is the shape that does it - one container with
 * more variables than `ENV_FOLD_AFTER`, a required key missing among them, a
 * Secret-backed one to reveal - beside the base fixture's `api`, whose seven
 * rows are the short list that must stay as it was.
 */
describe('a long list is folded behind its count', () => {
  const literal = (name: string): EnvVar => ({
    name,
    value: 'on',
    secret: false,
    held: false,
    clipped: false,
  });

  const long: PodContainer[] = [
    ...containers,
    {
      name: 'worker',
      image: 'shop/worker:2.0.0',
      init: false,
      env: [
        ...Array.from({ length: ENV_FOLD_AFTER + 2 }, (_, at) => literal(`FLAG_${String(at)}`)),
        {
          name: 'DB_PASSWORD',
          secret: true,
          source: { kind: 'Secret', name: 'db-credentials', key: 'password', optional: false },
          held: false,
          clipped: false,
        },
        {
          name: 'UNREAD',
          problem: 'the ConfigMap was not read',
          absence: 'unread',
          secret: false,
          held: false,
          clipped: false,
          source: { kind: 'ConfigMap', name: 'params', key: 'required', optional: false },
        },
      ],
      envFrom: Array.from({ length: ENV_FOLD_AFTER + 1 }, (_, at) => ({
        kind: 'ConfigMap',
        name: `settings-${String(at)}`,
        keys: 1,
        optional: false,
      })),
    },
  ];

  const hunter2 = {
    '/api/c/prod/env': {
      body: { state: 'ok', data: { key: 'password', value: 'hunter2', truncated: false } },
    },
  };

  /** One card, by its container's name. */
  const card = (root: HTMLElement, name: string): HTMLElement => {
    const found = root.querySelector(`[data-container="${name}"]`);
    if (found === null) throw new Error(`no container ${name}`);
    return found as HTMLElement;
  };

  /** The fold control of one list on one card, or `null` where it is drawn open. */
  const control = (root: HTMLElement, name: string, list: string): HTMLButtonElement | null =>
    card(root, name).querySelector(`[data-${list}] [data-fold-control]`);

  it('folds a long list behind a header that says how many rows it holds', async () => {
    const root = await mount({ containers: long });
    const button = control(root, 'worker', 'env="environment"');

    expect(button).not.toBeNull();
    expect(button?.tagName).toBe('BUTTON');
    expect(button?.getAttribute('type')).toBe('button');
    expect(button?.getAttribute('aria-expanded')).toBe('false');
    expect(button?.textContent).toBe(`Environment · ${String(ENV_FOLD_AFTER + 4)}`);
    // `aria-controls` names an element the page holds, even while it is empty.
    const body = root.querySelector(`[id="${button?.getAttribute('aria-controls') ?? ''}"]`);
    expect(body).not.toBeNull();
    expect(body?.querySelector('tr')).toBeNull();
    expect(card(root, 'worker').querySelectorAll('[data-env="environment"] tr')).toHaveLength(0);
  });

  it('says the warning its folded rows carry, and only while they are folded', async () => {
    const root = await mount({ containers: long });
    const faults = (): Element | null =>
      card(root, 'worker').querySelector('[data-env="environment"] [data-fold-faults]');

    expect(faults()?.textContent).toBe('1 not read');
    expect(faults()?.className).toContain('text-warn');

    fireEvent.click(control(root, 'worker', 'env="environment"') as Element);
    // Open, the row says it itself.
    expect(faults()).toBeNull();
    expect(rowFor(card(root, 'worker'), 'UNREAD').querySelector('[data-absence="unread"]')).not.toBeNull();
  });

  it('folds the import list the same way', async () => {
    const root = await mount({ containers: long });
    const button = control(root, 'worker', 'imports');

    expect(button?.textContent).toBe(`Imported from · ${String(ENV_FOLD_AFTER + 1)}`);
    expect(card(root, 'worker').querySelectorAll('[data-imports] li')).toHaveLength(0);

    fireEvent.click(button as Element);
    expect(card(root, 'worker').querySelectorAll('[data-imports] li')).toHaveLength(ENV_FOLD_AFTER + 1);
  });

  it('leaves a short list open, under the heading it always had', async () => {
    const root = await mount({ containers: long });
    const api = card(root, 'api');

    // Seven rows and two imports: nothing to fold, and no control that would
    // do nothing.
    expect(api.querySelector('[data-env] [data-fold-control], [data-imports] [data-fold-control]')).toBeNull();
    expect(api.querySelectorAll('[data-env="environment"] tbody tr')).toHaveLength(7);
    expect(api.querySelector('[data-env="environment"] p')?.textContent).toBe('Environment');
    expect(api.querySelectorAll('[data-imports] li')).toHaveLength(2);
  });

  it('opens on one click and closes on the next', async () => {
    const root = await mount({ containers: long });
    const button = control(root, 'worker', 'env="environment"') as HTMLButtonElement;

    fireEvent.click(button);
    expect(button.getAttribute('aria-expanded')).toBe('true');
    expect(card(root, 'worker').querySelectorAll('[data-env="environment"] tbody tr')).toHaveLength(
      ENV_FOLD_AFTER + 4,
    );
    // Its own list and no other: the imports beside it stay folded.
    expect(control(root, 'worker', 'imports')?.getAttribute('aria-expanded')).toBe('false');

    fireEvent.click(button);
    expect(button.getAttribute('aria-expanded')).toBe('false');
    expect(card(root, 'worker').querySelectorAll('[data-env="environment"] tr')).toHaveLength(0);
  });

  it('stays open across a move to another tab and back', async () => {
    // Activity draws the events inline, so the move asks for nothing; what matters is that
    // the details panel, and every card in it, is unmounted by it.
    const root = await mount({ containers: long, tabs: [{ key: 'events', label: 'Events', lazy: false }] });
    fireEvent.click(control(root, 'worker', 'env="environment"') as Element);

    fireEvent.click(root.querySelector('#object-tab-activity') as Element);
    await waitFor(() => {
      expect(root.querySelector('[data-section="containers"]')).toBeNull();
    });

    fireEvent.click(root.querySelector('#object-tab-details') as Element);
    await waitFor(() => {
      expect(root.querySelector('[data-section="containers"]')).not.toBeNull();
    });
    expect(control(root, 'worker', 'env="environment"')?.getAttribute('aria-expanded')).toBe('true');
    expect(card(root, 'worker').querySelectorAll('[data-env="environment"] tbody tr')).toHaveLength(
      ENV_FOLD_AFTER + 4,
    );
    // And only what was opened: the import list was never touched.
    expect(control(root, 'worker', 'imports')?.getAttribute('aria-expanded')).toBe('false');
  });

  it('stays open across a re-render, and does not open another pod’s list', async () => {
    const { container: root, redraw } = await draw('shop-prod');
    fireEvent.click(control(root, 'worker', 'env="environment"') as Element);

    redraw('shop-prod');
    expect(control(root, 'worker', 'env="environment"')?.getAttribute('aria-expanded')).toBe('true');

    // The same component at the same position, drawn for a same-named pod in
    // another namespace: a re-targeted drawer. Its list is its own, and folded.
    redraw('shop-staging');
    expect(control(root, 'worker', 'env="environment"')?.getAttribute('aria-expanded')).toBe('false');

    redraw('shop-prod');
    expect(control(root, 'worker', 'env="environment"')?.getAttribute('aria-expanded')).toBe('true');
  });

  it('reveals a Secret value inside the fold, and folding takes it off the page', async () => {
    const root = await mount({ containers: long }, hunter2);
    const button = control(root, 'worker', 'env="environment"') as HTMLButtonElement;
    fireEvent.click(button);

    fireEvent.click(rowFor(card(root, 'worker'), 'DB_PASSWORD').querySelector('[data-reveal]') as Element);
    await waitFor(() => {
      expect(rowFor(card(root, 'worker'), 'DB_PASSWORD').textContent).toContain('hunter2');
    });

    // Closed, the rows are not in the page at all, the credential with them.
    fireEvent.click(button);
    expect(root.textContent).not.toContain('hunter2');

    // Opened again, the row offers the control, and pressing it is a new reveal.
    fireEvent.click(button);
    const row = rowFor(card(root, 'worker'), 'DB_PASSWORD');
    expect(row.textContent).not.toContain('hunter2');
    expect(row.querySelector('[data-reveal]')).not.toBeNull();
    expect(asked.filter((path) => path.startsWith('/api/c/prod/env'))).toHaveLength(1);
  });

  /** The panel alone, drawn again in place for a pod in `namespace`. */
  async function draw(
    namespace: string,
  ): Promise<{ container: HTMLElement; redraw: (next: string) => void }> {
    stubFetch({});
    const client = createQueryClient();
    const panel = (at: string): ReactElement => (
      <QueryClientProvider client={client}>
        <Containers cluster="prod" kind="pods" namespace={at} name="api-1" containers={long} />
      </QueryClientProvider>
    );
    const view = render(panel(namespace));
    await waitFor(() => {
      expect(view.container.querySelector('[data-section="containers"]')).not.toBeNull();
    });
    return { container: view.container, redraw: (next) => view.rerender(panel(next)) };
  }
});

/**
 * A pod's debug containers (#147): after the pod's own, marked as what they
 * are, and said to stay - on each tile and at the heading.
 */
describe('ephemeral containers', () => {
  const running = { state: 'running', ready: true, restarts: 0 } as NonNullable<PodContainer['status']>;

  function draw(list: PodContainer[]): HTMLElement {
    const view = render(
      <QueryClientProvider client={createQueryClient()}>
        <Containers cluster="prod" kind="pods" namespace="pay" name="api-0" containers={list} />
      </QueryClientProvider>,
    );
    return view.container;
  }

  it('draws them after the others, with their target and the kept badge, and counts them at the heading', () => {
    const container = draw([
      { name: 'kt-debug-9q4mz', image: 'busybox:1.36', init: false, ephemeral: true, target: 'api', status: running },
      { name: 'api', image: 'gcr.io/distroless/static', init: false, status: running },
      { name: 'kt-debug-x1y2z', image: 'nicolaka/netshoot:v0.13', init: false, ephemeral: true, status: running },
    ]);
    const tiles = [...container.querySelectorAll('article[data-container]')];
    expect(tiles.map((one) => one.getAttribute('data-container'))).toEqual(['api', 'kt-debug-9q4mz', 'kt-debug-x1y2z']);
    expect(tiles[0]?.querySelector('[data-ephemeral-badges]')).toBeNull();

    const targeted = tiles[1]?.querySelector('[data-ephemeral-badges]');
    expect(targeted?.textContent).toContain('debug → api');
    expect(targeted?.querySelector('[data-tone="info"] use')?.getAttribute('href')).toBe('#i-debug');
    expect(targeted?.querySelector('[data-kept]')?.textContent).toBe('stays in pod');
    expect(targeted?.querySelector('[data-kept] [title]')?.getAttribute('title')).toContain('until the pod is deleted');

    const free = tiles[2]?.querySelector('[data-ephemeral-badges] [data-tone="info"]');
    expect(free?.textContent).toBe('debug');

    expect(container.querySelector('[data-ephemeral-count]')?.textContent).toBe('2 debug');
  });

  it('says nothing at the heading of a pod that has none', () => {
    const container = draw([{ name: 'api', image: 'shop/api:1', init: false, status: running }]);
    expect(container.querySelector('[data-ephemeral-count]')).toBeNull();
  });
});
