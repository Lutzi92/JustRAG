import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import MessageContent from './MessageContent';
import type { TrajectoryEvent } from '../types';
import * as ThemeContext from '../contexts/ThemeContext';

// Mock the module
vi.mock('../contexts/ThemeContext', () => ({
    useTheme: vi.fn(),
}));

describe('MessageContent', () => {
    beforeEach(() => {
        // Reset mock before each test
        vi.resetAllMocks();
        // Default mock implementation
        vi.mocked(ThemeContext.useTheme).mockReturnValue({ language: 'en' } as ReturnType<typeof ThemeContext.useTheme>);
    });

    it('renders content correctly', () => {
        render(<MessageContent content="Hello world" />);
        expect(screen.getByText('Hello world')).toBeInTheDocument();
    });

    it('renders reasoning when provided', () => {
        render(<MessageContent content="Hello" reasoning="Thinking process" />);
        expect(screen.getByText('Thinking process')).toBeInTheDocument();
        // Default language 'en' -> 'Chain of Thought'
        expect(screen.getByText('Chain of Thought')).toBeInTheDocument();
    });

    it('renders reasoning with correct label for German', () => {
        // Override mock for this test
        vi.mocked(ThemeContext.useTheme).mockReturnValue({ language: 'de' } as ReturnType<typeof ThemeContext.useTheme>);

        render(<MessageContent content="Hallo" reasoning="Denkprozess" />);
        expect(screen.getByText('Gedankengang')).toBeInTheDocument();
    });

    it('shows thinking indicator when isThinking is true', () => {
        const { container } = render(<MessageContent content="" reasoning="Thinking..." isThinking={true} />);
        // Check if details is open
        const details = container.querySelector('details');
        expect(details).toHaveAttribute('open');
        // Check for cursor blink span which contains '|'
        expect(screen.getByText('|')).toBeInTheDocument();
    });

    it('renders markdown content', () => {
        render(<MessageContent content="**Bold**" />);
        // ReactMarkdown renders bold as strong
        expect(screen.getByText('Bold').tagName).toBe('STRONG');
    });

    it('wraps plain [N] citations in a clickable source-ref sup', () => {
        const sources = [{ index: 1, fileName: 'doc.pdf', fileId: 'f1', content: '', score: 0.9, pages: [4] }];
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} />);
        const sup = container.querySelector('sup.source-ref');
        expect(sup).not.toBeNull();
        expect(sup?.getAttribute('data-source-index')).toBe('1');
    });

    it('wraps markdown-escaped \\[N\\] citations the same as plain ones', () => {
        // Some LLMs intermittently escape markdown brackets ("\[1\]") when
        // generating prose. Without this tolerance the citation would render
        // as plain text and lose its click-to-open-PDF handler.
        const sources = [{ index: 1, fileName: 'doc.pdf', fileId: 'f1', content: '', score: 0.9, pages: [4] }];
        const { container } = render(<MessageContent content="Claim \[1\]." sources={sources} />);
        const sup = container.querySelector('sup.source-ref');
        expect(sup).not.toBeNull();
        expect(sup?.getAttribute('data-source-index')).toBe('1');
    });

    it('wraps grouped [1, 2] citations as separate clickable source-refs', () => {
        // Follow-up turns commonly synthesize across several chunks and emit
        // grouped citations. Each index must become its own clickable sup so
        // it opens the right source — and the marker must not be left as
        // plain text (the original single-index regex missed this entirely).
        const sources = [
            { index: 1, fileName: 'a.pdf', fileId: 'f1', content: '', score: 0.9, pages: [4] },
            { index: 2, fileName: 'b.pdf', fileId: 'f2', content: '', score: 0.8, pages: [7] },
        ];
        const { container } = render(<MessageContent content="Both reports agree [1, 2]." sources={sources} />);
        const sups = container.querySelectorAll('sup.source-ref');
        expect(sups.length).toBe(2);
        expect(sups[0].getAttribute('data-source-index')).toBe('1');
        expect(sups[1].getAttribute('data-source-index')).toBe('2');
    });

    it('collapses same-file indices in a marker into a single link with merged pages', () => {
        // The model often cites several chunks of one document in one marker
        // ([1, 4, 13]). Rendering three links to the same file is confusing —
        // collapse to one link, keeping the first index and merging the pages.
        const sources = [
            { index: 1, fileName: 'report.pdf', fileId: 'f1', content: '', score: 0.9, pages: [4] },
            { index: 2, fileName: 'other.pdf', fileId: 'f2', content: '', score: 0.8, pages: [2] },
            { index: 3, fileName: 'report.pdf', fileId: 'f1', content: '', score: 0.7, pages: [9] },
        ];
        const { container } = render(<MessageContent content="See the analysis [1, 3]." sources={sources} />);
        const sups = container.querySelectorAll('sup.source-ref');
        // [1, 3] are both report.pdf → one link, not two.
        expect(sups.length).toBe(1);
        expect(sups[0].getAttribute('data-source-index')).toBe('1');
        // Pages from both chunks (4 and 9) merge into the label.
        expect(sups[0].textContent).toContain('4, 9');
    });

    it('keeps distinct-file indices in a marker as separate links', () => {
        const sources = [
            { index: 1, fileName: 'report.pdf', fileId: 'f1', content: '', score: 0.9, pages: [4] },
            { index: 2, fileName: 'other.pdf', fileId: 'f2', content: '', score: 0.8, pages: [2] },
        ];
        const { container } = render(<MessageContent content="Two docs [1, 2]." sources={sources} />);
        const sups = container.querySelectorAll('sup.source-ref');
        expect(sups.length).toBe(2);
    });

    it('wraps 3-digit [150] citations in a clickable source-ref sup', () => {
        const sources = Array.from({ length: 150 }, (_, i) => ({
            index: i + 1, fileName: `doc${i + 1}.pdf`, fileId: `f${i + 1}`, content: '', score: 0.5,
        }));
        const { container } = render(<MessageContent content="Per the appendix [150]." sources={sources} />);
        const sup = container.querySelector('sup.source-ref');
        expect(sup).not.toBeNull();
        expect(sup?.getAttribute('data-source-index')).toBe('150');
    });

    it('hides the trajectory panel when no trajectory is provided', () => {
        render(<MessageContent content="answer" />);
        expect(screen.queryByTestId('trajectory-panel')).toBeNull();
    });

    it('renders the trajectory panel collapsed and expands on click for a 3-hop trajectory', () => {
        const trajectory: TrajectoryEvent[] = [
            { stage: 'hop', step: 1, query: 'who lead the project' },
            { stage: 'decision', decision: 'crag_proceed', reason: 'relevant_chunks_sufficient' },
            { stage: 'hop', step: 2, query: 'what is their email', findings: 1, chunks: [{ idx: 1, file_name: 'team.md', score: 0.91 }] },
            { stage: 'answer', step: 2, decision: 'early_stop_sufficient' },
        ];
        render(<MessageContent content="answer" trajectory={trajectory} />);

        const toggle = screen.getByRole('button', { name: /Reasoning steps/i });
        // Collapsed by default — query is not in the DOM yet.
        expect(screen.queryByText(/who lead the project/i)).toBeNull();

        fireEvent.click(toggle);

        // Expanded — every stage's content is now rendered.
        expect(screen.getByText(/who lead the project/i)).toBeInTheDocument();
        expect(screen.getByText(/what is their email/i)).toBeInTheDocument();
        expect(screen.getByText(/crag_proceed/i)).toBeInTheDocument();
        expect(screen.getByText(/early_stop_sufficient/i)).toBeInTheDocument();
    });
});
