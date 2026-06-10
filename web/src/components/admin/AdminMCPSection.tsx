import { useEffect, useState, useCallback } from 'react';
import { CheckCircle2, AlertCircle, RefreshCw, ChevronDown, ChevronRight, Wrench } from 'lucide-react';
import { API_BASE_URL, authFetch } from '../../api';

// Mirrors mcp.ServerSpec on the backend.
interface ServerSpec {
    name: string;
    url: string;
    headers?: Record<string, string>;
}

// Mirrors mcp.ToolSummary.
interface ToolSummary {
    name: string;
    description?: string;
    origin: string;
}

// Mirrors mcp.ServerStatus.
interface ServerStatus {
    name: string;
    url: string;
    reachable: boolean;
    error?: string;
    tools?: ToolSummary[];
}

// Mirrors adminmcp.StatusResponse.
interface StatusResponse {
    use_mcp_tools: boolean;
    builtins: ToolSummary[];
    servers: ServerStatus[];
    server_spec: ServerSpec[];
}

interface AdminMCPSectionProps {
    siteConfigs: Record<string, string>;
    setSiteConfigs: React.Dispatch<React.SetStateAction<Record<string, string>>>;
}

// Validates and pretty-prints the mcp_servers JSON the user is editing.
// Returns [parsed, error]. An empty string parses as an empty array so a
// fresh deployment doesn't surface a confusing error.
function parseServers(raw: string): [ServerSpec[] | null, string | null] {
    const trimmed = raw.trim();
    if (trimmed === '') return [[], null];
    try {
        const parsed = JSON.parse(trimmed);
        if (!Array.isArray(parsed)) return [null, 'mcp_servers must be a JSON array'];
        for (const s of parsed) {
            if (typeof s !== 'object' || s === null) return [null, 'each entry must be an object'];
            if (typeof s.name !== 'string' || s.name === '') return [null, 'each entry needs a non-empty "name"'];
            if (typeof s.url !== 'string' || s.url === '') return [null, 'each entry needs a non-empty "url"'];
        }
        return [parsed as ServerSpec[], null];
    } catch (e) {
        return [null, (e as Error).message];
    }
}

function ServerStatusBadge({ s }: { s: ServerStatus }) {
    if (s.reachable) {
        return (
            <span style={{
                display: 'inline-flex', alignItems: 'center', gap: '0.3rem',
                padding: '0.2rem 0.5rem', borderRadius: '4px',
                background: 'rgba(40,160,90,0.15)', color: 'var(--success-color, #28a05a)',
                fontSize: '0.8rem',
            }}>
                <CheckCircle2 size={14} /> reachable
            </span>
        );
    }
    return (
        <span style={{
            display: 'inline-flex', alignItems: 'center', gap: '0.3rem',
            padding: '0.2rem 0.5rem', borderRadius: '4px',
            background: 'rgba(220,80,60,0.15)', color: 'var(--error-color, #c33)',
            fontSize: '0.8rem',
        }}>
            <AlertCircle size={14} /> unreachable
        </span>
    );
}

function ToolList({ tools }: { tools: ToolSummary[] }) {
    if (tools.length === 0) return <em style={{ opacity: 0.6 }}>no tools advertised</em>;
    return (
        <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: '0.25rem' }}>
            {tools.map(t => (
                <li key={t.name} style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', fontSize: '0.85rem' }}>
                    <Wrench size={12} aria-hidden="true" />
                    <code style={{ fontFamily: 'var(--font-mono, ui-monospace)', fontWeight: 600 }}>{t.name}</code>
                    {t.description && (
                        <span style={{ opacity: 0.75 }}>— {t.description}</span>
                    )}
                </li>
            ))}
        </ul>
    );
}

function ServerCard({ s }: { s: ServerStatus }) {
    const [open, setOpen] = useState(false);
    return (
        <div style={{
            border: '1px solid var(--border-color)',
            borderRadius: '8px',
            padding: '0.75rem 1rem',
            marginBottom: '0.5rem',
            background: 'var(--bg-secondary, transparent)',
        }}>
            <button
                type="button"
                onClick={() => setOpen(v => !v)}
                style={{
                    background: 'transparent', border: 'none', padding: 0,
                    width: '100%', textAlign: 'left', cursor: 'pointer',
                    display: 'flex', alignItems: 'center', gap: '0.5rem',
                }}
            >
                {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                <strong>{s.name}</strong>
                <span style={{ opacity: 0.7, fontSize: '0.85rem' }}>{s.url}</span>
                <span style={{ flex: 1 }} />
                <ServerStatusBadge s={s} />
            </button>
            {open && (
                <div style={{ marginTop: '0.5rem', paddingTop: '0.5rem', borderTop: '1px dashed var(--border-color)' }}>
                    {s.error && <div style={{ color: 'var(--error-color, #c33)', fontSize: '0.85rem', marginBottom: '0.5rem' }}>{s.error}</div>}
                    {s.reachable && <ToolList tools={s.tools ?? []} />}
                </div>
            )}
        </div>
    );
}

export default function AdminMCPSection({ siteConfigs, setSiteConfigs }: AdminMCPSectionProps) {
    const [status, setStatus] = useState<StatusResponse | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [loading, setLoading] = useState(true);
    const [reloading, setReloading] = useState(false);
    // Local editor state. Initialized from siteConfigs.mcp_servers; written
    // back to siteConfigs on every keystroke so the parent's "Save settings"
    // button persists it through the existing site_config pipeline.
    const [editor, setEditor] = useState(siteConfigs.mcp_servers ?? '[]');
    const [editorErr, setEditorErr] = useState<string | null>(null);

    // No synchronous setState here: `loading` starts true (initial load) and is
    // flipped on by the Refresh button's handler before re-invoking this.
    const fetchStatus = useCallback(() => {
        authFetch(`${API_BASE_URL}/api/admin/mcp/status`)
            .then(async r => {
                if (!r.ok) throw new Error(`HTTP ${r.status}`);
                return r.json() as Promise<StatusResponse>;
            })
            .then(setStatus)
            .catch(e => setError(String(e)))
            .finally(() => setLoading(false));
    }, []);

    const refreshStatus = useCallback(() => {
        setLoading(true);
        setError(null);
        fetchStatus();
    }, [fetchStatus]);

    const reload = useCallback(() => {
        setReloading(true);
        setError(null);
        authFetch(`${API_BASE_URL}/api/admin/mcp/reload`, { method: 'POST' })
            .then(async r => {
                if (!r.ok) {
                    const body = await r.text();
                    throw new Error(`HTTP ${r.status}: ${body}`);
                }
                return r.json() as Promise<StatusResponse>;
            })
            .then(setStatus)
            .catch(e => setError(String(e)))
            .finally(() => setReloading(false));
    }, []);

    useEffect(() => { fetchStatus(); }, [fetchStatus]);

    const onEditorChange = (next: string) => {
        setEditor(next);
        const [, perr] = parseServers(next);
        setEditorErr(perr);
        // Push valid (or empty-trimmed) values back to siteConfigs so
        // the form's Save settings includes them. Invalid JSON is left
        // unwritten — the user sees the inline error.
        if (!perr) {
            setSiteConfigs(prev => ({ ...prev, mcp_servers: next.trim() === '' ? '[]' : next }));
        }
    };

    const useMCPNow = (siteConfigs.chat_use_mcp_tools ?? '') === 'true' || (siteConfigs.chat_use_mcp_tools ?? '') === '1';

    return (
        <div style={{ marginTop: '2rem', padding: '1.25rem', border: '1px solid var(--border-color)', borderRadius: '8px' }}>
            <div style={{ marginBottom: '0.5rem' }}>
                <h4 style={{ margin: 0 }}>Tools (MCP)</h4>
            </div>
            <p style={{ fontSize: '0.85rem', opacity: 0.75, marginTop: 0 }}>
                The plan-execute orchestrator can dispatch tool calls through the MCP registry: the in-process built-ins (kb_search, web_search, memory_*) plus admin-configured remote MCP servers listed below.
            </p>

            {/* Master switch */}
            <label style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer', marginBottom: '1rem' }}>
                <input
                    type="checkbox"
                    checked={useMCPNow}
                    onChange={e => setSiteConfigs(prev => ({ ...prev, chat_use_mcp_tools: e.target.checked ? 'true' : 'false' }))}
                    style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                />
                <span>Route plan-execute searches through the MCP registry (chat_use_mcp_tools)</span>
            </label>

            {/* Server editor */}
            <div style={{ marginBottom: '1rem' }}>
                <label htmlFor="mcp-servers" style={{ display: 'block', marginBottom: '0.4rem', fontWeight: 500 }}>
                    Configured remote servers (mcp_servers, JSON array)
                </label>
                <textarea
                    id="mcp-servers"
                    value={editor}
                    onChange={e => onEditorChange(e.target.value)}
                    rows={Math.min(12, Math.max(4, editor.split('\n').length + 1))}
                    spellCheck={false}
                    style={{
                        width: '100%', padding: '0.75rem',
                        background: 'var(--bg-primary)', color: 'var(--text-primary)',
                        border: `1px solid ${editorErr ? 'var(--error-color, #c33)' : 'var(--border-color)'}`,
                        borderRadius: '6px',
                        fontFamily: 'var(--font-mono, ui-monospace)', fontSize: '0.85rem',
                    }}
                    placeholder={`[\n  {"name": "fetch", "url": "https://my-mcp-server.example.com/mcp", "headers": {"Authorization": "Bearer …"}}\n]`}
                />
                {editorErr && (
                    <div style={{ color: 'var(--error-color, #c33)', fontSize: '0.85rem', marginTop: '0.4rem' }}>
                        Invalid JSON: {editorErr}
                    </div>
                )}
                <p style={{ fontSize: '0.8rem', opacity: 0.65, marginTop: '0.4rem' }}>
                    Format: <code>{`[{name, url, headers?}]`}</code>. Save the form to persist; click <em>Reload registry</em> below to apply without restarting.
                </p>
            </div>

            {/* Live status panel */}
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '0.5rem' }}>
                <strong>Live status</strong>
                <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button
                        type="button"
                        onClick={refreshStatus}
                        disabled={loading || reloading}
                        style={{
                            background: 'transparent', color: 'var(--text-primary)',
                            border: '1px solid var(--border-color)', borderRadius: '6px',
                            padding: '0.3rem 0.7rem', fontSize: '0.85rem',
                            cursor: loading ? 'wait' : 'pointer',
                        }}
                    >
                        {loading ? '…' : 'Refresh'}
                    </button>
                    <button
                        type="button"
                        onClick={reload}
                        disabled={loading || reloading}
                        style={{
                            background: 'var(--accent-primary)', color: 'var(--bg-primary)',
                            border: '1px solid var(--accent-primary)', borderRadius: '6px',
                            padding: '0.3rem 0.7rem', fontSize: '0.85rem',
                            cursor: reloading ? 'wait' : 'pointer',
                            display: 'inline-flex', alignItems: 'center', gap: '0.3rem',
                        }}
                        title="Re-read mcp_servers from site_config and apply to the running registry"
                    >
                        <RefreshCw size={14} className={reloading ? 'animate-spin' : undefined} />
                        {reloading ? 'Applying…' : 'Reload registry'}
                    </button>
                </div>
            </div>
            {error && <div style={{ color: 'var(--error-color, #c33)', fontSize: '0.85rem', marginBottom: '0.5rem' }}>Error: {error}</div>}
            {status && (
                <>
                    {/* Built-ins */}
                    <div style={{ marginBottom: '0.75rem' }}>
                        <div style={{ fontSize: '0.85rem', opacity: 0.85, marginBottom: '0.3rem' }}>
                            Built-in tools ({status.builtins.length})
                        </div>
                        <ToolList tools={status.builtins} />
                    </div>

                    {/* Remote servers */}
                    {status.servers.length === 0 ? (
                        <div style={{ opacity: 0.65, fontSize: '0.85rem' }}>No remote servers configured.</div>
                    ) : (
                        <div>
                            <div style={{ fontSize: '0.85rem', opacity: 0.85, marginBottom: '0.3rem' }}>
                                Remote servers ({status.servers.length})
                            </div>
                            {status.servers.map(s => <ServerCard key={s.name} s={s} />)}
                        </div>
                    )}
                </>
            )}
        </div>
    );
}
