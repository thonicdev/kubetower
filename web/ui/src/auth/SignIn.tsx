import { useEffect, type ReactNode } from 'react';
import { useLocation } from 'react-router';

import { leaveFor } from './leave';
import { SERVER_SIGN_IN_PATH } from './next';

/**
 * The single-page sign-in route, which draws nothing and hands over to the
 * server's sign-in page.
 *
 * There is one sign-in page and the server renders it: a browser with no
 * session is sent there by every navigation, before any of this bundle can
 * load, so it is the page a person meets anyway. This route only catches an
 * address written for the console - a bookmark, a link - and carries its query
 * (`next`, a single sign-on code) across unchanged; the server validates both.
 */
export function SignIn(): ReactNode {
  const { search } = useLocation();
  useEffect(() => {
    leaveFor(`${SERVER_SIGN_IN_PATH}${search}`);
  }, [search]);
  return null;
}
