import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';

import {
  queryKeys,
  setRailNameplate,
  usePreferences,
  type Cluster,
  type ClusterColour,
  type RailNameplateWrite,
} from '../api';
import { ClusterName } from '../components/ClusterName';
import { MenuScrim } from '../components/MenuScrim';
import { focusRing } from '../focus';
import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { announce } from './announce';
import { ClusterIcon } from './ClusterIcon';
import { colourLabel, plateClass } from './clusterState';
import { settleIcon } from './firstIcon';
import { IconModal } from './IconModal';
import { iconOf, isResetIcon, QUICK_COLOURS, RESET_ICON, recolour, serialiseIcon, type IconRecipe } from './iconRecipe';

/** The menu's width, `w-64`, kept clear of the viewport's edges. */
const WIDTH = 256;
const MARGIN = 8;
/** Its height, about: enough to keep it on screen when the tile is low. */
const HEIGHT = 190;

type Change = Partial<Omit<RailNameplateWrite, 'cluster'>>;

/**
 * One cluster's look, from its tile: a small menu and a dialog.
 *
 * Opened by a right-click on a rail tile - and by the context-menu key,
 * Shift+F10 and F2, which raise the same `contextmenu` event or call the same
 * handler on the focused tile. The menu holds three things:
 *
 * - **Eight colors** (`QUICK_COLOURS`, the eight of the sixteen furthest
 *   apart). A click writes the cluster's color at once, as the swatch grid
 *   did: it is the background of the default icon, and of a stored icon too,
 *   whose background follows it. Every tile is an icon whatever is chosen.
 * - **Customize icon…**, which closes the menu and opens `IconModal`: every
 *   bundled icon library, searchable, the generated styles, and the controls.
 * - **Reset icon**, back to the default icon on the automatic color. The name
 *   is left alone; Arrange edits names. A cluster with no icon is given a
 *   random one (`firstIcon.ts`), so a reset tells that
 *   draw to leave this cluster alone on this page.
 *
 * ## A write that lands late closes nothing it did not open
 *
 * The write outlives the menu - Escape closes it while the request is still
 * out - and its success refreshes the clusters and the preferences whatever
 * happened since, because the change did land. The dialog closes on a save
 * only while it is still the one on screen.
 */
export function ClusterEditor({
  cluster,
  at,
  onClose,
}: {
  cluster: Cluster;
  /** The tile's box, which the menu is drawn beside. */
  at: { top: number; right: number };
  onClose: () => void;
}): ReactNode {
  const preferences = usePreferences();
  const stored = preferences.data?.rail?.nameplates?.[cluster.name];
  // Not before preferences have answered: the menu starts from them.
  if (preferences.isPending) return null;
  return <Editor cluster={cluster} at={at} onClose={onClose} stored={stored} />;
}

function Editor({
  cluster,
  at,
  onClose,
  stored,
}: {
  cluster: Cluster;
  at: { top: number; right: number };
  onClose: () => void;
  stored: { colour?: ClusterColour; icon?: string } | undefined;
}): ReactNode {
  const [customising, setCustomising] = useState(false);
  const client = useQueryClient();
  const title = useId();
  // What the tile draws now: the stored recipe, or the default icon.
  const drawn = iconOf(cluster.name, cluster.colour, stored?.icon);

  useEscape(KeyLayer.Menu, onClose, !customising);

  // Whether this editor is still the one on screen: set again on every mount,
  // so StrictMode's mount-unmount-mount leaves it true.
  const open = useRef(true);
  useEffect(() => {
    open.current = true;
    return () => {
      open.current = false;
    };
  }, []);

  const write = useMutation({
    mutationFn: ({ change }: { change: Change; said: string; close: boolean }) =>
      setRailNameplate(cluster.name, change),
    onSuccess: async (_result, { said, close }) => {
      announce(said);
      await Promise.all([
        client.invalidateQueries({ queryKey: queryKeys.clusters }),
        client.invalidateQueries({ queryKey: queryKeys.preferences }),
      ]);
      if (close && open.current) onClose();
    },
  });
  const pending = write.isPending;

  /** A color from the menu: the cluster's color, and the background of a
   *  stored icon with it, so the tile changes whatever it is drawn from. */
  function colour(key: ClusterColour): void {
    if (pending) return;
    write.mutate({ change: recolour(key, stored?.icon), said: `${cluster.label} is now ${colourLabel(key)}.`, close: false });
  }

  function save(recipe: IconRecipe): void {
    const icon = serialiseIcon(recipe);
    // Against what is drawn, not what is stored: saving an untouched default
    // must not pin it, or it would stop following the cluster's color.
    if (icon === serialiseIcon(drawn)) {
      onClose();
      return;
    }
    write.mutate({ change: { icon }, said: `${cluster.label} has a new icon.`, close: true });
  }

  if (customising) {
    return (
      <IconModal
        context={cluster.name}
        label={cluster.label}
        initial={drawn}
        pending={pending}
        error={write.isError ? (write.error instanceof Error ? write.error.message : 'The icon was not saved.') : undefined}
        onSave={save}
        onClose={onClose}
      />
    );
  }

  // Beside the tile, and kept on screen.
  const width = Math.min(WIDTH, window.innerWidth - 2 * MARGIN);
  const top = Math.max(MARGIN, Math.min(at.top, window.innerHeight - HEIGHT - MARGIN));
  const left = Math.min(at.right + MARGIN, Math.max(MARGIN, window.innerWidth - width - MARGIN));
  const nothingStored = (stored?.icon === undefined || isResetIcon(stored.icon)) && stored?.colour === undefined;

  return (
    <>
      <MenuScrim onDismiss={onClose} />
      <div
        role="dialog"
        aria-labelledby={title}
        data-editor={cluster.name}
        onKeyDown={keepTabInside}
        // The position is arithmetic from the tile's box, which is what an
        // inline style is for; every color and size is still a class.
        style={{ top, left }}
        className="fixed z-20 w-64 max-w-[calc(100vw-var(--spacing)*4)] rounded-xl border border-line bg-panel p-3 text-sm shadow-2xl"
      >
        <div className="flex items-center gap-2">
          <span aria-hidden="true" className="relative flex size-9 shrink-0 overflow-hidden rounded-lg bg-panel-2">
            <ClusterIcon recipe={drawn} fallback={null} />
          </span>
          <div className="min-w-0 flex-1">
            <h2 id={title} className="flex min-w-0 text-sm font-medium">
              <span className="sr-only">Look of </span>
              <ClusterName name={cluster.label} />
            </h2>
            <p className="flex min-w-0 font-mono text-2xs text-muted">
              <ClusterName name={cluster.name} />
            </p>
          </div>
        </div>

        <p className="mt-3 text-xs text-muted">Color</p>
        <div role="radiogroup" aria-label={`Color for ${cluster.label}`} className="mt-1 flex items-center gap-1.5">
          {QUICK_COLOURS.map((key) => (
            <button
              key={key}
              type="button"
              role="radio"
              aria-checked={key === cluster.colour}
              aria-label={colourLabel(key)}
              title={colourLabel(key)}
              data-swatch={key}
              onClick={() => colour(key)}
              className={`size-6 rounded-md ${plateClass(key)} ${focusRing} ${
                key === cluster.colour ? 'ring-2 ring-fg ring-offset-2 ring-offset-panel' : ''
              }`}
            />
          ))}
        </div>

        {write.isError ? (
          <p role="alert" className="mt-2 text-xs text-bad">
            {write.error instanceof Error ? write.error.message : 'The change was not saved.'}
          </p>
        ) : null}

        <div className="mt-3 flex flex-col gap-1">
          <button
            type="button"
            // Focus goes here on open: the one thing the menu cannot do in place.
            autoFocus
            onClick={() => setCustomising(true)}
            className={`min-h-6 rounded-md border border-line px-2 py-1 text-left text-xs hover:bg-panel-2 pointer-coarse:min-h-11 ${focusRing}`}
          >
            Customize icon…
          </button>
          <button
            type="button"
            disabled={nothingStored}
            aria-disabled={pending}
            title={
              nothingStored
                ? 'Already the default icon on the automatic color'
                : pending
                  ? 'Saving…'
                  : 'Back to the default icon on the automatic color'
            }
            onClick={() => {
              if (!pending) {
                // Back to the default, and not to a new random icon: the
                // marker is stored, so a reload does not draw one either.
                settleIcon(cluster.name);
                write.mutate({
                  change: { colour: '', icon: RESET_ICON },
                  said: `${cluster.label} is back to its default icon.`,
                  close: true,
                });
              }
            }}
            className={`min-h-6 rounded-md border border-line px-2 py-1 text-left text-xs text-muted disabled:cursor-not-allowed disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 pointer-coarse:min-h-11 ${focusRing}`}
          >
            Reset icon
          </button>
        </div>
        <p className="mt-2 text-2xs text-muted">Right-click a tile, or press F2 or Shift+F10 on it. Arrange edits names.</p>
      </div>
    </>
  );
}

/** The controls Tab stops on, in order: what is enabled and in the tab order. */
const TABBABLE = 'button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Tab from the last control goes to the first, and Shift+Tab from the first to
 *  the last, so the keyboard never leaves for the page behind the scrim. */
function keepTabInside(event: KeyboardEvent<HTMLElement>): void {
  if (event.key !== 'Tab') return;
  const stops = [...event.currentTarget.querySelectorAll<HTMLElement>(TABBABLE)].filter((each) => each.tabIndex >= 0);
  const first = stops[0];
  const last = stops[stops.length - 1];
  if (first === undefined || last === undefined) return;
  const here = document.activeElement;
  if (event.shiftKey && (here === first || !event.currentTarget.contains(here))) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (here === last || !event.currentTarget.contains(here))) {
    event.preventDefault();
    first.focus();
  }
}
