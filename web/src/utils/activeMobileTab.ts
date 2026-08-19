import type { MobileTab } from '../components/MobileTabBar';

/**
 * Derives which mobile tab is actually displayed, given the raw `mobileTab`
 * state and the current `kbView`.
 *
 * `mobileTab` and `kbView` can drift: 'chat' and 'workspace' both render
 * `ChatView` and are told apart only by `kbView`, but `ChatView`'s own header
 * tabs (icon-only on mobile) call `setKbView('workspace')` directly — they
 * don't go through the tab-bar/swipe path that keeps `mobileTab` in sync. So
 * for those two raw values the *displayed* tab has to be re-derived from
 * `kbView`, not trusted at face value. 'history' and 'files' render their own
 * panel and are never ambiguous.
 *
 * Shared by `KbWorkspaceLayout` (which content to render) and `useViewState`
 * (where a swipe should start counting from) so the two can't drift from each
 * other the way `mobileTab` and `kbView` themselves can.
 */
export function deriveActiveMobileTab(mobileTab: MobileTab, kbView: string): MobileTab {
    if (mobileTab === 'history' || mobileTab === 'files') return mobileTab;
    return kbView === 'workspace' ? 'workspace' : 'chat';
}
