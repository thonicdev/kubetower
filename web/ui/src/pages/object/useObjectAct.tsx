import { useState, type ReactNode } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import {
  ApiError,
  invalidateActedObjects,
  previewAction,
  runAction,
  type ActOutcome,
  type ConfirmationPlan,
  type DetailAction,
} from '../../api';
import { PlanDialog } from '../../components/PlanDialog';
import type { ObjectIdentity } from './address';

/**
 * One verb on one object: preview, the plan dialog, then the act.
 *
 * **One flow, two ways in.** The drawer's verb buttons (`Verbs.tsx`) and a list
 * row's context menu (`ResourceList.tsx`) both start here,
 * so a verb chosen from a row goes through the same plan, the same gesture, the
 * same 409 re-read and the same invalidation as the same verb pressed in the
 * drawer. A second copy of this state machine would be the first thing to
 * drift: a copy would miss the round key below, for instance.
 *
 * The object is given to `start` rather than to the hook, because a list's
 * menu acts on whichever row it was opened on, and the dialog has to keep
 * naming that object after the menu has closed.
 *
 * ## Preview, then act
 *
 * Pressing a verb previews and touches nothing. The plan carries the sentence,
 * the facts, the gesture and a token; `PlanDialog` draws it and only that dialog
 * posts the token. On success the object and its list are re-read: the act
 * changed what they say, and a panel still showing the old answer is the defect
 * this whole contract is arranged against.
 *
 * ## A verb in stages
 *
 * An outcome may carry `nextPlan`: a drain cordons and lists on the first
 * confirmation, and its evictions are a plan of their own, minted by the server
 * from that outcome. The dialog then opens that plan - the pods by name, each
 * with its warning, the node's name to type again - in place of closing, with
 * the first stage's sentence above it. Drawing the outcome as "1 done" and
 * closing would leave its sentence, *confirm again to evict*, describing a
 * request nothing could send.
 */
export function useObjectAct(
  /** Called once an act has finished, so the panel that owns the object can
   *  close itself where the object is gone. Deciding that here would mean
   *  knowing which verbs remove things, which is the table again. */
  onActed?: (outcome: ActOutcome, verb: string) => void,
  /** Called once the person closes an act's outcome. A panel that leaves an
   *  object it removed leaves here, not on `onActed`: the outcome dialog says
   *  what was done, and it stays up until it is closed even where the object
   *  is gone meanwhile. */
  onClosed?: (outcome: ActOutcome, verb: string) => void,
): {
  /** Preview `action` on `identity`, and open the dialog on the plan. */
  start: (identity: ObjectIdentity, action: DetailAction) => void;
  /** A preview is in flight: the verbs are not pressed twice. */
  pending: boolean;
  /** A preview is in flight or its dialog is open: `start` does nothing until
   *  it is answered and closed, so the dialog and Apply name one object. */
  busy: boolean;
  /** Why the last preview failed, while no dialog is open to say it. */
  failure: string | null;
  /** The plan dialog, or nothing. The caller draws it where it belongs. */
  dialog: ReactNode;
} {
  const client = useQueryClient();
  const [target, setTarget] = useState<ObjectIdentity | undefined>(undefined);
  const [plan, setPlan] = useState<ConfirmationPlan | undefined>(undefined);
  const [label, setLabel] = useState('');
  // The verb this confirmation is about, for `onActed`: a panel that closes on
  // a delete it made needs to know it was one.
  const [asked, setAsked] = useState('');
  const [outcome, setOutcome] = useState<ActOutcome | undefined>(undefined);
  const [notice, setNotice] = useState<string | undefined>(undefined);
  const [failure, setFailure] = useState<string | null>(null);
  /**
   * Which confirmation this is, for the dialog's key. A new preview and a next
   * stage start a new one; the fresh plan a 409 carries does not.
   *
   * It is not the token, because the token changes on all three - so a 409
   * would clear the name the user had typed, and on a node, whose plan the
   * kubelet's heartbeat refuses every minute, it would be cleared every time.
   * The fresh plan is the same confirmation, re-read: the user reads what
   * changed and confirms again with what they typed still there. A
   * name typed to cordon still does not stand as the name typed to evict,
   * because the next stage is a new round.
   */
  const [round, setRound] = useState(0);

  const review = useMutation({
    // A verb pressed on an object, or the request a plan's `alternative`
    // offers - which is the server's, and is previewed exactly as it came.
    mutationFn: ({
      on,
      asked,
    }: {
      on: ObjectIdentity;
      asked: string | Parameters<typeof previewAction>[1];
    }) =>
      previewAction(
        on.cluster,
        typeof asked !== 'string'
          ? asked
          : {
              verb: asked,
              target: {
                kind: on.kind,
                ...(on.namespace === undefined ? {} : { namespace: on.namespace }),
                name: on.name,
              },
            },
      ),
    onSuccess: (made, { on }) => {
      // The object is the one this plan was asked about, taken from the
      // request rather than from the last `start`: the dialog's title and the
      // invalidation after Apply are then always the plan's own object.
      setTarget(on);
      setPlan(made);
      setRound((n) => n + 1);
      setNotice(undefined);
      setFailure(null);
    },
    onError: (error: Error) => setFailure(error.message),
  });

  const commit = useMutation({
    mutationFn: ({ on, confirmed, force }: { on: ObjectIdentity; confirmed: string | undefined; force: boolean }) =>
      runAction(on.cluster, plan?.token ?? '', confirmed, force),
    onSuccess: (result, { on }) => {
      setFailure(null);
      // Every read of the object, and the row it lives in: a verb that scales
      // or annotates also changes the manifest, the data block and the
      // field-manager record, not only the object's own read and the kind's
      // list. `invalidateActedObjects` holds the whole list, beside the keys
      // that define it. A verb that removed the object leaves the
      // object's own read answering `absent`, which is a sentence the panel
      // knows how to say.
      void invalidateActedObjects(client, on.cluster, on.kind, [
        {
          kind: on.kind,
          ...(on.namespace === undefined ? {} : { namespace: on.namespace }),
          name: on.name,
        },
      ]);
      if (result.nextPlan !== undefined) {
        // One stage of several. The next plan is opened in place of the
        // outcome, and `onActed` waits for the last stage: a parent that closes
        // on it would close over the plan the user is about to read.
        setNotice(result.summary);
        setPlan(result.nextPlan);
        setRound((n) => n + 1);
        return;
      }
      setOutcome(result);
      onActed?.(result, asked);
    },
    onError: (error: Error) => {
      setFailure(error.message);
      // A 409 spent the token and carries the plan as it stands now: the object
      // moved under the confirmation, and what the user reads next has to
      // be that, not the plan they already agreed to. Adopting it is what makes
      // the precondition a second look rather than a dead end - and adopting it
      // in the same round keeps what was typed (see `round`).
      if (error instanceof ApiError && error.plan !== undefined) setPlan(error.plan);
    },
  });

  /*
    **One object per confirmation.** A list row's menu can be opened while a
    dialog is up - the dialog is not modal, so Tab leaves it - and a second verb
    from another row would set the target before its plan arrived: the dialog
    would read "Delete B?" while Apply posted A's token. So nothing starts while a preview is in flight or a plan is open;
    the caller says so (the row menu draws its verbs refused, with the reason).
  */
  const busy = review.isPending || plan !== undefined;
  const start = (identity: ObjectIdentity, action: DetailAction): void => {
    if (busy) return;
    setLabel(action.label);
    setAsked(action.verb);
    review.mutate({ on: identity, asked: action.verb });
  };

  const dialog =
    plan === undefined || target === undefined ? null : (
      <PlanDialog
        // Keyed on the round, not the token: the next stage starts with
        // nothing typed - a name typed to cordon must not stand as the name
        // typed to evict - and the fresh plan a 409 carries keeps what was
        // typed against the plan it replaces.
        key={round}
        title={`${label} ${target.name}?`}
        plan={plan}
        pending={commit.isPending}
        error={failure}
        outcome={outcome}
        notice={notice}
        onCancel={() => {
          const read = outcome;
          setPlan(undefined);
          setOutcome(undefined);
          setNotice(undefined);
          setFailure(null);
          if (read !== undefined) onClosed?.(read, asked);
        }}
        onApply={(confirmed, force) => {
          commit.mutate({ on: target, confirmed, force: force === true });
        }}
        // The plan the server offers instead - a drain including the pods
        // nothing owns. A plan of its own, from its
        // first stage, so it is a new round with nothing typed.
        onAlternative={(request) => {
          setOutcome(undefined);
          setNotice(undefined);
          review.mutate({ on: target, asked: request });
        }}
      />
    );

  return {
    start,
    pending: review.isPending,
    busy,
    failure: plan === undefined ? failure : null,
    dialog,
  };
}

/** What `useObjectAct` hands its caller: the verbs' state and their dialog. */
export type ObjectAct = ReturnType<typeof useObjectAct>;
