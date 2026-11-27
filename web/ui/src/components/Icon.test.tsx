import { render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { mountApp, stubFetch } from '../testing/harness';
import { Icon, IconSprite, type IconName } from './Icon';

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Every glyph the sheet defines.
 *  Written out rather than derived from the module, because a test that reads
 *  its subject's own list asserts that the list equals itself. */
const EVERY_GLYPH: readonly IconName[] = [
  'terminal',
  'settings',
  'logs',
  'close',
  'forward',
  'back',
  'stop',
  'copy',
  'external',
  'claude',
  'resource',
  'volume',
  'secret',
  'config',
  'node',
  'refresh',
  'edit',
  'trash',
  'evict',
  'cordon',
  'uncordon',
  'drain',
  'pause',
  'play',
  'trigger',
  'scale',
  'cpu',
  'memory',
  'network',
  'disk',
  // The kinds menu's disclosure marker.
  'chevron',
  // A shape per kind and per group in that menu. Which kind draws
  // which of them is `shell/kindGlyph.test.ts`, against the server's own list;
  // this file only says the sheet defines them.
  'pod',
  'deployment',
  'replicaset',
  'statefulset',
  'daemonset',
  'job',
  'cronjob',
  'service',
  'ingress',
  'endpoint',
  'class',
  'policy',
  'access',
  'quota',
  'lease',
  'webhook',
  'account',
  'role',
  'binding',
  'namespace',
  'event',
  'workloads',
  'cluster',
  // The dashboard's row at the top of the cluster group.
  'overview',
  // A magnifier, drawn by nothing today.
  'search',
  // The list's filter control.
  'filter',
  // A list row's menu.
  'more',
  // The menu's Extra heading and every extra's entry.
  'extra',
  // Log out, in the account menu and at the rail's foot.
  'logout',
  // The pin on a confirmation.
  'lock',
  // a favorite kind, offered and made.
  'star',
  'starred',
  // Something could not be read in full.
  'warning',
  // One per shipped extra, on its card and in its About window.
  'certificate',
  'delivery',
  'database',
  'globe',
  'pulse',
  'coffee',
  'sliders',
  'clock',
  'topology',
  'hourglass',
  'layers',
  'shield',
  // About an extra.
  'info',
  'plus',
  'check',
  // A debug container.
  'debug',
];

describe('the sheet carries every glyph a control can ask for', () => {
  it('defines every one of them, by the ids a `use` asks for', () => {
    const { container } = render(<IconSprite />);
    const defined = [...container.querySelectorAll('symbol')].map((one) => one.id);

    // The length comes from the list above, which is still written out by hand -
    // so this is not the module asserting that it equals itself. What the spelt
    // number bought was nothing the list does not already say, and it cost a
    // second edit, in a second file, every time a glyph is added.
    expect(defined).toHaveLength(EVERY_GLYPH.length);
    for (const name of EVERY_GLYPH) {
      expect(defined).toContain(`i-${name}`);
    }
  });

  it('gives every symbol a viewBox, or a browser draws it at the wrong scale', () => {
    const { container } = render(<IconSprite />);
    for (const symbol of container.querySelectorAll('symbol')) {
      expect(symbol.getAttribute('viewBox')).toMatch(/^0 0 \d+ \d+$/);
    }
  });

  it('keeps the mark at its published size and fill, rather than redrawing it', () => {
    // `claude` is the one glyph that is not on the 24x24 stroked grid. If this
    // fails because somebody "tidied" it onto the grid, that is a redrawn
    // trademark and the tidy is the defect.
    //
    // **And the fill is the published color, not `currentColor`.** A mark
    // taking the color of whatever button it sits in is a modified mark.
    const { container } = render(<IconSprite />);
    const mark = container.querySelector('#i-claude');
    expect(mark?.getAttribute('viewBox')).toBe('0 0 248 248');
    // Named here rather than imported from the file under test: a brand color
    // compared against the constant that defines it asserts nothing.
    expect(mark?.querySelector('path')?.getAttribute('fill')).toBe('#D97757'); // style-allow: the published mark
  });

  it('paints nothing itself', () => {
    const { container } = render(<IconSprite />);
    // A sheet that is laid out is a row of shapes at the top of the console.
    expect(container.querySelector('svg')?.getAttribute('class')).toBe('hidden');
  });
});

describe('the Shell is what puts the sheet in the document', () => {
  it('mounts it once, so a `use` anywhere in the console resolves', async () => {
    stubFetch({
      '/api/session': {
        body: {
          identity: 'alice',
          idleExpiresAt: '2999-01-01T00:00:00Z',
          absoluteExpiresAt: '2999-01-01T00:00:00Z',
        },
      },
      '/api/clusters': { body: { state: 'empty', items: [] } },
    });

    mountApp('/');

    // Not "a sprite is rendered somewhere" - the assertion that matters is that
    // the id a `<use href="#i-...">` asks for resolves in this document, and
    // that there is exactly one of it. Two sheets means two elements with the
    // same id, which is the bug where an icon silently draws the wrong glyph.
    expect(document.querySelectorAll('#i-trash')).toHaveLength(1);
    expect(document.querySelectorAll('symbol')).toHaveLength(EVERY_GLYPH.length);
  });
});

describe('an icon is a shape, and the label belongs to the control', () => {
  it('points at the symbol of the name it was given', () => {
    const { container } = render(<Icon name="trash" />);
    expect(container.querySelector('use')?.getAttribute('href')).toBe('#i-trash');
  });

  it('draws each glyph the sheet defines as itself, never as the fallback', () => {
    for (const name of EVERY_GLYPH) {
      const { container, unmount } = render(<Icon name={name} />);
      expect(container.querySelector('use')?.getAttribute('href'), name).toBe(`#i-${name}`);
      unmount();
    }
  });

  it('draws the extra glyph for a name the sheet does not define', () => {
    const { container } = render(<Icon name={'nonexistent' as IconName} />);
    expect(container.querySelector('use')?.getAttribute('href')).toBe('#i-extra');
  });

  it('is hidden from the accessibility tree and is not a tab stop', () => {
    const { container } = render(<Icon name="drain" />);
    const svg = container.querySelector('svg');
    expect(svg?.getAttribute('aria-hidden')).toBe('true');
    expect(svg?.getAttribute('focusable')).toBe('false');
    // The one thing an icon must never do: name itself. Every control that uses
    // one carries its own label, which is what makes that safe.
    expect(svg?.textContent).toBe('');
  });

  it('keeps the stroke rules when a caller adds a class of their own', () => {
    const { container } = render(<Icon name="cpu" className="text-accent" />);
    const className = container.querySelector('svg')?.getAttribute('class') ?? '';
    expect(className).toContain('text-accent');
    // Appended, not replaced: a caller sizing one differently must not be able
    // to drop `fill-none` and get a black square.
    expect(className).toContain('fill-none');
    expect(className).toContain('stroke-current');
    expect(className).toContain('size-[var(--icon)]');
  });
});
