package chat

import "testing"

func TestSanitizeParentMessageID(t *testing.T) {
	valid := "7812636d-dd9b-4fef-86d1-1815255a2761"

	tests := []struct {
		name string
		in   string
		want *string
	}{
		{"valid uuid is preserved", valid, &valid},
		{"empty string yields nil", "", nil},
		{"client temp-error placeholder yields nil", "temp-error-1782910283827", nil},
		{"client temp-user placeholder yields nil", "temp-user-123", nil},
		{"arbitrary non-uuid yields nil", "not-a-uuid", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeParentMessageID(tc.in)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("expected nil, got %q", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("expected %q, got nil", *tc.want)
			case tc.want != nil && got != nil && *got != *tc.want:
				t.Fatalf("expected %q, got %q", *tc.want, *got)
			}
		})
	}
}
