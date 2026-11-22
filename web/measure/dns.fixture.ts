import type { Figures, Points, Series, Span, Stats, Upstream } from '../app/extras/dns/data';

/**
 * DNS's fake cluster, for its preview picture (`extras.fixture.ts` names the
 * scene): three CoreDNS pods on a shop cluster, one upstream that stopped
 * answering and so a share of SERVFAIL. Every name is invented, and the
 * series are a fixed wave, the same on every run.
 */

const NOW = Math.floor(Date.now() / 1000);

function wave(span: Span, value: (i: number, n: number) => number): Points {
  const seconds = span === '15m' ? 900 : span === '6h' ? 21_600 : 3600;
  const n = 60;
  const step = seconds / n;
  return Array.from({ length: n }, (_, i) => [NOW - seconds + Math.round(i * step), value(i, n)] as [number, number]);
}

const ripple = (i: number, amplitude: number) => Math.sin(i * 0.7) * amplitude + Math.cos(i * 1.9) * amplitude * 0.4;

function series(span: Span): Series {
  const qps = (i: number) => 330 + ripple(i, 28) + (i / 60) * 20;
  // The upstream stopped answering about two thirds in: SERVFAIL climbs from nothing.
  const servfail = (i: number, n: number) => (i < n * 0.6 ? 0.8 : 0.8 + (i - n * 0.6) * 1.6);
  const nx = (i: number) => 28 + ripple(i, 4);
  return {
    step: span === '15m' ? 15 : span === '6h' ? 360 : 60,
    qps: wave(span, qps),
    byRcode: {
      NOERROR: wave(span, (i, n) => qps(i) - servfail(i, n) - nx(i)),
      NXDOMAIN: wave(span, nx),
      SERVFAIL: wave(span, servfail),
    },
    p50: wave(span, (i) => 0.0004 + Math.abs(ripple(i, 0.00006))),
    p99: wave(span, (i, n) => (i < n * 0.6 ? 0.012 + Math.abs(ripple(i, 0.004)) : 0.012 + (i - n * 0.6) * 0.045)),
    cacheHit: wave(span, (i) => 0.78 + ripple(i, 0.03)),
  };
}

const now: Figures = { qps: 352, nxdomain: 0.087, servfail: 0.062, p50: 0.0005, p99: 1.62, cacheHit: 0.77 };

/** The upstream a Corefile zone forwards to and nothing answers: no series at all, so only the Corefile lists it. */
const SILENT: Upstream = { to: '192.0.2.1:53', qps: 0, errQps: 0, p99: null, healthFailPs: 0, answers: false, zones: ['failing.test'] };

function stats(span: Span, silent = false): Stats {
  return {
    state: 'read',
    span,
    namespace: 'kube-system',
    version: '1.12.1',
    readAt: new Date(NOW * 1000).toISOString(),
    now,
    series: series(span),
    pods: [
      { pod: 'coredns-7d9f8b6c5d-4kx2m', node: 'node-1', figures: { qps: 131, nxdomain: 0.085, servfail: 0.064, p50: 0.0005, p99: 1.7, cacheHit: 0.76 } },
      { pod: 'coredns-7d9f8b6c5d-9tq7w', node: 'node-2', figures: { qps: 118, nxdomain: 0.09, servfail: 0.061, p50: 0.0005, p99: 1.6, cacheHit: 0.78 } },
      { pod: 'coredns-7d9f8b6c5d-m2bzr', node: 'node-3', figures: { qps: 103, nxdomain: 0.087, servfail: 0.061, p50: 0.0006, p99: 1.55, cacheHit: 0.77 } },
    ],
    upstreams: [
      { to: '10.0.0.2:53', qps: 96, errQps: 0, p99: 0.018, healthFailPs: 0, answers: true },
      { to: '10.0.0.3:53', qps: 91, errQps: 0.4, p99: 0.024, healthFailPs: 0, answers: true },
      { to: '10.0.8.15:53', qps: 0, errQps: 0, p99: null, healthFailPs: 2.4, answers: false },
      ...(silent ? [SILENT] : []),
    ],
  };
}

const base = (cluster: string) => `/x/${cluster}/dns/`;

/** The answers of the page, by path and query, for a cluster. */
export const DNS_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}stats?span=15m`]: stats('15m'),
  [`${base(cluster)}stats?span=1h`]: stats('1h'),
  [`${base(cluster)}stats?span=6h`]: stats('6h'),
});

/** CoreDNS found, and Prometheus holds no series for it: the page says "No metrics" and offers the how-to. */
export const DNS_NO_METRICS_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => {
  const empty = (span: Span): Stats => ({ state: 'empty', span, namespace: 'kube-system', detail: 'Prometheus holds no CoreDNS series', pods: [], upstreams: [] });
  return {
    [`${base(cluster)}stats?span=15m`]: empty('15m'),
    [`${base(cluster)}stats?span=1h`]: empty('1h'),
    [`${base(cluster)}stats?span=6h`]: empty('6h'),
  };
};

/** The Corefile names an upstream Prometheus holds nothing for: listed with No answers (the answer without it is DNS_ANSWERS). */
export const DNS_FORWARD_ANSWERS = (cluster: string): Readonly<Record<string, unknown>> => ({
  [`${base(cluster)}stats?span=15m`]: stats('15m', true),
  [`${base(cluster)}stats?span=1h`]: stats('1h', true),
  [`${base(cluster)}stats?span=6h`]: stats('6h', true),
});

/** The answer of a console whose person may not list the CoreDNS pods, and one whose read failed. */
export const DNS_REFUSED = {
  status: 403,
  body: { code: 'list_forbidden', message: 'The cluster says you may not list the CoreDNS pods, so nothing about DNS can be shown.' },
};
export const DNS_FAILED = { status: 502, body: { code: 'read_failed', message: 'The DNS figures could not be read.' } };
