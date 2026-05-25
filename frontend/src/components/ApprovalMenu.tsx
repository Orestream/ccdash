// ApprovalMenu — VSCode-style permission approval menu shown above the composer.
// Lists pending PermissionRequests; each offers Allow / Allow always / Deny.

import type { PermissionDecision, PermissionRequest } from '../types';

export interface ApprovalMenuProps {
  requests: PermissionRequest[];
  onDecide: (requestId: string, decision: PermissionDecision) => void;
  pendingId?: string | null;
}

function compactInput(input: Record<string, unknown>): string {
  try {
    return JSON.stringify(input);
  } catch {
    return '';
  }
}

export function ApprovalMenu({ requests, onDecide, pendingId }: ApprovalMenuProps) {
  if (requests.length === 0) return null;

  return (
    <div className="approval-menu" role="region" aria-label="Permission requests">
      {requests.map((req) => {
        const busy = pendingId === req.id;
        return (
          <div key={req.id} className="approval-request" data-request-id={req.id}>
            <div className="approval-body">
              <div className="approval-summary">{req.summary}</div>
              <div className="approval-meta">
                <span className="approval-tool">{req.toolName}</span>
                <code className="approval-input">{compactInput(req.input)}</code>
              </div>
            </div>
            <div className="approval-actions">
              <button
                type="button"
                className="approval-allow"
                disabled={busy}
                onClick={() => onDecide(req.id, 'allow')}
              >
                Allow
              </button>
              <button
                type="button"
                className="approval-allow-always"
                disabled={busy}
                onClick={() => onDecide(req.id, 'allow_always')}
              >
                Allow always
              </button>
              <button
                type="button"
                className="approval-deny danger"
                disabled={busy}
                onClick={() => onDecide(req.id, 'deny')}
              >
                Deny
              </button>
            </div>
          </div>
        );
      })}
    </div>
  );
}

export default ApprovalMenu;
