import { useState, useEffect, useCallback } from 'react';
import { Search, Loader2, Globe, Star } from 'lucide-react';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import type { KbCatalogEntry, KbCategory } from '../types';

interface KbCatalogPanelProps {
    /** Called after every successful subscribe/unsubscribe toggle, so the
     * caller can refetch the Favoriten list — a subscription change changes
     * what that list shows. */
    onSubscriptionChange: () => void;
    /** Opens a KB known only by its id — the catalog rows carry nothing else. */
    onOpenKb: (id: string) => void;
}

const SEARCH_DEBOUNCE_MS = 250;

/** How many cards the grid shows before the "show more" button takes over. */
const INITIAL_VISIBLE = 8;

/**
 * The discovery surface: search, category tabs and a favorites toggle over
 * every public KB the caller may discover. It lives inline in the Home
 * overview's "KBs entdecken" accordion, which unmounts it while collapsed —
 * so both fetches below re-run each time the section is expanded, and a KB
 * published after the page loaded appears without a reload.
 *
 * It is also the counterpart of the star on a Favoriten card: that star only
 * ever writes an opt-out (no membership change, no chat deletion), so every
 * KB removed there lands back here — including a staged one the caller
 * curates, which GET /api/kb/catalog lists to its members for that reason.
 */
export default function KbCatalogPanel({ onSubscriptionChange, onOpenKb }: KbCatalogPanelProps) {
    const { t } = useTheme();
    const toast = useToast();
    const [entries, setEntries] = useState<KbCatalogEntry[]>([]);
    const [categories, setCategories] = useState<KbCategory[]>([]);
    const [query, setQuery] = useState('');
    const [activeCategory, setActiveCategory] = useState<string | null>(null);
    const [pending, setPending] = useState<Set<string>>(new Set());
    const [loading, setLoading] = useState(true);
    const [expanded, setExpanded] = useState(false);

    // GET /api/kb-categories is authentication-only (its /api/admin/ twin
    // serves the same list to the curation UI). It used to be fetched from the
    // admin route, which answered 403 for everybody else — so the filter tabs
    // were invisible to exactly the users the catalog exists for.
    useEffect(() => {
        axios.get(`${API_BASE_URL}/api/kb-categories`)
            .then(res => setCategories(Array.isArray(res.data) ? res.data : []))
            .catch(() => setCategories([]));
    }, []);

    useEffect(() => {
        const handle = setTimeout(() => {
            const params = new URLSearchParams();
            if (query.trim()) params.set('q', query.trim());
            if (activeCategory) params.append('category', activeCategory);
            axios.get(`${API_BASE_URL}/api/kb/catalog?${params}`)
                .then(res => setEntries(res.data))
                .catch(() => setEntries([]))
                .finally(() => setLoading(false));
        }, SEARCH_DEBOUNCE_MS);
        return () => clearTimeout(handle);
    }, [query, activeCategory]);

    // A new filter is a new result set, so the fold closes again — otherwise
    // switching tabs after expanding once would silently keep every later
    // result set unfolded. Done in the two setters rather than in an effect on
    // [query, activeCategory]: they are the only ways the filter changes, and
    // an effect here would be a cascading render (react-hooks/set-state-in-effect).
    const changeQuery = useCallback((next: string) => {
        setQuery(next);
        setExpanded(false);
    }, []);

    const changeCategory = useCallback((next: string | null) => {
        setActiveCategory(next);
        setExpanded(false);
    }, []);

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

    // The catalog can hold every public KB in the deployment; showing all of
    // them at once buries the four sections below it. The fold is client-side
    // on purpose — the request is already capped at 200 rows, and paginating
    // the fetch would make the count in the button a guess.
    const visible = expanded ? entries : entries.slice(0, INITIAL_VISIBLE);
    const hidden = expanded ? 0 : entries.length - visible.length;

    return (
        <div className="home-view__catalog">
            <div className="input-group" style={{ marginBottom: '1rem' }}>
                <div style={{ position: 'relative' }}>
                    <Search
                        size={16}
                        aria-hidden="true"
                        style={{ position: 'absolute', insetInlineStart: '0.75rem', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-secondary)' }}
                    />
                    <input
                        value={query}
                        onChange={e => changeQuery(e.target.value)}
                        placeholder={t('catalogSearchPlaceholder')}
                        aria-label={t('catalogSearchPlaceholder')}
                        style={{ width: '100%', padding: '0.75rem 0.75rem 0.75rem 2.25rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                    />
                </div>
            </div>

            {/* Tabs, not chips: the filter is single-select and always has
                exactly one active value, which is what a tab strip states and
                a row of toggle chips does not. "Alle" is the leftmost tab
                rather than a way to clear the others. */}
            {categories.length > 0 && (
                <div className="kb-catalog__tabs" role="tablist" aria-label={t('catalogCategoryTabs')}>
                    <button
                        type="button"
                        role="tab"
                        onClick={() => changeCategory(null)}
                        aria-selected={activeCategory === null}
                        className={`kb-catalog__tab${activeCategory === null ? ' kb-catalog__tab--active' : ''}`}
                    >
                        {t('catalogAllCategories')}
                    </button>
                    {categories.map(c => (
                        <button
                            key={c.id}
                            type="button"
                            role="tab"
                            onClick={() => changeCategory(c.id)}
                            aria-selected={activeCategory === c.id}
                            className={`kb-catalog__tab${activeCategory === c.id ? ' kb-catalog__tab--active' : ''}`}
                        >
                            {c.name}
                        </button>
                    ))}
                </div>
            )}

            {loading ? (
                <p style={{ color: 'var(--text-secondary)', fontSize: '0.9rem', textAlign: 'start' }}>{t('loading')}</p>
            ) : entries.length === 0 ? (
                <p style={{ color: 'var(--text-secondary)', fontSize: '0.9rem', textAlign: 'start' }}>{t('catalogEmpty')}</p>
            ) : (
                <>
                <ul className="home-view__grid">
                    {visible.map(entry => (
                        <li
                            key={entry.id}
                            data-testid="catalog-entry"
                            className="source-card home-view__kb-card"
                            role="button" // eslint-disable-line jsx-a11y/no-noninteractive-element-to-interactive-role
                            tabIndex={0}
                            aria-label={`${t('openKb')}: ${entry.name}`}
                            onClick={() => onOpenKb(entry.id)}
                            onKeyDown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    onOpenKb(entry.id);
                                }
                            }}
                        >
                            <div className="home-view__card-top">
                                <Globe size={20} color="var(--accent-primary)" aria-hidden="true" />
                                <div className="home-view__badge-row">
                                    {/* Mirror image of the filled star on a Favoriten card:
                                        outline means "not in my favorites yet, click to add",
                                        filled means "already there, click to drop it". The
                                        card itself opens the KB, so this must not bubble. */}
                                    <button
                                        onClick={(e) => { e.stopPropagation(); void toggle(entry); }}
                                        disabled={pending.has(entry.id)}
                                        className="home-view__mini-icon"
                                        aria-pressed={entry.subscribed}
                                        title={entry.subscribed ? t('unsubscribe') : t('subscribe')}
                                        aria-label={entry.subscribed ? t('unsubscribe') : t('subscribe')}
                                    >
                                        {pending.has(entry.id)
                                            ? <Loader2 className="animate-spin" size={16} aria-hidden="true" />
                                            : <Star size={16} aria-hidden="true" fill={entry.subscribed ? 'currentColor' : 'none'} />}
                                    </button>
                                </div>
                            </div>

                            <div className="source-title home-view__kb-name">
                                <button
                                    type="button"
                                    className="text-button home-view__kb-name-btn"
                                    onClick={(e) => { e.stopPropagation(); onOpenKb(entry.id); }}
                                >
                                    {entry.name}
                                </button>
                            </div>

                            {entry.description && (
                                <div className="home-view__kb-header-text">{entry.description}</div>
                            )}
                        </li>
                    ))}
                </ul>
                {hidden > 0 && (
                    <div className="kb-catalog__more">
                        <button type="button" className="secondary-button" onClick={() => setExpanded(true)}>
                            {t('catalogShowMore').replace('{n}', String(hidden))}
                        </button>
                    </div>
                )}
                {expanded && entries.length > INITIAL_VISIBLE && (
                    <div className="kb-catalog__more">
                        <button type="button" className="secondary-button" onClick={() => setExpanded(false)}>
                            {t('catalogShowLess')}
                        </button>
                    </div>
                )}
                </>
            )}
        </div>
    );
}
