import { toPng } from 'html-to-image';
import { getNodesBounds, getViewportForBounds, type Node } from '@xyflow/react';

// Fixed target canvas; getViewportForBounds fits ALL nodes into this box so the
// export captures the whole graph, not just the on-screen viewport.
const IMAGE_WIDTH = 1600;
const IMAGE_HEIGHT = 1200;

export async function exportGraphPng(nodes: Node[], filename: string, backgroundColor: string): Promise<void> {
    if (nodes.length === 0) return;
    const viewportEl = document.querySelector('.react-flow__viewport') as HTMLElement | null;
    if (!viewportEl) return;

    const bounds = getNodesBounds(nodes);
    const { x, y, zoom } = getViewportForBounds(bounds, IMAGE_WIDTH, IMAGE_HEIGHT, 0.2, 2, 0.1);

    const dataUrl = await toPng(viewportEl, {
        backgroundColor,
        width: IMAGE_WIDTH,
        height: IMAGE_HEIGHT,
        pixelRatio: 2,
        // Skip web-font embedding: it fetches/inlines cross-origin font
        // stylesheets, which a strict CSP (connect-src 'self') blocks. Graph
        // labels use the app's own font stack, so the output is unaffected.
        skipFonts: true,
        style: {
            width: `${IMAGE_WIDTH}px`,
            height: `${IMAGE_HEIGHT}px`,
            transform: `translate(${x}px, ${y}px) scale(${zoom})`,
        },
    });

    const a = document.createElement('a');
    a.href = dataUrl;
    a.download = filename;
    a.click();
}
