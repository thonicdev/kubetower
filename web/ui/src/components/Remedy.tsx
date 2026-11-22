import type { ReactNode } from 'react';

import type { FailedState, Reason } from '../api';

/**
 * What an operator should *do*, one sentence per failed state.
 *
 * ## Why this exists at all
 *
 * Failure carried as **one string** draws `unreadable`, `absent`,
 * `unreachable` and `forbidden` as one sentence, none with a drawn form of its
 * own. This component draws the distinction. The obvious shortcut — render
 * `reason.message` for all four — passes every test anybody would think to
 * write, because every arm renders, every arm says something true, and nothing
 * is empty.
 *
 * What tells the four apart is not the color and not the wording of the
 * failure: it is **what the operator does next**, and the four answers are
 * genuinely different places to go.
 *
 * | State | Where the fault is | What to do |
 * |---|---|---|
 * | `unreachable` | between the console and the API server | wait, and try again |
 * | `forbidden` | the cluster's RBAC, working as configured | ask for a role |
 * | `absent` | nothing serves this kind here | install the thing that would |
 * | `unreadable` | **this console**, not the cluster | look at this console, not the cluster |
 *
 * The last row is the one that repays the whole component: an operator told
 * "could not be read" with no remedy goes and looks at a cluster that is
 * working.
 *
 * **The last row names no page**: `/health` leads to the console's home, the
 * cluster list. The sentence says where the fault is - this console, not the
 * cluster - without sending the reader to a page that is not there, in the
 * in-cluster console and the local one alike. Nothing here reads the profile:
 * `Remedy` needs no query client.
 *
 * ## Four rows, not five, because `pending` has no remedy
 *
 * This table deliberately has no arm for `pending`. `FailedState` is the four states that answer a read that
 * happened; `pending` says no read happened. There is nothing to fix, no role to
 * ask for, nothing to install, and no page of this console's is where
 * to look either — so a fifth sentence here would be advice invented for a
 * question nobody has put yet. `state/StateNotice.tsx` draws the true one:
 * an answer is coming and nothing is required.
 *
 * The call site in `ResourceList` narrows with `isFailedState` before rendering
 * this, which is what makes the absence checkable rather than a comment.
 *
 * ## It composes with `StateNotice` and does not replace it
 *
 * `state/StateNotice.tsx` draws the state — its name, its tone, the server's
 * sentence, the scope and the detail — and is what `ListView` and `ValueView`
 * render on a failed arm. This is the line underneath it. Two components rather
 * than one because the notice is also drawn 92 pixels wide in the rail, where
 * there is no room for a remedy and no page to act on.
 *
 * **The retry button stays with the notice**, where `reason.retryable` already
 * drives it. `api/openapi.yaml` is explicit that the flag "drives a retry
 * button, not a retry loop", and a second button here would be a retry
 * affordance this component decided to offer on its own — including on
 * `forbidden`, where retrying writes the same denial into the audit log every
 * few seconds.
 */

/**
 * The sentence, per state. Static text and no interpolation of anything the
 * server sent: `reason.message` is the server's own sentence and is rendered by
 * `StateNotice` verbatim. This adds the console's standing advice, which is a
 * property of the state and not of the occurrence.
 */
const REMEDY: Readonly<Record<FailedState, string>> = {
  unreachable:
    'The cluster did not answer. Nothing about it is known right now — including whether anything is running. Try again once it responds.',
  forbidden:
    'The cluster answered and refused. This is its RBAC working as configured, so another attempt will be refused too: ask for a role that permits this verb.',
  absent:
    'Nothing on this cluster serves this kind. A custom resource needs its definition installed before it can be listed.',
  unreadable:
    'The cluster answered and this console could not make sense of it. That is a fault in this console rather than in the cluster, so the cluster is not where to look.',
};

/**
 * What to ask for, spelled out of the scope the server sent.
 *
 * Only for `forbidden`, and only from `reason.scope` — never guessed. A denial
 * an operator can paste into a request for access is worth more than one they
 * have to translate, and the scope is exactly the three words RBAC is written
 * in: verb, resource, namespace.
 */
function accessLine(reason: Reason | undefined): string | undefined {
  const scope = reason?.scope;
  if (scope === undefined) return undefined;
  const verb = scope.verb ?? 'list';
  const resource = scope.resource;
  if (resource === undefined) return undefined;
  return scope.namespace === undefined
    ? `Ask for: ${verb} on ${resource}, cluster-wide.`
    : `Ask for: ${verb} on ${resource} in namespace ${scope.namespace}.`;
}

export interface RemedyProps {
  state: FailedState;
  reason?: Reason | undefined;
}

/**
 * `absent` about one object rather than a kind (`no_such_object`): the cluster
 * serves the kind and the object is not there. Installing anything would not
 * bring it back, so the sentence is the absence's own.
 */
const GONE =
  'The cluster serves this kind and this object is not there: it was deleted, or never made. Its list shows what there is now.';

export function Remedy({ state, reason }: RemedyProps): ReactNode {
  const access = state === 'forbidden' ? accessLine(reason) : undefined;
  const sentence = state === 'absent' && reason?.code === 'no_such_object' ? GONE : REMEDY[state];

  return (
    // `data-box`, so a panel that inset its boxed notices (`PANEL_BOX`) insets
    // this one with them: under a notice with a margin, a box without one
    // runs edge to edge.
    <div data-remedy={state} data-box className="mt-2 rounded-md border border-line bg-panel p-3">
      <p className="text-sm text-muted">{sentence}</p>

      {access !== undefined ? <p className="mt-1 font-mono text-xs text-fg">{access}</p> : null}
    </div>
  );
}

/** The table itself, for a test that asserts no two states say the same thing.
 *  Exported so the assertion reads the data rather than a rendering of it. */
export { REMEDY as remedies };
