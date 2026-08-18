package kbaccess

import "github.com/justrag/go-backend/internal/auth"

// CanRename decides whether the caller may change a knowledge base's name.
//
// Renaming is deliberately stricter than the KB role 'admin' that gates the
// rest of PATCH /api/kb/{id}: on a private KB only the owner (and a superadmin,
// who resolves to owner) may rename; on a public KB — which has no owner —
// only a system admin or superadmin may. A kb_members 'admin' row alone
// (curator, demoted ex-owner) is not enough on either kind of KB.
//
// access is the KBAccessResult the middleware injected (nil → false); sysRole
// is the caller's system role from the auth claims.
func CanRename(access *KBAccessResult, sysRole string) bool {
	if access == nil || access.KB == nil {
		return false
	}
	if access.Role == RoleOwner {
		return true
	}
	if access.KB.IsGlobal {
		return sysRole == auth.RoleAdmin || sysRole == auth.RoleSuperAdmin
	}
	return false
}
