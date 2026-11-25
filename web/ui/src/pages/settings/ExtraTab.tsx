import { useMutation, useQuery } from '@tanstack/react-query';
import { useId, useState, type ReactNode } from 'react';

import {
  listExtensionSuggestions,
  queryKeys,
  setExtensionEnabled,
  type Cluster,
  type ExtensionInfo,
} from '../../api';
import { Icon } from '../../components/Icon';
import { Pill } from '../../components/Pill';
import { TileGrid } from '../../components/Tile';
import type { Extension } from '../../extension';
import { focusRingTight } from '../../focus';
import { extraFavourite } from '../../shell/favouriteExtras';
import { useKindsChoice } from '../../shell/kindsMemory';
import { useShellIfPresent } from '../../shell/ShellContext';
import { ExtraPreview } from './ExtraPreview';
import { ExtraSettingsModal } from './ExtraSettingsModal';
import { badgesOf, ExtraGlyph, ExtraSwitch, lockState, StateBadge, useExtraReread, WriteFailure } from './extraParts';

/**
 * Extra: one card per extension the build carries (`extras.Catalogue()`,
 * nothing it does not carry), for one cluster.
 *
 * **A card**: the extra's glyph (`Extension.icon`), its name and one line of
 * purpose, the On/Off switch, a row of badges (On or Off for the cluster,
 * pinned, its component absent, Suggested, locked, the tools it works with),
 * and two buttons side by side, Edit and About. Detail goes in tooltips.
 *
 * **Every extension is off until it is turned on, on every profile, and every
 * one is listed**. The server says, per row, whether the person looking may
 * press its switch (`writable`) and, where not, why (`locked`, `reason`) - the
 * page decides nothing about who may, and the writes refuse on their own:
 *
 * - **writable**: the switch writes one thing, one request
 *   (`setExtensionEnabled`), and the listing is read again after - the card
 *   draws what the console keeps, never what was pressed.
 * - **`values`**, **`pinned`**, **`unregistered`**: the switch, disabled, at
 *   its state, and the server's reason on one line under the badges.
 * - **`use`** (no `use` permission on the cluster): no switch, and the reason.
 *
 * **Edit** opens `ExtraSettingsModal` (switch, settings, columns, status, with
 * Save and Cancel). **About** opens `ExtraPreview`: what the extra does, reads
 * and needs, with Close as its only button - nothing is switched from there.
 * Both windows are drawn from the row the listing holds now, so they follow
 * every re-read, and close on their own when the row leaves the listing
 * (another cluster picked).
 */
export function ExtraTab({
  cluster,
  clusters,
  items,
}: {
  cluster: string;
  clusters: readonly Cluster[];
  items: readonly ExtensionInfo[];
}): ReactNode {
  // The extras this build carries, for their glyph and what each says about
  // itself. Absent outside a Shell (a test that renders the page alone).
  const shipped = useShellIfPresent()?.extensions;
  // Favourites are per cluster, kept with the favourite kinds (#111).
  const choice = useKindsChoice(cluster);
  const own = (key: string): Extension | undefined => shipped?.find((extension) => extension.key === key);
  // Which window is open, and on which cluster: one opened on another cluster
  // is not reopened by coming back to it.
  const [open, setOpen] = useState<{ cluster: string; key: string; what: 'about' | 'edit' } | null>(null);
  const opened = open?.cluster === cluster ? items.find((item) => item.key === open.key) : undefined;
  const absent = useAbsent(cluster, items);
  const { suggested, settled } = useSuggested(cluster, items);
  // **The order is taken once per cluster, then held while the page is open**
  // (#62): suggested first is the order the page opens on, and turning an
  // extra on must not slide another card under the pointer for the next click.
  const natural = suggestedFirst(absentLast(byLabel(items), absent), suggested);
  const [held, setHeld] = useState<{ cluster: string; keys: readonly string[] } | null>(null);
  // Set while rendering, once per cluster (React's "adjusting state when a
  // prop changes"): the next render draws the held order at once.
  if (settled && items.length > 0 && held?.cluster !== cluster) {
    setHeld({ cluster, keys: natural.map((item) => item.key) });
  }
  const ordered = held?.cluster === cluster ? heldOrder(items, held.keys) : natural;
  const on = items.filter((item) => item.enabled).length;

  return (
    <section className="flex flex-col gap-3" data-extras="">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 className="text-base font-medium">Extra pages</h2>
        <span data-extras-count="" title={`${on} of ${items.length} on for this cluster`}>
          <Pill neutral="muted">
            {on}/{items.length} on
          </Pill>
        </span>
        <ClusterPicker cluster={cluster} clusters={clusters} />
      </header>

      <TileGrid label="Extensions">
        {ordered.map((item) => (
          <ExtensionCard
            key={item.key}
            cluster={cluster}
            item={item}
            own={own(item.key)}
            absent={absent.get(item.key)}
            suggested={suggested.get(item.key)}
            onEdit={() => setOpen({ cluster, key: item.key, what: 'edit' })}
            onAbout={() => setOpen({ cluster, key: item.key, what: 'about' })}
            favourite={choice.isFavourite(extraFavourite(item.key))}
            onFavourite={choice.ready ? () => choice.toggleFavourite(extraFavourite(item.key)) : undefined}
          />
        ))}
      </TileGrid>
      {opened === undefined || open === null ? null : open.what === 'edit' ? (
        <ExtraSettingsModal
          key={`${cluster}/${opened.key}`}
          cluster={cluster}
          item={opened}
          absent={absent.get(opened.key)}
          own={own(opened.key)}
          onClose={() => setOpen(null)}
        />
      ) : (
        <ExtraPreview
          key={`${cluster}/${opened.key}`}
          cluster={cluster}
          item={opened}
          own={own(opened.key)}
          onClose={() => setOpen(null)}
        />
      )}
    </section>
  );
}

/** The extras alphabetically by label, case-insensitively: the order the menu
 *  draws them in, whatever order the listing arrived in. */
export function byLabel(items: readonly ExtensionInfo[]): ExtensionInfo[] {
  return [...items].sort((a, b) => a.label.toLowerCase().localeCompare(b.label.toLowerCase()));
}

/**
 * The extras whose component the cluster does not run, by key, with the short
 * state the tile draws ("cert-manager absent"). The detection is the Suggesters'
 * (`extension.Suggester`), the banner's own query: asked only while some extra
 * is off and could be pressed, and a read that failed is no answer - the tile
 * then says nothing, never "absent". An extra that is on is never absent.
 */
function useAbsent(cluster: string, items: readonly ExtensionInfo[]): ReadonlyMap<string, string> {
  const asks = items.some((item) => !item.enabled && item.writable);
  const suggestions = useQuery({
    queryKey: queryKeys.extensionSuggestions(cluster),
    queryFn: ({ signal }) => listExtensionSuggestions(cluster, signal),
    enabled: asks,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const out = new Map<string, string>();
  if (!asks || suggestions.data?.state !== 'ok') return out;
  const off = new Set(items.filter((item) => !item.enabled).map((item) => item.key));
  for (const one of suggestions.data.items) {
    if (one.absent === true && off.has(one.key)) out.set(one.key, one.evidence);
  }
  return out;
}

/**
 * The sentence the suggestion banner says for each off extra the cluster gives
 * a reason for, by key. The banner's own query; a failed read gives none.
 */
function useSuggested(
  cluster: string,
  items: readonly ExtensionInfo[],
): { suggested: ReadonlyMap<string, string>; settled: boolean } {
  const asks = items.some((item) => !item.enabled && item.writable);
  const suggestions = useQuery({
    queryKey: queryKeys.extensionSuggestions(cluster),
    queryFn: ({ signal }) => listExtensionSuggestions(cluster, signal),
    enabled: asks,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const out = new Map<string, string>();
  // Settled once the suggestions have answered or failed, or where nothing asks.
  const settled = !asks || suggestions.isFetched;
  if (!asks || suggestions.data?.state !== 'ok') return { suggested: out, settled };
  const off = new Set(items.filter((item) => !item.enabled).map((item) => item.key));
  for (const one of suggestions.data.items) {
    if (one.absent !== true && off.has(one.key)) out.set(one.key, `${one.evidence} on this cluster.`);
  }
  return { suggested: out, settled };
}

/** The cards in a held order: each by its place in `keys`, any key it does not
 *  hold after them in the order given. */
export function heldOrder(items: readonly ExtensionInfo[], keys: readonly string[]): ExtensionInfo[] {
  const place = new Map(keys.map((key, index) => [key, index]));
  const held = items.filter((item) => place.has(item.key)).sort((a, b) => (place.get(a.key) ?? 0) - (place.get(b.key) ?? 0));
  return [...held, ...items.filter((item) => !place.has(item.key))];
}

/** The suggested extras before the others, each group in the order it came
 *  (#62): what the cluster gives a reason to turn on is read first. */
export function suggestedFirst(items: readonly ExtensionInfo[], suggested: ReadonlyMap<string, string>): ExtensionInfo[] {
  return [...items.filter((item) => suggested.has(item.key)), ...items.filter((item) => !suggested.has(item.key))];
}

/** The absent extras after the others, each group in the order it came. */
export function absentLast(items: readonly ExtensionInfo[], absent: ReadonlyMap<string, string>): ExtensionInfo[] {
  return [...items.filter((item) => !absent.has(item.key)), ...items.filter((item) => absent.has(item.key))];
}

/**
 * Which cluster the cards are about, beside them so the switch's blast radius
 * is always visible. With several clusters the choice is the rail's icons
 * (`Rail.tsx`), which are always drawn then, so there is no second control.
 */
function ClusterPicker({ cluster, clusters }: { cluster: string; clusters: readonly Cluster[] }): ReactNode {
  const known = clusters.find((one) => one.name === cluster);
  return (
    <span
      className="ml-auto flex items-center gap-1.5 text-xs text-muted"
      data-extras-cluster={cluster}
      title={`Cluster ${cluster}`}
    >
      <Icon name="cluster" />
      <span className="font-mono text-fg">{known?.label ?? cluster}</span>
    </span>
  );
}

function ExtensionCard({
  cluster,
  item,
  own,
  absent,
  suggested,
  onEdit,
  onAbout,
  favourite,
  onFavourite,
}: {
  cluster: string;
  item: ExtensionInfo;
  /** What the build carries for it: its glyph and its tool badges. */
  own: Extension | undefined;
  /** Its short state where the cluster does not run its component. */
  absent: string | undefined;
  /** The banner's sentence where the cluster gives a reason to turn it on. */
  suggested: string | undefined;
  onEdit: () => void;
  onAbout: () => void;
  /** Marked as a favourite of this cluster (#111). */
  favourite: boolean;
  /** Mark or unmark it; undefined until the choice has been read. */
  onFavourite: (() => void) | undefined;
}): ReactNode {
  const reread = useExtraReread(cluster);
  const toggle = useMutation({
    mutationFn: (enabled: boolean) => setExtensionEnabled(cluster, item.key, enabled),
    onSettled: reread,
  });
  const writable = item.writable;
  const { shownDisabled, reason } = lockState(item);
  const reasonId = useId();
  const failed = toggle.error;
  const summary = item.summary === undefined || item.summary === '' ? undefined : item.summary;
  const tools = badgesOf(own);

  return (
    <li
      data-extra={item.key}
      data-on={item.enabled ? 'true' : 'false'}
      {...(absent === undefined ? {} : { 'data-extra-absent': absent })}
      {...(suggested === undefined ? {} : { 'data-extra-suggested-card': '' })}
      className={`flex min-w-0 flex-col gap-2.5 rounded-lg border p-3 ${
        item.enabled ? 'border-ok/40 bg-panel' : suggested !== undefined ? 'border-accent bg-accent/10' : 'border-line bg-panel'
      } ${absent === undefined ? '' : 'opacity-70'}`}
    >
      <div className="flex items-start gap-3">
        <ExtraGlyph name={own?.icon} on={item.enabled} />
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-sm font-medium text-fg" title={item.label}>
            {item.label}
          </h3>
          {summary === undefined ? null : (
            <p className="truncate text-xs text-muted" title={summary} data-extra-summary="">
              {summary}
            </p>
          )}
          <div className="mt-2 flex min-w-0 flex-wrap items-center gap-1" data-extra-badges="">
            <StateBadge on={item.enabled} cluster={cluster} />
            {item.pinned ? (
              <span data-extra-pinned="">
                <Pill neutral="muted" title="Set by the deployment (KT_EXTRAS)">
                  pinned
                </Pill>
              </span>
            ) : null}
            {absent === undefined ? null : (
              <span data-extra-absent-state="">
                <Pill neutral="muted">{absent}</Pill>
              </span>
            )}
            {suggested === undefined ? null : (
              <span
                data-extra-suggested={suggested}
                title={suggested}
                className="inline-flex shrink-0 items-center gap-1 rounded-full border border-accent px-2 py-px text-2xs font-semibold text-fg"
              >
                <span aria-hidden="true" className="size-1.5 rounded-full bg-accent" />
                Suggested
              </span>
            )}
            {tools?.map((name) => (
              <span key={name} data-extra-tool="">
                <Pill neutral="muted">{name}</Pill>
              </span>
            ))}
          </div>
        </div>
        <button
          type="button"
          aria-pressed={favourite}
          aria-label={`Favorite: ${item.label}`}
          title={favourite ? 'A favorite. Press to unmark it.' : 'Mark as a favorite'}
          data-extra-favourite={item.key}
          disabled={onFavourite === undefined}
          onClick={onFavourite}
          className={`flex size-6 shrink-0 items-center justify-center rounded-md hover:text-fg ${favourite ? 'text-accent' : 'text-muted'} ${focusRingTight}`}
        >
          <Icon name={favourite ? 'starred' : 'star'} />
        </button>
        {writable || shownDisabled ? (
          <ExtraSwitch
            on={item.enabled}
            label={`${item.label}, cluster ${cluster}`}
            disabled={toggle.isPending || !writable}
            onToggle={() => toggle.mutate(!item.enabled)}
            describedBy={reason === undefined ? undefined : reasonId}
          />
        ) : null}
      </div>

      {reason === undefined ? null : (
        <p id={reasonId} className="flex min-w-0 items-center gap-1.5 text-xs text-muted" data-extra-reason={item.locked ?? ''}>
          <Icon name="lock" />
          <span className="truncate" title={reason}>
            {reason}
          </span>
        </p>
      )}
      {failed === null ? null : <WriteFailure error={failed} />}

      <div className="mt-auto flex items-center gap-2 border-t border-line pt-2.5">
        <button type="button" onClick={onEdit} aria-label={`Edit ${item.label}`} data-extra-edit="" className={CARD_BUTTON}>
          <Icon name="settings" />
          Edit
        </button>
        <button type="button" onClick={onAbout} aria-label={`About ${item.label}`} data-extra-about="" className={CARD_BUTTON}>
          <Icon name="info" />
          About
        </button>
      </div>
    </li>
  );
}

/** Edit and About: the same size, side by side. */
const CARD_BUTTON = `flex items-center gap-1.5 rounded-md border border-line px-2.5 py-1 text-xs text-fg hover:bg-panel-2 ${focusRingTight}`;
