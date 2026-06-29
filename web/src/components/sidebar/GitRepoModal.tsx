import React, { memo, useState } from 'react';
import { Plus, Loader2 } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { SourceModal } from './SourceModal';

interface GitRepoModalProps {
    show: boolean;
    onClose: () => void;
    loading: boolean;
    onAdd: (data: { repoUrl: string; isPrivate: boolean; accessToken?: string; branch?: string }) => void;
}

const GitRepoModalComp: React.FC<GitRepoModalProps> = ({ show, onClose, loading, onAdd }) => {
    const { t } = useTheme();
    const [isPrivate, setIsPrivate] = useState(false);
    const [repoUrl, setRepoUrl] = useState('');
    const [accessToken, setAccessToken] = useState('');
    const [branch, setBranch] = useState('');

    const valid = repoUrl.trim().startsWith('https://') && (!isPrivate || accessToken.trim() !== '');

    const handleSubmit = () => {
        if (!valid) return;
        onAdd({
            repoUrl: repoUrl.trim(),
            isPrivate,
            accessToken: isPrivate ? accessToken.trim() : undefined,
            branch: branch.trim() || undefined,
        });
        setRepoUrl('');
        setAccessToken('');
        setBranch('');
        setIsPrivate(false);
        onClose();
    };

    return (
        <SourceModal title={t('gitRepo')} show={show} onClose={onClose}>
            <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('gitRepoDesc')}</p>
            <div role="radiogroup" aria-label={t('gitRepo')} style={{ display: 'flex', gap: '1rem', fontSize: '0.85rem' }}>
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.375rem', cursor: 'pointer' }}>
                    <input
                        type="radio"
                        name="git-visibility"
                        checked={!isPrivate}
                        onChange={() => setIsPrivate(false)}
                    />
                    {t('gitRepoPublic')}
                </label>
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.375rem', cursor: 'pointer' }}>
                    <input
                        type="radio"
                        name="git-visibility"
                        checked={isPrivate}
                        onChange={() => setIsPrivate(true)}
                    />
                    {t('gitRepoPrivate')}
                </label>
            </div>
            <input
                type="url"
                placeholder="https://github.com/owner/repo"
                value={repoUrl}
                onChange={(e) => setRepoUrl(e.target.value)}
                aria-label={t('gitRepoUrl')}
                className="sidebar-left__tools-input"
                // eslint-disable-next-line jsx-a11y/no-autofocus -- focus first field on dialog open (WAI-ARIA dialog pattern)
                autoFocus
            />
            {isPrivate && (
                <input
                    type="password"
                    placeholder={t('gitRepoToken')}
                    value={accessToken}
                    onChange={(e) => setAccessToken(e.target.value)}
                    aria-label={t('gitRepoToken')}
                    className="sidebar-left__tools-input"
                />
            )}
            <input
                type="text"
                placeholder={t('gitRepoBranchPlaceholder')}
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
                aria-label={t('gitRepoBranch')}
                className="sidebar-left__tools-input"
            />
            <button
                onClick={handleSubmit}
                disabled={loading || !valid}
                className="search-button"
            >
                {loading ? <Loader2 className="animate-spin" size={16} /> : <Plus size={16} />}
                {t('addGitRepo')}
            </button>
        </SourceModal>
    );
};

export const GitRepoModal = memo(GitRepoModalComp);
