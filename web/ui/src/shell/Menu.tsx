import { NavLink, useLocation, useParams } from 'react-router';

import { useIntentPrefetch } from '../router/prefetch';
// A cycle - `routes.tsx` mounts `Shell`, which mounts this - and a safe one:
// the list is read during a render, long after both modules have evaluated.
// It is imported from the route table and not copied here so that the page
// list and the menu's idea of it are one fact.
import { NON_KIND_SEGMENTS } from '../router/routes';
import { extraFavouriteOfHref } from './favouriteExtras';
import {
  createContext,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react';

import { Icon } from '../components/Icon';
import { focusRingFlush, focusRingInside } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { useKeyLayer } from '../keyboard/useKeyLayer';

import {
  useClusters,
  useNav,
  useNavGroup,
  useNavGroupsInHand,
  useNavOccupancy,
  type Cluster,
  type NavDiscovery,
  type NavGroup,
  type Ref,
  type State,
} from '../api';
import { ValueView } from '../state/Result';
import { stateWord } from './clusterState';
import { RED, expired, useClusterVerdict } from './downCluster';
import {
  deferredGroupHoldsPath,
  groupHoldsPath,
  groupIsOpen,
  isOverviewHref,
  openGroups,
  setColumnHidden,
  setGroupOpen,
  useMenuHidden,
  useWindowNarrow,
  subscribeToOpenGroups,
  visitedGroup,
  visitGroup,
} from './menuMemory';
import { glyphForEntry, glyphForGroup, kindOfHref } from './kindGlyph';
import { useChosenNamespaces, withNamespaces } from './namespaceMemory';
import { rememberGroup, rememberedGroup } from './navMemory';
import { KindsToggle } from './KindsToggle';
import { useKindsChoice } from './kindsMemory';
import { useKindsAt, useRailDrawn } from './railDrawn';
import { useProfileRead } from './sharedCluster';
import { withoutContextual } from './contextualExtras';
import { useShellIfPresent } from './ShellContext';
import { internalHref } from '../externalHref';

/**
 * The kind menu.
 *
 * **The element is rendered unconditionally, even with nothing in it**: an
 * always-present region is the simpler thing to reason about.
 *
 * **What it holds without a cluster is nothing, and `empty:hidden` takes it off
 * screen.** On `/health` and `/settings/*` - routes outside `/c/:cluster` by
 * design - a sentence there would be a 192px band between the rail and a page
 * that has no relation to any cluster. The rail stays, this column collapses,
 * and the page takes the width: the cluster-less index (`ClusterList`)
 * already *is* the chooser.
 *
 * ## The menu is a cluster read, and it is enveloped
 *
 * The custom-resource groups come from discovery. `api/openapi.yaml`: "a cluster
 * whose discovery is refused shows its built-in menu and says why the rest is
 * missing, rather than showing a menu that is quietly short." A menu that is
 * quietly short is the founding defect wearing a navigation's clothes - nothing
 * errors, nothing is empty, and a kind simply is not there to click.
 *
 * ## The extras are not here, and that is this version rather than an oversight
 *
 * This menu draws no *Extras* section from `GET /api/c/{cluster}/extensions`:
 * the extras are reached through their own menu group, and an enveloped read
 * here would draw *Not refreshed* into the menu whenever a cluster was slow or
 * unreachable.
 *
 * The *offer* is cosmetic, as `extension.ts` says — **the server-side gate is
 * the enforcement**. The host - `createApp(el, extensions)`, `buildRoutes`,
 * `ShellContext` and everything `index.ts` exports - is what an extension
 * compiles against.
 *
 * ## The groups collapse, and what was opened is remembered
 *
 * `menuMemory.ts` carries the argument. The mechanism here is three rules:
 * the group holding the current page is open on arrival - for the visit, and not
 * written down - a group the person opens or closes is written down,
 * and nothing is ever forced open against them once they are on the page.
 *
 * ## A red cluster's menu does not navigate
 *
 * A red cluster's menu with every kind a link would land each click on a list
 * saying *unreachable* again in its own words. When `Cluster.state` is
 * `forbidden` or `unreachable` - read from the same clusters query `Topbar`
 * already makes, so nothing new is fetched - the groups are still drawn, so
 * the person sees what the console *would* offer,
 * but every row is inert: no href, `aria-disabled`, the muted tone, no
 * prefetch. One line at the top says the state in `clusterState.ts`'s own word
 * with the server's sentence. **Overview stays live**, and so does the shell's
 * Health link in the top bar, because those two are the pages that explain the
 * refusal; a menu with no way out would be a *"nothing here"* banner.
 * `pending` is not red: a cluster still being probed keeps its menu, or
 * a slow cluster is locked out for the ten seconds it takes to answer.
 *
 * ## A cluster the probe found unreachable has no menu at all
 *
 * Where the probe's verdict is `unreachable`
 * (`shell/downCluster.ts`), however old, the menu is not drawn and not asked
 * for - the page is one warning and a Refresh, and a column of kinds nobody can
 * open is the inert menu above with nothing to add. `forbidden` keeps the inert
 * menu and its line, because there the cluster answers.
 *
 * ## Shown or hidden, and nothing between
 *
 * Icons alone make no sense without their names, so the column has two
 * states, **shown**, the menu as it is, or **hidden**, no column at all. The
 * *Kinds* button (`KindsToggle.tsx`: at the bottom left in every mode) and
 * Ctrl+B (Cmd+B) switch between them; the choice is `menuMemory.ts`'s
 * `kubetower.menu.mode`. `/` and Ctrl+K on a hidden column show it and put the
 * keyboard in the jump box, because the box lives in the column.
 *
 * Hidden is drawn as `hidden` on the element rather than as no element: the
 * jump box's hotkeys and the shortcut are claimed from inside it, and they are
 * how a hidden column comes back.
 *
 * ## Kinds holding nothing are hidden, until asked for
 *
 * A kind no one uses is not drawn by default; the person can ask for it, and
 * marks the kinds they want kept as favorites. `GET /api/c/{cluster}/nav/occupancy` says, per kind slug, `some`,
 * `none` or `unknown`; a row is left out only when **all** of these hold:
 *
 * - the answer is `ok` and names the kind `none`. `unknown`, a kind the answer
 *   does not name, and an answer still pending or failed are drawn: an unknown
 *   count is never a zero, and a column is never short on missing data;
 * - the person has not asked to see unused kinds (`Show unused kinds`, under the
 *   jump box, kept by `kindsMemory.ts` on the server or in this browser);
 * - the kind is not a favorite of this cluster (the star on its row);
 * - it is not the kind on screen: the page you are on is never folded out of
 *   the menu, the prototype's rule `Groups` keeps for the groups;
 * - nothing is typed in the jump box. **A search reaches every kind**: a kind
 *   looked for by name is wanted, whatever it holds.
 *
 * Overview, Helm, port-forwards and an extension's page are not kinds
 * (`kindSlugOf`) and are never left out. A group left with nothing is not
 * drawn; the deferred custom resources heading is, because it cannot know
 * before it is opened, and says so when everything in it is hidden.
 */
export function Menu(): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  const { settled, down } = useClusterVerdict(cluster);
  const nav = useNav(cluster, settled && down === undefined);
  const discovery = nav.data?.state === 'ok' ? nav.data.data.discovery : undefined;
  const shell = useShellIfPresent();
  const red = useRedCluster(cluster);
  const [term, setTerm] = useState('');
  const column = useRef<HTMLElement>(null);
  const box = useRef<HTMLInputElement>(null);
  // **Folded on every console, the in-cluster one included**: it has the
  // Kinds button in the bottom left corner, so a hidden column always has its
  // way back.
  //
  // **Before the profile has answered, the stored choice is still applied**,
  // and that is the desktop's exactness rather than an oversight: not applying it would draw a desktop's hidden
  // column for the length of the capabilities read, on every cold load. The
  // cost is on the in-cluster console that has a hidden choice stored by an
  // earlier build, whose column appears that much later. Ctrl+B waits for the
  // profile in every case - below - because a key pressed in that window would
  // store a choice the in-cluster console then ignores.
  const kindsAt = useKindsAt();
  const toggled = kindsAt !== 'none';
  const profileRead = useProfileRead();
  const stored = useMenuHidden();
  const hidden = toggled && stored;
  // Phone-width (`menuMemory.ts`): the column opens over
  // the page rather than beside it, and leaves when it has been used.
  const narrow = useWindowNarrow();
  const over = narrow && toggled && !hidden && cluster !== undefined && down === undefined;
  const railDrawn = useRailDrawn();
  // what the kinds hold, and what the person chose about
  // the ones holding nothing. Asked where the menu itself is asked.
  const occupancy = useNavOccupancy(cluster, settled && down === undefined);
  const choice = useKindsChoice(cluster);
  const { pathname } = useLocation();
  const kinds = occupancy.data?.state === 'ok' ? occupancy.data.data.kinds : undefined;
  const sieve = sieveFor(kinds, choice, kindOfHref(pathname));

  // Ctrl+B (Cmd+B) shows and hides the column, as it toggles a side bar in the
  // editors people already use. At the page's layer, so nothing it does
  // reaches through a dialog, and not while the focus is in a text field, where
  // the browser or the field may have its own use for it. Only on a cluster's
  // pages, which are the only ones with a column to show.
  useKeyLayer(KeyLayer.Page, (event) => {
    // No column to show on a cluster the probe found unreachable, so the key
    // is left to whoever else wants it.
    if (cluster === undefined || down !== undefined) return false;
    // Nor on the in-cluster console, where the column cannot be hidden, nor
    // before the profile has said whether this is that console.
    if (!toggled || !profileRead) return false;
    if (event.key.toLowerCase() !== 'b' || !(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey) {
      return false;
    }
    if (isTyping(event.target)) return false;
    setColumnHidden(!hidden);
    return true;
  });
  // Escape closes the column where it lies over the page; on a wide window it
  // is part of the layout and Escape is left to whatever else claims it.
  useKeyLayer(KeyLayer.Page, (event) => {
    if (!over || event.key !== 'Escape') return false;
    // Typing in the jump box, Escape is the box's: it clears the term first.
    if (isTyping(event.target)) return false;
    setColumnHidden(true);
    return true;
  });

  return (
    <>
    {over ? (
      // The page beside the open column: a tap on it closes the column, as a
      // sheet's backdrop does. Not a focus stop - Escape is the keyboard's way.
      <button
        type="button"
        tabIndex={-1}
        aria-label="Close the kinds menu"
        data-menu-backdrop=""
        onClick={() => setColumnHidden(true)}
        className="absolute inset-0 z-20 bg-bg/60"
      />
    ) : null}
    <nav
      id="menu"
      ref={column}
      aria-label="Resource kinds"
      data-hidden={hidden ? '' : undefined}
      // Down and up are claimed once, here, and not on each of thirty-six rows.
      // `NamespaceMenu` puts `onKeyDown` on every entry because its entries are
      // one component's own children; these are five components deep and a
      // handler threaded through all of them would be a prop that exists only to
      // be passed on. One listener on the column reads `event.target` instead,
      // which is the same answer - the row the key happened in.
      onKeyDown={(event) => {
        arrows(event, column, box);
      }}
      // A jump clears the box: once a row is followed -
      // Enter in the box, which clicks it, Enter on a walked-to row, or a
      // pointer - what was typed has done its work, and leaving it would show
      // the next visit a column narrowed to one kind. Read off the click, so
      // there is one answer for all three.
      //
      // **Only a click that navigates this tab.** A Ctrl/Cmd/Shift/Alt click or
      // a middle click opens the kind elsewhere and leaves this column where it
      // was, still wanted. `defaultPrevented` is the router's own word for it:
      // `NavLink` prevents the default on exactly the clicks it navigates, and
      // its handler runs before this one, which is on an ancestor.
      onClick={(event) => {
        const inTab =
          event.button === 0 &&
          !event.ctrlKey &&
          !event.metaKey &&
          !event.shiftKey &&
          !event.altKey &&
          event.defaultPrevented;
        if (inTab && event.target instanceof Element && event.target.closest('[data-kind]') !== null) {
          setTerm('');
          // Over the page, the column has done its work once a kind is followed.
          if (over) setColumnHidden(true);
        }
      }}
      // `w-48` shown, nothing hidden: `hidden` takes the column off
      // screen and gives the page its 192px.
      // The scroll moved inside, to the groups. The column is a flex column -
      // box, groups - and a scroll bar on the column itself would be a second
      // scroll container around the one that scrolls.
      className={`${hidden ? 'hidden' : 'flex'} w-48 shrink-0 flex-col border-r border-line bg-panel empty:hidden ${
        over ? `absolute inset-y-0 z-30 shadow-xl ${railDrawn ? 'left-14' : 'left-0'}` : ''
      }`}
    >
      {/* `null` and not a sentence: with nothing inside, `empty:hidden` above
          is what makes the page start at the rail's right edge. */}
      {cluster === undefined || down !== undefined ? null : (
        <>
          <JumpBox
            term={term}
            onTerm={setTerm}
            column={column}
            box={box}
            hidden={hidden}
            below={
              kinds === undefined || !choice.ready ? (
                discovery === undefined ? null : (
                  <div className="mt-1.5 flex px-1.5">
                    <DiscoveryMark discovery={discovery} />
                  </div>
                )
              ) : (
                <UnusedToggle
                  shown={choice.showUnused}
                  count={unusedCount(kinds, choice.isFavourite)}
                  onShown={choice.setShowUnused}
                  mark={discovery === undefined ? undefined : <DiscoveryMark discovery={discovery} />}
                />
              )
            }
          />
          {red === undefined ? null : <RedLine cluster={red} />}
          {/* `overflow-x-hidden` is the belt to the wrapping above: one long
              kind name, or a group nobody anticipated, must not put a second
              scrollbar under a 192px column. Vertical scrolling is the one this
              column is allowed. */}
          <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto">
            <ValueView query={nav} what="the menu">
              {(menu) => (
                <Sieve.Provider value={sieve}>
                  <Groups
                    cluster={cluster}
                    groups={withoutContextual(menu.groups, shell?.extensions)}
                    term={term}
                    inert={red !== undefined}
                  />
                </Sieve.Provider>
              )}
            </ValueView>
          </div>
          {/* Where there is no rail the Kinds button is the column's foot while
              it is open, in the flow, so it covers nothing;
              folded, `KindsCorner` draws it over the page's corner. */}
          {kindsAt === 'corner' && !hidden ? (
            <div className="flex shrink-0 border-t border-line p-1.5">
              <KindsToggle form="corner" />
            </div>
          ) : null}
        </>
      )}
    </nav>
    </>
  );
}

/** The cluster on screen, when it is red - `undefined` otherwise, which is also
 *  the answer while the clusters list has not arrived: a menu is not locked on
 *  a guess. The same read `Topbar` makes, from the same query. */
function useRedCluster(name: string | undefined): Cluster | undefined {
  const clusters = useClusters();
  if (name === undefined || clusters.data?.state !== 'ok') return undefined;
  const known = clusters.data.items.find((item) => item.name === name);
  return known !== undefined && (RED.includes(known.state) || expired(known)) ? known : undefined;
}

/**
 * What the rows below `Menu` ask about the kinds column's choice.
 * A context rather than props: it is read four
 * components down, by `Groups`, `Body`, `Nested` and `Entry`, and a prop
 * threaded through `Group` and `Deferred` would exist only to be passed on.
 */
interface SieveValue {
  /** Whether a row is left out while nothing is typed. `undefined` leaves
   *  nothing out: no answer yet, a failed one, or unused kinds shown. */
  readonly hide: ((item: Ref) => boolean) | undefined;
  readonly favourites: readonly string[];
  readonly isFavourite: (slug: string) => boolean;
  readonly toggleFavourite: (slug: string) => void;
  /** The choice has been read, so a star can be written. */
  readonly canMark: boolean;
}

/** Outside a provider - a test mounting a piece alone - nothing is left out
 *  and nothing can be marked. */
const Sieve = createContext<SieveValue>({
  hide: undefined,
  favourites: [],
  isFavourite: () => false,
  toggleFavourite: () => undefined,
  canMark: false,
});

/** The kind a menu row opens, or `undefined` where it is not a kind: Overview
 *  (no segment), and Helm, port-forwards and an extension's pages, which the
 *  route table mounts under the cluster (`NON_KIND_SEGMENTS`). Only a kind has
 *  an occupancy. An extension's page can be a favorite too, kept beside the
 *  kinds (`favouriteExtras.ts`). */
function kindSlugOf(href: string | undefined): string | undefined {
  const slug = kindOfHref(href);
  return slug === undefined || NON_KIND_SEGMENTS.includes(slug) ? undefined : slug;
}

function sieveFor(
  kinds: Readonly<Record<string, { state: string }>> | undefined,
  choice: ReturnType<typeof useKindsChoice>,
  here: string | undefined,
): SieveValue {
  const hiding = kinds !== undefined && choice.ready && !choice.showUnused;
  return {
    hide: hiding
      ? (item) => {
          const slug = kindSlugOf(item.href);
          return (
            slug !== undefined && slug !== here && kinds[slug]?.state === 'none' && !choice.isFavourite(slug)
          );
        }
      : undefined,
    favourites: choice.favourites,
    isFavourite: choice.isFavourite,
    toggleFavourite: choice.toggleFavourite,
    canMark: choice.ready,
  };
}

/** How many kinds the answer says hold nothing and are not favorites - what
 *  the toggle shows. Custom resources included, opened or not: the answer
 *  names them all. */
function unusedCount(
  kinds: Readonly<Record<string, { state: string }>>,
  isFavourite: (slug: string) => boolean,
): number {
  return Object.entries(kinds).filter(([slug, one]) => one.state === 'none' && !isFavourite(slug)).length;
}

/**
 * Show or Hide unused kinds, under the jump box.
 *
 * At the top of the column, where the operator asked for it, and in the jump
 * box's strip rather than a band of its own: the two are the column's two
 * ways of deciding what it lists. Its label says what a press does, "Show
 * unused kinds" while they are hidden and "Hide unused kinds" while they are
 * listed (#60), so the state reads without the background. That label is the
 * state, so it has no `aria-pressed` as well: a toggle whose label changes
 * would say its state twice. The count is how many kinds it adds or hides,
 * drawn on the right in the counter's size. Drawn only once the occupancy has
 * answered and the choice has been read: before that nothing is hidden, so
 * there is nothing to show.
 */
function UnusedToggle({
  shown,
  count,
  onShown,
  mark,
}: {
  shown: boolean;
  count: number;
  onShown: (shown: boolean) => void;
  /** Where discovery of the served kinds stands, drawn in the filter glyph's place
   *  so the row keeps its width. */
  mark: ReactNode | undefined;
}): ReactNode {
  const many = count === 1 ? '1 kind holds' : `${String(count)} kinds hold`;
  return (
    <button
      type="button"
      data-unused-toggle=""
      data-shown={shown}
      title={
        shown
          ? `${many} nothing you can see, and ${count === 1 ? 'it is' : 'they are'} listed. Press to hide them.`
          : `${many} nothing you can see, and ${count === 1 ? 'it is' : 'they are'} hidden. Press to list them.`
      }
      onClick={() => {
        onShown(!shown);
      }}
      className={`mt-1.5 flex w-full items-center gap-1.5 rounded-md px-1.5 py-0.5 text-xs hover:bg-panel-2 hover:text-fg ${
        shown ? 'bg-panel-2 text-fg' : 'text-muted'
      } ${focusRingInside}`}
    >
      {mark ?? <Icon name="filter" />}
      <span className="min-w-0 truncate">{shown ? 'Hide unused kinds' : 'Show unused kinds'}</span>
      <span className="ml-auto text-2xs tabular-nums text-muted">{count}</span>
    </button>
  );
}

/**
 * Where the discovery of the kinds this cluster serves stands, in the
 * unused-kinds toggle's glyph: a spinner while it runs, a warning when it could not be
 * read in full, nothing when it is complete. The sentence is the server's, on
 * `title` only; the menu draws no text for it.
 */
function DiscoveryMark({ discovery }: { discovery: NavDiscovery }): ReactNode {
  const pending = discovery.state === 'pending';
  return (
    <span
      role="status"
      data-discovery={discovery.state}
      title={discovery.detail}
      aria-label={discovery.detail}
      className={`flex shrink-0 items-center text-xs ${pending ? 'text-muted' : 'text-warn'}`}
    >
      <Icon name={pending ? 'refresh' : 'warning'} className={pending ? 'animate-spin motion-reduce:animate-none' : ''} />
    </span>
  );
}

/** The tone of the line, the rail's own axis: `forbidden` is the warn dot and
 *  `unreachable` the bad one, in `clusterState.ts`. */
const RED_TONE: Readonly<Partial<Record<State, string>>> = {
  forbidden: 'text-warn',
  unreachable: 'text-bad',
};

/**
 * The one line at the top of a red cluster's menu.
 *
 * The word is `stateWord`'s - *refused*, *could not be reached* - so the menu
 * and the tile's description cannot say two different things about one
 * cluster, and the sentence is the server's own from `Cluster.reason`, the same
 * one `stateSentence` appends. Nothing is composed here.
 */
function RedLine({ cluster }: { cluster: Cluster }): ReactNode {
  // A lapsed session says so in three words: the page beside it carries the
  // whole message and the command.
  const word = !expired(cluster)
    ? stateWord(cluster.state)
    : cluster.reason?.code === 'credentials_refused'
      ? 'credentials refused'
      : 'session expired';
  const sentence = expired(cluster) ? undefined : cluster.reason?.message;
  return (
    <p
      role="status"
      data-note="cluster-state"
      data-state={cluster.state}
      className={`border-b border-line px-3 py-2 text-xs wrap-anywhere ${RED_TONE[cluster.state] ?? 'text-warn'}`}
    >
      <strong className="font-medium">{word}</strong>
      {sentence === undefined ? null : <>: {sentence}</>}
    </p>
  );
}

/**
 * The way to a kind that is not reading thirty-six names.
 *
 * ## It is the namespace box's keyboard, deliberately
 *
 * `components/NamespaceMenu.tsx` carries the
 * whole argument; this is the same idiom in a second place, which is the point.
 * A second keyboard convention in one console is the thing to avoid - an
 * operator who has learnt one box has learnt both, or neither.
 *
 * Restated only where it decides something here:
 *
 * - **Real focus, moved with `.focus()`**, never a roving tabindex and never
 *   `aria-activedescendant`. The rows are ordinary links and are each a tab stop
 *   already; and leaving the focus in the box while merely *claiming* a row is
 *   active would make Enter the box's Enter, so a row could not be followed with
 *   the keyboard at all.
 * - **The rows are read from the document**, `[data-kind]` under this column, as
 *   `nsfind.js` reads `[data-ns]`. What is rendered is the answer to "what can
 *   be walked", and it cannot go stale behind a row React has unmounted - nor
 *   include a row that does not match, because a row that does not match is not
 *   rendered.
 * - **`data-kind` is on the rows that route and on nothing else.** A kind the
 *   console cannot route to is drawn and is not walked into, exactly as "All
 *   namespaces" is not.
 * - **It stops at both ends and never wraps.** Past the last match the handler
 *   returns without preventing the default, so the column scrolls; up from the
 *   first match returns to the box, because a dead key at the top of a list is
 *   how an operator concludes the keyboard is not supported here. Up *in* the
 *   box refocuses the box and prevents the default, which looks like a no-op and
 *   is not: ArrowUp in a text field otherwise throws the caret to position 0.
 *
 * ## Enter follows the first match, and it clicks it
 *
 * Rather than working out which one that is a second time here. The first
 * `[data-kind]` in the column *is* the first match - the filter decided that
 * once, where the data is - and `.click()` on a `NavLink` is the router
 * navigating exactly as a pointer would. Two answers to "which is first" is how
 * they come to disagree. The one exception is a row the term *names* - a
 * kubectl short name, `po` - which the rows mark `data-named` and Enter follows
 * first; it is still read off the column, not recomputed.
 * Where several rows answer to the term - `service` is core Services and
 * Knative's Service - Enter does not guess: it puts the focus on the built-in
 * kind, or the first, and says how many there are.
 *
 * ## `/` and `Ctrl+K`, and the one thing a page-level hotkey owes
 *
 * Claimed at `KeyLayer.Page`, which is below every overlay, so `/` with a dialog
 * open is a slash in the dialog. And **it declines while the focus is in a text
 * field**: `stack.ts` never reads `event.target`, which is right for an
 * overlay's Escape and wrong for a bare printable key - `/` typed into the
 * namespace box is a slash. So the claimant reads it, and `layers.ts` records
 * that as a `Page` claimant's obligation rather than leaving it here.
 *
 * **On a hidden column they show it**: the box is in the column, so
 * reaching for it is asking for the column back. That is a choice about the
 * column and it is written down, as the *Kinds* button would write it.
 */
function JumpBox({
  term,
  onTerm,
  column,
  box,
  hidden,
  below,
}: {
  term: string;
  onTerm: (term: string) => void;
  column: RefObject<HTMLElement | null>;
  /** Owned by `Menu`, because the column's own key handler needs it too - up
   *  from the first match returns here. */
  box: RefObject<HTMLInputElement | null>;
  /** Whether the column is hidden: the box is in the document and cannot take
   *  the keyboard until the column is shown. */
  hidden: boolean;
  /** Drawn under the field, in its strip: `UnusedToggle`. */
  below?: ReactNode;
}): ReactNode {
  const hintId = useId();
  /*
    Reaching for the box while the column is hidden: the field is in a
    `display: none` column and cannot be focused, so the column is asked for
    first and the effect below focuses the field once it is drawn. A ref and not
    state for "a focus is owed": nothing renders it.
  */
  const owed = useRef(false);
  /*
    Where the focus was when a hotkey reached for the box, so that Escape on
    an empty box can give it back: otherwise the box keeps the keyboard after
    Escape, and the next keystrokes meant for the page go on narrowing the menu.
    Only a hotkey's arrival is remembered; a click into the box had the focus go
    where the pointer put it.
  */
  const from = useRef<HTMLElement | null>(null);
  /** The term Enter found several kinds named by, and how many. Drawn only
   *  while that term is still the one typed, so it goes with the next keystroke
   *  or the clear after a jump without being reset anywhere. */
  const [several, setSeveral] = useState<{ term: string; count: number } | null>(null);

  useEffect(() => {
    if (hidden || !owed.current) return;
    owed.current = false;
    focusBox(box.current);
  }, [hidden, box]);

  /** Reach for the box, wherever the column is. */
  const reach = (): void => {
    const active = document.activeElement;
    from.current = active instanceof HTMLElement && active !== document.body ? active : null;
    if (hidden) {
      owed.current = true;
      setColumnHidden(false);
      return;
    }
    focusBox(box.current);
  };

  useKeyLayer(KeyLayer.Page, (event) => {
    const wants =
      event.key === '/' || (event.key.toLowerCase() === 'k' && (event.ctrlKey || event.metaKey));
    if (!wants) return false;
    if (isTyping(event.target)) return false;
    reach();
    return true;
  });

  return (
    <div className="border-b border-line p-2">
      {/* A real label rather than a placeholder alone: a placeholder is gone the
          moment anything is typed, and a box whose only name is a placeholder is
          announced by a reader as an unlabeled text field. `sr-only` because
          the column is 192px wide and the placeholder already says it on
          screen. */}
      <label htmlFor="menu-jump" className="sr-only">
        Jump to a kind
      </label>
      <input
        id="menu-jump"
        ref={box}
        // `search` and not `text`, which is what `FilterBox` already uses for "a
        // box you type into to narrow a list" - so the console has one kind of
        // narrowing box rather than two that look alike. It also keeps this
        // field, which is on screen on every cluster page, out of the way of the
        // `input[type="text"]` a confirmation dialog's typed-name field is found
        // by: four tests reached into this box instead of the dialog's the first
        // time it existed, which is a real collision and not only a test one.
        type="search"
        autoComplete="off"
        spellCheck={false}
        value={term}
        aria-describedby={hintId}
        placeholder="Jump to a kind…"
        onChange={(event) => {
          setSeveral(null);
          onTerm(event.target.value);
        }}
        // Leaving the box any other way forgets where the hotkey came from:
        // an Escape typed into it later, after a click, must not send the
        // focus back to an element that was current minutes ago.
        onBlur={() => {
          from.current = null;
        }}
        onKeyDown={(event) => {
          // Down and up are the column's, above. What is this field's own is
          // Enter and Escape: Escape clears what is typed, and on an empty box
          // leaves it.
          if (event.key === 'Escape' && term !== '') {
            // Handled here and not through `useEscape`: the key never leaves
            // this field, so nothing below this layer could have wanted it, and
            // an Escape that clears the box being typed in needs no arbitration.
            event.preventDefault();
            onTerm('');
            return;
          }
          if (event.key === 'Escape') {
            // Empty already: the second Escape leaves, back to where a
            // hotkey found the focus, or nowhere in particular.
            event.preventDefault();
            const back = from.current;
            from.current = null;
            if (back !== null && back.isConnected) back.focus();
            else event.currentTarget.blur();
            return;
          }
          if (event.key !== 'Enter') return;
          event.preventDefault();
          // The row the term names - `po` is Pods, whatever else `po` narrows
          // to - and otherwise the first match, as the column drew them. Not
          // recomputed here: `data-named` was decided where the rows were.
          const rows = column.current;
          const named = [...(rows?.querySelectorAll<HTMLElement>('[data-kind][data-named]') ?? [])];
          if (named.length > 1) {
            // Several kinds answer to it - `service` is core Services and
            // Knative's Service. No jump on a guess: the focus goes to the
            // built-in kind (a custom resource's row is `data-custom`), or to
            // the first, and the note says why. Enter again opens it.
            (named.find((row) => !row.hasAttribute('data-custom')) ?? named[0])?.focus();
            setSeveral({ term, count: named.length });
            return;
          }
          (named[0] ?? rows?.querySelector<HTMLElement>('[data-kind]'))?.click();
        }}
        // The list's own box, `.filter` at app.css:293: the ring is drawn one
        // pixel inside and the border goes transparent under it, so the field
        // gains a ring rather than a second outline around the one it has.
        className={`w-full rounded-md border border-line bg-bg px-2 py-1 text-sm ${focusRingInside} focus-visible:border-transparent`}
      />
      <p id={hintId} className="sr-only">
        Type a kind&apos;s name or its kubectl short name to narrow the list. Down and up walk the
        matches. Enter follows the kind the name is, or else the first match; where several kinds
        have that name, Enter moves to one of them and says how many there are.
      </p>
      {several !== null && several.term === term && term !== '' ? (
        <p data-note="several-named" role="status" className="px-1 pt-1 text-2xs text-muted">
          {several.count} kinds answer to <code>{term.trim()}</code>. Enter opens the one with the focus.
        </p>
      ) : null}
      {below}
    </div>
  );
}

/** Focus a field and select what is in it, or do neither. A browser may refuse
 *  both - focus during an unrelated gesture, `select` on a type it does not
 *  support it on - and the control stays usable either way; only the convenience
 *  is lost. `NamespaceMenu` strikes the same bargain. */
function focusBox(node: HTMLInputElement | null): void {
  if (node === null) return;
  try {
    node.focus({ preventScroll: true });
    node.select();
  } catch {
    // Said above; nothing on screen is wrong.
  }
}

/** Whether a key belongs to whatever the operator is typing in. A bare `/` is a
 *  slash there, and a hotkey that took it would make every text field in the
 *  console unusable to find that out. */
function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

/**
 * Down and up, between the box and the matching rows.
 *
 * `NamespaceMenu.arrows` is the original and carries the argument for every line
 * of it. The two differences here are the attribute it walks (`data-kind`) and
 * where it reads it from (this column).
 */
function arrows(
  event: KeyboardEvent<HTMLElement>,
  column: RefObject<HTMLElement | null>,
  box: RefObject<HTMLInputElement | null>,
): void {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
  const list = [...(column.current?.querySelectorAll<HTMLElement>('[data-kind]') ?? [])];
  if (list.length === 0) return;
  // `target` and not `currentTarget`: the listener is on the column, so
  // `currentTarget` is always the column itself. -1 for the box, for a group
  // heading, and for anything else in here that is not a row - which is what
  // makes the first press land on the first match from any of them.
  const at = list.indexOf(event.target as HTMLElement);
  const next = event.key === 'ArrowDown' ? list[at + 1] : at <= 0 ? box.current : list[at - 1];
  if (next === undefined || next === null) return;
  event.preventDefault();
  try {
    next.focus({ preventScroll: false });
  } catch {
    // Same bargain as the shortcut above: the control stays usable and only the
    // convenience is lost.
  }
}

/**
 * The groups, and the one piece of state the menu owns.
 *
 * The arrangement is read from `menuMemory`, which is a module store rather than
 * state here, because it has to outlive every unmount of this component.
 * `visitGroup` then holds open the group containing the page that was arrived
 * at, **for the visit only**. Adding it to the arrangement would make "I have
 * been here" accumulate, and a visit to one custom resource page would leave
 * *Custom Resources* open, and its CustomResourceDefinition listing paid, on
 * every page after. `menuMemory.ts` has the argument; the dock draws the same
 * line between a pin and the `*` scope.
 *
 * That runs in an effect, so a group the memory does not carry is drawn shut for
 * one frame before it opens. Stated rather than hidden: the alternative is
 * deciding it during render, and a render may be discarded - a group opened by a
 * pass React threw away is a visit to a page nobody visited.
 *
 * **The effect is keyed on the group's key and not on the location**, and that
 * is the difference between a memory and a group that cannot be closed. An
 * person who closes the group they are standing in gets to keep it closed:
 * closing it ends the visit (`setGroupOpen`), the key has not changed, so
 * nothing re-opens it. Coming back to it from somewhere else does, which is the
 * rule that the current page must never be invisible in the menu.
 * Leaving it ends the visit, so a group only the visit held open shuts again.
 */
function Groups({
  cluster,
  groups,
  term,
  inert,
}: {
  cluster: string;
  groups: readonly NavGroup[];
  term: string;
  /** The cluster is red: every row but Overview routes nowhere. Threaded down
   *  because it changes what every row is. */
  inert: boolean;
}): ReactNode {
  const { pathname } = useLocation();
  const needle = term.trim().toLowerCase();
  const narrowing = needle !== '';
  // rows holding nothing are left out, except under a
  // search, which reaches every kind.
  const sieve = useContext(Sieve);
  const hide = narrowing ? undefined : sieve.hide;

  // `null` means nothing has ever been arranged in this browser. What that draws
  // is `groupIsOpen`'s answer and it is not simply "everything": a deferred group
  // is shut, because opening it *is* the fetch. `menuMemory.ts` has the argument.
  const open = useSyncExternalStore(subscribeToOpenGroups, openGroups, openGroups);
  // The group held open for the page on screen, which is drawn open and never
  // stored. One subscription for both: a write and a visit notify the same
  // listeners.
  const visit = useSyncExternalStore(subscribeToOpenGroups, visitedGroup, visitedGroup);

  const current =
    groups.find((group) => groupHoldsPath(group, pathname))?.key ??
    // A deferred group has no items yet, so `groupHoldsPath` cannot see the page
    // inside it - and a custom resource page is inside it by definition. Asked
    // from the other side, which is how the server asks it too - minus the
    // pages the route table mounts under the cluster, which are not kinds.
    deferredGroupHoldsPath(groups, pathname, NON_KIND_SEGMENTS);

  // What the arrangement draws open, which is what the memory writes down on a
  // first gesture. Not every key: a deferred group is shut by default, so the
  // two lists differ, and it is this one `setGroupOpen`'s argument is about.
  // And not the visit: on a custom resource page the heading is on screen, and
  // a first gesture on another group that wrote down what was on screen would
  // commit the heading with it.
  const arrangedOpen = groups.filter((group) => groupIsOpen(group, open)).map((group) => group.key);

  useEffect(() => {
    // The arrival rule: the key changing is the arrival. The arrangement is not
    // read here at all, so it is not a dependency to leave out - which is also
    // why closing the group a person stands in keeps it closed: nothing here
    // runs again until they arrive somewhere else.
    visitGroup(current);
    // Leaving - for another group, for no group, or off the cluster entirely -
    // ends the visit, and a group only the visit held open shuts again.
    return () => {
      visitGroup(undefined);
    };
  }, [current]);

  // The deferred groups' contents where they are already in hand. Read, never
  // asked for: `Deferred` is what fetches, and only once its heading is open.
  const deferred = groups.filter((group) => group.deferred);
  const inHand = useNavGroupsInHand(
    cluster,
    deferred.map((group) => ({ key: group.key, snapshot: rememberedGroup(cluster, group.key) })),
  );

  // What the loaded groups can answer, an open deferred group whose answer is in
  // hand included: its kinds are listed, so they are matches. One that is shut,
  // or not yet answered, is NOT in this - "it does not match" and "we have not
  // looked" would be the same answer, and those must stay different.
  const loadedMatches =
    groups.reduce((total, group) => total + matchCount(group, needle), 0) +
    deferred.reduce((total, group, index) => {
      const answer = inHand[index];
      return answer !== undefined && groupIsOpen(group, open, visit) ? total + matchCount(answer, needle) : total;
    }, 0);
  // A heading whose contents cost a cluster read and are not in hand. Opening it
  // *is* the fetch, so a search must not open it - it says so instead.
  const unsearched = groups.filter((group) => group.deferred && !groupIsOpen(group, open, visit));

  // The kinds marked as favorites, in the order they were marked, drawn first
  // and still in their own group. Only rows in hand: a custom resource kind
  // whose heading has never been opened is not found, and is not asked for.
  const everywhere = [...groups, ...deferred.flatMap((_, index) => inHand[index] ?? [])];
  const favourites = narrowing ? [] : favouriteRows(everywhere, sieve.favourites);

  return (
    <>
      {favourites.length > 0 ? (
        <section data-group="favourites" className="border-b border-line py-2">
          <h2 className="flex items-center gap-1 px-3 pb-1 text-sm font-semibold text-muted">
            <Icon name="starred" />
            Favorites
          </h2>
          {favourites.map((item) => (
            <Entry key={`favourites/${item.href ?? item.text}`} item={item} inert={inert} />
          ))}
        </section>
      ) : null}
      {groups.map((group) => {
        // While narrowing, a group with nothing in it is not drawn at all -
        // except a deferred one, which is drawn precisely because it cannot say.
        if (narrowing && !group.deferred && matchCount(group, needle) === 0) return null;
        // A group the sieve emptied is not drawn either. One that arrived empty
        // is: its note is what says why. The deferred heading cannot know before
        // it is opened, and `Body` says so inside it once it is.
        if (hide !== undefined && !group.deferred && emptied(group, hide)) return null;
        return (
          <Group
            key={group.key}
            cluster={cluster}
            group={group}
            term={needle}
            inert={inert}
            // A search opens what it has to show. Not the deferred heading: for
            // that one, being open is the 23.7 MB read the deferral exists to
            // avoid, and a keystroke must not trigger it.
            open={narrowing && !group.deferred ? true : groupIsOpen(group, open, visit)}
            onOpenChange={(isOpen) => {
              // Nothing is written while narrowing. The disclosures are being
              // driven by the search rather than by the operator, and React
              // setting `open` fires `toggle` exactly as a click does - so
              // without this, typing three letters would rewrite the remembered
              // arrangement and leave every group open after the box is cleared.
              if (narrowing) return;
              setGroupOpen(group.key, isOpen, arrangedOpen);
            }}
          />
        );
      })}

      {narrowing && loadedMatches === 0 ? (
        // Said, not left empty. A column that has silently gone blank under a
        // search reads as a cluster with no kinds - an empty answer drawn as a
        // missing one - and `NamespaceMenu` says it the same way.
        <p data-note="no-match" className="px-3 py-2 text-xs text-warn">
          No kind here matches <code>{term.trim()}</code>.
          {unsearched.length > 0 ? ' Open Custom Resources to search them too.' : ''}
        </p>
      ) : null}
    </>
  );
}

/** The rows of `slugs`, in that order, found among the groups and their
 *  sub-groups. A slug no group holds is skipped. */
function favouriteRows(groups: readonly NavGroup[], slugs: readonly string[]): Ref[] {
  const found = new Map<string, Ref>();
  const walk = (group: NavGroup): void => {
    for (const item of group.items) {
      const slug = kindSlugOf(item.href) ?? extraFavouriteOfHref(item.href);
      if (slug !== undefined && !found.has(slug)) found.set(slug, item);
    }
    (group.groups ?? []).forEach(walk);
  };
  groups.forEach(walk);
  return slugs.flatMap((slug) => {
    const item = found.get(slug);
    return item === undefined ? [] : [item];
  });
}

/** Whether one row is a match. `includes` on the label, not a prefix: an
 *  operator looking for `hpa` types `auto`, and `Horizontal pod autoscalers` is
 *  what they want. And a prefix of any of kubectl's names for the kind, which
 *  the server sends as `aliases`: `svc` is Services, `cm`
 *  is Config maps, as at a terminal. A prefix there and not `includes`, or `s`
 *  would match every kind whose short name holds one. */
function itemMatches(item: Ref, needle: string): boolean {
  return (
    item.text.toLowerCase().includes(needle) ||
    (item.aliases ?? []).some((alias) => alias.startsWith(needle))
  );
}

/** Whether what is typed *is* one of this row's names - `po` for Pods, not for
 *  Pod disruption budgets, which `po` also narrows to. Enter follows such a row
 *  ahead of the first match (`JumpBox`), so a kubectl short name lands where
 *  kubectl would. */
function itemIsNamed(item: Ref, needle: string): boolean {
  return needle !== '' && (item.text.toLowerCase() === needle || (item.aliases ?? []).includes(needle));
}

/** How many rows in this group and its sub-groups match, and are not left
 *  out by `hide`. `0` on an empty needle with no `hide` is
 *  deliberately not special-cased by the callers: they ask only while
 *  narrowing. */
function matchCount(group: NavGroup, needle: string, hide?: (item: Ref) => boolean): number {
  if (needle === '' && hide === undefined) return group.items.length;
  return (
    group.items.filter((item) => (needle === '' || itemMatches(item, needle)) && !(hide?.(item) ?? false)).length +
    (group.groups ?? []).reduce((total, sub) => total + matchCount(sub, needle, hide), 0)
  );
}

/** Whether a group held rows and `hide` left none of them. */
function emptied(group: NavGroup, hide: (item: Ref) => boolean): boolean {
  return matchCount(group, '', () => false) > 0 && matchCount(group, '', hide) === 0;
}

/**
 * One group, as a native disclosure.
 *
 * `<details>` rather than a button and a conditional render, because the open
 * state is a thing the platform already models, with
 * the keyboard behavior, the ARIA role and the marker all for free. It is
 * controlled here - `open` comes from the memory and `onToggle` writes back to
 * it - so the DOM and the memory cannot drift apart.
 *
 * `onToggle` is a React prop on the element, **not** a `document` listener in
 * the capture phase: `toggle` does not bubble, and here the element is ours
 * and React attaches to it directly.
 */
function Group({
  cluster,
  group,
  term,
  inert,
  open,
  onOpenChange,
}: {
  cluster: string;
  group: NavGroup;
  /** The jump box's text, already trimmed and lower-cased. `''` is not
   *  narrowing, which is the only state every component below tests for. */
  term: string;
  inert: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}): ReactNode {
  return (
    <details
      open={open}
      data-group={group.key}
      onToggle={(event) => {
        // Two kinds of `toggle` reach this handler that are not a gesture, and
        // either would write the visit into the arrangement.
        //
        // React dispatches `toggle` through its own tree although the DOM event
        // does not bubble, so a nested API-group disclosure opening on a custom
        // resource page arrives here too, carrying *this* element's state.
        if (event.target !== event.currentTarget) return;
        // And React setting `open` fires `toggle` exactly as a click does, so the
        // visit opening the heading would come back as "the person opened it".
        // A click changes the element before the event; a prop change is the
        // element agreeing with what was drawn. Only a disagreement is a gesture.
        if (event.currentTarget.open === open) return;
        onOpenChange(event.currentTarget.open);
      }}
      className="border-b border-line py-2"
    >
      {/*
        Sentence case at `text-2xs`, `font-medium`, and its own chevron.

        It was `.menu-group > summary`, app.css:425 - uppercase at 10px, tracked
        at .07em. The eyebrow is not gone; it has moved down a level, to the
        custom-resource API-group sub-headings in `Nested`, where a second
        disclosure inside this one needs the quieter voice. Two eyebrows nested
        say the same thing twice and neither reads as the outer one.

        `list-none` and the WebKit pseudo-element take the native marker off, so
        the chevron below is the only one. Dropping the marker without putting
        something in its place would leave a heading with nothing saying it
        opens - which is the whole reason this is a chevron and not a removal.
      */}
      <summary
        // `text-sm`: not `text-2xs`, which `theme.css` reserves for
        // "badges and counters, not for reading text", and level with the
        // rows it heads — see `ROW`, where the inversion is explained. `font-semibold` rather than a brighter color,
        // so the heading gains weight without competing with the active row,
        // which is the one thing in this column that should be the loudest.
        //
        // `focusRingFlush`, because the summary is the full width of the
        // column's scroll region: with no ring of its own it drew the browser's,
        // outside itself, and the region cut that off down both sides.
        className={`flex cursor-default list-none items-center gap-1 px-3 pb-1 text-sm font-semibold text-muted [&::-webkit-details-marker]:hidden ${focusRingFlush}`}
      >
        {/* Rotated rather than swapped, so what the eye follows is one shape
            turning. `open` is the prop this element is already controlled by,
            so the chevron cannot disagree with the disclosure under it. */}
        <Icon name="chevron" className={`size-3 transition-transform ${open ? 'rotate-90' : ''}`} />
        {/* The group's own shape, and deliberately not the shape of any kind
            inside it - a heading that draws its first row's glyph reads as that
            row. `kindGlyph.ts` answers by the key the server chose, so a heading
            renamed on the server draws the generic one rather than nothing. */}
        <Icon name={glyphForGroup(group.key)} />
        {/* The heading stays inside the summary rather than beside it: a
            disclosure whose label is not its own control is one a screen reader
            reads twice and a pointer can only open by hitting the triangle. */}
        <h2 className="inline">{group.label}</h2>
      </summary>

      {group.deferred ? (
        <Deferred cluster={cluster} group={group} term={term} inert={inert} open={open} />
      ) : (
        <Body group={group} term={term} inert={inert} />
      )}
    </details>
  );
}

/**
 * A group whose contents are somewhere else, fetched when the disclosure opens.
 *
 * Set on the custom resource heading and on nothing else. Filling it lists the
 * cluster's CustomResourceDefinitions - tens of megabytes and over half a
 * second on a large cluster - which is why the server sends the heading with
 * `deferred: true` and no items, and why `open` gates the query rather than
 * merely hiding its result.
 *
 * **`open` is the whole mechanism, and the memory is what makes it survive.** A
 * heading the person opened is remembered, so arriving with it open fetches on
 * arrival; and a custom resource page opens it on arrival because following a
 * link must never land you on a
 * page whose place in the menu is folded away.
 *
 * The three answers are drawn by three different things, deliberately. A cluster
 * that could not be asked at all is the envelope's failure arm, which `ValueView`
 * draws with its code and its remedy. A cluster that answered and serves no
 * custom resource kinds, and one whose kinds could not be listed, are both `ok`
 * with no items and a `note` - and the sentence is what tells them apart, because
 * the server is what discovered which it was.
 *
 * **And the answer outlives the tab.** Opened once on this cluster, the group's
 * `ok` answer is written to `navMemory` and handed back to the query as its
 * initial data on the next mount, stamped with when it was received - so after
 * F5 the heading is drawn before any request is made, and the query's own
 * `staleTime` decides whether one is made at all. Only an `ok` answer is
 * written: a failure is a fact about that
 * moment, and a snapshot of *serves none* still carries the sentence, so the
 * three answers stay three. `open` still gates the fetch exactly as before; a
 * snapshot changes what is drawn while it is open, never whether it is asked.
 */
function Deferred({
  cluster,
  group,
  term,
  inert,
  open,
}: {
  cluster: string;
  group: NavGroup;
  term: string;
  inert: boolean;
  open: boolean;
}): ReactNode {
  const filled = useNavGroup(cluster, group.key, open, rememberedGroup(cluster, group.key));

  // The write, on every `ok` answer and stamped with the query's own time.
  // An effect rather than a `queryFn` side effect, so that the api layer knows
  // the shape of a snapshot and nothing about where one lives. The answer
  // seeded from the snapshot itself comes through here too and is a no-op in
  // the store: same moment, not a new fact.
  const answer = filled.data;
  const answeredAt = filled.dataUpdatedAt;
  useEffect(() => {
    if (answer?.state === 'ok') rememberGroup(cluster, group.key, answer.data, answeredAt);
  }, [cluster, group.key, answer, answeredAt]);

  // Nothing has been asked for. Not drawn as pending and not as empty:
  // `ValueView` would say "Reading ..." about a request nobody has made, under a
  // heading that is closed.
  if (!open) {
    // Except under a search, where saying nothing would be the menu answering
    // "no" to a question it has not asked. The kinds are one click away and the
    // click is the fetch; the sentence is what makes that an offer rather than a
    // silence. A cluster serves 140 of these, so it is a real answer and not a
    // corner case.
    return term === '' ? null : (
      <p data-note="unsearched" className="px-3 pb-1 text-2xs text-muted">
        Open to search custom resources.
      </p>
    );
  }

  return (
    <ValueView query={filled} what={group.label.toLowerCase()} compact>
      {(answer) => <Body group={answer} term={term} inert={inert} />}
    </ValueView>
  );
}

/**
 * The entries of one group, and why it is short when it is.
 *
 * One component for the group that arrived with the menu and the group that
 * arrived a moment later, for the reason the server has one `customNavBody` for
 * both: a heading that looked different depending on when it arrived would be a
 * bug nobody could describe.
 */
function Body({
  group,
  term,
  inert,
}: {
  group: NavGroup;
  term: string;
  inert: boolean;
}): ReactNode {
  const sieve = useContext(Sieve);
  // Under a search every kind is reachable.
  const hide = term === '' ? sieve.hide : undefined;
  const matched = term === '' ? group.items : group.items.filter((item) => itemMatches(item, term));
  const shown = hide === undefined ? matched : matched.filter((item) => !hide(item));
  return (
    <>
      {/* Kinds this console may not list, left out, and the read that would
          show them. First and in the warning tone - the tone and the
          `data-note` a list narrowed by the access review uses - because as the
          last line under a column of API groups it would be scrolled past.
          Drawn under a search too: that is when a kind
          looked for and not found most needs telling apart from one the
          cluster does not serve. */}
      {group.accessNote !== undefined ? (
        <p data-note="access" className="px-3 py-1 text-xs wrap-anywhere text-warn">
          {group.accessNote}
        </p>
      ) : null}

      {shown.map((item) => (
        <Entry
          key={`${group.key}/${item.text}`}
          item={item}
          inert={inert}
          heading={group.key}
          named={itemIsNamed(item, term)}
        />
      ))}

      {/* One nested level, for the custom resource kinds grouped by API group.
          Flat, a cluster running cert-manager, Calico, Argo and Trivy puts a
          hundred-odd entries under one disclosure - one production cluster serves
          140. */}
      {(group.groups ?? []).map((sub) =>
        // A sub-group with no match is not drawn, exactly as a group is not -
        // nor one whose every kind the sieve left out.
        (term !== '' && matchCount(sub, term) === 0) || (hide !== undefined && emptied(sub, hide)) ? null : (
          <Nested key={sub.key} group={sub} term={term} inert={inert} />
        ),
      )}

      {/* Everything here holds nothing the person may see. Said rather than
          left blank, which would read as a cluster serving none: only the
          deferred heading reaches this, since any other group is not drawn. */}
      {hide !== undefined && emptied(group, hide) ? (
        <p data-note="all-unused" className="px-3 pt-1 text-2xs text-muted">
          Every kind here holds nothing you can see. Show unused kinds to list them.
        </p>
      ) : null}

      {/* Why a group is short or empty, when it is. The server carries the
          sentence because the server is what discovered the shortfall - and two
          different facts arrive here, a cluster serving none and a cluster that
          could not be asked, which the sentence is what separates. */}
      {group.note !== undefined ? (
        <p className="px-3 pt-1 text-2xs text-muted">
          {group.note}
        </p>
      ) : null}
    </>
  );
}

/**
 * One API group inside the custom resource heading.
 *
 * Open when it holds the page on screen, shut otherwise, decided from the
 * location rather than remembered - the sub-group holding the current kind is
 * open, so following a link never lands you inside a fold - and it is right
 * here for a reason of its own: these are the
 * operators somebody installed on this cluster, so the set changes underneath,
 * and an arrangement keyed on names that come and go quietly stops matching.
 *
 * `defaultOpen` and no `onToggle`, so opening one by hand sticks for as long as
 * the menu is mounted and costs nothing to record. The `key` is what re-applies
 * the rule: it changes when this group starts or stops holding the page, which
 * remounts the element with a fresh default instead of leaving a stale one.
 */
function Nested({
  group,
  term,
  inert,
}: {
  group: NavGroup;
  term: string;
  inert: boolean;
}): ReactNode {
  const { pathname } = useLocation();
  // A search opens it, for the same reason the page it holds does: a match the
  // person cannot see is a match the menu did not report. Nothing is written
  // down either way - this level is decided from the location and never
  // remembered, so a search leaves no trace here to undo.
  const holding = groupHoldsPath(group, pathname) || term !== '';
  const sieve = useContext(Sieve);
  const hide = term === '' ? sieve.hide : undefined;

  return (
    <details key={holding ? 'holding' : 'idle'} open={holding} data-group={group.key} className="group/api pl-2">
      {/* The eyebrow: this is the level that needs to be quieter than the one
          above it. */}
      <summary
        // Neither tracked nor uppercased, and `wrap-anywhere`, all three so
        // no horizontal scrollbar appears under the column. An API group is a
        // domain name — `gateway.networking.k8s.io` is 25 characters and **one
        // unbreakable word** - and `tracking-eyebrow` with `uppercase` would make
        // it about a third wider. Lower case is also how the group is actually
        // spelled.
        //
        // The size is `text-sm`, the kinds' own below it and the
        // "Custom Resources" heading's above it: `text-2xs` is `theme.css`'s
        // size for badges and counters, not for a name to be read, and a group
        // drawn smaller than its kinds read as the least important line of the
        // list. It is told apart by `text-muted` and `font-medium`, not by size.
        // The ring inside, for the reason the group's own summary gives.
        className={`mx-2 flex cursor-default list-none items-center gap-2 rounded-md px-2 py-1 text-sm font-medium text-muted hover:bg-panel-2 group-open/api:text-fg [&::-webkit-details-marker]:hidden ${focusRingFlush}`}
        title={group.label}
      >
        {/* No triangle: the row itself is the control. Open reads from the
            row's color, and the count says what is inside. One line, cut by
            an ellipsis, never wrapped; the whole name is the title. */}
        <h3 className="min-w-0 flex-1 truncate">{group.label}</h3>
        <span className="text-xs tabular-nums">{group.items.length}</span>
      </summary>
      {(term === ''
        ? group.items.filter((item) => !(hide?.(item) ?? false))
        : group.items.filter((item) => itemMatches(item, term))
      ).map(
        (item) => (
          <Entry key={`${group.key}/${item.text}`} item={item} inert={inert} named={itemIsNamed(item, term)} custom />
        ),
      )}
    </details>
  );
}

/**
 * One kind, as a row.
 *
 * ## The geometry, and the one thing it is arranged around
 *
 * A row inset from the column edge (`mx-2`), rounded, with a surface under the
 * pointer and under the page you are on: one step of gray alone, on a column of
 * thirty-six lines, would not mark the current kind.
 *
 * **The accent bar is declared at rest and only colored when active.** A
 * `border-l-2` that appears on the active row alone would push its label 2px
 * right of every other label, so the one row you are looking at is the one that
 * does not line up - and the jog is visible as you arrow down the column.
 * `border-transparent` reserves the space on every row; `border-accent` fills it
 * on one. The same two pixels, always, in the same place.
 *
 * **The corners are cut back to `rounded-r-md` when active**, and the radius is
 * therefore declared per branch rather than once in `ROW`: `rounded-md` and
 * `rounded-r-md` on the same element do not fight, they compose - the second
 * sets two corners and the first would still be rounding the other two. The
 * reason to want the left corners square is the same argument from the other
 * side: a 6px radius on the left corners would bow the
 * bar away from the row's own edge at top and bottom, and a 2px accent that is
 * straight for 20 of its 24 pixels reads as a rendering fault rather than a mark.
 *
 * `aria-current` is `NavLink`'s own and is untouched: the mark is the visible
 * half of something a reader was already being told.
 *
 * ## The glyph, and where it is decided
 *
 * `kindGlyph.ts`, from the slug in the href rather than from the label - a label
 * is a rendering and free to change, the slug is `Descriptor.Kind`. It is always
 * drawn: a kind with no shape of its own gets `resource`, which is the rule
 * `Metrics.tsx` already follows, and here it is what keeps the custom-resource
 * rows lined up with the built-in ones on a cluster serving a hundred of them.
 *
 * The icon takes `--icon`, the console's one icon size (15px), rather than a
 * fixed 16px. A one-pixel exception spelt into this component is a value that
 * stops moving when the theme moves; if 16 is wanted, the token is where it changes and
 * every icon in the console changes with it.
 *
 * ## Overview, which is the one row with no kind in its href
 *
 * The server puts it first in the cluster group, linking to the bare
 * `/c/{cluster}`. `kindOfHref` has no slug to answer with, so its glyph is
 * decided here rather than falling through to `resource` - the first row of the
 * menu drawn as an unknown kind. `end` keeps `NavLink` from calling it active
 * on every page under the cluster. And it is the one row that stays live when
 * the cluster is red, because it is the page that explains the refusal.
 *
 * ## An inert row
 *
 * The same geometry as a row that routes and the same dimming as one the
 * console cannot route to, with `role="link"` and `aria-disabled` so a reader
 * says what a sighted person sees: a link, unavailable. No `data-kind`, for
 * the jump box's rule - what the keyboard walks is what routes.
 *
 * ## The star
 *
 * A kind's row that routes carries a star after its name: pressed, the kind is
 * a favorite of this cluster and is drawn whatever it holds. A button beside
 * the link and not inside it - a control inside a link is two controls a
 * reader cannot tell apart - laid over the row's right end, whose padding is
 * reserved for it on every kind's row, so marking one moves nothing. The
 * outline is drawn under the pointer, under the keyboard's focus, and always
 * on a touch screen, where there is no hover; the filled star always. It is a
 * tab stop of its own, after its row: Down and Up walk the rows and skip it.
 */
function Entry({
  item,
  inert,
  heading,
  named = false,
  custom = false,
}: {
  item: Ref;
  inert: boolean;
  /** The key of the heading the row is under, where it is a top-level one:
   *  a kind with no shape of its own draws that heading's (`glyphForEntry`). */
  heading?: string;
  /** What is typed in the jump box is one of this row's names (`itemIsNamed`):
   *  marked `data-named`, which Enter follows ahead of the first match. */
  named?: boolean;
  /** A custom resource's row, under an API group: marked `data-custom`, so
   *  that where a built-in kind and a CRD answer to one name, the jump box
   *  puts the focus on the built-in one. */
  custom?: boolean;
}): ReactNode {
  /*
    **The namespace chosen on the last namespaced list rides along**,
    on the rows the server marks as opening one and on no
    other: Nodes and Overview are drawn as they are. Here and not in the jump
    box, because Enter there clicks this very link - one place decides what a
    row's address is, and the box and the pointer cannot disagree about it.
    `namespaceMemory.ts` says why it is the href that changes and never the
    page.
  */
  const { cluster } = useParams<{ cluster: string }>();
  const carried = useChosenNamespaces(cluster);
  const raw = internalHref(item.href);
  const href = raw !== undefined && item.namespaced === true ? withNamespaces(raw, carried) : raw;
  const overview = isOverviewHref(href);
  const glyph = overview ? 'overview' : glyphForEntry(item.href, heading);
  const live = href !== undefined && (!inert || overview);
  // Before the early return, because a hook may not be called conditionally.
  // It is handed an href that may be undefined for that reason alone, and
  // answers by warming nothing - which is also what an inert row asks of it.
  const intent = useIntentPrefetch(live ? href : undefined);
  const sieve = useContext(Sieve);
  // A kind's row, or an extra's (#111): both carry the star.
  const kind = kindSlugOf(item.href);
  const slug = kind ?? extraFavouriteOfHref(item.href);

  // The tooltip is the server's hint, where there is one.
  const label = item.hint;
  const row = `${ROW} gap-2 mx-2 px-2`;

  // A Ref with no usable href is a kind the console knows about and cannot
  // route to. It is still listed, dimmed, rather than dropped - a menu short by
  // one line is the thing this component exists not to be.
  if (href === undefined) {
    return (
      // The same geometry as a row that routes, so the column has one left edge
      // rather than a ragged one - and the same reserved border, or this line
      // alone would sit 2px left of the thirty-five above it.
      <span className={`${row} rounded-md border-transparent text-muted opacity-60`} title={label}>
        <Icon name={glyph} />
        {item.text}
      </span>
    );
  }

  if (!live) {
    return (
      <span
        role="link"
        aria-disabled="true"
        data-inert
        className={`${row} rounded-md border-transparent text-muted opacity-60`}
        title={label}
      >
        <Icon name={glyph} />
        {item.text}
      </span>
    );
  }

  const link = (
    <NavLink
      to={href}
      end={overview}
      title={label ?? item.text}
      // What the jump box walks, and what Enter follows. On the rows that route
      // and on nothing else - `NamespaceMenu`'s `data-ns` rule, which is what
      // keeps a row the console cannot route to out of the keyboard's path while
      // leaving it on screen.
      data-kind={kindOfHref(item.href) ?? item.text}
      data-named={named ? '' : undefined}
      data-custom={custom ? '' : undefined}
      {...intent}
      className={({ isActive }) =>
        `${slug === undefined ? row : `${ROW} gap-2 pl-2 pr-7`} ${
          isActive
            ? 'rounded-r-md border-accent bg-panel-2 text-fg'
            : 'rounded-md border-transparent text-muted hover:bg-panel-2'
        }`
      }
    >
      <Icon name={glyph} />
      {/* One line, cut rather than wrapped: "Horizontal
          pod autoscalers" took two lines of a 192px column, the one row in it
          twice the height of the rest. The hover is the server's hint, which
          names the kind whole. */}
      <span className="min-w-0 truncate">{item.text}</span>
    </NavLink>
  );
  if (slug === undefined) return link;

  const favourite = sieve.isFavourite(slug);
  return (
    <div className="group/row relative mx-2" data-row={slug}>
      {link}
      <button
        type="button"
        aria-pressed={favourite}
        aria-label={`Favorite: ${item.text}`}
        title={
          kind === undefined
            ? favourite
              ? 'A favorite. Press to unmark it.'
              : 'Mark as a favorite'
            : favourite
              ? 'A favorite: always listed. Press to unmark it.'
              : 'Mark as a favorite: always listed, even holding nothing.'
        }
        data-favourite={slug}
        disabled={!sieve.canMark}
        onClick={() => {
          sieve.toggleFavourite(slug);
        }}
        className={`absolute inset-y-0 right-0.5 my-auto flex size-6 items-center justify-center rounded-md hover:text-fg ${
          favourite
            ? 'text-accent'
            : 'text-muted opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100 pointer-coarse:opacity-100'
        } ${focusRingInside}`}
      >
        <Icon name={favourite ? 'starred' : 'star'} />
      </button>
    </div>
  );
}

/** What every row is, whether it routes or not. The border is declared here and
 *  colored by the caller - see `Entry`, where the two pixels are argued about. */
// **Every row in this column is the same size, and it is on the ladder.**
//
// With no size, a row would inherit the body's 14px and be the largest text in
// the column, larger than the group heading above it.
//
// `text-sm` here and on the group heading, which differs by weight instead:
// nothing a heading heads should be bigger than the heading. The API group
// stays smaller — it qualifies a kind rather than naming one.
const ROW = 'flex items-center border-l-2 py-1 text-sm';
