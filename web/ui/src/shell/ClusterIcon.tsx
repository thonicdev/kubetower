import { useId, useMemo, type ReactNode } from 'react';

import { parseIcon, serialiseIcon, useIconRenderer, type IconRecipe } from './iconRecipe';

/**
 * A cluster's icon, drawn to fill the box it is
 * put in - or `fallback` while the renderer is loading, when there is no
 * recipe, and when the recipe cannot be drawn. The fallback is the colored
 * plate the tile has always been, so nothing is ever blank.
 *
 * The caller owns the box: its size, its rounding and its `overflow-hidden` -
 * or passes `frame`, the classes of a span the drawing is put in, for a box
 * that cannot clip (the rail tile, whose state badge sits over its edge).
 */
export function ClusterIcon({
  recipe,
  fallback,
  frame,
}: {
  recipe: IconRecipe | undefined;
  fallback: ReactNode;
  frame?: string;
}): ReactNode {
  const { renderer, version } = useIconRenderer(recipe === undefined ? [] : [recipe.style]);
  // `useId` is `«r1»`-shaped; an id that goes into `url(#…)` keeps to word
  // characters.
  const prefix = `kti${useId().replace(/[^\w-]/g, '')}`;
  // Keyed on the recipe's stored form rather than the object: callers parse
  // it on every render, and an equal recipe must not redraw. `version` is in
  // the key because the style may arrive after the renderer.
  const key = recipe === undefined ? undefined : serialiseIcon(recipe);
  const drawn = useMemo(() => {
    const parsed = parseIcon(key);
    return renderer === undefined || parsed === undefined ? null : renderer.iconElement(parsed, prefix);
    // `version` is not read here; it is what says a style has arrived since.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [renderer, key, prefix, version]);
  if (drawn === null) return fallback;
  return frame === undefined ? (
    drawn
  ) : (
    <span aria-hidden="true" data-cluster-icon="" className={frame}>
      {drawn}
    </span>
  );
}
