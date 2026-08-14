import { lazy, Suspense, useMemo } from 'react';
import {
  BookOpen, Settings, Sun, Moon, User, LogOut, Copy, Check, Plus,
  Trash2, UserPlus, Globe, Pencil, FileText, MessageSquare, Loader2, Bot, Search, Star, Users
} from 'lucide-react';
import type { KnowledgeBase, SafeAIConfig, KbAssignableRole } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useAuth } from '../contexts/AuthContext';
import { KBCardSkeleton } from './Skeleton';
import { KbAccordion } from './KbAccordion';
import KbCatalogPanel from './KbCatalogPanel';
import './HomeView.css';

const MembersModal = lazy(() => import('./MembersModal').then(module => ({ default: module.MembersModal })));
const SettingsModal = lazy(() => import('./SettingsModal').then(module => ({ default: module.SettingsModal })));

export interface HomeViewProps {
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
  onDeleteKB: (kb: KnowledgeBase, e: React.MouseEvent) => void;
  removingKb: boolean;
  onCreateGlobalKB: () => void;
  /** Refetches the KB lists after a subscribe/unsubscribe in the discovery panel. */
  onSubscriptionChange: () => void;
  /** Opens a KB the caller knows only by id — the discovery panel's rows carry nothing else. */
  onOpenKbById: (id: string) => void;
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
  sharePermission: KbAssignableRole;
  setSharePermission: (v: KbAssignableRole) => void;
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

// Drei angezeigte Zustaende aus zwei gespeicherten. memberCount enthaelt den
// Owner, deshalb <= 1 und nicht === 0. Single source of truth for the
// three-way branch — text, CSS class and icon all derive from this instead
// of each re-implementing their own kb.visibility === 'public' check.
type VisibilityState = 'public' | 'shared' | 'personal';

function visibilityState(kb: KnowledgeBase): VisibilityState {
  if (kb.visibility === 'public') return 'public';
  return (kb.memberCount ?? 1) > 1 ? 'shared' : 'personal';
}

// Der Zaehler steht ausserhalb der Uebersetzung, weil t() keine
// Interpolation kann.
function visibilityBadge(kb: KnowledgeBase, t: (k: string) => string): string {
  const state = visibilityState(kb);
  if (state === 'public') return t('visibilityPublic');
  if (state === 'shared') return `${t('visibilityShared')} (${kb.memberCount})`;
  return t('visibilityPersonal');
}

// VisibilityBadge renders the three-state badge on every KB card, personal and
// public alike. It has to be shared: GET /api/kb hard-filters
// `WHERE kb.visibility = 'private'`, so a public KB never reaches the personal
// grid — and while the public grid rendered its own static "Global" chip, the
// 'public' state of this badge was unreachable code and the spec's third state
// shipped to nobody. One component, one branch, three reachable states.
function VisibilityBadge({ kb, t }: { kb: KnowledgeBase; t: (k: string) => string }) {
  const state = visibilityState(kb);
  return (
    <div className={`home-view__badge home-view__badge--${state}`}>
      {state === 'public'
        ? <Globe size={10} aria-hidden="true" />
        : <User size={10} aria-hidden="true" />}
      {visibilityBadge(kb, t)}
    </div>
  );
}

// canManageMembers gates the members-dialog trigger: admins and owners
// manage membership, edit/view callers don't. Checking myRole (not the
// legacy kb.userId === user.id owner comparison) is what makes the admin
// tier reachable from the UI at all — an admin who isn't the owner used to
// have no way to open the dialog.
function canManageMembers(kb: KnowledgeBase): boolean {
  return kb.myRole === 'admin' || kb.myRole === 'owner';
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

interface PublicKbCardProps {
  kb: KnowledgeBase;
  isSystemAdmin: boolean;
  removingKb: boolean;
  rtf: Intl.RelativeTimeFormat;
  t: (k: string) => string;
  onSelectKB: (kb: KnowledgeBase) => void;
  onOpenGlobalKbSettings: (kb: KnowledgeBase, e: React.MouseEvent) => void;
  onDeleteGlobalKB: (id: string, e: React.MouseEvent) => void;
  onDeleteKB: (kb: KnowledgeBase, e: React.MouseEvent) => void;
}

// PublicKbCard is one tile in the Favoriten section. System admins get the
// settings/delete pair on top of the shared card chrome.
function PublicKbCard({
  kb, isSystemAdmin, removingKb, rtf, t,
  onSelectKB, onOpenGlobalKbSettings, onDeleteGlobalKB, onDeleteKB,
}: PublicKbCardProps) {
  return (
    <li
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
          {isSystemAdmin && (
            <div className={`home-view__badge home-view__badge--publish ${kb.isPublished ? 'home-view__badge--published' : 'home-view__badge--unpublished'}`}>
              {kb.isPublished ? t('published') : t('unpublished')}
            </div>
          )}

          <VisibilityBadge kb={kb} t={t} />

          {isSystemAdmin && (
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

          {/* A favorites toggle, and nothing more. It is offered to everyone,
              system admins included — their overview query is the same one
              everybody else gets (kb.Handler.ListGlobalKnowledgeBases passes
              isAdmin=false), so the action sticks. removeKb (useKbRemoval)
              routes every public KB through the unsubscribe branch regardless
              of myRole: no membership is dropped, no chats are deleted, and
              the KB stays listed under "KBs entdecken". Deleting the KB
              outright is the separate Trash2 above, gated on isSystemAdmin. */}
          <button
            onClick={(e) => onDeleteKB(kb, e)}
            className="home-view__mini-icon"
            disabled={removingKb}
            title={t('removeFromFavorites')}
            aria-label={t('removeFromFavorites')}
          >
            <Star size={16} aria-hidden="true" fill="currentColor" />
          </button>
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
  );
}

interface PrivateKbCardProps {
  kb: KnowledgeBase;
  currentUserId?: string;
  removingKb: boolean;
  rtf: Intl.RelativeTimeFormat;
  t: (k: string) => string;
  onSelectKB: (kb: KnowledgeBase) => void;
  onOpenShare: (kb: KnowledgeBase, e: React.MouseEvent) => void;
  onDeleteKB: (kb: KnowledgeBase, e: React.MouseEvent) => void;
}

// PrivateKbCard is one tile in "Meine KBs" and "Mit mir geteilt" — the same
// card in both, since the only difference between the sections is the
// caller's own role, which the card already reads off myRole.
function PrivateKbCard({
  kb, currentUserId, removingKb, rtf, t, onSelectKB, onOpenShare, onDeleteKB,
}: PrivateKbCardProps) {
  return (
    // Card-level click is a mouse convenience (role="presentation"); the
    // accessible control is the KB-name button below, which carries the
    // label and the keyboard path.
    <li
      className="source-card home-view__kb-card"
      role="presentation"
      onClick={() => onSelectKB(kb)}
    >
      <div className="home-view__card-top">
        <BookOpen size={20} color="var(--text-secondary)" aria-hidden="true" />
        <div className="home-view__badge-row">
          <VisibilityBadge kb={kb} t={t} />

          {canManageMembers(kb) && (
            <button
              onClick={(e) => onOpenShare(kb, e)}
              className="home-view__mini-icon"
              title={t('share')}
              aria-label={t('share')}
            >
              <UserPlus size={16} aria-hidden="true" />
            </button>
          )}

          {/* Label + aria-label reflect the caller's own role: owners
              delete the KB outright, everyone else only leaves it
              (removeKb — useKbRemoval — decides which request that
              means; a missing myRole is treated as an implicit
              viewer, never as owner). Disabled while any removal is
              in flight (useKbRemoval.removing) so a double-click
              can't open a second confirmation or fire a second
              request — the hook also guards re-entry itself, this is
              just the UI-visible half of that guard. */}
          <button
            onClick={(e) => onDeleteKB(kb, e)}
            className="home-view__mini-icon"
            disabled={removingKb}
            title={kb.myRole === 'owner' ? t('deleteKb') : t('removeFromMyView')}
            aria-label={kb.myRole === 'owner' ? t('deleteKb') : t('removeFromMyView')}
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
        {kb.userId !== currentUserId && (
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
  );
}

export function HomeView(props: HomeViewProps) {
  const { theme, language, setLanguage, toggleTheme, t } = useTheme();
  const { user, siteConfigs } = useAuth();
  const rtf = useMemo(() => new Intl.RelativeTimeFormat(language, { numeric: 'auto' }), [language]);

  const {
    kbs, globalKbs, currentKb, availableConfigs,
    copySuccess, onCopyUserId, onLogout, onViewProfile, onViewAdmin, onViewAgents,
    onCreateKB, onSelectKB, onDeleteKB, removingKb, onCreateGlobalKB, onSubscriptionChange, onOpenKbById, onDeleteGlobalKB,
    onOpenGlobalKbSettings, onOpenShare, onUpdateKBSettings,
    showShareModal, setShowShareModal, sharingKb, shareUserId, setShareUserId,
    shareTargetUser, shareLoading, sharePermission, setSharePermission,
    onLookupUser, onConfirmShare, notFoundUsername, onPendingInvited,
    showSettings, setShowSettings,
  } = props;

  const isSystemAdmin = user?.role === 'admin' || user?.role === 'superadmin';

  // `kbs` is GET /api/kb — private KBs the caller holds any kb_members row
  // for. Splitting it here rather than server-side keeps it one request: the
  // rows already carry myRole, and every row in this list has one (the query
  // requires the membership). A KB I own is mine; anything else reached me
  // because somebody shared it.
  const { ownedKbs, sharedKbs } = useMemo(() => {
    const owned: KnowledgeBase[] = [];
    const shared: KnowledgeBase[] = [];
    for (const kb of kbs) {
      (kb.myRole === 'owner' ? owned : shared).push(kb);
    }
    return { ownedKbs: owned, sharedKbs: shared };
  }, [kbs]);

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
      {/* Favoriten — the public KBs this user keeps. Open by default: it is
          the section most people came for. */}
      <KbAccordion
        id="favorites"
        defaultOpen
        title={t('homeFavorites')}
        count={globalKbs.length}
        icon={<Star size={20} color="var(--accent-primary)" aria-hidden="true" />}
      >
        {globalKbs.length === 0 && !isSystemAdmin ? (
          <p className="home-view__section-empty">{t('homeFavoritesEmpty')}</p>
        ) : (
          <ul className="home-view__grid">
            {isSystemAdmin && (
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
              <PublicKbCard
                key={kb.id}
                kb={kb}
                isSystemAdmin={isSystemAdmin}
                removingKb={removingKb}
                rtf={rtf}
                t={t}
                onSelectKB={onSelectKB}
                onOpenGlobalKbSettings={onOpenGlobalKbSettings}
                onDeleteGlobalKB={onDeleteGlobalKB}
                onDeleteKB={onDeleteKB}
              />
            ))}
          </ul>
        )}
      </KbAccordion>

      {/* Discovery. Closed by default, and unmounted while closed — that is
          what makes expanding it re-read the catalog, so a KB published
          after this page loaded needs no reload to show up. */}
      <KbAccordion
        id="discover"
        defaultOpen={false}
        title={t('discoverKbs')}
        icon={<Search size={20} color="var(--text-secondary)" aria-hidden="true" />}
      >
        <KbCatalogPanel onSubscriptionChange={onSubscriptionChange} onOpenKb={onOpenKbById} />
      </KbAccordion>

      {/* Private KBs somebody else shared with me. */}
      <KbAccordion
        id="shared"
        defaultOpen={false}
        title={t('homeSharedWithMe')}
        count={sharedKbs.length}
        icon={<Users size={20} color="var(--text-secondary)" aria-hidden="true" />}
      >
        {sharedKbs.length === 0 ? (
          <p className="home-view__section-empty">{t('homeSharedWithMeEmpty')}</p>
        ) : (
          <ul className="home-view__grid">
            {sharedKbs.map(kb => (
              <PrivateKbCard
                key={kb.id}
                kb={kb}
                currentUserId={user?.id}
                removingKb={removingKb}
                rtf={rtf}
                t={t}
                onSelectKB={onSelectKB}
                onOpenShare={onOpenShare}
                onDeleteKB={onDeleteKB}
              />
            ))}
          </ul>
        )}
      </KbAccordion>

      {/* My own KBs, last and open by default. */}
      <KbAccordion
        id="mine"
        defaultOpen
        title={t('myKBs')}
        count={ownedKbs.length}
        icon={<BookOpen size={20} color="var(--text-secondary)" aria-hidden="true" />}
      >
        <ul className="home-view__grid">
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
          {ownedKbs.map(kb => (
            <PrivateKbCard
              key={kb.id}
              kb={kb}
              currentUserId={user?.id}
              removingKb={removingKb}
              rtf={rtf}
              t={t}
              onSelectKB={onSelectKB}
              onOpenShare={onOpenShare}
              onDeleteKB={onDeleteKB}
            />
          ))}
        </ul>
      </KbAccordion>
      </main>


      <Suspense fallback={<LoadingFallback />}>
        {showShareModal && <MembersModal
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
          myRole={sharingKb?.myRole ?? 'view'}
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
