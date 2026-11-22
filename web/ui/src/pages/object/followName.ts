import { EditorState, StateField, type Extension } from '@codemirror/state';

/**
 * A new object's labels follow its name, until the person changes them.
 *
 * A written template repeats the object's name where the object refers to
 * itself: `app: example-deployment` under the selector and the pod template,
 * `serviceName` on a StatefulSet. Rename the object and those stay behind, and
 * the document creates something called `web` that is labeled
 * `example-deployment`.
 *
 * ## Which lines
 *
 * The lines of the template as it arrived that hold exactly the name. So a
 * Service, whose selector names the Deployment and not itself, has none and
 * nothing follows. Counted once, and that count is the test of "not changed by
 * the person": every one of them still holding the name is what says so. Change
 * one, and the next rename leaves them all where they are, rather than making
 * half a selector agree. Set them back, and they follow again.
 *
 * ## One transaction
 *
 * The labels change in the transaction of the keystroke that changed the name,
 * by a filter rather than by a second dispatch, so one undo takes both back.
 * An undo itself is left alone: it already holds what the document was.
 */
export function followName(initial: string): Extension {
  const name = objectName(initial);
  const lines = name === undefined ? 0 : holding(initial, name).length;
  if (name === undefined || lines === 0) return [];

  /** The name the lines hold, or null where the person has changed them. */
  const held = StateField.define<string | null>({
    create: () => name,
    update: (was, transaction) => {
      if (!transaction.docChanged) return was;
      const doc = transaction.newDoc.toString();
      const now = objectName(doc);
      if (now !== undefined && holding(doc, now).length === lines) return now;
      return was !== null && holding(doc, was).length === lines ? was : null;
    },
  });

  return [
    held,
    EditorState.transactionFilter.of((transaction) => {
      if (!transaction.docChanged || transaction.isUserEvent('undo') || transaction.isUserEvent('redo')) {
        return transaction;
      }
      const was = transaction.startState.field(held);
      if (was === null) return transaction;
      const doc = transaction.newDoc.toString();
      const now = objectName(doc);
      if (now === undefined || now === was) return transaction;
      const spots = holding(doc, was);
      if (spots.length !== lines) return transaction;
      return [
        transaction,
        { changes: spots.map((spot) => ({ ...spot, insert: now })), sequential: true },
      ];
    },),
  ];
}

/** A label's value holds at most this many characters. */
const LABEL_MAX = 63;
/** What a name may be while it is typed: `web-` is on its way to `web-1`. A
 *  quoted name, or one with a comment after it, is left to the person. */
const PLAIN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const LINE = /^ *(?:app|serviceName): *(\S+) *$/gm;

/** Where the lines holding `name` hold it. */
function holding(doc: string, name: string): { from: number; to: number }[] {
  const out: { from: number; to: number }[] = [];
  for (const match of doc.matchAll(LINE)) {
    if (match[1] !== name) continue;
    const to = match.index + match[0].trimEnd().length;
    out.push({ from: to - name.length, to });
  }
  return out;
}

/** `metadata.name`, from the top-level `metadata:` block's own depth, so a
 *  deeper `name:` under it is not taken for it. Undefined where it is empty or
 *  not plain: nothing to follow yet. */
export function objectName(doc: string): string | undefined {
  const lines = doc.split('\n');
  const start = lines.findIndex((line) => /^metadata: *$/.test(line));
  if (start < 0) return undefined;
  let depth: number | undefined;
  for (const line of lines.slice(start + 1)) {
    if (line.trim() === '') continue;
    const indent = /^ */.exec(line)?.[0].length ?? 0;
    if (indent === 0) return undefined;
    depth ??= indent;
    if (indent !== depth || !line.trimStart().startsWith('name:')) continue;
    const value = line.trimStart().slice('name:'.length).trim();
    return PLAIN.test(value) && value.length <= LABEL_MAX ? value : undefined;
  }
  return undefined;
}
