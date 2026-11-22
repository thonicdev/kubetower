import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState, type ReactNode } from 'react';
import { afterEach, describe, expect, it } from 'vitest';

import { PageFactsProvider, usePageFacts, useDeclarePageFacts, type PageFacts } from './pageFacts';

afterEach(cleanup);

function Read(): ReactNode {
  const facts = usePageFacts();
  return <output data-testid="facts">{JSON.stringify(facts)}</output>;
}

function Page({ facts }: { facts: PageFacts }): null {
  useDeclarePageFacts(facts);
  return null;
}

function declared(): PageFacts {
  return JSON.parse(screen.getByTestId('facts').textContent ?? 'null') as PageFacts;
}

describe('useDeclarePageFacts', () => {
  it('titles the page from its trail where it gives one, whatever title came with it', () => {
    // The window title and the breadcrumb are one fact, so
    // a page cannot give the two different words.
    render(
      <PageFactsProvider>
        <Page facts={{ title: 'Something else', trail: [{ label: 'Pod', to: '/c/x/pods' }, { label: 'api-0' }] }} />
        <Read />
      </PageFactsProvider>,
    );
    expect(declared()).toEqual({ title: 'Pod / api-0', trail: [{ label: 'Pod', to: '/c/x/pods' }, { label: 'api-0' }] });
  });

  it('keeps the page’s own title where its trail is empty', () => {
    render(
      <PageFactsProvider>
        <Page facts={{ title: 'Applications', trail: [] }} />
        <Read />
      </PageFactsProvider>,
    );
    expect(declared()).toEqual({ title: 'Applications' });
  });

  it('follows a trail whose places change, though each render builds a new array', () => {
    function Moving(): ReactNode {
      const [to, setTo] = useState('/c/x/pods');
      return (
        <>
          <button type="button" onClick={() => setTo('/c/x/pods?namespace=a')}>
            move
          </button>
          {/* A fresh array on every render, as a page builds it. */}
          <Page facts={{ title: null, trail: [{ label: 'Pod', to }, { label: 'api-0' }] }} />
        </>
      );
    }
    render(
      <PageFactsProvider>
        <Moving />
        <Read />
      </PageFactsProvider>,
    );
    expect(declared().trail?.[0]?.to).toBe('/c/x/pods');
    fireEvent.click(screen.getByRole('button', { name: 'move' }));
    expect(declared().trail?.[0]?.to).toBe('/c/x/pods?namespace=a');
  });
});
