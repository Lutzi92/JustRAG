import { FolderOpen, MessageSquare, Sparkles, History } from 'lucide-react';
import { useTheme } from '../contexts/ThemeContext';
import './MobileTabBar.css';

export type MobileTab = 'history' | 'chat' | 'workspace' | 'files';

interface MobileTabBarProps {
    activeTab: MobileTab;
    onTabChange: (tab: MobileTab) => void;
}

type LabelKey = 'tabHistory' | 'tabChat' | 'tabWorkspace' | 'tabFiles';

// Reihenfolge entspricht der Desktop-Anordnung (Verlauf links, Quellen
// rechts) und damit auch der Swipe-Richtung in useViewState.
const TABS: { id: MobileTab; icon: typeof FolderOpen; labelKey: LabelKey }[] = [
    { id: 'history', icon: History, labelKey: 'tabHistory' },
    { id: 'chat', icon: MessageSquare, labelKey: 'tabChat' },
    { id: 'workspace', icon: Sparkles, labelKey: 'tabWorkspace' },
    { id: 'files', icon: FolderOpen, labelKey: 'tabFiles' },
];

export function MobileTabBar({ activeTab, onTabChange }: MobileTabBarProps) {
    const { t } = useTheme();

    return (
        <nav className="mobile-tab-bar" aria-label="Navigation">
            {TABS.map(({ id, icon: Icon, labelKey }) => (
                <button
                    key={id}
                    className={`mobile-tab-bar__tab ${activeTab === id ? 'mobile-tab-bar__tab--active' : ''}`}
                    onClick={() => onTabChange(id)}
                    aria-current={activeTab === id ? 'page' : undefined}
                >
                    <span className="mobile-tab-bar__icon">
                        <Icon size={20} />
                    </span>
                    {t(labelKey)}
                </button>
            ))}
        </nav>
    );
}
