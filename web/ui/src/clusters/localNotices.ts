import { queryOptions, useQuery } from '@tanstack/react-query';

import { asValue, request, type Cluster, type ValueResult } from '../api';
import type { ValueEnvelopeWire } from '../api/envelope';
import type { components } from '../api/schema';

/**
 * The machine's own notices, for the home's cluster cards: an expired AWS SSO
 * session with the command that renews it, and an AWS CLI credential cache
 * that will not read, with the files to delete - each on the card of the
 * cluster it concerns.
 *
 * **The source is `GET /api/health`'s `notices`**, the only answer that carries
 * them. The rows of that report are not read: the cards already have each
 * cluster's state, reason and `remedy` from `GET /api/clusters`, written by the
 * same server function (`clusterRemedy`).
 *
 * ## Which card a notice belongs to
 *
 * A notice names no context, so the concern is derived from what the server
 * did say:
 *
 * - **`sso`** goes on every card whose cluster signs in through the AWS CLI
 *   (`Cluster.aws`) **and** failed with `credentials_expired`, the code the
 *   probe gives a cluster whose sign-in lapsed. Several contexts on one SSO
 *   session each get it. A GKE or OIDC context that failed the same way is
 *   not told to run `aws sso login`. **What is not known**: which SSO session
 *   a context's AWS profile belongs to - the server does not carry it - so an
 *   AWS context on another session than the one reported would get it too.
 * - **The two credential caches** (`cache/container`, `cache/host`) go on no
 *   card: the server says what is broken and deliberately not which cluster it
 *   explains (`cacheBannerOf`, `internal/web/health.go`). They are drawn once,
 *   above the cards.
 * - **An `sso` notice no card matches** - the session cache could not be read
 *   (`measured: false`), or no cluster has failed on it yet - is drawn above
 *   the cards too. A notice is never dropped for want of a card.
 *
 * ## A failed read says nothing about local causes
 *
 * If `/api/health` does not answer `ok`, no card says anything about the
 * machine: no notice is not the same as "no problem", and a card is no place to
 * guess. The home says once, muted, that the local checks were not read
 * (an empty answer and a failed one are different answers).
 */

export type LocalNotice = components['schemas']['HealthNotice'];
type HealthReport = components['schemas']['HealthReport'];

export const localNoticesKey = ['health', 'notices'] as const;

async function getHealth(signal?: AbortSignal): Promise<ValueResult<HealthReport>> {
  const wire = await request<ValueEnvelopeWire<HealthReport>>('/api/health', signal ? { signal } : {});
  return asValue(wire);
}

/** Polled at the cluster list's own thirty seconds: a session renewed in a
 *  terminal leaves the cards within one read. */
export function localNoticesQuery() {
  return queryOptions({
    queryKey: localNoticesKey,
    queryFn: ({ signal }) => getHealth(signal),
    refetchInterval: 30_000,
  });
}

/** The code the probe gives a cluster whose sign-in lapsed. */
const SSO_FAILURE = 'credentials_expired';

export interface PlacedNotices {
  /** The read answered `ok`; `false` is a read that failed or has not answered. */
  read: boolean;
  /** Why it was not read, where the answer said. */
  why?: string;
  /** Per context name, the notices drawn on that cluster's card. */
  byCluster: ReadonlyMap<string, readonly LocalNotice[]>;
  /** The notices no card claims, drawn once above the cards. */
  unplaced: readonly LocalNotice[];
}

const NOTHING: PlacedNotices = { read: false, byCluster: new Map(), unplaced: [] };

/** Where each notice goes, given the clusters on the page. */
export function placeNotices(
  answer: ValueResult<HealthReport> | undefined,
  clusters: readonly Cluster[],
): PlacedNotices {
  if (answer === undefined) return NOTHING;
  if (answer.state !== 'ok') {
    return { ...NOTHING, why: answer.reason.message };
  }
  const notices = answer.data.notices ?? [];
  const byCluster = new Map<string, LocalNotice[]>();
  const unplaced: LocalNotice[] = [];
  for (const notice of notices) {
    const concerned =
      notice.key === 'sso'
        ? clusters.filter((cluster) => cluster.aws === true && cluster.reason?.code === SSO_FAILURE)
        : [];
    if (concerned.length === 0) {
      unplaced.push(notice);
      continue;
    }
    for (const cluster of concerned) {
      byCluster.set(cluster.name, [...(byCluster.get(cluster.name) ?? []), notice]);
    }
  }
  return { read: true, byCluster, unplaced };
}

/** The home's read of the notices, placed on the clusters it lists. */
export function useLocalNotices(clusters: readonly Cluster[]): PlacedNotices {
  const health = useQuery(localNoticesQuery());
  if (health.isError) {
    return { ...NOTHING, why: health.error instanceof Error ? health.error.message : String(health.error) };
  }
  return placeNotices(health.data, clusters);
}
