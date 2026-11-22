import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { ClusterName, graphemes, splitName } from './ClusterName';

const ARN = 'arn:aws:eks:eu-west-3:123456789012:cluster/a-very-long-production-cluster-name-for-layout';

describe('a cluster name is cut in the middle, keeping what tells two apart', () => {
  it('keeps an ARN tail and gives up its region and account first', () => {
    expect(splitName(ARN)).toEqual({
      cut: 'arn:aws:eks:eu-west-3:123456789012:',
      kept: 'cluster/a-very-long-production-cluster-name-for-layout',
    });
  });

  it('keeps the cluster of a kubeadm context, since every one of them starts kubernetes-admin@', () => {
    expect(splitName('kubernetes-admin@kubernetes')).toEqual({
      cut: 'kubernetes-admin@',
      kept: 'kubernetes',
    });
    // A user that is an address itself: the cluster is after the last `@`.
    expect(splitName('alice@corp.example@prod').kept).toBe('prod');
  });

  it('keeps the last twelve characters of any other name, so a suffix survives', () => {
    expect(splitName('STAGING-EKS-1_OWNER')).toEqual({
      cut: 'STAGING',
      kept: '-EKS-1_OWNER',
    });
    expect(splitName('STAGING-EKS-1').kept).toBe('STAGING-EKS-1'.slice(-12));
  });

  it('cuts nothing from a short name, and never inside a character', () => {
    expect(splitName('staging')).toEqual({ cut: '', kept: 'staging' });
    const astral = `${'集'.repeat(10)}😀${'群'.repeat(10)}`;
    const { cut, kept } = splitName(astral);
    expect(cut + kept).toBe(astral);
    expect([...kept]).toHaveLength(12);
  });

  it('never cuts a flag or a joined sequence in two', () => {
    // A flag is two regional indicators and a family is four people joined by
    // ZWJ: one character each to a reader, several code points each.
    const flag = '🇫🇷';
    const family = '👨‍👩‍👧‍👦';
    const name = `${'a'.repeat(10)}${flag}${family}${'b'.repeat(11)}`;
    expect(graphemes(name)).toHaveLength(23);
    const { cut, kept } = splitName(name);
    expect(cut + kept).toBe(name);
    expect(graphemes(kept)).toHaveLength(12);
    expect(kept.startsWith(family)).toBe(true);
    expect(cut.endsWith(flag)).toBe(true);
  });
});

describe('the drawn name', () => {
  it('is one line, whole on its title and in its text, whatever its length', () => {
    render(<ClusterName name={ARN} className="text-sm" />);
    const drawn = screen.getByTitle(ARN);
    // The text node is the whole name - the cut is paint, so a screen reader
    // and a copy both get all of it.
    expect(drawn.textContent).toBe(ARN);
    expect(drawn.className).toContain('whitespace-nowrap');
    expect(drawn.className).toContain('min-w-0');
    expect(drawn.className).toContain('text-sm');
    // The run cut first, then the kept run - which is cut before its last
    // twelve characters, so the end of the name is the last thing to go.
    const [cut, kept] = Array.from(drawn.children);
    expect(cut?.className).toContain('truncate');
    expect(cut?.textContent).toBe('arn:aws:eks:eu-west-3:123456789012:');
    expect(kept?.className).toContain('shrink-0');
    const [lead, tail] = Array.from(kept?.children ?? []);
    expect(lead?.className).toContain('truncate');
    expect(tail?.textContent).toBe(ARN.slice(-12));
    expect(tail?.className).toBe('shrink-0');
  });

  it('keeps apart two kubeadm contexts in the narrowest slot', () => {
    // What never shrinks is the kept run's last twelve characters: the part
    // still drawn when the slot has room for nothing else. Under the old rule
    // both kept `kubernetes-admin@` and lost `prod` and `staging` first.
    const drawnTail = (name: string): string | null | undefined => {
      const { container } = render(<ClusterName name={name} />);
      return container.querySelector(':scope > span > span:last-child > span.shrink-0')?.textContent;
    };
    const prod = drawnTail('kubernetes-admin@kubernetes-prod');
    const staging = drawnTail('kubernetes-admin@kubernetes-staging');
    expect(prod).toBe('ernetes-prod');
    expect(staging).toBe('etes-staging');
    expect(prod).not.toBe(staging);
  });

  it('keeps the suffix that tells an ARN from its twin, at any width', () => {
    render(<ClusterName name={`${ARN}_OWNER`} />);
    const drawn = screen.getByTitle(`${ARN}_OWNER`);
    expect(drawn.querySelector(':scope > span:last-child > span:last-child')?.textContent).toBe(
      'layout_OWNER',
    );
  });

  it('takes its direction from the name, so a right-to-left name is not drawn halves swapped', () => {
    render(<ClusterName name="אבגדהוזחטיכלמנסעפצקר" />);
    expect(screen.getByTitle('אבגדהוזחטיכלמנסעפצקר').getAttribute('dir')).toBe('auto');
  });

  it('leaves the tooltip to an enclosing element that carries its own', () => {
    render(
      <a href="/c/prod" title={'Cluster overview\nprod'}>
        <ClusterName name="Production" titled={false} />
      </a>,
    );
    const drawn = screen.getByText('Production').closest('[data-cluster-name]');
    expect(drawn?.hasAttribute('title')).toBe(false);
    expect(screen.getByText('Production').closest('[title]')?.getAttribute('title')).toBe('Cluster overview\nprod');
  });
});
