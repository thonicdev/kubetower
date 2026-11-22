import type { ReactNode } from 'react';

import type { ListResult } from '../../api';
import { Icon, type IconName } from '../../components/Icon';
import { focusRingTight } from '../../focus';
import { StateNotice } from '../../state/StateNotice';

import type { SkippedPanel } from './data';

/**
 * One panel of a composed answer.
 *
 * `state/Result.tsx` gives `ListView` and `ValueView`, and both take a
 * `UseQueryResult` - which is right for a panel that is its own request and
 * wrong for one that arrived as a *field* of somebody else's. The dashboard is
 * the second shape: one request, four envelopes inside it.
 *
 * `PanelList` is therefore `ListView` with the query half removed, and it keeps
 * the property that made `ListView` worth having: **`children` is called only on
 * the arm that carries `items`.** There is no `items ?? []` available here
 * either, because `ListResult` does not have one to reach for.
 *
 * **This could live in `state/`;** it is kept here so that `state/Result.tsx`
 * stays the home of the query-backed views only.
 */

export interface PanelProps {
  title: string;
  /** The glyph on the title row, from the sprite the Shell mounts. Optional
   *  because `Not shown` is a footnote about the page and not a panel of the
   *  cluster, and a footnote with a glyph reads as a panel. */
  icon?: IconName;
  /** Shown beside the title: a count, a stamp. Never a state. */
  note?: ReactNode;
  /** The fragment this panel answers to, where something links to it. Only
   *  `Not shown` has one, because only it is linked at. */
  id?: string;
  /** What a read behind this panel failed, if any. `PanelMark` draws it in the
   *  heading; passing the entries rather than a boolean is what lets the mark
   *  carry the server's own sentence. */
  spoilt?: readonly SkippedPanel[] | undefined;
  children: ReactNode;
}

/**
 * The frame every panel shares, and the page's hierarchy is decided here once.
 *
 * The hierarchy is structural rather than decorative: a title row that reads as
 * one (a glyph, a heavier title, the note beside it), a body with room around it,
 * and a frame with a slightly larger radius than the tiles inside it so the two
 * nest rather than stack. All of it is spacing, type scale and the existing
 * tokens: `rounded-2xl` is the scale's 10px, one step above the `rounded-xl` the
 * tiles take; the glyph is `components/Icon.tsx` at its own size in the muted
 * color, so the panels read as different things rather than gray boxes.
 * `ClusterHead` is not a `Panel` - it holds three
 * things - and takes the same three strings so it cannot drift from them.
 */
export const PANEL_FRAME = 'rounded-2xl border border-line bg-panel';
export const PANEL_HEAD = 'flex items-center gap-2 border-b border-line px-4 py-2.5';
export const PANEL_TITLE = 'text-base font-semibold';

export function Panel({ title, icon, note, id, spoilt, children }: PanelProps): ReactNode {
  // `data-incomplete` is the panel-level fact, for the case where the body is
  // still a list - five workload tiles where six belong carry no notice at all,
  // so the section is where a test, or a stylesheet, can tell the three
  // readings apart.
  const incomplete = spoilt !== undefined && spoilt.length > 0;
  return (
    <section id={id} data-incomplete={incomplete ? '' : undefined} className={PANEL_FRAME}>
      <div className={PANEL_HEAD}>
        {icon === undefined ? null : <Icon name={icon} className="text-muted" />}
        <h2 className={PANEL_TITLE}>{title}</h2>
        {note !== undefined ? <span className="text-xs text-muted">{note}</span> : null}
        <PanelMark spoilt={spoilt} />
      </div>
      <div className="p-4">{children}</div>
    </section>
  );
}

/**
 * **A panel whose data is not all there says so, on itself.**
 *
 * ## The reading it removes
 *
 * A panicking gathered task degrades its panel instead of ending the process:
 * the page answers `allWell: false` and `notShown: [{panel: "Warning events",
 * reason: "...panicked: ..."}]` - and the events panel answers
 * `{"state":"empty","items":[]}`, which would draw *"No warning events. This
 * cluster answered and had none."* A reader of the warnings panel would be told
 * the best possible news about the cluster, with the correction at the top of
 * the page in another component.
 *
 * An empty answer and a failed answer are different answers, and a view
 * rendering both alike is the defect. This is that rule held **at the panel**,
 * where every other surface in this console already holds it.
 *
 * ## Why a mark and not a failed envelope
 *
 * A mark is the smallest change that removes the false reading while keeping the
 * page glanceable; it does not turn a bad cluster day into a wall of error
 * boxes. So the panel is **not** given its own failure state - `notShown` stays
 * the one place that explains what happened, and this points at it.
 *
 * ## Why it looks like this
 *
 * `text-warn` because that is already this console's color for *a read did not
 * come back*: `StateNotice` wears it for `unreadable` and `forbidden`, and keeps
 * `bad` for a cluster that could not be asked at all. Nothing here is a claim
 * about the cluster's health, so nothing here is graded like one.
 *
 * The frame and the size are `Bits.tsx`'s `InitMark` - a bordered 2xs word, the
 * shape this page already uses for *this row is a different kind of thing*.
 * Filled like a `Pill` it would read as a status of the panel's subject, which
 * is the collapse one level up.
 *
 * It is a **link to the entry that explains it**, rather than a tooltip or a
 * repeat of the reason. The reason can be a Kubernetes paragraph and the panel
 * heading is not where it goes; `title` carries it for a hover, and the fragment
 * takes a reader who wants more to the one place that has it.
 *
 * ## Three readings, not two
 *
 * Marked is distinguishable from **empty** - that is the whole point - and it
 * has to stay distinguishable from **fine**, which is why the unmarked case
 * draws nothing at all rather than a green counterpart. A panel that is all
 * there says so by being unremarkable.
 */
export function PanelMark({ spoilt }: { spoilt?: readonly SkippedPanel[] | undefined }): ReactNode {
  if (spoilt === undefined || spoilt.length === 0) return null;

  return (
    <a
      href="#not-shown"
      data-panel-mark
      title={spoilt.map((entry) => `${entry.panel}: ${entry.reason}`).join('\n')}
      className={`shrink-0 rounded-sm border border-warn/50 px-1 text-2xs text-warn uppercase underline-offset-2 hover:underline ${focusRingTight}`}
    >
      incomplete
    </a>
  );
}

export interface PanelListProps<T> {
  /** Already narrowed by `data.ts`. `undefined` means the payload carried no
   *  such panel at all, which is not one of the six states. */
  result: ListResult<T> | undefined;
  /** What the panel is, in the reader's words. It appears in the missing-panel
   *  sentence and nowhere else. */
  what: string;
  /** Drawn on `empty`. Worth passing on every panel whose emptiness would read
   *  as a fact about the cluster - "no leaderboard" reads as "nothing is using
   *  anything", which is the collapse one level down. */
  empty?: ReactNode;
  /** True where a read behind this panel failed - the same fact `PanelMark`
   *  draws in the heading. It replaces the caller's `empty` sentence with
   *  `NotRead`. */
  spoilt?: boolean | undefined;
  onRetry?: (() => void) | undefined;
  children: (items: readonly T[]) => ReactNode;
}

/**
 * What a marked panel draws where its items would be, when nothing arrived.
 *
 * **The mark alone is not enough, and this is the half that is easy to miss.**
 * The sentences passed as `empty` are confident by design - *"No warning events.
 * This cluster answered and had none."*, *"The cluster answered and there is
 * nothing of any kind on it."* - because an unexplained empty panel is the
 * defect one level down. On a panel whose read failed, every one of them is a
 * claim about the cluster that nothing measured, sitting directly under a mark
 * saying the opposite. A panel contradicting itself is not three readings; it is
 * two, with the false one in larger type.
 *
 * **And withdrawing the sentence is not enough either.** If the `empty` notice
 * were kept with only its paragraph swapped, the panel would still open with
 * *"Nothing here"* in bold and `data-state="empty"` - under a mark in the heading,
 * the one word the mark exists to contradict, in larger type. An empty answer and
 * a failed answer must not share a state in the DOM or a first word on screen.
 *
 * So this is an em dash and two words. No count, no *none*, no error box, no
 * retry, and no second copy of the reason: the reason lives once, in *Not shown*,
 * where the mark in the heading already points. `data-state="incomplete"` is
 * none of the contract's six states on purpose, as `PanelMissing`'s
 * `missing` is not: it is the state of a drawing, not of an answer, and it is
 * what lets a test tell this apart from `empty` without reading prose.
 */
function NotRead(): ReactNode {
  return (
    <p role="status" data-state="incomplete" className="text-sm text-muted">
      <span aria-hidden="true">&mdash; </span>not read
    </p>
  );
}

export function PanelList<T>({
  result,
  what,
  empty,
  spoilt,
  onRetry,
  children,
}: PanelListProps<T>): ReactNode {
  if (result === undefined) return <PanelMissing what={what} />;

  if (result.state === 'ok') return children(result.items);

  if (result.state === 'empty') {
    if (spoilt === true) return <NotRead />;
    return empty ?? <StateNotice state="empty" reason={result.reason} />;
  }

  return <StateNotice state={result.state} reason={result.reason} onRetry={onRetry} />;
}

/**
 * The dashboard answered and said nothing about this panel.
 *
 * Not `absent`, not `empty`, and deliberately worded like neither. Panels
 * are optional in `api/openapi.yaml` while the Go side always sends all of
 * them, so a missing one is a disagreement between the contract and the
 * implementation - a fault in this product rather than a fact about a cluster,
 * which is the same reason `EnvelopeError` is not drawn as one of the six.
 * Someone told `absent` here would go and look at the cluster.
 */
function PanelMissing({ what }: { what: string }): ReactNode {
  return (
    <div role="status" data-state="missing" className="rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className="font-medium text-warn">Not answered</p>
      <p className="mt-1 text-muted">
        The dashboard was read and carried nothing at all about {what} - not an answer, and not a
        reason either.
      </p>
    </div>
  );
}
