import type { KnowledgeBase } from '../types';

/**
 * The system roles that may operate the KB "advanced settings" surface
 * (KbSettingsPanel: RAG Settings, Agenten & Teams, Evals, Workflow — plus the
 * per-KB re-ingest its Save flow offers).
 *
 * Same triple the backend's kbAdvancedChain passes to RequireRole, and the same
 * one Profile.tsx uses to decide whether to show the API-keys section.
 */
export const KB_ADVANCED_SYSTEM_ROLES: readonly string[] = ['api-user', 'admin', 'superadmin'];

/** Whether `systemRole` is one of KB_ADVANCED_SYSTEM_ROLES. */
export function hasAdvancedSystemRole(systemRole?: string): boolean {
  return systemRole != null && KB_ADVANCED_SYSTEM_ROLES.includes(systemRole);
}

/**
 * Whether the caller's EFFECTIVE role on `kb` is admin or better.
 *
 * Mirrors kbaccess.EffectiveRole (go-backend/internal/kbaccess/middleware.go),
 * truncated at the admin threshold — rules 4 and 5 only ever yield view or
 * nothing, so they are both "false" here:
 *
 *   1. system role superadmin        -> owner
 *   2. a kb_members row              -> that role
 *   3. public KB + system role admin -> admin
 *
 * Rule 2 must stay ahead of rule 3, exactly as on the server: an explicit
 * membership row beats the implicit role a public KB grants, or an editor on a
 * public KB would be read as an admin.
 *
 * Rule 3 is not redundant. `kb.myRole` is the RAW kb_members row (see
 * kbMembershipCols in internal/kb/store_pg.go), not the resolved role, so a
 * system admin curating a public KB they never joined arrives here with no
 * myRole at all while the server grants them admin.
 */
export function hasKbAdminRole(kb: Pick<KnowledgeBase, 'myRole' | 'isGlobal' | 'visibility'>, systemRole?: string): boolean {
  if (systemRole === 'superadmin') return true;
  if (kb.myRole) return kb.myRole === 'admin' || kb.myRole === 'owner';
  if (kb.isGlobal || kb.visibility === 'public') return systemRole === 'admin';
  return false;
}

/**
 * canOpenKbAdvancedSettings is the single predicate behind BOTH entry points to
 * KbSettingsPanel — the sliders icon on a KB card in HomeView and the one in
 * the ChatView header. They used to carry two hand-written gates that had
 * drifted apart (HomeView checked only the KB role, ChatView only the system
 * role plus the legacy owner-mirror column), so a plain user reached the panel
 * from one and not the other.
 *
 * It is the client-side twin of kbAdvancedChain: a system role in
 * {api-user, admin, superadmin} AND an effective KB role of admin or better.
 * Both terms are required, and the server enforces the same two — this only
 * decides whether to offer the door, never whether it opens.
 */
export function canOpenKbAdvancedSettings(
  kb: Pick<KnowledgeBase, 'myRole' | 'isGlobal' | 'visibility'> | null | undefined,
  systemRole?: string,
): boolean {
  if (kb == null) return false;
  return hasAdvancedSystemRole(systemRole) && hasKbAdminRole(kb, systemRole);
}
