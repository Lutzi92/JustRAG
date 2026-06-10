import { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Key, Plus, Trash2, Copy, Check, Eye, EyeOff } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { getApiErrorMessage } from '../../utils/apiError';
import { useTheme } from '../../contexts/ThemeContext';
import { useModalContext } from '../../contexts/ModalContext';

interface ApiKeyEntry {
    id: string;
    name: string;
    keyPrefix: string;
    lastUsedAt: string | null;
    expiresAt: string | null;
    createdAt: string;
}

interface NewlyCreatedKey {
    id: string;
    name: string;
    key: string;
    keyPrefix: string;
}

export default function ApiKeyManager() {
    const { t, language } = useTheme();
    const { showConfirm } = useModalContext();
    const [keys, setKeys] = useState<ApiKeyEntry[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [newKeyName, setNewKeyName] = useState('');
    const [creating, setCreating] = useState(false);
    const [newlyCreated, setNewlyCreated] = useState<NewlyCreatedKey | null>(null);
    const [showNewKey, setShowNewKey] = useState(false);
    const [copied, setCopied] = useState(false);
    const [deletingId, setDeletingId] = useState<string | null>(null);

    const fetchKeys = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/api-keys`);
            setKeys(res.data);
            setError('');
        } catch {
            setError(t('apiKeyLoadError'));
        } finally {
            setLoading(false);
        }
    }, [t]);

    useEffect(() => {
        fetchKeys();
    }, [fetchKeys]);

    const handleCreate = async () => {
        const trimmedName = newKeyName.trim();
        if (!trimmedName) return;

        setCreating(true);
        setError('');
        try {
            const res = await axios.post(`${API_BASE_URL}/api/api-keys`, { name: trimmedName });
            setNewlyCreated({
                id: res.data.id,
                name: res.data.name,
                key: res.data.key,
                keyPrefix: res.data.keyPrefix,
            });
            setShowNewKey(false);
            setCopied(false);
            setNewKeyName('');
            await fetchKeys();
        } catch (e: unknown) {
            setError(getApiErrorMessage(e, t('apiKeyCreateError')));
        } finally {
            setCreating(false);
        }
    };

    const handleDelete = async (id: string) => {
        const confirmed = await showConfirm(t('apiKeyDeleteConfirm'));
        if (!confirmed) return;
        setDeletingId(id);
        setError('');
        try {
            await axios.delete(`${API_BASE_URL}/api/api-keys/${id}`);
            if (newlyCreated?.id === id) {
                setNewlyCreated(null);
            }
            await fetchKeys();
        } catch {
            setError(t('apiKeyDeleteError'));
        } finally {
            setDeletingId(null);
        }
    };

    const handleCopy = async (text: string) => {
        try {
            await navigator.clipboard.writeText(text);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        } catch {
            // fallback: select text in a temporary input
        }
    };

    const formatDate = (dateStr: string | null) => {
        if (!dateStr) return '-';
        return new Date(dateStr).toLocaleDateString(language === 'de' ? 'de-DE' : 'en-US', {
            day: '2-digit',
            month: '2-digit',
            year: 'numeric',
            hour: '2-digit',
            minute: '2-digit',
        });
    };

    return (
        <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem' }}>
                <div style={{ background: 'var(--accent-primary)', color: 'white', padding: '0.5rem', borderRadius: '8px', display: 'flex' }}>
                    <Key size={20} />
                </div>
                <div>
                    <h3 style={{ margin: 0, color: 'var(--text-primary)' }}>{t('apiKeys')}</h3>
                    <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                        {t('apiKeysDescription')}
                    </p>
                </div>
            </div>

            {error && (
                <div style={{
                    padding: '0.75rem 1rem',
                    marginBottom: '1rem',
                    borderRadius: '8px',
                    background: 'rgba(239, 68, 68, 0.1)',
                    color: '#ef4444',
                    border: '1px solid rgba(239, 68, 68, 0.3)',
                    fontSize: '0.9rem',
                }}>
                    {error}
                </div>
            )}

            {newlyCreated && (
                <div style={{
                    padding: '1rem',
                    marginBottom: '1.5rem',
                    borderRadius: '8px',
                    background: 'rgba(34, 197, 94, 0.1)',
                    border: '1px solid rgba(34, 197, 94, 0.3)',
                }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
                        <strong style={{ color: 'var(--text-primary)', fontSize: '0.9rem' }}>
                            {t('apiKeyCreated')}: {newlyCreated.name}
                        </strong>
                        <button
                            onClick={() => setNewlyCreated(null)}
                            style={{
                                background: 'none',
                                border: 'none',
                                color: 'var(--text-secondary)',
                                cursor: 'pointer',
                                fontSize: '0.85rem',
                                textDecoration: 'underline',
                            }}
                        >
                            {t('apiKeyHide')}
                        </button>
                    </div>
                    <p style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', margin: '0 0 0.75rem 0' }}>
                        {t('apiKeyCopyWarning')}
                    </p>
                    <div style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '0.5rem',
                        background: 'var(--bg-primary)',
                        border: '1px solid var(--border-color)',
                        borderRadius: '6px',
                        padding: '0.5rem 0.75rem',
                    }}>
                        <code style={{
                            flex: 1,
                            fontFamily: 'monospace',
                            fontSize: '0.85rem',
                            color: 'var(--text-primary)',
                            wordBreak: 'break-all',
                        }}>
                            {showNewKey ? newlyCreated.key : newlyCreated.keyPrefix + '••••••••••••••••••••••••'}
                        </code>
                        <button
                            onClick={() => setShowNewKey(!showNewKey)}
                            title={showNewKey ? t('apiKeyHide') : t('apiKeyShow')}
                            style={{
                                background: 'none',
                                border: 'none',
                                cursor: 'pointer',
                                color: 'var(--text-secondary)',
                                padding: '4px',
                                display: 'flex',
                            }}
                        >
                            {showNewKey ? <EyeOff size={16} /> : <Eye size={16} />}
                        </button>
                        <button
                            onClick={() => handleCopy(newlyCreated.key)}
                            title={t('copy')}
                            style={{
                                background: 'none',
                                border: 'none',
                                cursor: 'pointer',
                                color: copied ? '#22c55e' : 'var(--text-secondary)',
                                padding: '4px',
                                display: 'flex',
                            }}
                        >
                            {copied ? <Check size={16} /> : <Copy size={16} />}
                        </button>
                    </div>
                </div>
            )}

            {/* Create form */}
            <div style={{
                display: 'flex',
                gap: '0.5rem',
                marginBottom: '1.5rem',
            }}>
                <input
                    type="text"
                    value={newKeyName}
                    onChange={(e) => setNewKeyName(e.target.value)}
                    onKeyDown={(e) => { if (e.key === 'Enter') handleCreate(); }}
                    placeholder={t('apiKeyNamePlaceholder')}
                    maxLength={100}
                    style={{
                        flex: 1,
                        padding: '0.6rem 0.75rem',
                        borderRadius: '8px',
                        border: '1px solid var(--border-color)',
                        background: 'var(--bg-primary)',
                        color: 'var(--text-primary)',
                        fontSize: '0.9rem',
                        outline: 'none',
                    }}
                />
                <button
                    onClick={handleCreate}
                    disabled={creating || !newKeyName.trim()}
                    style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '0.4rem',
                        padding: '0.6rem 1rem',
                        borderRadius: '8px',
                        border: 'none',
                        background: 'var(--accent-primary)',
                        color: 'white',
                        cursor: creating || !newKeyName.trim() ? 'not-allowed' : 'pointer',
                        opacity: creating || !newKeyName.trim() ? 0.6 : 1,
                        fontSize: '0.9rem',
                        fontWeight: 500,
                        whiteSpace: 'nowrap',
                    }}
                >
                    <Plus size={16} />
                    {creating ? t('creating') : t('create')}
                </button>
            </div>

            {/* Keys table */}
            {loading ? (
                <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
                    {t('loading')}
                </div>
            ) : keys.length === 0 ? (
                <div style={{
                    textAlign: 'center',
                    padding: '2rem',
                    color: 'var(--text-secondary)',
                    fontSize: '0.9rem',
                }}>
                    {t('apiKeyNone')}
                </div>
            ) : (
                <div style={{ overflowX: 'auto' }}>
                    <table style={{
                        width: '100%',
                        borderCollapse: 'collapse',
                        fontSize: '0.9rem',
                    }}>
                        <thead>
                            <tr style={{ borderBottom: '2px solid var(--border-color)' }}>
                                <th style={{ textAlign: 'left', padding: '0.6rem 0.75rem', color: 'var(--text-secondary)', fontWeight: 500, fontSize: '0.85rem' }}>{t('apiKeyColumnName')}</th>
                                <th style={{ textAlign: 'left', padding: '0.6rem 0.75rem', color: 'var(--text-secondary)', fontWeight: 500, fontSize: '0.85rem' }}>{t('apiKeyColumnKey')}</th>
                                <th style={{ textAlign: 'left', padding: '0.6rem 0.75rem', color: 'var(--text-secondary)', fontWeight: 500, fontSize: '0.85rem' }}>{t('apiKeyColumnCreated')}</th>
                                <th style={{ textAlign: 'left', padding: '0.6rem 0.75rem', color: 'var(--text-secondary)', fontWeight: 500, fontSize: '0.85rem' }}>{t('apiKeyColumnLastUsed')}</th>
                                <th style={{ textAlign: 'right', padding: '0.6rem 0.75rem', color: 'var(--text-secondary)', fontWeight: 500, fontSize: '0.85rem' }}></th>
                            </tr>
                        </thead>
                        <tbody>
                            {keys.map((k) => (
                                <tr key={k.id} style={{ borderBottom: '1px solid var(--border-color)' }}>
                                    <td style={{ padding: '0.6rem 0.75rem', color: 'var(--text-primary)' }}>
                                        {k.name}
                                    </td>
                                    <td style={{ padding: '0.6rem 0.75rem' }}>
                                        <code style={{
                                            fontFamily: 'monospace',
                                            fontSize: '0.85rem',
                                            color: 'var(--text-secondary)',
                                            background: 'var(--bg-primary)',
                                            padding: '0.15rem 0.4rem',
                                            borderRadius: '4px',
                                        }}>
                                            {k.keyPrefix}••••••••
                                        </code>
                                    </td>
                                    <td style={{ padding: '0.6rem 0.75rem', color: 'var(--text-secondary)' }}>
                                        {formatDate(k.createdAt)}
                                    </td>
                                    <td style={{ padding: '0.6rem 0.75rem', color: 'var(--text-secondary)' }}>
                                        {formatDate(k.lastUsedAt)}
                                    </td>
                                    <td style={{ padding: '0.6rem 0.75rem', textAlign: 'right' }}>
                                        <button
                                            onClick={() => handleDelete(k.id)}
                                            disabled={deletingId === k.id}
                                            title={t('delete')}
                                            style={{
                                                background: 'none',
                                                border: 'none',
                                                cursor: deletingId === k.id ? 'not-allowed' : 'pointer',
                                                color: deletingId === k.id ? 'var(--text-secondary)' : '#ef4444',
                                                padding: '4px',
                                                display: 'inline-flex',
                                                opacity: deletingId === k.id ? 0.5 : 1,
                                            }}
                                        >
                                            <Trash2 size={16} />
                                        </button>
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                </div>
            )}
        </div>
    );
}
