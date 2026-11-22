import { history, historyKeymap, defaultKeymap } from '@codemirror/commands';
import { yaml } from '@codemirror/lang-yaml';
import { HighlightStyle, indentUnit, syntaxHighlighting, syntaxTree } from '@codemirror/language';
import {
  EditorSelection,
  EditorState,
  Prec,
  StateEffect,
  StateField,
  type Extension,
  type Line,
  type Range,
  type SelectionRange,
} from '@codemirror/state';
import {
  Decoration,
  drawSelection,
  EditorView,
  keymap,
  lineNumbers,
  ViewPlugin,
  type Command,
  type DecorationSet,
  type ViewUpdate,
} from '@codemirror/view';
import { tags as t } from '@lezer/highlight';
import { useEffect, useLayoutEffect, useRef, type ReactNode } from 'react';

import type { Range as Match } from '../../components/useFind';
import { followName } from './followName';

/**
 * The manifest editor's text surface: CodeMirror 6, with YAML colors, line
 * numbers and a caret it places itself.
 *
 * **This module is the editor's chunk.** `ManifestEditor.tsx` loads it with
 * `React.lazy`, so CodeMirror is fetched the first time a manifest is edited
 * or created and never weighs on the console's first load. Nothing else may
 * import it statically, or the chunk folds back into whoever did.
 *
 * It holds no state of its own beyond the view: the document is the parent's
 * (`value` / `onChange`), as it was with the textarea, so "nothing has been
 * changed", the change counter and the reveal all keep reading one string.
 */
export default function ManifestCode({
  value,
  onChange,
  label,
  onReady,
  followsName = false,
  found = NO_MATCHES,
  current = 0,
}: {
  value: string;
  onChange: (next: string) => void;
  /** The accessible name of the text, on the element that takes the keyboard. */
  label: string;
  /** A new object from a template: its labels follow its name (`followName.ts`).
   *  Read once, with the first document. */
  followsName?: boolean;
  /** The find bar's matches in `value`, marked in place; `current` is the one
   *  the bar is on. */
  found?: readonly Match[];
  current?: number;
  /** What the parent may do to the view: the change stepper moves the caret. */
  onReady: (handle: CodeHandle | null) => void;
}): ReactNode {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  // Read through refs from inside the view's listener, which is built once.
  const changed = useRef(onChange);
  const ready = useRef(onReady);
  useLayoutEffect(() => {
    changed.current = onChange;
    ready.current = onReady;
  });
  // The first document only: later ones arrive through the effect below.
  const initial = useRef(value);

  useEffect(() => {
    const parent = host.current;
    if (parent === null) return undefined;
    const editor = new EditorView({
      parent,
      state: EditorState.create({
        doc: initial.current,
        extensions: [
          extensions(label),
          followsName ? followName(initial.current) : [],
          EditorView.updateListener.of((update: ViewUpdate) => {
            if (update.docChanged) changed.current(update.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = editor;
    ready.current({
      reveal: (offset) => {
        const at = Math.min(Math.max(0, offset), editor.state.doc.length);
        // Scrolled to and nothing else: the keyboard stays in the find box.
        editor.dispatch({ effects: EditorView.scrollIntoView(at, { y: 'center' }) });
      },
      moveCaret: (offset) => {
        const at = Math.min(Math.max(0, offset), editor.state.doc.length);
        // A caret and not a selection: a selection in a field being typed into
        // is one keystroke from replacing the lines it covers.
        editor.dispatch({
          selection: EditorSelection.cursor(at),
          effects: EditorView.scrollIntoView(at, { y: 'center' }),
        });
        editor.focus();
      },
    });
    // Opened to be typed in. The dialog's own handover ran before this chunk
    // arrived the first time, so the editor takes the keyboard on mount.
    editor.focus();
    return () => {
      ready.current(null);
      view.current = null;
      editor.destroy();
    };
    // The label is fixed for a dialog's life; the view is built once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // A document set from outside - a reveal writing the values in - replaces
  // only the stretch that differs, so the caret and the scroll stay put.
  useEffect(() => {
    const editor = view.current;
    if (editor === null) return;
    const current = editor.state.doc.toString();
    if (current === value) return;
    editor.dispatch({ changes: difference(current, value) });
  }, [value]);

  // The matches, after the document they were found in: the effect above has
  // already made the view's document `value`, so every range lands on the
  // text it was found in.
  useEffect(() => {
    const editor = view.current;
    if (editor === null) return;
    const length = editor.state.doc.length;
    const marks = found
      .filter((match) => match.to <= length)
      .map((match, at) => (at === current ? currentHit : hit).range(match.from, match.to));
    editor.dispatch({ effects: setHits.of(Decoration.set(marks)) });
  }, [found, current, value]);

  return <div ref={host} data-manifest-code="" className="h-full min-h-0" />;
}

/** What `ManifestEditor` may ask of the view. */
export interface CodeHandle {
  /** Put the caret at `offset`, scroll it to the middle, take the keyboard. */
  moveCaret: (offset: number) => void;
  /** Scroll `offset` to the middle, leaving the caret and the keyboard. */
  reveal: (offset: number) => void;
}

const NO_MATCHES: readonly Match[] = [];

/**
 * The find bar's marks, drawn as the read view's `Mark` draws them: every match
 * a tint of the warning color, the current one the full color under the
 * background's ink. `data-find-hit` and `data-find-current` are what a test
 * counts, as in the read view.
 */
const hit = Decoration.mark({
  class: 'rounded-sm bg-warn/30 text-fg',
  attributes: { 'data-find-hit': '' },
});
const currentHit = Decoration.mark({
  class: 'rounded-sm bg-warn text-bg',
  attributes: { 'data-find-hit': '', 'data-find-current': '' },
});

const setHits = StateEffect.define<DecorationSet>();

/** The marks, carried across an edit until the parent finds again over the
 *  new document - one render later. */
const hits = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update: (marks, transaction) => {
    let next = marks.map(transaction.changes);
    for (const effect of transaction.effects) if (effect.is(setHits)) next = effect.value;
    return next;
  },
  provide: (field) => EditorView.decorations.from(field),
});

/** Two spaces, never a tab: a tab in a YAML document is a parse error, so an
 *  editor that inserted one on the key named Tab would break documents for the
 *  reason it was added. */
const INDENT = '  ';

/** The lines a selection touches. A selection that ends at the very start of a
 *  line - a drag down to the left margin, Shift+Down from a line's start -
 *  does not take that line: nothing of it is selected. */
function touchedLines(state: EditorState, range: SelectionRange): Line[] {
  const first = state.doc.lineAt(range.from).number;
  let last = state.doc.lineAt(range.to).number;
  if (last > first && state.doc.line(last).from === range.to) last -= 1;
  const lines: Line[] = [];
  for (let number = first; number <= last; number++) lines.push(state.doc.line(number));
  return lines;
}

/** Tab. With no selection, two spaces at the caret. With one, two spaces at
 *  the head of every line it touches, the selection kept over the same text:
 *  replacing the selection with the spaces, as a caret's Tab does, would throw
 *  away the lines a person selected to move. */
export const indent: Command = (view) => {
  const { state } = view;
  view.dispatch(
    state.changeByRange((range) => {
      if (range.empty) {
        return {
          changes: { from: range.from, insert: INDENT },
          range: EditorSelection.cursor(range.from + INDENT.length),
        };
      }
      const changes = state.changes(
        touchedLines(state, range).map((line) => ({ from: line.from, insert: INDENT })),
      );
      return {
        changes,
        range: EditorSelection.range(changes.mapPos(range.anchor, 1), changes.mapPos(range.head, 1)),
      };
    }),
    { scrollIntoView: true, userEvent: 'input.indent' },
  );
  return true;
};

/** Shift+Tab: up to two spaces off the head of the caret's line, or of every
 *  line a selection touches, the selection kept over the same text. A line
 *  with less indentation loses what it has and no more. The key is taken
 *  either way, so it never moves the focus out by surprise. */
export const outdent: Command = (view) => {
  const { state } = view;
  let changed = false;
  const transaction = state.changeByRange((range) => {
    const changes = state.changes(
      touchedLines(state, range).flatMap((line) => {
        const depth = /^ {0,2}/.exec(line.text)?.[0].length ?? 0;
        return depth === 0 ? [] : [{ from: line.from, to: line.from + depth }];
      }),
    );
    if (!changes.empty) changed = true;
    return {
      changes,
      range: EditorSelection.range(changes.mapPos(range.anchor, 1), changes.mapPos(range.head, 1)),
    };
  });
  if (changed) view.dispatch(transaction, { scrollIntoView: true, userEvent: 'delete.dedent' });
  return true;
};

/** A line up to the caret that ends in a key with no value yet: `spec:`,
 *  `- name:`, `"a b":  # why`. Group 1 is the dashes the key stands behind. A
 *  value is what `[^#]` stops short of: `image: nginx:` is a scalar ending in a
 *  colon, refused below by the `: ` inside it. */
const OPENS_BLOCK = /^ *((?:- +)*)([^\s#-][^#]*?|-[^\s#][^#]*?):[ \t]*(?:#.*)?$/;

/** How much deeper than its own the line after `before` goes: a level after a
 *  key with no value, and past the dashes it stands behind; nothing otherwise,
 *  and nothing inside a block scalar, where `key:` is only text. */
function deeper(state: EditorState, at: number, before: string): string {
  const match = OPENS_BLOCK.exec(before);
  if (match === null || match[2]?.includes(': ') === true) return '';
  let node = syntaxTree(state).resolveInner(at, -1);
  for (;;) {
    if (node.name === 'BlockLiteral' || node.name === 'BlockLiteralContent') return '';
    if (node.parent === null) break;
    node = node.parent;
  }
  return ' '.repeat(match[1]?.length ?? 0) + INDENT;
}

/** Enter: a new line at the depth of the one the caret is on, one level deeper
 *  where that line opens a block (`deeper`). Keeping the indentation is what
 *  makes a nested block editable without retyping its depth, and YAML is the
 *  one format where getting that wrong changes the meaning rather than the
 *  look. So this is written here and is not the language's own guess, which
 *  also lines a new line up with a list item's content after `- name: api`,
 *  where the line's own depth is what a person continues from. */
export const newline: Command = (view) => {
  const { state } = view;
  view.dispatch(
    state.changeByRange((range) => {
      const line = state.doc.lineAt(range.from);
      const before = line.text.slice(0, range.from - line.from);
      const depth = /^ */.exec(before)?.[0] ?? '';
      const insert = `\n${depth}${deeper(state, range.from, before)}`;
      return {
        changes: { from: range.from, to: range.to, insert },
        range: EditorSelection.cursor(range.from + insert.length),
      };
    }),
    { scrollIntoView: true, userEvent: 'input' },
  );
  return true;
};

function extensions(label: string): Extension {
  return [
    lineNumbers(),
    history(),
    drawSelection(),
    EditorState.tabSize.of(2),
    indentUnit.of(INDENT),
    EditorView.lineWrapping,
    yaml(),
    syntaxHighlighting(tones),
    // Highest, and before `scalars`, so a match is drawn innermost and its
    // colors are the ones shown over a token's.
    Prec.highest(hits),
    // Highest, so its marks are drawn inside the highlighter's spans and their
    // color is the one shown: the dash is a `separator` to the highlighter,
    // drawn in the text color, and the mark inside it makes it muted.
    Prec.highest(scalars),
    theme,
    Prec.highest(
      keymap.of([
        { key: 'Tab', run: indent, shift: outdent },
        { key: 'Enter', run: newline },
      ]),
    ),
    // Escape is not CodeMirror's: the keyboard stack gives it to the dialog
    // (`keyboard/layers.ts`), and the stack listens in the capture phase, so
    // it would see the key first anyway. Left out so that holds even where
    // the stack has nothing to claim it with.
    keymap.of([...defaultKeymap.filter((binding) => binding.key !== 'Escape'), ...historyKeymap]),
    EditorView.contentAttributes.of({
      'aria-label': label,
      // The dialog's focus handover looks for this, for the opens after the
      // first, when the chunk is already here at mount.
      'data-dialog-focus': '',
      // Ctrl+F here is a search of this document (`components/useFind.ts`).
      'data-find-text': '',
    }),
  ];
}

/**
 * The read view's colors (`ManifestLines.tsx`, `tones`), from the same theme
 * tokens, on the lezer YAML grammar's tags. The server's tokenizer
 * (`internal/web/yamlcolour.go`) decides the read view's classes; this is the
 * same mapping as near as the grammar allows:
 *
 * - a key is `accent`; a quoted string `ok`; a comment `muted`, italic;
 * - an anchor, an alias or a tag is `bad` (the contract's `anchor`);
 * - a dash, a document marker and a block scalar's `|` are `muted`;
 * - the colon after a key is the text color: the server writes it `plain`;
 * - a plain scalar is the text color, as on the server, which leaves it
 *   uncolored so that color still means something - except a number or a
 *   YAML keyword (`true`, `no`, `null`...), which are `warn`. The grammar
 *   does not tell those apart from text, so `scalars` below marks them.
 *
 * The grammar gives the colon and the dash one tag (`separator`), so it is
 * drawn in the text color here, and `scalars` marks the dash `muted` by its
 * node.
 */
const tones = HighlightStyle.define([
  { tag: t.definition(t.propertyName), color: 'var(--color-accent)' },
  { tag: t.string, color: 'var(--color-ok)' },
  { tag: t.lineComment, color: 'var(--color-muted)', fontStyle: 'italic' },
  { tag: [t.labelName, t.typeName], color: 'var(--color-bad)' },
  {
    tag: [t.punctuation, t.meta, t.keyword, t.special(t.string)],
    color: 'var(--color-muted)',
  },
  // A sub-tag of `punctuation`, so named on its own to be drawn as text.
  { tag: t.separator, color: 'var(--color-fg)' },
]);

/** The plain scalars the server colors as `number` or `boolean`. */
const KEYWORDS = new Set(['true', 'false', 'null', '~', 'yes', 'no', 'on', 'off']);
const NUMBER = /^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/;

/** Whether a plain scalar reads as a number or a keyword, as `isYAMLNumber`
 *  and `yamlKeywords` decide on the server. */
export function isScalarWord(text: string): boolean {
  return KEYWORDS.has(text.toLowerCase()) || NUMBER.test(text);
}

const scalar = Decoration.mark({ class: 'cm-yaml-scalar' });
const dash = Decoration.mark({ class: 'cm-yaml-dash' });

function scalarMarks(view: EditorView): DecorationSet {
  const marks: Range<Decoration>[] = [];
  const tree = syntaxTree(view.state);
  for (const { from, to } of view.visibleRanges) {
    tree.iterate({
      from,
      to,
      enter: (node) => {
        if (node.name === '-') {
          marks.push(dash.range(node.from, node.to));
          return;
        }
        if (node.name !== 'Literal' || node.node.parent?.name === 'Key') return;
        if (isScalarWord(view.state.sliceDoc(node.from, node.to))) {
          marks.push(scalar.range(node.from, node.to));
        }
      },
    });
  }
  return Decoration.set(marks, true);
}

const scalars = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = scalarMarks(view);
    }
    update(update: ViewUpdate): void {
      if (
        update.docChanged ||
        update.viewportChanged ||
        syntaxTree(update.startState) !== syntaxTree(update.state)
      ) {
        this.decorations = scalarMarks(update.view);
      }
    }
  },
  { decorations: (plugin) => plugin.decorations },
);

/**
 * The frame, from the theme's tokens: colors are `var(--color-…)` and
 * lengths are multiples of `var(--spacing)`, so nothing here is a literal the
 * theme does not hold. The font, its size and the line height are inherited
 * from the frame's utilities in `ManifestEditor.tsx`, as the textarea had them.
 * Dark, because the console is dark only.
 */
const theme = EditorView.theme(
  {
    '&': { height: '100%', color: 'var(--color-fg)', backgroundColor: 'var(--color-bg)' },
    // The frame draws the focus ring, around the gutter and the text together.
    '&.cm-focused': { outline: 'none' },
    '.cm-scroller': { fontFamily: 'inherit', lineHeight: 'inherit' },
    '.cm-content': {
      caretColor: 'var(--color-fg)',
      padding: 'calc(var(--spacing) * 2) 0',
    },
    '.cm-line': { padding: '0 calc(var(--spacing) * 3) 0 calc(var(--spacing) * 2)' },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--color-fg)' },
    '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground':
      { backgroundColor: 'color-mix(in oklab, var(--color-accent) 35%, transparent)' },
    // The read view's gutter: `text-muted/60`, on the document's own ground.
    '.cm-gutters': {
      backgroundColor: 'var(--color-bg)',
      color: 'color-mix(in oklab, var(--color-muted) 60%, transparent)',
      border: 'none',
    },
    '.cm-lineNumbers .cm-gutterElement': { padding: '0 0 0 calc(var(--spacing) * 3)' },
    '.cm-yaml-scalar': { color: 'var(--color-warn)' },
    '.cm-yaml-dash': { color: 'var(--color-muted)' },
  },
  { dark: true },
);

/** The one stretch where `after` differs from `before`. */
function difference(before: string, after: string): { from: number; to: number; insert: string } {
  let start = 0;
  const shorter = Math.min(before.length, after.length);
  while (start < shorter && before.charCodeAt(start) === after.charCodeAt(start)) start++;
  let end = 0;
  while (
    end < shorter - start &&
    before.charCodeAt(before.length - 1 - end) === after.charCodeAt(after.length - 1 - end)
  ) {
    end++;
  }
  return { from: start, to: before.length - end, insert: after.slice(start, after.length - end) };
}
