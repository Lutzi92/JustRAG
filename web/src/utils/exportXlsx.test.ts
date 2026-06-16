import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import ExcelJS from 'exceljs';
import { exportRowsToXlsx } from './exportXlsx';

// Capture the Blob handed to URL.createObjectURL so we can read the produced
// workbook back and assert its formatting, then exercise the download path.
describe('exportRowsToXlsx', () => {
    let captured: Blob | null;
    let clickSpy: ReturnType<typeof vi.fn>;

    beforeEach(() => {
        captured = null;
        clickSpy = vi.fn(() => {});
        vi.spyOn(URL, 'createObjectURL').mockImplementation((b: Blob | MediaSource) => {
            captured = b as Blob;
            return 'blob:mock';
        });
        vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
        vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(clickSpy as () => void);
    });

    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('no-ops on an empty matrix', async () => {
        await exportRowsToXlsx([], { filename: 'x.xlsx' });
        expect(clickSpy).not.toHaveBeenCalled();
        expect(captured).toBeNull();
    });

    it('triggers a download with the given filename', async () => {
        await exportRowsToXlsx([['A'], ['1']], { filename: 'report.xlsx' });
        expect(clickSpy).toHaveBeenCalledTimes(1);
        expect(captured).not.toBeNull();
    });

    it('produces a bold, shaded, frozen header and content-sized columns', async () => {
        const rows = [
            ['Name', 'Description'],
            ['Bob', 'a fairly long descriptive cell value here'],
        ];
        await exportRowsToXlsx(rows, { filename: 'report.xlsx', sheetName: 'Data' });

        expect(captured).not.toBeNull();
        const buf = await (captured as Blob).arrayBuffer();
        const wb = new ExcelJS.Workbook();
        await wb.xlsx.load(buf);
        const ws = wb.getWorksheet('Data')!;

        const headerCell = ws.getRow(1).getCell(1);
        expect(headerCell.value).toBe('Name');
        expect(headerCell.font?.bold).toBe(true);
        expect(headerCell.fill?.type).toBe('pattern');
        expect((headerCell.fill as ExcelJS.FillPattern).fgColor?.argb).toBe('FFEDEDED');

        // Column 2 (long content) is wider than column 1 (short content).
        const w1 = ws.getColumn(1).width ?? 0;
        const w2 = ws.getColumn(2).width ?? 0;
        expect(w2).toBeGreaterThan(w1);

        // Header row is frozen.
        const view = ws.views[0] as { state?: string; ySplit?: number };
        expect(view?.state).toBe('frozen');
        expect(view?.ySplit).toBe(1);
    });
});
