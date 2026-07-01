export interface GraphNode {
  id: number;
  name: string;
  type: string;
  degree: number;
}

export interface GraphEdge {
  source: number;
  target: number;
  rel: string;
}

export interface GraphData {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export function toNodeLinkJSON(graph: GraphData): string {
  return JSON.stringify(
    {
      nodes: graph.nodes.map(n => ({ id: n.id, name: n.name, type: n.type, degree: n.degree })),
      links: graph.edges.map(e => ({ source: e.source, target: e.target, rel: e.rel })),
    },
    null,
    2,
  );
}

// RFC-4180: quote a field if it contains comma, double-quote, CR, or newline;
// escape embedded quotes by doubling them.
// Neutralize spreadsheet formula injection (CWE-1236): values starting with
// a formula trigger are prefixed with a single quote.
function csvField(value: string): string {
  const guarded = /^[=+\-@\t\r]/.test(value) ? `'${value}` : value;
  if (/[",\r\n]/.test(guarded)) return `"${guarded.replace(/"/g, '""')}"`;
  return guarded;
}

export function toEdgeListCSV(graph: GraphData): string {
  const nameById = new Map(graph.nodes.map(n => [n.id, n.name]));
  const rows = ['source,source_name,target,target_name,rel'];
  for (const e of graph.edges) {
    rows.push([
      String(e.source),
      csvField(nameById.get(e.source) ?? ''),
      String(e.target),
      csvField(nameById.get(e.target) ?? ''),
      csvField(e.rel),
    ].join(','));
  }
  return rows.join('\n') + '\n';
}

function xmlEscape(value: string): string {
  return value
    // eslint-disable-next-line no-control-regex
    .replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&apos;');
}

export function toGraphML(graph: GraphData): string {
  const lines: string[] = [];
  lines.push('<?xml version="1.0" encoding="UTF-8"?>');
  lines.push('<graphml xmlns="http://graphml.graphdrawing.org/xmlns">');
  lines.push('  <key id="name" for="node" attr.name="name" attr.type="string"/>');
  lines.push('  <key id="type" for="node" attr.name="type" attr.type="string"/>');
  lines.push('  <key id="degree" for="node" attr.name="degree" attr.type="int"/>');
  lines.push('  <key id="rel" for="edge" attr.name="rel" attr.type="string"/>');
  lines.push('  <graph edgedefault="directed">');
  for (const n of graph.nodes) {
    lines.push(`    <node id="n${n.id}">`);
    lines.push(`      <data key="name">${xmlEscape(n.name)}</data>`);
    lines.push(`      <data key="type">${xmlEscape(n.type)}</data>`);
    lines.push(`      <data key="degree">${n.degree}</data>`);
    lines.push('    </node>');
  }
  for (const e of graph.edges) {
    lines.push(`    <edge source="n${e.source}" target="n${e.target}">`);
    lines.push(`      <data key="rel">${xmlEscape(e.rel)}</data>`);
    lines.push('    </edge>');
  }
  lines.push('  </graph>');
  lines.push('</graphml>');
  return lines.join('\n') + '\n';
}

export function downloadText(filename: string, mime: string, text: string): void {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
