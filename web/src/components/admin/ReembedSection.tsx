import { useState } from 'react';
import axios from 'axios';
import { RefreshCw } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import { useModalContext } from '../../contexts/ModalContext';

export default function ReembedSection() {
    const { t } = useTheme();
    const toast = useToast();
    const { showConfirm } = useModalContext();
    const [loading, setLoading] = useState(false);
    const [result, setResult] = useState<{ queued: number } | null>(null);

    const handleReembedAll = async () => {
        if (!await showConfirm(t('confirmReembedAll'))) return;

        setLoading(true);
        setResult(null);
        try {
            const res = await axios.post(`${API_BASE_URL}/api/admin/reembed-all`);
            setResult({ queued: res.data.queued });
        } catch (err: unknown) {
            toast.error(t('reembedError'));
            console.error(err);
        } finally {
            setLoading(false);
        }
    };

    return (
        <section style={{ marginTop: '2rem', padding: '1.5rem', background: 'var(--bg-secondary)', borderRadius: '12px', border: '1px solid var(--border-color)' }}>
            <h3 style={{ margin: '0 0 0.5rem', color: 'var(--text-primary)' }}>{t('reembedAllTitle')}</h3>
            <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>
                {t('reembedAllDesc')}
            </p>
            <div style={{ display: 'flex', alignItems: 'center', gap: '1rem' }}>
                <button
                    onClick={handleReembedAll}
                    disabled={loading}
                    style={{
                        padding: '0.6rem 1.2rem',
                        background: loading ? 'var(--bg-hover)' : '#C2410C',
                        color: '#fff',
                        border: 'none',
                        borderRadius: '8px',
                        cursor: loading ? 'not-allowed' : 'pointer',
                        display: 'flex',
                        alignItems: 'center',
                        gap: '0.5rem',
                        fontWeight: 500,
                    }}
                >
                    <RefreshCw size={16} className={loading ? 'spin' : ''} />
                    {loading ? t('reembedQueuing') : t('reembedAllButton')}
                </button>
                {result && (
                    <span style={{ color: 'var(--text-secondary)', fontSize: '0.9rem' }}>
                        {t('reembedQueued').replace('{count}', String(result.queued))}
                    </span>
                )}
            </div>
        </section>
    );
}
