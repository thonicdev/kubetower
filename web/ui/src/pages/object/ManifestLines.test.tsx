import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { ManifestLines } from './ManifestLines';

afterEach(cleanup);

/**
 * How a read-only manifest line wraps on a narrow screen. jsdom lays nothing
 * out, so what is pinned here is the markup the wrap rests on; the layout suite
 * (`measure/narrow.spec.ts`) reads the timestamp whole at 390px.
 */
describe('a manifest line on a narrow screen', () => {
  const lines = [
    {
      number: 1,
      tokens: [
        { text: '  creationTimestamp', class: 'key' as const },
        { text: ': ', class: 'punctuation' as const },
        { text: '"2026-11-05T23:23:47Z"', class: 'string' as const },
      ],
    },
    {
      number: 2,
      tokens: [
        { text: '  description', class: 'key' as const },
        { text: ': ', class: 'punctuation' as const },
        { text: 'a value of several words', class: 'plain' as const },
      ],
    },
  ];

  function token(text: string): HTMLElement {
    const found = [...document.querySelectorAll<HTMLElement>('[data-manifest-lines] span span')].find(
      (span) => span.textContent === text,
    );
    if (found === undefined) throw new Error(`no token ${text}`);
    return found;
  }

  it('wraps between words, never anywhere inside one', () => {
    render(<ManifestLines lines={lines} byLine={new Map()} className="" />);
    const line = token('"2026-11-05T23:23:47Z"').parentElement;
    expect(line?.className).not.toContain('break-all');
    expect(line?.className).toContain('wrap-anywhere');
  });

  it('keeps a token with no space in it on one line while it fits, a token of words free to wrap', () => {
    render(<ManifestLines lines={lines} byLine={new Map()} className="" />);
    // A browser breaks after a hyphen: `2026-11-` and `05T23…` on two lines.
    // As its own box a token moves to the next line whole, and wraps inside
    // only where it is wider than the line.
    expect(token('"2026-11-05T23:23:47Z"').className).toContain('inline-block');
    expect(token('"2026-11-05T23:23:47Z"').className).toContain('max-w-full');
    expect(token('a value of several words').className).not.toContain('inline-block');
    expect(token('  creationTimestamp').className).not.toContain('inline-block');
  });
});
