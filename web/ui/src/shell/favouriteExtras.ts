/**
 * **A favourite extra is kept with the favourite kinds** (#111), as
 * `extra:<key>` in the same per-cluster list (`kindsMemory.ts`): the same
 * store - the desktop's state file, or this browser - the same order (the one
 * they were marked in), and the menu's one Favorites section draws both. No
 * kind slug holds a colon, so the two never meet.
 */
const PREFIX = 'extra:';

/** The favourite's entry for the extra `key`. */
export function extraFavourite(key: string): string {
  return `${PREFIX}${key}`;
}

/** The extra a menu row opens, from its href: `/c/<cluster>/x/<key>[/...]`. */
export function extraKeyOfHref(href: string | undefined): string | undefined {
  if (href === undefined) return undefined;
  return /^\/c\/[^/]+\/x\/([^/?#]+)/.exec(href)?.[1];
}

/** The favourite entry for the extra a row opens, or undefined for any other row. */
export function extraFavouriteOfHref(href: string | undefined): string | undefined {
  const key = extraKeyOfHref(href);
  return key === undefined ? undefined : extraFavourite(key);
}

/** The extras among a cluster's favourites, in the order they were marked. */
export function favouriteExtraKeys(favourites: readonly string[]): string[] {
  return favourites.filter((entry) => entry.startsWith(PREFIX)).map((entry) => entry.slice(PREFIX.length));
}
