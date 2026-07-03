import { useState } from 'react';
import type { ReactNode } from 'react';
import { ChevronDown, ChevronRight, Search, GitBranch, ArrowRight, AlertTriangle, CheckCircle, Edit3 } from 'lucide-react';
import type { TrajectoryEvent } from '../types';

interface TrajectoryPanelProps {
    trajectory: TrajectoryEvent[];
    /** "de" | "en" — controls the header + stage labels. */
    language?: 'de' | 'en';
    /** When true, the panel renders expanded on first paint. Defaults to false
     *  (collapsed): single-hop trajectories are noise, and the user can opt
     *  into the detail view. */
    defaultExpanded?: boolean;
}

const LABELS = {
    de: {
        title: 'Schritte des Assistenten',
        stages: {
            plan: 'Plan',
            iterate: 'Recherche-Runde',
            hop: 'Suchschritt',
            decision: 'Entscheidung',
            answer: 'Antwort',
            refine_start: 'Faktencheck läuft',
            refine_complete: 'Antwort korrigiert',
            teamRoute: 'Team-Router',
            teamSynthesis: 'Synthese',
        } as Record<string, string>,
        events: {
            singular: 'Schritt',
            plural: 'Schritte',
            findings: 'neue Treffer',
            chunks: 'Belege',
        },
    },
    en: {
        title: 'Reasoning steps',
        stages: {
            plan: 'Plan',
            iterate: 'Iterate',
            hop: 'Search step',
            decision: 'Decision',
            answer: 'Answer',
            refine_start: 'Fact-checking',
            refine_complete: 'Answer corrected',
            teamRoute: 'Team-Router',
            teamSynthesis: 'Synthesis',
        } as Record<string, string>,
        events: {
            singular: 'step',
            plural: 'steps',
            findings: 'new chunks',
            chunks: 'sources',
        },
    },
};

type Labels = (typeof LABELS)['de'];

function stageIcon(stage: string) {
    switch (stage) {
        case 'plan':
            return <GitBranch size={14} aria-hidden="true" />;
        case 'iterate':
        case 'hop':
            return <Search size={14} aria-hidden="true" />;
        case 'decision':
            return <AlertTriangle size={14} aria-hidden="true" />;
        case 'answer':
            return <CheckCircle size={14} aria-hidden="true" />;
        case 'refine_start':
        case 'refine_complete':
            return <Edit3 size={14} aria-hidden="true" />;
        default:
            return <ArrowRight size={14} aria-hidden="true" />;
    }
}

function renderRow(
    evt: TrajectoryEvent,
    key: number | string,
    isLast: boolean,
    labels: Labels,
    language: 'de' | 'en',
    stageLabelOverride?: string,
) {
    const stageLabel = stageLabelOverride ?? (labels.stages[evt.stage] ?? evt.stage);
    const queries = evt.queries && evt.queries.length > 0 ? evt.queries : (evt.query ? [evt.query] : []);
    return (
        <li
            key={key}
            className={`trajectory-event trajectory-stage-${evt.stage}`}
            style={{
                display: 'flex',
                alignItems: 'flex-start',
                gap: '0.5rem',
                paddingBottom: '0.4rem',
                borderBottom: isLast ? 'none' : '1px dashed var(--border-color)',
            }}
        >
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3rem', minWidth: '6.5rem', opacity: 0.85 }}>
                {stageIcon(evt.stage)}
                <strong style={{ fontWeight: 600 }}>{stageLabel}</strong>
                {evt.step ? <span style={{ opacity: 0.7 }}>#{evt.step}</span> : null}
            </span>
            <span style={{ flex: 1, minWidth: 0 }}>
                {queries.length > 0 && (
                    <span style={{ display: 'block', wordBreak: 'break-word' }}>
                        {queries.map((q, qi) => (
                            <em key={qi} style={{ fontStyle: 'italic', opacity: 0.92 }}>
                                {qi > 0 ? ' · ' : ''}“{q}”
                            </em>
                        ))}
                    </span>
                )}
                {evt.decision && (
                    <span
                        className="trajectory-decision"
                        style={{
                            display: 'inline-block',
                            fontFamily: 'var(--font-mono, ui-monospace)',
                            fontSize: '0.78rem',
                            background: 'var(--bg-tertiary, rgba(0,0,0,0.06))',
                            padding: '0.1rem 0.4rem',
                            borderRadius: '4px',
                            marginRight: '0.4rem',
                        }}
                    >
                        {evt.decision}
                    </span>
                )}
                {typeof evt.findings === 'number' && (
                    <span style={{ opacity: 0.75 }}>+{evt.findings} {labels.events.findings}</span>
                )}
                {evt.chunks && evt.chunks.length > 0 && (
                    <span style={{ display: 'block', opacity: 0.75, marginTop: '0.15rem' }}>
                        {evt.chunks.length} {labels.events.chunks}: {evt.chunks.map(c => c.file_name).join(', ')}
                    </span>
                )}
                {evt.reason && (
                    <span style={{ display: 'block', opacity: 0.6, marginTop: '0.15rem' }}>{evt.reason}</span>
                )}
                {evt.stage === 'refine_complete' && evt.diff && evt.diff.length > 0 && (
                    <span
                        data-testid="refine-diff"
                        style={{
                            display: 'block',
                            marginTop: '0.3rem',
                            lineHeight: 1.5,
                            wordBreak: 'break-word',
                        }}
                    >
                        {evt.diff.map((c, di) => {
                            if (c.kind === 'kept') {
                                return <span key={di} style={{ opacity: 0.55 }}>{c.text}</span>;
                            }
                            if (c.kind === 'added') {
                                return (
                                    <mark
                                        key={di}
                                        title={language === 'de' ? 'Korrigiert nach Faktencheck' : 'Corrected after fact check'}
                                        style={{
                                            background: 'rgba(255, 217, 102, 0.45)',
                                            color: 'inherit',
                                            padding: '0 0.1rem',
                                            borderRadius: '2px',
                                        }}
                                    >{c.text}</mark>
                                );
                            }
                            // removed
                            return (
                                <span
                                    key={di}
                                    style={{
                                        textDecoration: 'line-through',
                                        opacity: 0.55,
                                        background: 'rgba(220, 80, 80, 0.18)',
                                        padding: '0 0.1rem',
                                        borderRadius: '2px',
                                    }}
                                >{c.text}</span>
                            );
                        })}
                    </span>
                )}
                {evt.stage === 'refine_complete' && typeof evt.claims_after === 'number' && (
                    <span style={{ display: 'block', opacity: 0.6, marginTop: '0.15rem', fontSize: '0.75rem' }}>
                        {evt.claims_after === -1
                            ? (language === 'de' ? 'Verifier-Fehler nach Korrektur' : 'Verifier error after refine')
                            : `${evt.claims_before ?? 0} → ${evt.claims_after}`}
                    </span>
                )}
            </span>
        </li>
    );
}

function isTeamRoutePlan(evt: TrajectoryEvent): boolean {
    return evt.stage === 'plan' && evt.decision === 'team_route';
}

function isTeamSynthesisAnswer(evt: TrajectoryEvent): boolean {
    return evt.stage === 'answer' && evt.decision === 'team_synthesis';
}

/**
 * Renders team trajectories (router → per-agent hops → synthesis) as a
 * grouped view instead of the flat row list. Any other stages present
 * (e.g. a `decision` event for graph_traversal that preceded the route)
 * still render via the ordinary flat row renderer, in their original
 * position ahead of the group.
 */
function renderTeamGrouped(trajectory: TrajectoryEvent[], labels: Labels, language: 'de' | 'en') {
    const otherEvents: TrajectoryEvent[] = [];
    let teamRouteEvt: TrajectoryEvent | undefined;
    const hopsByAgent = new Map<string, TrajectoryEvent[]>();
    let synthesisEvt: TrajectoryEvent | undefined;

    for (const evt of trajectory) {
        if (isTeamRoutePlan(evt)) {
            teamRouteEvt = evt;
        } else if (evt.stage === 'hop') {
            const agent = evt.query ?? '';
            const bucket = hopsByAgent.get(agent) ?? [];
            bucket.push(evt);
            hopsByAgent.set(agent, bucket);
        } else if (isTeamSynthesisAnswer(evt)) {
            synthesisEvt = evt;
        } else {
            otherEvents.push(evt);
        }
    }

    // Total border-carrying rows for border-bottom placement: flat others +
    // router header + one row per hop + synthesis. Agent-group header rows
    // are EXCLUDED — they always render borderBottom 'none' and never advance
    // the counter, and they can never be the visually last <li> because every
    // group is built from at least one hop event that follows it.
    const hopCount = Array.from(hopsByAgent.values()).reduce((sum, hops) => sum + hops.length, 0);
    const totalRows = otherEvents.length
        + (teamRouteEvt ? 1 : 0)
        + hopCount
        + (synthesisEvt ? 1 : 0);
    let rowIndex = 0;
    const nextIsLast = () => {
        rowIndex += 1;
        return rowIndex === totalRows;
    };

    const rows: ReactNode[] = [];

    otherEvents.forEach((evt, i) => {
        rows.push(renderRow(evt, `other-${i}`, nextIsLast(), labels, language));
    });

    if (teamRouteEvt) {
        const queries = teamRouteEvt.queries ?? [];
        rows.push(
            <li
                key="team-route"
                className="trajectory-event trajectory-stage-plan trajectory-team-route"
                style={{
                    display: 'flex',
                    alignItems: 'flex-start',
                    gap: '0.5rem',
                    paddingBottom: '0.4rem',
                    borderBottom: nextIsLast() ? 'none' : '1px dashed var(--border-color)',
                }}
            >
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3rem', minWidth: '6.5rem', opacity: 0.85 }}>
                    <GitBranch size={14} aria-hidden="true" />
                    <strong style={{ fontWeight: 600 }}>{labels.stages.teamRoute}</strong>
                </span>
                <span style={{ flex: 1, minWidth: 0 }}>
                    {queries.length > 0 && (
                        <span style={{ display: 'block', wordBreak: 'break-word' }}>{queries.join(', ')}</span>
                    )}
                    {teamRouteEvt.reason && (
                        <span style={{ display: 'block', opacity: 0.6, marginTop: '0.15rem' }}>{teamRouteEvt.reason}</span>
                    )}
                </span>
            </li>,
        );
    }

    hopsByAgent.forEach((hops, agent) => {
        rows.push(
            <li
                key={`agent-${agent}`}
                className="trajectory-event trajectory-agent-group"
                style={{
                    display: 'flex',
                    alignItems: 'flex-start',
                    gap: '0.5rem',
                    paddingBottom: '0.2rem',
                    borderBottom: 'none',
                }}
            >
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3rem', minWidth: '6.5rem', opacity: 0.85 }}>
                    <Search size={14} aria-hidden="true" />
                </span>
                <span style={{ flex: 1, minWidth: 0 }}>
                    <strong style={{ fontWeight: 700 }}>{agent}</strong>
                </span>
            </li>,
        );
        hops.forEach((hop, hi) => {
            rows.push(
                <li
                    key={`agent-${agent}-hop-${hi}`}
                    className="trajectory-event trajectory-stage-hop"
                    style={{
                        display: 'flex',
                        alignItems: 'flex-start',
                        gap: '0.5rem',
                        paddingLeft: '1.5rem',
                        paddingBottom: '0.4rem',
                        borderBottom: nextIsLast() ? 'none' : '1px dashed var(--border-color)',
                    }}
                >
                    <span style={{ flex: 1, minWidth: 0 }}>
                        {typeof hop.findings === 'number' && (
                            <span style={{ opacity: 0.75 }}>+{hop.findings} {labels.events.findings}</span>
                        )}
                        {hop.chunks && hop.chunks.length > 0 && (
                            <span style={{ display: 'block', opacity: 0.75, marginTop: '0.15rem' }}>
                                {hop.chunks.length} {labels.events.chunks}: {hop.chunks.map(c => c.file_name).join(', ')}
                            </span>
                        )}
                        {hop.reason && (
                            <span style={{ display: 'block', opacity: 0.6, marginTop: '0.15rem' }}>{hop.reason}</span>
                        )}
                    </span>
                </li>,
            );
        });
    });

    if (synthesisEvt) {
        rows.push(renderRow(synthesisEvt, 'team-synthesis', nextIsLast(), labels, language, labels.stages.teamSynthesis));
    }

    return rows;
}

export function TrajectoryPanel({ trajectory, language = 'de', defaultExpanded = false }: TrajectoryPanelProps) {
    const [expanded, setExpanded] = useState(defaultExpanded);
    if (!trajectory || trajectory.length === 0) return null;

    const labels = LABELS[language] ?? LABELS.de;
    const total = trajectory.length;
    const headerCount = `${total} ${total === 1 ? labels.events.singular : labels.events.plural}`;
    const teamRoute = trajectory.find(isTeamRoutePlan);

    return (
        <div className="trajectory-panel" data-testid="trajectory-panel">
            <button
                type="button"
                className="trajectory-toggle"
                aria-expanded={expanded}
                onClick={() => setExpanded(v => !v)}
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '0.5rem',
                    background: 'transparent',
                    border: '1px solid var(--border-color)',
                    borderRadius: '6px',
                    padding: '0.4rem 0.75rem',
                    color: 'var(--text-secondary, var(--text-primary))',
                    fontSize: '0.85rem',
                    cursor: 'pointer',
                    marginBottom: expanded ? '0.5rem' : '0.75rem',
                }}
            >
                {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                <span style={{ fontWeight: 500 }}>{labels.title}</span>
                <span style={{ opacity: 0.7 }}>· {headerCount}</span>
            </button>

            {expanded && (
                <ol
                    className="trajectory-events"
                    style={{
                        listStyle: 'none',
                        margin: 0,
                        marginBottom: '1rem',
                        padding: '0.5rem 0.75rem',
                        background: 'var(--bg-secondary, rgba(0,0,0,0.03))',
                        border: '1px solid var(--border-color)',
                        borderRadius: '6px',
                        display: 'flex',
                        flexDirection: 'column',
                        gap: '0.4rem',
                        fontSize: '0.85rem',
                    }}
                >
                    {teamRoute
                        ? renderTeamGrouped(trajectory, labels, language)
                        : trajectory.map((evt, i) => renderRow(evt, i, i === trajectory.length - 1, labels, language))}
                </ol>
            )}
        </div>
    );
}
