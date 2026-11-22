import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Navigate, Outlet, useLocation, useParams } from 'react-router';

import { probeCluster, queryKeys, useClusters } from '../api';
import { ClusterDown } from '../pages/overview/ClusterDown';
import { FORWARDS } from '../router/pages';
import { expired, useClusterVerdict } from '../shell/downCluster';
import { Pending } from '../state/StateNotice';
import { canonicalClusterAddress, clusterAddressMayMove, clusterPath } from './clusterPath';

/**
 * The `/c/:cluster` layout. It moves an address naming the cluster in an old
 * spelling to the canonical one, and otherwise renders the page under it.
 *
 * The rules, and why the redirect cannot loop, are on `canonicalClusterAddress`.
 * `replace` because the old spelling is not a place: two history entries for one
 * page would make Back land on the same page again.
 *
 * **While the cluster list is on its first attempt, an address that could
 * still move draws `Pending` instead of the page.** Rendering the page and
 * redirecting afterwards would mount it, tear it down and mount it again. On
 * the Headlamp `--` spelling the param changes across the redirect, so the
 * page's reads would be made again under the new name, and the list consumes
 * `?open=` on mount (`ResourceList.tsx`), so the object it named would never
 * open. "Could still move" is
 * `clusterAddressMayMove`, a necessary condition for a redirect worked out from
 * the address alone, so an address that can only resolve to itself - every
 * link the console writes, for a name without `--` or `__` - draws at once and
 * never waits on the list.
 *
 * **The hold ends at the first sign that the list will not come soon**, and the
 * page then draws in the spelling it arrived in:
 *
 * - the first attempt failed (`failureCount > 0`). The query client retries a
 *   5xx or a network failure twice, 1 s and 2 s apart, and the query stays
 *   `pending` through them; holding until they are spent would keep the page
 *   back for about 3 s. If a retry then succeeds, the address is moved and the
 *   page is drawn again - the cost accepted for not waiting on a failing list;
 * - the query is paused, which is what an offline browser does to it: holding
 *   on that would hold until the network came back;
 * - the list answered with no names to resolve against: `empty`, `unreadable`,
 *   or an `ok` list that does not name the cluster. Nothing is redirected;
 * - a 401 is not retried, and goes on to the sign-in redirect.
 *
 * ## A cluster the probe found unreachable is arrived at on its warning
 *
 * The pages under `/c/:cluster` but two are views
 * of something read from the cluster - a kind's list, an object, Helm, an
 * extension's page - and a cluster the probe found unreachable has none to
 * show (its page loads nothing and is one warning and one Refresh,
 * `pages/overview/ClusterDown.tsx`). So an **arrival** on such an
 * address, while the verdict is `unreachable`, is replaced by `/c/<cluster>`,
 * where that warning is drawn. The rail's tile already links such a cluster to
 * its own address; this is the arrival by any other road - a bookmark, a typed
 * address, Back, a link. `ReachableOnly` holds the rules:
 *
 * - **On arrival, never under a page already drawn.** The decision is taken
 *   once per address, at the first render that knows the verdict. A page let
 *   through stays when the background round later finds the cluster down: the
 *   list is re-read every 30 s, and a redirect then would unmount an object
 *   page with a value being edited (`pages/object/Data.tsx`'s draft) and, with
 *   `replace`, take its address out of the history.
 *   The page is kept only while the person stays on it: once they leave, a
 *   return to it - Back among them - is an arrival again, decided again.
 * - **On a fresh verdict only.** One older than thirty seconds is asked again
 *   first (`shell/downCluster.ts`), with *Reading this cluster…* in the page's
 *   place: a cluster that answers keeps the address, its kind, namespace and
 *   query; one still down is replaced as above. A return to that address later
 *   is asked again if the verdict has aged again by then.
 * - **Not the port-forwards** (`port-forwards`). That page
 *   is this process's own forwards, not a read of the cluster, and it is where
 *   a forward to a cluster that has gone away is listed and stopped.
 * - **Only once the list has said so**: a page on a canonical address does not
 *   wait for it (above), so a cold load of a deeper address on a down cluster
 *   draws the page, and its first read goes out, before it is replaced.
 *
 * The kind remembered for the rail is not touched, so once the cluster answers
 * its tile leads back to it.
 */
export function CanonicalCluster(): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  const { pathname, search, hash } = useLocation();
  const clusters = useClusters();
  // Asked before any return: a hook behind a condition changes the hook order
  // with the address. `fresh` is what decides whether the panel checks first.
  const { fresh } = useClusterVerdict(cluster);
  if (cluster === undefined) return <Outlet />;
  const firstAttempt =
    clusters.isPending && clusters.fetchStatus === 'fetching' && clusters.failureCount === 0;
  if (firstAttempt && clusterAddressMayMove(pathname, cluster)) {
    return <Pending what="this cluster" />;
  }
  const names = clusters.data?.state === 'ok' ? clusters.data.items.map((item) => item.name) : [];
  const target = canonicalClusterAddress(pathname, cluster, names);
  if (target !== undefined) return <Navigate to={`${target}${search}${hash}`} replace />;
  // A cluster whose credentials have lapsed is one message on every page of it,
  // not each page's own failure: every read would be refused the same way,
  // and the page would show the kinds menu's error, half a list and "could not
  // be asked" at once. The overview draws the same panel
  // for an unreachable cluster; this does it for the whole scope.
  const known = clusters.data?.state === 'ok' ? clusters.data.items.find((item) => item.name === cluster) : undefined;
  if (known !== undefined && expired(known)) {
    return (
      <div className="p-4">
        <ClusterDown
          key={known.name}
          cluster={known.name}
          state={known.state}
          reason={known.reason}
          remedy={known.remedy}
          // An old verdict is asked again on arrival, as the overview does for
          // an unreachable cluster: a session renewed since may answer now.
          check={!fresh}
        />
      </div>
    );
  }
  return <ReachableOnly cluster={cluster} pathname={pathname} />;
}

/** The page under the cluster, or its own address where the probe found it
 *  unreachable on arrival. See `CanonicalCluster`'s last section. */
function ReachableOnly({ cluster, pathname }: { cluster: string; pathname: string }): ReactNode {
  const { settled, down, fresh } = useClusterVerdict(cluster);
  // Deeper is counted in segments, not compared with `clusterPath` as text: a
  // name the router spells otherwise than `clusterSegment` would compare unequal
  // on the cluster's own address, and the redirect would never settle. The
  // cluster's segment holds no `/` (`clusterSegment` encodes it), so its own
  // address is `/c/<segment>`, three parts, and a page under it more.
  const segments = pathname.split('/').filter((part, index) => index === 0 || part !== '');
  const guarded = segments.length > 3 && segments[3] !== FORWARDS;
  // The address whose page has been let through: it stays while the person
  // stays on it, whatever the verdict becomes, and is forgotten as soon as they
  // leave - so Back to it is an arrival again, decided again. Recorded during
  // render, as `pageFacts.tsx` keeps its trail,
  // so the page is never drawn once before the record exists.
  const [kept, setKept] = useState<string | undefined>(undefined);
  const lets = guarded && kept !== pathname && settled && down === undefined;
  const keep = lets || kept === pathname ? pathname : undefined;
  if (keep !== kept) setKept(keep);
  const deciding = guarded && kept !== pathname && !lets && down !== undefined;
  const stale = deciding && !fresh;

  // A stale verdict is asked again, once per arrival, and the decision waits for
  // the list re-read with the probe's answer. `probed` is the address whose
  // probe has been answered, forgotten like `kept` once the address is left,
  // so a return to it after the verdict has aged is probed again. The ref keeps
  // StrictMode's second effect from asking twice, and is cleared whenever there
  // is nothing to ask, which leaving the address always is.
  // Each probe carries the visit it was asked for, and only the one asked for
  // the current visit settles it: a probe from an earlier visit to the same
  // address, still out when the person left and came back, would otherwise
  // decide the return on its answer before the return's own probe has said
  // anything.
  const [probed, setProbed] = useState<string | undefined>(undefined);
  if (probed !== undefined && probed !== pathname) setProbed(undefined);
  const client = useQueryClient();
  const visit = useRef(0);
  const probe = useMutation({
    mutationFn: (_for: { address: string; visit: number }) => probeCluster(cluster),
    onSettled: async (_answer, _error, sent) => {
      await client.invalidateQueries({ queryKey: queryKeys.clusters });
      if (sent.visit === visit.current) setProbed(sent.address);
    },
  });
  const asked = useRef<string | undefined>(undefined);
  const { mutate } = probe;
  useEffect(() => {
    if (!stale) {
      asked.current = undefined;
      return;
    }
    if (asked.current === pathname) return;
    asked.current = pathname;
    visit.current += 1;
    mutate({ address: pathname, visit: visit.current });
  }, [stale, pathname, mutate]);

  if (!deciding) return <Outlet />;
  if (stale && probed !== pathname) return <Pending what="this cluster" />;
  return <Navigate to={clusterPath(cluster)} replace />;
}
