import { useEffect, useState } from 'react';
import { fetchKbAgents, type KbAgents } from '../components/agents/api';

// Loads the picker options (agents + teams attached to the KB). Refreshes on
// KB switch. Errors degrade to an empty picker (Standard only).
export function useKbAgents(kbId: string | undefined) {
  const [options, setOptions] = useState<KbAgents>({ agents: [], teams: [] });
  useEffect(() => {
    // Reset on EVERY kbId change (not only the empty case) so the previous
    // KB's options never leak into the new KB while its fetch is in flight —
    // ChatView's default-selection effect would otherwise re-apply the old
    // KB's isDefault team/agent against the new KB.
    setOptions({ agents: [], teams: [] });
    if (!kbId) return;
    let cancelled = false;
    fetchKbAgents(kbId)
      .then(o => { if (!cancelled) setOptions(o); })
      .catch(() => { if (!cancelled) setOptions({ agents: [], teams: [] }); });
    return () => { cancelled = true; };
  }, [kbId]);
  return options;
}
