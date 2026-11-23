import { useEffect, useId, useRef, useState, useSyncExternalStore, type ComponentProps, type ReactNode } from 'react';
import { Link, NavLink, useLocation, useMatch, useParams, useSearchParams } from 'react-router';

import { useIntentPrefetch } from '../router/prefetch';

import { useMutation, useQueryClient } from '@tanstack/react-query';

import {
  queryKeys,
  setRailBar,
  useCapabilities,
  useClusters,
  useKeepsPreferences,
  usePreferences,
  type Cluster,
  type ListResult,
} from '../api';
import { clusterPath } from '../clusters/clusterPath';
import { Icon } from '../components/Icon';
import { focusRing } from '../focus';
import { ListView } from '../state/Result';
import { CLUSTER_PARAM } from '../pages/settings/data';
import { OPEN_PARAMETER, openValue } from '../pages/object/address';
import {
  listedAsNamespaced,
  useChosenNamespaces,
  watchNamespaceMemory,
  withNamespaces,
} from './namespaceMemory';
import { lastKind, lastOpen, watchViewMemory } from './viewMemory';
import { useFirstIcons } from './firstIcon';
import { iconOf } from './iconRecipe';
import { ClusterIcon } from './ClusterIcon';
import { arrange, stateBadgeClass, stateSentence } from './clusterState';
import { ClusterEditor } from './ClusterEditor';
import { DOWN } from './downCluster';
import { KindsToggle } from './KindsToggle';
import { useKindsAt } from './railDrawn';
import { RailArrange } from './RailArrange';
import { SessionTile } from './SessionTile';

/**
 * The cluster rail.
 *
 * Outside `<Outlet/>`, so switching page never re-renders it: the router does
 * that structurally.
 *
 * ## Always 56px, and there is no fold
 *
 * `w-14` at every width: the rail reads like Lens, a strip of small squares,
 * always compact. There is no fold, no stored width and no `--spacing-rail`.
 *
 * A tile is a square carrying the server's `Cluster.initials`, with the
 * cluster's state as a badge in its corner. The name is never printed on the
 * rail: it is the tile's `title` and its accessible name, whole, which is also
 * why a long cluster name cannot wrap here - there is no line to wrap.
 *
 * ## Every arm of the envelope is drawn
 *
 * Through `<ListView>`, so a rail that could not read its configuration says so
 * where the tiles would be. A rail that renders nothing is indistinguishable
 * from a machine with no clusters configured, and the difference between those
 * two is the whole reason this product has a six-state vocabulary.
 */
export function Rail(): ReactNode {
  const clusters = useClusters();
  const preferences = usePreferences();
  const [arranging, setArranging] = useState(false);
  // The tile being edited by right-click, with where it is, and
  // the tile itself so the keyboard goes back to it when the editor closes.
  const [editing, setEditing] = useState<{ name: string; top: number; right: number } | null>(null);
  const editedTile = useRef<HTMLElement | null>(null);
  // Arrange and the bar switcher are both writes. Where the server keeps no
  // preferences neither is drawn, and the rail is the defaults for everybody
  // (`Capabilities.preferences`): several replicas, and the server profile, which
  // serves one cluster and where one person's nameplates and hidden tiles
  // would be everybody's rail.
  //
  // **And neither until the capabilities have answered.**
  // `useKeepsPreferences` reads unknown as true, which is right for drawing and
  // wrong for writing: a click in that window under the server profile would
  // open a dialog whose writes are not registered. `ResourceList.tsx` guards its own
  // writes the same way, with `decided`. Settled means answered or failed; a
  // failed read is an older server, which keeps them.
  const capabilities = useCapabilities();
  const decided = !capabilities.isPending;
  const keeps = useKeepsPreferences() && decided;

  const rail = preferences.data?.rail;
  // A cluster with no icon stored is given one drawn at random, and it is
  // stored. Not before the preferences have answered:
  // until then every cluster looks like one with no icon.
  useFirstIcons(
    clusters.data?.state === 'ok' ? clusters.data.items : undefined,
    rail?.nameplates,
    keeps && preferences.isSuccess && rail !== undefined,
  );
  const barCount = rail?.barCount ?? 1;
  const activeBar = rail?.activeBar ?? 1;
  // Not while preferences are in flight: "another bar than the active one" has
  // no meaning before the active one is known, and the tiles stack anyway.
  const switcher = rail !== undefined && anotherBarPopulated(clusters.data, activeBar);
  // Kinds waits until nothing above it or around it can still change.
  // **The cluster list**: until it answers, the rail is
  // drawn on a cold load even where one cluster will take it away
  // (`railDrawn.ts`), and Kinds would jump from this foot to the top bar.
  // **The capabilities and the preferences**: Arrange and the bar switcher wait
  // for them and sit above Kinds, so Kinds would move down when they arrived,
  // and a click aimed at it could land on Arrange. Settled means answered or
  // failed once - `isFetched`, not `!isPending`: a read that failed goes back to
  // pending while it is asked again, and Kinds' own observer of the list asks
  // again on mount, so `!isPending` would draw it and take it away in a loop.
  //
  // **For `KINDS_WAIT_MS` at most**: `request.ts`
  // sets no timeout, so a read that hangs would hide Kinds for good, and the
  // control that brings a hidden kinds column back must not wait on a server
  // that never answers. Every read answers long before that in the common
  // case, which keeps the cold load free of the jump.
  const [waitedOut, setWaitedOut] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => {
      setWaitedOut(true);
    }, KINDS_WAIT_MS);
    return () => {
      clearTimeout(timer);
    };
  }, []);
  const kindsAt = useKindsAt();
  const kindsSettled =
    waitedOut || (clusters.isFetched && capabilities.isFetched && preferences.isFetched);

  return (
    <div
      id="rail"
      aria-label="Clusters"
      // 56px at every width: see the note above. One number, and no breakpoint
      // or stored preference can choose another.
      className="flex w-14 shrink-0 flex-col border-r border-line bg-panel"
    >
      {/*
        The scroll is inside, on the tiles, for `Menu.tsx`'s reason: a control
        at the foot of a scroll region is a control most
        people never see, and fourteen clusters is enough to push Arrange -
        and the session - below the fold of the rail itself. `Shell` gives this
        column a definite height (`h-dvh`, `overflow-hidden`), so a flex column
        with the tiles as its one growing, scrolling child pins the controls by
        the layout rather than by a `sticky` that has to be believed.

        `overflow-x-hidden` is the belt: one long nameplate must not put a
        horizontal scrollbar under a 56px column.
      */}
      <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-x-hidden overflow-y-auto p-2">
        <ListView query={clusters} what="clusters" compact>
          {(items) => (
            <Tiles
              clusters={items}
              bar={rail === undefined ? undefined : activeBar}
              icons={rail?.nameplates}
              // Only where the nameplate write exists: elsewhere a right-click
              // is the browser's own menu, as it is on any link.
              onEdit={
                keeps
                  ? (name, tile) => {
                      const box = tile.getBoundingClientRect();
                      editedTile.current = tile;
                      setEditing({ name, top: box.top, right: box.right });
                    }
                  : undefined
              }
            />
          )}
        </ListView>
      </div>

      {/*
        The foot, top to bottom: the bar switcher, Arrange, Kinds and the
        session (`KindsToggle.tsx`). Each of the four can be absent - no second bar, no
        preferences, no cluster on screen, no way to sign back in - so a foot
        with none of them is `empty:hidden` rather than a ruled-off strip of
        padding.
      */}
      <div className="flex flex-col gap-2 border-t border-line p-2 empty:hidden">
        {switcher && keeps ? <BarSwitcher count={barCount} active={activeBar} /> : null}
        {keeps ? (
          <button
            type="button"
            onClick={() => setArranging(true)}
            // `aria-label`, because the button is its glyph alone: the rail is
            // 56px at every width, so the word "Arrange" is never printed here
            // and the name it would have given is said by the label instead.
            aria-label="Arrange"
            title="Arrange the rail"
            // `.rail-edit`, app.css:137.
            //
            // `hover:bg-panel-2` is the console's answer for a control that
            // is a glyph with no word - the kinds menu's kind links and Kinds just
            // below this one carry it - rather than `hover:text-fg`, which paints
            // nothing on a glyph whose fill is not `currentColor`.
            //
            // **This arm is not one of the three hover families.** That rule
            // gives a hover per family - the accent frame for
            // a control that watches something (Logs, the pod shell, the node
            // shell; `hover.ts`), Claude's own `--color-claude` for the mark, the
            // frame to full `--bad` for what destroys (`destructive.ts`). Arrange
            // opens a panel for reordering the rail: it watches nothing, it
            // destroys nothing, and it is not Claude.
            //
            // So it stays on the neutral treatment, with the other controls
            // outside all three arms - and `hover:bg-panel-2` rather than
            // `hover:text-fg` because of what it *is*, a control that folds down
            // to a glyph.
            //
            // The accent frame is the one thing it must not take, and the reason
            // is specific rather than tidiness: `border-accent` already means
            // *this is the one you are on* everywhere in this column - the active
            // cluster tile two functions below, and the bar switcher's current
            // bar. A hover in the same color would make every tile look selected
            // under the pointer. `Rail.test.tsx` pins the refusal.
            className={`flex min-h-6 items-center justify-center rounded-md border border-line px-1 py-1 text-xs text-muted hover:bg-panel-2 pointer-coarse:min-h-11 ${focusRing}`}
          >
            {/* The glyph alone, at every width: a 56px column has no room for
                the word, and a button whose label is cut mid-word is worse than
                one drawn as its icon with the word on `title` and in the
                accessible name. */}
            <Icon name="edit" />
          </button>
        ) : null}
        {kindsSettled && kindsAt === 'rail' ? <KindsToggle form="rail" /> : null}
        <SessionTile />
      </div>

      {arranging && keeps ? <RailArrange onClose={() => setArranging(false)} /> : null}
      {editing !== null && keeps ? (
        <EditorFor
          name={editing.name}
          at={editing}
          clusters={clusters.data}
          onClose={() => {
            setEditing(null);
            editedTile.current?.focus();
          }}
        />
      ) : null}
    </div>
  );
}

/** The editor for the cluster named, from the list on screen - and nothing
 *  when the list no longer holds it, which closes the editor rather than
 *  drawing one for a cluster that has gone. */
function EditorFor({
  name,
  at,
  clusters,
  onClose,
}: {
  name: string;
  at: { top: number; right: number };
  clusters: ListResult<Cluster> | undefined;
  onClose: () => void;
}): ReactNode {
  const cluster = clusters?.state === 'ok' ? clusters.items.find((one) => one.name === name) : undefined;
  return cluster === undefined ? null : <ClusterEditor cluster={cluster} at={at} onClose={onClose} />;
}

/**
 * The hotbar switcher, and the thing that turns a stacked rail back into a rail.
 *
 * **`barCount` is the server's and is read rather than assumed.** The contract
 * says so in as many words: *"a server constant, sent because the bar switcher
 * has to draw exactly that many digits, and a client that assumed a number would
 * be wrong in the direction that hides a bar's worth of clusters."* That is
 * about *how many digits*.
 *
 * ## Whether it is drawn at all is a different question, and it is not `barCount`
 *
 * `barCount` is a server *constant* and is `> 1` on every machine, so drawing
 * whenever `barCount > 1` would show the digits to people who never touched
 * Arrange.
 *
 * So the condition is `anotherBarPopulated()`: at least one cluster carries a
 * `bar` other than the one on screen. A cluster reaches another bar only through
 * Arrange's `Bar` field, so the switcher appearing is a direct consequence of
 * somebody having used it, and a fresh profile sees no digits at all. And it sits
 * at the foot of the rail, in the `mt-auto` block with Arrange and the session
 * tile - the controls, below the tiles, rather than a caption above them.
 *
 * The write is optimistic in the only sense that matters here: the preferences
 * are re-read after it, and until they answer the rail keeps drawing the bar it
 * was drawing. A switcher that moved the tiles before the server agreed would
 * show an arrangement nothing has stored.
 */
function BarSwitcher({ count, active }: { count: number; active: number }): ReactNode {
  const client = useQueryClient();
  const choose = useMutation({
    mutationFn: (bar: number) => setRailBar(bar),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: queryKeys.preferences });
    },
  });

  return (
    // Centered and wrapping, because 56px is the only width there is: three
    // digits do not fit across it, so they wrap into a short column rather than
    // being clipped. Centering is what makes the
    // wrapped column read as digits under each other rather than as a ragged
    // left edge.
    <div role="group" aria-label="Hotbar" className="flex flex-wrap justify-center gap-1">
      {Array.from({ length: count }, (_, index) => index + 1).map((bar) => (
        <button
          key={bar}
          type="button"
          aria-pressed={bar === active}
          aria-label={`Hotbar ${String(bar)}`}
          disabled={choose.isPending}
          title={choose.isPending ? 'Switching hotbar…' : undefined}
          onClick={() => choose.mutate(bar)}
          // A 24px box (44px tall under a coarse pointer): the digit alone was 20px.
          className={`inline-flex size-6 items-center justify-center rounded-md border text-2xs disabled:cursor-not-allowed disabled:opacity-50 pointer-coarse:h-11 ${focusRing} ${
            bar === active ? 'border-accent text-fg' : 'border-line text-muted hover:text-fg'
          }`}
        >
          {bar}
        </button>
      ))}
    </div>
  );
}

/**
 * Whether the switcher has anywhere to go: a cluster on a hotbar other than the
 * one on screen.
 *
 * A cluster with no `bar` is on no hotbar - `api/openapi.yaml`: absent "is what
 * `hidden` also says and is the state a hidden tile is in" - so it does not
 * populate a bar and does not count. And a list that is not `ok` has no clusters
 * to count: the envelope's own arm says why there are no tiles, and a switcher
 * over that notice would be a control for reaching bars of nothing.
 */
function anotherBarPopulated(clusters: ListResult<Cluster> | undefined, activeBar: number): boolean {
  if (clusters === undefined || clusters.state !== 'ok') return false;
  return clusters.items.some((cluster) => cluster.bar !== undefined && cluster.bar !== activeBar);
}

function Tiles({
  clusters,
  bar,
  onEdit,
  icons,
}: {
  clusters: readonly Cluster[];
  /** The stored nameplates, for their icon recipes. */
  icons?: Readonly<Record<string, { icon?: string }>> | undefined;
  /** Opens the nameplate editor for a tile; absent where nothing is kept. */
  onEdit?: ((name: string, tile: HTMLElement) => void) | undefined;
  /** The hotbar on screen. `undefined` while preferences are still in flight,
   *  which is the one case that still stacks - and it stacks rather than drawing
   *  an empty rail, because an empty rail reads as "no clusters". */
  bar?: number | undefined;
}): ReactNode {
  const arranged = arrange(clusters).filter((cluster) => cluster.hidden !== true);
  // On Settings > Extra the icons choose the cluster the page is about: the
  // address's `?cluster=` when it names one, else the first, as the tab does.
  const onExtra = useMatch('/settings/extra') !== null;
  const asked = useSearchParams()[0].get(CLUSTER_PARAM);
  const ordered = arrange(clusters);
  const chosen = onExtra ? (ordered.find((one) => one.name === asked) ?? ordered[0])?.name : undefined;
  // One bar, once preferences have answered. A tile with no bar is on no hotbar,
  // which the contract says `hidden` also says - so it is drawn on none of them
  // rather than on all of them.
  const visible = bar === undefined ? arranged : arranged.filter((cluster) => cluster.bar === bar);

  // Not the same as an empty list from the server, and it must not be drawn as
  // one: this is a rail the person emptied, and the way out of it is the
  // Arrange panel rather than a support conversation about a missing cluster.
  if (visible.length === 0) {
    return (
      <p role="status" className="px-1 py-1 text-xs text-muted">
        Every tile is hidden. Use Arrange.
      </p>
    );
  }

  /*
    The heading is `Cluster.bar`: a break between one run of tiles and the
    next, the **hotbar** - several numbered, ordered lists of tiles, which is
    what `arrange()` sorts by. The rail shows one hotbar with a switcher
    (`BarSwitcher`, at the foot of the rail).

    The headings are for the one case that still stacks: **preferences in
    flight**. Drawing an empty rail there would read as "no clusters", and the
    alternative to stacking with a visible break is stacking without one, which
    silently merges two hotbars into a plausible-looking third - the same family
    of defect as a list drawing "could not ask" as "nothing here".

    Drawn only when the visible tiles span more than one bar, so a machine using
    one hotbar - which is most of them - sees no heading at all rather than a
    "Bar 1" that answers a question nobody asked.

    **A rule rather than a word**: the rail is 56px at every width, so the
    break is a separator line with the heading on its `title` and its
    accessible name.

    The decision is made before the map and not inside it. A variable carried
    across iterations of a render callback is state that outlives the render it
    was computed in, which `react-hooks/immutability` refuses - and it is right
    to: under a re-render the second pass starts from a value the first pass left
    behind and the headings move.
  */
  const bars = new Set(visible.map((cluster) => cluster.bar));
  const heads =
    bars.size < 2
      ? visible.map(() => false)
      : visible.map((cluster, index) => index === 0 || cluster.bar !== visible[index - 1]?.bar);

  return (
    <>
      {visible.map((cluster, index) => (
        <div key={cluster.name}>
          {heads[index] === true ? (
            <div
              role="separator"
              aria-label={barHeading(cluster.bar)}
              title={barHeading(cluster.bar)}
              className={`mx-1 mb-2 border-t border-line ${index === 0 ? 'hidden' : ''}`}
            />
          ) : null}
          <Tile
            cluster={cluster}
            onEdit={onEdit}
            icon={icons?.[cluster.name]?.icon}
            settings={onExtra ? { chosen } : undefined}
          />
        </div>
      ))}
    </>
  );
}

/** A tile with no bar is on no hotbar, which `api/openapi.yaml` says "is what
 *  `hidden` also says". One that is drawn anyway is worth naming rather than
 *  filing under a number it does not have. */
function barHeading(bar: number | undefined): string {
  return bar === undefined ? 'On no bar' : `Bar ${String(bar)}`;
}

function Tile({
  cluster,
  onEdit,
  icon,
  settings,
}: {
  cluster: Cluster;
  /** Set on Settings > Extra: the icon then picks the cluster the page shows
   *  (`chosen` is the one shown) instead of going to the cluster's page. */
  settings?: { chosen: string | undefined } | undefined;
  onEdit?: ((name: string, tile: HTMLElement) => void) | undefined;
  /** The stored icon recipe, if any. Without one the tile
   *  is the cluster's default icon, on its color. */
  icon?: string | undefined;
}): ReactNode {
  const recipe = iconOf(cluster.name, cluster.colour, icon);
  /*
    Back to where the person was, per cluster, and to the overview the first
    time.

    **Read through `useSyncExternalStore`** rather than called directly: the
    store changes while this component is mounted — every list page writes to it
    as it opens — and a tile holding a value it read once would keep linking to
    whichever kind happened to be current when the rail first rendered.

    The cluster's own page is the fallback and is not a guess: it is where a
    cluster with no history should land.

    **Except on the tile of the cluster already on screen, which goes to the
    overview.** The memory is for the *switch back*: leave a cluster, come
    back, land where you were. On the tile you are already on, the remembered
    kind would make the click a no-op, with no gesture that reaches the
    dashboard. So the
    active tile links to the bare `/c/<cluster>`. The memory is not touched: it
    is read on the next switch back, which is the case it exists for, and both
    rules hold because they answer different clicks.

    **And to the drawer that was open there**: `?open=` on the kind's
    address, which is the arrival the list already reads for a relation
    followed from the drawer. The destination page re-opens the object for its
    own cluster; nothing is carried across, and `Drawer.tsx` still empties on
    the switch. Only where the remembered object is on the remembered kind - the
    store leaves it behind on a kind change, and the guard here is against a
    hand-edited storage saying otherwise.

    **And a cluster the probe found unreachable goes to its own address**
   : the remembered kind is a list that cannot be
    read, and landing there would carry the error. `/c/<cluster>` is where the warning and Refresh
    are drawn (`ClusterDown`). The memory is not touched, so the first switch
    back once the cluster answers again lands where it was left.
  */
  const remembered = useSyncExternalStore(
    watchViewMemory,
    () => lastKind(cluster.name),
    // The server renders nothing, and a store read there would reach a
    // `localStorage` that does not exist.
    () => undefined,
  );
  const open = useSyncExternalStore(
    watchViewMemory,
    () => lastOpen(cluster.name),
    () => undefined,
  );
  // The route's `:cluster`, decoded by the router, against the raw name - the
  // same comparison `Topbar.tsx` makes. `NavLink`'s own `isActive` would say the
  // same thing, but it is only available inside the render callbacks and the
  // `href` has to be known before `to` is set.
  const { cluster: onScreen } = useParams<{ cluster: string }>();
  const active = onScreen === cluster.name;
  // In Settings a tile replaces the entry and keeps its way back
  // (`settingsOrigin.ts`).
  const { state: settingsWay } = useLocation();
  /*
    **And with that cluster's namespace**: the list was
    left at a namespace, and a way back that named none would open it on every
    namespace while the menu beside it still carried the choice - one cluster
    drawn two ways. Only where that kind's list answered `namespaced` this
    session; `namespaceMemory.ts` holds both facts, per cluster, so another
    cluster's choice is never carried here.
  */
  const carried = useChosenNamespaces(cluster.name);
  const carries = useSyncExternalStore(
    watchNamespaceMemory,
    () => remembered !== undefined && listedAsNamespaced(cluster.name, remembered),
    () => false,
  );
  const base = clusterPath(cluster.name);
  const page = remembered === undefined ? undefined : `${base}/${encodeURIComponent(remembered)}`;
  const back =
    page === undefined
      ? undefined
      : open === undefined || open.kind !== remembered
        ? page
        : `${page}?${OPEN_PARAMETER}=${encodeURIComponent(openValue(open))}`;
  const href =
    settings !== undefined
      ? `/settings/extra?${CLUSTER_PARAM}=${encodeURIComponent(cluster.name)}`
      : active || cluster.state === DOWN || back === undefined
      ? base
      : carries
        ? withNamespaces(back, carried)
        : back;
  // Reading the overview ahead matters more here than in the menu: switching
  // cluster is the navigation that changes everything on screen, so it is the
  // one where a wait is most visible.
  const intent = useIntentPrefetch(href);
  const chosenHere = settings !== undefined && settings.chosen === cluster.name;
  const described = useId();

  return (
    <TileLink
      to={href}
      {...(settings !== undefined && settingsWay !== null && settingsWay !== undefined ? { state: settingsWay } : {})}
      {...intent}
      /*
        **The accessible name is the cluster's name, whole**:
        the tile prints only its initials, so the name has to be somewhere a
        screen reader reaches, and a name with the state glued to it is a name
        that changes every time a probe does. The state is the *description*,
        through `aria-describedby`, which a reader announces after the name.

        **`title` is the name and nothing else**:
        what the header's crumb shows, top left. The state is the badge and the
        description below, which carries the editing hint too; the context name
        is on the crumb's tooltip once the cluster is open.

        **And the initials are in the name too** (WCAG 2.5.3). The tile has no
        visible text, so the initials live only here - a voice
        user can still say "click PE". After the whole name, in brackets, so a
        screen reader still starts with the name.
      */
      aria-label={tileName(cluster)}
      aria-describedby={described}
      title={cluster.label}
      data-cluster={cluster.name}
      data-state={cluster.state}
      // Right-click edits this tile's nameplate. The context-menu
      // key and Shift+F10 raise the same event on a focused tile, and F2 - the
      // rename key - does the same on a keyboard that has neither, which is
      // every Mac's. The hint is on the tooltip and in the
      // description, so the route is said and not only there.
      onKeyDown={
        onEdit === undefined
          ? undefined
          : (event) => {
              if (event.key !== 'F2') return;
              event.preventDefault();
              onEdit(cluster.name, event.currentTarget);
            }
      }
      onContextMenu={
        onEdit === undefined
          ? undefined
          : (event) => {
              event.preventDefault();
              onEdit(cluster.name, event.currentTarget);
            }
      }
      /*
        A square, 36px: the rail is 56px with an 8px gutter each side, which
        leaves 39px of room, and 36 leaves the active ring its offset without
        reaching the column's edge.

        The active tile is ringed in the accent - *this is the one you are on* -
        which is what `border-accent` means on the hotbar switcher's current
        digit below.

        The square is the cluster's icon: its stored
        recipe, or its default one drawn
        on the server's palette key (`Cluster.colour`). `bg-panel-2` is what
        shows until the renderer has loaded - a neutral square, never initials.
      */
      chosenIn={settings === undefined ? undefined : chosenHere}
    >
      {/* The icon, and no initials: they are in the tile's accessible name
          (`tileName`) and nowhere on its face. */}
      <ClusterIcon recipe={recipe} frame="absolute inset-0 overflow-hidden rounded-lg" fallback={null} />
      {/* The state, as a badge in the corner. Its color is
          the dot's, so the rail, the cluster list and the header say one state
          one way, and its shape is the state's kind (`stateBadgeClass()`), so it
          is never the color alone. The words are the tile's description
          below.

          `ring-panel` cuts the badge out of the square it sits on, so it reads
          as a mark on the corner rather than a spot on the color. */}
      <span
        aria-hidden="true"
        data-badge={cluster.state}
        className={`absolute -right-0.5 -bottom-0.5 ring-2 ring-panel ${stateBadgeClass(cluster.state)}`}
      />
      <span id={described} className="sr-only">
        {onEdit === undefined ? stateSentence(cluster) : `${stateSentence(cluster)}. ${EDIT_HINT}`}
      </span>
    </TileLink>
  );
}

/**
 * The tile's link. A link to the cluster's page, ringed where the route says it
 * is the one on screen. On Settings > Extra (`chosenIn` set) the same square
 * picks the cluster the page is about, so it keeps the page and rings the one
 * chosen: a route match would ring every tile there.
 */
function TileLink({
  chosenIn,
  ...props
}: Omit<ComponentProps<typeof Link>, 'className' | 'style'> & { chosenIn: boolean | undefined }): ReactNode {
  const ring = (on: boolean): string =>
    `relative mx-auto flex size-9 items-center justify-center rounded-lg bg-panel-2 ${focusRing} ${
      on ? 'ring-2 ring-accent ring-offset-2 ring-offset-panel' : ''
    }`;
  return chosenIn === undefined ? (
    <NavLink {...props} className={({ isActive }) => ring(isActive)} />
  ) : (
    <Link {...props} replace className={ring(chosenIn)} aria-current={chosenIn ? 'page' : undefined} />
  );
}

/** How the nameplate editor is reached from a tile, said where the tile is. */
const EDIT_HINT = 'Right-click, F2 or Shift+F10 to change its color and icon.';

/** The tile's accessible name: the whole cluster name, then the initials the
 *  tile shows - unless they are the name already. */
function tileName(cluster: Cluster): string {
  // Required by the contract, and still absent from a server older than it.
  const initials: string | undefined = cluster.initials;
  return initials === undefined || initials === '' || initials.toLowerCase() === cluster.label.toLowerCase()
    ? cluster.label
    : `${cluster.label} (${initials})`;
}

/** How long Kinds waits for the reads around it before it is drawn anyway. */
const KINDS_WAIT_MS = 8_000;
