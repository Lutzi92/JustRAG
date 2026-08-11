import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import axios from 'axios';
import { RefreshCw, AlertTriangle, Loader2, ChevronDown, Hourglass, Play, XCircle, Trash2, UserCog } from 'lucide-react';
import { getApiErrorMessage } from './utils/apiError';
import { API_BASE_URL } from './api';
import { useTheme } from './contexts/ThemeContext';
import { useAuth } from './contexts/AuthContext';
import { KbDeleteDialog } from './components/admin/KbDeleteDialog';
import { KbTransferOwnerDialog } from './components/admin/KbTransferOwnerDialog';

interface QueueStats {
    waiting: number;
    active: number;
    failed: number;
}

interface KBRow {
    id: string;
    name: string;
    ownerName?: string;
    ownerId?: string;
    ownerUsername?: string;
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
    'processingFileCount' | 'messageCount' | 'chatCount' | 'createdAt'>
    | 'lastActivity';

interface ColumnDef {
    key: SortKey;
    label: string;
    numeric?: boolean;
    optional?: boolean;
}

// Most-recent of lastFileUploadAt / lastMessageAt, as a timestamp (NaN if neither).
function mergedActivityTs(row: KBRow): number {
    const a = row.lastFileUploadAt ? new Date(row.lastFileUploadAt).getTime() : NaN;
    const b = row.lastMessageAt ? new Date(row.lastMessageAt).getTime() : NaN;
    if (Number.isNaN(a) && Number.isNaN(b)) return NaN;
    if (Number.isNaN(a)) return b;
    if (Number.isNaN(b)) return a;
    return Math.max(a, b);
}

// The raw ISO string of whichever of the two timestamps is the most recent.
function mergedActivityIso(row: KBRow): string | undefined {
    const a = row.lastFileUploadAt ? new Date(row.lastFileUploadAt).getTime() : NaN;
    const b = row.lastMessageAt ? new Date(row.lastMessageAt).getTime() : NaN;
    if (Number.isNaN(a) && Number.isNaN(b)) return undefined;
    if (Number.isNaN(a)) return row.lastMessageAt;
    if (Number.isNaN(b)) return row.lastFileUploadAt;
    return a >= b ? row.lastFileUploadAt : row.lastMessageAt;
}

function formatBytes(bytes: number): string {
    if (bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

// Locale-aware relative time (improvement #5): drives "vor 2 Std." / "2 hr. ago"
// from the active UI language instead of the old hand-rolled mixed-language strings.
function formatRelative(iso: string | undefined, rtf: Intl.RelativeTimeFormat): string {
    if (!iso) return '—';
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return '—';
    const diffMs = then - Date.now(); // negative => in the past
    const sec = Math.round(diffMs / 1000);
    const min = Math.round(diffMs / 60000);
    const hr = Math.round(diffMs / 3600000);
    const day = Math.round(diffMs / 86400000);
    if (Math.abs(sec) < 60) return rtf.format(sec, 'second');
    if (Math.abs(min) < 60) return rtf.format(min, 'minute');
    if (Math.abs(hr) < 24) return rtf.format(hr, 'hour');
    return rtf.format(day, 'day');
}

const QUEUE_NAMES = ['rag-quick', 'rag-heavy', 'rag-batch'];

export default function KBOverviewDashboard() {
    const { t, language } = useTheme();
    const { user } = useAuth();
    const canManage = user?.role === 'superadmin';
    const [deleteTarget, setDeleteTarget] = useState<KBRow | null>(null);
    const [transferTarget, setTransferTarget] = useState<KBRow | null>(null);
    const [actionBusy, setActionBusy] = useState(false);
    const [actionError, setActionError] = useState<string | null>(null);
    const rtf = useMemo(() => new Intl.RelativeTimeFormat(language, { numeric: 'auto' }), [language]);
    const [data, setData] = useState<OverviewResponse | null>(null);
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [autoRefresh, setAutoRefresh] = useState(false);
    const [sortKey, setSortKey] = useState<SortKey>('name');
    const [sortAsc, setSortAsc] = useState(true);
    const [search, setSearch] = useState('');
    const [optionalVisible, setOptionalVisible] = useState<Record<string, boolean>>({
        processingFileCount: false,
        chatCount: false,
        createdAt: false,
    });
    const [columnsMenuOpen, setColumnsMenuOpen] = useState(false);
    const columnsMenuRef = useRef<HTMLDivElement | null>(null);

    // Close the column-toggle popover on outside click.
    useEffect(() => {
        if (!columnsMenuOpen) return;
        const onClick = (e: MouseEvent) => {
            if (columnsMenuRef.current && !columnsMenuRef.current.contains(e.target as Node)) {
                setColumnsMenuOpen(false);
            }
        };
        document.addEventListener('mousedown', onClick);
        return () => document.removeEventListener('mousedown', onClick);
    }, [columnsMenuOpen]);

    const fetchData = useCallback(async () => {
        setRefreshing(true);
        try {
            const res = await axios.get(`${API_BASE_URL}/api/admin/kb-overview`);
            setData(res.data as OverviewResponse);
            setError(null);
        } catch (err: unknown) {
            setError(getApiErrorMessage(err, t('kbOverviewLoadError')));
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    }, [t]);

    const confirmDelete = useCallback(async () => {
        if (!deleteTarget) return;
        setActionBusy(true);
        try {
            await axios.delete(`${API_BASE_URL}/api/admin/kbs/${deleteTarget.id}`);
            setDeleteTarget(null);
            setActionError(null);
            await fetchData();
        } catch (err: unknown) {
            setActionError(getApiErrorMessage(err, t('kbDeleteFailed')));
        } finally {
            setActionBusy(false);
        }
    }, [deleteTarget, fetchData, t]);

    const confirmTransfer = useCallback(async (userId: string) => {
        if (!transferTarget) return;
        setActionBusy(true);
        try {
            await axios.patch(`${API_BASE_URL}/api/admin/kbs/${transferTarget.id}/owner`, { userId });
            setTransferTarget(null);
            setActionError(null);
            await fetchData();
        } catch (err: unknown) {
            setActionError(getApiErrorMessage(err, t('kbTransferFailed')));
        } finally {
            setActionBusy(false);
        }
    }, [transferTarget, fetchData, t]);

    useEffect(() => { fetchData(); }, [fetchData]);

    useEffect(() => {
        if (!autoRefresh) return;
        const interval = setInterval(fetchData, 10000);
        return () => clearInterval(interval);
    }, [autoRefresh, fetchData]);

    const sortedRows = useMemo(() => {
        if (!data) return [];
        const needle = search.trim().toLowerCase();
        const rows = data.rows.filter((r) => !needle || r.name.toLowerCase().includes(needle));
        rows.sort((a, b) => {
            // 'lastActivity' is a synthetic column merging upload + message timestamps.
            if (sortKey === 'lastActivity') {
                const at = mergedActivityTs(a);
                const bt = mergedActivityTs(b);
                if (Number.isNaN(at) && Number.isNaN(bt)) return 0;
                if (Number.isNaN(at)) return 1;
                if (Number.isNaN(bt)) return -1;
                return sortAsc ? at - bt : bt - at;
            }
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
    }, [data, sortKey, sortAsc, search]);

    const toggleSort = (key: SortKey) => {
        if (key === sortKey) {
            setSortAsc(!sortAsc);
        } else {
            setSortKey(key);
            setSortAsc(true);
        }
    };

    const ALL_COLUMNS: ColumnDef[] = [
        { key: 'name', label: t('colName') },
        { key: 'ownerName', label: t('colOwner') },
        { key: 'fileCount', label: t('tabFiles'), numeric: true },
        { key: 'totalSizeBytes', label: t('colSize'), numeric: true },
        { key: 'failedFileCount', label: t('colFailed'), numeric: true },
        { key: 'messageCount', label: t('colMessages'), numeric: true },
        { key: 'lastActivity', label: t('colLastActivity') },
        { key: 'processingFileCount', label: t('colProcessing'), numeric: true, optional: true },
        { key: 'chatCount', label: t('colChats'), numeric: true, optional: true },
        { key: 'createdAt', label: t('colCreated'), optional: true },
    ];
    const columns = ALL_COLUMNS.filter((c) => !c.optional || optionalVisible[c.key]);
    const optionalColumns = ALL_COLUMNS.filter((c) => c.optional);

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
        borderRadius: 'var(--shape-lg)', padding: '1rem 1.25rem', minWidth: '160px',
    };
    const queueStat: React.CSSProperties = { display: 'inline-flex', alignItems: 'center', gap: '0.3rem' };

    const renderCell = (row: KBRow, key: SortKey): React.ReactNode => {
        switch (key) {
            case 'name':
                return (
                    <>
                        {row.name}
                        {row.isGlobal && <span style={{ marginLeft: 6, fontSize: '0.7rem', padding: '0.1rem 0.4rem', borderRadius: 'var(--shape-sm)', background: 'var(--accent-primary)', color: 'white' }}>{t('globalBadge')}</span>}
                        {row.isPublished && <span style={{ marginLeft: 6, fontSize: '0.7rem', padding: '0.1rem 0.4rem', borderRadius: 'var(--shape-sm)', border: '1px solid var(--border-color)' }}>{t('published')}</span>}
                    </>
                );
            case 'ownerName':
                return row.ownerName ?? '—';
            case 'fileCount':
                return row.fileCount;
            case 'totalSizeBytes':
                return formatBytes(row.totalSizeBytes);
            case 'failedFileCount':
                return (
                    <>
                        {row.failedFileCount > 0 && <AlertTriangle size={14} style={{ verticalAlign: 'middle', marginRight: 4 }} />}
                        {row.failedFileCount}
                    </>
                );
            case 'messageCount':
                return row.messageCount;
            case 'processingFileCount':
                return row.processingFileCount;
            case 'chatCount':
                return row.chatCount;
            case 'lastActivity':
                return formatRelative(mergedActivityIso(row), rtf);
            case 'createdAt':
                return formatRelative(row.createdAt, rtf);
            default:
                return null;
        }
    };

    return (
        <section className="admin-content" style={{ color: 'var(--text-primary)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                <h2 style={{ margin: 0 }}>{t('adminTabKbOverview')}</h2>
                <div style={{ display: 'flex', alignItems: 'center', gap: '1rem', flexWrap: 'wrap' }}>
                    <input
                        type="text"
                        value={search}
                        onChange={(e) => setSearch(e.target.value)}
                        placeholder={t('kbSearchPlaceholder')}
                        aria-label={t('kbSearchPlaceholder')}
                        style={{
                            background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                            color: 'var(--text-primary)', padding: '0.45rem 0.75rem', borderRadius: 'var(--shape-md)',
                            fontSize: '0.9rem', minWidth: '180px',
                        }}
                    />
                    <div ref={columnsMenuRef} style={{ position: 'relative' }}>
                        <button
                            type="button"
                            onClick={() => setColumnsMenuOpen((v) => !v)}
                            aria-haspopup="true"
                            aria-expanded={columnsMenuOpen}
                            aria-label={t('columnsToggle')}
                            style={{
                                background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                                color: 'var(--text-primary)', padding: '0.45rem 0.75rem', borderRadius: 'var(--shape-md)',
                                display: 'flex', alignItems: 'center', gap: '0.35rem', cursor: 'pointer', fontSize: '0.9rem',
                            }}
                        >
                            {t('columnsToggle')} <ChevronDown size={15} />
                        </button>
                        {columnsMenuOpen && (
                            <div
                                role="menu"
                                style={{
                                    position: 'absolute', right: 0, top: 'calc(100% + 4px)', zIndex: 10,
                                    background: 'var(--bg-secondary)', border: '1px solid var(--border-color)',
                                    borderRadius: 'var(--shape-md)', boxShadow: 'var(--shadow-md)', padding: '0.5rem',
                                    minWidth: '180px', display: 'flex', flexDirection: 'column', gap: '0.25rem',
                                }}
                            >
                                {optionalColumns.map((c) => (
                                    <label key={c.key} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.3rem 0.4rem', cursor: 'pointer', color: 'var(--text-primary)', fontSize: '0.9rem' }}>
                                        <input
                                            type="checkbox"
                                            checked={!!optionalVisible[c.key]}
                                            onChange={(e) => setOptionalVisible((prev) => ({ ...prev, [c.key]: e.target.checked }))}
                                        />
                                        {c.label}
                                    </label>
                                ))}
                            </div>
                        )}
                    </div>
                    <label style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', color: 'var(--text-secondary)' }}>
                        <input type="checkbox" checked={autoRefresh} onChange={(e) => setAutoRefresh(e.target.checked)} />
                        {t('kbAutoRefresh')}
                    </label>
                    <button
                        onClick={fetchData}
                        disabled={refreshing}
                        className="search-button"
                        style={{ background: 'var(--accent-primary)', color: 'white', border: 'none', padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: refreshing ? 'default' : 'pointer' }}
                    >
                        {refreshing ? <Loader2 size={16} className="spin" /> : <RefreshCw size={16} />} {t('refresh')}
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
                                <span title={t('queueWaiting')} style={queueStat}><Hourglass size={14} aria-hidden="true" /> {s.waiting}</span>
                                <span title={t('queueActive')} style={queueStat}><Play size={14} aria-hidden="true" /> {s.active}</span>
                                <span title={t('queueFailed')} style={{ ...queueStat, color: s.failed > 0 ? 'var(--error-text)' : undefined }}><XCircle size={14} aria-hidden="true" /> {s.failed}</span>
                            </div>
                        </div>
                    );
                })}
            </div>

            {error && (
                <div style={{ color: 'var(--error-text)', marginBottom: '1rem' }}>{error}</div>
            )}
            {loading && (
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', color: 'var(--text-secondary)' }}>
                    <Loader2 size={16} className="spin" /> {t('loading')}
                </div>
            )}

            {!loading && data && (
                <div style={{ overflowX: 'auto' }}>
                    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.9rem' }}>
                        <thead>
                            <tr>
                                {columns.map((c) => (
                                    <th
                                        key={c.key}
                                        style={{ ...thStyle, textAlign: c.numeric ? 'right' : 'left' }}
                                        onClick={() => toggleSort(c.key)}
                                    >
                                        {c.label}{sortKey === c.key ? (sortAsc ? ' ▲' : ' ▼') : ''}
                                    </th>
                                ))}
                                {canManage && (
                                    <th style={{ ...thStyle, textAlign: 'right', cursor: 'default' }}>{t('colActions')}</th>
                                )}
                            </tr>
                        </thead>
                        <tbody>
                            {sortedRows.map((row) => {
                                const hasFailed = row.failedFileCount > 0;
                                const rowStyle: React.CSSProperties = hasFailed
                                    ? { background: 'rgba(224,57,57,0.06)' }
                                    : {};
                                return (
                                    <tr key={row.id} style={rowStyle}>
                                        {columns.map((c, idx) => {
                                            const cellStyle: React.CSSProperties = {
                                                ...tdStyle,
                                                textAlign: c.numeric ? 'right' : 'left',
                                            };
                                            if (c.key === 'failedFileCount' && hasFailed) {
                                                cellStyle.color = 'var(--error-text)';
                                            }
                                            if (c.key === 'processingFileCount' && row.processingFileCount > 0) {
                                                cellStyle.color = 'var(--accent-primary)';
                                            }
                                            if (idx === 0 && hasFailed) {
                                                cellStyle.borderLeft = '3px solid var(--error-text)';
                                            }
                                            const title = c.key === 'lastActivity'
                                                ? mergedActivityIso(row)
                                                : c.key === 'createdAt'
                                                    ? row.createdAt
                                                    : undefined;
                                            return (
                                                <td key={c.key} style={cellStyle} title={title}>
                                                    {renderCell(row, c.key)}
                                                </td>
                                            );
                                        })}
                                        {canManage && (
                                            <td style={{ ...tdStyle, textAlign: 'right', whiteSpace: 'nowrap' }}>
                                                {!row.isGlobal && (
                                                    <button
                                                        type="button"
                                                        aria-label={t('kbActionTransfer')}
                                                        title={t('kbActionTransfer')}
                                                        onClick={() => { setActionError(null); setTransferTarget(row); }}
                                                        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '0.25rem' }}
                                                    >
                                                        <UserCog size={16} aria-hidden="true" />
                                                    </button>
                                                )}
                                                <button
                                                    type="button"
                                                    aria-label={t('kbActionDelete')}
                                                    title={t('kbActionDelete')}
                                                    onClick={() => { setActionError(null); setDeleteTarget(row); }}
                                                    style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--error-text)', padding: '0.25rem' }}
                                                >
                                                    <Trash2 size={16} aria-hidden="true" />
                                                </button>
                                            </td>
                                        )}
                                    </tr>
                                );
                            })}
                            {sortedRows.length === 0 && (
                                <tr><td style={tdStyle} colSpan={columns.length + (canManage ? 1 : 0)}>{t('kbNoKnowledgeBases')}</td></tr>
                            )}
                        </tbody>
                    </table>
                </div>
            )}

            {deleteTarget && (
                <KbDeleteDialog
                    kbName={deleteTarget.name}
                    isGlobal={deleteTarget.isGlobal}
                    fileCount={deleteTarget.fileCount}
                    sizeLabel={formatBytes(deleteTarget.totalSizeBytes)}
                    chatCount={deleteTarget.chatCount}
                    busy={actionBusy}
                    error={actionError}
                    onCancel={() => { setDeleteTarget(null); setActionError(null); }}
                    onConfirm={confirmDelete}
                />
            )}
            {transferTarget && (
                <KbTransferOwnerDialog
                    kbName={transferTarget.name}
                    currentOwnerId={transferTarget.ownerId ?? null}
                    currentOwnerName={transferTarget.ownerName ?? null}
                    busy={actionBusy}
                    error={actionError}
                    onCancel={() => { setTransferTarget(null); setActionError(null); }}
                    onConfirm={confirmTransfer}
                />
            )}
        </section>
    );
}
