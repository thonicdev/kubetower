import { useSyncExternalStore } from 'react';

import type { NavGroup } from '../api';

/**
 * Which left-menu groups a person opened, remembered across navigations.
 *
 * `Shell` is a pathless layout route, so the menu is not unmounted by a
 * navigation and React state alone would survive every click within a session.
 * What it would not survive is a reload, a second tab, or leaving the cluster
 * and coming back - and the arrangement is the person's. So it is remembered.
 *
 * ## Identified by key
 *
 * A group is identified by its `NavGroup.key` (`api/openapi.yaml`): stable,
 * server-chosen, and not a rendering. Neither an index - a fact about the menu
 * last drawn - nor the label, which stays free to change (a count, a
 * translation, a rename) without the arrangement resetting.
 *
 * ## Storage may not be there at all
 *
 * Private windows, blocked site data, a locked-down profile: every read and
 * every write can throw, and the menu has to draw correctly when it does. That
 * costs the memory and never the menu.
 *
 * ## `null` is not an empty arrangement, and that distinction is the whole
 * default
 *
 * `GET /api/c/{cluster}/nav` carries **no `open` flag and no ungrouped list**:
 * `apimount.go` folds those kinds into a group of their own. An empty answer and
 * a missing one are different answers:
 *
 * - **no memory at all** (`null`): only the group holding the page is open, held
 *   by the visit and never written down (`defaultOpen`). The page a person
 *   lands on is never folded away; the other headings wait to be asked for.
 * - **a memory, even an empty one** (`[]`): exactly what it says. A person who
 *   has closed every group gets every group closed, except the one the page is
 *   in, for the visit.
 *
 * Collapsing then only ever hides what nobody has opened yet, or what the
 * person hid.
 *
 * ## It is a store, not component state
 *
 * The arrangement has to outlive every navigation and every unmount of the
 * menu, and a `useState` in a route component is destroyed on navigation. So it
 * lives here at module scope, and `Menu.tsx` reads it through
 * `useSyncExternalStore`.
 *
 * That also settles where the "this group holds the page you are on" rule may
 * write from. It is an effect on arrival, and an effect may update an external
 * system; the same rule expressed as `setState` inside `useEffect` is a
 * cascading render, which `react-hooks/set-state-in-effect` refuses and is right
 * to.
 *
 * ## A visit is not an arrangement
 *
 * Arriving on a custom resource page has to open that heading - it is the
 * correctness rule below - but if the arrival were written down, the heading
 * would stay open on every page after, and since opening it **is** the
 * CustomResourceDefinition listing, every later arrival on the cluster would pay
 * for it too. The dock draws the same line: a pin on one cluster is a visit and
 * is never remembered, the `*` scope is a mode and is (`dock/DockContext.tsx`).
 *
 * So there are two things here and only one of them is stored:
 *
 * - **the arrangement** - what the person opened or closed with their own
 *   hand, under `KEY`, outliving the window it was made in;
 * - **the visit** - the one group held open because the page on screen is in
 *   it. In memory only, replaced by the next arrival, ended by a gesture on that
 *   heading, and never written. Leaving the page shuts the group again unless
 *   the arrangement holds it.
 *
 * The visit is module state for the arrangement's reason: it has to survive an
 * unmount of the column, and `Menu.tsx` reads it through the same subscription.
 *
 * ## Each window keeps its own menu
 *
 * A window's arrangement is held in memory, here, and a fold in one window
 * never moves the menu in another open window. A new window - a new tab, a
 * reload, a return to the console - starts from the last arrangement any window
 * saved (the test `starts a third window from the last arrangement any window
 * saved` in `menuMemory.test.ts` pins that start).
 *
 * There is no shared arrangement that one window could overwrite with a stale
 * copy, only each window's own and a starting point for the next one, and the
 * rules that keep that honest are:
 *
 * - **only a gesture writes, and it writes the gesturing window's own
 *   arrangement whole** - every key in what is stored is one that window draws
 *   open. Never a merge with storage, which would put another window's folds
 *   into this one's screen on its next gesture and into the next window's
 *   start;
 * - **an arrival, a visit or a redraw writes nothing** (below), so a window
 *   that is merely open never writes its arrangement over the one another
 *   window saved since;
 * - **nothing listens to `storage`.** A window does not read the arrangement
 *   again after its first read, so a write elsewhere cannot reach its screen,
 *   and a storage that refused this window's writes cannot make it lose its
 *   own gestures.
 *
 * Two windows writing in turn: the stored arrangement is the last one's, which
 * is the start the next window gets and nothing else. Only the arrangement
 * travels even that far - the visit is this window's page and nobody else's.
 *
 * The one storage key, spelled for this product. `sessionStorage` is
 * deliberately not used: a freshly installed custom resource should appear
 * after a reload, and this is the arrangement rather than the content, which
 * should outlive the window it was made in.
 */
const KEY = 'kubetower.menu.open';

/**
 * The snapshot, cached.
 *
 * `useSyncExternalStore` compares snapshots by identity and re-renders forever
 * if a fresh array comes back each time, so the parse is cached and the result
 * is replaced only by this window's own gestures (*Each window keeps its own
 * menu* above). `undefined` means "not read from storage yet"; `null` means
 * "read, and no window of this browser has saved an arrangement".
 */
let snapshot: readonly string[] | null | undefined;

/** The group held open for the page on screen, if any. Never written to storage:
 *  see *A visit is not an arrangement* above. */
let visiting: string | undefined;

const listeners = new Set<() => void>();

/** The remembered group keys, or `null` when this browser has never arranged the
 *  menu. Unreadable, absent and nonsense all answer `null` - the safe direction,
 *  because `null` shows everything. */
export function openGroups(): readonly string[] | null {
  if (snapshot === undefined) snapshot = readStorage();
  return snapshot;
}

/** Told of this window's own changes - a gesture or a visit - and of nothing
 *  else: no `storage` listener, so another window's write never redraws this
 *  menu. */
export function subscribeToOpenGroups(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * Open or close one group, which is the person's gesture and the only thing
 * that writes the arrangement.
 *
 * `arrangedOpen` is what **the arrangement draws open** - `groupIsOpen` without
 * the visit - and it is the base the first gesture against an unarranged menu
 * writes from. That base is empty, because `defaultOpen` opens nothing, so the
 * first gesture writes exactly the group it was about: opening Network on a
 * fresh browser stores `["network"]`.
 *
 * It stays a required parameter rather than a constant `[]` for the day the
 * default depends on the group again: getting it wrong would either shut the
 * other groups on the next reload or write nothing for an opened deferred
 * heading. The caller computes it from `defaultOpen`, so the two cannot
 * disagree.
 *
 * **And the visit is left out of it.** On
 * a custom resource page the heading is on screen because of the visit; a
 * first gesture on some other group, handed what is on screen, would write
 * that heading down with it - the same leak through a different door.
 *
 * Closing the group being visited ends the visit, so the heading shuts under the
 * person's hand and stays shut while they stand there. Nothing re-opens it
 * until they arrive somewhere else: a group closed while standing in it stays
 * closed.
 */
export function setGroupOpen(key: string, isOpen: boolean, arrangedOpen: readonly string[]): void {
  const ended = !isOpen && visiting === key;
  if (ended) visiting = undefined;
  // This window's own arrangement, not storage: what another window saved since
  // is that window's, and merging it in would change this menu.
  const base = openGroups() ?? arrangedOpen;
  if (base.includes(key) === isOpen) {
    if (ended) notify();
    return;
  }
  commit(isOpen ? [...base, key] : base.filter((entry) => entry !== key));
}

/**
 * The group holding the page that has just been arrived at, held open for the
 * visit and **not written down** - `undefined` when the page is in no group.
 *
 * See *A visit is not an arrangement* above. The case this rule exists for: a
 * custom resource page, whose heading is deferred and therefore shut, and which
 * must not be arrived at folded away.
 *
 * A no-op when the visit is already this group, because notifying every
 * subscriber for nothing re-renders the column for nothing.
 */
export function visitGroup(key: string | undefined): void {
  if (visiting === key) return;
  visiting = key;
  notify();
}

/** The group held open for the page on screen, or `undefined`. Read by
 *  `Menu.tsx` through `useSyncExternalStore`, beside the arrangement; a string
 *  compares by value, so there is no snapshot to cache. */
export function visitedGroup(): string | undefined {
  return visiting;
}

/**
 * Whether a group is open on a browser that has never arranged the menu.
 *
 * **None of them.** The group holding the page is open because the visit holds
 * it (`visitGroup`), and every other heading waits to be asked for, rather than
 * a column of open headings with the page somewhere inside it. A browser with no
 * arrangement has learned nothing, and the page it arrived on is never folded
 * away.
 *
 * The deferred group has its own reason too: opening it *is* the fetch, so
 * every arrival at every cluster would list that cluster's
 * CustomResourceDefinitions - tens of megabytes on a large cluster - for a
 * heading nobody clicked.
 *
 * `group` stays a parameter so the rule can again depend on the group without
 * every caller changing.
 */
export function defaultOpen(_group: NavGroup): boolean {
  return false;
}

/** Whether one group is drawn open, given the arrangement and the visit. `null`
 *  is the browser that has never arranged anything - see `defaultOpen`. Called
 *  without `visit`, it is the arrangement alone, which is what `setGroupOpen`
 *  is handed. */
export function groupIsOpen(
  group: NavGroup,
  arrangement: readonly string[] | null,
  visit?: string,
): boolean {
  if (visit !== undefined && group.key === visit) return true;
  return arrangement === null ? defaultOpen(group) : arrangement.includes(group.key);
}

function commit(keys: readonly string[]): void {
  snapshot = keys;
  try {
    localStorage.setItem(KEY, JSON.stringify([...keys]));
  } catch {
    // Private windows, blocked site data, a locked-down profile. The
    // arrangement then lasts as long as the window, which is the memory being
    // lost and never the menu. Not reported: there is nothing the person
    // could do about it and nothing on screen is wrong.
  }
  notify();
}

function notify(): void {
  for (const listener of [...listeners]) listener();
}

function readStorage(): readonly string[] | null {
  try {
    return parseStored(localStorage.getItem(KEY));
  } catch {
    return null;
  }
}

/** What is stored, reduced to what this module could have written. Absent and
 *  nonsense both answer `null`, never throw: only reading storage itself does. */
function parseStored(raw: string | null): readonly string[] | null {
  if (raw === null) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!Array.isArray(parsed)) return null;
  return parsed.filter((entry): entry is string => typeof entry === 'string');
}

/** Test seam. Leaves the module as a fresh import would - the cached snapshot
 *  included, or one test's arrangement is still in memory for the next. */
export function resetOpenGroups(): void {
  snapshot = undefined;
  visiting = undefined;
  listeners.clear();
}

/**
 * Whether this group holds the page that is open.
 *
 * The group holding the current page outranks a remembered arrangement:
 * following a link must not land you somewhere invisible in the menu. This
 * function is that rule, and `Menu.tsx` hands the answer to `visitGroup` on
 * arrival.
 *
 * **The query string is cut off before anything is compared**, and that is not a
 * tidying. `viewMemory.remembered` in `internal/web/views.go` appends the sort
 * last used to a kind's bare path, so that arriving from the menu does not undo
 * a sort - so a menu item's `href` is `/c/prod/pods?sort=age` as
 * often as it is `/c/prod/pods`. Comparing the whole string would silently stop
 * matching the moment somebody sorted a table, which is the kind of failure
 * that never errors and is blamed on the memory.
 *
 * The prefix match with a `/` boundary is what makes a detail route under a kind
 * count as being in that kind's group. Bare `startsWith` would put `/c/p/podsx`
 * in the `pods` group.
 *
 * **The cluster segment is not compared either.** A cluster's path has one
 * spelling (`clusters/clusterPath.ts` and its Go twin), but an address typed or
 * saved by hand may carry another - `/c/kubernetes-admin%40kubernetes/pods`
 * against `/c/kubernetes-admin@kubernetes/pods` - and the server still resolves
 * it. Compared whole, no loaded group would hold the page,
 * `deferredGroupHoldsPath` would conclude it was a custom resource page, and
 * every built-in page reached that way would open *Custom Resources* and fire
 * the CustomResourceDefinition listing. Both spellings resolve to the
 * same context on the server (`Manager.Resolve`), and the menu is by
 * construction the menu of the cluster in the address, so the segment carries
 * no information this comparison needs. What is compared is the path under it.
 */
export function groupHoldsPath(group: NavGroup, pathname: string): boolean {
  const here = underCluster(pathname);
  const held = group.items.some((item) => {
    const href = pathOf(item.href);
    if (href === '') return false;
    // The Overview entry is the bare cluster route, and it holds that page
    // alone. As a prefix it would hold every page on the cluster, so the
    // `Cluster` group - first in the menu - would be "the group holding the
    // page" on every arrival, and a custom resource page would never be seen
    // as inside the deferred heading.
    if (isOverviewHref(href)) return here === '';
    const there = underCluster(href);
    return here === there || here.startsWith(`${there}/`);
  });
  // And one level down, because the custom resource kinds are all there - grouped
  // by API group inside a single heading. A check that looked only at `items`
  // would answer "no" for every custom kind and fold the heading away on exactly
  // the page that is inside it.
  return held || (group.groups ?? []).some((sub) => groupHoldsPath(sub, pathname));
}

/**
 * Whether a deferred group is where this page must be, before its items exist.
 *
 * `groupHoldsPath` cannot answer for a group whose contents have not been fetched
 * - there is nothing to compare a location against - and the page that most needs
 * the answer is a custom resource page, where the heading is deferred by
 * definition. So the question is asked from the other side, which is exactly what
 * the server's `onACustomKind` does: a kind route that **no loaded group claims**
 * is not a built-in kind, and the only place left for it is the deferred one.
 *
 * Asked of the menu rather than of the slug's shape, for `customresources.go`'s
 * reason: a custom slug is `plural.group` and a built-in one has no dot, so a dot
 * test would work today and be a syntactic guess about a semantic question.
 *
 * The cost of being wrong is one fetch on a mistyped address, which is the same
 * fetch that address's own page is already making.
 *
 * **`reserved` is what the route table mounts under `/c/:cluster` that is not
 * a kind**, and it is a parameter rather than a list kept here. The shape test
 * alone says `/c/prod/helm` is a kind route, no loaded group claims it - Helm is
 * not a kind - and the only place left would be the deferred one: arriving on
 * Helm would open the heading and fire the CustomResourceDefinition listing the
 * deferral exists to avoid. The same holds for an extra's `/x/…` page.
 * `router/routes.tsx` owns the list, because it is
 * the file that adds such a page, and a copy here would be right until it did.
 */
export function deferredGroupHoldsPath(
  groups: readonly NavGroup[],
  pathname: string,
  reserved: readonly string[],
): string | undefined {
  const first = KIND_ROUTE.exec(pathname)?.[1];
  // One whole segment, not a prefix: `helmreleases.helm.toolkit.fluxcd.io` is
  // a kind Flux installs, and it is a custom one.
  if (first === undefined || reserved.includes(first)) return undefined;
  if (groups.some((group) => groupHoldsPath(group, pathname))) return undefined;
  return groups.find((group) => group.deferred)?.key;
}

/** `/c/{cluster}/{kind}`, with anything under it - an object route included,
 *  and the first segment under the cluster captured so that a reserved page can
 *  be told from a kind. The cluster overview, `/health` and `/settings` do not
 *  match at all; Helm and an extra's `/x/…` page match and are excluded by
 *  `reserved`, above. */
const KIND_ROUTE = /^\/c\/[^/]+\/([^/]+)/;

/** `/c/{cluster}` and nothing under it: the dashboard, which is the one menu
 *  entry with no kind segment. The query string is cut off first, as
 *  everywhere else in this module. */
export function isOverviewHref(href: string | undefined): boolean {
  return /^\/c\/[^/]+\/?$/.test(pathOf(href));
}

function pathOf(href: string | undefined): string {
  if (href === undefined) return '';
  return href.split(/[?#]/)[0] ?? '';
}

/** What a path says under `/c/{cluster}`: `''` for the dashboard, `/pods/…` for
 *  everything below it, and the path unchanged when it is not under a cluster at
 *  all (`/health`, `/settings/…`), so such a page still matches nothing. One
 *  trailing `/` is dropped, the spelling `isOverviewHref` already accepts. See
 *  `groupHoldsPath` for why the segment itself is not compared. */
function underCluster(path: string): string {
  const match = /^\/c\/[^/]+(\/.*)?$/.exec(path);
  if (match === null) return path;
  const rest = match[1] ?? '';
  return rest.endsWith('/') ? rest.slice(0, -1) : rest;
}

/* -----------------------------------------------------------------------------
   Whether the kinds column is shown, which is the second thing this module
   remembers.

   The column is shown, as it is, or hidden: `kubetower.menu.mode` = 'shown' |
   'hidden'. An earlier build stored a 48px icons-only fold under
   `kubetower.menu.folded`; **a folded value migrates to shown**, because folded
   meant "I want it narrow", not "I do not want this column". The old key is
   removed on the first read, so it is not left to be misread later.

   **Beside the open groups and not inside them**, which is a decision rather
   than a convenience. The arrangement above is a JSON array of group keys and
   is already in people's browsers; making it an
   object to carry one more value would mean every existing browser reads a
   shape its parser rejects, `readStorage` answers `null`, and `null` means
   *never arranged* - so every arrangement would be silently
   discarded by an unrelated feature. A second key is free and cannot do that.

   Everything else here is the same bargain as above, for the same reasons:
   reads and writes are guarded because storage may not be there at all; the
   snapshot is cached because `useSyncExternalStore` compares by identity; the
   store is module level because it has to outlive every unmount of the column.

   The default is **shown**, and that is `null`'s meaning here as it is above:
   nothing a person can see today may disappear because a feature was added.
----------------------------------------------------------------------------- */

const MODE_KEY = 'kubetower.menu.mode';
/** The key the 48px fold was stored under, read once to be migrated away. */
const FOLD_KEY = 'kubetower.menu.folded';

let hiddenNow: boolean | undefined;
const modeListeners = new Set<() => void>();

function readMode(): boolean {
  try {
    const stored = localStorage.getItem(MODE_KEY);
    if (stored === null && localStorage.getItem(FOLD_KEY) !== null) {
      // Folded or not, the old value means the column was wanted: shown.
      localStorage.removeItem(FOLD_KEY);
    }
    return stored === 'hidden';
  } catch {
    return false;
  }
}

/** Whether the kinds column is hidden. `false` until something says
 *  otherwise - unreadable, absent and nonsense all answer shown, the column as
 *  it is today. */
export function menuIsHidden(): boolean {
  if (hiddenNow === undefined) hiddenNow = readMode();
  return hiddenNow;
}

export function subscribeToMenuMode(listener: () => void): () => void {
  modeListeners.add(listener);
  return () => {
    modeListeners.delete(listener);
  };
}

export function setMenuHidden(next: boolean): void {
  if (menuIsHidden() === next) return;
  hiddenNow = next;
  try {
    localStorage.setItem(MODE_KEY, next ? 'hidden' : 'shown');
  } catch {
    // The choice then lasts as long as the tab. The memory is lost and never
    // the column, and there is nothing the person could do about it.
  }
  for (const listener of [...modeListeners]) listener();
}

/** Test seam, and the same contract as `resetOpenGroups`: leaves the module as
 *  a fresh import would, cached value included. */
export function resetMenuMode(): void {
  hiddenNow = undefined;
  narrowOpen = false;
  modeListeners.clear();
}

/*
  The kinds column on a phone-width window.

  At 390px the 192px column beside the 56px rail left the page about 150px,
  and every page inside a cluster was unreadable. Below `sm` the column
  therefore does not share the width: it is closed by default, whatever the
  wide window remembers, and opened **over** the page by the Kinds button;
  following a kind, Escape or a tap beside it closes it again. Nothing of this
  is stored - a phone that opens the console starts closed - and the wide
  window's own choice above is left untouched.

  **Below 1000px, not only on a phone.** At 900px the column took 192px from a
  list that had none to spare, so it folds there the same way, and opens over
  the page on the button.
  1000 and not 1024: the 1024px window keeps its column, which is the width
  the layout suite measures the drawer and the lists at.
*/

/** Below 1000px: the width under which the kinds column folds (#318, #263). */
export const NARROW_QUERY = '(max-width: 999.98px)';

let narrowOpen = false;

function narrowMedia(): MediaQueryList | undefined {
  return typeof window === 'undefined' || typeof window.matchMedia !== 'function'
    ? undefined
    : window.matchMedia(NARROW_QUERY);
}

/** Whether the window is phone-width now. */
export function windowIsNarrow(): boolean {
  return narrowMedia()?.matches ?? false;
}

function subscribeToNarrow(listener: () => void): () => void {
  const media = narrowMedia();
  // A window widened with the column open forgets it: narrowed again, it
  // starts closed, as a phone does.
  const changed = (): void => {
    if (!windowIsNarrow()) narrowOpen = false;
    listener();
  };
  modeListeners.add(listener);
  media?.addEventListener('change', changed);
  return () => {
    modeListeners.delete(listener);
    media?.removeEventListener('change', changed);
  };
}

/** Whether the column is hidden, on either width: the stored choice when the
 *  window is wide, closed-unless-opened when it is narrow. */
function hiddenEither(): boolean {
  return windowIsNarrow() ? !narrowOpen : menuIsHidden();
}

export function useMenuHidden(): boolean {
  return useSyncExternalStore(subscribeToNarrow, hiddenEither, hiddenEither);
}

export function useWindowNarrow(): boolean {
  return useSyncExternalStore(subscribeToNarrow, windowIsNarrow, windowIsNarrow);
}

/** Opens or closes the column for the width the window has now. */
export function setColumnHidden(next: boolean): void {
  if (!windowIsNarrow()) {
    setMenuHidden(next);
    return;
  }
  if (narrowOpen === !next) return;
  narrowOpen = !next;
  for (const listener of [...modeListeners]) listener();
}
