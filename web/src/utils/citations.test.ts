import { describe, it, expect } from 'vitest';
import { extractCitedSourceIndices } from './citations';

describe('extractCitedSourceIndices', () => {
  it('extracts a single citation marker', () => {
    const result = extractCitedSourceIndices('Antwort wie in [3] beschrieben.', 10);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([3]);
  });

  it('extracts multi-cite markers', () => {
    const result = extractCitedSourceIndices('Siehe [1, 2, 5] für Details.', 10);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([1, 2, 5]);
  });

  it('deduplicates repeated markers', () => {
    const result = extractCitedSourceIndices('Erst [3], dann nochmal [3].', 10);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([3]);
  });

  it('returns empty set when no markers exist', () => {
    const result = extractCitedSourceIndices('Reiner Text ohne Zitate.', 10);
    expect(result.size).toBe(0);
  });

  it('drops out-of-range indices', () => {
    const result = extractCitedSourceIndices('Hier [99] und [3].', 5);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([3]);
  });

  it('drops zero and negative indices defensively', () => {
    // [-1] never matches the \d+-only regex; [0] is dropped by the n >= 1 guard
    const result = extractCitedSourceIndices('Hier [0], [-1] und [2].', 5);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([2]);
  });

  it('tolerates whitespace inside multi-cite', () => {
    const result = extractCitedSourceIndices('Siehe [ 1 ,  2 ].', 10);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([1, 2]);
  });

  it('tolerates whitespace around a single index', () => {
    const result = extractCitedSourceIndices('Siehe [ 3 ].', 10);
    expect(Array.from(result)).toEqual([3]);
  });

  it('ignores non-numeric content', () => {
    const result = extractCitedSourceIndices('Kein Treffer: [abc].', 10);
    expect(result.size).toBe(0);
  });

  it('handles mixed valid and out-of-range in one query', () => {
    const result = extractCitedSourceIndices('A [2], B [99], C [4].', 5);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([2, 4]);
  });

  it('returns empty set when sourceCount is 0', () => {
    const result = extractCitedSourceIndices('Antwort mit [1] aber ohne Quellen.', 0);
    expect(result.size).toBe(0);
  });

  it('returns empty set when sourceCount is negative', () => {
    const result = extractCitedSourceIndices('Antwort mit [1].', -1);
    expect(result.size).toBe(0);
  });

  it('tolerates markdown-escaped brackets emitted by some LLMs', () => {
    const result = extractCitedSourceIndices('Wie in \\[1\\] und \\[2, 3\\] beschrieben.', 10);
    expect(Array.from(result).sort((a, b) => a - b)).toEqual([1, 2, 3]);
  });
});
