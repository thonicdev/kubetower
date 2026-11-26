import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { channelPath, MAX_INPUT_BYTES, openTerminalSocket, type TerminalSocket } from './termsocket';

/**
 * The terminal wire, from the browser's side.
 *
 * The server half is `internal/terminal/framedwire_test.go`, and the two are
 * written against the same paragraph of `api/websocket.md` rather than against
 * each other - which is the only arrangement where a disagreement shows up as a
 * failure instead of as two tests that agree on the wrong thing.
 *
 * What is *not* tested here is xterm. The pane wires this module to a terminal
 * and jsdom has no layout, so the fit, the cursor and the colors are a browser's
 * job; `TerminalPane.test.tsx` says what it can and cannot establish.
 */

/** Every socket this file opened, so a test can drive one from the server side. */
const sockets: FakeSocket[] = [];

class FakeSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  url: string;
  readyState = FakeSocket.CONNECTING;
  sent: string[] = [];
  closes = 0;

  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    sockets.push(this);
  }

  send(payload: string): void {
    this.sent.push(payload);
  }

  close(): void {
    this.closes += 1;
    this.readyState = FakeSocket.CLOSED;
  }

  /** The handshake completing. */
  open(): void {
    this.readyState = FakeSocket.OPEN;
    this.onopen?.();
  }

  /** One frame from the server. */
  deliver(frame: unknown): void {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }
}

function latest(): FakeSocket {
  const socket = sockets[sockets.length - 1];
  if (socket === undefined) throw new Error('no socket was opened');
  return socket;
}

function bodyOf(payload: string): Record<string, unknown> {
  return JSON.parse(payload) as Record<string, unknown>;
}

beforeEach(() => {
  sockets.length = 0;
  vi.stubGlobal('WebSocket', FakeSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('the address', () => {
  it('is the one x-channels advertises, per cluster', () => {
    expect(channelPath({ channel: 'terminal', cluster: 'prod' }))
      .toBe('/api/c/prod/ws/terminal');
    expect(channelPath({ channel: 'exec', cluster: 'prod', namespace: 'default', pod: 'api-1' }))
      .toBe('/api/c/prod/ws/exec?namespace=default&pod=api-1');
  });

  it('carries the image and the target on debug, and on nothing else', () => {
    // The target absent is "none": the debug container joins no process
    // namespace, which is a choice and not a gap for the server to fill.
    expect(channelPath({
      channel: 'debug', cluster: 'prod', namespace: 'pay', pod: 'api-1', image: 'nicolaka/netshoot:v0.13', target: 'api',
    })).toBe('/api/c/prod/ws/debug?namespace=pay&pod=api-1&image=nicolaka%2Fnetshoot%3Av0.13&target=api');
    expect(channelPath({ channel: 'debug', cluster: 'prod', namespace: 'pay', pod: 'api-1', image: 'busybox:1.36' }))
      .toBe('/api/c/prod/ws/debug?namespace=pay&pod=api-1&image=busybox%3A1.36');
    expect(channelPath({ channel: 'exec', cluster: 'prod', namespace: 'pay', pod: 'api-1', image: 'busybox:1.36', target: 'api' }))
      .toBe('/api/c/prod/ws/exec?namespace=pay&pod=api-1');
  });

  it('encodes a cluster name, because a context name is whatever a KUBECONFIG says', () => {
    // `api/openapi.yaml` reserves the one-letter `c` segment for exactly this -
    // "a segment the cluster names never sits beside a segment this console
    // names" - and the encoding here is the client half of it.
    expect(channelPath({ channel: 'terminal', cluster: 'arn:aws:eks:eu-west-1/prod' }))
      .toBe('/api/c/arn%3Aaws%3Aeks%3Aeu-west-1%2Fprod/ws/terminal');
  });

  it('carries the conversation on the assistant, and on nothing else', () => {
    // The id names a transcript and the server decides what to do with
    // it. It rides the assistant's query beside the framing, and a shell that
    // is handed one by mistake sends nothing - there is no conversation to
    // resume in a pod.
    expect(channelPath({ channel: 'assistant', cluster: 'prod', session: '3f2504e0-4f89-11d3-9a0c-0305e82c3301' })).toBe(
      '/api/ws/assistant?cluster=prod&session=3f2504e0-4f89-11d3-9a0c-0305e82c3301',
    );
    expect(channelPath({ channel: 'terminal', cluster: 'prod', session: '3f2504e0-4f89-11d3-9a0c-0305e82c3301' })).toBe(
      '/api/c/prod/ws/terminal',
    );
  });

  it('puts the assistant outside the cluster segment', () => {
    // It is a control channel and not a cluster read; the contract addresses it
    // at /api/ws/assistant, with no cluster in the path.
    expect(channelPath({ channel: 'assistant' })).toBe('/api/ws/assistant');
  });

  it('carries the plan token and never the node', () => {
    // The node is re-derived server-side from the plan the operator agreed to.
    // There is nowhere here for a different one to be named, which is stronger
    // than sending one and having the server compare.
    const path = channelPath({ channel: 'nodeshell', cluster: 'prod', token: 'abc123' });
    expect(path).toBe('/api/c/prod/ws/nodeshell?token=abc123');
    expect(path).not.toContain('node=');
  });
});

describe('the frames', () => {
  function open(): { socket: FakeSocket; handle: TerminalSocket; seen: Uint8Array[] } {
    const seen: Uint8Array[] = [];
    const handle = openTerminalSocket(
      { channel: 'terminal', cluster: 'prod' },
      {
        onData: (bytes) => seen.push(bytes),
        onState: () => {},
        onExit: () => {},
        onClosed: () => {},
      },
    );
    return { socket: latest(), handle, seen };
  }

  it('decodes base64 and survives a character split across two frames', () => {
    // The reason the wire is base64 at all: a read from a pseudo-terminal can
    // stop halfway through a multi-byte character, and half of "é" in a text
    // frame would abort the connection. Half of it in base64 is still base64.
    const { socket, seen } = open();
    socket.open();
    socket.deliver({ type: 'data', stream: 'stdout', data: btoa('\xc3') });
    socket.deliver({ type: 'data', stream: 'stdout', data: btoa('\xa9') });

    const joined = new Uint8Array([...(seen[0] ?? []), ...(seen[1] ?? [])]);
    expect(new TextDecoder().decode(joined)).toBe('é');
  });

  it('drops a frame it cannot read rather than printing it', () => {
    // An unrecognized message from a newer server must not end the session, and
    // printing the raw JSON into the pane would put text in front of the operator
    // that nothing ran.
    const { socket, seen } = open();
    socket.open();
    socket.onmessage?.({ data: 'not json at all' });
    socket.deliver({ type: 'data', data: '!!!not base64!!!' });
    socket.deliver({ type: 'something-new', data: btoa('x') });
    expect(seen).toHaveLength(0);
  });

  it('sends keystrokes as base64, including a character outside Latin-1', () => {
    // `btoa` throws on one of those, and a terminal is exactly where somebody
    // types one - so the text goes through a TextEncoder first.
    const { socket, handle } = open();
    socket.open();
    handle.send('é\r');
    const frame = bodyOf(socket.sent[0] ?? '');
    expect(frame.type).toBe('data');
    expect(new TextDecoder().decode(Uint8Array.from(atob(String(frame.data)), (c) => c.charCodeAt(0))))
      .toBe('é\r');
  });

  it('splits a paste past the server frame limit into frames under it, in order', () => {
    // The server closes a socket on a message over 1 MiB, so one paste
    // of a few megabytes sent as one frame would end the session it was pasted
    // into. Split, every frame is under the limit and the bytes arrive whole.
    const { socket, handle } = open();
    socket.open();
    const paste = 'é'.repeat(MAX_INPUT_BYTES) + 'tail';
    handle.send(paste);
    expect(socket.sent.length).toBe(3);
    const bytes: number[] = [];
    for (const payload of socket.sent) {
      expect(payload.length).toBeLessThan(1 << 20);
      const frame = bodyOf(payload);
      expect(frame.type).toBe('data');
      for (const c of atob(String(frame.data))) bytes.push(c.charCodeAt(0));
    }
    expect(new TextDecoder().decode(Uint8Array.from(bytes))).toBe(paste);
  });

  it('holds a keystroke typed before the handshake finished', () => {
    // A terminal is opened from a gesture and focused immediately, so the first
    // keystroke routinely arrives in the few milliseconds before `onopen`.
    const { socket, handle } = open();
    handle.send('l');
    expect(socket.sent).toHaveLength(0);
    socket.open();
    expect(socket.sent).toHaveLength(1);
    expect(bodyOf(socket.sent[0] ?? '').type).toBe('data');
  });

  it('sends a size asked for before the handshake, once, ahead of the keystrokes', () => {
    // The pane sends its first size in a macrotask after `fit`, which lands
    // before a WebSocket upgrade plus a Kubernetes exec can finish. Dropped, it
    // would leave `stty size` answering `0 0` inside a pod while xterm draws
    // 177 columns.
    const { socket, handle } = open();
    handle.resize(120, 40);
    handle.send('l');
    expect(socket.sent).toHaveLength(0);
    socket.open();
    expect(socket.sent.map((payload) => bodyOf(payload).type)).toEqual(['resize', 'data']);
    expect(bodyOf(socket.sent[0] ?? '')).toEqual({ type: 'resize', cols: 120, rows: 40 });
    // Once. A second open - there is none in a browser, but the guard is what
    // says the size is not a queue - sends nothing more.
    socket.onopen?.();
    expect(socket.sent).toHaveLength(2);
  });

  it('keeps only the last size asked for before the handshake', () => {
    // A size is a fact about now, not a keystroke to replay in order: the
    // ResizeObserver's own first callback lands in the same window as the
    // settle timer, and the shell should hear one size, the latest.
    const { socket, handle } = open();
    handle.resize(80, 24);
    handle.resize(177, 17);
    socket.open();
    expect(socket.sent).toHaveLength(1);
    expect(bodyOf(socket.sent[0] ?? '')).toEqual({ type: 'resize', cols: 177, rows: 17 });
  });

  it('still sends a resize at once when the socket is already open', () => {
    const { socket, handle } = open();
    socket.open();
    handle.resize(177, 18);
    expect(socket.sent).toHaveLength(1);
    expect(bodyOf(socket.sent[0] ?? '')).toEqual({ type: 'resize', cols: 177, rows: 18 });
  });

  it('never sends a zero-dimension resize', () => {
    // Zero in either dimension is what a browser reports for a hidden tab.
    // Applying it gives a 0x0 terminal and everything drawn while it was hidden
    // is lost.
    const { socket, handle } = open();
    socket.open();
    handle.resize(0, 40);
    handle.resize(120, 0);
    expect(socket.sent).toHaveLength(0);
    handle.resize(120, 40);
    expect(bodyOf(socket.sent[0] ?? '')).toEqual({ type: 'resize', cols: 120, rows: 40 });
  });
});

describe('how a session stops', () => {
  it('hands a refusal over with the server’s own sentence', () => {
    // **The whole reason this wire is framed.** A refusal written as a red line
    // inside the terminal output is one a person reads and a client cannot
    // branch on, so the session cap and the read-only fence are two
    // sentences that happen to differ rather than two answers.
    let got: { state: string; message: string | undefined } | null = null;
    openTerminalSocket(
      { channel: 'exec', cluster: 'prod' },
      {
        onData: () => {},
        onState: (state, reason) => { got = { state, message: reason?.message }; },
        onExit: () => {},
        onClosed: () => {},
      },
    );
    latest().open();
    latest().deliver({
      type: 'state',
      state: 'forbidden',
      reason: { code: 'verb_denied', message: 'prod is marked read-only' },
    });

    expect(got).toEqual({ state: 'forbidden', message: 'prod is marked read-only' });
  });

  it('reports an exit with no code as an exit with no code', () => {
    // The server cannot know one - the pseudo-terminal seam exposes no status on
    // either platform - and a pane printing "exited 0" from an absent number
    // would be inventing the one fact the operator came for.
    const codes: (number | undefined)[] = [];
    openTerminalSocket(
      { channel: 'terminal', cluster: 'prod' },
      {
        onData: () => {}, onState: () => {},
        onExit: (code) => codes.push(code),
        onClosed: () => {},
      },
    );
    latest().open();
    latest().deliver({ type: 'exit' });
    expect(codes).toEqual([undefined]);
  });

  it('keeps a close apart from a refusal, and claims no cause for it', () => {
    // A close carries nothing - no status, no body, not even whether the server
    // was reached. Routing it through `onState` would be claiming a cause.
    const events: string[] = [];
    openTerminalSocket(
      { channel: 'terminal', cluster: 'prod' },
      {
        onData: () => {},
        onState: () => events.push('state'),
        onExit: () => events.push('exit'),
        onClosed: () => events.push('closed'),
      },
    );
    latest().open();
    latest().onerror?.();
    latest().onclose?.();
    expect(events).toEqual(['closed']);
  });

  it('says nothing more once the caller has closed it', () => {
    // The operator shutting a tab is not the connection going away, and a pane
    // that drew "the connection went" over a tab the operator just closed would
    // be reporting its own gesture back at it.
    const events: string[] = [];
    const handle = openTerminalSocket(
      { channel: 'terminal', cluster: 'prod' },
      { onData: () => {}, onState: () => {}, onExit: () => {}, onClosed: () => events.push('closed') },
    );
    latest().open();
    handle.close();
    latest().onclose?.();
    expect(events).toEqual([]);
    expect(latest().closes).toBe(1);
  });
});


/**
 * The defect this pins, reported as *"claude marche pas"*: every assistant
 * session was refused with a plain `404 unknown cluster` **before the upgrade**,
 * because this module's own comment said the address carried nothing and the
 * first caller believed it.
 *
 * `internal/terminal/claude.go:580` resolves the cluster through `clusterParam`,
 * which reads the path value **or** the `cluster` query - and this address has no
 * path value. `:614-616` reads `kind`, `namespace` and `name` beside it.
 */
describe('the assistant is addressed in the query', () => {
  it('carries the cluster, without which the upgrade is refused', () => {
    const path = channelPath({ channel: 'assistant', cluster: 'prod' });
    expect(path).toContain('/api/ws/assistant?');
    expect(path).toContain('cluster=prod');
  });

  it('carries the object, which is what keeps two sessions from mixing', () => {
    const path = channelPath({
      channel: 'assistant',
      cluster: 'prod',
      namespace: 'argo',
      name: 'api-0',
      kind: 'Pod',
    });
    // The object keeps two sessions on two pods from mixing their context.
    expect(path).toContain('namespace=argo');
    expect(path).toContain('name=api-0');
    // The **Kind**, as a person writes it - the server validates it as one, and
    // the route's plural would pass validation while putting the wrong word in
    // the session's own CLAUDE.md.
    expect(path).toContain('kind=Pod');
  });

  it('still addresses the other three by path, and sends them no cluster query', () => {
    const exec = channelPath({ channel: 'exec', cluster: 'prod', namespace: 'argo', pod: 'api-0' });
    expect(exec).toBe('/api/c/prod/ws/exec?namespace=argo&pod=api-0');
    expect(exec).not.toContain('cluster=');
  });
});
