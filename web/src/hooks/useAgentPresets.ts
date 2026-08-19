import { useEffect, useState } from 'react';
import { listAgents } from '../components/agents/api';
import type { Preset } from '../components/Studio/WorkspacePromptDialog';

/**
 * Turns the user's own agents into selectable prompt templates: an agent's
 * persona prompt is frequently exactly the instruction they want to run an
 * analysis or a comparison with, and until now it was only reachable by
 * copying it out of the agent editor.
 *
 * This yields the agent's PROMPT only. Running a generation through the agent
 * pipeline stays the job of the separate agent picker in the same dialog —
 * the two are deliberately independent, so a user can take one agent's wording
 * and have a different agent (or none) execute it.
 *
 * Agents that are disabled, or whose persona prompt is blank, are skipped:
 * selecting them would insert nothing and look broken. Errors degrade to an
 * empty list — the built-in templates keep working.
 */
export function useAgentPresets(): Preset[] {
  const [presets, setPresets] = useState<Preset[]>([]);

  useEffect(() => {
    let cancelled = false;
    listAgents()
      .then(agents => {
        if (cancelled) return;
        setPresets(
          (agents ?? [])
            .filter(a => a.isEnabled && a.systemPrompt?.trim())
            .map(a => ({ label: a.name, prompt: a.systemPrompt })),
        );
      })
      .catch(() => { if (!cancelled) setPresets([]); });
    return () => { cancelled = true; };
  }, []);

  return presets;
}
