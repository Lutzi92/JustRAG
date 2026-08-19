import { useEffect, useState } from 'react';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import type { Preset } from '../components/Studio/WorkspacePromptDialog';

interface WorkspacePresets {
  analysis: Preset[];
  comparison: Preset[];
  compareEnabled: boolean;
}

const EMPTY: WorkspacePresets = { analysis: [], comparison: [], compareEnabled: false };

/**
 * Lädt die im Workspace angebotenen Prompt-Presets und das Vergleichs-Flag.
 * Fehler degradieren auf leere Listen — die Dialoge bleiben dann bedienbar,
 * nur ohne Vorschläge.
 */
export function useWorkspacePresets(kbId: string | undefined, lang: string): WorkspacePresets {
  const [data, setData] = useState<WorkspacePresets>(EMPTY);
  useEffect(() => {
    setData(EMPTY);
    if (!kbId) return;
    let cancelled = false;
    axios.get<WorkspacePresets>(`${API_BASE_URL}/api/kb/${kbId}/workspace/presets`, { params: { lang } })
      .then(res => { if (!cancelled) setData(res.data); })
      .catch(() => { if (!cancelled) setData(EMPTY); });
    return () => { cancelled = true; };
  }, [kbId, lang]);
  return data;
}
