import type { ReactNode } from 'react';
import { useParams } from 'react-router';

import { StateNotice } from '../../state/StateNotice';
import type { ObjectIdentity } from './address';
import { ObjectDetail } from './ObjectDetail';

/**
 * One object, at an address of its own.
 *
 * An object has a route of its own, `/c/:cluster/:kind/:ns/:name`, and the drawer
 * is what a list opens beside itself. Both exist: the route is what survives a
 * reload and what a link points at.
 *
 * ## Two routes, and the shape decides
 *
 * `/c/:cluster/:kind/:name` is a cluster-scoped object and
 * `/c/:cluster/:kind/:namespace/:name` a namespaced one - the same pair the API
 * keeps. The *shape of the address* decides which, and nothing here infers a
 * scope from whether a string looks like a namespace: reading both as the
 * second would report a node's name as a namespace, and that answer would pass
 * every validator on its way to being stated as fact.
 *
 * So `params.namespace` being present **is** the cluster-scoped/namespaced
 * distinction. It is never defaulted, and an empty one is not the same as an
 * absent one.
 *
 * ## What this page does not do
 *
 * It does not open the drawer, and the drawer does not navigate here. They are
 * two ways to one object rather than one wrapping the other, so neither has to
 * know the other exists - which is what keeps the drawer's own rule intact:
 * closing it returns the user to exactly where they were, with the list
 * behind it still refreshing.
 */
export function ObjectPage(): ReactNode {
  const { cluster, kind, namespace, name } = useParams<{
    cluster: string;
    kind: string;
    namespace: string;
    name: string;
  }>();

  if (cluster === undefined || kind === undefined || name === undefined) {
    // Unreachable through the router, which matched these segments to get here,
    // and drawn rather than thrown: a typed address that somehow lands short
    // should say so, not blank the page. `absent` and not `unreadable` - nothing
    // failed to be read, there is no object named by this address. The code is
    // `unclassified` because that is what it is: none of the fifteen classified
    // reasons is about an address this console built wrongly, and borrowing one
    // would put a wrong cause in front of the user.
    return (
      <StateNotice
        state="absent"
        reason={{
          code: 'unclassified',
          message: 'This address does not name an object.',
        }}
      />
    );
  }

  const identity: ObjectIdentity =
    namespace === undefined ? { cluster, kind, name } : { cluster, kind, namespace, name };

  return <ObjectDetail identity={identity} chrome="page" />;
}
