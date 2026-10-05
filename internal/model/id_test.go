package model

import (
	"errors"
	"strings"
	"testing"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
)

// Проверяет нормализацию UUID и отказ на неверном формате.
func TestNormalizeID(t *testing.T) {
	const canonical = "abcdef01-2345-4678-9abc-def012345678"
	for _, raw := range []string{canonical, strings.ToUpper(canonical), strings.ReplaceAll(canonical, "-", ""), strings.ToUpper(strings.ReplaceAll(canonical, "-", ""))} {
		got, err := NormalizeID(raw)
		if err != nil || got != canonical {
			t.Fatalf("NormalizeID(%q)=%q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "id", " " + canonical, canonical + " ", "abcdef012345-4678-9abc-def012345678", strings.ReplaceAll(canonical, "-", "_"), "gbcdef01-2345-4678-9abc-def012345678", strings.Repeat("0", 31), strings.Repeat("0", 33), "{" + canonical + "}"} {
		got, err := NormalizeID(raw)
		if !errors.Is(err, orderserrors.ErrInvalidInput) || got != "" {
			t.Fatalf("NormalizeID(%q)=%q, %v", raw, got, err)
		}
	}
}
