import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { FlightRecordingSection, durationLabel } from './FlightRecording';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const PATH = '/x/kind/jvm/pod/shop/billing-0/app/recording';

function stub(reply: () => Response): { url: string; method: string; marked: boolean }[] {
  const asked: { url: string; method: string; marked: boolean }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      asked.push({
        url: typeof input === 'string' ? input : input.toString(),
        method: init?.method ?? 'GET',
        marked: new Headers(init?.headers).get('X-KubeTower-Request') === '1',
      });
      return Promise.resolve(reply());
    }),
  );
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() }));
  return asked;
}

function mount(): void {
  render(<FlightRecordingSection cluster="kind" namespace="shop" name="billing-0" container="app" failureTitle={{}} noJdk={false} onNoJcmd={() => undefined} />);
}

describe('the flight recorder', () => {
  it('offers the route’s four durations, and runs nothing before the confirmation', () => {
    const asked = stub(() => new Response('x'));
    mount();
    const buttons = [...document.querySelectorAll('[data-jvm-jfr-seconds]')].map((b) => b.textContent);
    expect(buttons).toEqual(['30 s', '1 min', '2 min', '5 min']);
    fireEvent.click(screen.getByRole('button', { name: '2 min' }));
    fireEvent.click(screen.getByRole('button', { name: 'Record' }));
    expect(asked).toHaveLength(0);
    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent).toContain('Holds environment variables');
    expect(dialog.textContent).toContain('2 min · default profile');
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(asked).toHaveLength(0);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('records for the chosen seconds and downloads, as a marked POST carrying the confirmation word', async () => {
    const asked = stub(
      () =>
        new Response(new Uint8Array(4096), {
          headers: { 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="billing-0-app-1.jfr"' },
        }),
    );
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    mount();
    fireEvent.click(screen.getByRole('button', { name: '1 min' }));
    fireEvent.click(screen.getByRole('button', { name: 'Record' }));
    fireEvent.click(screen.getByRole('button', { name: 'Record and download' }));
    expect(document.querySelector('[data-jvm-jfr-progress]')).not.toBeNull();
    await waitFor(() => {
      expect(document.querySelector('[data-jvm-jfr-done]')).not.toBeNull();
    });
    expect(asked).toEqual([{ url: `${PATH}?confirm=confirm&seconds=60`, method: 'POST', marked: true }]);
    expect(clicked).toHaveBeenCalledOnce();
    clicked.mockRestore();
  });

  it('counts the recording down', async () => {
    vi.useFakeTimers();
    stub(() => new Response('x'));
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => undefined)));
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Record' }));
    fireEvent.click(screen.getByRole('button', { name: 'Record and download' }));
    expect(document.querySelector('[data-jvm-jfr-progress]')?.textContent).toBe('30 s');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(document.querySelector('[data-jvm-jfr-progress]')?.textContent).toBe('20 s');
  });

  it('names a recording already running and a JVM with no recorder, by their codes', async () => {
    for (const [status, code, title] of [
      [409, 'recording_busy', 'A recording is already running'],
      [422, 'jfr_unavailable', 'No Flight Recorder in this JVM'],
      [403, 'exec_forbidden', 'The recording failed'],
    ] as const) {
      cleanup();
      stub(() => new Response(JSON.stringify({ code, title: 'x', detail: 'why' }), { status, headers: { 'Content-Type': 'application/problem+json' } }));
      mount();
      fireEvent.click(screen.getByRole('button', { name: 'Record' }));
      fireEvent.click(screen.getByRole('button', { name: 'Record and download' }));
      await waitFor(() => {
        expect(document.querySelector(`[data-jvm-failure="${code}"]`)?.textContent).toBe(title);
      });
      expect(document.querySelector('[data-jvm-jfr-done]')).toBeNull();
    }
  });

  it('asks which JVM when the container runs several', async () => {
    const asked = stub(
      () =>
        new Response(JSON.stringify({ state: 'choose_jvm', jvms: [{ pid: 7, mainClass: 'shop.Main' }, { pid: 9, mainClass: 'shop.Batch' }] }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Record' }));
    fireEvent.click(screen.getByRole('button', { name: 'Record and download' }));
    await screen.findByText('shop.Batch');
    fireEvent.click(screen.getByText('shop.Batch'));
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(asked).toHaveLength(1);
  });

  it('writes a duration as a short label', () => {
    expect([30, 60, 120, 300].map(durationLabel)).toEqual(['30 s', '1 min', '2 min', '5 min']);
  });
});
