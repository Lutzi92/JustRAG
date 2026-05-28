import React from 'react';
import { X, Download, ChevronLeft, ChevronRight, FileText } from 'lucide-react';
import {
    BarChart, Bar, LineChart, Line, AreaChart, Area, PieChart, Pie, Cell, ScatterChart, Scatter, XAxis, YAxis, ZAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer
} from 'recharts';
import axios from 'axios';
import type { GeneratedContent } from '../types';
import { API_BASE_URL } from '../api';
import { useToast } from '../contexts/ToastContext';
import { useTheme } from '../contexts/ThemeContext';

interface ContentModalProps {
    show: boolean;
    onClose: () => void;
    selectedContent: GeneratedContent | null;
    currentCardIndex: number;
    setCurrentCardIndex: React.Dispatch<React.SetStateAction<number>>;
    isAnswerVisible: boolean;
    setIsAnswerVisible: (visible: boolean) => void;
}

export const ContentModal: React.FC<ContentModalProps> = ({
    show, onClose, selectedContent, currentCardIndex, setCurrentCardIndex,
    isAnswerVisible, setIsAnswerVisible
}) => {
    const toast = useToast();
    const { t } = useTheme();
    const [audioUrl, setAudioUrl] = React.useState<string | null>(null);

    React.useEffect(() => {
        let cancelled = false;

        if (selectedContent?.type === 'podcast' && (selectedContent.content.filePath || selectedContent.content.audioPath)) {
            axios.get(`${API_BASE_URL}/api/generated-content/${selectedContent.id}/stream`, {
                responseType: 'blob',
            }).then(response => {
                if (cancelled) return;
                // Use the blob directly — it carries the correct content type from the server
                const url = URL.createObjectURL(response.data);
                setAudioUrl(url);
            }).catch((err: unknown) => {
                console.error('Failed to load podcast audio:', err);
                toast.error(t('audioLoadError'));
            });
        } else {
            setAudioUrl(null);
        }

        return () => {
            cancelled = true;
            setAudioUrl(prev => {
                if (prev) URL.revokeObjectURL(prev);
                return null;
            });
        };
    }, [selectedContent?.id, selectedContent?.type]);

    if (!show || !selectedContent) return null;

    const handleDownload = () => {
        if (selectedContent.type === 'flashcards') {
            const csvContent = "data:text/csv;charset=utf-8," +
                selectedContent.content.map((c: { front: string; back: string }) => `"${c.front.replace(/"/g, '""')}","${c.back.replace(/"/g, '""')}"`).join("\n");
            const encodedUri = encodeURI(csvContent);
            const link = document.createElement("a");
            link.setAttribute("href", encodedUri);
            link.setAttribute("download", "flashcards.csv");
            document.body.appendChild(link);
            link.click();
            document.body.removeChild(link);
        } else if (selectedContent.type === 'presentation') {
            const filePath = selectedContent.content.filePath;
            if (filePath) {
                axios.get(`${API_BASE_URL}/api/generated-content/${selectedContent.id}/download`, {
                    responseType: 'blob',
                }).then((response) => {
                    const url = window.URL.createObjectURL(new Blob([response.data]));
                    const link = document.createElement('a');
                    link.href = url;

                    // Get filename from header if possible, or fallback
                    const contentDisposition = response.headers['content-disposition'];
                    let fileName = 'presentation.pptx';
                    if (contentDisposition) {
                        const match = contentDisposition.match(/filename="?(.+)"?/); // regex to capture filename
                        if (match && match[1]) fileName = match[1].replace(/"/g, '');
                    }

                    link.setAttribute('download', fileName);
                    document.body.appendChild(link);
                    link.click();
                    link.remove();
                    window.URL.revokeObjectURL(url);
                }).catch((err: unknown) => {
                    console.error('Download failed', err);
                    toast.error(t('downloadFailed'));
                });
            } else if (selectedContent.content.markdown) {
                // Fallback for old style if any
                const blob = new Blob([selectedContent.content.markdown], { type: 'text/markdown' });
                const url = URL.createObjectURL(blob);
                const link = document.createElement('a');
                link.href = url;
                link.download = 'presentation.md';
                document.body.appendChild(link);
                link.click();
                document.body.removeChild(link);
            }
        } else if (selectedContent.type === 'podcast' && (selectedContent.content.audioPath || selectedContent.content.filePath)) {
            axios.get(`${API_BASE_URL}/api/generated-content/${selectedContent.id}/download`, {
                responseType: 'blob',
            }).then((response) => {
                const url = window.URL.createObjectURL(new Blob([response.data]));
                const link = document.createElement('a');
                link.href = url;

                const contentDisposition = response.headers['content-disposition'];
                let fileName = 'podcast.mp3';
                if (contentDisposition) {
                    const match = contentDisposition.match(/filename="?(.+)"?/);
                    if (match && match[1]) fileName = match[1].replace(/"/g, '');
                }

                link.setAttribute('download', fileName);
                document.body.appendChild(link);
                link.click();
                link.remove();
                window.URL.revokeObjectURL(url);
            }).catch((err: unknown) => {
                console.error('Download failed', err);
                toast.error(t('downloadFailed'));
            });
        } else if (selectedContent.type === 'chart') {
            const svg = document.querySelector('.recharts-wrapper svg');
            if (svg) {
                const svgData = new XMLSerializer().serializeToString(svg);
                const blob = new Blob([svgData], { type: 'image/svg+xml;charset=utf-8' });
                const url = URL.createObjectURL(blob);
                const link = document.createElement('a');
                link.href = url;
                link.download = `${selectedContent.title.replace(/[^a-z0-9]/gi, '_').toLowerCase()}.svg`;
                document.body.appendChild(link);
                link.click();
                document.body.removeChild(link);
            }
        }
    };

    return (
        // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions, jsx-a11y/no-noninteractive-element-interactions
        <div className="modal-overlay" onClick={onClose}>
            <div className="modal-content" onClick={e => e.stopPropagation()} style={{ maxWidth: '800px', width: '90%' }} role="dialog" aria-modal="true" aria-labelledby="content-modal-title">
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                    <h3 id="content-modal-title" style={{ margin: 0 }}>{selectedContent.title}</h3>
                    <div style={{ display: 'flex', gap: '8px' }}>
                        {(selectedContent.type === 'flashcards' || selectedContent.type === 'chart' ||
                            (selectedContent.type === 'presentation' && (selectedContent.content.filePath || selectedContent.content.markdown)) ||
                            (selectedContent.type === 'podcast' && (selectedContent.content.audioPath || selectedContent.content.filePath))) && (
                                <button className="secondary-button" onClick={handleDownload}>
                                    <Download size={16} aria-hidden="true" />
                                    {selectedContent.type === 'flashcards' ? ' CSV' :
                                        selectedContent.type === 'presentation' ? ' Download PPTX' :
                                            selectedContent.type === 'podcast' ? ' Download Audio' : ' Download SVG'}
                                </button>
                            )}
                        <button onClick={onClose} className="icon-button" aria-label={t('close')}><X size={20} /></button>
                    </div>
                </div>

                <div style={{ overflowY: 'auto', maxHeight: '70vh' }}>
                    {selectedContent.type === 'chart' && (
                        <div style={{ padding: '1rem', display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
                            <p style={{ color: 'var(--text-secondary)', marginBottom: '1rem' }}>{selectedContent.content.description}</p>
                            <div style={{ width: '100%', height: 400 }}>
                                <ResponsiveContainer width="100%" height="100%">
                                    {(() => {
                                        const { type, config, series } = selectedContent.content;
                                        const colors = ['#165a97', '#2d8f4e', '#cc8400', '#c0392b', '#6a1b9a', '#d84315'];

                                        // Support both old and new schema
                                        const chartData = (series || config.data) as Record<string, unknown>[] | undefined;
                                        const xAxisKey = (config.xAxis || config.xKey) as string | undefined;
                                        const yAxisKeys = (config.keys || config.yKeys || []) as string[];

                                        if (!chartData || chartData.length === 0) {
                                            return <div style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-secondary)' }}>Keine Daten verfügbar</div>;
                                        }

                                        const commonProps = {
                                            data: chartData,
                                            margin: { top: 20, right: 30, left: 20, bottom: 5 },
                                        };

                                        switch (type) {
                                            case 'line':
                                                return (
                                                    <LineChart {...commonProps}>
                                                        <CartesianGrid strokeDasharray="3 3" vertical={false} />
                                                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                                                        <YAxis axisLine={false} tickLine={false} />
                                                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                                                        <Legend />
                                                        {yAxisKeys.map((key: string, idx: number) => (
                                                            <Line key={key} type="monotone" dataKey={key} stroke={colors[idx % colors.length]} strokeWidth={2} dot={{ r: 4 }} activeDot={{ r: 6 }} />
                                                        ))}
                                                    </LineChart>
                                                );
                                            case 'area':
                                                return (
                                                    <AreaChart {...commonProps}>
                                                        <CartesianGrid strokeDasharray="3 3" vertical={false} />
                                                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                                                        <YAxis axisLine={false} tickLine={false} />
                                                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                                                        <Legend />
                                                        {yAxisKeys.map((key: string, idx: number) => (
                                                            <Area key={key} type="monotone" dataKey={key} fill={colors[idx % colors.length]} stroke={colors[idx % colors.length]} fillOpacity={0.6} />
                                                        ))}
                                                    </AreaChart>
                                                );
                                            case 'pie':
                                                return (
                                                    <PieChart>
                                                        <Pie
                                                            data={chartData}
                                                            dataKey={(config.valueKey as string) || yAxisKeys?.[0] || 'value'}
                                                            nameKey={(config.nameKey as string) || xAxisKey || 'name'}
                                                            cx="50%"
                                                            cy="50%"
                                                            outerRadius={120}
                                                            label={({ name, percent }: { name?: string; percent?: number }) => `${name ?? ''} ${((percent ?? 0) * 100).toFixed(0)}%`}
                                                            labelLine={false}
                                                        >
                                                            {chartData.map((_: unknown, index: number) => (
                                                                <Cell key={`cell-${index}`} fill={colors[index % colors.length]} />
                                                            ))}
                                                        </Pie>
                                                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                                                        <Legend />
                                                    </PieChart>
                                                );
                                            case 'scatter':
                                                return (
                                                    <ScatterChart margin={{ top: 20, right: 30, left: 20, bottom: 5 }}>
                                                        <CartesianGrid strokeDasharray="3 3" />
                                                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                                                        <YAxis dataKey={(config.yAxis as string) || yAxisKeys?.[0]} axisLine={false} tickLine={false} />
                                                        <ZAxis range={[60, 400]} />
                                                        <Tooltip cursor={{ strokeDasharray: '3 3' }} contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                                                        <Legend />
                                                        <Scatter name={yAxisKeys?.[0] || 'Data'} data={chartData} fill={colors[0]} />
                                                    </ScatterChart>
                                                );
                                            case 'bar':
                                            default:
                                                return (
                                                    <BarChart {...commonProps}>
                                                        <CartesianGrid strokeDasharray="3 3" vertical={false} />
                                                        <XAxis dataKey={xAxisKey} axisLine={false} tickLine={false} />
                                                        <YAxis axisLine={false} tickLine={false} />
                                                        <Tooltip contentStyle={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                                                        <Legend />
                                                        {yAxisKeys.map((key: string, idx: number) => (
                                                            <Bar key={key} dataKey={key} fill={colors[idx % colors.length]} radius={[4, 4, 0, 0]} />
                                                        ))}
                                                    </BarChart>
                                                );
                                        }
                                    })()}
                                </ResponsiveContainer>
                            </div>
                        </div>
                    )}
                    {selectedContent.type === 'flashcards' && (
                        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '2rem', padding: '1rem' }}>
                            <button
                                className={`flashcard ${isAnswerVisible ? 'is-flipped' : ''}`}
                                onClick={() => setIsAnswerVisible(!isAnswerVisible)}
                                onKeyDown={(e) => {
                                    if (e.key === 'Enter' || e.key === ' ') {
                                        e.preventDefault();
                                        setIsAnswerVisible(!isAnswerVisible);
                                    }
                                }}
                                style={{ border: 'none', padding: 0 }}
                                aria-label={`Flashcard ${currentCardIndex + 1}. ${t('flashcardFlip')}`}
                            >
                                <div className="flashcard-inner">
                                    <div className="flashcard-front">
                                        <div style={{ padding: '2rem', textAlign: 'center' }}>
                                            <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '1rem', textTransform: 'uppercase' }}>Frage</div>
                                            <div style={{ fontSize: '1.25rem', fontWeight: 600 }}>{selectedContent.content[currentCardIndex].front}</div>
                                        </div>
                                    </div>
                                    <div className="flashcard-back">
                                        <div style={{ padding: '2rem', textAlign: 'center' }}>
                                            <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '1rem', textTransform: 'uppercase' }}>Antwort</div>
                                            <div style={{ fontSize: '1.25rem' }}>{selectedContent.content[currentCardIndex].back}</div>
                                        </div>
                                    </div>
                                </div>
                            </button>

                            <div style={{ display: 'flex', alignItems: 'center', gap: '2rem' }}>
                                <button
                                    className="icon-button"
                                    disabled={currentCardIndex === 0}
                                    onClick={() => {
                                        setCurrentCardIndex(prev => prev - 1);
                                        setIsAnswerVisible(false);
                                    }}
                                    style={{
                                        padding: '12px',
                                        background: 'var(--bg-secondary)',
                                        border: '1px solid var(--border-color)',
                                        borderRadius: '50%',
                                        opacity: currentCardIndex === 0 ? 0.3 : 1
                                    }}
                                    aria-label={t('previousCard')}
                                >
                                    <ChevronLeft size={24} aria-hidden="true" />
                                </button>

                                <div style={{ fontSize: '0.9rem', color: 'var(--text-secondary)', fontWeight: 500 }}>
                                    {currentCardIndex + 1} / {selectedContent.content.length}
                                </div>

                                <button
                                    className="icon-button"
                                    disabled={currentCardIndex === selectedContent.content.length - 1}
                                    onClick={() => {
                                        setCurrentCardIndex(prev => prev + 1);
                                        setIsAnswerVisible(false);
                                    }}
                                    style={{
                                        padding: '12px',
                                        background: 'var(--bg-secondary)',
                                        border: '1px solid var(--border-color)',
                                        borderRadius: '50%',
                                        opacity: currentCardIndex === selectedContent.content.length - 1 ? 0.3 : 1
                                    }}
                                    aria-label={t('nextCard')}
                                >
                                    <ChevronRight size={24} aria-hidden="true" />
                                </button>
                            </div>
                        </div>
                    )}
                    {selectedContent.type === 'presentation' && (
                        <div style={{ whiteSpace: 'pre-wrap', lineHeight: '1.6' }}>
                            {selectedContent.content.summary ? (
                                <div style={{ textAlign: 'center', padding: '2rem' }}>
                                    <FileText size={48} style={{ color: 'var(--accent-primary)', marginBottom: '1rem' }} aria-hidden="true" />
                                    <h3>Präsentation erstellt</h3>
                                    <p>{selectedContent.content.summary}</p>
                                    <p style={{ fontSize: '0.9rem', color: 'var(--text-secondary)' }}>
                                        Klicken Sie auf "Download PPTX" oben rechts, um die Datei herunterzuladen.
                                    </p>
                                </div>
                            ) : (
                                selectedContent.content.markdown
                            )}
                        </div>
                    )}
                    {selectedContent.type === 'podcast' && (
                        <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
                            {audioUrl && (
                                <div style={{
                                    padding: '1rem',
                                    background: 'var(--bg-secondary)',
                                    borderRadius: '8px',
                                    border: '1px solid var(--border-color)',
                                }}>
                                    <audio
                                        controls
                                        style={{ width: '100%', marginBottom: '0.5rem' }}
                                        src={audioUrl}
                                    >
                                        {t('audioNotSupported')}
                                    </audio>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                                        <span>{t('playbackSpeed')}:</span>
                                        {[0.5, 0.75, 1, 1.25, 1.5, 2].map(speed => (
                                            <button
                                                key={speed}
                                                className="secondary-button"
                                                style={{ padding: '2px 8px', fontSize: '0.75rem', minWidth: 'auto' }}
                                                onClick={() => {
                                                    const audio = document.querySelector('audio');
                                                    if (audio) audio.playbackRate = speed;
                                                }}
                                            >
                                                {speed}x
                                            </button>
                                        ))}
                                    </div>
                                </div>
                            )}
                            <div style={{ whiteSpace: 'pre-wrap', lineHeight: '1.6' }}>
                                {selectedContent.content.script}
                            </div>
                        </div>
                    )}
                </div>
            </div>
        </div>
    );
};
