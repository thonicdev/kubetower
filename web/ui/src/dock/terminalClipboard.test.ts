import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  browserClipboard,
  cleanPaste,
  clipboardHint,
  clipboardKey,
  clipboardKeys,
  guardPaste,
  onTerminalContextMenu,
  pasteLines,
  onTerminalKey,
  type Clip,
  type Gate,
  type Selectable,
} from './terminalClipboard';

/**
 * The clipboard rules of a terminal pane, over fakes. `TerminalPane.test.tsx` shows the keys reach the real emulator; this is
 * every branch, including the ones jsdom cannot reach - a selection in xterm,
 * and a browser with no Clipboard API.
 */

function term(selection = ''): Selectable & { pasted: string[]; cleared: number } {
  let held = selection;
  const fake = {
    pasted: [] as string[],
    cleared: 0,
    hasSelection: () => held !== '',
    getSelection: () => held,
    clearSelection: () => {
      held = '';
      fake.cleared += 1;
    },
    paste: (text: string) => {
      fake.pasted.push(text);
    },
  };
  return fake;
}

type Unreadable = 'refused' | 'insecure' | 'unsupported';

function clip(read: string = 'from the clipboard', writes = true): Clip & { written: string[] } {
  const written: string[] = [];
  const unreadable = (value: string): value is Unreadable =>
    value === 'refused' || value === 'insecure' || value === 'unsupported';
  return {
    written,
    write: (text) => {
      written.push(text);
      return Promise.resolve(writes);
    },
    read: () => Promise.resolve(unreadable(read) ? { unread: read } : { text: read }),
  };
}

/** A `Gate` that records what it was asked and keeps the sender, holding it
 *  until it is sent - as the pane does. */
function asker(): { asked: number[]; send: (() => void) | null; gate: Gate } {
  const record = {
    asked: [] as number[],
    send: null as (() => void) | null,
    gate: {
      holding: () => record.send !== null,
      ask: (lines: number, send: () => void) => {
        record.asked.push(lines);
        record.send = () => {
          record.send = null;
          send();
        };
      },
    },
  };
  return record;
}

const quiet = (): void => {};

/** A hostile paste: three commands, the last
 *  hidden behind an end-of-bracketed-paste sequence. */
const PAYLOAD = 'kubectl get pods\nrm -rf /tmp/x\n\u001b[201~whoami\n';

function key(init: KeyboardEventInit, type = 'keydown'): KeyboardEvent {
  return new KeyboardEvent(type, { cancelable: true, ...init });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  Reflect.deleteProperty(navigator, 'clipboard');
});

describe('which keys are the clipboard', () => {
  it('is Ctrl+Shift+C and Ctrl+Shift+V, whatever the case the key arrives in', () => {
    expect(clipboardKey(key({ key: 'C', ctrlKey: true, shiftKey: true }))).toBe('copy');
    expect(clipboardKey(key({ key: 'c', ctrlKey: true, shiftKey: true }))).toBe('copy');
    expect(clipboardKey(key({ key: 'V', ctrlKey: true, shiftKey: true }))).toBe('paste');
  });

  it('is never Ctrl+C alone, which is the shell interrupt', () => {
    expect(clipboardKey(key({ key: 'c', ctrlKey: true }))).toBeNull();
    expect(clipboardKey(key({ key: 'v', ctrlKey: true }))).toBeNull();
    expect(clipboardKey(key({ key: 'C', ctrlKey: true, shiftKey: true, altKey: true }))).toBeNull();
  });
});

describe('a key in the terminal', () => {
  it('lets Ctrl+C through to the emulator and copies nothing', () => {
    const board = clip();
    const event = key({ key: 'c', ctrlKey: true });
    expect(onTerminalKey(term('selected'), event, board, quiet)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
    expect(board.written).toEqual([]);
  });

  it('copies the selection on Ctrl+Shift+C and keeps the key from the shell', () => {
    const board = clip();
    const event = key({ key: 'C', ctrlKey: true, shiftKey: true });
    expect(onTerminalKey(term('pod/api-1 Running'), event, board, quiet)).toBe(false);
    expect(event.defaultPrevented).toBe(true);
    expect(board.written).toEqual(['pod/api-1 Running']);
  });

  it('copies nothing when nothing is selected, and still keeps the key from the shell', () => {
    const board = clip();
    expect(onTerminalKey(term(), key({ key: 'C', ctrlKey: true, shiftKey: true }), board, quiet)).toBe(false);
    expect(board.written).toEqual([]);
  });

  it('leaves Ctrl+Shift+V to the browser, whose paste event the pane guards', () => {
    const event = key({ key: 'V', ctrlKey: true, shiftKey: true });
    expect(onTerminalKey(term(), event, clip(), quiet)).toBe(false);
    expect(event.defaultPrevented).toBe(false);
  });

  it('keeps the release of a chord from the emulator too, and acts only on the press', () => {
    const board = clip();
    expect(onTerminalKey(term('x'), key({ key: 'C', ctrlKey: true, shiftKey: true }, 'keyup'), board, quiet)).toBe(false);
    expect(board.written).toEqual([]);
  });

  it('says so when the browser refuses the copy', async () => {
    const said: string[] = [];
    onTerminalKey(term('x'), key({ key: 'C', ctrlKey: true, shiftKey: true }), clip('', false), (text) => said.push(text));
    await vi.waitFor(() => {
      expect(said).toEqual([expect.stringMatching(/refused to copy/)]);
    });
  });
});

describe('a right-click in the terminal', () => {
  it('copies the selection and clears it, and answers that it copied', async () => {
    const shell = term('kubectl get pods');
    const board = clip();
    // `true` is what the pane says "Copied" on.
    await expect(onTerminalContextMenu(shell, board, quiet, asker().gate)).resolves.toBe(true);
    expect(board.written).toEqual(['kubectl get pods']);
    expect(shell.cleared).toBe(1);
    expect(shell.pasted).toEqual([]);
  });

  it('pastes one line with no newline straight in when nothing is selected', async () => {
    const shell = term();
    await onTerminalContextMenu(shell, clip('ls -la'), quiet, asker().gate);
    expect(shell.pasted).toEqual(['ls -la']);
  });

  it('holds a hostile paste until it is confirmed', async () => {
    const shell = term();
    const asking = asker();
    await onTerminalContextMenu(shell, clip(PAYLOAD), quiet, asking.gate);
    expect(asking.asked).toEqual([3]);
    expect(shell.pasted).toEqual([]);
    asking.send?.();
    expect(shell.pasted).toEqual(['kubectl get pods\nrm -rf /tmp/x\nwhoami\n']);
  });

  it('says the browser refused, not HTTPS, when a secure page is refused', async () => {
    const said: string[] = [];
    await onTerminalContextMenu(term(), clip('refused'), (text) => said.push(text), asker().gate);
    expect(said).toEqual([expect.stringMatching(/^The browser refused clipboard access\. Paste with Ctrl\+Shift\+V/)]);
  });

  it('says the browser does not offer reading, where it does not', async () => {
    const said: string[] = [];
    await onTerminalContextMenu(term(), clip('unsupported'), (text) => said.push(text), asker().gate);
    expect(said).toEqual([expect.stringMatching(/^This browser does not let the page read the clipboard\./)]);
  });

  it('says HTTPS when the page is not a secure context, and pastes nothing', async () => {
    const shell = term();
    const said: string[] = [];
    await onTerminalContextMenu(shell, clip('insecure'), (text) => said.push(text), asker().gate);
    expect(shell.pasted).toEqual([]);
    expect(said).toEqual([expect.stringMatching(/HTTPS.*Ctrl\+Shift\+V/)]);
  });
});

describe('saying which keys, on each platform', () => {
  /** The platform as the browser reports it, for the length of a test. */
  function platform(value: string): void {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue(value);
  }

  it('says Ctrl+Shift+C and Ctrl+Shift+V off a Mac, which is what the code handles there', () => {
    platform('Win32');
    expect(clipboardHint()).toBe('Right-click: copy selection / paste · Ctrl+Shift+C / Ctrl+Shift+V');
    platform('Linux x86_64');
    expect(clipboardKeys()).toEqual({ copy: 'Ctrl+Shift+C', paste: 'Ctrl+Shift+V' });
  });

  it('says ⌘C and ⌘V on a Mac, where Ctrl+Shift+V raises no paste event', () => {
    platform('MacIntel');
    expect(clipboardHint()).toBe('Right-click: copy selection / paste · ⌘C / ⌘V');
  });

  it('names the same keys in a refusal as in the footer', async () => {
    platform('MacIntel');
    const said: string[] = [];
    await onTerminalContextMenu(term(), clip('refused'), (text) => said.push(text), asker().gate);
    expect(said).toEqual(['The browser refused clipboard access. Paste with ⌘V instead.']);
  });

  it('names ⌘C in the copy refusal on a Mac, as the footer does', async () => {
    platform('MacIntel');
    const said: string[] = [];
    await onTerminalContextMenu(term('x'), clip('', false), (text) => said.push(text), asker().gate);
    expect(said).toEqual(['The browser refused to copy. Select the text and try ⌘C again.']);
  });

  it('answers that it did not copy when the clipboard refused, or when it pasted', async () => {
    await expect(onTerminalContextMenu(term('x'), clip('', false), quiet, asker().gate)).resolves.toBe(false);
    await expect(onTerminalContextMenu(term(), clip('ls'), quiet, asker().gate)).resolves.toBe(false);
  });
});

describe('the browser clipboard', () => {
  it('copies through the textarea fallback without TLS instead of throwing', async () => {
    vi.stubGlobal('isSecureContext', false);
    // jsdom has no `execCommand`; the fallback is what is under test.
    const exec = vi.fn(() => true);
    Object.defineProperty(document, 'execCommand', { value: exec, configurable: true });
    await expect(browserClipboard.write('selected text')).resolves.toBe(true);
    expect(exec).toHaveBeenCalledWith('copy');
    // And leaves nothing behind in the document.
    expect(document.querySelectorAll('textarea')).toHaveLength(0);
    Reflect.deleteProperty(document, 'execCommand');
  });

  it('reads nothing without TLS, and says the page is insecure', async () => {
    vi.stubGlobal('isSecureContext', false);
    await expect(browserClipboard.read()).resolves.toEqual({ unread: 'insecure' });
  });

  it('answers unsupported on a secure page whose clipboard has no readText', async () => {
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', { value: {}, configurable: true });
    await expect(browserClipboard.read()).resolves.toEqual({ unread: 'unsupported' });
  });

  it('tells a refusal on a secure page from an insecure page', async () => {
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', {
      value: { readText: () => Promise.reject(new DOMException('denied', 'NotAllowedError')) },
      configurable: true,
    });
    await expect(browserClipboard.read()).resolves.toEqual({ unread: 'refused' });
  });
});

describe('what a paste sends', () => {
  it('drops an end-of-bracketed-paste sequence, and every other escape', () => {
    expect(cleanPaste('echo hi\u001b[201~')).toBe('echo hi');
    expect(cleanPaste('a\u001b]0;title\u0007b\u001bcc')).toBe('abc');
  });

  it('never lets a stray ESC eat the newline or tab after it, so two lines stay two', () => {
    expect(cleanPaste('echo a\u001b\nrm x\n')).toBe('echo a\nrm x\n');
    expect(cleanPaste('a\u001b\tb')).toBe('a\tb');
  });

  it('drops only the start of an unterminated OSC, and keeps the rest of the paste', () => {
    expect(cleanPaste('echo safe\u001b]0;\nrm -rf x\n')).toBe('echo safe0;\nrm -rf x\n');
  });

  it('keeps tab and newline, drops every other control character, and makes CR LF one line', () => {
    expect(cleanPaste('a\tb\r\nc\rd\u0003e\u007ff\u0085g')).toBe('a\tb\nc\ndefg');
  });

  it('counts lines, a trailing newline ending the last', () => {
    expect(pasteLines('')).toBe(0);
    expect(pasteLines('one')).toBe(1);
    expect(pasteLines('one\n')).toBe(1);
    expect(pasteLines(cleanPaste(PAYLOAD))).toBe(3);
  });

  it('sends one line with no newline straight through, cleaned', () => {
    const shell = term();
    const asking = asker();
    guardPaste(shell, 'echo hi\u001b[201~', asking.gate, quiet);
    expect(shell.pasted).toEqual(['echo hi']);
    expect(asking.asked).toEqual([]);
  });

  it('asks about one command ending in a newline, which would run as it landed', () => {
    for (const raw of ['ls\n', 'ls\r', 'ls\r\n', 'curl x | sh\r']) {
      const shell = term();
      const asking = asker();
      guardPaste(shell, raw, asking.gate, quiet);
      expect(asking.asked).toEqual([1]);
      expect(shell.pasted).toEqual([]);
    }
  });

  it('refuses a second paste while one is waiting, and says so', () => {
    const shell = term();
    const asking = asker();
    const said: string[] = [];
    const say = (text: string): void => {
      said.push(text);
    };
    guardPaste(shell, PAYLOAD, asking.gate, say);
    guardPaste(shell, 'single', asking.gate, say);
    guardPaste(shell, 'x\ny\n', asking.gate, say);
    expect(asking.asked).toEqual([3]);
    expect(shell.pasted).toEqual([]);
    expect(said).toEqual([expect.stringMatching(/already waiting/), expect.stringMatching(/already waiting/)]);
    asking.send?.();
    expect(shell.pasted).toEqual(['kubectl get pods\nrm -rf /tmp/x\nwhoami\n']);
  });

  it('sends nothing for a paste that was only control characters', () => {
    const shell = term();
    guardPaste(shell, '\u001b[201~\u0003', asker().gate, quiet);
    expect(shell.pasted).toEqual([]);
  });
});
