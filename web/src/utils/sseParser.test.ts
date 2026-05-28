import { describe, it, expect, vi } from 'vitest';
import { parseSseStream } from './sseParser';


// ---------------------------------------------------------------------------
// Helper: create a mock ReadableStreamDefaultReader from predefined chunks
// ---------------------------------------------------------------------------
function createMockReader(chunks: string[]): ReadableStreamDefaultReader<Uint8Array> {
  const encoder = new TextEncoder();
  let index = 0;

  return {
    read: vi.fn(async () => {
      if (index >= chunks.length) {
        return { done: true as const, value: undefined };
      }
      const value = encoder.encode(chunks[index]);
      index++;
      return { done: false as const, value };
    }),
    releaseLock: vi.fn(),
    cancel: vi.fn(),
    closed: Promise.resolve(undefined),
  } as unknown as ReadableStreamDefaultReader<Uint8Array>;
}

describe('parseSseStream', () => {
  it('should parse a single event', async () => {
    const reader = createMockReader([
      'data: {"type":"hello","value":42}\n\n',
    ]);
    const events: unknown[] = [];

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
    });

    expect(events).toEqual([{ type: 'hello', value: 42 }]);
  });

  it('should parse multiple events', async () => {
    const reader = createMockReader([
      'data: {"a":1}\ndata: {"a":2}\ndata: {"a":3}\n',
    ]);
    const events: unknown[] = [];

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
    });

    expect(events).toEqual([{ a: 1 }, { a: 2 }, { a: 3 }]);
  });

  it('should handle [DONE] terminator and call onDone', async () => {
    const reader = createMockReader([
      'data: {"first":true}\ndata: [DONE]\ndata: {"ignored":true}\n',
    ]);
    const events: unknown[] = [];
    const onDone = vi.fn();

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
      onDone,
    });

    expect(events).toEqual([{ first: true }]);
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it('should handle incomplete buffer (data split across chunks)', async () => {
    // Split a single SSE event across two chunks
    const reader = createMockReader([
      'data: {"sp',
      'lit":"value"}\n\n',
    ]);
    const events: unknown[] = [];

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
    });

    expect(events).toEqual([{ split: 'value' }]);
  });

  it('should handle JSON parse errors and call onParseError', async () => {
    const reader = createMockReader([
      'data: not-valid-json\ndata: {"valid":true}\n',
    ]);
    const events: unknown[] = [];
    const onParseError = vi.fn();

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
      onParseError,
    });

    expect(events).toEqual([{ valid: true }]);
    expect(onParseError).toHaveBeenCalledTimes(1);
    expect(onParseError.mock.calls[0][1]).toBe('not-valid-json');
  });

  it('should stop processing when isStale returns true', async () => {
    let stale = false;
    const reader = createMockReader([
      'data: {"first":true}\n',
      'data: {"second":true}\n',
      'data: {"third":true}\n',
    ]);
    const events: unknown[] = [];

    await parseSseStream(reader, {
      onEvent: (data) => {
        events.push(data);
        // After processing first event, mark as stale
        stale = true;
      },
      isStale: () => stale,
    });

    // First chunk is processed (isStale checked after read, before processing),
    // second chunk is read but isStale returns true so processing stops.
    expect(events).toEqual([{ first: true }]);
  });

  it('should ignore empty lines and non-data lines', async () => {
    const reader = createMockReader([
      '\n\nevent: ping\nid: 123\ndata: {"real":"event"}\n\n',
    ]);
    const events: unknown[] = [];

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
    });

    expect(events).toEqual([{ real: 'event' }]);
  });

  it('should handle reader that ends without [DONE]', async () => {
    const reader = createMockReader([
      'data: {"a":1}\ndata: {"a":2}\n',
      // Reader ends (done: true) without sending [DONE]
    ]);
    const events: unknown[] = [];
    const onDone = vi.fn();

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
      onDone,
    });

    // Events should still be processed
    expect(events).toEqual([{ a: 1 }, { a: 2 }]);
    // onDone should NOT be called since [DONE] was never received
    expect(onDone).not.toHaveBeenCalled();
  });

  it('should handle multiple chunks building up a complete line', async () => {
    // Three chunks that together form one complete SSE event
    const reader = createMockReader([
      'da',
      'ta: {"k',
      'ey":"val"}\n',
    ]);
    const events: unknown[] = [];

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
    });

    expect(events).toEqual([{ key: 'val' }]);
  });

  it('should not call onParseError when onParseError is not provided', async () => {
    const reader = createMockReader([
      'data: {bad json}\ndata: {"good":true}\n',
    ]);
    const events: unknown[] = [];

    // Should not throw even without onParseError handler
    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
    });

    expect(events).toEqual([{ good: true }]);
  });

  it('should process events from multiple chunks correctly', async () => {
    const reader = createMockReader([
      'data: {"chunk":1}\n',
      'data: {"chunk":2}\n',
      'data: {"chunk":3}\ndata: [DONE]\n',
    ]);
    const events: unknown[] = [];

    await parseSseStream(reader, {
      onEvent: (data) => events.push(data),
    });

    expect(events).toEqual([{ chunk: 1 }, { chunk: 2 }, { chunk: 3 }]);
  });
});
