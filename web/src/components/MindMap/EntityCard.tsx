import { useEffect, useState } from 'react';
import axios from 'axios';
import { API_BASE_URL } from '../../api';

interface Source { fileId: string; fileName: string; chunkId: string; }
interface Neighbor { id: number; name: string; type: string; rel: string; }
interface Detail { id: number; name: string; type: string; aliases: string[]; degree: number; sources: Source[]; neighbors: Neighbor[]; }

interface Props {
    kbId: string;
    entityId: string;
    entityName: string;
    onAsk: (name: string) => void;
    onOpenSource: (fileId: string, fileName: string) => void;
    onClose: () => void;
    t: (k: string) => string;
}

export const EntityCard: React.FC<Props> = ({ kbId, entityId, entityName, onAsk, onOpenSource, onClose, t }) => {
    const [detail, setDetail] = useState<Detail | null>(null);
    const [error, setError] = useState(false);

    useEffect(() => {
        let cancelled = false;
        setDetail(null); setError(false);
        axios.get(`${API_BASE_URL}/api/kb/${kbId}/graph/entity/${encodeURIComponent(entityId)}`)
            .then(res => { if (!cancelled) setDetail(res.data as Detail); })
            .catch(() => { if (!cancelled) setError(true); });
        return () => { cancelled = true; };
    }, [kbId, entityId]);

    // Dedup sources by file for display.
    const byFile = new Map<string, Source>();
    for (const s of detail?.sources ?? []) if (s.fileId && !byFile.has(s.fileId)) byFile.set(s.fileId, s);

    return (
        <aside className="node-sources-panel entity-card" aria-label={`${t('sourcesFor')} ${entityName}`}>
            <header style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '8px' }}>
                <strong>{entityName}</strong>
                <button type="button" onClick={onClose} aria-label="close">×</button>
            </header>
            {detail && <div className="entity-card-type">{detail.type} · {detail.degree} {t('entityConnections')}</div>}
            {error && <p>{t('entityDetailError')}</p>}
            {detail && detail.neighbors.length > 0 && (
                <div className="entity-card-neighbors">
                    {t('entityNeighbors')}: {detail.neighbors.map(n => n.name).join(', ')}
                </div>
            )}
            {byFile.size === 0
                ? (detail && <p>{t('noSourcesForNode')}</p>)
                : (
                    <ul>
                        {[...byFile.values()].map(s => (
                            <li key={s.fileId}>
                                <button type="button" className="entity-card-src" onClick={() => onOpenSource(s.fileId, s.fileName)}>
                                    {s.fileName || s.fileId}
                                </button>
                            </li>
                        ))}
                    </ul>
                )}
            <button type="button" onClick={() => onAsk(entityName)}>{t('askAbout')} {entityName}</button>
        </aside>
    );
};
