import type { SessionStatus } from '../types';

const LABELS: Record<SessionStatus, string> = {
  idle: 'Idle',
  processing: 'Processing',
  awaiting_input: 'Awaiting input',
  done: 'Done',
  error: 'Error',
};

export interface StatusBadgeProps {
  status: SessionStatus;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const label = LABELS[status] ?? status;
  return (
    <span
      className={`status-badge status-${status}`}
      data-testid="status-badge"
      data-status={status}
    >
      {status === 'processing' && (
        <span className="spinner" data-testid="status-spinner" aria-hidden="true" />
      )}
      {label}
    </span>
  );
}

export default StatusBadge;
