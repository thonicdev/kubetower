import { describe, expect, it } from 'vitest';

import { externalHref, internalHref } from './externalHref';

/**
 * Five anchors in the console leave it, and the address in three of them
 * is written by somebody else - an Ingress host a cluster tenant chose, a
 * release page GitHub named. The renderer, not the producer, decides what may
 * become a link.
 */
describe('externalHref', () => {
  it('lets an http or https address through as it was written', () => {
    expect(externalHref('https://shop.example.com/path?q=1')).toBe('https://shop.example.com/path?q=1');
    expect(externalHref('http://127.0.0.1:30080')).toBe('http://127.0.0.1:30080');
    expect(externalHref('HTTPS://Example.com')).toBe('HTTPS://Example.com');
  });

  it('refuses every other scheme, and anything that is not an address', () => {
    for (const refused of [
      'javascript:alert(1)',
      ' javascript:alert(1)',
      'JaVaScRiPt:alert(1)',
      'data:text/html,<script>alert(1)</script>',
      'vbscript:msgbox(1)',
      'file:///etc/passwd',
      '//evil.example.com',
      '/c/prod/pods',
      'shop.example.com',
      '',
      undefined,
    ]) {
      expect(externalHref(refused), String(refused)).toBeUndefined();
    }
  });

  it('refuses an http address that needed trimming, which the parser would have trimmed', () => {
    // `new URL` strips the spaces and reads `https:`; only the trim check
    // refuses it (review of #80: no test reached that branch).
    expect(externalHref('https://shop.example.com ')).toBeUndefined();
    expect(externalHref('\thttps://shop.example.com')).toBeUndefined();
  });
});

describe('internalHref', () => {
  it('lets a path on this origin through, and nothing else', () => {
    expect(internalHref('/c/prod/nodes/node-3')).toBe('/c/prod/nodes/node-3');
    for (const refused of ['https://example.com', '//example.com', 'javascript:alert(1)', 'c/prod', '', undefined]) {
      expect(internalHref(refused), String(refused)).toBeUndefined();
    }
  });
});
