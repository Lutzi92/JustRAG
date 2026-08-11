import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import MessageContent from './MessageContent';
import type { TrajectoryEvent } from '../types';
import * as ThemeContext from '../contexts/ThemeContext';

// Mock the module
vi.mock('../contexts/ThemeContext', () => ({
    useTheme: vi.fn(),
}));

// Stub the export helper — it lazy-loads ExcelJS and touches the DOM/Blob APIs,
// neither of which we want to exercise in a unit test of the wiring.
vi.mock('../utils/exportXlsx', () => ({
    exportRowsToXlsx: vi.fn(),
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

    it('renders a Download .xlsx button on a Markdown table', () => {
        const md = '| Name | Score |\n| --- | --- |\n| Alice | 9 |\n| Bob | 7 |';
        render(<MessageContent content={md} />);
        expect(screen.getByRole('button', { name: /Download \.xlsx/i })).toBeInTheDocument();
    });

    it('does not render a Download button when there is no table', () => {
        render(<MessageContent content="Just prose, no table." />);
        expect(screen.queryByRole('button', { name: /Download \.xlsx/i })).toBeNull();
    });

    it('exports the table cells (incl. header) on click', async () => {
        const { exportRowsToXlsx } = await import('../utils/exportXlsx');

        const md = '| Name | Score |\n| --- | --- |\n| Alice | 9 |\n| Bob | 7 |';
        render(<MessageContent content={md} />);
        fireEvent.click(screen.getByRole('button', { name: /Download \.xlsx/i }));

        expect(exportRowsToXlsx).toHaveBeenCalledWith(
            [
                ['Name', 'Score'],
                ['Alice', '9'],
                ['Bob', '7'],
            ],
            { filename: 'table.xlsx' },
        );
    });

    // The per-answer knowledge-graph control moved out of MessageContent into
    // the MessageBubble §8 footer (rendered as the .graph-pill next to
    // "Quellen · N"), so its tests now live with MessageBubble's footer.

    it('strips citation markers from exported table cells', async () => {
        const { exportRowsToXlsx } = await import('../utils/exportXlsx');
        const sources = [{ index: 1, fileName: 'doc.pdf', fileId: 'f1', content: '', score: 0.9, pages: [4] }];

        // The cell carries an inline citation that renders as a sup.source-ref.
        const md = '| Topic | Detail |\n| --- | --- |\n| Budget | 5000 EUR [1] |';
        render(<MessageContent content={md} sources={sources} />);
        fireEvent.click(screen.getByRole('button', { name: /Download \.xlsx/i }));

        expect(exportRowsToXlsx).toHaveBeenCalledWith(
            [
                ['Topic', 'Detail'],
                ['Budget', '5000 EUR'],
            ],
            { filename: 'table.xlsx' },
        );
    });
});

describe('MessageContent citation source popover', () => {
    const sources = [
        { index: 1, fileName: 'bericht.pdf', fileId: 'f1', content: 'Der zitierte Absatz.', score: 0.9, pages: [7] },
        { index: 2, fileName: 'anhang.pdf', fileId: 'f2', content: 'Zweite Quelle.', score: 0.8, pages: [2] },
    ];

    beforeEach(() => {
        vi.resetAllMocks();
        // The popover renders localized labels, so `t` must be present here —
        // unlike the bare `language`-only stub the other suites use.
        vi.mocked(ThemeContext.useTheme).mockReturnValue({
            language: 'en',
            t: (key: string) => key,
        } as unknown as ReturnType<typeof ThemeContext.useTheme>);
    });

    const firstPill = (container: HTMLElement) => container.querySelector('sup.source-ref') as HTMLElement;

    it('opens the source popover when a citation pill is clicked', () => {
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        expect(screen.queryByRole('dialog')).toBeNull();

        fireEvent.click(firstPill(container));

        const dialog = screen.getByRole('dialog');
        expect(dialog).toHaveTextContent('bericht.pdf');
        expect(dialog).toHaveTextContent('S. 7');
        expect(dialog).toHaveTextContent('Der zitierte Absatz.');
    });

    it('does not open the popover on hover', () => {
        // The whole point of the change: hover is no longer a trigger.
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        fireEvent.pointerOver(firstPill(container));
        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('does not open the document directly when a pill is clicked', () => {
        // Previously a pill click jumped straight into the PDF. Now the click
        // only reveals the popover; opening is an explicit second action.
        const onOpenSource = vi.fn();
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={onOpenSource} />);

        fireEvent.click(firstPill(container));

        expect(onOpenSource).not.toHaveBeenCalled();
        expect(screen.getByRole('dialog')).toBeInTheDocument();
    });

    it('opens the document from the popover button with the clicked source', () => {
        const onOpenSource = vi.fn();
        const { container } = render(<MessageContent content="Claim [2]." sources={sources} onOpenSource={onOpenSource} />);

        fireEvent.click(firstPill(container));
        fireEvent.click(screen.getByRole('button', { name: /openInDocument/i }));

        expect(onOpenSource).toHaveBeenCalledTimes(1);
        expect(onOpenSource).toHaveBeenCalledWith(expect.objectContaining({ fileId: 'f2', fileName: 'anhang.pdf' }));
    });

    it('dismisses the popover when the document is opened', () => {
        // The document viewer opens on top of the answer; leaving the popover
        // up parks it over the newly opened document.
        const onOpenSource = vi.fn();
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={onOpenSource} />);

        fireEvent.click(firstPill(container));
        fireEvent.click(screen.getByRole('button', { name: /openInDocument/i }));

        expect(onOpenSource).toHaveBeenCalledTimes(1);
        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('leaves focus alone when the document is opened', () => {
        // Dismissal normally hands focus back to the pill (for Escape), but the
        // document viewer is taking focus here — pulling it back would yank the
        // user out of the dialog that just opened.
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        const pill = firstPill(container);

        fireEvent.click(pill);
        fireEvent.click(screen.getByRole('button', { name: /openInDocument/i }));

        expect(document.activeElement).not.toBe(pill);
    });

    it('closes the popover when the same pill is clicked again', () => {
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        const pill = firstPill(container);

        fireEvent.click(pill);
        expect(screen.getByRole('dialog')).toBeInTheDocument();

        fireEvent.click(pill);
        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('switches the popover to another citation when a different pill is clicked', () => {
        const { container } = render(<MessageContent content="Both [1] and [2]." sources={sources} onOpenSource={vi.fn()} />);
        const pills = container.querySelectorAll('sup.source-ref');

        fireEvent.click(pills[0]);
        expect(screen.getByRole('dialog')).toHaveTextContent('bericht.pdf');

        fireEvent.click(pills[1]);
        expect(screen.getByRole('dialog')).toHaveTextContent('anhang.pdf');
    });

    it('remounts the popover when switching pills from the keyboard', () => {
        // A keyboard switch never closes the popover in between, so without a
        // remount the anchored position would stay parked on the first pill:
        // useAnchoredPosition measures in a layout effect keyed on `open`, and
        // the trigger is a mutable ref it cannot observe. jsdom reports zero
        // rects, so identity of the dialog node is what we can assert.
        const { container } = render(<MessageContent content="Both [1] and [2]." sources={sources} onOpenSource={vi.fn()} />);
        const pills = container.querySelectorAll('sup.source-ref');

        fireEvent.keyDown(pills[0], { key: 'Enter' });
        const first = screen.getByRole('dialog');
        expect(first).toHaveTextContent('bericht.pdf');

        fireEvent.keyDown(pills[1], { key: 'Enter' });
        const second = screen.getByRole('dialog');
        expect(second).toHaveTextContent('anhang.pdf');
        expect(second).not.toBe(first);
    });

    it('closes the popover on Escape', () => {
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        fireEvent.click(firstPill(container));
        expect(screen.getByRole('dialog')).toBeInTheDocument();

        fireEvent.keyDown(document, { key: 'Escape' });

        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('closes the popover on an outside click', () => {
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        fireEvent.click(firstPill(container));
        expect(screen.getByRole('dialog')).toBeInTheDocument();

        fireEvent.mouseDown(document.body);

        expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('opens the popover from the keyboard with Enter', () => {
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        fireEvent.keyDown(firstPill(container), { key: 'Enter' });
        expect(screen.getByRole('dialog')).toBeInTheDocument();
    });

    it('exposes citation pills as focusable buttons', () => {
        // These attributes have to survive rehype-sanitize: the schema matches
        // HAST property names, so a hyphenated entry would be stripped silently
        // and the pill would drop out of the tab order.
        const { container } = render(<MessageContent content="Claim [1]." sources={sources} onOpenSource={vi.fn()} />);
        const pill = firstPill(container);
        expect(pill.getAttribute('role')).toBe('button');
        expect(pill.getAttribute('tabindex')).toBe('0');
        expect(pill.getAttribute('aria-haspopup')).toBe('dialog');
    });
});
