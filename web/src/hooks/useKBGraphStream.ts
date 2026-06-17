import { useEffect, useRef, useState } from 'react';
import { API_BASE_URL, authFetch } from '../api';
import { parseSseStream } from '../utils/sseParser';

interface GraphStreamEvent {
  type: 'status' | 'graph_changed' | 'heartbeat';
  processing?: boolean;
}

/**
 * Subscribes to the KB's mindmap live-update stream
 * (GET /api/kb/{id}/graph/stream). Calls onGraphChanged() when the backend
 * reports the graph data changed, and exposes `processing` (true while the KB
 * is still being ingested / its graph built). Reconnects with a short backoff
 * if the stream drops while the component is mounted.
 */
export function useKBGraphStream(kbId: string, onGraphChanged: () => void): { processing: boolean } {
  const [processing, setProcessing] = useState(false);
  // Keep the latest callback without re-subscribing on every render.
  const onChangedRef = useRef(onGraphChanged);
  onChangedRef.current = onGraphChanged;

  useEffect(() => {
    if (!kbId) return;
    const abort = new AbortController();
    let stopped = false;

    const run = async () => {
      while (!stopped) {
        try {
          const res = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/graph/stream`, {
            signal: abort.signal,
          });
          if (!res.ok || !res.body) throw new Error(`graph stream open failed: ${res.status}`);
          const reader = res.body.getReader();
          await parseSseStream(reader, {
            // Heartbeats arrive every ~15s; if 3 in a row are missed the
            // server/connection is dead — abort and reconnect.
            idleTimeoutMs: 45_000,
            isStale: () => stopped,
            onParseError: (e) => console.error('graph stream parse error', e),
            onEvent: (data: unknown) => {
              const ev = data as GraphStreamEvent;
              if (ev.type === 'status') setProcessing(!!ev.processing);
              else if (ev.type === 'graph_changed') onChangedRef.current();
              // 'heartbeat' and unknown types: ignore.
            },
          });
        } catch {
          // Swallow — disconnect/abort is normal. Fall through to backoff.
        }
        if (stopped) break;
        await new Promise((r) => setTimeout(r, 3000));
      }
    };
    void run();

    return () => {
      stopped = true;
      abort.abort();
    };
  }, [kbId]);

  return { processing };
}
