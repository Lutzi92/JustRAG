import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StructuredTableView } from './StructuredTableView';

describe('StructuredTableView', () => {
    it('renders a header per column and a row per data row', () => {
        render(
            <StructuredTableView
                table={{
                    columns: [
                        { key: 'id', label: 'ID', type: 'string' },
                        { key: 'herd', label: 'Herd size', type: 'number' },
                    ],
                    rows: [
                        { id: 'KZ 1', herd: 860 },
                        { id: 'KZ 2', herd: 1200 },
                    ],
                    truncated: false,
                    totalFiles: 2,
                }}
            />,
        );
        expect(screen.getByText('ID')).toBeInTheDocument();
        expect(screen.getByText('Herd size')).toBeInTheDocument();
        expect(screen.getByText('KZ 1')).toBeInTheDocument();
        expect(screen.getByText('1200')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: /xlsx|excel|download/i })).toBeInTheDocument();
    });

    it('shows a truncation banner when truncated', () => {
        render(
            <StructuredTableView
                table={{ columns: [{ key: 'id', label: 'ID', type: 'string' }], rows: [{ id: 'KZ 1' }], truncated: true, totalFiles: 50 }}
            />,
        );
        expect(screen.getByText(/50/)).toBeInTheDocument();
    });
});
