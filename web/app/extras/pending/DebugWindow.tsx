import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { focusRing, type ExtensionTabProps } from '@kubetower/ui';

import { KEY } from './data';
import { PodView } from './PodView';

/**
 * Pod Debugging's window, opened from the tile on a Pending pod's panel: why
 * it waits, the node × check grid, what it would take - the pod's own view -
 * and the way to every pending pod, which the menu no longer offers.
 */
export default function DebugWindow({ cluster, namespace, name }: ExtensionTabProps): ReactNode {
  if (namespace === undefined) return null;
  return (
    <div data-pending-window="" className="flex flex-col gap-3">
      <PodView cluster={cluster} namespace={namespace} name={name} />
      <Link
        data-pending-all=""
        to={`/c/${encodeURIComponent(cluster)}/x/${KEY}`}
        className={`self-end text-xs text-accent hover:underline ${focusRing}`}
      >
        All pending pods →
      </Link>
    </div>
  );
}
