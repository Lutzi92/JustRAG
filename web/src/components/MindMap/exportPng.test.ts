import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('html-to-image', () => ({ toPng: vi.fn(async () => 'data:image/png;base64,AAAA') }));
vi.mock('@xyflow/react', () => ({
  getNodesBounds: vi.fn(() => ({ x: 0, y: 0, width: 100, height: 80 })),
  getViewportForBounds: vi.fn(() => ({ x: 5, y: 7, zoom: 1.5 })),
}));

import { toPng } from 'html-to-image';
import { exportGraphPng } from './exportPng';

describe('exportGraphPng', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.innerHTML = '<div class="react-flow__viewport"></div>';
  });

  it('renders the full-graph viewport to PNG and downloads it', async () => {
    const click = vi.fn();
    const anchor = document.createElement('a');
    anchor.click = click;
    vi.spyOn(document, 'createElement').mockReturnValueOnce(anchor);

    await exportGraphPng([{ id: '1', position: { x: 0, y: 0 }, data: {} }] as never, 'graph.png', '#fff');

    expect(toPng).toHaveBeenCalledOnce();
    const [el, opts] = (toPng as unknown as ReturnType<typeof vi.fn>).mock.calls[0];
    expect((el as HTMLElement).className).toBe('react-flow__viewport');
    expect(opts.backgroundColor).toBe('#fff');
    expect(opts.style.transform).toBe('translate(5px, 7px) scale(1.5)');
    expect(opts.pixelRatio).toBe(2);
    expect(opts.width).toBe(1600);
    expect(opts.height).toBe(1200);
    expect(opts.skipFonts).toBe(true);
    expect(anchor.download).toBe('graph.png');
    expect(click).toHaveBeenCalledOnce();
    vi.restoreAllMocks();
  });

  it('no-ops when there are no nodes', async () => {
    document.body.innerHTML = '<div class="react-flow__viewport"></div>';
    await exportGraphPng([], 'graph.png', '#fff');
    expect(toPng).not.toHaveBeenCalled();
  });
});
