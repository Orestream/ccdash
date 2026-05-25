import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StatusBadge } from './StatusBadge';
import type { SessionStatus } from '../types';

const cases: Array<{ status: SessionStatus; label: string }> = [
  { status: 'idle', label: 'Idle' },
  { status: 'processing', label: 'Processing' },
  { status: 'awaiting_input', label: 'Awaiting input' },
  { status: 'done', label: 'Done' },
  { status: 'error', label: 'Error' },
];

describe('StatusBadge', () => {
  it.each(cases)('renders label and class for %s', ({ status, label }) => {
    render(<StatusBadge status={status} />);
    const badge = screen.getByTestId('status-badge');
    expect(badge).toHaveTextContent(label);
    expect(badge).toHaveClass('status-badge', `status-${status}`);
    expect(badge).toHaveAttribute('data-status', status);
  });

  it('shows a spinner only when processing', () => {
    const { rerender } = render(<StatusBadge status="processing" />);
    expect(screen.getByTestId('status-spinner')).toBeInTheDocument();

    rerender(<StatusBadge status="idle" />);
    expect(screen.queryByTestId('status-spinner')).not.toBeInTheDocument();
  });
});
