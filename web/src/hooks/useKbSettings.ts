import { useState, useCallback } from 'react';
import axios from 'axios';
import type { SafeAIConfig } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';

export function useKbSettings() {
  const { t } = useTheme();
  const toast = useToast();

  const [isPro, setIsPro] = useState(false);
  const [showSettings, setShowSettings] = useState(false);
  const [enhance, setEnhance] = useState<'rewrite' | 'expand' | 'spell' | null>(null);
  const [availableConfigs, setAvailableConfigs] = useState<SafeAIConfig[]>([]);
  const [reasoningEnabled, setReasoningEnabled] = useState(false);
  const [reasoningLevel, setReasoningLevel] = useState<'low' | 'medium' | 'high'>('low');
  const [researchRunning, setResearchRunning] = useState(false);
  const [academicResearchRunning, setAcademicResearchRunning] = useState(false);

  const fetchAvailableConfigs = useCallback(async () => {
    try {
      const res = await axios.get(`${API_BASE_URL}/api/public/configs`);
      setAvailableConfigs(res.data);
    } catch (err: unknown) {
      console.error('Failed to fetch configs:', err);
      toast.error(t('configsFetchError'));
    }
  }, []);

  return {
    isPro, setIsPro,
    showSettings, setShowSettings,
    enhance, setEnhance,
    availableConfigs,
    reasoningEnabled, setReasoningEnabled,
    reasoningLevel, setReasoningLevel,
    researchRunning, setResearchRunning,
    academicResearchRunning, setAcademicResearchRunning,
    fetchAvailableConfigs,
  };
}
