import type { DetailPage, NavMenu, ValueResult } from '../../api';
import type { Extension, ExtensionActivity, ExtensionTab, ExtensionTabObject } from '../../extension';

/**
 * The tabs extensions add to the object panel, and which extensions are on.
 *
 * **On is the server's word, never this bundle's.** An extension is in the
 * bundle on every cluster; it is on for a cluster when the server's menu lists
 * it under Extra, which is the gate's own answer. The menu's entry carries the extension's page
 * as `/c/{cluster}/x/{key}`, always that shape, and that last segment is the
 * key. A tab whose extension is not listed is not drawn - it
 * "degrades by being absent", the contract's rule for a tab whose page may not
 * be there.
 */

/** A tab as the panel draws it: the extension's, with the address it answers. */
export interface ContributedTab extends ExtensionTab {
  /** The extension's key. */
  extension: string;
  /** `x.<extension>.<tab>`, the value of `?tab=`. The prefix keeps it apart
   *  from the core's own tabs, whose keys are bare words. */
  id: string;
}

export function contributedTabs(extensions: readonly Extension[], kind: string): ContributedTab[] {
  const wanted = kind.toLowerCase();
  const out: ContributedTab[] = [];
  for (const extension of extensions) {
    for (const tab of extension.tabs ?? []) {
      if (tab.kinds.some((one) => one === '*' || one.toLowerCase() === wanted)) {
        out.push({ ...tab, extension: extension.key, id: `x.${extension.key}.${tab.key}` });
      }
    }
  }
  return out;
}

/** The activity view an extension offers for this kind: the first in the
 *  extensions' order, with the key it belongs to. */
export function contributedActivity(
  extensions: readonly Extension[],
  kind: string,
): (ExtensionActivity & { extension: string }) | undefined {
  const wanted = kind.toLowerCase();
  for (const extension of extensions) {
    const view = extension.activity;
    if (view !== undefined && view.kinds.some((one) => one === '*' || one.toLowerCase() === wanted)) {
      return { ...view, extension: extension.key };
    }
  }
  return undefined;
}

/** The marks a tab's `when` is asked about, from the panel's own payload;
 *  undefined while it has not been read, when no condition can be answered. */
export function marksOf(page: DetailPage | undefined): ExtensionTabObject | undefined {
  if (page === undefined) return undefined;
  return { labels: page.labels ?? {}, annotations: page.annotations ?? {} };
}

/** Whether a tab offered on this kind is drawn on this object. A tab with no
 *  `when` is; one with a `when` is only once the object has been read and the
 *  condition holds. */
export function answers(tab: ExtensionTab, marks: ExtensionTabObject | undefined): boolean {
  if (tab.when === undefined) return true;
  return marks !== undefined && tab.when(marks);
}

const EXTRA_PAGE = /\/x\/([^/?#]+)$/;

/** The keys of the extensions the server's menu lists as on for this cluster.
 *  Empty while the menu has not answered, or when it could not be read: no tab
 *  is drawn on a guess. */
export function enabledExtensions(menu: ValueResult<NavMenu> | undefined): Set<string> {
  const on = new Set<string>();
  if (menu?.state !== 'ok') return on;
  const extra = menu.data.groups.find((group) => group.key === 'extra');
  for (const item of extra?.items ?? []) {
    const match = EXTRA_PAGE.exec(item.href ?? '');
    if (match?.[1] !== undefined) on.add(decodeURIComponent(match[1]));
  }
  return on;
}
