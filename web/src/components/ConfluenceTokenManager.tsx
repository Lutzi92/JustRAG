import { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { BookOpen, CheckCircle, AlertCircle, Trash2, RefreshCw, Eye, EyeOff } from 'lucide-react';
import { getApiErrorMessage } from '../utils/apiError';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';
import type { ConfluenceConnectionInfo } from '../types';

export default function ConfluenceTokenManager() {
    const { t } = useTheme();
    const { showConfirm } = useModalContext();
    const [connInfo, setConnInfo] = useState<ConfluenceConnectionInfo | null>(null);
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [verifying, setVerifying] = useState(false);
    const [deleting, setDeleting] = useState(false);
    const [error, setError] = useState('');
    const [success, setSuccess] = useState('');
    const [tokenInput, setTokenInput] = useState('');
    const [showToken, setShowToken] = useState(false);
    const [editing, setEditing] = useState(false);

    const fetchConnection = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/confluence/connections`);
            setConnInfo(res.data);
            setError('');
        } catch {
            setError(t('confluenceProfileLoadError'));
        } finally {
            setLoading(false);
        }
    }, [t]);

    useEffect(() => {
        fetchConnection();
    }, [fetchConnection]);

    const showTempSuccess = (msg: string) => {
        setSuccess(msg);
        setTimeout(() => setSuccess(''), 3000);
    };

    const handleSave = async () => {
        const trimmed = tokenInput.trim();
        if (!trimmed) return;

        setSaving(true);
        setError('');
        setSuccess('');
        try {
            const hasExisting = connInfo?.connection;
            if (hasExisting) {
                const res = await axios.put(`${API_BASE_URL}/api/confluence/connections/${hasExisting.id}`, { token: trimmed });
                setConnInfo(prev => prev ? {
                    ...prev,
                    connection: { ...prev.connection!, ...res.data },
                } : prev);
            } else {
                const res = await axios.post(`${API_BASE_URL}/api/confluence/connections`, { token: trimmed });
                setConnInfo(res.data);
            }
            setTokenInput('');
            setEditing(false);
            showTempSuccess(t('confluenceProfileSaved'));
        } catch (e: unknown) {
            setError(getApiErrorMessage(e, t('error')));
        } finally {
            setSaving(false);
        }
    };

    const handleVerify = async () => {
        if (!connInfo?.connection) return;
        setVerifying(true);
        setError('');
        setSuccess('');
        try {
            const res = await axios.post(`${API_BASE_URL}/api/confluence/connections/${connInfo.connection.id}/verify`);
            if (res.data.ok) {
                showTempSuccess(t('confluenceProfileVerified'));
            } else {
                setError(res.data.error || t('confluenceProfileVerifyFailed'));
            }
            await fetchConnection();
        } catch (e: unknown) {
            setError(getApiErrorMessage(e, t('error')));
        } finally {
            setVerifying(false);
        }
    };

    const handleDelete = async () => {
        if (!connInfo?.connection) return;
        const confirmed = await showConfirm(t('confluenceProfileDeleteConfirm'));
        if (!confirmed) return;

        setDeleting(true);
        setError('');
        setSuccess('');
        try {
            await axios.delete(`${API_BASE_URL}/api/confluence/connections/${connInfo.connection.id}`);
            showTempSuccess(t('confluenceProfileDeleted'));
            await fetchConnection();
        } catch (e: unknown) {
            setError(getApiErrorMessage(e, t('error')));
        } finally {
            setDeleting(false);
        }
    };

    const connection = connInfo?.connection;
    const isConnected = !!connection;
    const isDecryptError = connection?.errorMessage?.includes('could not be decrypted') ?? false;

    return (
        <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem' }}>
                <div style={{ background: 'var(--accent-primary)', color: 'white', padding: '0.5rem', borderRadius: '8px', display: 'flex' }}>
                    <BookOpen size={20} />
                </div>
                <div>
                    <h3 style={{ margin: 0, color: 'var(--text-primary)' }}>{t('confluenceProfileTitle')}</h3>
                    <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                        {t('confluenceProfileDescription')}
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

            {success && (
                <div style={{
                    padding: '0.75rem 1rem',
                    marginBottom: '1rem',
                    borderRadius: '8px',
                    background: 'rgba(34, 197, 94, 0.1)',
                    color: '#22c55e',
                    border: '1px solid rgba(34, 197, 94, 0.3)',
                    fontSize: '0.9rem',
                }}>
                    {success}
                </div>
            )}

            {loading ? (
                <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
                    {t('loading')}
                </div>
            ) : isConnected && !editing ? (
                <div>
                    {/* Connection status */}
                    <div style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '0.75rem',
                        padding: '1rem',
                        background: connection.status === 'active'
                            ? 'rgba(34, 197, 94, 0.08)'
                            : 'rgba(239, 68, 68, 0.08)',
                        border: `1px solid ${connection.status === 'active' ? 'rgba(34, 197, 94, 0.3)' : 'rgba(239, 68, 68, 0.3)'}`,
                        borderRadius: '8px',
                        marginBottom: '1rem',
                    }}>
                        {connection.status === 'active' ? (
                            <CheckCircle size={20} color="#22c55e" />
                        ) : (
                            <AlertCircle size={20} color="#ef4444" />
                        )}
                        <div style={{ flex: 1 }}>
                            <div style={{ color: 'var(--text-primary)', fontWeight: 500, fontSize: '0.9rem' }}>
                                {connection.status === 'active' ? t('confluenceProfileStatusActive') : t('confluenceProfileStatusError')}
                            </div>
                            <div style={{ color: 'var(--text-secondary)', fontSize: '0.8rem', marginTop: '0.25rem' }}>
                                {t('confluenceProfileToken')}: <code style={{
                                    fontFamily: 'monospace',
                                    background: 'var(--bg-primary)',
                                    padding: '0.1rem 0.3rem',
                                    borderRadius: '3px',
                                }}>{connection.token}</code>
                            </div>
                            {connection.errorMessage && (
                                <div style={{ color: '#ef4444', fontSize: '0.8rem', marginTop: '0.25rem' }}>
                                    {connection.errorMessage}
                                </div>
                            )}
                            {connection.lastVerifiedAt && (
                                <div style={{ color: 'var(--text-secondary)', fontSize: '0.8rem', marginTop: '0.25rem' }}>
                                    {t('confluenceProfileLastVerified')}: {new Date(connection.lastVerifiedAt).toLocaleString()}
                                </div>
                            )}
                        </div>
                    </div>

                    {/* Action buttons */}
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                        {!isDecryptError && (
                            <>
                                <button
                                    onClick={() => { setEditing(true); setTokenInput(''); }}
                                    style={{
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: '0.4rem',
                                        padding: '0.5rem 1rem',
                                        borderRadius: '8px',
                                        border: '1px solid var(--border-color)',
                                        background: 'var(--bg-secondary)',
                                        color: 'var(--text-primary)',
                                        cursor: 'pointer',
                                        fontSize: '0.85rem',
                                        fontWeight: 500,
                                    }}
                                >
                                    {t('confluenceProfileChangeToken')}
                                </button>
                                <button
                                    onClick={handleVerify}
                                    disabled={verifying}
                                    style={{
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: '0.4rem',
                                        padding: '0.5rem 1rem',
                                        borderRadius: '8px',
                                        border: '1px solid var(--border-color)',
                                        background: 'var(--bg-secondary)',
                                        color: 'var(--text-primary)',
                                        cursor: verifying ? 'not-allowed' : 'pointer',
                                        opacity: verifying ? 0.6 : 1,
                                        fontSize: '0.85rem',
                                        fontWeight: 500,
                                    }}
                                >
                                    <RefreshCw size={14} className={verifying ? 'spinning' : ''} />
                                    {verifying ? t('confluenceProfileVerifying') : t('confluenceProfileVerify')}
                                </button>
                            </>
                        )}
                        <button
                            onClick={handleDelete}
                            disabled={deleting}
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '0.4rem',
                                padding: '0.5rem 1rem',
                                borderRadius: '8px',
                                border: '1px solid rgba(239, 68, 68, 0.3)',
                                background: 'rgba(239, 68, 68, 0.08)',
                                color: '#ef4444',
                                cursor: deleting ? 'not-allowed' : 'pointer',
                                opacity: deleting ? 0.6 : 1,
                                fontSize: '0.85rem',
                                fontWeight: 500,
                            }}
                        >
                            <Trash2 size={14} />
                            {t('delete')}
                        </button>
                    </div>
                </div>
            ) : (
                <div>
                    {/* Token input form */}
                    <p style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', margin: '0 0 0.75rem 0' }}>
                        {t('confluencePatHelp')}
                    </p>
                    <details style={{ marginBottom: '1rem', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                        <summary style={{ cursor: 'pointer', color: 'var(--accent-primary)', fontWeight: 500 }}>
                            {t('confluencePatHowTo')}
                        </summary>
                        <ol style={{ margin: '0.5rem 0 0 0', paddingLeft: '1.25rem', lineHeight: 1.8 }}>
                            <li>{t('confluencePatStep1')}</li>
                            <li>{t('confluencePatStep2')}</li>
                            <li>{t('confluencePatStep3')}</li>
                        </ol>
                    </details>
                    <div style={{ display: 'flex', gap: '0.5rem' }}>
                        <div style={{ flex: 1, position: 'relative' }}>
                            <input
                                type={showToken ? 'text' : 'password'}
                                value={tokenInput}
                                onChange={(e) => setTokenInput(e.target.value)}
                                onKeyDown={(e) => { if (e.key === 'Enter') handleSave(); }}
                                placeholder={t('confluencePatLabel')}
                                style={{
                                    width: '100%',
                                    padding: '0.6rem 2.5rem 0.6rem 0.75rem',
                                    borderRadius: '8px',
                                    border: '1px solid var(--border-color)',
                                    background: 'var(--bg-primary)',
                                    color: 'var(--text-primary)',
                                    fontSize: '0.9rem',
                                    outline: 'none',
                                    boxSizing: 'border-box',
                                }}
                            />
                            <button
                                onClick={() => setShowToken(!showToken)}
                                type="button"
                                style={{
                                    position: 'absolute',
                                    right: '0.5rem',
                                    top: '50%',
                                    transform: 'translateY(-50%)',
                                    background: 'none',
                                    border: 'none',
                                    cursor: 'pointer',
                                    color: 'var(--text-secondary)',
                                    padding: '4px',
                                    display: 'flex',
                                }}
                            >
                                {showToken ? <EyeOff size={16} /> : <Eye size={16} />}
                            </button>
                        </div>
                        <button
                            onClick={handleSave}
                            disabled={saving || !tokenInput.trim()}
                            style={{
                                padding: '0.6rem 1rem',
                                borderRadius: '8px',
                                border: 'none',
                                background: 'var(--accent-primary)',
                                color: 'white',
                                cursor: saving || !tokenInput.trim() ? 'not-allowed' : 'pointer',
                                opacity: saving || !tokenInput.trim() ? 0.6 : 1,
                                fontSize: '0.9rem',
                                fontWeight: 500,
                                whiteSpace: 'nowrap',
                            }}
                        >
                            {saving ? t('saving') : (editing ? t('confluenceProfileChangeToken') : t('connectToConfluence'))}
                        </button>
                        {editing && (
                            <button
                                onClick={() => { setEditing(false); setTokenInput(''); setError(''); }}
                                style={{
                                    padding: '0.6rem 1rem',
                                    borderRadius: '8px',
                                    border: '1px solid var(--border-color)',
                                    background: 'var(--bg-secondary)',
                                    color: 'var(--text-primary)',
                                    cursor: 'pointer',
                                    fontSize: '0.9rem',
                                    fontWeight: 500,
                                    whiteSpace: 'nowrap',
                                }}
                            >
                                {t('cancel')}
                            </button>
                        )}
                    </div>
                </div>
            )}
        </div>
    );
}
