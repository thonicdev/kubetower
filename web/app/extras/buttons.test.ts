import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

// An extra's page draws the console's buttons (`btn*` from `@kubetower/ui`):
// a bordered box of its own beside them is a second size, a second padding
// and a second disabled look in one console. Read from the source, so a
// button boxed by hand on any extra's page fails here.

const ROOT = __dirname;

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return name.endsWith('.tsx') && !name.includes('.test.') ? [path] : [];
  });
}

/** A box drawn on a button by its own classes. A pill (`rounded-full`) is a
 *  chip, not a button's box. */
const BOXED = /\brounded(?:-md|-sm)?(?!-)[^`"']*\bborder\b(?!-)/;

/**
 * Buttons that are not a button's box, named by the attribute that says what
 * they are: a tile with its count (`data-tile`), a card with its figure that
 * lights what came after a change (`data-correlation`), and the plain arrow
 * back of a page opened from elsewhere (`data-back`).
 */
const NOT_A_BUTTON = ['data-tile', 'data-correlation', 'data-back'];

export function boxedByHand(name: string, text: string): string[] {
  const file = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: string[] = [];
  const visit = (node: ts.Node): void => {
    if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
      const opening = ts.isJsxElement(node) ? node.openingElement : node;
      const carries = (attribute: string): boolean =>
        opening.attributes.properties.some((p) => ts.isJsxAttribute(p) && p.name.getText() === attribute);
      if (opening.tagName.getText() === 'button' && !NOT_A_BUTTON.some(carries)) {
        const className = opening.attributes.properties.find(
          (p): p is ts.JsxAttribute => ts.isJsxAttribute(p) && p.name.getText() === 'className',
        );
        const value = className?.initializer?.getText() ?? '';
        if (BOXED.test(value)) {
          const { line } = file.getLineAndCharacterOfPosition(node.getStart());
          found.push(`${name}:${String(line + 1)}`);
        }
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return found;
}

describe("the extras' buttons", () => {
  it("are the console's, not boxed by hand", () => {
    const files = sources(ROOT);
    expect(files.length).toBeGreaterThan(20);
    expect(
      files.flatMap((path) => boxedByHand(relative(ROOT, path).split('\\').join('/'), readFileSync(path, 'utf8'))),
    ).toEqual([]);
  });

  it('finds one boxed by hand', () => {
    const hand = `export const A = () => (
      <button type="button" className={\`rounded-md border border-line px-2 py-1 text-xs \${focusRing}\`}>Refresh</button>
    );
    export const B = () => <button type="button" className={btnSecondary}>Refresh</button>;`;
    expect(boxedByHand('hand.tsx', hand)).toEqual(['hand.tsx:2']);
  });
});
