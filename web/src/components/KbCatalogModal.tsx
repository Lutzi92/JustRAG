import { useState, useEffect, useCallback } from 'react';
import { X, Search, Loader2, CheckCircle2 } from 'lucide-react';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import type { KbCatalogEntry, KbCategory } from '../types';

interface KbCatalogModalProps {
    isOpen: boolean;
    onClose: () => void;
    /** Called after every successful subscribe/unsubscribe toggle, so the
     * caller can refetch the Home global-KB list — a subscription change
     * changes what that list shows. */
    onSubscriptionChange: () => void;
}

const SEARCH_DEBOUNCE_MS = 250;

export default function KbCatalogModal({ isOpen, onClose, onSubscriptionChange }: KbCatalogModalProps) {
    const { t } = useTheme();
    const toast = useToast();
    const [entries, setEntries] = useState<KbCatalogEntry[]>([]);
    const [categories, setCategories] = useState<KbCategory[]>([]);
    const [query, setQuery] = useState('');
    const [activeCategory, setActiveCategory] = useState<string | null>(null);
    const [pending, setPending] = useState<Set<string>>(new Set());

    // The category list lives behind the admin endpoint. For a normal user it
    // answers 403 — that's not an error, just "no filter chips". Search still
    // works independently, and a dedicated public categories endpoint would
    // be a route for pure comfort (the catalog rows already carry categoryIds).
    useEffect(() => {
        if (!isOpen) return;
        axios.get(`${API_BASE_URL}/api/admin/kb-categories`)
            .then(res => setCategories(res.data))
            .catch(() => setCategories([]));
    }, [isOpen]);

    useEffect(() => {
        if (!isOpen) return;
        const handle = setTimeout(() => {
            const params = new URLSearchParams();
            if (query.trim()) params.set('q', query.trim());
            if (activeCategory) params.append('category', activeCategory);
            axios.get(`${API_BASE_URL}/api/kb/catalog?${params}`)
                .then(res => setEntries(res.data))
                .catch(() => setEntries([]));
        }, SEARCH_DEBOUNCE_MS);
        return () => clearTimeout(handle);
    }, [isOpen, query, activeCategory]);

    const toggle = useCallback(async (entry: KbCatalogEntry) => {
        const next = !entry.subscribed;
        setPending(prev => new Set(prev).add(entry.id));
        // Optimistic flip so the switch doesn't wait on the round-trip; rolled
        // back below on failure.
        setEntries(prev => prev.map(e => (e.id === entry.id ? { ...e, subscribed: next } : e)));
        try {
            const url = `${API_BASE_URL}/api/kb/${entry.id}/subscription`;
            if (next) {
                await axios.put(url);
            } else {
                await axios.delete(url);
            }
            onSubscriptionChange();
        } catch {
            setEntries(prev => prev.map(e => (e.id === entry.id ? { ...e, subscribed: !next } : e)));
            toast.error(t('subscriptionError'));
        } finally {
            setPending(prev => {
                const copy = new Set(prev);
                copy.delete(entry.id);
                return copy;
            });
        }
    }, [onSubscriptionChange, toast, t]);

    if (!isOpen) return null;

    return (
        <div className="modal-overlay" role="presentation" onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
            <div className="modal-content" role="dialog" aria-modal="true" aria-labelledby="kb-catalog-modal-title" style={{ maxWidth: '560px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                    <h3 id="kb-catalog-modal-title" style={{ margin: 0 }}>{t('catalogTitle')}</h3>
                    <button onClick={onClose} className="icon-button" aria-label={t('close')}><X size={20} /></button>
                </div>

                <div className="input-group" style={{ marginBottom: '1rem' }}>
                    <div style={{ position: 'relative' }}>
                        <Search
                            size={16}
                            aria-hidden="true"
                            style={{ position: 'absolute', left: '0.75rem', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-secondary)' }}
                        />
                        <input
                            value={query}
                            onChange={e => setQuery(e.target.value)}
                            placeholder={t('catalogSearchPlaceholder')}
                            aria-label={t('catalogSearchPlaceholder')}
                            style={{ width: '100%', padding: '0.75rem 0.75rem 0.75rem 2.25rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                        />
                    </div>
                </div>

                {categories.length > 0 && (
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', marginBottom: '1.25rem' }} role="group" aria-label={t('catalogAllCategories')}>
                        <button
                            onClick={() => setActiveCategory(null)}
                            aria-pressed={activeCategory === null}
                            className={activeCategory === null ? 'search-button' : 'secondary-button'}
                            style={{ padding: '0.35rem 0.75rem', fontSize: '0.8rem' }}
                        >
                            {t('catalogAllCategories')}
                        </button>
                        {categories.map(c => (
                            <button
                                key={c.id}
                                onClick={() => setActiveCategory(c.id)}
                                aria-pressed={activeCategory === c.id}
                                className={activeCategory === c.id ? 'search-button' : 'secondary-button'}
                                style={{ padding: '0.35rem 0.75rem', fontSize: '0.8rem' }}
                            >
                                {c.name}
                            </button>
                        ))}
                    </div>
                )}

                {entries.length === 0 ? (
                    <p style={{ color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('catalogEmpty')}</p>
                ) : (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem', maxHeight: '360px', overflowY: 'auto' }}>
                        {entries.map(entry => (
                            <div
                                key={entry.id}
                                data-testid="catalog-entry"
                                style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'space-between',
                                    gap: '1rem',
                                    padding: '0.75rem',
                                    background: 'var(--bg-secondary)',
                                    borderRadius: '8px',
                                    border: '1px solid var(--border-color)',
                                }}
                            >
                                <div style={{ minWidth: 0 }}>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                                        <span style={{ fontWeight: 500, fontSize: '0.9rem', color: 'var(--text-primary)' }}>{entry.name}</span>
                                        {entry.subscribed && (
                                            <span
                                                style={{ display: 'inline-flex', alignItems: 'center', gap: '0.25rem', fontSize: '0.7rem', color: 'var(--accent-primary)', fontWeight: 600 }}
                                            >
                                                <CheckCircle2 size={12} aria-hidden="true" />
                                                {t('subscribedBadge')}
                                            </span>
                                        )}
                                    </div>
                                    {entry.description && (
                                        <p style={{ margin: '0.25rem 0 0 0', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{entry.description}</p>
                                    )}
                                </div>
                                <button
                                    onClick={() => toggle(entry)}
                                    disabled={pending.has(entry.id)}
                                    className={entry.subscribed ? 'secondary-button' : 'search-button'}
                                    style={{ flexShrink: 0, padding: '0.5rem 1rem', display: 'flex', alignItems: 'center', gap: '0.4rem' }}
                                >
                                    {pending.has(entry.id) && <Loader2 className="animate-spin" size={14} aria-hidden="true" />}
                                    {entry.subscribed ? t('unsubscribe') : t('subscribe')}
                                </button>
                            </div>
                        ))}
                    </div>
                )}
            </div>
        </div>
    );
}
