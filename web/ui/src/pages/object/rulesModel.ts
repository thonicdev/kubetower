import type { PolicyRule } from '../../api';

/**
 * The rules panel's presentational logic, kept apart from the markup so it can
 * be tested without drawing anything: how risky a verb is, what a group is
 * called, which rules draw as one line, and the summary over a whole Role.
 */

/** The pill tone a verb is drawn in. `neutral` is the ungraded pill. */
export type VerbRisk = 'neutral' | 'warn' | 'bad';

/** Verbs that let a subject raise its own privileges or act as somebody else. */
export const ESCALATION_VERBS: readonly string[] = ['escalate', 'bind', 'impersonate'];

const WRITE_VERBS: readonly string[] = ['create', 'update', 'patch', 'delete', 'deletecollection'];

/**
 * Reading is neutral, changing is `warn`, and `*` or an escalation verb is
 * `bad`. Anything else (`use`, `approve`, `sign`, a subresource verb) is
 * neutral: a tone the console does not recognize is not a license to invent one.
 */
export function verbRisk(verb: string): VerbRisk {
  if (verb === '*' || ESCALATION_VERBS.includes(verb)) return 'bad';
  if (WRITE_VERBS.includes(verb)) return 'warn';
  return 'neutral';
}

/** The core API group is the empty string; `core` is what a person calls it. */
export function groupName(group: string): string {
  return group === '' ? 'core' : group;
}

/** One line of the panel: one or several rules that differ only in resources. */
export interface RuleLine {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
  resourceNames: string[];
  nonResourceURLs: string[];
  /** How many stored rules this line stands for. */
  rules: number;
}

/**
 * Rules identical but for their resources draw as one line, the resources
 * joined in the order first met. A rule that names objects or URLs is only
 * joined with one that names the same, because `get` on `secrets` and `get` on
 * the secret `db` are different grants and merging them would widen the first.
 * Lines keep the position of the first rule they hold.
 */
export function groupRules(rules: readonly PolicyRule[]): RuleLine[] {
  const lines: RuleLine[] = [];
  const at = new Map<string, RuleLine>();

  for (const rule of rules) {
    const apiGroups = rule.apiGroups ?? [];
    const resources = rule.resources ?? [];
    const verbs = rule.verbs ?? [];
    const resourceNames = rule.resourceNames ?? [];
    const nonResourceURLs = rule.nonResourceURLs ?? [];
    // A NUL cannot occur in a Kubernetes name, so it separates without ambiguity.
    const key = [apiGroups, verbs, resourceNames, nonResourceURLs].map((part) => part.join('\u0000')).join('\u0001');

    const held = at.get(key);
    if (held === undefined) {
      const line: RuleLine = {
        apiGroups: [...apiGroups],
        resources: [...resources],
        verbs: [...verbs],
        resourceNames: [...resourceNames],
        nonResourceURLs: [...nonResourceURLs],
        rules: 1,
      };
      at.set(key, line);
      lines.push(line);
      continue;
    }
    held.rules += 1;
    for (const resource of resources) {
      if (!held.resources.includes(resource)) held.resources.push(resource);
    }
  }
  return lines;
}

/** What the strip above the rules shows. */
export interface RuleSummary {
  count: number;
  /** A `*` appears in some rule's groups, resources or verbs. */
  wildcard: boolean;
  /** Which of `escalate`, `bind` and `impersonate` appear, in that order. */
  escalation: string[];
}

export function summarize(rules: readonly PolicyRule[]): RuleSummary {
  const verbs = new Set<string>();
  let wildcard = false;
  for (const rule of rules) {
    for (const verb of rule.verbs ?? []) verbs.add(verb);
    const stars = [...(rule.apiGroups ?? []), ...(rule.resources ?? []), ...(rule.verbs ?? [])];
    if (stars.includes('*')) wildcard = true;
  }
  return {
    count: rules.length,
    wildcard,
    escalation: ESCALATION_VERBS.filter((verb) => verbs.has(verb)),
  };
}

/** Pills past this many in one group fold behind a "+N" button. */
export const PILL_LIMIT = 8;
