import { useEffect, useState } from 'react';
import axios from 'axios';
import { Plus, Trash2, Globe } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import { useModalContext } from '../../contexts/ModalContext';
import { useAuth } from '../../contexts/AuthContext';
import { getApiErrorMessage } from '../../utils/apiError';
import type { KbCategory } from '../../types';
import { AdminUnpublishDialog } from './AdminUnpublishDialog';
import AdminCategoriesSection from './AdminCategoriesSection';

interface GlobalKbRow {
    id: string;
    name: string;
    createdAt: string;
    headerText?: string;
    autoSubscribe: boolean;
}

interface AdminGlobalKbsTabProps {
    onEditGlobalKb?: (kb: { id: string; name: string }) => void;
}

/**
 * Admin surface for KBs that are visibility='public' — i.e. already
 * published. Publishing a private KB is deliberately not offered here: that
 * action targets a private KB, which by definition never appears in this
 * list. That entry point belongs on the KB overview instead.
 */
export default function AdminGlobalKbsTab({ onEditGlobalKb }: AdminGlobalKbsTabProps) {
    const { t } = useTheme();
    const toast = useToast();
    const { showPrompt, showConfirm } = useModalContext();
    const { user } = useAuth();
    const [globalKbs, setGlobalKbs] = useState<GlobalKbRow[]>([]);
    const [loading, setLoading] = useState(true);
    const [categories, setCategories] = useState<KbCategory[]>([]);
    const [kbCategoryIds, setKbCategoryIds] = useState<Record<string, string[]>>({});

    const [unpublishTarget, setUnpublishTarget] = useState<GlobalKbRow | null>(null);
    const [unpublishBusy, setUnpublishBusy] = useState(false);
    const [unpublishError, setUnpublishError] = useState<string | null>(null);

    useEffect(() => {
        let cancelled = false;
        (async () => {
            try {
                const res = await axios.get(`${API_BASE_URL}/api/admin/global-kbs`);
                const rows: GlobalKbRow[] = res.data;
                if (cancelled) return;
                setGlobalKbs(rows);
                // Pre-populate each row's current assignment so the multi-select
                // below doesn't start empty and wipe existing categories on the
                // first unrelated PUT.
                const entries = await Promise.all(rows.map(async (kb) => {
                    try {
                        const catRes = await axios.get(`${API_BASE_URL}/api/kb/${kb.id}/categories`);
                        return [kb.id, (catRes.data as KbCategory[]).map(c => c.id)] as const;
                    } catch {
                        return [kb.id, []] as const;
                    }
                }));
                if (!cancelled) setKbCategoryIds(Object.fromEntries(entries));
            } catch (err: unknown) {
                console.error(err);
            } finally {
                if (!cancelled) setLoading(false);
            }
        })();
        return () => { cancelled = true; };
    }, []);

    useEffect(() => {
        axios.get(`${API_BASE_URL}/api/admin/kb-categories`)
            .then(res => setCategories(res.data))
            .catch(() => setCategories([]));
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

    const handleToggleAutoSubscribe = async (kb: GlobalKbRow) => {
        const next = !kb.autoSubscribe;
        setGlobalKbs(prev => prev.map(k => (k.id === kb.id ? { ...k, autoSubscribe: next } : k)));
        try {
            await axios.patch(`${API_BASE_URL}/api/admin/global-kbs/${kb.id}`, { autoSubscribe: next });
        } catch (err: unknown) {
            console.error(err);
            toast.error(t('autoSubscribeError'));
            setGlobalKbs(prev => prev.map(k => (k.id === kb.id ? { ...k, autoSubscribe: kb.autoSubscribe } : k)));
        }
    };

    const handleCategoryChange = async (kbId: string, categoryIds: string[]) => {
        const previous = kbCategoryIds[kbId] ?? [];
        setKbCategoryIds(prev => ({ ...prev, [kbId]: categoryIds }));
        try {
            await axios.put(`${API_BASE_URL}/api/kb/${kbId}/categories`, { categoryIds });
        } catch (err: unknown) {
            console.error(err);
            toast.error(t('categoriesAssignError'));
            setKbCategoryIds(prev => ({ ...prev, [kbId]: previous }));
        }
    };

    const handleUnpublishConfirm = async (newOwnerId: string) => {
        if (!unpublishTarget) return;
        setUnpublishBusy(true);
        setUnpublishError(null);
        try {
            await axios.post(`${API_BASE_URL}/api/admin/kb/${unpublishTarget.id}/unpublish`, { newOwnerId });
            // The KB is no longer public — it drops out of this list.
            setGlobalKbs(prev => prev.filter(k => k.id !== unpublishTarget.id));
            setUnpublishTarget(null);
        } catch (err: unknown) {
            console.error(err);
            setUnpublishError(getApiErrorMessage(err, t('unpublishError')));
        } finally {
            setUnpublishBusy(false);
        }
    };

    return (
        <section className="admin-content">
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '2rem', alignItems: 'center' }}>
                <h2 style={{ color: 'var(--text-primary)', margin: 0 }}>{t('publicKnowledgeBases')}</h2>
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
                            display: 'flex', flexDirection: 'column', gap: '0.75rem',
                            padding: '1rem 1.25rem', borderRadius: '10px',
                            background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                        }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                <div>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                        <Globe size={16} color="var(--accent-primary)" />
                                        <span style={{ fontWeight: 600, fontSize: '0.95rem' }}>{kb.name}</span>
                                    </div>
                                    <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '4px' }}>
                                        {new Date(kb.createdAt).toLocaleDateString()}
                                        {kb.headerText && ` — ${kb.headerText.substring(0, 60)}${kb.headerText.length > 60 ? '...' : ''}`}
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
                                        onClick={() => { setUnpublishError(null); setUnpublishTarget(kb); }}
                                        style={{ padding: '0.4rem 0.8rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', color: 'var(--text-primary)', cursor: 'pointer', fontSize: '0.8rem' }}
                                    >
                                        {t('unpublishKb')}
                                    </button>
                                    <button
                                        onClick={() => handleDelete(kb.id)}
                                        style={{ padding: '0.4rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', cursor: 'pointer', color: 'var(--text-secondary)' }}
                                    >
                                        <Trash2 size={16} />
                                    </button>
                                </div>
                            </div>

                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
                                <button
                                    type="button"
                                    role="switch"
                                    aria-checked={kb.autoSubscribe}
                                    aria-label={t('autoSubscribe')}
                                    onClick={() => handleToggleAutoSubscribe(kb)}
                                    style={{
                                        position: 'relative', width: '38px', height: '20px', borderRadius: '999px',
                                        border: '1px solid var(--border-color)', cursor: 'pointer', padding: 0,
                                        background: kb.autoSubscribe ? 'var(--accent-primary)' : 'var(--bg-secondary)',
                                        flexShrink: 0,
                                    }}
                                >
                                    <span aria-hidden="true" style={{
                                        position: 'absolute', top: '1px', left: kb.autoSubscribe ? '19px' : '1px',
                                        width: '16px', height: '16px', borderRadius: '50%', background: 'white',
                                        transition: 'left 0.15s ease',
                                    }} />
                                </button>
                                <div>
                                    <div style={{ fontSize: '0.85rem', color: 'var(--text-primary)' }}>{t('autoSubscribe')}</div>
                                    <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>{t('autoSubscribeHint')}</div>
                                </div>
                            </div>

                            {categories.length > 0 && (
                                <div>
                                    <label htmlFor={`kb-categories-${kb.id}`} style={{ display: 'block', fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '0.3rem' }}>
                                        {t('categories')}
                                    </label>
                                    <select
                                        id={`kb-categories-${kb.id}`}
                                        multiple
                                        aria-label={t('categories')}
                                        value={kbCategoryIds[kb.id] ?? []}
                                        onChange={(e) => handleCategoryChange(kb.id, Array.from(e.target.selectedOptions).map(o => o.value))}
                                        style={{
                                            width: '100%', maxWidth: '320px', background: 'var(--bg-secondary)',
                                            border: '1px solid var(--border-color)', color: 'var(--text-primary)',
                                            borderRadius: 'var(--shape-md)', fontSize: '0.85rem', minHeight: '2.2rem',
                                        }}
                                    >
                                        {categories.map(cat => (
                                            <option key={cat.id} value={cat.id}>{cat.name}</option>
                                        ))}
                                    </select>
                                </div>
                            )}
                        </div>
                    ))}
                </div>
            )}

            {unpublishTarget && (
                <AdminUnpublishDialog
                    kbId={unpublishTarget.id}
                    kbName={unpublishTarget.name}
                    currentUserId={user.id}
                    busy={unpublishBusy}
                    error={unpublishError}
                    onCancel={() => { if (!unpublishBusy) setUnpublishTarget(null); }}
                    onConfirm={handleUnpublishConfirm}
                />
            )}

            <AdminCategoriesSection />
        </section>
    );
}
