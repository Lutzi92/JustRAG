import { useEffect, useState, useCallback } from 'react';
import { API_BASE_URL, authFetch } from '../../api';

// Distribution row mirroring adminagentmetrics.DecisionDistribution.
interface DecisionDistribution {
    total: number;
    by_label: Record<string, number>;
}

// AP-B4 ToolMixEntry: per-tool aggregate from agent_decisions.tool_calls.
interface ToolMixEntry {
    tool: string;
    calls: number;
    median_duration_ms: number;
    error_rate: number;
}

// Response from GET /api/admin/agent-metrics — see
// adminagentmetrics.AgentMetricsResponse for the authoritative shape.
interface AgentMetricsResponse {
    window_seconds: number;
    kb_id?: string;
    agentic_chat: DecisionDistribution;
    plan_execute: DecisionDistribution;
    crag: DecisionDistribution;
    median_hops: number;
    median_rounds: number;
    p95_latency_ms: number;
    tool_mix?: ToolMixEntry[] | null;
}

const WINDOWS: { value: '1h' | '24h' | '7d'; label: string }[] = [
    { value: '1h', label: '1h' },
    { value: '24h', label: '24h' },
    { value: '7d', label: '7d' },
];

// DistributionBars renders a horizontal stacked bar of outcome counts. Empty
// distributions (Total=0) render a placeholder row so the panel doesn't
// collapse — operators want to see "no data yet" rather than a missing
// section.
function DistributionBars({ title, dist }: { title: string; dist: DecisionDistribution }) {
    const entries = Object.entries(dist.by_label).sort((a, b) => b[1] - a[1]);
    const total = dist.total || 0;
    return (
        <div style={{ marginBottom: '1.25rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: '0.4rem' }}>
                <strong>{title}</strong>
                <span style={{ opacity: 0.7, fontSize: '0.85rem' }}>{total} runs</span>
            </div>
            {total === 0 ? (
                <div style={{ opacity: 0.5, fontSize: '0.85rem' }}>—</div>
            ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem' }}>
                    {entries.map(([label, count]) => {
                        const pct = total > 0 ? (count / total) * 100 : 0;
                        return (
                            <div key={label} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.85rem' }}>
                                <span style={{ minWidth: '14rem', fontFamily: 'var(--font-mono, ui-monospace)' }}>{label}</span>
                                <div style={{
                                    flex: 1,
                                    background: 'var(--bg-secondary, rgba(0,0,0,0.05))',
                                    borderRadius: '4px',
                                    overflow: 'hidden',
                                    height: '0.85rem',
                                    border: '1px solid var(--border-color)',
                                }}>
                                    <div style={{
                                        width: `${pct.toFixed(1)}%`,
                                        height: '100%',
                                        background: 'var(--accent-primary, #4f8df9)',
                                    }} />
                                </div>
                                <span style={{ minWidth: '4rem', textAlign: 'right', opacity: 0.85 }}>
                                    {count} ({pct.toFixed(0)}%)
                                </span>
                            </div>
                        );
                    })}
                </div>
            )}
        </div>
    );
}

// ToolMixCard renders the AP-B4 per-tool aggregate. When the array
// is empty (no MCP dispatches in the window), the card stays in the
// DOM but renders a "no tool dispatches yet" placeholder so admins
// know the panel is wired but waiting for traffic — distinguishable
// from a frontend bug that hides the section silently.
function ToolMixCard({ mix }: { mix: ToolMixEntry[] }) {
    const totalCalls = mix.reduce((acc, m) => acc + m.calls, 0);
    return (
        <div style={{ marginTop: '1.5rem', borderTop: '1px solid var(--border-color)', paddingTop: '1rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: '0.5rem' }}>
                <strong>Tool-Mix</strong>
                <span style={{ opacity: 0.7, fontSize: '0.85rem' }}>{totalCalls} dispatches</span>
            </div>
            {mix.length === 0 ? (
                <div style={{ opacity: 0.5, fontSize: '0.85rem' }}>No tool dispatches yet — the tool-aware planner is off, or the window is empty.</div>
            ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.3rem' }}>
                    {mix.map(m => {
                        const pct = totalCalls > 0 ? (m.calls / totalCalls) * 100 : 0;
                        const errPct = (m.error_rate * 100).toFixed(0);
                        return (
                            <div key={m.tool} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.85rem' }}>
                                <span style={{ minWidth: '11rem', fontFamily: 'var(--font-mono, ui-monospace)' }}>{m.tool}</span>
                                <div style={{
                                    flex: 1,
                                    background: 'var(--bg-secondary, rgba(0,0,0,0.05))',
                                    borderRadius: '4px',
                                    overflow: 'hidden',
                                    height: '0.85rem',
                                    border: '1px solid var(--border-color)',
                                }}>
                                    <div style={{
                                        width: `${pct.toFixed(1)}%`,
                                        height: '100%',
                                        background: m.error_rate > 0.1 ? 'var(--warning-color, #d97706)' : 'var(--accent-primary, #4f8df9)',
                                    }} />
                                </div>
                                <span style={{ minWidth: '11rem', textAlign: 'right', opacity: 0.85 }} title={`Median ${Math.round(m.median_duration_ms)}ms · ${errPct}% errors`}>
                                    {m.calls} ({pct.toFixed(0)}%) · {Math.round(m.median_duration_ms)}ms · err {errPct}%
                                </span>
                            </div>
                        );
                    })}
                </div>
            )}
        </div>
    );
}

export default function AdminAgentMetricsCard({ kbId }: { kbId?: string }) {
    const [window, setWindow] = useState<'1h' | '24h' | '7d'>('24h');
    // Fetch state is keyed by window/kbId/refresh-token; loading and error are
    // derived during render so the effect never calls setState synchronously.
    // Stale data stays visible while a newer query is in flight (same as before).
    const [result, setResult] = useState<{ key: string; data: AgentMetricsResponse | null; error: string | null } | null>(null);
    const [refreshToken, setRefreshToken] = useState(0);

    const queryKey = `${window}|${kbId ?? ''}|${refreshToken}`;
    const loading = result?.key !== queryKey;
    const data = result?.data ?? null;
    const error = !loading && result ? result.error : null;

    useEffect(() => {
        let cancelled = false;
        const qs = new URLSearchParams({ window });
        if (kbId) qs.set('kb_id', kbId);
        authFetch(`${API_BASE_URL}/api/admin/agent-metrics?${qs.toString()}`)
            .then(async r => {
                if (!r.ok) throw new Error(`HTTP ${r.status}`);
                return r.json() as Promise<AgentMetricsResponse>;
            })
            .then(d => { if (!cancelled) setResult({ key: queryKey, data: d, error: null }); })
            .catch(e => { if (!cancelled) setResult(prev => ({ key: queryKey, data: prev?.data ?? null, error: String(e) })); });
        return () => { cancelled = true; };
    }, [window, kbId, queryKey]);

    const reload = useCallback(() => setRefreshToken(n => n + 1), []);

    return (
        <div className="result-card" style={{ padding: '1.5rem', marginTop: '1.5rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
                <h4 style={{ margin: 0 }}>Agent decisions (live)</h4>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                    {WINDOWS.map(w => (
                        <button
                            key={w.value}
                            type="button"
                            onClick={() => setWindow(w.value)}
                            style={{
                                background: window === w.value ? 'var(--accent-primary)' : 'var(--bg-secondary)',
                                color: window === w.value ? 'var(--bg-primary)' : 'var(--text-primary)',
                                border: '1px solid var(--border-color)',
                                borderRadius: '6px',
                                padding: '0.3rem 0.7rem',
                                cursor: 'pointer',
                                fontSize: '0.85rem',
                            }}
                        >
                            {w.label}
                        </button>
                    ))}
                    <button
                        type="button"
                        onClick={reload}
                        disabled={loading}
                        style={{
                            background: 'transparent',
                            color: 'var(--text-primary)',
                            border: '1px solid var(--border-color)',
                            borderRadius: '6px',
                            padding: '0.3rem 0.7rem',
                            cursor: loading ? 'wait' : 'pointer',
                            fontSize: '0.85rem',
                        }}
                    >
                        {loading ? '…' : 'Refresh'}
                    </button>
                </div>
            </div>
            {error && <div style={{ color: 'var(--error-color, #c33)', fontSize: '0.85rem', marginBottom: '1rem' }}>Error: {error}</div>}
            {data && (
                <>
                    <DistributionBars title="Agentic chat" dist={data.agentic_chat} />
                    <DistributionBars title="Plan-Execute" dist={data.plan_execute} />
                    <DistributionBars title="CRAG" dist={data.crag} />
                    <div style={{ display: 'flex', gap: '1.5rem', fontSize: '0.85rem', opacity: 0.85, marginTop: '1rem' }}>
                        <span>Median hops: <strong>{data.median_hops.toFixed(1)}</strong></span>
                        <span>Median rounds: <strong>{data.median_rounds.toFixed(1)}</strong></span>
                        <span>P95 latency: <strong>{Math.round(data.p95_latency_ms)} ms</strong></span>
                    </div>
                    <ToolMixCard mix={data.tool_mix ?? []} />
                </>
            )}
        </div>
    );
}
