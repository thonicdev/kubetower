/**
 * The color a container printed.
 *
 * The dock's **terminals** are xterm.js, which reads escape sequences; the log
 * panel builds DOM nodes, which does not. Without this module two panes side by
 * side in one dock would disagree about color.
 *
 * ## The decision this rests on
 *
 * **The escapes leave the string, and the color travels beside it as runs.**
 * That is why `decodeANSI` returns `{ text, runs }` rather than HTML: nothing
 * here builds markup out of a string a container chose. `react/no-danger` is an
 * error in this project and this is the module that would otherwise be
 * tempted.
 *
 * ## No offset to shift
 *
 * The framed stream carries the API server's timestamp as `LogEvent.at` and
 * never puts it in `LogEvent.text` (`internal/web/apireads.go`), so no color
 * offset has to move past a prefix - forgetting that would paint a line's
 * colors one stamp's width early, only with the Time toggle on.
 */

/** One stretch of a line that shares a style. `to` is exclusive; runs are
 *  ascending and never overlap. */
export interface StyleRun {
  readonly from: number;
  readonly to: number;
  readonly style: AnsiStyle;
}

export interface AnsiStyle {
  /** Empty means the pane's own color, which is **not** the same as black: a
   *  container that never set a background must not be given one. */
  readonly fg: string;
  readonly bg: string;
  readonly bold: boolean;
  readonly dim: boolean;
  readonly italic: boolean;
  readonly underline: boolean;
  readonly inverse: boolean;
}

export interface Decoded {
  readonly text: string;
  readonly runs: readonly StyleRun[];
}

/**
 * Every escape sequence, and every one of them is removed whatever it was.
 *
 * Matching only the SGR ones and leaving the rest would fix a colored line
 * and leave `ESC[2K` sitting in a progress bar, which is the same defect one
 * alternative along.
 */
// eslint-disable-next-line no-control-regex -- the subject of this module is a control character
const ANSI_ESCAPE = /\x1b(?:\[([0-9;:<=>?]*)[\x20-\x2f]*([\x40-\x7e])|\][\s\S]*?(?:\x07|\x1b\\|$)|[\x40-\x5f])/g;

/** Shared, and frozen, so a caller cannot push into the empty answer and give
 *  every unstyled line in the buffer somebody else's colors. */
const NO_RUNS: readonly StyleRun[] = Object.freeze([]);

/** The eight colors and their bright halves, as `theme.css` defines them under
 *  `@theme`. Named rather than literal so the palette is one thing in one place:
 *  a terminal's black on this pane's background is unreadable, and deciding what
 *  to draw instead is the theme's job and not this file's. */
const BASIC = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white'] as const;

function ansiVar(name: string): string {
  return `var(--color-ansi-${name})`;
}

function rgb(r: number, g: number, b: number): string {
  return `rgb(${r} ${g} ${b})`;
}

/** The 6×6×6 cube of the 256-color palette, as xterm defines it. */
const CUBE = [0, 95, 135, 175, 215, 255] as const;

/** Indices 0-15 come back as names, so a `38;5;1` is the same red as a `31`
 *  rather than a second opinion about red. */
function ansi256(n: number): string {
  if (n < 8) return ansiVar(BASIC[n] ?? 'white');
  if (n < 16) return ansiVar(`bright-${BASIC[n - 8] ?? 'white'}`);
  if (n < 232) {
    const at = n - 16;
    return rgb(
      CUBE[Math.floor(at / 36) % 6] ?? 0,
      CUBE[Math.floor(at / 6) % 6] ?? 0,
      CUBE[at % 6] ?? 0,
    );
  }
  const grey = 8 + (n - 232) * 10;
  return rgb(grey, grey, grey);
}

function plainStyle(): AnsiStyle {
  return { fg: '', bg: '', bold: false, dim: false, italic: false, underline: false, inverse: false };
}

function isPlain(s: AnsiStyle): boolean {
  return !s.fg && !s.bg && !s.bold && !s.dim && !s.italic && !s.underline && !s.inverse;
}

function sameStyle(a: AnsiStyle | null, b: AnsiStyle | null): boolean {
  if (a === b) return true;
  if (a === null || b === null) return false;
  return (
    a.fg === b.fg &&
    a.bg === b.bg &&
    a.bold === b.bold &&
    a.dim === b.dim &&
    a.italic === b.italic &&
    a.underline === b.underline &&
    a.inverse === b.inverse
  );
}

/**
 * Read the tail of a 38 or 48: `;5;n` for the palette, `;2;r;g;b` for a literal
 * color. It reports how many parameters it consumed, and **that number is the
 * whole reason this is a function of its own.**
 *
 * A reader that does not consume them mis-colors everything after. In
 * `ESC[38;5;208;1m` the 5 and the 208 are arguments to the 38; read as
 * parameters in their own right they are "blink slowly" and a color nobody
 * asked for, and the `1` that meant bold arrives after two wrong answers.
 */
function extended(parts: readonly string[], at: number): { colour: string; used: number } | null {
  const kind = Number(parts[at + 1]);
  if (kind === 5) {
    const n = Number(parts[at + 2]);
    if (!Number.isInteger(n) || n < 0 || n > 255) return null;
    return { colour: ansi256(n), used: 2 };
  }
  if (kind === 2) {
    const channels = [Number(parts[at + 2]), Number(parts[at + 3]), Number(parts[at + 4])];
    for (const v of channels) if (!Number.isInteger(v) || v < 0 || v > 255) return null;
    return { colour: rgb(channels[0] ?? 0, channels[1] ?? 0, channels[2] ?? 0), used: 4 };
  }
  return null;
}

/**
 * Apply one `ESC[…m` to the style in force.
 *
 * Returns `null` for "back to the pane's own appearance" - which is the common
 * case, so it is the one that costs no run and no element - and also for
 * anything malformed, because the safe reading of a sequence this does not
 * understand is "no style" rather than a guess.
 */
function sgr(style: AnsiStyle | null, params: string): AnsiStyle | null {
  // `38:5:245` is the same sequence with the separator ITU T.416 specifies and
  // several loggers emit. Reading the sub-parameters as parameters is what the
  // terminals that accept both do; refusing them would leave a stray 5 and 245
  // to be read as attributes.
  const parts = String(params || '').replace(/:/g, ';').split(';');
  const s: AnsiStyle = style === null ? plainStyle() : { ...style };
  const next = { ...s } as {
    fg: string; bg: string; bold: boolean; dim: boolean;
    italic: boolean; underline: boolean; inverse: boolean;
  };

  for (let i = 0; i < parts.length; i++) {
    // `ESC[m` is `ESC[0m`, and so is every empty parameter inside one.
    const n = parts[i] === '' ? 0 : Number(parts[i]);
    if (!Number.isInteger(n) || n < 0) return null;

    if (n === 0) { Object.assign(next, plainStyle()); continue; }
    if (n === 1) { next.bold = true; continue; }
    if (n === 2) { next.dim = true; continue; }
    if (n === 3) { next.italic = true; continue; }
    if (n === 4) { next.underline = true; continue; }
    if (n === 7) { next.inverse = true; continue; }
    if (n === 21 || n === 22) { next.bold = false; next.dim = false; continue; }
    if (n === 23) { next.italic = false; continue; }
    if (n === 24) { next.underline = false; continue; }
    if (n === 27) { next.inverse = false; continue; }
    if (n === 39) { next.fg = ''; continue; }
    if (n === 49) { next.bg = ''; continue; }
    if (n >= 30 && n <= 37) { next.fg = ansiVar(BASIC[n - 30] ?? 'white'); continue; }
    if (n >= 40 && n <= 47) { next.bg = ansiVar(BASIC[n - 40] ?? 'white'); continue; }
    if (n >= 90 && n <= 97) { next.fg = ansiVar(`bright-${BASIC[n - 90] ?? 'white'}`); continue; }
    if (n >= 100 && n <= 107) { next.bg = ansiVar(`bright-${BASIC[n - 100] ?? 'white'}`); continue; }
    if (n === 38 || n === 48) {
      const found = extended(parts, i);
      if (found === null) return null; // half a color: see the note in extended
      if (n === 38) next.fg = found.colour;
      else next.bg = found.colour;
      i += found.used;
      continue;
    }
    // Blink, conceal, framed, the font selectors: read and dropped on purpose.
    // A log pane is not a terminal and does not owe one an implementation.
  }
  return isPlain(next) ? null : next;
}

/**
 * Split a raw line into the text it says and the runs of style over it.
 *
 * Pure - the same string in gives the same string out, always - which is what
 * lets the panel cache the answer against a line index for the life of the
 * buffer.
 */
export function decodeANSI(raw: string): Decoded {
  // The overwhelmingly common line, and the one this must not slow down: a
  // buffer of a hundred thousand lines from a container that colors nothing.
  if (!raw.includes('\x1b')) return { text: raw, runs: NO_RUNS };

  let text = '';
  const runs: StyleRun[] = [];
  let style: AnsiStyle | null = null;
  let open = 0;
  let at = 0;

  ANSI_ESCAPE.lastIndex = 0;
  let m = ANSI_ESCAPE.exec(raw);
  while (m !== null) {
    text += raw.slice(at, m.index);
    at = m.index + m[0].length;

    // m[2] is the final byte and exists only for the CSI alternative. An OSC or
    // a two-character escape leaves it undefined, which is not 'm', so both are
    // dropped by the same test that drops a cursor move.
    if (m[2] === 'm') {
      const nextStyle = sgr(style, m[1] ?? '');
      if (!sameStyle(nextStyle, style)) {
        if (style !== null && text.length > open) runs.push({ from: open, to: text.length, style });
        style = nextStyle;
        open = text.length;
      }
    }
    m = ANSI_ESCAPE.exec(raw);
  }
  text += raw.slice(at);
  if (style !== null && text.length > open) runs.push({ from: open, to: text.length, style });

  return { text, runs: runs.length > 0 ? runs : NO_RUNS };
}

/**
 * The inline style one run puts on one span.
 *
 * Colors are inline properties and everything else is a utility class, which is
 * not an inconsistency: the four attributes are fixed and belong in the theme,
 * and a `38;2;r;g;b` is a color no stylesheet could have anticipated. That is
 * also why this does not break the no-stylesheet rule - there is no stylesheet
 * here, only a computed value.
 */
export function runStyle(style: AnsiStyle): { color?: string; background?: string } {
  let fg = style.fg;
  let bg = style.bg;
  if (style.inverse) {
    // Swapping needs both sides resolved: a container that set only a
    // foreground and then asked for inverse wants that color *behind* the
    // pane's own text color, and an empty string would silently drop half of it.
    const was = fg || 'var(--color-fg)';
    fg = bg || 'var(--color-bg)';
    bg = was;
  }
  const out: { color?: string; background?: string } = {};
  if (fg) out.color = fg;
  if (bg) out.background = bg;
  return out;
}

/** The utility classes for the four non-color attributes. */
export function runClass(style: AnsiStyle): string {
  const classes: string[] = [];
  if (style.bold) classes.push('font-semibold');
  if (style.dim) classes.push('opacity-65');
  if (style.italic) classes.push('italic');
  if (style.underline) classes.push('underline underline-offset-2');
  return classes.join(' ');
}
