import { describe, expect, it } from 'vitest';

import type { ListPage, Row } from '../../api';
import { identityOf, listPath, objectPath } from './address';

/**
 * A cluster-scoped and a namespaced object have different URL shapes, and the
 * two places where they can be confused.
 *
 * Reading both shapes as the namespaced one would report **a node's name as a
 * namespace**, an answer that passes every validator, reaches `kubectl -n`, and
 * is then stated as fact. The second place is a list's namespace parameter,
 * which is a selection: read raw, it could address a namespace called
 * `argocd,kube-system`, a value that passes every validator, reaches the API
 * server, and comes back "not found" for an object plainly visible behind the
 * drawer.
 *
 * Both failures are silent and plausible, which is why these assert the refusal
 * rather than the happy path alone.
 */

const namespaced: ListPage = {
  kind: 'pods',
  label: 'Pods',
  namespaced: true,
  columns: [],
  rows: [],
};

const clusterScoped: ListPage = {
  kind: 'nodes',
  label: 'Nodes',
  namespaced: false,
  columns: [],
  rows: [],
};

/**
 * A row, with the two members that decide an identity written explicitly.
 *
 * Built with conditional spreads rather than by overriding a literal, because
 * `exactOptionalPropertyTypes` is on and `{ namespace: undefined }` is a type
 * error against `namespace?: string` - which is the compiler holding the same
 * line this module does: an absent namespace and an empty one are not the same
 * fact.
 */
function row(over: { name?: string | undefined; namespace?: string | undefined } = {}): Row {
  const name = 'name' in over ? over.name : 'storefront-web';
  const namespace = 'namespace' in over ? over.namespace : 'shop-prod';
  return {
    id: 'x',
    cells: {},
    ...(name === undefined ? {} : { name }),
    ...(namespace === undefined ? {} : { namespace }),
  };
}

describe('which URL shape', () => {
  it('puts a namespaced object under its namespace', () => {
    const id = identityOf('prod', namespaced, row());
    expect(id).toEqual({
      cluster: 'prod',
      kind: 'pods',
      namespace: 'shop-prod',
      name: 'storefront-web',
    });
    expect(objectPath({ cluster: 'prod', kind: 'pods', namespace: 'shop-prod', name: 'storefront-web' })).toBe(
      '/c/prod/pods/shop-prod/storefront-web',
    );
  });

  it('gives a cluster-scoped object one trailing segment, and that segment is its name', () => {
    const id = identityOf('prod', clusterScoped, row({ name: 'node-1', namespace: undefined }));
    // The absence of a namespace is asserted rather than its emptiness: an empty
    // string would build `/c/prod/nodes//node-1`, and a defaulted one would
    // build `/c/prod/nodes/node-1/…` - which is the shape that would report a node's
    // name as a namespace.
    expect(id).toEqual({ cluster: 'prod', kind: 'nodes', name: 'node-1' });
    expect(objectPath({ cluster: 'prod', kind: 'nodes', name: 'node-1' })).toBe('/c/prod/nodes/node-1');
  });

  it('reads the payload flag and never the row, so a namespaced kind whose row has no namespace opens nothing', () => {
    // The contract says so in as many words on `ListPage.namespaced`: "the front
    // end reads this rather than guessing from whether a row has a namespace".
    // A client that guessed would put this row at /c/prod/pods/storefront-web,
    // an address that resolves to the cluster-scoped handler.
    expect(identityOf('prod', namespaced, row({ namespace: undefined }))).toBeUndefined();
    expect(identityOf('prod', namespaced, row({ namespace: '' }))).toBeUndefined();
  });

  it('refuses a cluster-scoped row that carries a namespace rather than dropping it', () => {
    // Dropping it would produce a URL that works and answers about a different
    // object than the row describes.
    expect(identityOf('prod', clusterScoped, row({ name: 'node-1' }))).toBeUndefined();
  });
});

describe('an ambiguous identity opens nothing', () => {
  it('refuses a namespace that is a selection rather than a name', () => {
    // A list's namespace selection read raw. A comma cannot occur inside a DNS-1123
    // label, which is what makes the joined form detectable at all.
    expect(identityOf('prod', namespaced, row({ namespace: 'argocd,kube-system' }))).toBeUndefined();
  });

  it('refuses a row with no name', () => {
    expect(identityOf('prod', namespaced, row({ name: undefined }))).toBeUndefined();
    expect(identityOf('prod', namespaced, row({ name: '' }))).toBeUndefined();
  });

  it('refuses a name carrying a separator, which would address a different depth', () => {
    expect(identityOf('prod', namespaced, row({ name: 'a/b' }))).toBeUndefined();
  });

  it('refuses when there is no cluster', () => {
    expect(identityOf(undefined, namespaced, row())).toBeUndefined();
  });
});

describe('the cluster is carried, never re-judged', () => {
  it('opens an object on an EKS context whose name holds a slash', () => {
    // `arn:aws:eks:...:cluster/prod` is an ordinary context name. Applying the
    // row's "names exactly one" rule to the cluster as well would refuse to open
    // any object on it, on a list that had just drawn.
    expect(identityOf('arn:aws:eks:eu-west-1:1234:cluster/prod', namespaced, row())).toEqual({
      cluster: 'arn:aws:eks:eu-west-1:1234:cluster/prod',
      kind: 'pods',
      namespace: 'shop-prod',
      name: 'storefront-web',
    });
  });

  it('refuses no cluster at all', () => {
    expect(identityOf('', namespaced, row())).toBeUndefined();
  });
});

describe('every segment is encoded', () => {
  it('encodes what a context name, a kind and a name may actually hold', () => {
    expect(
      objectPath({ cluster: 'a b', kind: 'certificates.cert-manager.io', namespace: 'n', name: 'x#1' }),
    ).toBe('/c/a%20b/certificates.cert-manager.io/n/x%231');
  });
});

describe('the object in its list', () => {
  it('names the list, the namespace and the object, and no namespace for a cluster-scoped kind', () => {
    // The namespace is the list's own parameter, so the list
    // arrives narrowed; `open` is what it reads on arrival and then drops.
    expect(listPath({ cluster: 'prod', kind: 'replicasets', namespace: 'shop-prod', name: 'api-7c9' })).toBe(
      '/c/prod/replicasets?namespace=shop-prod&open=api-7c9',
    );
    // Absent rather than empty - `?namespace=` would ask for a namespace called
    // "", which is the namespace/name confusion on the list rather than on the object.
    expect(listPath({ cluster: 'prod', kind: 'nodes', name: 'node-1' })).toBe('/c/prod/nodes?open=node-1');
  });

  it('encodes the path segments and the query separately', () => {
    // A context name with a slash is a path segment; a name with a hash is a
    // query value. The two encodings differ and a single one gets one of them
    // wrong. The cluster is spelt by `clusterPath`: `:` as
    // it is, `/` escaped.
    expect(
      listPath({ cluster: 'arn:aws:eks:eu-west-1:1234:cluster/prod', kind: 'pods', namespace: 'n', name: 'x#1' }),
    ).toBe('/c/arn:aws:eks:eu-west-1:1234:cluster%2Fprod/pods?namespace=n&open=x%231');
  });
});
