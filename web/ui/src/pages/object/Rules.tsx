import { type ReactNode, useState } from 'react';

import { isReasonOnlyState, type PolicyRule, type RuleSet } from '../../api';
import { StateNotice } from '../../state/StateNotice';
import { Pill } from '../../components/Pill';
import { focusRing } from '../../focus';
import {
  groupName,
  groupRules,
  PILL_LIMIT,
  type RuleLine,
  summarize,
  verbRisk,
} from './rulesModel';

/**
 * What a Role grants, as compact wrapping lines.
 *
 * ## Why it exists
 *
 * A Role's page would otherwise draw Kind, API version, Name, Namespace,
 * Created, Generation, its labels, its relations and its events - **every
 * fact about the object except the permissions it exists to grant**, which
 * would be reachable only by switching to the Manifest tab and reading YAML.
 *
 * That is the same collapse `EventSaid` in `ObjectDetail.tsx` guards against,
 * one kind along: everything about the object except the sentence it exists to
 * carry.
 *
 * ## Lines and not a graph, and this panel costs nothing
 *
 * One line per rule (rules that differ only in their resources share one, see
 * `rulesModel.ts`), wrapping inside the drawer so nothing scrolls sideways,
 * read off the object the page already fetched - no
 * second cluster call, no binding walk, no `SelfSubjectRulesReview`. The
 * subject-to-permission graph most people picture is a second surface with its own layout, its own zoom and its own empty
 * cases, and the reverse question - *what can this ServiceAccount do* - means
 * listing every binding in scope and filtering client-side. Neither is done
 * here.
 *
 * ## The wildcard is drawn as it is stored, and marked
 *
 * A `*` is a red pill on the verbs and in the summary above the
 * lines, and it is never expanded. `note` carries what is true of the object
 * itself, an aggregated ClusterRole's rules are written by a controller, and
 * rides as the hover text of its selectors.
 *
 ## Empty and unreadable are different answers
 *
 * On this surface that is not a general principle but the point: the entire
 * reason somebody opens the panel is to conclude *that is all this Role can do*,
 * so a fault drawn as "grants nothing" is the worst available answer. The state
 * arrives on the payload and `StateNotice` draws it, which is the same
 * arrangement every other block on this page uses.
 */
export function Rules({ set }: { set: RuleSet | undefined }): ReactNode {
  if (set === undefined) return null;
  const rules = set.state === 'ok' ? (set.rules ?? []) : [];
  const selectors = set.selectors ?? [];

  return (
    <section data-section="rules" className="min-w-0">
      <div className="mb-1 flex flex-wrap items-center gap-1">
        <h3 className="mr-1 text-xs font-medium text-muted uppercase">Rules</h3>
        {rules.length === 0 ? null : <Summary rules={rules} />}
      </div>
      {selectors.length === 0 ? null : <Selectors selectors={selectors} note={set.note} />}
      <Body set={set} />
    </section>
  );
}

function Summary({ rules }: { rules: PolicyRule[] }): ReactNode {
  const found = summarize(rules);
  return (
    <span data-rules-summary="" className="inline-flex max-w-full min-w-0 flex-wrap items-center gap-1">
      <Pill neutral="muted" title="Rules">
        {found.count}
      </Pill>
      {found.wildcard ? (
        <Pill tone="bad" title="A * appears in a rule">
          *
        </Pill>
      ) : null}
      {found.escalation.map((verb) => (
        <Pill key={verb} tone="bad" title={`Escalation: ${verb}`}>
          {verb}
        </Pill>
      ))}
    </span>
  );
}

/** An aggregated ClusterRole's selectors: each one a group of requirements that
 *  all have to hold, drawn as pills. */
function Selectors({ selectors, note }: { selectors: string[][]; note: string | undefined }): ReactNode {
  return (
    <div
      data-rules-selectors=""
      className="mb-2 flex flex-wrap items-center gap-1"
      {...(note === undefined || note === '' ? {} : { title: note })}
    >
      <span className="text-2xs text-muted uppercase">Aggregates</span>
      {selectors.map((requirements, at) => (
        <span
          key={at}
          data-selector=""
          className="inline-flex max-w-full flex-wrap gap-1 rounded-lg border border-dashed border-line p-0.5 font-mono"
        >
          {requirements.length === 0 ? (
            <Pill neutral="muted">*</Pill>
          ) : (
            requirements.map((requirement, index) => (
              <Pill key={index} neutral="muted" title={requirement}>
                {requirement}
              </Pill>
            ))
          )}
        </span>
      ))}
    </div>
  );
}

function Body({ set }: { set: RuleSet }): ReactNode {
  const rules = set.rules ?? [];

  if (set.state === 'empty') {
    // A sentence about the object, not about the read. A Role with no rules is
    // a real and consequential thing - it grants nothing - and `StateNotice`'s
    // own `empty` wording is *"the request succeeded and there is nothing to
    // show"*, which is the sentence for a list and not for this. The reason the
    // server sent, where it sent one, still travels.
    return (
      <StateNotice
        state="empty"
        reason={
          set.reason ?? {
            code: 'empty_scope',
            message: 'This role grants nothing: its rules list is empty.',
          }
        }
      />
    );
  }

  if (set.state !== 'ok') {
    return isReasonOnlyState(set.state) ? (
      <StateNotice state={set.state} reason={set.reason} />
    ) : (
      // Unreachable under the contract's own enum, and written rather than cast
      // away, exactly as `Section` does above: a state this client does not know
      // is the console's API answering out of contract, and on a permissions
      // panel the safe reading of *I do not understand this answer* is never
      // "there are no rules".
      <StateNotice state="unreadable" reason={set.reason} />
    );
  }

  if (rules.length === 0) {
    // `ok` with no rule is a payload the producer cannot produce - it would be
    // `empty`. Drawn as unreadable rather than as nothing, for the reason above.
    return <StateNotice state="unreadable" />;
  }

  const lines = groupRules(rules);
  return (
    <ul data-rule-lines="" className="m-0 flex list-none flex-col gap-1.5 p-0">
      {lines.map((line, at) => (
        <Line key={at} line={line} />
      ))}
    </ul>
  );
}

/** One rule: groups, resources, verbs and what narrows them, wrapping. */
function Line({ line }: { line: RuleLine }): ReactNode {
  return (
    <li data-rule="" className="flex min-w-0 flex-wrap items-start gap-x-3 gap-y-1 border-b border-line/50 pb-1.5 font-mono">
      <Pills kind="groups" items={line.apiGroups.map(groupName)} neutral="muted" />
      <Pills kind="resources" items={line.resources} />
      <Pills kind="urls" items={line.nonResourceURLs} />
      <Pills kind="names" items={line.resourceNames} neutral="muted" title="Resource name" />
      <Pills kind="verbs" items={line.verbs} grade={verbRisk} />
    </li>
  );
}

interface PillsProps {
  kind: string;
  items: readonly string[];
  neutral?: 'normal' | 'muted';
  title?: string;
  grade?: (item: string) => 'neutral' | 'warn' | 'bad';
}

/** One group of pills. Past `PILL_LIMIT` the rest folds behind a "+N" button
 *  that expands this group alone. */
function Pills({ kind, items, neutral = 'normal', title, grade }: PillsProps): ReactNode {
  const [open, setOpen] = useState(false);
  if (items.length === 0) return null;

  const folded = items.length > PILL_LIMIT;
  const shown = folded && !open ? items.slice(0, PILL_LIMIT) : items;

  return (
    <span data-group={kind} className="flex max-w-full min-w-0 flex-wrap gap-1">
      {shown.map((item, at) => {
        const risk = grade === undefined ? 'neutral' : grade(item);
        return (
          <Pill
            key={at}
            neutral={neutral}
            {...(risk === 'neutral' ? {} : { tone: risk })}
            {...(title === undefined ? {} : { title })}
          >
            {item}
          </Pill>
        );
      })}
      {folded ? (
        <button
          type="button"
          aria-expanded={open}
          aria-label={open ? 'Show fewer' : `Show ${items.length - PILL_LIMIT} more`}
          onClick={() => setOpen(!open)}
          className={`rounded-full border border-line px-2 py-px text-xs font-semibold text-muted hover:text-fg ${focusRing}`}
        >
          {open ? '−' : `+${items.length - PILL_LIMIT}`}
        </button>
      ) : null}
    </span>
  );
}
