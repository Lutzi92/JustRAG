export interface SSEParserOptions {
  /** Called for each parsed JSON event from the stream */
  onEvent: (data: unknown) => void;
  /** Called when [DONE] terminator is received */
  onDone?: () => void;
  /** Called on JSON parse errors for individual events */
  onParseError?: (error: unknown, rawData: string) => void;
  /** Return true to abort processing (e.g., stale request check) */
  isStale?: () => boolean;
  /**
   * Reject (and cancel the reader) when no bytes arrive for this many ms.
   * Guards against a silently hung server keeping the stream open forever.
   * Undefined or 0 disables the watchdog.
   */
  idleTimeoutMs?: number;
}

/**
 * Parses an SSE stream from a ReadableStreamDefaultReader.
 * Handles buffer management, line splitting, data: prefix, [DONE] terminator.
 *
 * The caller is responsible for acquiring and releasing the reader.
 * This function does NOT call reader.releaseLock().
 */
export async function parseSseStream(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  options: SSEParserOptions
): Promise<void> {
  const { onEvent, onDone, onParseError, isStale, idleTimeoutMs } = options;

  // Wrap reader.read() in an idle watchdog when requested. On timeout the
  // underlying stream is cancelled (tearing down the HTTP connection) and
  // the returned promise rejects; the still-pending read settling later is
  // harmless because a promise only settles once.
  const read = (): Promise<ReadableStreamReadResult<Uint8Array>> => {
    if (!idleTimeoutMs) return reader.read();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        reader.cancel().catch(() => {});
        reject(new Error(`SSE stream idle for ${idleTimeoutMs}ms; connection aborted`));
      }, idleTimeoutMs);
      reader.read().then(
        (result) => {
          clearTimeout(timer);
          resolve(result);
        },
        (err: unknown) => {
          clearTimeout(timer);
          reject(err instanceof Error ? err : new Error(String(err)));
        }
      );
    });
  };

  const decoder = new TextDecoder();
  let buffer = '';
  let streamDone = false;

  while (!streamDone) {
    const { done, value } = await read();

    if (done) {
      // Flush any remaining bytes from the decoder
      buffer += decoder.decode();
    } else {
      // Allow caller to abort processing for stale requests
      if (isStale?.()) break;

      buffer += decoder.decode(value, { stream: true });
    }

    const lines = buffer.split('\n');
    buffer = lines.pop() || '';

    for (const line of lines) {
      if (!line.startsWith('data: ')) continue;
      const dataStr = line.slice(6);

      if (dataStr === '[DONE]') {
        streamDone = true;
        onDone?.();
        break;
      }

      try {
        const data = JSON.parse(dataStr);
        onEvent(data);
      } catch (e: unknown) {
        onParseError?.(e, dataStr);
      }
    }

    if (done) {
      // Process any remaining partial line in the buffer
      if (buffer.startsWith('data: ')) {
        const dataStr = buffer.slice(6);
        if (dataStr === '[DONE]') {
          onDone?.();
        } else {
          try {
            const data = JSON.parse(dataStr);
            onEvent(data);
          } catch (e: unknown) {
            onParseError?.(e, dataStr);
          }
        }
      }
      break;
    }
  }
}
