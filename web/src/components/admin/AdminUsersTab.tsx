import { Trash2 } from 'lucide-react';
import { motion } from 'framer-motion';
import { useReducedMotion } from '../../hooks/useReducedMotion';
import { useTheme } from '../../contexts/ThemeContext';

interface User {
    id: string;
    username: string;
    role: string;
    firstName?: string;
    lastName?: string;
}

interface AdminUsersTabProps {
    usersList: User[];
    currentUser: User;
    handleRoleChange: (id: string, newRole: string) => void;
    handleDeleteUser: (id: string, username: string) => void;
    loading: boolean;
}

export default function AdminUsersTab({ usersList, currentUser, handleRoleChange, handleDeleteUser, loading }: AdminUsersTabProps) {
    const reducedMotion = useReducedMotion();
    const { t } = useTheme();

    return (
        <div className="configs-list">
            {loading ? (
                <div className="loading-spinner"></div>
            ) : usersList.length === 0 ? (
                <p style={{ textAlign: 'center', opacity: 0.5, padding: '3rem', color: 'var(--text-secondary)' }}>{t('noUsersFound')}</p>
            ) : (
                usersList.map(u => (
                    <motion.div key={u.id} layout={!reducedMotion} className="result-card">
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                            <div>
                                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                                    <h3 style={{ margin: 0 }}>{u.firstName} {u.lastName}</h3>
                                    <span className="active-badge" style={{
                                        background: u.role === 'superadmin' ? '#6a1b9a'
                                            : u.role === 'admin' ? 'var(--accent-primary)'
                                            : u.role === 'api-user' ? '#2563eb'
                                            : 'var(--text-secondary)'
                                    }}>{u.role}</span>
                                </div>
                                <p style={{ opacity: 0.7, margin: '0.2rem 0' }}>@{u.username} &bull; ID: {u.id.slice(0, 8)}...</p>
                            </div>
                            <div style={{ display: 'flex', gap: '0.5rem' }}>
                                {u.role !== 'superadmin' && u.id !== currentUser.id && !(currentUser.role === 'admin' && u.role === 'admin') && (
                                    <>
                                        <select
                                            value={u.role}
                                            onChange={(e) => handleRoleChange(u.id, e.target.value)}
                                            className="search-button"
                                            style={{ padding: '0.4rem 0.8rem', fontSize: '0.8rem', cursor: 'pointer' }}
                                        >
                                            <option value="user">{t('roleUser')}</option>
                                            <option value="api-user">{t('roleApiUser')}</option>
                                            {currentUser.role === 'superadmin' && (
                                                <option value="admin">{t('roleAdmin')}</option>
                                            )}
                                        </select>
                                        {currentUser.role === 'superadmin' && (
                                            <button
                                                onClick={() => handleDeleteUser(u.id, u.username)}
                                                className="icon-button delete"
                                                title={t('deleteUser')}
                                                aria-label={t('deleteUser')}
                                                style={{ padding: '0.4rem' }}
                                            >
                                                <Trash2 size={20} />
                                            </button>
                                        )}
                                    </>
                                )}
                            </div>
                        </div>
                    </motion.div>
                ))
            )}
        </div>
    );
}
