import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
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

const askRequest: PermissionRequest = {
  id: 'ask1',
  sessionId: 's1',
  toolName: 'AskUserQuestion',
  input: {
    questions: [
      {
        question: 'What auth approach do you want?',
        header: 'Auth',
        options: [
          { label: 'Username + password', description: 'Session cookies.' },
          { label: 'OAuth', description: 'Delegated identity.' },
        ],
      },
      {
        question: 'Which features?',
        header: 'Features',
        multiSelect: true,
        options: [
          { label: 'Login', description: 'Required.' },
          { label: 'Signup', description: 'Optional.' },
          { label: 'Password reset', description: 'Optional.' },
        ],
      },
    ],
  },
  summary: 'AskUserQuestion',
  suggestions: ['allow', 'allow_always', 'deny'],
  createdAt: '2026-05-25T12:00:40Z',
};

describe('ApprovalMenu', () => {
  it('renders nothing when there are no requests', () => {
    const { container } = render(<ApprovalMenu requests={[]} onDecide={() => {}} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders the summary, tool name and a per-tool details view', () => {
    render(<ApprovalMenu requests={requests} onDecide={() => {}} />);
    expect(screen.getByText('Bash: git status')).toBeInTheDocument();
    expect(screen.getByText('Bash')).toBeInTheDocument();
    // Bash details show the bare command, not raw JSON.
    expect(screen.getByText('git status')).toBeInTheDocument();
    expect(screen.queryByText(/"command":/)).toBeNull();
  });

  it('renders Edit as a diff of old_string / new_string', () => {
    const edit: PermissionRequest = {
      id: 'edit1',
      sessionId: 's1',
      toolName: 'Edit',
      input: {
        file_path: '/tmp/x.css',
        old_string: '.foo { color: red; }',
        new_string: '.foo { color: blue; }',
      },
      summary: 'Edit: /tmp/x.css',
      suggestions: ['allow', 'allow_always', 'deny'],
      createdAt: '2026-05-25T12:00:40Z',
    };
    render(<ApprovalMenu requests={[edit]} onDecide={() => {}} />);
    expect(screen.getByText('.foo { color: red; }')).toBeInTheDocument();
    expect(screen.getByText('.foo { color: blue; }')).toBeInTheDocument();
    // file_path is in the summary — don't repeat it in the details.
    expect(screen.queryByText('/tmp/x.css', { selector: '.approval-detail-value' })).toBeNull();
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

  describe('AskUserQuestion', () => {
    it('renders each question with its options instead of allow/deny', () => {
      render(<ApprovalMenu requests={[askRequest]} onDecide={() => {}} />);
      expect(
        screen.getByText('What auth approach do you want?'),
      ).toBeInTheDocument();
      expect(screen.getByText('Which features?')).toBeInTheDocument();
      expect(screen.getByLabelText(/Username \+ password/)).toBeInTheDocument();
      expect(screen.getByLabelText(/Password reset/)).toBeInTheDocument();
      // The submit button replaces the generic Allow.
      expect(screen.getByRole('button', { name: /Submit answers/ })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Allow always' })).toBeNull();
    });

    it('submit is disabled until every question has a selection', () => {
      render(<ApprovalMenu requests={[askRequest]} onDecide={() => {}} />);
      const submit = screen.getByRole('button', { name: /Submit answers/ });
      expect(submit).toBeDisabled();

      fireEvent.click(screen.getByLabelText(/OAuth/));
      expect(submit).toBeDisabled();

      fireEvent.click(screen.getByLabelText(/^Login/));
      expect(submit).toBeEnabled();
    });

    it('submits answers keyed by question text with multi-select joined by ", "', () => {
      const onDecide = vi.fn();
      render(<ApprovalMenu requests={[askRequest]} onDecide={onDecide} />);

      fireEvent.click(screen.getByLabelText(/Username \+ password/));
      fireEvent.click(screen.getByLabelText(/^Login/));
      fireEvent.click(screen.getByLabelText(/Password reset/));

      fireEvent.click(screen.getByRole('button', { name: /Submit answers/ }));
      expect(onDecide).toHaveBeenCalledWith('ask1', 'allow', {
        'What auth approach do you want?': 'Username + password',
        'Which features?': 'Login, Password reset',
      });
    });

    it('single-select replaces rather than appends', () => {
      const onDecide = vi.fn();
      render(<ApprovalMenu requests={[askRequest]} onDecide={onDecide} />);

      fireEvent.click(screen.getByLabelText(/Username \+ password/));
      fireEvent.click(screen.getByLabelText(/OAuth/));
      fireEvent.click(screen.getByLabelText(/^Login/));

      fireEvent.click(screen.getByRole('button', { name: /Submit answers/ }));
      expect(onDecide).toHaveBeenCalledWith('ask1', 'allow', {
        'What auth approach do you want?': 'OAuth',
        'Which features?': 'Login',
      });
    });

    it('Skip sends a deny without answers', () => {
      const onDecide = vi.fn();
      render(<ApprovalMenu requests={[askRequest]} onDecide={onDecide} />);
      fireEvent.click(screen.getByRole('button', { name: 'Skip' }));
      expect(onDecide).toHaveBeenCalledWith('ask1', 'deny');
    });

    it('falls back to the generic menu when the input is malformed', () => {
      const broken: PermissionRequest = {
        ...askRequest,
        id: 'ask-broken',
        input: { questions: [{ question: 'no options here', options: [] }] },
      };
      render(<ApprovalMenu requests={[broken]} onDecide={() => {}} />);
      expect(screen.getByRole('button', { name: 'Allow' })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Deny' })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /Submit answers/ })).toBeNull();
    });

    it('scopes radio groups per request so two pending AskUserQuestion forms do not share state', () => {
      const second: PermissionRequest = { ...askRequest, id: 'ask2' };
      render(<ApprovalMenu requests={[askRequest, second]} onDecide={() => {}} />);
      const forms = screen.getAllByTestId('ask-user-question');
      expect(forms).toHaveLength(2);
      const first = within(forms[0]).getByLabelText(/Username \+ password/);
      fireEvent.click(first);
      const otherOAuth = within(forms[1]).getByLabelText(/OAuth/);
      expect(otherOAuth).not.toBeChecked();
    });
  });
});
