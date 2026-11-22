import { act, cleanup, fireEvent, render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ActOutcome, ConfirmationPlan } from '../api';
import { focusRingTight } from '../focus';
import { KeyboardStack } from '../keyboard/KeyboardStack';
import { KeyLayer } from '../keyboard/layers';
import { claimKey, resetKeyboardStack } from '../keyboard/stack';
import { PIN_CHANGED_HOLD_MS, PlanDialog } from './PlanDialog';

afterEach(() => {
  resetKeyboardStack();
});

/** The plan for a restart on a pod, which is the act the server spends a
 *  paragraph and a warning on because it is a delete. */
function podRestart(over: Partial<ConfirmationPlan['facts']> = {}): ConfirmationPlan {
  return {
    token: 'tok-1',
    expiresAt: '2999-01-01T00:00:00Z',
    sentence: 'Restart pods argo/api-0.',
    facts: {
      verb: 'restart',
      targets: [{ kind: 'pods', namespace: 'argo', name: 'api-0' }],
      count: 1,
      destructive: true,
      // Required rather than optional in the generated types: the contract gives
      // it a default, and `openapi-typescript` reads a defaulted member as
      // always present.
      irreversible: false,
      explanation: 'Restarting a pod means deleting it. A controller that owns this pod creates a replacement.',
      warning: 'If nothing owns this pod, deleting it is all that happens.',
      button: 'Delete this pod',
      confirmation: { kind: 'acknowledge' },
      ...over,
    },
  };
}

function draw(plan: ConfirmationPlan, outcome?: ActOutcome): HTMLElement {
  render(
    <KeyboardStack>
      <PlanDialog
        title="Restart api-0?"
        plan={plan}
        pending={false}
        error={null}
        outcome={outcome}
        onCancel={vi.fn()}
        onApply={vi.fn()}
      />
    </KeyboardStack>,
  );
  const box = document.querySelector('[role="dialog"]');
  if (!(box instanceof HTMLElement)) throw new Error('no dialog');
  return box;
}

/** The control that commits, and not `Dialog`'s own Close in the header. */
function commit(box: HTMLElement): HTMLButtonElement {
  const found = box.querySelector('button[data-commit]');
  if (!(found instanceof HTMLButtonElement)) throw new Error('no commit control');
  return found;
}

/**
 * The window says what the verb does, in the server's words.
 *
 * The server's `actionSpec` carries the words, keyed on (verb, kind), so a
 * restart on a Deployment, a restart on a Pod and a delete on a Pod are three
 * different windows.
 */
describe('the typed confirmation says what to type', () => {
  it('names the count as a count where the server sent no label', () => {
    // "Type 1 to confirm" read as "type the digit one".
    const box = draw(podRestart({ confirmation: { kind: 'count', expected: '1' } }));
    expect(box.textContent).toContain('Type 1 — the number of objects this acts on — to confirm');
  });

  it("keeps the server's own label where it sent one", () => {
    const box = draw(podRestart({ confirmation: { kind: 'count', expected: '4', label: 'Type 4 to evict' } }));
    expect(box.textContent).toContain('Type 4 to evict');
  });
});

describe('the verb’s own words', () => {
  it('draws the explanation, the warning and the button the server sent', () => {
    const box = draw(podRestart());

    expect(box.textContent).toContain('Restarting a pod means deleting it.');
    expect(box.textContent).toContain('If nothing owns this pod');
    expect(commit(box).textContent).toBe('Delete this pod');
  });

  /**
   * The verb's warning and the object's are not one voice. The verb's is the
   * same on every object, so it would be read past, with the object's beside
   * it in the same color. The object's stays in
   */
  it('keeps the object’s warnings in the open and folds the verb’s under Details', () => {
    const box = draw(podRestart({ warnings: ['A run is already in progress (nightly-29384).'] }));

    const alerts = [...box.querySelectorAll('[role="alert"]')].map((one) => one.textContent);
    expect(alerts).toEqual(['A run is already in progress (nightly-29384).']);
    for (const one of box.querySelectorAll('[role="alert"]')) {
      expect(one.className).toContain('text-warn');
      expect(one.closest('details')).toBeNull();
    }
    const folded = box.querySelector('details[data-plan-details]');
    expect(folded?.textContent).toContain('If nothing owns this pod, deleting it is all that happens.');
  });

  /**
   * A bulk plan's commit control.
   *
   * Without a `button`, the fallback would draw `facts.verb` - the wire slug,
   * lower-case `delete` on the button that commits the act.
   *
   * The phrase is one the *server* composes for a set - "Delete 4 pods", from
   * `spec.Label`, the resolved count and `api.KindWord` - and this asserts the
   * dialog draws it as sent. Not that it is correct English: that is
   * `internal/web`'s TestABulkPlansButtonIsAPhraseWrittenForASet, one layer
   * down, where the words are decided.
   */
  it('draws the bulk plan’s own button, and never the verb slug behind it', () => {
    const box = draw({
      token: 'tok-3',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Delete 4 pods. This cannot be undone.',
      facts: {
        verb: 'delete',
        targets: [
          { kind: 'pods', namespace: 'krow-system', name: 'krow-retention-29826917-6t5qn' },
          { kind: 'pods', namespace: 'krow-system', name: 'krow-retention-29826917-zpvpc' },
          { kind: 'pods', namespace: 'renovate', name: 'renovate-29825340-pl7wb' },
          { kind: 'pods', namespace: 'trivy-system', name: 'scan-vulnerabilityreport-6f5ff8bbd9-hj9p4' },
        ],
        count: 4,
        destructive: true,
        irreversible: false,
        button: 'Delete 4 pods',
        confirmation: { kind: 'count', expected: '4', label: 'Type 4 to confirm' },
      },
    });

    expect(commit(box).textContent).toBe('Delete 4 pods');
    expect(commit(box).textContent).not.toBe('delete');
    // And the red is still the flag's and never the word's: it must not move
    // onto a verb's name.
    expect(commit(box).className).toContain('bg-bad');
  });

  /**
   * A plan carrying no `button` at all, and **no slug is drawn in the gap**.
   *
   * Every plan family composes its own phrase at the server, so what is left
   * is the last resort, for a plan that carries nothing - which nothing in
   * this console produces, and which is a word rather than a slug the day
   * something does. Each family's wording is asserted one layer down:
   * `internal/web`'s TestNoPlanFamilyLeavesTheClientDrawingTheVerbSlug.
   */
  it('renders a plan that carries none of them, and draws no slug in the gap', () => {
    const box = draw({
      token: 'tok-2',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Apply ConfigMap argo/settings.',
      facts: {
        verb: 'apply',
        targets: [{ kind: 'configmaps', namespace: 'argo', name: 'settings' }],
        count: 1,
        destructive: true,
        irreversible: false,
        confirmation: { kind: 'acknowledge' },
      },
    });

    expect(box.textContent).toContain('Apply ConfigMap argo/settings.');
    expect(commit(box).textContent).toBe('Apply');
    expect(commit(box).textContent).not.toBe('apply');
  });

  /**
   * The six families as the server sends them now, each drawn as sent.
   *
   * One row per family, because the defect was per family: each is its own
   * `api.PlanFacts{}` with its own literal, and a fix reaching five of six
   * would leave the sixth saying "Apply" with nothing to notice it.
   */
  it('draws each plan family’s own button, whatever its verb slug is', () => {
    for (const one of [
      { verb: 'write', button: 'Write settings.yaml' },
      { verb: 'apply', button: 'Apply this ConfigMap' },
      { verb: 'nodeshell', button: 'Open a root shell on node-a' },
      { verb: 'discard', button: 'Discard 3 preferences' },
      { verb: 'empty', button: 'Empty this archive' },
      { verb: 'evict', button: 'Evict 2 pods' },
    ]) {
      const box = draw({
        token: `tok-${one.verb}`,
        expiresAt: '2999-01-01T00:00:00Z',
        facts: {
          verb: one.verb,
          targets: [],
          count: 1,
          destructive: true,
          irreversible: false,
          button: one.button,
          confirmation: { kind: 'acknowledge' },
        },
      });

      expect(commit(box).textContent).toBe(one.button);
      expect(commit(box).textContent).not.toBe(one.verb);
      cleanup();
    }
  });
});

/**
 * The one control that must not be pressed by reflex is the loudest in the box.
 *
 * It was `border-bad text-bad` on a dark panel - an outline, and the palest
 * thing in the dialog. `facts.destructive` decides it and the dialog never looks
 * at the verb, which is the same rule the selection bar follows.
 */
describe('the destructive commit control', () => {
  it('is filled, and disabled at half while the gesture is unsatisfied', () => {
    const box = draw(
      podRestart({ confirmation: { kind: 'name', expected: 'api-0', label: 'Type api-0' } }),
    );

    expect(commit(box).className).toContain('bg-bad');
    expect(commit(box).className).toContain('disabled:opacity-50');
    expect(commit(box).hasAttribute('disabled')).toBe(true);
  });

  it('is not drawn destructive on a plan the server did not mark', () => {
    const box = draw(podRestart({ destructive: false }));

    expect(commit(box).className).not.toContain('bg-bad');
  });
});

/**
 * The box itself is framed in red on a plan that removes something, seen
 * before the button is read - and only then.
 */
describe('the frame of a destructive plan', () => {
  it('is red where the server marked the plan destructive', () => {
    const box = draw(podRestart({ destructive: true }));

    expect(box.dataset.tone).toBe('danger');
    expect(box.className).toContain('border-bad/45');
    expect(box.className).not.toContain('border-line');
    // No bar down the left edge: it was an inset shadow.
    expect(box.className).not.toContain('shadow');
  });

  it('is the ordinary frame on a plan the server did not mark', () => {
    const box = draw(podRestart({ destructive: false }));

    expect(box.dataset.tone).toBe('default');
    expect(box.className).toContain('border-line');
    expect(box.className).not.toContain('border-bad');
  });
});

/**
 * The typed field's box.
 *
 * `focusRingTight` is the confirmation's ring. A narrow, square, `font-mono`
 * box would make that ring read as an outlined rectangle, so the box is what
 * is shaped; the ring is asserted here so that the next hand does not fix the
 * look by removing it.
 */
describe('the typed field’s box', () => {
  /** The field, and not any other input the dialog might grow. */
  function field(box: HTMLElement): HTMLInputElement {
    const found = box.querySelector('input[data-dialog-focus]');
    if (!(found instanceof HTMLInputElement)) throw new Error('no typed field');
    return found;
  }

  const classes = (el: HTMLElement): string[] => el.className.split(/\s+/).filter(Boolean);

  it('carries the tight ring and no other outline utility', () => {
    const box = draw(
      podRestart({ confirmation: { kind: 'name', expected: 'api-0', label: 'Type api-0' } }),
    );

    const own = classes(field(box));
    const ring = focusRingTight.split(/\s+/).sort();
    for (const utility of ring) expect(own).toContain(utility);
    // Every outline utility on the field is one of the ring's, and nothing else
    // - no `outline-none`, no second offset, no ring drawn by another name.
    expect(own.filter((utility) => utility.includes('outline')).sort()).toEqual(ring);
  });

  it('is `.action-field input`: the label’s width to 340px, 7px corners, 6px by 9px, the dialog’s own face', () => {
    const box = draw(
      podRestart({ confirmation: { kind: 'count', expected: '1', label: 'Type 1 to confirm' } }),
    );

    const own = classes(field(box));
    // max-width: 340px, otherwise the label's width.
    expect(own).toContain('w-full');
    expect(own).toContain('max-w-[340px]'); // style-allow: asserts the component's own allowed literal
    expect(own).not.toContain('w-40');
    // border-radius: 7px - the theme's `lg` step. The bare `rounded` that was
    // here generates no rule in this theme, so the corners were square.
    expect(own).toContain('rounded-lg');
    expect(own).not.toContain('rounded');
    // padding: 6px 9px.
    expect(own).toContain('py-1.5');
    expect(own).toContain('px-[9px]'); // style-allow: asserts the component's own allowed literal
    // border: 1px solid var(--line); background: var(--bg); color: var(--fg).
    expect(own).toContain('border');
    expect(own).toContain('border-line');
    expect(own).toContain('bg-bg');
    expect(own).toContain('text-fg');
    // font: inherit. A typed count or name is a word, not code.
    expect(own).not.toContain('font-mono');
  });

  it('gives the number field the same box, because it is the same element', () => {
    const named = field(draw(podRestart({ confirmation: { kind: 'name', expected: 'api-0' } }))).className;
    cleanup();

    const box = draw(
      podRestart({ confirmation: { kind: 'number', value: 3, minimum: 0, maximum: 10 } }),
    );
    const numberField = field(box);

    expect(numberField.type).toBe('number');
    expect(numberField.className).toBe(named);
  });
});
/**
 * The outcome keeps everything that worked.
 *
 * `ActOutcome.results` is *"per target, always"* and the dialog filtered it to
 * the failures, so after a bulk delete of twelve pods where two were refused the
 * screen named the two and not the ten - on the surface where the mistake that
 * actually happens is having ticked rows in a namespace you did not mean to be
 * in.
 */
describe('the outcome, per target', () => {
  const outcome: ActOutcome = {
    succeeded: 2,
    failed: 1,
    summary: '2 done, 1 refused.',
    results: [
      { target: { kind: 'pods', namespace: 'argo', name: 'api-0' }, ok: true, message: 'deleted' },
      { target: { kind: 'pods', namespace: 'argo', name: 'api-1' }, ok: true, message: 'deleted' },
      {
        target: { kind: 'pods', namespace: 'kube-system', name: 'dns-0' },
        ok: false,
        message: 'a disruption budget would be violated',
        reason: { code: 'verb_denied', message: 'a disruption budget would be violated' },
      },
    ],
  };

  it('names the targets that worked, not only the ones that did not', () => {
    const rows = draw(podRestart(), outcome).querySelectorAll('li');

    expect(rows).toHaveLength(3);
    const text = [...rows].map((one) => one.textContent ?? '').join('\n');
    expect(text).toContain('argo/api-0');
    expect(text).toContain('argo/api-1');
    expect(text).toContain('kube-system/dns-0');
  });

  it('puts the failures first and says the verdict on every row', () => {
    const rows = [...draw(podRestart(), outcome).querySelectorAll('li')];

    expect(rows[0]?.textContent).toContain('kube-system/dns-0');
    expect(rows[0]?.textContent).toContain('refused');
    expect(rows[1]?.textContent).toContain('done');
  });

  /** Countable without reading English, which is what `data-refusal` is
   *  for. */
  it('carries the refusal code on the row as data', () => {
    const rows = [...draw(podRestart(), outcome).querySelectorAll('li')];

    expect(rows[0]?.getAttribute('data-refusal')).toBe('verb_denied');
    expect(rows[1]?.getAttribute('data-refusal')).toBeNull();
  });

  /** A drain's eviction nobody answered - which may still be happening - and
   *  one never sent are not refusals. The word comes from the code, and only a
   *  refusal is red. */
  it('says "no answer" and "not sent" for what is not a refusal, in neutral', () => {
    const drain: ActOutcome = {
      succeeded: 1,
      failed: 3,
      summary: 'Evicted 1 of 4 pods.',
      results: [
        {
          target: { kind: 'pods', namespace: 'shop', name: 'budgeted' },
          ok: false,
          message: 'a PodDisruptionBudget refused this eviction',
          reason: { code: 'verb_denied', message: 'a PodDisruptionBudget refused this eviction' },
        },
        {
          target: { kind: 'pods', namespace: 'shop', name: 'unanswered' },
          ok: false,
          message: 'the API server did not answer this eviction within 15s',
          reason: { code: 'timeout', message: 'the API server did not answer this eviction within 15s', retryable: true },
        },
        {
          target: { kind: 'pods', namespace: 'shop', name: 'unsent' },
          ok: false,
          message: 'not attempted — nothing was sent',
          reason: { code: 'not_probed_yet', message: 'not attempted — nothing was sent', retryable: true },
        },
        { target: { kind: 'pods', namespace: 'shop', name: 'gone' }, ok: true, message: 'evicted' },
      ],
    };
    const rows = [...draw(podRestart(), drain).querySelectorAll('li')];
    const verdict = (name: string): HTMLElement => {
      const row = rows.find((one) => one.textContent?.includes(`shop/${name}`));
      const word = row?.querySelector('[data-verdict]');
      if (!(word instanceof HTMLElement)) throw new Error(`no verdict on ${name}`);
      return word;
    };

    expect(verdict('budgeted').textContent).toBe('refused');
    expect(verdict('budgeted').className).toContain('text-bad');

    expect(verdict('unanswered').textContent).toBe('no answer');
    expect(verdict('unanswered').className).toContain('text-muted');
    expect(verdict('unanswered').className).not.toContain('text-bad');

    expect(verdict('unsent').textContent).toBe('not sent');
    expect(verdict('unsent').className).toContain('text-muted');
    expect(verdict('unsent').className).not.toContain('text-bad');

    expect(verdict('gone').textContent).toBe('done');
    // The code stays on the row as data, whatever the word.
    expect(rows.map((one) => one.getAttribute('data-refusal'))).toEqual([
      'verb_denied',
      'timeout',
      'not_probed_yet',
      null,
    ]);
  });

  /** A code this client does not know is drawn as the refusal it always was,
   *  never as better news. */
  it('reads an unknown code as a refusal', () => {
    const odd: ActOutcome = {
      succeeded: 0,
      failed: 1,
      results: [
        {
          target: { kind: 'pods', namespace: 'shop', name: 'odd' },
          ok: false,
          message: 'something new',
          reason: { code: 'cluster_unreachable', message: 'something new' },
        },
      ],
    };
    const word = draw(podRestart(), odd).querySelector('[data-verdict]');
    expect(word?.textContent).toBe('refused');
    expect(word?.className).toContain('text-bad');
  });

  it('keeps the server’s summary above the rows', () => {
    const box = draw(podRestart(), outcome);

    expect(box.textContent).toContain('2 done, 1 refused.');
  });
});

/**
 * What the window does **not** say, which is most of what was wrong with it.
 *
 * A confirmation about one object states it once: the title names the pod and
 * the server's sentence names it with its kind and namespace, so no "1 object"
 * count, no list repeating it, and no bordered box explaining that there is no
 * before-and-after to draw - a note is a plain line outside any frame.
 *
 * These cases exist so the removals cannot come back one merge at a time.
 */
describe('a confirmation about one object says it once', () => {
  it('draws no count and no list when there is a single target', () => {
    const box = draw(podRestart());

    expect(box.textContent).not.toContain('1 object');
    // The sentence still names it — that one is the server's, and it is the one
    // statement of the fact this keeps.
    expect(box.textContent).toContain('Restart pods argo/api-0.');
    expect(box.querySelectorAll('li')).toHaveLength(0);
  });

  it('still counts and lists a set, which is what the count is for', () => {
    const box = draw(
      podRestart({
        count: 3,
        targets: [
          { kind: 'pods', namespace: 'argo', name: 'api-0' },
          { kind: 'pods', namespace: 'argo', name: 'api-1' },
          { kind: 'pods', namespace: 'argo', name: 'api-2' },
        ],
      }),
    );

    expect(box.textContent).toContain('3 objects');
    expect(box.querySelectorAll('li')).toHaveLength(3);
  });

  it('lists a single target that carries a note, because nothing else says it', () => {
    // Emptying the archive: one target, and its note is the whole substance of
    // the confirmation. The list is dropped where it repeats, never where it
    // adds.
    const box = draw(
      podRestart({
        targets: [{ kind: 'archive', name: "this console's archive", note: '4.0 MiB across 6 days' }],
      }),
    );

    expect(box.textContent).toContain('4.0 MiB across 6 days');
    expect(box.querySelectorAll('li')).toHaveLength(1);
    expect(box.textContent).not.toContain('1 object');
  });

  it('draws a refused row of a selection in the refusal tone, with its reason', () => {
    const box = draw(
      podRestart({
        targets: [
          { kind: 'pods', namespace: 'team-a', name: 'a-0' },
          { kind: 'pods', namespace: 'team-b', name: 'b-0', refused: true, note: 'pods is forbidden for you' },
        ],
      }),
    );

    const rows = [...box.querySelectorAll('li')];
    expect(rows).toHaveLength(2);
    expect(rows[0]?.querySelector('.text-bad')).toBeNull();
    expect(rows[1]?.querySelector('.text-bad')?.textContent).toContain('pods is forbidden for you');
  });

  it('draws a note-only recap as a sentence rather than inside a frame', () => {
    const box = draw(
      podRestart({
        recap: {
          note: 'There is no before and after to show: the object is removed.',
          absent: false,
          unchanged: false,
        },
      }),
    );

    expect(box.textContent).toContain('There is no before and after to show');
    // No bordered box anywhere in the body: the only frames left are the
    // gesture's input and the dialog's own.
    expect(box.querySelectorAll('div.rounded.border')).toHaveLength(0);
  });

  it('keeps the frame for a recap that actually has a before and an after', () => {
    const box = draw(
      podRestart({
        recap: {
          field: 'spec.replicas',
          before: '3',
          after: '0',
          absent: false,
          unchanged: false,
        },
      }),
    );

    expect(box.querySelectorAll('div.rounded.border')).toHaveLength(1);
    expect(box.textContent).toContain('spec.replicas');
  });

  // Nine digits of a value nobody can act on are plumbing, so the version and
  // the reason are on hover. The assertion that the number is *not* printed is
  // the half worth keeping: it is the one that fails if somebody helpfully
  // puts it back.
  it('draws the pin as a padlock and leaves what is pinned, the version and the reason on hover', () => {
    const box = draw(podRestart({ precondition: '370519006' }));

    expect(box.textContent).not.toContain('370519006');
    expect(box.textContent).not.toContain('Pinned to');
    expect(box.textContent).not.toContain('refused rather than overwritten');
    const pinned = box.querySelector('[data-pin]');
    if (!(pinned instanceof HTMLElement)) throw new Error('no pin');
    expect(pinned.querySelector('use')?.getAttribute('href')).toBe('#i-lock');
    // Named for a reader with no hover, in the same words as the hover.
    expect(pinned.getAttribute('role')).toBe('img');
    expect(pinned.getAttribute('aria-label')).toContain('Pinned to resourceVersion 370519006');
    expect(pinned.title).toBe(pinned.getAttribute('aria-label'));
    expect(pinned.title).toContain('refused rather than overwritten');
  });

  it('draws no padlock on a plan pinned to nothing', () => {
    const box = draw(podRestart());
    expect(box.querySelector('[data-pin]')).toBeNull();
  });

  it('does not repeat "this cannot be undone" under a line that already said it', () => {
    // The server's line says the loss in words on a single verb's plan.
    const said = draw(
      podRestart({
        irreversible: true,
        headline: 'Deletes pod debug. Nothing will recreate it.',
      }),
    );
    expect(said.textContent).not.toContain('This cannot be undone.');

    cleanup();
    // A plan with no line of its own - emptying the archive - has nowhere else
    // that says it.
    const bare = draw(podRestart({ irreversible: true }));
    expect(bare.textContent).toContain('This cannot be undone.');
  });
});

/** A single pod delete as the server sends it: the
 *  line about this pod is the sentence and the headline both. */
function podDelete(over: Partial<ConfirmationPlan['facts']> = {}, line?: string): ConfirmationPlan {
  const headline = line ?? 'Deletes pod web-7f9c-x2. ReplicaSet web-7f9c creates a new one.';
  return {
    token: 'tok-del',
    expiresAt: '2999-01-01T00:00:00Z',
    sentence: headline,
    facts: {
      verb: 'delete',
      targets: [{ kind: 'pods', namespace: 'shop', name: 'web-7f9c-x2' }],
      count: 1,
      destructive: true,
      irreversible: false,
      headline,
      explanation: 'The object is removed from the cluster and its dependents are collected behind it.',
      button: 'Delete web-7f9c-x2',
      precondition: '4242',
      confirmation: { kind: 'count', expected: '1' },
      ...over,
    },
  };
}

/**
 * A developer glances at a confirmation once and clicks the button, so it has
 * to be precise and concise.
 *
 * The dialog leads with one line about this object, says a warning only where
 * the server read one off it, folds what the verb means in general under
 * Details, and draws the pin as a padlock. The words are the server's and are
 * asserted one layer down (`internal/web`'s actionline_test.go); these assert
 * the dialog draws them in that order and in that shape.
 */
describe('one line about this object first, and the rest one click away', () => {
  /** The paragraphs drawn open, outside Details and the gesture's label:
   *  what the eye meets on the way to the button. */
  function open(box: HTMLElement): string[] {
    return [...box.querySelectorAll('p')]
      .filter((p) => p.closest('details') === null && p.closest('label') === null)
      .map((p) => p.textContent ?? '');
  }

  it('leads with the server’s one line, and nothing else is open above the gesture', () => {
    const box = draw(podDelete());

    expect(open(box)).toEqual(['Deletes pod web-7f9c-x2. ReplicaSet web-7f9c creates a new one.']);
    const line = box.querySelector('[data-plan-line]');
    // Something remakes this pod: the line is not drawn as a loss.
    expect(line?.className).toContain('text-fg');
    expect(line?.className).not.toContain('text-bad');
    expect(commit(box).textContent).toBe('Delete web-7f9c-x2');
  });

  it('draws the line red where nothing would make the object again, and says it once', () => {
    const box = draw(podDelete({ irreversible: true }, 'Deletes pod debug. Nothing will recreate it.'));

    expect(open(box)).toEqual(['Deletes pod debug. Nothing will recreate it.']);
    expect(box.querySelector('[data-plan-line]')?.className).toContain('text-bad');
  });

  it('folds the explanation under Details, closed, and opens it on one click', () => {
    const box = draw(podDelete());

    const details = box.querySelector('details[data-plan-details]');
    if (!(details instanceof HTMLDetailsElement)) throw new Error('no Details');
    expect(details.open).toBe(false);
    expect(details.textContent).toContain('its dependents are collected behind it');

    const summary = details.querySelector('summary');
    if (summary === null) throw new Error('no summary');
    expect(summary.textContent).toBe('Details');
    fireEvent.click(summary);
    expect(details.open).toBe(true);
  });

  it('folds the recap with it', () => {
    const box = draw(
      podDelete({
        recap: { field: 'spec.unschedulable', before: 'false', after: 'true', absent: false, unchanged: false },
      }),
    );

    const frame = box.querySelector('div.rounded.border');
    expect(frame?.closest('details')).not.toBeNull();
  });

  it('draws no Details at all for a plan with nothing to fold', () => {
    const plan = podDelete();
    delete (plan.facts as { explanation?: string }).explanation;
    const box = draw(plan);

    expect(box.querySelector('details')).toBeNull();
  });

  it('draws a warning only where the server read one off this object', () => {
    const quiet = draw(podDelete({ verb: 'trigger', button: 'Run nightly now' }, 'Starts a Job from nightly, now.'));
    expect(quiet.querySelectorAll('[role="alert"]')).toHaveLength(0);
    expect(open(quiet)).toEqual(['Starts a Job from nightly, now.']);

    cleanup();
    const running = draw(
      podDelete(
        {
          verb: 'trigger',
          button: 'Run nightly now',
          warnings: ['A run is already in progress (nightly-29384).', 'nightly is suspended: this run starts anyway.'],
        },
        'Starts a Job from nightly, now.',
      ),
    );
    expect([...running.querySelectorAll('[role="alert"]')].map((one) => one.textContent)).toEqual([
      'A run is already in progress (nightly-29384).',
      'nightly is suspended: this run starts anyway.',
    ]);
  });

  it('puts the pin beside the controls as a padlock, with its hover text', () => {
    const box = draw(podDelete());

    const pinned = box.querySelector('[data-pin]');
    expect(pinned?.parentElement?.contains(commit(box))).toBe(true);
    expect(pinned?.getAttribute('title')).toContain('Pinned to resourceVersion 4242');
    expect(pinned?.getAttribute('aria-label')).toBe(pinned?.getAttribute('title'));
  });

  it('keeps a set’s count and list, under its own sentence', () => {
    const plan = podDelete({
      count: 3,
      targets: [
        { kind: 'pods', namespace: 'shop', name: 'web-0' },
        { kind: 'pods', namespace: 'shop', name: 'web-1' },
        { kind: 'pods', namespace: 'shop', name: 'web-2' },
      ],
      button: 'Delete 3 pods',
    });
    delete (plan.facts as { headline?: string }).headline;
    plan.sentence = 'Delete 3 pods. This cannot be undone.';
    const box = draw(plan);

    expect(box.textContent).toContain('Delete 3 pods. This cannot be undone.');
    expect(box.textContent).toContain('3 objects');
    expect(box.querySelectorAll('li')).toHaveLength(3);
  });
});

/**
 * Both of this file's `<Dialog>`s are confirmations, so they claim
 * `KeyLayer.Confirm` rather than the `layer` prop's default.
 *
 * `layers.ts`'s row says a confirmation sits above a dialog *because a
 * confirmation raised from inside one must not close the dialog underneath
 * it*, and this component is the console's one plan renderer: every
 * confirmation the operator agrees to is one of its two returns.
 * `pages/object/Data.tsx` is the same shape and is on `KeyLayer.Confirm`.
 *
 * With one dialog on screen the default would order the stack identically;
 * it differs the first time a plan is confirmed from inside the manifest
 * editor.
 *
 * **Both registration orders, because one of them passes on the wrong
 * numbers.** With the thing underneath registered first, this dialog is the
 * newest claimant in its own layer and "whatever opened last" hands it the key
 * whether the two share a layer or not. Only the reverse order asks the
 * question; `keyboard/stack.test.ts` and `pages/object/Metrics.test.tsx` hold
 * the same pair.
 */
describe('the plan dialog claims the confirmation layer, not the dialog one', () => {
  /** The component's two `<Dialog>` returns: the question it asks, and the
   *  outcome it reports once the act has run. Two call sites, one layer, and a
   *  fix that named only the first would leave the other on the default. */
  const branches: readonly { readonly name: string; readonly outcome?: ActOutcome }[] = [
    { name: 'the question branch' },
    {
      name: 'the outcome branch',
      outcome: {
        succeeded: 1,
        failed: 0,
        summary: '1 done.',
        results: [{ target: { kind: 'pods', namespace: 'argo', name: 'api-0' }, ok: true }],
      },
    },
  ];

  /** Rendered with a real `onCancel`, because what "took Escape" means here is
   *  that this dialog's close ran and the claimant underneath never saw the
   *  key. */
  function raise(outcome?: ActOutcome): () => void {
    const onCancel = vi.fn();
    render(
      <KeyboardStack>
        <PlanDialog
          title="Restart api-0?"
          plan={podRestart()}
          pending={false}
          error={null}
          outcome={outcome}
          onCancel={onCancel}
          onApply={vi.fn()}
        />
      </KeyboardStack>,
    );
    return onCancel;
  }

  for (const branch of branches) {
    describe(branch.name, () => {
      it('still closes on Escape with nothing else on the stack', () => {
        // The case that already worked, asserted so that moving a claimant up
        // the stack is shown not to have changed it. This is what all four
        // callers do today: one dialog on screen, and Escape closes it.
        const onCancel = raise(branch.outcome);

        fireEvent.keyDown(window, { key: 'Escape' });

        expect(onCancel).toHaveBeenCalledTimes(1);
      });

      it('takes Escape from a dialog registered before it', () => {
        const under = vi.fn(() => true);
        const release = claimKey(KeyLayer.Dialog, under);
        const onCancel = raise(branch.outcome);

        fireEvent.keyDown(window, { key: 'Escape' });

        expect(onCancel).toHaveBeenCalledTimes(1);
        expect(under).not.toHaveBeenCalled();
        release();
      });

      it('takes Escape from a dialog registered after it', () => {
        // The order that tells the two mechanisms apart: the thing underneath
        // is now the newest claimant, so a shared layer gives it the key and
        // the plan stays open over a dialog that has just closed behind it.
        const onCancel = raise(branch.outcome);
        const under = vi.fn(() => true);
        const release = claimKey(KeyLayer.Dialog, under);

        fireEvent.keyDown(window, { key: 'Escape' });

        expect(onCancel).toHaveBeenCalledTimes(1);
        expect(under).not.toHaveBeenCalled();
        release();
      });
    });
  }
});

/**
 * What the dialog draws for a recap that says nothing.
 *
 * `internal/web`'s `recapOf` answers `nil` for a recap with neither a field nor
 * a note, *"because a client drawing `{}` would render a heading over nothing"*
 * — and the client it is talking about is this file. Every member of
 * `PlanRecap` is `omitempty`, so what a delete's plan carries is no `recap`
 * member at all. `TestADeleteCarriesNoRecapAndStillCarriesItsPrecondition` in
 * `internal/web` pins that end; these pin this one.
 *
 * **The absence is measured with an instrument shown to register presence.** A
 * case asserting "no frame, no line" passes on a dialog that draws no recap
 * under any circumstances — a component gutted to `return null` would be green
 * on it. So every case below is a *difference* against the same plan with no
 * recap, and the two readings that must differ are taken in the same test: a
 * recap with a field adds exactly one frame, a recap with a note adds exactly
 * one line of text, and the silent shapes add neither.
 */
describe('a recap that says nothing is drawn as nothing', () => {
  /** The recap's two visible shapes, counted. Blank paragraphs are counted
   *  apart from the ones carrying words, because an empty `<p>` under a heading
   *  is exactly the thing `recapOf`'s comment is about and it is invisible to
   *  `textContent`. */
  function body(over: Partial<ConfirmationPlan['facts']>): {
    frames: number;
    said: number;
    blank: number;
  } {
    // Each reading is its own render: two trees in one body and the queries
    // below would count the first one's nodes as well.
    cleanup();
    const box = draw(podRestart(over));
    const paragraphs = [...box.querySelectorAll('p')];
    return {
      frames: box.querySelectorAll('div.rounded.border').length,
      said: paragraphs.filter((p) => (p.textContent ?? '').trim() !== '').length,
      blank: paragraphs.filter((p) => (p.textContent ?? '').trim() === '').length,
    };
  }

  /** The recap the operator would read if there were one to read. */
  const noted = { note: 'There is no before and after to show.', absent: false, unchanged: false };
  const framed = { field: 'spec.replicas', before: '3', after: '0', absent: false, unchanged: false };

  it('draws neither a frame nor a line for a plan that carries no recap', () => {
    const bare = body({});

    expect(bare.frames).toBe(0);
    expect(bare.blank).toBe(0);

    // And the same counters do see a recap, or the two assertions above are
    // about a component that draws nothing at all.
    expect(body({ recap: framed }).frames).toBe(bare.frames + 1);
    expect(body({ recap: noted }).said).toBe(bare.said + 1);
  });

  it('draws neither a frame nor a line for a recap carrying no field and no note', () => {
    // `recapOf` never mints this shape — it answers `nil` for it, which is what
    // the Go case pins. The client holds against it for a different reason: the
    // schema makes every member of `recap` optional, so `{}` is a contract-legal
    // body, and a client that draws a heading over it is drawing over nothing
    // whoever sent it.
    const silent = body({ recap: { absent: false, unchanged: false } });
    const bare = body({});

    expect(silent.frames).toBe(bare.frames);
    expect(silent.said).toBe(bare.said);
    expect(silent.blank).toBe(bare.blank);
  });

  it('adds the note as a line and the field as a frame, and never both', () => {
    // The other half of the sentence: a recap that *does* say something is
    // drawn, and the two shapes are told apart by which counter moved rather
    // than by the words in them.
    const bare = body({});

    const withNote = body({ recap: noted });
    expect(withNote.said).toBe(bare.said + 1);
    expect(withNote.frames).toBe(bare.frames);

    const withField = body({ recap: framed });
    expect(withField.frames).toBe(bare.frames + 1);
    expect(withField.blank).toBe(bare.blank);
  });
});

/**
 * A 409's fresh plan keeps what was typed, so the commit control would be live
 * the instant it rendered and a second click would confirm a plan nobody had
 * read. When the pin moved, the control is held for a moment.
 */
describe('a fresh plan pinned to something else holds the commit control', () => {
  function cordon(token: string, precondition: string, preconditionUid = 'uid-node-a'): ConfirmationPlan {
    return {
      token,
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Cordon nodes node-a.',
      facts: {
        verb: 'cordon',
        targets: [{ kind: 'nodes', name: 'node-a' }],
        count: 1,
        destructive: false,
        irreversible: false,
        precondition,
        preconditionOn: 'spec.unschedulable and spec.taints',
        preconditionUid,
        confirmation: { kind: 'acknowledge' },
      },
    };
  }

  function view(plan: ConfirmationPlan): ReactElement {
    return (
      <KeyboardStack>
        <PlanDialog title="Cordon node-a?" plan={plan} pending={false} error={null} onCancel={vi.fn()} onApply={vi.fn()} />
      </KeyboardStack>
    );
  }

  function control(): HTMLButtonElement {
    const box = document.querySelector('[role="dialog"]');
    if (!(box instanceof HTMLElement)) throw new Error('no dialog');
    return commit(box);
  }

  afterEach(() => {
    vi.useRealTimers();
    cleanup();
  });

  it('is live on the first plan, held when a taint moved the pin, and live again after the hold', () => {
    vi.useFakeTimers();
    const { rerender } = render(view(cordon('tok-1', 'unschedulable=false; taints=none')));
    expect(control().disabled).toBe(false);

    rerender(view(cordon('tok-2', 'unschedulable=false; taints=zone:NoExecute')));
    expect(control().disabled).toBe(true);

    act(() => {
      vi.advanceTimersByTime(PIN_CHANGED_HOLD_MS - 1);
    });
    expect(control().disabled).toBe(true);
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(control().disabled).toBe(false);
  });

  it('is held when the object was recreated, though the value is equal', () => {
    vi.useFakeTimers();
    const { rerender } = render(view(cordon('tok-1', 'unschedulable=false; taints=none')));
    rerender(view(cordon('tok-2', 'unschedulable=false; taints=none', 'uid-node-a-again')));
    expect(control().disabled).toBe(true);
  });

  it('holds nothing when the fresh plan is pinned to what the old one was', () => {
    vi.useFakeTimers();
    const { rerender } = render(view(cordon('tok-1', 'unschedulable=false; taints=none')));
    rerender(view(cordon('tok-2', 'unschedulable=false; taints=none')));
    expect(control().disabled).toBe(false);
  });

  /**
   * A drain's eviction stage carries no pin - it is pinned to its set of pods,
   * and the server's 409 says a pod arrived or left. Keyed on the pin alone,
   * that fresh plan would hold nothing, so a second click would evict a pod
   * nobody had read the name of.
   */
  function evict(token: string, names: string[]): ConfirmationPlan {
    return {
      token,
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: `Evict ${String(names.length)} pods.`,
      facts: {
        verb: 'evict',
        targets: names.map((name) => ({ kind: 'pods', namespace: 'prod', name })),
        count: names.length,
        destructive: true,
        irreversible: false,
        confirmation: { kind: 'acknowledge' },
      },
    };
  }

  it('is held when an eviction stage with no pin names another set of pods', () => {
    vi.useFakeTimers();
    const { rerender } = render(view(evict('tok-1', ['api-1', 'api-2'])));
    expect(control().disabled).toBe(false);

    rerender(view(evict('tok-2', ['api-1', 'api-2', 'arrived'])));
    expect(control().disabled).toBe(true);
    act(() => {
      vi.advanceTimersByTime(PIN_CHANGED_HOLD_MS);
    });
    expect(control().disabled).toBe(false);
  });

  it('is held when one pod replaced another and the count did not move', () => {
    vi.useFakeTimers();
    const { rerender } = render(view(evict('tok-1', ['api-1', 'api-2'])));
    rerender(view(evict('tok-2', ['api-1', 'arrived'])));
    expect(control().disabled).toBe(true);
  });

  it('holds nothing when the same pods come back in another order', () => {
    vi.useFakeTimers();
    const { rerender } = render(view(evict('tok-1', ['api-1', 'api-2'])));
    rerender(view(evict('tok-2', ['api-2', 'api-1'])));
    expect(control().disabled).toBe(false);
  });
});

/**
 * The "Force delete" box: drawn from the plan's
 * `forceable` and nothing else, and handed to the owner only when ticked.
 */
describe('a forceable plan', () => {
  function drawWith(plan: ConfirmationPlan, onApply: (confirmed?: string, force?: boolean) => void): HTMLElement {
    render(
      <KeyboardStack>
        <PlanDialog
          title="Delete api-0?"
          plan={plan}
          pending={false}
          error={null}
          onCancel={vi.fn()}
          onApply={onApply}
        />
      </KeyboardStack>,
    );
    const box = document.querySelector('[role="dialog"]');
    if (!(box instanceof HTMLElement)) throw new Error('no dialog');
    return box;
  }

  it('draws no box on a plan that does not say it', () => {
    const box = draw(podRestart());
    expect(box.querySelector('[data-force]')).toBeNull();
    cleanup();
  });

  it('opens with the keyboard on Cancel, never on the box', () => {
    const box = drawWith(podRestart({ verb: 'delete', button: 'Delete', forceable: true }), vi.fn());
    const focused = document.activeElement;
    expect(focused).toBeInstanceOf(HTMLButtonElement);
    expect(focused?.textContent).toBe('Cancel');
    expect(box.contains(focused)).toBe(true);
    cleanup();
  });

  it('applies without force, and with it once ticked', () => {
    const onApply = vi.fn();
    const box = drawWith(podRestart({ verb: 'delete', button: 'Delete', forceable: true }), onApply);
    const force = box.querySelector('input[data-force]');
    if (!(force instanceof HTMLInputElement)) throw new Error('no Force delete box');
    expect(force.checked).toBe(false);

    fireEvent.click(commit(box));
    expect(onApply).toHaveBeenLastCalledWith(undefined, false);

    fireEvent.click(force);
    fireEvent.click(commit(box));
    expect(onApply).toHaveBeenLastCalledWith(undefined, true);
    cleanup();
  });
});

describe('what the owner draws of the plan', () => {
  it('sits after the sentence and before the gesture, and is gone once the act has run', () => {
    const plan = podRestart({ destructive: false, confirmation: { kind: 'name', expected: 'trivy-operator' } });
    const { rerender } = render(
      <KeyboardStack>
        <PlanDialog title="Install" plan={plan} pending={false} error={null} onCancel={vi.fn()} onApply={vi.fn()}>
          <pre data-owner="">kind: Application</pre>
        </PlanDialog>
      </KeyboardStack>,
    );
    const owner = document.querySelector('[data-owner]');
    const line = document.querySelector('[data-plan-line]');
    const field = document.querySelector('input[type="text"]');
    expect(owner).not.toBeNull();
    expect(line!.compareDocumentPosition(owner!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(owner!.compareDocumentPosition(field!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    rerender(
      <KeyboardStack>
        <PlanDialog
          title="Install"
          plan={plan}
          pending={false}
          error={null}
          outcome={{ succeeded: 1, failed: 0, results: [] }}
          onCancel={vi.fn()}
          onApply={vi.fn()}
        >
          <pre data-owner="">kind: Application</pre>
        </PlanDialog>
      </KeyboardStack>,
    );
    expect(document.querySelector('[data-owner]')).toBeNull();
    cleanup();
  });
});
