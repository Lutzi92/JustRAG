import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { useState } from 'react';
import AdminAgentTab from './AdminAgentTab';
import { translations } from '../../translations';

// Real English strings so labels can be matched by text.
const tMock = (key: string) => {
    const entry = translations[key as keyof typeof translations];
    return entry ? entry.en : key;
};
const themeMock = { t: tMock };
vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => themeMock }));
vi.mock('../../hooks/useReducedMotion', () => ({ useReducedMotion: () => true, getMotionProps: () => ({}) }));
vi.mock('./AdminAgentMetricsCard', () => ({ default: () => null }));
vi.mock('./AdminMCPSection', () => ({ default: () => null }));
vi.mock('framer-motion', () => ({
    motion: { div: ({ children, ...rest }: { children?: React.ReactNode } & Record<string, unknown>) => <div className={rest.className as string}>{children}</div> },
}));

// Open the validation section whichever way the persisted open-map came up
// (jsdom localStorage is not reliable across environments) — same pattern as
// AdminAgentTab.docling.test.tsx's openIngestion.
function openValidation() {
    const btn = screen.getByRole('button', { name: new RegExp(tMock('agentSectionValidation')) });
    if (btn.getAttribute('aria-expanded') !== 'true') fireEvent.click(btn);
}

// A tiny host that owns siteConfigs the way AdminPanel does, so the test
// observes real state updates rather than a mocked setter.
function Host({ initial, onChange }: { initial: Record<string, string>; onChange: (c: Record<string, string>) => void }) {
    const [siteConfigs, setSiteConfigs] = useState(initial);
    onChange(siteConfigs);
    return <AdminAgentTab siteConfigs={siteConfigs} setSiteConfigs={setSiteConfigs} onSubmit={e => e.preventDefault()} />;
}

describe('AdminAgentTab — ragas_samples_retention_days (Wave-5 Task 2)', () => {
    beforeEach(() => {
        try { localStorage.setItem('admin-agent-sections-open-v1', JSON.stringify({ validation: true })); } catch { /* jsdom */ }
    });

    it('renders the field defaulted to 90 and binds edits back to siteConfigs', () => {
        let latest: Record<string, string> = {};
        render(<Host initial={{}} onChange={c => { latest = c; }} />);
        openValidation();

        const input = screen.getByLabelText(tMock('ragasSamplesRetentionDays')) as HTMLInputElement;
        expect(input.type).toBe('number');
        expect(input.min).toBe('1');
        expect(input.max).toBe('3650');
        expect(input.value).toBe('90');

        fireEvent.change(input, { target: { value: '30' } });
        expect(latest.ragas_samples_retention_days).toBe('30');
    });

    it('reflects an existing site_config value instead of the default', () => {
        render(<Host initial={{ ragas_samples_retention_days: '180' }} onChange={() => {}} />);
        openValidation();

        const input = screen.getByLabelText(tMock('ragasSamplesRetentionDays')) as HTMLInputElement;
        expect(input.value).toBe('180');
    });
});
