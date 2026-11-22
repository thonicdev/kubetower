// @vitest-environment jsdom
import { fireEvent, render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { BackArrow, PageHeader } from './PageHeader';

function draw(ui: ReactNode) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

describe('PageHeader', () => {
  it('draws the kind, the name and the namespace as three levels', () => {
    draw(<PageHeader kind="Pod" name="api-0" namespace="shop" heading="h1" />);
    const heading = screen.getByRole('heading', { level: 1, name: 'api-0' });
    expect(heading.className).toContain('font-semibold');
    expect(document.querySelector('[data-header-kind]')?.textContent).toBe('Pod');
    expect(document.querySelector('[data-header-context]')?.textContent).toBe('shop');
  });

  it('draws no context line without a namespace, and adds the cluster when given', () => {
    const { unmount } = draw(<PageHeader kind="Node" name="n1" />);
    expect(document.querySelector('[data-header-context]')).toBeNull();
    unmount();
    draw(<PageHeader kind="Pod" name="api-0" namespace="shop" cluster="dev" />);
    expect(document.querySelector('[data-header-context]')?.textContent).toBe('shop · dev');
  });

  it('keeps the way back before the title and the actions after it', () => {
    const onBack = vi.fn();
    draw(
      <PageHeader
        back={<BackArrow onClick={onBack} />}
        kind="Pod"
        name="api-0"
        actions={<button type="button">Restart</button>}
      />,
    );
    const header = document.querySelector('[data-page-header]') as HTMLElement;
    expect([...header.children].map((c) => c.tagName)).toEqual(['BUTTON', 'DIV', 'BUTTON']);
    expect(screen.getByRole('button', { name: 'Restart' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Back' }));
    expect(onBack).toHaveBeenCalledTimes(1);
  });
});

describe('BackArrow', () => {
  it('is a link with a tooltip and a label when given an address, with no frame', () => {
    draw(<BackArrow to="/c/dev/pods" />);
    const link = screen.getByRole('link', { name: 'Back' });
    expect(link.getAttribute('href')).toBe('/c/dev/pods');
    expect(link.getAttribute('title')).toBe('Back');
    expect(link.className).not.toContain('border');
    expect(link.textContent).toBe('');
  });
});
