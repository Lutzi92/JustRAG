import React, { useState, useEffect, useRef } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import axios from 'axios';
import {
    Minimize2,
    Type, Layout, Mic, Check,
    FileText, Loader2, Newspaper, HelpCircle, GraduationCap, Clock, ListChecks, BarChart3, Scale
} from 'lucide-react';
import type { GeneratedContent } from '../../types';
import { isMarkdownArtifact } from '../../utils/artifactTypes';
import { QuizView } from './QuizView';
import { FlashcardsArtifact } from './FlashcardsArtifact';
import { PresentationArtifact } from './PresentationArtifact';
import { PodcastArtifact } from './PodcastArtifact';
import { ChartArtifact } from './ChartArtifact';
import { API_BASE_URL } from '../../api';
import { MarkdownEditor } from './MarkdownEditor';
import { WorkspacePromptDialog } from './WorkspacePromptDialog';
import { ComparisonDialog } from './ComparisonDialog';
import { useWorkspacePresets } from '../../hooks/useWorkspacePresets';
import type { AgentSelection } from '../../hooks/useKbSettings';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import { copyToClipboard } from '../../utils/clipboard';

interface StudioWorkspaceProps {
    kbId: string;
    generatedContent: GeneratedContent[];
    onGenerate: (type: 'cards' | 'presentation' | 'podcast' | 'chart' | 'abstract' | 'briefing_doc' | 'faq' | 'study_guide' | 'timeline' | 'quiz') => void;
    onDeleteContent: (id: string, e: React.MouseEvent) => void;
    onClose: () => void;
    selectedItem: GeneratedContent | null;
    hasFiles?: boolean;
    /** Called after a successful "New analysis" run with the created record;
     * the caller owns reloading the artifact list and opening the item. */
    onAnalysisCreated?: (item: GeneratedContent) => void;
    /** Called when the "Document comparison" tile's dialog is submitted; the
     * caller owns starting the comparison chat turn (`chat.startComparison`)
     * and switching to the chat view. Resolves to whether the turn actually
     * started — `false` on an upload failure (413/415/422/503), in which case
     * the dialog stays open instead of closing on a silent failure. */
    onStartComparison?: (v: { file: File; modes: string[]; instruction: string; agentSelection: AgentSelection }) => Promise<boolean> | boolean | void;
}

// `generatedContent` and `onDeleteContent` are accepted for interface
// stability with callers (the history panel now owns both the artifact list
// and its delete affordance) but are not consumed inside this component.
export const StudioWorkspace: React.FC<StudioWorkspaceProps> = ({
    kbId,
    onGenerate: onGenerateParent,
    onClose,
    selectedItem,
    hasFiles = true,
    onAnalysisCreated,
    onStartComparison,
}) => {
    const { t, language } = useTheme();
    const toast = useToast();
    const [editorContent, setEditorContent] = useState('');
    const [showComparison, setShowComparison] = useState(false);
    const [isComparing, setIsComparing] = useState(false);

    // When selection changes, update editor content if it's an analysis or abstract
    useEffect(() => {
        if (selectedItem && isMarkdownArtifact(selectedItem.type)) {
            setEditorContent((selectedItem.content as { text?: string }).text || '');
        }
    }, [selectedItem]);

    // Analysis State
    const [isAnalyzing, setIsAnalyzing] = useState(false);
    const [showAnalysisModal, setShowAnalysisModal] = useState(false);
    const [saveStatus, setSaveStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
    const presets = useWorkspacePresets(kbId, language || 'de');

    const handleAnalyzeRequest = () => {
        setShowAnalysisModal(true);
    };

    // Fail-soft hint: the backend still produces an analysis even when the
    // requested agent/team could no longer be used, and reports why via
    // `degradedReason`. It is bound to the id of the artifact it describes
    // (`degradedForId`), not shown unconditionally, so that:
    //  - selecting the freshly created artifact (which `onAnalysisCreated`
    //    drives from the parent, landing here as a `selectedItem` prop change
    //    in the same update as the reason itself) does NOT wipe the hint —
    //    without the id check, the effect below would race the prop change
    //    and clear the hint before it is ever seen;
    //  - navigating to any OTHER artifact afterwards does clear it, so the
    //    warning can never end up attached to an unrelated item.
    const [degradedReason, setDegradedReason] = useState('');
    const degradedForId = useRef<string | null>(null);
    useEffect(() => {
        if ((selectedItem?.id ?? null) !== degradedForId.current) {
            setDegradedReason('');
        }
    }, [selectedItem?.id]);

    const runAnalysis = async ({ prompt, agentSelection }: { prompt: string; agentSelection: AgentSelection }) => {
        setIsAnalyzing(true);
        setShowAnalysisModal(false);
        setDegradedReason('');
        degradedForId.current = null;
        try {
            const res = await axios.post(`${API_BASE_URL}/api/kb/${kbId}/generate/analysis`, {
                topic: prompt,
                language: language || 'de',
                ...(agentSelection.teamId ? { teamId: agentSelection.teamId } : {}),
                ...(agentSelection.agentId ? { agentId: agentSelection.agentId } : {}),
            });
            const record = res.data?.record;
            if (!record?.id) {
                toast.error(t('analysisError'));
                return;
            }
            const reason = res.data?.degradedReason || '';
            setDegradedReason(reason);
            degradedForId.current = reason ? record.id : null;
            onAnalysisCreated?.(record);
        } catch (err: unknown) {
            console.error('Analysis failed:', err);
            const axiosErr = err as { response?: { data?: { error?: string } } };
            toast.error(axiosErr.response?.data?.error || t('analysisError'));
        } finally {
            setIsAnalyzing(false);
        }
    };

    const handleSave = async () => {
        if (!selectedItem || !isMarkdownArtifact(selectedItem.type)) return;

        setSaveStatus('saving');
        try {
            await axios.patch(`${API_BASE_URL}/api/generated-content/${selectedItem.id}`, {
                content: { ...selectedItem.content, text: editorContent }
            });

            setSaveStatus('saved');
            setTimeout(() => setSaveStatus('idle'), 2000);
        } catch (err: unknown) {
            console.error('Save failed:', err);
            toast.error(t('saveError'));
            setSaveStatus('error');
            setTimeout(() => setSaveStatus('idle'), 3000);
        }
    };

    const [isCopied, setIsCopied] = useState(false);
    const [isGeneratingPdf, setIsGeneratingPdf] = useState(false);

    useEffect(() => {
        if (isCopied) {
            const timer = setTimeout(() => setIsCopied(false), 2000);
            return () => clearTimeout(timer);
        }
    }, [isCopied]);

    const handleCopyToClipboard = async () => {
        if (selectedItem && isMarkdownArtifact(selectedItem.type) && editorContent) {
            const copied = await copyToClipboard(editorContent);
            if (copied) {
                setIsCopied(true);
            }
        } else if (selectedItem) {
            const copied = await copyToClipboard(JSON.stringify(selectedItem.content, null, 2));
            if (copied) {
                setIsCopied(true);
            }
        }
    };

    const exportDocx = async () => {
        if (!selectedItem) return;
        try {
            const contentToExport = isMarkdownArtifact(selectedItem.type) ? editorContent : JSON.stringify(selectedItem.content, null, 2);

            // Matches ResearchMode.tsx: body: JSON.stringify({ report, goal })
            // Axios automatically stringifies the body, so we pass the object directly.
            const response = await axios.post(`${API_BASE_URL}/api/kb/${kbId}/export/docx`, {
                report: contentToExport,
                goal: selectedItem.title
            }, {
                responseType: 'blob'
            });

            const url = window.URL.createObjectURL(new Blob([response.data]));
            const a = document.createElement('a');
            a.href = url;
            a.download = `${selectedItem.title.replace(/[^a-z0-9]/gi, '_').toLowerCase()}.docx`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            window.URL.revokeObjectURL(url);
        } catch (e: unknown) {
            console.error(e);
            toast.error(t('exportFailed') || 'Export failed');
        }
    };

    const downloadPdf = () => {
        if (!selectedItem || isGeneratingPdf) return;
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

        const title = selectedItem.title;
        let contentHtml: string;

        if (isMarkdownArtifact(selectedItem.type)) {
            // Render markdown to HTML with proper structure
            contentHtml = renderToStaticMarkup(
                <div className="markdown-content">
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>
                        {editorContent}
                    </ReactMarkdown>
                </div>
            );
        } else {
            contentHtml = `<pre>${JSON.stringify(selectedItem.content, null, 2)}</pre>`;
        }

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
<body><h1>${title}</h1>${contentHtml}</body>
</html>`);
        doc.close();

        setTimeout(() => {
            iframe.contentWindow?.print();
            setTimeout(() => document.body.removeChild(iframe), 100);
            setIsGeneratingPdf(false);
        }, 500);
    };

    return (
        <div className="studio-workspace" style={{
            display: 'flex',
            flexDirection: 'column',
            height: '100%',
            background: 'var(--bg-primary)',
            position: 'relative',
            overflow: 'hidden'
        }}>
            <div className="studio-workspace__tiles" style={{
                padding: '1rem',
                display: 'flex',
                flexWrap: 'wrap',
                gap: '0.5rem',
                borderBottom: '1px solid var(--border-color)',
                background: 'var(--bg-secondary)',
                opacity: hasFiles ? 1 : 0.4
            }}
                 title={!hasFiles ? t('sourcesRequiredHint') : undefined}
            >
                <button onClick={handleAnalyzeRequest} disabled={!hasFiles || isAnalyzing} className="studio-gen-btn">
                    <FileText size={16} /> {t('newAnalysis')}
                </button>
                {presets.compareEnabled && (
                    <button onClick={() => setShowComparison(true)} disabled={!hasFiles} className="studio-gen-btn">
                        <Scale size={16} /> {t('documentComparison')}
                    </button>
                )}
                <button onClick={() => onGenerateParent('cards')} disabled={!hasFiles} className="studio-gen-btn">
                    <Type size={16} /> {t('flashcards')}
                </button>
                <button onClick={() => onGenerateParent('presentation')} disabled={!hasFiles} className="studio-gen-btn">
                    <Layout size={16} /> {t('slides')}
                </button>
                <button onClick={() => onGenerateParent('podcast')} disabled={!hasFiles} className="studio-gen-btn">
                    <Mic size={16} /> {t('podcast')}
                </button>

                <button onClick={() => onGenerateParent('abstract')} disabled={!hasFiles} className="studio-gen-btn">
                    <FileText size={16} /> {t('abstract')}
                </button>
                <button onClick={() => onGenerateParent('chart')} disabled={!hasFiles} className="studio-gen-btn">
                    <BarChart3 size={16} /> {t('chart')}
                </button>

                <button onClick={() => onGenerateParent('briefing_doc')} disabled={!hasFiles} className="studio-gen-btn">
                    <Newspaper size={16} /> {t('briefingDoc')}
                </button>
                <button onClick={() => onGenerateParent('faq')} disabled={!hasFiles} className="studio-gen-btn">
                    <HelpCircle size={16} /> {t('faq')}
                </button>
                <button onClick={() => onGenerateParent('study_guide')} disabled={!hasFiles} className="studio-gen-btn">
                    <GraduationCap size={16} /> {t('studyGuide')}
                </button>
                <button onClick={() => onGenerateParent('timeline')} disabled={!hasFiles} className="studio-gen-btn">
                    <Clock size={16} /> {t('timeline')}
                </button>
                <button onClick={() => onGenerateParent('quiz')} disabled={!hasFiles} className="studio-gen-btn">
                    <ListChecks size={16} /> {t('quiz')}
                </button>
            </div>

            {/* Main Area */}
            <div className="studio-workspace__body" style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
                <header style={{
                    padding: '1rem 2rem',
                    borderBottom: '1px solid var(--border-color)',
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    background: 'var(--bg-primary)'
                }}>
                    <h3 style={{ margin: 0 }}>
                        {selectedItem ? selectedItem.title : t('workspace')}
                    </h3>

                    <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                        {selectedItem && !isMarkdownArtifact(selectedItem.type) && (
                            <>
                                <button
                                    onClick={handleCopyToClipboard}
                                    title={t('copyToClipboard') || "Copy to Clipboard"}
                                    style={{
                                        background: 'none', border: 'none', cursor: 'pointer',
                                        color: isCopied ? 'var(--success-color, green)' : 'var(--text-secondary)',
                                        display: 'flex', alignItems: 'center', justifyContent: 'center',
                                        padding: '4px'
                                    }}
                                >
                                    <span className="icon-swap" key={isCopied ? 'check' : 'file'}>{isCopied ? <Check size={18} /> : <FileText size={18} />}</span>
                                </button>
                                <button
                                    onClick={exportDocx}
                                    title={t('exportDocx') || "Export DOCX"}
                                    style={{
                                        background: 'none', border: 'none', cursor: 'pointer',
                                        color: 'var(--text-secondary)',
                                        display: 'flex', alignItems: 'center', justifyContent: 'center',
                                        padding: '4px'
                                    }}
                                >
                                    <FileText size={18} /> <span style={{ fontSize: '0.7em', marginLeft: -6, marginTop: 6 }}>DOC</span>
                                </button>
                                <button
                                    onClick={downloadPdf}
                                    title={t('downloadPdf') || "Download PDF"}
                                    disabled={isGeneratingPdf}
                                    style={{
                                        background: 'none', border: 'none', cursor: 'pointer',
                                        color: 'var(--text-secondary)',
                                        display: 'flex', alignItems: 'center', justifyContent: 'center',
                                        padding: '4px',
                                        opacity: isGeneratingPdf ? 0.5 : 1
                                    }}
                                >
                                    <FileText size={18} /> <span style={{ fontSize: '0.7em', marginLeft: -6, marginTop: 6 }}>PDF</span>
                                </button>
                                <div style={{ width: 1, height: 20, background: 'var(--border-color)', margin: '0 8px' }} />
                            </>
                        )}

                        <button onClick={onClose} style={{
                            background: 'none', border: 'none', cursor: 'pointer',
                            color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: '8px'
                        }}>
                            <Minimize2 size={18} />
                            <span style={{ fontSize: '0.9rem' }}>{t('close')}</span>
                        </button>
                    </div>
                </header>

                {degradedReason && (
                    <div className="workspace-degraded" role="status">
                        {t('analysisDegradedNoAgent')}
                    </div>
                )}

                <div style={{ flex: 1, display: 'flex', overflow: 'hidden', padding: '2rem' }}>
                    {(selectedItem && isMarkdownArtifact(selectedItem.type)) ? (
                        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', height: '100%' }}>
                            <MarkdownEditor
                                value={editorContent}
                                onChange={setEditorContent}
                                onAnalyze={handleAnalyzeRequest} // Allows creating NEW analysis from here too
                                onSave={handleSave}
                                isAnalyzing={isAnalyzing}
                                saveStatus={saveStatus}
                                onCopy={handleCopyToClipboard}
                                onExportDocx={exportDocx}
                                onDownloadPdf={downloadPdf}
                                isCopied={isCopied}
                                isGeneratingPdf={isGeneratingPdf}
                            />
                        </div>
                    ) : selectedItem ? (
                        <div style={{
                            flex: 1,
                            border: '1px solid var(--border-color)',
                            borderRadius: '8px',
                            background: 'var(--bg-secondary)',
                            padding: '2rem',
                            overflowY: 'auto',
                            maxWidth: '800px',
                            margin: '0 auto',
                            width: '100%'
                        }}>
                            {selectedItem.type === 'flashcards' && (
                                <FlashcardsArtifact id={selectedItem.id} content={selectedItem.content} />
                            )}
                            {selectedItem.type === 'quiz' && Array.isArray(selectedItem.content) && (
                                <QuizView items={selectedItem.content} />
                            )}
                            {selectedItem.type === 'podcast' && (
                                <PodcastArtifact id={selectedItem.id} content={selectedItem.content} />
                            )}
                            {selectedItem.type === 'presentation' && (
                                <PresentationArtifact id={selectedItem.id} content={selectedItem.content} />
                            )}
                            {selectedItem.type === 'chart' && (
                                <ChartArtifact content={selectedItem.content} title={selectedItem.title} />
                            )}
                            {!['flashcards', 'quiz', 'podcast', 'presentation', 'chart'].includes(selectedItem.type) && (
                                <pre style={{ whiteSpace: 'pre-wrap', fontSize: '0.9rem', fontFamily: 'inherit' }}>
                                    {JSON.stringify(selectedItem.content, null, 2)}
                                </pre>
                            )}
                        </div>
                    ) : isAnalyzing ? (
                        <div style={{
                            flex: 1,
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            flexDirection: 'column',
                            gap: '1.5rem'
                        }}>
                            <div style={{
                                width: '64px',
                                height: '64px',
                                borderRadius: '50%',
                                background: 'var(--tag-bg)',
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'center'
                            }}>
                                <Loader2 className="animate-spin" size={32} style={{ color: 'var(--accent-primary)' }} />
                            </div>
                            <div style={{ textAlign: 'center' }}>
                                <p style={{ fontWeight: 500, fontSize: '1.1rem', margin: '0 0 0.5rem 0', color: 'var(--text-primary)' }}>
                                    {t('analyzingContent')}
                                </p>
                                <p style={{ color: 'var(--text-secondary)', fontSize: '0.875rem', margin: 0 }}>
                                    {t('analyzingContentHint')}
                                </p>
                            </div>
                        </div>
                    ) : (
                        <div style={{
                            flex: 1,
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            color: 'var(--text-secondary)',
                            flexDirection: 'column',
                            gap: '1rem'
                        }}>
                            <FileText size={48} style={{ opacity: 0.2 }} />
                            <p>{!hasFiles ? t('sourcesRequiredHint') : t('noAnalysisSelected')}</p>
                            {hasFiles && (
                                <button onClick={handleAnalyzeRequest} className="studio-gen-btn">
                                    {t('newAnalysis')}
                                </button>
                            )}
                        </div>
                    )}
                </div>
            </div>

            <WorkspacePromptDialog
                open={showAnalysisModal}
                title={t('newAnalysis')}
                submitLabel={t('start')}
                presets={presets.analysis}
                kbId={kbId}
                busy={isAnalyzing}
                onSubmit={runAnalysis}
                onClose={() => setShowAnalysisModal(false)}
            />

            <ComparisonDialog
                open={showComparison}
                kbId={kbId}
                presets={presets.comparison}
                busy={isComparing}
                onStart={async (v) => {
                    setIsComparing(true);
                    try {
                        // Keep the dialog open on a failed upload (413/415/422/503)
                        // instead of closing silently — onStartComparison resolves
                        // to whether the turn actually started.
                        const started = await onStartComparison?.(v);
                        if (started !== false) setShowComparison(false);
                    } finally {
                        setIsComparing(false);
                    }
                }}
                onClose={() => setShowComparison(false)}
            />
        </div>
    );
};
