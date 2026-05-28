export interface SSEParserOptions {
  /** Called for each parsed JSON event from the stream */
  onEvent: (data: unknown) => void;
  /** Called when [DONE] terminator is received */
  onDone?: () => void;
  /** Called on JSON parse errors for individual events */
  onParseError?: (error: unknown, rawData: string) => void;
  /** Return true to abort processing (e.g., stale request check) */
  isStale?: () => boolean;
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
  const { onEvent, onDone, onParseError, isStale } = options;

  const decoder = new TextDecoder();
  let buffer = '';
  let streamDone = false;

  while (!streamDone) {
    const { done, value } = await reader.read();

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
