import React from 'react';
import { useTheme } from '../../contexts/ThemeContext';

export interface NodeSource { fileId: string; fileName: string; chunkId: string; }

interface Props {
  entityName: string;
  sources: NodeSource[];
  onAsk: () => void;
  onClose: () => void;
}

export const NodeSourcesPanel: React.FC<Props> = ({ entityName, sources, onAsk, onClose }) => {
  const { t } = useTheme();
  // Dedup by file for display (one row per file even if multiple chunks).
  const byFile = new Map<string, NodeSource>();
  for (const s of sources) if (s.fileId && !byFile.has(s.fileId)) byFile.set(s.fileId, s);
  return (
    <aside className="node-sources-panel" aria-label={`${t('sourcesFor')} ${entityName}`}>
      <header style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '8px' }}>
        <strong>{t('sourcesFor')} "{entityName}"</strong>
        <button type="button" onClick={onClose} aria-label={t('close')}>×</button>
      </header>
      {byFile.size === 0 ? (
        <p>{t('noSourcesForNode')}</p>
      ) : (
        <ul>
          {[...byFile.values()].map(s => (
            <li key={s.fileId}>{s.fileName || s.fileId}</li>
          ))}
        </ul>
      )}
      <button type="button" onClick={onAsk}>{t('askAbout')} {entityName}</button>
    </aside>
  );
};
