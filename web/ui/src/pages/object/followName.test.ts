import { history, undo } from '@codemirror/commands';
import { EditorState, Transaction } from '@codemirror/state';
import { describe, expect, it } from 'vitest';

import { followName, objectName } from './followName';

const DEPLOYMENT = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: example-deployment
  namespace: shop
spec:
  selector:
    matchLabels:
      app: example-deployment
  template:
    metadata:
      labels:
        app: example-deployment
`;

function start(doc: string): EditorState {
  return EditorState.create({ doc, extensions: [history(), followName(doc)] });
}

/** Replaces the first `from` after `after` with `to`, as an edit by hand. */
function edit(state: EditorState, from: string, to: string, after = ''): EditorState {
  const doc = state.doc.toString();
  const at = doc.indexOf(from, after === '' ? 0 : doc.indexOf(after));
  expect(at).toBeGreaterThanOrEqual(0);
  return state.update({
    changes: { from: at, to: at + from.length, insert: to },
    annotations: Transaction.userEvent.of('input'),
  }).state;
}

const renamed = (state: EditorState, to: string): EditorState => edit(state, 'example-deployment', to, 'name:');

describe('a new object labels follow its name', () => {
  it('follows the rename on every line holding it', () => {
    const doc = renamed(start(DEPLOYMENT), 'web').doc.toString();
    expect(objectName(doc)).toBe('web');
    expect(doc).not.toContain('example-deployment');
    expect(doc.match(/app: web$/gm)).toHaveLength(2);
  });

  it('keeps following as the name is typed, through a name not finished yet', () => {
    let state = renamed(start(DEPLOYMENT), 'web-');
    expect(state.doc.toString()).toContain('app: web-\n');
    state = edit(state, 'name: web-', 'name: web-1');
    expect(state.doc.toString()).toContain('app: web-1\n');
  });

  it('waits through an empty name, and follows what comes next', () => {
    let state = renamed(start(DEPLOYMENT), '');
    expect(state.doc.toString()).toContain('app: example-deployment\n');
    state = edit(state, '  name: \n', '  name: web\n');
    expect(state.doc.toString().match(/app: web$/gm)).toHaveLength(2);
  });

  it('stops once the person has changed a label, and leaves the other alone', () => {
    let state = edit(start(DEPLOYMENT), 'app: example-deployment', 'app: mine');
    state = renamed(state, 'web');
    const doc = state.doc.toString();
    expect(doc).toContain('app: mine\n');
    expect(doc).toContain('app: example-deployment\n');
  });

  it('takes the name and the labels back in one undo', () => {
    let state = renamed(start(DEPLOYMENT), 'web');
    undo({ state, dispatch: (transaction) => (state = transaction.state) });
    expect(state.doc.toString()).toBe(DEPLOYMENT);
    // And it still follows afterwards: the undo is no edit of the labels.
    expect(renamed(state, 'api').doc.toString().match(/app: api$/gm)).toHaveLength(2);
  });

  it('follows on a StatefulSet serviceName too', () => {
    const doc = DEPLOYMENT.replace('kind: Deployment', 'kind: StatefulSet').replace(
      'spec:\n',
      'spec:\n  serviceName: example-deployment\n',
    );
    expect(renamed(start(doc), 'db').doc.toString()).toContain('serviceName: db\n');
  });

  it('leaves a selector that names another object', () => {
    const service = 'kind: Service\nmetadata:\n  name: example-service\nspec:\n  selector:\n    app: example-deployment\n';
    const doc = edit(start(service), 'example-service', 'web').doc.toString();
    expect(doc).toContain('name: web\n');
    expect(doc).toContain('app: example-deployment\n');
  });

  it('reads metadata.name at its own depth, not a deeper one', () => {
    expect(objectName('metadata:\n  labels:\n    name: deep\n  name: real\n')).toBe('real');
    expect(objectName('metadata:\n  labels:\n    name: deep\nspec:\n  name: other\n')).toBeUndefined();
    expect(objectName('metadata:\n  name: "quoted"\n')).toBeUndefined();
    expect(objectName(`metadata:\n  name: ${'a'.repeat(64)}\n`)).toBeUndefined();
  });
});
