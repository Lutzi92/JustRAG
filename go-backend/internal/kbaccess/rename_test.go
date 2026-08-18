package kbaccess_test

import (
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
)

func TestCanRename(t *testing.T) {
	private := &kbaccess.KnowledgeBase{ID: "kb", IsGlobal: false}
	public := &kbaccess.KnowledgeBase{ID: "kb", IsGlobal: true, IsPublished: true}

	cases := []struct {
		name    string
		kb      *kbaccess.KnowledgeBase
		role    string
		sysRole string
		want    bool
	}{
		{"private owner", private, kbaccess.RoleOwner, auth.RoleUser, true},
		{"private admin member", private, kbaccess.RoleAdmin, auth.RoleUser, false},
		{"private editor", private, kbaccess.RoleEdit, auth.RoleUser, false},
		{"private admin member with system admin", private, kbaccess.RoleAdmin, auth.RoleAdmin, false},
		{"private superadmin resolves to owner", private, kbaccess.RoleOwner, auth.RoleSuperAdmin, true},
		{"public system admin", public, kbaccess.RoleAdmin, auth.RoleAdmin, true},
		{"public superadmin", public, kbaccess.RoleOwner, auth.RoleSuperAdmin, true},
		{"public kb-admin member without system role", public, kbaccess.RoleAdmin, auth.RoleUser, false},
		{"public viewer", public, kbaccess.RoleView, auth.RoleUser, false},
		{"nil access", nil, "", auth.RoleAdmin, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var access *kbaccess.KBAccessResult
			if tc.kb != nil {
				access = &kbaccess.KBAccessResult{KB: tc.kb, Role: tc.role}
			}
			if got := kbaccess.CanRename(access, tc.sysRole); got != tc.want {
				t.Fatalf("CanRename(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
