import { describe, it, expect, beforeEach, vi, type Mock } from 'vitest';
import { render, waitFor, fireEvent } from '@testing-library/react';
import axios from 'axios';
import { MindMapView } from './MindMapView';

vi.mock('axios', () => ({ default: { get: vi.fn() } }));
vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({ theme: 'light', toggleTheme: () => {}, language: 'en', setLanguage: () => {}, t: (k: string) => k }),
}));
vi.mock('../../hooks/useKBGraphStream', () => ({ useKBGraphStream: () => ({ processing: false }) }));

// @xyflow/react pulls in canvas/ResizeObserver APIs jsdom lacks, so stub the
// named imports MindMapView relies on with lightweight equivalents.
// NOTE: setNodes/setEdges must be stable across renders — new vi.fn() each call
// changes the identity, re-triggers loadGraph, and causes a second API fetch.
vi.mock('@xyflow/react', () => {
  const stableSetNodes = vi.fn();
  const stableSetEdges = vi.fn();
  return {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ReactFlow: (props: any) => (
      <div data-testid="react-flow">
        <button data-testid="rf-fire-node-click" onClick={() => props.onNodeClick?.({}, { id: '1' })} />
        {props.children}
      </div>
    ),
    Background: () => null,
    Controls: () => null,
    MiniMap: () => null,
    useNodesState: () => [[], stableSetNodes, vi.fn()],
    useEdgesState: () => [[], stableSetEdges, vi.fn()],
    Position: { Left: 'left', Right: 'right', Top: 'top', Bottom: 'bottom' },
  };
});
vi.mock('@xyflow/react/dist/style.css', () => ({}));
vi.mock('./GraphExportPanel', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  GraphExportPanel: (props: any) => <div data-testid="graph-export-panel" data-scoped={String(props.scoped)} />,
}));
vi.mock('./GraphToolbar', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  GraphToolbar: () => <div data-testid="graph-toolbar" />,
}));
vi.mock('./EntityCard', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  EntityCard: (props: any) => <div data-testid="entity-card" data-entity={props.entityName} />,
}));

const mockedGet = axios.get as unknown as Mock;

describe('MindMapView scoped mode', () => {
  beforeEach(() => vi.clearAllMocks());

  it('fetches the scoped endpoint when messageId is set', async () => {
    mockedGet.mockResolvedValue({ data: { nodes: [{ id: 1, name: 'X', type: 'concept', degree: 0, sources: [] }], edges: [], scoped: true } });
    render(<MindMapView kbId="kb1" messageId="m-1" onAskAbout={vi.fn()} onClose={vi.fn()} onShowWholeKb={vi.fn()} onOpenSource={() => {}} />);
    await waitFor(() => expect(mockedGet).toHaveBeenCalled());
    expect(mockedGet.mock.calls[0][0]).toContain('messageId=m-1');
  });

  it('omits messageId param in whole-KB mode', async () => {
    mockedGet.mockResolvedValue({ data: { nodes: [{ id: 1, name: 'X', type: 'concept', degree: 0 }], edges: [] } });
    render(<MindMapView kbId="kb1" onAskAbout={vi.fn()} onClose={vi.fn()} onOpenSource={() => {}} />);
    await waitFor(() => expect(mockedGet).toHaveBeenCalled());
    expect(mockedGet.mock.calls[0][0]).not.toContain('messageId');
  });
});

describe('MindMapView export panel', () => {
  beforeEach(() => vi.clearAllMocks());

  it('renders the export panel once a graph has loaded', async () => {
    mockedGet.mockResolvedValueOnce({
      data: {
        nodes: [{ id: 1, name: 'Alice', type: 'org', degree: 1 }],
        edges: [],
      },
    });
    const { findByTestId } = render(
      <MindMapView kbId="kb1" onAskAbout={() => {}} onClose={() => {}} onOpenSource={() => {}} />,
    );
    expect(await findByTestId('graph-export-panel')).toBeTruthy();
  });

  it('does not render the export panel while the graph is empty', async () => {
    mockedGet.mockResolvedValueOnce({ data: { nodes: [], edges: [], processing: false } });
    const { queryByTestId } = render(
      <MindMapView kbId="kb1" onAskAbout={() => {}} onClose={() => {}} onOpenSource={() => {}} />,
    );
    await waitFor(() => expect(mockedGet).toHaveBeenCalled());
    expect(queryByTestId('graph-export-panel')).toBeNull();
  });
});

describe('MindMapView interactions', () => {
  beforeEach(() => vi.clearAllMocks());

  it('opens the entity card with the clicked node when a node is clicked', async () => {
    mockedGet.mockResolvedValueOnce({ data: { nodes: [{ id: 1, name: 'Alice', type: 'org', degree: 1 }], edges: [] } });
    const { findByTestId } = render(
      <MindMapView kbId="kb1" onAskAbout={() => {}} onClose={() => {}} onOpenSource={() => {}} />,
    );
    fireEvent.click(await findByTestId('rf-fire-node-click'));
    const card = await findByTestId('entity-card');
    expect(card.getAttribute('data-entity')).toBe('Alice');
  });
});
