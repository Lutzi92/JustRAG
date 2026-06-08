import { useCallback } from 'react';
import { utils, writeFile } from 'xlsx';
import type { StructuredTable } from '../types';

interface Props {
    table: StructuredTable;
}

const RESERVED = new Set(['_file_id', '_file_name', '_source_idx']);

function formatCell(value: unknown, type: string): string {
    if (value === null || value === undefined) return '—';
    if (type === 'number' && typeof value === 'string') {
        const n = Number(value.replace(/[, ]/g, ''));
        if (!Number.isNaN(n)) return String(n);
    }
    return String(value);
}

export function StructuredTableView({ table }: Props) {
    const exportXlsx = useCallback(() => {
        const cols = table.columns.filter((c) => !RESERVED.has(c.key));
        const header = cols.map((c) => c.label);
        const body = table.rows.map((row) =>
            cols.map((c) => {
                const raw = row[c.key];
                if (c.type === 'number' && raw != null) {
                    const n = Number(String(raw).replace(/[, ]/g, ''));
                    return Number.isNaN(n) ? raw : n;
                }
                return raw ?? '';
            }),
        );
        const ws = utils.aoa_to_sheet([header, ...body]);
        const wb = utils.book_new();
        utils.book_append_sheet(wb, ws, 'Comparison');
        writeFile(wb, 'comparison.xlsx');
    }, [table]);

    const columns = table.columns.filter((c) => !RESERVED.has(c.key));

    return (
        <div className="structured-table">
            {table.truncated && (
                <div className="structured-table__truncation" role="status">
                    Showing the top {table.rows.length} of {table.totalFiles} matching files.
                </div>
            )}
            <div className="structured-table__toolbar">
                <button type="button" aria-label="Download table as .xlsx" onClick={exportXlsx}>Download .xlsx</button>
            </div>
            <div className="structured-table__scroll">
                <table>
                    <thead>
                        <tr>{columns.map((c) => <th key={c.key}>{c.label}</th>)}</tr>
                    </thead>
                    <tbody>
                        {table.rows.map((row, i) => (
                            <tr key={String(row._file_id ?? i)}>
                                {columns.map((c) => <td key={c.key}>{formatCell(row[c.key], c.type)}</td>)}
                            </tr>
                        ))}
                    </tbody>
                </table>
            </div>
        </div>
    );
}
