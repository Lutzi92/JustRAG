import { lazy, Suspense, memo, useCallback, useRef, useEffect, useState } from 'react';
import { Virtuoso } from 'react-virtuoso';
import {
  ArrowLeft, BookOpen, Brain, Sun, Moon, Send,
  BarChart2, MessageSquare, UserPlus, X, Search, GitBranch, Settings, Check, Trash2, SlidersHorizontal,
} from 'lucide-react';
import { motion } from 'framer-motion';
import type { Message } from '../types';
import { useTheme } from '../contexts/ThemeContext';
import { useAuth } from '../contexts/AuthContext';
import { useIsMobileContext } from '../contexts/MobileContext';
import { useKbCore } from '../contexts/KbCoreContext';
import { useKbChat } from '../contexts/KbChatContext';
import { useKbData } from '../contexts/KbDataContext';
import { useKbLayout } from '../contexts/KbLayoutContext';
import { useReducedMotion, getMotionProps } from '../hooks/useReducedMotion';
import MessageBubble from '../MessageBubble';
import { findDefaultLeaf, getBranchInfo } from '../utils/messageTree';
import { HAPTIC_PATTERNS, triggerHaptic } from '../utils/haptics';
import { viewportHeight } from '../utils/viewport';
import { BranchTreeNav } from './BranchTreeNav';
import { MessageSkeleton } from './Skeleton';
import { BackgroundJobsIndicator } from './BackgroundJobsIndicator';
import { registerJob, unregisterJob } from '../utils/jobRegistry';

const Dashboard = lazy(() => import('../Dashboard'));
const ResearchMode = lazy(() => import('./ResearchMode'));
const AcademicMode = lazy(() => import('./AcademicMode'));
const KbSettingsPanelLazy = lazy(() => import('./kb-settings/KbSettingsPanel').then(m => ({ default: m.KbSettingsPanel })));
const StudioWorkspace = lazy(() => import('./Studio/StudioWorkspace').then(module => ({ default: module.StudioWorkspace })));
const ComparisonView = lazy(() => import('./ComparisonView').then(module => ({ default: module.ComparisonView })));

const ChatViewComp = () => {
  const { theme, toggleTheme, language, setLanguage, t } = useTheme();
  const { user, siteConfigs } = useAuth();
  const isMobile = useIsMobileContext();
  const reducedMotion = useReducedMotion();

  const {
    currentKb, isPro, availableConfigs, kbView, setKbView, handleGoHome, handleUpdateKBSettings,
  } = useKbCore();
  const {
    chat, enhance, setEnhance, reasoningEnabled, setReasoningEnabled,
    reasoningLevel, setReasoningLevel, showSettings,
    setResearchRunning, setAcademicResearchRunning,
  } = useKbChat();
  const { fileMgmt, webTools, content, sharing } = useKbData();
  const { sidebar } = useKbLayout();

  const [showSystemPrompt, setShowSystemPrompt] = useState(false);
  const [systemPromptDraft, setSystemPromptDraft] = useState(currentKb?.systemPrompt || '');
  const [showKbSettings, setShowKbSettings] = useState(false);

  const canTuneKB =
    currentKb != null &&
    (user?.role === 'api-user' || user?.role === 'admin' || user?.role === 'superadmin') &&
    (currentKb.userId === user?.id || user?.role === 'admin' || user?.role === 'superadmin');

  useEffect(() => {
    setSystemPromptDraft(currentKb?.systemPrompt || '');
  }, [currentKb?.id, currentKb?.systemPrompt]);

  const handleResearchRunningChange = useCallback((running: boolean) => {
    setResearchRunning(running);
    if (running) registerJob(currentKb?.id || '', currentKb?.name || '', 'research');
    else unregisterJob(currentKb?.id || '', 'research');
  }, [currentKb?.id, currentKb?.name, setResearchRunning]);

  const handleAcademicResearchRunningChange = useCallback((running: boolean) => {
    setAcademicResearchRunning(running);
    if (running) registerJob(currentKb?.id || '', currentKb?.name || '', 'academicResearch');
    else unregisterJob(currentKb?.id || '', 'academicResearch');
  }, [currentKb?.id, currentKb?.name, setAcademicResearchRunning]);

  const { hasFiles, selectedFileCount } = fileMgmt;
  const { handlePreviewSource, handlePdfSourceOpen } = webTools;
  const { generatedContent, handleGenerate, handleDeleteGeneratedContent, selectedContent } = content;
  const { handleOpenShare } = sharing;

  const initialRenderRef = useRef(true);
  useEffect(() => {
    const timer = setTimeout(() => { initialRenderRef.current = false; }, 1000);
    return () => clearTimeout(timer);
  }, []);
  const handleEditCancel = useCallback(() => chat.setEditingMessageId(null), [chat]);

  const viewTabStyle = (active: boolean): React.CSSProperties => ({
    display: 'flex',
    alignItems: 'center',
    gap: isMobile ? '0' : '6px',
    padding: isMobile ? '6px 8px' : '6px 12px',
    minHeight: isMobile ? '44px' : undefined,
    borderRadius: '6px',
    border: 'none',
    background: active ? 'var(--bg-primary)' : 'transparent',
    color: active ? 'var(--accent-primary)' : 'var(--text-secondary)',
    cursor: 'pointer',
    fontSize: '0.85rem',
    fontWeight: 500,
    transition: 'all 0.2s',
  });

  return (
    <div className="chat-area" style={isMobile ? { height: viewportHeight('calc(100dvh - 60px)', 'calc(100vh - 60px)') } : undefined}>
      <header className="chat-header" style={isMobile ? { padding: '0.75rem' } : undefined}>
        <div style={{ display: 'flex', alignItems: 'center', gap: isMobile ? '8px' : '12px', minWidth: 0 }}>
          {isMobile ? (
            <button
              onClick={handleGoHome}
              style={{ background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: '4px', color: 'var(--text-secondary)' }}
              aria-label={t('back')}
            >
              <ArrowLeft size={20} />
            </button>
          ) : (
            <div style={{ padding: '8px', background: 'var(--tag-bg)', borderRadius: '8px', color: 'var(--accent-primary)' }}>
              <BookOpen size={20} aria-hidden="true" />
            </div>
          )}
          <span style={{ fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: isMobile ? '0.85rem' : '1.15rem' }}>{currentKb?.name}</span>
          <div style={{ display: 'flex', marginLeft: isMobile ? '4px' : '24px', gap: '4px', background: 'var(--bg-secondary)', padding: '4px', borderRadius: '8px' }}>
            <button onClick={() => setKbView('chat')} style={viewTabStyle(kbView === 'chat')} aria-current={kbView === 'chat' ? 'page' : undefined} aria-label="Chat">
              <MessageSquare size={16} aria-hidden="true" />
              {!isMobile && 'Chat'}
            </button>
            {currentKb?.isGlobal && (user?.role === 'admin' || user?.role === 'superadmin') && (
              <button onClick={() => setKbView('dashboard')} style={viewTabStyle(kbView === 'dashboard')} aria-current={kbView === 'dashboard' ? 'page' : undefined} aria-label={t('analytics')}>
                <BarChart2 size={16} aria-hidden="true" />
                {!isMobile && t('analytics')}
              </button>
            )}
            <button
              onClick={() => setKbView('research')}
              style={viewTabStyle(kbView === 'research')}
              title={t('research')}
              aria-current={kbView === 'research' ? 'page' : undefined}
              aria-label={t('research')}
            >
              <Search size={16} aria-hidden="true" />
              {!isMobile && t('research')}
            </button>
          </div>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: isMobile ? '8px' : '16px' }}>
          <BackgroundJobsIndicator />
          {!isMobile && (
            <>
              <button
                onClick={toggleTheme}
                style={{ background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', color: 'var(--text-secondary)', padding: '4px' }}
                title={theme === 'light' ? t('switchToDark') : t('switchToLight')}
                aria-label={theme === 'light' ? t('switchToDark') : t('switchToLight')}
              >
                <span className="icon-swap" key={theme}>{theme === 'light' ? <Moon size={20} aria-hidden="true" /> : <Sun size={20} aria-hidden="true" />}</span>
              </button>
              <button
                onClick={() => setLanguage(language === 'de' ? 'en' : 'de')}
                style={{
                  background: 'none',
                  border: 'none',
                  cursor: 'pointer',
                  display: 'flex',
                  alignItems: 'center',
                  color: 'var(--text-secondary)',
                  padding: '4px',
                  fontWeight: 600,
                  fontSize: '0.85rem'
                }}
                title={t('switchLanguage')}
                aria-label={t('switchLanguage')}
              >
                {language.toUpperCase()}
              </button>
              {canTuneKB && (
                <button
                  type="button"
                  onClick={() => setShowKbSettings(true)}
                  style={{
                    background: showKbSettings ? 'var(--tag-bg)' : 'none',
                    border: 'none',
                    cursor: 'pointer',
                    display: 'flex',
                    alignItems: 'center',
                    color: showKbSettings ? 'var(--accent-primary)' : 'var(--text-secondary)',
                    padding: '4px',
                    borderRadius: '6px',
                  }}
                  title={t('kbTuning')}
                  aria-label={t('kbTuning')}
                  aria-pressed={showKbSettings}
                >
                  <SlidersHorizontal size={20} aria-hidden="true" />
                </button>
              )}
            </>
          )}
          {currentKb?.userId === user?.id && (
            <button
              type="button"
              onClick={(e) => handleOpenShare(currentKb, e)}
              style={{
                background: 'none',
                border: 'none',
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                color: 'var(--text-secondary)',
                padding: '4px',
              }}
              title={t('shareKb')}
              aria-label={t('shareKb')}
            >
              <UserPlus size={20} aria-hidden="true" />
            </button>
          )}
        </div>
      </header>

      {
        isPro && showSettings && (
          <motion.div
            {...getMotionProps(reducedMotion)}
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: 'auto', opacity: 1 }}
            style={{ background: 'var(--bg-primary)', borderBottom: '1px solid var(--border-color)', padding: '1.5rem 2rem', overflow: 'hidden' }}
          >
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '2rem' }}>
              <div>
                <label style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>{t('ragMode')}</label>
                <div style={{ color: 'var(--text-primary)', fontSize: '0.85rem', padding: '0.5rem', background: 'var(--bg-secondary)', borderRadius: '4px', border: '1px solid var(--border-color)' }}>
                  {t('vectorSearchOnly')}
                </div>
              </div>

              <div>
                <label htmlFor="inline-ai-config" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>{t('aiProvider')}</label>
                <select
                  id="inline-ai-config"
                  value={currentKb?.aiConfigId || ''}
                  onChange={(e) => handleUpdateKBSettings({ aiConfigId: e.target.value || null, chatModel: null, embeddingModel: null })}
                  style={{ width: '100%', padding: '0.5rem', borderRadius: '4px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                >
                  <option value="">{t('defaultSystemConfig')}</option>
                  {availableConfigs.map(c => (
                    <option key={c.id} value={c.id}>{c.name} ({c.provider}){c.is_active ? ` - ${t('standard')}` : ''}</option>
                  ))}
                </select>
              </div>

              {(() => {
                const selectedConfig = availableConfigs.find(c => c.id === currentKb?.aiConfigId) || availableConfigs.find(c => c.is_active);
                if (!selectedConfig) return null;

                return (
                  <>
                    <div>
                      <label htmlFor="inline-chat-model" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>{t('chatModel')}</label>
                      <select
                        id="inline-chat-model"
                        value={currentKb?.chatModel || ''}
                        onChange={(e) => handleUpdateKBSettings({ chatModel: e.target.value || null })}
                        style={{ width: '100%', padding: '0.5rem', borderRadius: '4px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                      >
                        <option value="">{currentKb?.aiConfigId ? t('providerDefault') : t('systemDefault')}</option>
                        {(selectedConfig.chat_models || []).map(m => (
                          <option key={m} value={m}>{m}</option>
                        ))}
                      </select>
                    </div>

                    <div>
                      <label htmlFor="inline-embedding-model" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>{t('embeddingModel')}</label>
                      <select
                        id="inline-embedding-model"
                        value={currentKb?.embeddingModel || ''}
                        onChange={(e) => handleUpdateKBSettings({ embeddingModel: e.target.value || null })}
                        style={{ width: '100%', padding: '0.5rem', borderRadius: '4px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                      >
                        <option value="">{currentKb?.aiConfigId ? t('providerDefault') : t('systemDefault')}</option>
                        {(selectedConfig.embedding_models || []).map(m => (
                          <option key={m} value={m}>{m}</option>
                        ))}
                      </select>
                    </div>

                    <div>
                      <label htmlFor="inline-rerank-model" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>{t('rerankModel')}</label>
                      <select
                        id="inline-rerank-model"
                        value={currentKb?.rerankModel || ''}
                        onChange={(e) => handleUpdateKBSettings({ rerankModel: e.target.value || null })}
                        style={{ width: '100%', padding: '0.5rem', borderRadius: '4px', border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', color: 'var(--text-primary)' }}
                      >
                        <option value="">{t('standard')}</option>
                        {(selectedConfig.rerank_models || []).map(m => (
                          <option key={m} value={m}>{m}</option>
                        ))}
                      </select>
                    </div>
                  </>
                );
              })()}
            </div>
          </motion.div>
        )
      }

      <main style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
        {
          kbView === 'chat' ? (
            chat.comparisonMode && chat.comparisonLeafId && chat.activeLeafId ? (
              <Suspense fallback={<div className="messages-container" style={{ justifyContent: 'flex-end' }}><MessageSkeleton role="user" /><MessageSkeleton role="ai" /></div>}>
                <div className="content-fade-in" style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
                  <ComparisonView
                    messageTree={chat.messageTree}
                    leafIdA={chat.activeLeafId}
                    leafIdB={chat.comparisonLeafId}
                    onUseBranch={(leafId) => {
                      chat.setActiveLeafId(leafId);
                      chat.setComparisonMode(false);
                      chat.setComparisonLeafId(null);
                    }}
                    onClose={() => {
                      chat.setComparisonMode(false);
                      chat.setComparisonLeafId(null);
                    }}
                    onPdfOpen={handlePdfSourceOpen}
                  />
                </div>
              </Suspense>
            ) : (
              <div style={{ display: 'flex', flex: 1, overflow: 'hidden' }}>
                <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden', minWidth: 0, maxWidth: '100%' }}>
                  {chat.messages.length === 0 ? (
                    <div
                      className="messages-container"
                      style={{ position: 'relative' }}
                      ref={chat.messagesContainerRef}
                      onScroll={chat.handleScroll}
                    >
                      <motion.div
                        className="empty-state"
                        {...getMotionProps(reducedMotion)}
                        initial={{ opacity: 0, y: 20 }}
                        animate={{ opacity: 1, y: 0 }}
                        transition={{ duration: 0.5, ease: 'easeOut' }}
                      >
                        {currentKb?.isGlobal && currentKb?.headerText ? (
                          <div style={{ marginBottom: '2rem', textAlign: 'center', color: 'var(--text-secondary)', fontSize: '1.1rem', whiteSpace: 'pre-wrap', maxWidth: '600px', margin: '0 auto 2rem' }}>
                            {currentKb.headerText}
                          </div>
                        ) : siteConfigs.kb_header ? (
                          <div style={{ marginBottom: '2rem', textAlign: 'center', color: 'var(--text-secondary)', fontSize: '1.1rem', whiteSpace: 'pre-wrap', maxWidth: '600px', margin: '0 auto 2rem' }}>
                            {siteConfigs.kb_header}
                          </div>
                        ) : null}
                        <h1>{currentKb?.name}</h1>
                        <p>{t('kbHeaderDefault')}</p>
                        {!hasFiles && !currentKb?.isGlobal && (
                          <div style={{
                            marginTop: '1.5rem',
                            padding: '0.75rem 1.25rem',
                            background: 'var(--tag-bg)',
                            borderRadius: '8px',
                            color: 'var(--text-secondary)',
                            fontSize: '0.9rem',
                            maxWidth: '400px'
                          }}>
                            {t('noSources')}
                          </div>
                        )}
                        <div style={{ display: 'flex', gap: '1rem', marginTop: '2rem', flexWrap: 'wrap', justifyContent: 'center', opacity: hasFiles ? 1 : 0.4, flexDirection: isMobile ? 'column' : 'row', alignItems: isMobile ? 'stretch' : undefined, padding: isMobile ? '0 1rem' : undefined }}>
                          {(currentKb?.isGlobal && currentKb?.examplePrompts
                            ? currentKb.examplePrompts.split('\n').filter(p => p.trim())
                            : siteConfigs.example_prompts
                              ? siteConfigs.example_prompts.split('\n').filter(p => p.trim())
                              : [
                                "Fasse die wichtigsten Punkte meiner Dokumente zusammen",
                                "Was sind die wichtigsten Erkenntnisse in [Name des Dokuments]?"
                              ]
                          ).map((prompt, idx) => (
                            <button
                              type="button"
                              key={idx}
                              className="source-card"
                              disabled={!hasFiles}
                              style={{ width: isMobile ? '100%' : '220px', cursor: !hasFiles ? 'not-allowed' : 'pointer', textAlign: 'left', minHeight: isMobile ? '48px' : '80px', display: 'flex', alignItems: 'center' }}
                              onClick={() => {
                                if (!hasFiles) return;
                                chat.setUserMessageInput(prompt.trim());
                                chat.textareaRef.current?.focus();
                              }}
                            >
                              &quot;{prompt.trim()}&quot;
                            </button>
                          ))}
                        </div>
                      </motion.div>
                    </div>
                  ) : (
                    <Virtuoso
                      className="messages-container"
                      style={{ height: '100%' }}
                      data={chat.messages}
                      initialTopMostItemIndex={Math.max(0, chat.messages.length - 1)}
                      followOutput="smooth"
                      scrollerRef={(ref: HTMLElement | Window | null) => {
                        if (chat.messagesContainerRef && ref instanceof HTMLElement) {
                          (chat.messagesContainerRef as React.MutableRefObject<HTMLDivElement | null>).current = ref as HTMLDivElement | null;
                        } else if (chat.messagesContainerRef && ref === null) {
                          (chat.messagesContainerRef as React.MutableRefObject<HTMLDivElement | null>).current = null;
                        }
                      }}
                      onScroll={chat.handleScroll}
                      components={{
                        Footer: () => {
                          const lastMsg = chat.messages.length > 0 ? chat.messages[chat.messages.length - 1] : null;
                          const isDeepSearching = lastMsg?.isDeepSearch && !lastMsg?.content;
                          const showLoader = chat.loading && !(lastMsg?.reasoning && !lastMsg?.content);
                          return (
                            <>
                              {showLoader && (
                                <div className="message-bubble message-ai message-ai--streaming" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                  <span className="loading-dots" aria-label={isDeepSearching ? t('searchingDeeper') : t('thinking')}>
                                    <span className="loading-dots__dot" />
                                    <span className="loading-dots__dot" />
                                    <span className="loading-dots__dot" />
                                  </span>
                                  {isDeepSearching ? t('searchingDeeper') : t('thinking')}
                                </div>
                              )}
                            </>
                          );
                        }
                      }}
                      itemContent={(index: number, msg: Message) => {
                        const bi = msg.id ? getBranchInfo(chat.messageTree, msg.id) : null;
                        return (
                          <MessageBubble
                            key={msg.id}
                            message={msg}
                            isStreaming={chat.loading && index === chat.messages.length - 1}
                            onPdfOpen={handlePdfSourceOpen}
                            onFollowUpClick={chat.handleFollowUpClick}
                            showFollowUps={!chat.loading && msg.role === 'ai' && index === chat.messages.length - 1}
                            branchInfo={bi}
                            onSwitchBranch={chat.handleSwitchBranch}
                            animationDelay={initialRenderRef.current ? Math.min(index * 0.05, 0.3) : 0}
                            onEdit={msg.role === 'user' && !msg.isEnhanced ? (chat.editingMessageId === msg.id ? chat.handleEditSubmit : chat.handleStartEdit) : undefined}
                            onFork={msg.role === 'ai' ? chat.handleForkFromMessage : undefined}
                            onCompare={bi ? chat.handleStartComparison : undefined}
                            onRegenerate={msg.role === 'ai' ? chat.handleRegenerate : undefined}
                            onFeedback={msg.role === 'ai' ? chat.handleFeedback : undefined}
                            isEditing={chat.editingMessageId === msg.id}
                            onEditCancel={handleEditCancel}
                            onPreviewSource={handlePreviewSource}
                          />
                        );
                      }}
                    />
                  )}

                  <div className="input-container">
                    {chat.forkPointId && (
                      <div style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        gap: '8px',
                        padding: '6px 12px',
                        marginBottom: '0.5rem',
                        background: 'var(--tag-bg)',
                        border: '1px solid var(--accent-primary)',
                        borderRadius: '8px',
                        fontSize: '0.8rem',
                        color: 'var(--accent-primary)',
                      }}>
                        <GitBranch size={14} />
                        <span>{language === 'en' ? 'Branching — type a different follow-up' : 'Abzweigung — gib eine andere Frage ein'}</span>
                        <button
                          onClick={() => {
                            chat.setForkPointId(null);
                            const leaf = findDefaultLeaf(chat.messageTree);
                            if (leaf) chat.setActiveLeafId(leaf);
                          }}
                          style={{
                            background: 'none',
                            border: 'none',
                            cursor: 'pointer',
                            color: 'var(--text-secondary)',
                            padding: '2px',
                            minWidth: '44px',
                            minHeight: '44px',
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            marginLeft: '4px',
                          }}
                          title={language === 'en' ? 'Cancel fork' : 'Abzweigung abbrechen'}
                        >
                          <X size={14} />
                        </button>
                      </div>
                    )}
                    {showSystemPrompt && currentKb && currentKb.userId === user?.id && (
                      <div style={{ marginBottom: '0.75rem', width: '100%' }}>
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '4px' }}>
                          <label htmlFor="chat-system-prompt" style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--text-secondary)' }}>
                            {t('systemPromptLabel')}
                          </label>
                          <span style={{ fontSize: '0.7rem', color: 'var(--text-secondary)' }}>
                            {systemPromptDraft.length} / 4000
                          </span>
                        </div>
                        <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', margin: '0 0 6px' }}>{t('systemPromptDescription')}</p>
                        <textarea
                          id="chat-system-prompt"
                          value={systemPromptDraft}
                          onChange={(e) => setSystemPromptDraft(e.target.value)}
                          placeholder={t('systemPromptPlaceholder')}
                          maxLength={4000}
                          rows={3}
                          style={{
                            width: '100%',
                            padding: '0.6rem',
                            borderRadius: '8px',
                            border: '1px solid var(--border-color)',
                            background: 'var(--bg-primary)',
                            color: 'var(--text-primary)',
                            resize: 'vertical',
                            fontFamily: 'inherit',
                            fontSize: '0.85rem',
                          }}
                        />
                        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '6px', marginTop: '6px' }}>
                          <button
                            type="button"
                            onClick={() => {
                              setSystemPromptDraft('');
                              handleUpdateKBSettings({ systemPrompt: null });
                              setShowSystemPrompt(false);
                            }}
                            className="icon-button"
                            style={{ color: 'var(--error-color, #e53e3e)', padding: '4px 8px', borderRadius: '6px', fontSize: '0.75rem', display: 'flex', alignItems: 'center', gap: '4px' }}
                            title={language === 'en' ? 'Delete system prompt' : 'System-Prompt löschen'}
                          >
                            <Trash2 size={14} />
                          </button>
                          <button
                            type="button"
                            onClick={() => {
                              const newValue = systemPromptDraft || null;
                              if (newValue !== (currentKb.systemPrompt || null)) {
                                handleUpdateKBSettings({ systemPrompt: newValue });
                              }
                              setShowSystemPrompt(false);
                            }}
                            className="icon-button"
                            style={{ color: 'var(--accent-primary)', padding: '4px 8px', borderRadius: '6px', fontSize: '0.75rem', display: 'flex', alignItems: 'center', gap: '4px' }}
                            title={language === 'en' ? 'Save system prompt' : 'System-Prompt speichern'}
                          >
                            <Check size={14} />
                          </button>
                        </div>
                      </div>
                    )}
                    <div role="group" aria-label={language === 'de' ? 'Nachricht verbessern' : 'Enhance message'} style={{ display: 'flex', gap: '8px', marginBottom: '1rem', flexWrap: 'wrap', justifyContent: 'center' }}>
                      {['rewrite', 'expand', 'spell'].map(m => (
                        <button
                          key={m}
                          type="button"
                          aria-pressed={enhance === m}
                          onClick={() => setEnhance(enhance === m ? null : m as 'rewrite' | 'expand' | 'spell')}
                          className="source-tag"
                          style={{
                            cursor: 'pointer',
                            background: enhance === m ? 'var(--accent-primary)' : 'var(--tag-bg)',
                            color: enhance === m ? 'white' : 'var(--accent-primary)',
                            border: 'none',
                            padding: '4px 12px',
                            transition: 'all 0.2s',
                            opacity: 0.9
                          }}
                        >
                          {m === 'rewrite' ? t('rewrite') : m === 'expand' ? t('expand') : t('spell')}
                        </button>
                      ))}
                    </div>
                    <form className="input-wrapper" onSubmit={chat.handleSendMessage}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '4px' }}>
                        {currentKb && currentKb.userId === user?.id && (
                          <button
                            type="button"
                            onClick={() => setShowSystemPrompt(!showSystemPrompt)}
                            className="settings-toggle"
                            style={{
                              position: 'static',
                              color: showSystemPrompt || currentKb.systemPrompt ? 'var(--accent-primary)' : 'var(--text-secondary)',
                              background: showSystemPrompt || currentKb.systemPrompt ? 'var(--tag-bg)' : 'transparent',
                              padding: '4px',
                              borderRadius: '6px',
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'center',
                              transition: 'all 0.2s',
                              border: showSystemPrompt || currentKb.systemPrompt ? '1px solid var(--accent-primary)' : '1px solid transparent',
                            }}
                            title={t('systemPromptLabel')}
                            aria-label={t('systemPromptLabel')}
                            aria-pressed={showSystemPrompt}
                          >
                            <Settings size={20} aria-hidden="true" />
                          </button>
                        )}
                        {(() => {
                          const selectedConfig = availableConfigs.find(c => c.id === currentKb?.aiConfigId) || availableConfigs.find(c => c.is_active);
                          const model = currentKb?.chatModel || (selectedConfig ? selectedConfig.chat_models[0] : null);
                          const isReasoningCapable = selectedConfig?.reasoning_models?.includes(model || '') || false;

                          if (!isReasoningCapable) return null;

                          return (
                            <button
                              type="button"
                              onClick={() => {
                                triggerHaptic(HAPTIC_PATTERNS.toggle);
                                setReasoningEnabled(!reasoningEnabled);
                              }}
                              className="settings-toggle"
                              style={{
                                position: 'static',
                                color: reasoningEnabled ? 'var(--accent-primary)' : 'var(--text-secondary)',
                                background: reasoningEnabled ? 'var(--tag-bg)' : 'transparent',
                                padding: '4px',
                                borderRadius: '6px',
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'center',
                                transition: 'all 0.2s',
                                border: reasoningEnabled ? '1px solid var(--accent-primary)' : '1px solid transparent'
                              }}
                              title={t('reasoningMode')}
                              aria-label={t('reasoningToggle')}
                              aria-pressed={reasoningEnabled}
                            >
                              <Brain size={20} aria-hidden="true" />
                            </button>
                          );
                        })()}

                        {reasoningEnabled && (() => {
                          const selectedConfig = availableConfigs.find(c => c.id === currentKb?.aiConfigId) || availableConfigs.find(c => c.is_active);
                          const model = currentKb?.chatModel || (selectedConfig ? selectedConfig.chat_models[0] : null);
                          const isReasoningCapable = selectedConfig?.reasoning_models?.includes(model || '') || false;

                          if (!isReasoningCapable) return null;

                          return (
                            <select
                              value={reasoningLevel}
                              onChange={(e) => setReasoningLevel(e.target.value as 'low' | 'medium' | 'high')}
                              className="source-tag"
                              aria-label={t('reasoningLevelLabel')}
                              style={{
                                background: 'var(--tag-bg)',
                                color: 'var(--accent-primary)',
                                border: '1px solid var(--border-color)',
                                padding: '2px 4px',
                                fontSize: '0.75rem',
                                borderRadius: '4px',
                                cursor: 'pointer',
                                outline: 'none'
                              }}
                            >
                              <option value="low">{t('low')}</option>
                              <option value="medium">{t('medium')}</option>
                              <option value="high">{t('high')}</option>
                            </select>
                          );
                        })()}
                      </div>
                      <label htmlFor="chat-message-input" className="sr-only">{t('chatPlaceholder')}</label>
                      <textarea
                        id="chat-message-input"
                        ref={chat.textareaRef}
                        className="chat-input"
                        enterKeyHint="send"
                        placeholder={currentKb?.name ? t('chatPlaceholderContextual').replace('{{name}}', currentKb.name) : t('chatPlaceholder')}
                        value={chat.userMessageInput}
                        onChange={(e) => chat.setUserMessageInput(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' && !e.shiftKey) {
                            e.preventDefault();
                            chat.handleSendMessage(e);
                          }
                        }}
                        rows={1}
                      />
                      <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <span style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                          {selectedFileCount} {selectedFileCount === 1 ? t('sourceCount') : t('sourcesCount')}
                        </span>
                        <button type="submit" className="send-button" disabled={chat.loading || !chat.userMessageInput.trim() || selectedFileCount === 0} aria-label={t('sendMessage')}>
                          <Send size={18} aria-hidden="true" />
                        </button>
                      </div>
                    </form>
                    <div style={{ textAlign: 'center', marginTop: '1rem', fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                      {siteConfigs.chat_footer || t('chatFooter')}
                    </div>
                  </div>
                </div>
                {!isMobile && (
                  <BranchTreeNav
                    messageTree={chat.messageTree}
                    activeLeafId={chat.activeLeafId}
                    onSelectBranch={(leafId) => {
                      chat.setActiveLeafId(leafId);
                      chat.setForkPointId(null);
                    }}
                  />
                )}
              </div>
            )
          ) : kbView === 'research' ? (
            <Suspense fallback={<div className="messages-container" style={{ justifyContent: 'flex-end' }}><MessageSkeleton role="user" /><MessageSkeleton role="ai" /></div>}>
              <div className="content-fade-in" style={{ flex: 1, overflow: 'hidden', padding: '1rem' }}>
                <ResearchMode
                  key={chat.loadedResearchSession?.id || 'new'}
                  kbId={currentKb?.id || ''}
                  onClose={() => setKbView('chat')}
                  loadedSession={chat.loadedResearchSession}
                  onSessionSaved={() => {
                    if (currentKb) chat.fetchChats(currentKb.id);
                  }}
                  onClearSession={() => chat.setLoadedResearchSession(null)}
                  onRunningChange={handleResearchRunningChange}
                />
              </div>
            </Suspense>
          ) : kbView === 'academic_research' ? (
            <Suspense fallback={<div className="messages-container" style={{ justifyContent: 'flex-end' }}><MessageSkeleton role="user" /><MessageSkeleton role="ai" /></div>}>
              <div className="content-fade-in" style={{ flex: 1, overflow: 'hidden', padding: '1rem' }}>
                <AcademicMode
                  key={chat.loadedAcademicSession?.id || 'new'}
                  kbId={currentKb?.id || ''}
                  onClose={() => setKbView('chat')}
                  loadedSession={chat.loadedAcademicSession}
                  onSessionSaved={() => {
                    if (currentKb) chat.fetchChats(currentKb.id);
                  }}
                  onClearSession={() => chat.setLoadedAcademicSession(null)}
                  onRunningChange={handleAcademicResearchRunningChange}
                />
              </div>
            </Suspense>
          ) : kbView === 'studio' ? (
            <Suspense fallback={<div className="messages-container" style={{ justifyContent: 'flex-end' }}><MessageSkeleton role="user" /><MessageSkeleton role="ai" /></div>}>
              <div className="content-fade-in" style={{ flex: 1, overflow: 'hidden' }}>
                <StudioWorkspace
                  kbId={currentKb?.id || ''}
                  generatedContent={generatedContent}
                  onGenerate={handleGenerate}
                  onDeleteContent={handleDeleteGeneratedContent}
                  onClose={() => {
                    setKbView('chat');
                    sidebar.setIsRightSidebarOpen(true);
                  }}
                  studioConfig={currentKb?.isGlobal ? (currentKb.studioConfig || undefined) : undefined}
                  initialSelectedItem={selectedContent}
                  hasFiles={hasFiles}
                />
              </div>
            </Suspense>
          ) : (
            <Suspense fallback={<div className="messages-container" style={{ justifyContent: 'flex-end' }}><MessageSkeleton role="user" /><MessageSkeleton role="ai" /></div>}>
              <div className="content-fade-in" style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
                <Dashboard kbId={currentKb?.id || ''} kbName={currentKb?.name || ''} />
              </div>
            </Suspense>
          )
        }
      </main>
      {showKbSettings && currentKb && (
        <div
          className="modal-overlay"
          style={{ zIndex: 3000 }}
          onClick={() => setShowKbSettings(false)}
          onKeyDown={(e) => { if (e.key === 'Escape') setShowKbSettings(false); }}
          role="presentation"
        >
          {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
          <div
            role="dialog"
            aria-modal="true"
            aria-label="KB settings"
            className="modal-content"
            style={{ maxWidth: '640px', maxHeight: '80vh', overflowY: 'auto' }}
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => e.stopPropagation()}
          >
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '1rem' }}>
              <h3 style={{ margin: 0 }}>KB Settings (RAG Tuning)</h3>
              <button
                type="button"
                onClick={() => setShowKbSettings(false)}
                style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '4px' }}
                aria-label="Close"
              >
                <X size={20} />
              </button>
            </div>
            <Suspense fallback={<div>Loading…</div>}>
              <KbSettingsPanelLazy kbId={currentKb.id} />
            </Suspense>
          </div>
        </div>
      )}
    </div>
  );
};

export const ChatView = memo(ChatViewComp);
