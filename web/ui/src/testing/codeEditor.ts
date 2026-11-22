import { EditorSelection } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { act, fireEvent, screen } from '@testing-library/react';

/**
 * Driving the manifest editor - CodeMirror 6 (`pages/object/ManifestCode.tsx`) -
 * from a test.
 *
 * **Test scaffolding only**, like `harness.tsx`: it imports CodeMirror, so a
 * page that imported it would pull the editor's chunk into its own.
 *
 * jsdom lays nothing out, and CodeMirror reads what it types from the DOM's
 * mutations and the browser's `beforeinput`, which `fireEvent` does not
 * reproduce faithfully. So text is put in by **dispatching a transaction on the
 * view**, which is what CodeMirror itself does with what it reads from a real
 * keystroke, and read back from the view's state rather than from the DOM.
 * Keys bound in the editor's keymap (Tab, Shift+Tab, Enter) are pressed as real
 * `keydown` events on the content element, because the keymap is what is under
 * test there.
 *
 * The view is found from its content element, which is the one with the
 * `textbox` role and the accessible name - the same query the textarea answered.
 */

/*
 * jsdom has no layout, and its `Range` has neither `getClientRects` nor
 * `getBoundingClientRect`. CodeMirror asks a range where a character is drawn
 * whenever it measures - the caret, the selection, a scroll into view - and
 * logs a TypeError from its measure loop when the answer is missing. An empty
 * answer is the truthful one here: nothing is drawn anywhere.
 */
if (typeof Range !== 'undefined' && !('getClientRects' in Range.prototype)) {
  Object.assign(Range.prototype, {
    getClientRects: () => document.createElement('div').getClientRects(),
    getBoundingClientRect: () => document.createElement('div').getBoundingClientRect(),
  });
}

/** The editor named `name`, once its lazily loaded chunk has drawn it. */
export async function findCode(name: string): Promise<EditorView> {
  return viewOf(await screen.findByRole('textbox', { name }));
}

/** The editor named `name`, already drawn. */
export function getCode(name: string): EditorView {
  return viewOf(screen.getByRole('textbox', { name }));
}

function viewOf(element: HTMLElement): EditorView {
  const view = EditorView.findFromDOM(element);
  if (view === null) throw new Error('the textbox is not a CodeMirror editor');
  return view;
}

/** The document as the editor holds it. */
export function textOf(view: EditorView): string {
  return view.state.doc.toString();
}

/** Replace the whole document, as a paste over everything would. */
export function typeInto(view: EditorView, next: string): void {
  act(() => {
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: next },
      userEvent: 'input.type',
    });
  });
}

/** Put the caret at `offset`. */
export function caretAt(view: EditorView, offset: number): void {
  act(() => {
    view.dispatch({ selection: EditorSelection.cursor(offset) });
  });
}

/** Select from `anchor` to `head`, as a drag or Shift+arrows would. */
export function selectRange(view: EditorView, anchor: number, head: number): void {
  act(() => {
    view.dispatch({ selection: EditorSelection.range(anchor, head) });
  });
}

/** The caret's offset in the document. */
export function caretOf(view: EditorView): number {
  return view.state.selection.main.head;
}

/** A key, pressed in the editor. */
export function press(view: EditorView, key: string, init: KeyboardEventInit = {}): void {
  act(() => {
    fireEvent.keyDown(view.contentDOM, { key, ...init });
  });
}
