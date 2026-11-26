import {
  useCallback,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from 'react';

import { btnSecondary } from '../components/buttons';
import { CloseButton } from '../components/CloseButton';
import { Icon } from '../components/Icon';
import { MenuScrim } from '../components/MenuScrim';
import { focusRingFlush } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { LogPane } from './LogPane';
import { TerminalPane } from './TerminalPane';
import { FINE, STEP } from '../shell/drawerWidth';
import { DEFAULT_HEIGHT, MIN_HEIGHT, clampHeight, useDock } from './DockContext';
import {
  endIsConfirmed,
  endWarning,
  isQuiet,
  liveElsewhere,
  scopeRows,
  shownCluster,
  visibleTabs,
  type Tab,
} from './tabs';
import { middleTruncate, shortCluster } from './truncate';

/**
 * The dock: the panel across the bottom, and everything live in it.
 *
 * **This component is the reason the Shell sits outside `<Outlet/>`.** A
 * terminal is a live WebSocket with a PTY behind it, and a log stream is one of
 * sixteen follows the server will hold, so anything that unmounts either kills
 * it. The router's own layout-route semantics keep both alive: this component is not inside the Outlet, so a
 * navigation does not touch it. The one thing to preserve when routes change:
 * `Dock` must never move inside an `<Outlet/>`, and there is no test that would
 * notice if it did.
 *
 * ## The one rule the React side must not get wrong
 *
 * **A hidden pane stays mounted.** `display: none` on a mounted subtree, never
 * conditional rendering: a React tab component that unmounts on `hidden` kills
 * the socket, which is the one thing the whole partition design forbids. A
 * `kubectl get pods -w` left on cluster A is still running, with its scrollback,
 * when the operator comes back to A on Friday.
 *
 * Tailwind's `hidden` utility is the tool and the `[hidden]` attribute is the
 * trap. `[hidden] { display: none }` lives in the **browser's** stylesheet and
 * an author rule beats it: a pane with `display: flex` and `hidden = true` is
 * still drawn, stacked by `position: absolute; inset: 0`, and whichever was
 * created last wins. Switching tabs would appear to do nothing at all.
 *
 * ## Three visual states and not two
 *
 * | | When | What it looks like |
 * |---|---|---|
 * | open | something is visible here | the panel |
 * | **folded** | the operator asked for the screen back | header only. Every socket, PTY and line of scrollback is kept |
 * | **quiet** | nothing open here, something open elsewhere | header only, with the badge |
 *
 * Folded is a choice and quiet is a consequence, which is why closing the panel
 * clears the first and nothing clears the second. A dock that closed itself when
 * it went quiet would leave live shells behind no UI at all - the one outcome
 * partitioning must not produce, arriving through the door nobody watches.
 */
export function Dock(): ReactNode {
  const { state, dispatch, height, setHeight } = useDock();
  const visible = visibleTabs(state);
  const elsewhere = liveElsewhere(state);
  const quiet = isQuiet(state);
  const [menuOpen, setMenuOpen] = useState(false);

  // The dock's Escape claimant, on the layer reserved for it in
  // `keyboard/layers.ts`. It is a menu and not a dialog - nothing here may
  // block the page.
  useEscape(KeyLayer.Menu, () => setMenuOpen(false), menuOpen);

  // Dismissed by a click elsewhere as well as by Escape, and the click lands on
  // `MenuScrim` inside `ScopeMenu` - the element the three other menus use -
  // and never on a `document` listener: a `pointerdown` on `document` would set
  // this false, then the button's own `click` would toggle it back, so
  // pressing the open button would do nothing visible. `fireEvent.click`
  // dispatches no `pointerdown`, so `Dock.test.tsx` dispatches both.

  // **The section is always in the document**, and only its class changes:
  //
  //   - `router/routes.test.tsx` asserts the dock element's **identity** across
  //     a navigation, not its presence, *"because a re-rendered dock is a killed
  //     PTY and a test that only asserts `toBeInTheDocument` passes for both"*.
  //     A component that returns `null` when it is empty is remounted the moment
  //     the first tab opens, which is the same defect wearing an empty dock's
  //     clothes.
  //
  // **Not closed while it has something to say.** A reload whose only stored
  // tab was a node shell restores no tab, and a note saying why written into
  // this section while it is `hidden` would be a live region in a
  // `display: none` subtree that nobody can see or hear. So an empty dock with a note draws its header and the note, and no
  // pane: there is nothing to give a height to.
  const closed = state.tabs.length === 0 && state.note === '';
  const collapsed = state.folded || quiet || state.tabs.length === 0;

  return (
    <section
      id="dock"
      aria-label="Terminals and logs"
      className={
        closed
          ? 'hidden'
          : 'flex shrink-0 flex-col border-t border-line bg-panel'
      }
      style={closed || collapsed ? undefined : { height: `${height}px` }}
    >
      <Grip height={height} onResize={setHeight} disabled={collapsed} />

      <div className="flex items-center gap-2 px-2 py-1">
        <ScopeButton
          open={menuOpen}
          elsewhere={elsewhere}
          onToggle={() => setMenuOpen((was) => !was)}
        />

        <div role="tablist" aria-label="Open panels" className="flex min-w-0 flex-1 gap-1 overflow-x-auto">
          {visible.map((tab) => (
            <TabButton key={tab.id} tab={tab} active={tab.id === state.active} />
          ))}
          {visible.length === 0 && (
            // Two words, and a distinction that matters more than it looks:
            // nothing open *on this cluster* is not nothing open anywhere. An
            // invisible region collapses both into a third rendering of a fourth
            // state.
            <p className="self-center px-1 text-xs text-muted">Nothing open.</p>
          )}
        </div>

        <FoldButton folded={state.folded} onFold={(folded) => dispatch({ type: 'fold', folded })} />
        <EndButton />
      </div>

      {/* The refusal line. Not a `window.confirm` and not a Dialog:
          confirm/alert/prompt block the page, and a blocked page cannot be
          driven - and a modal over a panel the operator is trying to tidy is the wrong
          shape. */}
      <p
        role="status"
        aria-live="polite"
        className="px-2 pb-1 text-xs text-warn empty:hidden"
      >
        {state.note}
      </p>

      {menuOpen && <ScopeMenu onClose={() => setMenuOpen(false)} />}

      {/* **Every pane, always mounted.** Only `hidden` moves. */}
      <div className={`relative min-h-0 flex-1 ${collapsed ? 'hidden' : ''}`}>
        {state.tabs.map((tab) => (
          <div
            key={tab.id}
            role="tabpanel"
            className={`absolute inset-0 ${tab.id === state.active && !collapsed ? 'flex' : 'hidden'}`}
          >
            {tab.kind === 'logs' ? <LogPane tab={tab} /> : <TerminalPane tab={tab} />}
          </div>
        ))}
      </div>
    </section>
  );
}

/**
 * The drag handle.
 *
 * **Pointer events and `setPointerCapture`**, not mouse events, so a trackpad
 * and a touchscreen both work - and so the drag survives the pointer leaving the
 * two-pixel grip, which it does immediately. And the keys, so a person without
 * a pointer can resize it too; a collapsed dock has no height to change, so its
 * grip is not a stop in the tab order.
 */
function Grip({
  height,
  onResize,
  disabled,
}: {
  height: number;
  onResize: (px: number) => void;
  disabled: boolean;
}): ReactNode {
  const dragging = useRef<{ y: number; height: number } | null>(null);

  const down = (event: PointerEvent<HTMLDivElement>): void => {
    if (disabled) return;
    const panel = event.currentTarget.parentElement;
    if (panel === null) return;
    dragging.current = { y: event.clientY, height: panel.getBoundingClientRect().height };
    event.currentTarget.setPointerCapture(event.pointerId);
  };

  const move = (event: PointerEvent<HTMLDivElement>): void => {
    const from = dragging.current;
    if (from === null) return;
    // Dragging up makes it taller, which is why the delta is subtracted.
    onResize(from.height - (event.clientY - from.y));
  };

  const up = (event: PointerEvent<HTMLDivElement>): void => {
    dragging.current = null;
    event.currentTarget.releasePointerCapture(event.pointerId);
  };

  // The drawer's keys (`shell/Drawer.tsx`), turned on their side: up is taller.
  // Every value goes through `onResize`, which clamps and stores.
  const key = (event: KeyboardEvent<HTMLDivElement>): void => {
    if (disabled) return;
    const step = event.shiftKey ? FINE : STEP;
    let next: number;
    if (event.key === 'ArrowUp') next = height + step;
    else if (event.key === 'ArrowDown') next = height - step;
    else if (event.key === 'End') next = Number.POSITIVE_INFINITY;
    else if (event.key === 'Home') next = DEFAULT_HEIGHT;
    else return;
    event.preventDefault();
    onResize(next);
  };

  return (
    <div
      role="separator"
      aria-orientation="horizontal"
      aria-label="Resize the panel"
      aria-controls="dock"
      aria-valuenow={height}
      aria-valuemin={MIN_HEIGHT}
      aria-valuemax={clampHeight(Number.POSITIVE_INFINITY, window.innerHeight)}
      tabIndex={disabled ? undefined : 0}
      title="Drag to resize the panel. Arrow keys resize; Shift for a finer step; End for the tallest; Home goes back to the default height."
      className={`h-0.5 shrink-0 bg-line ${disabled ? '' : `cursor-row-resize hover:bg-accent ${focusRingFlush}`}`}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onKeyDown={key}
    />
  );
}

/** The kinds whose pane is not built yet. Named rather than blank: a pane that
 *  draws nothing is indistinguishable from a session that failed to start, which
 *  is the distinction this whole surface is about. */

function TabButton({ tab, active }: { tab: Tab; active: boolean }): ReactNode {
  const { state, dispatch } = useDock();
  const everywhere = shownCluster(state) === '*';

  return (
    <span
      className={`flex shrink-0 items-center gap-1 rounded-sm border px-1.5 py-0.5 text-xs ${
        active ? 'border-accent bg-panel-2 text-fg' : 'border-line text-muted'
      } ${tab.live ? '' : 'opacity-55'}`}
    >
      <button
        type="button"
        role="tab"
        aria-selected={active}
        className={`min-h-6 max-w-56 truncate pointer-coarse:min-h-11 ${focusRingFlush}`}
        onClick={() => dispatch({ type: 'select', id: tab.id })}
      >
        {/* The assistant's tab wears the mark itself, in its own color, for the
            reason the drawer's button does: it is what says which tab this is
            without a word. The other four keep their typographic marks — a
            shell is `❯` in every console anybody has used, and inventing a
            glyph for it would be a shape to learn rather than one to
            recognize. */}
        {tab.kind === 'claude' ? (
          <Icon name="claude" className="mr-1 inline-block" />
        ) : tab.kind === 'debug' ? (
          // The bug the drawer's Debug control and its Start wear: a debug
          // session is a shell in a container this console added to the pod,
          // and `❯` beside it would read as the pod's own shell.
          <Icon name="debug" className="mr-1 inline-block" />
        ) : (
          <span aria-hidden="true" className="mr-1">{MARK[tab.kind] ?? '•'}</span>
        )}
        {/* In "all clusters" the tab names alone do not say which cluster a pod
            belongs to, so the name carries its cluster **there and only there**.
            A row of `api-7f9c4b8d6-x2kqp` tabs across four clusters is
            unreadable without it, and it only shows in a mode nobody tests. */}
        {everywhere && <span className="mr-1 text-muted">{shortCluster(tab.cluster, 12)}</span>}
        {middleTruncate(tab.label, 26)}
        {!tab.live && <span className="sr-only"> — {tab.dormant === true ? 'not reopened since the reload' : 'ended'}</span>}
      </button>
      <CloseButton
        label={`Close ${tab.label}`}
        size="inline"
        tone="bad"
        onClick={() => dispatch({ type: 'close', id: tab.id })}
      />
    </span>
  );
}

/** The mark is **passed rather than derived**, because a Claude session and a
 *  pod shell are the same kind of tab - a PTY on a WebSocket - and only the mark
 *  tells them apart in a row of them. */
const MARK: Readonly<Record<string, string>> = {
  terminal: '❯',
  exec: '❯',
  claude: '✦',
  nodeshell: '⌘',
  logs: '≡',
};

function ScopeButton({
  open, elsewhere, onToggle,
}: { open: boolean; elsewhere: number; onToggle: () => void }): ReactNode {
  const { state } = useDock();
  const shown = shownCluster(state);
  const label = shown === '*' ? 'All clusters' : shortCluster(shown ?? '', 16);

  return (
    <button
      type="button"
      aria-haspopup="menu"
      aria-expanded={open}
      className={`shrink-0 ${btnSecondary}`}
      // The badge is a per-window fact and the tooltip says so rather than
      // letting the number imply otherwise: this counts what this browser
      // window opened, not what the process is holding. A second window's
      // shells are not in it, and there is no route that would answer that.
      title={
        elsewhere === 0
          ? 'Which cluster the panel is showing'
          : `${elsewhere} session${elsewhere === 1 ? ' is' : 's are'} still running on other clusters, ` +
            'opened in this window. They are hidden, not closed — click to reach or close them.'
      }
      onClick={onToggle}
    >
      {label}
      {elsewhere > 0 && (
        <span className="rounded-full bg-accent px-1 text-2xs text-bg">+{elsewhere} elsewhere</span>
      )}
    </button>
  );
}

/**
 * The list behind the scope button: name, tally, reach, close.
 *
 * The only way to reach or end a session on a cluster the operator is not
 * looking at - which is what makes hiding one defensible at all. The close
 * really closes: it ends the socket, which ends the pump in `internal/terminal`,
 * which kills the shell and removes the session directory holding its
 * credentials. Nothing here is only visual.
 *
 * The list is in the flow of the panel rather than floated over it;
 * `relative z-20` is what keeps it above the
 * scrim, which sits at `z-10` over everything else on the page.
 */
function ScopeMenu({ onClose }: { onClose: () => void }): ReactNode {
  const { state, dispatch } = useDock();
  const rows = scopeRows(state);
  const shown = shownCluster(state);

  const pick = (scope: '*' | string | null): void => {
    dispatch({ type: 'scope', scope });
    onClose();
  };

  return (
    <>
      <MenuScrim onDismiss={onClose} />
      <ul
        role="menu"
        aria-label="Where the open panels are"
        className="relative z-20 mx-2 mb-1 rounded-sm border border-line bg-panel-2 text-xs"
      >
        <li role="none" className="border-b border-line">
          <button
            type="button"
            role="menuitem"
            aria-current={shown === '*'}
            className={`min-h-6 w-full px-2 py-1 text-left hover:bg-panel pointer-coarse:min-h-11 ${focusRingFlush}`}
            onClick={() => pick('*')}
          >
            All clusters <Tally live={total(rows, 'live')} ended={total(rows, 'ended')} />
          </button>
        </li>
        {state.scope !== null && state.scope !== '*' && (
          <li role="none" className="border-b border-line">
            <button
              type="button"
              role="menuitem"
              className={`min-h-6 w-full px-2 py-1 text-left hover:bg-panel pointer-coarse:min-h-11 ${focusRingFlush}`}
              onClick={() => pick(null)}
            >
              Follow the page
            </button>
          </li>
        )}
        {rows.map((row) => (
          <li key={row.cluster} role="none" className="flex items-center">
            <button
              type="button"
              role="menuitem"
              aria-current={shown === row.cluster}
              className={`min-h-6 min-w-0 flex-1 truncate px-2 py-1 text-left hover:bg-panel pointer-coarse:min-h-11 ${focusRingFlush}`}
              onClick={() => pick(row.cluster)}
            >
              {shortCluster(row.cluster, 28)} <Tally live={row.live} ended={row.ended} />
            </button>
            <CloseButton
              label={`Close the ${row.live + row.ended} ${
                row.live + row.ended === 1 ? 'panel' : 'panels'
              } open on ${row.cluster}`}
              size="inline"
              tone="bad"
              className="mx-1"
              onClick={() => dispatch({ type: 'closeCluster', cluster: row.cluster })}
            />
          </li>
        ))}
      </ul>
    </>
  );
}

function total(rows: readonly { live: number; ended: number }[], key: 'live' | 'ended'): number {
  return rows.reduce((sum, row) => sum + row[key], 0);
}

/** Built as elements rather than as one string, so the ended half can be styled
 *  down and omitted at zero. */
function Tally({ live, ended }: { live: number; ended: number }): ReactNode {
  return (
    <span className="text-muted">
      {live}
      {ended > 0 && <span className="opacity-60"> / +{ended} ended</span>}
    </span>
  );
}

/**
 * Fold, and why it is a second button rather than a second meaning.
 *
 * *"Get out of my way"* and *"I am finished with these"* are two intentions. An
 * operator tidying the screen must not lose every shell on every cluster: that
 * is the worst possible thing for that gesture to do, because the sessions are
 * the expensive part and the panel is free.
 *
 * Folding keeps every socket, every PTY and every line of scrollback, and it
 * keeps the header deliberately - a panel that vanished while shells ran behind
 * it is the outcome the partition forbids.
 */
function FoldButton({ folded, onFold }: { folded: boolean; onFold: (folded: boolean) => void }): ReactNode {
  return (
    <button
      type="button"
      aria-label={folded ? 'Unfold the panel' : 'Fold the panel, keeping every session running'}
      aria-expanded={!folded}
      className={`shrink-0 ${btnSecondary}`}
      onClick={() => onFold(!folded)}
    >
      {folded ? '▴' : '▾'}
    </button>
  );
}

/** The × asks once, and names the fold as the way out. */
function EndButton(): ReactNode {
  const { state, dispatch } = useDock();
  const armed = state.endArmedAt !== null;

  const press = useCallback(() => {
    if (endIsConfirmed(state, Date.now())) {
      dispatch({ type: 'closeAll' });
      return;
    }
    const warning = endWarning(state);
    dispatch({ type: 'armEnd', at: Date.now() });
    if (warning !== null) dispatch({ type: 'note', note: warning });
  }, [state, dispatch]);

  return (
    <CloseButton
      label={armed ? 'Press again to end every session' : 'Close the panel and end every session'}
      // The panel's ×, the drawer's and a dialog's size: one close box for a
      // panel, wherever the panel is. Armed, the red box says the next press
      // ends them.
      tone="bad"
      armed={armed}
      className="shrink-0"
      onClick={press}
    />
  );
}
