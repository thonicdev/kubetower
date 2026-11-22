import { useState, type ReactNode } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import {
  invalidateActedObjects,
  previewBulk,
  runBulk,
  type ActOutcome,
  type BulkAffordance,
  type ConfirmationPlan,
  type PlanTarget,
} from '../api';
import { btnDanger, btnSecondary } from './buttons';
import { PlanDialog } from './PlanDialog';
import type { Selection } from './useSelection';

/**
 * What is ticked, and what can be done to it.
 *
 * ## It carries the verbs the server offered, and no others
 *
 * `ListPage.bulk` arrives with a verb list already answered permitted or not,
 * per kind and per cluster - `apireads.go`'s `bulkAffordance`. **Nothing here
 * derives a verb from a kind**, and the reason is written into that function
 * after it happened: the kind was being taken and thrown away, so every list was
 * offered every verb - Cordon on a Secrets table, Suspend on nodes. A pod
 * selection is not offered Restart either, because on a pod restart *is* delete,
 * and a bar offering both offers one request under two words with the
 * gentle-sounding one being the destructive one.
 *
 * A verb the server marked `permitted: false` is drawn **disabled with its
 * reason**, not hidden: an operator looking for Delete finds it and learns that
 * this cluster is read-only, rather than wondering whether they misremembered.
 *
 * ## Red comes from the flag and never from the word
 *
 * `BulkVerb.destructive` is the server's answer, from the same producer as the
 * drawer's - `specFor`, keyed on (verb, kind). Without it **Delete, Restart and
 * Clear would be the same button**, on the control that acts on twelve objects
 * at once rather than one.
 *
 * **Nothing here looks at the verb's name.** A bar that colors by string
 * reddens a custom resource's `delete-snapshot` by accident and stays gray for a
 * destructive verb somebody called `recycle`, and both mistakes are invisible
 * until the day they matter. The same holds for the order below, which is keyed
 * on the flag too and for the same two reasons.
 *
 * ## The order: the way out, then the verbs, non-destructive before destructive
 *
 * Written as a rule rather than as a list, because "Clear, Evict, Delete" is
 * right for Pods and says nothing
 * about a ConfigMap list, which offers Delete alone, or a Node list, which offers
 * nothing destructive at all. Bulk acts on everything ticked, so **a Delete that
 * sits third on one kind and first on another is a hazard muscle memory walks
 * into**; the position has to be predictable across every kind or it is not worth
 * ordering at all.
 *
 * **Clear comes first because it is not a verb.** It acts on the selection and
 * never on the cluster, so it is the way out of a set assembled by mistake -
 * and drawn last, the cheapest control would sit on the far side of the most
 * expensive one. It is the object drawer's distinction too (the controls that
 * only *look*, then the controls that *change*).
 *
 * **The order among the verbs is the server's**, in `verbsFor`'s per-kind
 * declaration order - the everyday ones first, the irreversible one last. It has
 * to be, because it is not derivable here: `destructive` is a boolean, and
 * Evict carries it as well as Delete, so a client sorting on that flag
 * has two buckets and a pod bar's whole verb set in one of them. "Evict before
 * Delete" is a judgment about two verbs that are *both* destructive, and it
 * lives in the file where that judgment was made.
 *
 * **The sort below is a safety net and nothing else.** `Array.prototype.sort` is
 * stable, so within each bucket the server's order survives exactly as sent - on
 * a pod list it reorders nothing at all. What it catches is the one thing the
 * server's own test cannot: a verb appended later to a kind's list, after Delete,
 * in a table nobody re-read.
 *
 * ## Preview, then act, and the act is bound to the plan
 *
 * Pressing a verb previews. `ConfirmationPlan` comes back with the count the
 * *server* resolved, the gesture it wants, and a token; only the dialog posts
 * that token. The server re-resolves the selection from it and answers 409 if
 * the set has moved - *a confirmation that is not about the set being acted on
 * is not a confirmation* - and a 409 is re-read here, never retried. A button
 * that deletes without that binding would be worse than no button.
 *
 * **What the act leaves stale is the kind's list *and* every object it touched**
 * - the same pair `Verbs.tsx` invalidates on the single-object path, taken from
 * the outcome's per-target results rather than from what was ticked. With the
 * list alone, a row bulk-deleted with its own drawer open would go on being
 * drawn, and offered verbs. `commit`'s `onSuccess` carries the whole argument.
 *
 * ## It is out of the flow, and that is not a layout taste
 *
 * A bar that takes
 * height on appearing pushes the table down by its own height at the instant the
 * operator clicks a checkbox - so the row under the pointer moves, and the
 * second tick of a run lands on the wrong row. It is absolutely positioned over
 * the foot of the panel; the caller supplies the positioning context.
 *
 * ## The `dropped` line
 *
 * A ticked row that has left the list is not a detail. `useSelection` prunes it
 * on a successful read and reports how many went; saying so is what stops a bulk
 * action being taken over a set the operator last saw three refreshes ago.
 */
export function SelectionBar({
  selection,
  cluster,
  kind,
  bulk,
  targetsFor,
}: {
  selection: Selection;
  cluster: string;
  kind: string;
  /** What the server offered for this kind. Absent where it offered nothing. */
  bulk?: BulkAffordance | undefined;
  /** The picked ids, as the contract's targets for one verb. The list page owns
   *  the mapping because it owns the rows; this component never parses an id.
   *  A toggle verb gets only the rows it would change (`Row.unchanged`), so its
   *  set may be smaller than the selection. */
  targetsFor: (picked: ReadonlySet<string>, verb: string) => readonly PlanTarget[];
}): ReactNode {
  const client = useQueryClient();
  const [plan, setPlan] = useState<ConfirmationPlan | undefined>(undefined);
  const [verb, setVerb] = useState<string>('');
  /* How many objects the plan is about: the selection, or fewer for a toggle
     some ticked rows are already on. Captured with the verb, so the dialog's
     title does not move when the list is re-read under it. */
  const [acting, setActing] = useState(0);
  const [outcome, setOutcome] = useState<ActOutcome | undefined>(undefined);
  const [failure, setFailure] = useState<string | null>(null);

  const review = useMutation({
    mutationFn: (which: string) =>
      previewBulk(cluster, { verb: which, kind, targets: targetsFor(selection.picked, which) }),
    onSuccess: (made) => {
      setPlan(made);
      setFailure(null);
    },
    onError: (error: Error) => setFailure(error.message),
  });

  const commit = useMutation({
    mutationFn: (confirmed: string | undefined) => runBulk(cluster, plan?.token ?? '', confirmed),
    onSuccess: (result) => {
      setOutcome(result);
      setFailure(null);
      /*
        **Every object the act touched, and the kind's list.** The two are
        different keys - `resourceKind` is `['resources', …]` and one object's
        own read is `['resource', …]`, sharing no prefix - and the list is not a
        stand-in for the objects: only the list heals itself, on the kind's
        interval, so bulk-deleting a row whose drawer is open would leave the
        panel drawing the object and offering verbs on it until the operator
        navigated away. Which keys that is, and why `metrics` is not among them,
        is `invalidateActedObjects` in `api/queries.ts` - one place, shared by
        every caller that commits a plan.

        **What stays this file's own decision is which objects were acted on**,
        and each of the two nearer sources is wrong in its own way:

          - the ticked selection is what this client *asked* for. The server
            re-resolves the set from the token and may act on a different one,
            which is what the 409 is about;
          - `plan.facts.targets` may be short of `facts.count` - "12 shown of
            47" is the contract's own example - so a set built from it silently
            misses the objects that were not listed.

        `ActOutcome.results` is per target always (`apireads.go`, from the set
        the server resolved), and the failed ones are in it too. Those are
        invalidated with the rest deliberately: a target that failed because it
        had already moved is exactly the one whose cached read is wrong.
      */
      void invalidateActedObjects(
        client,
        cluster,
        kind,
        result.results.map((one) => one.target),
      );
    },
    onError: (error: Error) => setFailure(error.message),
  });

  // Kept while its dialog is up: an act that removed every ticked row empties
  // the selection when the list is re-read, and the outcome - and the keyboard
  // it holds - would go with the bar.
  if (selection.count === 0 && plan === undefined) return null;

  // Stable, and keyed on the flag rather than the label - see the header. A copy,
  // because `sort` is in place and `bulk` belongs to the query's cached page.
  const verbs = [...(bulk?.verbs ?? [])].sort(
    (left, right) => Number(left.destructive === true) - Number(right.destructive === true),
  );

  const close = (): void => {
    setPlan(undefined);
    setOutcome(undefined);
    setFailure(null);
    // A finished act leaves nothing sensible ticked: the objects are gone, or
    // changed, and the set the operator assembled was about the act.
    if (outcome !== undefined) selection.clear();
  };

  return (
    <div
      id="bulk-bar"
      role="status"
      // **A box in the corner, not a band across the foot.** Three things
      // about it are load-bearing:
      //
      //   - `absolute` against the panel and never `fixed`. The dock is the
      //     content's flex sibling, so opening it shrinks the panel and this
      //     rises with it, live, while a fixed box would have to read `--dock-h`
      //     and be a second answer to a question the layout already answers.
      //   - **right**, because the checkboxes are in the Name column at the
      //     left, and a box on that side sits over what is being clicked.
      //   - a shadow, not just a border: this floats over rows that are the same
      //     color as it is, and a bordered strip on matching rows reads as a row
      //     that has gone wrong. `shadow-lg`, which is what the four menus in
      //     this console already float with - one lift, not two.
      //
      // `max-w` keeps it inside the panel when the verb list is long - the
      // panel less the `right-4` inset on both sides, in the theme's own
      // spacing so it moves with it - and
      // `flex-wrap` lets it grow upward rather than off the edge.
      className={`absolute right-4 bottom-3.5 z-10 flex max-w-[calc(100%-var(--spacing)*8)] flex-wrap items-center gap-2.5 rounded-lg border border-line bg-panel-2 px-3 py-2 text-sm shadow-lg`}
    >
      <span className="font-medium">{selection.count} selected</span>

      {selection.dropped > 0 ? (
        <span className="text-warn">{selection.dropped} no longer listed</span>
      ) : null}

      {/* The way out, first: it acts on the selection and never on the cluster.

          **`hover:text-fg`, and that is the whole of it.** On a bar where both
          verb buttons beside it answer the pointer, a control that did not
          would read as disabled.

          Not `hover:border-accent`, which is `hover.ts`'s `watching`:
          `border-accent` already means *this is the one you are on*, and the
          three treatments are the accent for what watches, Claude's color for
          Claude, the red for what destroys from the flag. Clear is none of
          them. It acts on the selection rather than on the cluster, so it takes
          what this console gives an ordinary neutral control: the same
          `text-muted hover:text-fg` the non-destructive verb below uses, and
          Cancel in the plan dialog. */}
      <button
        type="button"
        onClick={selection.clear}
        className={btnSecondary}
      >
        Clear
      </button>

      {verbs.map((one) => {
        const count = targetsFor(selection.picked, one.verb).length;
        return (
          <button
            key={one.verb}
            type="button"
            disabled={one.permitted !== true || review.isPending}
            title={
              one.permitted !== true
                ? (one.reason?.message ?? 'not permitted here')
                : count < selection.count
                  ? `${one.label} ${String(count)} of the ${String(selection.count)} selected: the others would not change`
                  : one.reason?.message
            }
            onClick={() => {
              setVerb(one.label);
              setActing(count);
              review.mutate(one.verb);
            }}
            data-destructive={one.destructive === true ? '' : undefined}
            className={one.destructive === true ? btnDanger : btnSecondary}
          >
            {one.label}
          </button>
        );
      })}

      {failure === null || plan !== undefined ? null : (
        <span role="alert" className="text-bad text-xs">
          {failure}
        </span>
      )}

      {plan === undefined ? null : (
        <PlanDialog
          title={`${verb} ${String(acting)} ${acting === 1 ? 'object' : 'objects'}?`}
          plan={plan}
          pending={commit.isPending}
          error={failure}
          outcome={outcome}
          onCancel={close}
          onApply={(confirmed) => commit.mutate(confirmed)}
        />
      )}
    </div>
  );
}
