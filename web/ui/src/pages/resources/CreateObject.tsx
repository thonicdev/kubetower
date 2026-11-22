import { useId, useState, type ReactNode } from 'react';

import { useCreateTemplate, type CreateAffordance } from '../../api';
import { btnPrimary, btnSecondary } from '../../components/buttons';
import { Dialog } from '../../components/Dialog';
import { focusRing, focusRingTight } from '../../focus';
import { KeyLayer } from '../../keyboard/layers';
import { ManifestEditor } from '../object/ManifestEditor';

/**
 * The list's "+": a new object of the kind in view, from a template.
 *
 * ## Drawn from the server's verdict, never guessed
 *
 * `ListPage.create` is reviewed for the person, in the one namespace a new
 * object would go to. Absent - a kind the console reads and does not write, or
 * one nobody writes by hand (`neverCreated`, on the server) - is no button at
 * all. `permitted: false` is the button drawn grayed with the
 * reason: a missing button reads as a console without the feature, a grayed
 * one says why not. The server reviews again on `previewCreate`, so this is a
 * courtesy and not the gate.
 *
 * One refusal is not drawn grayed: a namespaced list shown across several
 * namespaces, or all of them, where the server says to choose one. The press
 * asks for that namespace (below), so the press itself answers the reason, and
 * the write is still reviewed for the namespace chosen.
 *
 * **The reason is the button's accessible description, and is shown on focus,
 * hover and a tap** - not a `title`, which a touch screen never shows and a
 * screen reader may not read. So the button is `aria-disabled` rather than
 * `disabled`: a disabled button takes neither the focus nor a tap, and the
 * reason would have no way to be asked for.
 *
 * ## Its name is the kind as the cluster spells it
 *
 * `create.kind` is discovery's Kind - "Create a ConfigMap", "Create an
 * Ingress" - and not a singular guessed from the list's heading, which reads
 * "Config maps".
 * `singularOf` is the fallback for a server that did not say.
 *
 * ## Where it floats
 *
 * Over the list, at its bottom-right corner, in no bar: the caller places it
 * outside the list's scroller, so scrolling never moves it, and inside the
 * list's column, so an open drawer sits beside it and the log dock below it.
 * Translucent, so a row that passes under it still reads. The list keeps room
 * after its last row, so every row can be scrolled clear of it.
 *
 * That corner is also the selection bar's (`SelectionBar.tsx`). **The caller
 * draws this only while no row is ticked**, so the two never share it: the bar
 * wraps upward when its verbs do not fit, so no fixed offset clears it at every
 * width, and while rows are ticked the person is acting on what exists - the
 * bar's Clear brings the "+" back.
 *
 * 40px, in the text color; 44px under a coarse pointer, where a finger is the
 * pointer.
 *
 * ## The namespace first, then the editor
 *
 * On a namespaced kind the press first asks which namespace the object goes
 * to, with the list's own namespace already chosen, so one more press opens
 * the editor in the usual case. A cluster-scoped kind has nothing to ask and
 * goes straight to the editor.
 *
 * The editor is the manifest editor: the template for the kind and the chosen
 * namespace is read, then `ManifestEditor` opens with `create` set - the same
 * textarea, keys and review, with `previewCreate` and `applyCreate` behind it.
 * The template is read on the press and not with the list, because a list is
 * opened far more often than anything is created from it.
 */
export function CreateObject({
  cluster,
  kind,
  label,
  create,
  namespaced,
  available = [],
  current = [],
  className = 'relative',
}: {
  cluster: string;
  /** The list's slug, `deployments`: what the template is asked for. */
  kind: string;
  /** The list's heading, `Deployments`, for the button's name. */
  label: string;
  create: CreateAffordance;
  /** Whether the kind lives in namespaces: only then is one asked for. */
  namespaced: boolean;
  /** The cluster's namespaces, to choose from. */
  available?: readonly string[];
  /** The namespaces the list is narrowed to, empty for all of them. */
  current?: readonly string[];
  /** Where the corner sits; positioned (`relative` or `absolute`), because the
   *  refusal's reason is drawn from it. */
  className?: string;
}): ReactNode {
  // A counter rather than a flag: each press is a fresh step and a fresh
  // editor, never the text a previous one was abandoned with.
  const [opened, setOpened] = useState(0);
  const [step, setStep] = useState<Step>({ at: 'closed' });
  // Whether the refusal's reason is on screen: while the button has the focus
  // or the pointer, and after a tap until the focus leaves.
  const [told, setTold] = useState(false);
  const reasonId = useId();
  const one = create.kind ?? singularOf(label);
  const name = `Create ${/^[aeiou]/i.test(one) ? 'an' : 'a'} ${one}`;
  // "Choose one namespace to create in" is what the namespace step answers.
  const answeredByStep =
    namespaced && create.namespace === undefined && create.reason?.code === 'not_supported_here';
  const refused =
    create.permitted || answeredByStep ? undefined : (create.reason?.message ?? 'not permitted here');

  return (
    <>
      {/* Positioned either way: the reason is drawn above the button. */}
      <div data-create-corner="" className={className}>
        {refused === undefined ? null : (
          <p
            id={reasonId}
            data-create-reason=""
            className={
              told
                ? 'absolute right-0 bottom-full mb-2 w-max max-w-64 rounded-md border border-line bg-panel-2 px-2 py-1 text-xs text-muted shadow-lg'
                : 'sr-only'
            }
          >
            {refused}
          </p>
        )}
        <button
          type="button"
          data-control="create-object"
          aria-label={name}
          aria-disabled={refused === undefined ? undefined : true}
          aria-describedby={refused === undefined ? undefined : reasonId}
          title={refused === undefined ? name : undefined}
          onFocus={() => setTold(refused !== undefined)}
          onBlur={() => setTold(false)}
          onMouseEnter={() => setTold(refused !== undefined)}
          onMouseLeave={() => setTold(false)}
          onClick={() => {
            // Refused, a press shows the reason - a tap is how a touch screen
            // asks - and opens nothing.
            if (refused !== undefined) {
              setTold(true);
              return;
            }
            setOpened((n) => n + 1);
            setStep(namespaced ? { at: 'namespace' } : { at: 'editor' });
          }}
          className={`inline-flex size-10 items-center justify-center rounded-full border border-line bg-panel-2/70 text-fg shadow-lg backdrop-blur-sm hover:bg-panel-2 pointer-coarse:size-11 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 aria-disabled:hover:bg-panel-2/70 ${focusRingTight}`}
        >
          <span aria-hidden="true" className="text-lg leading-none">
            +
          </span>
        </button>
      </div>
      {/* Outside the corner's box: the dialog is fixed over the page, and a
          positioned parent with a z-index would hold it under the panels. */}
      {step.at === 'namespace' ? (
        <NamespaceStep
          key={opened}
          title={name}
          one={one}
          available={available}
          preselected={preselect(create.namespace, current, available)}
          onChoose={(namespace) => {
            setStep({ at: 'editor', namespace });
          }}
          onClose={() => {
            setStep({ at: 'closed' });
          }}
        />
      ) : null}
      {step.at === 'editor' ? (
        <CreateDialog
          key={opened}
          cluster={cluster}
          kind={kind}
          namespace={step.namespace}
          fallbackTitle={`New ${one}`}
          onClose={() => {
            setStep({ at: 'closed' });
          }}
        />
      ) : null}
    </>
  );
}

/** Nothing open, the namespace asked for, or the editor for a namespace (none
 *  for a cluster-scoped kind). */
type Step = { at: 'closed' } | { at: 'namespace' } | { at: 'editor'; namespace?: string };

/**
 * The namespace already chosen when the step opens: the one the server reviewed
 * the button for, else the first the list shows, else `default` where the
 * cluster has it, else the cluster's first.
 */
export function preselect(
  reviewed: string | undefined,
  current: readonly string[],
  available: readonly string[],
): string {
  if (reviewed !== undefined && reviewed !== '') return reviewed;
  const shown = current[0];
  if (shown !== undefined) return shown;
  if (available.includes('default')) return 'default';
  return available[0] ?? 'default';
}

/**
 * Which namespace the new object goes to: a choice among the cluster's
 * namespaces where they could be read, and a field to type one in where they
 * could not - a list of namespaces that failed to arrive is no reason to create
 * nothing.
 */
function NamespaceStep({
  title,
  one,
  available,
  preselected,
  onChoose,
  onClose,
}: {
  title: string;
  one: string;
  available: readonly string[];
  preselected: string;
  onChoose: (namespace: string) => void;
  onClose: () => void;
}): ReactNode {
  const [chosen, setChosen] = useState(preselected);
  const id = useId();
  // The preselected one is offered even where the cluster's list lacks it:
  // the list's own namespace is the person's, whatever that read said.
  const offered = available.includes(preselected) ? available : [preselected, ...available];
  const field = `w-full rounded-md border border-line bg-panel-2 px-2 py-1 text-sm text-fg ${focusRing}`;
  const typed = chosen.trim();

  return (
    <Dialog title={title} onClose={onClose} layer={KeyLayer.Dialog}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (typed !== '') onChoose(typed);
        }}
      >
        <label htmlFor={id} className="flex flex-col gap-1 text-xs text-muted">
          {`Namespace for the new ${one}`}
          {available.length > 0 ? (
            <select
              id={id}
              value={chosen}
              className={field}
              onChange={(event) => {
                setChosen(event.target.value);
              }}
            >
              {offered.map((namespace) => (
                <option key={namespace} value={namespace}>
                  {namespace}
                </option>
              ))}
            </select>
          ) : (
            <input
              id={id}
              value={chosen}
              spellCheck={false}
              autoComplete="off"
              className={`${field} font-mono`}
              onChange={(event) => {
                setChosen(event.target.value);
              }}
            />
          )}
        </label>
        <div className="flex justify-end gap-2">
          <button type="button" className={btnSecondary} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className={btnPrimary} disabled={typed === ''}>
            Continue
          </button>
        </div>
      </form>
    </Dialog>
  );
}

/**
 * The template's read, then the editor. Until the template is in, and when it
 * cannot be had, a dialog of the same title says which - a press that opened
 * nothing would read as a button that does not work.
 */
function CreateDialog({
  cluster,
  kind,
  namespace,
  fallbackTitle,
  onClose,
}: {
  cluster: string;
  kind: string;
  namespace: string | undefined;
  fallbackTitle: string;
  onClose: () => void;
}): ReactNode {
  const template = useCreateTemplate(cluster, kind, namespace, true);
  const answer = template.data;

  if (answer?.state === 'ok') {
    return (
      <ManifestEditor
        cluster={cluster}
        kind={kind}
        namespace={namespace}
        document={answer.data.document}
        create={{
          kind: answer.data.kind,
          ...(answer.data.generic === undefined ? {} : { generic: answer.data.generic }),
        }}
        onDone={onClose}
        onCancel={onClose}
      />
    );
  }

  const problem = template.isError
    ? template.error.message
    : answer === undefined
      ? undefined
      : answer.reason.message;
  return (
    <Dialog title={fallbackTitle} onClose={onClose}>
      {problem === undefined ? (
        <p role="status" className="text-xs text-muted">
          Reading the template…
        </p>
      ) : (
        <p role="alert" className="text-xs text-bad">
          {problem}
        </p>
      )}
    </Dialog>
  );
}

/**
 * `Deployments` -> `Deployment`, for the button's name only: the editor's title
 * is the server's own spelling of the kind, once the template has said it.
 * English plurals as Kubernetes kinds use them, and a label that matches none
 * is used as it is.
 */
export function singularOf(label: string): string {
  if (/[^aeiou]ies$/i.test(label)) return `${label.slice(0, -3)}y`;
  if (/(ss|sh|ch|x)es$/i.test(label)) return label.slice(0, -2);
  if (/[^s]s$/i.test(label)) return label.slice(0, -1);
  return label;
}
