import { useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import { Failure, GoneNotice, Pending, focusRing, useDeclarePageFacts, type ExtensionTabProps } from '@kubetower/ui';

import { FlowUIButtons } from './FlowUIs';
import { PAGE_PATH, podQuery, problemCode, type PodAnswer } from './data';
import { arrowsOf, flowsOf, type Arrow, type Flow, type Side } from './flows';
import { IsolationChips, ModeBadge, Notices } from './parts';
import { PodGraph, type GraphSide, type Lit } from './PodGraph';
import { DirectionBox, type RowsLit } from './PodRules';

/**
 * The Network tab of a pod's panel: its isolation in each direction, the
 * policies that select it, and whom their rules let it talk to - as rules,
 * drawn dashed, never as traffic. One read when the tab opens; no interval.
 */
export default function PodTab({ cluster, namespace, name }: ExtensionTabProps): ReactNode {
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
  return <PodView cluster={cluster} answer={query.data} />;
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

export function PodView({ cluster, answer }: { cluster: string; answer: PodAnswer }): ReactNode {
  const pod = answer.pod;
  const id = `${pod.namespace}/${pod.name}`;
  const page = `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`;
  const sides = useMemo(() => {
    const make = (side: Side): { flows: Flow[]; arrows: Arrow[] } => {
      const direction = side === 'ingress' ? answer.ingress : answer.egress;
      const flows = flowsOf(direction, side, pod.namespace);
      return { flows, arrows: direction.isolated ? arrowsOf(flows) : [] };
    };
    return { ingress: make('ingress'), egress: make('egress') };
  }, [answer, pod.namespace]);
  // What a hover is on, and what a press has fixed: an arrow (`key`) or a row (`flow`).
  const [hover, setHover] = useState<{ side: Side; key?: string; flow?: string } | null>(null);
  const [sticky, setSticky] = useState<{ side: Side; key: string } | null>(null);
  useEffect(() => {
    if (sticky === null) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setSticky(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [sticky]);
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
      <PodGraph ingress={graphSide('ingress', answer.ingress.isolated)} egress={graphSide('egress', answer.egress.isolated)} lit={{ ingress: lit.ingress.graph, egress: lit.egress.graph }} onHover={onHover} onPick={onPick} />
      <div className="grid grid-cols-1 gap-3 2xl:grid-cols-2">
        <DirectionBox title="Ingress" side="ingress" direction={answer.ingress} hostNetwork={pod.hostNetwork} flows={sides.ingress.flows} lit={lit.ingress.rows} onRow={onRow('ingress')} onClearFilter={() => setSticky(null)} />
        <DirectionBox title="Egress" side="egress" direction={answer.egress} hostNetwork={pod.hostNetwork} flows={sides.egress.flows} lit={lit.egress.rows} onRow={onRow('egress')} onClearFilter={() => setSticky(null)} />
      </div>
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
    </div>
  );
}
