import { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Brain, Trash2, Download } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useModalContext } from '../../contexts/ModalContext';

interface MemoryEntry {
    id: number;
    kind: 'semantic' | 'procedural' | 'episodic';
    content: string;
    kbId: string;
    salience: number;
    createdAt: string;
}

const MEMORY_URL = `${API_BASE_URL}/api/user/memory`;

export default function MemoryManager() {
    const { t, language } = useTheme();
    const { showConfirm } = useModalContext();
    const [entries, setEntries] = useState<MemoryEntry[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [busyId, setBusyId] = useState<number | null>(null);

    const fetchEntries = useCallback(async () => {
        try {
            const res = await axios.get(MEMORY_URL);
            setEntries(res.data ?? []);
            setError('');
        } catch {
            setError(t('memoryLoadError'));
        } finally {
            setLoading(false);
        }
    }, [t]);

    useEffect(() => { fetchEntries(); }, [fetchEntries]);

    const handleDelete = async (id: number) => {
        if (!(await showConfirm(t('memoryDeleteConfirm')))) return;
        setBusyId(id);
        try {
            await axios.delete(`${MEMORY_URL}/${id}`);
            await fetchEntries();
        } catch {
            setError(t('memoryDeleteError'));
        } finally {
            setBusyId(null);
        }
    };

    const handleClearAll = async () => {
        if (!(await showConfirm(t('memoryClearAllConfirm')))) return;
        try {
            await axios.delete(MEMORY_URL);
            await fetchEntries();
        } catch {
            setError(t('memoryDeleteError'));
        }
    };

    const handleExport = async () => {
        try {
            const res = await axios.get(`${MEMORY_URL}/export`, { responseType: 'blob' });
            const url = URL.createObjectURL(res.data);
            const a = document.createElement('a');
            a.href = url;
            a.download = 'my-memory.json';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
        } catch {
            setError(t('memoryDeleteError'));
        }
    };

    const kindLabel = (k: MemoryEntry['kind']) =>
        k === 'semantic' ? t('memoryKindSemantic')
        : k === 'procedural' ? t('memoryKindProcedural')
        : t('memoryKindEpisodic');

    const formatDate = (s: string) =>
        new Date(s).toLocaleDateString(language === 'de' ? 'de-DE' : 'en-US',
            { day: '2-digit', month: '2-digit', year: 'numeric' });

    return (
        <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem' }}>
                <div style={{ background: 'var(--accent-primary)', color: 'white', padding: '0.5rem', borderRadius: '8px', display: 'flex' }}>
                    <Brain size={20} />
                </div>
                <div style={{ flex: 1 }}>
                    <h3 style={{ margin: 0, color: 'var(--text-primary)' }}>{t('myMemory')}</h3>
                    <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('myMemoryDescription')}</p>
                </div>
                <button onClick={handleExport} disabled={entries.length === 0}
                    style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', padding: '0.5rem 0.9rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', cursor: entries.length === 0 ? 'not-allowed' : 'pointer', opacity: entries.length === 0 ? 0.6 : 1, fontSize: '0.85rem' }}>
                    <Download size={15} />{t('memoryExport')}
                </button>
                <button onClick={handleClearAll} disabled={entries.length === 0}
                    style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', padding: '0.5rem 0.9rem', borderRadius: '8px', border: '1px solid rgba(239,68,68,0.3)', background: 'rgba(239,68,68,0.08)', color: '#ef4444', cursor: entries.length === 0 ? 'not-allowed' : 'pointer', opacity: entries.length === 0 ? 0.6 : 1, fontSize: '0.85rem' }}>
                    <Trash2 size={15} />{t('memoryClearAll')}
                </button>
            </div>

            {error && (
                <div style={{ padding: '0.75rem 1rem', marginBottom: '1rem', borderRadius: '8px', background: 'rgba(239,68,68,0.1)', color: '#ef4444', border: '1px solid rgba(239,68,68,0.3)', fontSize: '0.9rem' }}>{error}</div>
            )}

            {loading ? (
                <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>{t('loading')}</div>
            ) : entries.length === 0 ? (
                <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('memoryNone')}</div>
            ) : (
                <div style={{ overflowX: 'auto' }}>
                    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.9rem' }}>
                        <thead>
                            <tr style={{ borderBottom: '2px solid var(--border-color)' }}>
                                {[t('memoryColumnKind'), t('memoryColumnContent'), t('memoryColumnScope'), t('memoryColumnCreated'), ''].map((h, i) => (
                                    <th key={i} style={{ textAlign: i === 4 ? 'right' : 'left', padding: '0.6rem 0.75rem', color: 'var(--text-secondary)', fontWeight: 500, fontSize: '0.85rem' }}>{h}</th>
                                ))}
                            </tr>
                        </thead>
                        <tbody>
                            {entries.map((m) => (
                                <tr key={m.id} style={{ borderBottom: '1px solid var(--border-color)' }}>
                                    <td style={{ padding: '0.6rem 0.75rem' }}>
                                        <span style={{ background: 'var(--tag-bg)', color: 'var(--tag-text)', padding: '0.15rem 0.5rem', borderRadius: '4px', fontSize: '0.8rem' }}>{kindLabel(m.kind)}</span>
                                    </td>
                                    <td style={{ padding: '0.6rem 0.75rem', color: 'var(--text-primary)' }}>{m.content}</td>
                                    <td style={{ padding: '0.6rem 0.75rem', color: 'var(--text-secondary)' }}>{m.kbId ? m.kbId : t('memoryScopeGlobal')}</td>
                                    <td style={{ padding: '0.6rem 0.75rem', color: 'var(--text-secondary)' }}>{formatDate(m.createdAt)}</td>
                                    <td style={{ padding: '0.6rem 0.75rem', textAlign: 'right' }}>
                                        <button onClick={() => handleDelete(m.id)} disabled={busyId === m.id} title={t('delete')}
                                            style={{ background: 'none', border: 'none', cursor: busyId === m.id ? 'not-allowed' : 'pointer', color: '#ef4444', padding: '4px', display: 'inline-flex', opacity: busyId === m.id ? 0.5 : 1 }}>
                                            <Trash2 size={16} />
                                        </button>
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                </div>
            )}
        </div>
    );
}
