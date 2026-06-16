// Shared .xlsx export for chat tables (generic Markdown tables + corpus
// StructuredTable). Uses ExcelJS (not SheetJS community, which can't write cell
// styles) so headers can be bold/shaded and columns sized to their content.
//
// ExcelJS is loaded dynamically inside the click handler so its sizable bundle
// stays out of the initial chat payload and is only fetched when a user
// actually exports.

export type CellValue = string | number | null | undefined;

interface ExportOptions {
    filename: string;
    sheetName?: string;
}

// Column width is sized to the widest cell in the column, clamped so a single
// long cell can't blow the sheet out to thousands of characters.
const MIN_COL_WIDTH = 8;
const MAX_COL_WIDTH = 60;
const COL_WIDTH_PADDING = 2;

// exportRowsToXlsx writes a row-major matrix (header row first) to a styled
// .xlsx and triggers a browser download. Header row: bold, light-grey fill,
// thin bottom border, frozen. No-op on an empty matrix.
export async function exportRowsToXlsx(rows: CellValue[][], opts: ExportOptions): Promise<void> {
    if (rows.length === 0) return;

    const ExcelJS = (await import('exceljs')).default;
    const wb = new ExcelJS.Workbook();
    const ws = wb.addWorksheet(opts.sheetName ?? 'Sheet1');

    ws.addRows(rows.map((r) => r.map((c) => (c == null ? '' : c))));

    const header = ws.getRow(1);
    header.font = { bold: true };
    header.eachCell((cell) => {
        cell.fill = { type: 'pattern', pattern: 'solid', fgColor: { argb: 'FFEDEDED' } };
        cell.border = { bottom: { style: 'thin', color: { argb: 'FFCCCCCC' } } };
        cell.alignment = { vertical: 'middle' };
    });

    const colCount = rows.reduce((max, r) => Math.max(max, r.length), 0);
    for (let c = 0; c < colCount; c++) {
        let widest = MIN_COL_WIDTH;
        for (const r of rows) {
            const v = r[c];
            const len = v == null ? 0 : String(v).length;
            if (len > widest) widest = len;
        }
        ws.getColumn(c + 1).width = Math.min(widest + COL_WIDTH_PADDING, MAX_COL_WIDTH);
    }

    ws.views = [{ state: 'frozen', ySplit: 1 }];

    const buffer = await wb.xlsx.writeBuffer();
    const blob = new Blob([buffer], {
        type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = opts.filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
}
