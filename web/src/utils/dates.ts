// Shared date formatting for the Wave-3 "freshness" surface (source dates on
// citations/source cards, admin KB-overview staleness/last-sync columns, the
// Home KB-card freshness chip). Centralizing this means those surfaces cannot
// drift on edge-case handling (missing/invalid input) the way four
// independent copies could.
//
// Both functions degrade to the em-dash placeholder the app already used for
// "no value" (KBOverviewDashboard's original formatRelative) rather than
// throwing or rendering "Invalid Date" — every backend field behind these is
// optional (old messages have no createdAt/publishedAt, non-RSS files have no
// publishedAt, a KB with no synced source has no lastSyncAt).

const DATE_PLACEHOLDER = '—';

// formatDate renders an absolute, locale-aware calendar date (no time-of-day
// — a source date only needs day-level precision).
export function formatDate(iso: string | undefined, lang: 'de' | 'en'): string {
    if (!iso) return DATE_PLACEHOLDER;
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return DATE_PLACEHOLDER;
    return new Intl.DateTimeFormat(lang, { year: 'numeric', month: '2-digit', day: '2-digit' }).format(d);
}

// formatRelative renders locale-aware relative time ("vor 2 Std." / "2 hr.
// ago"). Moved from KBOverviewDashboard.tsx, which built one
// Intl.RelativeTimeFormat per render and threaded it through; this version
// takes the plain language code instead so every caller (citation popover,
// source cards, the dashboard, the Home chip) can call it the same way.
// Building the formatter per call is negligible at UI-render volume.
export function formatRelative(iso: string | undefined, lang: 'de' | 'en'): string {
    if (!iso) return DATE_PLACEHOLDER;
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return DATE_PLACEHOLDER;
    const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
    const diffMs = then - Date.now(); // negative => in the past
    const sec = Math.round(diffMs / 1000);
    const min = Math.round(diffMs / 60000);
    const hr = Math.round(diffMs / 3600000);
    const day = Math.round(diffMs / 86400000);
    if (Math.abs(sec) < 60) return rtf.format(sec, 'second');
    if (Math.abs(min) < 60) return rtf.format(min, 'minute');
    if (Math.abs(hr) < 24) return rtf.format(hr, 'hour');
    return rtf.format(day, 'day');
}
