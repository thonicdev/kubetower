import { useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import { Dialog, Failure, GoneNotice, KeyLayer, Pending, focusRing, useDeclarePageFacts, useEscape, type ExtensionTabProps } from '@kubetower/ui';

import { FlowUIButtons } from './FlowUIs';
import { PAGE_PATH, podQuery, problemCode, type PodAnswer } from './data';
import { MAX_ARROWS, MAX_ARROWS_LARGE, arrowsOf, flowsOf, type Arrow, type Flow, type Side } from './flows';
import { PodFlows } from './PodFlows';
import { IsolationChips, ModeBadge, Notices } from './parts';
import { PodGraph, type GraphSide, type Lit } from './PodGraph';
import { DirectionBox, type RowsLit } from './PodRules';

/**
 * The Network tab of a pod's panel: its isolation in each direction, the
 * policies that select it, and whom their rules let it talk to - as rules,
 * drawn dashed, never as traffic. One read when the tab opens; no interval.
 */
export default function PodTab(props: ExtensionTabProps): ReactNode {
  return <PodAnswerView {...props} large={false} />;
}

/**
 * The same answer enlarged, as the dialog draws it: what the Details line
 * opens over the panel (`ExtensionShortcut.dialog`), and what the tab's small
 * graph opens when pressed.
 */
/** The Escape that lets go of an arrow pressed in the enlarged view: above
 *  the dialog it sits in (an extension's dialog is an Overlay), so the first
 *  Escape lets go and only the second closes; below anything opened over it. */
const HELD_ARROW = KeyLayer.Overlay + 5;

export function PodLarge(props: ExtensionTabProps): ReactNode {
  return <PodAnswerView {...props} large />;
}

function PodAnswerView({ cluster, namespace, name, large }: ExtensionTabProps & { large: boolean }): ReactNode {
  const query = useQuery({ ...podQuery(cluster, namespace ?? '', name), enabled: namespace !== undefined && namespace !== '' });
  if (query.isError) {
    if (problemCode(query.error) === 'list_forbidden') {
      return (
        <p role="alert" data-refused="" className="text-sm text-warn">
          {query.error instanceof Error ? query.error.message : String(query.error)}
        </p>
      );
    }
    if (problemCode(query.error) === 'no_such_pod') return <GoneNotice what="Pod" detail={query.error instanceof Error ? query.error.message : undefined} />;
    if (problemCode(query.error) === 'pod_finished') {
      return (
        <p role="status" data-state="finished" data-finished="" className="text-sm text-muted" title={query.error instanceof Error ? query.error.message : undefined}>
          Pod has finished: no network to show
        </p>
      );
    }
    return <Failure error={query.error} onRetry={() => void query.refetch()} />;
  }
  if (query.data === undefined) return <Pending what="the NetworkPolicies that select this pod" />;
  return <PodView cluster={cluster} answer={query.data} large={large} />;
}

/**
 * The same answer as a page of its own, at `x/netinspect/pods/:namespace/:name`:
 * where the tile on a pod's Details leads, with room for the graph.
 */
export function PodPage(): ReactNode {
  const { cluster = '', namespace = '', name = '' } = useParams<{ cluster: string; namespace: string; name: string }>();
  useDeclarePageFacts({ title: `Network Inspector · ${name}` });
  return (
    <div className="flex flex-col gap-2 p-4" data-pod-page="">
      <h1 className="text-xl font-medium">
        <Link to={`/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`} className={`text-muted hover:text-fg ${focusRing}`}>
          Network Inspector
        </Link>{' '}
        <span className="text-muted">›</span> <span className="text-muted">{namespace}/</span>
        {name}
      </h1>
      <PodTab cluster={cluster} kind="pods" namespace={namespace} name={name} />
    </div>
  );
}

/**
 * One pod's rules: the graph, each direction's rows, and the links to the
 * page. `large` is the enlarged view of the dialog: a wider graph with more
 * arrows and whole labels, one filter over both directions, every row, and
 * what the flow source saw over a period picked. Otherwise the small graph
 * opens that view in a dialog when pressed.
 */
export function PodView({ cluster, answer, large = false }: { cluster: string; answer: PodAnswer; large?: boolean }): ReactNode {
  const pod = answer.pod;
  const id = `${pod.namespace}/${pod.name}`;
  const page = `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`;
  const [enlarged, setEnlarged] = useState(false);
  const [filter, setFilter] = useState('');
  const sides = useMemo(() => {
    const make = (side: Side): { flows: Flow[]; arrows: Arrow[] } => {
      const direction = side === 'ingress' ? answer.ingress : answer.egress;
      const flows = flowsOf(direction, side, pod.namespace);
      return { flows, arrows: direction.isolated ? arrowsOf(flows, large ? MAX_ARROWS_LARGE : MAX_ARROWS) : [] };
    };
    return { ingress: make('ingress'), egress: make('egress') };
  }, [answer, pod.namespace, large]);
  // What a hover is on, and what a press has fixed: an arrow (`key`) or a row (`flow`).
  const [hover, setHover] = useState<{ side: Side; key?: string; flow?: string } | null>(null);
  const [sticky, setSticky] = useState<{ side: Side; key: string } | null>(null);
  // In the dialog, Escape first lets go of a pressed arrow, above the
  // dialog's own layer; a second one closes the dialog.
  useEscape(HELD_ARROW, () => setSticky(null), large && sticky !== null);
  useEffect(() => {
    if (large || sticky === null) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setSticky(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [sticky, large]);
  const litOf = (side: Side): { graph: Lit; rows: RowsLit } => {
    const { flows, arrows } = sides[side];
    const pressed = sticky?.side === side ? arrows.find((a) => a.key === sticky.key) : undefined;
    const over = hover?.side === side ? hover : null;
    const keys = new Set<string>();
    let ids = new Set<string>();
    let dim = false;
    if (over?.key !== undefined || (over === null && pressed !== undefined)) {
      const arrow = over?.key !== undefined ? arrows.find((a) => a.key === over.key) : pressed;
      if (arrow !== undefined) {
        keys.add(arrow.key);
        ids = new Set(arrow.flowIds);
        dim = true;
      }
    } else if (over?.flow !== undefined) {
      ids = new Set([over.flow]);
      for (const a of arrows) if (a.flowIds.includes(over.flow)) keys.add(a.key);
    }
    if (pressed !== undefined) keys.add(pressed.key);
    const known = new Set(flows.map((f) => f.id));
    return {
      graph: { keys, dim, sticky: pressed?.key ?? null },
      rows: {
        ids,
        dim,
        filter: pressed !== undefined ? { ids: new Set(pressed.flowIds.filter((x) => known.has(x))), name: pressed.more ? `${pressed.name} more` : pressed.name } : null,
        pinned: pressed !== undefined,
      },
    };
  };
  const lit = { ingress: litOf('ingress'), egress: litOf('egress') };
  const graphSide = (side: Side, isolated: boolean): GraphSide => ({ isolated, arrows: sides[side].arrows, nothing: isolated && sides[side].arrows.length === 0 });
  const onHover = (side: Side, key: string | null) => setHover(key === null ? null : { side, key });
  const onPick = (side: Side, key: string) => setSticky(sticky?.side === side && sticky.key === key ? null : { side, key });
  const onRow = (side: Side) => (flow: string | null) => setHover(flow === null ? null : { side, flow });
  return (
    <div className="flex flex-col gap-3" data-netinspect-pod={id}>
      <div className="flex flex-wrap items-center gap-2">
        <ModeBadge mode={answer.mode} />
        <IsolationChips isolatedIn={pod.in} isolatedOut={pod.out} hostNetwork={pod.hostNetwork} />
        <span className="ml-auto">
          <FlowUIButtons cluster={cluster} namespace={pod.namespace} />
        </span>
      </div>
      <Notices complete={answer.complete} notes={answer.notes} cni={answer.cni} unevaluated={answer.unevaluated} />
      <PodGraph
        ingress={graphSide('ingress', answer.ingress.isolated)}
        egress={graphSide('egress', answer.egress.isolated)}
        lit={{ ingress: lit.ingress.graph, egress: lit.egress.graph }}
        onHover={onHover}
        onPick={onPick}
        large={large}
        {...(large
          ? {}
          : {
              onEnlarge: () => {
                // What the small graph held is let go: the dialog starts clear,
                // and nothing is left pressed behind it once it closes.
                setSticky(null);
                setHover(null);
                setEnlarged(true);
              },
            })}
      />
      {large ? (
        <input
          type="search"
          name="podq"
          value={filter}
          placeholder="Filter"
          aria-label="Filter the rules and flows"
          onChange={(e) => setFilter(e.target.value)}
          className={`w-64 rounded-md border border-line bg-panel-2 px-2 py-1 text-xs text-fg placeholder:text-muted ${focusRing}`}
        />
      ) : null}
      <div className={`grid grid-cols-1 gap-3 ${large ? 'xl:grid-cols-2' : '2xl:grid-cols-2'}`}>
        <DirectionBox title="Ingress" side="ingress" direction={answer.ingress} hostNetwork={pod.hostNetwork} flows={sides.ingress.flows} lit={lit.ingress.rows} onRow={onRow('ingress')} onClearFilter={() => setSticky(null)} {...(large ? { query: filter, expanded: true } : {})} />
        <DirectionBox title="Egress" side="egress" direction={answer.egress} hostNetwork={pod.hostNetwork} flows={sides.egress.flows} lit={lit.egress.rows} onRow={onRow('egress')} onClearFilter={() => setSticky(null)} {...(large ? { query: filter, expanded: true } : {})} />
      </div>
      {large ? <PodFlows cluster={cluster} namespace={pod.namespace} pod={pod.name} {...(pod.hostNetwork ? { address: pod.ip } : {})} query={filter} /> : null}
      <p className="flex flex-wrap gap-3 text-xs">
        <Link to={`${page}?ns=${encodeURIComponent(pod.namespace)}`} className={`text-accent underline ${focusRing}`}>
          Namespace
        </Link>
        <Link to={`${page}?ns=${encodeURIComponent(pod.namespace)}&from=${encodeURIComponent(id)}`} className={`text-accent underline ${focusRing}`}>
          Verdict from
        </Link>
        <Link to={`${page}?ns=${encodeURIComponent(pod.namespace)}&to=${encodeURIComponent(id)}`} className={`text-accent underline ${focusRing}`}>
          Verdict to
        </Link>
      </p>
      {enlarged ? (
        <Dialog title={`Network Inspector · ${pod.name}`} onClose={() => setEnlarged(false)} size="viewport" layer={KeyLayer.Overlay}>
          <div data-pod-enlarged="">
            <PodView cluster={cluster} answer={answer} large />
          </div>
        </Dialog>
      ) : null}
    </div>
  );
}
