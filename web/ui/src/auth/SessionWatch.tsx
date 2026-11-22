import { useEffect, type ReactNode } from 'react';
import { useLocation } from 'react-router';

import { onSessionLapsed } from './lapse';
import { leaveFor } from './leave';
import { signInPath } from './next';

/**
 * Sends a lapsed session to the sign-in page, and remembers where it was.
 *
 * Rendered inside the Shell, which means it is mounted on every authenticated
 * route and on none of the anonymous ones - so the sign-in view's own 401, which
 * is an ordinary wrong password, cannot reach it. That is belt; the braces are
 * `meta: ANONYMOUS` on the sign-in mutation, because a route is a place and this
 * is a property of the request.
 *
 * The server's page, by a navigation of the whole document (`leaveFor`), and
 * replacing this entry, so Back does not return to a page that answered 401 and
 * 401s again.
 */
export function SessionWatch(): ReactNode {
  const location = useLocation();

  useEffect(() => {
    return onSessionLapsed(() => {
      leaveFor(signInPath(location));
    });
  }, [location]);

  return null;
}
