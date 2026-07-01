import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';

vi.mock('@xyflow/react', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Panel: (props: any) => <div data-testid="panel">{props.children}</div>,
}));
vi.mock('../../contexts/ToastContext', () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn() }) }));
const downloadText = vi.fn();
vi.mock('./graphExport', () => ({
  toNodeLinkJSON: () => '{"json":true}',
  toEdgeListCSV: () => 'csv',
  toGraphML: () => '<graphml/>',
  downloadText: (...args: unknown[]) => downloadText(...args),
}));

import { GraphExportPanel } from './GraphExportPanel';

const graph = { nodes: [], edges: [] };
const t = (k: string) => k;

describe('GraphExportPanel', () => {
  beforeEach(() => vi.clearAllMocks());

  it('is collapsed until the Export button is clicked', () => {
    render(<GraphExportPanel graph={graph} scoped={false} t={t} />);
    expect(screen.queryByRole('menuitem', { name: 'graphFmtJson' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /graphExport/ }));
    expect(screen.getByRole('menuitem', { name: 'graphFmtJson' })).toBeTruthy();
  });

  it('downloads JSON with the whole-KB filename', () => {
    render(<GraphExportPanel graph={graph} scoped={false} t={t} />);
    fireEvent.click(screen.getByRole('button', { name: /graphExport/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'graphFmtJson' }));
    expect(downloadText).toHaveBeenCalledWith('knowledge-graph.json', 'application/json', '{"json":true}');
  });

  it('uses the -scoped filename suffix in scoped mode for CSV', () => {
    render(<GraphExportPanel graph={graph} scoped={true} t={t} />);
    fireEvent.click(screen.getByRole('button', { name: /graphExport/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'graphFmtCsv' }));
    expect(downloadText).toHaveBeenCalledWith('knowledge-graph-scoped.csv', 'text/csv', 'csv');
  });

  it('downloads GraphML with the application/xml mime type', () => {
    render(<GraphExportPanel graph={graph} scoped={false} t={t} />);
    fireEvent.click(screen.getByRole('button', { name: /graphExport/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'graphFmtGraphml' }));
    expect(downloadText).toHaveBeenCalledWith('knowledge-graph.graphml', 'application/xml', '<graphml/>');
  });

  it('does not offer PNG export (temporarily deactivated)', () => {
    render(<GraphExportPanel graph={graph} scoped={false} t={t} />);
    fireEvent.click(screen.getByRole('button', { name: /graphExport/ }));
    expect(screen.queryByRole('menuitem', { name: 'graphFmtPng' })).toBeNull();
  });
});
