import { User, ChevronLeft, Shield } from 'lucide-react';
import { useTheme } from './contexts/ThemeContext';
import { useAuth } from './contexts/AuthContext';
import ApiKeyManager from './components/ApiKeys/ApiKeyManager';
import ConfluenceTokenManager from './components/ConfluenceTokenManager';
import MemoryManager from './components/Memory/MemoryManager';

interface ProfileProps {
    user: { id: string; username: string; role: string; firstName?: string; lastName?: string; email?: string };
    onBack: () => void;
    onUpdateUser: (user: { id: string; username: string; role: string }) => void;
}

export default function Profile({ user, onBack }: ProfileProps) {
    const { t } = useTheme();
    const { siteConfigs } = useAuth();
    return (
        <div className="admin-container" style={{ maxWidth: '800px', margin: '0 auto', padding: '2rem' }}>
            <header className="header" style={{ display: 'flex', alignItems: 'center', gap: '1rem', borderBottom: '1px solid var(--border-color)', paddingBottom: '1rem', marginBottom: '2rem' }}>
                <button onClick={onBack} className="back-button" aria-label={t('back')}>
                    <ChevronLeft size={24} />
                </button>
                <div style={{ textAlign: 'left' }}>
                    <h1 style={{ color: 'var(--text-primary)', margin: 0 }}>{t('myProfile')}</h1>
                    <p style={{ color: 'var(--text-secondary)', margin: 0 }}>{t('profileSubtitle')}</p>
                </div>
            </header>

            <div className="result-card" style={{ padding: '2rem' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '1rem', marginBottom: '2rem', padding: '1rem', background: 'var(--bg-secondary)', borderRadius: '12px' }}>
                    <div style={{ background: 'var(--accent-primary)', color: 'white', padding: '1rem', borderRadius: '50%' }}>
                        <User size={32} />
                    </div>
                    <div>
                        <h3 style={{ margin: 0, fontSize: '1.2rem' }}>{user.username}</h3>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginTop: '0.25rem', opacity: 0.7 }}>
                            <Shield size={14} />
                            <span style={{ textTransform: 'capitalize' }}>{user.role}</span>
                        </div>
                    </div>
                </div>

                <div style={{
                    padding: '1rem',
                    marginBottom: '1.5rem',
                    borderRadius: '8px',
                    background: 'var(--tag-bg)',
                    color: 'var(--accent-primary)',
                    border: '1px solid var(--accent-primary)'
                }}>
                    {t('ldapNotice')}
                </div>

                <div className="form-grid">
                    <div className="input-group">
                        <span style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', display: 'block' }}>{t('firstName')}</span>
                        <div style={{
                            padding: '0.75rem 0',
                            color: 'var(--text-primary)',
                            fontSize: '1rem',
                            borderBottom: '1px solid var(--border-color)'
                        }}>
                            {user.firstName || '-'}
                        </div>
                    </div>
                    <div className="input-group">
                        <span style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', display: 'block' }}>{t('lastName')}</span>
                        <div style={{
                            padding: '0.75rem 0',
                            color: 'var(--text-primary)',
                            fontSize: '1rem',
                            borderBottom: '1px solid var(--border-color)'
                        }}>
                            {user.lastName || '-'}
                        </div>
                    </div>
                    <div className="input-group" style={{ gridColumn: 'span 2' }}>
                        <span style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', display: 'block' }}>{t('emailAddress')}</span>
                        <div style={{
                            padding: '0.75rem 0',
                            color: 'var(--text-primary)',
                            fontSize: '1rem',
                            borderBottom: '1px solid var(--border-color)'
                        }}>
                            {user.email || '-'}
                        </div>
                    </div>
                </div>
            </div>

            {(['api-user', 'admin', 'superadmin'].includes(user.role)) && (
                <div className="result-card" style={{ padding: '2rem', marginTop: '1.5rem' }}>
                    <ApiKeyManager />
                </div>
            )}

            {siteConfigs.confluence_enabled === 'true' && (
                <div className="result-card" style={{ padding: '2rem', marginTop: '1.5rem' }}>
                    <ConfluenceTokenManager />
                </div>
            )}

            <div className="result-card" style={{ padding: '2rem', marginTop: '1.5rem' }}>
                <MemoryManager />
            </div>
        </div>
    );
}
