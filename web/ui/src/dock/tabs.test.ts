import { describe, expect, it } from 'vitest';

import {
  END_ARM_MS,
  MAX_PER_CLUSTER,
  MAX_TABS,
  dockReducer,
  emptyDock,
  endIsConfirmed,
  endWarning,
  findLogsTab,
  isQuiet,
  liveElsewhere,
  parseStored,
  room,
  scopeRows,
  shownCluster,
  storable,
  visibleTabs,
  type DockAction,
  type DockState,
  type TabKind,
} from './tabs';

/**
 * The dock's partition, ceiling and fold, as behavior: scope changes only
 * hide, and every one of the five ways of opening a tab is capped.
 */

const KINDS: readonly TabKind[] = ['terminal', 'exec', 'claude', 'nodeshell', 'logs'];

function reduce(state: DockState, ...actions: readonly DockAction[]): DockState {
  return actions.reduce(dockReducer, state);
}

function open(cluster: string, kind: TabKind = 'exec', label = 'api-1'): DockAction {
  return { type: 'open', tab: { kind, cluster, label } };
}

describe('the ceiling, checked before anything is built', () => {
  // All five openers.
  it.each(KINDS)('caps a tab opened as %s', (kind) => {
    let state = { ...emptyDock('*'), following: 'prod' };
    for (let i = 0; i < MAX_PER_CLUSTER; i++) state = reduce(state, open('prod', kind));
    expect(state.tabs).toHaveLength(MAX_PER_CLUSTER);

    const refused = reduce(state, open('prod', kind));
    expect(refused.tabs).toHaveLength(MAX_PER_CLUSTER);
    expect(refused.note).not.toBe('');
  });

  it('refuses in a sentence beginning with the number itself', () => {
    // "A refusal that does not say what the limit is leaves the operator
    // guessing at the cluster."
    let state = { ...emptyDock(), following: 'prod-eks' };
    for (let i = 0; i < MAX_PER_CLUSTER; i++) state = reduce(state, open('prod-eks'));

    const perCluster = room(state, 'prod-eks');
    expect(perCluster).not.toBeNull();
    expect(perCluster?.startsWith(String(MAX_PER_CLUSTER))).toBe(true);
    // And it names the cluster, which is the half the server cannot say.
    expect(perCluster).toContain('prod-eks');
  });

  it('has a whole-dock ceiling as well as a per-cluster one', () => {
    // Two numbers because they answer different mistakes: clicking down a list
    // of pods, and doing that on four clusters in turn.
    let state = { ...emptyDock('*'), following: 'a' };
    for (let c = 0; c < MAX_TABS / MAX_PER_CLUSTER; c++) {
      for (let i = 0; i < MAX_PER_CLUSTER; i++) state = reduce(state, open(`cluster-${c}`));
    }
    expect(state.tabs).toHaveLength(MAX_TABS);

    // A fresh cluster is under its own limit and still refused.
    const refusal = room(state, 'somewhere-else');
    expect(refusal).not.toBeNull();
    expect(refusal?.startsWith(String(MAX_TABS))).toBe(true);
  });

  it('builds nothing when it refuses', () => {
    let state = { ...emptyDock(), following: 'prod' };
    for (let i = 0; i < MAX_PER_CLUSTER; i++) state = reduce(state, open('prod'));
    const before = state.tabs;
    const after = reduce(state, open('prod'));
    expect(after.tabs).toBe(before);
    expect(after.seq).toBe(state.seq);
  });
});

describe('the partition', () => {
  it('shows the tabs of the cluster on screen and hides the rest', () => {
    const state = reduce({ ...emptyDock(), following: 'prod' },
      open('prod', 'exec', 'api-1'),
      { type: 'followPage', cluster: 'staging' },
      open('staging', 'exec', 'web-1'));

    expect(visibleTabs(state).map((tab) => tab.label)).toEqual(['web-1']);
    expect(state.tabs).toHaveLength(2);
  });

  /**
   * Replaces `TestNothingIsClosedMerelyToHideIt`, which read four function
   * bodies looking for the substring `dispose`. Here it is the property: every
   * action that runs when the cluster on screen changes leaves the tab list
   * untouched.
   *
   * A terminal whose cluster is not on screen is still a terminal somebody is
   * using, and closing its socket reaps the PTY behind it - which no amount of
   * UI can undo.
   */
  it('closes nothing merely to hide it', () => {
    const opened = reduce({ ...emptyDock(), following: 'prod' },
      open('prod', 'exec', 'api-1'),
      open('prod', 'logs', 'api-1 logs'));
    const before = opened.tabs;

    for (const action of [
      { type: 'followPage', cluster: 'staging' },
      { type: 'scope', scope: 'nowhere' },
      { type: 'scope', scope: null },
      { type: 'scope', scope: '*' },
      { type: 'select', id: before[0]?.id ?? '' },
      { type: 'fold', folded: true },
    ] satisfies DockAction[]) {
      const after = dockReducer(opened, action);
      expect(after.tabs, `${action.type} closed something`).toEqual(before);
    }
  });

  it('replaces the active tab when it becomes hidden', () => {
    // An active tab that is hidden means a pane drawn over the panes of the
    // cluster on screen.
    const state = reduce({ ...emptyDock(), following: 'prod' },
      open('prod', 'exec', 'api-1'),
      { type: 'followPage', cluster: 'staging' },
      open('staging', 'exec', 'web-1'));
    expect(state.active).toBe(state.tabs[1]?.id);

    const back = dockReducer(state, { type: 'followPage', cluster: 'prod' });
    expect(back.active).toBe(back.tabs[0]?.id);
    expect(back.tabs).toHaveLength(2);
  });

  it('pins the scope to a tab opened for a cluster that is out of scope', () => {
    // A tab nobody can see is not worth opening.
    const state = reduce({ ...emptyDock(), following: 'prod' }, open('staging'));
    expect(shownCluster(state)).toBe('staging');
    expect(visibleTabs(state)).toHaveLength(1);
  });

  it('does not empty the dock on a page that is not about a cluster', () => {
    // /health and /settings. The operator checking why a cluster is unreachable
    // has not stopped working on it.
    const state = reduce({ ...emptyDock(), following: 'prod' },
      open('prod'),
      { type: 'followPage', cluster: '' });
    expect(state.following).toBe('prod');
    expect(visibleTabs(state)).toHaveLength(1);
  });
});

describe('all clusters, and the pin that is not remembered', () => {
  it('keeps every tab visible in all-clusters mode', () => {
    const state = reduce({ ...emptyDock('*'), following: 'prod' },
      open('prod'), open('staging'), open('dev'));
    expect(visibleTabs(state)).toHaveLength(3);
  });

  /**
   * `'*'` is a mode and a pin is a visit. Walking to a third cluster ends the
   * visit; remembering it would make the dock silently disagree with the page
   * for the rest of the session.
   */
  it('ends a pin when the page moves on, and keeps the all-clusters mode', () => {
    const pinned = reduce({ ...emptyDock(), following: 'prod' },
      { type: 'scope', scope: 'staging' },
      { type: 'followPage', cluster: 'dev' });
    expect(pinned.scope).toBeNull();
    expect(shownCluster(pinned)).toBe('dev');

    const everywhere = reduce({ ...emptyDock('*'), following: 'prod' },
      { type: 'followPage', cluster: 'dev' });
    expect(everywhere.scope).toBe('*');
  });
});

describe('what the header says about elsewhere', () => {
  it('counts live tabs on other clusters and not ended ones', () => {
    // "A badge that counts ended sessions is a badge reporting resources nobody
    // holds."
    // Walked to staging, opened two there, walked back. The visit ends with the
    // walk, so what is on screen is prod's one tab and two are live elsewhere.
    let state = reduce({ ...emptyDock(), following: 'prod' },
      open('prod'),
      { type: 'followPage', cluster: 'staging' },
      open('staging'), open('staging'),
      { type: 'followPage', cluster: 'prod' });
    expect(visibleTabs(state)).toHaveLength(1);
    expect(liveElsewhere(state)).toBe(2);

    const ended = state.tabs.find((tab) => tab.cluster === 'staging');
    state = dockReducer(state, { type: 'ended', id: ended?.id ?? '' });
    expect(liveElsewhere(state)).toBe(1);
  });

  it('goes quiet rather than closing when nothing is open on this cluster', () => {
    // A dock that closed itself here would leave live shells behind no UI at
    // all, which is the one outcome partitioning must not produce.
    const state = reduce({ ...emptyDock(), following: 'prod' },
      open('prod'),
      { type: 'followPage', cluster: 'staging' });
    expect(isQuiet(state)).toBe(true);
    expect(state.tabs).toHaveLength(1);

    // And an empty dock is not quiet - it is closed. The two are different
    // states, and each renders differently.
    expect(isQuiet(emptyDock())).toBe(false);
  });

  it('tallies each cluster separately, live and ended apart', () => {
    let state = reduce({ ...emptyDock('*'), following: 'prod' },
      open('prod'), open('prod'), open('staging'));
    state = dockReducer(state, { type: 'ended', id: state.tabs[1]?.id ?? '' });

    expect(scopeRows(state)).toEqual([
      { cluster: 'prod', live: 1, ended: 1 },
      { cluster: 'staging', live: 1, ended: 0 },
    ]);
  });
});

describe('ended, and revived', () => {
  it('keeps an ended tab and its place', () => {
    // The scrollback is the reason the operator opened it, and losing it the
    // instant a pod restarts would be losing the evidence.
    const state = reduce({ ...emptyDock(), following: 'prod' }, open('prod'));
    const ended = dockReducer(state, { type: 'ended', id: state.tabs[0]?.id ?? '' });
    expect(ended.tabs).toHaveLength(1);
    expect(ended.tabs[0]?.live).toBe(false);
    expect(visibleTabs(ended)).toHaveLength(1);
  });

  it('revives a log tab that reopens its own stream', () => {
    // One line, and the kind of line a port drops because `ended` reads like a
    // terminal state. Without it the badge under-reports, which is the mirror of
    // the failure `ended` exists to prevent.
    const state = reduce({ ...emptyDock(), following: 'prod' }, open('prod', 'logs'));
    const id = state.tabs[0]?.id ?? '';
    const cycled = reduce(state, { type: 'ended', id }, { type: 'revive', id });
    expect(cycled.tabs[0]?.live).toBe(true);
  });

  it('switching container revives the tab that is following again', () => {
    const state = reduce({ ...emptyDock(), following: 'prod' }, open('prod', 'logs'));
    const id = state.tabs[0]?.id ?? '';
    const switched = reduce(state,
      { type: 'ended', id },
      { type: 'setContainer', id, container: 'sidecar', previous: false });
    expect(switched.tabs[0]).toMatchObject({ live: true, container: 'sidecar', previous: false });
  });
});

describe('a log panel already open is found rather than counted against the ceiling', () => {
  /*
    Pressing Logs in the drawer must close it even at the ceiling. `room`
    counts and never looks at what is being asked for - so with the cluster's
    eight taken it would refuse a panel already in the strip, `openLogs` would
    answer `false`, and `LogsButton` only closes the drawer on `true`.

    This is the reducer's half of it, without a DOM - which is what this module
    exists to make possible.
  */
  function openLog(cluster: string, name: string, container?: string): DockAction {
    return {
      type: 'open',
      tab: {
        kind: 'logs', cluster, label: name, namespace: 'default', name,
        ...(container === undefined ? {} : { container }),
      },
    };
  }

  it('finds the panel on the same object', () => {
    const state = reduce({ ...emptyDock(), following: 'prod' }, openLog('prod', 'api-1'));
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'default', name: 'api-1' })?.id)
      .toBe('logs:1');
  });

  it('does not confuse another pod, another namespace or another cluster', () => {
    const state = reduce({ ...emptyDock('*'), following: 'prod' }, openLog('prod', 'api-1'));
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'default', name: 'api-2' })).toBeUndefined();
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'kube-system', name: 'api-1' })).toBeUndefined();
    expect(findLogsTab(state, { cluster: 'stage', namespace: 'default', name: 'api-1' })).toBeUndefined();
  });

  it('never matches a shell on the same pod', () => {
    // The dedup is `kind === 'logs'` and stops there. Two shells on one pod are
    // two working sessions - the block below is that rule - while a log panel is
    // a view of one stream, so a second identical one is the same lines twice.
    const state = reduce({ ...emptyDock(), following: 'prod' },
      { type: 'open', tab: { kind: 'exec', cluster: 'prod', label: 'api-1', namespace: 'default', name: 'api-1' } });
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'default', name: 'api-1' })).toBeUndefined();
  });

  it('holds a named container to an exact match, and lets an unnamed one take any', () => {
    // A request naming a container wants that view. One naming none is the
    // drawer's button, which sends no container at all, and it must still find
    // the panel after `setContainer` has moved it to a sidecar - otherwise it
    // opens a duplicate on a pod whose logs are already up.
    const state = reduce({ ...emptyDock(), following: 'prod' }, openLog('prod', 'api-1', 'sidecar'));
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'default', name: 'api-1', container: 'app' }))
      .toBeUndefined();
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'default', name: 'api-1', container: 'sidecar' })?.id)
      .toBe('logs:1');
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'default', name: 'api-1' })?.id).toBe('logs:1');
  });

  it('is answerable at the ceiling, where room is not', () => {
    // The report entire, in two lines: the cluster is full, and the panel being
    // asked for is one of the eight filling it.
    let state: DockState = { ...emptyDock(), following: 'prod' };
    for (let i = 0; i < MAX_PER_CLUSTER; i++) state = reduce(state, openLog('prod', `api-${i}`));
    expect(room(state, 'prod')).not.toBeNull();
    expect(findLogsTab(state, { cluster: 'prod', namespace: 'default', name: 'api-3' })?.id).toBe('logs:4');
  });
});

describe('focus, which is select plus what open does to the panel', () => {
  it('brings the tab forward, unfolds, and clears a stale refusal', () => {
    let state = reduce({ ...emptyDock(), following: 'prod' }, open('prod'), open('prod'));
    state = reduce(state,
      { type: 'fold', folded: true },
      { type: 'note', note: '8 are already open on prod' });
    const first = state.tabs[0]?.id ?? '';

    state = reduce(state, { type: 'focus', id: first });

    expect(state.active).toBe(first);
    expect(state.folded, 'a tab nobody can see is not an answer to the gesture').toBe(false);
    expect(state.note).toBe('');
  });

  it('pins the scope to a tab that is out of it, exactly as opening one does', () => {
    let state = reduce({ ...emptyDock(), following: 'prod' }, open('prod'));
    const onProd = state.tabs[0]?.id ?? '';
    state = reduce(state, { type: 'followPage', cluster: 'stage' }, open('stage'));
    expect(visibleTabs(state).map((tab) => tab.id)).not.toContain(onProd);

    state = reduce(state, { type: 'focus', id: onProd });

    expect(state.active).toBe(onProd);
    expect(visibleTabs(state).map((tab) => tab.id)).toContain(onProd);
  });

  it('changes nothing for a tab that is not there', () => {
    // An empty answer and a failed answer are different answers: a focus on a
    // tab closed between the read and the dispatch is a no-op, and not a dock
    // whose active id names nothing that renders.
    const state = reduce({ ...emptyDock(), following: 'prod' }, open('prod'));
    expect(reduce(state, { type: 'focus', id: 'logs:99' })).toBe(state);
  });

  it('opens nothing and closes nothing', () => {
    const state = reduce({ ...emptyDock(), following: 'prod' }, open('prod'), open('prod'));
    const after = reduce(state, { type: 'focus', id: state.tabs[0]?.id ?? '' });
    expect(after.tabs).toHaveLength(2);
    expect(after.seq, 'focusing must not burn a sequence').toBe(state.seq);
  });
});

describe('two shells on one pod are two sessions', () => {
  it('gives each a new id, because the id is a counter and not a content hash', () => {
    const state = reduce({ ...emptyDock(), following: 'prod' },
      open('prod', 'exec', 'api-1'),
      open('prod', 'exec', 'api-1'));
    expect(state.tabs).toHaveLength(2);
    expect(state.tabs[0]?.id).not.toBe(state.tabs[1]?.id);
    expect(state.tabs[1]?.seq).toBe(2);
  });

  it('never reuses a sequence after a close', () => {
    // The sequence is also the live-handle key. Reusing one would give a new
    // shell the previous shell's handle, which is the deduplication defeating
    // the counter.
    let state = reduce({ ...emptyDock(), following: 'prod' }, open('prod'));
    const first = state.tabs[0]?.id ?? '';
    state = reduce(state, { type: 'close', id: first }, open('prod'));
    expect(state.tabs[0]?.id).not.toBe(first);
  });

  it('files every tab under a cluster', () => {
    // A tab with no cluster belongs to no partition and is visible nowhere - a
    // running shell with no tab anywhere, which is the exact failure the whole
    // design exists to prevent.
    const state = reduce({ ...emptyDock('*'), following: 'prod' },
      ...KINDS.map((kind) => open('prod', kind)));
    for (const tab of state.tabs) expect(tab.cluster).toBe('prod');
  });
});

/**
 * Folding hides and never ends a session; closing asks once.
 */
describe('fold, and the × that asks once', () => {
  it('closes no session', () => {
    // If this needs to end something, it is not a fold. An operator tidying
    // the screen must not lose every shell on every cluster.
    const state = reduce({ ...emptyDock(), following: 'prod' }, open('prod'), open('prod'));
    const folded = dockReducer(state, { type: 'fold', folded: true });
    expect(folded.folded).toBe(true);
    expect(folded.tabs).toEqual(state.tabs);
    expect(folded.tabs.every((tab) => tab.live)).toBe(true);
  });

  it('opening a tab unfolds first', () => {
    // The ordering is load-bearing rather than courteous: a folded panel is
    // `display: none`, and a terminal built inside an element that is not laid
    // out measures a zero-width character cell and comes up one column wide - a
    // session that is running and looks broken.
    const state = reduce({ ...emptyDock(), following: 'prod' },
      { type: 'fold', folded: true },
      open('prod'));
    expect(state.folded).toBe(false);
  });

  it('arms before it ends, and names the fold as the way out', () => {
    const state = reduce({ ...emptyDock('*'), following: 'prod' },
      open('prod'), open('prod'), open('staging'));

    const warning = endWarning(state);
    expect(warning).not.toBeNull();
    expect(warning).toContain('3 sessions');
    expect(warning).toContain('2 clusters');
    // The word that turns a warning into a way out.
    expect(warning).toContain('fold');

    expect(endIsConfirmed(state, 1_000)).toBe(false);
    const armed = dockReducer(state, { type: 'armEnd', at: 1_000 });
    expect(endIsConfirmed(armed, 1_000 + END_ARM_MS - 1)).toBe(true);
    // An arming from a minute ago cannot end a session opened since.
    expect(endIsConfirmed(armed, 1_000 + END_ARM_MS + 1)).toBe(false);
  });

  it('asks nothing when there is nothing to lose', () => {
    // "A question asked when the answer never matters is one nobody reads when
    // it does." An empty panel closes on one press.
    expect(endWarning(emptyDock())).toBeNull();
    expect(endIsConfirmed(emptyDock(), 0)).toBe(true);
  });

  it('closing everything leaves the panel unfolded for the next tab', () => {
    const state = reduce({ ...emptyDock(), following: 'prod' },
      open('prod'),
      { type: 'fold', folded: true },
      { type: 'closeAll' });
    expect(state.folded).toBe(false);
    expect(state.tabs).toHaveLength(0);
    expect(state.active).toBeNull();
  });

  it('closes one cluster without touching another', () => {
    const state = reduce({ ...emptyDock('*'), following: 'prod' },
      open('prod'), open('staging'), open('staging'));
    const closed = dockReducer(state, { type: 'closeCluster', cluster: 'staging' });
    expect(closed.tabs).toHaveLength(1);
    expect(closed.tabs[0]?.cluster).toBe('prod');
  });
});

/**
 * What a reload gets back. The stored shape is an address and the
 * reopen goes through `open`, so the assertions are about three things - what
 * is written, what is refused on the way back, and what is deliberately not
 * reopened - and never about a socket, which none of this touches.
 */
describe('the tabs a reload gets back', () => {
  it('stores an address per tab and the index of the active one, and never a token', () => {
    const state = reduce(
      emptyDock(),
      { type: 'open', tab: { kind: 'exec', cluster: 'prod', label: 'api-1/app', namespace: 'default', name: 'api-1', container: 'app' } },
      { type: 'open', tab: { kind: 'nodeshell', cluster: 'prod', label: 'node-1', name: 'node-1', token: 'plan-token' } },
      { type: 'open', tab: { kind: 'claude', cluster: 'prod', label: 'Claude · api-1', namespace: 'default', name: 'api-1', objectKind: 'Pod' } },
      { type: 'select', id: 'exec:1' },
    );
    const stored = storable(state);
    expect(stored.active).toBe(0);
    expect(stored.tabs).toEqual([
      { kind: 'exec', cluster: 'prod', label: 'api-1/app', namespace: 'default', name: 'api-1', container: 'app' },
      { kind: 'nodeshell', cluster: 'prod', label: 'node-1', name: 'node-1' },
      { kind: 'claude', cluster: 'prod', label: 'Claude · api-1', namespace: 'default', name: 'api-1', objectKind: 'Pod' },
    ]);
    expect(JSON.stringify(stored)).not.toContain('plan-token');
  });

  it('keeps a Claude tab\'s conversation, and reads one back only when it is a UUID', () => {
    // The one stored field that is not an address. It goes through
    // `open` unchanged on a restore, which is how a reopened tab resumes.
    const state = reduce(emptyDock(), {
      type: 'open',
      tab: { kind: 'claude', cluster: 'prod', label: 'Claude · api-1', name: 'api-1', session: '3f2504e0-4f89-11d3-9a0c-0305e82c3301' },
    });
    expect(storable(state).tabs[0]).toMatchObject({ session: '3f2504e0-4f89-11d3-9a0c-0305e82c3301' });
    const back = reduce(emptyDock(), { type: 'restore', stored: storable(state) });
    expect(back.tabs[0]?.session).toBe('3f2504e0-4f89-11d3-9a0c-0305e82c3301');

    expect(
      parseStored({ tabs: [{ kind: 'claude', cluster: 'prod', label: 'x', session: 'not-a-uuid' }] })?.tabs[0],
    ).toEqual({ kind: 'claude', cluster: 'prod', label: 'x' });
  });

  it('reopens each tab through open, in order, and puts the same one in front', () => {
    const state = reduce(
      emptyDock(),
      open('prod', 'terminal', 'prod'),
      open('prod', 'exec', 'api-1'),
      { type: 'open', tab: { kind: 'logs', cluster: 'staging', label: 'web-0', namespace: 'default', name: 'web-0' } },
      { type: 'select', id: 'exec:2' },
    );
    const back = reduce(
      { ...emptyDock(), following: 'prod' },
      { type: 'restore', stored: storable(state) },
    );
    expect(back.tabs.map((tab) => [tab.kind, tab.cluster, tab.label])).toEqual([
      ['terminal', 'prod', 'prod'],
      ['exec', 'prod', 'api-1'],
      ['logs', 'staging', 'web-0'],
    ]);
    // A shell comes back as an offer and a log panel as a stream: its session
    // died with the page, and a tab that opened a new one by being drawn would
    // open one on every reload.
    expect(back.tabs.map((tab) => [tab.kind, tab.live, tab.dormant === true])).toEqual([
      ['terminal', false, true],
      ['exec', false, true],
      ['logs', true, false],
    ]);
    expect(back.active).toBe('exec:2');
    // The page is on prod, so the dock still follows it. Reopening the staging
    // tab through `open` tab by tab would pin the scope to staging - the last
    // stored tab's cluster - and leave `exec:2` in front of a strip that did
    // not show it.
    expect(back.scope).toBeNull();
    expect(visibleTabs(back).map((tab) => tab.id)).toContain('exec:2');
    // A log tab's address survives whole, because `LogPane` reads it.
    expect(back.tabs[2]).toMatchObject({ namespace: 'default', name: 'web-0' });
  });

  it('pins the scope once, to the tab in front, when the page names no cluster', () => {
    // The other half. On /settings nothing is "the page's cluster", so the
    // restore pins - the rule `open` applies to a tab nobody could see - but
    // once, to the tab that was in front, not tab by tab to whichever came last.
    const back = reduce(emptyDock(), {
      type: 'restore',
      stored: {
        tabs: [
          { kind: 'exec', cluster: 'prod', label: 'api-1' },
          { kind: 'logs', cluster: 'staging', label: 'web-0', namespace: 'default', name: 'web-0' },
        ],
        active: 0,
      },
    });
    expect(back.scope).toBe('prod');
    expect(back.active).toBe('exec:1');
    expect(visibleTabs(back).map((tab) => tab.id)).toEqual(['exec:1']);
  });

  it('pins once to the tab in front when none of the restored tabs is on the page cluster', () => {
    // The second case of the restore's one pin: the page names
    // a cluster, and nothing restored is on it. Following the page would draw
    // a quiet dock the operator did not have before the reload; the rule `open`
    // applies to a tab nobody could see is applied once, to the front tab - not
    // to the last stored one, which is where a per-tab pin would end.
    const back = reduce(
      { ...emptyDock(), following: 'dev' },
      {
        type: 'restore',
        stored: {
          tabs: [
            { kind: 'exec', cluster: 'prod', label: 'api-1' },
            { kind: 'exec', cluster: 'staging', label: 'web-0' },
          ],
          active: 0,
        },
      },
    );
    expect(back.scope).toBe('prod');
    expect(back.active).toBe('exec:1');
    expect(visibleTabs(back).map((tab) => tab.id)).toEqual(['exec:1']);
  });

  it('never leaves the tab in front hidden, whatever was stored', () => {
    // The front tab is on staging and the page is on prod: the dock follows
    // the page (a pin is a visit, and is not remembered), so a prod tab goes in
    // front through `activeStaysVisible`, the way every other path moves it.
    const back = reduce(
      { ...emptyDock(), following: 'prod' },
      {
        type: 'restore',
        stored: {
          tabs: [
            { kind: 'exec', cluster: 'prod', label: 'api-1' },
            { kind: 'exec', cluster: 'staging', label: 'web-0' },
          ],
          active: 1,
        },
      },
    );
    expect(back.scope).toBeNull();
    expect(back.active).toBe('exec:1');
  });

  it('leaves a node shell out, and says so in the note', () => {
    const back = reduce(emptyDock(), {
      type: 'restore',
      stored: {
        tabs: [
          { kind: 'exec', cluster: 'prod', label: 'api-1' },
          { kind: 'nodeshell', cluster: 'prod', label: 'node-1' },
          { kind: 'exec', cluster: 'prod', label: 'api-2' },
        ],
        active: 2,
      },
    });
    expect(back.tabs.map((tab) => tab.label)).toEqual(['api-1', 'api-2']);
    expect(back.note).toMatch(/node shell on node-1 .*not reopened/);
    // The active index counted the node shell; the mapping does not lose it.
    expect(back.active).toBe('exec:2');
  });

  it('meets the ceiling exactly as a click does, and keeps what fit', () => {
    const tabs = Array.from({ length: MAX_PER_CLUSTER + 2 }, (_value, i) => ({
      kind: 'exec' as const,
      cluster: 'prod',
      label: `api-${String(i)}`,
    }));
    const back = reduce(emptyDock(), { type: 'restore', stored: { tabs, active: null } });
    expect(back.tabs).toHaveLength(MAX_PER_CLUSTER);
    expect(back.note).toMatch(new RegExp(`^${MAX_PER_CLUSTER}\\b`));
  });

  it('wakes a restored shell only when asked, with its address and its conversation', () => {
    // `wake` is the operator pressing "Open a new
    // session" on a tab a reload put back; it is the only way out of dormant.
    const before = reduce(
      emptyDock(),
      { type: 'open', tab: { kind: 'claude', cluster: 'prod', label: 'Claude · api-1', name: 'api-1', session: '3f2504e0-4f89-11d3-9a0c-0305e82c3301' } },
      open('prod', 'exec', 'api-2'),
    );
    const back = reduce({ ...emptyDock(), following: 'prod' }, { type: 'restore', stored: storable(before) });
    expect(back.tabs.every((tab) => tab.dormant === true && !tab.live)).toBe(true);
    // Nothing that is dormant counts as running somewhere else.
    expect(liveElsewhere(back)).toBe(0);

    const claude = back.tabs[0]?.id ?? '';
    const woken = reduce(back, { type: 'wake', id: claude });
    expect(woken.tabs[0]).toMatchObject({ live: true, session: '3f2504e0-4f89-11d3-9a0c-0305e82c3301', name: 'api-1' });
    expect(woken.tabs[0]?.dormant).toBeUndefined();
    // The other one is still waiting to be asked.
    expect(woken.tabs[1]).toMatchObject({ live: false, dormant: true });

    // An ended tab is not a dormant one: `wake` does not bring back a session
    // that ended in this page.
    const ended = reduce(emptyDock(), open('prod'), { type: 'ended', id: 'exec:1' });
    expect(reduce(ended, { type: 'wake', id: 'exec:1' }).tabs[0]?.live).toBe(false);
  });

  it('restores into an empty dock only, so a second restore is a no-op', () => {
    const stored = { tabs: [{ kind: 'exec' as const, cluster: 'prod', label: 'api-1' }], active: 0 };
    const once = reduce(emptyDock(), { type: 'restore', stored });
    const twice = reduce(once, { type: 'restore', stored });
    expect(twice).toBe(once);
    expect(twice.tabs).toHaveLength(1);
  });

  it('reads back only what is a stored dock, and refuses the rest whole', () => {
    expect(parseStored(null)).toBeNull();
    expect(parseStored('[]')).toBeNull();
    expect(parseStored({ tabs: 'nope' })).toBeNull();
    expect(parseStored({ tabs: [{ kind: 'exec', cluster: '', label: 'x' }] })).toBeNull();
    expect(parseStored({ tabs: [{ kind: 'shell', cluster: 'prod', label: 'x' }] })).toBeNull();
    // One bad entry refuses the whole list: a partial restore that silently
    // dropped a tab is the dock disagreeing with what the operator had.
    expect(parseStored({ tabs: [{ kind: 'exec', cluster: 'prod', label: 'x' }, 7] })).toBeNull();

    const good = parseStored({
      tabs: [{ kind: 'logs', cluster: 'prod', label: 'web-0', namespace: 'default', name: 'web-0', token: 'never' }],
      active: 0,
    });
    expect(good).toEqual({
      tabs: [{ kind: 'logs', cluster: 'prod', label: 'web-0', namespace: 'default', name: 'web-0' }],
      active: 0,
    });
    // An active index that points past the list is no active tab, not a throw.
    expect(parseStored({ tabs: [{ kind: 'exec', cluster: 'prod', label: 'x' }], active: 4 })?.active).toBeNull();
  });
});
