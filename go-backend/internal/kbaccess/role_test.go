package kbaccess_test

import (
	"testing"

	"github.com/justrag/go-backend/internal/kbaccess"
)

func TestAtLeast(t *testing.T) {
	tests := []struct {
		granted, required string
		want              bool
	}{
		{kbaccess.RoleOwner, kbaccess.RoleAdmin, true},
		{kbaccess.RoleAdmin, kbaccess.RoleEdit, true},
		{kbaccess.RoleEdit, kbaccess.RoleView, true},
		{kbaccess.RoleView, kbaccess.RoleView, true},
		{kbaccess.RoleEdit, kbaccess.RoleAdmin, false},
		{kbaccess.RoleView, kbaccess.RoleEdit, false},
		{"", kbaccess.RoleView, false},
		{"bogus", kbaccess.RoleView, false},
		{kbaccess.RoleOwner, "bogus", false},
	}
	for _, tt := range tests {
		if got := kbaccess.AtLeast(tt.granted, tt.required); got != tt.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", tt.granted, tt.required, got, tt.want)
		}
	}
}

func TestAssignable(t *testing.T) {
	for _, r := range []string{kbaccess.RoleView, kbaccess.RoleEdit, kbaccess.RoleAdmin} {
		if !kbaccess.Assignable(r) {
			t.Errorf("Assignable(%q) = false, want true", r)
		}
	}
	for _, r := range []string{kbaccess.RoleOwner, "", "bogus"} {
		if kbaccess.Assignable(r) {
			t.Errorf("Assignable(%q) = true, want false", r)
		}
	}
}
