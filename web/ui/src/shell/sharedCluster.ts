import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { useSyncExternalStore } from 'react';

import { useCapabilities, useClusters, type Cluster } from '../api';
import { onlyCluster } from './clusterState';

/**
 * The in-cluster console and its one cluster.
 *
 * ## The one place the shell is keyed on the profile
 *
 * Everything else in the shell follows **the count, never the profile**
 * (`onlyCluster`): the rail goes for one cluster on a desktop as much as in a
 * pod. This does not, and deliberately: only the in-cluster console opens its
 * cluster straight away, so a desktop with one kubeconfig context keeps its
 * chooser and Kinds in the top bar. (`/health` leads to `/`.)
 * A console the server runs in a cluster serves that cluster and no other,
 * so a page choosing between contexts describes a choice that does
 * not exist there.
 *
 * So the key is both: `Capabilities.mode` is `shared` (`internal/web/apimount.go`
 * sets it under `inCluster`), **and** the list holds its one cluster. The second
 * half is what keeps an empty answer and a failed one visible: a list that
 * failed or named nothing has no cluster to open, so `/` keeps drawing the
 * list's own failure.
 *
 * Read by the landing (`clusters/OneClusterLanding.tsx`) and by the top bar,
 * from one function so that the redirect and the crumb cannot disagree about
 * which console they are in.
 *
 * ## Until the profile is known
 *
 * The capabilities and the cluster list are asked in parallel, and either can
 * answer first. A list that says one cluster before the profile has arrived is
 * a console that may be about to leave for that cluster, or not: so what would
 * be drawn and then taken away waits for the profile - the card grid and
 * Kinds (`useKindsAt`). Where the list has already said
 * anything but one, nothing waits: no profile changes what is drawn then.
 *
 * **Every wait here ends at `LOAD_WAIT_MS`**, the profile's and the list's
 * alike, measured once per load (`useLoadWaitOver`). `request.ts` sets no
 * timeout, so a read that hangs would otherwise keep `/` on its wait for good.
 * After it the console draws what it draws for a list that has not said one
 * cluster: the card grid. A list a browser offline has paused does not wait at
 * all.
 */
export interface SharedCluster {
  /** The cluster to open on: the profile is `shared` and the list holds exactly
   *  this one. `undefined` everywhere else, and while either is unread. */
  cluster: Cluster | undefined;
  /** The landing cannot tell yet whether it is to open the one cluster: the
   *  profile is unread and the list could still say one, or the profile is
   *  `shared` and the list is on its first read. Drawing the card grid in that
   *  window is the flash the issue rules out. */
  deciding: boolean;
}

export function useSharedCluster(): SharedCluster {
  const capabilities = useCapabilities();
  const clusters = useClusters();
  const waitOver = useLoadWaitOver();
  const profileRead = capabilities.isFetched || waitOver;
  const shared = capabilities.data?.mode === 'shared';
  const one = onlyCluster(clusters.data);
  // The list's first attempt, as `CanonicalCluster` reads it: pending, asking,
  // and not failed yet. A read that failed once, or that a browser offline has
  // paused, ends the wait - the console then draws what it would draw anyway,
  // the failure included, rather than holding on a server that is not answering.
  // And so does the load's wait running out, for a read that never answers.
  const listFirst =
    clusters.isPending && clusters.fetchStatus === 'fetching' && clusters.failureCount === 0 && !waitOver;
  const cluster = shared ? one : undefined;
  return {
    cluster,
    deciding: cluster === undefined && ((!profileRead && (listFirst || one !== undefined)) || (shared && listFirst)),
  };
}

/** How long a load waits for the profile and the cluster list before it draws
 *  as if neither had said anything: `Rail.tsx`'s `KINDS_WAIT_MS`, and for its
 *  reason. */
const LOAD_WAIT_MS = 8_000;

/**
 * Whether the capabilities have been read once - answered or failed - or the
 * load's wait has run out. `isFetched` and not `!isPending`, for the reason
 * `Rail.tsx` gives for Kinds: a read that failed goes back to pending while it
 * is asked again. **Bounded by `LOAD_WAIT_MS`**, as the rail's wait is:
 * `request.ts` sets no timeout, so a read that hangs
 * would otherwise hold the landing on its wait and hide
 * Kinds for good, on every profile. After the wait an unread profile is not
 * `shared`.
 */
export function useProfileRead(): boolean {
  const capabilities = useCapabilities();
  const waitOver = useLoadWaitOver();
  return capabilities.isFetched || waitOver;
}

/**
 * One clock per load. The first reader starts it, and every reader after -
 * the Shell, the top bar, a page opened a minute later - reads the same one, so
 * the cap is measured once from the start of the load rather than again from
 * each component's mount. One timer per load, armed by the first subscription.
 *
 * Keyed on the query client because that is what a load is: the application
 * makes one (`createApp.tsx`), and so does every test that mounts it, so a
 * second mount in the same module starts a clock of its own.
 */
interface LoadWait {
  over: boolean;
  readonly listeners: Set<() => void>;
}

const LOAD_WAITS = new WeakMap<QueryClient, LoadWait>();

function loadWait(client: QueryClient): LoadWait {
  let wait = LOAD_WAITS.get(client);
  if (wait === undefined) {
    const started: LoadWait = { over: false, listeners: new Set() };
    setTimeout(() => {
      started.over = true;
      for (const listener of started.listeners) listener();
    }, LOAD_WAIT_MS);
    LOAD_WAITS.set(client, started);
    wait = started;
  }
  return wait;
}

/** Whether `LOAD_WAIT_MS` has passed since this load's first reader. */
export function useLoadWaitOver(): boolean {
  const client = useQueryClient();
  return useSyncExternalStore(
    (listener) => {
      // Subscribing is what starts the clock: in a commit, never in a render.
      const wait = loadWait(client);
      wait.listeners.add(listener);
      return () => {
        wait.listeners.delete(listener);
      };
    },
    () => LOAD_WAITS.get(client)?.over ?? false,
  );
}
