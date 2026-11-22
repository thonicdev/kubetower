import { Link, useNavigate } from 'react-router';
import { useQueries, useQuery } from '@tanstack/react-query';
import type { MouseEvent, ReactNode } from 'react';

import { capabilitiesQuery, useClusters, type Capabilities, type Cluster, type State } from '../api';
import { ListView } from '../state/Result';
import { StateNotice } from '../state/StateNotice';
import { focusRing } from '../focus';
import { arrange, stateDotClass, stateWord } from '../shell/clusterState';
import { BADGE } from './badge';
import { clusterPath } from './clusterPath';
import { ClusterName } from '../components/ClusterName';
import { btnSecondary } from '../components/buttons';
import { Icon } from '../components/Icon';
import { Pill } from '../components/Pill';
import { fraction, Measure, type ReadingTone } from '../components/Reading';
import { useDockIfPresent } from '../dock/DockContext';
import { Sparkline, unchartable } from '../pages/overview/Bits';
import { overviewQuery, type OverviewPage } from '../pages/overview/data';
import {
  cpuHistory,
  historyFailure,
  podCount,
  readOf,
  usage,
  type OverviewRead,
  type Usage,
} from './fleet';
import { LocalNoticeLine, RemedyLine } from './LocalNotice';
import { useLocalNotices, type LocalNotice } from './localNotices';
import { SSOCardItems } from './SSOCard';
import { useSSOCards, withUnclaimedNotice } from './ssoSessions';

/**
 * Which contexts answer, and how each of them is doing.
 *
 * The chooser and the rail read the same query and draw the same facts at two
 * sizes: the rail is a square of initials with a state badge, this is the
 * sentence behind it. They are separate components rather than one responsive
 * one because the rail has to survive every navigation and this does not - it
 * is a page, it lives inside the
 * `<Outlet/>`, and merging them would put a route's element in the layout.
 *
 * **Every cluster is listed whatever its state**, including the ones that could
 * not be asked. A chooser that lists only the reachable ones answers a different
 * question from the one the person asked, and answers it silently: the cluster
 * that is down is exactly the cluster they came here to look at.
 *
 * ## The figures on a card are the overview's
 *
 * A reachable cluster's card carries its CPU line, its CPU and memory used
 * against allocatable, its nodes and its pods: the same `GET
 * /api/c/{cluster}/overview` its dashboard reads, under the same query key, so
 * opening the cluster afterwards draws from what is already held. **Only an
 * `ok` cluster is asked**: one the probe could not reach answers from its
 * verdict, and dialling it from a list would make the list as slow as its
 * slowest timeout. Polled at the list's own thirty seconds rather than the
 * dashboard's fifteen, because this page is glanced at rather than watched and
 * the read is one per cluster.
 *
 * **A usage tile is drawn only where usage was measured**: a cluster with no
 * metrics source, or whose metrics read failed, carries no CPU or memory tile
 * and no sentence about it. A capacity panel or a CPU history that failed is
 * still said.
 *
 * **No figure across the fleet**: the header carries
 * no CPU or memory share summed over every cluster, only the reachable count.
 */
export function ClusterList(): ReactNode {
  const clusters = useClusters();
  // Read once here and handed down, and **never asked again because this page
  // mounted**: a new observer re-asks a failed read on mount (`retryOnMount`)
  // and a stale one (`refetchOnMount`), and while it is in flight the read is
  // pending again - which takes the rail's Arrange away (`Rail.tsx`, `decided`)
  // under a pointer already on it, and draws the layout's wait over this very
  // page (`OneClusterLanding`). The Shell's own observers keep it fresh.
  const capabilities = useQuery({ ...capabilitiesQuery(), refetchOnMount: false, retryOnMount: false });
  const shell = capabilities.data?.localShell === true;

  const items = clusters.data?.state === 'ok' ? arrange(clusters.data.items) : [];
  const reachable = items.filter((cluster) => cluster.state === 'ok');
  // `pending` is the ordinary state of every cluster at start-up: not asked
  // yet, which is neither reachable nor down.
  const pending = items.filter((cluster) => cluster.state === 'pending').length;
  const overviews = useQueries({
    queries: reachable.map((cluster) => ({ ...overviewQuery(cluster.name), refetchInterval: 30_000 })),
  });
  const reads = new Map<string, OverviewRead>(
    reachable.map((cluster, index) => [cluster.name, readOf(overviews[index])]),
  );
  // The machine's own notices, placed on the cards they concern, the rest
  // above them (`localNotices.ts`).
  const notices = useLocalNotices(items);
  // The AWS SSO session's own card, after the clusters' (`ssoSessions.ts`):
  // none under the server profile.
  const read = useSSOCards(capabilities.data?.mode);
  const container = capabilities.data?.mode === 'container';
  // **One fact, one surface.** Where the SSO card is drawn it owns the
  // session's own state: an `sso` notice no cluster card claims - an unreadable
  // cache, an expired session with no AWS card failing on it - is not drawn a
  // second time above, and the card carries what only that notice held: the
  // unreadable cache's directory, the command for a renewable session the
  // clusters contradict. The expired-session notice on an AWS cluster card in
  // `credentials_expired` stays: it says why that cluster is down.
  const ssoCard = read.kind !== 'hidden';
  const sso = withUnclaimedNotice(
    read,
    notices.unplaced.find((notice) => notice.key === 'sso'),
  );
  const unplaced = ssoCard ? notices.unplaced.filter((notice) => notice.key !== 'sso') : notices.unplaced;
  // The cluster grid draws only over a list that answered with clusters; the
  // card goes in a grid of its own otherwise - an empty kubeconfig, a failed
  // list - rather than nowhere. **Not while the list is still loading**: the
  // card drawn there would move into the cluster grid the moment the list
  // answered, so it waits for the list, whichever grid it lands in.
  const inGrid = clusters.data?.state === 'ok';
  const listSettled = clusters.data !== undefined || clusters.isError;
  // What the line for a failed read names: not the SSO session where its card
  // already says it was not read.
  const unreadChecks = ssoCard ? 'credential caches' : 'SSO session, credential caches';

  return (
    <div className="flex flex-col gap-6 p-4">
      <header className="flex flex-col gap-4 border-b border-line pb-4 lg:flex-row lg:items-end lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-2xl font-semibold">Clusters</h1>
            {items.length === 0 ? null : (
              <Pill
                tone={reachable.length > 0 ? 'ok' : pending === items.length ? undefined : 'bad'}
                neutral="muted"
                title={
                  `${String(reachable.length)} of ${String(items.length)} contexts answered the probe` +
                  (pending === 0 ? '' : `, ${String(pending)} not asked yet`)
                }
              >
                {reachable.length} reachable
              </Pill>
            )}
          </div>
          <p className="mt-1 text-muted text-xs">Every context this console is configured for.</p>
          <ReadOnlyFacts readOnly={capabilities.data?.readOnly} />
        </div>
      </header>

      {unplaced.length === 0 ? null : (
        <section aria-label="This machine" data-local-notices="" className="flex flex-col gap-2">
          {unplaced.map((notice) => (
            <LocalNoticeLine key={notice.key} notice={notice} />
          ))}
        </section>
      )}
      {notices.read || notices.why === undefined ? null : (
        <p data-state="incomplete" data-local-notices-unread="" className="text-xs text-muted" title={notices.why}>
          <span aria-hidden="true">—</span> local checks ({unreadChecks}) not read
        </p>
      )}

      <ListView
        query={clusters}
        what="clusters"
        empty={
          <StateNotice
            state="empty"
            reason={{
              code: 'empty_scope',
              message: 'The configuration was read and it names no clusters.',
            }}
          />
        }
      >
        {(listed) => (
          // `grid-cols-1` below `sm` rather than the implicit column: an
          // implicit track is as wide as its widest card's one-line content,
          // so an 89-character ARN pushed the page sideways at 500px. Every
          // `grid-cols-N` is `minmax(0, 1fr)`, which lets `ClusterName` cut.
          <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {arrange(listed).map((cluster) => (
              <li key={cluster.name} className="min-w-0">
                <Card
                  cluster={cluster}
                  read={reads.get(cluster.name)}
                  shell={shell}
                  notices={notices.byCluster.get(cluster.name) ?? NO_NOTICES}
                />
              </li>
            ))}
            <SSOCardItems cards={sso} container={container} />
          </ul>
        )}
      </ListView>
      {inGrid || !listSettled || sso.kind === 'hidden' || sso.kind === 'waiting' ? null : (
        <ul data-sso-grid="" className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <SSOCardItems cards={sso} container={container} />
        </ul>
      )}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* The read-only guardrail                                                     */
/* -------------------------------------------------------------------------- */

/**
 * The three words for the terminal's read-only override, as the server measured
 * it at start-up (`Capabilities.readOnly.fence`).
 *
 * **`unknown` is not `off`, and they must not look alike**
 * (`docs/DEFINITION-OF-DONE.md` §3): a probe that never ran is not an override
 * found missing, and an operator sent to fix one that is fine stops reading
 * this line. So each state has its own words and its own tone, and the server's
 * sentence is on hover - and on the page where it is something to act on.
 */
const FENCE_WORDS: Readonly<Record<string, { text: string; tone: string }>> = {
  on: { text: 'read-only guardrail on', tone: 'ok' },
  off: { text: 'read-only guardrail NOT in place', tone: 'bad' },
  unknown: { text: 'read-only guardrail not measured', tone: 'warn' },
};

/**
 * Where the per-cluster `read-only` badges are, the two facts about them that
 * belong to no card: whether the override in front of a terminal's kubectl is
 * really there, and which `KT_READONLY_CLUSTERS` entries named no context.
 * An entry that matched nothing is a cluster the operator
 * believes protected and that nothing protects; the boot log said so once and
 * scrolled away.
 *
 * Nothing while the capabilities are not answered: a guess here is a promise.
 */
function ReadOnlyFacts({ readOnly }: { readOnly: Capabilities['readOnly'] | undefined }): ReactNode {
  if (readOnly === undefined) return null;
  // A word this page does not know is named rather than read as either side.
  const words = FENCE_WORDS[readOnly.fence] ?? { text: `read-only guardrail: ${readOnly.fence}`, tone: 'warn' };
  const detail = readOnly.detail === undefined || readOnly.detail === '' ? undefined : readOnly.detail;
  const unmatched = readOnly.unmatched ?? [];
  return (
    <div className="mt-2 flex flex-col gap-1 text-xs" data-readonly-fence={readOnly.fence}>
      <p className="flex flex-wrap items-baseline gap-2">
        <Pill tone={words.tone} title={detail}>
          {words.text}
        </Pill>
        {/* On the page only where it is something to act on: `on` says so on
            hover, the other two say why where the operator is reading. */}
        {readOnly.fence === 'on' || detail === undefined ? null : (
          <span className="break-words text-muted" data-readonly-detail="">
            {detail}
          </span>
        )}
      </p>
      {unmatched.length === 0 ? null : (
        <p className="break-words text-warn" data-readonly-unmatched="">
          KT_READONLY_CLUSTERS names {unmatched.map((entry) => `"${entry}"`).join(', ')},{' '}
          {unmatched.length === 1 ? 'which matches' : 'which match'} no context: nothing is protected by{' '}
          {unmatched.length === 1 ? 'that entry' : 'those entries'}.
        </p>
      )}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* One card                                                                    */
/* -------------------------------------------------------------------------- */

/** The state word's color, for the states that are drawn as a word. */
const STATE_TEXT: Readonly<Record<State, string>> = {
  ok: 'text-ok',
  empty: 'text-muted',
  pending: 'text-accent',
  absent: 'text-muted',
  unreadable: 'text-warn',
  forbidden: 'text-warn',
  unreachable: 'text-bad',
};

const TONE_TEXT: Readonly<Record<string, string>> = {
  normal: 'text-fg',
  muted: 'text-muted',
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
};

/** A `title` only where there are words for it: `title=""` is a tooltip of nothing. */
function titled(why: string | undefined): { title?: string } {
  return why === undefined || why === '' ? {} : { title: why };
}

const NO_NOTICES: readonly LocalNotice[] = [];

function Card({
  cluster,
  read,
  shell,
  notices,
}: {
  cluster: Cluster;
  read: OverviewRead | undefined;
  /** The profile offers a local shell (`Capabilities.localShell`). */
  shell: boolean;
  /** The machine notices that concern this cluster (`localNotices.ts`). */
  notices: readonly LocalNotice[];
}): ReactNode {
  const page = read?.kind === 'ok' ? read.page : undefined;
  const version = page?.version;
  const shownVersion = version !== undefined && version !== '' && version !== '—';
  const stale = read?.kind === 'ok' ? read.stale : undefined;
  // `reachable` is the green dot's word and is not repeated beside it; every
  // other state is said, because the dot alone
  // does not tell *could not be reached* from *refused*.
  const word = cluster.state === 'ok' ? undefined : stateWord(cluster.state);
  const navigate = useNavigate();

  /**
   * **A click anywhere on the card opens the cluster** - handled on the card
   * itself rather than by a link box laid over it, which would sit above every
   * `title` on the card.
   *
   * Left alone: a click on a control of its own (the name, Open, Shell), which
   * does what it says; a modified or non-primary click, which the name's link
   * answers the way a browser does (new tab, new window); and the end of a
   * text selection, so a version or a reason can still be copied. The
   * keyboard reaches the cluster through the name, the one link a card has.
   */
  const open = (event: MouseEvent<HTMLElement>): void => {
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if (event.target instanceof Element && event.target.closest('a, button') !== null) return;
    if ((window.getSelection()?.toString() ?? '') !== '') return;
    void navigate(clusterPath(cluster.name));
  };

  return (
    <article
      data-cluster-card={cluster.name}
      onClick={open}
      className="flex h-full min-w-0 cursor-pointer flex-col gap-4 rounded-2xl border border-line bg-panel p-4 transition-colors hover:border-accent/60"
    >
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex min-w-0 items-center gap-2">
          <span
            aria-hidden="true"
            className={`inline-block size-2 shrink-0 rounded-full ${stateDotClass(cluster.state)}`}
          />
          {/* The name is the card's link: one anchor per card carrying
              `data-cluster`, as every test that finds a card expects. **Not a
              box stretched over the card**: an overlay sits above every
              `title` under it, so a cut name could no longer be read whole and
              a reason given on hover would be given to nobody. */}
          <h2 className="flex min-w-0 flex-1 text-sm font-semibold">
            <Link
              to={clusterPath(cluster.name)}
              data-cluster={cluster.name}
              data-state={cluster.state}
              className={`flex min-w-0 rounded-sm hover:underline ${focusRing}`}
            >
              <ClusterName name={cluster.label} />
            </Link>
          </h2>
          {cluster.hidden === true ? <span className={BADGE}>hidden</span> : null}
          {cluster.loopback === true ? <span className={BADGE}>local</span> : null}
          {cluster.readOnly === true ? <span className={BADGE}>read-only</span> : null}
        </div>

        {/* The nameplate is the operator's word; the context name is the identity
            used in every path. Showing both is what makes two similarly renamed
            clusters distinguishable before the Delete, not after it. */}
        {cluster.label !== cluster.name ? (
          <span className="flex min-w-0 pl-4">
            <ClusterName name={cluster.name} className="font-mono text-xs text-muted" />
          </span>
        ) : null}

        {/* The dot is the only mark of `ok` on screen; a screen reader is
            given the word the operator took off the card. */}
        {cluster.state === 'ok' ? <span className="sr-only">{stateWord(cluster.state)}</span> : null}

        {word === undefined && !shownVersion && stale === undefined ? null : (
          <p className="flex flex-wrap items-center gap-x-2 pl-4 text-xs text-muted">
            {word === undefined ? null : <span className={`font-medium ${STATE_TEXT[cluster.state]}`}>{word}</span>}
            {shownVersion ? <span className="font-mono">{version}</span> : null}
            {/* A later poll failed over the figures below: they stay, and say so,
                as `ListView` does (`state/Result.tsx`). */}
            {stale === undefined ? null : (
              <span data-state="stale" className="text-warn" {...titled(stale)}>
                not refreshed
              </span>
            )}
          </p>
        )}

        {/* The reason, where the server sent one. `unreachable` that does not say
            what could not be reached is not usable in a support conversation. */}
        {cluster.reason !== undefined ? (
          <p className="pl-4 text-xs text-muted">{cluster.reason.message}</p>
        ) : null}

        {/* Who fixes it, and the machine's own notice where it concerns this
            cluster. */}
        {cluster.remedy === undefined ? null : (
          <div className="pl-4">
            <RemedyLine remedy={cluster.remedy} />
          </div>
        )}
        {notices.length === 0 ? null : (
          <div className="flex flex-col gap-1.5 pl-4">
            {notices.map((notice) => (
              <LocalNoticeLine key={notice.key} notice={notice} />
            ))}
          </div>
        )}
      </div>

      {cluster.state === 'ok' ? <Readings read={read} /> : null}

      <Footer cluster={cluster} page={page} shell={shell} />
    </article>
  );
}

/** The middle of a reachable cluster's card: its CPU line and its usage tiles. */
function Readings({ read }: { read: OverviewRead | undefined }): ReactNode {
  if (read === undefined || read.kind === 'waiting') {
    return <p className="text-xs text-muted">Reading its overview…</p>;
  }
  if (read.kind === 'failed') {
    return (
      <p data-state="incomplete" className="text-xs text-muted" {...titled(read.why)}>
        <span aria-hidden="true">—</span> overview not read
      </p>
    );
  }

  const page = read.page;
  const history = cpuHistory(page);
  const line = history?.series[0];
  const historyFailed = historyFailure(page);
  const cpu = usage(page, 'CPU');
  const memory = usage(page, 'Memory');
  const tiles = [
    { term: 'CPU', reading: cpu },
    { term: 'Memory', reading: memory },
  ].flatMap(({ term, reading }) => (reading === undefined ? [] : [{ term, reading }]));

  return (
    <div className="flex flex-col gap-3">
      {/* A cluster with no series source draws no line at all, as its overview
          does: an empty chart reads as an idle cluster. A read that failed is
          not an absence, and is said. */}
      {historyFailed !== undefined ? (
        <p data-state="incomplete" className="border-t border-line pt-3 text-xs text-muted" {...titled(historyFailed)}>
          CPU used <span aria-hidden="true">—</span> not read
        </p>
      ) : history === undefined || line === undefined || unchartable(line) ? null : (
        <div className="border-t border-line pt-3">
          <Sparkline panel={history} label="CPU used" />
        </div>
      )}
      {page.capacity !== undefined && page.capacity.state !== 'ok' && page.capacity.state !== 'empty' ? (
        <p data-state="incomplete" className="text-xs text-muted" {...titled(page.capacity.reason.message)}>
          <span aria-hidden="true">—</span> capacity not read
        </p>
      ) : tiles.length === 0 ? null : (
        // **No usage figure, no tile, and no sentence - whether no metrics
        // source is installed or its read failed.** A failed metrics read is
        // said on the cluster's own overview, not on the chooser.
        <div className="grid grid-cols-2 gap-3">
          {tiles.map(({ term, reading }) => (
            <Tile key={term} term={term} reading={reading} />
          ))}
        </div>
      )}
    </div>
  );
}

function toneFor(percent: number): ReadingTone {
  if (percent >= 90) return 'bad';
  if (percent >= 75) return 'warn';
  return 'ok';
}

/**
 * The graded bar and its percentage, and `10 / 40 cores` on hover: used over
 * allocatable, in the allocatable's one unit (`fleet.ts`), written as every
 * other reading's hover is (`components/Reading.tsx`).
 */
function Tile({ term, reading }: { term: string; reading: Usage }): ReactNode {
  const whole = reading.unit === '' ? reading.whole : `${reading.whole} ${reading.unit}`;
  return (
    <div data-capacity={term} className="flex min-w-0 flex-col gap-1 rounded-xl border border-line bg-panel-2 px-3 py-2.5">
      <span className="text-xs text-muted">{term} used</span>
      <Measure
        value={null}
        align="start"
        title={fraction(reading.used, whole)}
        percent={reading.percent}
        tone={toneFor(reading.percent)}
        label={term}
      />
    </div>
  );
}

function Footer({
  cluster,
  page,
  shell,
}: {
  cluster: Cluster;
  page: OverviewPage | undefined;
  shell: boolean;
}): ReactNode {
  const dock = useDockIfPresent();
  const pods = page === undefined ? undefined : podCount(page);

  return (
    <div className="mt-auto flex items-center justify-between gap-3 border-t border-line pt-3 text-xs text-muted">
      <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
        {page?.nodesSummary === undefined ? null : (
          <span className="flex min-w-0 items-center gap-1.5" title={page.nodesSummary.text}>
            <Icon name="node" className="shrink-0" />
            <span className={`truncate ${TONE_TEXT[page.nodesTone ?? 'normal'] ?? 'text-fg'}`}>
              {page.nodesSummary.text}
            </span>
          </span>
        )}
        {pods === undefined ? null : pods === 'failed' ? (
          <span className="flex items-center gap-1.5" title="Pods not read">
            <Icon name="pod" className="shrink-0" />
            <span className="font-mono">—</span>
          </span>
        ) : (
          <span className="flex items-center gap-1.5" title={pods.note ?? `${String(pods.count)} pods`}>
            <Icon name="pod" className="shrink-0" />
            <span className="font-mono tabular-nums text-fg">{pods.count}</span>
          </span>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-1">
        {/* Drawn only where the profile offers a local shell - absent, not
            disabled, as the top bar's Shell is. */}
        {shell && dock !== null && cluster.state === 'ok' ? (
          <button
            type="button"
            title={`Open a shell on ${cluster.label}`}
            aria-label={`Open a shell on ${cluster.label}`}
            onClick={() => dock.openShell({ kind: 'terminal', cluster: cluster.name, label: cluster.label })}
            className={`inline-flex min-h-6 min-w-6 items-center justify-center rounded-md p-1.5 text-muted hover:bg-panel-2 hover:text-fg pointer-coarse:min-h-11 pointer-coarse:min-w-11 ${focusRing}`}
          >
            <Icon name="terminal" />
          </button>
        ) : null}
        {/* The same address as the name, where a pointer looks for it. Out of
            the tab order and the accessibility tree: the name is the one link
            a keyboard or a screen reader meets, and two links to one place on
            every card would double the walk. No `data-cluster`, so a card
            still has exactly one anchor that names it. */}
        <Link
          to={clusterPath(cluster.name)}
          tabIndex={-1}
          aria-hidden="true"
          className={btnSecondary}
        >
          Open
          <Icon name="chevron" className="size-3" />
        </Link>
      </div>
    </div>
  );
}
