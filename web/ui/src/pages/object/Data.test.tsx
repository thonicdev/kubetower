import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyLayer } from '../../keyboard/layers';
import { claimKey, resetKeyboardStack } from '../../keyboard/stack';
import { mountApp, stubFetch, type StubRoutes } from '../../testing/harness';

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } };
const OBJECT = '/api/c/prod/r/secrets/argo/creds';

const detail = {
  body: {
    state: 'ok',
    data: {
      kind: 'secrets',
      namespace: 'argo',
      name: 'creds',
      title: 'Secret',
      // **The keys are an address and not a tab**: they are
      // drawn inside the details panel, between the facts and Related. A fixture
      // still declaring a `data` tab would be a server this client no longer
      // has, and every test below would pass against it by mounting `?tab=data`.
      data: '/api/c/prod/data?kind=secrets',
      // What the server declares on a Secret besides them: its changes and its
      // document - and **no metrics**, which is a pod's and a node's.
      tabs: [
        { key: 'changes', label: 'Changes', endpoint: '/api/c/prod/changes?kind=secrets' },
        { key: 'manifest', label: 'Manifest', endpoint: '/api/c/prod/manifest?kind=secrets' },
      ],
      sections: [],
    },
  },
};

function shell(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/c/prod/nav': { body: { state: 'ok', data: { groups: [] } } },
    '/api/capabilities': { body: { mode: 'desktop' } },
    [OBJECT]: detail,
    ...extra,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

function control(text: string): HTMLButtonElement {
  const found = [...page().querySelectorAll('button')].find((one) => one.textContent?.includes(text));
  if (found === undefined) throw new Error(`no control saying ${text}`);
  return found;
}

function controlOrNull(text: string): HTMLButtonElement | null {
  return [...page().querySelectorAll('button')].find((one) => one.textContent?.includes(text)) ?? null;
}

/** Every address the page asked for, in order, by wrapping the harness's stub
 *  rather than replacing it: the harness builds a real `Response`. Call it
 *  after `stubFetch` and before `mountApp`. */
function recording(): string[] {
  const asked: string[] = [];
  const stubbed = globalThis.fetch;
  vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
    asked.push(typeof input === 'string' ? input : String(input));
    return stubbed(input, init);
  });
  return asked;
}

const secret = {
  body: {
    state: 'ok',
    data: {
      kind: 'secrets',
      namespace: 'argo',
      name: 'creds',
      resourceVersion: '4821',
      editable: true,
      entries: [
        { key: 'password', masked: true },
        { key: 'note', masked: false, value: 'not a secret' },
      ],
    },
  },
};

/**
 * Opening a ConfigMap or a Secret directly shows its keys, a reveal and an
 * edit: the object endpoint declares the address of the keys.
 */
describe('a secret opened directly shows its keys', () => {
  it('lists them, and holds the masked value back until it is asked for', async () => {
    stubFetch(shell({ '/api/c/prod/data': secret }));
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    const text = page().textContent ?? '';
    expect(text).toContain('note');
    // The ConfigMap-shaped key is inline; the masked one is not, and says so
    // rather than drawing an empty box.
    expect(text).toContain('not a secret');
    expect(text).toContain('Held back until you ask for it');
  });

  it('badges the key masked while it is held back, revealed while it is shown', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: 'hunter2' } } },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    const badge = (): string | null | undefined => page().querySelector('[data-mask]')?.textContent;
    await waitFor(() => {
      expect(badge()).toBe('masked');
    });

    fireEvent.click(control('Show'));
    await waitFor(() => {
      expect(page().textContent).toContain('hunter2');
    });
    expect(badge()).toBe('revealed');
    expect(page().querySelector('[data-mask]')?.getAttribute('data-mask')).toBe('revealed');

    fireEvent.click(control('Hide'));
    expect(badge()).toBe('masked');
  });

  it('reveals one key through the one door, and shows what came back', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: 'hunter2' } } },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Show'));

    await waitFor(() => {
      expect(page().textContent).toContain('hunter2');
    });
  });

  it('treats a revealed empty value as a value, not as nothing', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: '' } } },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Show'));

    // The collapse this contract exists to remove, on the one payload where
    // being wrong hides or exposes a credential: `ok` with an empty string is a
    // revealed empty value, and the row must stop saying it is held back.
    await waitFor(() => {
      expect(page().textContent).not.toContain('Held back until you ask for it');
    });
  });

  it('says why a reveal was refused, rather than staying blank', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': {
          body: {
            state: 'forbidden',
            reason: { code: 'verb_denied', message: 'this account may not read secrets' },
          },
        },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Show'));

    await waitFor(() => {
      expect(page().textContent).toContain('this account may not read secrets');
    });
  });
});

/** A long value, past 400 characters; distinct at
 *  both ends so that a prefix and the whole cannot be confused. */
const LONG = `begin-${'x'.repeat(600)}-end`;

/**
 * On a Secret, *Show* draws a long value whole, with no *Expand* (a second
 * click for what the first one asked for), and *Edit* does not open the
 * textarea on the inline value, which a masked key does not carry, leaving a
 * blank box to edit.
 */
describe('a secret value, once asked for, is whole and is what gets edited', () => {
  it('draws the whole value on Show, with no Expand control', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: LONG } } },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Show'));

    await waitFor(() => {
      expect(page().textContent).toContain('begin-');
    });
    // The whole value and not a prefix: the one click was the request for it.
    expect(page().querySelector('pre')?.textContent).toBe(LONG);
    expect(controlOrNull('Expand')).toBeNull();
    expect(page().querySelector('pre')?.className).toContain('overflow-auto');
    expect(page().querySelector('pre')?.className).toContain('max-h-[60vh]');
  });

  it('reveals through the one door on Edit, and opens the textarea on the answer', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: 'hunter2' } } },
      }),
    );
    const asked = recording();
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    // No Show first. The masked row's own Edit control is the first one drawn.
    fireEvent.click(control('Edit'));

    await waitFor(() => {
      expect(page().querySelector('textarea')).not.toBeNull();
    });
    // The same door, once: one request, which is the one audit line asked
    // for - and the textarea already holds what came back.
    const reveals = asked.filter((path) => path.startsWith('/api/c/prod/env'));
    expect(reveals).toHaveLength(1);
    expect(new URLSearchParams(reveals[0]?.split('?')[1] ?? '').get('key')).toBe('password');
    expect((page().querySelector('textarea') as HTMLTextAreaElement).value).toBe('hunter2');
  });

  it('refuses the edit when the reveal was refused, with the same reason', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': {
          body: {
            state: 'forbidden',
            reason: { code: 'verb_denied', message: 'this account may not read secrets' },
          },
        },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Edit'));

    await waitFor(() => {
      expect(page().textContent).toContain('this account may not read secrets');
    });
    // No blank box to edit: an edit that cannot see the value is refused, not
    // opened on nothing.
    expect(page().querySelector('textarea')).toBeNull();
  });

  it('draws a ConfigMap value whole, in a block that scrolls rather than clips', async () => {
    // The acceptance fixture: a 200-line script, one line of 300 characters.
    const script = Array.from({ length: 200 }, (_, i) =>
      i === 99 ? `long-${'y'.repeat(295)}` : `echo line-${String(i + 1)}`,
    ).join('\n');
    expect(script.split('\n')).toHaveLength(200);
    expect(script.split('\n')[99]).toHaveLength(300);
    stubFetch(
      shell({
        '/api/c/prod/r/configmaps/argo/settings': {
          body: {
            state: 'ok',
            data: {
              ...detail.body.data,
              kind: 'configmaps',
              name: 'settings',
              title: 'ConfigMap',
              data: '/api/c/prod/data?kind=configmaps',
              tabs: [],
            },
          },
        },
        '/api/c/prod/data': {
          body: {
            state: 'ok',
            data: {
              kind: 'configmaps',
              namespace: 'argo',
              name: 'settings',
              resourceVersion: '12',
              editable: true,
              entries: [{ key: 'nginx.conf', masked: false, value: script }],
            },
          },
        },
      }),
    );
    mountApp('/c/prod/configmaps/argo/settings');

    await waitFor(() => {
      expect(page().textContent).toContain('nginx.conf');
    });
    // Whole, with no Expand: a long value scrolls inside its own block.
    const pre = page().querySelector('pre');
    expect(pre?.textContent).toBe(script);
    expect(pre?.textContent).toContain('echo line-200');
    expect(controlOrNull('Expand')).toBeNull();
    expect(pre?.className).toContain('overflow-auto');
    expect(pre?.className).toContain('max-h-[60vh]');
  });
});

/**
 * Once shown, a value must not stay on screen until the drawer closes. The
 * gesture is one-per-key and needs a way back - a user who checks a password
 * and then shares their screen needs a control to press.
 */
describe('a revealed value can be hidden again', () => {
  it('takes the value out of the DOM on Hide, and asks nothing', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: 'hunter2' } } },
      }),
    );
    const asked = recording();
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Show'));
    await waitFor(() => {
      expect(page().textContent).toContain('hunter2');
    });

    fireEvent.click(control('Hide'));

    expect(page().textContent).not.toContain('hunter2');
    expect(page().textContent).toContain('Held back until you ask for it');
    // Nothing was re-fetched: the audit line was written on Show, and that is
    // the record. The door is offered again, and pressing it is a new reveal.
    expect(asked.filter((path) => path.startsWith('/api/c/prod/env'))).toHaveLength(1);
    expect(controlOrNull('Hide')).toBeNull();
    expect(controlOrNull('Show')).not.toBeNull();
  });
});

describe('editing is preview-then-apply, and nothing else writes', () => {
  it('previews first, and posts only the token the plan carried', async () => {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/data/preview': {
          body: {
            token: 'plan-77',
            expiresAt: '2999-01-01T00:00:00Z',
            sentence: 'Replace the value of note.',
            facts: { verb: 'update', targets: [], count: 1, destructive: true, precondition: '4821' },
          },
        },
      }),
    );
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') {
        posted.push({ path, body: JSON.parse(String(init.body)) });
      }
      return stubbed(input, init);
    });

    mountApp('/c/prod/secrets/argo/creds');
    await waitFor(() => {
      expect(page().textContent).toContain('note');
    });

    fireEvent.click([...page().querySelectorAll('button')].filter((one) => one.textContent?.includes('Edit'))[1]!);
    fireEvent.change(page().querySelector('textarea')!, { target: { value: 'changed' } });
    fireEvent.click(control('Save…'));

    // The dialog, from the plan. The server's sentence is drawn and no second
    // one is composed here.
    await waitFor(() => {
      expect(document.body.textContent).toContain('Replace the value of note.');
    });
    // The precondition sentence is not drawn; the request still carries it.
    expect(document.body.textContent).not.toContain('Sent with');

    // Nothing has been written yet: the preview touches nothing.
    expect(posted.map((one) => one.path)).toEqual(['/api/c/prod/data/preview']);
    expect(posted[0]?.body).toMatchObject({
      resourceVersion: '4821',
      entries: [{ key: 'note', value: 'changed', delete: false }],
    });
  });

  it('disables the control and says which setting refuses, on an object that cannot be edited', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': {
          body: {
            state: 'ok',
            data: {
              ...secret.body.data,
              editable: false,
              editableNote: 'this object is marked immutable, so its values cannot be changed',
            },
          },
        },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('marked immutable');
    });
    // Disabled rather than absent: a user looking for the control finds it,
    // and the note says what to change. The two causes are different people's.
    expect(control('Edit').hasAttribute('disabled')).toBe(true);
  });

  it('draws Edit disabled with the reason where the console may not patch', async () => {
    const reason =
      "you may, but this console may not: its ServiceAccount is not granted patch on secrets in argo";
    stubFetch(
      shell({
        '/api/c/prod/data': {
          body: { state: 'ok', data: { ...secret.body.data, editable: false, editableNote: reason } },
        },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('this console may not');
    });
    const edit = control('Edit');
    expect(edit.hasAttribute('disabled')).toBe(true);
    expect(edit.getAttribute('title')).toBe(reason);
  });

  it('draws no Edit for a person who may not patch the object', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': { body: { state: 'ok', data: { ...secret.body.data, editable: false } } },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('not a secret');
    });
    expect(controlOrNull('Edit')).toBeNull();
  });
});


/**
 * The component named `Confirm` claims the confirmation layer, not `Dialog`.
 *
 * It draws `plan.sentence`, the plan's warnings and its precondition, and it
 * branches on `facts.destructive`: it is a confirmation by every property it
 * has. Without a `layer` it would take the `Dialog` default - the layer the
 * manifest editor sits on, and the one `layers.ts` says a confirmation must
 * out-rank so that answering a question does not dismiss what asked it. That
 * would be invisible only while nothing raises this from inside another
 * dialog.
 *
 * **Both registration orders.** The confirmation opens last, so within a shared
 * layer "whatever opened last wins" hands it the key anyway - the first
 * assertion below is green on the defect and proves nothing on its own. The
 * reverse order is the one that reads the numbers.
 */
describe('the save confirmation claims the confirm layer, not the dialog one', () => {
  const plan = {
    body: {
      token: 'plan-77',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Replace the value of note.',
      facts: { verb: 'update', targets: [], count: 1, destructive: true, precondition: '4821' },
    },
  };

  function dialog(): HTMLElement | null {
    const found = document.querySelector('[role="dialog"]');
    return found instanceof HTMLElement ? found : null;
  }

  async function asked(): Promise<void> {
    stubFetch(shell({ '/api/c/prod/data': secret, '/api/c/prod/data/preview': plan }));
    mountApp('/c/prod/secrets/argo/creds');
    await waitFor(() => {
      expect(page().textContent).toContain('note');
    });
    fireEvent.click(
      [...page().querySelectorAll('button')].filter((one) => one.textContent?.includes('Edit'))[1]!,
    );
    fireEvent.change(page().querySelector('textarea')!, { target: { value: 'changed' } });
    fireEvent.click(control('Save…'));
    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });
  }

  it('takes Escape from a dialog registered before it', async () => {
    const under = vi.fn(() => true);
    const release = claimKey(KeyLayer.Dialog, under);
    await asked();

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() => {
      expect(dialog()).toBeNull();
    });
    expect(under).not.toHaveBeenCalled();
    release();
  });

  it('takes Escape from a dialog registered after it', async () => {
    await asked();
    // The editor this confirmation would be raised from inside of, registering
    // after it: on a shared layer the newest claimant wins, so Escape would
    // close the editor and leave its own question on screen.
    const under = vi.fn(() => true);
    const release = claimKey(KeyLayer.Dialog, under);

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() => {
      expect(dialog()).toBeNull();
    });
    expect(under).not.toHaveBeenCalled();
    release();
  });
});

/**
 * A ConfigMap and a Secret land on Details like every other kind, and the keys
 * are **in** that panel. So the property worth pinning is not which tab is
 * selected but that opening the object the ordinary way puts the keys on
 * screen with no tab named and no second click: the keys are visible from a
 * bare address.
 */
describe('a ConfigMap and a Secret show their keys on the page they open on', () => {
  it('draws them with no tab named, and declares no tab for them either', async () => {
    stubFetch(shell({ '/api/c/prod/data': secret }));
    // No `?tab=` at all: the ordinary way a row opens.
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    // Details, which is every kind's landing tab and the empty address.
    expect(page().querySelector('#object-tab-details')?.getAttribute('aria-selected')).toBe('true');
    // And the strip has no Data at all: two ways to reach one section is the
    // state to avoid.
    expect(page().querySelector('#object-tab-data')).toBeNull();
  });

  it('puts them under their own heading, above Related', async () => {
    stubFetch(shell({ '/api/c/prod/data': secret }));
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    // The heading is what makes the section attributable in the middle of a
    // details page - and what makes an empty answer say what is empty.
    const block = page().querySelector('[data-section="data"]');
    expect(block).not.toBeNull();
    expect(block?.querySelector('h3')?.textContent).toBe('Data');
  });

  it('still honors a tab named in the address, so a reload lands where it was', async () => {
    stubFetch(shell({ '/api/c/prod/data': secret, '/api/c/prod/manifest': { body: { state: 'ok', data: { lines: [] } } } }));
    mountApp('/c/prod/secrets/argo/creds?tab=manifest');

    await waitFor(() => {
      expect(page().querySelector('#object-tab-manifest')?.getAttribute('aria-selected')).toBe('true');
    });
  });
});

/**
 * The one door reveals text. Asked for a key whose value is not text, answering
 * `{key, value: ''}` would be a *revealed empty value*, which the header of
 * `Data.tsx` calls "a real and confusing thing to meet in a Secret" - a
 * keystore would be drawn exactly like a key somebody had emptied. The server
 * refuses that key (`unreadable` / `undecodable`); this row's job is never to
 * ask.
 *
 * `binary` is the same fact said in advance, from the same flag the refusal is
 * computed from, so the panel and the door cannot disagree about which keys are
 * text.
 */
describe('a key whose value is not text is drawn as binary', () => {
  const keystore = {
    body: {
      state: 'ok',
      data: {
        kind: 'secrets',
        namespace: 'argo',
        name: 'creds',
        resourceVersion: '4821',
        editable: true,
        entries: [{ key: 'keystore.jks', masked: true, binary: true }],
      },
    },
  };

  it('says binary, offers no reveal, and never asks the door', async () => {
    stubFetch(shell({ '/api/c/prod/data': keystore }));
    const asked = recording();
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('keystore.jks');
    });

    const text = page().textContent ?? '';
    // The key is named as binary and said to be unshowable - not drawn as an
    // empty box, and not drawn as a key waiting to be asked for.
    expect(text).toContain('binary');
    expect(text).toContain('Binary, not shown.');
    expect(text).not.toContain('Held back until you ask for it');

    // No control that would fail after the click: the door refuses this key, and
    // a textarea opened on bytes is how a keystore gets destroyed by somebody
    // asking to see it.
    expect(controlOrNull('Show')).toBeNull();
    expect(controlOrNull('Edit')).toBeNull();

    // And nothing asked. The reveal is an audit line in a shared profile, so a
    // request this row can only be refused for is a line that should not exist.
    expect(asked.filter((path) => path.startsWith('/api/c/prod/env'))).toHaveLength(0);
  });

  it('draws the refusal if the door is reached anyway, rather than a blank value', async () => {
    // The panel's flag and the door's refusal are two halves of one fact, and a
    // client that only had the second still has to show something. A Secret whose
    // panel says nothing about `binary` - an older server, a hand-made request -
    // reaches the door and gets the refusal; the row draws the sentence.
    const onlyMasked = {
      body: {
        state: 'ok',
        data: { ...secret.body.data, entries: [{ key: 'password', masked: true }] },
      },
    };
    stubFetch(
      shell({
        '/api/c/prod/data': onlyMasked,
        '/api/c/prod/env': {
          body: {
            state: 'unreadable',
            reason: {
              code: 'undecodable',
              message:
                '"password" is not text — 8 B that does not decode to UTF-8. This console reveals text; read it with kubectl and a pipe.',
            },
          },
        },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Show'));

    await waitFor(() => {
      expect(page().textContent).toContain('is not text');
    });
    // The refusal is not an answer: no value is drawn, and the row does not
    // claim the key was revealed and found empty.
    expect(page().querySelector('pre')).toBeNull();
  });
});

/**
 * The client's half of the data contract. An empty value is empty, and a value
 * nobody withheld must not say it was withheld.
 *
 * The server must not omit `value` for a ConfigMap key set to `""`; these two
 * tests pin the client's side of the same contract, because a payload can be
 * right and a panel can still draw it wrong:
 *
 *  - given `value: ''`, the row must draw a **legible** empty value. The `<pre>`
 *    alone renders as nothing at all, which puts a value that is empty and a
 *    panel that failed to draw back into one face: an empty answer and a failed
 *    answer must be drawn differently.
 *  - given no `value` and `masked: false`, the row must not say *held back*.
 *    The only key that reaches that state is a ConfigMap's `binaryData`, and nothing is being withheld from anybody there.
 */
const configMap = {
  body: {
    state: 'ok',
    data: {
      kind: 'configmaps',
      namespace: 'argo',
      name: 'creds',
      resourceVersion: '4821',
      editable: true,
      entries: [
        { key: 'A_EMPTY', masked: false, value: '' },
        { key: 'B_BLOB', masked: false },
        { key: 'C_SET', masked: false, value: '5432' },
      ],
    },
  },
};

describe('an empty value is empty, and an absent one says which absence', () => {
  it('draws an empty value legibly rather than as an invisible box', async () => {
    stubFetch(shell({ '/api/c/prod/data': configMap }));
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('A_EMPTY');
    });

    const row = [...page().querySelectorAll('li')].find((one) =>
      one.textContent?.includes('A_EMPTY'),
    );
    if (row === undefined) throw new Error('no row for A_EMPTY');

    // Nothing is being withheld: this is a ConfigMap and the value arrived.
    expect(row.textContent).not.toContain('Held back until you ask for it');
    // And the emptiness is said, not left to an 8px sliver of background.
    expect(row.textContent).toContain('empty');
    // It is still a value, so it still wears the value's surface.
    expect(row.querySelector('pre')).not.toBeNull();
  });

  it('does not claim a value was held back when nothing is masked', async () => {
    stubFetch(shell({ '/api/c/prod/data': configMap }));
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('B_BLOB');
    });

    const row = [...page().querySelectorAll('li')].find((one) =>
      one.textContent?.includes('B_BLOB'),
    );
    if (row === undefined) throw new Error('no row for B_BLOB');

    expect(row.textContent).not.toContain('Held back until you ask for it');
    // It offers no Show either, because there is no door to press for it.
    expect([...row.querySelectorAll('button')].map((b) => b.textContent)).not.toContain('Show');
  });

  it('still says held back on a masked key, which is the one that means it', async () => {
    stubFetch(shell({ '/api/c/prod/data': secret }));
    mountApp('/c/prod/secrets/argo/creds');

    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });

    const row = [...page().querySelectorAll('li')].find((one) =>
      one.textContent?.includes('password'),
    );
    if (row === undefined) throw new Error('no row for password');
    expect(row.textContent).toContain('Held back until you ask for it');
  });
});

/**
 * What a data save leaves behind in the cache.
 *
 * The Manifest tab is one of the two places a user checks what they just
 * did, and the other one is the tab they were already on. If `Data.tsx`
 * invalidated `queryKeys.data` and nothing else, the manifest - a second read of
 * the same object, on a key that shares no prefix with the first - would keep
 * the document the cluster held *before* the save, and the two tabs would
 * disagree with the wrong one looking authoritative.
 *
 * **The window is `staleTime`.** Tabs unmount (`ObjectDetail` renders one panel,
 * not a hidden stack), `createQueryClient` sets `staleTime: 5_000`, and a remount
 * past that refetches on its own. What is left is the five seconds after the
 * manifest was last read - the gesture *save and immediately go and look* - plus
 * the user who then sits on the tab, for whom nothing corrects it at all: no
 * interval, no mount, nothing.
 *
 * So the test does the fast gesture, and counts. The assertion is the object's
 * second read happening; nothing here measures a layout, which in jsdom would be
 * a measurement of zero.
 */
describe('saving a key leaves the manifest tab holding the old document', () => {
  const before = {
    state: 'ok',
    data: {
      resourceVersion: '4821',
      lines: [
        {
          number: 1,
          tokens: [
            { text: '  note', class: 'key' },
            { text: ': ', class: 'punctuation' },
            { text: 'not a secret', class: 'string' },
          ],
        },
      ],
    },
  };

  const after = {
    state: 'ok',
    data: {
      resourceVersion: '4822',
      lines: [
        {
          number: 1,
          tokens: [
            { text: '  note', class: 'key' },
            { text: ': ', class: 'punctuation' },
            { text: 'changed', class: 'string' },
          ],
        },
      ],
    },
  };

  const plan = {
    body: {
      token: 'plan-77',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Replace the value of note.',
      facts: { verb: 'update', button: 'Save', targets: [], count: 1, destructive: true },
    },
  };

  const done = {
    succeeded: 1,
    failed: 0,
    summary: '1 updated.',
    results: [{ target: { kind: 'secrets', namespace: 'argo', name: 'creds' }, ok: true }],
  };

  /**
   * The cluster, answering the manifest differently once the write has been
   * posted - which is what a cluster does. Returns the addresses the manifest was
   * asked at: one on opening the tab, and a second one only if something
   * invalidated it.
   */
  function cluster(): string[] {
    const asked: string[] = [];
    let saved = false;
    stubFetch(shell({ '/api/c/prod/data': secret, '/api/c/prod/data/preview': plan }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const address = String(
        typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url,
      );
      const path = address.split('?')[0] ?? address;
      if (init?.method === 'POST' && path === '/api/c/prod/data') {
        saved = true;
        return Promise.resolve(
          new Response(JSON.stringify(done), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      if (path !== '/api/c/prod/manifest') return stubbed(input, init);
      asked.push(path);
      return Promise.resolve(
        new Response(JSON.stringify(saved ? after : before), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    });
    return asked;
  }

  function tab(key: string): HTMLButtonElement {
    const found = document.getElementById(`object-tab-${key}`);
    if (!(found instanceof HTMLButtonElement)) throw new Error(`no ${key} tab`);
    return found;
  }

  it('re-reads the manifest after the save, so the two tabs agree', async () => {
    const asked = cluster();
    mountApp('/c/prod/secrets/argo/creds?tab=manifest');

    // Read the document first, the way a user who is about to change
    // something does. Without this the manifest is never in the cache and there
    // is nothing stale to draw.
    await waitFor(() => {
      expect(page().textContent).toContain('not a secret');
    });
    expect(asked).toHaveLength(1);

    // **Details, not Data** - the keys live in that panel: the save is made
    // from the Details tab and still owes the manifest a re-read.
    fireEvent.click(tab('details'));
    await waitFor(() => {
      expect(page().textContent).toContain('note');
    });

    fireEvent.click(
      [...page().querySelectorAll('button')].filter((one) => one.textContent?.includes('Edit'))[1]!,
    );
    fireEvent.change(page().querySelector('textarea')!, { target: { value: 'changed' } });
    fireEvent.click(control('Save…'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Replace the value of note.');
    });
    const apply = [...document.querySelectorAll('[role="dialog"] button')].find(
      (one) => one.textContent?.trim() === 'Save',
    );
    if (!(apply instanceof HTMLButtonElement)) throw new Error('the dialog offers no Save');
    fireEvent.click(apply);
    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });

    // Back to the manifest, without reloading anything.
    fireEvent.click(tab('manifest'));

    await waitFor(() => {
      expect(page().textContent).toContain('changed');
    });
    // Two reads: the one that opened the tab, and the one the save owed.
    expect(asked).toHaveLength(2);
    expect(page().textContent).not.toContain('not a secret');
  });
});

/**
 * A search over the keys. The browser's own find
 * must miss a Secret's value that nobody revealed, which is not in the page -
 * so this one is told, by the panel that already draws by the same rule, which
 * values are there to be searched. A ConfigMap's value is drawn whole, and
 * searched whole.
 */
describe('the search over the keys', () => {
  function box(): HTMLInputElement {
    const found = page().querySelector('input[aria-label="Find in this data"]');
    if (!(found instanceof HTMLInputElement)) throw new Error('no find box');
    return found;
  }

  /** The `n/m` beside the box - `n/m+` when the marks were capped. Only the
   *  stepper's arrows and the spaces are dropped, so the `+` is asserted too. */
  function count(): string {
    const next = page().querySelector('[aria-label="Next match"]');
    return next?.parentElement?.textContent?.replace(/[‹›\s]/g, '') ?? '';
  }

  function current(): string {
    return [...page().querySelectorAll('[data-find-current]')].map((one) => one.textContent).join('');
  }

  /** Ctrl+F with the keyboard nowhere in particular, which is where a click on
   *  the page's text leaves it. `false` when the search took it. */
  function ctrlF(): boolean {
    return fireEvent.keyDown(document.body, { key: 'f', ctrlKey: true });
  }

  const configMap = (value: string): StubRoutes =>
    configMapOf([
      { key: 'mode', masked: false, value: 'debug' },
      { key: 'nginx.conf', masked: false, value },
    ]);

  const configMapOf = (entries: { key: string; masked: false; value: string }[]): StubRoutes => ({
    '/api/c/prod/r/configmaps/argo/settings': {
      body: {
        state: 'ok',
        data: {
          ...detail.body.data,
          kind: 'configmaps',
          name: 'settings',
          title: 'ConfigMap',
          data: '/api/c/prod/data?kind=configmaps',
          tabs: [],
        },
      },
    },
    '/api/c/prod/data': {
      body: {
        state: 'ok',
        data: {
          kind: 'configmaps',
          namespace: 'argo',
          name: 'settings',
          resourceVersion: '12',
          editable: true,
          entries,
        },
      },
    },
  });

  it('matches a masked value by its key alone, and never asks the door', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        // There, so that a search that knocked would be answered - and seen.
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: 'hunter2' } } },
      }),
    );
    const asked = recording();
    mountApp('/c/prod/secrets/argo/creds');
    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });

    expect(ctrlF()).toBe(false);
    fireEvent.change(box(), { target: { value: 'hunter' } });
    await waitFor(() => {
      expect(count()).toBe('0/0');
    });
    // Not matched, not revealed, not fetched.
    expect(page().textContent).not.toContain('hunter2');
    expect(asked.some((path) => path.startsWith('/api/c/prod/env'))).toBe(false);
    expect(page().textContent).toContain('Held back until you ask for it');

    // The key is searched, and so is a ConfigMap-shaped value beside it.
    fireEvent.change(box(), { target: { value: 'PASS' } });
    await waitFor(() => {
      expect(count()).toBe('1/1');
    });
    expect(current()).toBe('pass');
    fireEvent.change(box(), { target: { value: 'secret' } });
    await waitFor(() => {
      expect(current()).toBe('secret');
    });
    expect(count()).toBe('1/1');
    expect(asked.some((path) => path.startsWith('/api/c/prod/env'))).toBe(false);
  });

  it('searches a value once it is revealed, and stops on Hide', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/env': { body: { state: 'ok', data: { key: 'password', value: 'hunter2' } } },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds');
    await waitFor(() => {
      expect(page().textContent).toContain('password');
    });
    fireEvent.click(control('Show'));
    await waitFor(() => {
      expect(page().textContent).toContain('hunter2');
    });

    fireEvent.click(page().querySelector('button[aria-label="Search this data"]')!);
    fireEvent.change(box(), { target: { value: 'hunter' } });
    await waitFor(() => {
      expect(count()).toBe('1/1');
    });
    expect(current()).toBe('hunter');

    // Hidden again, it is out of the page and out of the search with it.
    fireEvent.click(control('Hide'));
    expect(count()).toBe('0/0');
    expect(page().querySelector('[data-find-hit]')).toBeNull();
  });

  it('counts the matches across keys and values, and steps through them', async () => {
    stubFetch(shell(configMap('debug=true')));
    mountApp('/c/prod/configmaps/argo/settings');
    await waitFor(() => {
      expect(page().textContent).toContain('nginx.conf');
    });

    expect(ctrlF()).toBe(false);
    fireEvent.change(box(), { target: { value: 'de' } });
    // "mode", "debug", and "debug=true": key before value, row after row.
    await waitFor(() => {
      expect(count()).toBe('1/3');
    });
    expect(page().querySelectorAll('[data-find-hit]')).toHaveLength(3);
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('2/3');
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('3/3');
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('1/3');
    fireEvent.keyDown(box(), { key: 'Enter', shiftKey: true });
    expect(count()).toBe('3/3');

    // Escape closes the bar and clears the marks.
    fireEvent.keyDown(box(), { key: 'Escape' });
    expect(page().querySelector('input[aria-label="Find in this data"]')).toBeNull();
    expect(page().querySelector('[data-find-hit]')).toBeNull();
  });

  it('finds a match anywhere in a long value, which is drawn whole', async () => {
    stubFetch(shell(configMap(LONG)));
    mountApp('/c/prod/configmaps/argo/settings');
    await waitFor(() => {
      expect(page().textContent).toContain('nginx.conf');
    });

    expect(ctrlF()).toBe(false);
    fireEvent.change(box(), { target: { value: '-' } });
    await waitFor(() => {
      expect(count()).toBe('1/2');
    });
    expect(page().querySelectorAll('pre')[1]?.textContent).toBe(LONG);
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('2/2');
    expect(page().textContent).not.toContain('match folded');
  });

  // A value is drawn whole, so a one-character
  // search over a value of a megabyte would mark every character of it - a
  // million nodes, and a tab that stops answering. The block marks at most
  // 1000 hits, the counter says the cap was reached, and the stepper still
  // goes round every hit that is marked. The test's own timeout is the
  // "stays responsive": without the cap it does not finish in it.
  it('marks at most 1000 hits in one value of a megabyte, and says so', { timeout: 20_000 }, async () => {
    stubFetch(shell(configMap('y'.repeat(1024 * 1024))));
    mountApp('/c/prod/configmaps/argo/settings');
    await waitFor(() => {
      expect(page().textContent).toContain('nginx.conf');
    });

    expect(ctrlF()).toBe(false);
    // `y` is in no key and in no other value: every hit is in the big one.
    fireEvent.change(box(), { target: { value: 'y' } });
    await waitFor(() => {
      expect(page().querySelector('[aria-label="Next match"]')?.parentElement?.textContent).toContain('1/1000+');
    });
    expect(page().querySelectorAll('mark[data-find-hit]')).toHaveLength(1000);
    expect(page().querySelector('[aria-label="Next match"]')?.parentElement?.getAttribute('title')).toBe(
      '1 of 1000+: the data marks its first 1000 matches only',
    );
    // The same sentence for a reader that never hovers: it describes the box.
    const described = box().getAttribute('aria-describedby') ?? '';
    expect(document.getElementById(described)?.textContent).toBe(
      '1 of 1000+: the data marks its first 1000 matches only',
    );

    // Back from the first is the last hit marked, and on from it is the first.
    fireEvent.keyDown(box(), { key: 'Enter', shiftKey: true });
    expect(count()).toBe('1000/1000+');
    const marks = page().querySelectorAll('mark[data-find-hit]');
    expect(marks[999]?.hasAttribute('data-find-current')).toBe(true);
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('1/1000+');
    expect(page().querySelectorAll('mark[data-find-hit]')[0]?.hasAttribute('data-find-current')).toBe(true);
  });

  it('does not say a cap was reached when a value has exactly 1000 hits', { timeout: 15_000 }, async () => {
    stubFetch(shell(configMap('y'.repeat(1000))));
    mountApp('/c/prod/configmaps/argo/settings');
    await waitFor(() => {
      expect(page().textContent).toContain('nginx.conf');
    });

    expect(ctrlF()).toBe(false);
    fireEvent.change(box(), { target: { value: 'y' } });
    await waitFor(() => {
      expect(count()).toBe('1/1000');
    });
    expect(page().querySelector('[aria-label="Next match"]')?.parentElement?.textContent).not.toContain('+');
    expect(box().hasAttribute('aria-describedby')).toBe(false);
    expect(page().querySelectorAll('mark[data-find-hit]')).toHaveLength(1000);
  });

  // The cap is the block's, not a value's: a hundred values of 1001 hits
  // each would be a hundred thousand marks under a cap per value.
  it('marks at most 1000 hits across the whole block, however many values', { timeout: 20_000 }, async () => {
    stubFetch(
      shell(
        configMapOf(
          Array.from({ length: 100 }, (_, i) => ({ key: `k-${String(i)}`, masked: false as const, value: 'y'.repeat(1001) })),
        ),
      ),
    );
    mountApp('/c/prod/configmaps/argo/settings');
    await waitFor(() => {
      expect(page().textContent).toContain('k-99');
    });

    expect(ctrlF()).toBe(false);
    fireEvent.change(box(), { target: { value: 'y' } });
    await waitFor(() => {
      expect(count()).toBe('1/1000+');
    });
    expect(page().querySelectorAll('mark[data-find-hit]')).toHaveLength(1000);
  });

  it('spends what is left of the cap on the next value, and steps across both', { timeout: 15_000 }, async () => {
    stubFetch(
      shell(
        configMapOf([
          { key: 'a', masked: false, value: `a:${'y'.repeat(600)}` },
          { key: 'b', masked: false, value: `b:${'y'.repeat(600)}` },
          { key: 'c', masked: false, value: 'c:y' },
        ]),
      ),
    );
    mountApp('/c/prod/configmaps/argo/settings');
    await waitFor(() => {
      expect(page().textContent).toContain('c:y');
    });

    expect(ctrlF()).toBe(false);
    fireEvent.change(box(), { target: { value: 'y' } });
    await waitFor(() => {
      expect(count()).toBe('1/1000+');
    });
    const inValue = (prefix: string): number =>
      [...page().querySelectorAll('mark[data-find-hit]')].filter((one) =>
        one.closest('pre')?.textContent?.startsWith(prefix),
      ).length;
    // All of the first value, what is left of the 1000 from the second, and
    // nothing from the third.
    expect(inValue('a:')).toBe(600);
    expect(inValue('b:')).toBe(400);
    expect(inValue('c:')).toBe(0);
    const currentIn = (): string | undefined =>
      page().querySelector('[data-find-current]')?.closest('pre')?.textContent?.slice(0, 2);
    expect(currentIn()).toBe('a:');

    // Back from the first wraps to the last mark, which is in the second value.
    fireEvent.keyDown(box(), { key: 'Enter', shiftKey: true });
    expect(count()).toBe('1000/1000+');
    expect(currentIn()).toBe('b:');
    // On from the last wraps to the first, in the first value.
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(count()).toBe('1/1000+');
    expect(currentIn()).toBe('a:');
  });

  it('does not say a cap was reached when the block has exactly 1000 hits', { timeout: 15_000 }, async () => {
    stubFetch(
      shell(
        configMapOf([
          { key: 'a', masked: false, value: `a:${'y'.repeat(300)}` },
          { key: 'b', masked: false, value: `b:${'y'.repeat(300)}` },
          { key: 'c', masked: false, value: `c:${'y'.repeat(400)}` },
        ]),
      ),
    );
    mountApp('/c/prod/configmaps/argo/settings');
    await waitFor(() => {
      expect(page().textContent).toContain('c:y');
    });

    expect(ctrlF()).toBe(false);
    fireEvent.change(box(), { target: { value: 'y' } });
    await waitFor(() => {
      expect(count()).toBe('1/1000');
    });
    expect(page().querySelectorAll('mark[data-find-hit]')).toHaveLength(1000);
  });

  it('leaves Ctrl+F to the browser on a tab that does not draw the keys', async () => {
    stubFetch(
      shell({
        '/api/c/prod/data': secret,
        '/api/c/prod/manifest': { body: { state: 'ok', data: { lines: [] } } },
      }),
    );
    mountApp('/c/prod/secrets/argo/creds?tab=manifest');
    await waitFor(() => {
      expect(page().querySelector('#object-tab-manifest')?.getAttribute('aria-selected')).toBe('true');
    });

    // The keys are on Details, not here, and an empty document has nothing to
    // search: the key is nobody's but the browser's.
    expect(ctrlF()).toBe(true);
    expect(page().querySelector('input[type="search"][aria-label^="Find in"]')).toBeNull();
  });
});
