import { useEffect, type ReactNode } from 'react';

import { installKeyboardStack } from './stack';

/**
 * Installs the application's one keyboard listener.
 *
 * Mounted above the router in `createApp`, so the stack exists on every route
 * and survives every navigation. It renders its children unchanged and holds no
 * state of its own - the registry is module level in `stack.ts`, for the reason
 * written there.
 */
export function KeyboardStack({ children }: { children: ReactNode }): ReactNode {
  useEffect(() => installKeyboardStack(), []);
  return children;
}
