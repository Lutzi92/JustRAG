import { useMemo, useState, useCallback } from 'react';
import type { Node, Edge } from '@xyflow/react';

interface RawNode { id: number; name: string; type: string; degree: number; }
interface RawEdge { source: number; target: number; rel: string; }
interface RawGraph { nodes: RawNode[]; edges: RawEdge[]; }

const DIM = 0.15;

// filterByType removes nodes whose type is not active, plus any edge incident to
// a removed node. Removing (rather than flagging `hidden`) keeps React Flow from
// processing filtered-out elements at all.
export function filterByType(
    nodes: Node[], edges: Edge[], activeTypes: Set<string>, typeById: Map<string, string>,
): { nodes: Node[]; edges: Edge[] } {
    const visible = (id: string) => {
        const ty = typeById.get(id);
        return ty === undefined || activeTypes.has(ty);
    };
    return {
        nodes: nodes.filter(n => visible(n.id)),
        edges: edges.filter(e => visible(e.source) && visible(e.target)),
    };
}

// litNodeSet returns the node ids that stay bright on hover (the hovered node +
// its direct neighbors), or null when nothing is hovered (everything bright).
export function litNodeSet(hoveredId: string | null, adjacency: Map<string, Set<string>>): Set<string> | null {
    if (!hoveredId) return null;
    const lit = new Set<string>([hoveredId]);
    for (const n of adjacency.get(hoveredId) ?? []) lit.add(n);
    return lit;
}

// buildDimCss produces a stylesheet that dims every node/edge except the lit
// ones, targeting React Flow's `data-id` DOM attributes. Applying the hover
// highlight via CSS (instead of rebuilding the nodes/edges arrays) means
// hovering never re-renders node components — the browser just recomposites
// opacity. Returns '' when nothing is hovered. Scoped under `.mm-graph` so it
// only affects this graph instance. Edges are re-lit only when both endpoints
// are lit.
export function buildDimCss(lit: Set<string> | null, edges: Edge[]): string {
    if (!lit) return '';
    const nodeSel = [...lit]
        .map(id => `.mm-graph .react-flow__node[data-id="${id}"]`)
        .join(',');
    const edgeSel = edges
        .filter(e => lit.has(e.source) && lit.has(e.target))
        .map(e => `.mm-graph .react-flow__edge[data-id="${e.id}"]`)
        .join(',');
    return [
        `.mm-graph .react-flow__node,.mm-graph .react-flow__edge{opacity:${DIM};transition:opacity .12s ease}`,
        nodeSel ? `${nodeSel}{opacity:1}` : '',
        edgeSel ? `${edgeSel}{opacity:1}` : '',
    ].filter(Boolean).join('\n');
}

export function useGraphInteractions(graph: RawGraph) {
    const [hoveredId, setHovered] = useState<string | null>(null);
    const [selectedId, setSelected] = useState<string | null>(null);

    const allTypes = useMemo(
        () => [...new Set(graph.nodes.map(n => n.type))].sort(),
        [graph],
    );
    const [activeTypes, setActiveTypes] = useState<Set<string>>(() => new Set(allTypes));
    // Initialise/refresh active set to "all types" whenever the type list changes.
    const activeKey = allTypes.join('|');
    const [prevKey, setPrevKey] = useState(activeKey);
    if (prevKey !== activeKey) {
        setPrevKey(activeKey);
        setActiveTypes(new Set(allTypes));
    }

    const toggleType = useCallback((t: string) => {
        setActiveTypes(prev => {
            const next = new Set(prev);
            if (next.has(t)) next.delete(t); else next.add(t);
            return next;
        });
    }, []);

    const typeById = useMemo(() => {
        const m = new Map<string, string>();
        for (const n of graph.nodes) m.set(String(n.id), n.type);
        return m;
    }, [graph]);

    const adjacency = useMemo(() => {
        const m = new Map<string, Set<string>>();
        const add = (a: string, b: string) => {
            if (!m.has(a)) m.set(a, new Set());
            m.get(a)!.add(b);
        };
        for (const e of graph.edges) {
            const s = String(e.source), t = String(e.target);
            add(s, t); add(t, s);
        }
        return m;
    }, [graph]);

    // Type filtering is independent of hover, so its identity only changes when
    // the active-type set (or the graph) changes — not on every mouse move.
    const applyTypeFilter = useCallback(
        (nodes: Node[], edges: Edge[]) => filterByType(nodes, edges, activeTypes, typeById),
        [activeTypes, typeById],
    );

    // The lit set changes on hover, but it is a small Set — the nodes/edges
    // arrays passed to React Flow stay referentially stable across hovers.
    const litNodes = useMemo(() => litNodeSet(hoveredId, adjacency), [hoveredId, adjacency]);

    return { hoveredId, setHovered, selectedId, setSelected, allTypes, activeTypes, toggleType, applyTypeFilter, litNodes };
}
