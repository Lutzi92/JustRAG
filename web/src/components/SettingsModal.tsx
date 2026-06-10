import React, { useState } from 'react';
import { X } from 'lucide-react';
import type { KnowledgeBase, SafeAIConfig } from '../types';
import { useTheme } from '../contexts/ThemeContext';

interface SettingsModalProps {
    show: boolean;
    onClose: () => void;
    currentKb: KnowledgeBase | null;
    availableConfigs: SafeAIConfig[];
    onUpdateSettings: (data: Record<string, unknown>) => void;
}

const SettingsModalContent: React.FC<Omit<SettingsModalProps, 'show'>> = ({
    onClose, currentKb, availableConfigs, onUpdateSettings
}) => {
    const { t } = useTheme();
    const [systemPrompt, setSystemPrompt] = useState(currentKb?.systemPrompt || '');

    return (
        <div className="modal-overlay" role="presentation" onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
            <div className="modal-content" style={{ maxWidth: '600px' }} role="dialog" aria-modal="true" aria-labelledby="settings-modal-title">
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                    <h3 id="settings-modal-title" style={{ margin: 0 }}>{t('settingsTitle')}: {currentKb?.name}</h3>
                    <button onClick={onClose} className="icon-button" aria-label={t('closeSettings')}><X size={20} /></button>
                </div>

                <div style={{ display: 'grid', gap: '1.5rem' }}>
                    <div className="input-group">
                        <span style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block', fontWeight: 600 }}>{t('ragMode')}</span>
                        <div style={{ color: 'var(--text-primary)', fontSize: '0.9rem', padding: '0.75rem', background: 'var(--bg-secondary)', borderRadius: '8px', border: '1px solid var(--border-color)' }}>
                            {t('vectorSearchOnly')}
                        </div>
                    </div>

                    <div className="input-group">
                        <label htmlFor="kb-language" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('languageFulltextSearch')}</label>
                        <select
                            id="kb-language"
                            value={currentKb?.language || 'de'}
                            onChange={(e) => onUpdateSettings({ language: e.target.value as 'de' | 'en' })}
                            style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                        >
                            <option value="de">Deutsch</option>
                            <option value="en">English</option>
                        </select>
                    </div>

                    <div className="input-group">
                        <label htmlFor="kb-system-prompt" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('systemPromptLabel')}</label>
                        <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 0.5rem' }}>{t('systemPromptDescription')}</p>
                        <textarea
                            id="kb-system-prompt"
                            value={systemPrompt}
                            onChange={(e) => setSystemPrompt(e.target.value)}
                            onBlur={() => {
                                const newValue = systemPrompt || null;
                                if (newValue !== (currentKb?.systemPrompt || null)) {
                                    onUpdateSettings({ systemPrompt: newValue });
                                }
                            }}
                            placeholder={t('systemPromptPlaceholder')}
                            maxLength={4000}
                            rows={4}
                            style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', resize: 'vertical', fontFamily: 'inherit' }}
                        />
                        <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', textAlign: 'right', marginTop: '0.25rem' }}>
                            {systemPrompt.length} / 4000
                        </div>
                    </div>

                    <div className="input-group">
                        <label htmlFor="ai-config" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('aiProviderConfig')}</label>
                        <select
                            id="ai-config"
                            value={currentKb?.aiConfigId || ''}
                            onChange={(e) => onUpdateSettings({ aiConfigId: e.target.value || null, chatModel: null, embeddingModel: null })}
                            style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                        >
                            <option value="">{t('defaultSystemConfig')}</option>
                            {availableConfigs.map(c => (
                                <option key={c.id} value={c.id}>{c.name} ({c.provider}){c.is_active ? ` - ${t('activeDefault')}` : ''}</option>
                            ))}
                        </select>
                    </div>

                    {(() => {
                        const selectedConfig = availableConfigs.find(c => c.id === currentKb?.aiConfigId) || availableConfigs.find(c => c.is_active);
                        if (!selectedConfig) return null;

                        return (
                            <>
                                <div className="input-group">
                                    <label htmlFor="chat-model" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('chatModel')}</label>
                                    <select
                                        id="chat-model"
                                        value={currentKb?.chatModel || ''}
                                        onChange={(e) => onUpdateSettings({ chatModel: e.target.value || null })}
                                        style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                                    >
                                        <option value="">{currentKb?.aiConfigId ? t('providerDefault') : t('systemDefault')}</option>
                                        {(selectedConfig.chat_models || []).map(m => (
                                            <option key={m} value={m}>{m}</option>
                                        ))}
                                    </select>
                                </div>

                                <div className="input-group">
                                    <label htmlFor="embedding-model" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('embeddingModel')}</label>
                                    <select
                                        id="embedding-model"
                                        value={currentKb?.embeddingModel || ''}
                                        onChange={(e) => onUpdateSettings({ embeddingModel: e.target.value || null })}
                                        style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                                    >
                                        <option value="">{currentKb?.aiConfigId ? t('providerDefault') : t('systemDefault')}</option>
                                        {(selectedConfig.embedding_models || []).map(m => (
                                            <option key={m} value={m}>{m}</option>
                                        ))}
                                    </select>
                                </div>

                                <div className="input-group">
                                    <label htmlFor="rerank-model" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('rerankModel')}</label>
                                    <select
                                        id="rerank-model"
                                        value={currentKb?.rerankModel || ''}
                                        onChange={(e) => onUpdateSettings({ rerankModel: e.target.value || null })}
                                        style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                                    >
                                        <option value="">{t('standard')}</option>
                                        {(selectedConfig.rerank_models || []).map(m => (
                                            <option key={m} value={m}>{m}</option>
                                        ))}
                                    </select>
                                </div>

                                <div className="input-group">
                                    <label htmlFor="tts-model" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('ttsModelLabel')}</label>
                                    <select
                                        id="tts-model"
                                        value={currentKb?.ttsModel || ''}
                                        onChange={(e) => onUpdateSettings({ ttsModel: e.target.value || null })}
                                        style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                                    >
                                        <option value="">{t('noneDisabled')}</option>
                                        {(selectedConfig.tts_models || []).map(m => (
                                            <option key={m} value={m}>{m}</option>
                                        ))}
                                    </select>
                                </div>
                            </>
                        );
                    })()}
                </div>

                <div style={{ marginTop: '2rem' }}>
                    <button onClick={onClose} className="search-button" style={{ width: '100%' }}>{t('closeSettings')}</button>
                </div>
            </div>
        </div>
    );
};

export const SettingsModal: React.FC<SettingsModalProps> = ({ show, ...rest }) => {
    if (!show) return null;
    // Key on the KB id so local edit state resets when a different KB is opened.
    return <SettingsModalContent key={rest.currentKb?.id ?? 'none'} {...rest} />;
};
