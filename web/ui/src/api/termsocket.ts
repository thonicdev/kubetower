import type { Reason, State } from './types';

/**
 * The one place in the SPA that opens a terminal socket.
 *
 * `WebSocket` is banned outside this directory by `no-restricted-globals`, for
 * the same reason `fetch` is: one module is what makes the session
 * cookie, the frame vocabulary and the refusal handling properties of the
 * application rather than of whichever component was written most recently.
 *
 * ## The frames, and why they are base64 inside JSON
 *
 * `api/websocket.md` is the contract and `internal/terminal/stream.go` is the
 * server half. Both directions are JSON with the bytes base64-encoded, and the
 * encoding is not decoration: **a read from a pseudo-terminal can stop halfway
 * through a multi-byte character**, and a WebSocket text frame must be valid
 * UTF-8 - a split character would abort the connection rather than be
 * reassembled. Half a character is still valid base64, so nothing here has to
 * buffer across frames.
 *
 * Binary frames and a `TextDecoder` would solve the same problem more cheaply.
 * What this shape buys is the reason the contract chose it:
 * **a refusal is a frame.** There is a `state` frame carrying the envelope's own
 * five-state vocabulary, so "the session cap is reached" and "this cluster is
 * read-only" are two answers a pane can draw differently rather than two
 * sentences that happen to differ.
 *
 * ## What a close does not tell you
 *
 * Nothing. A socket that opens and closes is indistinguishable from a container
 * that exited, which is why the server refuses before the upgrade with an
 * ordinary HTTP status wherever it can, and sends a frame wherever it cannot.
 * `onClosed` is therefore the last resort and says so: a pane may report that the
 * connection went, and may never claim why.
 */

/**
 * The prefix every address in this contract shares.
 *
 * **Not checked against `keyof paths`, and that is a real gap rather than an
 * oversight.** `endpoints.ts` verifies every template it uses against the
 * generated `schema.ts`, so renaming an operation in `api/openapi.yaml` fails the
 * type-check rather than 404-ing at run time. The four socket channels cannot get
 * that: OpenAPI 3.1 cannot describe a WebSocket - no verb, no request body, no
 * response object - so they live in `x-channels`, `openapi-typescript` does not
 * emit them, and there is nothing to compare a string to.
 *
 * What stands in for it is on the server: `internal/api/channels_test.go` reads
 * `x-channels` out of the YAML at run time and asserts every advertised address
 * is served. So a channel renamed in the contract fails there; a channel renamed
 * **here** and nowhere else does not fail anything, and the honest thing is to
 * say so rather than to write a check that looks like one.
 */
const API_PREFIX = '/api/';

/** Which channel to open. The four `x-channels` advertises. */
export type Channel = 'terminal' | 'exec' | 'nodeshell' | 'assistant';

export interface TerminalSocketQuery {
  readonly channel: Channel;
  /** Absent on `assistant`, which is not addressed per cluster. */
  readonly cluster?: string;
  readonly namespace?: string;
  readonly pod?: string;
  readonly container?: string;
  /** The object's **Kind** and name, on `assistant` only: the session's framing,
   *  and what keeps two sessions on two pods from mixing their context. */
  readonly kind?: string;
  readonly name?: string;
  /** The plan token, on `nodeshell` only. **The node is not sent**: the server
   *  reads it out of the plan the operator agreed to, which is the only thing
   *  binding the confirmation to what happens. */
  readonly token?: string;
  /** The conversation, on `assistant` only: the UUID the tab drew when it was
   *  first opened. The server looks for its transcript and decides between
   *  `--session-id` and `--resume` - this side never says which
   *  (`api/websocket.md`). */
  readonly session?: string;
}

export interface TerminalSocketHandlers {
  /** Bytes the far end printed. */
  readonly onData: (bytes: Uint8Array) => void;
  /** The server refusing or reporting, mid-stream, in the envelope's own
   *  vocabulary. A pane draws this; it never has to guess. */
  readonly onState: (state: State, reason: Reason | undefined) => void;
  /** The far end finished, said before the server closes the socket. `code` is
   *  the status a pod exec or a node shell reported when it was not zero, and
   *  absent everywhere else - the pseudo-terminal seam exposes no exit status.
   *  A pane that printed "exited 0" from an absent code would be inventing the
   *  one fact the operator came for. */
  readonly onExit: (code: number | undefined) => void;
  /** The socket went, and **nothing is known about why**. Not a refusal: the
   *  server frames those. */
  readonly onClosed: () => void;
}

export interface TerminalSocket {
  /** Keystrokes. Encoded here so no caller has to know the wire. */
  readonly send: (text: string) => void;
  /** The pane's size. Zero in either dimension is dropped: that is what a
   *  browser reports for a hidden tab, and applying it gives a 0x0 terminal with
   *  everything drawn while it was hidden lost. A size asked for before the
   *  socket is open is sent the moment it opens, ahead of any keystroke held
   *  the same way - it is the session's first size, not a later courtesy. */
  readonly resize: (cols: number, rows: number) => void;
  /** Closes it. Safe to call more than once. */
  readonly close: () => void;
}

/** The address of one channel, as `api/openapi.yaml` advertises it under
 *  `x-channels`. Built here so that no component composes one. */
export function channelPath(query: TerminalSocketQuery): string {
  const params = new URLSearchParams();
  if (query.namespace !== undefined && query.namespace !== '') {
    params.set('namespace', query.namespace);
  }
  if (query.pod !== undefined && query.pod !== '') params.set('pod', query.pod);
  if (query.container !== undefined && query.container !== '') {
    params.set('container', query.container);
  }
  if (query.token !== undefined && query.token !== '') params.set('token', query.token);

  if (query.channel === 'assistant') {
    /*
      **The assistant is addressed in the query.**

      Its address carries no `{cluster}` segment, which does not mean "no
      cluster": a caller that sent nothing would have every session refused
      with a plain `404 unknown cluster` before the upgrade.

      `internal/terminal/claude.go:580` resolves the cluster through
      `clusterParam`, which reads the path value **or** the `cluster` query, and
      `:614-616` reads `kind`, `namespace` and `name` beside it. All four are the
      session's framing rather than its boundary - the kubeconfig narrowed to the
      cluster is the boundary - and the object is what keeps two sessions on two
      pods from mixing their context.

      `kind` here is the **Kind** as a person writes it, "Pod", because
      `validSelection` is documented against one. It is the panel's `title`, not
      the route's plural.
    */
    if (query.cluster !== undefined && query.cluster !== '') params.set('cluster', query.cluster);
    if (query.kind !== undefined && query.kind !== '') params.set('kind', query.kind);
    if (query.name !== undefined && query.name !== '') params.set('name', query.name);
    if (query.session !== undefined && query.session !== '') params.set('session', query.session);
    const assistant = params.toString();
    return assistant === ''
      ? `${API_PREFIX}ws/assistant`
      : `${API_PREFIX}ws/assistant?${assistant}`;
  }

  const base = `${API_PREFIX}c/${encodeURIComponent(query.cluster ?? '')}/ws/${query.channel}`;
  const search = params.toString();
  return search === '' ? base : `${base}?${search}`;
}

/** The absolute `ws:`/`wss:` URL for a same-origin channel.
 *
 *  Same origin, so the session cookie is attached and `connect-src 'self'`
 *  covers it - the socket inherits the page's origin. Derived rather than
 *  configured: a base URL in a setting is a base URL that can point somewhere
 *  else. */
export function channelURL(query: TerminalSocketQuery): string {
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${scheme}//${window.location.host}${channelPath(query)}`;
}

/** What the server sends. Every frame is one of these four types. */
interface ServerFrame {
  type: 'data' | 'state' | 'exit';
  stream?: string;
  data?: string;
  state?: State;
  reason?: Reason;
  code?: number;
}

export function openTerminalSocket(
  query: TerminalSocketQuery,
  handlers: TerminalSocketHandlers,
): TerminalSocket {
  const socket = new WebSocket(channelURL(query));
  let closed = false;
  /** Anything typed before the socket is open. A terminal is opened from a
   *  gesture and focused immediately, so the first keystroke routinely arrives
   *  in the few milliseconds before `onopen` - dropping it loses a character
   *  with nothing to see. */
  const pending: string[] = [];
  /** The last size asked for before the socket is open. Held for the same
   *  reason `pending` is, and it matters more: the pane sends its first size in
   *  a macrotask after `fit`, which lands before a WebSocket upgrade plus a
   *  Kubernetes exec can possibly finish. Dropped, nothing would send the size
   *  again until the dock's grip is dragged, so the shell would run at the
   *  server's default (`stty size` answering `0 0` inside a pod) while xterm
   *  draws 177 columns. Only the last one is
   *  kept: a size is a fact about now, not a keystroke to replay in order. */
  let size: { cols: number; rows: number } | null = null;

  socket.onopen = () => {
    // The size first, ahead of the keystrokes. A keystroke into an 80-column
    // shell that is about to become 177 wide is the same defect one line later.
    if (size !== null) {
      socket.send(resizeFrame(size.cols, size.rows));
      size = null;
    }
    for (const text of pending) for (const frame of framesOf(text)) socket.send(frame);
    pending.length = 0;
  };

  socket.onmessage = (message: MessageEvent<string>) => {
    let frame: ServerFrame;
    try {
      frame = JSON.parse(message.data) as ServerFrame;
    } catch {
      // A frame this build cannot read is dropped rather than fatal: an
      // unrecognized message from a newer server must not end the session, and
      // printing the raw JSON into the pane would put text in front of the
      // operator that nothing ran.
      return;
    }
    switch (frame.type) {
      case 'data': {
        const bytes = decode(frame.data ?? '');
        if (bytes !== null) handlers.onData(bytes);
        return;
      }
      case 'state':
        handlers.onState(frame.state ?? 'unreadable', frame.reason);
        return;
      case 'exit':
        handlers.onExit(frame.code);
        return;
      default:
        return;
    }
  };

  socket.onclose = () => {
    if (closed) return;
    closed = true;
    handlers.onClosed();
  };

  // Not routed to `onState`. The browser's own error carries nothing - no
  // status, no body, not even whether the server was reached - and a pane that
  // rendered it as a state would be claiming a cause it does not have. `onclose`
  // always follows it, and that is the one that says something true.
  socket.onerror = () => {};

  return {
    send: (text) => {
      if (text === '') return;
      if (socket.readyState === WebSocket.OPEN) for (const frame of framesOf(text)) socket.send(frame);
      else if (socket.readyState === WebSocket.CONNECTING) pending.push(text);
    },
    resize: (cols, rows) => {
      if (cols <= 0 || rows <= 0) return;
      if (socket.readyState === WebSocket.OPEN) socket.send(resizeFrame(cols, rows));
      else if (socket.readyState === WebSocket.CONNECTING) size = { cols, rows };
    },
    close: () => {
      closed = true;
      socket.close();
    },
  };
}

/** The most bytes of input one frame carries. The server refuses a message
 *  over 1 MiB with 1009 and closes the session (`internal/terminal/stream.go`
 *  `maxFrame`), and base64 grows the bytes by a third: 512 KiB is about
 *  683 KiB on the wire, well under it. A longer paste goes as several frames,
 *  which the server writes into the terminal in order. */
export const MAX_INPUT_BYTES = 512 * 1024;

/** The keystroke frames for one piece of input. `TextEncoder` first, because a
 *  character outside Latin-1 makes `btoa` throw - and a terminal is exactly where
 *  somebody types one. A cut through a character is harmless: the server
 *  writes bytes, and the terminal reads them back together. */
function framesOf(text: string): string[] {
  const bytes = new TextEncoder().encode(text);
  const frames: string[] = [];
  for (let start = 0; start < bytes.length; start += MAX_INPUT_BYTES) {
    let binary = '';
    for (const byte of bytes.subarray(start, start + MAX_INPUT_BYTES)) binary += String.fromCharCode(byte);
    frames.push(JSON.stringify({ type: 'data', data: btoa(binary) }));
  }
  return frames;
}

/** One resize frame, `api/websocket.md`'s `{type:'resize', cols, rows}`. */
function resizeFrame(cols: number, rows: number): string {
  return JSON.stringify({ type: 'resize', cols, rows });
}

/** Base64 to bytes. Returns null rather than throwing on a frame that is not
 *  base64: xterm would otherwise be handed the raw characters, which is output
 *  the container did not print. */
function decode(data: string): Uint8Array | null {
  try {
    const binary = atob(data);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    return bytes;
  } catch {
    return null;
  }
}
