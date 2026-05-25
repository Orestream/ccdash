import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { SessionView } from './SessionView';
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
});
