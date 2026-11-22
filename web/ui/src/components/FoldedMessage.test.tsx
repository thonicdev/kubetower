import { fireEvent, render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { cleanMessage, FoldedMessage, Message, tellingLine } from './FoldedMessage';

/**
 * A Trivy container's termination message - the log tail
 * `FallbackToLogsOnError` hands back. Fabricated: the timestamps,
 * the four levels, one real ESC-colored line, one whose ESC was lost, an emoji.
 */
const TRIVY_TAIL = [
  '2026-10-15T07:59:58Z\tINFO\t[vulndb] Need to update DB',
  '2026-10-15T07:59:58Z\tINFO\t[vulndb] Downloading vulnerability DB...',
  '2026-10-15T07:59:59Z\tWARN\t[vulndb] Retrying download... attempt=1',
  '\x1b[34mNotices:\x1b[0m',
  '[34mNotices:[0m',
  ' - Version 0.76.0 of Trivy is now available 🎉',
  '2026-10-15T08:00:01Z\tERROR\t[vulndb] Failed to download artifact',
  '2026-10-15T08:00:01Z\tFATAL\tFatal error\tinit error: DB error: failed to download vulnerability DB',
  '',
].join('\n');

describe('a message is cleaned of what a terminal would have read', () => {
  it('drops real escape sequences and the [34m an ESC leaves behind, and nothing else', () => {
    const clean = cleanMessage(TRIVY_TAIL);
    expect(clean).not.toContain('\x1b');
    expect(clean).not.toMatch(/\[\d+m/);
    expect(clean.split('\n')[3]).toBe('Notices:');
    expect(clean.split('\n')[4]).toBe('Notices:');
    // The application's own text, brackets and emoji included, is kept.
    expect(clean).toContain('[vulndb] Need to update DB');
    expect(clean).toContain('🎉');
    expect(cleanMessage('items[i] and [m] stay')).toBe('items[i] and [m] stay');
  });

  it('keeps one kind of line ending and drops the trailing newline a tail ends with', () => {
    expect(cleanMessage('one\r\ntwo\rthree\n\n')).toBe('one\ntwo\nthree');
  });
});

describe('the line drawn while folded', () => {
  it('is the last line that says it failed', () => {
    expect(tellingLine(cleanMessage(TRIVY_TAIL))).toBe(
      '2026-10-15T08:00:01Z\tFATAL\tFatal error\tinit error: DB error: failed to download vulnerability DB',
    );
    expect(tellingLine('Exception in thread main\n\tat a.B(B.java:1)\nCaused by: java.io.IOException: gone\n\tat c.D(D.java:2)')).toBe(
      'Caused by: java.io.IOException: gone',
    );
  });

  it('is the last line where none says so, and a frame in errors.go is not one', () => {
    expect(tellingLine('starting\nlistening on :8080\n\n')).toBe('listening on :8080');
    expect(tellingLine('main.f()\n\t/src/errors.go:12')).toBe('/src/errors.go:12');
  });
});

describe('a folded message', () => {
  it('is closed by default: one control and one line, the whole line on hover', () => {
    const { container } = render(<FoldedMessage message={TRIVY_TAIL} label="show error" />);
    const details = container.querySelector('details[data-folded-message]');
    expect(details).not.toBeNull();
    expect(details?.hasAttribute('open')).toBe(false);
    expect(details?.querySelector('[data-folded-message-control]')?.textContent).toContain('show error');
    const line = details?.querySelector('[data-folded-message-line]');
    expect(line?.textContent).toContain('FATAL');
    expect(line?.getAttribute('title')).toBe(line?.textContent);
    expect(line?.className).toContain('truncate');
  });

  it('opens on a click, holding the whole text cleaned, monospace, kept newlines, bounded, with a copy control', () => {
    const { container } = render(<FoldedMessage message={TRIVY_TAIL} label="show error" />);
    const details = container.querySelector('details[data-folded-message]');
    const summary = details?.querySelector('summary');
    if (summary === null || summary === undefined) throw new Error('no summary');
    fireEvent.click(summary);
    expect(details?.hasAttribute('open')).toBe(true);
    const body = details?.querySelector('[data-folded-message-body]');
    expect(body?.textContent).toBe(cleanMessage(TRIVY_TAIL));
    expect(body?.className).toContain('font-mono');
    expect(body?.className).toContain('whitespace-pre-wrap');
    expect(body?.className).toContain('wrap-anywhere');
    expect(body?.className).toContain('max-h-48');
    expect(body?.className).toContain('overflow-y-auto');
    expect(details?.querySelector('[data-copy-state]')?.getAttribute('aria-label')).toBe('Copy this message');
    fireEvent.click(summary);
    expect(details?.hasAttribute('open')).toBe(false);
  });

  it('opens from the keyboard: the control is a native summary, with the focus ring', () => {
    // jsdom does not run a summary's keyboard activation; Chromium does, and
    // `measure/termination.spec.ts` presses Enter on it there. What can be checked
    // here is that it is the element the browser gives that behavior to.
    const { container } = render(<FoldedMessage message={'a\nb'} label="show error" />);
    const summary = container.querySelector('details[data-folded-message] > summary');
    expect(summary).not.toBeNull();
    expect(summary?.className).toContain('focus-visible:outline');
  });

  it('says hide once opened, as a swap of the closed word', () => {
    const { container } = render(<FoldedMessage message={'a\nb'} label="show message" />);
    const control = container.querySelector('[data-folded-message-control]');
    expect(control?.textContent).toBe('show messagehide message');
    expect(control?.querySelector('.group-open\\:hidden')?.textContent).toBe('show message');
  });

  it('draws nothing at all for a message that is empty once cleaned', () => {
    const { container } = render(<FoldedMessage message={'\x1b[0m\n'} label="show error" />);
    expect(container.querySelector('[data-folded-message]')).toBeNull();
  });
});

describe('a message somebody else wrote, anywhere else', () => {
  it('stands as a sentence on one line, cleaned, with no control', () => {
    const { container } = render(
      <Message message={'\x1b[31mback-off 5m0s restarting failed container\x1b[0m'} label="show message" inline={(text) => <p data-said>{text}</p>} />,
    );
    expect(container.querySelector('[data-said]')?.textContent).toBe('back-off 5m0s restarting failed container');
    expect(container.querySelector('[data-folded-message]')).toBeNull();
  });

  it('folds once it runs to several lines', () => {
    const { container } = render(
      <Message message={'MountVolume.SetUp failed\nOutput: mount.nfs: access denied'} label="show error" inline={(text) => <p data-said>{text}</p>} />,
    );
    expect(container.querySelector('[data-said]')).toBeNull();
    expect(container.querySelector('[data-folded-message-line]')?.textContent).toBe('Output: mount.nfs: access denied');
  });

  it('stays one line when the only other lines are blank ones before it (review, F3)', () => {
    const { container } = render(
      <Message message={'\n\nBack-off restarting failed container'} label="show message" inline={(text) => <p data-said>{text}</p>} />,
    );
    expect(container.querySelector('[data-folded-message]')).toBeNull();
    expect(container.querySelector('[data-said]')?.textContent).toBe('Back-off restarting failed container');
  });
});

/** The edges of cleaning and folding a message. */
describe('the edge cases', () => {
  it('keeps a bracket a `]` closes - a PromQL range, an index, a duration', () => {
    for (const text of [
      'value = array[0m] here',
      'the wall is [12m] high; retry in [5m]',
      'query sum(rate(http_requests_total{job="api"}[5m])) by (le) is over budget',
    ]) {
      expect(cleanMessage(text)).toBe(text);
    }
  });

  it('still strips the orphaned codes Trivy and logrus leave', () => {
    expect(cleanMessage('[34mNotices:[0m')).toBe('Notices:');
    expect(cleanMessage('[36mINFO[0m[0000] starting')).toBe('INFO[0000] starting');
    expect(cleanMessage('[1;31mFATAL[0m boom')).toBe('FATAL boom');
  });

  it('an unterminated OSC costs its own line and never the lines after it', () => {
    const clean = cleanMessage('line1\n\x1b]0;title without terminator\nline3\nFATAL boom');
    expect(clean).toBe('line1\n\nline3\nFATAL boom');
    expect(tellingLine(clean)).toBe('FATAL boom');
  });

  it('drops the blank lines a message starts with, and keeps the first line\'s indent', () => {
    expect(cleanMessage('\n \n  first\nsecond')).toBe('  first\nsecond');
  });

  it('the opened box is a keyboard stop with the console\'s ring', () => {
    const { container } = render(<FoldedMessage message={'a\nb'} label="show error" />);
    const body = container.querySelector('[data-folded-message-body]');
    expect(body?.getAttribute('tabindex')).toBe('0');
    expect(body?.className).toContain('focus-visible:outline');
  });

  it('the summary carries at most 200 characters of the line; title and box carry all of it', () => {
    const json = `{"level":"error","blob":"${'x'.repeat(4000)}"}`;
    const { container } = render(<FoldedMessage message={json} label="show error" />);
    const line = container.querySelector('[data-folded-message-line]');
    expect(line?.textContent).toHaveLength(201);
    expect(line?.textContent?.endsWith('…')).toBe(true);
    expect(line?.getAttribute('title')).toBe(json);
    expect(container.querySelector('[data-folded-message-body]')?.textContent).toBe(json);
    // A line under the cap is drawn whole, with no ellipsis.
    const short = render(<FoldedMessage message={'a\nFATAL boom'} label="show error" />).container;
    expect(short.querySelector('[data-folded-message-line]')?.textContent).toBe('FATAL boom');
  });

  it("Rust's panicked, zerolog's ERR and FTL, and klog's E and F headers are telling lines", () => {
    expect(tellingLine("thread 'main' panicked at src/main.rs:4:5:\nboom\nnote: run with `RUST_BACKTRACE=1`")).toBe(
      "thread 'main' panicked at src/main.rs:4:5:",
    );
    expect(tellingLine('8:00AM INF starting\n8:00AM ERR dial failed\n8:00AM INF retrying')).toBe('8:00AM ERR dial failed');
    expect(tellingLine('8:00AM INF starting\n8:00AM FTL cannot start\nbye')).toBe('8:00AM FTL cannot start');
    expect(tellingLine('I1015 08:00:12.1 1 main.go:1] up\nE1015 08:00:13.2 1 main.go:9] lost lease\nI1015 08:00:14.3 1 main.go:2] bye')).toBe(
      'E1015 08:00:13.2 1 main.go:9] lost lease',
    );
    // An `E` that is not klog's header is not one.
    expect(tellingLine('E2E suite\nall green')).toBe('all green');
  });
});
