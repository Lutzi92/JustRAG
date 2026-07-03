import { useState, useEffect, useCallback, useMemo, useRef, type FormEvent } from 'react';
import axios from 'axios';
import { motion } from 'framer-motion';
import { Play, RefreshCw, Download, Copy, Trash2, BarChart3, X, AlertCircle, Check, Upload } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import { getApiErrorMessage } from '../../utils/apiError';
import { fetchKbAgents, type KbAgentOption } from '../agents/api';

// Types mirror the backend DTOs (internal/admineval/types.go).
interface AggregateSummary {
    count: number;
    mean_recall: number;
    mrr: number;
}

interface RunSummary {
    id: string;
    label: string;
    status: 'queued' | 'running' | 'completed' | 'failed';
    created_at: string;
    started_at?: string;
    finished_at?: string;
    kb_id: string;
    kb_name?: string;
    judge_enabled: boolean;
    aggregate?: AggregateSummary;
    route_mean_recall?: Record<string, number>;
    error_message?: string;
}

interface ListRunsResponse {
    runs: RunSummary[];
    total: number;
}

interface GoldenSet {
    id: string;
    name: string;
    description?: string;
    content_hash: string;
    question_count: number;
    created_at: string;
}

interface ListGoldenSetsResponse {
    golden_sets: GoldenSet[];
}

interface CreateGoldenSetResponse {
    id: string;
    name: string;
    content_hash: string;
    question_count: number;
    created_at: string;
}

interface AdminEvalTabProps {
  /** API base for eval endpoints.
   *  Admin (default): the global admin eval prefix.
   *  KB-scoped: `/api/kb/${kbId}/eval`. */
  basePath?: string;
  /** When set, the tab is KB-scoped: kb_id pickers are hidden and kb_id is
   *  taken from the path, not the form. */
  kbId?: string;
}

export default function AdminEvalTab({ basePath = '/api/admin/eval', kbId }: AdminEvalTabProps) {
    const reducedMotion = useReducedMotion();
    const { t } = useTheme();
    const toast = useToast();

    // State: kick-off form
    const [label, setLabel] = useState('');
    const [formKbId, setFormKbId] = useState('');
    const [selectedGoldenSetId, setSelectedGoldenSetId] = useState<string>('');
    const [judgeEnabled, setJudgeEnabled] = useState(true);
    const [topK, setTopK] = useState(10);
    const [kickOffLoading, setKickOffLoading] = useState(false);
    const [selectedTeamId, setSelectedTeamId] = useState('');
    const [kbTeams, setKbTeams] = useState<KbAgentOption[]>([]);

    // State: golden sets
    const [goldenSets, setGoldenSets] = useState<GoldenSet[]>([]);
    const [uploadName, setUploadName] = useState('');
    const [uploadDescription, setUploadDescription] = useState('');
    const [uploadFile, setUploadFile] = useState<File | null>(null);
    const [uploadLoading, setUploadLoading] = useState(false);
    const [uploadKbId, setUploadKbId] = useState('');
    const fileInputRef = useRef<HTMLInputElement>(null);

    // State: generate from corpus
    const [genKbId, setGenKbId] = useState('');
    const [genName, setGenName] = useState('');
    const [genLang, setGenLang] = useState<'de' | 'en'>('de');
    const [genLookup, setGenLookup] = useState(20);
    const [genComplex, setGenComplex] = useState(10);
    const [genEnum, setGenEnum] = useState(5);
    const [genMultihop, setGenMultihop] = useState(5);
    const [genJobs, setGenJobs] = useState<Array<{ id: string; status: string; error?: string; golden_set_id?: string }>>([]);

    // State: history + pagination
    const [runs, setRuns] = useState<RunSummary[]>([]);
    const [total, setTotal] = useState(0);
    const [offset, setOffset] = useState(0);
    const [statusFilter, setStatusFilter] = useState<string>('');
    const [listLoading, setListLoading] = useState(false);

    // State: compare
    const [compareAId, setCompareAId] = useState<string>('');
    const [compareBId, setCompareBId] = useState<string>('');
    const [compareMarkdown, setCompareMarkdown] = useState<string>('');
    const [compareLoading, setCompareLoading] = useState(false);

    // Fetch golden sets
    const fetchGoldenSets = useCallback(async () => {
        try {
            const response = await axios.get<ListGoldenSetsResponse>(
                `${API_BASE_URL}${basePath}/golden-sets`
            );
            setGoldenSets(response.data.golden_sets || []);
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalGoldenSetsFetchFailed')));
        }
    }, [basePath, t, toast]);

    useEffect(() => {
        fetchGoldenSets();
    }, [fetchGoldenSets]);

    // Team select: only meaningful once a KB is in play (path-scoped kbId,
    // or an explicit kb_id typed into the admin-scope form).
    useEffect(() => {
        // Clear on every KB change (including formKbId edits) so switching
        // KB-A -> KB-B never resubmits KB-A's team id against KB-B; the
        // backend already 400s that mismatch, this just avoids the
        // confusing error by not offering a stale selection in the first
        // place.
        setSelectedTeamId('');
        const effectiveKb = kbId || formKbId;
        if (!effectiveKb) { setKbTeams([]); return; }
        fetchKbAgents(effectiveKb).then(o => setKbTeams(o.teams)).catch(() => setKbTeams([]));
    }, [kbId, formKbId]);

    // Fetch generation jobs
    const fetchGenJobs = useCallback(async () => {
        try {
            const res = await axios.get<{ jobs: Array<{ id: string; status: string; error?: string; golden_set_id?: string }> }>(
                `${API_BASE_URL}${basePath}/golden-sets/jobs`
            );
            setGenJobs(res.data.jobs || []);
        } catch { /* non-fatal */ }
    }, [basePath]);

    const genInFlight = useMemo(() => genJobs.some(j => j.status === 'queued' || j.status === 'running'), [genJobs]);

    useEffect(() => {
        fetchGenJobs();
        if (!genInFlight) return;
        const iv = setInterval(() => { fetchGenJobs(); fetchGoldenSets(); }, 5000);
        return () => clearInterval(iv);
    }, [fetchGenJobs, fetchGoldenSets, genInFlight]);

    const handleGenerate = async (e: FormEvent) => {
        e.preventDefault();
        if ((!kbId && !genKbId.trim()) || !genName.trim()) { toast.error(t('evalGenFailed')); return; }
        try {
            await axios.post(`${API_BASE_URL}${basePath}/golden-sets/generate`, {
                ...(kbId ? {} : { kb_id: genKbId.trim() }),
                name: genName.trim(), lang: genLang,
                counts: { lookup: genLookup, complex: genComplex, enumeration: genEnum, multihop: genMultihop },
            });
            toast.success(t('evalGenQueued'));
            fetchGenJobs();
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalGenFailed')));
        }
    };

    const handleDownloadGoldenSet = async (id: string, name: string) => {
        try {
            const res = await axios.get<{ content?: unknown[] }>(`${API_BASE_URL}${basePath}/golden-sets/${id}`);
            const lines = (res.data.content || []).map(q => JSON.stringify(q)).join('\n');
            const blob = new Blob([lines], { type: 'application/x-ndjson' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url; a.download = `${name}.jsonl`; a.click();
            URL.revokeObjectURL(url);
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalGoldenSetsFetchFailed')));
        }
    };

    // Fetch list — axios has a global Authorization header set by App.tsx; no per-call header needed.
    const fetchRuns = useCallback(async () => {
        setListLoading(true);
        try {
            const params = new URLSearchParams();
            params.set('limit', '50');
            params.set('offset', String(offset));
            if (statusFilter) params.set('status', statusFilter);
            const response = await axios.get<ListRunsResponse>(
                `${API_BASE_URL}${basePath}/runs?${params.toString()}`
            );
            setRuns(response.data.runs);
            setTotal(response.data.total);
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalFetchFailed')));
        } finally {
            setListLoading(false);
        }
    }, [basePath, offset, statusFilter, toast, t]);

    // Poll while any run is queued/running
    const hasInFlight = useMemo(() => runs.some(r => r.status === 'queued' || r.status === 'running'), [runs]);
    useEffect(() => {
        fetchRuns();
        if (!hasInFlight) return;
        const interval = setInterval(fetchRuns, 5000);
        return () => clearInterval(interval);
    }, [fetchRuns, hasInFlight]);

    const handleUpload = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!uploadFile || !uploadName.trim()) {
            toast.error(t('evalGoldenSetMissingFields'));
            return;
        }
        // In admin (non-KB-scoped) mode, kb_id is required.
        if (!kbId && !uploadKbId.trim()) {
            toast.error(t('evalGoldenSetMissingFields'));
            return;
        }
        setUploadLoading(true);
        try {
            const formData = new FormData();
            formData.append('name', uploadName.trim());
            if (uploadDescription.trim()) formData.append('description', uploadDescription.trim());
            formData.append('file', uploadFile);
            // KB-scoped mode: kb_id comes from the path (CreateGoldenSetForKB).
            // Admin mode: must be supplied explicitly.
            if (!kbId) formData.append('kb_id', uploadKbId.trim());

            const response = await axios.post<CreateGoldenSetResponse>(
                `${API_BASE_URL}${basePath}/golden-sets`,
                formData,
                { headers: { 'Content-Type': 'multipart/form-data' } }
            );
            toast.success(t('evalGoldenSetUploaded'));
            setUploadName('');
            setUploadDescription('');
            setUploadFile(null);
            setUploadKbId('');
            if (fileInputRef.current) fileInputRef.current.value = '';
            fetchGoldenSets();
            // Auto-select the newly-uploaded set
            setSelectedGoldenSetId(response.data.id);
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalGoldenSetUploadFailed')));
        } finally {
            setUploadLoading(false);
        }
    };

    const handleDeleteGoldenSet = async (id: string, name: string) => {
        if (!window.confirm(`${t('evalGoldenSetConfirmDelete')} "${name}"?`)) return;
        try {
            await axios.delete(`${API_BASE_URL}${basePath}/golden-sets/${id}`);
            toast.success(t('evalGoldenSetDeleted'));
            if (selectedGoldenSetId === id) setSelectedGoldenSetId('');
            fetchGoldenSets();
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalGoldenSetDeleteFailed')));
        }
    };

    const handleKickOff = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!selectedGoldenSetId) {
            toast.error(t('evalSelectGoldenSet'));
            return;
        }
        setKickOffLoading(true);
        try {
            const body: Record<string, unknown> = {
                label,
                judge_enabled: judgeEnabled,
                top_k: topK,
            };
            if (!kbId && formKbId) body.kb_id = formKbId;
            if (selectedGoldenSetId) body.golden_set_id = selectedGoldenSetId;
            if (selectedTeamId) body.team_id = selectedTeamId;
            await axios.post(`${API_BASE_URL}${basePath}/runs`, body);
            toast.success(t('evalKickedOff'));
            setLabel('');
            fetchRuns();
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalKickOffFailed')));
        } finally {
            setKickOffLoading(false);
        }
    };

    const handleDelete = async (id: string, status: RunSummary['status']) => {
        if (status === 'running') {
            toast.error(t('evalCannotDeleteRunning'));
            return;
        }
        if (!window.confirm(t('evalConfirmDelete'))) return;
        try {
            await axios.delete(`${API_BASE_URL}${basePath}/runs/${id}`);
            toast.success(t('evalDeleted'));
            fetchRuns();
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalDeleteFailed')));
        }
    };

    const handleExport = async (id: string, compareWith?: string, download = false) => {
        try {
            const url = compareWith
                ? `${API_BASE_URL}${basePath}/runs/${id}/export?compare_with=${compareWith}`
                : `${API_BASE_URL}${basePath}/runs/${id}/export`;
            const response = await axios.get<string>(url, {
                responseType: 'text',
            });
            if (download) {
                const blob = new Blob([response.data], { type: 'text/markdown' });
                const dlUrl = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = dlUrl;
                a.download = compareWith
                    ? `eval-delta-${id.slice(0, 8)}-${compareWith.slice(0, 8)}.md`
                    : `eval-run-${id.slice(0, 8)}.md`;
                a.click();
                URL.revokeObjectURL(dlUrl);
                toast.success(t('evalDownloaded'));
            } else {
                await navigator.clipboard.writeText(response.data);
                toast.success(t('evalCopied'));
            }
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalExportFailed')));
        }
    };

    const handleCompare = async () => {
        if (!compareAId || !compareBId) {
            toast.error(t('evalSelectBothRuns'));
            return;
        }
        setCompareLoading(true);
        try {
            const response = await axios.get<string>(
                `${API_BASE_URL}${basePath}/runs/${compareAId}/export?compare_with=${compareBId}`,
                { responseType: 'text' }
            );
            setCompareMarkdown(response.data);
        } catch (err) {
            toast.error(getApiErrorMessage(err, t('evalCompareFailed')));
        } finally {
            setCompareLoading(false);
        }
    };

    // Render: three sections
    return (
        <motion.div {...getMotionProps(reducedMotion)} initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="result-card" style={{ padding: '2rem' }}>
            <h3 style={{ marginTop: 0 }}>{t('evalRunner')}</h3>
            <p style={{ opacity: 0.7 }}>{t('evalRunnerDesc')}</p>

            {/* Golden sets panel */}
            <div style={{ marginTop: '2rem', padding: '1.5rem', border: '1px solid var(--border-color)', borderRadius: '8px', background: 'var(--bg-secondary)' }}>
                <h4 style={{ margin: '0 0 0.5rem 0' }}>{t('evalGoldenSets')}</h4>
                <p style={{ opacity: 0.7, fontSize: '0.9rem', margin: '0 0 1rem 0' }}>{t('evalGoldenSetsDesc')}</p>

                {/* Existing sets */}
                {goldenSets.length === 0 ? (
                    <div style={{ opacity: 0.6, fontSize: '0.9rem', padding: '0.5rem 0' }}>{t('evalGoldenSetsEmpty')}</div>
                ) : (
                    <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: '1.5rem' }}>
                        <thead>
                            <tr style={{ borderBottom: '1px solid var(--border-color)' }}>
                                <th style={{ textAlign: 'left', padding: '0.3rem 0.5rem', fontSize: '0.85rem' }}>{t('evalGoldenSetName')}</th>
                                <th style={{ textAlign: 'right', padding: '0.3rem 0.5rem', fontSize: '0.85rem' }}>{t('evalQuestionCount')}</th>
                                <th style={{ textAlign: 'left', padding: '0.3rem 0.5rem', fontSize: '0.85rem' }}>{t('evalStarted')}</th>
                                <th style={{ textAlign: 'left', padding: '0.3rem 0.5rem', fontSize: '0.85rem' }}>{t('evalGoldenSetHash')}</th>
                                <th style={{ textAlign: 'right', padding: '0.3rem 0.5rem', fontSize: '0.85rem' }}>{t('evalActions')}</th>
                            </tr>
                        </thead>
                        <tbody>
                            {goldenSets.map(gs => (
                                <tr key={gs.id} style={{ borderBottom: '1px solid var(--border-color)' }}>
                                    <td style={{ padding: '0.3rem 0.5rem' }}>
                                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
                                            {gs.name}
                                            {(gs.description || '').startsWith('auto-generated from corpus') && (
                                                <span style={{ padding: '0.1rem 0.4rem', borderRadius: '3px', fontSize: '0.7rem', background: 'var(--accent-primary)', color: 'white', opacity: 0.8 }}>{t('evalGenDraftBadge')}</span>
                                            )}
                                        </div>
                                        {gs.description && <div style={{ fontSize: '0.75rem', opacity: 0.6 }}>{gs.description}</div>}
                                    </td>
                                    <td style={{ padding: '0.3rem 0.5rem', textAlign: 'right' }}>{gs.question_count}</td>
                                    <td style={{ padding: '0.3rem 0.5rem', fontSize: '0.85rem' }}>{new Date(gs.created_at).toLocaleString()}</td>
                                    <td style={{ padding: '0.3rem 0.5rem', fontFamily: 'monospace', fontSize: '0.75rem', opacity: 0.7 }}>{gs.content_hash.slice(0, 12)}</td>
                                    <td style={{ padding: '0.3rem 0.5rem', textAlign: 'right' }}>
                                        <button type="button" onClick={() => handleDownloadGoldenSet(gs.id, gs.name)} title={t('evalDownload')} style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0.2rem', color: 'var(--text-primary)' }}>
                                            <Download size={14} />
                                        </button>
                                        <button type="button" onClick={() => handleDeleteGoldenSet(gs.id, gs.name)} title={t('delete')} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#d93535', padding: '0.2rem' }}>
                                            <Trash2 size={14} />
                                        </button>
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                )}

                {/* Generate from corpus */}
                <form onSubmit={handleGenerate} style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', padding: '1rem', background: 'var(--bg-primary)', border: '1px solid var(--border-color)', borderRadius: '4px', marginBottom: '1rem' }}>
                    <div style={{ fontWeight: 600, fontSize: '0.9rem' }}>{t('evalGenerateTitle')}</div>
                    <div style={{ fontSize: '0.85rem', opacity: 0.7 }}>{t('evalGenerateHint')}</div>
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                        {!kbId && (
                        <input
                            type="text"
                            id="gen-kb-id"
                            value={genKbId}
                            onChange={e => setGenKbId(e.target.value)}
                            placeholder="kb_id"
                            style={{ flex: 1, minWidth: '200px', padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)', fontFamily: 'monospace' }}
                        />
                        )}
                        <input
                            type="text"
                            id="gen-name"
                            value={genName}
                            onChange={e => setGenName(e.target.value)}
                            placeholder={t('evalGenName')}
                            style={{ flex: 2, minWidth: '200px', padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                        />
                        <select
                            id="gen-lang"
                            value={genLang}
                            onChange={e => setGenLang(e.target.value as 'de' | 'en')}
                            style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                        >
                            <option value="de">de</option>
                            <option value="en">en</option>
                        </select>
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                        <label style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem', fontSize: '0.85rem' }}>
                            {t('evalGenLookup')}
                            <input type="number" min={0} max={200} value={genLookup} onChange={e => setGenLookup(Number(e.target.value))} style={{ width: '80px', padding: '0.3rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }} />
                        </label>
                        <label style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem', fontSize: '0.85rem' }}>
                            {t('evalGenComplex')}
                            <input type="number" min={0} max={200} value={genComplex} onChange={e => setGenComplex(Number(e.target.value))} style={{ width: '80px', padding: '0.3rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }} />
                        </label>
                        <label style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem', fontSize: '0.85rem' }}>
                            {t('evalGenEnumeration')}
                            <input type="number" min={0} max={200} value={genEnum} onChange={e => setGenEnum(Number(e.target.value))} style={{ width: '80px', padding: '0.3rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }} />
                        </label>
                        <label style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem', fontSize: '0.85rem' }}>
                            {t('evalGenMultiHop')}
                            <input type="number" min={0} max={200} value={genMultihop} onChange={e => setGenMultihop(Number(e.target.value))} style={{ width: '80px', padding: '0.3rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }} />
                        </label>
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
                        <button type="submit" disabled={genInFlight} style={{ padding: '0.4rem 1rem', background: 'var(--accent-primary)', color: 'white', border: 'none', borderRadius: '4px', cursor: genInFlight ? 'not-allowed' : 'pointer', opacity: genInFlight ? 0.5 : 1 }}>
                            {genInFlight ? t('evalGenRunning') : t('evalGenButton')}
                        </button>
                    </div>
                    {genJobs.length > 0 && (
                        <ul style={{ margin: '0.5rem 0 0 0', padding: '0 0 0 1rem', fontSize: '0.85rem', opacity: 0.8 }}>
                            {genJobs.slice(0, 5).map(j => (
                                <li key={j.id}>{j.status}{j.error ? ` — ${j.error}` : ''}</li>
                            ))}
                        </ul>
                    )}
                </form>

                {/* Upload form */}
                <form onSubmit={handleUpload} style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', padding: '1rem', background: 'var(--bg-primary)', border: '1px solid var(--border-color)', borderRadius: '4px' }}>
                    <div style={{ fontWeight: 600, fontSize: '0.9rem' }}>{t('evalGoldenSetUpload')}</div>
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                        {!kbId && (
                        <input
                            type="text"
                            value={uploadKbId}
                            onChange={e => setUploadKbId(e.target.value)}
                            placeholder="kb_id"
                            style={{ flex: 1, minWidth: '200px', padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)', fontFamily: 'monospace' }}
                        />
                        )}
                        <input
                            type="text"
                            value={uploadName}
                            onChange={e => setUploadName(e.target.value)}
                            placeholder={t('evalGoldenSetName')}
                            required
                            maxLength={255}
                            style={{ flex: 1, minWidth: '200px', padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                        />
                        <input
                            type="text"
                            value={uploadDescription}
                            onChange={e => setUploadDescription(e.target.value)}
                            placeholder={t('evalGoldenSetDescription')}
                            style={{ flex: 2, minWidth: '200px', padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                        />
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                        <input
                            ref={fileInputRef}
                            type="file"
                            accept=".jsonl,.ndjson,application/x-ndjson,text/plain"
                            onChange={e => setUploadFile(e.target.files?.[0] || null)}
                            style={{ flex: 1, padding: '0.3rem' }}
                        />
                        <button type="submit" disabled={uploadLoading || !uploadFile || !uploadName.trim()} style={{ padding: '0.4rem 1rem', background: 'var(--accent-primary)', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer', opacity: (uploadLoading || !uploadFile || !uploadName.trim()) ? 0.5 : 1, display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                            <Upload size={14} />
                            {uploadLoading ? t('loading') : t('evalGoldenSetUpload')}
                        </button>
                    </div>
                </form>
            </div>

            {/* Section 1: Kick-off form */}
            <form onSubmit={handleKickOff} style={{ display: 'flex', flexDirection: 'column', gap: '1rem', marginTop: '2rem', padding: '1.5rem', border: '1px solid var(--border-color)', borderRadius: '8px', background: 'var(--bg-secondary)' }}>
                {/* Label */}
                <div className="input-group" style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                    <label htmlFor="eval-label" style={{ fontSize: '0.9rem', opacity: 0.8 }}>{t('evalLabel')}</label>
                    <input id="eval-label" type="text" value={label} onChange={e => setLabel(e.target.value)} maxLength={255} placeholder={t('evalLabelPlaceholder')} style={{ padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-primary)', color: 'var(--text-primary)' }} />
                </div>
                {/* KB ID — hidden when the tab is already scoped to a KB */}
                {!kbId && (
                <div className="input-group" style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                    <label htmlFor="eval-kb-id" style={{ fontSize: '0.9rem', opacity: 0.8 }}>{t('evalKbId')}</label>
                    <input id="eval-kb-id" type="text" value={formKbId} onChange={e => setFormKbId(e.target.value)} placeholder={t('evalKbIdPlaceholder')} style={{ padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-primary)', color: 'var(--text-primary)', fontFamily: 'monospace' }} />
                </div>
                )}
                {/* Golden set selector */}
                <div className="input-group" style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                    <label htmlFor="eval-golden-set" style={{ fontSize: '0.9rem', opacity: 0.8 }}>{t('evalGoldenSet')}</label>
                    <select
                        id="eval-golden-set"
                        value={selectedGoldenSetId}
                        onChange={e => setSelectedGoldenSetId(e.target.value)}
                        required
                        style={{ padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                    >
                        <option value="">{t('evalPickGoldenSet')}</option>
                        {goldenSets.map(gs => (
                            <option key={gs.id} value={gs.id}>{gs.name} ({gs.question_count} {t('evalQuestions')})</option>
                        ))}
                    </select>
                </div>
                {/* Team selector — only shown once a KB is in play (its teams may be empty) */}
                {kbTeams.length > 0 && (
                <div className="input-group" style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                    <label htmlFor="eval-team-id" style={{ fontSize: '0.9rem', opacity: 0.8 }}>{t('evalTeamLabel')}</label>
                    <select
                        id="eval-team-id"
                        value={selectedTeamId}
                        onChange={e => setSelectedTeamId(e.target.value)}
                        style={{ padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                    >
                        <option value="">{t('evalTeamStandard')}</option>
                        {kbTeams.map(tm => (
                            <option key={tm.id} value={tm.id}>{tm.name}</option>
                        ))}
                    </select>
                </div>
                )}
                {/* Top-k + judge + submit row */}
                <div style={{ display: 'flex', gap: '1.5rem', alignItems: 'flex-end', flexWrap: 'wrap' }}>
                    <div className="input-group" style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', width: '100px' }}>
                        <label htmlFor="eval-topk" style={{ fontSize: '0.9rem', opacity: 0.8 }}>{t('evalTopK')}</label>
                        <input id="eval-topk" type="number" min={1} max={100} value={topK} onChange={e => setTopK(parseInt(e.target.value, 10) || 10)} style={{ padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-primary)', color: 'var(--text-primary)' }} />
                    </div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <input id="eval-judge" type="checkbox" checked={judgeEnabled} onChange={e => setJudgeEnabled(e.target.checked)} />
                        <label htmlFor="eval-judge" style={{ cursor: 'pointer' }}>{t('evalJudge')}</label>
                    </div>
                    <button type="submit" disabled={kickOffLoading || hasInFlight || !selectedGoldenSetId} style={{ padding: '0.5rem 1rem', background: 'var(--accent-primary)', color: 'white', border: 'none', borderRadius: '4px', cursor: kickOffLoading || hasInFlight || !selectedGoldenSetId ? 'not-allowed' : 'pointer', opacity: kickOffLoading || hasInFlight || !selectedGoldenSetId ? 0.5 : 1, display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <Play size={16} />
                        {kickOffLoading ? t('evalKickingOff') : t('evalKickOff')}
                    </button>
                </div>
                {/* Warnings */}
                {judgeEnabled && (
                    <div style={{ fontSize: '0.85rem', opacity: 0.7, display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <AlertCircle size={14} />
                        {t('evalJudgeWarning')}
                    </div>
                )}
                {hasInFlight && (
                    <div style={{ fontSize: '0.85rem', opacity: 0.7, color: 'var(--accent-primary)', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <RefreshCw size={14} />
                        {t('evalInFlight')}
                    </div>
                )}
            </form>

            {/* Section 2: History table */}
            <div style={{ marginTop: '2.5rem' }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '1rem' }}>
                    <h4 style={{ margin: 0 }}>{t('evalHistory')}</h4>
                    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                        <select value={statusFilter} onChange={e => { setStatusFilter(e.target.value); setOffset(0); }} style={{ padding: '0.3rem 0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
                            <option value="">{t('evalFilterAll')}</option>
                            <option value="queued">{t('evalStatusQueued')}</option>
                            <option value="running">{t('evalStatusRunning')}</option>
                            <option value="completed">{t('evalStatusCompleted')}</option>
                            <option value="failed">{t('evalStatusFailed')}</option>
                        </select>
                        <button type="button" onClick={fetchRuns} style={{ padding: '0.3rem 0.5rem', background: 'none', border: '1px solid var(--border-color)', borderRadius: '4px', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                            <RefreshCw size={14} /> {t('evalRefresh')}
                        </button>
                    </div>
                </div>
                {listLoading && <div style={{ opacity: 0.6 }}>{t('loading')}...</div>}
                {!listLoading && runs.length === 0 && <div style={{ opacity: 0.6 }}>{t('evalNoRuns')}</div>}
                {!listLoading && runs.length > 0 && (
                    <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                        <thead>
                            <tr style={{ borderBottom: '1px solid var(--border-color)' }}>
                                <th style={{ textAlign: 'left', padding: '0.5rem' }}>{t('evalStatus')}</th>
                                <th style={{ textAlign: 'left', padding: '0.5rem' }}>{t('evalLabel')}</th>
                                <th style={{ textAlign: 'left', padding: '0.5rem' }}>{t('evalStarted')}</th>
                                <th style={{ textAlign: 'right', padding: '0.5rem' }}>{t('evalDuration')}</th>
                                <th style={{ textAlign: 'left', padding: '0.5rem' }}>{t('evalKbName')}</th>
                                <th style={{ textAlign: 'center', padding: '0.5rem' }}>{t('evalJudge')}</th>
                                <th style={{ textAlign: 'left', padding: '0.5rem' }}>{t('evalRecallPerRoute')}</th>
                                <th style={{ textAlign: 'right', padding: '0.5rem' }}>{t('evalActions')}</th>
                            </tr>
                        </thead>
                        <tbody>
                            {runs.map(r => <RunRow key={r.id} run={r} onDelete={handleDelete} onExport={handleExport} onCompareWith={(cmpId) => { setCompareAId(r.id); setCompareBId(cmpId); setCompareMarkdown(''); }} runs={runs} />)}
                        </tbody>
                    </table>
                )}
                {total > 50 && (
                    <div style={{ marginTop: '1rem', display: 'flex', gap: '0.5rem', justifyContent: 'center' }}>
                        <button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 50))}>{t('prev')}</button>
                        <span style={{ padding: '0.3rem 0.5rem' }}>{offset + 1} – {Math.min(offset + 50, total)} / {total}</span>
                        <button type="button" disabled={offset + 50 >= total} onClick={() => setOffset(offset + 50)}>{t('next')}</button>
                    </div>
                )}
            </div>

            {/* Section 3: Compare view (conditional) */}
            {compareAId && compareBId && (
                <div style={{ marginTop: '2.5rem', padding: '1.5rem', border: '1px solid var(--border-color)', borderRadius: '8px' }}>
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '1rem' }}>
                        <h4 style={{ margin: 0 }}>{t('evalCompare')}</h4>
                        <button type="button" onClick={() => { setCompareAId(''); setCompareBId(''); setCompareMarkdown(''); }} style={{ background: 'none', border: 'none', cursor: 'pointer' }}><X size={16} /></button>
                    </div>
                    <div style={{ fontSize: '0.85rem', opacity: 0.7, marginBottom: '1rem' }}>
                        A: <code>{compareAId}</code> → B: <code>{compareBId}</code>
                    </div>
                    {!compareMarkdown && (
                        <button type="button" onClick={handleCompare} disabled={compareLoading} style={{ padding: '0.5rem 1rem', background: 'var(--accent-primary)', color: 'white', border: 'none', borderRadius: '4px', cursor: 'pointer' }}>
                            <BarChart3 size={16} style={{ verticalAlign: 'middle', marginRight: '0.3rem' }} />
                            {compareLoading ? t('loading') : t('evalRunCompare')}
                        </button>
                    )}
                    {compareMarkdown && (
                        <>
                            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
                                <button type="button" onClick={() => navigator.clipboard.writeText(compareMarkdown).then(() => toast.success(t('evalCopied')))} style={{ padding: '0.3rem 0.5rem', background: 'none', border: '1px solid var(--border-color)', borderRadius: '4px', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                                    <Copy size={14} /> {t('evalExportMarkdown')}
                                </button>
                                <button type="button" onClick={() => handleExport(compareAId, compareBId, true)} style={{ padding: '0.3rem 0.5rem', background: 'none', border: '1px solid var(--border-color)', borderRadius: '4px', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                                    <Download size={14} /> {t('evalDownloadMarkdown')}
                                </button>
                            </div>
                            <pre style={{ padding: '1rem', background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', borderRadius: '4px', overflow: 'auto', maxHeight: '600px', fontSize: '0.85rem', whiteSpace: 'pre-wrap' }}>
                                {compareMarkdown}
                            </pre>
                        </>
                    )}
                </div>
            )}
        </motion.div>
    );
}

// Sub-component: single run row in the table
function RunRow({ run, onDelete, onExport, onCompareWith, runs }: { run: RunSummary; onDelete: (id: string, status: RunSummary['status']) => void; onExport: (id: string, cmp?: string, dl?: boolean) => void; onCompareWith: (cmpId: string) => void; runs: RunSummary[] }) {
    const { t } = useTheme();
    const [showComparePicker, setShowComparePicker] = useState(false);
    const otherRuns = runs.filter(r => r.id !== run.id && r.status === 'completed');

    const statusBadge = (s: RunSummary['status']) => {
        const colorMap: Record<RunSummary['status'], string> = { queued: '#888', running: 'var(--accent-primary)', completed: '#2d9d4a', failed: '#d93535' };
        return <span style={{ padding: '0.1rem 0.4rem', borderRadius: '3px', fontSize: '0.75rem', background: colorMap[s], color: 'white' }}>{s}</span>;
    };

    // Elapsed time for running runs: Date.now() is impure, so it is sampled in
    // an effect (ticking every second while the run is in progress) instead of
    // being called during render.
    const [nowMs, setNowMs] = useState<number | null>(null);
    useEffect(() => {
        if (run.status !== 'running' || !run.started_at) return;
        const update = () => setNowMs(Date.now());
        const immediate = setTimeout(update, 0);
        const interval = setInterval(update, 1000);
        return () => { clearTimeout(immediate); clearInterval(interval); };
    }, [run.status, run.started_at]);

    const duration = run.started_at && run.finished_at
        ? `${Math.round((new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000)}s`
        : run.started_at && (run.status === 'running') && nowMs !== null
        ? `${Math.round((nowMs - new Date(run.started_at).getTime()) / 1000)}s …`
        : '—';

    return (
        <tr style={{ borderBottom: '1px solid var(--border-color)' }}>
            <td style={{ padding: '0.5rem' }}>{statusBadge(run.status)}</td>
            <td style={{ padding: '0.5rem' }}>{run.label || <span style={{ opacity: 0.5 }}>({t('evalNoLabel')})</span>}</td>
            <td style={{ padding: '0.5rem', fontSize: '0.85rem' }}>{run.started_at ? new Date(run.started_at).toLocaleString() : '—'}</td>
            <td style={{ padding: '0.5rem', textAlign: 'right', fontSize: '0.85rem' }}>{duration}</td>
            <td style={{ padding: '0.5rem', fontSize: '0.85rem' }}>{run.kb_name || run.kb_id.slice(0, 8)}</td>
            <td style={{ padding: '0.5rem', textAlign: 'center' }}>{run.judge_enabled ? <Check size={14} /> : <X size={14} style={{ opacity: 0.3 }} />}</td>
            <td style={{ padding: '0.5rem' }}>
                {run.route_mean_recall
                    ? Object.entries(run.route_mean_recall).map(([route, val]) => (
                        <span key={route} style={{ display: 'inline-block', marginRight: '0.5rem', fontSize: '0.75rem' }}>
                            {route.slice(0, 3)}: {(val * 100).toFixed(0)}%
                        </span>
                    ))
                    : '—'}
            </td>
            <td style={{ padding: '0.5rem', textAlign: 'right' }}>
                <button type="button" onClick={() => onExport(run.id)} title={t('evalExportSingle')} style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0.2rem' }}><Copy size={14} /></button>
                {run.status === 'completed' && otherRuns.length > 0 && (
                    <>
                        <button type="button" onClick={() => setShowComparePicker(!showComparePicker)} title={t('evalCompareWith')} style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0.2rem' }}><BarChart3 size={14} /></button>
                        {showComparePicker && (
                            <select
                                // eslint-disable-next-line jsx-a11y/no-autofocus -- focus the just-revealed picker so keyboard users land on it and onBlur dismissal works (dialog-like disclosure pattern)
                                autoFocus
                                onChange={e => { onCompareWith(e.target.value); setShowComparePicker(false); }}
                                onBlur={() => setShowComparePicker(false)}
                                style={{ marginLeft: '0.3rem' }}
                            >
                                <option value="">{t('evalPickRun')}</option>
                                {otherRuns.map(r => <option key={r.id} value={r.id}>{r.label || r.id.slice(0, 8)}</option>)}
                            </select>
                        )}
                    </>
                )}
                <button type="button" onClick={() => onDelete(run.id, run.status)} disabled={run.status === 'running'} title={t('delete')} style={{ background: 'none', border: 'none', cursor: run.status === 'running' ? 'not-allowed' : 'pointer', opacity: run.status === 'running' ? 0.3 : 1, padding: '0.2rem', color: '#d93535' }}><Trash2 size={14} /></button>
            </td>
        </tr>
    );
}
