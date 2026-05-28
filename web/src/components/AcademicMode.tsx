/**
 * Academic Mode Wrapper
 *
 * Manages the state machine between basic search (AcademicSearchMode)
 * and comprehensive research (AcademicResearchMode). Lifts shared state
 * (query, papers, peerReviewedOnly) so it persists across mode transitions.
 */

import { useState, useCallback } from 'react';
import { Search, BookOpen } from 'lucide-react';
import { useAuth } from '../contexts/AuthContext';
import { useTheme } from '../contexts/ThemeContext';
import type { AcademicPaper, AcademicFinding } from '../types';
import AcademicSearchMode from './AcademicSearchMode';
import AcademicResearchMode from './AcademicResearchMode';

// ---------------------------------------------------------------------------
// Props — same interface ChatView currently passes to AcademicResearchMode
// ---------------------------------------------------------------------------

interface AcademicModeProps {
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
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

function AcademicMode({
    kbId,
    onClose,
    loadedSession,
    onSessionSaved,
    onClearSession,
    onRunningChange,
}: AcademicModeProps) {
    const { siteConfigs } = useAuth();
    const { language } = useTheme();

    // If a saved session is provided, go straight to research mode
    const [mode, setMode] = useState<'search' | 'research'>(
        loadedSession ? 'research' : 'search'
    );

    // Shared state lifted from search mode
    const [query, setQuery] = useState('');
    const [peerReviewedOnly, setPeerReviewedOnly] = useState(true);
    const [discoveredPapers, setDiscoveredPapers] = useState<AcademicPaper[]>([]);

    // Track if research is running (hide toggle during active research)
    const [isResearchRunning, setIsResearchRunning] = useState(false);

    const academicSearchName = siteConfigs?.academic_search_name as string | undefined;

    const handleBackToSearch = () => {
        setMode('search');
    };

    const handleResearchRunningChange = useCallback((running: boolean) => {
        setIsResearchRunning(running);
        onRunningChange?.(running);
    }, [onRunningChange]);

    // Labels
    const searchLabel = language === 'en' ? 'Quick Search' : 'Schnellsuche';
    const researchLabel = language === 'en' ? 'Deep Research' : 'Tiefenrecherche';

    // Don't show toggle for loaded sessions or while research is running
    const showToggle = !loadedSession && !isResearchRunning;

    const modeToggle = showToggle ? (
        <div
            style={{
                display: 'flex',
                alignItems: 'center',
                gap: '4px',
                padding: '8px 2rem',
                borderBottom: '1px solid var(--border-color)',
                background: 'var(--bg-secondary)',
            }}
        >
            <button
                onClick={() => setMode('search')}
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '6px',
                    padding: '6px 14px',
                    borderRadius: '6px',
                    border: 'none',
                    cursor: mode === 'search' ? 'default' : 'pointer',
                    fontSize: '0.85rem',
                    fontWeight: mode === 'search' ? 600 : 400,
                    background: mode === 'search' ? 'var(--accent-primary)' : 'transparent',
                    color: mode === 'search' ? '#fff' : 'var(--text-secondary)',
                    transition: 'all 0.15s',
                }}
            >
                <Search size={14} />
                {searchLabel}
            </button>
            <button
                onClick={() => setMode('research')}
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '6px',
                    padding: '6px 14px',
                    borderRadius: '6px',
                    border: 'none',
                    cursor: mode === 'research' ? 'default' : 'pointer',
                    fontSize: '0.85rem',
                    fontWeight: mode === 'research' ? 600 : 400,
                    background: mode === 'research' ? 'var(--accent-primary)' : 'transparent',
                    color: mode === 'research' ? '#fff' : 'var(--text-secondary)',
                    transition: 'all 0.15s',
                }}
            >
                <BookOpen size={14} />
                {researchLabel}
            </button>
        </div>
    ) : null;

    return (
        <>
            {mode === 'search' ? (
                <AcademicSearchMode
                    kbId={kbId}
                    query={query}
                    onQueryChange={setQuery}
                    peerReviewedOnly={peerReviewedOnly}
                    onPeerReviewedChange={setPeerReviewedOnly}
                    papers={discoveredPapers}
                    onPapersChange={setDiscoveredPapers}
                    onClose={onClose}
                    academicSearchName={academicSearchName}
                    modeToggle={modeToggle}
                />
            ) : (
                <AcademicResearchMode
                    kbId={kbId}
                    onClose={onClose}
                    loadedSession={loadedSession}
                    onSessionSaved={onSessionSaved}
                    onClearSession={onClearSession}
                    onRunningChange={handleResearchRunningChange}
                    initialTopic={loadedSession ? undefined : query}
                    initialPapers={loadedSession ? undefined : discoveredPapers}
                    initialPeerReviewedOnly={loadedSession ? undefined : peerReviewedOnly}
                    onBackToSearch={loadedSession ? undefined : handleBackToSearch}
                    modeToggle={modeToggle}
                />
            )}
        </>
    );
}

export default AcademicMode;
