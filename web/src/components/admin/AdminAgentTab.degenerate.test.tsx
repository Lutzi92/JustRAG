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
// AdminAgentTab.ragas.test.tsx.
function openValidation() {
    const btn = screen.getByRole('button', { name: new RegExp(tMock('agentSectionValidation')) });
    if (btn.getAttribute('aria-expanded') !== 'true') fireEvent.click(btn);
}

function Host({ initial, onChange }: { initial: Record<string, string>; onChange: (c: Record<string, string>) => void }) {
    const [siteConfigs, setSiteConfigs] = useState(initial);
    onChange(siteConfigs);
    return <AdminAgentTab siteConfigs={siteConfigs} setSiteConfigs={setSiteConfigs} onSubmit={e => e.preventDefault()} />;
}

describe('AdminAgentTab — chat_answer_degenerate_run_limit (Wave-5 Task 7)', () => {
    beforeEach(() => {
        try { localStorage.setItem('admin-agent-sections-open-v1', JSON.stringify({ validation: true })); } catch { /* jsdom */ }
    });

    it('renders the field defaulted to 400 and binds edits back to siteConfigs', () => {
        let latest: Record<string, string> = {};
        render(<Host initial={{}} onChange={c => { latest = c; }} />);
        openValidation();

        const input = screen.getByLabelText(tMock('chatAnswerDegenerateRunLimit')) as HTMLInputElement;
        expect(input.type).toBe('number');
        // 0 must be reachable from the UI — it is the kill switch.
        expect(input.min).toBe('0');
        expect(input.max).toBe('100000');
        expect(input.value).toBe('400');

        fireEvent.change(input, { target: { value: '0' } });
        expect(latest.chat_answer_degenerate_run_limit).toBe('0');
    });

    it('reflects an existing site_config value instead of the default', () => {
        render(<Host initial={{ chat_answer_degenerate_run_limit: '1200' }} onChange={() => {}} />);
        openValidation();

        const input = screen.getByLabelText(tMock('chatAnswerDegenerateRunLimit')) as HTMLInputElement;
        expect(input.value).toBe('1200');
    });
});
