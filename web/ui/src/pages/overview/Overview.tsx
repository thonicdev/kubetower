import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router';

import { ApiError, isFailedState, type ListResult, type ValueResult } from '../../api';
import { ClusterName } from '../../components/ClusterName';
import { Icon } from '../../components/Icon';
import { focusRing, focusRingInside } from '../../focus';
import { glyphForKind } from '../../shell/kindGlyph';
import { DOWN, useClusterVerdict } from '../../shell/downCluster';
import { useDeclarePageFacts } from '../../shell/pageFacts';
import { StateNotice } from '../../state/StateNotice';
import { ValueView } from '../../state/Result';
import {
  refusedPage,
  spoilt,
  useOverview,
  type AlertEntry,
  type CapacityCell,
  type CountEntry,
  type EventEntry,
  type OverviewPage,
  type RestartEntry,
  type TopBoard,
  type TroubleEntry,
} from './data';
import { InitMark, PanelTable, PhaseBar, Pill, Sparkline, unchartable } from './Bits';
import { ClusterDown } from './ClusterDown';
import { ReadingText } from './Reading';
import { CapacityDonut } from './Donut';
import { Paged } from './Paged';
import { ExtraTiles } from './ExtraTiles';
import { FavouriteExtras } from './FavouriteExtras';
import { Panel, PanelList, PanelMark, PANEL_FRAME, PANEL_HEAD } from './Panel';
import { PanelValue } from './PanelValue';
import { RefLink } from './RefLink';
import { cleanMessage } from '../../components/FoldedMessage';
import { bodyCell } from '../../components/tableLook';
import { formatInstant } from '../../components/instant';

/**
 * The cluster dashboard, at `/c/:cluster`.
 *
 * ## The panels
 *
 * The page carries the cluster head with its alerts and its charts, then the
 * workloads, then what is wrong, then the events, then what could not be read.
 * A dashboard of descriptions alone is a page that is never wrong and never
 * useful, so it also carries the things to act on: node alerts, workloads in
 * trouble, restarting containers, the phase breakdown and the history.
 *
 * **There is no per-node table.** A table with one row per node would repeat the
 * Nodes page, which the head's pill leads to; the node alerts in the head are
 * what keeps a sick machine impossible to miss here.
 *
 * ## One request, nine answers
 *
 * The page is a value envelope and each panel is an envelope of its own. **A
 * panel that failed is drawn, in its place, with the server's own sentence, and
 * the other eight are drawn beside it** - the property `api/openapi.yaml`
 * composed the envelope for, and the one that is lost the moment somebody
 * renders from `data.counts.items` directly. `data.ts` narrows all nine before
 * the page sees any of them.
 *
 * ## What it deliberately does not do
 *
 * **It draws no all-well badge.** `allWell` is the server's judgment over
 * both halves of the page; a client computing it from whichever panels it
 * happened to receive would raise it on a page with two panels missing.
 *
 * **A metrics source with nothing to say draws nothing.** No Prometheus, or one
 * holding no series either history line can be charted from, is no *Over time*
 * section; an empty or absent leaderboard is no *Most used* panel. A read that
 * failed stays drawn, short: empty and failed remain different answers, and
 * different does not mean louder.
 *
 * **Its sparklines do not open.** `getMetricsPanel` requires `kind` and `name`,
 * so there is no way to re-ask for a cluster's own series at a wider window; the
 * server therefore sends `zoomable: false` and this page honors it rather than
 * offering an overlay that would open onto nothing.
 *
 * ## An unreachable cluster is one warning and a Refresh
 *
 * Where the probe's verdict on this cluster is `unreachable`
 * (`shell/downCluster.ts`), however old, the page reads nothing - no overview, no
 * menu - and is `ClusterDown`: the connection problem in the server's words and
 * a Refresh that re-probes this cluster. A verdict older than thirty seconds is
 * re-probed once on arrival, with the warning already drawn. A cluster the list
 * still calls up whose overview read is refused as `unreachable` is drawn the
 * same way, rather than as a notice, a pointer and eleven panels each repeating
 * it. Every other refusal - `forbidden` among them - draws the notice and the
 * panels, each in the refusal's own state.
 */
export function Overview(): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  const { settled, down, fresh } = useClusterVerdict(cluster);
  const overview = useOverview(cluster, settled && down === undefined);
  // The tab title and the breadcrumb's last segment. A page that declares
  // nothing leaves the window titled "KubeTower" alone, which is a taskbar of
  // identical entries.
  //
  // **Not "Overview" on a cluster drawn as down**: the page there is the warning
  // and nothing of the dashboard, and a tab and a breadcrumb reading Overview
  // over it would suggest a view of a cluster that cannot be read.
  const answer = overview.data;
  const refusedDown =
    answer !== undefined && answer.state === DOWN && cluster !== undefined;
  useDeclarePageFacts({ title: down !== undefined || refusedDown ? 'Unreachable' : 'Overview' });

  // A context in the URL that this console does not serve.
  //
  // The API answers 404 with a problem object, and a problem object is not an
  // envelope - `ValueView` would draw it as "The request failed (404)", which
  // is true and useless. "There is no such cluster" is a different sentence
  // from "the cluster did not answer", and that difference is what this product
  // is organized around, so it is said here rather than left to the generic arm.
  if (overview.error instanceof ApiError && overview.error.status === 404) {
    return (
      <div className="p-4">
        <StateNotice
          state="absent"
          reason={{
            code: 'no_such_kind',
            message: 'This console is not configured for that context.',
            scope: cluster === undefined ? {} : { cluster },
          }}
        />
      </div>
    );
  }

  // The cluster's own refusal - the outer envelope on its failure arm, which is
  // what the server answers when the node list, the page's proof that the
  // cluster answers at all, could not be read.
  const refused =
    answer !== undefined && answer.state !== 'ok' && isFailedState(answer.state)
      ? { state: answer.state, reason: answer.reason }
      : undefined;

  if (down !== undefined) {
    // However old the verdict: an old one is checked by the probe, once, while
    // the warning is already on screen (`check`), never by a loading dashboard.
    return (
      <div className="p-4">
        <ClusterDown key={down.name} cluster={down.name} state={down.state} reason={down.reason} remedy={down.remedy} check={!fresh} />
      </div>
    );
  }
  // Only `unreachable` collapses. A `forbidden` overview is this person's
  // permissions, which a Refresh that re-probes connectivity cannot change, so
  // it keeps the permission notice below.
  if (cluster !== undefined && refused !== undefined && refused.state === DOWN) {
    return (
      <div className="p-4">
        <ClusterDown key={cluster} cluster={cluster} state={refused.state} reason={refused.reason} />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4 p-4">
      {/*
        **The refusal is the first thing on the page.** `ValueView` draws the
        notice on a failed envelope and the dashboard on an answered one, and it
        comes before everything else in both cases, so the first thing read on a
        cluster that refuses is the refusal. The heading is not lost - it is
        `ClusterHead`'s, drawn below, on the page composed from the refusal.
      */}
      <ValueView query={overview} what="the dashboard">
        {(page) => <Dashboard key={cluster} page={page} cluster={cluster} onRetry={() => void overview.refetch()} />}
      </ValueView>

      {refused === undefined ? null : (
        <>
          <RefusalNote />
          {/*
            And the panels, each in the refusal's own state, rather than a
            blank. `refusedPage` says why. No retry on them: the notice above
            already offers the one retry there is, and nine buttons that all
            re-read the same page are nine copies of one control.
          */}
          <Dashboard
            key={cluster}
            page={refusedPage(cluster ?? 'Cluster', refused.state, refused.reason)}
            onRetry={undefined}
          />
        </>
      )}
    </div>
  );
}

/**
 * The line under the cluster's own refusal.
 *
 * What lands in the notice above is the API server's own words - `Unauthorized`,
 * `certificate signed by unknown authority`, `no such host` - and on their own
 * they leave someone who signed in successfully with nothing to do next. This
 * says whose refusal it is: the cluster's, not the console's sign-in.
 *
 * It carries no link: the sentence is complete without one.
 */
function RefusalNote(): ReactNode {
  return (
    <p className="text-xs text-muted">
      This is the cluster&apos;s own refusal and not KubeTower&apos;s front door: signing in here
      grants the console, never a cluster.
    </p>
  );
}

/** A way to re-read the page, or none: the dashboard composed from a refusal
 *  offers its one retry in the notice above it. */
type Retry = (() => void) | undefined;

/** `cluster` is set on the page the cluster answered, never on the one
 *  composed from its refusal: the extensions' tiles read the cluster too. */
function Dashboard({ page, cluster, onRetry }: { page: OverviewPage; cluster?: string | undefined; onRetry: Retry }): ReactNode {
  return (
    <div className="flex flex-col gap-4">
      <ClusterHead page={page} onRetry={onRetry} />
      {cluster === undefined ? null : <FavouriteExtras cluster={cluster} />}
      <AccessNote page={page} />
      <Workloads page={page} onRetry={onRetry} />
      {cluster === undefined ? null : <ExtraTiles cluster={cluster} />}
      <Trouble page={page} onRetry={onRetry} />
      <Restarting page={page} onRetry={onRetry} />
      <Leaderboards page={page} onRetry={onRetry} />
      <Warnings page={page} onRetry={onRetry} />
      <NotShown page={page} />
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* 1. The cluster head: the badge, the alerts, the charts                      */
/* -------------------------------------------------------------------------- */

function ClusterHead({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  // Two of this section's three readings can be spoilt on their own: the version
  // comes from the discovery call and the capacity from the pod list, and either
  // can fail while the other answers. `PanelMark` is used directly here because
  // neither is a `Panel` - the head is one section holding three things.
  const versionFailed = spoilt(page, 'version');
  const capacityFailed = spoilt(page, 'capacity');

  return (
    <section className={PANEL_FRAME}>
      {/* The same head as every `Panel`, with the cluster's own glyph and its
          name one step up the scale: it is the page's title and not one panel's.
          `flex-wrap` because the badge and the stamp are on this row
          too, and at a narrow width they fold under the name. */}
      <div className={`${PANEL_HEAD} flex-wrap`}>
        <Icon name="cluster" className="text-muted" />
        {/* The cluster's name on one line, cut in the middle where the row has
            no room - an EKS ARN is 80-odd characters. `min-w-0` so the
            heading can shrink inside the wrapping row rather than push it. */}
        <h1 className="flex min-w-0 max-w-full text-2xl font-semibold">
          <ClusterName name={page.cluster} />
        </h1>

        {page.nodesSummary === undefined ? null : (
          <Pill tone={page.nodesTone}>
            <RefLink reference={page.nodesSummary} ring={focusRingInside} />
          </Pill>
        )}

        {/* No all-well pill: a cluster with something to act on says so in the
            nodes pill, the alerts and the panels below, never in a badge.
            `allWell` stays in the contract; this page does not read it. */}

        {/* What the cluster is running.

            **"unknown" is not the same answer as "the call failed"**: the mark is
            the second.

            No read time is drawn. `collectedAt` stays in the contract; the server
            dates the page's other ages against it. */}
        <span className="ml-auto flex items-baseline gap-2 text-xs text-muted">
          <span>
            {page.version === undefined ? 'Kubernetes version unknown' : `Kubernetes ${page.version}`}
          </span>
          <PanelMark spoilt={versionFailed} />
        </span>
      </div>

      <div className="flex flex-col gap-4 p-4">
        <Alerts page={page} onRetry={onRetry} />

        {/*
          **Capacity first and across the whole width.** The three rings are the
          reading this page is opened for, so they head the page at full width
          rather than sharing half a panel with the history, which is a row of its
          own under them.

          The history keeps two columns rather than becoming three full-width
          charts, so each sparkline is drawn at a modest width.
        */}
        <div className="flex flex-col gap-2">
          <div className="flex items-baseline gap-2">
            <h2 className="text-xs font-medium text-muted uppercase">Capacity</h2>
            <PanelMark spoilt={capacityFailed} />
          </div>
          <PanelList
            result={page.capacity}
            what="capacity"
            onRetry={onRetry}
            spoilt={capacityFailed.length > 0}
            empty={<StateNotice state="empty" />}
          >
            {(cells) => (
              // A grid and not a wrapping flex row: the tiles are the same
              // width whatever their readings measure, so three rings line up
              // across the page instead of drifting with the length of
              // `1.2 cores` against `31.4 GiB`.
              <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {cells.map((cell, index) => (
                  <li key={`${cell.label}:${String(index)}`}>
                    <Capacity cell={cell} />
                  </li>
                ))}
              </ul>
            )}
          </PanelList>
        </div>

        <History page={page} onRetry={onRetry} />
      </div>
    </section>
  );
}

/**
 * The cluster's own series, or nothing at all.
 *
 * **Nothing is drawn where there is nothing to chart from.** A cluster whose
 * Prometheus holds no container or pod cAdvisor series would otherwise draw a
 * filler sentence over a four-line caption. There are two such answers, and both
 * draw no heading, no line, no caption:
 *
 *   - `absent` with `not_installed`: no metrics backend in this build, or a
 *     cluster that keeps no series (metrics-server and no Prometheus);
 *   - `ok` where every line is `unchartable`: a Prometheus answered and holds
 *     neither shape of either counter.
 *
 * **What stays, and short.** A failed read is not an absence: `unreachable`,
 * `unreadable`, `forbidden` keep their notice, as every panel on this page
 * does (both range queries failing arrives here as a failure, not as
 * `absent`). A line whose own read failed says *not read* with
 * the reason on hover; an empty window over series that exist keeps *No
 * samples in this window.*. Where no line drew, the caption only repeats what
 * the lines already say, so it moves to a hover on the heading; where a line
 * drew, it stays in view, because it is the line's source (a pod-cgroup sum must
 * not be read as the container sum).
 */
function History({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  const history = page.history;
  if (refusedWithCapacity(page, history)) return null;
  if (history?.state === 'absent' && history.reason.code === 'not_installed') return null;
  if (history?.state === 'ok' && history.data.series.every(unchartable)) return null;

  const drew =
    history?.state === 'ok' &&
    history.data.series.some((series) => series.data.values.some((value) => value !== null && Number.isFinite(value)));
  const caption = history?.state === 'ok' ? history.data.caption : undefined;

  return (
    <div className="flex flex-col gap-2">
      <h2 className="text-xs font-medium text-muted uppercase" title={drew ? undefined : caption}>
        Over time
      </h2>
      <PanelValue result={history} what="the cluster's history" onRetry={onRetry}>
        {(panel) => (
          <div className="grid gap-3 lg:grid-cols-2">
            {panel.series
              .filter((series) => !unchartable(series))
              .map((series) => (
                <Sparkline key={series.key} panel={{ ...panel, series: [series] }} label={series.label} />
              ))}
            {!drew || panel.caption === undefined ? null : (
              <p className="text-xs text-muted lg:col-span-2">{panel.caption}</p>
            )}
          </div>
        )}
      </PanelValue>
    </div>
  );
}

/**
 * Whether a panel of the cluster head failed with the capacity's own failure.
 *
 * The capacity, the node alerts and the history are all read from the
 * machines, and a person who may not list nodes is refused all three with one
 * sentence, scoped to the node list. It is said once, on the capacity; the
 * other two draw nothing rather than repeating it. A failure of their own - a
 * different state or a different sentence - is still drawn where it happened,
 * and so is the page composed from a refusal of the whole dashboard, whose
 * failure is not the node list's.
 */
function refusedWithCapacity(
  page: OverviewPage,
  result: ListResult<unknown> | ValueResult<unknown> | undefined,
): boolean {
  const capacity = page.capacity;
  if (capacity === undefined || capacity.state === 'ok' || capacity.state === 'empty') return false;
  if (result === undefined || result.state === 'ok' || result.state === 'empty') return false;
  if (!isFailedState(capacity.state) || capacity.reason.scope?.resource !== 'nodes') return false;
  return (
    result.state === capacity.state &&
    result.reason.code === capacity.reason.code &&
    result.reason.message === capacity.reason.message
  );
}

/**
 * Node conditions worth acting on, lifted out of the table.
 *
 * A red pill in the twenty-third row of a table is entirely missable, and a node
 * under MemoryPressure must be impossible to miss.
 *
 * The subject opens the machine. An alert that names a node and gives no way to
 * reach it makes the reader retype the name into a filter box, which is the
 * moment the alert stops being read.
 *
 * Nothing at all is drawn on the `empty` arm - a quiet cluster is not a page
 * with an empty alerts box on it - but a *failure* still is, because "we could
 * not look at the machines" and "the machines are fine" is exactly the pair this
 * product exists to keep apart.
 */
function Alerts({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  if (page.alerts === undefined || page.alerts.state === 'empty') return null;
  if (refusedWithCapacity(page, page.alerts)) return null;

  return (
    <PanelList result={page.alerts} what="the node alerts" onRetry={onRetry}>
      {(alerts) => (
        <ul className="flex flex-col gap-1">
          {alerts.map((alert, index) => (
            <li key={`${alert.subject.text}:${String(index)}`}>
              <Alert alert={alert} />
            </li>
          ))}
        </ul>
      )}
    </PanelList>
  );
}

function Alert({ alert }: { alert: AlertEntry }): ReactNode {
  return (
    <div className="flex flex-wrap items-baseline gap-2 rounded-md border border-line bg-panel-2 px-2 py-1.5 text-xs">
      <Pill tone={alert.tone}>
        <RefLink reference={alert.subject} ring={focusRingInside} />
      </Pill>
      <span>{alert.summary}</span>
      {alert.detail === undefined ? null : (
        // A sentence, so it wraps. The panel is not a table and there is no
        // `nowrap` to defeat here; `break-words` keeps one unbroken token from
        // widening the panel past the window.
        <span className="min-w-0 break-words text-muted">{alert.detail}</span>
      )}
    </div>
  );
}

/**
 * One resource: the ring, where it leads, and what is actually being used.
 *
 * **The ring is the dashboard's capacity donut**, drawn for CPU, memory and
 * pods. See `Donut.tsx`.
 *
 * **Requested and used are two different quantities from two different
 * sources**, and the ring is the first: requests against allocatable, which is
 * what decides whether the next pod schedules and is available without any
 * metrics backend at all. `used` is what the machines are using now - the
 * series' newest sample, or metrics-server's reading where there is no series
 * - and is drawn as the thin arc inside the ring; where
 * nothing measured it, it is a state and not a number, and `ReadingText` draws
 * the dash and the server's sentence.
 *
 * Drawing them as one figure under two labels would read as a cluster whose
 * requests match its consumption exactly, on every cluster, always - and an
 * over-requested cluster is the one thing this panel exists to show.
 */
function Capacity({ cell }: { cell: CapacityCell }): ReactNode {
  return (
    // A tile rather than a 144px column: it fills the grid cell it was given, so
    // the three line up across the panel whatever their readings measure.
    <div
      data-capacity={cell.label}
      className="flex h-full flex-col items-center gap-3 rounded-xl border border-line bg-panel-2 p-4"
    >
      {/* **The title row, and it leads to the machines.** The link is on the
          caption and never on the ring: an arc 13 units thick is not a pointer
          target. The destination is deliberate - these are sums over the
          machines, and the Nodes page is where the machines are, one by one.

          Above the shape rather than under it, which is the tile's hierarchy: what
          this is, then the shape, then the readings. */}
      <span className="text-center text-xs">
        {cell.ref === undefined ? `${cell.label} requested` : <RefLink reference={cell.ref} />}
      </span>

      {/* The ring, the percentage in its center, and the two named readings
          under it. Which arc is which is decided in `Donut.tsx` and written on
          the tile rather than left to the reader. */}
      <CapacityDonut cell={cell} />
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* 2. Workloads: the phase bar and the kind tiles                              */
/* -------------------------------------------------------------------------- */

function Workloads({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  // A kind whose list call failed is left out of the tiles entirely rather than
  // counted as zero - which is right, and leaves five tiles looking like the
  // whole inventory. The mark is what says they are five of six.
  const failed = spoilt(page, 'counts');

  return (
    <Panel
      title="Workloads"
      icon="workloads"
      note={page.workloadsSummary === undefined ? undefined : <RefLink reference={page.workloadsSummary} />}
      spoilt={failed}
    >
      <div className="flex flex-col gap-4">
        <PanelList
          result={page.phases}
          what="the pod phases"
          onRetry={onRetry}
          empty={<StateNotice state="empty" />}
        >
          {(phases) => <PhaseBar phases={phases} />}
        </PanelList>

        <PanelList
          result={page.counts}
          what="the workload kinds"
          onRetry={onRetry}
          spoilt={failed.length > 0}
          empty={<StateNotice state="empty" />}
        >
          {(kinds) => (
            <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
              {kinds.map((kind, index) => (
                <li key={`${kind.kind}:${String(index)}`}>
                  <KindTile entry={kind} />
                </li>
              ))}
            </ul>
          )}
        </PanelList>
      </div>
    </Panel>
  );
}

/**
 * One kind, as `healthy / total` with what the rest are.
 *
 * `2` alone is not a fact anybody acts on - `count` on its own answers a
 * question nobody asked. `healthy` is optional and **absent is not zero**: a kind with no notion of health draws the total alone rather than
 * `0 / 38`, which is the reassuring-direction failure at tile scale.
 */
function KindTile({ entry }: { entry: CountEntry }): ReactNode {
  const body = (
    <>
      {/* The kind's own glyph beside its label - the one the menu draws for
          the same kind, from `kindGlyph`, so a tile and the row it links to
          wear the same mark. */}
      <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted">
        <Icon name={glyphForKind(entry.kind)} />
        <span className="truncate">{entry.label}</span>
      </span>
      {/* The large number: `healthy` at the top of the scale and
          semibold, the total beside it one step down and muted, so the eye
          takes the figure that matters first and reads the denominator second.
          `leading-tight` because a 21px line at 1.5 is a tile mostly air. */}
      <span className="text-2xl leading-tight font-semibold tabular-nums">
        {entry.healthy === undefined ? (
          entry.count
        ) : (
          <>
            {entry.healthy}
            <span className="text-base font-normal text-muted"> / {entry.count}</span>
          </>
        )}
      </span>
      {/* "all healthy" is the page's sentence and not the server's: a server
          sending it on every healthy tile is the same string stored n times. */}
      <span className={`truncate text-xs ${entry.note === undefined ? 'text-muted' : toneText(entry.tone)}`}>
        {entry.note ?? 'all healthy'}
      </span>
    </>
  );

  return (
    <div
      data-kind={entry.kind}
      className="flex flex-col gap-1 rounded-xl border border-line bg-panel-2 px-3 py-2.5"
    >
      {entry.ref === undefined ? body : <RefLinkTile reference={entry.ref}>{body}</RefLinkTile>}
    </div>
  );
}

const TONE_TEXT: Readonly<Record<string, string>> = {
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
  muted: 'text-muted',
  normal: 'text-fg',
};

function toneText(tone: string | undefined): string {
  return TONE_TEXT[tone ?? 'normal'] ?? 'text-fg';
}

/**
 * A tile that is entirely a link.
 *
 * `RefLink` truncates a label into one line, which is right for a cell and wrong
 * for a tile with three. Same rule about the destination: a href that does not
 * begin with `/` is drawn as text, which closes `javascript:` as a vector.
 */
function RefLinkTile({
  reference,
  children,
}: {
  reference: { text: string; href?: string | undefined; hint?: string | undefined };
  children: ReactNode;
}): ReactNode {
  const href = reference.href;
  if (href === undefined || !href.startsWith('/')) {
    return <span className="flex flex-col gap-1">{children}</span>;
  }
  return (
    <Link to={href} title={reference.hint} className={`flex flex-col gap-1 rounded-xs hover:text-accent ${focusRing}`}>
      {children}
    </Link>
  );
}

/* -------------------------------------------------------------------------- */
/* 3. Not in the state it should be in                                         */
/* -------------------------------------------------------------------------- */

/**
 * **The panel the dashboard exists for.**
 *
 * A dashboard reporting 430 pods and a full capacity bar while eight Deployments
 * are unavailable is a reassuring page about a broken cluster. Nothing is drawn
 * on the `empty` arm; a failure is.
 *
 * **Since** is the fifth column: *What* is a sentence about the present, and
 * nothing else on the row says how long it has been so - the first thing a reader
 * needs to tell a Deployment mid-rollout from one broken since Tuesday. The rows
 * arrive worst first and, within a tone, oldest first; the server sorts, this
 * page draws.
 */
function Trouble({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  const failed = spoilt(page, 'trouble');

  // **Empty hides this panel, and a spoilt one may not be hidden.** A page with
  // nothing in trouble should not carry an empty panel saying so; a page whose
  // Deployment list failed has no idea whether anything is in trouble, and
  // removing the panel altogether states the reassuring half of that out loud.
  // Here the false reading would be the absence of a panel.
  if (page.trouble === undefined) return null;
  if (page.trouble.state === 'empty' && failed.length === 0) return null;

  return (
    <Panel title="Not in the state it should be in" icon="deployment" spoilt={failed}>
      <PanelList
        result={page.trouble}
        what="what is not in the state it should be in"
        onRetry={onRetry}
        spoilt={failed.length > 0}
      >
        {(all) => (
          <Paged items={all} what="Workloads not in the state they declare">
            {(rows) => (
          <PanelTable
            label="Workloads not in the state they declare"
            pad={TABLE_PAD}
            head={[
              { key: 'kind', text: 'Kind' },
              { key: 'namespace', text: 'Namespace' },
              { key: 'name', text: 'Name' },
              { key: 'what', text: 'What' },
              { key: 'since', text: 'Since' },
            ]}
          >
            {rows.map((row, index) => (
              <TroubleRow key={`${row.object.text}:${String(index)}`} row={row} />
            ))}
          </PanelTable>
            )}
          </Paged>
        )}
      </PanelList>
    </Panel>
  );
}

/**
 * A name cut at the end below `xl`, whole from it, and whole on hover at every
 * width: `RefLink`'s `fill` cuts to its box, and this is the box. 10rem is a
 * Kubernetes name of about 26 characters in the table's `text-xs`, which keeps
 * the part that tells two apart - `ledger-api-7d9f8b6c4-x2k…` - and leaves the
 * column that takes the rest a readable share at 1024px. Lifted at `xl`, where
 * the rest has hundreds of pixels to spare and a cut would hide the suffix for
 * nothing.
 */
const CUT_NAME = 'block max-w-40 xl:max-w-none';

function TroubleRow({ row }: { row: TroubleEntry }): ReactNode {
  return (
    <tr className="border-b border-line/50">
      <td className={bodyCell(false, TABLE_PAD)}>{row.kind === undefined ? '—' : <RefLink reference={row.kind} />}</td>
      <td className={bodyCell(false, TABLE_PAD)}>
        {row.scope === undefined ? (
          '—'
        ) : (
          <span className={CUT_NAME}>
            <RefLink reference={row.scope} fill />
          </span>
        )}
      </td>
      <td className={bodyCell(false, TABLE_PAD)}>
        <span className={CUT_NAME}>
          <RefLink reference={row.object} fill />
        </span>
      </td>
      {/*
        What takes what the other columns leave, cut at the end, whole on
        hover: `max-w-0` takes it out of the table's minimum width and `w-full`
        hands it the rest. A rollout's sentence can make this table wider than its
        wrapper at 1024px; its start is the part that says what is wrong.
      */}
      <td className={`w-full max-w-0 ${bodyCell(false, TABLE_PAD)}`}>
        <Pill tone={row.tone} title={row.detail}>
          {row.detail}
        </Pill>
      </td>
      {/*
        The same shape as the restarting panel's "When": the server's own age in
        the cell, because the question is "since Tuesday or since this morning";
        the exact instant in the tooltip, because the follow-up is "was that the
        03:00 deploy". The server dates a row from the object's own record of the
        transition - the contract says which record per kind - and a kind that
        keeps none arrives with neither member. That row draws the em dash and
        says why on hover. **Never "0s"**: this is the cell that tells a
        Deployment broken since Tuesday from one mid-rollout, and dating it from
        now would make them the same row.
      */}
      <td
        className={`${bodyCell(false, TABLE_PAD)} text-muted`}
        title={
          row.since === undefined
            ? 'this kind keeps no record of when it left the state it declares'
            : formatInstant(row.since, { year: true, seconds: true })
        }
      >
        {row.age ?? '—'}
      </td>
    </tr>
  );
}

/* -------------------------------------------------------------------------- */
/* 4. Restarting                                                               */
/* -------------------------------------------------------------------------- */

/**
 * The cheapest early signal there is, and one invisible in a phase breakdown: a
 * CrashLoopBackOff pod counts as Running.
 *
 * The pod name opens the pod.
 */
function Restarting({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  if (page.restarting === undefined || page.restarting.state === 'empty') return null;

  return (
    <Panel title="Restarting" icon="refresh">
      <PanelList result={page.restarting} what="the restarting containers" onRetry={onRetry}>
        {(all) => (
          <Paged items={all} total={page.restartingTotal} what="Containers that keep exiting">
            {(rows) => (
          <PanelTable
            label="Containers that keep exiting"
            pad={TABLE_PAD}
            head={[
              { key: 'namespace', text: 'Namespace' },
              { key: 'pod', text: 'Pod' },
              { key: 'container', text: 'Container' },
              { key: 'restarts', text: 'Restarts', numeric: true },
              { key: 'exit', text: 'Last exit' },
              { key: 'when', text: 'When' },
            ]}
          >
            {rows.map((row, index) => (
              <RestartRow key={`${row.object.text}:${row.container}:${String(index)}`} row={row} />
            ))}
          </PanelTable>
            )}
          </Paged>
        )}
      </PanelList>
    </Panel>
  );
}

/**
 * One row, and it says which of the kubelet's two lists the container came from.
 *
 * Init containers are listed on this panel, and the row carries a mark for them:
 * a crashing init container must not be indistinguishable from a crashing app
 * container, because the logs are in a different place.
 *
 * The mark rides on the Container cell rather than in a seventh column, and it
 * is deliberately toneless - `Bits.tsx`'s `InitMark` carries the whole argument.
 * The row's *grading* is the Restarts pill and stays the only thing on the row
 * wearing a color.
 */
function RestartRow({ row }: { row: RestartEntry }): ReactNode {
  return (
    <tr className="border-b border-line/50" data-init={row.init ? 'true' : 'false'}>
      {/*
        The same pattern as the trouble and warning tables: a 52-character pod
        name - `kube-prometheus-stack-prometheus-node-exporter-x2k9p` - can make
        this table wider than its wrapper at 1024px. The pod takes what the other
        columns leave and is cut at the end, whole on hover; the namespace and the
        container are cut below `xl`.
      */}
      <td className={bodyCell(false, TABLE_PAD)}>
        {row.scope === undefined ? (
          '—'
        ) : (
          <span className={CUT_NAME}>
            <RefLink reference={row.scope} fill />
          </span>
        )}
      </td>
      <td className={`w-full max-w-0 ${bodyCell(false, TABLE_PAD)}`}>
        <RefLink reference={row.object} fill />
      </td>
      <td className={bodyCell(false, TABLE_PAD)}>
        <span className="flex items-baseline gap-1.5">
          {row.init ? <InitMark /> : null}
          <span className="min-w-0 max-w-40 truncate xl:max-w-none" title={row.container}>
            {row.container}
          </span>
        </span>
      </td>
      <td className={`${bodyCell(false, TABLE_PAD)} text-right`}>
        <Pill tone={row.tone}>{row.restarts}</Pill>
      </td>
      <td className={bodyCell(false, TABLE_PAD)}>{row.reason ?? '—'}</td>
      {/*
        Relative in the cell because the question is "recently or not"; the exact
        instant in the tooltip because the follow-up is "was that during the
        03:00 incident". When there is no recorded termination the tooltip says
        so rather than leaving an em dash to be read as a rendering fault - which
        is why `lastExitAt` being absent is a sentence and not a blank.
      */}
      <td
        className={`${bodyCell(false, TABLE_PAD)} text-muted`}
        title={
          row.lastExitAt === undefined
            ? 'the kubelet no longer holds this container’s last exit'
            : formatInstant(row.lastExitAt, { year: true, seconds: true })
        }
      >
        {row.age ?? '—'}
      </td>
    </tr>
  );
}

/* -------------------------------------------------------------------------- */
/* 5. The leaderboards                                                         */
/* -------------------------------------------------------------------------- */

/**
 * *Most used*, or no panel at all.
 *
 * An empty leaderboard would otherwise draw *"Nothing here - The metrics source
 * answered and produced no leaderboard. This is not a claim that nothing is
 * using anything."*, which is filler on a cluster with no usable series. So an
 * `empty` leaderboard, an `absent` one (no metrics source on this cluster: the
 * server sends `absent` only with `not_installed`), and an `ok` one whose boards
 * all came with no rows draw nothing - and so does a board with no rows beside
 * one that has some, rather than *"This board was produced and has no rows."*,
 * the same filler one level down. A source that
 * did not answer (`unreachable`) draws nothing either: no metrics reads as no
 * block. `unreadable` and `forbidden` keep the panel and its
 * notice: the source answered and was refused or garbled, which is a fault to
 * fix, not an absence. A leaderboard missing from the payload keeps
 * `PanelMissing`, which is a fault in this product and not a quiet cluster.
 */
function Leaderboards({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  const top = page.top;
  if (top?.state === 'empty') return null;
  // No metrics: no metrics backend (`absent`, which the server sends
  // only with `not_installed`) or one that did not answer (`unreachable`) - the
  // block is left out rather than drawn as a message.
  if (top?.state === 'absent' || top?.state === 'unreachable') return null;
  if (top?.state === 'ok' && top.items.every((board) => board.entries.length === 0)) return null;

  return (
    <Panel title="Most used" icon="cpu">
      <PanelList result={top} what="the leaderboards" onRetry={onRetry}>
        {(boards) => (
          <div className="grid gap-3 lg:grid-cols-2">
            {boards
              .filter((board) => board.entries.length > 0)
              .map((board, index) => (
                <Board key={`${board.label}:${String(index)}`} board={board} />
              ))}
          </div>
        )}
      </PanelList>
    </Panel>
  );
}

function Board({ board }: { board: TopBoard }): ReactNode {
  return (
    <div>
      <h3 className="text-2xs tracking-heading text-muted uppercase">{board.label}</h3>
      {/* A compact table rather than a list: the rank in front, the
          reading at the end, and the same half-strength rule between rows as
          every other table on the page, so the two boards read with the
          Warnings table under them and not as two paragraphs. A board with no
          rows never reaches here: `Leaderboards` leaves it out. */}
      <ol className="mt-1 flex flex-col">
        {board.entries.map((entry, index) => (
          <li
            key={`${entry.ref.text}:${String(index)}`}
            className="flex items-center gap-2 border-b border-line/50 py-1 text-xs last:border-b-0"
          >
            <span className="w-4 shrink-0 text-right text-muted tabular-nums">{index + 1}</span>
            <span className="min-w-0 flex-1">
              <RefLink reference={entry.ref} />
            </span>
            <span className="shrink-0 text-right tabular-nums">
              <ReadingText reading={entry.reading} />
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}

/**
 * The overview tables' horizontal padding, on every cell and every heading
 * alike: 4px a side below `2xl` and 8px from it. One constant so a heading
 * cannot drift off its column.
 *
 * At 1024px the 8px gutters would leave a long name too little room inside its
 * first DNS label, so the tables take 4px there: what that gives back goes to
 * the column that takes the rest - What, and the event's Message. At 1280px the
 * names are whole from `xl` on, at the lists' 13px, and 8px gutters would leave
 * What under a quarter of the panel; so the tighter gutters hold through `xl`.
 */
const TABLE_PAD = 'px-1 2xl:px-2';

/* -------------------------------------------------------------------------- */
/* 6. Recent warnings                                                          */
/* -------------------------------------------------------------------------- */

/**
 * Six columns, and the panel carried one.
 *
 * Each row carries the age, the count and both links, not only a label and a
 * destination: *"A `BackOff` that has fired 340 times and one that fired once
 * are the same row."*
 */
function Warnings({ page, onRetry }: { page: OverviewPage; onRetry: Retry }): ReactNode {
  // A panicked event feed would otherwise leave this reading "No warning events.
  // This cluster answered and had none."
  const failed = spoilt(page, 'events');

  return (
    <Panel title="Recent warnings" icon="event" spoilt={failed}>
      <PanelList
        result={page.events}
        what="the warning events"
        onRetry={onRetry}
        spoilt={failed.length > 0}
        empty={<StateNotice state="empty" />}
      >
        {(all) => (
          <Paged items={all} total={page.eventsTotal} what="Recent warning events">
            {(events) => (
          <PanelTable
            label="Recent warning events"
            pad={TABLE_PAD}
            head={[
              { key: 'age', text: 'Age', numeric: true },
              { key: 'namespace', text: 'Namespace' },
              { key: 'object', text: 'Object' },
              { key: 'reason', text: 'Reason' },
              { key: 'message', text: 'Message' },
              { key: 'count', text: 'Count', numeric: true },
            ]}
          >
            {events.map((event, index) => (
              <WarningRow key={`${event.text}:${String(index)}`} event={event} />
            ))}
          </PanelTable>
            )}
          </Paged>
        )}
      </PanelList>
    </Panel>
  );
}

function WarningRow({ event }: { event: EventEntry }): ReactNode {
  return (
    <tr className="border-b border-line/50">
      <td className={`${bodyCell(false, TABLE_PAD)} text-right`} title={event.at === undefined ? undefined : formatInstant(event.at, { year: true, seconds: true })}>
        {event.age ?? '—'}
      </td>
      <td className={bodyCell(false, TABLE_PAD)}>
        {event.scope === undefined ? (
          '—'
        ) : (
          <span className={CUT_NAME}>
            <RefLink reference={event.scope} fill />
          </span>
        )}
      </td>
      <td className={bodyCell(false, TABLE_PAD)}>
        {event.object === undefined ? (
          '—'
        ) : (
          <span className={CUT_NAME}>
            <RefLink reference={event.object} fill />
          </span>
        )}
      </td>
      {/* A reason is one CamelCase word, and `NodeHasInsufficientMemory` is
          the long one: 8rem below `xl`, whole on hover. The pill's own
          `max-w-full truncate` cuts it to this box. */}
      <td className={bodyCell(false, TABLE_PAD)}>
        {event.reason === undefined ? (
          '—'
        ) : (
          <span className="block max-w-32 xl:max-w-none">
            <Pill tone={event.tone} title={event.reason}>
              {event.reason}
            </Pill>
          </span>
        )}
      </td>
      {/* The one cell on this page whose content is a sentence. `message` is the
          class theme.css matches, on the cell. It takes what the other columns
          leave (`w-full max-w-0`), and `wrap-anywhere` lets a token longer than
          that - `{node.kubernetes.io/memory-pressure},` - break rather than
          set the table's minimum width at 1024px. On the cell, like the wrap
          itself. */}
      <td className={`message w-full max-w-0 ${bodyCell(false, TABLE_PAD)} wrap-anywhere`}>{cleanMessage(event.message ?? event.text)}</td>
      <td className={`${bodyCell(false, TABLE_PAD)} text-right tabular-nums`}>{event.count ?? '—'}</td>
    </tr>
  );
}

/**
 * **What the access review left out of this page**, said above the panels it
 * shapes. Every count below is then about the namespaces the person may list,
 * and a dashboard that says "3 pods" without saying so is one more place an
 * empty answer and a narrowed one look alike. The sentence is the server's.
 */
function AccessNote({ page }: { page: OverviewPage }): ReactNode {
  if (page.accessNote === undefined) return null;
  return (
    <p className="text-xs text-warn" data-note="access">
      {page.accessNote}
    </p>
  );
}

/* -------------------------------------------------------------------------- */
/* 7. Not shown                                                                */
/* -------------------------------------------------------------------------- */

/**
 * **What this page did not look at, and why.**
 *
 * An empty panel that never says why is how "the cluster is quiet" and "we were
 * not allowed to look" come to look the same.
 *
 * This is a **page-level list**. The per-panel envelope covers a panel that was
 * asked and failed; it says nothing about one that was never asked because a
 * prerequisite was missing, and those panels have no envelope at all. It is one
 * place that says what you are not seeing, which ten scattered per-panel reasons
 * do not.
 *
 * **Nothing here leads anywhere, on purpose.** The subject is a panel that did
 * not load, and the only useful destination would be whatever the reason says to
 * fix - a kubeconfig or an IAM role, and not a page.
 */
function NotShown({ page }: { page: OverviewPage }): ReactNode {
  if (page.notShown.length === 0) return null;

  return (
    <Panel title="Not shown" id="not-shown">
      <PanelTable
        label="Panels this page could not read"
        head={[
          { key: 'panel', text: 'Panel' },
          { key: 'reason', text: 'Why' },
        ]}
      >
        {page.notShown.map((skipped) => (
          <tr key={skipped.panel} className="border-b border-line/50">
            <th scope="row" className={`${bodyCell()} text-left font-medium`}>
              {skipped.panel}
            </th>
            <td className={`message ${bodyCell()} text-muted`}>{skipped.reason}</td>
          </tr>
        ))}
      </PanelTable>
    </Panel>
  );
}
