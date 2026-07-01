import { useEffect, useRef, useState } from 'react';
import { API_BASE_URL, authFetch } from '../api';
import { parseSseStream } from '../utils/sseParser';
import { debounce } from '../utils/debounce';

// While a KB is ingesting, `graph_changed` events can arrive in bursts. Each one
// triggers a full re-fetch + synchronous dagre relayout, so coalesce them: fire
// at most ~once per quiet second, and at least once every few seconds during a
// continuous burst so the mind map still updates live.
const GRAPH_CHANGE_DEBOUNCE_MS = 800;
const GRAPH_CHANGE_MAX_WAIT_MS = 4000;

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
  useEffect(() => {
    onChangedRef.current = onGraphChanged;
  });

  useEffect(() => {
    if (!kbId) return;
    const abort = new AbortController();
    let stopped = false;
    const notifyChanged = debounce(
        () => onChangedRef.current(),
        GRAPH_CHANGE_DEBOUNCE_MS,
        GRAPH_CHANGE_MAX_WAIT_MS,
    );

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
              else if (ev.type === 'graph_changed') notifyChanged();
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
      notifyChanged.cancel();
    };
  }, [kbId]);

  return { processing };
}
