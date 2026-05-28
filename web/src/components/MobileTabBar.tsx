import { FolderOpen, MessageSquare, Sparkles } from 'lucide-react';
import { useTheme } from '../contexts/ThemeContext';
import './MobileTabBar.css';

export type MobileTab = 'files' | 'chat' | 'studio';

interface MobileTabBarProps {
    activeTab: MobileTab;
    onTabChange: (tab: MobileTab) => void;
}

const TABS: { id: MobileTab; icon: typeof FolderOpen; labelKey: 'tabFiles' | 'tabChat' | 'tabStudio' }[] = [
    { id: 'files', icon: FolderOpen, labelKey: 'tabFiles' },
    { id: 'chat', icon: MessageSquare, labelKey: 'tabChat' },
    { id: 'studio', icon: Sparkles, labelKey: 'tabStudio' },
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
