import { describe, it, expect } from 'vitest';
import type { KnowledgeBase } from '../types';
import { canOpenKbAdvancedSettings, hasAdvancedSystemRole, hasKbAdminRole } from './kbAccess';

type KbShape = Pick<KnowledgeBase, 'myRole' | 'isGlobal' | 'visibility'>;

const privateKb: KbShape = { visibility: 'private', isGlobal: false };
const publicKb: KbShape = { visibility: 'public', isGlobal: true };

describe('hasAdvancedSystemRole', () => {
  it.each(['api-user', 'admin', 'superadmin'])('accepts %s', (role) => {
    expect(hasAdvancedSystemRole(role)).toBe(true);
  });

  it.each(['user', '', 'Admin', 'apiuser'])('rejects %j', (role) => {
    expect(hasAdvancedSystemRole(role)).toBe(false);
  });

  it('rejects an absent role', () => {
    expect(hasAdvancedSystemRole(undefined)).toBe(false);
  });
});

describe('hasKbAdminRole', () => {
  it('grants a superadmin without any kb_members row (ladder rule 1)', () => {
    expect(hasKbAdminRole({ ...privateKb }, 'superadmin')).toBe(true);
  });

  it.each(['admin', 'owner'] as const)('grants an explicit %s row (rule 2)', (role) => {
    expect(hasKbAdminRole({ ...privateKb, myRole: role }, 'user')).toBe(true);
  });

  it.each(['edit', 'view'] as const)('refuses an explicit %s row (rule 2)', (role) => {
    expect(hasKbAdminRole({ ...privateKb, myRole: role }, 'admin')).toBe(false);
  });

  it('grants a system admin on a PUBLIC KB with no row (rule 3)', () => {
    expect(hasKbAdminRole({ ...publicKb }, 'admin')).toBe(true);
  });

  it('refuses a system admin on a PRIVATE KB with no row — rule 3 needs a public KB', () => {
    expect(hasKbAdminRole({ ...privateKb }, 'admin')).toBe(false);
  });

  it('lets an explicit edit row BEAT the public-KB admin rule, as rule 2 does server-side', () => {
    expect(hasKbAdminRole({ ...publicKb, myRole: 'edit' }, 'admin')).toBe(false);
  });

  it('refuses a plain user on a published public KB (rule 4 yields view, not admin)', () => {
    expect(hasKbAdminRole({ ...publicKb }, 'user')).toBe(false);
  });
});

// The predicate behind both entry points to KbSettingsPanel. Its whole job is
// that BOTH terms are required — the pairs below are the cases where exactly
// one holds, which is what the two hand-written gates used to get wrong in
// opposite directions.
describe('canOpenKbAdvancedSettings', () => {
  it('refuses a plain user who OWNS the KB — the restored system-role gate', () => {
    expect(canOpenKbAdvancedSettings({ ...privateKb, myRole: 'owner' }, 'user')).toBe(false);
  });

  it('refuses an api-user with no KB role — the KB-role term is still load-bearing', () => {
    expect(canOpenKbAdvancedSettings({ ...privateKb }, 'api-user')).toBe(false);
  });

  it.each(['api-user', 'admin', 'superadmin'])('allows %s who owns the KB', (role) => {
    expect(canOpenKbAdvancedSettings({ ...privateKb, myRole: 'owner' }, role)).toBe(true);
  });

  it('allows a system admin curating a public KB they never joined', () => {
    expect(canOpenKbAdvancedSettings({ ...publicKb }, 'admin')).toBe(true);
  });

  it('refuses when there is no KB at all', () => {
    expect(canOpenKbAdvancedSettings(null, 'superadmin')).toBe(false);
    expect(canOpenKbAdvancedSettings(undefined, 'superadmin')).toBe(false);
  });
});
