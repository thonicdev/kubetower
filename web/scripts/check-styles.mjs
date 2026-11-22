// The styling rules, checked. Without this they are comments.
//
//   Tailwind only: no hand-written CSS file, and no hand-written HTML file,
//   with the exceptions named in `ALLOWED` below - so another is a defect
//   rather than a judgment call.
//
// A Node script rather than `find | grep` because the front-end CI job and a
// developer's machine do not agree on which shell they have, and a check that
// only runs in CI is one nobody runs before pushing.

import { readdirSync, statSync, readFileSync, existsSync } from 'node:fs';
import { relative, join, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const webRoot = fileURLToPath(new URL('..', import.meta.url));

/**
 * **The whole repository**, not only `web/`: **no hand-written CSS or HTML
 * anywhere in this repository** outside the named exceptions, Go's embedded
 * static files included.
 *
 * **And it walks `web/` when there is no repository above it**, the one place
 * that happens: the image's front-end stage copies `web/` **alone** into
 * `/web`, so `../..` there is the container's filesystem root, and walking it
 * reaches `/dev/fd/…` and dies with `ENOENT: stat`. A gate that cannot run is
 * worse than a narrow one.
 *
 * `go.mod` is the marker because it is the one file that says *this is the
 * repository* and cannot be anywhere else.
 */
const above = fileURLToPath(new URL('../..', import.meta.url));
const inRepository = existsSync(join(above, 'go.mod'));
const repoRoot = inRepository ? above : webRoot;

const SKIP = new Set(['node_modules', 'dist', '.git', 'coverage', 'test-results', 'playwright-report']);

// Relative to whatever was walked, so the same two names work from the
// repository and from `web/` alone.
//
// **No vendored stylesheet.** xterm's stylesheet is imported from the
// `@xterm/xterm` package (`ui/src/dock/terminal.ts` says why), so the CSS and
// the JavaScript it styles come out of one package at one version; a copy in
// the tree could drift from that version. Third-party CSS comes from its
// package. And no entry here for an absent file: it would match nothing, stay
// green, and let through the first stylesheet dropped at that exact path.
const ALLOWED = new Set(
  [
    // The theme itself. One file, tokens only, no component rules.
    'app/theme.css',
    // Vite's entry point. A <div id="root">, the installable-application links,
    // and nothing else.
    'index.html',
  ].map((path) => (inRepository ? `web/${path}` : path)),
);

// What counts as a stylesheet or a page: every spelling, in any case.
//
// Case-insensitive, and `.scss`, `.less` and `.htm` too: the same hand-written
// stylesheet or page under another name. A preprocessor's source is a
// stylesheet before it is compiled, and refusing only the output would admit
// it one step upstream. The preprocessor list is the one Vite treats as
// CSS (`CSS_LANGS_RE` in vite 8: `less sass scss styl stylus pcss postcss sss`).
//
// The two exceptions in `ALLOWED` still match exactly, case included: on the
// Linux runner `app/Theme.css` is another file, not the theme.
const STYLE_OR_PAGE = /\.(?:css|less|sass|scss|styl|stylus|pcss|postcss|sss|html?|xhtml)$/i;

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    if (SKIP.has(name)) continue;
    const full = join(dir, name);
    if (statSync(full).isDirectory()) walk(full, out);
    else out.push(full);
  }
  return out;
}

const offenders = walk(repoRoot)
  .map((full) => relative(repoRoot, full).split(sep).join('/'))
  .filter((path) => STYLE_OR_PAGE.test(path))
  .filter((path) => !ALLOWED.has(path));

if (offenders.length > 0) {
  console.error('check-styles: hand-written CSS or HTML outside the named exceptions:');
  for (const path of offenders) console.error(`  ${path}`);
  process.exit(1);
}

// The third rule: no literal color, size or radius outside the theme. The two
// checks above only look at file extensions.
//
// Why it matters beyond tidiness: the theme is a measured set of tokens, and
// its type scale exists because most of the console's text sits below
// Tailwind's smallest default step. A literal `text-[13px]`
// or `#161b22` in a component is a value that no longer moves when the theme
// moves - the drift is invisible until two surfaces disagree.
//
// Deliberately narrow. It refuses a hex color and an arbitrary length inside a
// Tailwind utility, which is where the drift actually happens. It does not try to
// police every number in a TypeScript file: a width computed in JavaScript is not
// a design token, and a check that flagged it would be turned off.
const VALUE_RULES = [
  [/\b(?:bg|text|border|fill|stroke|ring|outline|shadow|from|via|to)-\[#[0-9a-fA-F]{3,8}\]/g,
   'a literal color in a utility - use a theme token'],
  // Any utility's arbitrary value holding a literal length, wherever in the
  // brackets it sits and whatever the prefix: `shadow-[inset_3px_0_0_…]` and
  // `align-[-2px]` are the same drift as `w-[3px]`.
  //
  // And in an arbitrary property as well as an arbitrary value -
  // `[margin-top:3px]` and `[--icon:16px]` are the same literal spelt another
  // way - with a decimal that has no leading digit (`.5rem`), `pt`, and a unit
  // in capitals.
  [/(?:\b[a-z][a-z0-9-]*-\[|\[(?:--)?[a-z][a-z0-9-]*:)[^\]\s'"`]*?(?<![a-z0-9.])(?:\d+(?:\.\d+)?|\.\d+)(?:px|rem|em|pt)(?![a-z])[^\]\s'"`]*\]/gi,
   'a literal size in a utility - use a theme step, or say why in a comment'],
  [/(?:^|[^-\w])#[0-9a-fA-F]{6}\b(?![0-9a-fA-F])/g,
   'a raw hex color - the palette is in app/theme.css'],
];

// A line may opt out by naming the reason. A rule with no escape hatch is one
// somebody deletes the first time it is wrong.
const ALLOW_COMMENT = /style-allow:/i;

const valueOffenders = [];
for (const full of walk(webRoot)) {
  const path = relative(webRoot, full).split(sep).join('/');
  if (!/\.(ts|tsx)$/.test(path)) continue;
  const lines = readFileSync(full, 'utf8').split('\n');
  lines.forEach((line, i) => {
    if (ALLOW_COMMENT.test(line)) return;
    for (const [re, why] of VALUE_RULES) {
      re.lastIndex = 0;
      const m = re.exec(line);
      if (m) valueOffenders.push(`  web/${path}:${i + 1}  ${m[0].trim()} - ${why}`);
    }
  });
}

if (valueOffenders.length > 0) {
  console.error('check-styles: literal design values outside the theme:');
  for (const line of valueOffenders) console.error(line);
  console.error('\nAdd `style-allow:` and the reason to a line that genuinely needs one.');
  process.exit(1);
}

// --- The sixteen ANSI colors the log pane can ask for ------------------------
//
// `ui/src/dock/ansi.ts` writes `var(--color-ansi-<name>)` for every color a
// container's escape sequence names. A token the theme does not define resolves
// to nothing, and the line is then drawn in the pane's own color - silently.
//
// **This is one half of a two-sided check, and the split is a limitation rather
// than a design.** The other half is in `ui/src/dock/ansi.test.ts`, which
// asserts the decoder asks for exactly these sixteen names. Neither runner can
// see both files: Vitest cannot read `theme.css` as text, because the Tailwind
// plugin intercepts a `?raw` import of it and hands back an empty string. So a rename on either side reddens one of the two,
// and both are needed.
const ANSI = [
  'black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white',
  'bright-black', 'bright-red', 'bright-green', 'bright-yellow',
  'bright-blue', 'bright-magenta', 'bright-cyan', 'bright-white',
];

const themeSource = readFileSync(join(webRoot, 'app', 'theme.css'), 'utf8');
const missingAnsi = ANSI.filter((name) => !themeSource.includes(`--color-ansi-${name}:`));
if (missingAnsi.length > 0) {
  console.error('check-styles: app/theme.css does not define every ANSI color the log pane can ask for:');
  for (const name of missingAnsi) console.error(`  --color-ansi-${name}`);
  console.error(
    "\nA token the decoder names and the theme does not define resolves to nothing, and the\n" +
    "line is drawn in the pane's own color with nothing on screen saying so.",
  );
  process.exit(1);
}

// --- The nameplate palette, one token per key the contract allows ------------
//
// `Cluster.colour` is a key of `ClusterColour` in
// `api/openapi.yaml`, and `ui/src/shell/clusterState.ts` maps each key to
// `bg-plate-<key>` and an ink. The compiler holds that map's keys to the enum
// (`Record<ClusterColour, ...>`); nothing held the theme or the class strings
// to it, and a key with no token, or a misspelt class, draws a tile with no
// color and no error. Read from the contract rather than listed here, so this
// check cannot agree with a stale copy of itself.
//
// **The contract is required, not optional**: skipping the palette when
// `api/openapi.yaml` is absent would turn the check off in silence. The image's front-end stage copies
// the contract to the same relative place (`Dockerfile`, `COPY api/openapi.yaml
// /api/openapi.yaml`), so there is no copy of `web/` this runs in without it.
const spec = join(webRoot, '..', 'api', 'openapi.yaml');
if (!existsSync(spec)) {
  console.error(`check-styles: ${spec} is missing, so the nameplate palette cannot be checked against the contract.`);
  process.exit(1);
}
const contract = readFileSync(spec, 'utf8');
// The schema's own lines: from `    ClusterColour:` to the next line indented
// no deeper than it, and the enum's items are the `        - key` among them.
const contractLines = contract.split(/\r?\n/);
const enumStart = contractLines.indexOf('    ClusterColour:');
const enumEnd =
  enumStart < 0 ? -1 : contractLines.findIndex((line, at) => at > enumStart && /^ {0,4}\S/.test(line));
const plates =
  enumStart < 0
    ? []
    : contractLines
        .slice(enumStart + 1, enumEnd < 0 ? undefined : enumEnd)
        .map((line) => /^ {8}- ([a-z-]+)\s*$/.exec(line)?.[1])
        .filter((key) => key !== undefined);
if (plates.length === 0) {
  console.error('check-styles: api/openapi.yaml declares no ClusterColour enum this check can read.');
  process.exit(1);
}

// Tokens are read with the comments taken out first: a token moved into a
// `/* */` is not defined, and a substring search would find it anyway.
const themeTokens = new Map(
  [...themeSource.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/(--color-[a-z0-9-]+)\s*:\s*([^;]+);/g)].map(
    ([, name, value]) => [name, value.trim()],
  ),
);
const missingPlates = plates.filter((key) => !themeTokens.has(`--color-plate-${key}`));
if (missingPlates.length > 0) {
  console.error('check-styles: app/theme.css does not define a token for every ClusterColour key:');
  for (const key of missingPlates) console.error(`  --color-plate-${key}`);
  console.error('\nA tile whose key has no token is drawn with no color, and nothing says so.');
  process.exit(1);
}

// The class map itself. Every key's value must be exactly its own swatch and one
// of the two inks: Tailwind generates nothing for a misspelt class, the tile is
// then drawn with no background, and no compiler or test saw it.
const stateSource = readFileSync(join(webRoot, 'ui', 'src', 'shell', 'clusterState.ts'), 'utf8');
const plateBlock = /const PLATE[^=]*=\s*\{([\s\S]*?)\n\};/.exec(stateSource)?.[1];
if (plateBlock === undefined) {
  console.error('check-styles: ui/src/shell/clusterState.ts has no `const PLATE = { ... };` this check can read.');
  process.exit(1);
}
const plateClasses = new Map(
  [...plateBlock.matchAll(/^\s*'?([a-z-]+)'?\s*:\s*'([^']*)',?\s*$/gm)].map(([, key, classes]) => [key, classes]),
);
const plateProblems = [];
for (const key of plates) {
  const classes = plateClasses.get(key);
  const ink = classes === undefined ? undefined : new RegExp(`^bg-plate-${key} text-plate-ink-(light|dark)$`).exec(classes)?.[1];
  if (classes === undefined) plateProblems.push(`  ${key}: no entry in PLATE`);
  else if (ink === undefined) plateProblems.push(`  ${key}: '${classes}' is not 'bg-plate-${key} text-plate-ink-light|dark'`);
}
for (const key of plateClasses.keys()) {
  if (!plates.includes(key)) plateProblems.push(`  ${key}: in PLATE but not in the contract's ClusterColour`);
}
if (plateProblems.length > 0) {
  console.error('check-styles: the PLATE map in ui/src/shell/clusterState.ts does not match the palette:');
  for (const line of plateProblems) console.error(line);
  process.exit(1);
}

// --- And every swatch is readable under its own initials ---------------------
//
// The initials are `text-sm font-semibold`, 13px in this theme, so WCAG AA's
// 4.5:1 applies (13px semibold is not "large text"). Computed here from
// the tokens, so a swatch or an ink retuned below the line goes red where it
// is changed rather than on somebody's screen.
const MIN_CONTRAST = 4.5;
function rgbOf(value) {
  const hex = /^#([0-9a-f]{6})$/i.exec(value)?.[1];
  if (hex !== undefined) return [0, 2, 4].map((at) => Number.parseInt(hex.slice(at, at + 2), 16) / 255);
  const hsl = /^hsl\(\s*([\d.]+)\s+([\d.]+)%\s+([\d.]+)%\s*\)$/i.exec(value);
  if (hsl === null) return undefined;
  const [h, s, l] = [Number(hsl[1]), Number(hsl[2]) / 100, Number(hsl[3]) / 100];
  const a = s * Math.min(l, 1 - l);
  const f = (n) => {
    const k = (n + h / 30) % 12;
    return l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
  };
  return [f(0), f(8), f(4)];
}
function luminance([r, g, b]) {
  const lin = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}
const contrastProblems = [];
let lowest = Infinity;
for (const key of plates) {
  const ink = /text-plate-ink-(light|dark)$/.exec(plateClasses.get(key))[1];
  const swatch = rgbOf(themeTokens.get(`--color-plate-${key}`));
  const inkRgb = rgbOf(themeTokens.get(`--color-plate-ink-${ink}`) ?? '');
  if (swatch === undefined || inkRgb === undefined) {
    contrastProblems.push(`  ${key}: a value this check cannot read (a #rrggbb or an hsl(h s% l%))`);
    continue;
  }
  const [hi, lo] = [luminance(swatch), luminance(inkRgb)].sort((x, y) => y - x);
  const ratio = (hi + 0.05) / (lo + 0.05);
  lowest = Math.min(lowest, ratio);
  if (ratio < MIN_CONTRAST) contrastProblems.push(`  ${key} with the ${ink} ink: ${ratio.toFixed(2)}:1`);
}
if (contrastProblems.length > 0) {
  console.error(`check-styles: a nameplate swatch is below ${MIN_CONTRAST}:1 against the ink its initials are drawn in:`);
  for (const line of contrastProblems) console.error(line);
  process.exit(1);
}

console.log(
  `check-styles: ok across ${inRepository ? 'the repository' : 'web/ (no repository above this copy)'}. ` +
  `${ALLOWED.size} stylesheet exceptions, no others; no literal design values; ` +
  `${ANSI.length} ANSI colors defined; ${plates.length} nameplate colors defined, ` +
  `each at ${lowest.toFixed(2)}:1 or more under its initials.`,
);
