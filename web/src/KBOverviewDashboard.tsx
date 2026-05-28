import { useState, useEffect, useCallback, useMemo } from 'react';
import axios from 'axios';
import { RefreshCw, AlertTriangle, Loader2 } from 'lucide-react';
import { getApiErrorMessage } from './utils/apiError';
import { API_BASE_URL } from './api';

interface QueueStats {
    waiting: number;
    active: number;
    failed: number;
}

interface KBRow {
    id: string;
    name: string;
    ownerName?: string;
    isGlobal: boolean;
    isPublished: boolean;
    fileCount: number;
    totalSizeBytes: number;
    failedFileCount: number;
    processingFileCount: number;
    messageCount: number;
    chatCount: number;
    lastFileUploadAt?: string;
    lastMessageAt?: string;
    createdAt: string;
}

interface OverviewResponse {
    rows: KBRow[];
    queueSummary: Record<string, QueueStats>;
    timestamp: string;
}

type SortKey = keyof Pick<KBRow,
    'name' | 'ownerName' | 'fileCount' | 'totalSizeBytes' | 'failedFileCount' |
    'processingFileCount' | 'messageCount' | 'chatCount' | 'lastFileUploadAt' |
    'lastMessageAt' | 'createdAt'>;

function formatBytes(bytes: number): string {
    if (bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatRelative(iso?: string): string {
    if (!iso) return '—';
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return '—';
    const diffMs = Date.now() - then;
    const day = 24 * 60 * 60 * 1000;
    if (diffMs < 60 * 1000) return 'gerade eben';
    if (diffMs < 60 * 60 * 1000) return `${Math.floor(diffMs / (60 * 1000))}m ago`;
    if (diffMs < day) return `${Math.floor(diffMs / (60 * 60 * 1000))}h ago`;
    return `${Math.floor(diffMs / day)}d ago`;
}

const QUEUE_NAMES = ['rag-quick', 'rag-heavy', 'rag-batch'];

export default function KBOverviewDashboard() {
    const [data, setData] = useState<OverviewResponse | null>(null);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [autoRefresh, setAutoRefresh] = useState(false);
    const [sortKey, setSortKey] = useState<SortKey>('name');
    const [sortAsc, setSortAsc] = useState(true);

    const fetchData = useCallback(async () => {
        setRefreshing(true);
        try {
            const res = await axios.get(`${API_BASE_URL}/api/admin/kb-overview`);
            setData(res.data as OverviewResponse);
            setError(null);
        } catch (err: unknown) {
            setError(getApiErrorMessage(err, 'Fehler beim Laden der KB-Übersicht'));
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    }, []);

    useEffect(() => { fetchData(); }, [fetchData]);

    useEffect(() => {
        if (!autoRefresh) return;
        const interval = setInterval(fetchData, 10000);
        return () => clearInterval(interval);
    }, [autoRefresh, fetchData]);

    const sortedRows = useMemo(() => {
        if (!data) return [];
        const rows = [...data.rows];
        rows.sort((a, b) => {
            const av = a[sortKey];
            const bv = b[sortKey];
            // Nullish values sort last regardless of direction.
            if (av == null && bv == null) return 0;
            if (av == null) return 1;
            if (bv == null) return -1;
            let cmp: number;
            if (typeof av === 'number' && typeof bv === 'number') {
                cmp = av - bv;
            } else {
                cmp = String(av).localeCompare(String(bv));
            }
            return sortAsc ? cmp : -cmp;
        });
        return rows;
    }, [data, sortKey, sortAsc]);

    const toggleSort = (key: SortKey) => {
        if (key === sortKey) {
            setSortAsc(!sortAsc);
        } else {
            setSortKey(key);
            setSortAsc(true);
        }
    };

    const columns: { key: SortKey; label: string }[] = [
        { key: 'name', label: 'Name' },
        { key: 'ownerName', label: 'Owner' },
        { key: 'fileCount', label: 'Files' },
        { key: 'totalSizeBytes', label: 'Size' },
        { key: 'failedFileCount', label: 'Failed' },
        { key: 'processingFileCount', label: 'Processing' },
        { key: 'messageCount', label: 'Messages' },
        { key: 'chatCount', label: 'Chats' },
        { key: 'lastFileUploadAt', label: 'Last upload' },
        { key: 'lastMessageAt', label: 'Last message' },
        { key: 'createdAt', label: 'Created' },
    ];

    const thStyle: React.CSSProperties = {
        textAlign: 'left', padding: '0.6rem 0.75rem', cursor: 'pointer',
        color: 'var(--text-secondary)', fontWeight: 600, whiteSpace: 'nowrap',
        borderBottom: '1px solid var(--border-color)', userSelect: 'none',
    };
    const tdStyle: React.CSSProperties = {
        padding: '0.6rem 0.75rem', color: 'var(--text-primary)',
        borderBottom: '1px solid var(--border-color)', whiteSpace: 'nowrap',
    };
    const cardStyle: React.CSSProperties = {
        background: 'var(--bg-secondary)', border: '1px solid var(--border-color)',
        borderRadius: '12px', padding: '1rem 1.25rem', minWidth: '160px',
    };

    return (
        <section className="admin-content" style={{ color: 'var(--text-primary)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                <h2 style={{ margin: 0 }}>KB Overview</h2>
                <div style={{ display: 'flex', alignItems: 'center', gap: '1rem' }}>
                    <label style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', color: 'var(--text-secondary)' }}>
                        <input type="checkbox" checked={autoRefresh} onChange={(e) => setAutoRefresh(e.target.checked)} />
                        Auto-refresh (10s)
                    </label>
                    <button
                        onClick={fetchData}
                        disabled={refreshing}
                        className="search-button"
                        style={{ background: 'var(--accent-primary)', color: 'white', border: 'none', padding: '0.5rem 1rem', borderRadius: '8px', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: refreshing ? 'default' : 'pointer' }}
                    >
                        {refreshing ? <Loader2 size={16} className="spin" /> : <RefreshCw size={16} />} Refresh
                    </button>
                </div>
            </div>

            {/* Global queue summary */}
            <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginBottom: '2rem' }}>
                {QUEUE_NAMES.map((q) => {
                    const s = data?.queueSummary?.[q] ?? { waiting: 0, active: 0, failed: 0 };
                    return (
                        <div key={q} style={cardStyle}>
                            <div style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', marginBottom: '0.4rem' }}>{q}</div>
                            <div style={{ display: 'flex', gap: '1rem' }}>
                                <span title="waiting">⏳ {s.waiting}</span>
                                <span title="active">▶ {s.active}</span>
                                <span title="failed" style={{ color: s.failed > 0 ? 'var(--error, #e5484d)' : undefined }}>✕ {s.failed}</span>
                            </div>
                        </div>
                    );
                })}
            </div>

            {error && (
                <div style={{ color: 'var(--error, #e5484d)', marginBottom: '1rem' }}>{error}</div>
            )}
            {loading && (
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', color: 'var(--text-secondary)' }}>
                    <Loader2 size={16} className="spin" /> Lädt…
                </div>
            )}

            {!loading && data && (
                <div style={{ overflowX: 'auto' }}>
                    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.9rem' }}>
                        <thead>
                            <tr>
                                {columns.map((c) => (
                                    <th key={c.key} style={thStyle} onClick={() => toggleSort(c.key)}>
                                        {c.label}{sortKey === c.key ? (sortAsc ? ' ▲' : ' ▼') : ''}
                                    </th>
                                ))}
                            </tr>
                        </thead>
                        <tbody>
                            {sortedRows.map((row) => (
                                <tr key={row.id}>
                                    <td style={tdStyle}>
                                        {row.name}
                                        {row.isGlobal && <span style={{ marginLeft: 6, fontSize: '0.7rem', padding: '0.1rem 0.4rem', borderRadius: 4, background: 'var(--accent-primary)', color: 'white' }}>global</span>}
                                        {row.isPublished && <span style={{ marginLeft: 6, fontSize: '0.7rem', padding: '0.1rem 0.4rem', borderRadius: 4, border: '1px solid var(--border-color)' }}>published</span>}
                                    </td>
                                    <td style={tdStyle}>{row.ownerName ?? '—'}</td>
                                    <td style={tdStyle}>{row.fileCount}</td>
                                    <td style={tdStyle}>{formatBytes(row.totalSizeBytes)}</td>
                                    <td style={{ ...tdStyle, color: row.failedFileCount > 0 ? 'var(--error, #e5484d)' : undefined }}>
                                        {row.failedFileCount > 0 && <AlertTriangle size={14} style={{ verticalAlign: 'middle', marginRight: 4 }} />}
                                        {row.failedFileCount}
                                    </td>
                                    <td style={{ ...tdStyle, color: row.processingFileCount > 0 ? 'var(--accent-primary)' : undefined }}>
                                        {row.processingFileCount}
                                    </td>
                                    <td style={tdStyle}>{row.messageCount}</td>
                                    <td style={tdStyle}>{row.chatCount}</td>
                                    <td style={tdStyle} title={row.lastFileUploadAt}>{formatRelative(row.lastFileUploadAt)}</td>
                                    <td style={tdStyle} title={row.lastMessageAt}>{formatRelative(row.lastMessageAt)}</td>
                                    <td style={tdStyle} title={row.createdAt}>{formatRelative(row.createdAt)}</td>
                                </tr>
                            ))}
                            {sortedRows.length === 0 && (
                                <tr><td style={tdStyle} colSpan={columns.length}>Keine Knowledge Bases.</td></tr>
                            )}
                        </tbody>
                    </table>
                </div>
            )}
        </section>
    );
}
