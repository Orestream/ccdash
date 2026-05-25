// useSessionStream — accumulates `session.delta` events into a live "in-progress"
// bubble for a single session. The live bubble is cleared when the turn ends
// (status leaves `processing`) or when callers explicitly reset it (e.g. once a
// final `session.message` finalizes the turn).

import { useCallback, useState } from 'react';
import type { DeltaKind, SessionDelta } from '../types';

export interface LiveSegment {
  kind: DeltaKind;
  text: string;
}

export interface UseSessionStreamResult {
  segments: LiveSegment[];
  /** Feed a delta event; ignored if it targets another session. */
  pushDelta: (delta: SessionDelta) => void;
  /** Clear the live accumulator (turn ended / finalized). */
  reset: () => void;
}

export function useSessionStream(sessionId: string): UseSessionStreamResult {
  const [segments, setSegments] = useState<LiveSegment[]>([]);

  const pushDelta = useCallback(
    (delta: SessionDelta) => {
      if (delta.sessionId !== sessionId) return;
      setSegments((prev) => {
        const last = prev[prev.length - 1];
        // Coalesce consecutive deltas of the same kind into one segment.
        if (last && last.kind === delta.kind) {
          const next = prev.slice();
          next[next.length - 1] = { kind: last.kind, text: last.text + delta.text };
          return next;
        }
        return [...prev, { kind: delta.kind, text: delta.text }];
      });
    },
    [sessionId],
  );

  const reset = useCallback(() => {
    setSegments((prev) => (prev.length === 0 ? prev : []));
  }, []);

  return { segments, pushDelta, reset };
}

export default useSessionStream;
