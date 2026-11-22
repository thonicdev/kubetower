import { useEffect, useRef, type RefObject } from 'react';

/**
 * The panel the rows scroll in, sent back to its top on a new kind and when
 * the answer for a new filter lands.
 */
export function useScrollReset(
  cluster: string | undefined,
  kind: string | undefined,
  answered: { selector: string; search: string },
): RefObject<HTMLDivElement | null> {
  // A React re-render paints nothing the eye catches and a navigation paints
  // nothing at all, so scrolling back to the top of a new kind is explicit work.
  // Checked by looking, which is the only way.
  const panel = useRef<HTMLDivElement>(null);
  /*
    And back to the top when the answer for a **new filter** lands.

    The rows of the previous filter stand in while the next is read, so the
    user may have scrolled them; when the narrower answer replaces them, its
    first matches would sit above the viewport, in a table that looks as if it
    found less than it did. Keyed on the filter the rows *answer*, which moves
    only when a real answer for a different filter arrives - never while the
    stand-in is up, and never on the two-second refresh of the same filter.
  */
  useEffect(() => {
    const node = panel.current;
    if (node === null) return;
    node.scrollTop = 0;
  }, [answered.selector, answered.search]);
  useEffect(() => {
    // The two properties and not `scrollTo`: the method is optional in the DOM
    // standard's own terms and absent in jsdom, so the smooth-scroll spelling of
    // this throws in every test that renders the page. Assigning both is the same effect with no capability to detect.
    const node = panel.current;
    if (node === null) return;
    node.scrollTop = 0;
    node.scrollLeft = 0;
  }, [cluster, kind]);
  return panel;
}
