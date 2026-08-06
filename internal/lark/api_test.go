package lark

import (
	"strings"
	"testing"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

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

func TestAPIProblemPreservesFieldViolation(t *testing.T) {
	err := apiProblem(99992402, map[string]any{
		"msg": "field validation failed",
		"error": map[string]any{"field_violations": []any{
			map[string]any{"field": "token", "description": "token is required"},
		}},
	}, IdentityUser)
	problem, ok := errs.ProblemOf(err)
	if !ok || problem.Param != "token" ||
		!strings.Contains(problem.Hint, "token is required") {
		t.Fatalf("problem=%+v", problem)
	}
}

func TestAPIProblemClassifiesRequiredPrivilegesAsMissingScope(t *testing.T) {
	err := apiProblem(403, map[string]any{
		"msg": "Unauthorized. You do not have permission to perform the requested operation on the resource. " +
			"Please request user re-authorization and try again. required one of these privileges under the user identity: " +
			"[bitable:app:readonly, bitable:app, base:record:retrieve]",
	}, IdentityUser)
	problem, ok := errs.ProblemOf(err)
	if !ok ||
		problem.Category != errs.CategoryAuthorization ||
		problem.Subtype != errs.SubtypeMissingScope ||
		problem.Identity != string(IdentityUser) {
		t.Fatalf("problem=%+v", problem)
	}
}

func TestAPIProblemKeepsGenericResourceDenialDistinctFromMissingScope(t *testing.T) {
	err := apiProblem(403, map[string]any{
		"msg": "You do not have permission to access this Base record",
	}, IdentityUser)
	problem, ok := errs.ProblemOf(err)
	if !ok ||
		problem.Category != errs.CategoryAuthorization ||
		problem.Subtype != errs.SubtypeFailedPrecondition {
		t.Fatalf("problem=%+v", problem)
	}
}
