import type { ReactNode } from 'react';
import { Navigate, Outlet } from 'react-router';

import { useSharedCluster } from '../shell/sharedCluster';
import { Pending } from '../state/StateNotice';
import { clusterPath } from './clusterPath';

/**
 * The layout over `/`: an in-cluster console opens on its one cluster, and
 * every other console draws the page under it.
 *
 * The condition is `useSharedCluster`'s, and its header says why this is the one
 * place keyed on the profile rather than on the count. The address is replaced
 * rather than pushed, so Back from the cluster does not return to a list -
 * after sign-in, which lands on `/` (`auth/next.ts`), or from a bookmark.
 * `/health`, an older address of the home, redirects to `/` and so
 * comes through here too (`router/routes.tsx`).
 *
 * **While it cannot tell, it draws a wait and not the page under it**, which is
 * how the card grid is never shown in this profile: the capabilities and the
 * cluster list are asked in parallel, and a list that answered first with one
 * cluster would otherwise be drawn as one card until the profile arrived. The
 * wait is the one a page's chunk draws (`router/routes.tsx`, `page()`), so on
 * the desktop and the container it is a sentence they already draw on a cold
 * load, for the length of a capabilities read, and then the page as before.
 *
 * **A list that failed, or named nothing, opens nothing**: there is no cluster
 * to go to, and an empty answer and a failed one are different answers that the
 * list page draws as such.
 */
export function OneClusterLanding(): ReactNode {
  const { cluster, deciding } = useSharedCluster();
  if (cluster !== undefined) return <Navigate to={clusterPath(cluster.name)} replace />;
  if (deciding) return <Pending what="this page" />;
  return <Outlet />;
}
