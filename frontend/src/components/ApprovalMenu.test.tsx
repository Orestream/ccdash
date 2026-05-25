import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { ApprovalMenu } from './ApprovalMenu';
import type { PermissionRequest } from '../types';

const requests: PermissionRequest[] = [
  {
    id: 'req1',
    sessionId: 's1',
    toolName: 'Bash',
    input: { command: 'git status' },
    summary: 'Bash: git status',
    suggestions: ['allow', 'allow_always', 'deny'],
    createdAt: '2026-05-25T12:00:40Z',
  },
];

describe('ApprovalMenu', () => {
  it('renders nothing when there are no requests', () => {
    const { container } = render(<ApprovalMenu requests={[]} onDecide={() => {}} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders the summary, tool name and a compact input view', () => {
    render(<ApprovalMenu requests={requests} onDecide={() => {}} />);
    expect(screen.getByText('Bash: git status')).toBeInTheDocument();
    expect(screen.getByText('Bash')).toBeInTheDocument();
    expect(screen.getByText(/"command":"git status"/)).toBeInTheDocument();
  });

  it('fires allow / allow_always / deny with the request id', () => {
    const onDecide = vi.fn();
    render(<ApprovalMenu requests={requests} onDecide={onDecide} />);

    fireEvent.click(screen.getByRole('button', { name: 'Allow' }));
    expect(onDecide).toHaveBeenLastCalledWith('req1', 'allow');

    fireEvent.click(screen.getByRole('button', { name: 'Allow always' }));
    expect(onDecide).toHaveBeenLastCalledWith('req1', 'allow_always');

    fireEvent.click(screen.getByRole('button', { name: 'Deny' }));
    expect(onDecide).toHaveBeenLastCalledWith('req1', 'deny');

    expect(onDecide).toHaveBeenCalledTimes(3);
  });

  it('disables buttons for the request currently being decided', () => {
    render(
      <ApprovalMenu requests={requests} onDecide={() => {}} pendingId="req1" />,
    );
    expect(screen.getByRole('button', { name: 'Allow' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Deny' })).toBeDisabled();
  });
});
