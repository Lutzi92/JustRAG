import { describe, it, expect, vi, afterEach } from 'vitest';
import { toNodeLinkJSON, toEdgeListCSV, toGraphML, downloadText, type GraphData } from './graphExport';

// Fixture deliberately includes a comma (CSV quoting) and an ampersand + angle
// bracket (XML escaping) to prove the serializers are robust.
const fixture: GraphData = {
  nodes: [
    { id: 1, name: 'Alice, Inc.', type: 'org', degree: 2 },
    { id: 2, name: 'Bob <B>', type: 'person', degree: 1 },
    { id: 3, name: 'R&D', type: 'topic', degree: 1 },
  ],
  edges: [
    { source: 1, target: 2, rel: 'employs' },
    { source: 1, target: 3, rel: 'owns' },
  ],
};

describe('toNodeLinkJSON', () => {
  it('emits node-link shape with all node fields and edge rels', () => {
    const parsed = JSON.parse(toNodeLinkJSON(fixture));
    expect(parsed.nodes).toHaveLength(3);
    expect(parsed.nodes[0]).toEqual({ id: 1, name: 'Alice, Inc.', type: 'org', degree: 2 });
    expect(parsed.links).toHaveLength(2);
    expect(parsed.links[0]).toEqual({ source: 1, target: 2, rel: 'employs' });
  });
});

describe('toEdgeListCSV', () => {
  it('emits header + one row per edge with names, quoting fields with commas', () => {
    const lines = toEdgeListCSV(fixture).trim().split('\n');
    expect(lines[0]).toBe('source,source_name,target,target_name,rel');
    expect(lines).toHaveLength(3);
    // Alice, Inc. contains a comma -> must be double-quoted
    expect(lines[1]).toBe('1,"Alice, Inc.",2,Bob <B>,employs');
    expect(lines[2]).toBe('1,"Alice, Inc.",3,R&D,owns');
  });

  it('neutralizes formula-injection names (CWE-1236) by prefixing with a single quote', () => {
    const injectionGraph: GraphData = {
      nodes: [
        { id: 1, name: '=cmd()', type: 'entity', degree: 1 },
        { id: 2, name: 'Safe', type: 'entity', degree: 1 },
      ],
      edges: [{ source: 1, target: 2, rel: 'links' }],
    };
    const lines = toEdgeListCSV(injectionGraph).trim().split('\n');
    // The formula-injection name must be prefixed with a single quote to prevent
    // Excel/Sheets from interpreting it as a formula.
    // "'=cmd()" has no comma/quote/CR/LF -> no double-quoting wrapping.
    expect(lines[1]).toBe("1,'=cmd(),2,Safe,links");
  });
});

describe('toGraphML', () => {
  it('is well-formed, directed, declares keys, and XML-escapes text', () => {
    const xml = toGraphML(fixture);
    expect(xml).toContain('<?xml version="1.0" encoding="UTF-8"?>');
    expect(xml).toContain('edgedefault="directed"');
    expect(xml).toContain('<key id="name" for="node" attr.name="name" attr.type="string"/>');
    expect(xml).toContain('<key id="rel" for="edge" attr.name="rel" attr.type="string"/>');
    // XML escaping of & and <
    expect(xml).toContain('R&amp;D');
    expect(xml).toContain('Bob &lt;B&gt;');
    // node ids namespaced as n<id>, 3 nodes + 2 edges
    expect((xml.match(/<node /g) || [])).toHaveLength(3);
    expect((xml.match(/<edge /g) || [])).toHaveLength(2);
    expect(xml).toContain('<edge source="n1" target="n2">');
  });
});

describe('downloadText', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('creates a blob url and triggers an anchor download', () => {
    vi.useFakeTimers();
    const createURL = vi.fn(() => 'blob:mock');
    const revokeURL = vi.fn();
    global.URL.createObjectURL = createURL;
    global.URL.revokeObjectURL = revokeURL;
    const click = vi.fn();
    const anchor = document.createElement('a');
    anchor.click = click;
    vi.spyOn(document, 'createElement').mockReturnValueOnce(anchor);

    downloadText('graph.json', 'application/json', '{}');

    expect(createURL).toHaveBeenCalledOnce();
    expect(anchor.download).toBe('graph.json');
    expect(click).toHaveBeenCalledOnce();
    // revokeObjectURL is deferred via setTimeout — flush timers before asserting
    expect(revokeURL).not.toHaveBeenCalled();
    vi.runAllTimers();
    expect(revokeURL).toHaveBeenCalledWith('blob:mock');
  });
});
