import type { ReactNode } from 'react';
import { Link } from 'react-router';

import { useNav } from '../../api';
import { Icon } from '../../components/Icon';
import { focusRing } from '../../focus';
import { extraKeyOfHref, favouriteExtraKeys } from '../../shell/favouriteExtras';
import { useKindsChoice } from '../../shell/kindsMemory';

/**
 * The favourite extras of this cluster, a link each under the cluster's head
 * (#111), in the order they were marked. Only the ones the menu offers - on
 * for this cluster - and nothing at all while there are none.
 */
export function FavouriteExtras({ cluster }: { cluster: string }): ReactNode {
  const choice = useKindsChoice(cluster);
  const keys = favouriteExtraKeys(choice.favourites);
  const nav = useNav(cluster, keys.length > 0);
  const offered = new Map<string, { text: string; href: string }>();
  const menu = nav.data;
  for (const group of menu?.state === 'ok' ? menu.data.groups : []) {
    if (group.key !== 'extra') continue;
    for (const item of group.items) {
      const key = extraKeyOfHref(item.href);
      if (key !== undefined && item.href !== undefined && !offered.has(key)) offered.set(key, { text: item.text, href: item.href });
    }
  }
  const links = keys.flatMap((key) => {
    const found = offered.get(key);
    return found === undefined ? [] : [{ key, ...found }];
  });
  if (links.length === 0) return null;
  return (
    <nav aria-label="Favorite extras" data-favourite-extras="" className="flex flex-wrap items-center gap-2">
      <span className="text-accent" title="Favorites">
        <Icon name="starred" />
      </span>
      {links.map((link) => (
        <Link
          key={link.key}
          to={link.href}
          data-favourite-extra={link.key}
          className={`inline-flex min-w-0 items-center rounded-full border border-line bg-panel px-3 py-1 text-sm hover:border-accent ${focusRing}`}
        >
          <span className="truncate">{link.text}</span>
        </Link>
      ))}
    </nav>
  );
}
