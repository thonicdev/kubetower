import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Container } from './data';
import { runtimeInspector as jvm } from './index';
import { podTile, runtimeOf, tileFor } from './shortcut';

afterEach(() => {
  vi.unstubAllGlobals();
});

const GI = 1024 ** 3;

function container(over: Partial<Container> = {}): Container {
  return {
    name: 'app',
    image: 'eclipse-temurin:21-jre',
    java: true,
    hints: [{ source: 'image', reason: 'a Java image' }],
    request: null,
    limit: { bytes: 2 * GI, text: '2Gi' },
    heap: { bytes: 1.6 * GI, text: '1.6 GiB', from: 'explicit', source: '“-Xmx1638m” in the command line' },
    risk: { level: 'warning', percent: 80, sentence: 'The heap may grow to 80 % of the memory limit.' },
    notes: [],
    ...over,
  };
}

describe('the JVM tile on a pod', () => {
  it('names the runtime an image says, and nothing it does not', () => {
    expect(runtimeOf('eclipse-temurin:21-jre')).toBe('Temurin 21');
    expect(runtimeOf('amazoncorretto:17.0.9')).toBe('Corretto 17');
    expect(runtimeOf('openjdk:8u342-jre')).toBe('OpenJDK 8');
    expect(runtimeOf('jenkins/jenkins:lts-jdk17')).toBe('Java 17');
    expect(runtimeOf('registry.example.com/shop/catalogue:2.3-jdk17')).toBe('Java 17');
    expect(runtimeOf('registry.example.com/shop/billing:2.3')).toBeUndefined();
    expect(runtimeOf('bitnami/kafka:3.7')).toBeUndefined();
    expect(runtimeOf('eclipse-temurin')).toBe('Temurin');
  });

  it('grades the risk in its tone, draws the heap against the limit with the 75 % line, and opens the JVM tab', () => {
    const tile = tileFor({ cluster: 'prod', namespace: 'shop', name: 'billing-0' }, [container()]);
    expect(tile?.title).toBe('Temurin 21 in app');
    expect(tile?.to).toBe('/c/prod/pods/shop/billing-0?tab=x.runtime.jvm');
    expect(tile?.status).toEqual({ text: 'Tight', tone: 'warn', hint: 'The heap may grow to 80 % of the memory limit.' });
    expect(tile?.meter?.segments).toEqual([expect.objectContaining({ width: 80, tone: 'warn' })]);
    expect(tile?.meter?.threshold?.at).toBe(75);
    expect(tile?.meter?.text).toBe('heap 1.6 GiB of 2Gi');
  });

  it('speaks for the riskiest Java container, and draws no meter without a limit', () => {
    const tile = tileFor({ cluster: 'prod', namespace: 'shop', name: 'billing-0' }, [
      container({ name: 'sidecar', java: false, hints: [], heap: null, risk: null }),
      container({ name: 'ok', risk: { level: 'ok', percent: 25, sentence: 'room' } }),
      container({ name: 'bad', risk: { level: 'error', percent: 100, sentence: 'reaches the limit' }, heap: { bytes: 2 * GI, text: '2.0 GiB', from: 'explicit', source: '-Xmx2g' } }),
    ]);
    expect(tile?.title).toBe('Temurin 21 in bad');
    expect(tile?.status?.tone).toBe('bad');
    expect(tile?.detail).toBe('+1 Java container');

    const nolimit = tileFor({ cluster: 'prod', namespace: 'shop', name: 'x' }, [
      container({ limit: null, heap: { bytes: null, text: '', from: 'nolimit', source: 'on the node' }, risk: { level: 'nolimit', percent: null, sentence: 'no limit' } }),
    ]);
    expect(nolimit?.meter).toBeUndefined();
    expect(nolimit?.status).toBeUndefined();
    expect(nolimit?.badges).toEqual(['No limit']);
  });

  it('is declared on pods, reads the spec route only, and draws nothing for a pod that is not Java', async () => {
    expect(jvm.shortcuts?.map((s) => [s.key, s.kinds])).toEqual([['pod', ['pods']]]);
    const asked: string[] = [];
    const answer = (java: boolean) =>
      vi.stubGlobal(
        'fetch',
        vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
          asked.push(`${init?.method ?? 'GET'} ${String(input)}`);
          const body = { cluster: 'prod', namespace: 'shop', name: 'billing-0', phase: 'Running', java, containers: [container({ java })] };
          return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
        }),
      );
    const signal = new AbortController().signal;
    const object = { cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'billing-0' };
    answer(true);
    await expect(podTile(object, signal)).resolves.toMatchObject({ title: 'Temurin 21 in app' });
    answer(false);
    await expect(podTile(object, signal)).resolves.toBeNull();
    await expect(podTile({ ...object, namespace: undefined }, signal)).resolves.toBeNull();
    expect(asked).toEqual(['GET /x/prod/runtime/pod/shop/billing-0', 'GET /x/prod/runtime/pod/shop/billing-0']);
  });
});
