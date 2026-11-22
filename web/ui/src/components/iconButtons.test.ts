import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

// Every button that shows no word - an icon or a glyph alone - carries its
// name on the control (`aria-label` or `aria-labelledby`), or a screen reader
// announces it as "button" and nothing else. Read from the source, so a new
// icon-only button anywhere in the package fails here.

const ROOT = join(__dirname, '..');

/** The extras' pages: their close × is the console's too. */
const EXTRAS = join(__dirname, '..', '..', '..', 'app', 'extras');

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return name.endsWith('.tsx') && !name.includes('.test.') ? [path] : [];
  });
}

/** Whether a child can show a word: text with a letter, or an expression,
 *  which may hold one. An icon, an aria-hidden span or a lone glyph cannot. */
function showsAWord(child: ts.JsxChild): boolean {
  if (ts.isJsxText(child)) return /\p{L}/u.test(child.text);
  if (ts.isJsxExpression(child)) return child.expression !== undefined;
  if (ts.isJsxSelfClosingElement(child)) return false;
  if (ts.isJsxElement(child)) {
    const hidden = child.openingElement.attributes.properties.some(
      (p) => ts.isJsxAttribute(p) && p.name.getText() === 'aria-hidden',
    );
    return !hidden && child.children.some(showsAWord);
  }
  return true;
}

function unnamedIconButtons(path: string): string[] {
  const text = readFileSync(path, 'utf8');
  const file = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: string[] = [];
  const visit = (node: ts.Node): void => {
    if (ts.isJsxElement(node) && node.openingElement.tagName.getText() === 'button') {
      const attrs = node.openingElement.attributes.properties;
      const named = attrs.some(
        (p) =>
          ts.isJsxSpreadAttribute(p) ||
          (ts.isJsxAttribute(p) && ['aria-label', 'aria-labelledby'].includes(p.name.getText())),
      );
      if (!named && !node.children.some(showsAWord)) {
        const { line } = file.getLineAndCharacterOfPosition(node.getStart());
        found.push(`${relative(ROOT, path)}:${line + 1}`);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return found;
}

describe('icon-only buttons', () => {
  it('each carries an accessible name', () => {
    const files = sources(ROOT);
    expect(files.length).toBeGreaterThan(50); // the walk found the package
    expect(files.flatMap(unnamedIconButtons)).toEqual([]);
  });
});

/** Whether a button shows a close glyph and no word: the `close` icon, or a
 *  typed ×. */
function closeGlyph(child: ts.JsxChild): boolean {
  if (ts.isJsxText(child)) return /[×✕✖]/u.test(child.text);
  if (ts.isJsxExpression(child)) {
    const value = child.expression;
    return value !== undefined && ts.isStringLiteral(value) && /[×✕✖]/u.test(value.text);
  }
  const opening = ts.isJsxSelfClosingElement(child) ? child : ts.isJsxElement(child) ? child.openingElement : undefined;
  if (opening === undefined) return false;
  const isIcon =
    opening.tagName.getText() === 'Icon' &&
    opening.attributes.properties.some(
      (p) =>
        ts.isJsxAttribute(p) &&
        p.name.getText() === 'name' &&
        p.initializer !== undefined &&
        ts.isStringLiteral(p.initializer) &&
        p.initializer.text === 'close',
    );
  return isIcon || (ts.isJsxElement(child) && child.children.some(closeGlyph));
}

function closeButtonsOfTheirOwn(path: string): string[] {
  const text = readFileSync(path, 'utf8');
  const file = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: string[] = [];
  const visit = (node: ts.Node): void => {
    if (ts.isJsxElement(node) && ['button', 'a', 'Link'].includes(node.openingElement.tagName.getText())) {
      const words = node.children.some((child) => showsAWord(child) && !closeGlyph(child));
      if (!words && node.children.some(closeGlyph)) {
        const { line } = file.getLineAndCharacterOfPosition(node.getStart());
        found.push(`${relative(ROOT, path)}:${line + 1}`);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return found;
}

describe('the close ×', () => {
  // One shape for every close: `CloseButton` holds the glyph, the sizes and
  // the name. A button of its own drawing a × is a box that differs from the
  // others by a few pixels, which is how the dock's came to.
  it('is drawn by CloseButton and nowhere else', () => {
    const files = sources(ROOT).filter((path) => !path.endsWith(join('components', 'CloseButton.tsx')));
    expect(files.length).toBeGreaterThan(50);
    expect(files.flatMap(closeButtonsOfTheirOwn)).toEqual([]);
  });

  // An extra's page closes a log, a key or a filter with the same ×.
  it("is CloseButton's on the extras' pages too", () => {
    const files = sources(EXTRAS);
    expect(files.length).toBeGreaterThan(20);
    expect(files.flatMap(closeButtonsOfTheirOwn)).toEqual([]);
  });

  it('finds one drawn by hand, as a button or as a link', () => {
    const path = join(ROOT, 'components', 'CloseButton.tsx');
    // The button and the link `to` draws.
    expect(closeButtonsOfTheirOwn(path)).toHaveLength(2);
  });
});
