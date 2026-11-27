import { useId, useState, type ReactNode } from 'react';
import { useMutation } from '@tanstack/react-query';

import {
  previewNodeShell,
  useCapabilities,
  type Capabilities,
  type ConfirmationPlan,
  type PodContainer,
  type ShellOffer,
} from '../../api';
import { Icon } from '../../components/Icon';
import { MenuScrim } from '../../components/MenuScrim';
import { PlanDialog } from '../../components/PlanDialog';
import { useDockIfPresent } from '../../dock/DockContext';
import { btn } from '../../components/buttons';
import { ImageRef } from '../../components/ImageRef';
import { debugOn, debugRequest, debugTargets, probablyNoShell, type DebugChoice } from '../../debug/debug';
import { DebugForm } from '../../debug/DebugForm';
import { knownNoShell, useNoShellFacts } from '../../debug/noShellMemory';
import { focusRing } from '../../focus';
import { watching } from '../../hover';
import { KeyLayer } from '../../keyboard/layers';
import { useEscape } from '../../keyboard/useKeyLayer';
import { useShellIfPresent } from '../../shell/ShellContext';
import type { ObjectIdentity } from './address';

/**
 * The three ways into a terminal on one object: a shell inside a pod, a shell on
 * a node, and a Claude session.
 *
 * ## Opened from a click, never from an effect
 *
 * `api/websocket.md` states it and gives the reason: React's StrictMode
 * double-invokes effects, so a socket opened in `useEffect` opens **two**, and
 * the session cap then reports itself as reached at half the sessions — in
 * development only, where it is least expected. Every control here opens on the
 * user's gesture.
 *
 * ## Which container is asked, not guessed
 *
 * A Shell request that sends no `container` makes the API server take
 * `spec.containers[0]` - silently, on every pod with a sidecar, with nothing
 * offered to change it. That is the shape of defect this whole console is
 * arranged against: a confident answer to a question nobody was asked.
 *
 * So a pod with more than one app container is asked about here. A pod with one
 * container is not asked, and an init container is never offered: it has finished by the time anybody
 * opens a shell, and `initContainers` is a separate list the exec subresource
 * cannot reach anyway.
 *
 * ## The capability is a hint and never the control
 *
 * Each button is drawn on `capabilities`, and the contract is explicit that this
 * is a courtesy: *"the capability flag is the front end's hint; the refusal is
 * the contract. Every channel checks its own condition on the upgrade."* So a
 * missing button is a tidier console, never a guarantee — which is why nothing
 * here re-derives a permission and why a refusal arrives as a frame on the
 * socket rather than as an assumption on this side.
 */
export function Terminals({
  identity,
  containers,
  kindLabel,
  subjectNote,
  shellOffer,
  debugOffer,
}: {
  identity: ObjectIdentity;
  /** Whether this pod's page offers Debug: `DetailPage.debug`, on both
   *  profiles (`debugOn`). */
  debugOffer?: ShellOffer | undefined;
  /** Whether this pod's page draws Shell, under `shared`: `DetailPage.shell`,
   *  answered for this pod (`shellOn`). Ignored on the desktop. */
  shellOffer?: ShellOffer | undefined;
  /** The object's Kind as the server writes it - `DetailPage.title`, "Pod" - for
   *  the assistant session's framing. The route carries the discovery plural and
   *  that is not the same word.
   *
   *  **Its absence disables the assistant control**, and that is not fussiness
   *  about a label: `claude.go`'s `hasObject` is `Kind != "" && Name != ""`, so
   *  a session opened without the Kind is a session with no subject at all - the
   *  object's own framing is dropped whole, silently, and the pane opens on the
   *  cluster while claiming in its tab to be about a pod. */
  kindLabel?: string | undefined;
  /** Why there is no Kind yet, for the disabled controls' own title - the
   *  assistant's and the Shell's, which is the same fact and
   *  therefore the same sentence. Still being read, or could not be read: two
   *  different sentences, and a user hovering a grayed button is owed the
   *  right one. */
  subjectNote?: string | undefined;
  /** The pod's containers, where the object has any. A pod with several is asked
   *  about rather than guessed at; the server would otherwise pick the first,
   *  which on a pod with a sidecar is rarely the one a user meant. */
  containers?: readonly PodContainer[] | undefined;
}): ReactNode {
  const dock = useDockIfPresent();
  const shell = useShellIfPresent();
  const capabilities = useCapabilities();
  /*
    The node shell's plan **with the node and cluster it was made for**, as
    `useObjectAct` holds `target` beside its plan. The dialog's title and the
    dock tab must not read `identity`, which is whatever this panel is showing
    *now*: re-targeted while the dialog was open, they would name node B while
    `plan.token` - the only thing the server reads - opened node A. Whatever
    names the confirmation reads it from here.
  */
  const [held, setHeld] = useState<{ cluster: string; node: string; plan: ConfirmationPlan } | undefined>(
    undefined,
  );
  const [failure, setFailure] = useState<string | null>(null);
  const [picking, setPicking] = useState(false);
  const [debugging, setDebugging] = useState(false);
  const panelId = useId();
  const debugPanelId = useId();
  // Read so a Shell tab ending in `no_shell` draws Debug here at once, with
  // this drawer still open beside the dock.
  useNoShellFacts();

  // Escape closes the container menu, claimed at `KeyLayer.Menu` from the one
  // keyboard stack - `ColumnMenu`'s claim, made the same way, for the reason
  // `MenuScrim` sets out at length: a hand-written `document` listener runs in a
  // different phase from every React handler. Declared here with the other
  // hooks, above the `dock === null` return, because a hook behind a condition
  // changes the hook order with the presence of a dock.
  useEscape(
    KeyLayer.Menu,
    () => {
      setPicking(false);
      setDebugging(false);
    },
    picking || debugging,
  );

  const able = capabilities.data;
  const pod = isPodKind(identity.kind);
  const node = isNodeKind(identity.kind);

  const review = useMutation({
    mutationFn: (on: { cluster: string; node: string }) => previewNodeShell(on.cluster, on.node),
    // The node is the one the preview was asked about, taken from the request
    // rather than from `identity` when the answer lands.
    onSuccess: (plan, on) => {
      setHeld({ ...on, plan });
      setFailure(null);
    },
    onError: (error: Error) => setFailure(error.message),
  });

  /**
   * A pane at the bottom read beside a drawer on the right is a terminal
   * squeezed into what is left, and a user who asks for the pane wants the width
   * for it. So **every control on this row that opens a pane closes the drawer
   * once the pane is open** - and only then: a refused open leaves the user on
   * the object with the dock's note saying why. `LogsButton` makes the same
   * gesture.
   *
   * One function, called by every control on the row, so the next control
   * inherits the rule rather than the omission. `shell` may be null where there
   * is no Shell above this control, and there a drawer to close does not exist.
   */
  const giveThePaneTheWidth = (opened: boolean | undefined): void => {
    if (opened === true) shell?.closeDrawer();
  };

  const openExec = (container?: string, image?: string): void => {
    setPicking(false);
    const opened = dock?.openShell({
      kind: 'exec',
      cluster: identity.cluster,
      // The pod, and the container where one was chosen. `truncate.ts` decides
      // what survives in a narrow tab; composing a shorter label here would be a
      // second opinion about that.
      label: container === undefined ? identity.name : `${identity.name}/${container}`,
      ...(identity.namespace === undefined ? {} : { namespace: identity.namespace }),
      name: identity.name,
      ...(container === undefined ? {} : { container }),
      // Not sent on `exec`: carried so a Shell that finds no shell can say in
      // which image it found none (`noShellMemory.ts`).
      ...(image === undefined || image === '' ? {} : { image }),
    });
    giveThePaneTheWidth(opened);
  };

  const openDebug = (choice: DebugChoice): void => {
    setPicking(false);
    setDebugging(false);
    if (identity.namespace === undefined) return;
    giveThePaneTheWidth(
      dock?.openShell(debugRequest({ cluster: identity.cluster, namespace: identity.namespace, pod: identity.name }, choice)),
    );
  };

  const openNodeShell = (): void => {
    if (held === undefined) return;
    setHeld(undefined);
    dock?.openShell({
      kind: 'nodeshell',
      cluster: held.cluster,
      label: held.node,
      // **The node is not sent.** The server reads it out of the plan, which is
      // what makes the token the only thing that decides which machine this
      // opens on - `api/websocket.md`, and `termsocket.ts` says the same from
      // the other end. Passing it here would be a second address for one fact,
      // and the weaker of the two.
      //
      // **The token crosses protocols**, and it is the only thing binding the
      // sentence the user agreed to to the pod that is then created. The
      // upgrade spends it exactly once and refuses without it.
      token: held.plan.token,
    });
  };

  const { choices, notRunning } = execChoices(containers);
  /*
    **The picker offers only what can take a shell.** A container the kubelet reported as waiting or terminated ends in a refused
    exec, so it is left out of the question once another one is running. A
    container nobody reported stays in: unknown is not stopped.
  */
  const only = choices.length === 1 ? choices[0]?.name : undefined;

  /**
   * Whether the object has answered yet - one fact, read once, for both
   * controls that depend on it.
   *
   * `kindLabel` and `containers` arrive together, in the same payload. Before
   * it, `choices` is empty on a pod that has four, so the question below is not
   * asked at all and the exec route falls back to `spec.containers[0]` - the
   * silent pick this file's header says the console refuses to ship. The
   * capabilities read can land before the object's, so without this the Shell
   * control would be enabled from the first paint, wide enough to click
   * through, while the assistant control already waited.
   *
   * Deliberately the same condition and the same sentence as the assistant
   * control below rather than a second pair: two ways of saying *not yet* in
   * one component drift, and a user hovering either grayed control is
   * owed the same answer about which read is still out.
   */
  const unread = kindLabel === undefined;
  const unreadNote =
    subjectNote ?? 'This object has not been read yet, so a session about it has no subject.';

  /*
    **Nothing to open a shell in.** A Pending pod, or one whose
    only container is in ImagePullBackOff, offered Shell as if a process were
    there to attach to, and the click ended in a refused upgrade in the dock.
    `exec` needs a running container, and the payload says which are: the
    control is disabled, with the reason on it, until one is.

    **Only on positive evidence.** Every container
    must have been *reported* stopped - waiting or terminated. The server always
    sends a status, and `unreported` is what it sends when the status block
    could not be read (`internal/detail/health.go`): that is the console not
    knowing, and "not reported" is not "not running". One unreported container,
    or no containers in the payload, keeps the control. This is the cosmetic
    half; the server's refusal of the exec is the one that holds.

    **And it says how to check again.** The drawer does not re-read its object
    (`resourceDetailQuery`: a drawer is a thing being read), so a pod that
    starts while it is open keeps this grayed control until it is read again.
    Re-reading on a timer would add the behavior that query deliberately
    refuses; the sentence tells the user the one gesture that re-reads it.
  */

  // No dock, no controls - the same guard `LogsButton` uses, and for the same
  // reason: these open a tab, and a tree with no dock has nowhere to put one.
  //
  // **After the hooks and not before them.** Returning above `useMutation` would
  // be a conditional hook call: the hook order would change with the presence of
  // a dock and React would lose track of which state belongs to what. It would
  // still type-check.
  if (dock === null) return null;

  const execOn = shellOn(able, shellOffer, !unread);
  const offersExec = pod && execOn.drawn;
  /*
    **Debug, where a shell is missing.** The Shell menu always carries the debug
    form; a Debug control of its own is drawn beside Shell only where a running
    container probably has no shell - by its image (`probablyNoShell`), or
    because a Shell on it already found none (`noShellMemory.ts`). A pod whose
    containers all have one is not offered a second way into the same thing on
    its row.

    Never on an ephemeral container: it is the debug container itself, and it
    is not a container another one may join.
  */
  const debugAt = debugOn(able, debugOffer, !unread);
  const debugNamespace = identity.namespace;
  const offersDebug = offersExec && debugAt.drawn && debugNamespace !== undefined && debugNamespace !== '';
  const targets = debugTargets(containers);
  const shellless = offersDebug
    ? targets.filter(
        (one) =>
          probablyNoShell(one.image) ||
          knownNoShell({
            cluster: identity.cluster,
            namespace: debugNamespace,
            pod: identity.name,
            container: one.name,
            image: one.image,
          }),
      )
    : [];
  const targetNames = targets.map((one) => one.name);
  const offersNodeShell = node && able?.nodeShell === true;
  /*
    **Not on a Secret**, which is the one kind this control is withheld on for a
    reason that is not a capability.

    The assistant must not be offered on Secrets, since it has no right to read
    them. Withheld rather than disabled, and the same shape as the capability
    arm for the same reason: a grayed control invites the question of what would
    enable it, and nothing would.

    **Both names for the kind, and the route's is the load-bearing one.** The
    discovery plural is in the URL and is therefore known on the first paint;
    `kindLabel` arrives with the payload, a read this file already documents as
    landing *after* the capabilities one, and a control drawn for that
    gap is a control that can be clicked in it. `kindLabel` is read as well
    because the route segment is not guaranteed to be the core plural on a kind
    served under another name.

    **And this is the cosmetic half**, which this file's own header already says
    of every control here: *"a missing button is a tidier console, never a
    guarantee"*. `internal/terminal/claude.go` refuses an assistant session
    addressed to a Secret at the upgrade, and that is the half that holds when
    the socket is addressed by something other than this button.
  */
  const secret = isSecretKind(identity.kind) || kindLabel === 'Secret';
  const offersAssistant = able?.assistant === true && !secret;

  // Nothing offered, nothing drawn - including while `capabilities` is still in
  // flight, which is the ordinary first paint. An empty group is a gap in the
  // action row that appears and then fills, and the row jumps under the
  // user's pointer as it does.
  if (!offersExec && !offersNodeShell && !offersAssistant) return null;

  return (
    <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Terminals">
      {offersExec ? (
        /*
          The Shell control, and under it the question of which container.

          ## The container menu is the console's own popover

          A **row of buttons** would be wrong about the shape: four containers
          push the whole action band wider inside a 560px drawer, and the
          controls beside it move under the pointer.

          A **native `<select>`** is drawn *after* the button rather than under
          it, so the row reflows anyway; the browser paints the popup in its own
          style in the middle of a console that has one; and its empty *Which
          container?* resting option is a second click bought for nothing.

          **A popover anchored to the button** is what this is - the pattern
          `ColumnMenu` and `TableMenu` already implement, in the console's own
          style, and deliberately not a fifth private popover: the same
          `MenuScrim` four other menus share for the pointer, and the same
          `KeyLayer.Menu` Escape claim above for the keyboard. `relative` here
          and `absolute` on the panel is what keeps the promise the `<select>`
          could not: **the button does not move or change size when the menu
          opens.**

          The panel is anchored `left-0` rather than `ColumnMenu`'s `right-0`.
          That is not a preference either: this control sits at the near end of the drawer's row, so a panel hung from its right edge
          would open across the row's width instead of under the button it
          belongs to.
        */
        <div className="relative">
          {/* Shell and its menu control side by side, in a row of their own
              inside the anchor, so the panel below is a block after that row:
              its static position is under the pair, which is what the phone
              width's `fixed` panel keeps as its top. */}
          <div className="flex items-center">
            <button
              type="button"
              // Disabled until the object has answered, rather than opening a shell
              // in whichever container the server would have picked. See `unread`.
              disabled={unread || notRunning !== undefined || execOn.refused !== undefined}
              // Announced as a menu button **only where there is a menu**. A pod
              // with one container opens a shell on the click and has no panel to
              // expand, and `aria-expanded="false"` there would promise a reader
              // something that never arrives.
              aria-expanded={choices.length > 1 ? picking : undefined}
              aria-controls={choices.length > 1 && picking ? panelId : undefined}
              onClick={() => {
                // The question is asked from the payload and never from an empty
                // list: `choices` is empty both on a pod with one container and on a
                // pod nobody has read yet, and only the first of those may open a
                // shell without asking. The guard is here as well as on `disabled`
                // because a disabled attribute is a property of the button and this
                // is a property of the gesture.
                if (unread || notRunning !== undefined || execOn.refused !== undefined) return;
                if (choices.length > 1) {
                  setPicking((was) => !was);
                  return;
                }
                openExec(only, choices[0]?.image);
              }}
              title={unread ? unreadNote : (execOn.refused ?? notRunning ?? `Open a shell inside ${identity.name}`)}
              // `watching`, with Logs and the node shell: it carries its own disabled negation - a Shell the pod has not
              // answered for must not draw the accent frame under the pointer,
              // which is a click it refuses. Square on its right: the menu
              // control is joined to it.
              className={`${btn} ${offersDebug ? 'rounded-r-none' : ''} border-line text-muted ${watching}`}
            >
              <Icon name="terminal" />
              Shell
            </button>
            {/* The menu: the containers, then a debug container. Shell keeps
                its one click; this is the way to the rest of the menu on a pod
                where that click opens a shell without asking. Drawn only where
                a debug container is offered, since without one the menu holds
                nothing Shell's own click does not, and narrow, joined to Shell:
                a pod's control row has to fit the drawer's 1024px cap (471px)
                on one line, and a separate button overran it. Grayed only where
                nothing in the menu could be taken: before the pod is read, or
                with no container running. */}
            {offersDebug ? (
              <button
                type="button"
                data-shell-menu=""
                aria-label="Shell menu"
                aria-expanded={picking}
                aria-controls={picking ? panelId : undefined}
                disabled={unread || notRunning !== undefined}
                title={unread ? unreadNote : (notRunning ?? 'Containers and a debug container')}
                onClick={() => {
                  if (unread || notRunning !== undefined) return;
                  setDebugging(false);
                  setPicking((was) => !was);
                }}
                className={`${btn} -ml-px rounded-l-none border-line !px-0 text-muted ${watching}`}
              >
                <Icon name="chevron" className="size-3 rotate-90" />
              </button>
            ) : null}
          </div>

          {picking ? (
            <>
              <MenuScrim
                onDismiss={() => {
                  setPicking(false);
                }}
              />

              <div
                id={panelId}
                // `z-20` over the scrim's `z-10`, or the first click on a row
                // closes the menu instead of choosing. `ColumnMenu`'s panel, to
                // the class.
                //
                // At phone width it spans the drawer instead (`fixed`, its top
                // left where it stands): a 288px panel hung from Shell would
                // run past the screen's right edge.
                className={`absolute left-0 z-20 mt-1 rounded-lg border border-line bg-panel-2 p-2 shadow-lg ${
                  offersDebug ? POPOVER_WIDE : 'w-56'
                }`}
              >
                <ul aria-label="Container">
                  {choices.map((one, index) => (
                    <li key={one.name}>
                      <button
                        type="button"
                        // The first row focused on open, so the menu is usable
                        // from the keyboard the moment it exists rather than
                        // after a Tab that lands nowhere visible. The scrim is
                        // `tabIndex={-1}` and out of the accessibility tree
                        // so Tab from here reaches the next row.
                                        autoFocus={index === 0}
                        disabled={execOn.refused !== undefined}
                        title={execOn.refused}
                        onClick={() => {
                          openExec(one.name, one.image);
                        }}
                        className={`flex w-full flex-col items-start gap-0.5 rounded-sm px-1 py-1 text-left text-sm hover:bg-panel disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent ${focusRing}`}
                      >
                        <span className="max-w-full truncate">{one.name}</span>
                        {/* The image beneath the name, because on a pod with a
                            sidecar the names are `app` and `sidecar` and the
                            image is the only thing that says which is which.
                            Guarded rather than assumed present: `image` is
                            required in the contract and a payload that arrived
                            without it must draw one line, not the word
                            "undefined" under every row. */}
                        {one.image === undefined || one.image === '' ? null : (
                          <ImageRef image={one.image} copy={false} className="max-w-full" />
                        )}
                      </button>
                    </li>
                  ))}
                </ul>
                {offersDebug ? (
                  <div data-debug-in-menu="" className="mt-2 border-t border-line pt-2">
                    <p className={`mb-1 flex items-center gap-1 text-sm ${debugAt.refused === undefined ? '' : 'text-muted'}`}>
                      <Icon name="debug" />
                      Debug container
                    </p>
                    <DebugForm targets={targetNames} refused={debugAt.refused} onStart={openDebug} />
                  </div>
                ) : null}
              </div>
            </>
          ) : null}
        </div>
      ) : null}

      {offersDebug && shellless.length > 0 ? (
        <div className="relative">
          <button
            type="button"
            data-debug-entry=""
            aria-expanded={debugging}
            aria-controls={debugging ? debugPanelId : undefined}
            disabled={debugAt.refused !== undefined}
            onClick={() => {
              if (debugAt.refused !== undefined) return;
              setPicking(false);
              setDebugging((was) => !was);
            }}
            title={debugAt.refused ?? `Start a debug container in ${identity.name}`}
            className={`${btn} border-line text-muted ${watching}`}
          >
            <Icon name="debug" />
            Debug
          </button>
          {debugging ? (
            <>
              <MenuScrim onDismiss={() => setDebugging(false)} />
              <div
                id={debugPanelId}
                role="dialog"
                aria-label="Debug container"
                className={`absolute left-0 z-20 mt-1 rounded-lg border border-line bg-panel-2 p-3 shadow-lg ${POPOVER_WIDE}`}
              >
                {/* The container that has no shell is the one chosen: it is why
                    this control is here. */}
                <DebugForm targets={targetNames} initialTarget={shellless[0]?.name} onStart={openDebug} />
              </div>
            </>
          ) : null}
        </div>
      ) : null}

      {offersNodeShell ? (
        <button
          type="button"
          disabled={review.isPending}
          onClick={() => review.mutate({ cluster: identity.cluster, node: identity.name })}
          title={`Open a shell on ${identity.name}`}
          // The third watching control, alongside Logs and Shell.
          className={`${btn} border-line text-muted ${watching}`}
        >
          <Icon name="node" />
          {review.isPending ? 'Checking…' : 'Node shell'}
        </button>
      ) : null}

      {offersAssistant ? (
        <button
          type="button"
          // Disabled rather than opening a subjectless session that says
          // otherwise on its tab. See `kindLabel`, and `unread` for why the
          // Shell control is drawn on the same fact.
          disabled={unread}
          onClick={() =>
            giveThePaneTheWidth(
              dock.openShell({
                kind: 'claude',
                cluster: identity.cluster,
                // **All four are sent.** The address has no `{cluster}` segment,
                // `termsocket.ts` reads that as "addressed by nothing", and
                // without them every session would be refused with a plain
                // `404 unknown cluster` before the upgrade.
                // `internal/terminal/claude.go` resolves the cluster out of the
                // query and reads the object beside it; the object is what keeps
                // two sessions on two pods from mixing their context.
                ...(identity.namespace === undefined ? {} : { namespace: identity.namespace }),
                name: identity.name,
                // The Kind as a person writes it - "Pod" - because the server
                // validates it as one. The route's plural would pass validation
                // and put the wrong word in the session's own CLAUDE.md.
                ...(kindLabel === undefined ? {} : { objectKind: kindLabel }),
                // `Claude · <object>`: the label says which resource the session
                // was opened on. Without the prefix a Claude tab and a shell tab
                // on the same pod are the same word.
                label: `Claude · ${identity.name}`,
              }),
            )
          }
          title={unread ? unreadNote : `Open a Claude session about ${identity.name}`}
          // **The mark alone, and the name on `aria-label`.** Every other
          // control in this row is an `<Icon/>` plus its word. This one is not:
          // a mark recolored to the row's muted gray with *Claude* written
          // beside it would take away the one thing that identifies it.
          // `Icon.tsx` fills `i-claude` with `CLAUDE_MARK`, so the mark says
          // which button this is on its own.
          //
          // The name is on `aria-label` rather than left to `title`, and that
          // is not belt and braces. The sprite is `aria-hidden`, so with the
          // text gone the accessible name would be computed from `title` - a
          // fallback, announced inconsistently between screen readers, and one
          // that here carries the *disabled* sentence instead of the name.
          // `aria-label` is the constant name; `title` stays the sentence that
          // changes with `unread`.
          aria-label="Claude"
          // **The hover lights the surface, not the mark.** `hover:text-fg`, as
          // on the neighboring controls, would change the computed `color` on
          // hover but not a pixel on screen: Logs and Shell change under that
          // rule because their glyph and their word are painted in
          // `currentColor`, whereas the only thing painted here is the mark, and
          // its fill is `CLAUDE_MARK`.
          //
          // So letting the mark inherit `currentColor` to get a hover would put a
          // recolored trademark back. Nor a brightness or an opacity filter on
          // the mark: quieter, and the same act.
          //
          // `hover:bg-panel-2` is **the console's existing answer for a control
          // that is a glyph with no word** - `Menu.tsx`'s fold, its jump box and
          // its kind links, and `Rail.tsx`'s fold, all light their surface
          // rather than their content once they are folded to an icon. This is
          // that pattern carried across rather than a fifth vocabulary invented
          // for one button. It stays quieter
          // than the destructive verbs on the row's other arm, which are drawn
          // in `--color-bad`, and it cannot swallow the keyboard: the focus ring
          // is a 2px accent *outline* one pixel outside the border, and this is
          // a fill inside it.
          //
          // **The hover names the control.** The pair is the surface fill plus
          // `hover:border-claude`, in Claude's own color. `--color-claude` is
          // in the theme; the mark itself keeps `CLAUDE_MARK` from `Icon.tsx`,
          // its one exemption from the console's own palette. Nothing here
          // recolors the mark, and nothing here is a text color - the
          // paragraph above is why neither is available.
          //
          // Not the accent frame its neighbors take. Logs, Shell and the node
          // shell are the watching arm; this is Claude's own, and giving all four
          // the same blue would stop the hover saying which control it is on.
          //
          // **The height comes from a line box, not from the row.** With no word
          // the button's only flex item is the 15px mark, which would stand 21px
          // tall beside neighbors of 24. `self-stretch` would borrow the height
          // from a taller sibling, which is only there when Shell or Logs is: on
          // a Namespace, where Claude is the one terminal control, there is
          // nothing to stretch to. The mark sits in a box one line of
          // `text-xs` tall (`h-lh`), which is exactly what the word gives its
          // neighbors, so the button is their height whichever of them are
          // present - and it follows the type scale rather than a pinned figure.
          className={`${btn} border-line text-muted hover:border-claude hover:bg-panel-2 disabled:hover:border-line disabled:hover:bg-transparent`}
        >
          <span data-line-box="" className="flex h-lh items-center">
            <Icon name="claude" />
          </span>
        </button>
      ) : null}

      {failure === null ? null : (
        <span role="alert" className="text-xs text-bad">
          {failure}
        </span>
      )}

      {held === undefined ? null : (
        <PlanDialog
          title={`Open a shell on ${held.node}?`}
          plan={held.plan}
          pending={false}
          error={failure}
          onCancel={() => setHeld(undefined)}
          onApply={openNodeShell}
        />
      )}
    </div>
  );
}

/** A popover holding the debug form: 288px under its control, and at phone
 *  width the drawer's width from its static top (the rail and the drawer's
 *  gutter on the left, the gutter on the right). */
const POPOVER_WIDE = 'w-72 max-sm:fixed max-sm:right-3 max-sm:left-[4.25rem] max-sm:w-auto'; // style-allow: the rail (56px) and the drawer's 12px gutter, where the drawer's content starts at phone width

/**
 * Why no container can take a shell, in the kubelet's own word where it gave
 * one: `ImagePullBackOff`, `ContainerCreating`, `CrashLoopBackOff`. The word is
 * what a user searches for, so it is in the sentence rather than "not
 * ready".
 */
function notRunningNote(containers: readonly PodContainer[]): string {
  const words = [
    ...new Set(
      containers.map((one) => {
        const status = one.status;
        if (status === undefined) return 'stopped';
        return status.reason === undefined || status.reason === '' ? status.state : status.reason;
      }),
    ),
  ];
  return (
    `No container in this pod is running (${words.join(', ')}), so there is nothing to open a shell in. ` +
    'Close this object and open it again to check whether one has started.'
  );
}

/**
 * Which containers a shell may be opened in, and why none can where none can.
 *
 * The picker's own rule, in one place for its two callers: this file's Shell
 * control and a list row's context menu, which offers the same shells as the
 * drawer and no others. **The picker offers only what can take a shell**: a container the kubelet reported as
 * waiting or terminated ends in a refused exec, so it is left out once another
 * one is running, and a container nobody reported stays in - unknown is not
 * stopped. `notRunning` is set only on positive evidence that every container
 * is stopped; see the control's own comment.
 */
/**
 * Whether Shell is drawn on a pod, and why it is grayed where it is.
 *
 * **On the desktop `podExec` alone decides**: the credentials are the user's
 * own and the console does not ask.
 *
 * **Under `shared` the pod's page decides** (`DetailPage.shell`). `podExec`
 * there says only that this build can open a shell in a pod at all; a single
 * review of `create pods/exec` in every namespace would answer no to a person
 * granted it in some namespaces only and draw Shell nowhere while the socket
 * would let them in. So:
 *
 * - **not drawn until the page has answered** - drawing it grayed while the
 *   object is read and then taking it away from a person who may not would be
 *   a control that appears and vanishes under the pointer;
 * - **not drawn where `shell` is absent**: the person may not open a shell in
 *   this pod, which is withheld as a verb they may not perform is;
 * - **drawn grayed with the server's sentence** where it is not permitted: the
 *   console could not ask, or the person may and the console's own
 *   ServiceAccount may not.
 *
 * The cosmetic half, like every control here: the socket reviews the person
 * again when it opens.
 */
export function shellOn(
  able: Capabilities | undefined,
  offer: ShellOffer | undefined,
  read: boolean,
): { drawn: boolean; refused: string | undefined } {
  if (able?.podExec !== true) return { drawn: false, refused: undefined };
  if (able.mode !== 'shared') return { drawn: true, refused: undefined };
  if (!read || offer === undefined) return { drawn: false, refused: undefined };
  if (offer.permitted) return { drawn: true, refused: undefined };
  return {
    drawn: true,
    refused: offer.reason?.message ?? 'The console could not establish whether a shell may be opened here.',
  };
}

export function execChoices(containers: readonly PodContainer[] | undefined): {
  choices: PodContainer[];
  notRunning: string | undefined;
} {
  // Never a debug container: it is the console's own, and on a pod whose app
  // container is down it would hide the note and stand as the only choice.
  const declared = (containers ?? []).filter((one) => one.init !== true && one.ephemeral !== true);
  const choices = declared.some(isRunning) ? declared.filter((one) => !isStopped(one)) : declared;
  const notRunning =
    declared.length > 0 && declared.every(isStopped) ? notRunningNote(declared) : undefined;
  return { choices, notRunning };
}

/** Reported running by the kubelet. */
function isRunning(container: PodContainer): boolean {
  return container.status?.state === 'running';
}

/** Reported, by the kubelet, as not running: waiting or terminated. Never
 *  `unreported` and never an absent status - those are unknown, not stopped. */
function isStopped(container: PodContainer): boolean {
  const state = container.status?.state;
  return state === 'waiting' || state === 'terminated';
}

/** The two kinds these controls are about, by the discovery plural the route
 *  carries. Deliberately narrow: a kind this does not recognize gets no button,
 *  which is the direction to be wrong in - the alternative is offering a shell
 *  inside a ConfigMap. */
export function isPodKind(kind: string): boolean {
  return kind === 'pods' || kind === 'pod';
}

function isNodeKind(kind: string): boolean {
  return kind === 'nodes' || kind === 'node';
}

/** A Secret, by either of the two names a kind travels under here: the route's
 *  discovery plural and the singular the payload's `title` carries.
 *
 *  Deliberately not a suffix test. `SealedSecret`, `ExternalSecret` and
 *  `SecretStore` are custom resources holding a reference or a ciphertext rather
 *  than the credential, and only Secrets are in question. The server's
 *  `isSecretKind` draws the same line, and the server's is the one that
 *  enforces. */
function isSecretKind(kind: string): boolean {
  return kind === 'secrets' || kind === 'secret';
}
