import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import axios from 'axios';
import { RefreshCw, AlertTriangle, Loader2, ChevronDown, Hourglass, Play, XCircle, Trash2, UserCog, Globe } from 'lucide-react';
import { getApiErrorMessage } from './utils/apiError';
import { formatRelative } from './utils/dates';
import { translations } from './translations';
import { API_BASE_URL } from './api';
import { useTheme } from './contexts/ThemeContext';
import { useAuth } from './contexts/AuthContext';
import { KbDeleteDialog } from './components/admin/KbDeleteDialog';
import { KbPublishDialog } from './components/admin/KbPublishDialog';
import { KbTransferOwnerDialog } from './components/admin/KbTransferOwnerDialog';

interface QueueStats {
    waiting: number;
    active: number;
    failed: number;
}

// Per-source-kind sync status (Wave-4 Task 7 / W4-R9). One entry per kind
// ("rss" | "confluence" | "git") the KB actually has sources of — a healthy
// RSS feed must not hide a git source that has never succeeded.
interface SyncKindStatus {
    kind: string;
    lastSyncAt?: string;
    syncSucceeded: boolean;
    syncFailing: boolean;
    sourceCount: number;
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
    webTurns: number;
    apiTurns: number;
    chatCount: number;
    lastFileUploadAt?: string;
    lastTurnAt?: string;
    createdAt: string;
    // Freshness (Wave-3 Task 5/6). All optional on the wire — a KB with no
    // files, or no RSS/Confluence/git source, sends none of these.
    oldestFileAt?: string;
    staleFileCount?: number;
    staleShare?: number;
    lastSyncAt?: string;
    syncSucceeded?: boolean;
    syncFailing?: boolean;
    syncKinds?: string[];
    // Per-kind breakdown (Wave-4 Task 7). Empty/absent for a KB with no
    // external sources, same as syncKinds above.
    syncByKind?: SyncKindStatus[];
    // RAGAS 24h sample stats (Wave-5 Task 2). Absent for a KB with no
    // samples in the trailing 24h window — distinct from a zeroed block.
    ragas?: RagasStats;
    // Files the ingest prompt-injection screen flagged (Wave-5 Task 6).
    // Always sent, 0 for a KB with no external sources — optional here only
    // so a pod serving the previous image does not break the column.
    injectionFlagged?: number;
}

interface RagasStats {
    n24h: number;
    faithfulness?: number;
    answerRelevance?: number;
    contextPrecision?: number;
}

interface OverviewResponse {
    rows: KBRow[];
    queueSummary: Record<string, QueueStats>;
    timestamp: string;
    // Threshold (days) behind staleFileCount/staleShare above — global
    // kb_stale_days, default 180. Surfaced in the colStaleShare tooltip.
    staleDays?: number;
}

type SortKey = keyof Pick<KBRow,
    'name' | 'ownerName' | 'fileCount' | 'totalSizeBytes' | 'failedFileCount' |
    'processingFileCount' | 'chatCount' | 'createdAt' |
    'oldestFileAt' | 'staleShare' | 'lastSyncAt'>
    | 'lastActivity' | 'activity' | 'ragasN24h';

// n24h is the sort value for the ragasN24h column — nested under row.ragas,
// so it cannot be read via a[sortKey] like the other numeric columns.
function ragasN24h(row: KBRow): number | undefined {
    return row.ragas?.n24h;
}

// "n · F 0.61 / AR 0.98 / CP 0.47" with a dash for any missing metric — a
// judge prompt that failed leaves that one mean nil (see RagasStats' backend
// doc comment), which must not be conflated with a score of exactly zero.
function formatRagasCell(row: KBRow): string {
    if (!row.ragas) return '—';
    const fmt = (v?: number) => (v != null ? v.toFixed(2) : '–');
    return `${row.ragas.n24h} · F ${fmt(row.ragas.faithfulness)} / AR ${fmt(row.ragas.answerRelevance)} / CP ${fmt(row.ragas.contextPrecision)}`;
}

interface ColumnDef {
    key: SortKey;
    label: string;
    numeric?: boolean;
    optional?: boolean;
}

// Most-recent of lastFileUploadAt / lastTurnAt, as a timestamp (NaN if neither).
function mergedActivityTs(row: KBRow): number {
    const a = row.lastFileUploadAt ? new Date(row.lastFileUploadAt).getTime() : NaN;
    const b = row.lastTurnAt ? new Date(row.lastTurnAt).getTime() : NaN;
    if (Number.isNaN(a) && Number.isNaN(b)) return NaN;
    if (Number.isNaN(a)) return b;
    if (Number.isNaN(b)) return a;
    return Math.max(a, b);
}

// The raw ISO string of whichever of the two timestamps is the most recent.
function mergedActivityIso(row: KBRow): string | undefined {
    const a = row.lastFileUploadAt ? new Date(row.lastFileUploadAt).getTime() : NaN;
    const b = row.lastTurnAt ? new Date(row.lastTurnAt).getTime() : NaN;
    if (Number.isNaN(a) && Number.isNaN(b)) return undefined;
    if (Number.isNaN(a)) return row.lastTurnAt;
    if (Number.isNaN(b)) return row.lastFileUploadAt;
    return a >= b ? row.lastFileUploadAt : row.lastTurnAt;
}

// syncKindRank orders a per-kind sync status from worst to best: a kind that
// has never succeeded is worse than one that is currently failing but has
// succeeded before, which is worse than a healthy kind (W4-R9 — the whole
// point is that a single healthy kind must not mask a worse one).
function syncKindRank(k: SyncKindStatus): number {
    if (!k.syncSucceeded) return 0;
    if (k.syncFailing) return 1;
    return 2;
}

// The worst-ranked entry in row.syncByKind, or undefined for a KB with no
// per-kind breakdown (no external sources, or an older backend response).
function worstSyncKind(row: KBRow): SyncKindStatus | undefined {
    if (!row.syncByKind || row.syncByKind.length === 0) return undefined;
    return [...row.syncByKind].sort((a, b) => syncKindRank(a) - syncKindRank(b))[0];
}

// Label for one sync kind. t() returns the KEY when a translation is missing,
// so an unknown kind would render "syncKindLabel_svn" at the operator; fall
// back to the raw kind string instead. The lookup goes against the translation
// table rather than t()'s return value because "did t() find it?" is not
// answerable from the return value alone — the key IS the fallback.
function syncKindLabel(t: (key: string) => string, kind: string): string {
    const key = `syncKindLabel_${kind}`;
    return key in translations ? t(key) : kind;
}

// Tooltip text listing EVERY sync kind with its own last-sync time (raw ISO,
// matching the other columns' title convention) — the cell above shows only
// the worst kind, this is where an operator finds the other ones. Falls back
// to the pre-Wave-4 syncKinds + single lastSyncAt tooltip when no per-kind
// breakdown is present.
function syncTooltip(row: KBRow, t: (key: string) => string): string | undefined {
    if (row.syncByKind && row.syncByKind.length > 0) {
        return row.syncByKind
            .map((k) => {
                const label = syncKindLabel(t, k.kind);
                const when = k.lastSyncAt ?? '—';
                return k.syncSucceeded ? `${label}: ${when}` : `${label}: ${when} (${t('syncNeverSucceeded')})`;
            })
            .join(' · ');
    }
    // syncKinds is the missing half of "5 days ago": which source kinds
    // that timestamp describes (rss / confluence / git).
    return [row.syncKinds?.length ? row.syncKinds.join(', ') : null, row.lastSyncAt]
        .filter(Boolean).join(' · ') || undefined;
}

// Aktivität = every accepted turn on every surface. One combined column
// (web + API) keeps an already-wide table narrow; the split rides the tooltip.
function turnTotal(row: KBRow): number {
    return (row.webTurns ?? 0) + (row.apiTurns ?? 0);
}

// Sort comparator for the "Last sync" column (Wave-4 Task 7 fix round 1):
// the cell displays the WORST kind, so the column must sort by that same
// severity — never-succeeded, then currently-failing, then healthy — before
// falling back to that kind's own timestamp for a tie. Ascending numeric
// order on syncKindRank (0 = worst) is exactly descending order on
// "urgency" (2 - rank), i.e. ascending puts problems on top; the caller's
// sortAsc flip mirrors that for the other direction, same as every other
// numeric column in this table.
//
// A row with no per-kind breakdown (no external sources at all, or an
// older cached response) falls back to row.lastSyncAt directly and is
// treated as the same severity tier as a healthy kind, so it sorts purely
// by timestamp among rows lacking a real breakdown. A row with neither a
// breakdown nor a lastSyncAt has nothing to rank on and sorts last
// regardless of direction — matching the "nullish sorts last" convention
// the generic branch below uses for every other column, which is why the
// direction flip is applied here (not by the caller) and skipped for that
// case specifically.
function compareSyncUrgency(a: KBRow, b: KBRow, sortAsc: boolean): number {
    const wa = worstSyncKind(a);
    const wb = worstSyncKind(b);
    const ta = wa ? wa.lastSyncAt : a.lastSyncAt;
    const tb = wb ? wb.lastSyncAt : b.lastSyncAt;
    const hasA = wa != null || ta != null;
    const hasB = wb != null || tb != null;
    if (!hasA && !hasB) return 0;
    if (!hasA) return 1;
    if (!hasB) return -1;

    const ra = wa ? syncKindRank(wa) : 2;
    const rb = wb ? syncKindRank(wb) : 2;
    const cmp = ra !== rb ? ra - rb : (ta ?? '').localeCompare(tb ?? '');
    return sortAsc ? cmp : -cmp;
}

function formatBytes(bytes: number): string {
    if (bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const QUEUE_NAMES = ['rag-quick', 'rag-heavy', 'rag-batch'];

export default function KBOverviewDashboard() {
    const { t, language } = useTheme();
    const { user } = useAuth();
    // Delete and transfer stay superadmin-only. Publishing does not: it sits on
    // adminChain server-side (POST /api/admin/kb/{id}/publish), so plain system
    // admins get that one action — and therefore the actions column — too.
    const canManage = user?.role === 'superadmin';
    const canPublish = user?.role === 'admin' || user?.role === 'superadmin';
    const showActions = canManage || canPublish;
    const [deleteTarget, setDeleteTarget] = useState<KBRow | null>(null);
    const [publishTarget, setPublishTarget] = useState<KBRow | null>(null);
    const [transferTarget, setTransferTarget] = useState<KBRow | null>(null);
    const [actionBusy, setActionBusy] = useState(false);
    const [actionError, setActionError] = useState<string | null>(null);
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
        oldestFileAt: false,
        staleShare: false,
        lastSyncAt: false,
        ragasN24h: false,
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

    // Publishing flips visibility to 'public' and (server-side) forces
    // is_published = false, so the row's badges change in two ways at once. The
    // local patch keeps the row honest the instant the request returns; the
    // refetch behind it reconciles the derived columns (owner is cleared on
    // publish) without a page reload — same shape as confirmDelete above.
    const confirmPublish = useCallback(async () => {
        if (!publishTarget) return;
        setActionBusy(true);
        try {
            await axios.post(`${API_BASE_URL}/api/admin/kb/${publishTarget.id}/publish`);
            const publishedId = publishTarget.id;
            setData((prev) => prev && {
                ...prev,
                rows: prev.rows.map((r) => r.id === publishedId
                    ? { ...r, isGlobal: true, isPublished: false, ownerName: undefined, ownerId: undefined, ownerUsername: undefined }
                    : r),
            });
            setPublishTarget(null);
            setActionError(null);
            await fetchData();
        } catch (err: unknown) {
            setActionError(getApiErrorMessage(err, t('kbPublishFailed')));
        } finally {
            setActionBusy(false);
        }
    }, [publishTarget, fetchData, t]);

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
            if (sortKey === 'activity') {
                const cmp = turnTotal(a) - turnTotal(b);
                return sortAsc ? cmp : -cmp;
            }
            // 'lastSyncAt' sorts by the same worst-kind severity the cell
            // displays (never-succeeded > failing > ok), not the aggregate
            // MAX(success) timestamp — see compareSyncUrgency.
            if (sortKey === 'lastSyncAt') {
                return compareSyncUrgency(a, b, sortAsc);
            }
            // 'ragasN24h' is nested under row.ragas, so it cannot go through
            // the generic a[sortKey] lookup below.
            if (sortKey === 'ragasN24h') {
                const an = ragasN24h(a);
                const bn = ragasN24h(b);
                if (an == null && bn == null) return 0;
                if (an == null) return 1;
                if (bn == null) return -1;
                const cmp = an - bn;
                return sortAsc ? cmp : -cmp;
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
        { key: 'activity', label: t('colActivity'), numeric: true },
        { key: 'lastActivity', label: t('colLastActivity') },
        { key: 'processingFileCount', label: t('colProcessing'), numeric: true, optional: true },
        { key: 'chatCount', label: t('colChats'), numeric: true, optional: true },
        { key: 'createdAt', label: t('colCreated'), optional: true },
        { key: 'oldestFileAt', label: t('colOldestContent'), optional: true },
        { key: 'staleShare', label: t('colStaleShare'), numeric: true, optional: true },
        { key: 'lastSyncAt', label: t('colLastSync'), optional: true },
        { key: 'ragasN24h', label: t('colRagas'), numeric: true, optional: true },
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
            case 'activity':
                return turnTotal(row);
            case 'processingFileCount':
                return row.processingFileCount;
            case 'chatCount':
                return row.chatCount;
            case 'lastActivity':
                return formatRelative(mergedActivityIso(row), language);
            case 'createdAt':
                return formatRelative(row.createdAt, language);
            case 'oldestFileAt':
                return formatRelative(row.oldestFileAt, language);
            case 'staleShare':
                return row.staleShare != null ? `${Math.round(row.staleShare * 100)}%` : '—';
            case 'lastSyncAt': {
                // W4-R9: with a per-kind breakdown, show the WORST kind
                // (never-succeeded beats currently-failing beats healthy) so
                // one healthy RSS feed cannot hide a git/Confluence source
                // that has never synced. Falls back to the pre-Wave-4
                // aggregate-only rendering when no breakdown is present.
                const worst = worstSyncKind(row);
                if (worst) {
                    const neverSucceeded = !worst.syncSucceeded;
                    const badge = neverSucceeded || worst.syncFailing;
                    return (
                        <>
                            {badge && (
                                <span
                                    data-testid="kb-sync-failing-badge"
                                    title={neverSucceeded ? t('syncNeverSucceeded') : t('kbSyncFailing')}
                                >
                                    <AlertTriangle size={14} style={{ verticalAlign: 'middle', marginRight: 4, color: 'var(--error-text)' }} />
                                </span>
                            )}
                            {syncKindLabel(t, worst.kind)}: {worst.lastSyncAt ? formatRelative(worst.lastSyncAt, language) : '—'}
                        </>
                    );
                }
                if (!row.lastSyncAt) return '—';
                // syncSucceeded=false means the shown time is only the last
                // ATTEMPT (no success yet) — flag it the same way a currently
                // failing streak (syncFailing) is flagged, so an operator does
                // not read either as a healthy recent sync.
                const failing = row.syncFailing || row.syncSucceeded === false;
                return (
                    <>
                        {failing && (
                            <span data-testid="kb-sync-failing-badge" title={t('kbSyncFailing')}>
                                <AlertTriangle size={14} style={{ verticalAlign: 'middle', marginRight: 4, color: 'var(--error-text)' }} />
                            </span>
                        )}
                        {formatRelative(row.lastSyncAt, language)}
                    </>
                );
            }
            case 'ragasN24h':
                return formatRagasCell(row);
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
                                {showActions && (
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
                                            // The name is the one free-text column; letting it
                                            // wrap is what keeps a long KB name from widening
                                            // the table past the viewport and pushing the
                                            // actions column into a horizontal scroll.
                                            if (c.key === 'name') {
                                                cellStyle.whiteSpace = 'normal';
                                                cellStyle.minWidth = '14rem';
                                            }
                                            // The files column doubles as the screening
                            // surface: a flagged file is advisory, so it gets
                            // a tooltip on a count that is already there
                            // rather than a column of its own.
                            const title = c.key === 'fileCount'
                                ? `${t('colInjectionFlagged')}: ${row.injectionFlagged ?? 0}`
                                : c.key === 'lastActivity'
                                                ? mergedActivityIso(row)
                                                : c.key === 'activity'
                                                    ? `Web: ${row.webTurns ?? 0} · API: ${row.apiTurns ?? 0}`
                                                    : c.key === 'createdAt'
                                                        ? row.createdAt
                                                        : c.key === 'oldestFileAt'
                                                            ? row.oldestFileAt
                                                            : c.key === 'staleShare'
                                                                ? `${row.staleFileCount ?? 0}/${row.fileCount} > ${data?.staleDays ?? 180}d`
                                                                : c.key === 'lastSyncAt'
                                                                    ? syncTooltip(row, t)
                                                                    : c.key === 'ragasN24h'
                                                                        ? t('colRagasTooltip')
                                                                        : undefined;
                                            return (
                                                <td key={c.key} style={cellStyle} title={title}>
                                                    {renderCell(row, c.key)}
                                                </td>
                                            );
                                        })}
                                        {showActions && (
                                            <td style={{ ...tdStyle, textAlign: 'right', whiteSpace: 'nowrap' }}>
                                                {/* isGlobal is the generated mirror of (visibility = 'public'),
                                                    so !isGlobal is exactly "still private" — the only state
                                                    Publish accepts (it answers 409 otherwise). The reverse
                                                    direction lives in the global-KB admin tab. */}
                                                {canPublish && !row.isGlobal && (
                                                    <button
                                                        type="button"
                                                        aria-label={t('kbActionPublish')}
                                                        title={t('kbActionPublish')}
                                                        onClick={() => { setActionError(null); setPublishTarget(row); }}
                                                        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '0.25rem' }}
                                                    >
                                                        <Globe size={16} aria-hidden="true" />
                                                    </button>
                                                )}
                                                {canManage && !row.isGlobal && (
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
                                                {canManage && (
                                                    <button
                                                        type="button"
                                                        aria-label={t('kbActionDelete')}
                                                        title={t('kbActionDelete')}
                                                        onClick={() => { setActionError(null); setDeleteTarget(row); }}
                                                        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--error-text)', padding: '0.25rem' }}
                                                    >
                                                        <Trash2 size={16} aria-hidden="true" />
                                                    </button>
                                                )}
                                            </td>
                                        )}
                                    </tr>
                                );
                            })}
                            {sortedRows.length === 0 && (
                                <tr><td style={tdStyle} colSpan={columns.length + (showActions ? 1 : 0)}>{t('kbNoKnowledgeBases')}</td></tr>
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
            {publishTarget && (
                <KbPublishDialog
                    kbName={publishTarget.name}
                    busy={actionBusy}
                    error={actionError}
                    onCancel={() => { setPublishTarget(null); setActionError(null); }}
                    onConfirm={confirmPublish}
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
