import { useLayoutEffect, useRef, useState, type ReactNode } from 'react';

import type { DetailAction } from '../../api';
import { useCapabilities, useResourceDetail } from '../../api';
import type { RowMenuAnchor } from '../../components/DataGrid';
import { MenuPanel, OverflowMenuItem, OverflowMenuSeparator } from '../../components/OverflowMenu';
import { useDockIfPresent } from '../../dock/DockContext';
import { useShellIfPresent } from '../../shell/ShellContext';
import type { ObjectIdentity } from '../object/address';
import { execChoices, isPodKind, shellOn } from '../object/Terminals';

/**
 * A list row's context menu.
 *
 * Its layout:
 *
 * 1. a header line naming the object, `namespace/name`;
 * 2. **the safe entries first**: Open (the drawer), Logs, Shell, Copy name;
 * 3. a separator;
 * 4. **the drawer's verbs**, every one still through the plan dialog, and
 *    only an irreversible one drawn red (`DetailAction.irreversible`): the
 *    drawer's own chips keep coloring by `destructive`.
 *
 * ## Only what the drawer offers, for this object and this person
 *
 * Logs and Shell are offered exactly where the drawer's own controls are:
 * Logs on a pod with a namespace (`LogsButton`), Shell on a pod where this
 * console may exec - `capabilities.podExec`, and under `shared` the pod's own
 * answer (`shellOn`, shared with `Terminals`) - in the containers the drawer's
 * picker offers and refused with its sentence where none is running or the
 * pod's answer refuses it (`execChoices`, shared with `Terminals`). Neither
 * exists without a dock to open a tab in. A node's shell is not here: it is a
 * confirmation of its own (the node-shell plan) and stays the drawer's.
 *
 * The verbs are `DetailPage.actions`, from the object's own read - the query the
 * drawer itself makes, so opening the drawer afterwards reads nothing again -
 * and never a second list kept in the client. Not the list's `bulk` verbs: those
 * are per kind, and the drawer's are per object and per person (Restart is
 * withheld from a pod nothing would recreate, and under the shared profile a
 * verb the person may not perform is withheld).
 *
 * ## Three answers, kept apart
 *
 * Reading, failed, and an object with no verb are three sentences, never one
 * empty section: an empty menu cannot say whether the object has no action or
 * the console could not ask.
 *
 * ## Where it is drawn
 *
 * At the point it was asked for - the pointer for a right-click, under the `⋯`
 * for the `⋯`, the menu key and Shift+F10 - and `fixed`, because a list's cells
 * clip what they hold. Then moved inside the window where it would cross an
 * edge. The menu itself - its scrim, its keys, Escape - is `MenuPanel`, the one
 * every `⋯` in the console opens.
 */

/** Why every verb is refused while another confirmation is up. */
const BUSY = 'Another confirmation is open: finish or cancel it first';

/** What the menu says where the object has no verb for it. */
export const NO_VERB = 'No verb for it in this menu. Open it to read or edit it.';

export function RowMenu({
  identity,
  anchor,
  onClose,
  onChoose,
  onOpen,
  onCopy,
  busy = false,
}: {
  identity: ObjectIdentity;
  anchor: RowMenuAnchor;
  onClose: () => void;
  /** A verb was chosen. The caller previews it, through the drawer's own flow. */
  onChoose: (action: DetailAction) => void;
  /** Open the object in the drawer, as a click on its row does. */
  onOpen: () => void;
  /** Copy this text, and say that it was copied. */
  onCopy: (text: string) => void;
  /** Another confirmation is in flight or open: every verb is drawn refused,
   *  saying so, until it is answered and closed. */
  busy?: boolean;
}): ReactNode {
  const detail = useResourceDetail(identity.cluster, identity.kind, identity.namespace, identity.name);
  const capabilities = useCapabilities().data;
  const dock = useDockIfPresent();
  const shell = useShellIfPresent();
  const panel = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState({ left: anchor.x, top: anchor.y });

  const answer = detail.data;
  const page = answer?.state === 'ok' ? answer.data : undefined;
  const actions: readonly DetailAction[] | undefined = page === undefined ? undefined : (page.actions ?? []);
  /* Why the read failed, or undefined while it is still being made: an answer
     that is not `ok` carries the server's sentence, and a request that never
     got one carries the client's. */
  const failure =
    answer !== undefined && answer.state !== 'ok'
      ? answer.reason.message
      : detail.isError
        ? detail.error.message
        : undefined;

  const pod = isPodKind(identity.kind) && identity.namespace !== undefined && identity.namespace !== '';
  const offersLogs = dock !== null && pod;
  // The containers arrive with the object's read: before it, no Shell, rather
  // than one that would open in whichever container the server picked.
  const execOn = shellOn(capabilities, page?.shell, page !== undefined);
  const exec = dock !== null && pod && page !== undefined && execOn.drawn ? execChoices(page.containers) : undefined;

  // Inside the window, measured once the panel has its size - and again when
  // what it holds changes, because the verbs and the shells arrive after it is
  // drawn.
  const content = `${String(actions?.length ?? (failure === undefined ? 'reading' : 'failed'))}|${String(exec?.choices.length ?? -1)}`;
  useLayoutEffect(() => {
    const box = panel.current?.getBoundingClientRect();
    if (box === undefined) return;
    const room = 8;
    // Right of and below the point where it fits, left of and above it where it
    // does not - and never past an edge: a `⋯` scrolled out of a wide table's
    // view is a point outside the window.
    const place = (point: number, size: number, window: number): number => {
      const flipped = point + size + room > window ? point - size : point;
      return Math.min(Math.max(room, flipped), Math.max(room, window - size - room));
    };
    const left = place(anchor.x, box.width, window.innerWidth);
    const top = place(anchor.y, box.height, window.innerHeight);
    setAt((was) => (was.left === left && was.top === top ? was : { left, top }));
  }, [anchor.x, anchor.y, content]);

  const close = (): void => {
    onClose();
    anchor.from.focus();
  };

  // `namespace/name`, or the name alone on a cluster-scoped kind: what the
  // header says and what Copy name copies.
  const name = `${identity.namespace === undefined ? '' : `${identity.namespace}/`}${identity.name}`;

  /** A shell tab gets the pane's width, as the drawer's Shell gives it. */
  const openShell = (container: string | undefined): void => {
    const opened = dock?.openShell({
      kind: 'exec',
      cluster: identity.cluster,
      label: container === undefined ? identity.name : `${identity.name}/${container}`,
      ...(identity.namespace === undefined ? {} : { namespace: identity.namespace }),
      name: identity.name,
      ...(container === undefined ? {} : { container }),
    });
    if (opened === true) shell?.closeDrawer();
  };

  return (
    <MenuPanel
      label={`Actions on ${name}`}
      close={close}
      onDismiss={onClose}
      panelRef={panel}
      style={{ left: at.left, top: at.top }}
      className="fixed max-w-80"
    >
      {/* What the menu is about: the row it was opened on, which a menu at the
          pointer does not otherwise say. */}
      <p className="truncate px-1.5 py-1 text-2xs text-muted" title={name}>
        {name}
      </p>

      {/* The safe entries: nothing here changes the cluster. */}
      <OverflowMenuItem label="Open" title="Open it beside the list" onSelect={onOpen} />
      {offersLogs ? (
        <OverflowMenuItem
          label="Logs"
          title="Follow its logs in the dock"
          onSelect={() => {
            if (identity.namespace === undefined) return;
            if (dock?.openLogs({ cluster: identity.cluster, kind: 'pods', namespace: identity.namespace, name: identity.name })) {
              shell?.closeDrawer();
            }
          }}
        />
      ) : null}
      {exec === undefined ? null : execOn.refused !== undefined || exec.notRunning !== undefined ? (
        <OverflowMenuItem
          label="Shell"
          title={execOn.refused ?? exec.notRunning}
          disabled
          onSelect={() => undefined}
        />
      ) : exec.choices.length > 1 ? (
        // Several containers: one entry each, the drawer's picker laid flat,
        // so no shell opens in a container nobody chose.
        exec.choices.map((container) => (
          <OverflowMenuItem
            key={container.name}
            label={`Shell: ${container.name}`}
            title={`Open a shell in ${container.name}`}
            onSelect={() => {
              openShell(container.name);
            }}
          />
        ))
      ) : (
        <OverflowMenuItem
          label="Shell"
          title="Open a shell in it"
          onSelect={() => {
            openShell(exec.choices[0]?.name);
          }}
        />
      )}
      <OverflowMenuItem
        label="Copy name"
        title={`Copy ${name}`}
        onSelect={() => {
          onCopy(name);
        }}
      />

      <OverflowMenuSeparator />

      {/* The drawer's verbs. */}
      {busy ? <p className="px-1.5 py-1 text-xs text-warn">{BUSY}.</p> : null}
      {actions === undefined ? (
        failure === undefined ? (
          <p className="px-1.5 py-1 text-xs text-muted">Reading what can be done to it…</p>
        ) : (
          <p role="alert" className="px-1.5 py-1 text-xs text-bad">
            {failure === '' ? 'Could not read what can be done to it.' : `Could not read what can be done to it: ${failure}`}
          </p>
        )
      ) : actions.length === 0 ? (
        // Not "nothing can be done to it": that is false of every Secret and
        // ConfigMap, whose drawer edits a key, and of the manifest's own Edit.
        // What is true is that this menu has no verb.
        <p className="px-1.5 py-1 text-xs text-muted">{NO_VERB}</p>
      ) : (
        actions.map((action) => (
          <OverflowMenuItem
            key={action.verb}
            label={action.label}
            // `Verbs.tsx`'s title, for the same two cases: a refused verb says
            // why, and a permitted one carries a reason only when the console
            // could not ask the cluster and offers it anyway.
            title={
              busy
                ? BUSY
                : action.permitted === true
                  ? (action.reason?.message ?? action.label)
                  : (action.reason?.message ?? 'not permitted on this cluster')
            }
            // **Red only where the verb is irreversible** on this menu: Delete,
            // and never Evict or Restart, which stay `destructive` everywhere
            // else - the drawer's chips, the selection bar and the confirmation
            // color by that flag.
            // The server's answer (`DetailAction.irreversible`), never the
            // verb's name.
            destructive={action.irreversible === true}
            disabled={busy || action.permitted !== true}
            onSelect={() => {
              onChoose(action);
            }}
          />
        ))
      )}
    </MenuPanel>
  );
}
