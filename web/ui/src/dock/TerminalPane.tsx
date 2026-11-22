import type { FitAddon } from '@xterm/addon-fit';
import type { Terminal } from '@xterm/xterm';
import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';

import { openTerminalSocket, type Channel, type Reason, type State, type TerminalSocket } from '../api';
import { btnPrimary, btnSecondary } from '../components/buttons';
import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { useLiveHandle } from '../live/useLiveHandle';
import { isChunkLoadFailure, reloadForNewBuild } from '../router/chunkReload';
import { useDock } from './DockContext';
import {
  browserClipboard,
  clipboardHint,
  guardPaste,
  onTerminalContextMenu,
  onTerminalKey,
  type Gate,
} from './terminalClipboard';
import type { Tab } from './tabs';

/**
 * The face of every terminal in this console, written out rather than referred
 * to.
 *
 * It has to be a literal: xterm measures a cell on a canvas, and a canvas
 * resolves no CSS custom property — see the comment where it is passed. So this
 * is the one place in the front end that repeats what `app/theme.css` already
 * says, and `TerminalPane.test.tsx` reads that file and asserts the two agree
 * rather than trusting anybody to remember.
 *
 * Cascadia Mono leads on the platforms that have it — it is what Windows
 * Terminal and VS Code draw a console in — and everything after it is
 * `--font-mono` unchanged, so a machine without it lands exactly where it
 * landed before.
 */
export const TERMINAL_FONT =
  '"Cascadia Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace';

/**
 * A shell in the dock: a cluster shell, a pod shell, a node shell or the
 * assistant, all four over one component.
 *
 * Deliberately one and not four. `internal/terminal/exec.go` says the same thing
 * from the other side - *"a shell inside a pod, over the same socket protocol as
 * the shell on the cluster. Deliberately the same: the UI mounts one terminal
 * component and points it at one URL or the other."* Four components would drift,
 * and the mark on the tab is what tells a Claude session from a pod shell in a
 * row of them.
 *
 * ## The trap this is written around
 *
 * React 19's StrictMode double-invokes effects in development: mount, effect,
 * cleanup, effect again. A terminal opened in a bare `useEffect` therefore opens
 * **two** sockets and two pseudo-terminals, and `internal/terminal/limit.go` caps
 * live sessions at sixteen for the whole process - so what it looks like is a
 * session limit reached at half the sessions, in development only, which is where
 * it is least expected and hardest to attribute.
 *
 * `live/handles.ts` is the fix and this is its first caller after the log pane:
 * a module-level registry so a handle outlives the component, plus a **deferred**
 * close, because refcounting alone does not help - StrictMode's cleanup takes the
 * count to zero *between* the two mounts.
 *
 * **The key contains `tab.seq`.** Without it two shells on one pod are one shell:
 * the deduplication finds the first tab's handle and defeats the counter that
 * exists to keep them apart. `tabs.ts` says the same thing about the tab id.
 *
 * ## Why xterm is constructed with an explicit theme
 *
 * Because it colors its own output from that object and inherits no CSS of
 * ours, so a terminal with no theme comes up black-on-black against a dark
 * panel. The values are read off the computed style of the host element, so the
 * sixteen ANSI custom properties in `theme.css` remain the single source and
 * this is not a second palette.
 *
 * ## Why the emulator is imported inside `open`, and the handle holds a promise
 *
 * `@xterm/xterm` is a large share of the bundle, and in the entry chunk the
 * browser would download and parse it before it could draw the sign-in form,
 * on the one page reached with a cold cache. Nothing but this pane
 * constructs a terminal, and the handle's `open` is the one place it does, so
 * that is where `./terminal` is asked for, dynamically, and the sign-in view
 * carries no emulator at all. **Only type imports of xterm are allowed at the
 * top of this file**: a value import would pull the chunk back into the entry,
 * silently, and the only symptom is the build's size warning returning.
 *
 * `acquireHandle` is synchronous - it has to be, so that StrictMode's second
 * mount finds the first handle in the same tick - so the handle's value is a
 * *promise* of the session rather than the session. `attach` waits on it and
 * `dispose` remembers whether it was ever asked to close, so a pane released
 * while the chunk is still on the wire closes what arrives instead of leaking
 * it. The socket is opened after the import, not before: bytes delivered to a
 * terminal that does not exist yet are bytes lost.
 *
 * A chunk that does not arrive is a tab left open across an upgrade, the same
 * case `router/chunkReload.ts` handles for a page - but this import is not a
 * route and `RouteError` never sees it, so the pane goes through the same
 * once-only guard itself, and where the reload has been spent it says what
 * happened rather than drawing an empty pane over a session that never opened.
 */
export function TerminalPane({ tab }: { tab: Tab }): ReactNode {
  // Two components and not one with a condition inside, because the live one
  // is all hooks and a hook cannot be skipped: a dormant tab must not reach
  // `useLiveHandle`, which opens the session by being called.
  return tab.dormant === true ? <Dormant tab={tab} /> : <LiveTerminal tab={tab} />;
}

/**
 * A tab a reload put back, offering a session rather than being one. The
 * sentence says what happened to the old
 * one, because "nothing is running here" is the fact the operator needs before
 * deciding whether to ask for a new one; the button is the asking.
 */
function Dormant({ tab }: { tab: Tab }): ReactNode {
  const { dispatch } = useDock();
  const what =
    tab.kind === 'claude'
      ? 'This Claude session'
      : tab.kind === 'exec'
        ? 'This shell in the pod'
        : 'This shell';
  const after =
    tab.kind === 'claude'
      ? 'A new session tries to resume the conversation, which may not have been kept.'
      : 'A new session starts from a fresh prompt.';
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col items-start gap-2 px-3 py-2 text-xs">
      <p className="text-muted">
        {what} was open before the page was reloaded, and its session ended with the page. {after}
      </p>
      <button
        type="button"
        className={btnSecondary}
        onClick={() => dispatch({ type: 'wake', id: tab.id })}
      >
        Open a new session
      </button>
    </div>
  );
}

function LiveTerminal({ tab }: { tab: Tab }): ReactNode {
  const { dispatch } = useDock();
  const host = useRef<HTMLDivElement>(null);
  const [note, setNote] = useState<{ text: string; bad: boolean } | null>(null);
  // What a copy or a paste had to say - a refusal, or "paste by key" over plain
  // HTTP. Its own line and not `note`, which says how the session ended and
  // must not be overwritten by a remark about the clipboard.
  const [hint, setHint] = useState<string | null>(null);
  // A paste holding a newline, held until the operator says (`guardPaste`):
  // each line would run as it landed. The pane's own line asks, as the dock's
  // × does, rather than a dialog over the console; refusing sends nothing.
  //
  // A ref beside the state, because the gate is asked synchronously - a second
  // paste in the same tick must see the first one waiting - and because the
  // emulator's key handler, set once per session, reads it.
  const [held, setHeld] = useState<Held | null>(null);
  const heldNow = useRef<Held | null>(null);
  const shown = useRef<Terminal | null>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const confirm = useRef<HTMLButtonElement>(null);
  const promptId = useId();
  const hold = useCallback((next: Held | null) => {
    heldNow.current = next;
    setHeld(next);
  }, []);
  const gate = useMemo<Gate>(
    () => ({ holding: () => heldNow.current !== null, ask: (lines, send) => hold({ lines, send }) }),
    [hold],
  );
  // Either answer gives the terminal its keys back; the button that had the
  // focus is gone with the prompt, and focus would otherwise fall to the page.
  const answer = useCallback((paste: boolean) => {
    const waiting = heldNow.current;
    hold(null);
    if (paste) waiting?.send();
    shown.current?.focus();
  }, [hold]);
  // Cancel, the safe answer, takes the focus the moment the prompt appears, so
  // the keys the operator reaches for go to the prompt and not to the shell.
  useEffect(() => {
    if (held !== null) cancel.current?.focus();
  }, [held]);
  useEscape(KeyLayer.Confirm, () => answer(false), held !== null);
  useEffect(() => {
    if (hint === null) return undefined;
    const timer = setTimeout(() => setHint(null), 6_000);
    return () => clearTimeout(timer);
  }, [hint]);
  // "Copied", after a right-click copy the clipboard took.
  //
  // **Emptied first and written a moment later, every time.** A live region is
  // announced when its text changes, so a second copy inside the window that
  // only restarted the timer left "Copied" as it was and said nothing to a
  // screen reader. The gap is the change it announces.
  const [copied, setCopied] = useState(false);
  const copiedTimers = useRef<{ show?: ReturnType<typeof setTimeout>; hide?: ReturnType<typeof setTimeout> }>({});
  useEffect(() => {
    const timers = copiedTimers.current;
    return () => {
      clearTimeout(timers.show);
      clearTimeout(timers.hide);
    };
  }, []);
  const sayCopied = useCallback(() => {
    const timers = copiedTimers.current;
    clearTimeout(timers.show);
    clearTimeout(timers.hide);
    setCopied(false);
    timers.show = setTimeout(() => {
      setCopied(true);
      timers.hide = setTimeout(() => setCopied(false), COPIED_MS);
    }, COPIED_GAP_MS);
  }, []);

  const channel = channelFor(tab.kind);
  // Read once per pane: the platform does not change under it.
  const [footer] = useState(clipboardHint);

  // Read through a ref so neither is in the handle's dependency list: both are
  // new functions on some renders, and depending on them would close and reopen
  // the session - taking a second of the server's sixteen slots each time. The
  // key is the dependency, because the key is the identity.
  const onState = useCallback((state: State, reason: Reason | undefined) => {
    // The server's own sentence, drawn rather than paraphrased. This is the
    // whole reason the framed wire exists: as a line inside the terminal
    // output, a client could not tell the session cap from the read-only
    // fence.
    setNote({ text: reason?.message ?? `the session stopped: ${state}`, bad: true });
    // A paste waiting for an answer has nowhere to go any more.
    hold(null);
    dispatch({ type: 'ended', id: tab.id });
  }, [dispatch, hold, tab.id]);

  const onExit = useCallback((code: number | undefined) => {
    // No code is the ordinary case and is said as such. The pseudo-terminal seam
    // exposes no exit status on either platform, and "exited 0" printed from an
    // absent number would be inventing the one fact the operator came for.
    setNote({
      text: code === undefined ? 'The session ended.' : `The session ended (${code}).`,
      bad: false,
    });
    hold(null);
    dispatch({ type: 'ended', id: tab.id });
  }, [dispatch, hold, tab.id]);

  const onClosed = useCallback(() => {
    // The browser's own close carries nothing - no status, no body, not even
    // whether the server was reached - so the sentence names what is knowable and
    // claims no cause. A refusal would have arrived as a frame or as an HTTP
    // status before the upgrade; this is neither.
    setNote((was) => was ?? { text: 'The connection to the session went away.', bad: true });
    hold(null);
    dispatch({ type: 'ended', id: tab.id });
  }, [dispatch, hold, tab.id]);

  const latest = useRef({ onState, onExit, onClosed });
  useEffect(() => {
    latest.current = { onState, onExit, onClosed };
  });

  const key = `term:${tab.seq}:${channel}:${tab.cluster}/${tab.namespace ?? ''}/${tab.name ?? ''}:${tab.container ?? ''}:${tab.objectKind ?? ''}`;

  /** Put an opened session on this pane's host element. The body of the old
   *  synchronous `attach`, unchanged; only when it runs has moved. */
  const show = (session: Session): (() => void) | undefined => {
      const { term, fit, socket } = session;
      const element = host.current;
      if (element === null) return undefined;
      term.open(element);
      shown.current = term;
      applyTheme(term, element);
      // **A session just opened takes the keyboard.** It was asked for by a
      // button - Shell, Open a shell, Open a new session - and the focus left on
      // that button sent the first keys nowhere. Once per session: a tab
      // brought back by a re-render or a route does not take the focus from
      // whatever the operator moved to since.
      if (session.shown !== true) {
        session.shown = true;
        term.focus();
      }

      // **Never `requestAnimationFrame` and never a bare call.** A pane that has
      // just been shown has not been laid out when this runs, so `fit` measures
      // zero and the terminal comes up one column wide - a session that is
      // running and looks broken. A macrotask is after layout; an animation
      // frame is before paint and, in a background tab, never.
      //
      // And a macrotask is long before the socket is open - a WebSocket upgrade
      // plus a Kubernetes exec take far longer - so this first size lands on a
      // `CONNECTING` socket. `termsocket.ts` holds it and sends it on `open`,
      // ahead of any keystroke; dropped, the shell would run at 80 columns
      // until the grip was dragged.
      const settle = setTimeout(() => {
        fit.fit();
        socket.resize(term.cols, term.rows);
      }, 0);

      // ResizeObserver rather than a window listener, because the panel is
      // resized by its own grip and by folding, neither of which resizes the
      // window. The guard against zero is the same one the resize frame carries:
      // a hidden pane measures 0x0, and applying that gives a 0x0 terminal with
      // everything drawn while it was hidden lost.
      const observer = new ResizeObserver(() => {
        if (element.clientHeight === 0 || element.clientWidth === 0) return;
        fit.fit();
        socket.resize(term.cols, term.rows);
      });
      observer.observe(element);

      // Right-click copies the selection, or pastes when there is none
      // (`terminalClipboard.ts` has the rules). On
      // the host and not on xterm's canvas, so it is removed with the rest of
      // what this attach put here; the session outlives it.
      const onMenu = (event: MouseEvent): void => {
        event.preventDefault();
        void onTerminalContextMenu(term, browserClipboard, setHint, gate).then((done) => {
          if (done) sayCopied();
          if (heldNow.current === null) term.focus();
        });
      };
      element.addEventListener('contextmenu', onMenu);

      // Ctrl+Shift+V, and any other paste the browser makes: caught on the way
      // down to xterm's textarea, so the text goes through the same guard as a
      // right-click instead of into xterm's `paste()` unfiltered.
      const onPaste = (event: ClipboardEvent): void => {
        event.preventDefault();
        event.stopPropagation();
        guardPaste(term, event.clipboardData?.getData('text/plain') ?? '', gate, setHint);
      };
      element.addEventListener('paste', onPaste, true);

      return () => {
        clearTimeout(settle);
        observer.disconnect();
        element.removeEventListener('contextmenu', onMenu);
        element.removeEventListener('paste', onPaste, true);
        // Detached, not disposed. The session outlives this component - a hidden
        // tab keeps running, and that is the promise the whole partition rests
        // on. `dispose` belongs to the handle and runs only when nobody holds it.
        element.replaceChildren();
        if (shown.current === term) shown.current = null;
      };
  };

  useLiveHandle<Promise<Session>>(
    key,
    () => {
      let disposed = false;
      let live: Session | undefined;

      const session = (async (): Promise<Session> => {
        const { Terminal, FitAddon } = await terminalChunk.load();
        // Released while the chunk was on the wire - the operator closed the
        // tab, or the grace period ran out. Nothing is opened for nobody.
        if (disposed) throw new Error('the terminal was closed before its code arrived');

        const term = new Terminal({
          // **A real stack, not `var(--font-mono)`, and that is not a
          // preference.** xterm hands this string to a canvas as
          // `ctx.font = '12.5px ' + fontFamily` to measure a cell, and **a
          // canvas does not resolve CSS custom properties** - the declaration is
          // invalid, the context keeps its default 10px sans-serif, and every
          // column width in the terminal is measured against a font it is not
          // drawing in.
          //
          // It is the same ladder `--font-mono` names, with Cascadia Mono ahead
          // of Consolas: it ships with Windows Terminal and VS Code, it is the
          // one on this platform drawn for a console, and where it is absent the
          // line falls through to the same faces. `terminalFontMatchesTheme`
          // keeps the two spellings in step, because two lists that must agree
          // and cannot import each other are two lists that drift.
          fontFamily: TERMINAL_FONT,
          fontSize: 12.5,
          cursorBlink: true,
          scrollback: 5_000,
          allowProposedApi: true,
          // **Off on every platform, and that is what makes the footer true.**
          // xterm defaults it to on for a Mac: its own `contextmenu` listener,
          // on `.xterm` inside the host, selects the word under the pointer
          // before the pane's `onMenu` runs, so a right-click there would
          // always find a selection, copy a word and never paste. The pane's right-click is "copy the selection,
          // or paste when there is none", on a Mac as anywhere.
          rightClickSelectsWord: false,
        });
        const fit = new FitAddon();
        term.loadAddon(fit);
        const socket = openTerminalSocket(
          {
            channel,
            cluster: tab.cluster,
            ...(tab.namespace === undefined ? {} : { namespace: tab.namespace }),
            ...(tab.name === undefined ? {} : { pod: tab.name }),
            ...(tab.container === undefined ? {} : { container: tab.container }),
            // The assistant's framing. `pod` above carries the same name for
            // `exec`, and the two channels read different parameters out of the
            // same tab - which is why both are sent and `channelPath` decides
            // which reach the wire.
            ...(tab.objectKind === undefined ? {} : { kind: tab.objectKind }),
            ...(tab.name === undefined ? {} : { name: tab.name }),
            ...(tab.token === undefined ? {} : { token: tab.token }),
            ...(tab.session === undefined ? {} : { session: tab.session }),
          },
          {
            onData: (bytes) => term.write(bytes),
            onState: (state, reason) => latest.current.onState(state, reason),
            onExit: (code) => latest.current.onExit(code),
            onClosed: () => latest.current.onClosed(),
          },
        );
        term.onData((text) => socket.send(text));
        // Ctrl+Shift+C and Ctrl+Shift+V are the clipboard's; Ctrl+C stays the
        // shell's interrupt. Once per emulator, because xterm keeps one handler
        // and the emulator outlives the pane that shows it.
        //
        // While a paste waits for an answer no key reaches the shell: Escape
        // cancels it, and any other key sends the focus back to the prompt.
        term.attachCustomKeyEventHandler((event) => {
          if (heldNow.current !== null) {
            event.preventDefault();
            if (event.type === 'keydown') {
              if (event.key === 'Escape') answer(false);
              else cancel.current?.focus();
            }
            return false;
          }
          return onTerminalKey(term, event, browserClipboard, setHint);
        });
        live = { term, fit, socket };
        return live;
      })();
      // Handled here once, so a handle nobody attaches to - disposed inside its
      // grace period, say - is not an unhandled rejection. Every attach handles
      // it again for its own purposes.
      session.catch(() => undefined);

      return {
        value: session,
        dispose: () => {
          disposed = true;
          if (live !== undefined) {
            live.socket.close();
            live.term.dispose();
          }
        },
      };
    },
    (session) => {
      let cancelled = false;
      let detach: (() => void) | undefined;
      session.then(
        (live) => {
          if (cancelled) return;
          detach = show(live);
        },
        (error: unknown) => {
          if (cancelled) return;
          if (isChunkLoadFailure(error) && reloadForNewBuild()) return;
          // Two reasons to be here and one sentence for both, because the
          // message is the only thing that tells them apart: the chunk is
          // gone and the one reload is spent (a build on the server lacks a
          // file), or the import failed for a reason that is not a chunk at
          // all. Either way the socket was never opened, so the tab is over.
          setNote({
            text: `The terminal could not be loaded: ${error instanceof Error ? error.message : String(error)}`,
            bad: true,
          });
          dispatch({ type: 'ended', id: tab.id });
        },
      );
      return () => {
        cancelled = true;
        detach?.();
      };
    },
  );


  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div ref={host} className="min-h-0 flex-1 px-1" />
      {note !== null && (
        <p className={`px-2 py-1 text-xs ${note.bad ? 'text-warn' : 'text-muted'}`} role="status">
          — {note.text} —
        </p>
      )}
      {held !== null && (
        // A dialog in the pane's own line, not a live region holding buttons:
        // it takes the focus, Tab stays between its two answers, and Escape is
        // the same as Cancel.
        <div
          role="alertdialog"
          aria-labelledby={promptId}
          className="flex flex-wrap items-center gap-2 px-2 py-1 text-xs"
          onKeyDown={(event) => {
            if (event.key === 'Escape') {
              event.preventDefault();
              answer(false);
            } else if (event.key === 'Tab') {
              event.preventDefault();
              (document.activeElement === cancel.current ? confirm : cancel).current?.focus();
            }
          }}
        >
          <span id={promptId} className="text-warn">
            Paste {lineCount(held.lines)}? Each line runs in the shell as it arrives.
          </span>
          <button
            ref={confirm}
            type="button"
            className={btnPrimary}
            onClick={() => answer(true)}
          >
            Paste {lineCount(held.lines)}
          </button>
          <button
            ref={cancel}
            type="button"
            className={btnSecondary}
            onClick={() => answer(false)}
          >
            Cancel
          </button>
        </div>
      )}
      {hint !== null && (
        <p className="px-2 py-1 text-xs text-muted" role="status">
          {hint}
        </p>
      )}
      {/* The footer. The only right-click in the console is this
          one, and nothing on screen said what it does: so the gestures are
          written down for as long as the pane is open, in the keys that work
          on this platform (`clipboardHint`). It wraps rather than scrolls in
          a narrow dock.

          "Copied" sits in a live region that is always in the document, so
          the polite announcement is made: a region that appears with its
          text already in it is one a screen reader may never read. */}
      <p className="flex flex-wrap items-baseline gap-x-2 border-t border-line px-2 py-0.5 text-xs text-muted">
        <span>{footer}</span>
        <span role="status" className="text-ok">
          {copied ? 'Copied' : ''}
        </span>
      </p>
    </div>
  );
}

/** How long "Copied" stays: long enough to be read after the click, short
 *  enough not to describe the next one. */
const COPIED_MS = 1_500;
/** How long the region stays empty before "Copied" is written into it: one
 *  change a screen reader announces, too short for a sighted reader to see as
 *  a blink. */
const COPIED_GAP_MS = 100;

/** A paste waiting for the operator's answer. */
interface Held {
  readonly lines: number;
  readonly send: () => void;
}

function lineCount(lines: number): string {
  return lines === 1 ? '1 line' : `${lines} lines`;
}

/** What one live handle holds once the chunk has arrived: the emulator, the
 *  addon that sizes it and the socket behind it. */
interface Session {
  term: Terminal;
  fit: FitAddon;
  socket: TerminalSocket;
  /** Whether the session has been put on screen once: its first showing takes
   *  the keyboard, a later one - a tab brought back - does not. */
  shown?: boolean;
}

/** The shape of `./terminal`, spelled from the type imports at the top so that
 *  no `typeof import()` - which the lint fences forbid - is needed. */
interface TerminalModule {
  Terminal: typeof Terminal;
  FitAddon: typeof FitAddon;
}

/**
 * How the emulator's chunk is asked for.
 *
 * `load` is what `open` calls; `real` is the one dynamic import, and the only
 * thing that ever sets `load` to anything else is `TerminalPane.test.tsx`,
 * making the import fail the way a browser fails a chunk that no longer exists,
 * or handing back an emulator that already holds a selection, which jsdom gives
 * no pointer to make (the "Copied" test).
 * The console never touches it. A seam rather than a module mock because this
 * suite mocks no modules, and because the thing under test - what the pane does
 * when the code does not come - is a property of the pane, not of the bundler.
 */
export const terminalChunk: {
  load: () => Promise<TerminalModule>;
  readonly real: () => Promise<TerminalModule>;
} = {
  load: () => import('./terminal'),
  real: () => import('./terminal'),
};

/** Which channel a tab of this kind opens. `claude` is the tab's name for the
 *  assistant, kept because `tabs.ts` already spells it that way and renaming a
 *  tab kind is a change to the store rather than to this pane. */
function channelFor(kind: Tab['kind']): Channel {
  switch (kind) {
    case 'exec':
      return 'exec';
    case 'nodeshell':
      return 'nodeshell';
    case 'claude':
      return 'assistant';
    default:
      return 'terminal';
  }
}

/**
 * The theme, read off the host element's computed style.
 *
 * xterm needs an object of color **values** and inherits no stylesheet of
 * ours, so a terminal built without one comes up black-on-black against a dark
 * panel. So this is the
 * one place in the SPA where a color crosses as a value rather than as a class.
 *
 * **There are no fallbacks, deliberately.** The obvious shape is to read each
 * property with a literal beside it, and it is wrong twice: sixteen literals are
 * a second palette, which colors living in `theme.css` exist to prevent, and
 * `pnpm run check:styles` refuses a literal color outside the theme -
 * correctly, since a fallback that is never exercised is a value nobody would
 * notice drifting. A property that does not resolve is omitted here and xterm
 * keeps its own default: a visible fault in one color rather than a silent
 * second source for sixteen.
 */
function applyTheme(term: Terminal, host: HTMLElement): void {
  const style = getComputedStyle(host);
  const theme: Record<string, string> = {};
  for (const [key, property] of Object.entries(THEME_KEYS)) {
    const value = style.getPropertyValue(property).trim();
    if (value !== '') theme[key] = value;
  }
  term.options.theme = theme;
}

/**
 * Which custom property fills which of xterm's theme keys.
 *
 * The sixteen ANSI ones are `theme.css`'s, and the four surface ones are the panel this pane sits in - a
 * terminal whose background is not the panel's is a rectangle, not a pane.
 */
const THEME_KEYS: Readonly<Record<string, string>> = {
  background: '--color-panel',
  foreground: '--color-fg',
  cursor: '--color-fg',
  selectionBackground: '--color-line',
  black: '--color-ansi-black',
  red: '--color-ansi-red',
  green: '--color-ansi-green',
  yellow: '--color-ansi-yellow',
  blue: '--color-ansi-blue',
  magenta: '--color-ansi-magenta',
  cyan: '--color-ansi-cyan',
  white: '--color-ansi-white',
  brightBlack: '--color-ansi-bright-black',
  brightRed: '--color-ansi-bright-red',
  brightGreen: '--color-ansi-bright-green',
  brightYellow: '--color-ansi-bright-yellow',
  brightBlue: '--color-ansi-bright-blue',
  brightMagenta: '--color-ansi-bright-magenta',
  brightCyan: '--color-ansi-bright-cyan',
  brightWhite: '--color-ansi-bright-white',
};
