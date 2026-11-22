import type { ReactNode } from 'react';
import { useLocation } from 'react-router';

import { backOf, withTab } from './back';
import { BackArrow } from './PageHeader';

/**
 * A page's *Back*, at the left of its header: to the
 * view the page was opened from when the console opened it (`back.ts`), else
 * to `fallback`, the object's list as the operator left it.
 *
 * A link and not `history.back()`: what it returns to is an address the page
 * knows, so a middle-click opens it too, and a page whose previous entry is
 * somewhere else entirely still lands on the list.
 *
 * `tab`, when the page has tabs, is the one it shows now: the view it returns
 * to opens its drawer on it rather than on the tab the page was opened from.
 * It changes only a way back the console recorded; the fallback has no drawer
 * to open.
 */
export function BackLink({ fallback, tab }: { fallback: string; tab?: string }): ReactNode {
  const { state } = useLocation();
  const recorded = backOf(state);
  return (
    <BackArrow
      to={recorded === undefined ? fallback : tab === undefined ? recorded : withTab(recorded, tab)}
      data-page-back=""
    />
  );
}
