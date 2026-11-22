import { describe, expect, it } from 'vitest';

import { KeyLayer } from './layers';

/**
 * Every `<Dialog>` in the console names a layer, or is recorded here as a
 * default somebody chose.
 *
 * ## Why a test that reads source text
 *
 * `Dialog`'s `layer` prop is optional and falls back to `KeyLayer.Dialog`. That
 * default is right for a dialog and wrong for everything else, and nothing
 * about a call site says which of the two happened: a dialog on the `Dialog`
 * layer because somebody decided it belongs there, and a dialog on the `Dialog`
 * layer because nobody thought about it, are the same absence of eleven
 * characters.
 *
 * Such a site does not misbehave at first: a claimant that shares a layer with
 * the thing underneath it still wins on the tie-break, *as long as it
 * registered last*. That is the whole reason this class is invisible. It costs
 * nothing until a dialog is raised from inside another one, and then one
 * keystroke closes the wrong thing - which is the defect the keyboard stack was
 * built to end (`layers.ts`, "The problem").
 *
 * So the omission is what this test looks for, in the only place it is visible:
 * the source. Three properties follow.
 *
 *   - **A new `<Dialog>` with no `layer` fails this test by name**, printing its
 *     file and its title, with no run-time condition to reproduce.
 *   - **A default stays legal.** It is recorded below with the reason, which is
 *     the sentence that was missing.
 *   - **The comparison runs both ways.** A site that gains a layer and is left
 *     in the list fails too, so the list cannot rot into a list of exceptions
 *     nobody reads.
 *
 * ## What it does not do
 *
 * It does not check that a named layer is the *right* layer - `stack.test.ts`
 * and the two page suites do that, by dispatching a key in both registration
 * orders. It reads text, not types: a `layer` spread in from an object would
 * read as named here. And it skips `*.test.tsx`, where a `<Dialog>` is an
 * arrangement rather than a call site in the console.
 */

/** Every source file in `ui/src`, as text. `import.meta.glob` rather than
 *  `node:fs`, so the paths are resolved by the same bundler that builds the
 *  application and cannot drift from whatever working directory a runner
 *  happens to start in. */
const modules = import.meta.glob(['../**/*.ts', '../**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

interface Site {
  /** `pages/object/Data.tsx` - relative to `ui/src`, forward slashes on every
   *  platform, because this string is compared against the list below. */
  readonly file: string;
  /** The `title` attribute as written, which is what tells two dialogs in one
   *  file apart without pinning a line number that every edit above it moves. */
  readonly title: string;
  readonly layered: boolean;
}

/** From `<Dialog` to the `>` that ends its opening tag, ignoring anything inside
 *  a brace or a quote - ``title={`Save ${key}?`}`` is one attribute and not
 *  three. */
function openingTag(text: string, from: number): string {
  let depth = 0;
  let quote = '';
  for (let at = from; at < text.length; at++) {
    const char = text[at];
    if (quote !== '') {
      if (char === quote) quote = '';
      continue;
    }
    if (char === '"' || char === "'" || char === '`') quote = char;
    else if (char === '{') depth++;
    else if (char === '}') depth--;
    else if (char === '>' && depth === 0) return text.slice(from, at + 1);
  }
  throw new Error(`a <Dialog ...> whose opening tag never ends, at ${from}`);
}

/** One attribute as written, whitespace collapsed, or `''` when it is absent.
 *  A quoted value ends at its closing quote; a braced one at the brace that
 *  balances it, so ``{`Edit ${name}`}`` is the whole value and not the half of
 *  it before the interpolation. */
function attribute(tag: string, named: string): string {
  const at = tag.search(new RegExp(`\\s${named}=`));
  if (at < 0) return '';
  const from = at + named.length + 2;
  const opener = tag[from];
  if (opener === '"' || opener === "'") {
    const end = tag.indexOf(opener, from + 1);
    return `${named}=${tag.slice(from, end + 1)}`.replace(/\s+/g, ' ');
  }
  let depth = 0;
  let quote = '';
  for (let cursor = from; cursor < tag.length; cursor++) {
    const char = tag[cursor];
    if (quote !== '') {
      if (char === quote) quote = '';
      continue;
    }
    if (char === '"' || char === "'" || char === '`') quote = char;
    else if (char === '{') depth++;
    else if (char === '}' && --depth === 0) {
      return `${named}=${tag.slice(from, cursor + 1)}`.replace(/\s+/g, ' ');
    }
  }
  throw new Error(`a <Dialog ...> whose ${named} never closes`);
}

function sites(): Site[] {
  const found: Site[] = [];
  for (const [path, text] of Object.entries(modules)) {
    const file = path.replace(/^\.\.\//, '');
    if (/\.test\.tsx?$/.test(file)) continue;
    const opens = /<Dialog(?=[\s/>])/g;
    for (let match = opens.exec(text); match !== null; match = opens.exec(text)) {
      const tag = openingTag(text, match.index);
      found.push({ file, title: attribute(tag, 'title'), layered: /\slayer=/.test(tag) });
    }
  }
  return found;
}

function named(site: Site): string {
  return `${site.file} - ${site.title}`;
}

/**
 * The call sites that take the default, and why each one is a decision.
 *
 * A new entry here is a sentence somebody had to write. That is the point: the
 * cost of not thinking about the layer is now a paragraph rather than nothing.
 */
const RECORDED: readonly { readonly site: string; readonly because: string }[] = [
  // A site leaves this list when it gains a layer: a list of exceptions nobody
  // takes anything out of is a list nobody reads.
  {
    site: 'pages/object/ManifestEditor.tsx - title={title}',
    because:
      "This is the `Dialog` row of `layers.ts`'s own table - the manifest " +
      'dialog, the thing every other layer is placed above or below. The ' +
      'default is the layer it would otherwise name. Its create mode is the ' +
      'same dialog with another title.',
  },
  {
    site: 'pages/resources/CreateObject.tsx - title={fallbackTitle}',
    because:
      'The stand-in for the manifest editor while the template of a new object ' +
      'is read, or when it could not be: it opens where the ' +
      "editor would, so it takes the editor's layer, the default.",
  },
  {
    site: 'shell/RailArrange.tsx - title="Arrange the rail"',
    because:
      'A modal that owns the screen until it is answered, opened from the rail ' +
      'and from nothing else. Its own header already says so: "Escape at ' +
      '`KeyLayer.Dialog` from the one keyboard stack".',
  },
];

describe('every dialog in the console has a layer or a recorded reason', () => {
  it('has no `<Dialog>` that simply left the layer out', () => {
    const defaulted = sites()
      .filter((one) => !one.layered)
      .map(named)
      .sort();

    expect(
      defaulted,
      'A `<Dialog>` with no `layer` takes `KeyLayer.Dialog`, which is right for a ' +
        'dialog and wrong for a confirmation or an overlay - and nothing at the ' +
        'call site says which of those it is. Pass a layer, or add the site to ' +
        'RECORDED in this file with the sentence saying why the default is the ' +
        'right one. A site that has gained a layer comes back out of the list.',
    ).toEqual(RECORDED.map((one) => one.site).sort());
  });

  it('is reading the source it claims to read', () => {
    // A scanner that quietly matches nothing agrees with an empty list, and the
    // test above would then guard nothing for as long as it took somebody to
    // notice. These are the two floors it was written against; both only ever
    // go up, so neither has to be maintained.
    expect(Object.keys(modules).length).toBeGreaterThan(50);
    expect(sites().length).toBeGreaterThanOrEqual(6);
  });

  it('keeps both layers above the default', () => {
    // Not which call site uses them - that is the two page suites, in both
    // registration orders. This is the table itself: a renumbering that put
    // either at or below `Dialog` would make every `layer=` above correct and
    // inert.
    expect(KeyLayer.Overlay).toBeGreaterThan(KeyLayer.Dialog);
    expect(KeyLayer.Confirm).toBeGreaterThan(KeyLayer.Dialog);
  });
});
