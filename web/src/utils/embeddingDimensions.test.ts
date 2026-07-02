import { describe, it, expect } from 'vitest';
import { parseDimensionsInput, formatDimensionsValue } from './embeddingDimensions';

// ai_models.dimensions semantics: 0 = "auto" (model-native size, backend
// omits the request parameter), > 0 = requested MRL-truncated size. The old
// UI coerced empty/0 to 1536, which would now silently truncate embeddings.

describe('parseDimensionsInput', () => {
    it('parses a positive number', () => {
        expect(parseDimensionsInput('2048')).toBe(2048);
    });

    it('treats empty input as 0 (auto), never coercing to 1536', () => {
        expect(parseDimensionsInput('')).toBe(0);
    });

    it('treats 0 as 0 (auto)', () => {
        expect(parseDimensionsInput('0')).toBe(0);
    });

    it('treats garbage as 0 (auto)', () => {
        expect(parseDimensionsInput('abc')).toBe(0);
    });

    it('clamps negatives to 0', () => {
        expect(parseDimensionsInput('-5')).toBe(0);
    });
});

describe('formatDimensionsValue', () => {
    it('renders a positive value as-is', () => {
        expect(formatDimensionsValue(2048)).toBe('2048');
    });

    it('renders 0 as empty string so the "auto" placeholder shows', () => {
        expect(formatDimensionsValue(0)).toBe('');
    });

    it('renders undefined as empty string', () => {
        expect(formatDimensionsValue(undefined)).toBe('');
    });
});
