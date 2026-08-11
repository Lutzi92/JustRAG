import { lazy, Suspense, useMemo } from 'react';
import {
  BookOpen, Settings, Sun, Moon, User, LogOut, Copy, Check, Plus,
  Trash2, UserPlus, Globe, Pencil, FileText, MessageSquare, Loader2, Bot
} from 'lucide-react';
import type { KnowledgeBase, SafeAIConfig } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useAuth } from '../contexts/AuthContext';
import { KBCardSkeleton } from './Skeleton';
import './HomeView.css';

const ShareModal = lazy(() => import('./ShareModal').then(module => ({ default: module.ShareModal })));
const SettingsModal = lazy(() => import('./SettingsModal').then(module => ({ default: module.SettingsModal })));

interface HomeViewProps {
  kbs: KnowledgeBase[];
  globalKbs: KnowledgeBase[];
  currentKb: KnowledgeBase | null;
  availableConfigs: SafeAIConfig[];
  copySuccess: boolean;
  onCopyUserId: () => void;
  onLogout: () => void;
  onViewProfile: () => void;
  onViewAdmin: () => void;
  onViewAgents: () => void;
  onCreateKB: () => void;
  onSelectKB: (kb: KnowledgeBase) => void;
  onDeleteKB: (id: string, e: React.MouseEvent) => void;
  onCreateGlobalKB: () => void;
  onDeleteGlobalKB: (id: string, e: React.MouseEvent) => void;
  onOpenGlobalKbSettings: (kb: KnowledgeBase, e: React.MouseEvent) => void;
  onOpenShare: (kb: KnowledgeBase, e: React.MouseEvent) => void;
  onUpdateKBSettings: (data: Record<string, unknown>) => void;
  showShareModal: boolean;
  setShowShareModal: (v: boolean) => void;
  sharingKb: KnowledgeBase | null;
  shareUserId: string;
  setShareUserId: (v: string) => void;
  shareTargetUser: { id: string; firstName: string; lastName: string; username: string } | null;
  shareLoading: boolean;
  sharePermission: 'view' | 'edit';
  setSharePermission: (v: 'view' | 'edit') => void;
  onLookupUser: () => void;
  onConfirmShare: () => void;
  notFoundUsername: string | null;
  onPendingInvited: () => void;
  showSettings: boolean;
  setShowSettings: (v: boolean) => void;
}

const LoadingFallback = () => (
  <ul className="home-view__grid" aria-busy="true" aria-label="Loading...">
    <KBCardSkeleton />
    <KBCardSkeleton />
    <KBCardSkeleton />
  </ul>
);

// lastActiveLabel renders the KB-card freshness line (improvement #6): the
// newest of lastMessageAt / createdAt as a locale-aware "Zuletzt aktiv vor …".
function lastActiveLabel(
  kb: KnowledgeBase,
  rtf: Intl.RelativeTimeFormat,
  t: (k: string) => string,
): string {
  const iso = kb.lastMessageAt || kb.createdAt;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '';
  const diffMs = then - Date.now(); // negative => past
  const min = Math.round(diffMs / 60000);
  const hr = Math.round(diffMs / 3600000);
  const day = Math.round(diffMs / 86400000);
  let rel: string;
  if (Math.abs(min) < 60) rel = rtf.format(min, 'minute');
  else if (Math.abs(hr) < 24) rel = rtf.format(hr, 'hour');
  else rel = rtf.format(day, 'day');
  return `${t('kbLastActive')} ${rel}`;
}

// KbCardChips is the compact metadata slice on each Home KB card (improvement
// #6): up to two scent chips (files · messages) plus a single needs-attention
// chip (failed, else processing). Lucide icons (#2), status tokens (#1).
function KbCardChips({ kb, t }: { kb: KnowledgeBase; t: (k: string) => string }) {
  const processing = kb.processingFileCount ?? 0;
  const files = kb.fileCount ?? 0;
  const messages = kb.messageCount ?? 0;
  if (files === 0 && messages === 0 && processing === 0) return null;
  return (
    <div className="home-view__chip-row">
      {files > 0 && (
        <span className="home-view__chip">
          <FileText size={12} aria-hidden="true" />
          {t('kbFilesChip').replace('{n}', String(files))}
        </span>
      )}
      {messages > 0 && (
        <span className="home-view__chip">
          <MessageSquare size={12} aria-hidden="true" />
          {t('kbMessagesChip').replace('{n}', String(messages))}
        </span>
      )}
      {processing > 0 && (
        <span className="home-view__chip home-view__chip--processing">
          <Loader2 size={12} className="spin" aria-hidden="true" />
          {t('kbProcessingChip').replace('{n}', String(processing))}
        </span>
      )}
    </div>
  );
}

export function HomeView(props: HomeViewProps) {
  const { theme, language, setLanguage, toggleTheme, t } = useTheme();
  const { user, siteConfigs } = useAuth();
  const rtf = useMemo(() => new Intl.RelativeTimeFormat(language, { numeric: 'auto' }), [language]);

  const {
    kbs, globalKbs, currentKb, availableConfigs,
    copySuccess, onCopyUserId, onLogout, onViewProfile, onViewAdmin, onViewAgents,
    onCreateKB, onSelectKB, onDeleteKB, onCreateGlobalKB, onDeleteGlobalKB,
    onOpenGlobalKbSettings, onOpenShare, onUpdateKBSettings,
    showShareModal, setShowShareModal, sharingKb, shareUserId, setShareUserId,
    shareTargetUser, shareLoading, sharePermission, setSharePermission,
    onLookupUser, onConfirmShare, notFoundUsername, onPendingInvited,
    showSettings, setShowSettings,
  } = props;

  return (
    <div className="home-view">
      <a href="#home-main-content" className="skip-link">{t('skipToContent')}</a>
      <header className="home-view__header">
        <div className="home-view__logo-row">
          <div className="home-view__logo-box">
            {siteConfigs.logo_path ? (
              <img src={`${API_BASE_URL}${siteConfigs.logo_path}`} alt={t('websiteLogo')} className="home-view__logo" />
            ) : (
              <BookOpen size={60} aria-hidden="true" />
            )}
          </div>
        </div>
        <h1 className="home-view__title">{t('myKBs')}</h1>
        <p className="home-view__subtitle">{t('kbDescription')}</p>
      </header>

      <div className="home-view__actions">
        <div className="home-view__user-pill">
          <User size={16} aria-hidden="true" />
          <span>@{user?.username}</span>
          <button
            onClick={onCopyUserId}
            className={`home-view__copy-btn${copySuccess ? ' home-view__copy-btn--success' : ''}`}
            title={t('copyUsername')}
            aria-label={t('copyUsername')}
          >
            <span className="icon-swap" key={copySuccess ? 'check' : 'copy'}>{copySuccess ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}</span>
          </button>
        </div>

        <button
          onClick={onViewProfile}
          className="home-view__icon-button"
          title={t('profile')}
          aria-label={t('profile')}
        >
          <User size={20} aria-hidden="true" />
        </button>

        <button
          onClick={onViewAgents}
          className="home-view__icon-button"
          title={t('myAgents')}
          aria-label={t('myAgents')}
        >
          <Bot size={20} aria-hidden="true" />
        </button>

        <button
          onClick={toggleTheme}
          className="home-view__icon-button"
          title={theme === 'light' ? t('switchToDark') : t('switchToLight')}
          aria-label={theme === 'light' ? t('switchToDark') : t('switchToLight')}
        >
          <span className="icon-swap" key={theme}>{theme === 'light' ? <Moon size={20} aria-hidden="true" /> : <Sun size={20} aria-hidden="true" />}</span>
        </button>

        <button
          onClick={() => setLanguage(language === 'de' ? 'en' : 'de')}
          className="home-view__icon-button home-view__icon-button--lang"
          title={t('switchLanguage')}
          aria-label={t('switchLanguage')}
        >
          {language.toUpperCase()}
        </button>

        <button
          onClick={onLogout}
          className="home-view__icon-button"
          title={t('logout')}
          aria-label={t('logout')}
        >
          <LogOut size={20} aria-hidden="true" />
        </button>
      </div>

      <main id="home-main-content">
      {globalKbs.length > 0 && (
        <section className="home-view__section">
          <div className="home-view__section-header">
            <Globe size={20} color="var(--accent-primary)" aria-hidden="true" />
            <h2 className="home-view__section-title">{t('globalKBs')}</h2>
          </div>

          <ul className="home-view__grid">
            {(user?.role === 'admin' || user?.role === 'superadmin') && (
              <li className="source-card home-view__create-card">
                <button
                  type="button"
                  onClick={onCreateGlobalKB}
                  aria-label={t('createGlobalKB')}
                  className="home-view__create-button"
                >
                  <Plus size={32} aria-hidden="true" />
                  <span className="home-view__create-label">{t('createGlobalKB')}</span>
                </button>
              </li>
            )}

            {globalKbs.map(kb => (
              <li
                key={kb.id}
                className="source-card home-view__kb-card"
                role="button" // eslint-disable-line jsx-a11y/no-noninteractive-element-to-interactive-role
                tabIndex={0}
                aria-label={`${t('openKb')}: ${kb.name}`}
                onClick={() => onSelectKB(kb)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onSelectKB(kb);
                  }
                }}
              >
                <div className="home-view__card-top">
                  <Globe size={20} color="var(--accent-primary)" aria-hidden="true" />
                  <div className="home-view__badge-row">
                    {(user?.role === 'admin' || user?.role === 'superadmin') && (
                      <div className={`home-view__badge home-view__badge--publish ${kb.isPublished ? 'home-view__badge--published' : 'home-view__badge--unpublished'}`}>
                        {kb.isPublished ? t('published') : t('unpublished')}
                      </div>
                    )}

                    <div className="home-view__badge home-view__badge--global">
                      <Globe size={10} aria-hidden="true" />
                      {t('globalBadge')}
                    </div>

                    {(user?.role === 'admin' || user?.role === 'superadmin') && (
                      <>
                        <button
                          onClick={(e) => onOpenGlobalKbSettings(kb, e)}
                          className="home-view__mini-icon"
                          title={t('editSettings')}
                          aria-label={t('editSettings')}
                        >
                          <Pencil size={16} aria-hidden="true" />
                        </button>
                        <button
                          onClick={(e) => onDeleteGlobalKB(kb.id, e)}
                          className="home-view__mini-icon"
                          title={t('delete')}
                          aria-label={t('deleteGlobalKb')}
                        >
                          <Trash2 size={16} aria-hidden="true" />
                        </button>
                      </>
                    )}
                  </div>
                </div>

                <div className="source-title home-view__kb-name">
                  <button
                    type="button"
                    className="text-button home-view__kb-name-btn"
                    onClick={(e) => {
                      e.stopPropagation();
                      onSelectKB(kb);
                    }}
                  >
                    {kb.name}
                  </button>
                </div>

                {kb.headerText && <div className="home-view__kb-header-text">{kb.headerText}</div>}
                <div className="source-meta home-view__kb-meta">{lastActiveLabel(kb, rtf, t)}</div>
                <KbCardChips kb={kb} t={t} />
              </li>
            ))}
          </ul>
        </section>
      )}

      {globalKbs.length === 0 && (user?.role === 'admin' || user?.role === 'superadmin') && (
        <section className="home-view__section">
          <div className="home-view__section-header">
            <Globe size={20} color="var(--accent-primary)" aria-hidden="true" />
            <h2 className="home-view__section-title">{t('globalKBs')}</h2>
          </div>
          <div className="home-view__grid">
            <button
              type="button"
              className="source-card home-view__create-card home-view__create-card-btn"
              onClick={onCreateGlobalKB}
              aria-label={t('createGlobalKB')}
            >
              <Plus size={32} aria-hidden="true" />
              <span className="home-view__create-label">{t('createGlobalKB')}</span>
            </button>
          </div>
        </section>
      )}

      <section className="home-view__section home-view__section--tight">
        {(globalKbs.length > 0 || (user?.role === 'admin' || user?.role === 'superadmin')) && (
          <div className="home-view__section-header">
            <BookOpen size={20} color="var(--text-secondary)" aria-hidden="true" />
            <h2 className="home-view__section-title">{t('myKBs')}</h2>
          </div>
        )}
      </section>

      <ul className="home-view__grid home-view__grid--main">
        <li className="source-card home-view__create-card">
          <button
            type="button"
            onClick={onCreateKB}
            aria-label={t('createNewKb')}
            className="home-view__create-button"
          >
            <Plus size={32} aria-hidden="true" />
            <span className="home-view__create-label">{t('newKB')}</span>
          </button>
        </li>

        {kbs.map(kb => (
          // Card-level click is a mouse convenience (role="presentation"); the
          // accessible control is the KB-name button below, which carries the
          // label and the keyboard path.
          <li
            key={kb.id}
            className="source-card home-view__kb-card"
            role="presentation"
            onClick={() => onSelectKB(kb)}
          >
            <div className="home-view__card-top">
              <BookOpen size={20} color="var(--text-secondary)" aria-hidden="true" />
              <div className="home-view__badge-row">
                {kb.userId !== user?.id && (
                  <div className="home-view__badge home-view__badge--shared">
                    <User size={10} aria-hidden="true" />
                    {t('sharedBadge')}
                  </div>
                )}

                {kb.userId === user?.id && (
                  <button
                    onClick={(e) => onOpenShare(kb, e)}
                    className="home-view__mini-icon"
                    title={t('share')}
                    aria-label={t('share')}
                  >
                    <UserPlus size={16} aria-hidden="true" />
                  </button>
                )}

                <button
                  onClick={(e) => onDeleteKB(kb.id, e)}
                  className="home-view__mini-icon"
                  title={t('delete')}
                  aria-label={t('delete')}
                >
                  <Trash2 size={16} aria-hidden="true" />
                </button>
              </div>
            </div>

            <div className="source-title home-view__kb-name">
              <button
                type="button"
                className="text-button home-view__kb-name-btn"
                aria-label={`${t('openKb')}: ${kb.name}`}
                onClick={(e) => {
                  e.stopPropagation();
                  onSelectKB(kb);
                }}
              >
                {kb.name}
              </button>
            </div>

            <div className="home-view__meta-row">
              <div className="source-meta home-view__kb-meta">{lastActiveLabel(kb, rtf, t)}</div>
              {kb.userId !== user?.id && (
                <div className="home-view__owner-meta">
                  <User size={12} aria-hidden="true" />
                  {(() => {
                    const fullName = `${kb.ownerFirstName || ''} ${kb.ownerLastName || ''}`.trim();
                    const displayName = fullName || kb.ownerUsername || t('unknownUser');
                    return t('sharedBy').replace('{name}', displayName);
                  })()}
                </div>
              )}
            </div>
            <KbCardChips kb={kb} t={t} />
          </li>
        ))}
      </ul>
      </main>

      <Suspense fallback={<LoadingFallback />}>
        {showShareModal && <ShareModal
          show={showShareModal}
          onClose={() => setShowShareModal(false)}
          sharingKb={sharingKb}
          shareUserId={shareUserId}
          setShareUserId={setShareUserId}
          shareTargetUser={shareTargetUser}
          shareLoading={shareLoading}
          sharePermission={sharePermission}
          setSharePermission={setSharePermission}
          onLookupUser={onLookupUser}
          onConfirmShare={onConfirmShare}
          notFoundUsername={notFoundUsername}
          onPendingInvited={onPendingInvited}
        />}
      </Suspense>

      <Suspense fallback={null}>
        {showSettings && <SettingsModal
          show={showSettings}
          onClose={() => setShowSettings(false)}
          currentKb={currentKb}
          availableConfigs={availableConfigs}
          onUpdateSettings={onUpdateKBSettings}
        />}
      </Suspense>

      {(user?.role === 'admin' || user?.role === 'superadmin') && (
        <button
          onClick={onViewAdmin}
          className="home-view__admin-fab"
          aria-label={t('adminSettings')}
        >
          <Settings size={24} aria-hidden="true" />
        </button>
      )}

      {siteConfigs.imprint && (
        <footer className="home-view__imprint">{siteConfigs.imprint}</footer>
      )}
    </div>
  );
}
