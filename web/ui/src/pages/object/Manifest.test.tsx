import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import { renderInRouter, stubFetch, type StubRoutes } from '../../testing/harness';
import { Manifest } from './Manifest';

afterEach(() => {
  vi.unstubAllGlobals();
  // The editor is a dialog and a dialog claims a layer; the registry is module
  // level and outlives the component that registered in it.
  resetKeyboardStack();
});

/** A masked Secret, which is the document this panel most has to get right: the
 *  server prepends a comment saying the values are masked, and the values
 *  themselves never leave `internal/detail`. */
const secret = {
  state: 'ok',
  data: {
    lines: [
      {
        number: 1,
        tokens: [{ text: '# The values below are masked.', class: 'comment' }],
      },
      {
        number: 2,
        tokens: [
          { text: 'kind', class: 'key' },
          { text: ': ', class: 'punctuation' },
          { text: 'Secret', class: 'plain' },
        ],
      },
      {
        number: 3,
        tokens: [
          { text: '  password', class: 'key' },
          { text: ': ', class: 'punctuation' },
          { text: '<masked>', class: 'string' },
        ],
      },
    ],
    raw: '# The values below are masked.\nkind: Secret\n  password: <masked>\n',
    maskedKeys: ['password'],
    resourceVersion: '44',
    editable: true,
  },
};

function stub(reply: unknown, extra: StubRoutes = {}): void {
  stubFetch({ '/api/c/prod/manifest': { body: reply }, ...extra });
}

function panel(): HTMLElement {
  const element = document.querySelector('[data-panel="manifest"]');
  if (element === null) throw new Error('no manifest panel in the document');
  return element as HTMLElement;
}

function mount(): void {
  renderInRouter(<Manifest cluster="prod" kind="secrets" namespace="shop" name="db" />);
}

describe('the document', () => {
  it('draws the server’s tokens and never composes markup', async () => {
    stub(secret);
    mount();

    await waitFor(() => expect(panel().textContent).toContain('kind'));
    // Every token is its own element, which is what makes this a render of data
    // rather than of a string the server built. A panel drawn from an HTML
    // string would have the same text and no spans to count.
    expect(panel().querySelectorAll('code span').length).toBeGreaterThan(3);
    expect(panel().innerHTML).not.toContain('<script');
  });

  it('numbers the lines from the server and not from the array', async () => {
    stub(secret);
    mount();

    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    // The gutter is `ManifestLine.number`. It agrees with the index today and
    // would stop agreeing the first time the server sends a window over a long
    // document — and a gutter that disagrees with the document is what somebody
    // quotes in a ticket.
    //
    // Read inside the document: the toolbar above it draws the search control's
    // glyph, which is `aria-hidden` for its own reason and is not a gutter.
    const gutters = [...panel().querySelectorAll('pre [aria-hidden="true"]')].map(
      (node) => node.textContent,
    );
    expect(gutters).toEqual(['1', '2', '3']);
  });

  it('carries the masking notice the server put in the document', async () => {
    stub(secret);
    mount();

    await waitFor(() => expect(panel().textContent).toContain('masked'));
    // Nothing in this component knows what a Secret is, deliberately. The
    // masking announces itself as an ordinary comment, because the decision of
    // what to hide belongs to `detail.Trimmed` and to nothing else.
    expect(panel().textContent).toContain('# The values below are masked.');
    expect(panel().textContent).toContain('<masked>');
  });
});

describe('the editor', () => {
  it('is handed what the server said it masked, so it can ask the one door for exactly that', async () => {
    stub(secret);
    mount();

    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    fireEvent.click(screen.getByRole('button', { name: 'Edit this manifest' }));

    // `ManifestPanel.maskedKeys` travels from the panel to the editor as it
    // arrived. This component still knows nothing about Secrets: it hands on a
    // list, and the editor offers a reveal for its length.
    expect(await screen.findByRole('button', { name: 'Reveal 1 value' })).toBeTruthy();
  });

  it('opens over the document rather than in place of it', async () => {
    stub(secret);
    mount();

    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    fireEvent.click(screen.getByRole('button', { name: 'Edit this manifest' }));

    // The *edit act* is a dialog and the read view stays where it is: the
    // colored document is still underneath, and the drawer around it still
    // says which object this is.
    expect(await screen.findByRole('dialog', { name: 'Edit db' })).toBeTruthy();
    expect(panel().textContent).toContain('# The values below are masked.');
    expect(panel().textContent).toContain('kind');
  });
});

describe('when the write is refused', () => {
  it('says which setting refused it', async () => {
    stub({
      ...secret,
      data: {
        ...secret.data,
        editable: false,
        editableNote: 'prod is marked read-only in KT_READONLY_CLUSTERS; changes are refused there',
      },
    });
    mount();

    await waitFor(() => expect(panel().textContent).toContain('KT_READONLY_CLUSTERS'));
    // `editable: false` on its own leaves a user looking at a missing
    // control with no way to find out what to change, and the two causes are
    // different people's to fix.
    expect(await screen.findByRole('status')).toBeTruthy();
  });

  it('draws Edit disabled with the reason where the console may not patch', async () => {
    const reason =
      "this console's ServiceAccount, which the shared account acts with, is not granted patch on secrets in shop";
    stub({ ...secret, data: { ...secret.data, editable: false, editableNote: reason } });
    mount();

    const edit = await screen.findByRole('button', { name: 'Edit this manifest' });
    expect(edit.hasAttribute('disabled')).toBe(true);
    expect(edit.getAttribute('title')).toBe(reason);
    const described = document.getElementById(edit.getAttribute('aria-describedby') ?? '');
    expect(described?.textContent).toBe(reason);
  });

  it('draws no Edit for a person who may not patch the object', async () => {
    stub({ ...secret, data: { ...secret.data, editable: false } });
    mount();

    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    expect(screen.queryByRole('button', { name: 'Edit this manifest' })).toBeNull();
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('offers Edit, enabled, to a person who may patch', async () => {
    stub(secret);
    mount();

    const edit = await screen.findByRole('button', { name: 'Edit this manifest' });
    expect(edit.hasAttribute('disabled')).toBe(false);
  });

  it('says nothing where the write is allowed', async () => {
    stub(secret);
    mount();

    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    expect(screen.queryByRole('status')).toBeNull();
  });
});

describe('the five ways it can fail', () => {
  it('draws a refusal rather than an empty document', async () => {
    stub({
      state: 'forbidden',
      reason: { code: 'verb_denied', message: 'secrets is forbidden for this account' },
    });
    mount();

    await waitFor(() =>
      expect(document.body.textContent).toContain('secrets is forbidden for this account'),
    );
    // Not a blank `<pre>`. An object whose manifest cannot be read is a
    // different answer from one whose manifest is empty, and a panel that drew
    // both the same would be the defect this product exists to remove.
    expect(document.querySelector('[data-panel="manifest"]')).toBeNull();
  });

  it('tells an object that is not there apart from one that is empty', async () => {
    stub({
      state: 'absent',
      reason: { code: 'not_found', message: 'no secret called db in shop' },
    });
    mount();

    await waitFor(() => expect(document.body.textContent).toContain('no secret called db in shop'));
  });
});

/**
 * A search inside the document. The browser's own
 * find reads the page's text and cannot be told what the console means by a
 * match, so the panel draws its own - over the tokens as drawn, and nothing
 * else.
 */
describe('the search', () => {
  /** With the keyboard stack, which the hotkey and Escape are claims on. */
  function mountWithKeys(): () => void {
    const { unmount } = renderInRouter(
      <KeyboardStack>
        <Manifest cluster="prod" kind="secrets" namespace="shop" name="db" />
      </KeyboardStack>,
    );
    return unmount;
  }

  function box(): HTMLInputElement {
    return screen.getByLabelText('Find in this manifest') as HTMLInputElement;
  }

  /** The `n/m` beside the box. */
  function count(): string {
    return screen.getByLabelText('Next match').parentElement?.textContent?.replace(/[^\d/]/g, '') ?? '';
  }

  /** The text of the line the current match is on. */
  function currentLine(): string {
    const mark = panel().querySelector('[data-find-current]');
    return mark?.closest('pre > code > div')?.textContent ?? '';
  }

  /** Ctrl+F where the keyboard is, as the window's one listener sees it.
   *  `false` when a claimant took it from the browser. */
  function ctrlF(on: Element = document.body): boolean {
    return fireEvent.keyDown(on, { key: 'f', ctrlKey: true });
  }

  it('counts the matches, marks them, and steps through them both ways', async () => {
    stub(secret);
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));

    fireEvent.click(screen.getByRole('button', { name: 'Search this manifest' }));
    // Case-insensitive unless asked: the comment's "masked" and the value's
    // "<masked>" are both found by a reader who typed it in capitals.
    fireEvent.change(box(), { target: { value: 'MASKED' } });

    await waitFor(() => expect(count()).toBe('1/2'));
    expect(panel().querySelectorAll('[data-find-hit]')).toHaveLength(2);
    expect(currentLine()).toContain('# The values below are masked.');

    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('2/2');
    expect(currentLine()).toContain('password');
    // Exactly one match is the current one, marked apart from the others.
    expect(panel().querySelectorAll('[data-find-current]')).toHaveLength(1);

    // Past the last, back to the first; and Shift+Enter goes the other way.
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('1/2');
    fireEvent.keyDown(box(), { key: 'Enter', shiftKey: true });
    expect(count()).toBe('2/2');
  });

  it('matches case only when asked to', async () => {
    stub(secret);
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));

    fireEvent.click(screen.getByRole('button', { name: 'Search this manifest' }));
    fireEvent.change(box(), { target: { value: 'secret' } });
    await waitFor(() => expect(count()).toBe('1/1'));

    fireEvent.click(screen.getByRole('button', { name: 'Aa' }));
    await waitFor(() => expect(count()).toBe('0/0'));
    expect(panel().querySelector('[data-find-hit]')).toBeNull();
  });

  it('settles on Enter rather than waiting out the pause in the typing', async () => {
    stub(secret);
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));

    fireEvent.click(screen.getByRole('button', { name: 'Search this manifest' }));
    fireEvent.change(box(), { target: { value: 'kind' } });
    // The first Enter is "search now", and lands on the first match rather
    // than stepping past it to a second the reader has not seen.
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('1/1');
    expect(currentLine()).toContain('kind: Secret');
  });

  it('closes on Escape, and takes its marks with it', async () => {
    stub(secret);
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));

    const opener = screen.getByRole('button', { name: 'Search this manifest' });
    opener.focus();
    fireEvent.click(opener);
    fireEvent.change(box(), { target: { value: 'masked' } });
    await waitFor(() => expect(panel().querySelectorAll('[data-find-hit]')).toHaveLength(2));

    fireEvent.keyDown(box(), { key: 'Escape' });

    expect(screen.queryByLabelText('Find in this manifest')).toBeNull();
    expect(panel().querySelector('[data-find-hit]')).toBeNull();
    // The keyboard goes back to where it was when the bar was asked for.
    expect(document.activeElement).toBe(opener);

    // And a search opened again starts empty.
    fireEvent.click(opener);
    expect(box().value).toBe('');
  });

  it('opens on Ctrl+F while the document is shown, with the keyboard in the box', async () => {
    stub(secret);
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));

    // Taken from the browser, which is the point: its own find does not know
    // what this panel means by a match.
    expect(ctrlF()).toBe(false);
    expect(document.activeElement).toBe(box());

    // ⌘F is the same key on an Apple keyboard.
    fireEvent.keyDown(box(), { key: 'Escape' });
    expect(fireEvent.keyDown(document.body, { key: 'f', metaKey: true })).toBe(false);
    expect(document.activeElement).toBe(box());
  });

  it('leaves Ctrl+F to the browser when the document is not shown', async () => {
    stub({
      state: 'forbidden',
      reason: { code: 'verb_denied', message: 'secrets is forbidden for this account' },
    });
    const unmount = mountWithKeys();
    await waitFor(() =>
      expect(document.body.textContent).toContain('secrets is forbidden for this account'),
    );

    // No document, nothing to search: the key is not claimed.
    expect(ctrlF()).toBe(true);
    expect(screen.queryByLabelText('Find in this manifest')).toBeNull();

    // Nor once the view is gone.
    act(() => unmount());
    expect(ctrlF()).toBe(true);
  });

  it('leaves Ctrl+F to a text field elsewhere, and Ctrl+R to the browser, while it is shown', async () => {
    stub(secret);
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));

    // A text area outside the view - the dock's terminal is one - keeps the key:
    // Ctrl+F there is a shell's forward-a-character.
    const elsewhere = document.createElement('textarea');
    document.body.append(elsewhere);
    try {
      elsewhere.focus();
      expect(ctrlF(elsewhere)).toBe(true);
      expect(screen.queryByLabelText('Find in this manifest')).toBeNull();
    } finally {
      elsewhere.remove();
    }

    // Reload stays the browser's.
    expect(fireEvent.keyDown(document.body, { key: 'r', ctrlKey: true })).toBe(true);
  });
});

describe('the enlarged view', () => {
  function mountWithKeys(): void {
    renderInRouter(
      <KeyboardStack>
        <Manifest cluster="prod" kind="secrets" namespace="shop" name="db" />
      </KeyboardStack>,
    );
  }

  async function enlarge(): Promise<HTMLElement> {
    stub(secret);
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    fireEvent.click(screen.getByRole('button', { name: 'Enlarge' }));
    return screen.findByRole('region', { name: 'db manifest, read-only' });
  }

  /** Each drawn token's text and color class, in reading order. */
  function tokens(root: Element): [string, string][] {
    return [...root.querySelectorAll('[data-manifest-lines] code > div > span:last-child > span')].map(
      (span) => [span.textContent ?? '', span.className],
    );
  }

  it('shows the document read-only until Edit is pressed', async () => {
    const region = await enlarge();
    expect(region.querySelector('textarea, input, [contenteditable="true"]')).toBeNull();
    expect(region.textContent).toContain('kind: Secret');
    expect(screen.queryByRole('dialog', { name: 'Edit db' })).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const editor = await screen.findByRole('dialog', { name: 'Edit db' });
    expect(editor).not.toBeNull();
    expect(screen.queryByRole('region', { name: 'db manifest, read-only' })).toBeNull();
  });

  // Enlarging the document keeps its YAML colors: the same manifest shows the
  // same colors in both.
  it('keeps the YAML colors of the tab, token for token', async () => {
    const region = await enlarge();
    const inTab = tokens(panel());
    const enlarged = tokens(region);
    expect(enlarged.length).toBeGreaterThan(0);
    expect(enlarged).toEqual(inTab);
    // And they are colors: a key is drawn in the key class, not as plain text.
    // A word with no space also carries the classes that keep it whole on a
    // narrow line, so the color is one class among them.
    const key = enlarged.find(([text]) => text === 'kind');
    expect(key?.[1].split(' ')).toContain('text-accent');
  });

  it('searches with Ctrl+F in the enlarged view and marks the match', async () => {
    const region = await enlarge();
    region.focus();
    // Taken from the browser, even with the focus in the read-only block.
    expect(fireEvent.keyDown(region, { key: 'f', ctrlKey: true })).toBe(false);
    const bar = screen.getByLabelText('Find in this manifest') as HTMLInputElement;
    expect(document.activeElement).toBe(bar);

    fireEvent.change(bar, { target: { value: 'password' } });
    await waitFor(() => expect(region.querySelector('[data-find-current]')?.textContent).toBe('password'));
  });

  it('has the tab’s search control, which opens the bar and counts as the tab does', async () => {
    const region = await enlarge();
    const dialog = screen.getByRole('dialog', { name: 'db manifest' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Search this manifest' }));
    const bar = within(dialog).getByLabelText('Find in this manifest') as HTMLInputElement;
    expect(document.activeElement).toBe(bar);

    fireEvent.change(bar, { target: { value: 'MASKED' } });
    await waitFor(() =>
      expect(
        within(dialog).getByLabelText('Next match').parentElement?.textContent?.replace(/[^\d/]/g, ''),
      ).toBe('1/2'),
    );
    expect(region.querySelectorAll('[data-find-hit]')).toHaveLength(2);
    expect(region.querySelectorAll('[data-find-current]')).toHaveLength(1);
  });

  it('opens its own search on Ctrl+F, and never the tab’s behind it', async () => {
    const region = await enlarge();
    region.focus();
    fireEvent.keyDown(region, { key: 'f', ctrlKey: true });
    // One bar, and it is the dialog's: the tab underneath stays as it was.
    expect(screen.getAllByLabelText('Find in this manifest')).toHaveLength(1);
    expect(panel().querySelector('[data-find-bar]')).toBeNull();
  });

  it('takes Escape for the find bar first and the dialog second', async () => {
    const region = await enlarge();
    region.focus();
    fireEvent.keyDown(region, { key: 'f', ctrlKey: true });
    const bar = screen.getByLabelText('Find in this manifest');

    fireEvent.keyDown(bar, { key: 'Escape' });
    expect(screen.queryByLabelText('Find in this manifest')).toBeNull();
    expect(screen.queryByRole('region', { name: 'db manifest, read-only' })).not.toBeNull();

    fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(screen.queryByRole('region', { name: 'db manifest, read-only' })).toBeNull();
  });

  it('searches the drawn lines and never the editor’s raw text', async () => {
    stub({
      ...secret,
      data: { ...secret.data, raw: secret.data.raw + 'only-in-raw: hunter2\n' },
    });
    mountWithKeys();
    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    fireEvent.click(screen.getByRole('button', { name: 'Enlarge' }));
    const region = await screen.findByRole('region', { name: 'db manifest, read-only' });
    expect(region.textContent).not.toContain('hunter2');

    region.focus();
    fireEvent.keyDown(region, { key: 'f', ctrlKey: true });
    fireEvent.change(screen.getByLabelText('Find in this manifest'), {
      target: { value: 'hunter2' },
    });
    await waitFor(() =>
      expect(screen.getByLabelText('Next match').parentElement?.textContent).toContain('0'),
    );
    expect(region.querySelector('[data-find-hit]')).toBeNull();
  });
});

describe('the editor, opened from the tab', () => {
  it('opens its own search on Ctrl+F, and never the tab’s behind it', async () => {
    stub(secret);
    renderInRouter(
      <KeyboardStack>
        <Manifest cluster="prod" kind="secrets" namespace="shop" name="db" />
      </KeyboardStack>,
    );
    await waitFor(() => expect(panel().textContent).toContain('Secret'));
    fireEvent.click(screen.getByRole('button', { name: 'Edit this manifest' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit db' });
    const content = await within(dialog).findByRole('textbox', { name: 'db manifest' });

    // A field that takes typing, as a browser has it; jsdom does not compute it.
    Object.defineProperty(content, 'isContentEditable', { configurable: true, value: true });
    expect(fireEvent.keyDown(content, { key: 'f', ctrlKey: true })).toBe(false);
    expect(screen.getAllByLabelText('Find in this manifest')).toHaveLength(1);
    expect(within(dialog).getByLabelText('Find in this manifest')).toBe(document.activeElement);
    expect(panel().querySelector('[data-find-bar]')).toBeNull();
  });
});
