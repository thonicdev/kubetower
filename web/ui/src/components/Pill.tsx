import type { ReactNode } from 'react';

/**
 * # One status vocabulary. Decided once, here.
 *
 * A **graded reading** is a short value the server has put a grade on: a pod's
 * `Running` or `CrashLoopBackOff`, a `1/1`, a restart count, a Helm release's
 * `failed`, a node's `Ready`. It is not a sentence — a colored paragraph
 * explaining what went wrong is prose and stays prose (`text-warn` on a `<p>`),
 * and collapsing the two would put a border round every apology in the console.
 *
 * The grid's cell, the drawer's fact, the overview's tile, Health's counts and
 * Helm's status all draw one, and five answers to "how do I draw one" is how a
 * console ends up looking like five consoles — the same
 * argument `components/Reading.tsx` makes for a measured quantity, and this file
 * is its twin for a graded one. **Read it before drawing a status anywhere**,
 * and change it here rather than beside the next component.
 *
 * ## The shape
 *
 * A rounded pill around a graded value, so Ready, Restarts, Containers and
 * Status read as statuses rather than raw text: fully rounded, `1px 8px`
 * inside, semibold, a 1px tinted border and a tinted background. Any cell
 * carrying a grade is wrapped in one, and `graded()`
 * (`internal/resources/format.go`) says which columns are graded.
 *
 * `text-xs` and not `text-2xs`: `--text-2xs` (11px) is what this console
 * reserves for badges and counters (`theme.css`). A status **is** a badge by
 * that rule, but it is also the thing being read in the column, and 11px
 * semibold inside a 14px table reads as a footnote beside the name. The step
 * up is stated so the next reader does not take it for a slip.
 *
 * ## The neutral pill, which is the half everybody drops
 *
 * *An ungraded value rendering as bare text beside graded ones reads as a
 * rendering fault.* A Restarts column where `7` wears a chip and `0`
 * does not looks like the console failed to draw the `0`, not like the `0` is
 * fine.
 *
 * **It is a rule about a column, not about a table**: chipping every plain
 * string in the table is the opposite of what that sentence asks for. The
 * caller therefore says whether this value has graded
 * neighbors; a column where nothing is graded draws plain text.
 *
 * ## Utilities, never a rule in `theme.css`
 *
 * `@layer components` is kept for the table's wrap and fixed-layout rules and
 * nothing else, and `check:styles` enforces it. Every value here is a theme
 * token or a Tailwind step, so the test — can it be said as a utility —
 * answers yes.
 */

/** Full class strings, looked up, never built by interpolation: Tailwind finds
 *  utilities by scanning source text, so `text-${tone}` is a class that exists
 *  in the DOM and in no stylesheet — which fails silently and looks exactly like
 *  a color nobody chose. */
const TINT: Readonly<Record<string, string>> = {
  ok: 'border-ok/40 bg-ok/10 text-ok',
  warn: 'border-warn/40 bg-warn/10 text-warn',
  bad: 'border-bad/40 bg-bad/10 text-bad',
  info: 'border-accent/40 bg-accent/10 text-accent',
  /* The neutral pill. `bg-white/5` and not `bg-panel-2`, because a row under the
     pointer is already `bg-panel-2` and a picked row is `bg-panel-2` at
     rest: a chip drawn in the same surface as the thing it sits on is a chip
     that vanishes exactly when somebody is looking at it. A white wash tints
     whatever is behind it, which is the same mechanism the three graded tints
     use. */
  normal: 'border-line bg-white/5 text-fg',
  muted: 'border-line bg-white/5 text-muted',
};

/** The one shape. `max-w-full truncate` is not decoration: a `CrashLoopBackOff`
 *  in a 90px column would paint over the column beside it, and a pill that can
 *  grow past its box takes the table's arithmetic with it — the grid sizes the
 *  columns, not the values. */
const SHAPE =
  'inline-block max-w-full shrink-0 truncate rounded-full border px-2 py-px text-xs font-semibold';

export interface PillProps {
  /** The server's grade. `ok`, `warn` and `bad` are graded; anything else — and
   *  nothing at all — is the neutral pill, because a tone the client does not
   *  recognize is not a license to invent a color. */
  tone?: string | undefined;
  /** The neutral pill's two readings. `muted` where the value is deliberately
   *  quiet (an ungraded cell beside graded ones); `normal` where it is the thing
   *  being read. Ignored where `tone` is graded. */
  neutral?: 'normal' | 'muted';
  /** The hover sentence, where the surface has one. */
  title?: string | undefined;
  children: ReactNode;
}

/**
 * One graded reading, in a chip.
 *
 * `data-pill` and `data-tone` are both here and they are not redundant:
 * `data-pill` is what the grid's tests select the chip by and carries the grade
 * as drawn, `data-tone` is what the overview's tests read. Two names for one
 * fact is a cost of folding five components into one, and paid once here rather
 * than by rewriting four test files around a rename.
 */
export function Pill({ tone, neutral = 'normal', title, children }: PillProps): ReactNode {
  const graded = tone === 'ok' || tone === 'warn' || tone === 'bad' || tone === 'info';
  const key = graded ? tone : neutral;

  return (
    <span
      data-pill={key}
      data-tone={tone ?? 'normal'}
      className={`${SHAPE} ${TINT[key] ?? TINT['normal'] ?? ''}`}
      {...(title === undefined || title === '' ? {} : { title })}
    >
      {children}
    </span>
  );
}
