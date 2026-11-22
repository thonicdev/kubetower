import { useRef, type ReactNode } from 'react';

import type { Cell, Column } from '../../api';
import { CopyButton } from '../../components/CopyButton';
import { Pill } from '../../components/Pill';
import { Measure } from '../../components/Reading';
import { externalHref } from '../../externalHref';
import { focusRingInside } from '../../focus';

/**
 * One cell of the grid.
 *
 * ## A cell has its own six states, and this is where they are honored
 *
 * `api/errors.md`: the envelope **composes**, and a response envelope alone
 * means one unreadable field makes the whole page unreadable — or, worse,
 * because it looks fine, the page is `ok` and usage is drawn as zero beside a
 * correct capacity. So `Cell` carries its own optional `state` and `reason`, and
 * the case it exists for is exact: `resources.Usage` carries
 * `ComputeUnmeasured` for an object the metrics backend said nothing about while
 * answering for others. In the envelope that is a cell-level `absent`, **not a
 * zero**.
 *
 * A cell whose state is not `ok` therefore draws **no value at all**. There is
 * no number to soften, no bar to leave empty and no zero to explain away — the
 * em dash is the whole answer, and the reason is on the cell for anyone who
 * hovers or reads it out.
 *
 * ## `trustworthy` is the same argument for a value that does exist
 *
 * `api/openapi.yaml`: *"a bare float is not acceptable anywhere a reading comes
 * from a metrics source: a number with no companion flag is a complete-looking
 * value, and an untrustworthy reading drawn as a number is the empty-versus-
 * failed collapse in a gauge, where it looks fine."* So an untrustworthy reading
 * is drawn **differently from a trustworthy one** — in the warning tone, with a
 * marker beside it and a sentence for a screen reader. Color alone would be a
 * distinction half the users cannot see.
 *
 * `value: null` means there is no sample. It is not zero. JSON has no NaN and no
 * infinity and the Go encoder refuses both, so a non-finite sample arrives here
 * as `null` or the whole response fails to marshal — which is why `null` is
 * drawn rather than defaulted.
 *
 * ## `text` is the server's, and the client composes no units of its own
 *
 * `Reading.text` is "the rendered form, computed once on the server so that a
 * table cannot report three different units for the same quantity". Nothing here
 * formats a number.
 *
 * ## The bar
 *
 * `Reading` carries `value`, `trustworthy` and `text` and no denominator, so a
 * bar's length cannot be drawn from it, and inventing a denominator from a
 * column heading would be a guess. `theme.css` records measured defects of a gauge
 * whose length is a **fraction** (the flex row that decouples the bar from the
 * digits beside it, the `shrink-0` track, the light trough, an unmeasured cell
 * drawing no track at all), and each of them applies here.
 *
 * `Cell.gauge` is the denominator, arriving already divided: `Gauge` is
 * `{percent, tone}` and the contract says why it is a percentage rather than a
 * value and a whole - *"the fill is clamped on the server, so a value outside the
 * track cannot be drawn outside it, and the arithmetic that decides what the whole
 * is stays where the sources are rather than being reassembled per client."* So
 * nothing here divides anything, and a percent past 100 is still clamped below,
 * because a client that trusts a bound it did not enforce draws a bar out of its
 * own cell the first time a source disagrees.
 *
 * **How that bar is drawn is not decided here.** `components/Reading.tsx` holds
 * the console's one anatomy for a measured quantity and this cell asks for it:
 * *one style for a reading, decided once*. The defects `theme.css` records are
 * honored there; in short:
 *
 * - the bar and its **percentage** share one line, and the quantity (`1163m`) is
 *   on the hover as `used / total`, which the server writes into `Cell.hint`. A
 *   bar at the cell's leading edge with its digits a cell-width away is attached
 *   by the eye to the previous column; beside its own percentage it ends where
 *   its figure begins;
 * - the bar is never positioned *by the number*: a bar drawn beside `1163m` and
 *   another beside `368m` landed a few pixels apart. The percentage sits in a box
 *   of fixed width, so the bar is the cell less a constant and no digits can move
 *   it;
 * - the trough is **light** (`bg-white/10`) and has no border. The sunken idiom
 *   fails: on this palette there is nothing darker than `--color-bg` to sink into,
 *   so the track was invisible and the fill's right edge became the reading,
 *   wandering every two seconds;
 * - an unmeasured cell draws an em dash and **no track at all**. An empty track
 *   reads as 0%, which reads as an idle machine, which is the opposite of the
 *   truth. Nothing here has an "empty gauge" branch;
 * - `Gauge.tone` is deliberately **not** `Cell.tone`. A node at 91% memory is a
 *   well-packed machine rather than an incident: coloring the bar says *look
 *   here*, promoting the row says *this is a fault*, and only the first is true.
 *   So the gauge's tone never reaches the percentage beside it.
 *
 * ## No link on the name
 *
 * `Cell.ref.href` addresses the detail drawer, and the name is not a link: the
 * **row** carries the action. A name that was also a link to the object's page
 * would fire both when clicked - the drawer opens and the page navigates out from
 * under it. One row, one action.
 *
 * The controls that are *not* the row's action say so with `data-no-open`, which
 * `DataGrid` reads from the event target upwards: an attribute rather than a
 * class list, so a control added later opts out by declaring it.
 * `Column.role: 'namespace'` is one of them: it re-asks the list this page is
 * already on rather than opening anything.
 */

/** Full class strings, looked up, never built by interpolation: Tailwind finds
 *  utilities by scanning source text, so `text-${tone}` is a class that exists
 *  in the DOM and in no stylesheet. */
const TONE: Readonly<Record<NonNullable<Cell['tone']>, string>> = {
  normal: '',
  muted: 'text-muted',
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
};

/** The em dash is the console's word for "there is nothing to say here", and it
 *  is deliberately the same glyph for every not-`ok` cell state: what differs is
 *  the sentence beside it, not the placeholder. */
const NOTHING = '—';

/**
 * The chip around a graded value.
 *
 * Ready, Restarts, Containers and Status are graded cells, so they are the
 * columns that wear a chip (`.pill`); drawing them as colored text alone reads
 * as too raw.
 *
 * **How it is drawn is not decided here.** `components/Pill.tsx` holds the
 * console's one status vocabulary — the shape, the three tints and the neutral
 * one — and this cell asks for it, as `components/Reading.tsx` does for a
 * measured quantity.
 *
 * **The ungraded arm.** Three tones and no ungraded arm would put a chip round
 * every graded value and none on a plain one beside it, and an ungraded value
 * rendering as bare text **beside graded ones** reads as a rendering fault. That
 * is a rule about a **column**, not about a table: a Restarts column where `7`
 * wears a chip and `0` does not looks like the console failed to draw the `0`.
 * So the neutral pill goes on an ungraded cell whose column grades something,
 * and a column that grades nothing draws plain text — which is why
 * `ResourceList.test.tsx`'s "an ungraded one as text" holds: its column has one
 * cell and no grade on it.
 */

/**
 * How a `role: 'name'` cell is drawn.
 *
 * `listData` carries `NameAt` and `NamespaceAt` because after a drag, `first` is
 * whatever was dragged there, and without them an age would be styled as a name
 * on every row. So this is keyed on the declared role and never on the column's
 * position.
 */
/**
 * ...and the weight it carries.
 *
 * **The Name column is the only one drawn in the foreground color**, and
 * everything ungraded beside it is muted. A row is then read by running the eye
 * down one column of names, and what pulls the eye off that line is a grade -
 * which is the only thing that should.
 *
 * `namespace` stays in the foreground deliberately, and it is the one exception:
 * it is a control, and is not styled as a link because forty underlined blue
 * words down a column would read as the most important thing on a screen whose
 * subject is the names beside them. Muting it instead would say the opposite of
 * what the button is for. It reads as the name's companion, which is what it is.
 */
const ROLE: Readonly<Record<Column['role'], string>> = {
  name: 'font-medium text-fg',
  namespace: '',
  age: 'text-muted',
  other: 'text-muted',
};

export interface ResourceCellProps {
  cell: Cell | undefined;
  /** The column's declared facts. Read for `role`, `gauged` and `copyable`, none
   *  of which may be inferred from the cell in front of the renderer. */
  column: Column;
  /** Re-ask this list for one namespace. Only a `role: 'namespace'` column uses
   *  it, and only when it is given: a cell that would be a control and has
   *  nothing to call is drawn as text rather than as a button that does nothing. */
  onPickNamespace?: ((namespace: string) => void) | undefined;
  /**
   * Some other cell of **this column** carries a grade.
   *
   * Whether to draw a neutral chip is a rule about a column and cannot be
   * answered from one cell, so the caller answers it: `ResourceList` looks down the column once per
   * render. A cell cannot see its neighbors and must not guess at them — the
   * same reason `Column.role` is declared rather than derived from position.
   */
  gradedColumn?: boolean;
}

export function ResourceCell({
  cell,
  column,
  onPickNamespace,
  gradedColumn = false,
}: ResourceCellProps): ReactNode {
  /*
    The element holding the text **as drawn**, for `CopyButton`'s last resort:
    where no clipboard may be written, it selects what is on screen.

    It is attached to the wrapper below rather than threaded down into the value,
    and that is not only tidier - `react-hooks/refs` refuses passing a ref into a
    function called during render, and it is right to, because a function that
    receives one may read `.current` mid-render. The wrapper is the element
    holding the text anyway, which is exactly what `selectInPlace` needs.

    Declared unconditionally. A hook behind a branch is a hook order that changes
    between renders.
  */
  const shown = useRef<HTMLSpanElement>(null);

  // A column with no cell renders empty, not missing — `api/openapi.yaml` says
  // so on `Row.cells`, and a row short by one `<td>` would slide every later
  // value into the wrong column.
  if (cell === undefined) return null;

  if (cell.state !== undefined && cell.state !== 'ok' && cell.state !== 'empty') {
    // No track, deliberately, on a gauged column too. `theme.css`: "an empty
    // track reads as 0%, which reads as an idle machine, which is the opposite
    // of the truth."
    return (
      <span
        data-cell-state={cell.state}
        className="text-muted"
        title={cell.reason?.message ?? `This value is ${cell.state}.`}
      >
        {NOTHING}
        <span className="sr-only"> {cell.reason?.message ?? `not available: ${cell.state}`}</span>
      </span>
    );
  }

  const value = (
    <Value
      cell={cell}
      column={column}
      onPickNamespace={onPickNamespace}
      gradedColumn={gradedColumn}
    />
  );

  /*
    The copy control, and **the cell decides whether there is one**.

    `column.copyable` is what reserves the width, as a function of the declaration
    so the colgroup never has to be recomputed when a value lands. Whether the
    button is *drawn* is a different question: the control appears where there is something to put on
    the clipboard, and nowhere else.

    **Not `cell.copy ?? cell.text`: the fallback would be a bug.**
    `internal/resources`' `copyableText` sets `Copy` from the same string it sets
    `Text` from, so the two are equal wherever there is a value and `Copy` is
    empty exactly where there is none — `Dash("")` is an em dash with no `Copy`
    beside it. Falling back to the text therefore recovers no missing copy value;
    it puts a copy button on every em dash in an address column, offering to place
    an em dash on the clipboard.

    Where the two genuinely differ is the case the member exists for: a cell
    holding three hosts and `+2 more` is the one somebody needs the clipboard for,
    and the two that were left out are the point.
  */
  const copyable = column.copyable ? (cell.copy ?? '') : '';
  if (copyable === '') return value;

  return (
    <span className="flex items-center gap-1">
      <span ref={shown} className="min-w-0 flex-1 truncate">
        {value}
      </span>
      <CopyButton
        ring={focusRingInside}
        value={copyable}
        label={`Copy this ${column.label.toLowerCase()}`}
        selectRef={shown}
      />
    </span>
  );
}

/** The reading itself, before any control is put beside it. */
function Value({
  cell,
  column,
  onPickNamespace,
  gradedColumn,
}: {
  cell: Cell;
  column: Column;
  onPickNamespace: ((namespace: string) => void) | undefined;
  gradedColumn: boolean;
}): ReactNode {
  const gauge = cell.gauge;
  const reading = cell.reading;

  /*
    The graded chip comes first.

    The pill arm precedes the gauge arm, so a cell that was both graded and
    gauged would draw as a chip. That cell does not exist: `withGauge`
    (`internal/resources/format.go:177`) decorates a cell built by `text()` or
    `count()`, neither of which sets a `Class`, and every gauged cell in the
    console comes through it. The two are disjoint in the data, which is why the
    order is unobservable; what would make it observable is a new descriptor
    grading a gauged column, and then a chip is the answer wanted.
  */
  if (cell.tone === 'ok' || cell.tone === 'warn' || cell.tone === 'bad') {
    return <Pill tone={cell.tone}>{cell.text}</Pill>;
  }


  if (column.gauged && gauge !== undefined) {
    // Clamped here as well as on the server. The contract says the server does
    // it; a client that draws `width: 140%` because it trusted that is a bar
    // over the column beside it, and the cost of the second clamp is nothing.
    const percent = Math.min(100, Math.max(0, gauge.percent));
    const text = reading?.text ?? cell.text;
    return (
      // **How a gauged cell reads is `components/Reading.tsx`'s, not this
      // file's** — this file deliberately holds no second copy of it, because
      // several surfaces drawing a reading from several private answers is how
      // a console ends up looking like several consoles. In one line:
      // the bar and its percentage on one line, and the quantity on hover.
      //
      // **The hover is the server's `Cell.hint`, and it is the whole string.**
      // `Cell.gauge` is `{percent, tone}` and arrives already divided — "the
      // arithmetic that decides what the whole is stays where the sources are
      // rather than being reassembled per client" — so there is no whole here
      // to write `used / total` from, and nothing here invents `/ 8 cores`. The
      // server writes it (`resources.Fraction`: `1.2 / 8 cores
      // allocatable`), `DataGrid` puts it on the `<td>`, and `Measure` puts it
      // on the reading and in its screen-reader text. Where a hint is missing
      // the cell's own text is the hover, so the quantity is never lost.
      <span data-gauge={String(Math.round(percent))} className="block">
        <Measure
          value={text === '' ? NOTHING : text}
          title={cell.hint}
          percent={percent}
          tone={gauge.tone}
          label={column.label}
        />
      </span>
    );
  }

  if (reading !== undefined) {
    if (reading.value === null) {
      return (
        <span data-reading="none" className="text-muted" title="No sample.">
          {NOTHING}
        </span>
      );
    }

    if (!reading.trustworthy) {
      return (
        <span
          data-reading="doubtful"
          className="text-warn"
          title="This reading may be wrong."
        >
          {reading.text ?? cell.text}
          <span aria-hidden="true"> ?</span>
          <span className="sr-only"> (this reading may be wrong)</span>
        </span>
      );
    }

    return (
      <span data-reading="ok">
        {reading.text ?? cell.text}
      </span>
    );
  }

  /*
    An addressable cell: the reading broken into the pieces somebody would click.

    The contract states the invariant for `Cell.parts`: **the pieces concatenate to exactly
    `Cell.text`, separators included, so a client never joins them itself.**
    A test pins it, because re-adding a comma on the client would leave a
    data-level test green — the filter searches one string and the user reads
    another, "and one comma is exactly the size of drift nobody reports and
    nobody can then explain". Nothing here writes a separator.

    A piece with no `href` is drawn as text beside the ones that have one: `*` is
    not an address, neither is `*.example.com`, and neither is the `, ` between
    two hosts.

    `target="_blank"` because this leaves the console — an Ingress host is
    somebody else's application, and replacing the table with it takes the
    user out of a page they are working in — and `rel="noopener noreferrer"`
    goes with it, `noopener` being what stops the opened page reaching back
    through `window.opener`.

    **These anchors are also why a row click declines over them.**
    A click on a link would otherwise fire both the navigation and the drawer.
    `data-no-open` is what `DataGrid` reads and is the guard; `data-cell-addr`
    stays as the name the tests find the anchor by.

    **Only an http or https address becomes an anchor** (`externalHref`): the host half is whoever wrote the Ingress, so a piece whose href
    is anything else is drawn as the text it is, like a separator.
  */
  if (cell.parts !== undefined && cell.parts.length > 0) {
    return (
      <span data-parts={String(cell.parts.length)}>
        {cell.parts.map((part, at) =>
          /* The index is the key, deliberately. The pieces are positional by
             construction — they concatenate to `Cell.text` in order — and two of
             them are routinely the same string, since the separators are pieces
             too and `", "` appears twice in three hosts. Keying on the text would
             be a duplicate key; keying on the href would be one on every
             separator, which has none. */
          externalHref(part.href) === undefined ? (
            <span key={at}>{part.text}</span>
          ) : (
            <a
              key={at}
              data-cell-addr=""
              data-no-open=""
              href={externalHref(part.href)}
              target="_blank"
              rel="noopener noreferrer"
              title={`Open ${part.href}`}
              // The one control answered with an underline rather than a ring:
              // hover and focus-visible are the same rule.
              className="text-accent underline-offset-2 hover:underline focus-visible:underline"
            >
              {part.text}
            </a>
          ),
        )}
      </span>
    );
  }

  if (cell.text === '') return <span className="text-muted">{NOTHING}</span>;

  /*
    The neutral pill, and it is only ever drawn beside a graded one.

    Last of the arms on purpose. A gauged cell, a reading and an addressable one
    are drawn by their own arms above and are **not** status readings — a chip
    round a bar's digits or round three Ingress hosts is the "chip everything"
    fix this rule does not ask for. What is left here is the plain string,
    which is the case the sentence is about.

    `muted` rather than `normal`: an ungraded cell is the quiet one by
    construction, and every non-Name cell stays muted unless graded.

    The namespace arm below wins over this, and that is deliberate: it is a
    control, and it is not styled as a chip either — forty chips down a namespace
    column would be the same mistake in a different color.
  */
  if (gradedColumn && column.role !== 'namespace') {
    return <Pill neutral="muted">{cell.text}</Pill>;
  }

  const tone = TONE[cell.tone ?? 'normal'];
  const role = ROLE[column.role];
  const className = [tone, role].filter((part) => part !== '').join(' ');

  /*
    A namespace is clickable, and `api/openapi.yaml` says why it is worth a
    control of its own: *"a table of four hundred pods across thirty namespaces
    is read by finding one interesting row and then wanting its neighbors."*

    It re-asks this list rather than navigating. The namespace is a query
    parameter this page already reads and honors, so the answer is the same
    page with one question changed — which is also the only version of this that
    can be built today, since there is no route that takes a namespace.
  */
  if (column.role === 'namespace' && onPickNamespace !== undefined) {
    return (
      <button
        type="button"
        data-role="namespace"
        // The row is what opens the drawer; this control is not it. Without this
        // the namespace filter would also open the panel, which reads as the
        // filter being ignored.
        data-no-open=""
        aria-label={`Show only ${cell.text}`}
        onClick={() => {
          onPickNamespace(cell.text);
        }}
        // Deliberately not styled as a link at rest. Every row has one, so
        // forty underlined blue words down a column would read as the most
        // important thing on a screen whose subject is the names beside them.
        // So: the same color as the name beside it, and the affordance on
        // hover, where somebody is already looking.
        //
        // `bg-line` and not `bg-panel-2` for that hover, because the row under
        // the pointer is already `hover:bg-panel-2` (DataGrid) and a picked row
        // is `bg-panel-2` at rest: a hover drawn in the same surface as the
        // thing it sits on is a hover nobody sees. A surface one step up the same
        // ramp reads on both backgrounds without making a link of it.
        //
        // The ring is drawn inside, like the copy control's: a list's cells clip
        // what they hold, and at the compact density the cell leaves less room
        // above this button than a ring drawn outside it needs.
        className={`max-w-full cursor-pointer truncate rounded-xs text-left text-fg hover:bg-line ${focusRingInside} ${className}`}
      >
        {cell.text}
      </button>
    );
  }

  return (
    <span data-role={column.role} className={className}>
      {cell.text}
    </span>
  );
}
