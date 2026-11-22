import { useId, type ReactNode, type RefObject } from 'react';
import { focusRingTight } from '../focus';

/** A one-glyph control (‹ ›) in a 24px box, 44px under a coarse pointer. */
const glyphButton = `inline-flex size-6 items-center justify-center rounded-sm hover:text-fg pointer-coarse:size-11 ${focusRingTight}`;

/**
 * The search box the console draws wherever a reader looks for text in
 * something already on screen: the log pane's filter, and the find bar over an
 * object's manifest and its data.
 *
 * ## One drawing, shared with the log pane
 *
 * The box, its `Aa` toggle, its `.*` toggle, the regular expression's error and
 * the `‹ n/m ›` stepper are the log pane's toolbar and the find bar's alike: a
 * second copy is two drawings of one control waiting to differ. So the markup
 * is here and `LogPane.tsx`'s toolbar draws it, in the same order inside the
 * same wrapping row.
 *
 * It renders a **fragment**, not a box of its own, for that reason: the log
 * pane's bar is one `flex-wrap` row where the filter grows into what the other
 * controls leave, and a wrapper would make the filter and its toggles one item
 * that wraps as a block. A caller that wants them grouped puts them in a group.
 *
 * ## What stays the caller's
 *
 * What the text is matched against, and what Enter means. The log pane settles
 * the filter at once on Enter (it filters, it does not step); the find bar
 * settles on the first Enter and steps on the next, and goes back on
 * Shift+Enter. So the key is handed back with whether Shift was held and the
 * field decides nothing about it. The settling itself (`useSettling`, 140 ms)
 * is the caller's too, because the field draws what was typed - never the
 * settled value, which is how a box ends up showing something nobody typed.
 *
 * The regular-expression toggle is drawn only where the caller offers one. The
 * find bar does not: a manifest and a ConfigMap are read for a key or a value,
 * and a pattern box over a Secret's keys is a control nobody asked for.
 */
export function SearchField({ inputRef, ...props }: {
  /** The field's accessible name. A placeholder is gone the moment anything is
   *  typed, so it is never the only name. */
  label: string;
  placeholder: string;
  query: string;
  onQuery: (text: string) => void;
  /** Enter in the field; `backwards` is Shift+Enter. */
  onEnter: (backwards: boolean) => void;
  matchCase: boolean;
  onMatchCase: (on: boolean) => void;
  /** The `.*` toggle, where the caller has one. */
  regex?: { on: boolean; onChange: (on: boolean) => void } | undefined;
  /** Why the pattern does not compile, or `null`. */
  error: string | null;
  /** How many matches there are, and which one is current (from 0). */
  matches: number;
  at: number;
  /** Set when `matches` is a cap the text went past: the counter reads `m+`,
   *  and this is the sentence on its hover that says what was left out. */
  more?: string | undefined;
  onStep: (step: number) => void;
  inputRef?: RefObject<HTMLInputElement | null> | undefined;
}): ReactNode {
  const shown = Math.min(props.at + 1, props.matches);
  const sentence = `${String(shown)} of ${String(props.matches)}${props.more === undefined ? '' : `+: ${props.more}`}`;
  // The cap's sentence, for a reader that never hovers: the box and the two
  // arrows are described by it, from an `sr-only` span beside the counter -
  // beside, not inside, so the counter's text is still `n/m+` and nothing more.
  const capped = useId();
  const describedBy = props.more === undefined || props.query === '' || props.error !== null ? undefined : capped;
  return (
    <>
      <input
        ref={inputRef}
        type="search"
        aria-label={props.label}
        aria-describedby={describedBy}
        placeholder={props.placeholder}
        value={props.query}
        onChange={(event) => props.onQuery(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter') props.onEnter(event.shiftKey);
        }}
        className={`min-w-32 max-w-md flex-1 rounded-sm border bg-panel-2 px-1 ${
          props.error === null ? 'border-line' : 'border-warn'
        }`}
      />
      <Toggle on={props.matchCase} label="Aa" title="Match case" onClick={() => props.onMatchCase(!props.matchCase)} />
      {props.regex === undefined ? null : (
        <Toggle
          on={props.regex.on}
          label=".*"
          title="Regular expression"
          onClick={() => props.regex?.onChange(!props.regex.on)}
        />
      )}
      {props.error !== null && <span className="text-warn">{props.error}</span>}
      {props.query !== '' && props.error === null && (
        // `n/m` on screen, as the log pane always drew it, and the sentence on
        // hover: the glyph is compact enough for a 560px drawer and a dock at its
        // minimum, and the words are there for whoever is not sure what it counts.
        <span className="text-muted" title={sentence}>
          <button type="button" aria-label="Previous match" aria-describedby={describedBy} onClick={() => props.onStep(-1)} className={glyphButton}>‹</button>
          {' '}{shown}/{props.matches}{props.more === undefined ? '' : '+'}{' '}
          <button type="button" aria-label="Next match" aria-describedby={describedBy} onClick={() => props.onStep(1)} className={glyphButton}>›</button>
        </span>
      )}
      {describedBy === undefined ? null : (
        <span id={describedBy} className="sr-only">
          {sentence}
        </span>
      )}
    </>
  );
}

/**
 * A two-state control in a toolbar: bordered in the accent while it is on.
 *
 * The log pane's, moved with the field because the field's toggles are two of
 * them; the pane's own `Prev run` is a third and imports it from here.
 */
export function Toggle({
  on, label, title, onClick, disabled = false,
}: { on: boolean; label: string; title: string; onClick: () => void; disabled?: boolean }): ReactNode {
  return (
    <button
      type="button"
      aria-pressed={on}
      disabled={disabled}
      title={title}
      className={`inline-flex min-h-6 min-w-6 items-center justify-center rounded-md border px-1 pointer-coarse:min-h-11 disabled:cursor-not-allowed disabled:opacity-50 ${focusRingTight} ${
        on ? 'border-accent text-fg' : 'border-line text-muted'
      }`}
      onClick={onClick}
    >
      {label}
    </button>
  );
}
