import { createContext, useContext, type ReactNode } from 'react';
import type { RssFeed, GeneratedContent, ConfluenceSource, ConfluenceConnectionInfo, ConfluenceSpace, ConfluencePage, ConfluencePageWithPath } from '../types';
import type { useFileManagement } from '../hooks/useFileManagement';
import type { useWebTools } from '../hooks/useWebTools';
import type { useGeneratedContent } from '../hooks/useGeneratedContent';
import type { useSharing } from '../hooks/useSharing';

export interface KbDataContextValue {
  fileMgmt: ReturnType<typeof useFileManagement>;
  webTools: ReturnType<typeof useWebTools>;
  content: ReturnType<typeof useGeneratedContent>;
  sharing: ReturnType<typeof useSharing>;
  // RSS
  rssFeeds: RssFeed[];
  rssLoading: boolean;
  fetchRssFeeds: (kbId?: string) => void;
  addRssFeed: (url: string, pollInterval: number) => void;
  updateRssFeed: (feedId: string, updates: { pollInterval?: number; status?: 'active' | 'paused' }) => void;
  deleteRssFeed: (feedId: string) => void;
  pollFeedNow: (feedId: string) => void;
  // Confluence
  confluenceSources: ConfluenceSource[];
  confluenceConnection: ConfluenceConnectionInfo | null;
  confluenceLoading: boolean;
  fetchConfluenceSources: (kbId?: string) => void;
  fetchConfluenceConnectionInfo: () => void;
  saveConfluenceConnection: (token: string, displayName?: string) => Promise<void>;
  addConfluenceSource: (data: { connectionId: string; spaceKey: string; rootPageId?: string; rootPageTitle?: string; includeAttachments?: boolean; syncInterval?: number }) => void;
  updateConfluenceSource: (sourceId: string, updates: { includeAttachments?: boolean; syncInterval?: number | null; status?: 'active' | 'paused' }) => void;
  deleteConfluenceSource: (sourceId: string) => void;
  syncConfluenceNow: (sourceId: string) => void;
  fetchConfluenceSpaces: () => Promise<ConfluenceSpace[]>;
  fetchConfluenceSpacePages: (spaceKey: string) => Promise<ConfluencePage[]>;
  fetchConfluencePageChildren: (pageId: string) => Promise<ConfluencePage[]>;
  fetchConfluenceAllSpacePages: (spaceKey: string) => Promise<ConfluencePageWithPath[]>;
  // Actions
  handleSelectContent: (item: GeneratedContent) => void;
  handleExpandStudio: () => void;
}

const KbDataContext = createContext<KbDataContextValue | null>(null);

export function KbDataProvider({ value, children }: { value: KbDataContextValue; children: ReactNode }) {
  return <KbDataContext.Provider value={value}>{children}</KbDataContext.Provider>;
}

export function useKbData(): KbDataContextValue {
  const ctx = useContext(KbDataContext);
  if (!ctx) throw new Error('useKbData must be used within KbDataProvider');
  return ctx;
}
