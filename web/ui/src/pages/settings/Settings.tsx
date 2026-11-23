import { type ReactNode } from 'react';
import { NavLink, Navigate, useLocation, useParams, useSearchParams } from 'react-router';

import { Icon } from '../../components/Icon';
import { focusRing } from '../../focus';
import { KeyLayer } from '../../keyboard/layers';
import { useKeyLayer } from '../../keyboard/useKeyLayer';
import { useDeclarePageFacts } from '../../shell/pageFacts';
import { useNoteSettingsScroll, useSettingsBack } from '../../shell/settingsOrigin';
import { CLUSTER_PARAM, useExtraTab, useSettings, type ExtraTab as ExtraTabState } from './data';
import { ExtraTab } from './ExtraTab';
import { GeneralTab } from './GeneralTab';
import { InfosTab } from './InfosTab';

/**
 * The console's settings, at `/settings/:tab`.
 *
 * ## Each tab is a real address
 *
 * The addressability is deliberate: a `useState` tab strip loses reload
 * and loses a bookmark, and this is the page somebody is told to open by
 * somebody else. `TABS` is the vocabulary in **one place** so a tab renamed here
 * cannot leave a link pointing at one that no longer exists.
 *
 * **The Extra tab is drawn only where there is something to show.** One tile per
 * extension the build carries, every one off until it is turned on, with a
 * button to turn it on and the extension's own settings. In a cluster the
 * chart's values (`KT_EXTRAS`) say which may be turned on, and a person holding
 * the `use` permission on one turns it on; the listing says per row whether this
 * person may, and why not (`ExtraTab.tsx`). Drawn on any profile only where the
 * listing names at least one extra (`useExtraTab`). A build that carries none
 * has two tabs, and `/settings/extra` is not a dead address: where the tab is
 * not drawn it lands on General like any other stale bookmark, so removing a tab
 * needs no redirect of its own.
 *
 * ## The whole width
 *
 * The page uses the whole width of the screen rather than a narrow column. A
 * form label must not sit a screen away from its control, and that is each
 * tab's job: General lays its sections out as cards in a grid
 * that adds a column as the screen widens, so a section is never wider than a
 * card, Extra draws its tiles the same way, and Infos is one small card.
 *
 * ## The way back
 *
 * Opened from a console page, Settings draws a « ← Back » control before its
 * title, the page's whole address in its tooltip, that returns to that page exactly - its address, and its scroll - and Escape
 * does the same where no dialog or menu holds it. The tabs replace the history
 * entry and carry the origin along, so the browser's Back leaves Settings in
 * one press. `../../shell/settingsOrigin.ts` holds the mechanism.
 *
 * **Anything unrecognized lands on General** rather than on an empty panel.
 * A stale bookmark is a stale
 * bookmark, not an error page.
 *
 * ## What is deliberately not here
 *
 * **Emptying the archive.** There is no control for it: `ClearRequest.scope` is
 * `preferences | clusterState | all` and has no arm for the archive. It would
 * be a **destructive** act needing either a fourth scope or an endpoint of its
 * own, so it is not offered here.
 *
 * **A read-only guardrail section and a change-password section** are not built
 * here either.
 */

/** The tabs, and the vocabulary in one place. */
const TABS = [
  { key: 'general', label: 'General' },
  { key: 'extra', label: 'Extra' },
  { key: 'infos', label: 'Infos' },
] as const;

type TabKey = (typeof TABS)[number]['key'];

function isTab(value: string | undefined): value is TabKey {
  return TABS.some((tab) => tab.key === value);
}

export function SettingsPage(): ReactNode {
  const { tab } = useParams<{ tab: string }>();
  const settings = useSettings();
  const extra = useExtraTab();
  const asked = useSearchParams()[0].get(CLUSTER_PARAM);
  const carried = asked === null ? '' : `?${CLUSTER_PARAM}=${encodeURIComponent(asked)}`;
  // Where Settings was opened from, carried in the history entry's state, and
  // forwarded by every move inside Settings.
  const { state } = useLocation();
  const way = useSettingsBack();
  useNoteSettingsScroll(way?.origin);
  useKeyLayer(
    KeyLayer.Page,
    (event) => {
      if (event.key !== 'Escape' || way === undefined || editing(event.target)) return false;
      way.back();
      return true;
    },
    way !== undefined,
  );
  // The tabs this console draws: Extra only where `useExtraTab` says so.
  const drawn = TABS.filter((one) => one.key !== 'extra' || extra.state === 'shown');
  // The tab title and the breadcrumb's last segment. A page that declares
  // nothing leaves the window titled "KubeTower" alone, which is a taskbar of
  // identical entries.
  // Declared above the redirect: a hook behind a condition changes the hook
  // order with the address.
  useDeclarePageFacts({
    title: `Settings / ${TABS.find((one) => one.key === tab)?.label ?? 'General'}`,
  });

  // A stale bookmark or a hand-edited address. It lands on General rather than
  // on an empty panel or a 404; the page the user wanted is one of the tabs.
  if (!isTab(tab)) return <Navigate to={`/settings/general${carried}`} state={state} replace />;
  // Extra where this console draws no Extra tab is a stale address like any
  // other. Not while that is still being read: a reload of the tab would
  // otherwise land on General before the answer came in.
  if (tab === 'extra' && extra.state === 'none') return <Navigate to={`/settings/general${carried}`} state={state} replace />;

  return (
    // The whole width: the menu column is gone on this route (`Menu.tsx`), and
    // each tab lays its sections out in a grid of cards rather than stretching
    // one form across it. Not centered - the page starts at the rail's edge like
    // every other.
    <div className="flex w-full min-w-0 flex-col gap-4 p-4">
      <header className="flex flex-wrap items-center gap-3">
        {way === undefined ? null : (
          <button
            type="button"
            onClick={way.back}
            title={way.origin.path}
            data-settings-back={way.origin.path}
            className={`flex items-center gap-1.5 rounded-md border border-line px-2 py-1 text-xs text-fg hover:bg-panel-2 ${focusRing}`}
          >
            <Icon name="back" />
            Back
          </button>
        )}
        <h1 className="text-xl font-medium">Settings</h1>
      </header>

      <nav aria-label="Settings sections" className="flex gap-1 border-b border-line">
        {drawn.map((each) => (
          <NavLink
            key={each.key}
            to={`/settings/${each.key}${carried}`}
            state={state}
            replace
            className={({ isActive }) =>
              `-mb-px border-b-2 px-3 py-1.5 text-xs ${focusRing} ${
                isActive ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg'
              }`
            }
          >
            {each.label}
          </NavLink>
        ))}
      </nav>

      {settings.isPending ? (
        <p role="status" className="text-sm text-muted">
          Reading the settings…
        </p>
      ) : settings.isError ? (
        // Not one of the six states, and it must not be dressed as one. Settings
        // are this process's own file: they cannot be unreachable, and a file the
        // console cannot read is a broken console.
        <div role="alert" className="rounded-lg border border-line bg-panel-2 p-3 text-sm">
          <p className="font-medium text-bad">The console could not read its own settings</p>
          <p className="mt-1 break-words text-muted">
            {settings.error instanceof Error ? settings.error.message : 'No reason was given.'}
          </p>
          <p className="mt-1 text-muted">
            That is a fault in this console rather than anything about a cluster.
          </p>
        </div>
      ) : (
        <Tab tab={tab} settings={settings.data} extra={extra} />
      )}
    </div>
  );
}

/** A field being typed in keeps its own Escape. */
function editing(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLElement &&
    (target.isContentEditable || target.closest('input, textarea, select, [contenteditable="true"]') !== null)
  );
}

function Tab({
  tab,
  settings,
  extra,
}: {
  tab: TabKey;
  settings: ReturnType<typeof useSettings>['data'] & object;
  extra: ExtraTabState;
}): ReactNode {
  switch (tab) {
    case 'extra':
      return extra.state === 'shown' ? (
        <ExtraTab cluster={extra.cluster} clusters={extra.clusters} items={extra.items} />
      ) : (
        <p role="status" className="text-sm text-muted">
          Reading which extra pages this console carries…
        </p>
      );
    case 'infos':
      return <InfosTab info={settings.info} />;
    default:
      return <GeneralTab settings={settings} />;
  }
}
