import { useState, useRef, useEffect, useCallback } from 'react';
import { Panel } from '@xyflow/react';
import { Download } from 'lucide-react';
import { toNodeLinkJSON, toEdgeListCSV, toGraphML, downloadText, type GraphData } from './graphExport';
import { useToast } from '../../contexts/ToastContext';

// PNG export is temporarily deactivated: the html-to-image capture produced an
// illegible image (mis-scaled bounds). The exportPng.ts helper is retained for
// when the rendering is fixed. Re-add 'png' here and the menu item below to
// restore it.
type Fmt = 'graphml' | 'json' | 'csv';

interface Props {
    graph: GraphData;
    scoped: boolean;
    t: (k: string) => string;
}

export const GraphExportPanel: React.FC<Props> = ({ graph, scoped, t }) => {
    const [open, setOpen] = useState(false);
    const ref = useRef<HTMLDivElement>(null);
    const toast = useToast();

    useEffect(() => {
        if (!open) return;
        const onDoc = (e: MouseEvent) => {
            if (ref.current && !ref.current.contains(e.target as globalThis.Node)) setOpen(false);
        };
        const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
        document.addEventListener('mousedown', onDoc);
        document.addEventListener('keydown', onKey);
        return () => {
            document.removeEventListener('mousedown', onDoc);
            document.removeEventListener('keydown', onKey);
        };
    }, [open]);

    const base = scoped ? 'knowledge-graph-scoped' : 'knowledge-graph';

    const onSelect = useCallback((fmt: Fmt) => {
        setOpen(false);
        try {
            if (fmt === 'json') downloadText(`${base}.json`, 'application/json', toNodeLinkJSON(graph));
            else if (fmt === 'csv') downloadText(`${base}.csv`, 'text/csv', toEdgeListCSV(graph));
            else if (fmt === 'graphml') downloadText(`${base}.graphml`, 'application/xml', toGraphML(graph));
        } catch {
            toast.error(t('exportFailed'));
        }
    }, [graph, base, toast, t]);

    return (
        <Panel position="top-left">
            <div className="graph-export-menu" ref={ref}>
                <button
                    type="button"
                    className="graph-export-btn"
                    onClick={() => setOpen(o => !o)}
                    aria-haspopup="menu"
                    aria-expanded={open}
                >
                    <Download size={14} aria-hidden="true" /> {t('graphExport')} ▾
                </button>
                {open && (
                    <ul role="menu" className="graph-export-list">
                        <li><button type="button" role="menuitem" onClick={() => onSelect('graphml')}>{t('graphFmtGraphml')}</button></li>
                        <li><button type="button" role="menuitem" onClick={() => onSelect('json')}>{t('graphFmtJson')}</button></li>
                        <li><button type="button" role="menuitem" onClick={() => onSelect('csv')}>{t('graphFmtCsv')}</button></li>
                    </ul>
                )}
            </div>
        </Panel>
    );
};
