/**
 * The dock's tab registry, as data.
 *
 * ## Where this lives, and why here rather than in a component
 *
 * `docs/migration/03-javascript.md` names the requirement - *"the terminal/log
 * state has to live in a store keyed by tab id, not in the component that
 * renders the visible pane"* - and names no store. The answer taken here is the
 * cheapest one the sweep offers, and it is two halves:
 *
 *   - **`live/handles.ts` holds the live object** - the socket, the PTY, the
 *     stream. It is already a module-level keyed registry with a refcount and a
 *     deferred close, built for exactly this, and it is what makes StrictMode's
 *     second mount find the first rather than open a second PTY.
 *   - **This module holds the tab list**, as a pure reducer over plain data, and
 *     a React context distributes it. Pure because every interesting rule on
 *     this surface is a rule about the *list* - which tabs are visible, whether
 *     there is room for another, how many are running elsewhere - and each of
 *     them was a defect once. A rule that can be tested without a DOM is a rule
 *     that gets tested.
 *
 * ## The one rule the React side must not get wrong
 *
 * **A hidden tab stays mounted.** Nothing in here disposes anything on the
 * hiding path, and nothing that renders from it may unmount a pane to hide it:
 * a `kubectl get pods -w` left on cluster A is still running, with its
 * scrollback, when the operator comes back to A on Friday. That is the single
 * promise the whole partition rests on: changing scope **only ever hides**.
 */

/** What a tab is a view of. The mark is passed rather than derived, because a
 *  Claude session and a pod shell are the same kind of thing - a PTY on a
 *  WebSocket - and only the mark tells them apart in a row of them. */
export type TabKind = 'terminal' | 'exec' | 'claude' | 'nodeshell' | 'logs';

export interface Tab {
  /** `<kind>:<seq>`. **A counter, not a content hash**, so opening a second
   *  shell on the same pod gives a second session rather than re-selecting the
   *  first. The sequence is also what keeps two shells on one pod apart in
   *  `live/handles.ts`: a key without it finds the first tab's handle and the
   *  counter is defeated by the deduplication. */
  readonly id: string;
  readonly kind: TabKind;
  /** The cluster this tab belongs to, in the route's own spelling. A tab with
   *  no cluster belongs to no partition and is visible nowhere. */
  readonly cluster: string;
  readonly label: string;
  /** Whether the thing behind it is still running. **Not whether the tab is
   *  open**: an ended tab keeps its scrollback, because the scrollback is the
   *  reason it was opened and losing it the instant a pod restarts would be
   *  losing the evidence. */
  readonly live: boolean;
  readonly seq: number;
  /** What it was opened on, for the panes that need it. */
  readonly namespace?: string;
  readonly name?: string;
  readonly container?: string;
  /** The object's **Kind** - "Pod", not "pods" - where the tab is about one. The
   *  assistant channel takes it as framing; nothing else reads it. Named apart
   *  from `kind`, which is this tab's own kind and was here first. */
  readonly objectKind?: string;
  /** A Claude tab's conversation, as a UUID the browser drew when the tab was
   *  first opened. Stored with the tab and sent on the channel's
   *  query; whether it is a first open or a return is the **server's** call,
   *  from whether a transcript exists, never this side's. Absent on every other
   *  kind. */
  readonly session?: string;
  readonly previous?: boolean;
  /** The plan token, on a node shell and nowhere else.
   *
   *  Held on the tab because the node shell is the one confirm-then-act pair
   *  whose act is a socket: the confirmation happens in a drawer, the commit is
   *  a dock button, and the token is what carries the agreement across. **The
   *  node is not here**, deliberately - the server reads it out of the plan, so
   *  there is nothing in the browser that could name a different one. */
  readonly token?: string;
  /** Put back by a reload and **not reopened**: a shell, a pod shell or a
   *  Claude tab whose session died with the page, drawn as an offer to open a
   *  new one rather than as one.
   *  `live` is false beside it, so nothing counts it as running. Absent on
   *  every tab the operator opened in this page, and on a log panel - a log is
   *  re-read, not re-run, so a reload brings it back whole. */
  readonly dormant?: boolean;
}

/**
 * `'*'` is every cluster at once; `null` follows the page; anything else is a
 * cluster pinned by hand.
 *
 * The three are not interchangeable: `'*'` is a **mode** - the operator asked
 * for everything at once - so it survives a reload. A pin is
 * a **visit**: they went to look at a shell left behind on another cluster, and
 * walking to a third cluster ends the visit. Remembering that one would make the
 * dock silently disagree with the page for the rest of the session, which is the
 * confusion partitioning exists to remove.
 */
export type Scope = '*' | string | null;

export interface DockState {
  readonly tabs: readonly Tab[];
  readonly active: string | null;
  readonly seq: number;
  readonly scope: Scope;
  /** The cluster the page is about, **remembered rather than recomputed**: a
   *  route that names no cluster - Health, Settings - must not empty the dock,
   *  because the operator checking why a cluster is unreachable has not stopped
   *  working on it. */
  readonly following: string;
  /** Not persisted, deliberately. A SPA has no full page load to reset it, so
   *  a stored fold would be permanent: a dock that opens folded for no reason
   *  anybody can see. */
  readonly folded: boolean;
  readonly note: string;
  /** When the × was first pressed. `null` means it is not armed. */
  readonly endArmedAt: number | null;
}

export function emptyDock(scope: Scope = null): DockState {
  return {
    tabs: [], active: null, seq: 0, scope, following: '',
    folded: false, note: '', endArmedAt: null,
  };
}

// --- The ceilings --------------------------------------------------------------

/**
 * Two client limits, checked **before anything is built**.
 *
 * The real ceilings are on the server - three of them, all 16, all process-wide,
 * counting terminals, followed log streams and tunnels separately. These are
 * here so the refusal arrives as a sentence rather than as a socket that opens
 * and immediately closes, and because the server cannot say *"8 are already open
 * on prod-eks"*: it does not partition by cluster, and does not know which one
 * the operator is looking at.
 *
 * Two numbers because they answer different mistakes - clicking down a list of
 * pods, and doing that on four clusters in turn, which is the mistake
 * partitioning makes easy since the first three piles are no longer on screen to
 * remind anyone.
 */
export const MAX_PER_CLUSTER = 8;
export const MAX_TABS = 16;

// --- What a reload gets back --------------------------------------------------

/**
 * A tab as it is written to storage: its **address** and nothing else.
 *
 * Without it a reload - F5, a laptop that slept, a network that changed - would
 * draw an empty dock, and the operator would rebuild by hand which pods they
 * were watching and which shells they had open. What comes back is the
 * *layout*: which kind, on which object, on which cluster, in which order, and
 * which was in front. Not the session - the shell's history and the running
 * command died with the socket, and nothing keeps them alive across a reload.
 *
 * **Why these fields and no others.** `id`, `seq` and `live` are the reducer's
 * own and are re-made by `open`; `previous` is a log follow's state, not its
 * address; and `token` is a plan's proof and is never written - a node shell is
 * opened through a plan and a typed confirmation, and a reload is not that
 * gesture. Nothing here is a credential: a pod's name in the browser's storage
 * is what the URL bar already holds.
 */
export interface StoredTab {
  readonly kind: TabKind;
  readonly cluster: string;
  readonly label: string;
  readonly namespace?: string;
  readonly name?: string;
  readonly container?: string;
  readonly objectKind?: string;
  /** See `Tab.session`. The one stored field that is not an address: it is
   *  what a reopened Claude tab resumes. Not a credential - it names a
   *  transcript on the console's own machine and opens nothing by itself. */
  readonly session?: string;
}

/** The shape `crypto.randomUUID()` produces and the server accepts; anything
 *  else in storage is dropped rather than sent. */
const SESSION_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

export interface StoredDock {
  readonly tabs: readonly StoredTab[];
  /** The index into `tabs` of the one that was in front, or null. An index and
   *  not an id, because ids are `<kind>:<seq>` and the counter restarts. */
  readonly active: number | null;
}

const KINDS: ReadonlySet<string> = new Set<TabKind>(['terminal', 'exec', 'claude', 'nodeshell', 'logs']);

/** The state, as it is written. Every tab, node shells included - see `restore`
 *  for what becomes of those. */
export function storable(state: DockState): StoredDock {
  const tabs = state.tabs.map<StoredTab>((tab) => ({
    kind: tab.kind,
    cluster: tab.cluster,
    label: tab.label,
    ...(tab.namespace === undefined ? {} : { namespace: tab.namespace }),
    ...(tab.name === undefined ? {} : { name: tab.name }),
    ...(tab.container === undefined ? {} : { container: tab.container }),
    ...(tab.objectKind === undefined ? {} : { objectKind: tab.objectKind }),
    ...(tab.session === undefined ? {} : { session: tab.session }),
  }));
  const active = state.tabs.findIndex((tab) => tab.id === state.active);
  return { tabs, active: active < 0 ? null : active };
}

/**
 * What was written, read back with suspicion. Storage is the browser's and
 * anything can be in it - an older shape, a hand edit, a truncated write - and
 * the answer to a value that is not a stored dock is *no stored dock*, never a
 * throw and never a tab with an `undefined` cluster reaching the reducer.
 */
export function parseStored(raw: unknown): StoredDock | null {
  if (typeof raw !== 'object' || raw === null) return null;
  const record = raw as { tabs?: unknown; active?: unknown };
  if (!Array.isArray(record.tabs)) return null;
  const tabs: StoredTab[] = [];
  for (const entry of record.tabs as unknown[]) {
    if (typeof entry !== 'object' || entry === null) return null;
    const tab = entry as Record<string, unknown>;
    const { kind, cluster, label } = tab;
    if (typeof kind !== 'string' || !KINDS.has(kind)) return null;
    if (typeof cluster !== 'string' || cluster === '' || typeof label !== 'string') return null;
    const optional = (key: 'namespace' | 'name' | 'container' | 'objectKind'): { [k: string]: string } => {
      const value = tab[key];
      return typeof value === 'string' && value !== '' ? { [key]: value } : {};
    };
    const session = typeof tab['session'] === 'string' && SESSION_RE.test(tab['session']) ? { session: tab['session'] } : {};
    tabs.push({
      kind: kind as TabKind,
      cluster,
      label,
      ...optional('namespace'),
      ...optional('name'),
      ...optional('container'),
      ...optional('objectKind'),
      ...session,
    });
  }
  const active =
    typeof record.active === 'number' && Number.isInteger(record.active) && record.active >= 0 && record.active < tabs.length
      ? record.active
      : null;
  return { tabs, active };
}

/**
 * An open log panel on the same object, where there is one.
 *
 * **Asked before `room`, and that order is the whole point.** `room` counts and
 * never looks at what is being asked for, so at the ceiling it would refuse a
 * log panel the operator already has open - and `LogsButton` leaves the drawer
 * where it was on a refusal, so pressing Logs on a pod **already in the tab
 * strip** would do nothing but say the cluster is at its limit.
 *
 * Asking for a second copy of a panel that is already there is not asking for
 * room. It is asking to be taken to it.
 *
 * ## Logs and not shells, deliberately
 *
 * `Tab.id` says a shell is `<kind>:<seq>` - *a counter, not a content hash, so
 * opening a second shell on the same pod gives a second session rather than
 * re-selecting the first*. That is right for a PTY: two shells on one pod are
 * two working sessions, and collapsing them would take one away.
 *
 * A log panel is a **view of one stream**. A second identical one is not a
 * second session, it is the same lines twice - costing a tab, one of the
 * server's sixteen follows, and one of the cluster's eight. So the dedup is
 * `kind === 'logs'` and stops there.
 *
 * ## The container, and why a request naming none matches any
 *
 * `LogsButton` sends no container: the server picks the pod's default. But
 * `setContainer` moves a panel to another container afterwards, so a panel
 * opened on the default may now be showing a sidecar. Matching strictly would
 * open a second panel on a pod whose logs are already up - the exact case this
 * exists to remove.
 *
 * So a request that **names** a container must match it exactly (those are
 * genuinely different views), and a request that names none matches any panel on
 * the object. The cost is named rather than left to be discovered: an operator
 * who moved a panel to a sidecar and presses Logs again is taken to the sidecar
 * rather than to the default. That is still a panel on the pod they asked about,
 * which beats a duplicate, and the container picker is inside the panel.
 */
export function findLogsTab(
  state: DockState,
  request: { cluster: string; namespace?: string; name: string; container?: string },
): Tab | undefined {
  return state.tabs.find((tab) => tab.kind === 'logs'
    && tab.cluster === request.cluster
    && tab.namespace === request.namespace
    && tab.name === request.name
    && (request.container === undefined || tab.container === request.container));
}

/**
 * Whether another tab fits, and the sentence if it does not.
 *
 * Each refusal **begins with the number itself**: a refusal that does not say
 * what the limit is leaves the operator guessing at the cluster.
 *
 * **It counts and does not look.** It cannot answer "is this one already open" -
 * that is `findLogsTab`, and a caller that can reuse a panel asks that first.
 */
export function room(state: DockState, cluster: string): string | null {
  if (state.tabs.length >= MAX_TABS) {
    return `${MAX_TABS} terminals and log panels are open across every cluster, which is the limit. ` +
      'Close some — the button beside the tabs lists where they are.';
  }
  const here = state.tabs.filter((tab) => tab.cluster === cluster).length;
  if (here >= MAX_PER_CLUSTER) {
    return `${MAX_PER_CLUSTER} are already open on ${cluster}, which is the limit for one cluster. Close one first.`;
  }
  return null;
}

// --- The partition -------------------------------------------------------------

/** Which cluster the dock is showing, resolved from the scope and the page. */
export function shownCluster(state: DockState): Scope {
  if (state.scope === '*') return '*';
  return state.scope ?? state.following;
}

/**
 * What is on screen. **The one place that decides**, so opening, closing,
 * navigating and picking a scope all end here rather than each having an opinion.
 *
 * The reason there is a partition at all: fourteen contexts on the rail, and a
 * dock that keeps everything visible is a strip of tab names - pod names, mostly
 * - with no way to tell which cluster any of them is a shell on.
 */
export function visibleTabs(state: DockState): readonly Tab[] {
  const shown = shownCluster(state);
  if (shown === '*') return state.tabs;
  if (shown === '' || shown === null) return [];
  return state.tabs.filter((tab) => tab.cluster === shown);
}

/**
 * How many **live** tabs are somewhere else.
 *
 * Ended tabs are excluded or the badge would be reporting resources nobody
 * holds. What it answers is *"a hidden PTY nobody knows about is a leak with a
 * UI"*: the badge is how the operator knows they are there.
 *
 * It counts this browser window's tabs and not this process's sessions. A
 * second window's shells do not
 * appear here, and the tooltip says so rather than the badge implying otherwise.
 */
export function liveElsewhere(state: DockState): number {
  const visible = new Set(visibleTabs(state).map((tab) => tab.id));
  return state.tabs.filter((tab) => tab.live && !visible.has(tab.id)).length;
}

/**
 * Nothing open here, something open elsewhere.
 *
 * A third visual state beside open and folded, and **not** the same as folded:
 * folded is the operator's choice and this is a consequence. A dock that closed
 * itself here would leave live shells behind no UI at all, which is the one
 * outcome partitioning must not produce.
 */
export function isQuiet(state: DockState): boolean {
  return state.tabs.length > 0 && visibleTabs(state).length === 0;
}

export interface ScopeRow {
  readonly cluster: string;
  readonly live: number;
  readonly ended: number;
}

/** One row per cluster with something open, in first-opened order so the list
 *  does not reshuffle under the pointer. */
export function scopeRows(state: DockState): readonly ScopeRow[] {
  const order: string[] = [];
  const byCluster = new Map<string, { live: number; ended: number }>();
  for (const tab of state.tabs) {
    let tally = byCluster.get(tab.cluster);
    if (tally === undefined) {
      tally = { live: 0, ended: 0 };
      byCluster.set(tab.cluster, tally);
      order.push(tab.cluster);
    }
    if (tab.live) tally.live += 1;
    else tally.ended += 1;
  }
  return order.map((cluster) => {
    const tally = byCluster.get(cluster) ?? { live: 0, ended: 0 };
    return { cluster, live: tally.live, ended: tally.ended };
  });
}

// --- The reducer ---------------------------------------------------------------

export type DockAction =
  | { type: 'open'; tab: Omit<Tab, 'id' | 'seq' | 'live'> }
  | { type: 'restore'; stored: StoredDock }
  | { type: 'close'; id: string }
  | { type: 'closeCluster'; cluster: string }
  | { type: 'closeAll' }
  | { type: 'select'; id: string }
  | { type: 'focus'; id: string }
  | { type: 'ended'; id: string }
  | { type: 'revive'; id: string }
  | { type: 'wake'; id: string }
  | { type: 'setContainer'; id: string; container: string; previous: boolean }
  | { type: 'scope'; scope: Scope }
  | { type: 'followPage'; cluster: string }
  | { type: 'fold'; folded: boolean }
  | { type: 'note'; note: string }
  | { type: 'armEnd'; at: number | null };

export function dockReducer(state: DockState, action: DockAction): DockState {
  switch (action.type) {
    case 'open': {
      const refusal = room(state, action.tab.cluster);
      if (refusal !== null) return { ...state, note: refusal };

      const seq = state.seq + 1;
      const tab: Tab = { ...action.tab, id: `${action.tab.kind}:${seq}`, seq, live: true };
      // A tab opened for a cluster that is out of scope pins the scope to it: a
      // tab nobody can see is not worth opening.
      const scope = state.scope === '*' || tab.cluster === shownCluster(state)
        ? state.scope
        : tab.cluster;
      // Unfolded here rather than by whatever renders the pane. A folded panel
      // is `display: none`, and a terminal built inside an element that is not
      // laid out measures zero and comes up one column wide - a session that is
      // running and looks broken.
      return {
        ...state,
        tabs: [...state.tabs, tab],
        active: tab.id,
        seq,
        scope,
        folded: false,
        note: '',
        endArmedAt: null,
      };
    }

    case 'close':
      return withoutTabs(state, (tab) => tab.id === action.id);

    case 'closeCluster':
      return withoutTabs(state, (tab) => tab.cluster === action.cluster);

    case 'closeAll':
      return { ...emptyDock(state.scope), following: state.following };

    // The reload's tabs, reopened **through `open`**, one by one, in their
    // order - so every rule a click meets, a reopen meets: the ceilings, the
    // scope pin, the unfold. A refused reopen leaves the ceiling's own sentence
    // in `note` and the rest still open; nothing here bypasses `room`.
    //
    // **Only into an empty dock.** A restore is a first act and never a merge:
    // StrictMode runs an effect twice, and a second restore on top of the
    // first would be every tab twice. The guard is here, in the reducer, rather
    // than in the effect that dispatches it, because this is where it can be
    // tested without a DOM.
    //
    // **A node shell is not reopened.** It was opened through a plan and a
    // typed confirmation, the plan is not stored (`StoredTab`), and a
    // reload is not that gesture. The note says one was open, so the operator
    // knows why the dock is one tab short rather than concluding it was never
    // there.
    //
    // **The scope pin is the one rule of `open` applied to the whole restore
    // rather than tab by tab**. Per tab, each one on a cluster other than the
    // one shown would pin the scope to itself, so the dock would come back
    // pinned to whichever cluster the *last* stored tab was on - a pin
    // `DockContext` says is never remembered - with the front tab hidden
    // behind it. So the scope is the one the dock had before (the page, or
    // `'*'`), and it is pinned only where that would show nothing, and then to
    // the tab that was in front. The front tab goes through
    // `activeStaysVisible`, as on every other path that moves it.
    case 'restore': {
      if (state.tabs.length > 0) return state;
      let next = state;
      const ids: (string | null)[] = [];
      const nodeShells: string[] = [];
      for (const tab of action.stored.tabs) {
        if (tab.kind === 'nodeshell') {
          nodeShells.push(tab.label);
          ids.push(null);
          continue;
        }
        const before = next.tabs.length;
        next = dockReducer(next, { type: 'open', tab });
        const reopened = next.tabs.length > before ? next.tabs[next.tabs.length - 1] : undefined;
        ids.push(reopened?.id ?? null);
        // **A pseudo-terminal comes back as an offer, never as a session.**
        // Its shell died with the page, and a tab that opened a new one by
        // being drawn would open one on every reload, each holding one of the
        // server's sixteen slots. The tab keeps its place, its address
        // and a Claude tab's conversation; `wake` is the operator asking.
        if (reopened !== undefined && tab.kind !== 'logs') {
          next = mapTab(next, reopened.id, (was) => ({ ...was, live: false, dormant: true }));
        }
      }
      const wanted = action.stored.active === null ? null : (ids[action.stored.active] ?? null);
      const note =
        nodeShells.length === 0
          ? next.note
          : `A node shell on ${nodeShells.join(', ')} was open before the reload and was not reopened: it needs its confirmation again.`;
      let restored: DockState = { ...next, scope: state.scope, note, ...(wanted === null ? {} : { active: wanted }) };
      if (restored.tabs.length > 0 && visibleTabs(restored).length === 0) {
        const front = restored.tabs.find((tab) => tab.id === wanted) ?? restored.tabs[0];
        if (front !== undefined) restored = { ...restored, scope: front.cluster };
      }
      return activeStaysVisible(restored);
    }

    case 'select':
      return { ...state, active: action.id };

    // What `open` does to the panel, for a tab that is already in it: bring it
    // to the front, make sure it can be seen, and clear a stale refusal.
    //
    // **Not `select`.** `select` is a press on a tab that is already on screen
    // and changes nothing else, deliberately. This arrives from a control
    // somewhere else in the console - `LogsButton` in the drawer - where the
    // panel may be folded, or scoped to another cluster, and a `select` alone
    // would answer the gesture by moving something nobody can see. Every line
    // below is one `open` already runs for the same reason; the tab is the only
    // difference.
    case 'focus': {
      const wanted = state.tabs.find((tab) => tab.id === action.id);
      if (wanted === undefined) return state;
      // A tab out of scope pins the scope to it, exactly as opening one does: a
      // tab nobody can see is not worth being sent to.
      const scope = state.scope === '*' || wanted.cluster === shownCluster(state)
        ? state.scope
        : wanted.cluster;
      return { ...state, active: wanted.id, scope, folded: false, note: '', endArmedAt: null };
    }

    // Not closed. The scrollback is the reason the operator opened it, and
    // losing it the instant a pod restarts would be losing the evidence.
    case 'ended':
      return mapTab(state, action.id, (tab) => ({ ...tab, live: false }));

    // The opposite, for the one thing that reopens its own stream: a log panel
    // re-reads the log when the container or the timestamps toggle changes. One
    // line, and the kind of line a port drops - `ended` reads like a terminal
    // state. Without it the badge under-reports, which is the mirror of the
    // failure `ended` exists to prevent.
    case 'revive':
      return mapTab(state, action.id, (tab) => ({ ...tab, live: true }));

    // A tab a reload put back, opened again because the operator pressed for
    // it. The only way out of `dormant`, and the pane opens its session on
    // the render that follows - with the tab's own address and, on a Claude
    // tab, its own conversation.
    case 'wake':
      return mapTab(state, action.id, (tab) => {
        if (tab.dormant !== true) return tab;
        const woken: { -readonly [K in keyof Tab]: Tab[K] } = { ...tab, live: true };
        delete woken.dormant;
        return woken;
      });

    case 'setContainer':
      return mapTab(state, action.id, (tab) => ({
        ...tab, container: action.container, previous: action.previous, live: true,
      }));

    case 'scope':
      return activeStaysVisible({ ...state, scope: action.scope, endArmedAt: null });

    case 'followPage':
      // A route that names no cluster changes nothing. /health and /settings are
      // not about a cluster, and arriving on one must not empty the dock.
      if (action.cluster === '' || action.cluster === state.following) return state;
      return activeStaysVisible({
        ...state,
        following: action.cluster,
        // Walking to another cluster ends a visit. `'*'` is a mode and survives.
        scope: state.scope === '*' ? '*' : null,
        endArmedAt: null,
      });

    case 'fold':
      // **This may never end anything**: if a fold needs to dispose something,
      // it is not a fold. "Get out of my way" and "I am finished with these"
      // are two intentions, and an operator tidying the screen must not lose
      // every shell on every cluster.
      return { ...state, folded: action.folded, endArmedAt: null };

    case 'note':
      return { ...state, note: action.note };

    case 'armEnd':
      return { ...state, endArmedAt: action.at };
  }
}

function withoutTabs(state: DockState, gone: (tab: Tab) => boolean): DockState {
  const tabs = state.tabs.filter((tab) => !gone(tab));
  if (tabs.length === state.tabs.length) return state;
  const next = { ...state, tabs, note: '', endArmedAt: null };
  // Closing the last one closes the panel, and an unfolded panel is what the
  // next tab opens into.
  if (tabs.length === 0) return { ...next, active: null, folded: false };
  return activeStaysVisible(next);
}

function mapTab(state: DockState, id: string, change: (tab: Tab) => Tab): DockState {
  return { ...state, tabs: state.tabs.map((tab) => (tab.id === id ? change(tab) : tab)) };
}

/**
 * The active tab is replaced if it has become hidden.
 *
 * An active tab that is hidden means a pane drawn over the panes of the cluster
 * on screen. Nothing is closed to achieve it - only which one is on top changes.
 */
function activeStaysVisible(state: DockState): DockState {
  const visible = visibleTabs(state);
  if (state.active !== null && visible.some((tab) => tab.id === state.active)) return state;
  return { ...state, active: visible.length > 0 ? (visible[visible.length - 1]?.id ?? null) : null };
}

// --- What the × says -----------------------------------------------------------

/** Long enough to be a second press rather than a double-click, short enough
 *  that an arming from a minute ago cannot end a session the operator has since
 *  opened. */
export const END_ARM_MS = 4_000;

/**
 * The sentence the × writes into the note line before it will close anything.
 *
 * Three things, each a separate mistake if it is dropped: it says how many
 * sessions and across how many clusters; it is only asked when there is
 * something to lose, because a question asked when the answer never matters is
 * one nobody reads when it does; and **it names the fold as the way out**, which
 * is the whole answer to what the operator probably meant.
 */
export function endWarning(state: DockState): string | null {
  const live = state.tabs.filter((tab) => tab.live).length;
  if (state.tabs.length === 0) return null;
  const clusters = new Set(state.tabs.map((tab) => tab.cluster)).size;
  const sessions = live === 1 ? '1 session' : `${live} sessions`;
  const across = clusters === 1 ? '1 cluster' : `${clusters} clusters`;
  return `This ends ${sessions} across ${across} and cannot be undone. ` +
    'Press × again to confirm, or fold the panel to put it away and keep them running.';
}

/** Whether a press of the × should close everything, given when it was armed. */
export function endIsConfirmed(state: DockState, now: number): boolean {
  if (state.tabs.length === 0) return true;
  return state.endArmedAt !== null && now - state.endArmedAt <= END_ARM_MS;
}
