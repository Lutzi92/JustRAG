import React from 'react';
import { PanelLeftClose, PanelRightClose } from 'lucide-react';
import { useIsMobileContext } from '../../contexts/MobileContext';
import { viewportHeight } from '../../utils/viewport';
import '../sidebar-primitives.css';
import './SidebarShell.css';

export interface SidebarShellProps {
  /** Bestimmt Randseite und Richtung der Einklapp-Icons. */
  side: 'left' | 'right';
  isOpen: boolean;
  width: number;
  onExpand: () => void;
  onCollapse: () => void;
  expandLabel: string;
  collapseLabel: string;
  /** Optionale Icon-Leiste im zugeklappten Zustand. */
  collapsedPreview?: React.ReactNode;
  children: React.ReactNode;
}

/**
 * Rahmen beider KB-Seitenleisten: Breite, Einklapp-Zustand, Randseite und
 * Mobile-Höhe. Vorher lag diese Logik dupliziert in SidebarLeft und
 * SidebarRight; seit dem Panel-Tausch (Verlauf links, Quellen rechts) ist die
 * Position eine Prop und kein Bestandteil der Komponente mehr.
 *
 * Der Einklapp-Button ist bewusst NICHT Teil von `children`: er ist das
 * einzige Bedienelement, das im zugeklappten Zustand existiert, und gehört
 * damit zum Rahmen.
 */
export function SidebarShell({
  side, isOpen, width, onExpand, onCollapse,
  expandLabel, collapseLabel, collapsedPreview, children,
}: SidebarShellProps) {
  const isMobile = useIsMobileContext();
  const open = isMobile ? true : isOpen;
  const CollapseIcon = side === 'left' ? PanelLeftClose : PanelRightClose;

  return (
    <aside
      className={`sidebar sidebar-shell sidebar-shell--${side} sidebar-ui__panel${isMobile ? ' sidebar-shell--mobile' : ''}`}
      style={{
        width: isMobile ? '100%' : (open ? `${width}px` : '60px'),
        height: isMobile ? viewportHeight('calc(100dvh - 60px)', 'calc(100vh - 60px)') : undefined,
      }}
    >
      {!isMobile && !open && (
        <button
          type="button"
          onClick={onExpand}
          className="settings-toggle sidebar-shell__collapsed sidebar-ui__collapsed"
          aria-label={expandLabel}
          aria-expanded={false}
        >
          <div className="sidebar-ui__collapsed-icon-wrap">
            <CollapseIcon size={20} />
          </div>
          {collapsedPreview}
        </button>
      )}

      <div
        className="sidebar-shell__body"
        style={{ display: open ? 'flex' : 'none' }}
      >
        {!isMobile && (
          <div className="sidebar-shell__collapse-row">
            <button
              type="button"
              onClick={onCollapse}
              className="sidebar-shell__collapse-btn"
              title={collapseLabel}
              aria-label={collapseLabel}
              aria-expanded={true}
            >
              <CollapseIcon size={20} />
            </button>
          </div>
        )}
        {children}
      </div>
    </aside>
  );
}
