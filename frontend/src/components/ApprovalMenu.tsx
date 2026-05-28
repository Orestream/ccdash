// ApprovalMenu — VSCode-style permission approval menu shown above the composer.
// Lists pending PermissionRequests. Most tools offer Allow / Allow always / Deny;
// AskUserQuestion is special-cased into a per-question picker (radio or checkbox)
// whose selections ride back as `answers` in the allow decision.

import { useMemo, useState } from 'react';
import type { PermissionDecision, PermissionRequest } from '../types';

export interface ApprovalMenuProps {
  requests: PermissionRequest[];
  // answers is forwarded as RespondPermissionInput.answers; supplied only for
  // AskUserQuestion submissions.
  onDecide: (
    requestId: string,
    decision: PermissionDecision,
    answers?: Record<string, string>,
  ) => void;
  pendingId?: string | null;
}

interface AskOption {
  label: string;
  description?: string;
}

interface AskQuestion {
  question: string;
  header?: string;
  multiSelect?: boolean;
  options: AskOption[];
}

// parseAskUserQuestion narrows a permission input into the AskUserQuestion shape
// when possible. Returns null if anything is missing — the caller falls back to
// the generic allow/deny menu so a malformed payload still has a working escape
// hatch.
function parseAskUserQuestion(input: Record<string, unknown>): AskQuestion[] | null {
  const raw = (input as { questions?: unknown }).questions;
  if (!Array.isArray(raw) || raw.length === 0) return null;
  const out: AskQuestion[] = [];
  for (const q of raw) {
    if (!q || typeof q !== 'object') return null;
    const obj = q as Record<string, unknown>;
    const question = typeof obj.question === 'string' ? obj.question : '';
    const options = Array.isArray(obj.options) ? obj.options : [];
    if (!question || options.length === 0) return null;
    const parsedOpts: AskOption[] = [];
    for (const opt of options) {
      if (!opt || typeof opt !== 'object') return null;
      const o = opt as Record<string, unknown>;
      if (typeof o.label !== 'string' || !o.label) return null;
      parsedOpts.push({
        label: o.label,
        description: typeof o.description === 'string' ? o.description : undefined,
      });
    }
    out.push({
      question,
      header: typeof obj.header === 'string' ? obj.header : undefined,
      multiSelect: obj.multiSelect === true,
      options: parsedOpts,
    });
  }
  return out;
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
        const ask =
          req.toolName === 'AskUserQuestion' ? parseAskUserQuestion(req.input) : null;
        if (ask) {
          return (
            <AskUserQuestionForm
              key={req.id}
              request={req}
              questions={ask}
              busy={busy}
              onDecide={onDecide}
            />
          );
        }
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

interface AskUserQuestionFormProps {
  request: PermissionRequest;
  questions: AskQuestion[];
  busy: boolean;
  onDecide: ApprovalMenuProps['onDecide'];
}

// AskUserQuestionForm renders each question with its options. Single-select
// questions use radios; multi-select use checkboxes. Submitting calls onDecide
// with allow + a {question → answer} map (multi-select values are ", "-joined,
// matching what the claude SDK accepts on the can_use_tool channel).
function AskUserQuestionForm({
  request,
  questions,
  busy,
  onDecide,
}: AskUserQuestionFormProps) {
  // selections[i] holds the chosen option labels for question i.
  const [selections, setSelections] = useState<string[][]>(() =>
    questions.map(() => []),
  );

  const ready = useMemo(
    () => selections.every((sel) => sel.length > 0),
    [selections],
  );

  const toggle = (qIdx: number, label: string, multi: boolean) => {
    setSelections((prev) => {
      const next = prev.map((s) => [...s]);
      if (multi) {
        const at = next[qIdx].indexOf(label);
        if (at === -1) next[qIdx].push(label);
        else next[qIdx].splice(at, 1);
      } else {
        next[qIdx] = [label];
      }
      return next;
    });
  };

  const submit = () => {
    if (!ready || busy) return;
    const answers: Record<string, string> = {};
    questions.forEach((q, i) => {
      answers[q.question] = selections[i].join(', ');
    });
    onDecide(request.id, 'allow', answers);
  };

  return (
    <div
      className="approval-request approval-ask"
      data-request-id={request.id}
      data-testid="ask-user-question"
    >
      <div className="approval-body">
        <div className="approval-summary">Claude is asking a question</div>
        {questions.map((q, qIdx) => {
          const groupName = `${request.id}-${qIdx}`;
          return (
            <fieldset key={qIdx} className="ask-question">
              <legend className="ask-question-legend">
                {q.header && <span className="ask-question-header">{q.header}</span>}
                <span className="ask-question-text">{q.question}</span>
              </legend>
              <ul className="ask-options">
                {q.options.map((opt) => {
                  const checked = selections[qIdx].includes(opt.label);
                  return (
                    <li key={opt.label} className="ask-option">
                      <label>
                        <input
                          type={q.multiSelect ? 'checkbox' : 'radio'}
                          name={groupName}
                          value={opt.label}
                          checked={checked}
                          disabled={busy}
                          onChange={() =>
                            toggle(qIdx, opt.label, q.multiSelect === true)
                          }
                        />
                        <span className="ask-option-label">{opt.label}</span>
                        {opt.description && (
                          <span className="ask-option-description">
                            {opt.description}
                          </span>
                        )}
                      </label>
                    </li>
                  );
                })}
              </ul>
            </fieldset>
          );
        })}
      </div>
      <div className="approval-actions">
        <button
          type="button"
          className="approval-allow"
          disabled={busy || !ready}
          onClick={submit}
        >
          Submit answers
        </button>
        <button
          type="button"
          className="approval-deny danger"
          disabled={busy}
          onClick={() => onDecide(request.id, 'deny')}
        >
          Skip
        </button>
      </div>
    </div>
  );
}

export default ApprovalMenu;
