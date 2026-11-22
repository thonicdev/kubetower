import { cleanup, render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';

import type { Ref } from '../../api';
import { RefLink } from './RefLink';

/**
 * `fill` mode cuts a long name on screen and puts the whole name in `title`.
 * The name is the hover and the server's hint is the link's accessible name, so
 * "Open ip-10-0-142-37.eu-west-3.compute.internal" - the sentence the contract
 * carries for every object link - is not lost.
 */
const EKS = 'ip-10-0-142-37.eu-west-3.compute.internal';

function show(element: ReactNode): void {
  const router = createMemoryRouter([{ path: '/', element }], { initialEntries: ['/'] });
  render(<RouterProvider router={router} />);
}

afterEach(cleanup);

describe('RefLink', () => {
  it('in fill mode keeps the whole name on hover and the hint as the accessible name', () => {
    const reference: Ref = { text: EKS, href: `/c/prod/nodes/${EKS}`, hint: `Open ${EKS}` };
    show(<RefLink reference={reference} fill />);

    const link = screen.getByRole('link', { name: `Open ${EKS}` });
    expect(link.getAttribute('title')).toBe(EKS);
    expect(link.textContent).toBe(EKS);
  });

  it('in fill mode with no hint is named by its text', () => {
    const reference: Ref = { text: EKS, href: `/c/prod/nodes/${EKS}` };
    show(<RefLink reference={reference} fill />);

    const link = screen.getByRole('link', { name: EKS });
    expect(link.getAttribute('title')).toBe(EKS);
    expect(link.hasAttribute('aria-label')).toBe(false);
  });

  it('outside fill mode is unchanged: the hint is the hover and no aria-label is added', () => {
    const reference: Ref = { text: 'worker-01', href: '/c/prod/nodes/worker-01', hint: 'Open worker-01' };
    show(<RefLink reference={reference} />);

    const link = screen.getByRole('link', { name: 'worker-01' });
    expect(link.getAttribute('title')).toBe('Open worker-01');
    expect(link.hasAttribute('aria-label')).toBe(false);
  });
});
