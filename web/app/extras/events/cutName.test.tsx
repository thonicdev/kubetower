import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { CutName, tailOf } from './parts';

describe('a name cut in the middle', () => {
  it('leaves a short name whole', () => {
    expect(tailOf('checkout-api-6f9c-q0z')).toBe('');
    const { container } = render(<CutName name="checkout-api" />);
    const name = container.querySelector('[data-cut-name]');
    expect(name?.textContent).toBe('checkout-api');
    expect(name?.children).toHaveLength(0);
  });

  it("keeps a pod's ReplicaSet and pod hashes as its end", () => {
    expect(tailOf('checkout-api-settlement-reconciliation-orchestrator-7f9c6d8b5-x2k9q')).toBe('-7f9c6d8b5-x2k9q');
  });

  it('keeps the last ten characters where the last segments are long', () => {
    expect(tailOf('a-name-whose-last-segments-are-both-unusually-longsegment-anotherlongone')).toBe('herlongone');
    expect(tailOf('averyveryverylongnamewithnohyphensatallreally')).toBe('tallreally');
  });

  it('draws the start and the end apart, the whole name as its text and tooltip', () => {
    const name = 'checkout-api-settlement-reconciliation-orchestrator-7f9c6d8b5-x2k9q';
    const { container } = render(<CutName name={name} />);
    const cut = container.querySelector('[data-cut-name]');
    expect(cut?.getAttribute('title')).toBe(name);
    expect(cut?.textContent).toBe(name);
    expect(cut?.children[0]?.className).toContain('truncate');
    expect(cut?.children[1]?.textContent).toBe('-7f9c6d8b5-x2k9q');
    expect(cut?.children[1]?.className).toContain('shrink-0');
  });
});
