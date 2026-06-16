import { useCallback } from 'react';
import { exportRowsToXlsx } from '../utils/exportXlsx';

// Minimal hast (HTML AST) node shape — react-markdown passes the parsed node
// for each rendered element via the `node` prop.
interface HastNode {
    type?: string;
    tagName?: string;
    value?: string;
    properties?: { className?: string[] | string };
    children?: HastNode[];
}

interface Props {
    node?: HastNode;
    children?: React.ReactNode;
    label: string;
    filename: string;
}

function hasClass(node: HastNode, name: string): boolean {
    const cls = node.properties?.className;
    if (Array.isArray(cls)) return cls.includes(name);
    if (typeof cls === 'string') return cls.split(/\s+/).includes(name);
    return false;
}

// Residual inline citation markers the LLM may emit as literal text inside a
// cell ("[1]", "[1, 2]", "[S. 4]") rather than the rendered source-ref sup.
const CITATION_TEXT_RE = /\[\s*(?:S\.\s*)?\d+(?:\s*[,–-]\s*\d+)*\s*\]/g;

// textOf flattens a node's text, skipping rendered citation markers
// (sup.source-ref injected by addCitationRefs) so they never reach the export.
function textOf(node?: HastNode): string {
    if (!node) return '';
    if (node.type === 'text') return node.value ?? '';
    if (node.tagName === 'sup' && hasClass(node, 'source-ref')) return '';
    return (node.children ?? []).map(textOf).join('');
}

function cleanCell(node: HastNode): string {
    return textOf(node).replace(CITATION_TEXT_RE, '').replace(/\s+/g, ' ').trim();
}

// extractRows walks the table AST and returns a row-major array of cleaned cell
// strings (header row included). Whitespace-only text nodes between elements are
// skipped because they are not `tr`/`td`/`th` nodes.
function extractRows(table?: HastNode): string[][] {
    const rows: string[][] = [];
    const walk = (n?: HastNode) => {
        if (!n) return;
        if (n.tagName === 'tr') {
            const cells = (n.children ?? []).filter((c) => c.tagName === 'th' || c.tagName === 'td');
            rows.push(cells.map(cleanCell));
            return;
        }
        (n.children ?? []).forEach(walk);
    };
    walk(table);
    return rows;
}

// MarkdownTable renders a GFM table and, when it has extractable rows, a
// "Download .xlsx" button. It is wired as the ReactMarkdown `table` component
// override so EVERY table in an AI answer becomes exportable — not just the
// corpus-comparison StructuredTable (which has its own dedicated view).
export function MarkdownTable({ node, children, label, filename }: Props) {
    const exportXlsx = useCallback(() => {
        void exportRowsToXlsx(extractRows(node), { filename });
    }, [node, filename]);

    const hasRows = extractRows(node).length > 0;

    return (
        <div className="md-table">
            {hasRows && (
                <div className="md-table__toolbar">
                    <button type="button" aria-label={label} onClick={exportXlsx}>{label}</button>
                </div>
            )}
            <table>{children}</table>
        </div>
    );
}
