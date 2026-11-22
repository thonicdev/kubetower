import { useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';

import { queryKeys, setRailNameplate, writeInOrder, type Cluster, type ClusterColour } from '../api';
import { randomIcon, serialiseIcon } from './iconRecipe';

/**
 * A cluster's first icon is drawn at random.
 *
 * One logo, one color and one style per cluster, drawn at random when the
 * cluster is first seen and changeable by hand after. So a
 * cluster with **no stored recipe** is given one - a style of the offered ones,
 * a swatch of the sixteen and a fresh seed (`randomIcon`) - and it is **stored
 * like a recipe chosen in the dialog**: the same nameplate `icon` write, with
 * nothing beside it. From then on the tile draws the stored recipe, so it stays
 * the same across reloads and in every browser, and the menu and the dialog
 * edit it like any other.
 *
 * What is left alone:
 *
 * - **A stored recipe**, whatever it is - one this build cannot read included:
 *   it may be a later build's, and a draw would overwrite it.
 * - **A color chosen by hand**: it is the drawn icon's background rather
 *   than a swatch drawn over it, so a hand choice is never undone by chance.
 * - **A cluster already drawn for, or reset, on this page.** The set below is
 *   per page and kept nowhere: it is what stops a second draw while the first
 *   write is still out or the preferences are being read again, a retry loop
 *   against a write that is refused, and *Reset icon* being undone the moment
 *   it lands.
 *
 * Only where the console keeps preferences (`Capabilities.preferences`): there
 * the write is not registered, and the tile is the default icon as before.
 */
const asked = new Set<string>();

/** Says a cluster's icon is not to be drawn on this page: *Reset icon* calls it. */
export function settleIcon(context: string): void {
  asked.add(context);
}

/** Forgets every draw, for the test setup. */
export function resetFirstIcons(): void {
  asked.clear();
}

/** What a cluster's nameplate holds, as far as a draw is concerned. */
type Plates = Readonly<Record<string, { readonly name?: string; readonly colour?: ClusterColour; readonly icon?: string } | undefined>>;

/**
 * The icons to write: one per cluster that has no stored recipe and has not
 * been drawn for on this page, in the cluster list's order. Pure apart from the
 * dice, which a test passes.
 */
export function iconsToDraw(
  clusters: readonly Pick<Cluster, 'name'>[],
  plates: Plates | undefined,
  random?: () => number,
): { cluster: string; icon: string }[] {
  const out: { cluster: string; icon: string }[] = [];
  for (const { name } of clusters) {
    const plate = plates?.[name];
    if (asked.has(name) || (plate?.icon !== undefined && plate.icon !== '')) continue;
    out.push({ cluster: name, icon: serialiseIcon(randomIcon(plate?.colour, random)) });
  }
  return out;
}

/**
 * Draws and stores the first icon of every cluster that has none. `enabled` is
 * the caller's: the capabilities have answered and keep preferences, and the
 * preferences have been read - before then "no stored recipe" means nothing.
 */
export function useFirstIcons(
  clusters: readonly Pick<Cluster, 'name'>[] | undefined,
  plates: Plates | undefined,
  enabled: boolean,
): void {
  const client = useQueryClient();
  useEffect(() => {
    if (!enabled || clusters === undefined) return;
    const draws = iconsToDraw(clusters, plates);
    if (draws.length === 0) return;
    for (const { cluster } of draws) asked.add(cluster);
    // One after another, as Arrange writes: the server rewrites one file per
    // write. A refusal leaves the rest as the default icon until the next load.
    writeInOrder(draws.map(({ cluster, icon }) => () => setRailNameplate(cluster, { icon })))
      .catch(() => undefined)
      .finally(() => {
        void client.invalidateQueries({ queryKey: queryKeys.preferences });
        void client.invalidateQueries({ queryKey: queryKeys.clusters });
      });
  }, [clusters, plates, enabled, client]);
}
