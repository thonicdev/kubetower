import { queryOptions, useQuery, type UseQueryResult } from '@tanstack/react-query';

import {
  asList,
  asValue,
  request,
  type FailedState,
  type ListResult,
  type Reason,
  type Ref,
  type ValueResult,
} from '../../api';
import type { ListEnvelopeWire, ValueEnvelopeWire } from '../../api/envelope';
import type { components, paths } from '../../api/schema';

/**
 * The dashboard's one read, and the **ten** envelopes inside it.
 *
 * ## Why the narrowing happens here and not in the page
 *
 * `GET /api/c/{cluster}/overview` answers a **value** envelope whose data holds
 * ten more envelopes, one per panel. `api/openapi.yaml` says why in the schema
 * itself: enveloping only the page would mean one missing metrics source makes
 * the whole dashboard unreadable, or - far worse, because it looks fine - draws
 * usage as zero beside a correct capacity.
 *
 * So the composition has to survive the network layer. `asValue` narrows the
 * outer envelope and `asList` narrows each panel, both with the arm checks that
 * turn `{"state":"ok","items":[]}` into an `EnvelopeError` rather than into a
 * calm empty panel. What the page receives is ten results, each of which has
 * `items` on two arms and on no other - so **one failed panel cannot reach the
 * other nine**, and no panel can be rendered without saying which arm it is in.
 *
 * ## A panel that is not in the payload at all
 *
 * `Overview` requires only `cluster`; every panel is optional in the contract. A
 * missing panel is therefore legal and is **not** one of the six states - nobody
 * said anything about it. It arrives here as `undefined` and `Panel.tsx` draws
 * it as its own sentence, because turning silence into `absent` would be this
 * client inventing a server answer.
 *
 * ## `notShown` is not a panel and is not narrowed
 *
 * It is a plain array on the page: the list of panels that were *never asked
 * for*, because a prerequisite was missing. Those panels have no envelope to
 * carry a reason, which is precisely why the list exists - see `SkippedPanel` in
 * the contract.
 */

type Schemas = components['schemas'];

export type CountEntry = Schemas['CountEntry'];
export type CapacityCell = Schemas['CapacityCell'];
export type TopBoard = Schemas['TopBoard'];
export type Reading = Schemas['Reading'];
export type EventEntry = Schemas['EventEntry'];
export type AlertEntry = Schemas['AlertEntry'];
export type TroubleEntry = Schemas['TroubleEntry'];
export type RestartEntry = Schemas['RestartEntry'];
export type PhaseEntry = Schemas['PhaseEntry'];
export type SkippedPanel = Schemas['SkippedPanel'];
export type MetricsPanel = Schemas['MetricsPanel'];
type OverviewWire = Schemas['Overview'];

/** The dashboard, with every envelope already narrowed. */
export interface OverviewPage {
  readonly cluster: string;
  /** The API server's own version string. The wire's `collectedAt` is not
   *  carried: nothing on the client reads it. */
  readonly version: string | undefined;
  readonly nodesSummary: Ref | undefined;
  readonly nodesTone: string | undefined;
  readonly workloadsSummary: Ref | undefined;

  /** One tile per workload kind: healthy over total, with what the rest are. */
  readonly counts: ListResult<CountEntry> | undefined;
  /** CPU, memory and pods: capacity, what is requested, and what is used. */
  readonly capacity: ListResult<CapacityCell> | undefined;
  /** The leaderboards. */
  readonly top: ListResult<TopBoard> | undefined;
  /** Warning events, with their age, their count and both links. */
  readonly events: ListResult<EventEntry> | undefined;
  /** How many warning events there were before the server cut `events` to
   *  what it sends. */
  readonly eventsTotal: number | undefined;
  /** Node conditions worth acting on, lifted out of the node list. */
  readonly alerts: ListResult<AlertEntry> | undefined;
  /** Workloads not in the state they declare. */
  readonly trouble: ListResult<TroubleEntry> | undefined;
  /** Containers that keep exiting. */
  readonly restarting: ListResult<RestartEntry> | undefined;
  /** How many containers were restarting before the server cut `restarting`
   *  to what it sends. */
  readonly restartingTotal: number | undefined;
  /** The pod phase breakdown. */
  readonly phases: ListResult<PhaseEntry> | undefined;
  /** The cluster's own series over time. A value and not a list: one panel, one
   *  answer. */
  readonly history: ValueResult<MetricsPanel> | undefined;
  /** What this page did not look at, and why. */
  readonly notShown: readonly SkippedPanel[];
  /** The access review narrowed what this page counts - the person may not
   *  list pods, events or a workload kind in every namespace - in the server's
   *  own sentence. `undefined` when every list was the cluster's whole answer. */
  readonly accessNote: string | undefined;
}

/** A member of the page a failure can spoil, as the contract spells it. The
 *  union is generated, so a member renamed in `api/openapi.yaml` fails
 *  `pnpm run typecheck` at every call of `spoilt` below. */
export type OverviewMember = SkippedPanel['affects'][number];

/**
 * The entries in `notShown` that left one drawn member incomplete.
 *
 * **This is what stops a panel reading as a fact about the cluster.** A gathered
 * task that panics degrades its panel rather than ending the process, and the
 * panel's own envelope still comes back `empty` - so the warnings panel would
 * draw *"No warning events. This cluster answered and had none."* on a cluster
 * whose event feed had just crashed, with the correction at the top of the page
 * in another component.
 *
 * **The matching is on `affects` and never on `panel`.** `panel` is prose the
 * server writes for a person - "Warning events", against a panel this page
 * titles "Recent warnings" - and a client keying on it would be re-deriving an
 * identity from a printed string, which stops matching the day that wording
 * improves and says nothing when it does. `affects` names the payload's own
 * members, and one failed read can name several: the Deployments list is
 * behind both a `counts` tile and the `trouble` rows.
 *
 * It returns the entries rather than a boolean, so the mark can carry the
 * server's own sentence.
 */
export function spoilt(page: OverviewPage, member: OverviewMember): readonly SkippedPanel[] {
  return page.notShown.filter((entry) => entry.affects.includes(member));
}

/**
 * The page a cluster that refused would have answered, every panel in the
 * refusal's own state.
 *
 * When the cluster itself cannot be asked, the server answers the **outer**
 * envelope's failure arm and no `data` - the node list is the page's proof that
 * the cluster answers at all, and without it there is nothing to compose. Drawn
 * as it arrived, that is one notice and a blank below it, and the blank is the
 * thing this console exists to refuse: a dashboard with no panels on it reads as
 * a dashboard with nothing to say, when what is true is that every one of its ten
 * panels could not be read.
 *
 * So the page is composed here, from the one fact in hand: each panel carries
 * the refusal's state and the refusal's reason, which is exactly what the server
 * would have put in each of them had it got as far as asking. Nothing is
 * invented - no count, no capacity - and `notShown` is empty because nothing
 * was skipped; it was all asked, of a cluster that did not answer. A panel says
 * its own state, at the panel, and the page-level notice is the summary rather
 * than the substitute.
 *
 * A failed state only. `pending` is not a refusal - the probe has not reached
 * this cluster yet - and nine panels each saying so would be nine copies of the
 * notice's own standing line.
 */
export function refusedPage(cluster: string, state: FailedState, reason: Reason): OverviewPage {
  const panel = { state, reason } as const;
  return {
    cluster,
    version: undefined,
    nodesSummary: undefined,
    nodesTone: undefined,
    workloadsSummary: undefined,
    counts: panel,
    capacity: panel,
    top: panel,
    events: panel,
    eventsTotal: undefined,
    alerts: panel,
    trouble: panel,
    restarting: panel,
    restartingTotal: undefined,
    phases: panel,
    history: panel,
    notShown: [],
    accessNote: undefined,
  };
}

/**
 * A path template checked against the contract.
 *
 * The same eight lines as `api/endpoints.ts`, deliberately copied rather than
 * imported: `url` is private there. What matters is kept: `T extends keyof paths`
 * means a renamed operation fails `pnpm run typecheck` here rather than 404-ing
 * at runtime, and the cluster segment is percent-encoded because a context name
 * is whatever a `KUBECONFIG` says it is.
 */
function path<T extends keyof paths & string>(
  template: T,
  params: Readonly<Record<string, string>> = {},
): string {
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => {
    const value = params[name];
    if (value === undefined) throw new Error(`${template}: no value for {${name}}`);
    return encodeURIComponent(value);
  });
}

function panelOf<T>(wire: ListEnvelopeWire<T> | undefined): ListResult<T> | undefined {
  return wire === undefined ? undefined : asList(wire);
}

function valueOf<T>(wire: ValueEnvelopeWire<T> | undefined): ValueResult<T> | undefined {
  return wire === undefined ? undefined : asValue(wire);
}

export async function getOverview(
  cluster: string,
  signal?: AbortSignal,
): Promise<ValueResult<OverviewPage>> {
  const wire = await request<ValueEnvelopeWire<OverviewWire>>(
    path('/api/c/{cluster}/overview', { cluster }),
    signal ? { signal } : {},
  );

  const outer = asValue(wire);
  if (outer.state !== 'ok') return outer;

  const page: OverviewPage = {
    cluster: outer.data.cluster,
    version: outer.data.version,
    nodesSummary: outer.data.nodesSummary,
    nodesTone: outer.data.nodesTone,
    workloadsSummary: outer.data.workloadsSummary,
    counts: panelOf(outer.data.counts),
    capacity: panelOf(outer.data.capacity),
    top: panelOf(outer.data.top),
    events: panelOf(outer.data.events),
    eventsTotal: outer.data.eventsTotal,
    alerts: panelOf(outer.data.alerts),
    trouble: panelOf(outer.data.trouble),
    restarting: panelOf(outer.data.restarting),
    restartingTotal: outer.data.restartingTotal,
    phases: panelOf(outer.data.phases),
    history: valueOf(outer.data.history),
    notShown: outer.data.notShown ?? [],
    accessNote: outer.data.accessNote,
  };

  return outer.reason === undefined
    ? { state: 'ok', data: page }
    : { state: 'ok', data: page, reason: outer.reason };
}

export const overviewKey = (cluster: string) => ['overview', cluster] as const;

export function overviewQuery(cluster: string) {
  return queryOptions({
    queryKey: overviewKey(cluster),
    queryFn: ({ signal }) => getOverview(cluster, signal),
    /**
     * Fifteen seconds: a dashboard is watched rather than glanced at, so it
     * polls faster than the rail's thirty, but every poll is a cluster read per
     * open tab (and retries applied to a poll would multiply the audit-log
     * entries), so it is not faster still.
     */
    refetchInterval: 15_000,
  });
}

/** `enabled` false withholds the read: a cluster the probe found down moments
 *  ago is not asked. */
export function useOverview(
  cluster: string | undefined,
  enabled = true,
): UseQueryResult<ValueResult<OverviewPage>> {
  return useQuery({ ...overviewQuery(cluster ?? ''), enabled: enabled && cluster !== undefined });
}
