import { Fragment, useEffect, useState, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';

import { useCapabilities, useClusters, useKeepsPreferences, type Cluster } from '../api';
import { clusterPath } from '../clusters/clusterPath';
import { btnGhost } from '../components/buttons';
import { CountBadge } from '../components/OverflowMenu';
import { useUpdateBadge } from '../pages/settings/UpdateControl';
import { ClusterName } from '../components/ClusterName';
import { useDockIfPresent } from '../dock/DockContext';
import { focusRing } from '../focus';
import { usePageFacts, type Crumb } from './pageFacts';
import { RailArrange } from './RailArrange';
import { SessionInline, useAccountMenu } from './SessionTile';
import { useOpenSettings } from './settingsOrigin';
import { useSharedCluster } from './sharedCluster';
import { onlyCluster, stateDotClass, stateWord } from './clusterState';

/**
 * The strip above the page: where you are, and the three controls that do not
 * belong to any one page.
 *
 * A region in `Shell.tsx`, above the `<Outlet/>` and outside it — so a
 * navigation replaces the page beneath it and leaves this standing, which is
 * what makes the breadcrumb a statement about the console rather than something
 * each page has to draw.
 *
 * ## The breadcrumb says the nameplate and addresses the real name
 *
 * `KubeTower / <cluster> / <page>`. The cluster segment shows the chosen
 * **nameplate**, with the real context name on the tooltip, and every URL it
 * builds carries the real name untouched. That split is not cosmetic: a raw EKS
 * ARN is the thing the API server answers to and the last thing a person can
 * read, so the console has to show one and address the other. Where the cluster
 * list has not answered yet, or the cluster was never renamed, the segment is
 * the real name — which is honest rather than blank.
 *
 * **And the segment is a link to the cluster overview, drawn as one**: drawn
 * as a title it would not read as the way to the overview. It is `text-accent`, like every other
 * navigation in a sentence here, and the tooltip's first line says where it
 * goes. The rail tile for the active cluster goes to the same place (`Rail.tsx`,
 * `Tile`), so there are two one-click routes home from anywhere on a cluster.
 *
 * ## The page's segments link too, but the last
 *
 * Drawn as one string, `facts.title`, an object page's `Pod / default / api-0`
 * would name the list and the namespace it came from and lead to neither. A
 * page that has places in its title declares them as
 * a `trail` (`pageFacts.tsx`): the kind links to its list as the menu would
 * open it, the namespace to that list narrowed to it, drawn like the cluster
 * segment - and, like it, cut inside the link so the focus ring is not.
 * The last segment is where you are and is never a link, whatever the page
 * declared. A page that declares only a title is drawn as one segment.
 *
 * ## No live indicator
 *
 * Nothing here says a page refreshes: such an indicator would say nothing a
 * person needs, on every list. A list still refreshes on the server's interval
 * (`ListPage.refreshSeconds`, `api/queries.ts`).
 *
 * ## Settings and Log out under the name
 *
 * Where the header draws who is signed in - one cluster, so no rail, and a way
 * to sign back in - the name is a button, and the menu it opens holds Settings
 * and Log out (`SessionInline`). There is then no Settings link here: one way
 * to each, and the right end of the header is Arrange, the session warning and
 * the name. Arrange stays a control of its own, and the warning stays drawn,
 * because a warning in a closed menu is not read.
 *
 * **Where no name is drawn here, Settings stays a plain link**: the loopback
 * desktop, which has no way back in and so no Log out to put beside it
 * (`useSignsBackIn`), and a console with a rail, whose name and Log out are at
 * the rail's foot (`SessionTile`) - a menu of one entry would be a link with an
 * extra click.
 *
 * ## Two controls, each absent rather than disabled where it cannot work
 *
 * The terminal is drawn only with a cluster to point it at **and**
 * `capabilities.localShell`: *a rule applied to the controls somebody thought
 * of leaves the one they did not*.
 * Settings is always there - a link, or the first entry of the menu under the
 * signed-in name (below).
 *
 * Windows has a PTY layer: `internal/terminal/pty_windows.go` is a ConPTY
 * implementation, `ptysupport_windows.go` measures the export rather than
 * asserting its absence, and the Windows desktop binary answers
 * `localShell: true`.
 *
 * So the capability is read for the general reason rather
 * than a platform: a profile may not offer a local shell, and Windows before
 * 10 1809 has no `CreatePseudoConsole` for `conptyAvailable` to find. Naming a
 * platform in a comment would date it — the capability answers the question
 * per build, which is why it exists.
 *
 * **There is no Cluster link**: the way
 * to every cluster is the console's home, `/`, which the `KubeTower` crumb
 * at the left of this bar already leads to (on the in-cluster console, to its
 * one cluster), and `/health` now redirects there (`router/routes.tsx`).
 */
export function Topbar(): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  const clusters = useClusters();
  const shared = useSharedCluster();
  const capabilities = useCapabilities();
  const facts = usePageFacts();
  const dock = useDockIfPresent();
  const openSettings = useOpenSettings().onClick;
  // An update ready to install marks every way into Settings (#48).
  const badge = useUpdateBadge();
  const settingsTo = cluster === undefined ? '/settings' : `/settings?cluster=${encodeURIComponent(cluster)}`;

  const known =
    clusters.data?.state === 'ok'
      ? clusters.data.items.find((item: Cluster) => item.name === cluster)
      : undefined;
  const nameplate = named(known) ? known.label : cluster;

  useDocumentTitle(cluster, named(known) ? known.label : undefined, facts.title);

  const localShell = capabilities.data?.localShell === true;
  // With one cluster there is no rail (`Shell.tsx`), so what the rail carried
  // for it is drawn here: its state beside its name, Arrange, and the session.
  const alone = onlyCluster(clusters.data);
  const home = shared.cluster === undefined ? '/' : clusterPath(shared.cluster.name);
  // Settings is in the menu under the name wherever that menu is drawn.
  const accountMenu = useAccountMenu() && alone !== undefined;
  // Below `sm` only the breadcrumb's last part is drawn: at 390px five parts
  // shared the bar and each was cut to one letter. The rail and the kinds
  // column are the way back on a phone; the place you are on has to read.
  const lastIs = facts.title !== null ? 'page' : cluster !== undefined ? 'cluster' : 'root';
  const early = (part: typeof lastIs): string => (part === lastIs ? '' : ' max-sm:hidden');

  return (
    <div
      id="topbar"
      className="flex h-9 shrink-0 pointer-coarse:h-12 items-center gap-2 border-b border-line bg-panel px-3"
    >
      <nav aria-label="Breadcrumb" className="min-w-0 flex-1">
        <ol className="flex min-w-0 items-center gap-1.5 text-xs text-muted">
          {/* Below `sm` the root crumb gives way rather than painting over the
              controls: the Kinds button takes its room at 500px
              with the column shown. Above `sm` it keeps its whole word
              and its focus ring unclipped. */}
          <li className={`shrink-0 max-sm:min-w-0 max-sm:shrink max-sm:truncate${early('root')}`}>
            {/* The console's root, which on the in-cluster console is its
                cluster: `/` would only redirect there, and
                a crumb should say where it goes rather than where it bounces. */}
            <Link to={home} className={`rounded-sm hover:text-fg ${focusRing}`}>
              KubeTower
            </Link>
          </li>
          {cluster !== undefined && (
            <>
              <li aria-hidden className="shrink-0 text-line max-sm:hidden">
                /
              </li>
              {/* No floor: at a 500px window the controls leave this crumb no
                  room and it shrinks to nothing - a floor would paint it over
                  the Shell control (measured on the `names` scene). */}
              <li className={`flex min-w-0 shrink${early('cluster')}`}>
                <Link
                  to={clusterPath(cluster)}
                  // Two lines. The first says where the link goes, so the
                  // segment does not read as a title. The second is the real
                  // context name, on the element showing the nameplate:
                  // whoever has to type it into kubectl can read it here
                  // without going to Settings.
                  title={`Cluster overview\n${cluster}`}
                  // `text-accent` at rest and the console's own ring: the
                  // affordance every other in-sentence navigation carries
                  // (`Health.tsx`, `HelmList.tsx`), rather than `text-fg`
                  // with an underline that appeared only under the pointer.
                  // One line, cut in the middle where the crumb has no room:
                  // `ClusterName`, the component every surface that
                  // draws a cluster name goes through.
                  className={`flex min-w-0 text-accent underline-offset-2 hover:underline ${focusRing}`}
                >
                  <ClusterName name={nameplate ?? cluster} titled={false} />
                </Link>
              </li>
              {alone !== undefined && alone.name === cluster ? (
                <li className={`shrink-0${early('cluster')}`}>
                  {/* The rail tile's dot, where the tile is gone: one cluster
                      still has a state, and "could not be reached" must stay
                      as visible as it was. */}
                  <span
                    role="img"
                    aria-label={stateWord(alone.state)}
                    title={alone.reason?.message ?? stateWord(alone.state)}
                    className={`block size-1.5 rounded-full ${stateDotClass(alone.state)}`}
                  />
                </li>
              ) : null}
            </>
          )}
          {facts.title !== null &&
            pageSegments(facts.title, facts.trail).map((segment, index, all) => (
              <Fragment key={index}>
                <li aria-hidden className="shrink-0 text-line max-sm:hidden">
                  /
                </li>
                {/* Every segment that names a place links to it, drawn as the
                    cluster's crumb is; the last one is where you are and is
                    text whatever the page gave it. */}
                {segment.to !== undefined && index < all.length - 1 ? (
                  // Cut on the inner span, not on the item: `truncate` is
                  // `overflow: hidden`, and on the item it would clip the
                  // link's focus ring, drawn outside it - the cluster crumb's
                  // structure above, for the same reason.
                  // The label on the tooltip too: a long namespace or name is
                  // cut to an ellipsis here, and the whole of it should be
                  // one hover away.
                  <li className="flex min-w-0 shrink max-sm:hidden">
                    <Link
                      to={segment.to}
                      title={segment.label}
                      className={`flex min-w-0 text-accent underline-offset-2 hover:underline ${focusRing}`}
                    >
                      <span className="truncate">{segment.label}</span>
                    </Link>
                  </li>
                ) : (
                  <li
                    title={segment.label}
                    className={`min-w-0 shrink truncate text-fg${index < all.length - 1 ? ' max-sm:hidden' : ''}`}
                  >
                    {segment.label}
                  </li>
                )}
              </Fragment>
            ))}
        </ol>
      </nav>

      <div className="flex shrink-0 items-center gap-1">
        {cluster !== undefined && localShell && dock !== null && (
          <button
            type="button"
            // The label names the cluster, because a row of dock tabs called
            // "Shell" is a row that cannot be told apart.
            title={`Open a shell on ${nameplate ?? cluster}`}
            // `nameplate ?? cluster` and not `nameplate`: the fallback is what
            // narrows it to a string inside this guard, and it is also the right
            // value - a tab labeled with the raw context name is readable, a
            // tab labeled `undefined` is a defect nobody notices until there
            // are two of them.
            onClick={() =>
              dock.openShell({ kind: 'terminal', cluster, label: nameplate ?? cluster })
            }
            className={btnGhost}
          >
            Shell
          </button>
        )}
        {accountMenu ? null : (
          <Link
            to={settingsTo}
            onClick={openSettings(settingsTo)}
            title={badge.shown ? badge.title : 'Settings'}
            className={btnGhost}
          >
            Settings
            {badge.shown ? <CountBadge>1</CountBadge> : null}
          </Link>
        )}
        {alone !== undefined ? <ArrangeInline /> : null}
        {alone !== undefined ? <SessionInline /> : null}
      </div>
    </div>
  );
}

/**
 * Arrange, where there is no rail to carry it.
 *
 * The rail's foot holds Arrange, the session and Kinds (and the bar switcher,
 * where a second bar holds a tile). With no rail one cluster fills one bar, so
 * Arrange and the session are what the header has to take or they become
 * unreachable. It is not cosmetic: the nameplate, the color, the bar and
 * **hidden** are set nowhere else. A desktop that hid a cluster and then went
 * down to one kubeconfig context - or hid its only one - would have no way to
 * reveal it once the rail stopped being drawn.
 *
 * The same dialog as the rail's, so both reach the same four writes - and so
 * it is withheld where the rail's is: where the server keeps no preferences
 * (the server profile and several replicas, `Capabilities.preferences`) those
 * writes are not registered, and an Arrange in the header would be a dialog of
 * 404s. Under the server profile this is the arm that matters: it serves one
 * cluster, so the rail is not drawn and the header is where Arrange would be.
 */
function ArrangeInline(): ReactNode {
  const [arranging, setArranging] = useState(false);
  const keeps = useKeepsPreferences();
  // Not before the capabilities have answered, for the reason `Rail.tsx` gives
  // for its own Arrange: unknown reads as "keeps", and that is
  // right for drawing and wrong for a dialog of writes.
  const decided = !useCapabilities().isPending;
  if (!keeps || !decided) return null;
  return (
    <>
      <button
        type="button"
        onClick={() => setArranging(true)}
        title="Arrange clusters: name, color, bar, hidden"
        className={btnGhost}
      >
        Arrange
      </button>
      {arranging ? <RailArrange onClose={() => setArranging(false)} /> : null}
    </>
  );
}

/** What the breadcrumb draws after the cluster: the page's trail where it
 *  declared one, its title as one segment where it did not. */
function pageSegments(title: string, trail: readonly Crumb[] | undefined): readonly Crumb[] {
  return trail !== undefined && trail.length > 0 ? trail : [{ label: title }];
}

/** A cluster carries its own name as its label until somebody renames it, so
 *  "has a nameplate" is the two differing and not the label being non-empty. */
function named(cluster: Cluster | undefined): cluster is Cluster {
  return cluster !== undefined && cluster.label !== '' && cluster.label !== cluster.name;
}

/**
 * The window title, which is what a taskbar of console windows is read from.
 *
 * **The cluster goes in only when it has been named.** A raw EKS ARN is neither
 * short enough to sit in a tab nor the thing anybody scans a taskbar for, so an
 * unnamed cluster leaves the title as `KubeTower` plus the page — which is less
 * information and more of it legible.
 */
function useDocumentTitle(
  cluster: string | undefined,
  nameplate: string | undefined,
  page: string | null,
): void {
  useEffect(() => {
    const parts = ['KubeTower'];
    if (cluster !== undefined && nameplate !== undefined) parts.push(nameplate);
    if (page !== null) parts.push(page);
    document.title = parts.join(' · ');
  }, [cluster, nameplate, page]);
}
