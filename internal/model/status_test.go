package model

import "testing"

// Проверяет набор разрешённых статусов.
func TestOrderStatusIsValid(t *testing.T) {
	tests := []struct {
		status OrderStatus
		valid  bool
	}{
		{"created", true},
		{"cooking", true},
		{"ready", true},
		{"delivering", true},
		{"delivered", true},
		{"cancelled", true},
		{"", false},
		{"unknown", false},
		{"canceled", false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			if got := tc.status.IsValid(); got != tc.valid {
				t.Fatalf("IsValid(%q) = %v, want %v", tc.status, got, tc.valid)
			}
		})
	}
}

// Проверяет разрешённые и запрещённые переходы статуса.
func TestOrderStatusCanTransitionTo(t *testing.T) {
	states := []OrderStatus{"created", "cooking", "ready", "delivering", "delivered", "cancelled", "", "unknown", "canceled"}
	allowed := map[[2]OrderStatus]bool{
		{"created", "cooking"}:      true,
		{"created", "cancelled"}:    true,
		{"cooking", "ready"}:        true,
		{"ready", "delivering"}:     true,
		{"delivering", "delivered"}: true,
	}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				want := allowed[[2]OrderStatus{from, to}]
				if got := from.CanTransitionTo(to); got != want {
					t.Fatalf("CanTransitionTo(%q, %q) = %v, want %v", from, to, got, want)
				}
			})
		}
	}
}
