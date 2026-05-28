import { API_BASE_URL, authFetch } from '../../api';

/**
 * Export query results as a CSV file download.
 */
export async function exportCsv(
    kbId: string,
    fileId: string,
    query: {
        groupBy?: string;
        aggregations?: { column: string; fn: string; alias?: string }[];
        filters?: { column: string; op: string; value: string | number | boolean }[];
        orderBy?: { column: string; direction: 'ASC' | 'DESC' };
    }
): Promise<void> {
    const response = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/data-explorer/export`, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
        },
        body: JSON.stringify({
            fileId,
            ...query,
        }),
    });

    if (!response.ok) {
        const err = await response.json().catch(() => ({ error: 'Export failed' }));
        throw new Error(err.error || 'Export failed');
    }

    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `export_${Date.now()}.csv`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
}

/**
 * Export the chart as a PNG image by capturing the Recharts SVG container.
 */
export function exportPng(chartContainerRef: React.RefObject<HTMLDivElement | null>, title?: string): void {
    const container = chartContainerRef.current;
    if (!container) return;

    const svgElement = container.querySelector('svg.recharts-surface');
    if (!svgElement) {
        // Fallback: try any SVG in the container
        const anySvg = container.querySelector('svg');
        if (!anySvg) return;
        renderSvgToPng(anySvg, title);
        return;
    }

    renderSvgToPng(svgElement as SVGSVGElement, title);
}

function renderSvgToPng(svgElement: SVGSVGElement, title?: string): void {
    const svgRect = svgElement.getBoundingClientRect();
    const canvas = document.createElement('canvas');
    const scale = 2; // Retina
    canvas.width = svgRect.width * scale;
    canvas.height = svgRect.height * scale;

    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    ctx.scale(scale, scale);

    // Clone SVG and inline styles
    const clonedSvg = svgElement.cloneNode(true) as SVGSVGElement;
    clonedSvg.setAttribute('width', String(svgRect.width));
    clonedSvg.setAttribute('height', String(svgRect.height));

    const svgData = new XMLSerializer().serializeToString(clonedSvg);
    const svgBlob = new Blob([svgData], { type: 'image/svg+xml;charset=utf-8' });
    const url = URL.createObjectURL(svgBlob);

    const img = new Image();
    img.onload = () => {
        // White background
        ctx.fillStyle = '#ffffff';
        ctx.fillRect(0, 0, canvas.width, canvas.height);
        ctx.drawImage(img, 0, 0, svgRect.width, svgRect.height);

        canvas.toBlob(blob => {
            if (!blob) return;
            const pngUrl = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = pngUrl;
            a.download = `${title || 'chart'}_${Date.now()}.png`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(pngUrl);
        }, 'image/png');

        URL.revokeObjectURL(url);
    };
    img.src = url;
}
