import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import ResearchMode from './ResearchMode';

// The research stream is relayed from a worker: the worker persists the report
// and then publishes an internal `__done__` marker, which makes the relay close
// the SSE connection. No `data: [DONE]` line ever reaches the browser — that
// token belongs to the chat/openai-compat/publicapi endpoints, not this one.
// These tests pin that the component notices completion from the stream ending.
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'de' }) }));
vi.mock('../contexts/ToastContext', () => ({ useToast: () => ({ error: vi.fn(), success: vi.fn() }) }));

const authFetch = vi.fn();
vi.mock('../api', () => ({
  API_BASE_URL: '',
  authFetch: (...args: unknown[]) => authFetch(...args),
}));

/** A Response whose body streams the given SSE lines and then closes. */
function sseResponse(lines: string[]): Response {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const line of lines) controller.enqueue(encoder.encode(`data: ${line}\n\n`));
      controller.close();
    },
  });
  return { ok: true, status: 200, body } as unknown as Response;
}

async function runResearch(onSessionSaved: () => void) {
  render(
    <ResearchMode
      kbId="kb1"
      onClose={vi.fn()}
      loadedSession={null}
      onSessionSaved={onSessionSaved}
      onClearSession={vi.fn()}
      onRunningChange={vi.fn()}
    />,
  );
  // ResearchMode carries its own de/en label object rather than going through
  // t(), so these are the real strings the German UI renders.
  await userEvent.type(screen.getByPlaceholderText(/Was möchten Sie recherchieren/), 'Zero-Trust');
  await userEvent.click(screen.getByRole('button', { name: 'Recherche starten' }));
}

beforeEach(() => {
  authFetch.mockReset();
  vi.clearAllMocks();
});

describe('ResearchMode: Abschluss des Streams', () => {
  it('meldet die gespeicherte Sitzung, wenn der Stream ohne [DONE] endet', async () => {
    authFetch.mockResolvedValue(
      sseResponse([
        JSON.stringify({ type: 'session', sessionId: 's1' }),
        JSON.stringify({ type: 'complete', stepNumber: 10, totalSteps: 10, report: '# Bericht' }),
      ]),
    );
    const onSessionSaved = vi.fn();
    await runResearch(onSessionSaved);

    await waitFor(() => expect(onSessionSaved).toHaveBeenCalled());
  });

  it('meldet auch dann, wenn der Stream gar keine Events geliefert hat', async () => {
    authFetch.mockResolvedValue(sseResponse([]));
    const onSessionSaved = vi.fn();
    await runResearch(onSessionSaved);

    // The backend creates the chat row before the first event, so it belongs in
    // the history even when the run produced nothing.
    await waitFor(() => expect(onSessionSaved).toHaveBeenCalled());
  });
});
