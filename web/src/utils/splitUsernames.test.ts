import { describe, it, expect } from 'vitest';
import { splitUsernames } from './splitUsernames';

describe('splitUsernames', () => {
  it('splits on commas, spaces, and newlines', () => {
    expect(splitUsernames('alice, bob\ncarol  dave')).toEqual(['alice', 'bob', 'carol', 'dave']);
  });

  it('trims and drops empties', () => {
    expect(splitUsernames('  alice ,, \n  bob ')).toEqual(['alice', 'bob']);
  });

  it('dedupes case-insensitively, keeping first-seen', () => {
    expect(splitUsernames('Alice alice ALICE bob')).toEqual(['Alice', 'bob']);
  });

  it('returns [] for blank input', () => {
    expect(splitUsernames('   \n  ')).toEqual([]);
  });
});
