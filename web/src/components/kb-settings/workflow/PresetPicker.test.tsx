import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PresetPicker } from './PresetPicker';
import type { WorkflowPreset } from '../../../types';

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

function renderPicker(over: Partial<Parameters<typeof PresetPicker>[0]> = {}) {
  const onError = vi.fn();
  const onApplied = vi.fn().mockResolvedValue(undefined);
  const utils = render(
    <PresetPicker
      kbId="kb-1"
      lane="complex_reasoning"
      presetBase=""
      presetBaseKnown
      deviations={[]}
      draftPending={false}
      onError={onError}
      onApplied={onApplied}
      {...over}
    />,
  );
  return { ...utils, onError, onApplied };
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

  it('selecting a preset shows a confirmation naming the overwrite count from the preview endpoint', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue({
      preset: 'fast', label: 'Schnell', overwrites: ['crag_enabled', 'mmr_lambda'],
    });
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));

    expect(await screen.findByText(/2 deiner Einstellungen werden überschrieben/)).toBeInTheDocument();
    expect(previewPreset).toHaveBeenCalledWith('kb-1', 'fast');
    expect(previewPreset).toHaveBeenCalledTimes(1);
  });

  it('shows a reassuring line, not a false zero, when the preview reports no overwrites', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: [] });
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));

    expect(await screen.findByText(/Keine deiner eigenen Einstellungen wird überschrieben/)).toBeInTheDocument();
  });

  it('confirming calls the apply endpoint exactly once and refetches', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: ['crag_enabled'] });
    vi.mocked(applyPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: ['crag_enabled'] });
    const { onApplied } = renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    await screen.findByText(/1 deiner Einstellungen wird überschrieben/);

    await userEvent.click(screen.getByRole('button', { name: /^anwenden$/i }));

    await waitFor(() => expect(applyPreset).toHaveBeenCalledTimes(1));
    expect(applyPreset).toHaveBeenCalledWith('kb-1', 'fast');
    await waitFor(() => expect(onApplied).toHaveBeenCalledTimes(1));
    // The dialog closes on success.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('cancelling calls neither apply nor refetch', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: ['crag_enabled'] });
    const { onApplied } = renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    await screen.findByText(/wird überschrieben/);

    await userEvent.click(screen.getByRole('button', { name: /abbrechen/i }));

    expect(applyPreset).not.toHaveBeenCalled();
    expect(onApplied).not.toHaveBeenCalled();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('a failed apply surfaces the server message and leaves the picker usable', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: [] });
    vi.mocked(applyPreset).mockRejectedValue(new Error('chat_self_rag_enabled and chat_factuality_verifier_enabled cannot both be enabled'));
    const { onError, onApplied } = renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    await screen.findByText(/Keine deiner eigenen Einstellungen/);
    await userEvent.click(screen.getByRole('button', { name: /^anwenden$/i }));

    await waitFor(() => expect(onError).toHaveBeenCalledWith(
      expect.stringContaining('chat_self_rag_enabled and chat_factuality_verifier_enabled cannot both be enabled'),
    ));
    expect(onApplied).not.toHaveBeenCalled();
    // Picker is usable again: the dialog is gone and the card can be clicked once more.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    vi.mocked(previewPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: [] });
    await userEvent.click(screen.getByText('Schnell'));
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  describe('deviation line (Basis: …)', () => {
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
      vi.mocked(previewPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: ['crag_enabled'] });
      vi.mocked(applyPreset).mockResolvedValue({ preset: 'fast', label: 'Schnell', overwrites: ['crag_enabled'] });
      renderPicker({ presetBase: 'fast', presetBaseKnown: true, deviations: ['crag_enabled'] });

      await userEvent.click(await screen.findByRole('button', { name: /zurücksetzen/i }));
      await screen.findByText(/1 deiner Einstellungen wird überschrieben/);
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

  it('lists the overwritten keys inside the confirmation, not just the count', async () => {
    vi.mocked(fetchPresets).mockResolvedValue(presets);
    vi.mocked(previewPreset).mockResolvedValue({
      preset: 'fast', label: 'Schnell', overwrites: ['crag_enabled', 'mmr_lambda'],
    });
    renderPicker();

    await userEvent.click(await screen.findByText('Schnell'));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByText('crag_enabled')).toBeInTheDocument();
    expect(within(dialog).getByText('mmr_lambda')).toBeInTheDocument();
  });
});
