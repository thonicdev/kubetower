import { queryOptions } from '@tanstack/react-query';
import {
  ApiError,
  EnvelopeError,
  NotJsonError,
  request,
  type ListResult,
  type Reason,
  type ReasonCode,
} from '@kubetower/ui';

import { gateUnknownCluster } from '../gate';
import type { Condition, Destination, Source } from './application';

/**
 * What `GET /x/{cluster}/argocd/` answers, and what the page makes of it.
 *
 * The route is `extras/argocd/delivery.go`. It does not speak the core's
 * envelope - it is an extension route, and `extension.Host` hands it rows and a
 * completeness flag rather than a state - so this module is where its answer
 * becomes one of the console's own shapes: a `ListResult`, which `ListView`
 * draws, and which cannot put an empty list where a refusal belongs.
 */

/** One Application, as `delivery.go`'s `Application` sends it. */
export interface Application {
  namespace: string;
  name: string;
  /** Argo CD's own word off `status.sync.status`, or `''` when Argo has not
   *  reported one. Never translated, never defaulted. */
  sync: string;
  /** The same for `status.health.status`. */
  health: string;
  /** `metadata.deletionTimestamp` is set. A field of its own and not a health
   *  word: an Application wedged on a finalizer reads Healthy for days. */
  deleting: boolean;
  /** `spec.project` and `spec.destination`: the tile's one line under the
   *  name. Absent from an answer by an older server. */
  project?: string;
  destination?: Destination;
  /** How many of `status.resources` Argo CD marks `OutOfSync`. Their names
   *  are on the Application's own page. */
  outOfSyncCount?: number;
  /** The first condition Argo CD reports as an error - usually why the sync
   *  word is `Unknown`. */
  condition?: Condition;
  /** Where the desired state is read from: `spec.source`, or the first of a
   *  multi-source Application's `spec.sources`, `sources` saying how many. */
  source?: Source;
  sources?: number;
  /** `status.sync.revision`: what Argo CD compares the cluster against. */
  revision?: string;
  /** `spec.syncPolicy.automated` is set. */
  automatedSync?: boolean;
  /** `status.health.message`: why the health is what it is. */
  healthMessage?: string;
  /** The last (or running) sync, `status.operationState`, its message only
   *  when it did not succeed. */
  lastSync?: LastSync;
}

export interface LastSync {
  phase: string;
  startedAt?: string;
  finishedAt?: string;
  message?: string;
}

/** The 200 body. */
interface Answer {
  applications?: unknown;
  complete?: unknown;
  note?: unknown;
}

/** The problem body `writeProblem` sends on every refusal. */
export interface Problem {
  code?: unknown;
  message?: unknown;
  detail?: unknown;
  /** On `list_forbidden`: the account the cluster refused, as it named it. */
  subject?: unknown;
}

/**
 * The answers that are facts rather than failures to ask: four about the
 * cluster, and `cluster_unknown`, about the address - the console has no
 * cluster of that name, so nothing was asked and asking again changes nothing.
 * Spelled as `delivery.go`'s constants spell them - they are
 * protocol, and the Go tests assert the same strings.
 */
export type Refusal = 'kind_unknown' | 'kind_not_served' | 'kind_ambiguous' | 'list_forbidden' | 'cluster_unknown';

const REFUSALS: readonly Refusal[] = ['kind_unknown', 'kind_not_served', 'kind_ambiguous', 'list_forbidden', 'cluster_unknown'];

/** The kind the page asks for. The same string as `argocd.ApplicationKind`. */
export const APPLICATION_KIND = 'applications.argoproj.io';

export interface Delivery {
  /** What `ListView` draws. */
  list: ListResult<Application>;
  /**
   * Set when rows were read and the read stopped before the end, to the
   * sentence saying why. `extension.Page`: an incomplete list drawn as a
   * complete one is the same failure as an empty one, one step along - so it
   * travels beside the rows and is never folded into them. With no rows at all
   * there is nothing to travel beside, and `list` itself says so.
   */
  incomplete: string | undefined;
  /** Which of the refusals `list` is, when it is one. */
  refusal: Refusal | undefined;
  /** On `list_forbidden`, the refused account when the cluster named one. */
  subject?: string | undefined;
}

/**
 * Where the answer is. The cluster arrives from the router in the same
 * DNS-friendly spelling the console uses in every `/c/{cluster}/…` address and
 * in `extraPageURL`, so it is passed through as a path segment, escaped, and
 * never re-derived.
 */
export function deliveryPath(cluster: string): string {
  return `/x/${encodeURIComponent(cluster)}/argocd/`;
}

/** Said when the server marks the list incomplete and gives no reason. */
const STANDING_INCOMPLETE = 'The read stopped before the end of the list, and the cluster did not say why.';

export async function getDelivery(cluster: string, signal?: AbortSignal): Promise<Delivery> {
  let body: Answer;
  try {
    body = await request<Answer>(deliveryPath(cluster), signal === undefined ? {} : { signal });
  } catch (error) {
    const refusal = refusalOf(error);
    if (refusal !== undefined) {
      const problem = (error as ApiError).body as Problem | undefined;
      return { list: refused(refusal, problem, cluster), incomplete: undefined, refusal, subject: subjectOf(problem) };
    }
    throw said(error);
  }

  if (!Array.isArray(body.applications) || typeof body.complete !== 'boolean') {
    // A 200 whose body is not the route's shape. A fault in this product, not a
    // fact about a cluster, so it is a failure and never an empty list.
    throw new EnvelopeError('the Delivery Center answered without an applications array and a complete flag', body);
  }

  const items = (body.applications as Application[]).slice().sort(byPlace);
  const note = body.complete
    ? undefined
    : typeof body.note === 'string' && body.note.trim() !== ''
      ? body.note
      : STANDING_INCOMPLETE;

  if (items.length > 0) {
    // Rows, and whether they are all of them. The note travels beside the rows,
    // for `Incomplete` to draw above them.
    return { list: { state: 'ok', items }, incomplete: note, refusal: undefined };
  }

  if (note === undefined) {
    return {
      list: {
        state: 'empty',
        items: [],
        reason: {
          code: 'empty_scope',
          message: 'Argo CD answered on this cluster and lists no Application this console may read.',
        },
      },
      incomplete: undefined,
      refusal: undefined,
    };
  }

  // **No rows and not complete is not `empty`.** Nothing was read before the
  // read stopped, so the list is no answer about the cluster, and `empty` in the
  // DOM - `data-state="empty"` - would be the claim that it is. It is
  // `unreadable` with the core's `truncated`: the contract's word for *there is
  // more and it was not read*, carried on an `unreadable` arm exactly as the log
  // stream carries it. The server's note is the detail, and asking again can
  // differ, so the notice offers it.
  return {
    list: {
      state: 'unreadable',
      reason: {
        code: 'truncated',
        message: 'The list stopped before any Application was read, so this is not an answer about the cluster.',
        detail: note,
        scope: { cluster, resource: APPLICATION_KIND, verb: 'list' },
        retryable: true,
      },
    },
    incomplete: undefined,
    refusal: undefined,
  };
}

/** Namespace, then name: the order an operator scans a delivery list in, and
 *  one that does not move between two refreshes of the same answer. */
function byPlace(a: Application, b: Application): number {
  return a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name);
}

/**
 * One of the refusals, or nothing.
 *
 * **On the code and never on the status or the sentence.** Two of the three are
 * 404, and so is the console's own gate when the extra is off on this cluster -
 * which answers in plain text and therefore arrives as a `NotJsonError`, is not
 * matched here, and is drawn as the failure it is rather than as "Argo CD is not
 * installed". `errors.ts` in the core states the rule: anything that branches on
 * a failure branches on the code.
 */
export function refusalOf(error: unknown): Refusal | undefined {
  if (!(error instanceof ApiError) || error instanceof NotJsonError) return undefined;
  // The console's gate answers an unknown cluster before the route runs, with
  // the API's code rather than the route's: the same answer either way.
  if (gateUnknownCluster(error) !== undefined) return 'cluster_unknown';
  const code = (error.body as Problem | undefined)?.code;
  return REFUSALS.find((refusal) => refusal === code);
}

/**
 * How each refusal is drawn, in the console's six-state vocabulary.
 *
 * - **Not installed** is `absent`: the thing that would serve this page is not
 *   there, which is exactly `StateNotice`'s word for it.
 * - **Not served** and **ambiguous** are `unreadable`: something is there, and
 *   it cannot be read - one because every version is switched off, one because
 *   two groups claim the name. They share a state and differ in their sentence,
 *   their code and the next step the page draws under them (`NextStep`), so
 *   neither can be mistaken for the other or for the first.
 *
 * - **Refused** is `unreadable` with `read_denied`, the vocabulary's own words
 *   for "it exists and the credentials do not allow listing it". Not
 *   `forbidden`, which is a verb refused, and never a 502: Argo CD answered, and
 *   a Try again under a refusal is a button that cannot work.
 *
 * - **Unknown cluster** is `absent` with `no_such_kind`, as the console's own
 *   pages draw a context it does not serve (`ClusterHome`, `Overview`). Its
 *   scope is the cluster alone, and it carries the console's sentence and no
 *   detail: the detail is the Go error, which says the same thing again.
 *
 * `retryable: false` on all five: asking again changes nothing until somebody
 * changes the cluster or the kubeconfig, and the page's own Refresh is there
 * for when they have.
 */
/** Which engine a refusal is about: the kind it asked for and the product's
 *  name, for the fallbacks below and the notice's scope line. */
export interface Engine {
  resource: string;
  product: string;
}

export const ARGO_CD: Engine = { resource: APPLICATION_KIND, product: 'Argo CD' };

function drawnFor(
  refusal: Refusal,
  { resource, product }: Engine,
): { state: 'absent' | 'unreadable'; code: ReasonCode; fallback: string } {
  switch (refusal) {
    case 'kind_unknown':
      return { state: 'absent', code: 'not_installed', fallback: `This cluster serves no ${resource}, so ${product} is not installed on it.` };
    case 'kind_not_served':
      return { state: 'unreadable', code: 'no_such_kind', fallback: `The definition of ${resource} is present and no version of it is served.` };
    case 'kind_ambiguous':
      return { state: 'unreadable', code: 'no_such_kind', fallback: `Two API groups define the same plural, so ${resource} does not name one kind.` };
    case 'list_forbidden':
      return {
        state: 'unreadable',
        code: 'read_denied',
        fallback: `${product} is installed on this cluster, and the account this console read it with may not list what it manages.`,
      };
    case 'cluster_unknown':
      return { state: 'absent', code: 'no_such_kind', fallback: 'There is no cluster of this name in the kubeconfig this console reads.' };
  }
}

/**
 * The sentence a refusal carries: the route's `message`, or, for an unknown
 * cluster the console's gate refused, its `detail`.
 */
export function sentenceOf(problem: Problem | undefined, unknown: boolean): string | undefined {
  if (typeof problem?.message === 'string' && problem.message !== '') return problem.message;
  if (unknown && problem?.code === 'not_found' && typeof problem.detail === 'string' && problem.detail !== '') return problem.detail;
  return undefined;
}

/** The refused account a `list_forbidden` problem names, when it names one. */
export function subjectOf(problem: Problem | undefined): string | undefined {
  return typeof problem?.subject === 'string' && problem.subject !== '' ? problem.subject : undefined;
}

export function refused<T>(
  refusal: Refusal,
  problem: Problem | undefined,
  cluster: string,
  engine: Engine = ARGO_CD,
): ListResult<T> {
  return drawnRefusal(refusal, problem, cluster, engine);
}

function drawnRefusal(
  refusal: Refusal,
  problem: Problem | undefined,
  cluster: string,
  engine: Engine,
): { state: 'absent' | 'unreadable'; reason: Reason } {
  const drawn = drawnFor(refusal, engine);
  // An unknown cluster is about the address, so nothing was listed: the scope
  // is the cluster alone, and the detail - the Go error restating the sentence -
  // is left out, so the sentence is said once. From the console's gate the
  // sentence is the detail, as the API writes it.
  const unknown = refusal === 'cluster_unknown';
  const message = sentenceOf(problem, unknown) ?? drawn.fallback;
  const reason: Reason = {
    code: drawn.code,
    message,
    scope: unknown ? { cluster } : { cluster, resource: engine.resource, verb: 'list' },
    retryable: false,
    ...(!unknown && typeof problem?.detail === 'string' && problem.detail !== '' ? { detail: problem.detail } : {}),
  };
  return { state: drawn.state, reason };
}

/**
 * A cluster the console does not know, drawn on every Argo CD surface as the
 * Delivery Center draws it (`refused`): `absent` with `no_such_kind`, the
 * cluster alone as scope, `retryable: false`, the console's sentence once and no
 * detail. `undefined` when `error` is anything else.
 *
 * For the surfaces that read one thing and throw what they were answered (the
 * Application page, the Argo CD tab): `said()` keeps the problem as the error's
 * body, so the code is read there, as `refusalOf` reads it everywhere.
 */
export function unknownClusterOf(error: unknown, cluster: string): { state: 'absent' | 'unreadable'; reason: Reason } | undefined {
  if (!(error instanceof ApiError) || refusalOf(error) !== 'cluster_unknown') return undefined;
  return drawnRefusal('cluster_unknown', error.body as Problem | undefined, cluster, ARGO_CD);
}

/**
 * The console's gate refused the address, in the page's words.
 *
 * **A plain-text 404 at this address is the gate and never the route**:
 * `delivery.go` answers every 404 in JSON with a code, so a 404 that is not JSON
 * came from the console's mount (`refuseExtraOff`, `internal/web/extras.go` in
 * the core) before the route was reached. It stays a failure: it is the console
 * declining the page, not a fact about Argo CD on the cluster.
 *
 * It names the Settings tab, which lists every extra on every profile:
 * every extra is off until it is turned
 * on there, and in a cluster only by a person holding `use` on it, once the
 * chart's values name it (`internal/api/router.go`). The sentence says both,
 * since the page does not know which profile it is on. It is the fallback: the
 * gate's own sentence, the `NotJsonError` excerpt, is shown when there is one.
 */
const GATE_REFUSED =
  'The Delivery Center is not enabled for this cluster in this console. Turn it on in Settings > Extra; ' +
  "where the console runs inside a cluster, the chart's values must name it and you need the verb use on " +
  'extras.kubetower.dev/argocd.';

/**
 * A failure to ask, with the route's own sentence as its message.
 *
 * `request` makes the problem's `detail` the message, which for this route is
 * the raw Go error (`connection refused`); the sentence written for the operator
 * is `message`. So a problem this route wrote is re-said with the sentence first
 * and the error after it - except `cluster_unknown`, whose error restates the
 * sentence, so it is said once. Anything else - a network error, a body that was not
 * JSON, a problem the route did not write - is passed through untouched, because
 * its type is what `Failure` draws its title from.
 */
export function said(error: unknown): unknown {
  if (error instanceof NotJsonError && error.status === 404) return new ApiError(404, error.excerpt ?? GATE_REFUSED);
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error instanceof EnvelopeError) return error;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.message !== 'string' || problem.message === '') return error;
  const detail =
    problem.code !== 'cluster_unknown' && typeof problem.detail === 'string' && problem.detail !== '' ? ` ${problem.detail}` : '';
  return new ApiError(error.status, `${problem.message}${detail}`, error.body);
}

export const deliveryKey = (cluster: string) => ['argocd', 'applications', cluster] as const;

export function deliveryQuery(cluster: string) {
  return queryOptions({
    queryKey: deliveryKey(cluster),
    queryFn: ({ signal }) => getDelivery(cluster, signal),
  });
}
