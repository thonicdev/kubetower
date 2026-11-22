import { describe, expect, it } from 'vitest';

import { decodeANSI, runClass, runStyle } from './ansi';

/**
 * The decoder, and the palette it asks for. The two properties that belong to
 * the log pane rather than to this module - the cache moving with the lines it
 * describes, and the download writing what the container printed - are on the
 * pane's own test file.
 */
describe('decodeANSI', () => {
  it('leaves a line with no escapes exactly as it arrived, and allocates no runs', () => {
    // The overwhelmingly common line, and the one this must not slow down: a
    // buffer of a hundred thousand lines from a container that colors nothing.
    const decoded = decodeANSI('listening on :8080');
    expect(decoded.text).toBe('listening on :8080');
    expect(decoded.runs).toHaveLength(0);
  });

  it('takes the escapes out of the text and reports the color beside it', () => {
    // A Traefik pod's line.
    const decoded = decodeANSI('\x1b[90m2026-08-30T13:21:52Z\x1b[0m \x1b[31mERR\x1b[0m done');
    expect(decoded.text).toBe('2026-08-30T13:21:52Z ERR done');
    expect(decoded.text).not.toContain('\x1b');

    expect(decoded.runs).toHaveLength(2);
    expect(decoded.runs[0]).toMatchObject({ from: 0, to: 20 });
    expect(decoded.runs[0]?.style.fg).toBe('var(--color-ansi-bright-black)');
    expect(decoded.runs[1]).toMatchObject({ from: 21, to: 24 });
    expect(decoded.runs[1]?.style.fg).toBe('var(--color-ansi-red)');
  });

  it('removes every escape and not only the SGR ones', () => {
    // Matching only the SGR ones and leaving the rest would fix a colored
    // line and leave ESC[2K sitting in a progress bar.
    expect(decodeANSI('\x1b[2Kprogress').text).toBe('progress'); // CSI, any final byte
    expect(decodeANSI('\x1b]0;a title\x07after').text).toBe('after'); // OSC, BEL-terminated
    expect(decodeANSI('\x1b]0;a title\x1b\\after').text).toBe('after'); // OSC, ST-terminated
    expect(decodeANSI('\x1bMplain').text).toBe('plain'); // two-character escape
    expect(decodeANSI('\x1b[1;31mred\x1b[0m\x1b[2J').text).toBe('red');
  });

  /**
   * The limit of the pattern, stated rather than discovered.
   *
   * The two-character alternative is `ESC` followed by a byte in `0x40-0x5f`, so
   * the charset-selection family - `ESC ( B`, `ESC ) 0` - is not matched: `(` is
   * `0x28`. The whole sequence, escape byte included, reaches the pane in front
   * of the line. It is rare in container output and the fix is a wider pattern
   * rather than a different design; it is pinned here so that a future widening
   * is a deliberate change with a test that goes green, rather than a
   * rediscovery of the same defect one family along.
   */
  it('does not strip the charset-selection escapes, the pattern\'s known limit', () => {
    expect(decodeANSI('\x1b(Bplain').text).toBe('\x1b(Bplain');
  });

  it('reads a 256-colour index as the same color a basic code gives', () => {
    // Indices 0-15 come back as names, so a 38;5;1 is the same red as a 31
    // rather than a second opinion about red.
    expect(decodeANSI('\x1b[38;5;1mx').runs[0]?.style.fg).toBe('var(--color-ansi-red)');
    expect(decodeANSI('\x1b[31mx').runs[0]?.style.fg).toBe('var(--color-ansi-red)');
    expect(decodeANSI('\x1b[38;5;9mx').runs[0]?.style.fg).toBe('var(--color-ansi-bright-red)');
    // The cube and the grays.
    expect(decodeANSI('\x1b[38;5;196mx').runs[0]?.style.fg).toBe('rgb(255 0 0)');
    expect(decodeANSI('\x1b[38;5;232mx').runs[0]?.style.fg).toBe('rgb(8 8 8)');
  });

  it('reads a 24-bit color', () => {
    expect(decodeANSI('\x1b[38;2;12;34;56mx').runs[0]?.style.fg).toBe('rgb(12 34 56)');
  });

  /**
   * The reason `extended` is a function of its own, and the assertion that pins
   * it: in `ESC[38;5;208;1m` the 5 and the 208 are arguments to the 38. Read as
   * parameters in their own right they are "blink slowly" and a color nobody
   * asked for, and the `1` that meant bold arrives after two wrong answers.
   */
  it('consumes an extended color parameters, so what follows is read correctly', () => {
    const style = decodeANSI('\x1b[38;5;208;1mx').runs[0]?.style;
    expect(style?.fg).toBe('rgb(255 135 0)');
    expect(style?.bold).toBe(true);
  });

  it('accepts the colon separator several loggers emit', () => {
    // `38:5:245` is the same sequence with ITU T.416's separator. Refusing it
    // would leave a stray 5 and 245 to be read as attributes.
    expect(decodeANSI('\x1b[38:5:1mx').runs[0]?.style.fg).toBe('var(--color-ansi-red)');
  });

  it('reads a malformed sequence as no style rather than guessing', () => {
    // Half a color. The safe reading is "no style"; a guess puts a color on a
    // line the container did not color.
    expect(decodeANSI('\x1b[38mx').runs).toHaveLength(0);
    expect(decodeANSI('\x1b[38;5;999mx').runs).toHaveLength(0);
    expect(decodeANSI('\x1b[-4mx').runs).toHaveLength(0);
  });

  it('treats an empty parameter as a reset', () => {
    // ESC[m is ESC[0m, and so is every empty parameter inside one.
    const decoded = decodeANSI('\x1b[31mred\x1b[mplain');
    expect(decoded.text).toBe('redplain');
    expect(decoded.runs).toHaveLength(1);
    expect(decoded.runs[0]).toMatchObject({ from: 0, to: 3 });
  });

  it('emits no run for a sequence that changes nothing', () => {
    // Two resets in a row, and a color set to what it already is. Each would
    // otherwise cut a line into spans that all look the same.
    expect(decodeANSI('\x1b[0m\x1b[0mplain').runs).toHaveLength(0);
    expect(decodeANSI('\x1b[31ma\x1b[31mb').runs).toHaveLength(1);
  });

  it('closes an unterminated style at the end of the line', () => {
    // A container that colors and never resets is ordinary. The run has to end
    // somewhere or nothing is drawn at all.
    const decoded = decodeANSI('\x1b[32mstarted');
    expect(decoded.runs).toHaveLength(1);
    expect(decoded.runs[0]).toMatchObject({ from: 0, to: 7 });
  });

  it('is pure, which is what lets the panel cache it against a line index', () => {
    const raw = '\x1b[1;4;35mx\x1b[0m y';
    expect(decodeANSI(raw)).toEqual(decodeANSI(raw));
  });

  it('resets the shared pattern, so one colored line does not derange the next', () => {
    // `ANSI_ESCAPE` is a module-level `/g` regex. Reusing one without resetting
    // `lastIndex` reads from halfway through the previous line.
    decodeANSI('\x1b[31maaaaaaaaaaaaaaaaaaaa\x1b[0m');
    expect(decodeANSI('\x1b[32mok').text).toBe('ok');
  });
});

describe('the style a run puts on a span', () => {
  it('sets no background where the container set none', () => {
    // Empty means the pane's own colors, which is not the same as black on
    // white: a container that never set a background must not be given one.
    const style = decodeANSI('\x1b[31mx').runs[0]?.style;
    expect(style).toBeDefined();
    expect(runStyle(style!).background).toBeUndefined();
  });

  it('resolves both sides before swapping them for inverse', () => {
    // A container that set only a foreground and then asked for inverse wants
    // that color *behind* the pane's own text color; an empty string would
    // silently drop half of it.
    const style = decodeANSI('\x1b[31;7mx').runs[0]?.style;
    expect(runStyle(style!)).toEqual({
      color: 'var(--color-bg)',
      background: 'var(--color-ansi-red)',
    });
  });

  it('puts the four attributes in classes and the colors in properties', () => {
    // The classes are fixed and belong in the theme; a 38;2;r;g;b is a color no
    // stylesheet could have anticipated, so no stylesheet is needed for them.
    const style = decodeANSI('\x1b[1;2;3;4mx').runs[0]?.style;
    const classes = runClass(style!);
    expect(classes).toContain('font-semibold');
    expect(classes).toContain('opacity-65');
    expect(classes).toContain('italic');
    expect(classes).toContain('underline');
  });
});

/**
 * Every color the log panel can ask for is in the theme.
 *
 * **Half of it, and the other half is in `web/scripts/check-styles.mjs`.** That
 * is a limitation rather than a design: Vitest cannot read `theme.css` as text,
 * because the Tailwind plugin intercepts a `?raw` import of it and hands back an
 * empty string - and it fails by *passing* nothing,
 * which is the worst way for a check to break. So the two directions are split
 * across the two runners that can each see one file:
 *
 *   - here: the decoder asks for exactly these sixteen names and no others;
 *   - `check:styles`: `theme.css` defines all sixteen.
 *
 * A rename on either side reddens one of the two. Neither is sufficient alone,
 * which is why both say so.
 */
describe('the palette', () => {
  const EXPECTED = [
    'black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white',
    'bright-black', 'bright-red', 'bright-green', 'bright-yellow',
    'bright-blue', 'bright-magenta', 'bright-cyan', 'bright-white',
  ].map((name) => `var(--color-ansi-${name})`);

  it('names exactly the sixteen tokens the theme is checked for', () => {
    const asked = new Set<string>();
    for (let code = 30; code <= 37; code++) {
      asked.add(decodeANSI(`[${code}mx`).runs[0]?.style.fg ?? '');
    }
    for (let code = 90; code <= 97; code++) {
      asked.add(decodeANSI(`[${code}mx`).runs[0]?.style.fg ?? '');
    }
    expect([...asked].sort()).toEqual([...EXPECTED].sort());
  });

  it('reaches the same sixteen through the 256-colour palette', () => {
    // Indices 0-15 come back as names, so a 38;5;n is the same color as its
    // basic code rather than a second opinion about it.
    for (let n = 0; n < 16; n++) {
      const viaPalette = decodeANSI(`[38;5;${n}mx`).runs[0]?.style.fg;
      const viaCode = decodeANSI(`[${n < 8 ? 30 + n : 90 + (n - 8)}mx`).runs[0]?.style.fg;
      expect(viaPalette, `38;5;${n}`).toBe(viaCode);
      expect(EXPECTED).toContain(viaPalette);
    }
  });

  it('never names a background token the foreground set does not contain', () => {
    // The same sixteen on both axes. A background-only token would be one the
    // theme is not checked for.
    for (let code = 40; code <= 47; code++) {
      expect(EXPECTED).toContain(decodeANSI(`[${code}mx`).runs[0]?.style.bg);
    }
    for (let code = 100; code <= 107; code++) {
      expect(EXPECTED).toContain(decodeANSI(`[${code}mx`).runs[0]?.style.bg);
    }
  });
});
