import { useCapabilities, useClusters } from '../api';
import { onlyCluster } from './clusterState';
import { useProfileRead } from './sharedCluster';

/**
 * Whether `Shell` draws the rail. One answer, read by the Shell that draws it
 * and by `useKindsAt` below, which puts the Kinds button somewhere else exactly
 * where the rail is not drawn (`KindsToggle.tsx`) - two conditions would leave a
 * window where neither drew it, or both.
 *
 * Not drawn for one cluster (`onlyCluster`, the count and never the profile).
 * A pending list is not "one cluster", so a cold load draws the rail and then
 * takes it away. **Except on a shared console**: the server profile serves one
 * cluster, so once the capabilities say `shared` there is no rail to draw
 * while the list is read. A list that failed is not pending, and
 * draws the rail that reports it.
 */
export function useRailDrawn(): boolean {
  const clusters = useClusters();
  const capabilities = useCapabilities();
  const alone =
    onlyCluster(clusters.data) !== undefined ||
    (clusters.isPending && capabilities.data?.mode === 'shared');
  return !alone;
}

/**
 * Where the Kinds button is: **at the bottom left in every mode**:
 * in the rail's foot where there is a rail; in the same
 * corner, on its own, where there is none - one cluster on the desktop, and
 * the in-cluster console, which would otherwise have no way to fold the column
 * or to get its phone-width overlay. `none` while the profile is unread.
 */
export function useKindsAt(): 'rail' | 'corner' | 'none' {
  const railDrawn = useRailDrawn();
  const capabilities = useCapabilities();
  const profileRead = useProfileRead();
  if (railDrawn) return 'rail';
  return profileRead || capabilities.data?.mode === 'shared' ? 'corner' : 'none';
}
