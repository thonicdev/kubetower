/**
 * Copying out of a terminal, and pasting into one.
 *
 * The desktop window draws no context menu
 * (`go-webview2` turns WebView2's default menus off outside debug), and
 * **Ctrl+C is SIGINT and must stay so**. So the gestures are the ones a
 * terminal on this platform already has - Windows Terminal's:
 *
 * - **right-click** copies the selection and clears it, or pastes when there is
 *   none;
 * - **Ctrl+Shift+C** copies, **Ctrl+Shift+V** pastes;
 * - **Ctrl+C** is left to the emulator, which sends `\x03`.
 *
 * ## Pasting by key starts as the browser's
 *
 * Ctrl+Shift+V is let through rather than handled: the browser turns it into
 * its own `paste` event, on xterm's textarea, and that path needs no
 * permission and no secure context. The pane catches that event on the way
 * down and sends the text through `guardPaste`, below, so xterm never pastes
 * it unfiltered. Reading the clipboard from script is only needed for the
 * right-click, which has no browser default to fall back on.
 *
 * ## Without TLS
 *
 * `navigator.clipboard` is undefined outside a secure context, so reading
 * `.writeText` off it throws. Copying then goes through a hidden textarea and
 * `execCommand('copy')`, as `components/CopyButton.tsx` does; reading has no
 * such fallback, so a right-click paste says to use Ctrl+Shift+V instead of
 * throwing or doing nothing.
 *
 * ## What reaches the shell is cleaned, and several lines are asked about
 *
 * xterm's `paste()` turns newlines into carriage returns and wraps the text
 * in `ESC[200~ … ESC[201~` when the shell asked for bracketed paste - and
 * filters nothing. So a clipboard holding three lines ran three commands in a
 * pod's `sh`, which never asks for bracketed paste, and an `ESC[201~` inside
 * the text closed bash's bracket early and ran the rest anyway. Every paste,
 * by right-click or by Ctrl+Shift+V, goes through `guardPaste`:
 *
 * - control characters are removed except tab and newline, escape sequences
 *   whole (`cleanPaste`);
 * - text holding a newline - one command and a newline is enough to run it -
 *   is held and the pane asks first, naming the line count. Refusing sends
 *   nothing, and a second paste is refused while one is waiting.
 *
 * **Whatever the bracketed-paste mode.** xterm sets that mode from what the
 * session prints, so a `cat` of a file or a container's motd can turn it on
 * over a plain `sh` that knows nothing of brackets; the mode cannot be trusted
 * to make a paste safe. The stripping still
 * keeps a pasted `ESC[201~` from closing a real shell's bracket early.
 *
 * ## Saying so, on each platform
 *
 * The pane's footer carries
 * `clipboardHint`, and a right-click copy that reached the clipboard says
 * "Copied" (`onTerminalContextMenu` answers whether it did). **The keys are
 * named for the platform, because on a Mac the code does not do what the
 * other line says.** There, a paste by key is the browser's `paste` event and
 * Ctrl+Shift+V raises none, so it pastes nothing; ⌘V raises one, which the
 * pane guards like any other. ⌘C is never seen by `clipboardKey` - it carries
 * `metaKey` - and xterm leaves it to the browser, whose `copy` event xterm
 * fills with the selection. So a Mac is told ⌘C / ⌘V, which work there, and
 * the refusals below name the same keys.
 *
 * Written over two small interfaces rather than over xterm and `navigator`, so
 * that every branch is tested without a canvas or a browser: jsdom has neither
 * a selection model for xterm nor a clipboard.
 */

/** What this needs of the emulator: the four calls xterm's `Terminal` has. */
export interface Selectable {
  hasSelection(): boolean;
  getSelection(): string;
  clearSelection(): void;
  paste(text: string): void;
}

/** Why the clipboard could not be read. Three answers that are not one:
 *  an insecure page, a browser with no `readText`, and a refusal. */
export type Unread = 'insecure' | 'unsupported' | 'refused';

/** What this needs of the clipboard. A failed read says why, which is not
 *  the same as an empty clipboard. */
export interface Clip {
  write(text: string): Promise<boolean>;
  read(): Promise<{ readonly text: string } | { readonly unread: Unread }>;
}

/** A sentence for the pane's note line. */
export type Say = (text: string) => void;

/** Where a paste that needs an answer waits. `holding` says one already
 *  does; `ask` puts one there, and `send` pastes it - dropping the request
 *  sends nothing. */
export interface Gate {
  holding(): boolean;
  ask(lines: number, send: () => void): void;
}

const WAITING = 'A paste is already waiting for an answer. Paste or cancel it first.';

/** The keys that copy and paste in a terminal here, as the footer and the
 *  refusals spell them. */
export interface ClipboardKeys {
  readonly copy: string;
  readonly paste: string;
}

/** Whether this is an Apple platform, where the clipboard's keys are ⌘'s.
 *
 *  `navigator.platform`, the property xterm reads for its own `isMac`, but a
 *  wider test: xterm's list is the four Mac values (`Macintosh`, `MacIntel`,
 *  `MacPPC`, `Mac68K`) and this also takes an iPad or an iPhone, whose
 *  keyboards copy with ⌘ too. The two disagree there and nowhere else, and
 *  harmlessly: ⌘C and ⌘V carry `metaKey`, so `clipboardKey` leaves them to the
 *  browser whatever xterm decided. */
export function applePlatform(): boolean {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad|iPod/.test(navigator.platform);
}

/** The keys that work on this platform - see "Saying so, on each platform". */
export function clipboardKeys(apple: boolean = applePlatform()): ClipboardKeys {
  return apple
    ? { copy: '⌘C', paste: '⌘V' }
    : { copy: 'Ctrl+Shift+C', paste: 'Ctrl+Shift+V' };
}

/** The terminal pane's permanent footer line. */
export function clipboardHint(keys: ClipboardKeys = clipboardKeys()): string {
  return `Right-click: copy selection / paste · ${keys.copy} / ${keys.paste}`;
}

function unread(why: Unread): string {
  const { paste } = clipboardKeys();
  switch (why) {
    case 'insecure':
      return `The clipboard can only be read over HTTPS here. Paste with ${paste} instead.`;
    case 'unsupported':
      return `This browser does not let the page read the clipboard. Paste with ${paste} instead.`;
    case 'refused':
      return `The browser refused clipboard access. Paste with ${paste} instead.`;
  }
}

function copyRefused(): string {
  return `The browser refused to copy. Select the text and try ${clipboardKeys().copy} again.`;
}

/** Which clipboard gesture a key is, if any. Ctrl+Shift only: Ctrl alone is
 *  the shell's, and Ctrl+C there is an interrupt. */
export function clipboardKey(event: KeyboardEvent): 'copy' | 'paste' | null {
  if (!event.ctrlKey || !event.shiftKey || event.altKey || event.metaKey) return null;
  const key = event.key.toLowerCase();
  if (key === 'c') return 'copy';
  if (key === 'v') return 'paste';
  return null;
}

/**
 * The body of xterm's custom key handler: `true` lets the emulator handle the
 * key, `false` keeps it out of the shell.
 *
 * Every event of a clipboard chord returns `false`, keyup and keypress
 * included, or the emulator sees half of it. Only the keydown acts.
 */
export function onTerminalKey(term: Selectable, event: KeyboardEvent, clip: Clip, say: Say): boolean {
  const gesture = clipboardKey(event);
  if (gesture === null) return true;
  if (event.type !== 'keydown') return false;
  if (gesture === 'paste') {
    // Not prevented: the browser's own paste event is what carries the text,
    // and the pane's `paste` listener sends it through `guardPaste`.
    return false;
  }
  event.preventDefault();
  if (term.hasSelection()) void copy(term.getSelection(), clip, say);
  return false;
}

/**
 * A right-click on the terminal: copy the selection and clear it, or paste
 * when there is none. The context menu is suppressed by the caller, since in a
 * browser tab it would open over the paste.
 *
 * Answers `true` when it copied and the clipboard took the text, which is when
 * the pane says "Copied": a refusal already has its own sentence, and a paste
 * shows itself in the shell.
 */
export async function onTerminalContextMenu(term: Selectable, clip: Clip, say: Say, gate: Gate): Promise<boolean> {
  if (term.hasSelection()) {
    const text = term.getSelection();
    term.clearSelection();
    return copy(text, clip, say);
  }
  const read = await clip.read();
  if ('unread' in read) {
    say(unread(read.unread));
    return false;
  }
  guardPaste(term, read.text, gate, say);
  return false;
}

/**
 * Text on its way into a shell, without what a shell would act on unseen:
 * escape sequences whole (CSI, OSC, and any other `ESC x`), then every other
 * control character but tab and newline, C1 and DEL included. Line endings
 * become newlines first, so a Windows clipboard's CR LF is one line break.
 *
 * Two shapes are cut short on purpose. An OSC is removed only when it is
 * terminated on its own line; an unterminated one loses its `ESC ]` and keeps
 * its text, rather than taking the rest of the paste with it unseen. And a
 * stray ESC takes at most one printable character, never a newline or a tab,
 * so `echo a`, ESC, newline, `rm x` stays two lines and is asked about as two.
 */
export function cleanPaste(text: string): string {
  return text
    .replace(/\r\n?/g, '\n')
    // eslint-disable-next-line no-control-regex -- control characters are the subject
    .replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, '')
    // eslint-disable-next-line no-control-regex -- an OSC, ended by BEL or ST
    .replace(/\u001b\][^\u0007\u001b\n]*(?:\u0007|\u001b\\)/g, '')
    // eslint-disable-next-line no-control-regex -- any other escape and one printable character
    .replace(/\u001b[ -~]?/g, '')
    // eslint-disable-next-line no-control-regex -- C0 but tab and newline, DEL, C1
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, '');
}

/** How many lines a paste is: a trailing newline ends the last line and does
 *  not start another. */
export function pasteLines(text: string): number {
  if (text === '') return 0;
  return text.replace(/\n$/, '').split('\n').length;
}

/**
 * Every paste goes through here, by right-click or by Ctrl+Shift+V. Text with
 * no newline goes straight in; text with one - a single command ending in a
 * newline included, which runs as it lands - is held until the pane has asked.
 * While one is held, any other paste is refused, so nothing lands ahead of it
 * or replaces it.
 */
export function guardPaste(term: Selectable, raw: string, gate: Gate, say: Say): void {
  if (gate.holding()) {
    say(WAITING);
    return;
  }
  const text = cleanPaste(raw);
  if (text === '') return;
  if (text.includes('\n')) {
    gate.ask(pasteLines(text), () => term.paste(text));
    return;
  }
  term.paste(text);
}

async function copy(text: string, clip: Clip, say: Say): Promise<boolean> {
  const done = await clip.write(text);
  if (!done) say(copyRefused());
  return done;
}

/** The browser's clipboard, degrading where it is absent. */
export const browserClipboard: Clip = {
  async write(text) {
    if (window.isSecureContext && navigator.clipboard?.writeText !== undefined) {
      try {
        await navigator.clipboard.writeText(text);
        return true;
      } catch {
        // A refusal by the browser or a permissions policy: try the old way.
      }
    }
    return execCopy(text);
  },
  async read() {
    if (!window.isSecureContext) return { unread: 'insecure' };
    if (navigator.clipboard?.readText === undefined) return { unread: 'unsupported' };
    try {
      return { text: await navigator.clipboard.readText() };
    } catch {
      // A refused prompt, a permissions policy, or the desktop window's own
      // answer: on a secure page, the browser said no.
      return { unread: 'refused' };
    }
  },
};

/** The pre-Clipboard-API path: a textarea off screen, selected, and
 *  `execCommand('copy')`. It works over plain HTTP inside a user gesture. */
function execCopy(text: string): boolean {
  const area = document.createElement('textarea');
  area.value = text;
  area.readOnly = true;
  area.style.position = 'fixed';
  area.style.top = '-1000px';
  area.style.opacity = '0';
  // Where focus was, because selecting the textarea takes it: the terminal
  // would otherwise stop receiving keys until it is clicked again.
  const was = document.activeElement;
  document.body.append(area);
  try {
    area.select();
    area.setSelectionRange(0, text.length);
    return typeof document.execCommand === 'function' && document.execCommand('copy');
  } catch {
    return false;
  } finally {
    area.remove();
    if (was instanceof HTMLElement) was.focus({ preventScroll: true });
  }
}
