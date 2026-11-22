import type { ReactNode } from 'react';

import type { ValueResult } from '../../api';
import { StateNotice } from '../../state/StateNotice';

/**
 * `PanelList`'s sibling for a panel that is one answer rather than a collection.
 *
 * The dashboard's `history` is the case: one panel, one series set, one answer.
 * `ValueResult` has **no `empty` arm** and that is `api/errors.md`'s rule rather
 * than an omission here - *a single thing that is not there is `absent`*, and two
 * spellings of one fact is how two clients end up disagreeing about which to
 * check. So there are exactly three shapes below: not in the payload, an answer,
 * and a reason.
 *
 * Same property as `PanelList` and it is the one that matters: **`children` is
 * called only on the arm that carries `data`.** There is no `data ?? {}` to reach
 * for, because `ValueResult` does not have one.
 *
 * Written here beside `PanelList` rather than in `state/Result.tsx`.
 */
export interface PanelValueProps<T> {
  /** Already narrowed by `data.ts`. `undefined` means the payload carried no
   *  such panel at all, which is not one of the six states. */
  result: ValueResult<T> | undefined;
  /** What the panel is, in the reader's words. */
  what: string;
  onRetry?: (() => void) | undefined;
  children: (data: T) => ReactNode;
}

export function PanelValue<T>({ result, what, onRetry, children }: PanelValueProps<T>): ReactNode {
  if (result === undefined) return <PanelMissing what={what} />;
  if (result.state === 'ok') return children(result.data);
  return <StateNotice state={result.state} reason={result.reason} onRetry={onRetry} />;
}

/**
 * The dashboard answered and said nothing about this panel.
 *
 * Not `absent`, not `empty`, and deliberately worded like neither - the same
 * argument as `Panel.tsx`'s own `PanelMissing`, which this deliberately
 * duplicates rather than exporting across: a panel that is missing from the
 * payload is a disagreement between the contract and the implementation, a fault
 * in this product rather than a fact about a cluster. Someone told `absent`
 * here would go and look at the cluster.
 */
function PanelMissing({ what }: { what: string }): ReactNode {
  return (
    <div role="status" data-state="missing" className="rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className="font-medium text-warn">Not answered</p>
      <p className="mt-1 text-muted">
        The dashboard was read and carried nothing at all about {what} - not an answer, and not a
        reason either.
      </p>
    </div>
  );
}
