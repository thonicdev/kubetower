import type { EditorView } from '@codemirror/view';
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import {
  caretAt,
  caretOf,
  findCode,
  getCode,
  press,
  selectRange,
  textOf,
  typeInto,
} from '../../testing/codeEditor';
import { renderInRouter } from '../../testing/harness';
import { ManifestEditor } from './ManifestEditor';

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const DOCUMENT = 'kind: Deployment\nspec:\n  replicas: 3\n';

const plan = {
  token: 'plan-token-1',
  expiresAt: '2099-01-01T00:00:00Z',
  facts: {
    verb: 'apply',
    targets: [{ kind: 'Deployment', namespace: 'shop', name: 'api' }],
    count: 1,
    destructive: true,
    diff: '@@ -2,1 +2,1 @@\n-  replicas: 3\n+  replicas: 5\n',
    fields: ['spec.replicas'],
    request: '{"spec":{"replicas":5},"metadata":{"resourceVersion":"310"}}',
    precondition: '310',
  },
};

interface Sent {
  path: string;
  /** The address whole, query included - a reveal names its key there. */
  url: string;
  method: string;
  body: unknown;
}

/** Records what left the process, because "it refused" and "it refused and sent
 *  nothing" are different claims and only the second is a guardrail. */
function recorder(replies: Readonly<Record<string, unknown>>): Sent[] {
  const sent: Sent[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    const path = typeof input === 'string' ? input : String(input);
    const key = path.split('?')[0] ?? path;
    sent.push({
      path: key,
      url: path,
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
    });
    const reply = replies[key];
    if (reply === undefined) {
      return Promise.resolve(
        new Response(JSON.stringify({ status: 404, title: 'Not Found', code: 'not_found' }), {
          status: 404,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
      );
    }
    return Promise.resolve(
      new Response(JSON.stringify(reply), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });
  return sent;
}

/** The editor is a dialog, and a dialog claims Escape from the one
 *  keyboard stack - so these mount inside `KeyboardStack`, which is where the
 *  application puts it too. Without it the claim is registered and nothing
 *  listens, and every Escape test below would pass by proving nothing. */
async function mount(onDone = (): void => {}, onCancel = (): void => {}): Promise<Router> {
  const { router } = renderInRouter(
    <KeyboardStack>
      <ManifestEditor
        cluster="prod"
        kind="deployments"
        namespace="shop"
        name="api"
        document={DOCUMENT}
        resourceVersion="310"
        onDone={onDone}
        onCancel={onCancel}
      />
    </KeyboardStack>,
  );
  // The editor is its own chunk: wait for it to draw.
  await findCode('api manifest');
  return router;
}

type Router = ReturnType<typeof renderInRouter>['router'];

/** The editor - CodeMirror's view, found from its named textbox. */
function editor(): EditorView {
  return getCode('api manifest');
}

/** What the editor holds. */
function text(): string {
  return textOf(editor());
}

/** The editor's own dialog, by its accessible name. There are two dialogs on
 *  screen once the review is up, and `getByRole('dialog')` would find both -
 *  which is the whole reason the confirmation is named differently. */
function editorDialog(): HTMLElement {
  return screen.getByRole('dialog', { name: 'Edit api' });
}

/** The review, by name. */
function confirmation(): Promise<HTMLElement> {
  return screen.findByRole('dialog', { name: 'Apply this manifest?' });
}

function type(next: string): void {
  typeInto(editor(), next);
}

/**
 * Where the editor is drawn.
 *
 * Replacing the read view inside the object drawer - `min(560px, 46vw)` - would
 * edit a manifest of 80 to 120 columns of YAML through a letterbox. jsdom lays nothing out, so "wider than the
 * drawer" cannot be measured here; what is asserted instead is the declared
 * size, which is the thing that would regress.
 */
describe('the host', () => {
  it('draws the document in a dialog at the viewport size, with the two controls in it', async () => {
    recorder({});
    await mount();

    const dialog = editorDialog();
    // The document, the reveal, the two controls and any refusal are all inside
    // the one box - "a dry-run refusal is drawn inside the dialog, not behind
    // it".
    expect(dialog.contains(editor().dom)).toBe(true);
    expect(dialog.contains(screen.getByRole('button', { name: 'Review changes' }))).toBe(true);
    expect(dialog.contains(screen.getByRole('button', { name: 'Cancel' }))).toBe(true);
    // `size="viewport"` and not a confirmation's `max-w-2xl`: this is the one
    // assertion that fails if the editor is put back in a narrow box.
    expect(dialog.className).toContain('max-w-[80vw]');
    expect(dialog.className).not.toContain('max-w-2xl');
  });

  it('opens with the keyboard in the document', async () => {
    recorder({});
    await mount();

    // The editor takes the keyboard when it draws (and `data-dialog-focus` on
    // its content names it for the dialog's handover). It is opened to be typed
    // in, and a dialog that opens with the focus on its Close button costs a
    // Tab before the first keystroke.
    await waitFor(() => expect(document.activeElement).toBe(editor().contentDOM));
  });

  it('closes on Escape without writing anything', async () => {
    const sent = recorder({});
    const cancelled = vi.fn();
    await mount(() => {}, cancelled);

    fireEvent.keyDown(window, { key: 'Escape' });

    expect(cancelled).toHaveBeenCalledTimes(1);
    // Escape is a way out, never a way to apply: nothing left the process.
    expect(sent).toHaveLength(0);
  });

  it('takes Escape from the review first, and leaves the editor and what was typed alone', async () => {
    const sent = recorder({ '/api/c/prod/manifest/preview': plan });
    const cancelled = vi.fn();
    await mount(() => {}, cancelled);

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));
    await confirmation();

    fireEvent.keyDown(window, { key: 'Escape' });

    // Which layer wins is a number in `keyboard/layers.ts`: the review claims
    // `KeyLayer.Confirm`, which is above `KeyLayer.Dialog`. This passing does
    // not depend on the review being rendered after the editor. The editor must
    // not be the one that closes.
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Apply this manifest?' })).toBeNull(),
    );
    expect(cancelled).not.toHaveBeenCalled();
    expect(editorDialog()).toBeTruthy();
    expect(text()).toContain('replicas: 5');
    expect(sent.some((one) => one.path.endsWith('/apply'))).toBe(false);
  });

  it('draws a dry-run refusal inside the dialog', async () => {
    vi.stubGlobal('fetch', () =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            status: 422,
            title: 'Unprocessable Entity',
            code: 'invalid',
            detail: 'the dry run refused this: spec.replicas must be an integer',
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );
    await mount();

    type(DOCUMENT.replace('replicas: 3', 'replicas: abc'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('spec.replicas must be an integer');
    // Inside the box the user is looking at. A refusal drawn behind the
    // dialog is a refusal nobody reads.
    expect(editorDialog().contains(alert)).toBe(true);
    // And the document is still there to correct.
    expect(text()).toContain('replicas: abc');
  });

  it('draws the whole of a long refusal, wrapped and scrolling, and not cut', async () => {
    const fields = Array.from(
      { length: 12 },
      (_, at) => `spec.field${String(at)}: Required value`,
    ).join(', ');
    const detail = `the cluster refused the change: HorizontalPodAutoscaler "web" is invalid: [${fields}, spec.scaleTargetRef.name: Required value]`;
    expect(detail.length).toBeGreaterThan(300);
    vi.stubGlobal('fetch', () =>
      Promise.resolve(
        new Response(
          JSON.stringify({ status: 403, title: 'Forbidden', code: 'rbac_denied', detail }),
          { status: 403, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );
    await mount();

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));

    const alert = await screen.findByRole('alert');
    // The end of it is there - the field that explains the refusal is the
    // last one named.
    expect(alert.textContent).toContain('spec.scaleTargetRef.name: Required value]');
    // Wrapped, and scrolling past a few lines, rather than cut at the edge.
    expect(alert.className).toContain('overflow-auto');
    expect(alert.className).toContain('whitespace-pre-wrap');
    expect(alert.className).toContain('wrap-anywhere');
  });
});

describe('the review', () => {
  it('is offered only once something has changed, and says so before asking', async () => {
    recorder({});
    await mount();

    expect(screen.getByRole('button', { name: 'Review changes' }).hasAttribute('disabled')).toBe(
      true,
    );
    // The button is disabled and no sentence says why.
    expect(document.body.textContent).not.toContain('Nothing has been changed');
  });

  it('sends the document and the precondition, and writes nothing', async () => {
    const sent = recorder({ '/api/c/prod/manifest/preview': plan });
    await mount();

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));

    await confirmation();
    expect(sent).toHaveLength(1);
    expect(sent[0]?.path).toBe('/api/c/prod/manifest/preview');
    expect(sent[0]?.method).toBe('POST');
    expect(sent[0]?.body).toEqual({
      document: DOCUMENT.replace('replicas: 3', 'replicas: 5'),
      resourceVersion: '310',
    });
    // Two requests and not one, and this is the half that changes nothing. A
    // single call that previewed and applied would be a confirmation the
    // user reads afterwards.
    expect(sent.some((one) => one.path.endsWith('/apply'))).toBe(false);
  });
});

describe('the confirmation', () => {
  it('shows the four things it is made of', async () => {
    recorder({ '/api/c/prod/manifest/preview': plan });
    await mount();
    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));

    const dialog = await confirmation();
    // The diff says what the document looks like afterwards.
    expect(dialog.textContent).toContain('+  replicas: 5');
    // The fields say what the request asks for, which is a different statement:
    // a diff of a reindented block is large and touches nothing.
    expect(dialog.textContent).toContain('spec.replicas');
    // The precondition, stated rather than implied - a 409 with no earlier
    // mention of a version reads as a bug rather than as the guard working.
    expect(dialog.textContent).toContain('310');
    // And the literal body, so the confirmation cannot show one thing and send
    // another.
    expect(dialog.textContent).toContain('{"spec":{"replicas":5}');
  });

  it('sends the token and never the document again', async () => {
    const sent = recorder({
      '/api/c/prod/manifest/preview': plan,
      '/api/c/prod/manifest/apply': { succeeded: 1, failed: 0, results: [] },
    });
    const done = vi.fn();
    await mount(done);

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));
    await confirmation();
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));

    await waitFor(() => expect(done).toHaveBeenCalled());
    const apply = sent.find((one) => one.path.endsWith('/apply'));
    // The token binds the act to the plan the user read, and the server
    // re-derives the facts before acting. Sending the document again would be
    // sending a second document, and the one agreed to would be the one not
    // applied.
    expect(apply?.body).toEqual({ token: 'plan-token-1' });
    expect(JSON.stringify(apply?.body)).not.toContain('replicas');
  });

  it('writes nothing when it is dismissed', async () => {
    const sent = recorder({ '/api/c/prod/manifest/preview': plan });
    await mount();

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));
    await confirmation();
    // By name, and the name is deliberately not "Cancel": the editor behind
    // the dialog has one of those and it abandons the edit instead. The
    // confirmation has no Close of its own, and no sentence about the
    // precondition.
    expect(
      within(await confirmation()).queryByRole('button', { name: 'Close' }),
    ).toBeNull();
    expect(document.body.textContent).not.toContain('Sent with');
    fireEvent.click(screen.getByRole('button', { name: 'Back' }));

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Apply this manifest?' })).toBeNull(),
    );
    // And the editor's own dialog is still there: dismissing the review is a
    // way back into the document, not a second way out of it.
    expect(editorDialog()).toBeTruthy();
    // And what was typed is still there, which is what makes it a way back
    // rather than a second way out.
    expect(text()).toContain('replicas: 5');
    expect(sent.some((one) => one.path.endsWith('/apply'))).toBe(false);
  });

  it('says what the server said when the write is refused', async () => {
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const path = typeof input === 'string' ? input : String(input);
      if (path.includes('/apply')) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              status: 409,
              title: 'Conflict',
              code: 'conflict',
              detail: 'this deployment changed since you opened it',
            }),
            { status: 409, headers: { 'Content-Type': 'application/problem+json' } },
          ),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify(plan), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    await mount();

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));
    await confirmation();
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));

    // The server's own sentence, not one composed here. A 409 is the precondition
    // doing its job, and the user has to be told which object moved.
    await waitFor(() =>
      expect(document.body.textContent).toContain('this deployment changed since you opened it'),
    );
    // The dialog stays open: a refusal the user can act on is not a reason
    // to throw away what they typed.
    expect(screen.queryByRole('dialog', { name: 'Apply this manifest?' })).not.toBeNull();
  });
});

/**
 * The editor itself: CodeMirror, loaded on demand, with
 * the read view's colors and a gutter.
 */
describe('the editor', () => {
  /** The color a CSS rule gives `element` through one of its classes, read
   *  from the stylesheets CodeMirror injected - jsdom lays nothing out, but it
   *  does hold the rules. */
  function colourOf(element: Element): string | undefined {
    const classes = [...element.classList];
    for (const sheet of [...document.styleSheets, ...(document.adoptedStyleSheets ?? [])]) {
      for (const rule of [...sheet.cssRules]) {
        if (!(rule instanceof CSSStyleRule)) continue;
        if (classes.some((name) => rule.selectorText.split(/[\s,]+/).includes(`.${name}`))) {
          const colour = rule.style.getPropertyValue('color');
          if (colour !== '') return colour;
        }
      }
    }
    return undefined;
  }

  /** The innermost element holding exactly `text` on the editor's lines. */
  function token(text: string): Element {
    // The last in document order: a mark nested in another is found after the
    // one around it, and the innermost is the one whose color is drawn.
    const found = [...editor().contentDOM.querySelectorAll('.cm-line span')]
      .filter((span) => span.textContent === text)
      .pop();
    if (found === undefined) throw new Error(`no token "${text}" in the editor`);
    return found;
  }

  it('colors the YAML with the read view\'s theme tokens', async () => {
    recorder({});
    await mount();

    // A key in the accent, as `ManifestLines` draws a `key` token.
    expect(colourOf(token('kind'))).toBe('var(--color-accent)');
    expect(colourOf(token('replicas'))).toBe('var(--color-accent)');
    // A number in `warn`, as the server's `number`; a plain word uncolored.
    expect(token('3').classList.contains('cm-yaml-scalar')).toBe(true);
    expect(colourOf(token('3'))).toBe('var(--color-warn)');
    expect(() => token('Deployment')).toThrow();
  });

  it('draws punctuation as the read view does: a key\'s colon in the text color, a dash muted', async () => {
    recorder({});
    await mount();
    type('kind: Deployment\nitems:\n  - one\n');

    // The server's tokenizer writes the colon after a key as `plain`, which
    // `ManifestLines` draws in the text color; a sequence dash is
    // `punctuation`, which it draws muted.
    const colons = [...editor().contentDOM.querySelectorAll('.cm-line')].map((line) => {
      const walker = document.createTreeWalker(line, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
        if (node.textContent?.startsWith(':') === true) return node.parentElement;
      }
      return null;
    });
    const colon = colons.find((element) => element !== null);
    expect(colon).toBeTruthy();
    const drawn = colon === null || colon === undefined ? undefined : colourOf(colon);
    expect(drawn === undefined || drawn === 'var(--color-fg)').toBe(true);
    expect(colourOf(token('-'))).toBe('var(--color-muted)');
  });

  it('numbers its lines, outside the text', async () => {
    recorder({});
    await mount();

    const gutter = editorDialog().querySelector('.cm-lineNumbers');
    // Less the hidden spacer CodeMirror sizes the gutter with.
    const numbers = [...(gutter?.querySelectorAll<HTMLElement>('.cm-gutterElement') ?? [])]
      .filter((element) => element.style.visibility !== 'hidden')
      .map((element) => element.textContent);
    // Four: the document's trailing newline opens an empty fourth line.
    expect(numbers).toEqual(['1', '2', '3', '4']);
    // The gutter is not the document: what is edited and sent has no numbers.
    expect(editor().contentDOM.contains(gutter)).toBe(false);
    expect(text()).toBe(DOCUMENT);
  });

  it('gives Escape to the dialog even with the keyboard in the editor', async () => {
    const sent = recorder({});
    const cancelled = vi.fn();
    await mount(() => {}, cancelled);

    press(editor(), 'Escape');

    expect(cancelled).toHaveBeenCalledTimes(1);
    expect(sent).toHaveLength(0);
  });
});

describe('the three keys', () => {
  it('indents with spaces and never with a tab', async () => {
    recorder({});
    await mount();

    caretAt(editor(), 0);
    press(editor(), 'Tab');

    // A tab in a YAML document is a parse error, so an editor that inserted one
    // on the key named Tab would break documents for the reason it was added.
    expect(text().startsWith('  kind:')).toBe(true);
    expect(text()).not.toContain('\t');
  });

  it('outdents on shift, and does nothing where there is no indentation to take', async () => {
    recorder({});
    await mount();

    type('  kind: Deployment\n');
    caretAt(editor(), 4);
    press(editor(), 'Tab', { shiftKey: true });
    expect(text().startsWith('kind:')).toBe(true);

    // Again, with nothing left to remove. Not an error and not a line that
    // loses its first two characters.
    caretAt(editor(), 2);
    press(editor(), 'Tab', { shiftKey: true });
    expect(text().startsWith('kind:')).toBe(true);
  });

  it('indents every line a selection touches, and outdents them all back', async () => {
    recorder({});
    await mount();

    const doc = 'spec:\n  template:\n    spec:\n      containers:\n        - name: api\nkind: Deployment\n';
    type(doc);
    // From inside line 2 to inside line 5: every line it touches moves, and
    // none of them is replaced - a Tab over a selection used to put two spaces
    // in place of the four lines.
    const from = doc.indexOf('template');
    const to = doc.indexOf('name: api') + 2;
    selectRange(editor(), from, to);
    press(editor(), 'Tab');

    const indented =
      'spec:\n    template:\n      spec:\n        containers:\n          - name: api\nkind: Deployment\n';
    expect(text()).toBe(indented);
    // The selection is over the same text, so a second Tab moves it again.
    const selection = editor().state.selection.main;
    expect(editor().state.sliceDoc(selection.from, selection.to)).toBe(
      'template:\n      spec:\n        containers:\n          - na',
    );

    press(editor(), 'Tab', { shiftKey: true });
    expect(text()).toBe(doc);
    // Every line kept, the selection still over them.
    const back = editor().state.selection.main;
    expect(editor().state.sliceDoc(back.from, back.to)).toBe(
      'template:\n    spec:\n      containers:\n        - na',
    );

    // Shift+Tab takes up to two from each line, and leaves a line with less
    // alone rather than eating into its text.
    selectRange(editor(), 0, doc.indexOf('template'));
    press(editor(), 'Tab', { shiftKey: true });
    expect(text()).toBe('spec:\ntemplate:\n    spec:\n      containers:\n        - name: api\nkind: Deployment\n');
  });

  it('keeps the indentation on Enter', async () => {
    recorder({});
    await mount();

    type('spec:\n  replicas: 3');
    caretAt(editor(), text().length);
    press(editor(), 'Enter');

    // Editing a nested block without retyping its depth on every line, in the
    // one document format where getting that wrong changes the meaning.
    expect(text().endsWith('\n  ')).toBe(true);
  });

  it('keeps the depth of the line on Enter, and does not guess a deeper one', async () => {
    recorder({});
    await mount();

    // After a list item, CodeMirror's own Enter would line the new line up
    // with the item's content, two columns past the dash. The textarea kept
    // the line's own depth, and so does this editor: what Enter does to
    // indentation is the operator's to see, in the one format where it
    // changes the meaning (the three keys are kept as they were).
    const doc = 'spec:\n  containers:\n    - name: api';
    type(doc);
    caretAt(editor(), doc.length);
    press(editor(), 'Enter');

    expect(text()).toBe(`${doc}\n    `);
  });

  it.each([
    ['a key with no value', 'spec:', '  '],
    ['a nested key', 'spec:\n  template:', '    '],
    ['a key and a comment', 'spec:  # the pod', '  '],
    ['a list item that is a key', 'spec:\n  - name:', '      '],
    ['a quoted key', '"a b":', '  '],
  ])('goes one level deeper on Enter after %s', async (_what, doc, depth) => {
    recorder({});
    await mount();
    type(doc);
    caretAt(editor(), doc.length);
    press(editor(), 'Enter');
    expect(text()).toBe(`${doc}\n${depth}`);
  });

  it.each([
    ['a value', 'spec:\n  replicas: 3', '  '],
    ['a value ending in a colon', 'image: nginx:', ''],
    ['a comment', '# spec:', ''],
    ['a block scalar\'s text', 'data: |\n  key:', '  '],
  ])('keeps the depth on Enter after %s', async (_what, doc, depth) => {
    recorder({});
    await mount();
    type(doc);
    caretAt(editor(), doc.length);
    press(editor(), 'Enter');
    expect(text()).toBe(`${doc}\n${depth}`);
  });
});

/**
 * A Secret's values come through the one door, and go back out as the mask.
 *
 * The document arrives with `<masked>` over
 * every value (`detail.Trimmed`), and `internal/edit` diffs the edited document
 * against that same masked form. So: the values a user needs to edit are
 * fetched through `revealEnvValue`, one request per key - which is one line on
 * the record per key, the same audit line *Show* on the Data tab writes - and a
 * value they leave as it came back is sent as its mask again, so only what they
 * changed enters the patch.
 */
describe('a masked secret', () => {
  const MASKED = [
    // The note `detail.manifestOf` writes over a masked document.
    '# The values below are masked. Editing anything else here is safe: a masked',
    '# value is compared against the same mask, so it cannot be written back over the',
    '# real one. To change a value, type the new base64 over its mask.',
    'apiVersion: v1',
    'data:',
    '  password: <masked>',
    '  username: <masked>',
    'kind: Secret',
    'metadata:',
    '  name: db',
    '  namespace: shop',
    'type: Opaque',
    '',
  ].join('\n');

  /** The door, answering by key: `recorder` keys on the path alone, and the
   *  two reveals share one. A key not in `values` is refused. */
  function door(values: Readonly<Record<string, string>>): Sent[] {
    const sent = recorder({ '/api/c/prod/manifest/preview': plan });
    const fallback = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : String(input);
      if (url.startsWith('/api/c/prod/env?')) {
        sent.push({ path: '/api/c/prod/env', url, method: 'GET', body: undefined });
        const key = new URLSearchParams(url.split('?')[1]).get('key') ?? '';
        const value = values[key];
        const body =
          value === undefined
            ? { state: 'forbidden', reason: { code: 'verb_denied', message: `${key} is forbidden` } }
            : { state: 'ok', data: { key, value, source: 'Secret' } };
        return Promise.resolve(
          new Response(JSON.stringify(body), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      return fallback(input, init);
    });
    return sent;
  }

  async function mountSecret(maskedKeys: readonly string[] = ['password', 'username']): Promise<void> {
    renderInRouter(
      <KeyboardStack>
        <ManifestEditor
          cluster="prod"
          kind="secrets"
          namespace="shop"
          name="db"
          document={MASKED}
          resourceVersion="44"
          maskedKeys={maskedKeys}
          onDone={() => {}}
          onCancel={() => {}}
        />
      </KeyboardStack>,
    );
    await findCode('db manifest');
  }

  function secretEditor(): EditorView {
    return getCode('db manifest');
  }

  function secretText(): string {
    return textOf(secretEditor());
  }

  function keyOf(one: Sent): string | null {
    return new URLSearchParams(one.url.split('?')[1]).get('key');
  }

  it('offers a reveal, and says what it costs, only when something is masked', async () => {
    door({});
    await mountSecret();
    const reveal = screen.getByRole('button', { name: 'Reveal 2 values' });
    expect(reveal).toBeTruthy();
    // The door is inside the dialog with the editor and it is the
    // same door: one control, inside the box that holds the document it opens.
    expect(screen.getByRole('dialog', { name: 'Edit db' }).contains(reveal)).toBe(true);
    // Said before the click: one request per key, each on the record.
    expect(document.body.textContent).toContain('one request per key');

    cleanup();
    door({});
    await mountSecret([]);
    expect(screen.queryByRole('button', { name: /Reveal/ })).toBeNull();
  });

  it('reveals through the one door, one request per key, and never a second way', async () => {
    const sent = door({ password: 'hunter2', username: 'admin' });
    await mountSecret();

    fireEvent.click(screen.getByRole('button', { name: 'Reveal 2 values' }));

    await waitFor(() => expect(secretText()).toContain('aHVudGVyMg=='));
    // The audit line per key: two requests to the door, each naming its key
    // in the query and never a value. That is the record asked for, and
    // it is written by the same route *Show* on the Data tab uses.
    const reveals = sent.filter((one) => one.path === '/api/c/prod/env');
    expect(reveals).toHaveLength(2);
    expect(reveals.map(keyOf).sort()).toEqual(['password', 'username']);
    expect(
      reveals.every((one) => one.url.includes('kind=secrets') && one.url.includes('name=db')),
    ).toBe(true);
    // Nothing else was asked for - no per-manifest reveal, no second door.
    expect(sent.filter((one) => one.path !== '/api/c/prod/env')).toHaveLength(0);

    // Both values are in the document, quoted, and the masks are gone.
    expect(secretText()).toContain('  password: "aHVudGVyMg=="');
    expect(secretText()).toContain('  username: "YWRtaW4="');
    expect(secretText()).not.toContain('<masked>');
    // And the editor does not count looking as changing.
    expect(
      screen.getByRole('button', { name: 'Review changes' }).hasAttribute('disabled'),
    ).toBe(true);
  });

  it('sends a value the operator only looked at as its mask, and only a changed one as typed', async () => {
    const sent = door({ password: 'hunter2', username: 'admin' });
    await mountSecret();
    fireEvent.click(screen.getByRole('button', { name: 'Reveal 2 values' }));
    await waitFor(() => expect(secretText()).toContain('YWRtaW4='));

    typeInto(secretEditor(), secretText().replace('"aHVudGVyMg=="', '"bmV3LXBhc3N3b3Jk"'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));
    await confirmation();

    const preview = sent.find((one) => one.path === '/api/c/prod/manifest/preview');
    const body = preview?.body as { document: string; resourceVersion: string };
    // `internal/edit` diffs against the masked form. The untouched value goes
    // back as the mask - equal on both sides, out of the patch - and the
    // revealed base64 of a key nobody changed never leaves the browser.
    expect(body.document).toContain('  username: <masked>');
    expect(body.document).not.toContain('YWRtaW4=');
    expect(body.document).toContain('  password: "bmV3LXBhc3N3b3Jk"');
    expect(body.resourceVersion).toBe('44');
  });

  it('drops the note that the values are masked once they are all shown', async () => {
    const sent = door({ password: 'hunter2', username: 'admin' });
    await mountSecret();
    expect(secretText()).toContain('masked');

    fireEvent.click(screen.getByRole('button', { name: 'Reveal 2 values' }));
    await waitFor(() => expect(secretText()).toContain('YWRtaW4='));

    // Nothing in the document says "masked" any more: it would be untrue.
    expect(secretText()).not.toMatch(/masked/i);
    expect(secretText().startsWith('apiVersion: v1')).toBe(true);
    // Removing the note is not an edit: nothing to review, nothing counted,
    // and closing does not ask.
    expect(
      screen.getByRole('button', { name: 'Review changes' }).hasAttribute('disabled'),
    ).toBe(true);
    expect(document.querySelector('[data-manifest-changes]')).toBeNull();

    // And a change made after it is counted on the editor's own lines.
    typeInto(secretEditor(), secretText().replace('type: Opaque', 'type: Other'));
    expect(document.querySelector('[data-manifest-changes]')?.textContent).toContain('1 changed line');
    fireEvent.click(screen.getByRole('button', { name: 'Next change' }));
    expect(secretEditor().state.doc.lineAt(caretOf(secretEditor())).text).toBe('type: Other');
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));
    await confirmation();
    const preview = sent.find((one) => one.path === '/api/c/prod/manifest/preview');
    const body = preview?.body as { document: string };
    expect(body.document).toContain('  username: <masked>');
    expect(body.document).toContain('type: Other');
  });

  it('keeps the note while a value the door refused is still masked', async () => {
    door({ password: 'hunter2' });
    await mountSecret();
    fireEvent.click(screen.getByRole('button', { name: 'Reveal 2 values' }));
    await waitFor(() => expect(secretText()).toContain('aHVudGVyMg=='));
    expect(secretText().startsWith('# The values below are masked.')).toBe(true);
  });

  it('keeps the mask on a key the door refused, and says which', async () => {
    door({ password: 'hunter2' });
    await mountSecret();
    fireEvent.click(screen.getByRole('button', { name: 'Reveal 2 values' }));

    await waitFor(() => expect(secretText()).toContain('aHVudGVyMg=='));
    // The refusal is the server's sentence, beside the key it is about; the
    // key it is about is still masked and still editable by typing over it.
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('username is forbidden');
    expect(secretText()).toContain('  username: <masked>');
  });

  it('counts changes on the document as it would be sent, so a reveal is not one', async () => {
    door({ password: 'hunter2', username: 'admin' });
    await mountSecret();
    const changes = (): Element | null => document.querySelector('[data-manifest-changes]');

    fireEvent.click(screen.getByRole('button', { name: 'Reveal 2 values' }));
    await waitFor(() => expect(secretText()).toContain('YWRtaW4='));
    // Two lines of the text area differ from the document opened, and neither
    // would leave as anything but its mask: nothing to count.
    expect(changes()).toBeNull();

    typeInto(secretEditor(), secretText().replace('"aHVudGVyMg=="', '"bmV3LXBhc3N3b3Jk"'));
    // The one value changed, and not the revealed one beside it.
    expect(changes()?.textContent).toContain('1 changed line');
    expect(changes()?.textContent).toContain('1/1');
  });
});

describe('the changes', () => {
  const LONG = [
    'apiVersion: apps/v1',
    'kind: Deployment',
    'metadata:',
    '  name: api',
    '  namespace: shop',
    'spec:',
    '  replicas: 3',
    '  template:',
    '    spec:',
    '      containers:',
    '        - name: api',
    '          image: api:1',
    '',
  ].join('\n');

  async function mountLong(): Promise<void> {
    renderInRouter(
      <KeyboardStack>
        <ManifestEditor
          cluster="prod"
          kind="deployments"
          namespace="shop"
          name="api"
          document={LONG}
          resourceVersion="310"
          onDone={() => {}}
          onCancel={() => {}}
        />
      </KeyboardStack>,
    );
    await findCode('api manifest');
  }

  function changes(): HTMLElement | null {
    return document.querySelector('[data-manifest-changes]');
  }

  /** The line the caret is on, from 0. */
  function caretLine(): number {
    const view = editor();
    return view.state.doc.lineAt(caretOf(view)).number - 1;
  }

  it('shows nothing while the document is as it was opened', async () => {
    recorder({});
    await mountLong();
    expect(changes()).toBeNull();
    expect(screen.queryByRole('button', { name: 'Next change' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Previous change' })).toBeNull();
  });

  it('counts three separate edits, and steps through them with the arrows', async () => {
    recorder({});
    await mountLong();

    type(
      LONG.replace('namespace: shop', 'namespace: shop-2')
        .replace('replicas: 3', 'replicas: 5')
        .replace('image: api:1', 'image: api:2'),
    );

    const bar = changes();
    expect(bar?.textContent).toContain('3 changed lines');
    expect(bar?.textContent).toContain('1/3');
    // Inside the editor's dialog, not floating somewhere else.
    expect(editorDialog().contains(bar)).toBe(true);

    fireEvent.click(screen.getByRole('button', { name: 'Next change' }));
    expect(changes()?.textContent).toContain('2/3');
    // The caret is on the second change - `replicas`, line 6 - and the area
    // has the keyboard, so the caret is where the user types next.
    expect(caretLine()).toBe(6);
    expect(document.activeElement).toBe(editor().contentDOM);

    fireEvent.click(screen.getByRole('button', { name: 'Next change' }));
    expect(changes()?.textContent).toContain('3/3');
    expect(caretLine()).toBe(11);

    fireEvent.click(screen.getByRole('button', { name: 'Previous change' }));
    expect(changes()?.textContent).toContain('2/3');
    expect(caretLine()).toBe(6);

    // Wraps: ↓ from the last goes to the first.
    fireEvent.click(screen.getByRole('button', { name: 'Next change' }));
    fireEvent.click(screen.getByRole('button', { name: 'Next change' }));
    expect(changes()?.textContent).toContain('1/3');
    expect(caretLine()).toBe(4);
  });

  it('says one line in the singular, and counts a block edited together as one change', async () => {
    recorder({});
    await mountLong();

    type(LONG.replace('replicas: 3', 'replicas: 4'));
    expect(changes()?.textContent).toContain('1 changed line');
    expect(changes()?.textContent).not.toContain('lines');
    expect(changes()?.textContent).toContain('1/1');

    type(LONG.replace('  name: api\n  namespace: shop', '  name: api-2\n  namespace: shop-2'));
    expect(changes()?.textContent).toContain('2 changed lines');
    expect(changes()?.textContent).toContain('1/1');
  });

  it('keeps the counter inside the changes left, and goes away when the edit is undone', async () => {
    recorder({});
    await mountLong();

    type(LONG.replace('namespace: shop', 'namespace: x').replace('image: api:1', 'image: x'));
    fireEvent.click(screen.getByRole('button', { name: 'Next change' }));
    expect(changes()?.textContent).toContain('2/2');

    // The second change is typed away: the counter falls back to the one left.
    type(LONG.replace('namespace: shop', 'namespace: x'));
    expect(changes()?.textContent).toContain('1/1');

    type(LONG);
    expect(changes()).toBeNull();
  });

  /** A document of `size` lines, mounted as the original. */
  async function mountLines(size: number): Promise<string[]> {
    const lines = Array.from({ length: size }, (_, at) => `  key${String(at)}: value`);
    renderInRouter(
      <KeyboardStack>
        <ManifestEditor
          cluster="prod"
          kind="deployments"
          namespace="shop"
          name="api"
          document={lines.join('\n')}
          resourceVersion="310"
          onDone={() => {}}
          onCancel={() => {}}
        />
      </KeyboardStack>,
    );
    await findCode('api manifest');
    return lines;
  }

  it('counts thirty edits scattered through 10,000 lines, and ↓ reaches every one', async () => {
    recorder({});
    const lines = await mountLines(10_000);
    const edited = Array.from({ length: 30 }, (_, at) => 150 + at * 330);
    const after = [...lines];
    for (const at of edited) after[at] = `  key${String(at)}: changed`;
    // Each found once: a role query walks the computed style of the whole
    // dialog, and the editor's injected stylesheet makes sixty of those the
    // slowest thing in the test. The button and the view outlive every step.
    const view = editor();
    typeInto(view, after.join('\n'));

    expect(changes()?.textContent).toContain('30 changed lines');
    expect(changes()?.textContent).toContain('1/30');
    const next = screen.getByRole('button', { name: 'Next change' });
    const reached: number[] = [];
    for (let step = 0; step < 30; step++) {
      fireEvent.click(next);
      reached.push(view.state.doc.lineAt(caretOf(view)).number - 1);
    }
    // ↓ from 1/30 is the second change; thirty steps come back round to the first.
    expect(reached).toEqual([...edited.slice(1), edited[0]]);
    expect(changes()?.textContent).toContain('1/30');
    // Fifteen seconds rather than five. jsdom pays about a second for
    // CodeMirror to take a 10,000-line document in place of another - half of
    // it the line-number gutter - which a browser does in milliseconds; the
    // run is about three seconds alone and is slower under a full suite.
  }, 15_000);

  it('says it did not count a rewrite too large to compare, and offers no arrows', async () => {
    recorder({});
    const lines = await mountLines(1500);
    type(lines.map((line) => line.replace('value', 'other')).join('\n'));

    expect(changes()?.textContent).toBe('Too many changed lines to count');
    expect(screen.queryByRole('button', { name: 'Next change' })).toBeNull();
  });
});

describe('closing an edited document', () => {
  function question(): HTMLElement | null {
    return screen.queryByRole('dialog', { name: 'Discard your changes?' });
  }

  it('asks before Escape throws the edit away, keeps editing, then discards', async () => {
    const sent = recorder({});
    const cancelled = vi.fn();
    await mount(() => {}, cancelled);

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.keyDown(window, { key: 'Escape' });

    const asked = await screen.findByRole('dialog', { name: 'Discard your changes?' });
    expect(cancelled).not.toHaveBeenCalled();
    // The way back first in the keyboard: a reflex Enter keeps the work.
    await waitFor(() =>
      expect(document.activeElement).toBe(within(asked).getByRole('button', { name: 'Keep editing' })),
    );
    fireEvent.click(within(asked).getByRole('button', { name: 'Keep editing' }));

    await waitFor(() => expect(question()).toBeNull());
    expect(cancelled).not.toHaveBeenCalled();
    expect(text()).toContain('replicas: 5');

    // Escape on the question is "keep editing" too: the key that asked does not
    // also answer.
    fireEvent.keyDown(window, { key: 'Escape' });
    await screen.findByRole('dialog', { name: 'Discard your changes?' });
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => expect(question()).toBeNull());
    expect(cancelled).not.toHaveBeenCalled();

    fireEvent.keyDown(window, { key: 'Escape' });
    fireEvent.click(
      within(await screen.findByRole('dialog', { name: 'Discard your changes?' })).getByRole(
        'button',
        { name: 'Discard' },
      ),
    );
    expect(cancelled).toHaveBeenCalledTimes(1);
    expect(sent).toHaveLength(0);
  });

  it('asks the same from the X and from Cancel', async () => {
    recorder({});
    const cancelled = vi.fn();
    await mount(() => {}, cancelled);
    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));

    fireEvent.click(within(editorDialog()).getByRole('button', { name: 'Close' }));
    fireEvent.click(
      within(await screen.findByRole('dialog', { name: 'Discard your changes?' })).getByRole(
        'button',
        { name: 'Keep editing' },
      ),
    );
    await waitFor(() => expect(question()).toBeNull());

    fireEvent.click(within(editorDialog()).getByRole('button', { name: 'Cancel' }));
    await screen.findByRole('dialog', { name: 'Discard your changes?' });
    expect(cancelled).not.toHaveBeenCalled();
  });

  it('closes an unchanged document at once, and one typed back to how it was', async () => {
    recorder({});
    const cancelled = vi.fn();
    await mount(() => {}, cancelled);

    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    type(DOCUMENT);
    fireEvent.click(within(editorDialog()).getByRole('button', { name: 'Cancel' }));

    expect(question()).toBeNull();
    expect(cancelled).toHaveBeenCalledTimes(1);
  });
});

/**
 * Leaving the page is closing the editor too: another address in the console is
 * held and asks, and the page itself asks the browser to.
 */
describe('leaving the page with an edited document', () => {
  function question(): HTMLElement | null {
    return screen.queryByRole('dialog', { name: 'Discard your changes?' });
  }
  /** What the browser does before a reload or a closed tab: true when the page
   *  asked it to ask. */
  function unloadAsks(): boolean {
    const event = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(event);
    return event.defaultPrevented;
  }

  it('holds a move to another address and asks; Keep editing stays, Discard goes', async () => {
    const sent = recorder({});
    const cancelled = vi.fn();
    const router = await mount(() => {}, cancelled);
    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));

    await act(async () => {
      await router.navigate('/c/prod/pods');
    });
    const asked = await screen.findByRole('dialog', { name: 'Discard your changes?' });
    expect(router.state.location.pathname).toBe('/');
    fireEvent.click(within(asked).getByRole('button', { name: 'Keep editing' }));
    await waitFor(() => expect(question()).toBeNull());
    expect(router.state.location.pathname).toBe('/');
    expect(text()).toContain('replicas: 5');

    // Asked again: Discard goes on to where the move was going.
    await act(async () => {
      await router.navigate('/c/prod/pods');
    });
    fireEvent.click(
      within(await screen.findByRole('dialog', { name: 'Discard your changes?' })).getByRole('button', {
        name: 'Discard',
      }),
    );
    await waitFor(() => expect(router.state.location.pathname).toBe('/c/prod/pods'));
    expect(sent).toHaveLength(0);
  });

  it('lets a document as it was opened leave without asking', async () => {
    recorder({});
    const router = await mount();
    expect(unloadAsks()).toBe(false);

    await act(async () => {
      await router.navigate('/c/prod/pods');
    });
    expect(router.state.location.pathname).toBe('/c/prod/pods');
    expect(question()).toBeNull();
  });

  it('asks the browser before a reload while edited, and not once typed back', async () => {
    recorder({});
    await mount();
    expect(unloadAsks()).toBe(false);
    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    expect(unloadAsks()).toBe(true);
    type(DOCUMENT);
    expect(unloadAsks()).toBe(false);
  });
});

describe('creating', () => {
  const TEMPLATE = 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\n  namespace: qa-a\n';

  async function mountCreate(onCancel = (): void => {}): Promise<void> {
    renderInRouter(
      <KeyboardStack>
        <ManifestEditor
          cluster="prod"
          kind="configmaps"
          namespace="qa-a"
          document={TEMPLATE}
          create={{ kind: 'ConfigMap' }}
          onDone={() => {}}
          onCancel={onCancel}
        />
      </KeyboardStack>,
    );
    await findCode('New ConfigMap manifest');
  }

  it('names the kind and the namespace of the document in the confirmation, not the list\'s', async () => {
    recorder({
      '/api/c/prod/create/preview': {
        ...plan,
        facts: {
          ...plan.facts,
          verb: 'create',
          destructive: false,
          targets: [{ kind: 'Secret', namespace: 'qa-b', name: 'sneaky' }],
        },
      },
    });
    await mountCreate();

    typeInto(
      getCode('New ConfigMap manifest'),
      'apiVersion: v1\nkind: Secret\nmetadata:\n  name: sneaky\n  namespace: qa-b\n',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Review' }));

    expect(await screen.findByRole('dialog', { name: 'Create this Secret in qa-b?' })).toBeTruthy();
    expect(screen.queryByRole('dialog', { name: /ConfigMap\?/ })).toBeNull();
  });

  it('closes an untouched template at once, and asks once it is edited', async () => {
    recorder({});
    const cancelled = vi.fn();
    await mountCreate(cancelled);

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(cancelled).toHaveBeenCalledTimes(1);

    cleanup();
    resetKeyboardStack();
    const second = vi.fn();
    await mountCreate(second);
    typeInto(getCode('New ConfigMap manifest'), `${TEMPLATE}data:\n  a: b\n`);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(await screen.findByRole('dialog', { name: 'Discard your changes?' })).toBeTruthy();
    expect(second).not.toHaveBeenCalled();
  });
});

describe('the confirmation\'s foot', () => {
  it('puts the act first and the way back after it, as every other dialog does', async () => {
    recorder({ '/api/c/prod/manifest/preview': plan });
    await mount();
    type(DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.click(screen.getByRole('button', { name: 'Review changes' }));

    const names = within(await confirmation())
      .getAllByRole('button')
      .map((button) => button.textContent);
    expect(names.indexOf('Apply')).toBeGreaterThanOrEqual(0);
    expect(names.indexOf('Apply')).toBeLessThan(names.indexOf('Back'));
    // The editor's own foot, the same way round.
    const foot = within(editorDialog())
      .getAllByRole('button')
      .map((button) => button.textContent);
    expect(foot.indexOf('Review changes')).toBeLessThan(foot.indexOf('Cancel'));
  });
});

/**
 * The tab's search, over the editor's document: the same bar, the same button
 * and the same Ctrl+F, so a manifest is searched one way whether it is read or
 * edited.
 */
describe('the search', () => {
  function box(): HTMLInputElement {
    return within(editorDialog()).getByLabelText('Find in this manifest') as HTMLInputElement;
  }

  /** The `n/m` beside the box. */
  function count(): string {
    return (
      within(editorDialog()).getByLabelText('Next match').parentElement?.textContent?.replace(/[^\d/]/g, '') ??
      ''
    );
  }

  /** The document's element, as a browser has it: a field that takes typing.
   *  jsdom does not compute `isContentEditable`, and without it a Ctrl+F there
   *  would pass for one pressed on plain text. */
  function editable(): HTMLElement {
    const content = editor().contentDOM;
    Object.defineProperty(content, 'isContentEditable', { configurable: true, value: true });
    return content;
  }

  it('has the tab’s search control, which opens the bar with the keyboard in it', async () => {
    recorder({});
    await mount();

    fireEvent.click(within(editorDialog()).getByRole('button', { name: 'Search this manifest' }));
    expect(document.activeElement).toBe(box());
  });

  it('opens on Ctrl+F with the keyboard in the document, and not the browser’s', async () => {
    recorder({});
    await mount();

    let open = true;
    act(() => {
      open = fireEvent.keyDown(editable(), { key: 'f', ctrlKey: true });
    });
    // Taken from the browser: the default was prevented.
    expect(open).toBe(false);
    expect(document.activeElement).toBe(box());
  });

  it('counts the matches, marks them in the document, and steps through them', async () => {
    recorder({});
    await mount();

    fireEvent.keyDown(editable(), { key: 'f', ctrlKey: true });
    // Case-insensitive unless asked, as in the tab.
    fireEvent.change(box(), { target: { value: 'P' } });
    await waitFor(() => expect(count()).toBe('1/3'));
    const content = editor().contentDOM;
    await waitFor(() => expect(content.querySelectorAll('[data-find-hit]')).toHaveLength(3));
    expect(content.querySelectorAll('[data-find-current]')).toHaveLength(1);
    expect(content.querySelector('[data-find-current]')?.closest('.cm-line')?.textContent).toBe(
      'kind: Deployment',
    );

    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('2/3');
    await waitFor(() =>
      expect(content.querySelector('[data-find-current]')?.closest('.cm-line')?.textContent).toBe('spec:'),
    );
    fireEvent.keyDown(box(), { key: 'Enter', shiftKey: true });
    expect(count()).toBe('1/3');
    // The document is searched, never changed.
    expect(text()).toBe(DOCUMENT);
  });

  it('follows the document as it is typed into', async () => {
    recorder({});
    await mount();

    press(editor(), 'f', { ctrlKey: true });
    fireEvent.change(box(), { target: { value: 'replicas' } });
    await waitFor(() => expect(count()).toBe('1/1'));
    typeInto(editor(), `${DOCUMENT}  # replicas\n`);
    await waitFor(() => expect(count()).toBe('1/2'));
  });

  it('leaves Ctrl+F alone while the question over the editor is up', async () => {
    recorder({});
    await mount();

    typeInto(editor(), DOCUMENT.replace('replicas: 3', 'replicas: 5'));
    fireEvent.keyDown(window, { key: 'Escape' });
    const asked = await screen.findByRole('dialog', { name: 'Discard your changes?' });

    fireEvent.keyDown(asked, { key: 'f', ctrlKey: true });
    expect(screen.queryByLabelText('Find in this manifest')).toBeNull();
  });

  it('takes Escape for the bar first and the dialog second', async () => {
    recorder({});
    const cancelled = vi.fn();
    await mount(() => {}, cancelled);

    press(editor(), 'f', { ctrlKey: true });
    fireEvent.change(box(), { target: { value: 'kind' } });
    await waitFor(() => expect(editor().contentDOM.querySelectorAll('[data-find-hit]')).toHaveLength(1));

    fireEvent.keyDown(box(), { key: 'Escape' });
    expect(within(editorDialog()).queryByLabelText('Find in this manifest')).toBeNull();
    expect(editor().contentDOM.querySelector('[data-find-hit]')).toBeNull();
    expect(cancelled).not.toHaveBeenCalled();
    // The keyboard is back in the document it came from.
    expect(editor().contentDOM.contains(document.activeElement)).toBe(true);

    press(editor(), 'Escape');
    expect(cancelled).toHaveBeenCalledTimes(1);
  });
});
