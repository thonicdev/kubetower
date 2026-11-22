import { type Page, type Request, type Route } from '@playwright/test';
/**
 * Geometry assertions, one per defect class, measured in a real engine
 * against the real compiled stylesheet.
 *
 * ## Why numbers and not screenshots
 *
 * No golden images anywhere here, deliberately. A reference set breaks on a font
 * update, a renderer version or a runner's DPI, it says "something moved" and
 * never which thing, and a check that cries wolf is switched off — at which
 * point the repository has a disabled check and believes it has a passing one.
 * **Every assertion below is a number with a tolerance, and every failure
 * message names the number that moved and what it was supposed to be.**
 *
 * ## What each one guards
 *
 * | | The number |
 * |---|---|
 * | 1 | every column is drawn at the same multiple of what it declares, and a sized table fills its panel at two panel widths |
 * | 2 | a gauge track and its percentage share a line inside their own cell, the track nearer its own figure than the previous column's digits, and as long beside `6%` as beside `58%` |
 * | 3 | the cluster rail is 56px at 1280px and at 500px, with no fold control, and stays 56px after a reload |
 * | 4 | the kinds column is 192px shown and takes no width hidden, hidden survives a reload, and 36 rows scroll inside the column and not the page at 900px high |
 * | 5 | `scrollWidth == clientWidth` at 420px, and no document horizontal scroll |
 * | 6 | Logs is at the same x on a Pod and on a Namespace, and every verb is past the spacer |
 * | 7 | every tile and every foot control fits the 56px rail at 1280px and at 500px, and nothing scrolls sideways |
 * | 8 | a drag moves a boundary: the columns after it absorb it, the total is unchanged, and a reload keeps the shares |
 * | 9 | every control Tab reaches in a confirmation, the menu, a list's cells and the dashboard has its whole ring inside every box that clips it |
 * | — | the overview's trouble, warning-event and restarting tables scroll sideways nowhere at 1024 to 1920, the column that takes the rest keeps a quarter of the panel, every cut name is whole on hover and whole from 1280 (unnumbered) |
 * | 10 | the environment table's name column stays inside `w-48`, its value column keeps at least 320px, no name or key is broken inside a word, and an absent optional key is one short line with its address in at most two |
 * | 11 | with eight clusters including an 89-character ARN and `kubernetes-admin@kubernetes`, every drawn cluster name is one line, inside its box, whole on the tooltip a hover over it shows, and the page never scrolls sideways - on the overview, the pods list and the picker, at 1280px and at 500px |
 * | 12 | Arrange, opened on eight clusters with the 89-character ARN, has no horizontal scroll at 1280px or at 500px; the sixteen swatches are inline at 1280px and one color button at 500px |
 * | 13 | the dock's log toolbar is one row at 1024 and 1280, the filter keeps its room, the ⋯ stays at the right edge when the bar wraps, and its menu is inside the window |
 * | 14 | on a cluster with no metrics backend, nothing in a capacity tile starts before or ends past the tile, at 1280px |
 * | 15 | a last termination's message - a 176-character JSON line, a 4 KB tail with an unbroken token - scrolls the pod panel sideways nowhere at 1024 and 1280, closed is one line, opened is at most 192px tall; a terminated container's fifteen-line message costs its card one line closed, opens from the keyboard, and draws no escape |
 * | 16 | with the nameplate POST hung: a color picked in the tile's menu keeps the keyboard inside the menu through Tab and Shift+Tab, Reset says pending, and Escape still closes it; Save in the Customize dialog keeps the keyboard and says pending, and Escape still closes the dialog |
 * | 17 | the Pods list with filter chips, a `⋯` per row and its menus open scrolls sideways nowhere at 1024 and 1280, each `⋯` is inside its cell, and each menu is inside the window; the `⋯` shows on hover without moving anything, the menu's entries and its one red verb, a right-click opens at the pointer and the `⋯`, Shift+F10 and the menu key under the `⋯`, the browser's menu is never drawn over it, and a link in a row keeps its own |
 * | 18 | on the whole console at 1280 and 500, Kinds is inside the 56px rail, below Arrange and above Log out, and the top bar carries none, its breadcrumb starting at its padding |
 * | 19 | at 1024 and 1280 with a drawer open every toolbar control is whole, left of the drawer and found at its center; a table wider than its panel fades at the cut and only while there is more; a cut heading or cell offers its whole text on hover and a whole one nothing, and no pill is cut |
 * | 20 | not geometry, but the one browser behavior jsdom cannot perform: a selection dragged over a following log pane, with more lines arriving than the pane draws, is what the platform's copy shortcut copies, and the pane follows again once it is let go |
 * | 21 | with a pod taller than the window open in the drawer, and as a page, over a forty-row list: the document never scrolls, and the only elements that do are the drawer's or page's tab panel and the list's own panel - at 1024, 1280, 1440 and 1920 wide and 600 to 1080 high |
 * | 22 | a metrics source with nothing to say draws nothing: the quiet overview has no *Over time*, no *Most used* and no all-well pill at 1440, and still ends its head on the Capacity rings |
 * | 23 | the drawer's inner edge resizes it: a drag at 1440 widens it by what was dragged, never past what leaves the list 305px, with the toolbar whole and the document scrolling nowhere, the width survives a reload, a narrower window takes a widened drawer back to the default before the list loses anything; it narrows a pod's, a service's and a secret's drawer to the measured floor (444px) or what the content needs if more, where header, controls and tabs still fit and a pixel less wraps a pod's controls; a width stored under the floor is drawn at it after a reload, at 1440 and beside the 1024px list; the object page draws no handle |
 * | 24 | a drawer's fact labels, for a Pod, a Job, a CronJob, a Deployment and a Terminating Namespace at 390, 1024, 1280 and 1440, are one line each, whole unless longer than the 40% cap and then whole in their title, and nothing in the table is drawn past the drawer's edge; a label the cap cut, and only such a label, is a button that opens it whole in place from the keyboard, with nothing past the edge, and closes it again |
 * | 25 | the list's "+" is wholly inside the list's panel, in its bottom-right corner and inside the window, at 390 and 1440; with a row ticked it is not drawn, so the selection bar has the corner alone, and Clear brings it back |
 *
 * `measure/main.tsx` holds what this cannot catch. Read it before adding another.
 */

/* -------------------------------------------------------------------------- */
/* The cluster, answered from here                                             */
/* -------------------------------------------------------------------------- */

export const CAPABILITIES = { podExec: true, nodeShell: true, assistant: true };

export const CLUSTERS = {
  state: 'ok',
  items: [
    { name: 'demo', label: 'Demo', initials: 'DM', colour: 'blue', state: 'ok', order: 0 },
    { name: 'staging', label: 'Staging', initials: 'ST', colour: 'green-pale', state: 'ok', order: 1 },
  ],
};

export const NAV = {
  state: 'ok',
  data: {
    groups: [
      {
        key: 'workloads',
        label: 'Workloads',
        deferred: false,
        items: [
          { text: 'Pods', href: '/c/demo/pods' },
          { text: 'Deployments', href: '/c/demo/deployments' },
          { text: 'Cron jobs', href: '/c/demo/cronjobs' },
        ],
      },
      {
        key: 'network',
        label: 'Network',
        deferred: false,
        items: [
          { text: 'Services', href: '/c/demo/services' },
          { text: 'Ingresses', href: '/c/demo/ingresses' },
        ],
      },
    ],
  },
};

export const POD_ACTIONS = [
  { verb: 'delete', label: 'Delete', permitted: true, destructive: true },
  { verb: 'restart', label: 'Restart', permitted: true, destructive: true },
];

export function detailPage(kind: string, name: string, namespace: string | undefined, title: string) {
  return {
    state: 'ok',
    data: {
      kind,
      name,
      ...(namespace === undefined ? {} : { namespace }),
      title,
      tabs: [],
      sections: [],
      actions: kind === 'pods' ? POD_ACTIONS : [],
    },
  };
}

/* The drawer's facts, one object of each kind scene 24 measures. Shaped on
   what `internal/` sends - the Summary first, then the kind's own section -
   with values that can collapse the label column: an owner name and an image
   digest with nowhere to break, and a namespace's condition types, the one
   label longer than the cap. */
type Fact = { label: string; value: string; prose: boolean; pill: boolean; tone?: string };
const fact = (label: string, value: string, more: Partial<Fact> = {}): Fact => ({
  label,
  value,
  prose: false,
  pill: false,
  ...more,
});
const LONG_OWNER = 'scan-vulnerabilityreport-7d9f8c6b5-node-collector-with-an-owner-name-that-has-no-break';
const DIGEST = 'ghcr.io/aquasecurity/node-collector@sha256:' + '0123456789abcdef'.repeat(4);
function summary(kind: string, apiVersion: string, namespace: string | undefined): Fact[] {
  return [
    fact('Kind', kind),
    fact('API version', apiVersion),
    fact('Name', `${LONG_OWNER}-${kind.toLowerCase()}`),
    ...(namespace === undefined ? [] : [fact('Namespace', namespace)]),
    fact('Created', '2026-11-03 09:14:07 (3h ago)'),
    fact('Controlled by', LONG_OWNER),
  ];
}
function factPage(kind: string, title: string, namespace: string | undefined, own: Fact[], apiVersion: string) {
  return {
    state: 'ok',
    data: {
      kind,
      name: `facts-${kind}`,
      ...(namespace === undefined ? {} : { namespace }),
      title,
      tabs: [],
      sections: [
        { key: 'summary', label: 'Summary', state: 'ok', fields: summary(title, apiVersion, namespace) },
        { key: 'own', label: title, state: 'ok', fields: own },
      ],
      actions: [],
    },
  };
}
export const CONDITION_TYPES = [
  'NamespaceDeletionGroupVersionParsingFailure',
  'NamespaceDeletionContentFailure',
  'NamespaceContentRemaining',
  'NamespaceFinalizersRemaining',
];
const FACT_PAGES: Readonly<Record<string, unknown>> = {
  pods: factPage(
    'pods',
    'Pod',
    'trivy-system',
    [
      fact('Phase', 'Running', { pill: true, tone: 'ok' }),
      fact('Node', 'ip-10-0-3-41.eu-west-3.compute.internal'),
      fact('Restart policy', 'Never'),
      fact('Image', DIGEST),
    ],
    'v1',
  ),
  jobs: factPage(
    'jobs',
    'Job',
    'trivy-system',
    [
      fact('Completions', '1/1'),
      fact('Parallelism', '1'),
      fact('Backoff limit', '0'),
      fact('Backoff limit per index', '—'),
      fact('Suspend', 'false'),
    ],
    'batch/v1',
  ),
  cronjobs: factPage(
    'cronjobs',
    'CronJob',
    'trivy-system',
    [
      fact('Schedule', '*/30 * * * *'),
      fact('Suspend', 'false'),
      fact('Last schedule', '2026-11-03 12:00:00 (4m ago)'),
      fact('Concurrency policy', 'Forbid'),
    ],
    'batch/v1',
  ),
  deployments: factPage(
    'deployments',
    'Deployment',
    'trivy-system',
    [
      fact('Replicas', '1/1 ready'),
      fact('Strategy', 'RollingUpdate'),
      fact('Selector', 'app.kubernetes.io/name=trivy-operator,app.kubernetes.io/instance=trivy-operator'),
    ],
    'apps/v1',
  ),
  namespaces: factPage(
    'namespaces',
    'Namespace',
    undefined,
    [
      fact('Phase', 'Terminating', { pill: true, tone: 'warn' }),
      fact('Deleting since', '2026-11-02 18:40:12 (18h ago)'),
      ...CONDITION_TYPES.map((type) =>
        fact(
          type,
          'True — SomeResourcesRemain: Some content in the namespace has finalizers remaining: ' +
            'kubernetes.io/pvc-protection in 1 resource instances, since 2026-11-02',
          { prose: true, tone: 'warn' },
        ),
      ),
    ],
    'v1',
  ),
};

/**
 * Everything under `/api/`, answered here.
 *
 * **Matched on the path and not by a glob**, and that is a defect report rather
 * than a style: `page.route('**\/api\/**')` also matches
 * `/ui/src/api/index.ts`, so Vite's own module graph was served as JSON and the
 * page failed with a MIME-type error that says nothing about the cause.
 *
 * An unrecognized path is **refused**, not answered with something plausible. A
 * catch-all that returns `{state:'ok'}` for a shape the client expects to be a
 * list is how a scene ends up measuring an error notice and reporting a width.
 */
export async function answerTheCluster(page: Page): Promise<void> {
  await page.route(
    (url) => url.pathname.startsWith('/api/'),
    async (route: Route) => {
      const path = new URL(route.request().url()).pathname;

      if (path === '/api/capabilities') return route.fulfill({ json: CAPABILITIES });
      if (path === '/api/clusters') return route.fulfill({ json: CLUSTERS });
      if (path === '/api/preferences') return route.fulfill({ json: { state: 'ok', data: {} } });
      if (/^\/api\/c\/[^/]+\/nav$/.test(path)) return route.fulfill({ json: NAV });
      const facts = /^\/api\/c\/[^/]+\/r\/([^/]+)\/(?:[^/]+\/)?facts-[^/]+$/.exec(path);
      if (facts !== null) {
        const kind = facts[1] ?? '';
        const page = FACT_PAGES[kind];
        if (page !== undefined) return route.fulfill({ json: page });
      }
      if (/^\/api\/c\/[^/]+\/r\/pods\//.test(path)) {
        return route.fulfill({ json: detailPage('pods', 'prometheus-0', 'observability', 'Pod') });
      }
      if (/^\/api\/c\/[^/]+\/r\/namespaces\//.test(path)) {
        return route.fulfill({
          json: detailPage('namespaces', 'observability', undefined, 'Namespace'),
        });
      }
      return route.fulfill({ status: 404, json: { state: 'absent', reason: { code: 'absent' } } });
    },
  );
}

/**
 * What Chromium says when the host's network changed under a request.
 *
 * The runners share one VM, and a job on the other runner that starts a
 * container adds an interface to it. Chromium then aborts every request in
 * flight with this error, loopback included, and a scene whose modules were
 * being fetched never mounts: `#root` stays empty and the wait below runs out
 * the test's budget on a blank page, not a slow one. A longer timeout would
 * wait for a page that is never going to draw.
 */
export const NETWORK_CHANGED = 'net::ERR_NETWORK_CHANGED';

/** How many loads in a row a network change may interrupt before it is a failure. */
export const LOADS = 3;

/**
 * Loads a scene and waits for it to draw. A load the host's network
 * interrupted (`NETWORK_CHANGED`) is loaded again, at most `LOADS` times; any
 * other failure is the test's. Nothing about the page is
 * asserted differently: a retried load is a fresh navigation to the same scene.
 */
export async function show(page: Page, scene: string): Promise<void> {
  await answerTheCluster(page);
  for (let load = 1; ; load += 1) {
    let interrupt = (): void => undefined;
    const interrupted = new Promise<'interrupted'>((resolve) => {
      interrupt = () => resolve('interrupted');
    });
    const listener = (request: Request): void => {
      if (request.failure()?.errorText === NETWORK_CHANGED) interrupt();
    };
    page.on('requestfailed', listener);
    const drawn = page
      .goto(`/?scene=${scene}`)
      .then(() => page.waitForSelector('#root > *'))
      .then(
        () => 'drawn' as const,
        (error: unknown) => {
          // The document itself can be the request the change aborted.
          if (String(error).includes(NETWORK_CHANGED)) return 'interrupted' as const;
          throw error;
        },
      );
    // The loser of the race below is left running, and must not surface as an
    // unhandled rejection once the next navigation or the test's end ends it.
    drawn.catch(() => undefined);
    try {
      if ((await Promise.race([drawn, interrupted])) === 'drawn') return;
    } finally {
      page.off('requestfailed', listener);
    }
    if (load === LOADS) {
      throw new Error(
        `the scene ${scene} was interrupted by a change in the host's network on ${String(LOADS)} loads in a row`,
      );
    }
  }
}

/**
 * One stored value, read as a string.
 *
 * The argument is **source text and not a closure** because `eslint.config.js`
 * bans the bare `localStorage` identifier outside a few named files, and a
 * spec that writes it would either redden the lint or need another exemption
 * for a check. Nothing is written from here — state is set by clicking the
 * control a person clicks, which is also the only way to prove the round trip
 * through storage.
 */
export async function stored(page: Page, key: string): Promise<string | null> {
  return page.evaluate<string | null>(`window.localStorage.getItem(${JSON.stringify(key)})`);
}

/** Rounded to a tenth, so a failure message reads as a measurement. */
export function px(value: number): string {
  return `${String(Math.round(value * 10) / 10)}px`;
}

/* -------------------------------------------------------------------------- */
/* 1 — a stored width is a share, and the table fills its panel               */
/* -------------------------------------------------------------------------- */

/**
 * Every column's declaration and every column's drawn width, from one scene.
 *
 * The declarations are read off the `<colgroup>` rather than restated here,
 * because five of the eight are computed from the runner's font (limit 4 in
 * `main.tsx`) — what is asserted about them is the *relation* between what a
 * column declares and what it is drawn at, which is font-independent.
 */
export async function table(page: Page): Promise<{
  classes: string;
  width: number;
  panel: number;
  columns: { label: string; declared: number; drawn: number }[];
}> {
  return page.evaluate(() => {
    const grid = document.querySelector('#panel > .table-scroll > .kt-grid');
    const panel = document.getElementById('panel');
    if (grid === null || panel === null) throw new Error('no table under #panel');
    const cols = [...grid.querySelectorAll('colgroup col')];
    return {
      classes: grid.className,
      width: grid.getBoundingClientRect().width,
      // The panel's content box, whatever its padding.
      panel:
        panel.clientWidth -
        Number.parseFloat(getComputedStyle(panel).paddingLeft) -
        Number.parseFloat(getComputedStyle(panel).paddingRight),
      columns: [...grid.querySelectorAll('thead th')].map((th, at) => ({
        label: (th.textContent ?? '').trim(),
        declared: Number.parseFloat((cols[at] as HTMLElement | undefined)?.style.width ?? '0'),
        drawn: th.getBoundingClientRect().width,
      })),
    };
  });
}

export const sum = (numbers: readonly number[]): number => numbers.reduce((total, each) => total + each, 0);


/**
 * Four viewports, and a 203-target plan, so the box carries both things that
 * could overflow it: a long sentence and a list long enough to need its own
 * scroll region.
 */
export const VIEWPORTS = [
  { width: 420, height: 760 },
  { width: 900, height: 420 },
  { width: 1280, height: 720 },
  { width: 1920, height: 1080 },
];


/**
 * One scroll bar, not two, when a pod is opened: the drawer's tab panel
 * scrolls, and the document must not.
 *
 * The trap is a copy button's live region. `CopyButton` announces through an
 * `sr-only` span, which is `position: absolute`; with no positioned ancestor
 * nearer than `<body>`, its containing block is the page, so the tab panel's
 * `overflow-auto` neither clips it nor counts it - and the document grows to
 * reach where it would sit in the panel's content. One live region is enough,
 * which is why this measures the page's scrollers rather than a height.
 *
 * So the number is: every element that scrolls vertically, and the document.
 * The document scrolls nowhere, and what does scroll is in the allowed set - the
 * tab panel (`object-panel-*`) and the list's `#panel`.
 */
export async function verticalScrollers(page: Page): Promise<{ document: number; window: number; scrollers: string[] }> {
  return page.evaluate(() => {
    const scrollers: string[] = [];
    for (const element of document.querySelectorAll<HTMLElement>('body *')) {
      if (!/(auto|scroll)/.test(getComputedStyle(element).overflowY)) continue;
      if (element.scrollHeight <= element.clientHeight + 1) continue;
      scrollers.push(element.id !== '' ? `#${element.id}` : `${element.tagName.toLowerCase()}.${element.className}`);
    }
    return {
      document: document.documentElement.scrollHeight,
      window: document.documentElement.clientHeight,
      scrollers,
    };
  });
}
