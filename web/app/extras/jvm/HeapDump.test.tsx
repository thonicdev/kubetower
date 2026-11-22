import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { HeapDumpSection } from './HeapDump';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const PATH = '/x/kind/jvm/pod/shop/billing-0/app/heapdump';

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
  render(<HeapDumpSection cluster="kind" namespace="shop" name="billing-0" container="app" failureTitle={{}} noJdk={false} onNoJcmd={() => undefined} />);
}

describe('the heap dump', () => {
  it('runs nothing before the second click, and the confirmation says what the file holds', () => {
    const asked = stub(() => new Response('x'));
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Heap dump' }));
    expect(asked).toHaveLength(0);
    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent).toMatch(/secrets and data/);
    expect(dialog.textContent).toMatch(/passwords, tokens/);
    expect(dialog.textContent).toMatch(/pauses/);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(asked).toHaveLength(0);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('downloads once confirmed, as a marked POST carrying the confirmation word', async () => {
    const asked = stub(
      () =>
        new Response(new Uint8Array(2048), {
          headers: { 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="billing-0-app-1.hprof"' },
        }),
    );
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Heap dump' }));
    fireEvent.click(screen.getByRole('button', { name: 'Dump and download' }));
    await waitFor(() => {
      expect(document.querySelector('[data-jvm-heap-done]')).not.toBeNull();
    });
    expect(asked).toEqual([{ url: `${PATH}?confirm=confirm`, method: 'POST', marked: true }]);
    expect(clicked).toHaveBeenCalledOnce();
    clicked.mockRestore();
  });

  it('names a container out of room, and offers no file', async () => {
    stub(
      () =>
        new Response(JSON.stringify({ code: 'no_space', title: 'x', detail: 'The container has no room left in /tmp for the dump.' }), {
          status: 507,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    );
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Heap dump' }));
    fireEvent.click(screen.getByRole('button', { name: 'Dump and download' }));
    await waitFor(() => {
      expect(document.querySelector('[data-jvm-failure="no_space"]')?.textContent).toBe('No room in the container');
    });
    expect(document.querySelector('[data-jvm-heap-done]')).toBeNull();
  });

  it('asks which JVM when the container runs several, and confirms again for it', async () => {
    const asked = stub(
      () =>
        new Response(JSON.stringify({ state: 'choose_jvm', jvms: [{ pid: 7, mainClass: 'shop.Main' }, { pid: 9, mainClass: 'shop.Batch' }] }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Heap dump' }));
    fireEvent.click(screen.getByRole('button', { name: 'Dump and download' }));
    await screen.findByText('shop.Batch');
    fireEvent.click(screen.getByText('shop.Batch'));
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(asked).toHaveLength(1);
  });
});
