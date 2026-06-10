/**
 * Research Mode Component
 * 
 * A dedicated interface for the multi-step research agent.
 * Shows research progress, findings, and final report.
 */

import { useState, useRef, useEffect, useCallback } from 'react';
import { API_BASE_URL, authFetch } from '../api';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import { copyToClipboard } from '../utils/clipboard';
import DOMPurify from 'dompurify';

interface ResearchProgressEvent {
    type: 'started' | 'planning' | 'searching' | 'reading' | 'synthesizing' | 'refining' | 'generating_report' | 'complete' | 'error' | 'cancelled';
    message: string;
    stepNumber: number;
    totalSteps: number;
    findings?: Finding[];
    plan?: PlanStep[];
    report?: string;
    timestamp: number;
}

interface Finding {
    content: string;
    sources: SourceRef[];
    relevanceScore: number;
}

interface SourceRef {
    fileName: string;
    fileId: string;
    pages?: number[];
}

interface PlanStep {
    id: string;
    action: string;
    query?: string;
    status: string;
    result?: string;
}

interface ResearchModeProps {
    kbId: string;
    onClose: () => void;
    loadedSession?: { id: string; goal: string; report: string; findings: Finding[] } | null;
    onSessionSaved?: () => void;
    onClearSession?: () => void;
    onRunningChange?: (running: boolean) => void;
}

const STATUS_ICONS: Record<string, string> = {
    started: '🚀',
    planning: '📋',
    searching: '🔍',
    reading: '📖',
    synthesizing: '🧠',
    refining: '🔄',
    generating_report: '📝',
    complete: '✅',
    error: '❌',
    cancelled: '🛑',
};

const STATUS_LABELS: Record<string, Record<string, string>> = {
    de: {
        started: 'Gestartet',
        planning: 'Plane Recherche',
        searching: 'Durchsuche Wissensbasis',
        reading: 'Lese Dokumente',
        synthesizing: 'Synthetisiere Erkenntnisse',
        refining: 'Verfeinere Strategie',
        generating_report: 'Erstelle Bericht',
        complete: 'Abgeschlossen',
        error: 'Fehler',
        cancelled: 'Abgebrochen',
    },
    en: {
        started: 'Started',
        planning: 'Planning Research',
        searching: 'Searching Knowledge Base',
        reading: 'Reading Documents',
        synthesizing: 'Synthesizing Findings',
        refining: 'Refining Strategy',
        generating_report: 'Generating Report',
        complete: 'Complete',
        error: 'Error',
        cancelled: 'Cancelled',
    },
};

export default function ResearchMode({ kbId, onClose, loadedSession, onSessionSaved, onClearSession, onRunningChange }: ResearchModeProps) {
    const { language, t } = useTheme();
    const toast = useToast();
    const [goal, setGoal] = useState(loadedSession?.goal || '');
    const [isRunning, setIsRunning] = useState(false);
    useEffect(() => {
        onRunningChange?.(isRunning);
    }, [isRunning, onRunningChange]);
    const [events, setEvents] = useState<ResearchProgressEvent[]>([]);
    const [findings, setFindings] = useState<Finding[]>(loadedSession?.findings || []);
    const [report, setReport] = useState<string | null>(loadedSession?.report || null);
    const [error, setError] = useState<string | null>(null);
    const [currentStatus, setCurrentStatus] = useState<string | null>(loadedSession ? 'complete' : null);
    const [progress, setProgress] = useState({ step: loadedSession ? 10 : 0, total: 10 });
    const [sectionsCollapsed, setSectionsCollapsed] = useState(!!loadedSession?.report);
    const [findingsCollapsed, setFindingsCollapsed] = useState(!!loadedSession?.report);
    const [isGeneratingPdf, setIsGeneratingPdf] = useState(false);
    const [isCopied, setIsCopied] = useState(false);

    useEffect(() => {
        if (isCopied) {
            const timer = setTimeout(() => setIsCopied(false), 2000);
            return () => clearTimeout(timer);
        }
    }, [isCopied]);

    const abortControllerRef = useRef<AbortController | null>(null);
    const eventSourceRef = useRef<EventSource | null>(null);
    const progressRef = useRef<HTMLDivElement>(null);
    const reportContentRef = useRef<HTMLDivElement>(null);

    const labels = language === 'en' ? {
        title: 'Report creation',
        subtitle: 'Deep, autonomous research with multi-step reasoning exclusively in the sources',
        goalLabel: 'Research Goal',
        goalPlaceholder: 'What would you like to research? (e.g., "What are the main causes of...")',
        startButton: 'Start Research',
        cancelButton: 'Cancel',
        closeButton: 'Close',
        findingsTitle: 'Findings',
        reportTitle: 'Research Report',
        noFindings: 'No findings yet...',
        copyReport: 'Copy Report in Markdown',
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
    } : {
        title: 'Bericht erstellen',
        subtitle: 'Tiefe, autonome Recherche mit mehrstufigem Denken ausschließlich in den Quellen',
        goalLabel: 'Forschungsziel',
        goalPlaceholder: 'Was möchten Sie recherchieren? (z.B. "Was sind die Hauptursachen für...")',
        startButton: 'Recherche starten',
        cancelButton: 'Abbrechen',
        closeButton: 'Schließen',
        findingsTitle: 'Erkenntnisse',
        reportTitle: 'Forschungsbericht',
        noFindings: 'Noch keine Erkenntnisse...',
        copyReport: 'Bericht kopieren in Markdown',
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
    };

    // Cleanup on unmount. The ref objects (not their .current values) are
    // captured up front; .current must still be read at cleanup time because
    // the controller/source are created after mount.
    useEffect(() => {
        const abortRef = abortControllerRef;
        const sourceRef = eventSourceRef;
        return () => {
            if (abortRef.current) {
                abortRef.current.abort();
            }
            if (sourceRef.current) {
                sourceRef.current.close();
            }
        };
    }, []);

    const resetResearch = () => {
        setGoal('');
        setReport(null);
        setFindings([]);
        setEvents([]);
        setError(null);
        setCurrentStatus(null);
        setProgress({ step: 0, total: 10 });
        setIsRunning(false);
        setSectionsCollapsed(false);
        setFindingsCollapsed(false);
        onClearSession?.();
    };

    // Auto-scroll progress
    useEffect(() => {
        if (progressRef.current) {
            progressRef.current.scrollTop = progressRef.current.scrollHeight;
        }
    }, [events]);

    const startResearch = async () => {
        if (!goal.trim() || isRunning) return;

        setIsRunning(true);
        setEvents([]);
        setFindings([]);
        setReport(null);
        setError(null);
        setCurrentStatus('started');

        abortControllerRef.current = new AbortController();

        try {

            const response = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/research?stream=true`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    goal: goal.trim(),
                    maxSteps: 10,
                    language,
                }),
                signal: abortControllerRef.current.signal,
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            const reader = response.body?.getReader();
            if (!reader) throw new Error('No reader available');

            const decoder = new TextDecoder();
            let buffer = '';

            while (true) {
                const { done, value } = await reader.read();
                if (done) break;

                buffer += decoder.decode(value, { stream: true });
                const lines = buffer.split('\n');
                buffer = lines.pop() || '';

                for (const line of lines) {
                    if (line.startsWith('data: ')) {
                        const data = line.slice(6);
                        if (data === '[DONE]') {
                            setIsRunning(false);
                            onSessionSaved?.();
                            break;
                        }

                        try {
                            const parsed = JSON.parse(data);

                            // Handle session ID event from backend
                            if (parsed.type === 'session' && parsed.sessionId) {
                                continue;
                            }

                            const event = parsed as ResearchProgressEvent;
                            setEvents(prev => [...prev, event]);
                            setCurrentStatus(event.type);
                            setProgress({ step: event.stepNumber, total: event.totalSteps });

                            if (event.findings) {
                                setFindings(event.findings);
                            }
                            if (event.report) {
                                setReport(event.report);
                                // Auto-collapse sections when report is ready
                                setSectionsCollapsed(true);
                                setFindingsCollapsed(true);
                            }
                            if (event.type === 'error') {
                                setError(event.message);
                            }
                        } catch {
                            // Ignore parse errors for malformed events
                        }
                    }
                }
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
                body: JSON.stringify({ report, goal }),
            });

            if (!response.ok) throw new Error('Export failed');

            const blob = await response.blob();
            const url = window.URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `Research_${new Date().toISOString().split('T')[0]}.docx`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            window.URL.revokeObjectURL(url);
        } catch (e: unknown) {
            console.error(e);
            toast.error(t('docxExportFailed'));
        }
    };

    const exportBibtex = async () => {
        if (findings.length === 0) return;
        try {

            const response = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/export/bibtex`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ findings }),
            });

            if (!response.ok) throw new Error('Export failed');

            const blob = await response.blob();
            const url = window.URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `citations_${new Date().toISOString().split('T')[0]}.bib`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            window.URL.revokeObjectURL(url);
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

        const title = goal.length > 60
            ? `Research Report - ${goal.substring(0, 60)}...`
            : `Research Report - ${goal}`;

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
        font-family: Verdana, Helvetica, Arial, sans-serif;
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

        // Allow content to render, then trigger print dialog.
        // print() is blocking — JS pauses until the dialog is closed.
        setTimeout(() => {
            iframe.contentWindow?.print();
            setTimeout(() => document.body.removeChild(iframe), 100);
            setIsGeneratingPdf(false);
        }, 250);
    }, [goal, isGeneratingPdf]);

    return (
        <div className="research-mode-container">
            {/* Header */}
            <div className="research-mode-header">
                <div className="research-mode-header-left">
                    <h2>{labels.title}</h2>
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
                    <button
                        className="research-mode-close"
                        onClick={onClose}
                        aria-label={labels.closeButton}
                    >
                        ✕
                    </button>
                </div>
            </div>

            {/* Collapsible Details Section (Goal + Progress) */}
            {report && (
                <button
                    className="research-mode-accordion-toggle"
                    onClick={() => setSectionsCollapsed(!sectionsCollapsed)}
                    aria-expanded={!sectionsCollapsed}
                >
                    <span className="research-mode-accordion-icon">
                        {sectionsCollapsed ? '▶' : '▼'}
                    </span>
                    <span>{sectionsCollapsed ? labels.showDetails : labels.hideDetails}</span>
                    {sectionsCollapsed && (
                        <span className="research-mode-accordion-summary">
                            {goal.length > 60 ? `"${goal.substring(0, 60)}..."` : `"${goal}"`}
                        </span>
                    )}
                </button>
            )}

            {/* Goal Input */}
            <div className={`research-mode-input-section ${sectionsCollapsed ? 'collapsed' : ''}`}>
                <label htmlFor="research-goal">{labels.goalLabel}</label>
                <textarea
                    id="research-goal"
                    value={goal}
                    onChange={(e) => setGoal(e.target.value)}
                    placeholder={labels.goalPlaceholder}
                    disabled={isRunning || !!report}
                    rows={3}
                />
                <div className="research-mode-actions">
                    {!isRunning ? (
                        <button
                            className="research-mode-start-btn"
                            onClick={startResearch}
                            disabled={!goal.trim()}
                        >
                            {labels.startButton}
                        </button>
                    ) : (
                        <button
                            className="research-mode-cancel-btn"
                            onClick={cancelResearch}
                        >
                            {labels.cancelButton}
                        </button>
                    )}
                </div>
            </div>

            {/* Progress Section */}
            {(isRunning || events.length > 0) && (
                <div className={`research-mode-progress-section ${sectionsCollapsed ? 'collapsed' : ''}`}>
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
                                {STATUS_ICONS[currentStatus] || '⏳'}
                            </span>
                            <span className="research-mode-status-text">
                                {STATUS_LABELS[language][currentStatus] || currentStatus}
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
                        {events.map((event, index) => (
                            <div key={index} className={`research-mode-event research-mode-event-${event.type}`}>
                                <span className="research-mode-event-icon">
                                    {STATUS_ICONS[event.type] || '•'}
                                </span>
                                <span className="research-mode-event-message">
                                    {event.message}
                                </span>
                            </div>
                        ))}
                    </div>
                </div>
            )}

            {/* Error Display */}
            {error && (
                <div className="research-mode-error">
                    <span className="research-mode-error-icon">❌</span>
                    <span>{error}</span>
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
                            {findingsCollapsed ? '▶' : '▼'}
                        </span>
                        <span>{findingsCollapsed ? labels.showFindings : labels.hideFindings}</span>
                        <span className="research-mode-accordion-summary">
                            ({findings.length})
                        </span>
                    </button>
                    <div className={`research-mode-findings-section ${findingsCollapsed ? 'collapsed' : ''}`}>
                        <h3>{labels.findingsTitle} ({findings.length})</h3>
                        <div className="research-mode-findings-list">
                            {findings.map((finding, index) => (
                                <div key={index} className="research-mode-finding">
                                    <p className="research-mode-finding-content">{finding.content}</p>
                                    <div className="research-mode-finding-sources">
                                        {finding.sources.map((source, sIndex) => (
                                            <span key={sIndex} className="research-mode-finding-source">
                                                📄 {source.fileName}
                                                {source.pages?.length ? ` (p. ${source.pages.join('-')})` : ''}
                                            </span>
                                        ))}
                                    </div>
                                </div>
                            ))}
                        </div>
                    </div>
                </>
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
                            >
                                {isCopied ? labels.copied : labels.copyReport}
                            </button>
                            <button
                                className="research-mode-copy-btn"
                                onClick={downloadPdf}
                                disabled={isGeneratingPdf}
                            >
                                {isGeneratingPdf ? labels.generatingPdf : labels.downloadPdf}
                            </button>
                            <button
                                className="research-mode-copy-btn"
                                onClick={exportDocx}
                            >
                                {labels.downloadDocx}
                            </button>
                            <button
                                className="research-mode-copy-btn"
                                onClick={exportBibtex}
                            >
                                {labels.downloadBibtex}
                            </button>
                        </div>
                    </div>
                    <div className="research-mode-report-content markdown-content" ref={reportContentRef}>
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>
                            {report}
                        </ReactMarkdown>
                    </div>
                </div>
            )}
        </div>
    );
}
