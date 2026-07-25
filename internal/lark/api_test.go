package lark

import "testing"

func TestNumericCodeRejectsMissingOrUnknownShape(t *testing.T) {
	for _, value := range []any{nil, "", "not-a-number", map[string]any{"code": 0}} {
		if code, ok := numericCode(value); ok {
			t.Fatalf("numericCode(%v)=%d,true; want false", value, code)
		}
	}
}

func TestNumericCodeAcceptsDocumentedShapes(t *testing.T) {
	for _, value := range []any{float64(0), 0, "0"} {
		if code, ok := numericCode(value); !ok || code != 0 {
			t.Fatalf("numericCode(%v)=%d,%v; want 0,true", value, code, ok)
		}
	}
}

func TestRequireSuccessCodeFailsOnUnknownSuccessfulEnvelope(t *testing.T) {
	if err := requireSuccessCode(map[string]any{"data": map[string]any{}}, IdentityBot); err == nil {
		t.Fatal("missing code was accepted")
	}
}

func TestRequireSuccessCodeFailsOnNonZeroCode(t *testing.T) {
	if err := requireSuccessCode(map[string]any{"code": float64(999), "msg": "denied"}, IdentityUser); err == nil {
		t.Fatal("non-zero code was accepted")
	}
}
