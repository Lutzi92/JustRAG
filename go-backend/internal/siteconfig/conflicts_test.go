package siteconfig

import (
	"strings"
	"testing"
)

func confSP(s string) *string { return &s }

func TestValidateConflicts(t *testing.T) {
	tests := []struct {
		name     string
		existing map[string]*string
		updates  []KeyValue
		wantErr  string // substring; "" = no error
	}{
		{
			name:     "enabling both halves in one batch is rejected",
			existing: map[string]*string{},
			updates: []KeyValue{
				{Key: "raptor_enabled", Value: confSP("true")},
				{Key: "parent_child_enabled", Value: confSP("true")},
			},
			wantErr: "mutually exclusive",
		},
		{
			name:     "enabling one half while the other is already on is rejected",
			existing: map[string]*string{"parent_child_enabled": confSP("true")},
			updates:  []KeyValue{{Key: "raptor_enabled", Value: confSP("true")}},
			wantErr:  "mutually exclusive",
		},
		{
			name:     "self-rag vs factuality verifier is rejected",
			existing: map[string]*string{"chat_factuality_verifier_enabled": confSP("1")},
			updates:  []KeyValue{{Key: "chat_self_rag_enabled", Value: confSP("true")}},
			wantErr:  "chat_self_rag_enabled",
		},
		{
			name:     "swap in one batch (enable one, disable the other) is allowed",
			existing: map[string]*string{"parent_child_enabled": confSP("true")},
			updates: []KeyValue{
				{Key: "raptor_enabled", Value: confSP("true")},
				{Key: "parent_child_enabled", Value: confSP("false")},
			},
		},
		{
			name:     "clearing a key (nil value) counts as off",
			existing: map[string]*string{"parent_child_enabled": confSP("true")},
			updates: []KeyValue{
				{Key: "raptor_enabled", Value: confSP("true")},
				{Key: "parent_child_enabled", Value: nil},
			},
		},
		{
			name:     "pre-existing incoherent state does not block unrelated saves",
			existing: map[string]*string{"raptor_enabled": confSP("true"), "parent_child_enabled": confSP("true")},
			updates:  []KeyValue{{Key: "logo_path", Value: confSP("/uploads/logo/x.png")}},
		},
		{
			name:     "enabling one half while the other is explicitly false is allowed",
			existing: map[string]*string{"parent_child_enabled": confSP("false")},
			updates:  []KeyValue{{Key: "raptor_enabled", Value: confSP("true")}},
		},
		{
			name:     "disabling a flag never conflicts",
			existing: map[string]*string{"raptor_enabled": confSP("true"), "parent_child_enabled": confSP("true")},
			updates:  []KeyValue{{Key: "raptor_enabled", Value: confSP("false")}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConflicts(tt.existing, tt.updates)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
