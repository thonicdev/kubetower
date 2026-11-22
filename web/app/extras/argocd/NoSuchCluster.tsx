import type { ReactNode } from 'react';
import { StateNotice, type Reason } from '@kubetower/ui';

/**
 * A cluster the console does not know, as one notice: the console's sentence
 * once, no failure and no "Try again" - the Delivery Center's drawing, for the
 * Argo CD surfaces that read one thing (the Application page, the Argo CD tab
 * and the difference under a resource's row). What it draws comes from
 * `unknownClusterOf` (`data.ts`), so the state, the code and the scope are the
 * Delivery Center's own.
 */
export function NoSuchCluster({ unknown }: { unknown: { state: 'absent' | 'unreadable'; reason: Reason } }): ReactNode {
  return (
    <div data-refusal="cluster_unknown">
      <StateNotice state={unknown.state} reason={unknown.reason} title="No such cluster" />
    </div>
  );
}
