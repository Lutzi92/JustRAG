import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { openExternal } from './openExternal';

describe('openExternal', () => {
    let openSpy: ReturnType<typeof vi.spyOn>;

    beforeEach(() => {
        openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    });

    afterEach(() => {
        openSpy.mockRestore();
    });

    it('opens http(s) URLs with noopener,noreferrer', () => {
        expect(openExternal('https://example.com/page')).toBe(true);
        expect(openSpy).toHaveBeenCalledWith('https://example.com/page', '_blank', 'noopener,noreferrer');

        expect(openExternal('http://example.com')).toBe(true);
    });

    it('rejects javascript: URLs', () => {
        expect(openExternal('javascript:alert(1)')).toBe(false);
        expect(openSpy).not.toHaveBeenCalled();
    });

    it('rejects data: URLs', () => {
        expect(openExternal('data:text/html,<script>alert(1)</script>')).toBe(false);
        expect(openSpy).not.toHaveBeenCalled();
    });

    it('rejects malformed input', () => {
        expect(openExternal('not a url')).toBe(false);
        expect(openSpy).not.toHaveBeenCalled();
    });
});
