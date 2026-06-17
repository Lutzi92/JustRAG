import React, { useState, useEffect } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import axios from 'axios';
import {
    Trash2, Minimize2,
    Type, Layout, Mic, X, Check,
    FileText, Loader2, Newspaper, HelpCircle, GraduationCap, Clock, ListChecks, BarChart3
} from 'lucide-react';
import type { GeneratedContent, StudioConfig } from '../../types';
import { isMarkdownArtifact, artifactTypeLabel } from '../../utils/artifactTypes';
import { QuizView } from './QuizView';
import { API_BASE_URL } from '../../api';
import { MarkdownEditor } from './MarkdownEditor';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import { copyToClipboard } from '../../utils/clipboard';

interface StudioWorkspaceProps {
    kbId: string;
    generatedContent: GeneratedContent[];
    onGenerate: (type: 'cards' | 'presentation' | 'podcast' | 'chart' | 'abstract' | 'briefing_doc' | 'faq' | 'study_guide' | 'timeline' | 'quiz') => void;
    onDeleteContent: (id: string, e: React.MouseEvent) => void;
    onClose: () => void;
    studioConfig?: StudioConfig;
    initialSelectedItem?: GeneratedContent | null;
    hasFiles?: boolean;
}

export const StudioWorkspace: React.FC<StudioWorkspaceProps> = ({
    kbId,
    generatedContent,
    onGenerate: onGenerateParent,
    onDeleteContent,
    onClose,
    studioConfig,
    initialSelectedItem,
    hasFiles = true
}) => {
    const { t, language } = useTheme();
    const toast = useToast();
    // We treat 'analysis' items as the editable notes.
    const [selectedItem, setSelectedItem] = useState<GeneratedContent | null>(initialSelectedItem || null);
    const [editorContent, setEditorContent] = useState('');
    const [localGeneratedContent, setLocalGeneratedContent] = useState<GeneratedContent[]>([]);

    // Sync props to local state to allow immediate UI updates
    useEffect(() => {
        setLocalGeneratedContent(generatedContent);
    }, [generatedContent]);

    // Update selected item if initialSelectedItem changes (e.g. re-opening studio with different item)
    useEffect(() => {
        if (initialSelectedItem) {
            setSelectedItem(initialSelectedItem);
        }
    }, [initialSelectedItem]);

    // When selection changes, update editor content if it's an analysis or abstract
    useEffect(() => {
        if (selectedItem && isMarkdownArtifact(selectedItem.type)) {
            setEditorContent((selectedItem.content as { text?: string }).text || '');
        }
    }, [selectedItem]);

    // Analysis State
    const [isAnalyzing, setIsAnalyzing] = useState(false);
    const [showAnalysisModal, setShowAnalysisModal] = useState(false);
    const [analysisInstruction, setAnalysisInstruction] = useState('');
    const [saveStatus, setSaveStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');

    const sc = { cards: true, presentation: true, podcast: true, chart: true, abstract: true, briefingDoc: true, faq: true, studyGuide: true, timeline: true, quiz: true, ...studioConfig };

    const handleAnalyzeRequest = () => {
        setShowAnalysisModal(true);
        setAnalysisInstruction('');
    };

    const performAnalysis = async () => {
        if (!analysisInstruction.trim()) return;
        setIsAnalyzing(true);
        setShowAnalysisModal(false);

        try {
            const res = await axios.post(`${API_BASE_URL}/api/kb/${kbId}/generate/analysis`, {
                topic: analysisInstruction,
                language: language || 'de'
            });

            const newItem = res.data;
            if (!newItem?.id) {
                toast.error(t('analysisError'));
                return;
            }
            // Optimistic update
            const updatedList = [newItem, ...localGeneratedContent];
            setLocalGeneratedContent(updatedList);
            setSelectedItem(newItem);

            // Optimistic local state; parent syncs on next fetch
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

            // Update local state item
            const updatedItem = { ...selectedItem, content: { ...selectedItem.content, text: editorContent } } as GeneratedContent;
            setSelectedItem(updatedItem);
            setLocalGeneratedContent(prev => prev.map(item => item.id === updatedItem.id ? updatedItem : item));

            setSaveStatus('saved');
            setTimeout(() => setSaveStatus('idle'), 2000);
        } catch (err: unknown) {
            console.error('Save failed:', err);
            toast.error(t('saveError'));
            setSaveStatus('error');
            setTimeout(() => setSaveStatus('idle'), 3000);
        }
    };

    const handleDelete = (id: string, e: React.MouseEvent) => {
        e.stopPropagation();
        if (selectedItem?.id === id) {
            setSelectedItem(null);
        }
        onDeleteContent(id, e);
        setLocalGeneratedContent(prev => prev.filter(item => item.id !== id));
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
        let contentHtml = '';

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
            height: '100%',
            background: 'var(--bg-primary)',
            position: 'relative',
            overflow: 'hidden'
        }}>
            {/* Left Sidebar: Content List */}
            <div style={{
                width: '300px',
                borderRight: '1px solid var(--border-color)',
                display: 'flex',
                flexDirection: 'column',
                background: 'var(--bg-secondary)'
            }}>
                <div style={{ padding: '1.5rem', borderBottom: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: 0, fontSize: '1.1rem', fontWeight: 600 }}>{t('studio')}</h2>
                    <p style={{ margin: '0.5rem 0 0 0', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                        {t('researchWorkspace')}
                    </p>
                </div>

                <div style={{ padding: '1rem', display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem', opacity: hasFiles ? 1 : 0.4 }}
                     title={!hasFiles ? t('sourcesRequiredHint') : undefined}
                >
                    <button onClick={handleAnalyzeRequest} disabled={!hasFiles || isAnalyzing} className="studio-gen-btn">
                        <FileText size={16} /> {t('newAnalysis')}
                    </button>
                    {sc.cards && (
                        <button onClick={() => onGenerateParent('cards')} disabled={!hasFiles} className="studio-gen-btn">
                            <Type size={16} /> {t('flashcards')}
                        </button>
                    )}
                    {sc.presentation && (
                        <button onClick={() => onGenerateParent('presentation')} disabled={!hasFiles} className="studio-gen-btn">
                            <Layout size={16} /> {t('slides')}
                        </button>
                    )}
                    {sc.podcast && (
                        <button onClick={() => onGenerateParent('podcast')} disabled={!hasFiles} className="studio-gen-btn">
                            <Mic size={16} /> {t('podcast')}
                        </button>
                    )}

                    {sc.abstract && (
                        <button onClick={() => onGenerateParent('abstract')} disabled={!hasFiles} className="studio-gen-btn">
                            <FileText size={16} /> {t('abstract')}
                        </button>
                    )}
                    {sc.chart && (
                        <button onClick={() => onGenerateParent('chart')} disabled={!hasFiles} className="studio-gen-btn">
                            <BarChart3 size={16} /> {t('chart')}
                        </button>
                    )}

                    {sc.briefingDoc && (
                        <button onClick={() => onGenerateParent('briefing_doc')} disabled={!hasFiles} className="studio-gen-btn">
                            <Newspaper size={16} /> {t('briefingDoc')}
                        </button>
                    )}
                    {sc.faq && (
                        <button onClick={() => onGenerateParent('faq')} disabled={!hasFiles} className="studio-gen-btn">
                            <HelpCircle size={16} /> {t('faq')}
                        </button>
                    )}
                    {sc.studyGuide && (
                        <button onClick={() => onGenerateParent('study_guide')} disabled={!hasFiles} className="studio-gen-btn">
                            <GraduationCap size={16} /> {t('studyGuide')}
                        </button>
                    )}
                    {sc.timeline && (
                        <button onClick={() => onGenerateParent('timeline')} disabled={!hasFiles} className="studio-gen-btn">
                            <Clock size={16} /> {t('timeline')}
                        </button>
                    )}
                    {sc.quiz && (
                        <button onClick={() => onGenerateParent('quiz')} disabled={!hasFiles} className="studio-gen-btn">
                            <ListChecks size={16} /> {t('quiz')}
                        </button>
                    )}
                </div>

                <div style={{ flex: 1, overflowY: 'auto', padding: '1rem' }}>
                    <h3 style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.05em', marginTop: 0 }}>
                        {t('generatedContentHeader')}
                    </h3>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                        {localGeneratedContent.map(item => (
                            <div
                                key={item.id}
                                className={`source-card ${selectedItem?.id === item.id ? 'active' : ''}`}
                                role="button"
                                tabIndex={0}
                                onClick={() => setSelectedItem(item)}
                                onKeyDown={(e) => {
                                    // Ignore keys bubbling from the nested delete button.
                                    if (e.target !== e.currentTarget) return;
                                    if (e.key === 'Enter' || e.key === ' ') {
                                        e.preventDefault();
                                        setSelectedItem(item);
                                    }
                                }}
                                style={{
                                    cursor: 'pointer',
                                    border: selectedItem?.id === item.id ? '1px solid var(--accent-primary)' : undefined,
                                    padding: '0.75rem'
                                }}
                            >
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                                    <div style={{ overflow: 'hidden' }}>
                                        <div style={{ fontWeight: 500, fontSize: '0.9rem', marginBottom: '0.25rem' }}>
                                            {item.title}
                                        </div>
                                        <div className="source-meta">
                                            {artifactTypeLabel(item.type, t)} • {new Date(item.createdAt).toLocaleDateString()}
                                        </div>
                                    </div>
                                    <button
                                        onClick={(e) => handleDelete(item.id, e)}
                                        className="settings-toggle"
                                        style={{ padding: '4px' }}
                                    >
                                        <Trash2 size={14} />
                                    </button>
                                </div>
                            </div>
                        ))}
                    </div>
                </div>
            </div>

            {/* Main Area */}
            <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
                <header style={{
                    padding: '1rem 2rem',
                    borderBottom: '1px solid var(--border-color)',
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    background: 'var(--bg-primary)'
                }}>
                    <h3 style={{ margin: 0 }}>
                        {selectedItem ? selectedItem.title : t('studio')}
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
                                <div>
                                    {(selectedItem.content as Array<{ front: string; back: string }>).map((card, idx) => (
                                        <div key={idx} style={{
                                            background: 'var(--bg-primary)',
                                            padding: '1rem',
                                            marginBottom: '1rem',
                                            borderRadius: '8px',
                                            border: '1px solid var(--border-color)'
                                        }}>
                                            <strong>F: {card.front}</strong>
                                            <div style={{ marginTop: '0.5rem', paddingTop: '0.5rem', borderTop: '1px dashed var(--border-color)' }}>
                                                A: {card.back}
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            )}
                            {selectedItem.type === 'quiz' && Array.isArray(selectedItem.content) && (
                                <QuizView items={selectedItem.content} />
                            )}
                            {selectedItem.type !== 'flashcards' && !(selectedItem.type === 'quiz' && Array.isArray(selectedItem.content)) && (
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

            {/* Analysis Modal */}
            {showAnalysisModal && (
                <div style={{
                    position: 'fixed',
                    top: 0,
                    left: 0,
                    right: 0,
                    bottom: 0,
                    background: 'rgba(0,0,0,0.5)',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    zIndex: 1000
                }}
                    // Backdrop click-to-close is a mouse convenience redundant with the
                    // modal's close button; only clicks on the backdrop itself dismiss.
                    role="presentation"
                    onClick={(e) => { if (e.target === e.currentTarget) setShowAnalysisModal(false); }}
                >
                    <div style={{
                        background: 'var(--bg-primary)',
                        padding: '2rem',
                        borderRadius: '12px',
                        width: '500px',
                        maxWidth: '90%',
                        boxShadow: '0 4px 20px rgba(0,0,0,0.2)',
                        border: '1px solid var(--border-color)'
                    }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '1rem' }}>
                            <h3 style={{ margin: 0 }}>{t('deepAnalysis')}</h3>
                            <button onClick={() => setShowAnalysisModal(false)} style={{ background: 'none', border: 'none', cursor: 'pointer' }}>
                                <X size={20} />
                            </button>
                        </div>

                        <p style={{ color: 'var(--text-secondary)', marginBottom: '1rem' }}>
                            {t('deepAnalysisDescription')}
                        </p>

                        <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem', flexWrap: 'wrap' }}>
                            {[
                                { label: t('compareSources'), value: "Vergleiche Quellen" },
                                { label: t('criticalAnalysis'), value: "Kritische Analyse" },
                                { label: t('extractMethodology'), value: "Methodik extrahieren" },
                                { label: t('summary'), value: "Zusammenfassung" }
                            ].map(p => (
                                <button
                                    key={p.value}
                                    onClick={() => setAnalysisInstruction(p.value)}
                                    className="studio-gen-btn"
                                    style={{
                                        justifyContent: 'center',
                                        flex: '1 1 auto'
                                    }}
                                >
                                    {p.label}
                                </button>
                            ))}
                        </div>

                        <textarea
                            value={analysisInstruction}
                            onChange={(e) => setAnalysisInstruction(e.target.value)}
                            placeholder={t('deepAnalysisPlaceholder')}
                            style={{
                                width: '100%',
                                height: '100px',
                                padding: '0.75rem',
                                marginBottom: '1.5rem',
                                borderRadius: '8px',
                                border: '1px solid var(--border-color)',
                                background: 'var(--bg-secondary)',
                                color: 'var(--text-primary)',
                                resize: 'none'
                            }}
                            // eslint-disable-next-line jsx-a11y/no-autofocus -- focus first field on dialog open (WAI-ARIA dialog pattern)
                            autoFocus
                        />

                        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
                            <button
                                onClick={() => setShowAnalysisModal(false)}
                                style={{
                                    padding: '0.75rem 1.5rem',
                                    borderRadius: '8px',
                                    border: '1px solid var(--border-color)',
                                    background: 'transparent',
                                    cursor: 'pointer',
                                    color: 'var(--text-primary)'
                                }}
                            >
                                {t('cancel')}
                            </button>
                            <button
                                onClick={performAnalysis}
                                disabled={!analysisInstruction.trim()}
                                style={{
                                    padding: '0.75rem 1.5rem',
                                    borderRadius: '8px',
                                    border: 'none',
                                    background: 'var(--accent-primary)',
                                    color: '#fff',
                                    cursor: 'pointer',
                                    opacity: analysisInstruction.trim() ? 1 : 0.5,
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '8px'
                                }}
                            >
                                <Check size={16} />
                                {t('startAnalysis')}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};
