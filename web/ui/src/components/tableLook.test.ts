import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

import { bodyCell, headCell, TABLE } from './tableLook';

// Every table of the console is drawn with `tableLook.ts`: the `<table>` takes
// `TABLE`, a heading row `HEAD_ROW`, a heading `headCell` and a cell `bodyCell`
// (`DataGrid` reaches them through its own `HEAD` and `cellClass`). Read from
// the source, so a table drawn with classes of its own fails here, wherever it
// is added.

const ROOT = join(__dirname, '..');

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return name.endsWith('.tsx') && !name.includes('.test.') ? [path] : [];
  });
}

/** What each element's `className` must name, by tag. */
const WANTS: Readonly<Record<string, RegExp>> = {
  table: /\bTABLE\b/,
  th: /\b(?:headCell|bodyCell|cellClass)\(|\bHEAD\b/,
  td: /\b(?:bodyCell|cellClass)\(/,
};

function attribute(opening: ts.JsxOpeningLikeElement, name: string): ts.JsxAttribute | undefined {
  return opening.attributes.properties.find(
    (p): p is ts.JsxAttribute => ts.isJsxAttribute(p) && p.name.getText() === name,
  );
}

/** The elements of one source that draw a table part with classes of their own. */
function drawnByHand(name: string, text: string): string[] {
  const file = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: string[] = [];
  const visit = (node: ts.Node, parent: string | undefined): void => {
    let tag = parent;
    if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
      const opening = ts.isJsxElement(node) ? node.openingElement : node;
      tag = opening.tagName.getText();
      const className = attribute(opening, 'className')?.initializer?.getText() ?? '';
      // A heading row is the one `<tr>` whose classes are the table's.
      const want = tag === 'tr' && parent === 'thead' ? /\bHEAD_ROW\b/ : WANTS[tag];
      // A cell spanning every column is a sentence about the table (its empty
      // state), not a cell of a column.
      const spans = attribute(opening, 'colSpan') !== undefined;
      if (want !== undefined && !spans && !want.test(className)) {
        const { line } = file.getLineAndCharacterOfPosition(node.getStart());
        found.push(`${name}:${String(line + 1)} <${tag}>`);
      }
    }
    ts.forEachChild(node, (child) => {
      visit(child, tag);
    });
  };
  visit(file, undefined);
  return found;
}

/**
 * The drawer's fact sheets: a field and its value, a container's environment, a
 * role's rules, a bundle's certificates. Their layout is their own - a label
 * column, values that wrap, no list row height - and **their size is the
 * lists'**: the `<table>` carries `TABLE`, and nothing in it sets a smaller one.
 */
const SHEETS: ReadonlySet<string> = new Set([
  'pages/object/Certificates.tsx',
  'pages/object/Containers.tsx',
  'pages/object/ObjectDetail.tsx',
  'pages/object/Rules.tsx',
]);

/** Inside a sheet's table, a size below the lists' body. */
const SMALLER = /\btext-(?:xs|2xs)\b/;

/** The table of a sheet without `TABLE`, or a cell of it drawn smaller. */
function sheetOutOfLine(name: string, text: string): string[] {
  const file = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: string[] = [];
  const visit = (node: ts.Node): void => {
    if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
      const opening = ts.isJsxElement(node) ? node.openingElement : node;
      const tag = opening.tagName.getText();
      const className = attribute(opening, 'className')?.initializer?.getText() ?? '';
      const { line } = file.getLineAndCharacterOfPosition(node.getStart());
      if (tag === 'table' && !WANTS.table!.test(className)) found.push(`${name}:${String(line + 1)} <table>`);
      // Every cell, wherever it is written: a sheet's rows are often a
      // component of their own, outside the `<table>` in the source.
      if ((tag === 'td' || tag === 'th') && SMALLER.test(className)) {
        found.push(`${name}:${String(line + 1)} <${tag}> smaller`);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return found;
}

/**
 * The extras' pages, which reach the look through `@kubetower/ui`: a table on
 * an extra's page is a table of the console, at the lists' size.
 */
const EXTRAS = join(__dirname, '..', '..', '..', 'app', 'extras');

/**
 * The extras' tables that are not rows of a list, and keep a look of their
 * own: they are read as code, monospaced and dense, as the manifest is.
 */
const OWN_LOOK: ReadonlySet<string> = new Set([
  // A desired field against its live value, tinted per side.
  'extras/argocd/ArgoTab.tsx',
  // Redis's key tree, indented per folder, and a key's value, element by element.
  'extras/redis/InstancePage.tsx',
  // A Postgres table's first rows, cell by cell, as the database holds them.
  'extras/cnpg/Database.tsx',
  // Every namespace against every namespace: a grid of colored cells, not rows.
  'extras/netinspect/Matrix.tsx',
]);

describe('every table', () => {
  const files = [
    ...sources(ROOT).map((path) => ({
      name: relative(ROOT, path).split('\\').join('/'),
      text: readFileSync(path, 'utf8'),
    })),
    ...sources(EXTRAS).map((path) => ({
      name: `extras/${relative(EXTRAS, path).split('\\').join('/')}`,
      text: readFileSync(path, 'utf8'),
    })),
  ];

  it('is drawn with the classes of tableLook.ts', () => {
    expect(files.length).toBeGreaterThan(50); // the walk found the package
    expect(
      files
        .filter((file) => !SHEETS.has(file.name) && !OWN_LOOK.has(file.name))
        .flatMap((file) => drawnByHand(file.name, file.text)),
    ).toEqual([]);
  });

  it("walks the extras' pages too", () => {
    expect(files.filter((file) => file.name.startsWith('extras/')).length).toBeGreaterThan(20);
    expect(files.filter((file) => OWN_LOOK.has(file.name))).toHaveLength(OWN_LOOK.size);
  });

  it("draws the drawer's sheets at the lists' size", () => {
    expect(files.filter((file) => SHEETS.has(file.name))).toHaveLength(SHEETS.size);
    expect(files.filter((file) => SHEETS.has(file.name)).flatMap((file) => sheetOutOfLine(file.name, file.text))).toEqual(
      [],
    );
  });

  it('finds a table drawn by hand', () => {
    const hand = `export const A = () => (
      <table className="kt-grid w-full text-xs">
        <thead><tr className="text-2xs uppercase"><th className="px-2 py-1.5">Name</th></tr></thead>
        <tbody><tr><td className="px-2 py-1">x</td><td colSpan={2} className="p-6">none</td></tr></tbody>
      </table>
    );`;
    expect(drawnByHand('hand.tsx', hand)).toEqual([
      'hand.tsx:2 <table>',
      'hand.tsx:3 <tr>',
      'hand.tsx:3 <th>',
      'hand.tsx:4 <td>',
    ]);
  });
});

describe('the look', () => {
  it('draws every body at one size', () => {
    expect(TABLE.split(' ')).toEqual(['kt-grid', 'text-sm']);
  });

  it('pads a heading as its column', () => {
    expect(headCell()).toContain('px-2');
    expect(bodyCell()).toContain('px-2');
    expect(headCell('px-1')).toContain('px-1');
    expect(bodyCell(false, 'px-1')).toContain('px-1');
  });

  // The height is the row's, whatever it holds: a list of plain text and a
  // list with a pill in it are drawn at one height. The pill is centered so it
  // never stands on the text's baseline and lifts the line box.
  it('states the row height at both densities and centers a pill', () => {
    expect(bodyCell()).toContain('h-9');
    expect(bodyCell(true)).toContain('h-7');
    expect(bodyCell()).toContain('**:data-pill:align-middle');
    expect(bodyCell(true)).toContain('**:data-pill:align-middle');
  });
});
