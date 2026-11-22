import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router';
import { CloseButton } from './CloseButton';

describe('CloseButton', () => {
  it('is named on the control, hides its glyph, and closes on a click', () => {
    const onClick = vi.fn();
    render(<CloseButton onClick={onClick} label="Close the search" escape />);
    const button = screen.getByRole('button', { name: 'Close the search' });
    expect(button.getAttribute('title')).toBe('Close the search (Escape)');
    expect(button.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
    expect(button.textContent).toBe('');
    fireEvent.click(button);
    expect(onClick).toHaveBeenCalledOnce();
  });

  it('defaults to "Close", the toolbar square, and no Escape in its tooltip', () => {
    render(<CloseButton onClick={() => undefined} />);
    const button = screen.getByRole('button', { name: 'Close' });
    expect(button.getAttribute('title')).toBe('Close');
    expect(button.className).toContain('w-8');
  });

  it('is 24px inline, and 44px under a coarse pointer', () => {
    render(<CloseButton onClick={() => undefined} size="inline" />);
    const cls = screen.getByRole('button', { name: 'Close' }).className;
    expect(cls).toContain('size-6');
    expect(cls).toContain('pointer-coarse:size-11');
  });

  it('leads somewhere as a link with the same box and name, given `to`', () => {
    render(
      <MemoryRouter>
        <CloseButton to="/c/prod/x/kubecron" label="Close the full page" />
      </MemoryRouter>,
    );
    const link = screen.getByRole('link', { name: 'Close the full page' });
    expect(link.getAttribute('href')).toBe('/c/prod/x/kubecron');
    expect(link.className).toContain('w-8');
    expect(link.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
    expect(screen.queryByRole('button')).toBeNull();
  });
});
