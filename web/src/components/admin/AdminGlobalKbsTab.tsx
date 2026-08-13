import { useEffect, useState } from 'react';
import axios from 'axios';
import { Plus, Trash2, Globe, X } from 'lucide-react';
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

interface KbCategoryChipsProps {
    kbId: string;
    categories: KbCategory[];
    assigned: string[];
    loadFailed: boolean;
    onChange: (kbId: string, categoryIds: string[]) => void;
    t: (k: string) => string;
}

/**
 * Category assignment for one public KB, as toggle chips.
 *
 * This replaced a native `<select multiple>`, which read as a plain list: the
 * assigned entries were only distinguishable by the browser's selection
 * highlight, and *removing* one meant knowing to ctrl-click it — undiscoverable
 * enough that assignments were effectively write-once. Chips state each
 * category's membership on the chip itself and remove on a plain click.
 */
function KbCategoryChips({ kbId, categories, assigned, loadFailed, onChange, t }: KbCategoryChipsProps) {
    const toggle = (categoryId: string) => {
        const next = assigned.includes(categoryId)
            ? assigned.filter(id => id !== categoryId)
            : [...assigned, categoryId];
        onChange(kbId, next);
    };

    return (
        <div>
            <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '0.15rem' }}>
                {t('categories')}
            </div>
            <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', margin: '0 0 0.5rem' }}>
                {t('categoriesAssignHint')}
            </p>
            <div
                role="group"
                aria-label={t('categories')}
                style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4rem', opacity: loadFailed ? 0.6 : 1 }}
            >
                {categories.map(cat => {
                    const isAssigned = assigned.includes(cat.id);
                    return (
                        <button
                            key={cat.id}
                            type="button"
                            aria-pressed={isAssigned}
                            // The label spells out the state as well as the name, so a
                            // screen-reader user does not depend on the ✓ / × glyph.
                            aria-label={t(isAssigned ? 'categoryAssigned' : 'categoryNotAssigned').replace('{name}', cat.name)}
                            disabled={loadFailed}
                            onClick={() => toggle(cat.id)}
                            style={{
                                display: 'inline-flex', alignItems: 'center', gap: '0.3rem',
                                padding: '0.25rem 0.6rem', borderRadius: '999px',
                                fontSize: '0.8rem', cursor: loadFailed ? 'not-allowed' : 'pointer',
                                border: `1px solid ${isAssigned ? 'var(--accent-primary)' : 'var(--border-color)'}`,
                                background: isAssigned ? 'var(--accent-primary)' : 'var(--bg-secondary)',
                                color: isAssigned ? 'white' : 'var(--text-secondary)',
                            }}
                        >
                            {isAssigned ? <X size={12} aria-hidden="true" /> : <Plus size={12} aria-hidden="true" />}
                            {cat.name}
                        </button>
                    );
                })}
            </div>
            {assigned.length === 0 && !loadFailed && (
                <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', margin: '0.4rem 0 0' }}>
                    {t('categoriesNoneAssigned')}
                </p>
            )}
            {loadFailed && (
                <p style={{ color: 'var(--error-text)', fontSize: '0.75rem', margin: '0.4rem 0 0' }}>
                    {t('categoriesLoadError')}
                </p>
            )}
        </div>
    );
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
    // Tracks, per row, whether the GET /api/kb/{id}/categories prefetch below
    // failed — an empty array from a failed fetch must never be
    // indistinguishable from "this KB genuinely has no categories", because
    // the former rendered as if true would let a subsequent PUT silently
    // wipe the KB's real assignments.
    const [kbCategoryLoadFailed, setKbCategoryLoadFailed] = useState<Record<string, boolean>>({});

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
                // first unrelated PUT. A failed fetch must not masquerade as "no
                // categories" — track it separately (kbCategoryLoadFailed) so the
                // row's control can be disabled instead of rendered as if the
                // empty result were true.
                const entries = await Promise.all(rows.map(async (kb) => {
                    try {
                        const catRes = await axios.get(`${API_BASE_URL}/api/kb/${kb.id}/categories`);
                        return { id: kb.id, categoryIds: (catRes.data as KbCategory[]).map(c => c.id), failed: false };
                    } catch (err: unknown) {
                        console.error(err);
                        return { id: kb.id, categoryIds: [] as string[], failed: true };
                    }
                }));
                if (!cancelled) {
                    setKbCategoryIds(Object.fromEntries(entries.map(e => [e.id, e.categoryIds])));
                    setKbCategoryLoadFailed(Object.fromEntries(entries.map(e => [e.id, e.failed])));
                }
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
        // Defense in depth alongside the disabled select below: a row whose
        // current assignments failed to load must never be saved over, even
        // if a change event reaches this handler some other way.
        if (kbCategoryLoadFailed[kbId]) return;
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
                                <KbCategoryChips
                                    kbId={kb.id}
                                    categories={categories}
                                    assigned={kbCategoryIds[kb.id] ?? []}
                                    loadFailed={!!kbCategoryLoadFailed[kb.id]}
                                    onChange={handleCategoryChange}
                                    t={t}
                                />
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
