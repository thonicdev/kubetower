import { QueryClientProvider, useQueryClient } from '@tanstack/react-query';
import { StrictMode, Suspense, useEffect, useRef, useState, type ReactNode } from 'react';
import { createRoot } from 'react-dom/client';
import { createMemoryRouter, MemoryRouter, Route, RouterProvider, Routes } from 'react-router';

import '../app/theme.css';

import type { Cell, Column, EnvVar, PodContainer } from '../ui/src/api';
import { DataGrid, type GridColumn } from '../ui/src/components/DataGrid';
import { Dialog } from '../ui/src/components/Dialog';
import { IconSprite } from '../ui/src/components/Icon';
import { Pill } from '../ui/src/components/Pill';
import { PlanDialog } from '../ui/src/components/PlanDialog';
import { Tile, TileGrid } from '../ui/src/components/Tile';
import { Dock } from '../ui/src/dock/Dock';
import { DockProvider, useDock } from '../ui/src/dock/DockContext';
import { KeyboardStack } from '../ui/src/keyboard/KeyboardStack';
import { Containers } from '../ui/src/pages/object/Containers';
import { Metrics } from '../ui/src/pages/object/Metrics';
import { Rules } from '../ui/src/pages/object/Rules';
import { Shortcuts } from '../ui/src/pages/object/Shortcuts';
import { ObjectDetail } from '../ui/src/pages/object/ObjectDetail';
import { ResourceCell } from '../ui/src/pages/resources/ResourceCell';
import { createQueryClient } from '../ui/src/query/client';
import { buildRoutes } from '../ui/src/router/routes';
import { ShellProvider } from '../ui/src/shell/ShellContext';
import { Menu } from '../ui/src/shell/Menu';
import { Rail } from '../ui/src/shell/Rail';

import type { Extension } from '../ui/src/extension';
import { extras } from '../app/extras';
import { events as eventsExtra } from '../app/extras/events';
import { pending } from '../app/extras/pending';
import { security } from '../app/extras/security';
import { redis } from '../app/extras/redis';

import { ACTIVITY_EXTRA, activityAnswers, activityPath } from './events.fixture';
import { UPDATE_STEPS, updateSettings } from './update.fixture';
import { replaceReleases } from '../ui/src/whatsnew/changelog';
import { WHATS_NEW_RELEASES, whatsNewAnswers } from './whatsnew.fixture';
import { LONG_ARN, LONG_NAME_ANSWERS } from './clusters.fixture';
import { EXTRA_CLUSTER, EXTRA_SCENES, ExtraProblem, type ExtraAnswer } from './extras.fixture';
import { DESKTOP_ONE_ANSWERS, IN_CLUSTER_ANSWERS, IN_CLUSTER_ENDING_ANSWERS, OVERVIEW_ANSWERS, OVERVIEW_CLUSTER, OVERVIEW_QUIET_ANSWERS, OVERVIEW_UNMEASURED_ANSWERS } from './overview.fixture';
import {
  drawerAnswers,
  CLUSTER_ROLES_PATH,
  DEPLOYMENTS_PATH,
  drawerPath,
  INGRESS_PATH,
  listsAnswers,
  ingressAnswers,
  PODS_PATH,
  PODS_TALL_DRAWER,
  PODS_TALL_PAGE,
  podsAnswers,
  podsLongStatusAnswers,
  podsManyAnswers,
  podsTallAnswers,
} from './pods.fixture';
import { pendingDeploymentCells } from './pending.fixture';
import { RBAC_CASES } from './rbac.fixture';
import { STORAGE_ANSWERS } from './storage.fixture';
import { securityPodCells } from './security.fixture';
import { SETTINGS_EXTRA_ITEMS } from './settings.fixture';
import { ExtraTab } from '../ui/src/pages/settings/ExtraTab';

/**
 * The layout harness. Six scenes, one per defect class, each mounting the
 * **real** component against the **real** compiled stylesheet in a **real**
 * browser engine, with synthetic data and no cluster, no back end and no
 * password.
 *
 * The specs in `measure/` drive it; `?scene=` picks which scene is mounted.
 *
 * ## Why it exists
 *
 * **Not one of the unit tests can see a pixel**: Vitest runs in jsdom, which
 * parses CSS and lays nothing out. A stored column width drawn twice too wide
 * (a `@layer utilities` `w-full` and an id-scoped rule each beating the rule
 * that lays it out) or a gauge track far from its own figure is pure geometry,
 * and leaves that suite green. Under Tailwind the cascade — layers, utilities,
 * an id-scoped rule — is doing real work, and that work is exactly what jsdom
 * does not simulate.
 *
 * ## WHAT THIS CANNOT CATCH. Read this before adding an assertion
 *
 * Stated here rather than discovered later, because a harness whose limits are
 * unwritten gets trusted for things it never checked:
 *
 * 1. **Anything that only appears with real server data.** Every row, column,
 *    capability and action below is a literal in this file. A column set that
 *    changes in `internal/resources/kinds.go`, a cell format that changes in
 *    `rows.go`, a width the server stores differently — none of it reaches here.
 *    The fixtures are *copied* from those files and copies drift. What this
 *    proves is that the **stylesheet and the components** lay out correctly for
 *    the shape given; it proves nothing about the shape being current.
 * 2. **Anything behind authentication, a cluster or a live socket.** No sign-in,
 *    no websocket, no metrics. `e2e/smoke.spec.ts` is the path that signs in to
 *    a running console, and it is deliberately a different path.
 * 3. **Color, contrast and legibility.** `theme.css` already records the case:
 *    a trough measured level and was invisible, and *"geometry and legibility are
 *    two questions"*. Every assertion here is a rectangle. None is a color.
 * 4. **Fonts other than the runner's.** Text advance decides a column's
 *    automatic width, so any assertion on an *undeclared* column's exact pixel
 *    width would be a font check wearing a layout check's clothes. The
 *    assertions therefore pin declared widths exactly and undeclared ones only
 *    by relation (present, non-zero, summing).
 * 5. **One engine.** Chromium, for the reason `playwright.config.ts` already
 *    gives for the browser path.
 * 6. **Anything the harness itself composes.** Where a scene reproduces
 *    structure — `#panel > .table-scroll`, four lines copied from
 *    `ResourceList.tsx` — an assertion about *that* structure would be the
 *    harness testing itself. The two grid scenes are therefore assertions about
 *    `theme.css` and `DataGrid.tsx`, never about the panel. The drawer scene
 *    mounts the real `ObjectDetail` for exactly this reason: its arm's order is
 *    the thing under test, so the harness must not hold a copy of it.
 */

const parameters = new URLSearchParams(window.location.search);
const scene = parameters.get('scene') ?? '';
// The What's new scenes draw from fake releases; no other scene states a release.
if (scene.startsWith('console-whats-new-')) replaceReleases(WHATS_NEW_RELEASES);

/**
 * The panel the list scenes are drawn in, in pixels; 1290 by default.
 *
 * **It is a parameter because a sized table's width follows its panel**,
 * and one fixed box cannot tell "fills its panel" from "happens to be that
 * wide". One number in the URL is the whole mechanism: the spec asks for the
 * same scene at 1290 and at 1700 and compares.
 */
const panelWidth = Number(parameters.get('width') ?? '1290');

/**
 * Widths for the list scene, as `name:240,age:64`, replacing whatever the
 * fixture declares.
 *
 * **This is what a reload is, in a harness with no server.** The console's
 * widths arrive as props out of a payload, so a fresh mount from numbers in the
 * URL is the same act — and it is the only way here to assert that what a
 * gesture *posted* is what comes back. A column named here that the fixture does
 * not have is ignored; a fixture column not named keeps what it declares.
 */
function storedWidths(): Record<string, number> {
  const out: Record<string, number> = {};
  for (const pair of (parameters.get('stored') ?? '').split(',')) {
    const [key, width] = pair.split(':');
    const number = Number(width);
    if (key !== undefined && key !== '' && Number.isFinite(number)) out[key] = number;
  }
  return out;
}

/* -------------------------------------------------------------------------- */
/* Fixtures                                                                    */
/* -------------------------------------------------------------------------- */

/**
 * Copied from `internal/resources/kinds.go:131-145` (the CronJobs column set)
 * and `rows.go:626-650` (`cronJobRow`), which is what decides the cell formats:
 * a schedule expression, `3h ago`, `in 3h`, `12d`. See limit 1 above — this is a
 * copy, and a copy drifts.
 */
interface Row {
  id: string;
  cells: readonly Cell[];
}

function column(key: string, label: string, extra: Partial<Column> = {}): Column {
  return {
    key,
    label,
    visible: true,
    order: 0,
    sortable: true,
    secondary: false,
    gauged: false,
    copyable: false,
    prose: false,
    ...extra,
  };
}

/**
 * `GridColumn` from a `Column`, the way `ResourceList` builds one.
 *
 * `reserve` is `ResourceList`'s own `GAUGE_SPAN` — 64 + 6 + 56 = 126 — restated
 * here because `reserveFor` is private to that file. If that figure moves there
 * and not here, the gauge scene sizes its column differently from the console
 * and this comment is the only thing that will say so.
 */
function gridColumn(source: Column, cells: (row: Row) => Cell): GridColumn<Row> {
  return {
    key: source.key,
    label: source.label,
    ...(source.align === undefined ? {} : { align: source.align }),
    ...(source.width === undefined ? {} : { width: source.width }),
    ...(source.gauged ? { reserve: 126 } : {}),
    measure: (row) => cells(row).text,
    cell: (row) => <ResourceCell cell={cells(row)} column={source} />,
  };
}

function cellAt(row: Row, index: number): Cell {
  return row.cells[index] ?? { text: '' };
}

/* -------------------------------------------------------------------------- */
/* Scene 1 — a resource list, some columns stored and some not                */
/* -------------------------------------------------------------------------- */

const CRON_ROWS: readonly Row[] = [
  ['backup-nightly', 'default', '0 2 * * *', 'Active', '0', '3h ago', 'in 3h', '12d'],
  ['prune-images-and-old-layers', 'kube-system', '*/15 * * * *', 'Active', '1', '4m ago', 'in 11m', '61d'],
  ['report-weekly', 'observability', '0 6 * * 1', 'Suspended', '0', '2d ago', '—', '184d'],
  ['rotate-certificates', 'cert-manager', '30 3 * * *', 'Active', '0', '19h ago', 'in 5h', '7d'],
  ['vacuum-analyze', 'postgres', '0 4 * * 0', 'Active', '0', '5d ago', 'in 2d', '312d'],
].map((texts, index) => ({
  id: `cron-${String(index)}`,
  cells: texts.map((text) => ({ text })),
}));

/**
 * The state the console is actually in: **some columns stored and some not.** A table where every width is
 * declared is `min-content` over a complete colgroup; a table where none is is
 * never `.sized` at all. The mixture is what a real operator produces after one
 * drag, and it is where `width: min-content` has to hold the declared numbers
 * while the browser still sizes the rest.
 *
 * `56` is the grip's own floor and `200` an ordinary stored width: both must be
 * drawn at exactly what they declare.
 */
const CRON_COLUMNS: readonly Column[] = [
  column('name', 'Name', { width: 200 }),
  column('namespace', 'Namespace'),
  column('schedule', 'Schedule', { width: 56 }),
  column('state', 'State'),
  column('active', 'Active', { align: 'end' }),
  column('last', 'Last run', { align: 'end', width: 90 }),
  column('next', 'Next run', { align: 'end' }),
  column('age', 'Age', { align: 'end' }),
];

const CRON_UNSIZED: readonly Column[] = CRON_COLUMNS.map(({ width: _width, ...rest }) => rest);

/**
 * `#panel` and nothing between it and the grid.
 *
 * Four lines copied from `ResourceList.tsx:781`, and its comment is the reason
 * they have to be exact: `theme.css` scopes the grid's sizing rule to
 * `#panel > .table-scroll > .kt-grid`, so a wrapper here would silently take the
 * whole rule with it and the scene would measure a table nothing applies to.
 *
 * The outer box is a fixed width so the reading does not depend on the runner's
 * window. 1290px is the default; `?width=` asks for another, which is how a
 * spec tells "fills its panel" from "is that wide".
 */
function Panel({ children }: { children: ReactNode }): ReactNode {
  return (
    <div style={{ width: panelWidth, height: 600, display: 'flex', flexDirection: 'column' }}>
      <div id="panel" className="min-h-0 flex-1 overflow-auto">
        {children}
      </div>
    </div>
  );
}

/**
 * The list, with the widths the URL asks for, and — under `?live=1` — a gesture
 * that is kept.
 *
 * **What the live arm reproduces, and why that is not the harness testing
 * itself.** Limit 6 forbids asserting about structure the harness composed. The
 * thing under test here is `DataGrid`'s gesture arithmetic and `theme.css`'s
 * layout; what this arm stands in for is the *round trip* — `Store.Resize`
 * clamps each width to [56, 720], keeps it, and the next payload carries it
 * back. Those two lines are a **copy** of `internal/columns/columns.go`'s clamp
 * and are named as one (limit 1): if that bound moves there and not here, this
 * scene keeps a width the console would refuse.
 *
 * The posted set is put on the panel as an attribute rather than kept in the
 * harness, so the spec can reload from exactly the numbers the gesture sent
 * instead of from numbers it composed itself.
 */
function ListScene({ columns, live = false }: { columns: readonly Column[]; live?: boolean }): ReactNode {
  const asked = storedWidths();
  const declared = columns.map((source) =>
    asked[source.key] === undefined ? source : { ...source, width: asked[source.key] },
  );
  const [kept, setKept] = useState<Record<string, number | null>>({});
  const [posted, setPosted] = useState<readonly { key: string; width: number | null }[]>([]);

  const shown = !live
    ? declared
    : declared.map((source) =>
        kept[source.key] === undefined
          ? source
          : { ...source, ...(kept[source.key] === null ? { width: undefined } : { width: kept[source.key] as number }) },
      );

  return (
    <Panel>
      <div data-posted={JSON.stringify(posted)} hidden />
      <DataGrid
        columns={shown.map((source, index) => gridColumn(source, (row) => cellAt(row, index)))}
        rows={CRON_ROWS}
        rowKey={(row) => row.id}
        label="Cron jobs"
        empty={<>Nothing here</>}
        {...(live
          ? {
              onResize: (sizes: readonly { key: string; width: number | null }[]) => {
                setPosted([...sizes]);
                setKept((previous) => {
                  const next = { ...previous };
                  for (const each of sizes) {
                    // `internal/columns/columns.go:clampWidth`, copied. See above.
                    next[each.key] =
                      each.width === null ? null : Math.min(720, Math.max(56, Math.round(each.width)));
                  }
                  return next;
                });
              },
            }
          : {})}
      />
    </Panel>
  );
}

/**
 * A list long enough to scroll, with its headings asked to stay.
 *
 * The same columns as `list-unsized`, and sixty rows rather than five, so that
 * `#panel` has somewhere to scroll to. `stickyHead` is what the console's own
 * list passes; the scene passes it too so that the assertion is about
 * `DataGrid.tsx`, never about the harness.
 */
const TALL_ROWS: readonly Row[] = Array.from({ length: 12 }, (_, round) =>
  CRON_ROWS.map((row) => ({ ...row, id: `${row.id}-${String(round)}` })),
).flat();

function TallListScene(): ReactNode {
  return (
    <Panel>
      <DataGrid
        columns={CRON_UNSIZED.map((source, index) => gridColumn(source, (row) => cellAt(row, index)))}
        rows={TALL_ROWS}
        rowKey={(row) => row.id}
        label="Cron jobs"
        empty={<>Nothing here</>}
        stickyHead
      />
    </Panel>
  );
}

/**
 * A list whose cells hold controls: the namespace filter, an address's copy
 * button, the tick in the name's cell, and rows that open. `?compact=1` draws it
 * at the compact density, where a cell has two pixels of padding above and
 * below and clips what it holds.
 *
 * The column set is a Pods list cut down to the three columns that carry a
 * control (see limit 1: a copy, not the server's list).
 */
const POD_COLUMNS: readonly Column[] = [
  column('name', 'Name', { role: 'name' }),
  column('namespace', 'Namespace', { role: 'namespace' }),
  column('ip', 'IP', { copyable: true }),
];

const POD_ROWS: readonly Row[] = [
  ['prometheus-0', 'observability', '10.0.4.17'],
  ['ledger-api-7d9f8b6c4-x2k9p', 'payments', '10.0.7.102'],
  ['indexer-5b7c9d-qw4tz', 'search', '10.0.2.9'],
].map((texts, index) => ({
  id: `pod-${String(index)}`,
  cells: texts.map((text) => ({ text, copy: text })),
}));

function ControlsScene(): ReactNode {
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set());
  return (
    <Panel>
      <DataGrid
        compact={parameters.get('compact') === '1'}
        columns={POD_COLUMNS.map((source, index) => ({
          key: source.key,
          label: source.label,
          measure: (row: Row) => cellAt(row, index).text,
          cell: (row: Row) => (
            <ResourceCell cell={cellAt(row, index)} column={source} onPickNamespace={() => undefined} />
          ),
        }))}
        rows={POD_ROWS}
        rowKey={(row) => row.id}
        label="Pods"
        empty={<>Nothing here</>}
        rowAction={() => () => undefined}
        selection={{
          picked,
          toggle: (id) => {
            setPicked((previous) => {
              const next = new Set(previous);
              if (next.has(id)) next.delete(id);
              else next.add(id);
              return next;
            });
          },
          setAll: () => undefined,
          allPicked: false,
          somePicked: picked.size > 0,
        }}
      />
    </Panel>
  );
}

/* -------------------------------------------------------------------------- */
/* Scene 2 — a gauged column beside a numeric one                            */
/* -------------------------------------------------------------------------- */

/** `internal/resources/kinds.go:533-542`, the Nodes column set. */
const NODE_COLUMNS: readonly Column[] = [
  column('name', 'Name'),
  column('status', 'Status'),
  column('taints', 'Taints', { align: 'end' }),
  column('cpu', 'CPU', { align: 'end', gauged: true }),
  column('memory', 'Memory', { align: 'end', gauged: true }),
  column('disk', 'Disk', { align: 'end', gauged: true }),
  column('version', 'Version'),
  column('ip', 'Internal IP'),
  column('instance', 'Instance type'),
  column('age', 'Age', { align: 'end' }),
];

const NODE_ROWS: readonly Row[] = [
  {
    id: 'node-0',
    cells: [
      { text: 'worker-01' },
      { text: 'Ready' },
      // A right-aligned digit in the column immediately before the first gauge:
      // a bar drawn too close to it reads as part of this column.
      { text: '2' },
      { text: '1.8 cores', gauge: { percent: 58 } },
      { text: '12.4 GiB', gauge: { percent: 71, tone: 'warn' } },
      { text: '104.0 GiB', gauge: { percent: 33 } },
      { text: 'v1.31.4' },
      { text: '10.0.4.17' },
      { text: 'm6i.2xlarge' },
      { text: '284d' },
    ],
  },
  {
    id: 'node-1',
    cells: [
      { text: 'worker-02' },
      { text: 'Ready' },
      { text: '0' },
      { text: '368m', gauge: { percent: 6 } },
      { text: '3.1 GiB', gauge: { percent: 19 } },
      { text: '—' },
      { text: 'v1.31.4' },
      { text: '10.0.4.18' },
      { text: 'm6i.2xlarge' },
      { text: '284d' },
    ],
  },
];

function GaugeScene(): ReactNode {
  return (
    <Panel>
      <DataGrid
        columns={NODE_COLUMNS.map((source, index) =>
          gridColumn(source, (row) => cellAt(row, index)),
        )}
        rows={NODE_ROWS}
        rowKey={(row) => row.id}
        label="Nodes"
        empty={<>Nothing here</>}
      />
    </Panel>
  );
}

/* -------------------------------------------------------------------------- */
/* Scenes 3 and 4 — the rail's width and the menu's shown/hidden              */
/* -------------------------------------------------------------------------- */

/**
 * Both render their own chrome — the element carrying the width — before any
 * query resolves, so neither needs a back end to be measured; the menu reads
 * shown or hidden from `menuMemory.ts`, and the rail has one width.
 * `Menu` does need a `cluster` route parameter: without one its whole body is
 * `null` and `empty:hidden` collapses the `<nav>` to no width at all, which
 * would read as a column hidden by nobody.
 */
function ShellScene({ what }: { what: 'rail' | 'menu' }): ReactNode {
  return (
    <div className="flex h-screen">
      {what === 'rail' ? <Rail /> : <Menu />}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* Scene 5 — a confirmation dialog at a narrow viewport                       */
/* -------------------------------------------------------------------------- */

/**
 * A 203-target plan, so the box has both things
 * that could overflow it: a long sentence and a list long enough to need its own
 * scroll region.
 */
const TARGETS = Array.from({ length: 203 }, (_value, index) => ({
  kind: 'Pod',
  namespace: 'observability',
  name: `prometheus-node-exporter-${String(index).padStart(3, '0')}`,
}));

function DialogScene(): ReactNode {
  return (
    <PlanDialog
      title="Delete 203 pods"
      plan={{
        token: 'measure',
        // A literal, not `Date.now() + 60_000`. The dialog draws no countdown,
        // so the value is inert here — and a clock read during render is what
        // `react-hooks/purity` refuses, correctly: a component whose output
        // depends on the moment it re-rendered is a component that measures
        // differently on the second reading.
        expiresAt: '2099-01-01T00:00:00Z',
        sentence: 'Delete 203 pods in observability? This cannot be undone.',
        facts: {
          verb: 'delete',
          targets: TARGETS,
          count: TARGETS.length,
          destructive: true,
          irreversible: true,
        },
      }}
      pending={false}
      error={null}
      onCancel={() => undefined}
      onApply={() => undefined}
    />
  );
}

/**
 * A single pod's delete, with the typed gesture: the dialog as the operator
 * meets it from a pod's page. `Dialog` focuses the field on open, so its ring is
 * lit from the first frame, and the field sits at the left edge of the body's
 * scroll region - which is where a ring drawn outside the field is clipped.
 */
function TypedDialogScene(): ReactNode {
  return (
    <PlanDialog
      title="Delete pod"
      plan={{
        token: 'measure',
        expiresAt: '2099-01-01T00:00:00Z',
        sentence: 'Delete Pod observability/prometheus-0?',
        facts: {
          verb: 'delete',
          targets: [{ kind: 'Pod', namespace: 'observability', name: 'prometheus-0' }],
          count: 1,
          destructive: true,
          irreversible: true,
          button: 'Delete this pod',
          confirmation: { kind: 'count', expected: '1' },
        },
      }}
      pending={false}
      error={null}
      onCancel={() => undefined}
      onApply={() => undefined}
    />
  );
}

/** The plain dialog too, since `PlanDialog` renders inside one. */
function PlainDialogScene(): ReactNode {
  return (
    <Dialog title="Delete this object?" onClose={() => undefined}>
      <p>
        Deleting a Deployment removes the ReplicaSets it owns and the Pods those own. This cannot be
        undone, and nothing here recreates them.
      </p>
    </Dialog>
  );
}

/* -------------------------------------------------------------------------- */
/* Scene 6 — the object drawer's control arm                                 */
/* -------------------------------------------------------------------------- */

/**
 * **The real `ObjectDetail`, not a copy of its arm.** The order of the controls
 * is the thing under test and it lives in that file, so a harness holding its own
 * `<div data-object-controls>` would be asserting about itself. The
 * `measure/*.spec.ts` specs answer `/api/**` from Playwright, which is what
 * stands in for the cluster.
 */
function ArmScene({ kind, namespace, name }: { kind: string; namespace?: string; name: string }): ReactNode {
  return (
    <div style={{ width: 1600 }}>
      <ObjectDetail
        identity={{ cluster: 'demo', kind, ...(namespace === undefined ? {} : { namespace }), name }}
        chrome="drawer"
      />
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* The drawer's fact tables                                                  */
/* -------------------------------------------------------------------------- */

/**
 * The real `ObjectDetail`, drawn as the drawer, at the drawer's own width:
 * `--drawer-w` follows the viewport, so the spec picks the width by sizing the
 * window. `?kind=` names the object; its facts are answered from the spec
 * (`FACT_PAGES`), under a name that starts `facts-`.
 *
 * **The box is the drawer's `<aside>` as `shell/Drawer.tsx` draws it with no
 * width chosen** - the same classes, `overflow-auto` included, so a fact past
 * its edge makes it scroll sideways as it would on screen - **and not `Drawer`
 * itself**, which reads the shell and the router and draws only what the shell
 * put in it. What that leaves out: a width set by dragging the grip, and the
 * list column beside the drawer.
 */
function FactsScene(): ReactNode {
  const kind = parameters.get('kind') ?? 'pods';
  const namespace = kind === 'namespaces' ? undefined : 'trivy-system';
  return (
    <aside
      data-facts-drawer=""
      className="relative w-[var(--drawer-w)] shrink-0 overflow-auto border-l border-line bg-panel"
    >
      <ObjectDetail
        identity={{ cluster: 'demo', kind, ...(namespace === undefined ? {} : { namespace }), name: `facts-${kind}` }}
        chrome="drawer"
      />
    </aside>
  );
}

/* -------------------------------------------------------------------------- */
/* Scene 7 — the whole console on one cluster's dashboard                    */
/* -------------------------------------------------------------------------- */

/**
 * **A mock, not a measurement**, and the one scene here whose reader is a
 * person rather than the `measure/*.spec.ts` specs: a design is judged on a
 * before/after pair at 1440px, and both want the page as it is met — the rail,
 * the menu, the topbar and the dashboard in its real slot — drawn from numbers
 * that do not move between the two shots.
 *
 * So it mounts the **whole application** — `buildRoutes`, `ShellProvider`, the
 * same providers in the same order as `createApp` — at `/c/<cluster>`, rather
 * than the `Overview` component in a box: a page mocked in a slot of the wrong
 * width is a mock of a different page, and the `Icon` sprite the panels draw
 * from is mounted by the Shell and nothing else.
 *
 * **The cluster is answered by a `fetch` shim, here, and not from Playwright.**
 * The six layout scenes are answered from the spec because the spec is their
 * only reader. This one is opened in a browser by a person:
 *
 *     pnpm measure            →  http://127.0.0.1:5847/?scene=overview
 *
 * and a scene that draws only under a test runner is not a fixtures route.
 * `?scene=overview-unmeasured` is the same cluster with no metrics backend at
 * all (`OVERVIEW_UNMEASURED_ANSWERS`), which is where a `used` sentence under a
 * ring is longest. The
 * shim is `testing/harness.tsx`'s `stubFetch` moved to the browser: a path in
 * `OVERVIEW_ANSWERS` is answered as JSON, anything else is a 404 problem — never
 * something plausible, for the reason `layoutkit.ts`'s `answerTheCluster` gives.
 * `fetch` is the console's one door to the server (`api/request.ts`), and
 * nothing this page opens is a socket or an `EventSource`.
 *
 * Limit 2 above still holds in the other direction: nothing here proves the
 * fixture is a shape the server sends today. `overview.fixture.ts` is a copy.
 */
function answerFromFixture(answers: Readonly<Record<string, unknown>>): void {
  const real = window.fetch.bind(window);
  window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, window.location.href);
    if (!url.pathname.startsWith('/api/')) return real(input, init);
    // Decoded, so a key can name a context as the kubeconfig spells it: the
    // console escapes the ARN's `/` and `:` into its address.
    const body = answers[decodeURIComponent(url.pathname)];
    const headers = { 'Content-Type': 'application/json' };
    if (body === undefined) {
      return Promise.resolve(
        new Response(JSON.stringify({ state: 'absent', reason: { code: 'absent', message: `no fixture for ${url.pathname}` } }), {
          status: 404,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
      );
    }
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers }));
  };
}

function ConsoleScene({ at, extensions = [] }: { at: string; extensions?: readonly Extension[] }): ReactNode {
  const [router] = useState(() => createMemoryRouter(buildRoutes(extensions), { initialEntries: [at] }));
  return (
    <ShellProvider extensions={extensions}>
      <RouterProvider router={router} />
    </ShellProvider>
  );
}

/* -------------------------------------------------------------------------- */
/* Scene 8 - the environment table under long names                          */
/* -------------------------------------------------------------------------- */

/**
 * The real `Containers` panel, in a box as wide as the drawer, holding what
 * `argocd-application-controller-0` carries: a run of variables with 47- to
 * 51-character names, all read out of `argocd-cmd-params-cm`,
 * all optional, none of whose keys the ConfigMap carries. Then one required key
 * that is missing, which is the absence that must still warn.
 *
 * The names are shaped on Argo CD's. Six are exactly 47 characters, and one is
 * Argo CD's real `ARGOCD_APPLICATION_CONTROLLER_REPO_SERVER_PLAINTEXT`, 51
 * characters: long enough to leave a single letter on a third line unless names
 * are broken at their underscores. They are not a copy of its manifest. Limit 1 above
 * applies: the server's `envfrom.go` decides which rows are `expected`, and
 * this copies its answer.
 *
 * `--drawer-w` and the tab panel's `p-3`, so the table gets the width it gets
 * inside the drawer. At a 1600px viewport the token is its 560px cap.
 */
const ARGO_PARAMS = 'argocd-cmd-params-cm';

function notSet(name: string, key: string): EnvVar {
  return {
    name,
    problem: 'no such key, and the reference is optional — this variable is not set',
    absence: 'expected',
    secret: false,
    held: false,
    clipped: false,
    source: { kind: 'ConfigMap', name: ARGO_PARAMS, key, optional: true },
  };
}

const ARGO_CONTROLLER: PodContainer = {
  name: 'argocd-application-controller',
  image: 'quay.io/argoproj/argocd:v3.1.0',
  init: false,
  probes: [],
  status: { state: 'running', ready: true, restarts: 0 },
  env: [
    notSet('ARGOCD_APPLICATION_CONTROLLER_STATUS_PROCESSORS', 'controller.status.processors'),
    notSet('ARGOCD_APPLICATION_CONTROLLER_REPO_SERVER_PLAINTEXT', 'controller.repo.server.plaintext'),
    notSet('ARGOCD_APPLICATION_CONTROLLER_PERSIST_HEALTH_ON', 'controller.resource.health.persist'),
    notSet('ARGOCD_APPLICATION_CONTROLLER_KUBECTL_PARALLEL_', 'controller.kubectl.parallelism.limit'),
    notSet('ARGOCD_APPLICATION_CONTROLLER_SERVER_SIDE_DIFFS', 'controller.diff.server.side'),
    notSet('ARGOCD_APPLICATION_CONTROLLER_OTLP_INSECURE_ON_', 'otlp.insecure'),
    {
      name: 'ARGOCD_APPLICATION_CONTROLLER_REDIS_COMPRESSION',
      problem: 'no such key, and the reference is not optional — the container will not start',
      absence: 'unset',
      secret: false,
      held: false,
      clipped: false,
      source: { kind: 'ConfigMap', name: ARGO_PARAMS, key: 'redis.compression', optional: false },
    },
  ],
};

function EnvScene(): ReactNode {
  return (
    <div style={{ width: 'var(--drawer-w)' }} className="border-l border-line bg-panel">
      <div className="p-3">
        <Containers cluster="demo" kind="pods" namespace="argocd" name="argocd-application-controller-0" containers={[ARGO_CONTROLLER]} />
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* A last termination's message in the pod panel                            */
/* -------------------------------------------------------------------------- */

/**
 * The real `Containers` panel at the drawer's width, as in the `env` scene,
 * holding two restarted containers whose previous run left a termination
 * message the panel cannot break at a space:
 *
 * - **one line of JSON, 176 characters**, the shape a structured logger writes
 *   and `FallbackToLogsOnError` hands back, far wider than the panel unless
 *   the message wraps;
 * - **a multi-line tail near the kubelet's 4 KB cap**, with one unbroken
 *   token in it, which must keep its newlines.
 *
 * And two containers whose **current** state is `terminated: Error`: the same
 * container twice, once carrying a fifteen-line Trivy tail - escapes, an orphaned `[34m`,
 * an emoji - as its message and once carrying none, so the spec can measure
 * what the message costs the card rather than guess at a height.
 *
 * Fabricated, shaped on what such tails look like; no workload's real output.
 */
const JSON_LINE =
  '{"level":"error","ts":1760434951.041,"caller":"db/pool.go:118","msg":"connection refused",' +
  '"dsn_host":"payments_db_primary_internal","attempt":7,"err":"dial_tcp_10_0_3_41_5432"}';

const LONG_TAIL = [
  'goroutine 1 [running]:',
  ...Array.from(
    { length: 40 },
    (_, at) =>
      `main.handler${String(at)}(0xc000${String(1000 + at)}, 0x${String(at).padStart(4, '0')}) ` +
      `/src/internal/handler/serve.go:${String(40 + at)} +0x1a4`,
  ),
  `token=${'A1b2C3d4'.repeat(100)}`,
].join('\n');

const TRIVY_TAIL = [
  ...Array.from(
    { length: 11 },
    (_, at) => `2026-10-15T08:00:${String(at).padStart(2, '0')}Z\tINFO\t[vulndb] Downloading artifact part ${String(at)}...`,
  ),
  '2026-10-15T08:00:11Z\tWARN\t[vulndb] Retrying download... attempt=3',
  '\x1b[34mNotices:\x1b[0m',
  '[34mNotices:[0m - Version 0.76.0 of Trivy is now available \u{1f389}',
  '2026-10-15T08:00:13Z\tFATAL\tFatal error\tinit error: DB error: failed to download vulnerability DB',
].join('\n');

/** The terminated container, with and without its message. */
const stopped = (name: string, message?: string): PodContainer => ({
  name,
  image: 'mirror.gcr.io/aquasec/trivy:0.75.0',
  init: false,
  probes: [],
  status: {
    state: 'terminated',
    ready: false,
    restarts: 0,
    reason: 'Error',
    exitCode: 1,
    ...(message === undefined ? {} : { message }),
  },
});

const RESTARTED_WITH_MESSAGES: PodContainer[] = [
  {
    name: 'payments-api',
    image: 'registry.example.com/payments-api:4.2.0',
    init: false,
    probes: [],
    status: {
      state: 'running',
      ready: true,
      restarts: 12,
      lastTermination: {
        exitCode: 1,
        reason: 'Error',
        message: JSON_LINE,
        startedAt: '2026-10-14T08:00:00Z',
        finishedAt: '2026-10-14T08:41:30Z',
      },
    },
  },
  {
    name: 'ledger-worker',
    image: 'registry.example.com/ledger-worker:1.9.3',
    init: false,
    probes: [],
    status: {
      state: 'running',
      ready: true,
      restarts: 3,
      lastTermination: {
        exitCode: 2,
        reason: 'Error',
        message: LONG_TAIL,
        startedAt: '2026-10-14T07:00:00Z',
        finishedAt: '2026-10-14T07:05:00Z',
      },
    },
  },
  stopped('grafana-sc-datasources', TRIVY_TAIL),
  stopped('grafana-sc-silent'),
];

/** The real `Rules` panel at the drawer's width (or the viewport's, where that is
 *  narrower): `?case=short|cluster-admin|long|aggregated|role`. */
function RbacScene(): ReactNode {
  const set = RBAC_CASES[parameters.get('case') ?? 'short'];
  return (
    <div data-rbac-panel style={{ width: 'min(var(--drawer-w), 100vw)' }} className="border-l border-line bg-panel">
      <div className="p-3">
        <Rules set={set} />
      </div>
    </div>
  );
}

function TerminationMessageScene(): ReactNode {
  return (
    <div data-pod-panel style={{ width: 'var(--drawer-w)' }} className="border-l border-line bg-panel">
      <div className="p-3">
        <Containers cluster="demo" kind="pods" namespace="payments" name="payments-api-0" containers={RESTARTED_WITH_MESSAGES} />
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* What each container is started with                                       */
/* -------------------------------------------------------------------------- */

/**
 * The real `Containers` panel at the drawer's width, holding three cases: an
 * init container running `sh -c` with a multi-line script, a
 * container setting both `command` and `args` - one argument holding a space,
 * one holding quotes, so a drawing that re-quotes or splits them shows it - and
 * a container setting neither, whose image's own entrypoint runs.
 *
 * Opened by a person as well as by the `measure/*.spec.ts` specs: it is the tile
 * `shot-commands.mjs` captures. Fabricated, shaped on what such
 * pods look like; no workload's real manifest.
 */
const RENDER_SCRIPT = [
  'set -eu',
  'echo "rendering config for ${POD_NAME:-web-0}"',
  "for f in /templates/*.tmpl; do",
  '  out="/config/$(basename "$f" .tmpl)"',
  "  sed -e \"s|@DB_HOST@|${DB_HOST}|g\" \"$f\" > \"$out\"",
  '  echo "wrote $out"',
  'done',
  "until nc -z \"${DB_HOST}\" 5432; do",
  "  echo 'waiting for the database'",
  '  sleep 2',
  'done',
  'touch /config/.ready',
].join('\n');

const STARTED: PodContainer[] = [
  {
    name: 'render-config',
    image: 'busybox:1.36',
    init: true,
    command: ['/bin/sh', '-c'],
    args: [RENDER_SCRIPT],
    env: [{ name: 'DB_HOST', value: 'payments-db.payments.svc', secret: false, held: false, clipped: false }],
    probes: [
      { kind: 'liveness', state: 'absent' },
      { kind: 'readiness', state: 'absent' },
      { kind: 'startup', state: 'absent' },
    ],
    status: { state: 'terminated', ready: false, restarts: 0, reason: 'Completed' },
  },
  {
    name: 'api',
    image: 'registry.example.com/payments-api:4.2.0',
    init: false,
    command: ['/app/payments-api'],
    args: ['--listen=:8080', '--log-format', 'json', '--greeting', 'hello world', '--filter=app="web"', ''],
    requests: [
      { name: 'cpu', value: '100m' },
      { name: 'memory', value: '256Mi' },
    ],
    limits: [{ name: 'memory', value: '512Mi' }],
    env: [
      { name: 'LOG_LEVEL', value: 'info', secret: false, held: false, clipped: false },
      {
        name: 'DB_PASSWORD',
        secret: true,
        held: false,
        clipped: false,
        source: { kind: 'Secret', name: 'payments-db', key: 'password', optional: false },
      },
    ],
    probes: [
      { kind: 'liveness', state: 'declared', handler: 'httpGet', target: '/healthz:8080', periodSeconds: 10, failureThreshold: 3 },
      { kind: 'readiness', state: 'declared', handler: 'httpGet', target: '/ready:8080', periodSeconds: 5, failureThreshold: 2 },
      { kind: 'startup', state: 'absent' },
    ],
    status: { state: 'running', ready: true, restarts: 0 },
  },
  {
    name: 'envoy',
    image: 'envoyproxy/envoy:v1.31.2',
    init: false,
    probes: [
      { kind: 'liveness', state: 'absent' },
      { kind: 'readiness', state: 'declared', handler: 'tcpSocket', target: ':9901', periodSeconds: 5 },
      { kind: 'startup', state: 'absent' },
    ],
    status: { state: 'running', ready: true, restarts: 0 },
  },
];

function CommandsScene(): ReactNode {
  return (
    <div data-pod-panel style={{ width: 'var(--drawer-w)' }} className="border-l border-line bg-panel">
      {/* The glyphs the tile's chips and folds draw, which the shell mounts in the console. */}
      <IconSprite />
      <div className="p-3">
        <Containers cluster="demo" kind="pods" namespace="payments" name="payments-api-0" containers={STARTED} />
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* The dock's log toolbar at the width the shell leaves it                   */
/* -------------------------------------------------------------------------- */

/**
 * A log pane in the dock, beside the rail and the menu as `Shell.tsx` lays
 * them out, so the toolbar gets the width it gets in the console and not the
 * viewport's. The pod has three containers, one an init and one restarted, so
 * the switcher, its restart badge and Prev run are all drawn: the widest bar
 * the pane can have before anything is typed.
 *
 * The container list is put in the query cache rather than answered from the
 * spec, because `answerTheCluster` refuses what it does not name and the
 * stream itself is an EventSource that has nothing to connect to here - the
 * pane says so in its marker, below the toolbar, which is not measured.
 */
const DOCK_POD = { cluster: 'demo', kind: 'pods', namespace: 'observability', name: 'prometheus-0' };

function DockLogScene(): ReactNode {
  const client = useQueryClient();
  const { openLogs } = useDock();
  useState(() =>
    client.setQueryData(['logContainers', DOCK_POD.cluster, DOCK_POD.namespace, DOCK_POD.name], {
      state: 'ok',
      items: [
        { name: 'init-chown-data', init: true, ready: true, restarts: 0 },
        { name: 'prometheus', init: false, ready: true, restarts: 4 },
        { name: 'config-reloader', init: false, ready: true, restarts: 0 },
      ],
    }),
  );
  // Once, StrictMode's second effect included: a second tab is another pane.
  const opened = useRef(false);
  useEffect(() => {
    if (opened.current) return;
    opened.current = true;
    openLogs(DOCK_POD);
  }, [openLogs]);
  return (
    <div className="flex h-screen w-full overflow-hidden bg-bg text-fg">
      <Rail />
      <Menu />
      <div className="flex min-w-0 flex-1 flex-col">
        <main className="min-h-0 flex-1" />
        <Dock />
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */

/**
 * A tile that opens, with a chip and a detail that carry hover sentences of
 * their own, which the open control's stretched box must not cover. `data-opened` counts
 * the opens, so a click on the chip can be told from a click on nothing.
 */
function TilesScene(): ReactNode {
  const [opened, setOpened] = useState(0);
  return (
    <div className="w-96 p-4" data-opened={opened}>
      <TileGrid label="Applications">
        <Tile
          title="cilium"
          tone="warn"
          onOpen={() => setOpened((n) => n + 1)}
          badges={
            <Pill tone="warn" title="ComparisonError: one resource drifted">
              OutOfSync
            </Pill>
          }
          detail={<span title="project default, namespace kube-system">default · kube-system</span>}
        />
      </TileGrid>
    </div>
  );
}

function Scene(): ReactNode {
  switch (scene) {
    // The sign-in view, outside the shell: the card alone.
    case 'tiles':
      return <TilesScene />;
    case 'list':
      return <ListScene columns={CRON_COLUMNS} />;
    case 'list-unsized':
      return <ListScene columns={CRON_UNSIZED} />;
    // The same list, with the gesture kept: `?live=1` is what makes a drag
    // survive the pointer coming up. See ListScene.
    case 'list-live':
      return <ListScene columns={CRON_COLUMNS} live />;
    case 'list-unsized-live':
      return <ListScene columns={CRON_UNSIZED} live />;
    case 'list-tall':
      return <TallListScene />;
    case 'gauge':
      return <GaugeScene />;
    case 'rail':
      return <ShellScene what="rail" />;
    case 'menu':
      return <ShellScene what="menu" />;
    case 'dialog':
      return <DialogScene />;
    case 'dialog-plain':
      return <PlainDialogScene />;
    case 'dialog-typed':
      return <TypedDialogScene />;
    case 'list-controls':
      return <ControlsScene />;
    case 'arm-pod':
      return <ArmScene kind="pods" namespace="observability" name="prometheus-0" />;
    case 'arm-namespace':
      return <ArmScene kind="namespaces" name="observability" />;
    case 'env':
      return <EnvScene />;
    case 'facts':
      return <FactsScene />;
    case 'termination-message':
      return <TerminationMessageScene />;
    case 'rbac':
      return <RbacScene />;
    case 'commands':
      return <CommandsScene />;
    case 'pvc-storage':
      return <StorageScene />;
    case 'dock-log':
      return <DockLogScene />;
    case 'settings-extra':
    case 'settings-extra-open':
    case 'settings-extra-columns':
    case 'settings-extra-suggested':
      return <SettingsExtraScene />;
    default:
      return <p data-unknown-scene={scene}>No such scene: {scene === '' ? '(none given)' : scene}</p>;
  }
}

/** A claim's storage chart, silent as on the claim's panel, over `storage.fixture.ts`. */
function StorageScene(): ReactNode {
  return (
    <div className="min-h-screen bg-bg p-6 text-fg">
      <IconSprite />
      <div className="max-w-xl">
        <Metrics cluster="demo" kind="persistentvolumeclaims" namespace="shop" name="data" silent />
      </div>
    </div>
  );
}

/** Three or more suggested extras, as the server answers them. */
const SUGGESTED = {
  state: 'ok',
  items: [
    { key: 'redis', label: 'Redis viewer', evidence: '2 Redis or Valkey Services in 1 namespace' },
    { key: 'certmanager', label: 'Cert Manager', evidence: 'cert-manager is installed with 15 certificates' },
    { key: 'security', label: 'Security Center', evidence: 'trivy-operator reports in 6 namespaces' },
    { key: 'cnpg', label: 'Postgres (CNPG)', evidence: '3 CloudNativePG clusters in 2 namespaces' },
  ],
};
if (scene === 'settings-extra-suggested') answerFromFixture({ '/api/c/demo/extensions/suggestions': SUGGESTED });

/** Settings > Extra as the tab draws it, over the listing of `settings.fixture.ts`. */
function SettingsExtraScene(): ReactNode {
  return (
    <div className="min-h-screen bg-bg p-6 text-fg">
      <IconSprite />
      <ShellProvider extensions={extras}>
        <ExtraTab cluster="demo" clusters={[{ name: 'demo', label: 'demo' } as never]} items={SETTINGS_EXTRA_ITEMS} />
      </ShellProvider>
    </div>
  );
}

const root = document.getElementById('root');
if (root === null) throw new Error('#root is missing');

/**
 * `MemoryRouter` at `/c/demo` because three of the scenes reach `useParams()` for
 * the cluster: `Menu` (or its body is null), `Rail`'s tiles, and `DockProvider`,
 * which is what `LogsButton` reads to decide whether it renders at all.
 *
 * No `StrictMode` double-mount surprises are papered over: it is on, because the
 * application's own entry has it on and a component that measures differently
 * under it measures differently in production.
 */
/*
  Scene 9 - the whole console with eight clusters whose names break a layout:
  `?scene=names&at=<path>`, where `at` is any console address - `/` for the
  cluster picker, and the overview and the pods list of the 89-character ARN by
  default. `clusters.fixture.ts` says what the eight are and why. Like the
  overview scene it is opened by a person as well as by the `measure/*.spec.ts`
  specs.
*/
const at = parameters.get('at') ?? `/c/${encodeURIComponent(LONG_ARN)}`;

/**
 * The scenes that mount the whole console, each on its own fixture through the
 * same `fetch` shim: the dashboard, measured and unmeasured; scene 9's long
 * cluster names; and the Pods list with a Node and a Status chosen in the
 * filter control, unarranged and arranged.
 * The `measure/*.spec.ts` specs measure the list page's sideways overflow at 1024
 * and 1280 on the last two, and the menus the funnel and a row's `⋯` open.
 */
/** The console's menu with an Extra group naming `key`: what says an extra is
 *  on for the cluster, so its columns are drawn. */
function withExtraOn(answers: Readonly<Record<string, unknown>>, key: string, label: string): Readonly<Record<string, unknown>> {
  const path = `/api/c/${OVERVIEW_CLUSTER}/nav`;
  const nav = answers[path] as { state: string; data: { groups: readonly unknown[] } };
  const extra = { key: 'extra', label: 'Extra', deferred: false, items: [{ text: label, href: `/c/${OVERVIEW_CLUSTER}/x/${key}` }] };
  return { ...answers, [path]: { ...nav, data: { ...nav.data, groups: [...nav.data.groups, extra] } } };
}

/** The kinds column with a Custom Resources heading whose API groups carry
 *  long names, and (`favourites`) kinds marked as favorites on the cluster. */
function menuKindsAnswers(favourites: readonly string[] = []): Readonly<Record<string, unknown>> {
  const path = `/api/c/${OVERVIEW_CLUSTER}/nav`;
  const nav = OVERVIEW_ANSWERS[path] as { state: string; data: { groups: readonly unknown[] } };
  const kind = (text: string, slug: string): unknown => ({ text, href: `/c/${OVERVIEW_CLUSTER}/${slug}` });
  const long = 'prometheusrules.monitoring.coreos.example-platform.internal.k8s.io';
  const custom = { key: 'custom-resources', label: 'Custom Resources', deferred: true, items: [] };
  return {
    ...OVERVIEW_ANSWERS,
    '/api/me/kinds': { showUnused: true, favourites: { [OVERVIEW_CLUSTER]: favourites } },
    [path]: { ...nav, data: { ...nav.data, groups: [...nav.data.groups, custom] } },
    [`${path}/custom-resources`]: {
      state: 'ok',
      data: {
        ...custom,
        deferred: false,
        groups: [
          { key: 'cert-manager.io', label: 'cert-manager.io', deferred: false, items: [kind('Certificates', 'certificates.cert-manager.io'), kind('ClusterIssuers', 'clusterissuers.cert-manager.io')] },
          { key: long, label: long, deferred: false, items: [kind('AlertmanagerConfigs', `alertmanagerconfigs.${long}`), kind('ServiceMonitorsForEveryNamespaceInTheCluster', `servicemonitors.${long}`)] },
          { key: 'gateway.networking.k8s.io', label: 'gateway.networking.k8s.io', deferred: false, items: [kind('Gateways', 'gateways.gateway.networking.k8s.io')] },
        ],
      },
    },
  };
}

/** The menu's answer with its discovery state set. */
function withDiscovery(answers: Readonly<Record<string, unknown>>, discovery: { state: string; detail: string }): Readonly<Record<string, unknown>> {
  const path = `/api/c/${OVERVIEW_CLUSTER}/nav`;
  const nav = answers[path] as { state: string; data: object };
  // With occupancy answered, so the Show unused kinds toggle the mark sits beside is drawn.
  const occupancy = {
    state: 'ok',
    data: { kinds: { pods: { state: 'some' }, deployments: { state: 'some' }, cronjobs: { state: 'none' }, ingresses: { state: 'none' } } },
  };
  return { ...answers, [path]: { ...nav, data: { ...nav.data, discovery } }, [`${path}/occupancy`]: occupancy };
}

/** A console scene may carry extras (`extensions`) and what their routes
 *  under `/x/` answer (`extra`, by path and query). */
const CONSOLES: Readonly<
  Record<string, { at: string; answers: Readonly<Record<string, unknown>>; extensions?: readonly Extension[]; extra?: Readonly<Record<string, ExtraAnswer>> }>
> = {
  overview: { at: `/c/${OVERVIEW_CLUSTER}`, answers: OVERVIEW_ANSWERS },
  // Four extras suggested at once: the one grouped line above the page.
  'suggestions-group': {
    at: `/c/${OVERVIEW_CLUSTER}`,
    answers: { ...OVERVIEW_ANSWERS, [`/api/c/${OVERVIEW_CLUSTER}/extensions/suggestions`]: SUGGESTED },
  },
  // One or two suggested extras: a line each, with its own Turn on.
  'suggestions-two': {
    at: `/c/${OVERVIEW_CLUSTER}`,
    answers: { ...OVERVIEW_ANSWERS, [`/api/c/${OVERVIEW_CLUSTER}/extensions/suggestions`]: { ...SUGGESTED, items: SUGGESTED.items.slice(0, 2) } },
  },
  // Every suggested extra pinned or already on: the server answers an empty list, and no line is drawn.
  'suggestions-none': {
    at: `/c/${OVERVIEW_CLUSTER}`,
    answers: { ...OVERVIEW_ANSWERS, [`/api/c/${OVERVIEW_CLUSTER}/extensions/suggestions`]: { state: 'empty', items: [] } },
  },
  // Settings > Extra over two clusters: the rail's icons switch the cluster.
  'settings-switch': {
    at: `/settings/extra?cluster=${OVERVIEW_CLUSTER}`,
    answers: {
      ...OVERVIEW_ANSWERS,
      '/api/settings': { info: {} },
      [`/api/c/${OVERVIEW_CLUSTER}/extensions`]: { state: 'ok', items: SETTINGS_EXTRA_ITEMS },
      '/api/c/staging/extensions': {
        state: 'ok',
        items: SETTINGS_EXTRA_ITEMS.map((item, index) => ({ ...item, enabled: index % 2 === 0 ? !item.enabled : item.enabled })),
      },
    },
  },
  // A newer release and the desktop's installer at each step (#48): Settings ›
  // Infos with its banner, and the overview with the badge on Settings.
  ...Object.fromEntries(
    Object.entries(UPDATE_STEPS).map(([phase, install]) => [
      `console-update-${phase}`,
      { at: '/settings/infos', answers: { ...OVERVIEW_ANSWERS, ...updateSettings(install) } },
    ]),
  ),
  // Welcome and What's new (#80): a first visit, an upgrade across two
  // releases with highlights, and a release with nothing notable.
  ...Object.fromEntries(
    Object.entries(whatsNewAnswers(OVERVIEW_ANSWERS['/api/capabilities'] as Readonly<Record<string, unknown>>)).map(
      ([state, answers]) => [`console-whats-new-${state}`, { at: `/c/${OVERVIEW_CLUSTER}`, answers: { ...OVERVIEW_ANSWERS, ...answers } }],
    ),
  ),
  'console-update-badge': {
    at: `/c/${OVERVIEW_CLUSTER}`,
    answers: { ...OVERVIEW_ANSWERS, ...updateSettings(UPDATE_STEPS.idle) },
  },
  // The kinds column with kinds that hold nothing, so its Show / Hide unused
  // kinds toggle is drawn (#60).
  'console-unused-kinds': {
    at: `/c/${OVERVIEW_CLUSTER}`,
    answers: {
      ...OVERVIEW_ANSWERS,
      [`/api/c/${OVERVIEW_CLUSTER}/nav/occupancy`]: {
        state: 'ok',
        data: {
          kinds: {
            pods: { state: 'some' },
            deployments: { state: 'some' },
            jobs: { state: 'none' },
            cronjobs: { state: 'none' },
            persistentvolumeclaims: { state: 'none' },
          },
        },
      },
    },
  },
  'overview-unmeasured': { at: `/c/${OVERVIEW_CLUSTER}`, answers: OVERVIEW_UNMEASURED_ANSWERS },
  // A Prometheus with no usable cAdvisor series and an empty leaderboard, as
  // an in-cluster console can answer.
  'overview-quiet': { at: `/c/${OVERVIEW_CLUSTER}`, answers: OVERVIEW_QUIET_ANSWERS },
  // The in-cluster console on the same cluster.
  'in-cluster': { at: `/c/${OVERVIEW_CLUSTER}`, answers: IN_CLUSTER_ANSWERS },
  // Its session in its last minutes: the warning beside the name.
  'in-cluster-ending': { at: `/c/${OVERVIEW_CLUSTER}`, answers: IN_CLUSTER_ENDING_ANSWERS },
  // The loopback desktop with one context: no identity, a plain Settings.
  'desktop-one': { at: `/c/${OVERVIEW_CLUSTER}`, answers: DESKTOP_ONE_ANSWERS },
  names: { at, answers: LONG_NAME_ANSWERS },
  // The kinds column, Custom Resources with long API-group and kind names.
  'menu-kinds': { at: `/c/${OVERVIEW_CLUSTER}`, answers: menuKindsAnswers() },
  // The same column while discovery is still running, and after it failed:
  // an icon beside Show unused kinds, never a sentence.
  'menu-discovery-pending': {
    at: `/c/${OVERVIEW_CLUSTER}`,
    answers: withDiscovery(menuKindsAnswers(), { state: 'pending', detail: 'Discovering the kinds this cluster serves' }),
  },
  'menu-discovery-failed': {
    at: `/c/${OVERVIEW_CLUSTER}`,
    answers: withDiscovery(menuKindsAnswers(), { state: 'failed', detail: 'Discovery could not read metrics.k8s.io/v1beta1' }),
  },
  'console-pods': { at: PODS_PATH, answers: podsAnswers(false) },
  'console-pods-arranged': { at: PODS_PATH, answers: podsAnswers(true) },
  // The same list with its "+" permitted: where it sits,
  // and that it gives the corner to the selection bar.
  'console-pods-create': { at: PODS_PATH, answers: podsAnswers(false, true) },
  // One cluster, so no rail: the list under the Kinds button, with a "+" and with none.
  'one-cluster-pods-create': {
    at: PODS_PATH,
    answers: {
      ...podsAnswers(false, true),
      '/api/capabilities': IN_CLUSTER_ANSWERS['/api/capabilities'],
      '/api/clusters': IN_CLUSTER_ANSWERS['/api/clusters'],
    },
  },
  'one-cluster-pods': {
    at: PODS_PATH,
    answers: {
      ...podsAnswers(false, false),
      '/api/capabilities': IN_CLUSTER_ANSWERS['/api/capabilities'],
      '/api/clusters': IN_CLUSTER_ANSWERS['/api/clusters'],
    },
  },
  // The same list with a pod's drawer open on arrival: at 1024 the page is
  // what the drawer leaves of it.
  // The longest common statuses, one a row, for the pill's width.
  // 5000 pods: every one is listed, the grid draws the rows in view.
  'console-pods-many': { at: `/c/${OVERVIEW_CLUSTER}/pods`, answers: podsManyAnswers() },
  'console-pods-statuses': { at: `/c/${OVERVIEW_CLUSTER}/pods`, answers: podsLongStatusAnswers() },
  'console-pods-open': { at: `${PODS_PATH}&open=${encodeURIComponent('observability/prometheus-0')}`, answers: podsAnswers(false) },
  // Arranged, with the drawer open: a sized table in a narrow panel.
  'console-pods-arranged-open': {
    at: `${PODS_PATH}&open=${encodeURIComponent('observability/prometheus-0')}`,
    answers: podsAnswers(true),
  },
  'console-ingresses': { at: INGRESS_PATH, answers: ingressAnswers() },
  // A pod taller than the window, in the drawer and as a page, over a list
  // longer than one: only the drawer body and the list's
  // own panel scroll, never the document.
  'console-pod-tall-open': { at: PODS_TALL_DRAWER, answers: podsTallAnswers() },
  'console-pod-tall-page': { at: PODS_TALL_PAGE, answers: podsTallAnswers() },
  // A pod, a service and a secret open in the drawer, each with its whole tab
  // strip and its kind's verbs: what the drawer's narrowest width is measured
  // on.
  'console-drawer-pods': { at: drawerPath('pods'), answers: drawerAnswers() },
  'console-drawer-services': { at: drawerPath('services'), answers: drawerAnswers() },
  'console-drawer-secrets': { at: drawerPath('secrets'), answers: drawerAnswers() },
  // Two more lists with short headings over short values, for the headings'
  // width at 900px.
  'console-deployments': { at: DEPLOYMENTS_PATH, answers: listsAnswers() },
  'console-clusterroles': { at: CLUSTER_ROLES_PATH, answers: listsAnswers() },
  // The Pods list with Security Center on: its column, read once for the list.
  'console-pods-security': {
    at: `/c/${OVERVIEW_CLUSTER}/pods`,
    answers: withExtraOn(podsAnswers(false), 'security', 'Security Center'),
    extensions: [security],
    extra: { [`/x/${OVERVIEW_CLUSTER}/security/cells?kind=pods`]: securityPodCells(OVERVIEW_CLUSTER) },
  },
  // The Activity tab of a crash-looping pod, with Event Intelligence off and on.
  'console-activity-off': {
    at: activityPath(OVERVIEW_CLUSTER),
    answers: activityAnswers(OVERVIEW_CLUSTER, OVERVIEW_ANSWERS),
    extensions: [eventsExtra],
    extra: ACTIVITY_EXTRA(OVERVIEW_CLUSTER) as Readonly<Record<string, ExtraAnswer>>,
  },
  'console-activity-on': {
    at: activityPath(OVERVIEW_CLUSTER),
    answers: withExtraOn(activityAnswers(OVERVIEW_CLUSTER, OVERVIEW_ANSWERS), 'events', 'Event Intelligence'),
    extensions: [eventsExtra],
    extra: ACTIVITY_EXTRA(OVERVIEW_CLUSTER) as Readonly<Record<string, ExtraAnswer>>,
  },
  // The Deployments list with Pod Debugging on: its Pending column.
  'console-deployments-pending': {
    at: DEPLOYMENTS_PATH,
    answers: withExtraOn(listsAnswers(), 'pending', 'Pod Debugging'),
    extensions: [pending],
    extra: { [`/x/${OVERVIEW_CLUSTER}/pending/cells?kind=deployments`]: pendingDeploymentCells(OVERVIEW_CLUSTER) },
  },
};
const consoleScene = CONSOLES[scene];
if (consoleScene !== undefined) answerFromFixture(consoleScene.answers);
if (consoleScene?.extra !== undefined) answerExtra(consoleScene.extra);

/* -------------------------------------------------------------------------- */
/* The Redis viewer's key panel - a hash with a very long field name          */
/* -------------------------------------------------------------------------- */

/**
 * The Redis viewer's instance page, on its own routes, with a hash whose
 * first field name is far longer than the table: the panel caps a field to
 * one line within about 40 % of the table, so the value keeps the rest.
 * `measure/redis.spec.ts` opens the key and measures the cell.
 *
 * The extra's routes are POSTs under `/x/…`, not `/api/…`, so they get their
 * own answers here rather than `answerFromFixture`'s.
 */
const REDIS_LONG_FIELD = `${'session-attribute-with-a-very-long-name-'.repeat(8)}end`;
const REDIS_AT = '/c/demo/x/redis/cache/redis-master/6379';
const REDIS_ROUTE = '/x/demo/redis/instances/cache/redis-master/6379';

function answerRedis(): void {
  const real = window.fetch.bind(window);
  const instance = {
    instance: { namespace: 'cache', service: 'redis-master', port: 6379, why: 'the port is named tcp-redis' },
    pod: 'redis-master-0',
    db: 0,
    server: { engine: 'Redis', version: '7.4.1', mode: 'standalone', databases: 16 },
    overview: { keys: 1, keyspace: [{ index: 0, keys: 1, expires: 0 }] },
    scan: { match: '', cursor: '0', done: true, keys: [{ name: 'session:1', type: 'hash', ttl: 'no expiry' }] },
  };
  const key = {
    ...instance,
    key: {
      name: 'session:1',
      type: 'hash',
      ttl: 'no expiry',
      length: 3,
      pageSize: 100,
      entries: [
        { field: REDIS_LONG_FIELD, value: 'a short value' },
        { field: 'user', value: '42' },
        { field: 'prefs', value: '{"theme":"dark","lang":"en"}' },
      ],
    },
  };
  window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, window.location.href);
    if (!url.pathname.startsWith('/x/') && answers[url.pathname] === undefined) return real(input, init);
    const body = url.pathname === `${REDIS_ROUTE}/key` ? key : url.pathname === REDIS_ROUTE ? instance : undefined;
    return Promise.resolve(
      new Response(JSON.stringify(body ?? { code: 'absent', kind: 'absent', message: `no answer for ${url.pathname}` }), {
        status: body === undefined ? 404 : 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  };
}

function RedisScene(): ReactNode {
  const [router] = useState(() => createMemoryRouter([{ path: 'c/:cluster', children: redis.routes }], { initialEntries: [REDIS_AT] }));
  return <RouterProvider router={router} />;
}
if (scene === 'redis-hash') answerRedis();
if (scene === 'pvc-storage') answerFromFixture(STORAGE_ANSWERS);

/**
 * Settings > Extra writes, answered as the console answers them: an address
 * that is not http or https is a 400 with the setting's own refusal, and
 * anything else is kept (204).
 */
function answerSettingsWrite(): void {
  const real = window.fetch.bind(window);
  window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, window.location.href);
    if ((init?.method ?? 'GET') !== 'PUT' || !/\/extensions\/[^/]+\/settings\//.test(url.pathname)) return real(input, init);
    const text = (JSON.parse(String(init?.body ?? '{}')) as { text?: string }).text ?? '';
    if (/^https?:\/\/[^/]+/.test(text) || text === '' || !url.pathname.endsWith('-url')) {
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    return Promise.resolve(
      new Response(
        JSON.stringify({
          type: 'about:blank',
          title: 'Bad request',
          status: 400,
          code: 'invalid_request',
          detail: 'invalid request: that value cannot be used: the address must start with http:// or https://',
        }),
        { status: 400, headers: { 'Content-Type': 'application/problem+json' } },
      ),
    );
  };
}
if (scene.startsWith('settings-extra')) answerSettingsWrite();

/* -------------------------------------------------------------------------- */
/* Each shipped extra's main page, for Settings > Extra's preview             */
/* -------------------------------------------------------------------------- */

/**
 * `?scene=extra-<key>`: the page the extra's `nav.path` opens, on its own
 * routes as the console mounts them, answered from `extras.fixture.ts`.
 * `shot-extras.mjs` photographs each into `app/extras/<key>/assets/preview.png`.
 *
 * The extras' routes live under `/x/…` and are answered by path and query
 * together, whatever the method (a Redis probe is a POST). Anything unnamed is
 * a 404 problem, never something plausible, so a page asking for more than
 * the fixture holds shows it as a failure in the picture.
 */
function answerExtra(answers: Readonly<Record<string, ExtraAnswer>>): void {
  const real = window.fetch.bind(window);
  window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, window.location.href);
    if (!url.pathname.startsWith('/x/') && answers[url.pathname] === undefined) return real(input, init);
    const answer = answers[url.pathname + url.search];
    const headers = { 'Content-Type': 'application/json' };
    if (answer === undefined) {
      return Promise.resolve(
        new Response(JSON.stringify({ code: 'absent', message: `no fixture for ${url.pathname}${url.search}` }), { status: 404, headers }),
      );
    }
    if (answer instanceof ExtraProblem) {
      return Promise.resolve(new Response(JSON.stringify(answer.body), { status: answer.status, headers }));
    }
    return Promise.resolve(new Response(JSON.stringify(answer), { status: 200, headers }));
  };
}

/** Where an extra's scene opens: its `nav.path`, another of its pages
 *  (`at`, relative to `/c/:cluster`), one of its tabs in a panel's width, or
 *  the shortcut tile it draws on an object's Details. A tab named by `key` is
 *  drawn alone in a `data-shot` frame; one named by its kind, in a panel. */
interface ExtraAt {
  at?: string | undefined;
  tab?: { key?: string; kind: string; namespace: string; name: string };
  shortcut?: { kind: string; namespace: string; name: string };
}

function ExtraScene({ extensionKey, at, tab, shortcut }: { extensionKey: string } & ExtraAt): ReactNode {
  const extension = extras.find((each) => each.key === extensionKey);
  if (extension === undefined) throw new Error(`no extra ${extensionKey}`);
  const Tab =
    tab === undefined
      ? undefined
      : extension.tabs?.find((each) => (tab.key === undefined ? each.kinds.includes(tab.kind) : each.key === tab.key))
          ?.Component;
  const [router] = useState(() =>
    createMemoryRouter(
      [
        {
          path: 'c/:cluster',
          children:
            shortcut !== undefined
              ? [
                  {
                    index: true,
                    element: (
                      <ShellProvider extensions={[extension]}>
                        <div data-shot="" className="w-96 max-w-full bg-bg p-4">
                          <Shortcuts
                            identity={{ cluster: EXTRA_CLUSTER, ...shortcut }}
                            marks={{ labels: {}, annotations: {} }}
                          />
                        </div>
                      </ShellProvider>
                    ),
                  },
                ]
              : Tab === undefined || tab === undefined
              ? extension.routes
              : [
                  {
                    index: true,
                    element: (
                      tab.key !== undefined ? (
                        <Suspense>
                          <div data-shot="" className="max-w-3xl bg-bg p-3">
                            <Tab cluster={EXTRA_CLUSTER} kind={tab.kind} namespace={tab.namespace} name={tab.name} />
                          </div>
                        </Suspense>
                      ) : (
                        <div className="ml-auto min-h-screen w-full max-w-lg border-l border-line bg-panel p-3">
                          <Suspense fallback={null}>
                            <Tab cluster={EXTRA_CLUSTER} kind={tab.kind} namespace={tab.namespace} name={tab.name} />
                          </Suspense>
                        </div>
                      )
                    ),
                  },
                ],
        },
      ],
      {
        // A scene may open the page on a question of its own (`at`).
        initialEntries: [
          Tab === undefined && shortcut === undefined
            ? `/c/${EXTRA_CLUSTER}/${at ?? extension.nav.path}`
            : `/c/${EXTRA_CLUSTER}/`,
        ],
      },
    ),
  );
  return (
    <div className="min-h-screen bg-bg text-fg">
      <IconSprite />
      <RouterProvider router={router} />
    </div>
  );
}

const extraScene = (EXTRA_SCENES as Readonly<Record<string, (typeof EXTRA_SCENES)[keyof typeof EXTRA_SCENES]>>)[scene];
if (extraScene !== undefined) answerExtra(extraScene.answers);

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={createQueryClient()}>
      <KeyboardStack>
        {consoleScene !== undefined ? (
          <ConsoleScene at={consoleScene.at} {...(consoleScene.extensions === undefined ? {} : { extensions: consoleScene.extensions })} />
        ) : scene === 'redis-hash' ? (
          <RedisScene />
        ) : extraScene !== undefined ? (
          <ExtraScene
            extensionKey={extraScene.key}
            {...('at' in extraScene ? { at: extraScene.at } : {})}
            {...('tab' in extraScene ? { tab: extraScene.tab } : {})}
            {...('shortcut' in extraScene ? { shortcut: extraScene.shortcut } : {})}
          />
        ) : (
        <MemoryRouter initialEntries={['/c/demo']}>
          <Routes>
            <Route
              path="/c/:cluster"
              element={
                <DockProvider>
                  <Scene />
                </DockProvider>
              }
            />
          </Routes>
        </MemoryRouter>
        )}
      </KeyboardStack>
    </QueryClientProvider>
  </StrictMode>,
);
