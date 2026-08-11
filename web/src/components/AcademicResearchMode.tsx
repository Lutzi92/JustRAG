/**
 * Academic Research Mode Component
 *
 * A dedicated interface for the academic research agent that searches
 * academic databases, analyzes papers, and produces a literature review.
 * Shows research progress, findings, papers found, and final report.
 */

import { useState, useRef, useEffect, useCallback } from 'react';
import {
    GraduationCap,
    Loader2,
    Copy,
    FileText,
    FileDown,
    Download,
    ChevronDown,
    ChevronRight,
    ExternalLink,
    CheckSquare,
    Square,
    X,
    Rocket,
    ClipboardList,
    Search,
    BookOpen,
    FlaskConical,
    Brain,
    CheckCircle2,
    AlertCircle,
    Ban,
    Circle,
} from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { API_BASE_URL, authFetch } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useAuth } from '../contexts/AuthContext';
import { useToast } from '../contexts/ToastContext';
import { copyToClipboard } from '../utils/clipboard';
import DOMPurify from 'dompurify';
import type { AcademicPaper, AcademicFinding } from '../types';

// ---------------------------------------------------------------------------
// Local types (mirror backend SSE events)
// ---------------------------------------------------------------------------

interface AcademicResearchProgressEvent {
    type:
    | 'started'
    | 'planning'
    | 'searching'
    | 'reading'
    | 'analyzing'
    | 'synthesizing'
    | 'generating_report'
    | 'complete'
    | 'error'
    | 'cancelled';
    message: string;
    stepNumber: number;
    totalSteps: number;
    findings?: AcademicFinding[];
    papers?: AcademicPaper[];
    report?: string;
    timestamp: number;
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

interface AcademicResearchModeProps {
    kbId: string;
    onClose: () => void;
    loadedSession?: {
        id: string;
        goal: string;
        report: string;
        findings: AcademicFinding[];
        papers?: AcademicPaper[];
    } | null;
    onSessionSaved?: () => void;
    onClearSession?: () => void;
    onRunningChange?: (running: boolean) => void;
    /** Pre-fill topic from basic search */
    initialTopic?: string;
    /** Papers already found in basic search */
    initialPapers?: AcademicPaper[];
    /** Peer-reviewed filter from basic search */
    initialPeerReviewedOnly?: boolean;
    /** Callback to return to search mode */
    onBackToSearch?: () => void;
    /** Optional toggle element rendered below the header */
    modeToggle?: React.ReactNode;
}

// ---------------------------------------------------------------------------
// Status Icons & Labels
// ---------------------------------------------------------------------------

// Research step \u2192 lucide icon (improvement #2: one icon set, no emoji).
const STATUS_ICONS: Record<string, LucideIcon> = {
    started: Rocket,
    planning: ClipboardList,
    searching: Search,
    reading: BookOpen,
    analyzing: FlaskConical,
    synthesizing: Brain,
    generating_report: FileText,
    complete: CheckCircle2,
    error: AlertCircle,
    cancelled: Ban,
};

const STATUS_LABELS: Record<string, Record<string, string>> = {
    de: {
        started: 'Gestartet',
        planning: 'Plane Recherche',
        searching: 'Durchsuche akademische Datenbanken',
        reading: 'Lese Paper',
        analyzing: 'Analysiere Paper',
        synthesizing: 'Synthetisiere Erkenntnisse',
        generating_report: 'Erstelle akademischen Bericht',
        complete: 'Abgeschlossen',
        error: 'Fehler',
        cancelled: 'Abgebrochen',
    },
    en: {
        started: 'Started',
        planning: 'Planning Research',
        searching: 'Searching academic databases',
        reading: 'Reading Papers',
        analyzing: 'Analyzing Papers',
        synthesizing: 'Synthesizing Findings',
        generating_report: 'Generating Academic Report',
        complete: 'Complete',
        error: 'Error',
        cancelled: 'Cancelled',
    },
};

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

function AcademicResearchMode({
    kbId,
    onClose,
    loadedSession,
    onSessionSaved,
    onClearSession,
    onRunningChange,
    initialTopic,
    initialPapers,
    initialPeerReviewedOnly,
    onBackToSearch,
    modeToggle,
}: AcademicResearchModeProps) {
    const { language, t } = useTheme();
    const { siteConfigs } = useAuth();
    const toast = useToast();

    // ---- State ----
    const [topic, setTopic] = useState(loadedSession?.goal || initialTopic || '');
    const [peerReviewedOnly, setPeerReviewedOnly] = useState(initialPeerReviewedOnly ?? true);
    const [isRunning, setIsRunning] = useState(false);
    useEffect(() => {
        onRunningChange?.(isRunning);
    }, [isRunning, onRunningChange]);
    const [events, setEvents] = useState<AcademicResearchProgressEvent[]>([]);
    const [findings, setFindings] = useState<AcademicFinding[]>(loadedSession?.findings || []);
    const [papers, setPapers] = useState<AcademicPaper[]>(loadedSession?.papers || initialPapers || []);
    const [report, setReport] = useState<string | null>(loadedSession?.report || null);
    const [error, setError] = useState<string | null>(null);
    const [currentStatus, setCurrentStatus] = useState<string | null>(loadedSession ? 'complete' : null);
    const [progress, setProgress] = useState({ step: loadedSession ? 10 : 0, total: 10 });
    const [selectedPaperIds, setSelectedPaperIds] = useState<Set<string>>(new Set());
    const [addingPapers, setAddingPapers] = useState(false);

    // UI toggles
    const [sectionsCollapsed, setSectionsCollapsed] = useState(!!loadedSession?.report);
    const [findingsCollapsed, setFindingsCollapsed] = useState(!!loadedSession?.report);
    const [papersExpanded, setPapersExpanded] = useState(false);
    const [isGeneratingPdf, setIsGeneratingPdf] = useState(false);
    const [isCopied, setIsCopied] = useState(false);
    const [expandedAbstracts, setExpandedAbstracts] = useState<Set<string>>(new Set());

    useEffect(() => {
        if (isCopied) {
            const timer = setTimeout(() => setIsCopied(false), 2000);
            return () => clearTimeout(timer);
        }
    }, [isCopied]);

    // ---- Refs ----
    const abortControllerRef = useRef<AbortController | null>(null);
    const progressRef = useRef<HTMLDivElement>(null);
    const reportContentRef = useRef<HTMLDivElement>(null);

    // ---- Labels ----
    const labels = language === 'en'
        ? {
            title: 'Academic Research',
            subtitle: 'Search academic papers and generate a literature review. The search is performed within JustFind and is limited to the "Articles and more" section. Only online available articles are considered.',
            peerReviewedLabel: 'Peer-reviewed only',
            topicLabel: 'Research Topic',
            topicPlaceholder: 'Enter research topic or question...',
            startButton: 'Start Academic Research',
            cancelButton: 'Cancel',
            closeButton: 'Close',
            findingsTitle: 'Findings',
            reportTitle: 'Academic Report',
            copyReport: 'Copy Markdown',
            copied: 'Copied!',
            downloadPdf: 'Download PDF',
            downloadDocx: 'Download DOCX',
            downloadBibtex: 'Export BibTeX',
            generatingPdf: 'Generating...',
            progressLabel: 'Research Progress',
            newResearch: 'New Research',
            showDetails: 'Show Details',
            hideDetails: 'Hide Details',
            showFindings: 'Show Findings',
            hideFindings: 'Hide Findings',
            papersFound: 'Papers Found',
            addSelectedToKb: 'Add Selected to Knowledge Base',
            addingPapers: 'Adding papers...',
            noPdfAvailable: 'No PDF available',
            pdfAvailable: 'PDF available',
            citations: 'citations',
            showAbstract: 'Show abstract',
            hideAbstract: 'Hide abstract',
        }
        : {
            title: 'Akademische Recherche',
            subtitle: 'Suche nach akademischen Papieren und erstelle einen Literaturüberblick. Die Suche findet innerhalb JustFind statt und bezieht sich dabei auf den Bereich "Artikel und mehr". Die Artikel müssen online verfügbar sein.',
            peerReviewedLabel: 'Nur Peer-Reviewed',
            topicLabel: 'Forschungsthema',
            topicPlaceholder: 'Forschungsthema oder Fragestellung eingeben...',
            startButton: 'Akademische Recherche starten',
            cancelButton: 'Abbrechen',
            closeButton: 'Schließen',
            findingsTitle: 'Erkenntnisse',
            reportTitle: 'Akademischer Bericht',
            copyReport: 'Markdown kopieren',
            copied: 'Kopiert!',
            downloadPdf: 'PDF herunterladen',
            downloadDocx: 'DOCX herunterladen',
            downloadBibtex: 'BibTeX exportieren',
            generatingPdf: 'Wird erstellt...',
            progressLabel: 'Recherche Fortschritt',
            newResearch: 'Neue Recherche',
            showDetails: 'Details anzeigen',
            hideDetails: 'Details ausblenden',
            showFindings: 'Erkenntnisse anzeigen',
            hideFindings: 'Erkenntnisse ausblenden',
            papersFound: 'Gefundene Paper',
            addSelectedToKb: 'Ausgewählte zur Wissensdatenbank hinzufügen',
            addingPapers: 'Papers werden hinzugefügt...',
            noPdfAvailable: 'Kein PDF verfügbar',
            pdfAvailable: 'PDF verfügbar',
            citations: 'Zitationen',
            showAbstract: 'Abstract anzeigen',
            hideAbstract: 'Abstract ausblenden',
        };

    const academicSearchName = siteConfigs?.academic_search_name || labels.title;

    // ---- Cleanup on unmount ----
    useEffect(() => {
        return () => {
            if (abortControllerRef.current) {
                abortControllerRef.current.abort();
            }
        };
    }, []);

    // ---- Auto-scroll progress ----
    useEffect(() => {
        if (progressRef.current) {
            progressRef.current.scrollTop = progressRef.current.scrollHeight;
        }
    }, [events]);

    // ---- Reset ----
    const resetResearch = () => {
        setTopic('');
        setPeerReviewedOnly(true);
        setReport(null);
        setFindings([]);
        setPapers([]);
        setEvents([]);
        setError(null);
        setCurrentStatus(null);
        setProgress({ step: 0, total: 10 });
        setIsRunning(false);
        setSectionsCollapsed(false);
        setFindingsCollapsed(false);
        setSelectedPaperIds(new Set());
        setExpandedAbstracts(new Set());
        onClearSession?.();
    };

    // ---- SSE Streaming ----
    const startResearch = async () => {
        if (!topic.trim() || isRunning) return;

        setIsRunning(true);
        setEvents([]);
        setFindings([]);
        setPapers([]);
        setReport(null);
        setError(null);
        setCurrentStatus('started');

        abortControllerRef.current = new AbortController();

        try {
            const response = await authFetch(
                `${API_BASE_URL}/api/kb/${kbId}/academic-research?stream=true`,
                {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({
                        topic: topic.trim(),
                        maxSteps: 10,
                        language,
                        peerReviewedOnly,
                        initialPapers: initialPapers && initialPapers.length > 0 ? initialPapers : undefined,
                    }),
                    signal: abortControllerRef.current.signal,
                },
            );

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            const reader = response.body?.getReader();
            if (!reader) throw new Error('No reader available');

            const decoder = new TextDecoder();
            let buffer = '';

            try {
                while (true) {
                    const { done, value } = await reader.read();
                    if (done) break;

                    buffer += decoder.decode(value, { stream: true });
                    const lines = buffer.split('\n');
                    buffer = lines.pop() || '';

                    let streamDone = false;
                    for (const line of lines) {
                        if (line.startsWith('data: ')) {
                            const data = line.slice(6);
                            if (data === '[DONE]') {
                                setIsRunning(false);
                                onSessionSaved?.();
                                streamDone = true;
                                break;
                            }

                            try {
                                const parsed = JSON.parse(data);

                                // Handle session ID event from backend
                                if (parsed.type === 'session' && parsed.sessionId) {
                                    continue;
                                }

                                const event = parsed as AcademicResearchProgressEvent;
                                setEvents(prev => [...prev, event]);
                                setCurrentStatus(event.type);
                                setProgress({ step: event.stepNumber, total: event.totalSteps });

                                if (event.findings) {
                                    setFindings(event.findings);
                                }
                                if (event.papers) {
                                    if (initialPapers && initialPapers.length > 0) {
                                        const existingDois = new Set(initialPapers.filter(p => p.doi).map(p => p.doi));
                                        const existingTitles = new Set(initialPapers.map(p => p.title.toLowerCase()));
                                        const newPapers = event.papers.filter(p =>
                                            !(p.doi && existingDois.has(p.doi)) &&
                                            !existingTitles.has(p.title.toLowerCase())
                                        );
                                        setPapers([...initialPapers, ...newPapers]);
                                    } else {
                                        setPapers(event.papers);
                                    }
                                }
                                if (event.report) {
                                    setReport(event.report);
                                    setSectionsCollapsed(true);
                                    setFindingsCollapsed(true);
                                }
                                if (event.type === 'error') {
                                    setError(event.message);
                                }
                            } catch (parseErr) {
                                console.error('Failed to parse SSE event:', parseErr, 'data:', data);
                            }
                        }
                    }
                    if (streamDone) break;
                }
            } finally {
                reader.releaseLock();
            }
        } catch (err: unknown) {
            const errorObj = err as Error;
            if (errorObj.name === 'AbortError') {
                setCurrentStatus('cancelled');
            } else {
                setError(errorObj.message || String(err));
                setCurrentStatus('error');
            }
        } finally {
            setIsRunning(false);
        }
    };

    const cancelResearch = () => {
        if (abortControllerRef.current) {
            abortControllerRef.current.abort();
        }
        setIsRunning(false);
        setCurrentStatus('cancelled');
    };

    // ---- Export Functions ----
    const copyReport = async () => {
        if (report) {
            const copied = await copyToClipboard(report);
            if (copied) {
                setIsCopied(true);
            }
        }
    };

    const exportDocx = async () => {
        if (!report) return;
        try {
            const response = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/export/docx`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ report, goal: topic }),
            });

            if (!response.ok) throw new Error('Export failed');

            const blob = await response.blob();
            const url = window.URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `Academic_Research_${new Date().toISOString().split('T')[0]}.docx`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            setTimeout(() => window.URL.revokeObjectURL(url), 0);
        } catch (e: unknown) {
            console.error(e);
            toast.error(t('docxExportFailed'));
        }
    };

    const exportBibtex = async () => {
        if (papers.length === 0) return;
        try {
            const response = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/export/academic-bibtex`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ papers }),
            });

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
        } catch (e: unknown) {
            console.error(e);
            toast.error(t('bibtexExportFailed'));
        }
    };

    const downloadPdf = useCallback(() => {
        const el = reportContentRef.current;
        if (!el || isGeneratingPdf) return;

        setIsGeneratingPdf(true);

        const iframe = document.createElement('iframe');
        iframe.style.position = 'fixed';
        iframe.style.right = '0';
        iframe.style.bottom = '0';
        iframe.style.width = '0';
        iframe.style.height = '0';
        iframe.style.border = 'none';
        document.body.appendChild(iframe);

        const doc = iframe.contentDocument || iframe.contentWindow?.document;
        if (!doc) {
            document.body.removeChild(iframe);
            setIsGeneratingPdf(false);
            return;
        }

        const title =
            topic.length > 60
                ? `Academic Research - ${topic.substring(0, 60)}...`
                : `Academic Research - ${topic}`;

        doc.open();
        doc.write(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>${title.replace(/</g, '&lt;')}</title>
<style>
    @page {
        margin: 20mm;
        size: A4;
    }
    body {
        font-family: var(--font-sans);
        color: #000;
        line-height: 1.4em;
        font-size: 11pt;
        margin: 0;
        padding: 0;
    }
    h1, h2, h3, h4, h5, h6 {
        color: #444444;
        margin-top: 1.5em;
        margin-bottom: 0.5em;
        font-weight: 600;
        page-break-after: avoid;
    }
    h1 { font-size: 18pt; }
    h2 { font-size: 15pt; }
    h3 { font-size: 13pt; }
    p { margin: 0.5em 0; }
    ul, ol { padding-left: 1.5em; margin: 0.5em 0; }
    li { margin-bottom: 0.25em; }
    code {
        background: #f2f2f2;
        padding: 0.15em 0.3em;
        border-radius: 3px;
        font-family: 'JetBrains Mono', 'SFMono-Regular', Consolas, monospace;
        font-size: 0.9em;
    }
    pre {
        background: #f2f2f2;
        color: #000;
        padding: 0.8em;
        border-radius: 6px;
        overflow-x: auto;
        margin: 0.8em 0;
        page-break-inside: avoid;
    }
    pre code { background: transparent; padding: 0; }
    blockquote {
        border-left: 3px solid #cccccc;
        margin: 0.8em 0;
        padding-left: 0.8em;
        color: #444444;
    }
    table {
        width: 100%;
        border-collapse: collapse;
        margin: 0.8em 0;
        font-size: 0.9em;
        page-break-inside: avoid;
    }
    th, td {
        padding: 0.5em 0.75em;
        border: 1px solid #cccccc;
        text-align: left;
    }
    th { background: #f2f2f2; font-weight: 600; }
    tr:nth-child(even) { background: #f9f9f9; }
    a { color: #165a97; }
    img { max-width: 100%; }
</style>
</head>
<body>${DOMPurify.sanitize(el.innerHTML)}</body>
</html>`);
        doc.close();

        setTimeout(() => {
            iframe.contentWindow?.print();
            setTimeout(() => document.body.removeChild(iframe), 100);
            setIsGeneratingPdf(false);
        }, 250);
    }, [topic, isGeneratingPdf]);

    // ---- Add Papers to KB ----
    const handleAddPapers = async () => {
        const selected = papers.filter(p => selectedPaperIds.has(p.id) && p.pdfUrl);
        if (selected.length === 0) return;
        setAddingPapers(true);
        try {
            const response = await authFetch(
                `${API_BASE_URL}/api/kb/${kbId}/academic-research/add-papers`,
                {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({ papers: selected }),
                },
            );

            if (!response.ok) throw new Error('Add papers failed');

            const result = await response.json();
            const addedCount = result.added?.length ?? 0;
            const failedCount = result.failed?.length ?? 0;

            if (addedCount > 0) {
                toast.success(`${addedCount} ${t('papersAdded')}`);
            }
            if (failedCount > 0) {
                const failedTitles = result.failed.map((f: { title: string }) => f.title).join(', ');
                toast.error(`${t('papersAddFailed')}: ${failedTitles}`);
            }

            setSelectedPaperIds(new Set());
            onSessionSaved?.();
        } catch {
            toast.error(t('papersAddFailed'));
        } finally {
            setAddingPapers(false);
        }
    };

    // ---- Paper selection helpers ----
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

    // Count of papers that have a PDF and can be added
    const selectablePaperCount = papers.filter(p => p.pdfUrl).length;
    const selectedCount = papers.filter(p => selectedPaperIds.has(p.id) && p.pdfUrl).length;

    // ---- Render ----
    return (
        <div className="research-mode-container">
            {/* Header */}
            <div className="research-mode-header">
                <div className="research-mode-header-left">
                    <h2 style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <GraduationCap size={22} />
                        {academicSearchName}
                    </h2>
                    <span className="research-mode-subtitle">{labels.subtitle}</span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                    {(report || loadedSession) && !isRunning && (
                        <button
                            className="research-mode-start-btn"
                            onClick={resetResearch}
                            style={{ fontSize: '0.85rem', padding: '6px 12px' }}
                        >
                            {labels.newResearch}
                        </button>
                    )}
                    {onBackToSearch && !isRunning && (
                        <button className="btn btn-secondary btn-sm" onClick={onBackToSearch}>
                            {language === 'en' ? 'Back to Search' : 'Zurück zur Suche'}
                        </button>
                    )}
                    <button
                        className="research-mode-close"
                        onClick={onClose}
                        aria-label={labels.closeButton}
                    >
                        <X size={18} />
                    </button>
                </div>
            </div>

            {/* Mode Toggle */}
            {modeToggle}

            {/* Collapsible Details Section (Topic + Progress) */}
            {report && (
                <button
                    className="research-mode-accordion-toggle"
                    onClick={() => setSectionsCollapsed(!sectionsCollapsed)}
                    aria-expanded={!sectionsCollapsed}
                >
                    <span className="research-mode-accordion-icon">
                        {sectionsCollapsed ? '\u25B6' : '\u25BC'}
                    </span>
                    <span>{sectionsCollapsed ? labels.showDetails : labels.hideDetails}</span>
                    {sectionsCollapsed && (
                        <span className="research-mode-accordion-summary">
                            {topic.length > 60 ? `"${topic.substring(0, 60)}..."` : `"${topic}"`}
                        </span>
                    )}
                </button>
            )}

            {/* Topic Input */}
            <div className={`research-mode-input-section ${sectionsCollapsed ? 'collapsed' : ''}`}>
                <label htmlFor="academic-research-topic">{labels.topicLabel}</label>
                <textarea
                    id="academic-research-topic"
                    value={topic}
                    onChange={e => setTopic(e.target.value)}
                    placeholder={labels.topicPlaceholder}
                    disabled={isRunning || !!report}
                    rows={3}
                />
                <div className="research-mode-actions" style={{ display: 'flex', alignItems: 'center', gap: '12px', flexWrap: 'wrap' }}>
                    <label
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '6px',
                            cursor: isRunning || !!report ? 'default' : 'pointer',
                            opacity: isRunning || !!report ? 0.5 : 1,
                            fontSize: '0.85rem',
                        }}
                    >
                        <input
                            type="checkbox"
                            checked={peerReviewedOnly}
                            onChange={e => setPeerReviewedOnly(e.target.checked)}
                            disabled={isRunning || !!report}
                        />
                        {labels.peerReviewedLabel}
                    </label>
                    {!isRunning ? (
                        <button
                            className="research-mode-start-btn"
                            onClick={startResearch}
                            disabled={!topic.trim()}
                            style={{ display: 'flex', alignItems: 'center', gap: '6px' }}
                        >
                            <GraduationCap size={16} />
                            {labels.startButton}
                        </button>
                    ) : (
                        <button className="research-mode-cancel-btn" onClick={cancelResearch}>
                            {labels.cancelButton}
                        </button>
                    )}
                </div>
            </div>

            {/* Progress Section */}
            {(isRunning || events.length > 0) && (
                <div
                    className={`research-mode-progress-section ${sectionsCollapsed ? 'collapsed' : ''}`}
                >
                    {/* Progress Bar */}
                    <div
                        className="research-mode-progress-bar"
                        role="progressbar"
                        aria-valuenow={progress.step}
                        aria-valuemin={0}
                        aria-valuemax={progress.total}
                        aria-label={labels.progressLabel}
                    >
                        <div
                            className="research-mode-progress-fill"
                            style={{ width: `${(progress.step / progress.total) * 100}%` }}
                        />
                    </div>

                    {/* Current Status */}
                    {currentStatus && (
                        <div
                            className={`research-mode-status research-mode-status-${currentStatus}`}
                            aria-live="polite"
                        >
                            <span className="research-mode-status-icon">
                                {(() => {
                                    const Icon = STATUS_ICONS[currentStatus];
                                    return Icon
                                        ? <Icon size={18} aria-hidden="true" />
                                        : <Loader2 size={18} className="spin" aria-hidden="true" />;
                                })()}
                            </span>
                            <span className="research-mode-status-text">
                                {STATUS_LABELS[language]?.[currentStatus] || currentStatus}
                            </span>
                            <span className="research-mode-status-step">
                                ({progress.step}/{progress.total})
                            </span>
                        </div>
                    )}

                    {/* Event Log */}
                    <div
                        className="research-mode-event-log"
                        ref={progressRef}
                        role="log"
                        aria-live="polite"
                    >
                        {events.map((event, index) => {
                            const EventIcon = STATUS_ICONS[event.type] || Circle;
                            return (
                                <div
                                    key={index}
                                    className={`research-mode-event research-mode-event-${event.type}`}
                                >
                                    <span className="research-mode-event-icon">
                                        <EventIcon size={14} aria-hidden="true" />
                                    </span>
                                    <span className="research-mode-event-message">{event.message}</span>
                                </div>
                            );
                        })}
                    </div>
                </div>
            )}

            {/* Error Display */}
            {error && (
                <div className="research-mode-error">
                    <span className="research-mode-error-icon">{'\u274C'}</span>
                    <span>{error}</span>
                </div>
            )}

            {/* Final Report */}
            {report && (
                <div className="research-mode-report-section">
                    <div className="research-mode-report-header">
                        <h3>{labels.reportTitle}</h3>
                        <div className="research-mode-report-actions">
                            <button
                                className="research-mode-copy-btn"
                                onClick={copyReport}
                                disabled={isCopied}
                                title={labels.copyReport}
                            >
                                <Copy size={14} />
                                {isCopied ? labels.copied : labels.copyReport}
                            </button>
                            <button
                                className="research-mode-copy-btn"
                                onClick={downloadPdf}
                                disabled={isGeneratingPdf}
                                title={labels.downloadPdf}
                            >
                                <FileText size={14} />
                                {isGeneratingPdf ? labels.generatingPdf : labels.downloadPdf}
                            </button>
                            <button
                                className="research-mode-copy-btn"
                                onClick={exportDocx}
                                title={labels.downloadDocx}
                            >
                                <FileDown size={14} />
                                {labels.downloadDocx}
                            </button>
                            <button
                                className="research-mode-copy-btn"
                                onClick={exportBibtex}
                                title={labels.downloadBibtex}
                            >
                                <Download size={14} />
                                {labels.downloadBibtex}
                            </button>
                        </div>
                    </div>
                    <div
                        className="research-mode-report-content markdown-content"
                        ref={reportContentRef}
                    >
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>{report}</ReactMarkdown>
                    </div>
                </div>
            )}

            {/* Findings Section */}
            {findings.length > 0 && (
                <>
                    <button
                        className="research-mode-accordion-toggle"
                        onClick={() => setFindingsCollapsed(!findingsCollapsed)}
                        aria-expanded={!findingsCollapsed}
                        style={{ borderTop: 'none' }}
                    >
                        <span className="research-mode-accordion-icon">
                            {findingsCollapsed ? '\u25B6' : '\u25BC'}
                        </span>
                        <span>
                            {findingsCollapsed ? labels.showFindings : labels.hideFindings}
                        </span>
                        <span className="research-mode-accordion-summary">
                            ({findings.length})
                        </span>
                    </button>
                    <div
                        className={`research-mode-findings-section ${findingsCollapsed ? 'collapsed' : ''}`}
                    >
                        <h3>
                            {labels.findingsTitle} ({findings.length})
                        </h3>
                        <div className="research-mode-findings-list">
                            {findings.map((finding, index) => (
                                <div key={index} className="research-mode-finding">
                                    <p className="research-mode-finding-content">{finding.content}</p>
                                    <div className="research-mode-finding-sources">
                                        {finding.papers.map((paper, pIndex) => (
                                            <span key={pIndex} className="research-mode-finding-source">
                                                {'\u{1F4C4}'} {paper.harvardCitation || paper.title}
                                            </span>
                                        ))}
                                    </div>
                                </div>
                            ))}
                        </div>
                    </div>
                </>
            )}

            {/* Papers Panel */}
            {papers.length > 0 && (
                <div
                    style={{
                        marginTop: '16px',
                        border: '1px solid var(--border-color)',
                        borderRadius: '8px',
                        overflow: 'hidden',
                    }}
                >
                    {/* Papers Header */}
                    <button
                        onClick={() => setPapersExpanded(!papersExpanded)}
                        aria-expanded={papersExpanded}
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '8px',
                            width: '100%',
                            padding: '12px 16px',
                            background: 'var(--bg-secondary)',
                            border: 'none',
                            cursor: 'pointer',
                            color: 'var(--text-primary)',
                            fontSize: '0.95rem',
                            fontWeight: 600,
                        }}
                    >
                        {papersExpanded ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
                        <GraduationCap size={16} />
                        <span>
                            {labels.papersFound} ({papers.length})
                        </span>
                    </button>

                    {/* Papers List */}
                    {papersExpanded && (
                        <div style={{ padding: '8px 0', maxHeight: '400px', overflowY: 'auto' }}>
                            {papers.map(paper => {
                                const isSelected = selectedPaperIds.has(paper.id);
                                const hasPdf = !!paper.pdfUrl;
                                const abstractExpanded = expandedAbstracts.has(paper.id);
                                const abstractSnippet =
                                    paper.abstract && paper.abstract.length > 150
                                        ? paper.abstract.substring(0, 150) + '...'
                                        : paper.abstract;

                                return (
                                    <div
                                        key={paper.id}
                                        style={{
                                            display: 'flex',
                                            alignItems: 'flex-start',
                                            gap: '10px',
                                            padding: '10px 16px',
                                            borderBottom: '1px solid var(--border-color)',
                                        }}
                                    >
                                        {/* Checkbox */}
                                        <button
                                            onClick={() => hasPdf && togglePaperSelection(paper.id)}
                                            disabled={!hasPdf}
                                            aria-label={
                                                isSelected
                                                    ? `Deselect ${paper.title}`
                                                    : `Select ${paper.title}`
                                            }
                                            style={{
                                                background: 'none',
                                                border: 'none',
                                                cursor: hasPdf ? 'pointer' : 'not-allowed',
                                                padding: '2px',
                                                marginTop: '2px',
                                                color: hasPdf
                                                    ? 'var(--accent-primary)'
                                                    : 'var(--text-secondary)',
                                                opacity: hasPdf ? 1 : 0.4,
                                                flexShrink: 0,
                                            }}
                                        >
                                            {isSelected ? (
                                                <CheckSquare size={18} />
                                            ) : (
                                                <Square size={18} />
                                            )}
                                        </button>

                                        {/* Paper Info */}
                                        <div style={{ flex: 1, minWidth: 0 }}>
                                            {/* Title */}
                                            <div style={{ marginBottom: '4px' }}>
                                                <a
                                                    href={paper.url}
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
                                                    <ExternalLink
                                                        size={12}
                                                        style={{ flexShrink: 0 }}
                                                    />
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
                                                {paper.journal ? ` - ${paper.journal}` : ''}
                                            </div>

                                            {/* Badges row */}
                                            <div
                                                style={{
                                                    display: 'flex',
                                                    alignItems: 'center',
                                                    gap: '8px',
                                                    flexWrap: 'wrap',
                                                    marginBottom: paper.abstract ? '4px' : '0',
                                                }}
                                            >
                                                {/* Citation count badge */}
                                                {paper.citationCount != null &&
                                                    paper.citationCount > 0 && (
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
                                                            fontSize: '0.8rem',
                                                            color: 'var(--text-secondary)',
                                                        }}
                                                    >
                                                        <span
                                                            style={{
                                                                fontSize: '0.75rem',
                                                                color: 'var(--accent-primary)',
                                                                cursor: 'pointer',
                                                            }}
                                                        >
                                                            {abstractExpanded
                                                                ? labels.hideAbstract
                                                                : labels.showAbstract}
                                                        </span>
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
                                    </div>
                                );
                            })}

                            {/* Add to KB Button */}
                            <div
                                style={{
                                    padding: '12px 16px',
                                    display: 'flex',
                                    justifyContent: 'flex-end',
                                }}
                            >
                                <button
                                    className="research-mode-start-btn"
                                    onClick={handleAddPapers}
                                    disabled={selectedCount === 0 || addingPapers}
                                    style={{
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: '6px',
                                        fontSize: '0.85rem',
                                        padding: '8px 16px',
                                    }}
                                >
                                    {addingPapers ? (
                                        <>
                                            <Loader2 size={14} className="animate-spin" />
                                            {labels.addingPapers}
                                        </>
                                    ) : (
                                        <>
                                            <Download size={14} />
                                            {labels.addSelectedToKb}
                                            {selectedCount > 0 && selectablePaperCount > 0 && (
                                                <span>
                                                    ({selectedCount}/{selectablePaperCount})
                                                </span>
                                            )}
                                        </>
                                    )}
                                </button>
                            </div>
                        </div>
                    )}
                </div>
            )}
        </div>
    );
}

export default AcademicResearchMode;
