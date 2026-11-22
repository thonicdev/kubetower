import { useRef, type ReactNode } from 'react';

import { decodeANSI } from '../dock/ansi';
import { focusRing } from '../focus';
import { CopyButton } from './CopyButton';

/**
 * A message somebody else wrote - a container's termination message, a
 * kubelet's waiting message, an event's - folded behind one control so that the
 * card it sits in keeps the height of its own facts.
 *
 * ## Why it folds
 *
 * A container that ends `Terminated: Error` with `terminationMessagePolicy:
 * FallbackToLogsOnError` has its message filled by the kubelet with the log's
 * tail - fifteen lines of timestamps, levels, a raw `[34m` and an emoji - and
 * a stack-trace dump pasted as one wrapped paragraph under the exit code is
 * not readable.
 *
 * So, closed, it is **one line**: the control, and beside it the one line of the
 * text most likely to be the answer, cut at the panel's edge with the whole line
 * in `title`. Opened, the whole text with its newlines, monospace, in a box that
 * scrolls on its own past twelve rem and breaks anywhere rather than scroll
 * sideways, with a copy control beside it.
 *
 * ## `<details>`, because the console already discloses that way
 *
 * The annotations on the details page and the rail's groups are `<details>`: the
 * keyboard (Enter and Space on the summary), the expanded state a screen reader
 * announces, and the closed content staying in the page for find-in-page all
 * come with the element and none of it has to be kept in step by hand.
 *
 * ## What it is cleaned of, and what it is not
 *
 * Escape sequences, whatever they were: a log tail is written for a terminal and
 * this is not one. Both spellings - the real ESC byte, through the dock's own
 * decoder, and the `[34m` an ESC leaves behind once something upstream dropped
 * the byte, which is what the screenshot showed. Nothing else: the text is the
 * application's, and what it said is what the operator came for.
 */

/**
 * An SGR whose ESC was lost before it got here: `[34m`, `[0m`, `[1;31m`. Digits
 * required, so `[m]` and `x[i]` in ordinary text are left alone - only the shape
 * a color code takes is removed.
 *
 * **And never one that a `]` closes**: `[5m]` is a PromQL range, `array[0m]`
 * an index, `retry in [5m]` a duration, and an event quoting `rate(x[5m])`
 * would be drawn as `rate(x])`. What an orphaned code is
 * followed by is the text it colored - `[34mNotices:`, logrus's
 * `[36mINFO[0m[0000]` - never the bracket that would close it.
 */
const ORPHAN_SGR = /\[\d{1,3}(?:;\d{1,3})*m(?!\])/g;

/** The blank lines before the first line with something on it. */
const LEADING_BLANK_LINES = /^(?:[ \t]*\n)+/;

/**
 * The text as it should be read: escapes gone, line endings one kind, and the
 * blank lines a log tail starts or ends with dropped, so the box neither opens
 * nor ends on an empty line and a one-line message is one line (review, F3).
 *
 * **Line by line** (review, F2). The dock's decoder is fed one line at a time,
 * and an OSC it finds unterminated runs to the end of what it was given - which,
 * handed the whole message, erased every line after it, the FATAL one included.
 * A termination message is the output of a process that died mid-write, so the
 * case is exactly this surface's.
 *
 * Pure, and exported so the cleaning is tested as a function rather than through
 * a rendering of it.
 */
export function cleanMessage(raw: string): string {
  return raw
    .split(/\r\n?|\n/)
    .map((line) => decodeANSI(line).text.replace(ORPHAN_SGR, ''))
    .join('\n')
    .replace(LEADING_BLANK_LINES, '')
    .trimEnd();
}

/**
 * A word that marks the line a reader is looking for. Case-blind; `fatal`,
 * `panic` and Rust's `panicked` as whole words, `error` and `exception` also as
 * the end of a class name (`IOException`, `RuntimeError`), and zerolog's console
 * levels `ERR` and `FTL`. The end must be a word's end, so `errors.go` in a
 * stack frame is not one.
 */
const TELLING = /\b(?:fatal|panic(?:ked)?|err|ftl)\b|(?:error|exception)\b/i;

/** klog's header for an error or a fatal line, `E1015 08:00:13.123456 …`:
 *  case matters here, so it is a pattern of its own. */
const KLOG_FAILED = /^[EF]\d{4} /;

/**
 * How much of the telling line the summary carries. `truncate` cuts it on
 * screen, but the summary's accessible name is its text, and a single-line
 * 4 KB JSON message read out on every focus is not a name (review, F5). The
 * whole line stays in `title` and in the opened box.
 */
const LINE_CAP = 200;

/**
 * The one line drawn closed: the **last** line that says it failed, else the
 * last line. A log tail ends where the process died - Trivy's `FATAL`, a Java
 * trace's last `Caused by:` - and the first line is usually the oldest thing in
 * it. Exported for the tests.
 */
export function tellingLine(text: string): string {
  const lines = text.split('\n').map((line) => line.trim()).filter((line) => line !== '');
  for (let at = lines.length - 1; at >= 0; at -= 1) {
    const line = lines[at];
    if (line !== undefined && (TELLING.test(line) || KLOG_FAILED.test(line))) return line;
  }
  return lines.at(-1) ?? '';
}

export interface FoldedMessageProps {
  /** The text as it arrived, escapes and all. */
  message: string;
  /** What the control says closed - `show error`, `show message`. Opened, its
   *  first word becomes `hide`. */
  label: string;
  /** Which message of several on one card this is - `state`, `last` - as
   *  `data-message-of`, for a test or a measure to tell them apart. */
  of?: string | undefined;
}

export function FoldedMessage({ message, label, of }: FoldedMessageProps): ReactNode {
  const body = useRef<HTMLPreElement>(null);
  const text = cleanMessage(message);
  if (text === '') return null;
  const line = tellingLine(text);
  const shown = line.length > LINE_CAP ? `${line.slice(0, LINE_CAP)}…` : line;
  const opened = label.replace(/^show\b/, 'hide');

  return (
    <details data-folded-message data-message-of={of} className="group w-full min-w-0">
      {/* `flex` is what removes the disclosure triangle in Chromium and Firefox
          (a summary that is not `list-item` has no marker); the WebKit
          pseudo-element is the one engine that keeps it regardless. */}
      <summary
        className={`flex min-w-0 cursor-pointer items-baseline gap-2 rounded-sm [&::-webkit-details-marker]:hidden ${focusRing}`}
      >
        <span
          data-folded-message-control
          className="shrink-0 rounded-sm border border-line px-1 text-2xs text-muted uppercase hover:text-fg"
        >
          <span className="group-open:hidden">{label}</span>
          <span className="hidden group-open:inline">{opened}</span>
        </span>
        <span data-folded-message-line title={line} className="min-w-0 truncate font-mono text-muted">
          {shown}
        </span>
      </summary>
      <div className="mt-1 flex items-start gap-2">
        {/* Focusable, so the keyboard can scroll it where the engine does not
            make a scroller focusable on its own - Safari - and so the ring is
            the console's rather than the user agent's (review, F4). */}
        <pre
          ref={body}
          data-folded-message-body
          tabIndex={0}
          className={`max-h-48 min-w-0 flex-1 overflow-y-auto rounded border border-line bg-panel-2 p-2 font-mono text-2xs whitespace-pre-wrap wrap-anywhere text-fg ${focusRing}`}
        >
          {text}
        </pre>
        <CopyButton value={text} label="Copy this message" selectRef={body} />
      </div>
    </details>
  );
}

/**
 * The rule every other surface applies to a message it did not write: **one line
 * reads where it stands** - a kubelet's `back-off 5m0s restarting failed
 * container`, an event's sentence - cleaned, and drawn by `inline` the way that
 * surface draws a sentence; **more than one line is output somebody pasted**,
 * and folds behind `label`.
 *
 * A terminated container's message does not go through this. It is the
 * application's own output whatever its length, and is folded always.
 */
export function Message({
  message,
  label,
  of,
  inline,
}: FoldedMessageProps & { inline: (text: string) => ReactNode }): ReactNode {
  const text = cleanMessage(message);
  if (text === '') return null;
  if (!text.includes('\n')) return inline(text);
  return <FoldedMessage message={text} label={label} of={of} />;
}
