import React, { memo, useState, useEffect, useCallback } from 'react';
import { Plus, Loader2, ChevronDown, ChevronRight, Info, FileText, AlertCircle } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { getApiErrorMessage } from '../../utils/apiError';
import { SourceModal } from './SourceModal';
import type { ConfluenceConnectionInfo, ConfluenceSpace, ConfluencePage, ConfluencePageWithPath } from '../../types';

interface ConfluenceModalProps {
    show: boolean;
    onClose: () => void;
    confluenceConnection: ConfluenceConnectionInfo | null;
    confluenceLoading: boolean;
    onSaveConnection: (token: string, displayName?: string) => Promise<void>;
    onAddSource: (data: { spaceKey: string; rootPageId?: string; rootPageTitle?: string; includeAttachments?: boolean; syncInterval?: number }) => void;
    fetchSpaces: () => Promise<ConfluenceSpace[]>;
    fetchSpacePages: (spaceKey: string) => Promise<ConfluencePage[]>;
    fetchPageChildren: (pageId: string) => Promise<ConfluencePage[]>;
    fetchAllSpacePages: (spaceKey: string) => Promise<ConfluencePageWithPath[]>;
}

type ScopeMode = 'entire' | 'page_tree';

interface PageNode extends ConfluencePage {
    children?: PageNode[];
    expanded?: boolean;
    childrenLoaded?: boolean;
}

const ConfluenceModalComp: React.FC<ConfluenceModalProps> = ({
    show, onClose, confluenceConnection, confluenceLoading,
    onSaveConnection, onAddSource, fetchSpaces, fetchSpacePages, fetchPageChildren,
    fetchAllSpacePages,
}) => {
    const { t } = useTheme();

    // Step tracking
    const [step, setStep] = useState<'connect' | 'space' | 'scope' | 'options' | 'confirm'>('connect');

    // Connection form
    const [pat, setPat] = useState('');
    const [saving, setSaving] = useState(false);
    const [howToOpen, setHowToOpen] = useState(false);

    // Space selection
    const [spaces, setSpaces] = useState<ConfluenceSpace[]>([]);
    const [spacesLoading, setSpacesLoading] = useState(false);
    const [spacesAttempted, setSpacesAttempted] = useState(false);
    const [spacesError, setSpacesError] = useState<string | null>(null);
    const [selectedSpace, setSelectedSpace] = useState<string>('');

    // Scope selection
    const [scopeMode, setScopeMode] = useState<ScopeMode>('entire');
    const [rootPages, setRootPages] = useState<PageNode[]>([]);
    const [pagesLoading, setPagesLoading] = useState(false);
    const [rootPagesAttempted, setRootPagesAttempted] = useState(false);
    const [rootPagesError, setRootPagesError] = useState<string | null>(null);
    const [selectedPages, setSelectedPages] = useState<{ id: string; title: string }[]>([]);

    // Search state for the space picker (step 2)
    const [spaceSearch, setSpaceSearch] = useState('');

    // Search state for the page tree (step 3, page_tree mode)
    const [pageSearch, setPageSearch] = useState('');
    const [allPagesCache, setAllPagesCache] = useState<Record<string, ConfluencePageWithPath[]>>({});
    const [allPagesLoading, setAllPagesLoading] = useState(false);

    // Options
    const [includeAttachments, setIncludeAttachments] = useState(false);
    const [syncInterval, setSyncInterval] = useState<number>(0);

    // Reset state only when modal opens (show transitions false→true)
    const prevShowRef = React.useRef(false);
    useEffect(() => {
        if (show && !prevShowRef.current) {
            const hasConnection = confluenceConnection?.connection?.status === 'active';
            setStep(hasConnection ? 'space' : 'connect');
            setPat('');
            setSaving(false);
            setHowToOpen(false);
            setSpaces([]);
            setSpacesAttempted(false);
            setSpacesError(null);
            setSelectedSpace('');
            setScopeMode('entire');
            setRootPages([]);
            setRootPagesAttempted(false);
            setRootPagesError(null);
            setSelectedPages([]);
            setIncludeAttachments(false);
            setSyncInterval(0);
            setSpaceSearch('');
            setPageSearch('');
            setAllPagesCache({});
            setAllPagesLoading(false);
        }
        prevShowRef.current = show;
    }, [show, confluenceConnection]);

    // Load spaces when entering space step. spacesAttempted is a one-shot
    // guard so a failed fetch (e.g. the stored token was silently revoked)
    // doesn't retrigger the effect via setSpacesLoading(false) and spin into
    // an infinite refresh loop.
    useEffect(() => {
        if (step === 'space' && !spacesAttempted && !spacesLoading) {
            setSpacesLoading(true);
            fetchSpaces()
                .then(s => { setSpaces(s); if (s.length > 0) setSelectedSpace(s[0].key); })
                .catch(err => setSpacesError(getApiErrorMessage(err, t('error'))))
                .finally(() => { setSpacesAttempted(true); setSpacesLoading(false); });
        }
    }, [step, spacesAttempted, spacesLoading, fetchSpaces, t]);

    // Load root pages when entering scope step. Same one-shot guard.
    useEffect(() => {
        if (step === 'scope' && scopeMode === 'page_tree' && !rootPagesAttempted && !pagesLoading && selectedSpace) {
            setPagesLoading(true);
            fetchSpacePages(selectedSpace)
                .then(pages => setRootPages(pages.map(p => ({ ...p, children: [], expanded: false, childrenLoaded: false }))))
                .catch(err => setRootPagesError(getApiErrorMessage(err, t('error'))))
                .finally(() => { setRootPagesAttempted(true); setPagesLoading(false); });
        }
    }, [step, scopeMode, rootPagesAttempted, pagesLoading, selectedSpace, fetchSpacePages, t]);

    // First-keystroke eager load of all pages in the selected space.
    // Once cached, subsequent searches filter in memory.
    useEffect(() => {
        if (
            step !== 'scope' ||
            scopeMode !== 'page_tree' ||
            !selectedSpace ||
            pageSearch.trim() === '' ||
            allPagesCache[selectedSpace] !== undefined ||
            allPagesLoading
        ) {
            return;
        }
        setAllPagesLoading(true);
        fetchAllSpacePages(selectedSpace)
            .then(pages => setAllPagesCache(prev => ({ ...prev, [selectedSpace]: pages })))
            .catch(() => setAllPagesCache(prev => ({ ...prev, [selectedSpace]: [] })))
            .finally(() => setAllPagesLoading(false));
    }, [step, scopeMode, selectedSpace, pageSearch, allPagesCache, allPagesLoading, fetchAllSpacePages]);

    const handleSaveConnection = useCallback(async () => {
        if (!pat.trim()) return;
        setSaving(true);
        try {
            await onSaveConnection(pat.trim());
            // Clear the failsafe error so the spaces-loading effect retries
            // with the new token. Same reset when first connecting.
            setSpacesError(null);
            setSpacesAttempted(false);
            setSpaces([]);
            setPat('');
            setStep('space');
        } catch {
            // error handled upstream
        } finally {
            setSaving(false);
        }
    }, [pat, onSaveConnection]);

    const handleExpandPage = useCallback(async (pageId: string) => {
        // Toggle or load children
        setRootPages(prev => {
            const update = (nodes: PageNode[]): PageNode[] =>
                nodes.map(n => {
                    if (n.id === pageId) {
                        if (n.childrenLoaded) {
                            return { ...n, expanded: !n.expanded };
                        }
                        // Will load below
                        return { ...n, expanded: true };
                    }
                    if (n.children && n.children.length > 0) {
                        return { ...n, children: update(n.children) };
                    }
                    return n;
                });
            return update(prev);
        });

        // Check if we need to load children
        const findNode = (nodes: PageNode[]): PageNode | undefined => {
            for (const n of nodes) {
                if (n.id === pageId) return n;
                if (n.children) {
                    const found = findNode(n.children);
                    if (found) return found;
                }
            }
            return undefined;
        };
        const node = findNode(rootPages);
        if (node && !node.childrenLoaded) {
            try {
                const children = await fetchPageChildren(pageId);
                setRootPages(prev => {
                    const update = (nodes: PageNode[]): PageNode[] =>
                        nodes.map(n => {
                            if (n.id === pageId) {
                                return {
                                    ...n,
                                    children: children.map(c => ({ ...c, children: [], expanded: false, childrenLoaded: false })),
                                    childrenLoaded: true,
                                    expanded: true,
                                };
                            }
                            if (n.children && n.children.length > 0) {
                                return { ...n, children: update(n.children) };
                            }
                            return n;
                        });
                    return update(prev);
                });
            } catch {
                // ignore
            }
        }
    }, [rootPages, fetchPageChildren]);

    const handleAddSource = useCallback(() => {
        const baseData: { spaceKey: string; includeAttachments?: boolean; syncInterval?: number } = {
            spaceKey: selectedSpace,
        };
        if (includeAttachments) baseData.includeAttachments = true;
        if (syncInterval > 0) baseData.syncInterval = syncInterval;

        if (scopeMode === 'page_tree' && selectedPages.length > 0) {
            for (const page of selectedPages) {
                onAddSource({ ...baseData, rootPageId: page.id, rootPageTitle: page.title });
            }
        } else {
            onAddSource(baseData);
        }
        onClose();
    }, [selectedSpace, scopeMode, selectedPages, includeAttachments, syncInterval, onAddSource, onClose]);

    const togglePageSelection = useCallback((page: { id: string; title: string }) => {
        setSelectedPages(prev => {
            const exists = prev.some(p => p.id === page.id);
            return exists ? prev.filter(p => p.id !== page.id) : [...prev, page];
        });
    }, []);

    const renderPageTree = (nodes: PageNode[], depth = 0) => (
        <div style={{ marginLeft: depth > 0 ? 18 : 0, borderLeft: depth > 0 ? '1px solid var(--border-primary)' : 'none' }}>
            {nodes.map(node => {
                const isSelected = selectedPages.some(p => p.id === node.id);
                return (
                    <div key={node.id}>
                        <div
                            style={{
                                display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer',
                                background: isSelected ? 'var(--bg-tertiary)' : 'transparent',
                                border: isSelected ? '2px solid var(--accent-primary)' : '1px solid transparent',
                                borderRadius: 6, padding: '4px 6px', marginLeft: depth > 0 ? 8 : 0,
                            }}
                        >
                            <button
                                onClick={() => handleExpandPage(node.id)}
                                style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 2, color: 'var(--text-secondary)', display: 'flex', flexShrink: 0 }}
                            >
                                {node.expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                            </button>
                            <input
                                type="checkbox"
                                checked={isSelected}
                                onChange={() => togglePageSelection({ id: node.id, title: node.title })}
                                style={{ flexShrink: 0, cursor: 'pointer' }}
                            />
                            <FileText size={14} style={{ color: 'var(--text-secondary)', flexShrink: 0 }} />
                            <span
                                role="button"
                                tabIndex={0}
                                onClick={() => togglePageSelection({ id: node.id, title: node.title })}
                                onKeyDown={(e) => {
                                    if (e.key === 'Enter' || e.key === ' ') {
                                        e.preventDefault();
                                        togglePageSelection({ id: node.id, title: node.title });
                                    }
                                }}
                                aria-pressed={isSelected}
                                style={{ fontSize: '0.85rem', color: 'var(--text-primary)', flex: 1, cursor: 'pointer' }}
                            >
                                {node.title}
                            </span>
                        </div>
                        {node.expanded && node.children && node.children.length > 0 && renderPageTree(node.children, depth + 1)}
                    </div>
                );
            })}
        </div>
    );

    const renderFlatMatches = (matches: ConfluencePageWithPath[]) => (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {matches.map(node => {
                const isSelected = selectedPages.some(p => p.id === node.id);
                const breadcrumb = node.ancestorTitles.join(' / ');
                return (
                    <div
                        key={node.id}
                        style={{
                            display: 'flex', alignItems: 'flex-start', gap: 6, cursor: 'pointer',
                            background: isSelected ? 'var(--bg-tertiary)' : 'transparent',
                            border: isSelected ? '2px solid var(--accent-primary)' : '1px solid transparent',
                            borderRadius: 6, padding: '6px 8px',
                        }}
                    >
                        <input
                            type="checkbox"
                            checked={isSelected}
                            onChange={() => togglePageSelection({ id: node.id, title: node.title })}
                            style={{ flexShrink: 0, cursor: 'pointer', marginTop: 2 }}
                        />
                        <FileText size={14} style={{ color: 'var(--text-secondary)', flexShrink: 0, marginTop: 2 }} />
                        <div
                            role="button"
                            tabIndex={0}
                            onClick={() => togglePageSelection({ id: node.id, title: node.title })}
                            onKeyDown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    togglePageSelection({ id: node.id, title: node.title });
                                }
                            }}
                            aria-pressed={isSelected}
                            style={{ flex: 1, minWidth: 0, cursor: 'pointer' }}
                        >
                            <div style={{ fontSize: '0.85rem', color: 'var(--text-primary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                {node.title}
                            </div>
                            {breadcrumb && (
                                <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                    {breadcrumb}
                                </div>
                            )}
                        </div>
                    </div>
                );
            })}
        </div>
    );

    // renderTokenForm renders the PAT input + "how to create a token" steps.
    // Used by both the initial connect step and the failsafe-reconnect view
    // shown when a previously-saved token has been silently revoked.
    const renderTokenForm = (submitLabel: string) => (
        <>
            <button
                onClick={() => setHowToOpen(!howToOpen)}
                style={{
                    background: 'none', border: 'none', cursor: 'pointer', padding: '4px 0',
                    fontSize: '0.8rem', color: 'var(--accent-primary)', display: 'flex', alignItems: 'center', gap: 4,
                }}
            >
                {howToOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                {t('confluencePatHowTo')}
            </button>
            {howToOpen && (
                <ol style={{ margin: '4px 0 12px', paddingLeft: 20, fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                    <li>{t('confluencePatStep1')}</li>
                    <li>{t('confluencePatStep2')}</li>
                    <li>{t('confluencePatStep3')}</li>
                </ol>
            )}

            <label style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: 4, display: 'block' }}>{t('confluencePatLabel')}</label>
            <input
                type="password"
                value={pat}
                onChange={e => setPat(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') handleSaveConnection(); }}
                className="sidebar-left__tools-input"
                // eslint-disable-next-line jsx-a11y/no-autofocus -- focus first field on dialog open (WAI-ARIA dialog pattern)
                autoFocus
                placeholder="••••••••••••"
            />
            <button
                onClick={handleSaveConnection}
                disabled={saving || !pat.trim()}
                className="search-button"
                style={{ marginTop: 8 }}
            >
                {saving ? <Loader2 className="animate-spin" size={16} /> : null}
                {submitLabel}
            </button>
        </>
    );

    const renderStepContent = () => {
        // Not enabled
        if (confluenceConnection && !confluenceConnection.enabled) {
            return (
                <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '8px 0' }}>
                    <Info size={18} style={{ color: 'var(--text-secondary)', flexShrink: 0, marginTop: 2 }} />
                    <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('confluenceNotEnabled')}</p>
                </div>
            );
        }

        if (step === 'connect') {
            return (
                <>
                    <h4 style={{ margin: '0 0 8px', fontSize: '0.95rem' }}>{t('connectToConfluence')}</h4>
                    <p style={{ margin: '0 0 12px', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('confluencePatHelp')}</p>
                    {renderTokenForm(t('connect'))}
                </>
            );
        }

        if (step === 'space') {
            // Failsafe: the cached connection looked 'active' but the spaces
            // call rejected the token. Show a reconnect form rather than
            // leaving the user staring at an empty list (or, before the loop
            // fix, a never-ending spinner).
            if (spacesError) {
                return (
                    <>
                        <div
                            role="alert"
                            style={{
                                display: 'flex', alignItems: 'flex-start', gap: 8, padding: '10px 12px', marginBottom: 12,
                                borderRadius: 8, background: 'rgba(239, 68, 68, 0.08)',
                                border: '1px solid rgba(239, 68, 68, 0.3)',
                            }}
                        >
                            <AlertCircle size={18} style={{ color: '#ef4444', flexShrink: 0, marginTop: 2 }} />
                            <div style={{ flex: 1 }}>
                                <div style={{ fontWeight: 500, color: 'var(--text-primary)', fontSize: '0.9rem', marginBottom: 4 }}>
                                    {t('confluenceTokenInvalidTitle')}
                                </div>
                                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                                    {t('confluenceTokenInvalidHelp')}
                                </div>
                            </div>
                        </div>
                        {renderTokenForm(t('confluenceRetryAfterReconnect'))}
                    </>
                );
            }
            const q = spaceSearch.trim().toLowerCase();
            const filteredSpaces = q === ''
                ? spaces
                : spaces.filter(s =>
                    s.name.toLowerCase().includes(q) ||
                    s.key.toLowerCase().includes(q)
                );
            return (
                <>
                    <label style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: 4, display: 'block' }}>{t('selectSpace')}</label>
                    {spacesLoading ? (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '12px 0' }}>
                            <Loader2 className="animate-spin" size={16} />
                            <span style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('loading')}</span>
                        </div>
                    ) : (
                        <>
                            <input
                                type="text"
                                value={spaceSearch}
                                onChange={e => setSpaceSearch(e.target.value)}
                                placeholder={t('searchSpacesPlaceholder')}
                                className="sidebar-left__tools-input"
                                style={{ marginBottom: 8 }}
                            />
                            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxHeight: 400, overflowY: 'auto' }}>
                                {filteredSpaces.length === 0 ? (
                                    <p style={{ margin: 0, padding: '8px 4px', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('noMatches')}</p>
                                ) : filteredSpaces.map(s => (
                                    <button
                                        key={s.key}
                                        onClick={() => setSelectedSpace(s.key)}
                                        style={{
                                            display: 'block', width: '100%', textAlign: 'left',
                                            padding: '8px 10px', borderRadius: 6, cursor: 'pointer',
                                            fontSize: '0.85rem', color: 'var(--text-primary)',
                                            background: selectedSpace === s.key ? 'var(--bg-tertiary)' : 'transparent',
                                            border: selectedSpace === s.key ? '2px solid var(--accent-primary)' : '1px solid var(--border-primary)',
                                        }}
                                    >
                                        <strong>{s.name}</strong> <span style={{ color: 'var(--text-secondary)', fontSize: '0.8rem' }}>({s.key})</span>
                                    </button>
                                ))}
                            </div>
                            <button
                                onClick={() => { setRootPages([]); setSelectedPages([]); setStep('scope'); }}
                                disabled={!selectedSpace}
                                className="search-button"
                                style={{ marginTop: 8 }}
                            >
                                {t('onboardingNext')}
                            </button>
                        </>
                    )}
                </>
            );
        }

        if (step === 'scope') {
            return (
                <>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 12 }}>
                        <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: '0.85rem' }}>
                            <input
                                type="radio"
                                name="scope"
                                checked={scopeMode === 'entire'}
                                onChange={() => { setScopeMode('entire'); setSelectedPages([]); }}
                            />
                            {t('entireSpace')}
                        </label>
                        <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: '0.85rem' }}>
                            <input
                                type="radio"
                                name="scope"
                                checked={scopeMode === 'page_tree'}
                                onChange={() => setScopeMode('page_tree')}
                            />
                            {t('specificPageTree')}
                        </label>
                    </div>

                    {scopeMode === 'page_tree' && rootPagesError && (
                        <div
                            role="alert"
                            style={{
                                display: 'flex', alignItems: 'flex-start', gap: 8, padding: '8px 10px', marginBottom: 8,
                                borderRadius: 6, background: 'rgba(239, 68, 68, 0.08)',
                                border: '1px solid rgba(239, 68, 68, 0.3)',
                            }}
                        >
                            <AlertCircle size={14} style={{ color: '#ef4444', flexShrink: 0, marginTop: 2 }} />
                            <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{rootPagesError}</span>
                        </div>
                    )}

                    {scopeMode === 'page_tree' && (
                        <>
                            <label style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: 4, display: 'block' }}>{t('selectRootPage')}</label>

                            <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
                                <input
                                    type="text"
                                    value={pageSearch}
                                    onChange={e => setPageSearch(e.target.value)}
                                    placeholder={t('searchPagesPlaceholder')}
                                    className="sidebar-left__tools-input"
                                    style={{ flex: 1 }}
                                />
                                {allPagesLoading && pageSearch.trim() !== '' && (
                                    <Loader2 className="animate-spin" size={16} style={{ color: 'var(--text-secondary)', flexShrink: 0 }} />
                                )}
                            </div>

                            {pageSearch.trim() === '' ? (
                                pagesLoading ? (
                                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 0' }}>
                                        <Loader2 className="animate-spin" size={16} />
                                    </div>
                                ) : (
                                    <div style={{ maxHeight: 400, overflowY: 'auto', border: '1px solid var(--border-primary)', borderRadius: 6, padding: 4 }}>
                                        {renderPageTree(rootPages)}
                                    </div>
                                )
                            ) : (
                                <div style={{ maxHeight: 400, overflowY: 'auto', border: '1px solid var(--border-primary)', borderRadius: 6, padding: 4 }}>
                                    {(() => {
                                        const cached = allPagesCache[selectedSpace];
                                        if (!cached) {
                                            return (
                                                <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 4px' }}>
                                                    <Loader2 className="animate-spin" size={14} />
                                                    <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('loading')}</span>
                                                </div>
                                            );
                                        }
                                        const q = pageSearch.trim().toLowerCase();
                                        const matches = cached.filter(p =>
                                            p.title.toLowerCase().includes(q) ||
                                            p.ancestorTitles.some(a => a.toLowerCase().includes(q))
                                        );
                                        if (matches.length === 0) {
                                            return (
                                                <p style={{ margin: 0, padding: '8px 4px', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('noMatches')}</p>
                                            );
                                        }
                                        return renderFlatMatches(matches);
                                    })()}
                                </div>
                            )}

                            {selectedPages.length > 0 && (
                                <p style={{ margin: '8px 0 0', fontSize: '0.8rem', color: 'var(--accent-primary)' }}>
                                    {selectedPages.length} {t('confluenceSelectedPages')}
                                </p>
                            )}
                        </>
                    )}

                    <button
                        onClick={() => setStep('options')}
                        disabled={scopeMode === 'page_tree' && selectedPages.length === 0}
                        className="search-button"
                        style={{ marginTop: 8 }}
                    >
                        {t('onboardingNext')}
                    </button>
                </>
            );
        }

        if (step === 'options') {
            return (
                <>
                    <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: '0.85rem', marginBottom: 12 }}>
                        <input
                            type="checkbox"
                            checked={includeAttachments}
                            onChange={e => setIncludeAttachments(e.target.checked)}
                        />
                        {t('includeAttachments')}
                    </label>

                    <div className="sidebar-left__slider-row">
                        <label htmlFor="confluence-sync-interval">{t('syncIntervalLabel')}</label>
                        <select
                            id="confluence-sync-interval"
                            value={syncInterval}
                            onChange={e => setSyncInterval(parseInt(e.target.value, 10))}
                            className="sidebar-left__tools-select"
                        >
                            <option value="0">{t('manualOnly')}</option>
                            <option value="360">{t('every6Hours')}</option>
                            <option value="720">{t('every12Hours')}</option>
                            <option value="1440">{t('daily')}</option>
                            <option value="10080">{t('weekly')}</option>
                        </select>
                    </div>

                    <button
                        onClick={handleAddSource}
                        disabled={confluenceLoading}
                        className="search-button"
                        style={{ marginTop: 8 }}
                    >
                        {confluenceLoading ? <Loader2 className="animate-spin" size={16} /> : <Plus size={16} />}
                        {t('addConfluenceSource')}
                    </button>
                </>
            );
        }

        return null;
    };

    return (
        <SourceModal title={t('confluence')} show={show} onClose={onClose} className="source-modal--large">
            {renderStepContent()}
        </SourceModal>
    );
};

export const ConfluenceModal = memo(ConfluenceModalComp);
