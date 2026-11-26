import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useParams } from 'react-router';

import {
  dockReducer,
  emptyDock,
  findLogsTab,
  parseStored,
  room,
  storable,
  type DockAction,
  type DockState,
  type Scope,
  type StoredDock,
  type TabKind,
} from './tabs';

/**
 * The dock's state, and the three things that are not in the reducer.
 *
 * The reducer (`./tabs.ts`) is pure and holds the tab list. What is here is what
 * a pure reducer cannot be: the browser's storage, the router, and the panel's
 * height.
 *
 * ## Following the page is one subscription
 *
 * A link, the back button and a history restore all reach the dock through
 * `useParams().cluster`, which is current by construction, so the dock never
 * keeps showing the cluster you left.
 *
 * The one property that has to hold: a route that names **no**
 * cluster changes nothing. `/health` and `/settings` are not about a cluster and
 * arriving on one must not empty the dock - the operator checking why a cluster
 * is unreachable has not stopped working on it. `params.cluster ?? ''` reaching
 * the reducer is exactly that bug, and the reducer refuses an empty cluster for
 * this reason.
 *
 * ## What is stored, and the two things that are deliberately not
 *
 * | | Stored | Why |
 * |---|---|---|
 * | height | `kubetower.dock.height` | a panel of a size they chose, still that size tomorrow |
 * | all-clusters | `kubetower.dock.scope`, and only the value `*` | a **mode**: the operator asked for everything at once |
 * | the tabs | `kubetower.dock.tabs`, in **`sessionStorage`** — each tab's *address*, a Claude tab's conversation id, and which was in front; never a plan token | the layout they had, back after a reload, each tab reopened through `open` so the ceilings still apply. Not the sessions behind them: those died with the socket, and nothing keeps them alive. A shell or Claude tab comes back *dormant*, offering a new session rather than opening one, and a node shell is not reopened at all; the note says so. Per browser tab, see below |
 * | a pin on one cluster | **no** | a **visit**. Remembering it would make the dock silently disagree with the page for the rest of the session |
 * | folded | **no** | see `DockState.folded`. A SPA has no full page load to reset it, so a stored fold would be permanent |
 *
 * Three flags and a list, two policies, one reason, and both omissions are easy
 * to "fix" into a bug.
 *
 * ## The tabs belong to the browser tab that opened them
 *
 * In `localStorage`, which every tab of the origin shares, a second browser tab
 * would read the first one's shells and open them as its own, and every reload
 * would open each shell again, each holding one of the server's sixteen slots.
 * Two choices, and each closes one half:
 *
 * - **`sessionStorage`**, which a reload keeps and another tab never sees. A
 *   second tab starts with an empty dock and opens nothing. The cost, accepted:
 *   closing the tab or the desktop window forgets the layout. The same key is
 *   removed from `localStorage` on load, so a value left there by an older
 *   build is not kept for ever, read by nobody.
 * - **A restored pseudo-terminal is dormant** (`Tab.dormant`): it is drawn with
 *   an offer to open a new session, and nothing is opened until the operator
 *   presses it. A reload therefore opens no session at all - the shell it had
 *   is gone either way, and nothing reattaches to it.
 *
 * Height and the all-clusters mode stay in `localStorage`: they are preferences
 * and cost nothing on the server.
 */

const HEIGHT_KEY = 'kubetower.dock.height';
const SCOPE_KEY = 'kubetower.dock.scope';
const TABS_KEY = 'kubetower.dock.tabs';

export const DEFAULT_HEIGHT = 300;
/** The panel may not be shorter than this, nor taller than the window less this,
 *  so there is always a page above it and always a tab strip in it. */
export const MIN_HEIGHT = 120;
export const EDGE = 120;

/** What a caller has to say to open a log tab. The channel and the container
 *  default are the server's; nothing here builds either. */
export interface LogsRequest {
  readonly cluster: string;
  readonly kind: string;
  readonly namespace: string;
  readonly name: string;
  readonly container?: string;
}

export interface DockApi {
  readonly state: DockState;
  readonly dispatch: (action: DockAction) => void;
  readonly height: number;
  readonly setHeight: (px: number) => void;
  /** Open a log panel on one object, **or bring the open one to the front** -
   *  a panel on this object already up is focused and unfolded, and that counts
   *  as `true`, because the operator asked to see the logs and can now see them.
   *
   *  `false` means the ceiling refused a panel that did not yet exist, and the
   *  refusal is already the panel's note - a caller that has to compose its own
   *  would be a second place the number is written. */
  readonly openLogs: (request: LogsRequest) => boolean;
  /** Open a shell: a cluster shell, a pod shell, a node shell or the assistant.
   *  One opener rather than four, because they are one kind of thing - a
   *  pseudo-terminal on a WebSocket - and only the mark on the tab tells them
   *  apart in a row of them. Returns false when the ceiling refused it, having
   *  said so in the note. */
  readonly openShell: (request: ShellRequest) => boolean;
}

export interface ShellRequest {
  readonly kind: Exclude<TabKind, 'logs'>;
  readonly cluster: string;
  readonly label: string;
  readonly namespace?: string;
  readonly name?: string;
  readonly container?: string;
  /** The object's **Kind** - "Pod", not "pods" - on an assistant session. Its
   *  framing, and what keeps two sessions on two pods from mixing their
   *  context. See `tabs.ts`. */
  readonly objectKind?: string;
  /** The plan token, on a node shell. Without it the upgrade is refused with 403
   *  and no socket is opened - which is the point of the pair. */
  readonly token?: string;
  /** A Claude tab's conversation, when it already has one - a restore hands
   *  the stored id back through here. Omitted, a Claude tab draws a new one. */
  readonly session?: string;
  /** On a debug session: the image, and the container whose processes it
   *  joins - absent for none. See `Tab.image`. */
  readonly image?: string;
  readonly target?: string;
}

const DockContext = createContext<DockApi | null>(null);

function readStoredHeight(): number {
  try {
    const raw = localStorage.getItem(HEIGHT_KEY);
    if (raw === null) return DEFAULT_HEIGHT;
    const value = Number.parseInt(raw, 10);
    return Number.isFinite(value) ? value : DEFAULT_HEIGHT;
  } catch {
    // Storage can be unavailable - a private window, a blocked origin. A dock at
    // its default height is a working dock; throwing here would be a console
    // that will not start.
    return DEFAULT_HEIGHT;
  }
}

/**
 * The tabs the last page load had, or nothing. Read **once**, in the provider's
 * first render, and before anything writes: the effect that keeps the key
 * current runs on mount too, with an empty dock, and a read that came after it
 * would find the empty dock it had just written.
 */
function readStoredTabs(): StoredDock | null {
  try {
    const raw = sessionStorage.getItem(TABS_KEY);
    if (raw === null) return null;
    return parseStored(JSON.parse(raw) as unknown);
  } catch {
    return null;
  }
}

function readStoredScope(): Scope {
  try {
    // **One key with exactly one legal value.** Anything else in it is a pin
    // somebody wrote by hand or an older build; it is ignored rather than
    // honored, because honoring it is the "dock stuck on yesterday's cluster"
    // failure this key exists to avoid.
    return localStorage.getItem(SCOPE_KEY) === '*' ? '*' : null;
  } catch {
    return null;
  }
}

export function clampHeight(px: number, windowHeight: number): number {
  return Math.max(MIN_HEIGHT, Math.min(px, Math.max(MIN_HEIGHT, windowHeight - EDGE)));
}

export function DockProvider({ children }: { children: ReactNode }): ReactNode {
  const [state, dispatch] = useReducer(dockReducer, undefined, () => emptyDock(readStoredScope()));
  const [stored] = useState(readStoredTabs);
  const [height, setHeightState] = useState(readStoredHeight);
  const { cluster } = useParams();

  // Following the page's cluster. `followPage`
  // returns the state unchanged when the cluster has not moved, which it has to:
  // this runs on every render of every route.
  useEffect(() => {
    if (cluster !== undefined && cluster !== '') {
      dispatch({ type: 'followPage', cluster });
    }
  }, [cluster]);

  // The reload's tabs, put back once. **After `followPage` above**, in
  // declaration order, so the reducer sees which cluster the page is about and
  // keeps following it, pinning only where the page names none (`restore` in
  // `tabs.ts`); **before the write below**, so what
  // is written first is what came back and not the empty dock of the first
  // render. `restore` is a no-op on a dock that already has tabs, which is what
  // makes StrictMode's second run of this effect harmless - the reducer says so.
  useEffect(() => {
    if (stored !== null && stored.tabs.length > 0) dispatch({ type: 'restore', stored });
  }, [stored]);

  // What the next load gets back. Written on every change of the list or of
  // the front tab, as addresses only - `storable` is what decides that.
  useEffect(() => {
    try {
      if (state.tabs.length === 0) sessionStorage.removeItem(TABS_KEY);
      else sessionStorage.setItem(TABS_KEY, JSON.stringify(storable(state)));
    } catch {
      // See readStoredHeight.
    }
  }, [state]);

  // The key in `localStorage`, emptied once. Nothing reads it any more, and a
  // list of somebody's shells left in shared storage is a list every future
  // tab could be taught to open again.
  useEffect(() => {
    try {
      localStorage.removeItem(TABS_KEY);
    } catch {
      // See readStoredHeight.
    }
  }, []);

  // `'*'` is written and a pin removes the key. Nothing else is ever put here.
  useEffect(() => {
    try {
      if (state.scope === '*') localStorage.setItem(SCOPE_KEY, '*');
      else localStorage.removeItem(SCOPE_KEY);
    } catch {
      // See readStoredHeight. A scope that does not survive a reload is a
      // smaller loss than a console that will not draw.
    }
  }, [state.scope]);

  const setHeight = useCallback((px: number) => {
    const clamped = clampHeight(px, window.innerHeight);
    setHeightState(clamped);
    try {
      localStorage.setItem(HEIGHT_KEY, String(clamped));
    } catch {
      // As above.
    }
  }, []);

  // The refusal clears itself. Six seconds: long
  // enough to read a sentence naming a number, short enough that it is not still
  // there when the operator has moved on and would read it as a new refusal.
  useEffect(() => {
    if (state.note === '') return undefined;
    const timer = setTimeout(() => dispatch({ type: 'note', note: '' }), 6_000);
    return () => clearTimeout(timer);
  }, [state.note]);

  // A ref rather than a dependency, so `openLogs` is stable: it is passed to
  // buttons on every list row, and a new identity on every dock change would
  // re-render all of them.
  const latest = useRef(state);
  useEffect(() => {
    latest.current = state;
  });

  const openLogs = useCallback((request: LogsRequest): boolean => {
    const kind: TabKind = 'logs';
    // **Already open beats room, and the order matters.** `room` counts and
    // never looks at what is being asked for, so with the cluster's eight taken
    // it would refuse a panel the operator already has - and `LogsButton` only
    // closes the drawer on `true`, so the gesture would do nothing at all and
    // the drawer would stay open over the console.
    //
    // It also answers `true`, which is the half that reaches the operator: the
    // panel they asked for is now in front of them and the drawer gets out of
    // the way, which is what pressing Logs has always meant.
    const open = findLogsTab(latest.current, request);
    if (open !== undefined) {
      dispatch({ type: 'focus', id: open.id });
      return true;
    }
    // **The ceiling is asked before anything is built**, as every opener does.
    // The reducer asks it again and would refuse on its own; asking here is what lets the caller know, and it is the
    // same function rather than a second copy of the numbers.
    const refusal = room(latest.current, request.cluster);
    if (refusal !== null) {
      dispatch({ type: 'note', note: refusal });
      return false;
    }
    dispatch({
      type: 'open',
      tab: {
        kind,
        cluster: request.cluster,
        label: request.name,
        namespace: request.namespace,
        name: request.name,
        ...(request.container === undefined ? {} : { container: request.container }),
      },
    });
    return true;
  }, []);

  const openShell = useCallback((request: ShellRequest): boolean => {
    // The ceiling first, for the same reason `openLogs` asks it first: it is
    // the same function rather than a second copy of the numbers.
    const refusal = room(latest.current, request.cluster);
    if (refusal !== null) {
      dispatch({ type: 'note', note: refusal });
      return false;
    }
    // The conversation's id, drawn here and not in the reducer - a reducer
    // that draws random numbers is one whose tests cannot say what it built.
    // Only a Claude tab has one; a request that brings its own (a restore) keeps
    // it, which is the whole point of storing it.
    const session =
      request.kind === 'claude' ? (request.session ?? crypto.randomUUID()) : undefined;
    dispatch({
      type: 'open',
      tab: {
        kind: request.kind,
        cluster: request.cluster,
        label: request.label,
        ...(request.namespace === undefined ? {} : { namespace: request.namespace }),
        ...(request.name === undefined ? {} : { name: request.name }),
        ...(request.container === undefined ? {} : { container: request.container }),
        ...(request.objectKind === undefined ? {} : { objectKind: request.objectKind }),
        ...(request.token === undefined ? {} : { token: request.token }),
        ...(session === undefined ? {} : { session }),
        ...(request.image === undefined ? {} : { image: request.image }),
        ...(request.target === undefined ? {} : { target: request.target }),
      },
    });
    return true;
  }, []);

  const value = useMemo<DockApi>(
    () => ({ state, dispatch, height, setHeight, openLogs, openShell }),
    [state, height, setHeight, openLogs, openShell],
  );

  return <DockContext value={value}>{children}</DockContext>;
}

export function useDock(): DockApi {
  const value = useContext(DockContext);
  if (value === null) throw new Error('useDock outside <DockProvider>');
  return value;
}

/**
 * The dock's API where there may not be one.
 *
 * For a control that offers to open a log panel and is rendered in a test, or in
 * a Storybook, or anywhere the Shell is not above it. It answers `null` rather
 * than throwing, so the control is simply absent - which is the honest rendering
 * of "there is no dock here" and not a page that will not draw.
 */
export function useDockIfPresent(): DockApi | null {
  return useContext(DockContext);
}
