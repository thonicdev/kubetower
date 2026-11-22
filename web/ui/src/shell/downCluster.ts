import { useClusters, type Cluster, type State } from '../api';
import { useNow } from '../components/useNow';

/**
 * A cluster the probe found down - the case where its verdict stands in for a
 * read.
 *
 * A cluster the probe found unreachable
 * has a page that loads nothing - no overview read, no lists, no navigation -
 * and is one warning and one Refresh, and its kinds menu is not drawn.
 *
 * **That holds for as long as the verdict is red, however old.** The
 * background round re-probes a failing cluster at most once a minute, backing
 * off to once every ten (`internal/health`, `backgroundDue`), so a cluster that
 * has been down a while nearly always carries a verdict older than thirty
 * seconds - and a thirty-second limit would drop its page back to a loading
 * dashboard, up to ten seconds of dialling, and the warning beside an inert
 * menu.
 *
 * **The age decides only whether the verdict is checked first.** A verdict
 * younger than thirty seconds (`fresh`) is the answer. An older one is a reason
 * to look again, so the page asks the probe once, on arrival, while it already
 * shows the warning with *Checking…* - see `ClusterDown`'s `check`. The probe
 * restamps the verdict on the server, so the rail, the menu and the page then
 * read the same new answer; a read of the overview would not, and the menu
 * would stay hidden beside a dashboard until the background round came round.
 *
 * **Thirty seconds is the server's `verdictFresh`** (`internal/web/apimount.go`),
 * which answers an overview read from a fresh verdict without dialling. Read
 * against this browser's clock: `checkedAt` is the server's, and a skew between
 * the two moves the edge by the skew.
 *
 * **Down is `unreachable` and nothing else.** `forbidden` is a cluster that
 * answered and said no to this person: a Refresh that re-probes connectivity
 * cannot change an RBAC answer, so a forbidden page keeps the permission
 * notice. `pending` is a cluster nobody has asked, and
 * `unreadable` one whose lists can still say which read failed.
 */
export const VERDICT_FRESH_MS = 30_000;

/** The one state a page is reduced to its warning on. */
export const DOWN: State = 'unreachable';

/** The states the kinds menu is locked on: wider than `DOWN`, because
 *  a locked menu still explains itself, where a reduced page would not. */
export const RED: readonly State[] = ['forbidden', 'unreachable'];

/**
 * Whether this cluster's credentials are what every read fails on: its AWS SSO
 * session has lapsed, or its credential plugin ran and refused. Drawn as a down
 * cluster: every read would fail the same way, and would otherwise show as the
 * rail's kinds error, half the list and "could not be asked" scattered over the
 * page. One message says it instead, with the command where there is one.
 *
 * **`credentials_expired` only where the server has a command to offer**,
 * which is an AWS context (`remedy.command`). A 401 on any other context is
 * also `credentials_expired`, and there it may be one API that refuses while
 * the rest answers - an aggregated API, a webhook - so its pages keep drawing
 * what they can and say what failed, as before.
 *
 * **`credentials_refused` always**: the plugin refused before any request was
 * sent, so no API of this cluster can answer either.
 */
export function expired(cluster: Cluster | undefined): boolean {
  if (cluster?.state !== 'unreadable') return false;
  const code = cluster.reason?.code;
  if (code === 'credentials_refused') return true;
  return code === 'credentials_expired' && (cluster.remedy?.command ?? '') !== '';
}

/** The reason codes a read carries when it failed at the credential step
 *  (`internal/web`'s `classifyRead`): a sign that the cluster's verdict may be
 *  older than its session (`clusters/credentialWatch.ts`). */
export const CREDENTIAL_CODES: readonly string[] = ['credentials_expired', 'credentials_refused'];

/** Whether this entry is a down verdict younger than `VERDICT_FRESH_MS` at `now`. */
export function freshAt(cluster: Cluster, now: number): boolean {
  if (cluster.checkedAt === undefined) return false;
  const checked = Date.parse(cluster.checkedAt);
  return !Number.isNaN(checked) && now - checked < VERDICT_FRESH_MS;
}

/**
 * What is known about the cluster on screen: `down` when the probe's verdict
 * is `unreachable`, whatever its age; `fresh` when that verdict is younger
 * than thirty seconds; and `settled` once the clusters list has answered (or
 * failed) - so a page asks for nothing until it knows whether it should. Not
 * waiting is a race the page loses: the menu and the overview were asked for
 * in the same tick as the list that says the cluster is down.
 */
export function useClusterVerdict(name: string | undefined): {
  settled: boolean;
  down: Cluster | undefined;
  fresh: boolean;
} {
  const clusters = useClusters();
  const now = useNow();
  const settled = clusters.data !== undefined || clusters.isError;
  if (name === undefined || clusters.data?.state !== 'ok') return { settled, down: undefined, fresh: false };
  const known = clusters.data.items.find((item) => item.name === name);
  if (known === undefined || (known.state !== DOWN && !expired(known))) return { settled, down: undefined, fresh: false };
  return { settled, down: known, fresh: freshAt(known, now) };
}
