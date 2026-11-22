import { useQuery } from '@tanstack/react-query';
import { useRef, useState, type FormEvent, type ReactNode } from 'react';
import { CopyButton, Failure, Pending, btnSecondary, focusRing } from '@kubetower/ui';

import { verdictQuery, type Backend, type Side, type Verdict, type VerdictAsk } from './data';

type Go = (next: Record<string, string>) => void;

/**
 * "Would A reach B on port P?" - from the rules. The question is in the
 * address (`from`, `to`, `port`, `protocol`), so it is asked only once all
 * three are there, and once per question: no interval.
 *
 * The answer is two boxes, because a flow passes only if both ends let it:
 * the source's egress, then the destination's ingress, each with the policy
 * and rule that decide - or "open" when nothing isolates it in that
 * direction.
 */
export function VerdictPanel({ cluster, params, go }: { cluster: string; params: URLSearchParams; go: Go }): ReactNode {
  const ask: VerdictAsk = {
    from: params.get('from') ?? '',
    to: params.get('to') ?? '',
    port: params.get('port') ?? '',
    protocol: params.get('protocol') ?? 'TCP',
  };
  const asked = ask.from !== '' && ask.to !== '' && ask.port !== '';
  const [draft, setDraft] = useState(ask);
  const [seen, setSeen] = useState(ask);
  if (seen.from !== ask.from || seen.to !== ask.to || seen.port !== ask.port || seen.protocol !== ask.protocol) {
    // A row's "from" or "to" changed the address: the form follows it.
    setSeen(ask);
    setDraft(ask);
  }
  const query = useQuery({ ...verdictQuery(cluster, ask), enabled: asked });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    go({ from: draft.from.trim(), to: draft.to.trim(), port: draft.port.trim(), protocol: draft.protocol === 'TCP' ? '' : draft.protocol });
  };
  const input = `w-full rounded-md border border-line bg-panel-2 px-2 py-1 text-xs text-fg ${focusRing}`;
  return (
    <section aria-label="Verdict" data-verdict-panel="" className="flex flex-col gap-2 rounded-lg border border-line bg-panel p-3">
      <h2 className="text-sm font-medium">Verdict</h2>
      <form onSubmit={submit} className="grid grid-cols-2 gap-2 text-xs">
        <label className="col-span-2 flex flex-col gap-0.5 text-muted">
          From
          <input name="from" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} placeholder="shop/web-7d9f-abcde" className={input} />
        </label>
        <label className="col-span-2 flex flex-col gap-0.5 text-muted">
          To
          <input name="to" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} placeholder="shop/api-5c4b-fghij · svc:shop/api" className={input} />
        </label>
        <label className="flex flex-col gap-0.5 text-muted">
          Port
          <input name="port" value={draft.port} onChange={(e) => setDraft({ ...draft, port: e.target.value })} placeholder="8080" className={input} />
        </label>
        <label className="flex flex-col gap-0.5 text-muted">
          Protocol
          <select name="protocol" value={draft.protocol} onChange={(e) => setDraft({ ...draft, protocol: e.target.value })} className={input}>
            <option>TCP</option>
            <option>UDP</option>
            <option>SCTP</option>
          </select>
        </label>
        <button type="submit" className={`${btnSecondary} col-span-2`} disabled={draft.from === '' || draft.to === '' || draft.port === ''}>
          Check
        </button>
      </form>
      {!asked ? null : query.isError ? (
        <Failure error={query.error} onRetry={() => void query.refetch()} />
      ) : query.data === undefined ? (
        <Pending what="the verdict" />
      ) : (
        <Answer verdict={query.data} />
      )}
    </section>
  );
}

const ANSWER: Record<Verdict['answer'], { word: string; tone: string }> = {
  yes: { word: 'Allowed', tone: 'border-ok/60 bg-ok/10 text-ok' },
  no: { word: 'Denied', tone: 'border-bad/60 bg-bad/10 text-bad' },
  unevaluated: { word: 'Not evaluated', tone: 'border-line bg-panel-2 text-muted' },
  depends: { word: 'Plugin-defined', tone: 'border-warn/60 bg-warn/10 text-warn' },
};

export function Answer({ verdict }: { verdict: Verdict }): ReactNode {
  const a = ANSWER[verdict.answer];
  return (
    <div data-verdict={verdict.answer} className="flex flex-col gap-2 text-xs">
      <div className={`rounded-md border px-2.5 py-1.5 ${a.tone}`}>
        <strong className="text-sm font-semibold">{a.word}</strong>
        <p className="text-muted" title={verdict.summary}>
          {verdict.from} → {verdict.to} · {verdict.port}/{verdict.protocol}
        </p>
      </div>
      {verdict.service !== undefined ? (
        <Backends service={verdict.service} />
      ) : (
        <div className="flex items-stretch gap-1.5">
          <SideBox side={verdict.egress} title="Egress" />
          <svg viewBox="0 0 20 10" className="w-5 shrink-0 self-center" aria-hidden="true">
            <line x1="0" y1="5" x2="15" y2="5" stroke="var(--color-accent)" strokeWidth="1.5" strokeDasharray="3 2" />
            <path d="M 14 1 L 20 5 L 14 9 z" fill="var(--color-accent)" />
          </svg>
          <SideBox side={verdict.ingress} title="Ingress" />
        </div>
      )}
      {verdict.policy !== '' ? <PolicyBlock yaml={verdict.policy} /> : null}
    </div>
  );
}

/** Each pod behind a Service, with the port its traffic reaches there. */
function Backends({ service }: { service: NonNullable<Verdict['service']> }): ReactNode {
  return (
    <div data-backends="" className="flex flex-col gap-1">
      <div className="flex items-center gap-1.5 text-muted">
        <span className="font-mono text-fg">
          {service.portName !== undefined && service.portName !== '' ? `${service.portName} ` : ''}
          {service.port}
        </span>
        <span aria-hidden="true">→</span>
        <span className="font-mono text-fg">{service.targetPort}</span>
        <span title={service.selector} className="ml-auto truncate font-mono">
          {service.selector}
        </span>
      </div>
      <ul className="flex flex-col gap-1">
        {service.backends.map((b) => (
          <BackendRow key={b.pod} backend={b} />
        ))}
      </ul>
      {service.capped ? (
        <span data-backends-capped="" className="text-muted">
          {service.backends.length} / {service.total}
        </span>
      ) : null}
    </div>
  );
}

const DOT: Record<Verdict['answer'], string> = { yes: 'bg-ok', no: 'bg-bad', depends: 'bg-warn', unevaluated: 'bg-muted' };

function BackendRow({ backend }: { backend: Backend }): ReactNode {
  return (
    <li data-backend={backend.pod} data-answer={backend.answer} className="flex items-center gap-2 rounded-md border border-line bg-panel-2 px-2 py-1">
      <span className={`size-2 shrink-0 rounded-full ${DOT[backend.answer]}`} title={ANSWER[backend.answer].word} />
      <span className="min-w-0 flex-1 truncate" title={backend.pod}>
        {backend.pod}
      </span>
      <span className="font-mono text-muted">{backend.port}</span>
      <span className="text-2xs text-muted uppercase" title={backend.egress.reason}>
        out {STATE[backend.egress.state].word}
      </span>
      <span className="text-2xs text-muted uppercase" title={backend.ingress.reason}>
        in {STATE[backend.ingress.state].word}
      </span>
    </li>
  );
}

/** The NetworkPolicy that would allow the flow: text to copy, never applied. */
function PolicyBlock({ yaml }: { yaml: string }): ReactNode {
  const ref = useRef<HTMLPreElement>(null);
  return (
    <div data-policy-copy="" className="flex flex-col gap-1 rounded-md border border-line bg-panel-2 p-2">
      <div className="flex items-center justify-between">
        <span className="text-2xs font-bold tracking-wide text-muted uppercase">NetworkPolicy</span>
        <CopyButton value={yaml} label="Copy" selectRef={ref} />
      </div>
      <pre ref={ref} className="max-h-screen overflow-auto font-mono text-2xs leading-snug whitespace-pre text-fg">
        {yaml}
      </pre>
    </div>
  );
}

const STATE: Record<Side['state'], { word: string; tone: string }> = {
  open: { word: 'open', tone: 'border-line' },
  allowed: { word: 'allowed', tone: 'border-ok/60' },
  denied: { word: 'refused', tone: 'border-bad/60' },
  unevaluated: { word: 'not evaluated', tone: 'border-line border-dotted' },
  depends: { word: 'plugin-defined', tone: 'border-warn/60' },
  outside: { word: 'not a pod', tone: 'border-line border-dashed' },
};

function SideBox({ side, title }: { side: Side; title: string }): ReactNode {
  const s = STATE[side.state];
  return (
    <div data-side={side.direction} data-state={side.state} className={`flex min-w-0 flex-1 flex-col gap-1 rounded-md border bg-panel-2 p-2 ${s.tone}`}>
      <span className="text-2xs font-bold tracking-wide text-muted uppercase">{title}</span>
      <strong className={side.state === 'denied' ? 'text-bad' : side.state === 'allowed' ? 'text-ok' : side.state === 'depends' ? 'text-warn' : 'text-fg'}>{s.word}</strong>
      <span className="truncate text-muted" title={side.pod}>
        {side.pod}
      </span>
      {side.decided !== undefined ? (
        <span title={side.decided.text} data-rule-kind={side.decided.kind}>
          {side.decided.kind === 'deny' ? 'deny · ' : ''}
          {side.decided.name} · rule {side.decided.rule}
        </span>
      ) : side.isolating.length > 0 ? (
        <span className="text-muted" title={side.reason}>
          {side.isolating.map((p) => p.name).join(', ')}
        </span>
      ) : null}
      {side.caveats.length > 0 ? (
        <span className="text-warn" title={side.caveats.join(' ')}>
          plugin-defined
        </span>
      ) : null}
    </div>
  );
}
