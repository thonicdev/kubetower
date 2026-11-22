import { useEffect, useState, type ReactNode } from 'react';

import type { ActOutcome, ConfirmationPlan } from '../api';
import { Dialog } from './Dialog';
import { btnDanger, btnPrimary, btnSecondary } from './buttons';
import { focusRing, focusRingTight, focusRoom } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { Icon } from './Icon';

/**
 * How long the commit control stays disabled after a fresh plan arrives that
 * describes another act than the plan it replaces - pinned to something else,
 * or naming another set of targets. See "A plan that replaces this one".
 */
export const PIN_CHANGED_HOLD_MS = 1500;

/**
 * What a plan asks the operator to agree to, as one string: the members
 * `sameFacts` in `internal/api/plans.go` compares, and only those. Two plans
 * with the same string are the same act to the server, and a fresh plan with
 * another one is a plan nobody has read yet.
 *
 * **The targets are in it, not only the pin.** A drain's
 * eviction stage carries no pin: it is pinned to its set, and the server refuses
 * it with a 409 when a pod landed on the node or left it. Keyed on the pin
 * alone, that fresh plan held nothing - the node's name stayed typed and a
 * second click evicted a pod that had arrived a moment earlier, unread. The
 * order of the targets is not part of the act, so they are sorted, as the
 * server sorts them.
 */
function agreedTo(facts: ConfirmationPlan['facts']): string {
  const targets = (facts.targets ?? [])
    .map((target) => JSON.stringify([target.kind, target.namespace ?? '', target.name]))
    .sort();
  return JSON.stringify([
    facts.verb,
    facts.count,
    facts.destructive,
    facts.preconditionOn ?? '',
    facts.precondition ?? '',
    facts.preconditionUid ?? '',
    targets,
  ]);
}

/**
 * The typed field's box: the full width of its label capped at 340px, 7px
 * corners (`rounded-lg` is the theme's 7px step - the commonest corner in the
 * console), `6px 9px` inside, in the dialog's own face. A typed count or name
 * is a word rather than code, so not `font-mono`. A short square box would
 * read, with its always-lit ring (`Dialog` focuses the field on open), as an
 * outlined rectangle rather than as a field to type in.
 *
 * The ring is not touched here. `focus.ts` is every control's, and the test
 * beside this file asserts the field carries `focusRingTight` and no other
 * outline utility, so a later hand does not "fix" the look by removing it.
 *
 * `type="number"` (the Scale gesture) is the same element and takes the same
 * box: one rule for both.
 */
const typedField = `w-full max-w-[340px] rounded-lg border border-line bg-bg px-[9px] py-1.5 text-fg ${focusRingTight}`; // style-allow: `.action-field input`'s 340px cap and 9px sides, neither on the 4px scale

/**
 * A `ConfirmationPlan`, drawn, and the gesture it asks for.
 *
 * ## One renderer, because there are four acts and one contract
 *
 * The manifest editor and the data panel each grew their own copy of this, and
 * the bulk bar would have been the third. They are the same object with the same
 * rules, and the rules are the kind that decay quietly in a copy: *the server
 * composes the sentence*, *`facts` is the authority*, *the act is bound to the
 * plan by a token*. A second renderer that forgets one of those is a
 * confirmation that says something the act will not do.
 *
 * ## What it draws, and in what order
 *
 * The server's `sentence` when there is one, and **never one composed here** -
 * two authors for one sentence is worse than either alone. Then the warnings
 * read off this object, then the count the *server* resolved and the list, for
 * a set. Then **Details**, folded: the verb's own explanation, its general
 * warning and the recap. Then the gesture, and the commit control with the pin
 * beside it as a glyph.
 *
 * ## One line first, and the rest one click away
 *
 * A developer glances at a confirmation once and clicks the button, so it has
 * to be precise and concise; a warning that is the same on every pod is read
 * as decoration on the one pod where it is the whole point.
 *
 * So a single verb's plan leads with **one line about this object**, which
 * the server composes from what its preview read and sends as `sentence`
 * (`facts.headline` is the same line, and its presence is how this file knows
 * the sentence is that line): "Deletes pod web-7f9c-x2. ReplicaSet web-7f9c
 * creates a new one." Where nothing would make the object again the plan is
 * `irreversible` and the line says so - "Nothing will recreate it.", "Cannot
 * be undone." - and it is drawn red, because it is the one line on the dialog
 * that is about a loss. A warning is drawn in the open only where the server
 * read it off this object: a run already in flight, a suspended CronJob.
 *
 * What the verb means in general is **folded, not dropped**: `explanation`,
 * the verb's `warning` and the recap sit under a closed `<details>`, the way
 * the console discloses everywhere else (`FoldedMessage`). It is still the
 * server's prose, still one click away, and still there for find-in-page; it
 * is no longer what the eye lands on before the button.
 *
 * `explanation`, `warning` and `button` are the verb's words and they arrive
 * from the same place for the same reason: `internal/web`'s `actionSpec` is
 * keyed on (verb, kind) and knows that restarting a Pod deletes it, so a
 * restart on a Deployment, a restart on a Pod and a delete on a Pod are three
 * different windows.
 *
 * **Nothing here humanizes that verb, and nothing here may.** `facts.verb` is
 * the wire slug, and a client that title-cased it would be the second author of
 * the confirmation's wording that the contract's property 1 exists to prevent -
 * `internal/api/plans.go` has the argument. The answer to a slug on screen is
 * always the server sending a word, never this file making one.
 *
 * **Every plan family sends its button word**, each composed beside the facts
 * it resolved: a bulk plan's "Delete 4 pods" from `spec.Label`, the resolved
 * count and `api.KindWord`, and the same for a data write, a manifest apply, a
 * node shell, a state discard, emptying the archive and a drain's second stage.
 * `internal/web`'s TestNoPlanFamilyLeavesTheClientDrawingTheVerbSlug holds
 * that, one layer down where the words are decided.
 * prevent.
 *
 * **`facts.count` and not `targets.length`.** The contract is explicit: the
 * array may be shorter than the count where the list was too long to send, so
 * "12 shown of 47" is the truth and "12" is not.
 *
 * ## The gesture
 *
 * `facts.confirmation.kind` decides it and this component never chooses:
 *
 * - `acknowledge` — the button is the whole gesture.
 * - `count` — the number of targets, typed back. A deletion of eleven pods is
 *   confirmed by *eleven*, which is the figure that is wrong when the set has
 *   changed under the operator.
 * - `name` — the object's name, typed out, asked for on a Delete and a Drain
 *   and on nothing else.
 * - `number` — a value the act *needs* rather than a proof of intent: a replica
 *   count. Bounded by `minimum` and `maximum` where the server sent them.
 *
 * `expected` is read off what is on screen, so the check fails when the screen
 * is not describing what the operator believes it is. The server checks it
 * again: this is the courtesy, that is the control.
 *
 * ## One resource, deleted
 *
 * A single delete's plan asks with `acknowledge`, its sentence is "Are you
 * sure?", its button "Delete", and it carries no detail - all the server's, so
 * nothing here looks at the verb. What the plan adds is `forceable`, and that
 * draws a "Force delete" box: ticked, the act is sent with `force` and the
 * object is deleted with a grace period of zero.
 *
 * ## The one control that must not be pressed by reflex
 *
 * The commit control uses the console's one destructive style, filled, never
 * an outline, and `facts.destructive` decides it: **the dialog never looks at
 * the verb**, which is the same rule the selection bar follows and for the
 * same reason.
 *
 * ## Where it sits in the keyboard stack
 *
 * underneath it.* Named at both returns rather than left to the prop's
 * default, `KeyLayer.Dialog` (the manifest editor's own layer): with one
 * dialog on screen the tie-break ("whatever opened last") would order the
 * stack the same way, but not once a plan is confirmed from inside another
 * dialog.
 *
 * ## A plan that follows an act
 *
 * An outcome may carry `nextPlan` - a drain's evictions after its cordon - and
 * the owner then hands this component that plan with the first stage's
 * `summary` as `notice`. It is drawn above the sentence, in the outcome's
 * color, so the operator reads what has already happened before reading what
 * is being asked. What is typed does not carry across: the owner keys this
 * component on the stage, so a name typed to cordon is typed again to evict.
 *
 * ## A plan that replaces this one
 *
 * A 409 carries the plan as it stands now, and the owner hands it to this same
 * component without a new key. **What was typed is kept**:
 * the operator reads the refusal and the fresh plan, and confirms again without
 * typing the node's name a second time. It is kept only while the gesture asks
 * for the same thing - a fresh plan asking for a different name or a different
 * kind of gesture starts empty, because a value typed for one question is not
 * an answer to another.
 *
 * **And the commit control is held for a moment when the pin moved.** Keeping
 * the typed name means the button is live the instant the fresh plan renders,
 * so a second click - a double click, or a reflex - would confirm a plan nobody
 * had read: a cordon against a node that had just gained a `NoExecute` taint.
 * When the fresh plan is pinned to another value or another object than the
 * one it replaces, or names another set of targets - a drain's eviction stage,
 * which has no pin, refused because a pod landed on the node - the control is
 * disabled for `PIN_CHANGED_HOLD_MS`, long enough to break a reflex and short
 * enough not to read as a fault. A fresh plan that agrees to the same act
 * holds nothing; `agreedTo` is what "the same" means, and it is the server's
 * `sameFacts`.
 *
 * ## A plan that offers another
 *
 * `facts.alternative` is a plan the server offers in place of this one - a
 * drain's eviction stage that leaves pods nothing owns on the node offers the
 * same drain including them. It is drawn as a control with
 * the server's label, and pressing it hands the server's request to the owner
 * to preview; nothing about it is composed here. An owner that passes no
 * `onAlternative` draws no control, rather than one that does nothing.
 *
 * An outcome can carry the same offer (`outcome.alternative`), where the act
 * ended with no next stage to carry it: a drain's cordon over a node holding
 * no pod a drain evicts by default but pods nothing owns. It is drawn beside
 * Close, the same way.
 */
export function PlanDialog({
  title,
  plan,
  pending,
  error,
  outcome,
  notice,
  onCancel,
  onApply,
  onAlternative,
  children,
}: {
  title: string;
  plan: ConfirmationPlan;
  pending: boolean;
  error: string | null;
  /** Set once the act has run. The dialog then reports rather than asks. */
  outcome?: ActOutcome | undefined;
  /** What the stage before this plan did, where there was one. */
  notice?: string | undefined;
  onCancel: () => void;
  /** `force` is the "Force delete" box, ticked; only ever true on a plan that
   *  says `forceable`. */
  onApply: (confirmed?: string, force?: boolean) => void;
  /** Preview the plan `facts.alternative` offers. */
  onAlternative?: (request: NonNullable<ConfirmationPlan['facts']['alternative']>['request']) => void;
  /** What the owner draws of the plan's own subject - an install's chart and
   *  the document it sends - after the targets and before the gesture. Values,
   *  never a sentence about the act: that is the server's. */
  children?: ReactNode;
}): ReactNode {
  const facts = plan.facts;
  const gesture = facts.confirmation;
  // What was typed, and for which question. A fresh plan from a 409 keeps it
  // when it asks the same thing; see "A plan that replaces this one".
  const question = `${gesture?.kind ?? ''}|${gesture?.expected ?? ''}`;
  const [answer, setAnswer] = useState({ question, value: '' });
  const typed = answer.question === question ? answer.value : '';
  const setTyped = (value: string): void => setAnswer({ question, value });
  // The "Force delete" box. Drawn only where the plan
  // says `forceable`, and sent only then, so a plan that stops saying it - a
  // fresh one from a 409 - cannot carry a tick made against another.
  const [forceTicked, setForceTicked] = useState(false);
  const force = facts.forceable === true && forceTicked;

  // The act this dialog was last drawn for - its pin and its targets - and
  // whether the commit control is held because it just changed; see "A plan
  // that replaces this one". The comparison is made while rendering, React's
  // pattern for state derived from a prop that changed, so the first render of
  // the fresh plan is already held.
  const pin = agreedTo(facts);
  const [drawnPin, setDrawnPin] = useState(pin);
  const [held, setHeld] = useState(false);
  if (pin !== drawnPin) {
    setDrawnPin(pin);
    setHeld(true);
  }
  useEffect(() => {
    if (!held) return undefined;
    const timer = setTimeout(() => setHeld(false), PIN_CHANGED_HOLD_MS);
    return () => clearTimeout(timer);
  }, [held, drawnPin]);

  const expected = gesture?.expected ?? '';
  // Whether the sentence is the server's one line about this object; see "One
  // line first". Read off the member's presence, never off the words.
  const headline = facts.headline !== undefined && facts.headline !== '';
  const satisfied =
    gesture === undefined ||
    gesture.kind === 'acknowledge' ||
    (gesture.kind === 'number' ? typed.trim() !== '' : typed.trim() === expected.trim());

  if (outcome !== undefined) {
    return (
      // Keyed apart from the plan's dialog: the control that held the keyboard
      // - the commit, pressed - is gone once the act has run, so the outcome
      // is a dialog of its own that takes the focus on its Close.
      <Dialog key="outcome" title={title} layer={KeyLayer.Confirm} onClose={onCancel}>
        <Outcome outcome={outcome} onClose={onCancel} onAlternative={onAlternative} />
      </Dialog>
    );
  }

  return (
    <Dialog
      key="plan"
      title={title}
      layer={KeyLayer.Confirm}
      onClose={onCancel}
      tone={facts.destructive === true ? 'danger' : 'default'}
    >
      <div className={`flex max-h-[70vh] min-w-0 flex-col gap-3 overflow-auto text-xs ${focusRoom}`}>
        {notice === undefined || notice === '' ? null : (
          <p role="status" className="text-ok">
            {notice}
          </p>
        )}

        {/* The server's sentence: on a single verb's plan, the one line about
            this object, drawn red where the plan says nothing would make the
            object again - the line then says so in words, and the color is
            the reason a glance stops on it. A plan with no headline keeps its
            sentence in the ordinary color; its loss, if any, is the line
            below. */}
        {plan.sentence === undefined || plan.sentence === null ? null : (
          <p data-plan-line="" className={headline && facts.irreversible === true ? 'text-bad' : 'text-fg'}>
            {plan.sentence}
          </p>
        )}

        {/* The warnings read off **this object**, in the open. The verb's own
            warning - the same on every object - is folded under Details. */}
        {(facts.warnings ?? []).map((warning) => (
          <p key={warning} role="alert" className="text-warn">
            {warning}
          </p>
        ))}

        {/* Only on a plan whose line does not already say it. A single verb's
            headline says "Nothing will recreate it." or "Cannot be undone."
            itself, where it is true of the object, and a second line under it
            saying the same thing in red is the dialog arguing with itself. A
            plan with no headline - emptying the archive, discarding state -
            has no other place it is said. */}
        {facts.irreversible === true && !headline ? (
          <p className="text-bad">This cannot be undone.</p>
        ) : null}

        {/* The count, and the list, **only for a set**.

            One object is already named - in the title, and in the server's
            sentence above with its kind and its namespace. "1 object" over a
            one-item list repeating that name is three statements of one fact.

            The truncation note stays, because a list shorter than the count is
            the one case where the list is not the whole answer.

            **And a single target is still listed when it carries a `note`**,
            which is not a refinement but the rule itself: the list is redundant
            only where it repeats. Emptying the archive previews one target whose
            note is "4.0 MiB across 6 days" - the whole substance of that
            confirmation; `Settings.test.tsx` holds it. */}
        {facts.count <= 1 ? null : (
          <p className="text-muted">
            {facts.count} objects
            {(facts.targets ?? []).length > 0 && (facts.targets ?? []).length < facts.count
              ? ` — ${(facts.targets ?? []).length} of them listed below`
              : ''}
          </p>
        )}

        {(facts.targets ?? []).length === 0 ||
        (facts.count <= 1 &&
          !(facts.targets ?? []).some(
            (target) => target.note !== undefined && target.note !== '',
          )) ? null : (
          <ul className="max-h-40 overflow-auto rounded border border-line">
            {(facts.targets ?? []).map((target) => (
              <li
                key={`${target.namespace ?? ''}/${target.name}`}
                className="border-b border-line px-2 py-0.5 font-mono last:border-b-0"
              >
                {target.namespace === undefined || target.namespace === ''
                  ? target.name
                  : `${target.namespace}/${target.name}`}
                {target.note === undefined ? null : (
                  <span className={target.refused === true ? 'text-bad' : 'text-muted'}> · {target.note}</span>
                )}
              </li>
            ))}
          </ul>
        )}

        {children}

        <Details facts={facts} />

        {gesture === undefined || gesture.kind === 'acknowledge' ? null : (
          <label className="flex flex-col gap-1">
            <span className="text-muted">
              {gesture.label ??
                (gesture.kind === 'count'
                  ? // "Type 1 to confirm" would read as "type the digit one".
                    // Said as what the number is, in the words the
                    // server's own refusal uses.
                    `Type ${expected} — the number of objects this acts on — to confirm`
                  : gesture.kind === 'name'
                    ? `Type ${expected} to confirm`
                    : 'Value')}
            </span>
            <input
              data-dialog-focus
              type={gesture.kind === 'number' ? 'number' : 'text'}
              value={typed}
              onChange={(event) => setTyped(event.target.value)}
              {...(gesture.kind === 'number' && gesture.minimum !== undefined
                ? { min: gesture.minimum }
                : {})}
              {...(gesture.kind === 'number' && gesture.maximum !== undefined
                ? { max: gesture.maximum }
                : {})}
              className={typedField}
            />
          </label>
        )}

        {facts.forceable === true ? (
          <label className="flex w-fit items-center gap-2 text-muted">
            <input
              type="checkbox"
              data-force=""
              checked={forceTicked}
              onChange={(event) => setForceTicked(event.target.checked)}
              className={focusRingTight}
            />
            Force delete
          </label>
        ) : null}

        {error === null ? null : (
          <p role="alert" className="text-bad">
            {error}
          </p>
        )}

        <div className="flex gap-2">
          <button
            type="button"
            // The control that commits, named so a test can find it among the
            // dialog's own chrome: `Dialog` draws a Close in its header, so
            // "the button that is not Cancel" is two buttons.
            data-commit=""
            disabled={pending || held || !satisfied}
            onClick={() =>
              onApply(gesture === undefined || gesture.kind === 'acknowledge' ? undefined : typed, force)
            }
            title={
              pending
                ? undefined
                : held
                  ? 'The plan just changed: read it before confirming'
                  : satisfied
                    ? undefined
                    : gesture?.kind === 'number'
                      ? 'Enter a value first'
                      : `Type ${expected} first`
            }
            className={facts.destructive === true ? btnDanger : btnPrimary}
          >
            {/* The server's word. Never a word composed here, and **never
                `facts.verb`**, which is the wire slug - see the header. */}
            {pending ? 'Working…' : (facts.button ?? 'Apply')}
          </button>
          <button
            type="button"
            // Where the keyboard lands when there is nothing to type: the
            // dialog's first control
            // otherwise, which on a delete is the "Force delete" box - a
            // reflex Space would tick it. Cancel is the way out, and pressing
            // it by reflex does nothing.
            {...(gesture === undefined || gesture.kind === 'acknowledge' ? { 'data-dialog-focus': '' } : {})}
            onClick={onCancel}
            className={btnSecondary}
          >
            Cancel
          </button>
          <Pin facts={facts} />
        </div>

        {facts.alternative === undefined || onAlternative === undefined ? null : (
          <div>
            <button
              type="button"
              data-alternative=""
              disabled={pending}
              onClick={() => {
                if (facts.alternative !== undefined) onAlternative(facts.alternative.request);
              }}
              className={btnSecondary}
            >
              {facts.alternative.label}
            </button>
          </div>
        )}
      </div>
    </Dialog>
  );
}

/**
 * What happened, per target - **every target**.
 *
 * **Partial success is the normal case**, which is why this is a list and not a
 * sentence: a bulk act that half worked has to say which half. The server's
 * `summary` is drawn where there is one, for the same reason its `sentence` is,
 * and it stays above the rows.
 *
 * ## Why the successes are drawn too
 *
 * The contract is explicit that `results` is *"per target, always"*, because
 * *"an outcome that reports one verdict for twelve objects is the
 * empty-versus-failed collapse wearing a different hat"*. After a bulk delete of
 * twelve pods where two were refused, "10 done, 2 refused" naming only the two
 * would leave **which ten were deleted** off screen, on the surface where the
 * mistake that actually happens is having ticked rows in a namespace you did
 * not mean to be in.
 * namespace you did not mean to be in.
 *
 * ## Failures first, and the code as data
 *
 * Refusals are what the operator acts on next, so they lead; within each group
 * the server's order is kept, because it is the order the act ran in. Each row
 * carries its namespace and name, its verdict in a word, and the message.
 *
 * `data-refusal` is the refusal's **code**, so
 * refusals can be counted by cause without reading English,
 * which is what a support conversation about a partial bulk act needs. Absent on
 * a row that worked rather than empty - a code of `""` is a refusal with no
 * reason, and this is not a refusal at all.
 *
 * ## The verdict word comes from the code, and only a refusal is red
 *
 * A failed row's word is not always "refused": a drain's eviction the API
 * server had not answered may still be happening, and one that was never sent
 * is neither. Those are different next steps: a
 * refusal is the cluster saying no, "no answer" means look at the pod before
 * doing anything, and "not sent" means it is safe to run again. So the word is
 * read from `reason.code` (verdictOf), never from the message, and the two that
 * are not a refusal are drawn neutral.
 */
function Outcome({
  outcome,
  onClose,
  onAlternative,
}: {
  outcome: ActOutcome;
  onClose: () => void;
  onAlternative?:
    | ((request: NonNullable<ConfirmationPlan['facts']['alternative']>['request']) => void)
    | undefined;
}): ReactNode {
  const results = outcome.results ?? [];
  const failures = results.filter((one) => one.ok !== true);
  const done = results.filter((one) => one.ok === true);
  const rows = [...failures, ...done];

  return (
    <div className={`flex max-h-[70vh] min-w-0 flex-col gap-2 overflow-auto text-xs ${focusRoom}`}>
      <p className={outcome.failed > 0 ? 'text-warn' : 'text-ok'}>
        {outcome.summary ?? `${outcome.succeeded} done, ${outcome.failed} refused.`}
      </p>

      {rows.length === 0 ? null : (
        <ul className="max-h-60 overflow-auto rounded border border-line">
          {rows.map((one) => (
            <li
              key={`${one.target.kind}/${one.target.namespace ?? ''}/${one.target.name}`}
              data-refusal={one.ok === true ? undefined : (one.reason?.code ?? '')}
              className="flex flex-wrap items-baseline gap-x-2 border-b border-line px-2 py-0.5 last:border-b-0"
            >
              <span className="font-mono">
                {one.target.namespace === undefined || one.target.namespace === ''
                  ? one.target.name
                  : `${one.target.namespace}/${one.target.name}`}
              </span>
              <span className={verdictOf(one).tone} data-verdict={verdictOf(one).word}>
                {verdictOf(one).word}
              </span>
              {one.message === undefined && one.reason?.message === undefined ? null : (
                <span className="min-w-0 text-muted">
                  {one.message ?? one.reason?.message}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}

      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          data-dialog-focus
          onClick={onClose}
          className={btnSecondary}
        >
          Close
        </button>
        {/* The plan the server offers now that this act is done, where there
            is no next stage to carry it - a drain's cordon over a node holding
            only pods nothing owns. The server's label and
            request, as on a plan; nothing is composed here. */}
        {outcome.alternative === undefined || onAlternative === undefined ? null : (
          <button
            type="button"
            data-alternative=""
            onClick={() => {
              if (outcome.alternative !== undefined) onAlternative(outcome.alternative.request);
            }}
            className={btnSecondary}
          >
            {outcome.alternative.label}
          </button>
        )}
      </div>
    </div>
  );
}

type ActResult = NonNullable<ActOutcome['results']>[number];

/**
 * One row's verdict in a word, and its color, from the row's code.
 *
 * `timeout` is a request sent and not answered in time; `not_probed_yet` one
 * that never left the console (`api/openapi.yaml`, ReasonCode). Anything else
 * that failed - `verb_denied`, or a code this client does not know yet - is a
 * refusal: the word an unknown code falls back to is the one the dialog has
 * always said, so a new code is never drawn as better news than it is.
 */
function verdictOf(one: ActResult): { word: string; tone: string } {
  if (one.ok === true) return { word: 'done', tone: 'text-ok' };
  switch (one.reason?.code) {
    case 'timeout':
      return { word: 'no answer', tone: 'text-muted' };
    case 'not_probed_yet':
      return { word: 'not sent', tone: 'text-muted' };
    default:
      return { word: 'refused', tone: 'text-bad' };
  }
}

/**
 * What the verb means in general, folded: its explanation, its warning, and
 * the recap.
 *
 * All three are the server's prose - nothing here is composed. They are
 * folded rather than dropped because each still answers a question
 * somebody will ask: what `kubectl` would have done, why the Job a Run now
 * creates is not owned by its CronJob, which field a cordon writes and what it
 * holds now. The order of reading is the line about this object first, the
 * button, and this for whoever wants to know why.
 *
 * `<details>`, closed, as the console discloses everywhere (`FoldedMessage`,
 * the annotations on the details page): the keyboard, the expanded state a
 * screen reader announces, and find-in-page all come with the element. The
 * verb's warning loses `role="alert"` in here: an alert is announced when it
 * appears, and this one is the same on every object and is not news.
 *
 * Nothing is drawn - not even the summary - for a plan that carries none of
 * the three, so a disclosure never opens on nothing.
 */
function Details({ facts }: { facts: ConfirmationPlan['facts'] }): ReactNode {
  const explanation = facts.explanation ?? '';
  const warning = facts.warning ?? '';
  const recap = facts.recap;
  const hasRecap = recap !== undefined && (recap.field !== undefined || recap.note !== undefined);
  if (explanation === '' && warning === '' && !hasRecap) return null;

  return (
    <details data-plan-details="" className="group">
      {/* `flex` takes the disclosure triangle away in Chromium and Firefox,
          and the WebKit marker is hidden by name; the chevron is the console's
          own, turned when open, as the menu's groups draw theirs. */}
      <summary
        className={`flex w-fit cursor-pointer items-center gap-1 rounded-sm text-muted hover:text-fg [&::-webkit-details-marker]:hidden ${focusRing}`}
      >
        <Icon name="chevron" className="transition-transform group-open:rotate-90" />
        Details
      </summary>
      <div className="mt-2 flex flex-col gap-2">
        {explanation === '' ? null : <p className="text-muted">{explanation}</p>}
        {warning === '' ? null : <p className="text-warn">{warning}</p>}
        {recap === undefined ? null : <Recap recap={recap} />}
      </div>
    </details>
  );
}

/**
 * The pin, as a glyph beside the controls.
 *
 * Not a line of words and not the number: a value nobody can act on is the
 * plumbing of this console in the middle of a delete, and a line that is the
 * same on every confirmation is one more line read past on the way to the
 * button. What the operator needs on screen is *that* the act is pinned, and a
 * padlock says that; *what* is pinned, the value and why it matters are on
 * hover, and in `aria-label` for a reader that has no hover - the same words.
 *
 * **What is pinned is the server's word**:
 * `preconditionOn`, which is `spec.unschedulable and spec.taints` on a node and
 * `metadata.generation` on a workload, so that a status write by a kubelet or a
 * controller does not refuse the act. Absent means the resourceVersion, which
 * is what every other plan carries.
 */
function Pin({ facts }: { facts: ConfirmationPlan['facts'] }): ReactNode {
  if (facts.precondition === undefined || facts.precondition === '') return null;
  const said = `Pinned to ${facts.preconditionOn ?? 'resourceVersion'} ${facts.precondition}. An act that somebody else got to first is refused rather than overwritten.`;
  return (
    <span data-pin="" role="img" aria-label={said} title={said} className="ml-auto self-center text-muted">
      <Icon name="lock" />
    </span>
  );
}

/**
 * The change as fields.
 *
 * `absent` and `unchanged` are the two the contract spends its words on, and
 * both are about not promising something false: a field that is not written and
 * the same field written `false` are indistinguishable to an API server and
 * different things to read before acting on a production node; and uncordoning a
 * node that is already schedulable is a no-op, so a confirmation promising a
 * change would be wrong about it - *worse than saying nothing, because it is the
 * sentence the reader will believe*.
 */
function Recap({ recap }: { recap: NonNullable<ConfirmationPlan['facts']['recap']> }): ReactNode {
  // A recap carrying only a note is a remark, not a diff, so it is drawn as one,
  // outside the bordered box: a delete's note - "there is no before and after
  // to show" - inside a frame would draw an empty-looking frame around a
  // sentence saying there was nothing to frame.
  if (recap.field === undefined) {
    return recap.note === undefined ? null : <p className="text-muted">{recap.note}</p>;
  }

  return (
    <div className="rounded border border-line px-2 py-1">
      <p>
        <span className="font-mono text-fg">{recap.field}</span>
        <span className="text-muted">
          {' '}
          {recap.absent === true ? 'is not set' : `is ${String(recap.before ?? '—')}`} →{' '}
        </span>
        <span className="font-mono text-fg">{String(recap.after ?? '—')}</span>
      </p>
      {recap.unchanged === true ? (
        <p className="text-muted">It is already at that value, so this changes nothing.</p>
      ) : null}
      {recap.note === undefined ? null : <p className="text-muted">{recap.note}</p>}
    </div>
  );
}
