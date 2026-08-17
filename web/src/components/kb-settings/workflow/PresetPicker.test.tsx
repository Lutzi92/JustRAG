import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PresetPicker } from './PresetPicker';
import type { WorkflowPreset, WorkflowPresetApplyResult } from '../../../types';

vi.mock('./api', () => ({
  fetchPresets: vi.fn(),
  previewPreset: vi.fn(),
  applyPreset: vi.fn(),
}));
import { fetchPresets, previewPreset, applyPreset } from './api';

const presets: WorkflowPreset[] = [
  {
    id: 'fast',
    label: 'Schnell',
    description: 'Minimale Latenz.',
    bundle: {},
    costs: {
      lookup: { estLlmCalls: 3, estLatencyMs: 1200 },
      enumeration: { estLlmCalls: 3, estLatencyMs: 1200 },
      complex_reasoning: { estLlmCalls: 3, estLatencyMs: 1200 },
    },
  },
  {
    id: 'high_precision',
    label: 'Hohe Präzision',
    description: 'Maximale Sorgfalt.',
    bundle: {},
    costs: {
      lookup: { estLlmCalls: 11, estLatencyMs: 9000 },
      enumeration: { estLlmCalls: 11, estLatencyMs: 9000 },
      complex_reasoning: { estLlmCalls: 8, estLatencyMs: 7000 },
    },
  },
];

/** A preview/apply response with the fields the server always sends. */
function result(over: Partial<WorkflowPresetApplyResult> = {}): WorkflowPresetApplyResult {
  return { preset: 'fast', label: 'Schnell', overwrites: [], effective: [], pinned: 21, ...over };
}

/** A promise the test resolves/rejects by hand — the only way to hold the
 * component in its in-flight state long enough to click through it. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: Error) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function renderPicker(over: Partial<Parameters<typeof PresetPicker>[0]> = {}) {
  const onError = vi.fn();
  const onApplied = vi.fn().mockResolvedValue(undefined);
  const props = {
    kbId: 'kb-1',
    lane: 'complex_reasoning' as const,
    presetBase: '',
    presetBaseKnown: true,
    presetBaseLoading: false,
    deviations: [],
    draftPending: false,
    onError,
    onApplied,
    ...over,
  };
  const utils = render(<PresetPicker {...props} />);
  return {
    ...utils,
    onError,
    onApplied,
    /** Re-render with changed props (a draft appearing mid-dialog, say). */
    update: (next: Partial<Parameters<typeof PresetPicker>[0]>) =>
      utils.rerender(<PresetPicker {...props} {...next} />),
  };
}

describe('PresetPicker', () => {
  beforeEach(() => {
    vi.mocked(fetchPresets).mockReset();
    vi.mocked(previewPreset).mockReset();
    vi.mocked(applyPreset).mockReset();
  });

  it('lists every preset with its label and the cost for the current lane', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    renderPicker({ lane: 'complex_reasoning' });

    expect(await screen.findByText('Schnell')).toBeInTheDocument();
    expect(screen.getByText('Hohe Präzision')).toBeInTheDocument();
    // complex_reasoning lane: high_precision costs 8, not its lookup figure of 11.
    expect(screen.getByText(/8 LLM-Aufrufe/)).toBeInTheDocument();
    expect(screen.queryByText(/11 LLM-Aufrufe/)).not.toBeInTheDocument();
  });

  it('shows the lookup-lane cost when the lookup lane is selected, not the complex figure', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    renderPicker({ lane: 'lookup' });

    expect(await screen.findByText(/11 LLM-Aufrufe/)).toBeInTheDocument();
    expect(screen.queryByText(/8 LLM-Aufrufe/)).not.toBeInTheDocument();
  });

  // The badge prices the bundle over the DEPLOYMENT defaults (PricePresets);
  // the canvas's own cost line prices THIS KB. They diverge whenever the KB
  // overrides a cost-bearing key outside the preset vocabulary, so the list has
  // to say which configuration its numbers describe.
  it('says which configuration the cost badges describe', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    renderPicker();

    expect(await screen.findByText(/ohne weitere eigene Einstellungen/)).toBeInTheDocument();
  });

  it('selecting a preset shows a confirmation naming what actually changes', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(result({
      overwrites: ['crag_enabled'],
      effective: ['chat_supervisor_enabled', 'crag_enabled'],
      pinned: 21,
    }));
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));

    // The number that decides whether clicking is safe is the EFFECTIVE one…
    expect(await screen.findByText(/2 Einstellungen ändern dadurch ihr Verhalten/)).toBeInTheDocument();
    // …and the admin's own share is stated separately, not instead of it.
    expect(screen.getByText(/Davon hast du 1 selbst gesetzt/)).toBeInTheDocument();
    expect(previewPreset).toHaveBeenCalledWith('kb-1', 'fast');
    expect(previewPreset).toHaveBeenCalledTimes(1);
  });

  // The finding this pins: a KB that has overridden nothing gets overwrites=[]
  // and the old dialog therefore said „Keine deiner eigenen Einstellungen wird
  // überschrieben." while the apply pinned 21 keys and (on this deployment)
  // turned the globally-enabled supervisor off.
  it('still names the behaviour changes when the admin has overridden nothing', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(result({
      overwrites: [],
      effective: ['chat_supervisor_enabled'],
      pinned: 21,
    }));
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));

    expect(await screen.findByText(/1 Einstellung ändert dadurch ihr Verhalten/)).toBeInTheDocument();
    expect(screen.getByText(/Keine davon hast du selbst gesetzt/)).toBeInTheDocument();
    expect(within(screen.getByRole('dialog')).getByText('chat_supervisor_enabled')).toBeInTheDocument();
  });

  it('states how many settings the apply pins away from the global defaults', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(result({ effective: [], overwrites: [], pinned: 21 }));
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));

    expect(await screen.findByText(/Keine Einstellung ändert dadurch ihr Verhalten/)).toBeInTheDocument();
    expect(screen.getByText(/21 Einstellungen werden dabei für diese Wissensbank festgeschrieben/))
      .toBeInTheDocument();
  });

  // A 200 that is missing a field yields `undefined`, which passes a
  // `=== null` "still loading" guard and then throws on `.length` in render.
  it('survives a response missing the new fields instead of crashing', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(
      { preset: 'fast', label: 'Schnell' } as unknown as WorkflowPresetApplyResult,
    );
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));

    expect(await screen.findByText(/Keine Einstellung ändert dadurch ihr Verhalten/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^anwenden$/i })).toBeEnabled();
  });

  it('confirming calls the apply endpoint exactly once and refetches', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(result({ overwrites: ['crag_enabled'], effective: ['crag_enabled'] }));
    vi.mocked(applyPreset).mockResolvedValue(result({ overwrites: ['crag_enabled'], effective: ['crag_enabled'] }));
    const { onApplied } = renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    await screen.findByText(/1 Einstellung ändert dadurch ihr Verhalten/);

    await userEvent.click(screen.getByRole('button', { name: /^anwenden$/i }));

    await waitFor(() => expect(applyPreset).toHaveBeenCalledTimes(1));
    expect(applyPreset).toHaveBeenCalledWith('kb-1', 'fast');
    await waitFor(() => expect(onApplied).toHaveBeenCalledTimes(1));
    // The dialog closes on success.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('cancelling calls neither apply nor refetch', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(result({ overwrites: ['crag_enabled'], effective: ['crag_enabled'] }));
    const { onApplied } = renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    await screen.findByText(/ihr Verhalten/);

    await userEvent.click(screen.getByRole('button', { name: /abbrechen/i }));

    expect(applyPreset).not.toHaveBeenCalled();
    expect(onApplied).not.toHaveBeenCalled();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('a failed apply surfaces the server message and leaves the picker usable', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(result());
    vi.mocked(applyPreset).mockRejectedValue(new Error('chat_self_rag_enabled and chat_factuality_verifier_enabled cannot both be enabled'));
    const { onError, onApplied } = renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    await screen.findByText(/Keine Einstellung ändert dadurch ihr Verhalten/);
    await userEvent.click(screen.getByRole('button', { name: /^anwenden$/i }));

    await waitFor(() => expect(onError).toHaveBeenCalledWith(
      expect.stringContaining('chat_self_rag_enabled and chat_factuality_verifier_enabled cannot both be enabled'),
    ));
    expect(onApplied).not.toHaveBeenCalled();
    // Picker is usable again: the dialog is gone and the card can be clicked once more.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    vi.mocked(previewPreset).mockResolvedValue(result());
    await userEvent.click(screen.getByText('Schnell'));
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  // -------------------------------------------------------------------------
  // The property the whole design rests on: „Anwenden" is unreachable until
  // the server has said what the apply costs, and unreachable while a draft
  // could be saved back over it. Nothing tested these before — a refactor that
  // enabled the button during the loading state would have left the suite green.
  // -------------------------------------------------------------------------

  describe('the apply gate', () => {
    it('does not apply while the preview is still in flight', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      const pending = deferred<WorkflowPresetApplyResult>();
      vi.mocked(previewPreset).mockReturnValue(pending.promise);
      renderPicker();

      await userEvent.click(await screen.findByText('Schnell'));
      const dialog = await screen.findByRole('dialog');
      expect(within(dialog).getByText(/Prüft, was sich ändert/)).toBeInTheDocument();

      const apply = within(dialog).getByRole('button', { name: /^anwenden$/i });
      expect(apply).toBeDisabled();
      await userEvent.click(apply);
      expect(applyPreset).not.toHaveBeenCalled();

      // And it becomes reachable once the numbers are in — otherwise this test
      // would also pass against a dialog that never enables the button at all.
      pending.resolve(result({ effective: ['crag_enabled'] }));
      await waitFor(() => expect(apply).toBeEnabled());
    });

    it('does not apply when a draft appeared after the dialog was opened', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockResolvedValue(result({ effective: ['crag_enabled'] }));
      const { update } = renderPicker({ draftPending: false });

      await userEvent.click(await screen.findByText('Schnell'));
      await screen.findByText(/ihr Verhalten/);

      // The canvas behind the dialog is still live: an edit there creates a
      // draft while the confirmation is open.
      update({ draftPending: true });

      await userEvent.click(screen.getByRole('button', { name: /^anwenden$/i }));
      expect(applyPreset).not.toHaveBeenCalled();
    });

    it('applies once for a double click, not twice', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockResolvedValue(result({ effective: ['crag_enabled'] }));
      const applying = deferred<WorkflowPresetApplyResult>();
      vi.mocked(applyPreset).mockReturnValue(applying.promise);
      renderPicker();

      await userEvent.click(await screen.findByText('Schnell'));
      await screen.findByText(/ihr Verhalten/);

      const apply = screen.getByRole('button', { name: /^anwenden$/i });
      await userEvent.click(apply);
      await userEvent.click(apply);

      expect(applyPreset).toHaveBeenCalledTimes(1);
      applying.resolve(result());
    });
  });

  describe('a preview that never lands on its dialog', () => {
    it('reports a failed preview and closes the dialog it belonged to', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockRejectedValue(new Error('preview preset: 500'));
      const { onError } = renderPicker();

      await userEvent.click(await screen.findByText('Schnell'));

      await waitFor(() => expect(onError).toHaveBeenCalledWith(expect.stringContaining('preview preset: 500')));
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(applyPreset).not.toHaveBeenCalled();
    });

    it('stays silent when a cancelled dialog\'s preview rejects afterwards', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      const pending = deferred<WorkflowPresetApplyResult>();
      vi.mocked(previewPreset).mockReturnValue(pending.promise);
      const { onError } = renderPicker();

      await userEvent.click(await screen.findByText('Schnell'));
      await screen.findByRole('dialog');
      await userEvent.click(screen.getByRole('button', { name: /abbrechen/i }));

      pending.reject(new Error('preview preset: 500'));
      await waitFor(() => expect(previewPreset).toHaveBeenCalledTimes(1));

      // The dialog it belonged to is gone; an error banner for it would be
      // about nothing the admin can see.
      expect(onError).not.toHaveBeenCalled();
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });

    it('never lets a stale preview populate the dialog that replaced it', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      const stale = deferred<WorkflowPresetApplyResult>();
      vi.mocked(previewPreset).mockReturnValueOnce(stale.promise);
      renderPicker();

      await userEvent.click(await screen.findByText('Schnell'));
      await screen.findByRole('dialog');
      await userEvent.click(screen.getByRole('button', { name: /abbrechen/i }));

      vi.mocked(previewPreset).mockResolvedValue(result({
        preset: 'high_precision', label: 'Hohe Präzision', effective: ['crag_enabled'],
      }));
      await userEvent.click(screen.getByText('Hohe Präzision'));
      await screen.findByText(/ihr Verhalten/);

      // The first dialog's response arrives now, naming a key the second
      // preset does not touch.
      stale.resolve(result({ effective: ['STALE_KEY'], overwrites: ['STALE_KEY'] }));
      await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());
      expect(within(screen.getByRole('dialog')).queryByText('STALE_KEY')).not.toBeInTheDocument();
    });
  });

  // -------------------------------------------------------------------------
  // Focus. On open the trigger is disabled, the browser blurs it, and focus
  // falls to <body> — from where a keyboard user could reach the canvas BEHIND
  // an open confirmation for a destructive bulk write.
  // -------------------------------------------------------------------------

  describe('dialog focus', () => {
    it('moves focus into the dialog on open and back to the trigger on close', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockResolvedValue(result({ effective: ['crag_enabled'] }));
      renderPicker();

      const card = await screen.findByText('Schnell');
      const trigger = card.closest('button')!;
      await userEvent.click(trigger);

      const dialog = await screen.findByRole('dialog');
      await waitFor(() => expect(dialog).toHaveFocus());

      await userEvent.click(screen.getByRole('button', { name: /abbrechen/i }));
      await waitFor(() => expect(trigger).toHaveFocus());
    });

    // The trigger can be GONE or disabled by the time focus is restored — a
    // draft appearing while the dialog is open disables every card. Falling
    // through to <body> would strand a keyboard user at the top of the
    // document, so the picker root takes the focus instead.
    it('falls back to the picker when the trigger is disabled at close', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockResolvedValue(result({ effective: ['crag_enabled'] }));
      const { update } = renderPicker({ draftPending: false });

      const card = await screen.findByText('Schnell');
      const trigger = card.closest('button')!;
      await userEvent.click(trigger);
      await screen.findByRole('dialog');

      update({ draftPending: true });
      await userEvent.click(screen.getByRole('button', { name: /abbrechen/i }));

      await waitFor(() => {
        expect(trigger).toBeDisabled();
        expect(document.body).not.toHaveFocus();
        expect(document.activeElement).toHaveClass('wf-presets');
      });
    });

    it('closes on Escape', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockResolvedValue(result({ effective: ['crag_enabled'] }));
      renderPicker();

      await userEvent.click(await screen.findByText('Schnell'));
      await screen.findByRole('dialog');

      await userEvent.keyboard('{Escape}');

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
      expect(applyPreset).not.toHaveBeenCalled();
    });

    it('keeps Tab inside the dialog', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockResolvedValue(result({ effective: ['crag_enabled'] }));
      renderPicker();

      await userEvent.click(await screen.findByText('Schnell'));
      const dialog = await screen.findByRole('dialog');
      await screen.findByText(/ihr Verhalten/);

      // Tab through every control and one past the last one.
      const controls = within(dialog).getAllByRole('button');
      for (let i = 0; i <= controls.length; i += 1) {
        await userEvent.tab();
        expect(dialog.contains(document.activeElement)).toBe(true);
      }
    });
  });

  describe('deviation line (Basis: …)', () => {
    // "" is both "no base" and "not loaded yet"; only the canvas knows which,
    // and asserting the former during the latter tells a KB that IS based on
    // „Hohe Präzision" the opposite for the duration of the load.
    it('claims nothing about the base while the projection is still loading', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      renderPicker({ presetBase: '', presetBaseKnown: true, deviations: [], presetBaseLoading: true });

      expect(await screen.findByText('Schnell')).toBeInTheDocument();
      expect(screen.queryByText(/Noch keine Vorlage/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Basis:/)).not.toBeInTheDocument();
    });

    it('says plainly that no preset has been applied, with no deviation count, when there is no base', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      renderPicker({ presetBase: '', presetBaseKnown: true, deviations: [] });

      expect(await screen.findByText(/Noch keine Vorlage/)).toBeInTheDocument();
      expect(screen.queryByText(/Abweichung/)).not.toBeInTheDocument();
    });

    it('renders "Basis: <Label> · N Abweichungen" with a reset action for a live base', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      renderPicker({ presetBase: 'fast', presetBaseKnown: true, deviations: ['crag_enabled', 'mmr_lambda'] });

      expect(await screen.findByText(/Basis: Schnell · 2 Abweichungen/)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /zurücksetzen/i })).toBeInTheDocument();
    });

    it('uses the singular for exactly one deviation', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      renderPicker({ presetBase: 'fast', presetBaseKnown: true, deviations: ['crag_enabled'] });

      expect(await screen.findByText(/Basis: Schnell · 1 Abweichung\b/)).toBeInTheDocument();
      expect(screen.queryByText(/1 Abweichungen/)).not.toBeInTheDocument();
    });

    it('hides the reset action when the KB already matches its base exactly', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      renderPicker({ presetBase: 'fast', presetBaseKnown: true, deviations: [] });

      expect(await screen.findByText(/Basis: Schnell · 0 Abweichungen/)).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /zurücksetzen/i })).not.toBeInTheDocument();
    });

    it('renders an honest, distinct message for a base that no longer resolves to a real preset', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      renderPicker({ presetBase: 'retired_preset', presetBaseKnown: false, deviations: [] });

      const base = await screen.findByText(/retired_preset/);
      // No NUMBERED deviation count — that would claim a bundle exists to
      // compare against when there is none. The message may still use the
      // word "Abweichungen" to explain why it cannot be determined.
      expect(base.textContent).not.toMatch(/\d+ Abweichung/);
      expect(screen.queryByRole('button', { name: /zurücksetzen/i })).not.toBeInTheDocument();
    });

    it('the reset action re-applies the current base preset', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      vi.mocked(previewPreset).mockResolvedValue(result({ overwrites: ['crag_enabled'], effective: ['crag_enabled'] }));
      vi.mocked(applyPreset).mockResolvedValue(result({ overwrites: ['crag_enabled'], effective: ['crag_enabled'] }));
      renderPicker({ presetBase: 'fast', presetBaseKnown: true, deviations: ['crag_enabled'] });

      await userEvent.click(await screen.findByRole('button', { name: /zurücksetzen/i }));
      await screen.findByText(/1 Einstellung ändert dadurch ihr Verhalten/);
      await userEvent.click(screen.getByRole('button', { name: /^anwenden$/i }));

      await waitFor(() => expect(applyPreset).toHaveBeenCalledWith('kb-1', 'fast'));
    });
  });

  describe('a pending draft', () => {
    it('blocks preset selection and explains why, rather than silently discarding the draft', async () => {
      vi.mocked(fetchPresets).mockResolvedValue(presets);
      renderPicker({ draftPending: true });

      expect(await screen.findByText(/Speichere oder verwirf/)).toBeInTheDocument();
      await userEvent.click(screen.getByText('Schnell'));
      expect(previewPreset).not.toHaveBeenCalled();
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
  });

  it('surfaces a preset-list load failure locally without crashing', async () => {
    vi.mocked(fetchPresets).mockRejectedValue(new Error('fetch presets: 500'));
    renderPicker();

    expect(await screen.findByRole('alert')).toHaveTextContent(/nicht geladen werden/);
  });

  it('lists the changed keys inside the confirmation, not just the count', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue(result({
      overwrites: ['crag_enabled'],
      effective: ['crag_enabled', 'mmr_lambda'],
    }));
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByText('crag_enabled')).toBeInTheDocument();
    expect(within(dialog).getByText('mmr_lambda')).toBeInTheDocument();
    // The admin's own key is marked as such inside the same list.
    expect(within(dialog).getByText(/von dir gesetzt/)).toBeInTheDocument();
  });
});
