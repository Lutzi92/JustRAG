import React, { useEffect, useRef, useState } from 'react';
import { Download } from 'lucide-react';
import axios from 'axios';
import type { PodcastContentData } from '../../types';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';

const PLAYBACK_SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2];

/**
 * Podcast artifact view: audio player (streamed from the generated-content
 * blob endpoint) + download. Ported from the retired `ContentModal` (see
 * `docs/superpowers/specs/2026-08-18-ui-workspace-rework-design.md` fix wave
 * item 1) so podcasts opened from the Workspace still have a player and a
 * download control, not just a JSON dump.
 */
export const PodcastArtifact: React.FC<{ id: string; content: PodcastContentData }> = ({ id, content }) => {
    const { t } = useTheme();
    const toast = useToast();
    const [audioUrl, setAudioUrl] = useState<string | null>(null);
    const hasAudio = !!(content.filePath || content.audioPath);
    const audioRef = useRef<HTMLAudioElement>(null);

    useEffect(() => {
        let cancelled = false;
        let objectUrl: string | null = null;

        if (hasAudio) {
            axios.get(`${API_BASE_URL}/api/generated-content/${id}/stream`, {
                responseType: 'blob',
            }).then(response => {
                if (cancelled) return;
                // Use the blob directly — it carries the correct content type from the server
                objectUrl = URL.createObjectURL(response.data);
                setAudioUrl(objectUrl);
            }).catch((err: unknown) => {
                console.error('Failed to load podcast audio:', err);
                toast.error(t('audioLoadError'));
            });
        } else {
            setAudioUrl(null);
        }

        return () => {
            cancelled = true;
            if (objectUrl) URL.revokeObjectURL(objectUrl);
        };
        // eslint-disable-next-line react-hooks/exhaustive-deps -- `id` (via hasAudio) is the stable identity of the artifact being streamed
    }, [id, hasAudio]);

    const handleDownload = () => {
        if (!hasAudio) return;
        axios.get(`${API_BASE_URL}/api/generated-content/${id}/download`, {
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
    };

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
            {hasAudio && (
                <div style={{
                    padding: '1rem',
                    background: 'var(--bg-primary)',
                    borderRadius: '8px',
                    border: '1px solid var(--border-color)',
                }}>
                    {audioUrl && (
                        <>
                            {/* eslint-disable-next-line jsx-a11y/media-has-caption -- generated TTS audio, no captions available */}
                            <audio
                                ref={audioRef}
                                controls
                                style={{ width: '100%', marginBottom: '0.5rem' }}
                                src={audioUrl}
                            >
                                {t('audioNotSupported')}
                            </audio>
                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                                <span>{t('playbackSpeed')}:</span>
                                {PLAYBACK_SPEEDS.map(speed => (
                                    <button
                                        key={speed}
                                        className="secondary-button"
                                        style={{ padding: '2px 8px', fontSize: '0.75rem', minWidth: 'auto' }}
                                        onClick={() => {
                                            if (audioRef.current) audioRef.current.playbackRate = speed;
                                        }}
                                    >
                                        {speed}x
                                    </button>
                                ))}
                            </div>
                        </>
                    )}
                    <button className="secondary-button" onClick={handleDownload} style={{ marginTop: audioUrl ? '0.75rem' : 0 }}>
                        <Download size={16} aria-hidden="true" /> {t('downloadAudio')}
                    </button>
                </div>
            )}
            {content.script && (
                <div style={{ whiteSpace: 'pre-wrap', lineHeight: '1.6' }}>
                    {content.script}
                </div>
            )}
        </div>
    );
};
