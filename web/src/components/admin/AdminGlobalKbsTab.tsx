import { useState, useEffect } from 'react';
import axios from 'axios';
import { Plus, Trash2, Globe } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import { useModalContext } from '../../contexts/ModalContext';

interface AdminGlobalKbsTabProps {
    onEditGlobalKb?: (kb: { id: string; name: string }) => void;
}

export default function AdminGlobalKbsTab({ onEditGlobalKb }: AdminGlobalKbsTabProps) {
    const { t } = useTheme();
    const toast = useToast();
    const { showConfirm, showPrompt } = useModalContext();
    const [globalKbs, setGlobalKbs] = useState<{ id: string; name: string; createdAt: string; headerText?: string }[]>([]);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        const fetch = async () => {
            try {
                const res = await axios.get(`${API_BASE_URL}/api/admin/global-kbs`);
                setGlobalKbs(res.data);
            } catch (err: unknown) { console.error(err); }
            finally { setLoading(false); }
        };
        fetch();
    }, []);

    const handleCreate = async () => {
        const name = await showPrompt(t('globalKbNamePrompt'));
        if (!name) return;
        try {
            const res = await axios.post(`${API_BASE_URL}/api/admin/global-kbs`, { name });
            setGlobalKbs(prev => [res.data, ...prev]);
        } catch (err: unknown) {
            console.error(err);
            toast.error(t('globalKbCreateError'));
        }
    };

    const handleDelete = async (id: string) => {
        if (!await showConfirm(t('confirmDeleteGlobalKb'))) return;
        try {
            await axios.delete(`${API_BASE_URL}/api/admin/global-kbs/${id}`);
            setGlobalKbs(prev => prev.filter(k => k.id !== id));
        } catch (err: unknown) {
            console.error(err);
            toast.error(t('globalKbDeleteError'));
        }
    };

    return (
        <section className="admin-content">
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '2rem', alignItems: 'center' }}>
                <h2 style={{ color: 'var(--text-primary)', margin: 0 }}>{t('globalKnowledgeBases')}</h2>
                <button onClick={handleCreate} style={{ background: 'var(--accent-primary)', color: 'white', border: 'none', padding: '0.6rem 1.2rem', borderRadius: '8px', display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer' }}>
                    <Plus size={18} /> {t('create')}
                </button>
            </div>
            {loading ? (
                <p style={{ color: 'var(--text-secondary)' }}>{t('loading')}</p>
            ) : globalKbs.length === 0 ? (
                <p style={{ color: 'var(--text-secondary)', textAlign: 'center', padding: '2rem' }}>{t('noGlobalKbs')}</p>
            ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
                    {globalKbs.map(kb => (
                        <div key={kb.id} style={{
                            display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                            padding: '1rem 1.25rem', borderRadius: '10px',
                            background: 'var(--bg-primary)', border: '1px solid var(--border-color)'
                        }}>
                            <div>
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <Globe size={16} color="var(--accent-primary)" />
                                    <span style={{ fontWeight: 600, fontSize: '0.95rem' }}>{kb.name}</span>
                                </div>
                                <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '4px' }}>
                                    {new Date(kb.createdAt).toLocaleDateString()}
                                    {kb.headerText && ` \u2014 ${kb.headerText.substring(0, 60)}${kb.headerText.length > 60 ? '...' : ''}`}
                                </div>
                            </div>
                            <div style={{ display: 'flex', gap: '0.5rem' }}>
                                <button
                                    onClick={() => onEditGlobalKb?.(kb)}
                                    style={{ padding: '0.4rem 0.8rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', color: 'var(--text-primary)', cursor: 'pointer', fontSize: '0.8rem' }}
                                >
                                    {t('edit')}
                                </button>
                                <button
                                    onClick={() => handleDelete(kb.id)}
                                    style={{ padding: '0.4rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', cursor: 'pointer', color: 'var(--text-secondary)' }}
                                >
                                    <Trash2 size={16} />
                                </button>
                            </div>
                        </div>
                    ))}
                </div>
            )}
        </section>
    );
}
