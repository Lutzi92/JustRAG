import { useEffect, useState } from 'react';
import axios from 'axios';
import { Plus, Pencil, Trash2 } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import { useModalContext } from '../../contexts/ModalContext';
import type { KbCategory } from '../../types';

/**
 * CRUD section for the shared category taxonomy (GET/POST /api/admin/kb-categories,
 * PATCH/DELETE /api/admin/kb-categories/{catId}) — the flat, system-admin curated
 * list the catalog's filter chips and the public-KB category assignment draw from.
 * Self-contained: owns its own fetch, independent of the KB list above it.
 */
export default function AdminCategoriesSection() {
    const { t } = useTheme();
    const toast = useToast();
    const { showPrompt, showConfirm } = useModalContext();
    const [categories, setCategories] = useState<KbCategory[]>([]);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        axios.get(`${API_BASE_URL}/api/admin/kb-categories`)
            .then(res => setCategories(res.data))
            .catch((err: unknown) => console.error(err))
            .finally(() => setLoading(false));
    }, []);

    const handleCreate = async () => {
        const name = await showPrompt(t('categoryNamePrompt'));
        if (!name) return;
        try {
            const res = await axios.post(`${API_BASE_URL}/api/admin/kb-categories`, { name, sortOrder: categories.length });
            setCategories(prev => [...prev, res.data]);
        } catch (err: unknown) {
            console.error(err);
            toast.error(t('categorySaveError'));
        }
    };

    const handleRename = async (cat: KbCategory) => {
        const name = await showPrompt(t('categoryNamePrompt'), cat.name);
        if (!name || name === cat.name) return;
        try {
            const res = await axios.patch(`${API_BASE_URL}/api/admin/kb-categories/${cat.id}`, { name, sortOrder: cat.sortOrder });
            setCategories(prev => prev.map(c => (c.id === cat.id ? res.data : c)));
        } catch (err: unknown) {
            console.error(err);
            toast.error(t('categorySaveError'));
        }
    };

    const handleDelete = async (cat: KbCategory) => {
        if (!await showConfirm(t('categoryDeleteConfirm'))) return;
        try {
            await axios.delete(`${API_BASE_URL}/api/admin/kb-categories/${cat.id}`);
            setCategories(prev => prev.filter(c => c.id !== cat.id));
        } catch (err: unknown) {
            console.error(err);
            toast.error(t('categoryDeleteError'));
        }
    };

    return (
        <section className="admin-content" style={{ marginTop: '2rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '1rem', alignItems: 'center' }}>
                <h3 style={{ color: 'var(--text-primary)', margin: 0 }}>{t('categories')}</h3>
                <button onClick={handleCreate} style={{ background: 'var(--accent-primary)', color: 'white', border: 'none', padding: '0.5rem 1rem', borderRadius: '8px', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
                    <Plus size={16} /> {t('create')}
                </button>
            </div>
            {loading ? (
                <p style={{ color: 'var(--text-secondary)' }}>{t('loading')}</p>
            ) : categories.length === 0 ? (
                <p style={{ color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('noCategories')}</p>
            ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                    {categories.map(cat => (
                        <div key={cat.id} style={{
                            display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                            padding: '0.6rem 1rem', borderRadius: '8px',
                            background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                        }}>
                            <span style={{ fontSize: '0.9rem' }}>{cat.name}</span>
                            <div style={{ display: 'flex', gap: '0.4rem' }}>
                                <button
                                    onClick={() => handleRename(cat)}
                                    aria-label={`${t('edit')}: ${cat.name}`}
                                    style={{ padding: '0.35rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', cursor: 'pointer', color: 'var(--text-secondary)' }}
                                >
                                    <Pencil size={14} />
                                </button>
                                <button
                                    onClick={() => handleDelete(cat)}
                                    aria-label={`${t('delete')}: ${cat.name}`}
                                    style={{ padding: '0.35rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', cursor: 'pointer', color: 'var(--text-secondary)' }}
                                >
                                    <Trash2 size={14} />
                                </button>
                            </div>
                        </div>
                    ))}
                </div>
            )}
        </section>
    );
}
