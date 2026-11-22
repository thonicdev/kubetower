import { useEffect, useRef } from 'react';

import type { KeyLayer } from './layers';
import { claimKey, type KeyClaim } from './stack';

/**
 * Claim keys at a layer for as long as this component is mounted and `active`.
 *
 * The claim is read through a ref, so a handler that closes over fresh props
 * does not re-register the claimant on every render - re-registering would
 * move it to the front of its own layer on each render, which is a tie-break
 * changing under a component that never moved.
 */
export function useKeyLayer(layer: KeyLayer | number, claim: KeyClaim, active = true): void {
  const latest = useRef(claim);
  // Written in an effect rather than during render: a render may be thrown away
  // under concurrent rendering, and a ref written by a discarded render is a
  // handler nobody asked for.
  useEffect(() => {
    latest.current = claim;
  });

  useEffect(() => {
    if (!active) return;
    return claimKey(layer, (event) => latest.current(event));
  }, [layer, active]);
}

/**
 * The common case, named, because Escape is the key with five claimants and
 * writing the comparison out five times is how they drifted apart.
 *
 * `onEscape` returns nothing and is taken to have handled the key. A claimant
 * that sometimes declines should use `useKeyLayer` and say so.
 */
export function useEscape(layer: KeyLayer | number, onEscape: () => void, active = true): void {
  useKeyLayer(
    layer,
    (event) => {
      if (event.key !== 'Escape') return false;
      onEscape();
      return true;
    },
    active,
  );
}
