import { useEffect, type ReactNode } from 'react';
import { Outlet } from 'react-router';

import { useClusters } from '../api';
import { SessionWatch } from '../auth/SessionWatch';
import { Dock } from '../dock/Dock';
import { DockProvider } from '../dock/DockContext';
import { IconSprite } from '../components/Icon';
import { Drawer } from './Drawer';
import { LIST_MIN } from './drawerWidth';
import { LiveRegion } from './LiveRegion';
import { useRestoreSettingsScroll } from './settingsOrigin';
import { KindsCorner } from './KindsToggle';
import { Menu } from './Menu';
import { PageFactsProvider } from './pageFacts';
import { Rail } from './Rail';
import { Suggestions } from './Suggestions';
import { Topbar } from './Topbar';
import { forgetOtherClusters } from './navMemory';
import { useRailDrawn } from './railDrawn';
import { WhatsNewBanner } from '../whatsnew/WhatsNew';

/**
 * The application shell.
 *
 * ## The whole point, in one sentence
 *
 * `Rail`, `Menu`, `Dock` and `Drawer` are siblings of `<Outlet/>`, never
 * descendants of it. `Shell` is the element of a **pathless layout route** at
 * the top of the tree (`../router/routes.tsx`), so React Router keeps this
 * component mounted across every navigation and replaces only what the Outlet
 * renders.
 *
 * Nothing per link is needed to keep the rail, the menu and the drawer: the
 * router simply does not unmount a layout route whose path still matches.
 *
 * **A change of cluster is a navigation like any other**, and the Shell is above
 * `/c/:cluster` rather than inside it, so a terminal opened on one pod list is
 * still open four clusters later. The Drawer is the one region that deliberately
 * does *not* survive it, for the reason written in `Drawer.tsx`.
 *
 * ## And it is where a lapsed session is noticed
 *
 * `SessionWatch` renders nothing. It is mounted here rather than at the root
 * because the Shell is exactly the set of routes that require a session - the
 * sign-in view is the one route outside it - so a 401 from the sign-in form
 * itself has no watcher to send it back to the page it is already on.
 *
 * ## No rail for one cluster
 *
 * The rail is for choosing a cluster, and with one there is nothing to choose:
 * every in-cluster console and every desktop with one kubeconfig
 * context drew a column of monograms, bars and folds around one tile. So with
 * exactly one cluster the rail is not drawn and the page has the width.
 *
 * **The condition is the count, never the profile** - see `onlyCluster` - so a
 * one-context desktop loses the rail too, and the day a deployment reaches two
 * clusters it comes back because there are two, without a reload: the cluster
 * list is a live query. **And Log out, Arrange and Kinds move to the header**
 * (`Topbar`, `SessionInline`, `ArrangeInline`, `KindsToggle`), because the
 * rail's foot was where all three were: a console with one cluster and no way
 * to sign out is what this must never produce, and one with no way to reveal a
 * hidden cluster, rename it, or bring the kinds column back is the same defect,
 * quieter. (Log out is drawn in neither place where there is no way to sign
 * back in - the loopback desktop; `SessionTile.tsx` says why.)
 *
 * **Kinds is at the bottom left in every mode**: the
 * rail's foot, or where there is no rail the open column's foot or, folded,
 * over the page's corner (`KindsCorner`) - the in-cluster console included.
 * `useKindsAt` holds it for all of them.
 */
export function Shell(): ReactNode {
  const clusters = useClusters();
  // `railDrawn.ts` holds the condition, because the top bar reads it too.
  const railDrawn = useRailDrawn();
  // The page's scroll, put back once Settings is left for the page it was
  // opened from.
  useRestoreSettingsScroll();

  // The menu's per-cluster snapshots of a cluster that has left the list are
  // let go of. Only on an `ok` answer: a list that could not be
  // read says nothing about which clusters exist.
  const listed = clusters.data?.state === 'ok' ? clusters.data.items : undefined;
  useEffect(() => {
    if (listed !== undefined) forgetOtherClusters(listed.map((cluster) => cluster.name));
  }, [listed]);
  return (
    // `DockProvider` wraps the whole shell rather than the panel, because the
    // controls that *open* a tab are on the pages inside the Outlet and the
    // panel that draws it is outside. It holds no live object - `live/handles.ts`
    // does - so wrapping widely costs a context and nothing else.
    <DockProvider>
      <PageFactsProvider>
      {/* No `text-sm` here: a size set on the one element every page is inside
          would shrink the whole console under the 14px `body`. A base size
          belongs in `theme.css` on `body`, where a component cannot quietly
          disagree with it. */}
      {/* `relative` is what makes `overflow-hidden` hold the page to the
          window. An `absolute` box with no positioned ancestor - every
          `sr-only` live region is one - takes the page as its containing block,
          so no scroller between it and the page clips it, and the document
          grew to reach where it sat: a second scroll bar beside the drawer's
          Positioned here, nothing
          inside the shell can make the document taller than the window. Each
          scroller is positioned too (`main`, the drawer, the tab panel, the
          list's `#panel`, the log pane), so what sits in it is clipped and
          scrolled by it rather than by this box. */}
      <div className="relative flex h-dvh w-full overflow-hidden bg-bg text-fg">
        {/* The icon sheet, once, above everything that draws one. `<use>`
            resolves against the document, so an `<Icon>` rendered in a tree that
            does not carry this draws nothing at all and reports nothing - which
            is why it is mounted here, where nothing unmounts it, rather than per
            page. */}
        <IconSprite />
        <SessionWatch />
        {/* One live region for the console, mounted where nothing unmounts it.
            `LiveRegion.tsx` says why there is exactly one. */}
        <LiveRegion />
        {railDrawn ? <Rail /> : null}
        <Menu />

        {/* The list's column and the drawer share one positioned box, so that
            below `sm` - a phone, where a list and a drawer cannot both be read
            side by side - the drawer can cover exactly this box as a sheet,
            leaving the rail and the kinds column where they are. Above `sm`
            the box is a plain row and nothing about the two changes. */}
        <div className="relative flex min-w-0 flex-1">
        {/* Based at the list's usable minimum rather than at zero, so a drawer
            the operator widened gives way before the list does - and stops at
            its default, whatever the window. `Drawer.tsx` says how the two
            share it; everything else in this row is `shrink-0`, so the basis
            changes nothing while no drawer is open. */}
        <div className="flex min-w-0 flex-1 flex-col" style={{ flexBasis: LIST_MIN }}>
          {/* Above the Outlet and outside it, so a navigation replaces the page
              beneath it and leaves the breadcrumb standing. It draws its live
              indicator from what the page declared through `pageFacts`, never
              from being rendered - see `Topbar.tsx`. */}
          <Topbar />
          {/* Welcome and What's new (#80), on the first visit of a release. */}
          <WhatsNewBanner />
          {/* An extension the cluster gives a reason to turn on,
              above the page and outside it, like the
              breadcrumb: it stays while one navigates the cluster. */}
          <Suggestions />
          {/* The only region a navigation replaces. */}
          <div className="relative flex min-h-0 flex-1 flex-col">
            <main id="page" className="relative min-h-0 flex-1 overflow-auto overscroll-contain">
              <Outlet />
            </main>
            {/* Beside the page and not inside it, so it stays when the page
                scrolls; above the dock, which is outside this box. */}
            <KindsCorner />
          </div>
          <Dock />
        </div>

        <Drawer />
        </div>
      </div>
      </PageFactsProvider>
    </DockProvider>
  );
}
