/**
 * A claim's storage chart as the server sends it: the used and capacity bytes of
 * a 20 GiB volume that a database fills over the last hour. A copy of the shape,
 * as every fixture here is; the Go test `TestAClaimsPanelDrawsUsedAndCapacity`
 * is what says the server sends it.
 */
const GIB = 1024 ** 3;
const POINTS = 120;

export const STORAGE_ANSWERS: Readonly<Record<string, unknown>> = {
  '/api/c/demo/metrics': {
    state: 'ok',
    data: {
      series: [
        {
          key: 'volume_used',
          chart: 'volume',
          label: 'Used',
          unit: 'bytes',
          data: {
            startedAt: '2026-11-16T10:00:00Z',
            stepSeconds: 30,
            values: Array.from({ length: POINTS }, (_, i) => (11.2 + (i / POINTS) * 1.4 + Math.sin(i / 9) * 0.05) * GIB),
          },
        },
        {
          key: 'volume_capacity',
          chart: 'volume',
          label: 'Capacity',
          unit: 'bytes',
          data: {
            startedAt: '2026-11-16T10:00:00Z',
            stepSeconds: 30,
            values: Array.from({ length: POINTS }, () => 20 * GIB),
          },
        },
      ],
      metrics: [{ key: 'volume', label: 'Storage', available: true, selected: true }],
      windows: [
        { key: '15m', label: '15m' },
        { key: '1h', label: '1h', selected: true },
        { key: '6h', label: '6h' },
        { key: '24h', label: '24h' },
        { key: '7d', label: '7d' },
      ],
    },
  },
};
