import type { NavGroup } from '../api';
import type { Extension } from '../extension';

const EXTRA_PAGE = /\/x\/([^/?#]+)$/;

/**
 * The menu without the extras that declare no entry of their own
 * (`Extension.nav.menu === false`): an extra reached only from the panel of
 * the object it is about, as Pod Debugging is from a Pending pod.
 *
 * **Only the drawing drops them.** The server's Extra group still lists every
 * extra that is on, and `enabledExtensions` still reads it to decide which
 * tabs and tiles exist, so a contextual extra's tab is drawn exactly as before.
 * An Extra group left empty by this is not drawn.
 */
export function withoutContextual(groups: readonly NavGroup[], extensions: readonly Extension[] | undefined): readonly NavGroup[] {
  const contextual = new Set((extensions ?? []).filter((one) => one.nav.menu === false).map((one) => one.key));
  if (contextual.size === 0) return groups;
  return groups.flatMap((group) => {
    if (group.key !== 'extra') return [group];
    const items = group.items.filter((item) => {
      const match = EXTRA_PAGE.exec(item.href ?? '');
      return match?.[1] === undefined || !contextual.has(decodeURIComponent(match[1]));
    });
    return items.length === 0 ? [] : [{ ...group, items }];
  });
}
