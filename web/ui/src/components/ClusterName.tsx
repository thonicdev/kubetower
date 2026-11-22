import type { ReactNode } from 'react';

/**
 * A cluster's name, on one line, cut where it does not fit.
 *
 * A long cluster name wrapped onto a second line makes the row taller than its
 * neighbors, and the surface looks broken. The real long
 * names are an EKS context, which is its ARN, and kubeadm's
 * `kubernetes-admin@kubernetes`. So **a cluster name never wraps**: it stays on
 * one line, is cut with an ellipsis where its slot ends, and is whole on `title`
 * and to a screen reader - the text node is the whole name; only the paint is
 * cut.
 *
 * ## Cut in the middle, keeping the part that tells two clusters apart
 *
 * A tail cut turns `…cluster/prod` and `…cluster/prod_OWNER` into one name
 * (`dock/truncate.ts` says the same). So the name is
 * drawn as two runs: one that shrinks, with its own ellipsis, and one that is
 * kept for as long as there is room for it.
 *
 * - **An ARN keeps its tail**, from the last `:` - `cluster/<name>` - and gives up
 *   its region and account first: `arn:aws:eks:eu-…cluster/prod`.
 * - **`user@cluster` keeps the cluster**, from the last `@`, and gives up the
 *   user first: `kube…@kubernetes-staging`. Every kubeadm context starts
 *   `kubernetes-admin@`, so the user is the part two contexts share; keeping
 *   it would make `…-prod` against `…-staging` lose their difference first.
 * - **Anything else keeps its last twelve characters**, enough for
 *   `-1_OWNER` and for a numbered suffix: `STAGING-…EKS-1_OWNER`.
 *
 * In CSS rather than counted in characters, so the cut follows the slot's real
 * width at every viewport instead of a fit chosen for one of them. Where even the
 * kept run does not fit, it is cut before its last twelve characters, so the
 * end of the name is the last thing to go and nothing ever overflows.
 *
 * ## Right to left, and characters a reader sees as one
 *
 * `dir="auto"` on the root: the two runs are flex items, and in a left-to-right
 * box a Hebrew or Arabic nameplate would be drawn with its halves swapped - the
 * kept end first, then the start - even with room to spare. With the direction
 * taken from the name's first strong character, the row runs the name's way.
 * And the runs are split by grapheme (`Intl.Segmenter`), not by code point, so
 * a flag or a ZWJ sequence is never drawn half in one run and half in the next.
 *
 * ## The title, unless the parent carries a better one
 *
 * A browser shows the innermost `title` under the pointer. Where the name sits
 * inside an element that has its own - the breadcrumb, whose tooltip says where
 * the link goes and the real context name under a nameplate - `titled={false}`
 * leaves the parent's in charge.
 *
 * **One component for every surface that draws a cluster name**: a pattern
 * fixed in one place and not in the next is the defect moved, not fixed.
 */
export function ClusterName({
  name,
  className = '',
  titled = true,
}: {
  /** The name to draw - a context name or a nameplate. */
  name: string;
  /** Type and color; layout is this component's. */
  className?: string;
  /**
   * False where an enclosing element carries the tooltip: the innermost `title`
   * is the one a browser shows, so this one would hide the parent's.
   */
  titled?: boolean;
}): ReactNode {
  const { cut, kept } = splitName(name);
  const floored = graphemes(cut).length > 2;
  // A floor of one ellipsis on a run long enough to be cut: below the width of
  // "…" a browser clips the glyphs instead, and an ARN's region shrunk to one
  // pixel read as a stray letter glued to `cluster/` (seen at 500px in
  // Arrange). A run of a character or two is never cut, so it gets no floor -
  // a floor wider than its text would push a gap into a short name.
  const shrinking = (
    <span className={`${floored ? 'min-w-4' : 'min-w-0'} truncate`}>{cut}</span>
  );
  /*
    The kept run is itself two runs, and it always ends the name: its last twelve
    characters never shrink, and what is before them is cut second. An ARN's
    `cluster/<name>` can be fifty characters on its own, and a kept run cut at
    its end would cut exactly the `_OWNER` that tells it from its twin - seen at
    500px on the picker before this split.

    `shrink-0` on the kept run is what orders the cuts: it does not give way
    until the other run is down to its floor, and its `max-w` is the slot less
    that floor (`--spacing` times four is `min-w-4`), so the two together never
    exceed the slot and the kept run is cut before its last twelve.
  */
  const points = graphemes(kept);
  const tail = points.slice(-KEEP).join('');
  const lead = points.slice(0, Math.max(0, points.length - KEEP)).join('');
  const holding = (
    <span className={`flex min-w-0 shrink-0 ${floored ? 'max-w-[calc(100%-var(--spacing)*4)]' : 'max-w-full'}`}>
      <span className="min-w-0 truncate">{lead}</span>
      {tail === '' ? null : <span className="shrink-0">{tail}</span>}
    </span>
  );
  return (
    <span
      data-cluster-name=""
      dir="auto"
      title={titled ? name : undefined}
      className={`inline-flex max-w-full min-w-0 overflow-hidden whitespace-nowrap ${className}`}
    >
      {shrinking}
      {holding}
    </span>
  );
}

/** How many characters of a name with no recognisable shape are kept whole. */
const KEEP = 12;

/**
 * The two runs of a name: the one that is cut first, and the one kept.
 * Exported for the test, which reads the rule rather than a rendering of it.
 */
export function splitName(name: string): { cut: string; kept: string } {
  if (name.startsWith('arn:')) {
    const colon = name.lastIndexOf(':');
    if (colon > 0 && colon < name.length - 1) {
      return { cut: name.slice(0, colon + 1), kept: name.slice(colon + 1) };
    }
  }
  // The last `@`: a user can itself be an address (`alice@corp.example@prod`).
  const at = name.lastIndexOf('@');
  if (at > 0 && at < name.length - 1) {
    return { cut: name.slice(0, at + 1), kept: name.slice(at + 1) };
  }
  // By grapheme, so neither a character outside the BMP nor a flag or a ZWJ
  // sequence is ever cut in two.
  const points = graphemes(name);
  if (points.length <= KEEP) return { cut: '', kept: name };
  return {
    cut: points.slice(0, points.length - KEEP).join(''),
    kept: points.slice(points.length - KEEP).join(''),
  };
}

const segmenter =
  typeof Intl !== 'undefined' && 'Segmenter' in Intl ? new Intl.Segmenter(undefined, { granularity: 'grapheme' }) : null;

/**
 * A string as the characters a reader sees. `Intl.Segmenter` is in every
 * browser the console supports; the code-point split is the floor where it is
 * not, which still never cuts inside a surrogate pair.
 */
export function graphemes(text: string): string[] {
  if (segmenter === null) return [...text];
  return Array.from(segmenter.segment(text), (each) => each.segment);
}
