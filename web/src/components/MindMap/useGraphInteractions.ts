import { useMemo, useState, useCallback } from 'react';
import type { Node, Edge } from '@xyflow/react';

interface RawNode { id: number; name: string; type: string; degree: number; }
interface RawEdge { source: number; target: number; rel: string; }
interface RawGraph { nodes: RawNode[]; edges: RawEdge[]; }

const DIM = 0.15;

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

    const decorate = useCallback((nodes: Node[], edges: Edge[]) => {
        const neighbors = hoveredId ? (adjacency.get(hoveredId) ?? new Set<string>()) : null;
        const lit = (id: string) => !hoveredId || id === hoveredId || (neighbors?.has(id) ?? false);
        const visible = (id: string) => {
            const ty = typeById.get(id);
            return ty === undefined || activeTypes.has(ty);
        };
        const outNodes = nodes.map(n => ({
            ...n,
            hidden: !visible(n.id),
            style: { ...n.style, opacity: lit(n.id) ? 1 : DIM },
        }));
        const outEdges = edges.map(e => {
            const hiddenEdge = !visible(e.source) || !visible(e.target);
            const litEdge = !hoveredId || (lit(e.source) && lit(e.target));
            return { ...e, hidden: hiddenEdge, style: { ...e.style, opacity: litEdge ? 1 : DIM } };
        });
        return { nodes: outNodes, edges: outEdges };
    }, [hoveredId, adjacency, typeById, activeTypes]);

    return { hoveredId, setHovered, selectedId, setSelected, allTypes, activeTypes, toggleType, decorate };
}
