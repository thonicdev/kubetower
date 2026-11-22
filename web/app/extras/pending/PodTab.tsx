import type { ReactNode } from 'react';
import type { ExtensionTabProps } from '@kubetower/ui';

import { PodCompact } from './PodCompact';

/** The Pending tab in a pod's panel: the compact tile, whose Expand opens the pod's full view. */
export default function PodTab({ cluster, namespace, name }: ExtensionTabProps): ReactNode {
  if (namespace === undefined) return null;
  return (
    <div data-pending-tab="">
      <PodCompact cluster={cluster} namespace={namespace} name={name} />
    </div>
  );
}
