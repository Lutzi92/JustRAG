/**
 * Academic Search Mode Component
 *
 * A dedicated interface for basic JustFind search that lets users search
 * academic papers, browse results, add papers to a knowledge base, export
 * BibTeX citations, and kick off a deeper research session.
 */

import { useState } from 'react';
import {
    Search,
    Loader2,
    FileText,
    ExternalLink,
    CheckSquare,
    Square,
    Check,
    Download,
    ChevronDown,
    ChevronRight,
    AlertCircle,
    X,
} from 'lucide-react';
import { API_BASE_URL, authFetch } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import type { AcademicPaper } from '../types';

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

interface AcademicSearchModeProps {
    kbId: string;
    query: string;
    onQueryChange: (query: string) => void;
    peerReviewedOnly: boolean;
    onPeerReviewedChange: (value: boolean) => void;
    papers: AcademicPaper[];
    onPapersChange: (papers: AcademicPaper[]) => void;
    onClose: () => void;
    academicSearchName?: string;
    /** Optional toggle element rendered below the header */
    modeToggle?: React.ReactNode;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

function AcademicSearchMode({
    kbId,
    query,
    onQueryChange,
    peerReviewedOnly,
    onPeerReviewedChange,
    papers,
    onPapersChange,
    onClose,
    academicSearchName,
    modeToggle,
}: AcademicSearchModeProps) {
    const { language } = useTheme();
    const toast = useToast();

    // ---- State ----
    const [isSearching, setIsSearching] = useState(false);
    const [hasSearched, setHasSearched] = useState(papers.length > 0);
    const [error, setError] = useState<string | null>(null);
    const [selectedPaperIds, setSelectedPaperIds] = useState<Set<string>>(new Set());
    const [addingPapers, setAddingPapers] = useState(false);
    const [addingPaperIds, setAddingPaperIds] = useState<Set<string>>(new Set());
    const [addedPaperIds, setAddedPaperIds] = useState<Set<string>>(new Set());
    const [expandedAbstracts, setExpandedAbstracts] = useState<Set<string>>(new Set());
    const [isExportingBibtex, setIsExportingBibtex] = useState(false);


    // ---- Labels ----
    const labels = language === 'en'
        ? {
            title: academicSearchName || 'Academic Search',
            subtitle: 'Search academic papers within JustFind. Select papers to add to your knowledge base or export citations.',
            searchPlaceholder: 'Enter search query...',
            peerReviewedLabel: 'Peer-reviewed only',
            searchButton: 'Search',
            searching: 'Searching...',
            resultsTitle: 'Search Results',
            noResults: 'No papers found. Try a different search query.',
            selectAll: 'Select All',
            deselectAll: 'Deselect All',
            addSelectedToKb: 'Add Selected to Knowledge Base',
            addingPapers: 'Adding...',
            addToKb: 'Add to KB',
            added: 'Added',
            exportBibtex: 'Export BibTeX',
            exporting: 'Exporting...',
            noPdfAvailable: 'No PDF',
            pdfAvailable: 'PDF',
            citations: 'citations',
            showAbstract: 'Show abstract',
            hideAbstract: 'Hide abstract',
        }
        : {
            title: academicSearchName || 'Akademische Suche',
            subtitle: 'Suche nach akademischen Papieren in JustFind. Wähle Papiere aus, um sie zur Wissensdatenbank hinzuzufügen oder Zitationen zu exportieren.',
            searchPlaceholder: 'Suchanfrage eingeben...',
            peerReviewedLabel: 'Nur Peer-Reviewed',
            searchButton: 'Suchen',
            searching: 'Suche läuft...',
            resultsTitle: 'Suchergebnisse',
            noResults: 'Keine Paper gefunden. Versuche eine andere Suchanfrage.',
            selectAll: 'Alle auswählen',
            deselectAll: 'Alle abwählen',
            addSelectedToKb: 'Ausgewählte zur Wissensdatenbank hinzufügen',
            addingPapers: 'Wird hinzugefügt...',
            addToKb: 'Zur WDB',
            added: 'Hinzugefügt',
            exportBibtex: 'BibTeX exportieren',
            exporting: 'Exportiere...',
            noPdfAvailable: 'Kein PDF',
            pdfAvailable: 'PDF',
            citations: 'Zitationen',
            showAbstract: 'Abstract anzeigen',
            hideAbstract: 'Abstract ausblenden',
        };

    // ---- Search ----
    const handleSearch = async () => {
        if (!query.trim() || isSearching) return;

        setIsSearching(true);
        setError(null);
        onPapersChange([]);
        setSelectedPaperIds(new Set());
        setAddedPaperIds(new Set());

        try {
            const response = await authFetch(
                `${API_BASE_URL}/api/kb/${kbId}/academic-search`,
                {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ query: query.trim(), peerReviewedOnly }),
                },
            );

            if (!response.ok) {
                const data = await response.json().catch(() => ({}));
                throw new Error(data.error || `Search failed (HTTP ${response.status})`);
            }

            const data = await response.json();
            onPapersChange(data.papers ?? []);
            setHasSearched(true);
        } catch (err: unknown) {
            const errorObj = err as Error;
            setError(errorObj.message || String(err));
        } finally {
            setIsSearching(false);
        }
    };

    const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
        if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
            e.preventDefault();
            handleSearch();
        }
    };

    // ---- Paper Selection ----
    const togglePaperSelection = (paperId: string) => {
        setSelectedPaperIds(prev => {
            const next = new Set(prev);
            if (next.has(paperId)) {
                next.delete(paperId);
            } else {
                next.add(paperId);
            }
            return next;
        });
    };

    const selectablePapers = papers.filter(p => p.pdfUrl && !addedPaperIds.has(p.id));
    const selectedCount = selectablePapers.filter(p => selectedPaperIds.has(p.id)).length;
    const allSelected = selectablePapers.length > 0 && selectedCount === selectablePapers.length;

    const toggleSelectAll = () => {
        if (allSelected) {
            setSelectedPaperIds(new Set());
        } else {
            setSelectedPaperIds(new Set(selectablePapers.map(p => p.id)));
        }
    };

    // ---- Abstract Toggle ----
    const toggleAbstract = (paperId: string) => {
        setExpandedAbstracts(prev => {
            const next = new Set(prev);
            if (next.has(paperId)) {
                next.delete(paperId);
            } else {
                next.add(paperId);
            }
            return next;
        });
    };

    // ---- Add papers (bulk) ----
    const handleAddSelected = async () => {
        const selected = papers.filter(p => selectedPaperIds.has(p.id) && p.pdfUrl);
        if (selected.length === 0) return;

        setAddingPapers(true);
        try {
            const response = await authFetch(
                `${API_BASE_URL}/api/kb/${kbId}/academic-research/add-papers`,
                {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ papers: selected }),
                },
            );

            if (!response.ok) throw new Error('Add papers failed');

            const result = await response.json();
            const addedCount = result.added?.length ?? 0;
            const failedCount = result.failed?.length ?? 0;

            if (addedCount > 0) {
                const newAdded = new Set(addedPaperIds);
                (result.added as { id: string }[]).forEach(r => newAdded.add(r.id));
                setAddedPaperIds(newAdded);
                toast.success(`${addedCount} ${language === 'en' ? 'paper(s) added' : 'Paper hinzugefügt'}`);
            }
            if (failedCount > 0) {
                const failedTitles = result.failed.map((f: { title: string }) => f.title).join(', ');
                toast.error(`${language === 'en' ? 'Failed to add' : 'Fehler beim Hinzufügen'}: ${failedTitles}`);
            }

            setSelectedPaperIds(new Set());
        } catch {
            toast.error(language === 'en' ? 'Failed to add papers' : 'Fehler beim Hinzufügen der Paper');
        } finally {
            setAddingPapers(false);
        }
    };

    // ---- Add single paper ----
    const handleAddSinglePaper = async (paper: AcademicPaper) => {
        if (!paper.pdfUrl || addingPaperIds.has(paper.id) || addedPaperIds.has(paper.id)) return;

        setAddingPaperIds(prev => new Set(prev).add(paper.id));
        try {
            const response = await authFetch(
                `${API_BASE_URL}/api/kb/${kbId}/academic-research/add-papers`,
                {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ papers: [paper] }),
                },
            );

            if (!response.ok) throw new Error('Add paper failed');

            const result = await response.json();
            if ((result.added?.length ?? 0) > 0) {
                setAddedPaperIds(prev => new Set(prev).add(paper.id));
                toast.success(`${language === 'en' ? 'Paper added' : 'Paper hinzugefügt'}`);
            } else {
                toast.error(language === 'en' ? 'Failed to add paper' : 'Fehler beim Hinzufügen');
            }
        } catch {
            toast.error(language === 'en' ? 'Failed to add paper' : 'Fehler beim Hinzufügen');
        } finally {
            setAddingPaperIds(prev => {
                const next = new Set(prev);
                next.delete(paper.id);
                return next;
            });
        }
    };

    // ---- BibTeX Export ----
    const handleExportBibtex = async () => {
        const toExport = selectedPaperIds.size > 0
            ? papers.filter(p => selectedPaperIds.has(p.id))
            : papers;

        if (toExport.length === 0) return;

        setIsExportingBibtex(true);
        try {
            const response = await authFetch(
                `${API_BASE_URL}/api/kb/${kbId}/export/academic-bibtex`,
                {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ papers: toExport }),
                },
            );

            if (!response.ok) throw new Error('Export failed');

            const blob = await response.blob();
            const url = window.URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `academic_citations_${new Date().toISOString().split('T')[0]}.bib`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            setTimeout(() => window.URL.revokeObjectURL(url), 0);
        } catch {
            toast.error(language === 'en' ? 'BibTeX export failed' : 'BibTeX-Export fehlgeschlagen');
        } finally {
            setIsExportingBibtex(false);
        }
    };

    // ---- Render ----
    return (
        <div className="research-mode-container">
            {/* Header */}
            <div className="research-mode-header">
                <div className="research-mode-header-left">
                    <h2 style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Search size={22} />
                        {labels.title}
                    </h2>
                    <span className="research-mode-subtitle">{labels.subtitle}</span>
                </div>
                <button
                    className="research-mode-close"
                    onClick={onClose}
                    aria-label={language === 'en' ? 'Close' : 'Schließen'}
                >
                    <X size={18} />
                </button>
            </div>

            {/* Mode Toggle */}
            {modeToggle}

            {/* Search Input */}
            <div className="research-mode-input-section">
                <textarea
                    value={query}
                    onChange={e => onQueryChange(e.target.value)}
                    onKeyDown={handleKeyDown}
                    placeholder={labels.searchPlaceholder}
                    rows={2}
                    disabled={isSearching}
                />
                <div className="research-mode-actions" style={{ display: 'flex', alignItems: 'center', gap: '12px', flexWrap: 'wrap' }}>
                    <label
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '6px',
                            cursor: isSearching ? 'default' : 'pointer',
                            opacity: isSearching ? 0.5 : 1,
                            fontSize: '0.85rem',
                        }}
                    >
                        <input
                            type="checkbox"
                            checked={peerReviewedOnly}
                            onChange={e => onPeerReviewedChange(e.target.checked)}
                            disabled={isSearching}
                        />
                        {labels.peerReviewedLabel}
                    </label>
                    <button
                        className="research-mode-start-btn"
                        onClick={handleSearch}
                        disabled={!query.trim() || isSearching}
                        style={{ display: 'flex', alignItems: 'center', gap: '6px' }}
                    >
                        {isSearching ? (
                            <>
                                <Loader2 size={14} className="animate-spin" />
                                {labels.searching}
                            </>
                        ) : (
                            <>
                                <Search size={14} />
                                {labels.searchButton}
                            </>
                        )}
                    </button>
                </div>
            </div>

            {/* Error */}
            {error && (
                <div className="research-mode-error">
                    <span className="research-mode-error-icon"><AlertCircle size={16} /></span>
                    <span>{error}</span>
                </div>
            )}

            {/* Results */}
            {papers.length > 0 && (
                <div
                    style={{
                        border: '1px solid var(--border-color)',
                        borderRadius: '8px',
                        overflow: 'hidden',
                    }}
                >
                    {/* Results Header */}
                    <div
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'space-between',
                            padding: '10px 16px',
                            background: 'var(--bg-secondary)',
                            borderBottom: '1px solid var(--border-color)',
                            gap: '8px',
                            flexWrap: 'wrap',
                        }}
                    >
                        <span
                            style={{
                                fontSize: '0.9rem',
                                fontWeight: 600,
                                color: 'var(--text-primary)',
                                display: 'flex',
                                alignItems: 'center',
                                gap: '6px',
                            }}
                        >
                            <Search size={15} />
                            {labels.resultsTitle} ({papers.length})
                        </span>

                        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
                            {/* Select All */}
                            {selectablePapers.length > 0 && (
                                <button
                                    className="research-mode-copy-btn"
                                    onClick={toggleSelectAll}
                                >
                                    {allSelected ? <CheckSquare size={14} /> : <Square size={14} />}
                                    {allSelected ? labels.deselectAll : labels.selectAll}
                                </button>
                            )}

                            {/* BibTeX Export */}
                            <button
                                className="research-mode-copy-btn"
                                onClick={handleExportBibtex}
                                disabled={isExportingBibtex}
                            >
                                {isExportingBibtex ? (
                                    <Loader2 size={14} className="animate-spin" />
                                ) : (
                                    <Download size={14} />
                                )}
                                {isExportingBibtex ? labels.exporting : labels.exportBibtex}
                                {selectedPaperIds.size > 0 && ` (${selectedPaperIds.size})`}
                            </button>
                        </div>
                    </div>

                    {/* Papers List */}
                    <div style={{ maxHeight: '480px', overflowY: 'auto' }}>
                        {papers.map(paper => {
                            const isSelected = selectedPaperIds.has(paper.id);
                            const hasPdf = !!paper.pdfUrl;
                            const abstractExpanded = expandedAbstracts.has(paper.id);
                            const isAdding = addingPaperIds.has(paper.id);
                            const isAdded = addedPaperIds.has(paper.id);
                            const abstractSnippet =
                                paper.abstract && paper.abstract.length > 150
                                    ? paper.abstract.substring(0, 150) + '...'
                                    : paper.abstract;
                            const paperLink = paper.doi
                                ? `https://doi.org/${paper.doi}`
                                : paper.url;

                            return (
                                <div
                                    key={paper.id}
                                    style={{
                                        display: 'flex',
                                        alignItems: 'flex-start',
                                        gap: '10px',
                                        padding: '10px 16px',
                                        borderBottom: '1px solid var(--border-color)',
                                        background: isSelected ? 'var(--bg-selected, color-mix(in srgb, var(--accent-primary) 5%, transparent))' : undefined,
                                        transition: 'background 0.15s',
                                    }}
                                >
                                    {/* Checkbox */}
                                    <button
                                        onClick={() => hasPdf && togglePaperSelection(paper.id)}
                                        disabled={!hasPdf}
                                        aria-label={isSelected ? `Deselect ${paper.title}` : `Select ${paper.title}`}
                                        style={{
                                            background: 'none',
                                            border: 'none',
                                            cursor: hasPdf ? 'pointer' : 'not-allowed',
                                            padding: '2px',
                                            marginTop: '2px',
                                            color: hasPdf ? 'var(--accent-primary)' : 'var(--text-secondary)',
                                            opacity: hasPdf ? 1 : 0.4,
                                            flexShrink: 0,
                                        }}
                                    >
                                        {isSelected ? <CheckSquare size={18} /> : <Square size={18} />}
                                    </button>

                                    {/* Paper Info */}
                                    <div style={{ flex: 1, minWidth: 0 }}>
                                        {/* Title */}
                                        <div style={{ marginBottom: '4px' }}>
                                            <a
                                                href={paperLink}
                                                target="_blank"
                                                rel="noopener noreferrer"
                                                style={{
                                                    color: 'var(--accent-primary)',
                                                    textDecoration: 'none',
                                                    fontWeight: 500,
                                                    fontSize: '0.9rem',
                                                    display: 'inline-flex',
                                                    alignItems: 'center',
                                                    gap: '4px',
                                                }}
                                            >
                                                {paper.title}
                                                <ExternalLink size={12} style={{ flexShrink: 0 }} />
                                            </a>
                                        </div>

                                        {/* Authors + Year + Journal */}
                                        <div
                                            style={{
                                                fontSize: '0.8rem',
                                                color: 'var(--text-secondary)',
                                                marginBottom: '4px',
                                            }}
                                        >
                                            {(paper.authors || []).join(', ') || 'Unknown'}
                                            {paper.year ? ` (${paper.year})` : ''}
                                            {paper.journal ? ` — ${paper.journal}` : ''}
                                            {paper.doi && (
                                                <span style={{ marginLeft: '4px' }}>
                                                    · DOI: {paper.doi}
                                                </span>
                                            )}
                                        </div>

                                        {/* Badges row */}
                                        <div
                                            style={{
                                                display: 'flex',
                                                alignItems: 'center',
                                                gap: '8px',
                                                flexWrap: 'wrap',
                                                marginBottom: paper.abstract ? '4px' : 0,
                                            }}
                                        >
                                            {/* Citation count */}
                                            {paper.citationCount != null && paper.citationCount > 0 && (
                                                <span
                                                    style={{
                                                        display: 'inline-flex',
                                                        alignItems: 'center',
                                                        gap: '4px',
                                                        fontSize: '0.75rem',
                                                        padding: '2px 8px',
                                                        borderRadius: '10px',
                                                        background: 'var(--bg-secondary)',
                                                        color: 'var(--text-secondary)',
                                                        fontWeight: 500,
                                                    }}
                                                >
                                                    {paper.citationCount} {labels.citations}
                                                </span>
                                            )}

                                            {/* PDF indicator */}
                                            {hasPdf ? (
                                                <span
                                                    style={{
                                                        display: 'inline-flex',
                                                        alignItems: 'center',
                                                        gap: '3px',
                                                        fontSize: '0.75rem',
                                                        padding: '2px 8px',
                                                        borderRadius: '10px',
                                                        background: 'var(--badge-success-bg, #e6f4ea)',
                                                        color: 'var(--badge-success-foreground, #1e7e34)',
                                                        fontWeight: 500,
                                                    }}
                                                >
                                                    <FileText size={12} />
                                                    {labels.pdfAvailable}
                                                </span>
                                            ) : (
                                                <span
                                                    style={{
                                                        display: 'inline-flex',
                                                        alignItems: 'center',
                                                        gap: '3px',
                                                        fontSize: '0.75rem',
                                                        padding: '2px 8px',
                                                        borderRadius: '10px',
                                                        background: 'var(--bg-secondary)',
                                                        color: 'var(--text-secondary)',
                                                        fontWeight: 500,
                                                    }}
                                                >
                                                    {labels.noPdfAvailable}
                                                </span>
                                            )}
                                        </div>

                                        {/* Abstract */}
                                        {paper.abstract && (
                                            <div style={{ marginTop: '4px' }}>
                                                <button
                                                    onClick={() => toggleAbstract(paper.id)}
                                                    style={{
                                                        background: 'none',
                                                        border: 'none',
                                                        cursor: 'pointer',
                                                        padding: 0,
                                                        fontSize: '0.75rem',
                                                        color: 'var(--accent-primary)',
                                                        display: 'inline-flex',
                                                        alignItems: 'center',
                                                        gap: '3px',
                                                    }}
                                                >
                                                    {abstractExpanded
                                                        ? <ChevronDown size={12} />
                                                        : <ChevronRight size={12} />}
                                                    {abstractExpanded ? labels.hideAbstract : labels.showAbstract}
                                                </button>
                                                {abstractExpanded ? (
                                                    <p
                                                        style={{
                                                            fontSize: '0.8rem',
                                                            color: 'var(--text-secondary)',
                                                            lineHeight: 1.4,
                                                            margin: '4px 0 0',
                                                        }}
                                                    >
                                                        {paper.abstract}
                                                    </p>
                                                ) : (
                                                    abstractSnippet &&
                                                    abstractSnippet !== paper.abstract && (
                                                        <p
                                                            style={{
                                                                fontSize: '0.8rem',
                                                                color: 'var(--text-secondary)',
                                                                lineHeight: 1.4,
                                                                margin: '4px 0 0',
                                                            }}
                                                        >
                                                            {abstractSnippet}
                                                        </p>
                                                    )
                                                )}
                                            </div>
                                        )}
                                    </div>

                                    {/* Individual Add to KB button */}
                                    <button
                                        className="research-mode-copy-btn"
                                        onClick={() => handleAddSinglePaper(paper)}
                                        disabled={!hasPdf || isAdding || isAdded}
                                        title={hasPdf ? undefined : labels.noPdfAvailable}
                                        style={{
                                            flexShrink: 0,
                                            whiteSpace: 'nowrap',
                                            opacity: hasPdf ? 1 : 0.4,
                                        }}
                                    >
                                        {isAdding ? (
                                            <Loader2 size={12} className="animate-spin" />
                                        ) : isAdded ? (
                                            <Check size={12} />
                                        ) : (
                                            <FileText size={12} />
                                        )}
                                        {isAdded ? labels.added : labels.addToKb}
                                    </button>
                                </div>
                            );
                        })}
                    </div>

                    {/* Bulk Actions Bar */}
                    {(selectedCount > 0 || papers.length > 0) && (
                        <div
                            style={{
                                padding: '10px 16px',
                                background: 'var(--bg-secondary)',
                                borderTop: '1px solid var(--border-color)',
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'flex-end',
                                gap: '8px',
                                flexWrap: 'wrap',
                            }}
                        >
                            {selectedCount > 0 && (
                                <button
                                    className="research-mode-start-btn"
                                    onClick={handleAddSelected}
                                    disabled={addingPapers}
                                    style={{ display: 'flex', alignItems: 'center', gap: '6px', fontSize: '0.85rem', padding: '6px 12px' }}
                                >
                                    {addingPapers ? (
                                        <>
                                            <Loader2 size={14} className="animate-spin" />
                                            {labels.addingPapers}
                                        </>
                                    ) : (
                                        <>
                                            <Download size={14} />
                                            {labels.addSelectedToKb} ({selectedCount})
                                        </>
                                    )}
                                </button>
                            )}
                        </div>
                    )}
                </div>
            )}

            {/* Empty state */}
            {!isSearching && papers.length === 0 && hasSearched && error === null && (
                <p style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', textAlign: 'center', padding: '16px 0' }}>
                    {labels.noResults}
                </p>
            )}
        </div>
    );
}

export default AcademicSearchMode;
