import { useCallback, useState, type ReactNode } from 'react';
import { ChevronDown } from 'lucide-react';
import { useTheme } from '../contexts/ThemeContext';

const STORAGE_PREFIX = 'justrag.home.section.';

// readStored resolves the persisted open/closed state for one section.
// localStorage access is wrapped because it throws outright in Safari's
// private mode and under a blocked-cookies policy — a storage failure must
// cost the user a remembered preference, never the whole overview.
function readStored(id: string, fallback: boolean): boolean {
  try {
    const raw = window.localStorage.getItem(STORAGE_PREFIX + id);
    if (raw === null) return fallback;
    return raw === '1';
  } catch {
    return fallback;
  }
}

function writeStored(id: string, open: boolean): void {
  try {
    window.localStorage.setItem(STORAGE_PREFIX + id, open ? '1' : '0');
  } catch {
    // See readStored: a preference that cannot be persisted is not an error.
  }
}

export interface KbAccordionProps {
  /** Stable key for the persisted open/closed state. */
  id: string;
  title: string;
  icon: ReactNode;
  /** Open state on first visit, before the user has ever toggled the section. */
  defaultOpen: boolean;
  /** Rendered right of the title, e.g. the number of KBs in the section. */
  count?: number;
  /** Rendered at the far end of the header row, e.g. a "create" action. */
  headerAction?: ReactNode;
  children: ReactNode;
}

/**
 * One collapsible section of the Home overview. All four sections (Favoriten,
 * Entdecken, Mit mir geteilt, Meine KBs) use this, so they share one set of
 * header affordances and one persistence scheme.
 *
 * The body is unmounted while collapsed rather than hidden with CSS. That is
 * deliberate and load-bearing for the discovery panel: its fetch hangs off
 * mount, so expanding the section is what re-reads the catalog. A KB that was
 * published after the page loaded therefore shows up on the next expand,
 * which is exactly the reload the section is meant to avoid.
 */
export function KbAccordion({ id, title, icon, defaultOpen, count, headerAction, children }: KbAccordionProps) {
  const { t } = useTheme();
  const [open, setOpen] = useState(() => readStored(id, defaultOpen));

  const toggle = useCallback(() => {
    setOpen((prev) => {
      writeStored(id, !prev);
      return !prev;
    });
  }, [id]);

  const panelId = `home-section-panel-${id}`;
  const buttonId = `home-section-button-${id}`;

  return (
    <section className="home-view__section home-view__accordion">
      <div className="home-view__section-header">
        <button
          type="button"
          id={buttonId}
          className="home-view__accordion-trigger"
          onClick={toggle}
          aria-expanded={open}
          aria-controls={panelId}
        >
          <ChevronDown
            size={18}
            aria-hidden="true"
            className={`home-view__accordion-chevron${open ? ' home-view__accordion-chevron--open' : ''}`}
          />
          {icon}
          <h2 className="home-view__section-title">{title}</h2>
          {count !== undefined && <span className="home-view__accordion-count">{count}</span>}
          <span className="sr-only">{open ? t('collapseSection') : t('expandSection')}</span>
        </button>
        {headerAction && <div className="home-view__accordion-action">{headerAction}</div>}
      </div>
      {open && (
        <div id={panelId} role="region" aria-labelledby={buttonId}>
          {children}
        </div>
      )}
    </section>
  );
}
