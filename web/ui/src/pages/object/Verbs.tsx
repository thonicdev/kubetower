import type { ReactNode } from 'react';

import type { ActOutcome, DetailAction } from '../../api';
import { Icon, type IconName } from '../../components/Icon';
import { btnDanger, btnSecondary } from '../../components/buttons';
import type { ObjectIdentity } from './address';
import { useObjectAct, type ObjectAct } from './useObjectAct';

/**
 * The verbs, on one object.
 *
 * ## The list is the server's, and this file could not compute it
 *
 * `DetailPage.actions` arrives already answered permitted or not. The set is a
 * property of the kind and the permission is the read-only guardrail; both are
 * the server's, and neither is guessable from here: answering from the kind
 * alone would put "permitted: true" on a drawer for a cluster the guardrail
 * refuses.
 *
 * **Nothing in this file knows what a pod is.** A kind-to-verb table on this side
 * would be a second copy of one that exists on the server, and the first thing a second
 * copy does is offer Cordon on a Secrets table.
 *
 * ## Disabled, withheld, and the difference between them
 *
 * A verb the server sent with `permitted: false` is **disabled and carries its
 * reason** - a user looking for Delete on a read-only cluster learns why
 * rather than wondering whether they misremembered. A verb the object does not
 * have at all is **absent**, and that decision is also the server's: Restart on a
 * pod nothing will recreate never arrives here.
 *
 * ## The red is the console's one red, and it comes from the flag
 *
 * `action.destructive` is the server's answer, from `specFor`, keyed on (verb,
 * kind) - so restart is destructive on a Pod and not on a Deployment, which no
 * table on this side could say. The style is `destructive.ts`, shared with the
 * plan dialog's commit control and the selection bar's verbs: three treatments
 * for one meaning teaches the hand nothing.
 *
 * ## Preview, then act, in one place
 *
 * Pressing a verb previews and touches nothing; the plan dialog, the stages and
 * the 409 re-read are `useObjectAct`'s, shared with a list row's context menu
 * so a verb chosen from a row is the same act as the same
 * verb pressed here.
 */
export function Verbs({
  identity,
  actions,
  onActed,
  act: owned,
}: {
  identity: ObjectIdentity;
  actions: readonly DetailAction[];
  /** Called once an act has finished, so the panel that owns this row can close
   *  itself where the object is gone. Deciding that here would mean this
   *  component knowing which verbs remove things, which is the table again. */
  onActed?: (outcome: ActOutcome, verb: string) => void;
  /** The act, held by a panel that draws its dialog itself: one whose verbs
   *  row goes when the object does, while the dialog has to stay. Without it
   *  this row holds its own act and draws its dialog in place. */
  act?: ObjectAct;
}): ReactNode {
  const own = useObjectAct(onActed);
  const act = owned ?? own;

  if (actions.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Actions">
      {actions.map((action) => (
        <button
          key={action.verb}
          type="button"
          disabled={action.permitted !== true || act.busy}
          title={
            // A permitted verb carries a reason only when the console could
            // not ask the cluster and offers it anyway (the desktop): the
            // note says the cluster will decide, so it is the title.
            action.permitted === true
              ? (action.reason?.message ?? action.label)
              : (action.reason?.message ?? 'not permitted on this cluster')
          }
          onClick={() => {
            act.start(identity, action);
          }}
          data-destructive={action.destructive === true ? '' : undefined}
          className={action.destructive === true ? btnDanger : btnSecondary}
        >
          {GLYPH[action.verb] === undefined ? null : <Icon name={GLYPH[action.verb] as IconName} />}
          {action.label}
        </button>
      ))}

      {act.failure === null ? null : (
        <span role="alert" className="text-xs text-bad">
          {act.failure}
        </span>
      )}

      {owned === undefined ? act.dialog : null}
    </div>
  );
}

/**
 * The glyph for a verb, from the icon sprite.
 *
 * Keyed on the verb the server sends, and **a verb with no glyph draws its label
 * alone** rather than nothing: a verb added on the server and not here is still
 * reachable, which is the direction to be wrong in. The table is presentation
 * and belongs on this side: which glyph to draw is a
 * presentation decision.
 */
const GLYPH: Record<string, IconName | undefined> = {
  delete: 'trash',
  restart: 'refresh',
  evict: 'evict',
  cordon: 'cordon',
  uncordon: 'uncordon',
  drain: 'drain',
  suspend: 'pause',
  resume: 'play',
  trigger: 'trigger',
  scale: 'scale',
};
