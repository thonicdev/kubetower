import { describe, expect, it } from 'vitest';

import { middleTruncate, shortCluster } from './truncate';

/**
 * `middleTruncate` has to be `internal/web/rail.go`'s, rune for rune.
 *
 * Two implementations cannot be compared as text across languages, so this pins
 * the **arithmetic** instead, with the cases the Go function's own comment
 * names as the wrong ones.
 */
describe('middleTruncate', () => {
  it('leaves a name that fits exactly as it is', () => {
    expect(middleTruncate('prod-eks', 22)).toBe('prod-eks');
    expect(middleTruncate('exactly-eleven', 14)).toBe('exactly-eleven');
  });

  it('keeps both ends, because both carry meaning', () => {
    // The failure the function exists for: three tiles reading STAGING-E… and
    // three reading …KS-1_OWNER were both shipped, and both were wrong.
    const a = middleTruncate('STAGING-EKS-1', 11);
    const b = middleTruncate('STAGING-EKS-1_OWNER', 11);
    expect(a).not.toBe(b);
    expect(a.startsWith('STAG')).toBe(true);
    expect(a.endsWith('KS-1')).toBe(true);
    expect(b.endsWith('OWNER')).toBe(true);
  });

  it('reproduces the Go arithmetic: a 2/5 head, a 3/5 tail, one ellipsis', () => {
    // fit 11 → tail = floor(10*3/5) = 6, head = 11-1-6 = 4.
    const out = middleTruncate('abcdefghijklmnop', 11);
    expect(out).toBe('abcd…klmnop');
    expect([...out]).toHaveLength(11);
  });

  it('never returns more runes than it was asked for', () => {
    for (const fit of [2, 3, 5, 8, 11, 20, 22, 26]) {
      const out = middleTruncate('a'.repeat(200), fit);
      expect([...out].length, `fit ${fit}`).toBeLessThanOrEqual(fit);
    }
  });

  /**
   * The load-bearing half. `String.prototype.slice` is a UTF-16 code-unit slice
   * and cuts an astral character in half, leaving a lone surrogate that renders
   * as a replacement character; Go slices `[]rune`.
   */
  it('does not cut a character in half', () => {
    const name = '🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀';
    const out = middleTruncate(name, 7);
    expect([...out]).toHaveLength(7);
    expect(out).not.toContain('�');
    // Every rune that is not the ellipsis is a whole rocket.
    for (const rune of [...out]) {
      expect(rune === '…' || rune === '🚀').toBe(true);
    }
  });

  it('counts a name in runes and not in code units', () => {
    // Twelve code points, twenty-four code units. A length test on `.length`
    // would truncate a name that fits.
    const name = '🚀'.repeat(12);
    expect(middleTruncate(name, 12)).toBe(name);
  });
});

describe('shortCluster', () => {
  it('cuts an ARN at its last slash before truncating', () => {
    // Middle-truncating the whole ARN keeps the account number and loses the
    // cluster, which is the one part the operator recognizes.
    const arn = 'arn:aws:eks:eu-west-1:123456789012:cluster/example-prod-eks';
    expect(shortCluster(arn)).toBe('example-prod-eks');
  });

  it('leaves a plain context name alone', () => {
    expect(shortCluster('minikube')).toBe('minikube');
  });

  it('still truncates a long name that has no slash', () => {
    const out = shortCluster('a'.repeat(60));
    expect([...out]).toHaveLength(24);
    expect(out).toContain('…');
  });
});
