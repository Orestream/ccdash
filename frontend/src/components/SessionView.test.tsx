import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { SessionView } from './SessionView';
import { parseToolContent } from './toolContent';
import type { Message, PermissionRequest, Session } from '../types';
import * as client from '../api/client';

const session: Session = {
  id: 's1',
  projectId: 'p1',
  claudeSessionId: '',
  title: 'Add auth',
  status: 'awaiting_input',
  model: 'claude-opus-4-7',
  permissionMode: 'default',
  createdAt: '2026-05-25T12:00:00Z',
  updatedAt: '2026-05-25T12:01:00Z',
};

const sentMessage: Message = {
  id: 'm1',
  sessionId: 's1',
  role: 'user',
  content: 'hello world',
  createdAt: '2026-05-25T12:00:30Z',
};

// Minimal WebSocket stub so useWebSocket can run without a real socket.
class StubWebSocket {
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  onclose: ((ev: unknown) => void) | null = null;
  constructor() {
    // open asynchronously so the recovery effect fires
    setTimeout(() => this.onopen?.(undefined), 0);
  }
  close() {
    this.onclose?.(undefined);
  }
}

describe('SessionView composer', () => {
  beforeEach(() => {
    vi.stubGlobal('WebSocket', StubWebSocket as unknown as typeof WebSocket);
    vi.spyOn(client, 'getSession').mockResolvedValue(session);
    vi.spyOn(client, 'listMessages').mockResolvedValue([]);
    vi.spyOn(client, 'listPermissions').mockResolvedValue([] as PermissionRequest[]);
    vi.spyOn(client, 'sendMessage').mockResolvedValue(sentMessage);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('submits on Enter and clears the draft', async () => {
    render(<SessionView sessionId="s1" />);
    const textarea = (await screen.findByLabelText('Prompt')) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: 'hello world' } });
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: false });

    await waitFor(() =>
      expect(client.sendMessage).toHaveBeenCalledWith('s1', { content: 'hello world' }),
    );
    await waitFor(() => expect(textarea.value).toBe(''));
  });

  it('does NOT submit on Shift+Enter (newline)', async () => {
    render(<SessionView sessionId="s1" />);
    const textarea = await screen.findByLabelText('Prompt');

    fireEvent.change(textarea, { target: { value: 'line one' } });
    fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: true });

    expect(client.sendMessage).not.toHaveBeenCalled();
  });

  it('keeps the textarea typeable while processing', async () => {
    vi.spyOn(client, 'getSession').mockResolvedValue({
      ...session,
      status: 'processing',
    });
    render(<SessionView sessionId="s1" />);
    const textarea = (await screen.findByLabelText('Prompt')) as HTMLTextAreaElement;
    expect(textarea).not.toBeDisabled();
    fireEvent.change(textarea, { target: { value: 'queued' } });
    expect(textarea.value).toBe('queued');
  });

  it('renames the session via the title and persists on Enter', async () => {
    const renamed = { ...session, title: 'My new name' };
    const spy = vi.spyOn(client, 'renameSession').mockResolvedValue(renamed);
    render(<SessionView sessionId="s1" />);

    const heading = await screen.findByText('Add auth');
    fireEvent.click(heading);
    const input = (await screen.findByLabelText('Session title')) as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'My new name' } });
    fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() => expect(spy).toHaveBeenCalledWith('s1', 'My new name'));
    await waitFor(() => expect(screen.getByText('My new name')).toBeInTheDocument());
  });

  it('does not call rename when the title is unchanged', async () => {
    const spy = vi.spyOn(client, 'renameSession');
    render(<SessionView sessionId="s1" />);
    const heading = await screen.findByText('Add auth');
    fireEvent.click(heading);
    const input = await screen.findByLabelText('Session title');
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(spy).not.toHaveBeenCalled();
  });

  it('keeps composer drafts per session when switching chats', async () => {
    const otherSession: Session = { ...session, id: 's2', title: 'Other chat' };
    vi.spyOn(client, 'getSession').mockImplementation(async (id) =>
      id === 's2' ? otherSession : session,
    );

    const { rerender } = render(<SessionView sessionId="s1" />);
    const ta1 = (await screen.findByLabelText('Prompt')) as HTMLTextAreaElement;
    fireEvent.change(ta1, { target: { value: 'draft for s1' } });

    rerender(<SessionView sessionId="s2" />);
    const ta2 = (await screen.findByLabelText('Prompt')) as HTMLTextAreaElement;
    expect(ta2.value).toBe('');
    fireEvent.change(ta2, { target: { value: 'draft for s2' } });

    rerender(<SessionView sessionId="s1" />);
    const ta1Again = (await screen.findByLabelText('Prompt')) as HTMLTextAreaElement;
    expect(ta1Again.value).toBe('draft for s1');

    rerender(<SessionView sessionId="s2" />);
    const ta2Again = (await screen.findByLabelText('Prompt')) as HTMLTextAreaElement;
    expect(ta2Again.value).toBe('draft for s2');
  });

  it('attaches a pasted image as image-1.png and shows a chip', async () => {
    render(<SessionView sessionId="s1" />);
    const textarea = await screen.findByLabelText('Prompt');
    const file = new File(['fake-png-bytes'], 'pasted.png', { type: 'image/png' });
    fireEvent.paste(textarea, {
      clipboardData: {
        items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
      },
    });
    // FileReader resolves async, so wait for the chip.
    expect(await screen.findByText('image-1.png')).toBeInTheDocument();
    // Send button is enabled even with empty text once an image is attached.
    expect(screen.getByRole('button', { name: /send/i })).not.toBeDisabled();
  });

  it('renders attachment thumbnails for a message', async () => {
    const withImage: Message = {
      id: 'm9',
      sessionId: 's1',
      role: 'user',
      content: 'see image-1',
      createdAt: '2026-05-25T12:00:50Z',
      attachments: [
        {
          id: 'att1',
          messageId: 'm9',
          sessionId: 's1',
          name: 'image-1.png',
          mediaType: 'image/png',
          createdAt: '2026-05-25T12:00:50Z',
        },
      ],
    };
    vi.spyOn(client, 'listMessages').mockResolvedValue([withImage]);
    render(<SessionView sessionId="s1" />);
    const img = (await screen.findByAltText('image-1.png')) as HTMLImageElement;
    expect(img).toHaveAttribute('src', '/api/attachments/att1');
  });

  it('renders a tool message as tool name + file basename', async () => {
    const toolMsg: Message = {
      id: 't1',
      sessionId: 's1',
      role: 'tool',
      content: 'Edit: /home/robin/priv/ccdash/frontend/src/App.tsx',
      createdAt: '2026-05-25T12:00:40Z',
    };
    vi.spyOn(client, 'listMessages').mockResolvedValue([toolMsg]);
    render(<SessionView sessionId="s1" />);
    expect(await screen.findByText('Edit')).toBeInTheDocument();
    const detail = await screen.findByText('App.tsx');
    expect(detail).toBeInTheDocument();
    expect(detail).toHaveAttribute(
      'title',
      '/home/robin/priv/ccdash/frontend/src/App.tsx',
    );
  });
});

describe('parseToolContent', () => {
  it('shows the basename for file tools', () => {
    expect(parseToolContent('Read: /a/b/c.go')).toEqual({
      name: 'Read',
      detail: 'c.go',
      full: '/a/b/c.go',
    });
  });

  it('keeps the full detail for non-file tools', () => {
    expect(parseToolContent('Bash: git status')).toEqual({
      name: 'Bash',
      detail: 'git status',
      full: 'git status',
    });
  });

  it('handles a bare tool name with no detail', () => {
    expect(parseToolContent('Edit')).toEqual({ name: 'Edit', detail: '', full: '' });
  });
});
