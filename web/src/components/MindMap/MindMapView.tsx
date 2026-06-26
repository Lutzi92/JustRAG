import { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import {
    ReactFlow, Background, Controls, MiniMap,
    useNodesState, useEdgesState, type Node, type Edge,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import dagre from 'dagre';
import { Loader2, Network, X } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useKBGraphStream } from '../../hooks/useKBGraphStream';
import { NodeSourcesPanel } from './NodeSourcesPanel';

interface MindMapViewProps {
    kbId: string;
    // onAskAbout prefills the chat input with a question about the clicked
    // entity and switches back to the chat view.
    onAskAbout: (entityName: string) => void;
    onClose: () => void;
    // Query-scoped mindmap (wired in a later task). When set, the view should
    // load the per-answer subgraph for this message id instead of the whole-KB
    // graph; null = whole-KB graph. Declared now so callers type-check.
    messageId?: string | null;
    // Clears the scoped message id but stays in the mindmap view (reloads the
    // whole-KB graph). Behavior implemented in a later task.
    onShowWholeKb?: () => void;
}

interface NodeSource { fileId: string; fileName: string; chunkId: string; }
interface GraphNode { id: number; name: string; type: string; degree: number; sources?: NodeSource[]; }
interface GraphEdge { source: number; target: number; rel: string; }
interface GraphData { nodes: GraphNode[]; edges: GraphEdge[]; processing?: boolean; scoped?: boolean; }

const NODE_W = 170;
const NODE_H = 44;

// Stable per-type colour so the same entity type is consistently coloured.
const PALETTE = ['#165a97', '#2d8f4e', '#cc8400', '#c0392b', '#6a1b9a', '#0d7d7d', '#b03a6e'];
function colorForType(type: string): string {
    let h = 0;
    for (let i = 0; i < type.length; i++) h = (h * 31 + type.charCodeAt(i)) >>> 0;
    return PALETTE[h % PALETTE.length];
}

// layoutGraph runs dagre over the KB graph and produces react-flow nodes/edges
// with computed positions. Node ids are stringified entity ids.
function layoutGraph(data: GraphData): { nodes: Node[]; edges: Edge[] } {
    const g = new dagre.graphlib.Graph();
    g.setDefaultEdgeLabel(() => ({}));
    g.setGraph({ rankdir: 'LR', nodesep: 30, ranksep: 90 });

    const present = new Set(data.nodes.map(n => String(n.id)));
    data.nodes.forEach(n => g.setNode(String(n.id), { width: NODE_W, height: NODE_H }));
    data.edges.forEach(e => {
        const s = String(e.source), t = String(e.target);
        if (present.has(s) && present.has(t)) g.setEdge(s, t);
    });
    dagre.layout(g);

    const nodes: Node[] = data.nodes.map(n => {
        const p = g.node(String(n.id));
        return {
            id: String(n.id),
            position: { x: (p?.x ?? 0) - NODE_W / 2, y: (p?.y ?? 0) - NODE_H / 2 },
            data: { label: n.name },
            style: {
                background: colorForType(n.type),
                color: '#fff',
                border: 'none',
                borderRadius: 8,
                fontSize: 12,
                width: NODE_W,
                padding: '6px 8px',
            },
        };
    });

    const edges: Edge[] = data.edges.map((e, i) => ({
        id: `e-${e.source}-${e.target}-${i}`,
        source: String(e.source),
        target: String(e.target),
        label: e.rel,
        style: { stroke: 'var(--border-color)' },
        labelStyle: { fontSize: 10, fill: 'var(--text-secondary)' },
    }));

    return { nodes, edges };
}

type Status = 'loading' | 'ready' | 'error' | 'empty';

export const MindMapView: React.FC<MindMapViewProps> = ({ kbId, onAskAbout, onClose, messageId, onShowWholeKb }) => {
    const { t } = useTheme();
    const [status, setStatus] = useState<Status>('loading');
    const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
    const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
    // node id -> entity name, for the click-to-ask handler.
    const [names, setNames] = useState<Record<string, string>>({});
    // node id -> source chunks, for the (scoped) provenance UI.
    const [sources, setSources] = useState<Record<string, NodeSource[]>>({});
    // currently selected node id (scoped mode opens a sources side panel).
    const [selectedNode, setSelectedNode] = useState<string | null>(null);

    const loadGraph = useCallback(() => {
        let cancelled = false;
        const url = messageId
            ? `${API_BASE_URL}/api/kb/${kbId}/graph?messageId=${encodeURIComponent(messageId)}`
            : `${API_BASE_URL}/api/kb/${kbId}/graph`;
        axios.get(url)
            .then(res => {
                if (cancelled) return;
                const data = res.data as GraphData;
                if (!data?.nodes || data.nodes.length === 0) {
                    // Keep showing the spinner while extraction is still in
                    // flight; only declare "empty" once the KB is idle.
                    setStatus(data?.processing ? 'loading' : 'empty');
                    return;
                }
                const laid = layoutGraph(data);
                setNodes(laid.nodes);
                setEdges(laid.edges);
                setNames(Object.fromEntries(data.nodes.map(n => [String(n.id), n.name])));
                setSources(Object.fromEntries(data.nodes.map(n => [String(n.id), n.sources ?? []])));
                setStatus('ready');
            })
            .catch(() => { if (!cancelled) setStatus('error'); });
        return () => { cancelled = true; };
    }, [kbId, messageId, setNodes, setEdges]);

    useEffect(() => {
        const cancel = loadGraph();
        return cancel;
    }, [loadGraph]);

    // Live updates: re-fetch on graph_changed; `processing` drives the spinner.
    const { processing } = useKBGraphStream(kbId, loadGraph);

    const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
        if (messageId) { setSelectedNode(node.id); return; }
        const name = names[node.id] ?? (node.data?.label as string | undefined);
        if (name) onAskAbout(name);
    }, [messageId, names, onAskAbout]);

    return (
        <div style={{ height: '100%', display: 'flex', flexDirection: 'column', background: 'var(--bg-primary)' }}>
            <header style={{
                padding: '1rem 2rem', borderBottom: '1px solid var(--border-color)',
                display: 'flex', justifyContent: 'space-between', alignItems: 'center', background: 'var(--bg-primary)',
            }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                    <Network size={20} style={{ color: 'var(--accent-primary)' }} aria-hidden="true" />
                    <div>
                        <h3 style={{ margin: 0 }}>{t('mindMap')}</h3>
                        <p style={{ margin: 0, fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('mindMapNodeHint')}</p>
                    </div>
                    {messageId && (
                        <>
                            <span style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('scopedToThisAnswer')}</span>
                            <button type="button" className="message-action-btn" onClick={() => onShowWholeKb?.()}>
                                {t('showWholeKb')}
                            </button>
                        </>
                    )}
                </div>
                <button onClick={onClose} style={{
                    background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)',
                    display: 'flex', alignItems: 'center', gap: '8px',
                }} aria-label={t('close')}>
                    <X size={18} />
                    <span style={{ fontSize: '0.9rem' }}>{t('close')}</span>
                </button>
            </header>

            <div style={{ flex: 1, position: 'relative', overflow: 'hidden' }}>
                {processing && (
                    <div style={{
                        position: 'absolute', top: 12, right: 12, zIndex: 10,
                        display: 'flex', alignItems: 'center', gap: 8,
                        background: 'var(--bg-secondary)', border: '1px solid var(--border-color)',
                        borderRadius: 8, padding: '6px 10px', fontSize: '0.8rem',
                        color: 'var(--text-secondary)', boxShadow: '0 1px 4px rgba(0,0,0,0.15)',
                    }}>
                        <Loader2 className="animate-spin" size={16} style={{ color: 'var(--accent-primary)' }} />
                        <span>{t('mindMapBuilding')}</span>
                    </div>
                )}
                {status === 'ready' ? (
                    <ReactFlow
                        nodes={nodes}
                        edges={edges}
                        onNodesChange={onNodesChange}
                        onEdgesChange={onEdgesChange}
                        onNodeClick={onNodeClick}
                        fitView
                        minZoom={0.1}
                    >
                        <Background />
                        <Controls />
                        <MiniMap pannable zoomable />
                    </ReactFlow>
                ) : (
                    <div style={{
                        position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column',
                        alignItems: 'center', justifyContent: 'center', gap: '1rem',
                        color: 'var(--text-secondary)', textAlign: 'center', padding: '2rem',
                    }}>
                        {status === 'loading' && <Loader2 className="animate-spin" size={32} style={{ color: 'var(--accent-primary)' }} />}
                        {status === 'loading' && <p>{t('mindMapLoading')}</p>}
                        {status === 'error' && <p>{t('mindMapError')}</p>}
                        {status === 'empty' && (<><Network size={48} style={{ opacity: 0.2 }} /><p style={{ maxWidth: 460 }}>{t('mindMapEmpty')}</p>{messageId && (
                            <button type="button" className="message-action-btn" onClick={() => onShowWholeKb?.()}>
                                {t('showWholeKb')}
                            </button>
                        )}</>)}
                    </div>
                )}
                {selectedNode && (
                    <NodeSourcesPanel
                        entityName={names[selectedNode] ?? ''}
                        sources={sources[selectedNode] ?? []}
                        onAsk={() => { const n = names[selectedNode]; if (n) onAskAbout(n); setSelectedNode(null); }}
                        onClose={() => setSelectedNode(null)}
                    />
                )}
            </div>
        </div>
    );
};
