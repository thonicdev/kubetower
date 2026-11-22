import type { QueryClient } from '@tanstack/react-query';

import { probeCluster, queryKeys, type Cluster, type ListResult } from '../api';
import { CREDENTIAL_CODES, expired } from '../shell/downCluster';

/**
 * A read that failed at the credential step asks the probe for its cluster's
 * verdict.
 *
 * The cluster list answers out of what the probe last found, and an `ok`
 * verdict is believed for a while. A session that lapses after it - the
 * ordinary case: the console was opened in the morning and the AWS SSO
 * session ends at noon - leaves the cluster `ok` on the list while every read
 * of it is refused, and the page draws each refusal in its own panel. The
 * reads know first: `internal/web` answers them `credentials_expired` or
 * `credentials_refused`. So the first such answer asks the probe, which runs
 * the credential step itself and says what the session is, and the list is
 * read again: a lapsed session then replaces the page with the one message
 * that says so (`CanonicalCluster`).
 *
 * **Bounded per cluster.** One probe at a time, and none within `AGAIN_MS` of
 * the last; when the probe answered that the cluster is fine - a 401 from one
 * aggregated API, say, which the probe's `/version` does not see - the next is
 * not asked for `QUIET_MS`, so a page that keeps reading that API does not keep
 * the probe busy.
 */
export const AGAIN_MS = 30_000;
export const QUIET_MS = 5 * 60_000;

interface Watch {
  readonly busy: Set<string>;
  readonly next: Map<string, number>;
}

const watches = new WeakMap<QueryClient, Watch>();

function watchOf(client: QueryClient): Watch {
  let watch = watches.get(client);
  if (watch === undefined) {
    watch = { busy: new Set(), next: new Map() };
    watches.set(client, watch);
  }
  return watch;
}

/** The cluster a read's answer says failed at the credential step, if any. */
export function credentialFailureOf(data: unknown): string | undefined {
  if (typeof data !== 'object' || data === null) return undefined;
  const reason = (data as { reason?: unknown }).reason;
  if (typeof reason !== 'object' || reason === null) return undefined;
  const { code, scope } = reason as { code?: unknown; scope?: unknown };
  if (typeof code !== 'string' || !CREDENTIAL_CODES.includes(code)) return undefined;
  if (typeof scope !== 'object' || scope === null) return undefined;
  const cluster = (scope as { cluster?: unknown }).cluster;
  return typeof cluster === 'string' && cluster !== '' ? cluster : undefined;
}

/** Called with every answer the query cache receives. */
export function watchCredentials(client: QueryClient, data: unknown, now: number = Date.now()): void {
  const cluster = credentialFailureOf(data);
  if (cluster === undefined) return;
  // Already drawn as one message: the panel's own Retry asks from here on.
  const list = client.getQueryData<ListResult<Cluster>>(queryKeys.clusters);
  if (list?.state === 'ok' && expired(list.items.find((item) => item.name === cluster))) return;
  const watch = watchOf(client);
  if (watch.busy.has(cluster) || now < (watch.next.get(cluster) ?? 0)) return;
  watch.busy.add(cluster);
  watch.next.set(cluster, now + AGAIN_MS);
  void probeCluster(cluster)
    .then((answer) => {
      if (answer.state === 'ok' && !expired(answer.data)) watch.next.set(cluster, Date.now() + QUIET_MS);
    })
    // A probe that could not be made says nothing about the session; the page
    // keeps its own failures and the next read may ask again.
    .catch(() => undefined)
    .finally(() => {
      watch.busy.delete(cluster);
      void client.invalidateQueries({ queryKey: queryKeys.clusters });
    });
}
