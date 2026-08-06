package model

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type FailureCategory string

const (
	FailureInvalidRequest        FailureCategory = "invalid_request"
	FailureAuthentication        FailureCategory = "authentication"
	FailurePermission            FailureCategory = "permission"
	FailureNotFound              FailureCategory = "not_found"
	FailureRateLimit             FailureCategory = "rate_limit"
	FailureQuotaExhausted        FailureCategory = "quota_exhausted"
	FailureTimeout               FailureCategory = "timeout"
	FailureOverloaded            FailureCategory = "overloaded"
	FailureNetwork               FailureCategory = "network"
	FailureContextOverflow       FailureCategory = "context_overflow"
	FailureRequestTooLarge       FailureCategory = "request_too_large"
	FailureInvalidProviderOutput FailureCategory = "invalid_provider_output"
	FailureCancelled             FailureCategory = "cancelled"
)

type Failure struct {
	Category       FailureCategory
	HTTPStatus     int
	Retryable      bool
	RetryAfter     time.Duration
	RecoveryAction RecoveryAction
	Diagnostic     string
}

func ClassifyHTTPFailure(status int, body string, retryAfter string, now time.Time) Failure {
	diagnostic := redactDiagnostic(strings.TrimSpace(body))
	lower := strings.ToLower(body)
	failure := Failure{
		HTTPStatus: status,
		Diagnostic: diagnostic,
	}
	if containsAny(lower, "insufficient quota", "quota exhausted", "account balance", "余额", "额度不足") {
		failure.Category = FailureQuotaExhausted
		failure.RecoveryAction = RecoveryStopDeterministic
		return failure
	}
	switch status {
	case http.StatusBadRequest:
		failure.Category = FailureInvalidRequest
		failure.RecoveryAction = RecoveryStopDeterministic
	case http.StatusUnauthorized:
		failure.Category = FailureAuthentication
		failure.RecoveryAction = RecoveryStopDeterministic
	case http.StatusForbidden:
		failure.Category = FailurePermission
		failure.RecoveryAction = RecoveryStopDeterministic
	case http.StatusNotFound:
		failure.Category = FailureNotFound
		failure.RecoveryAction = RecoveryStopDeterministic
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		failure.Category = FailureRateLimit
		failure.Retryable = true
		failure.RetryAfter = parseRetryAfter(retryAfter, now)
		failure.RecoveryAction = RecoveryRetryStep
	case 529:
		failure.Category = FailureOverloaded
		failure.Retryable = true
		failure.RetryAfter = parseRetryAfter(retryAfter, now)
		failure.RecoveryAction = RecoveryRetryStep
	default:
		if status >= 500 && status <= 599 {
			failure.Category = FailureOverloaded
			failure.Retryable = true
			failure.RetryAfter = parseRetryAfter(retryAfter, now)
			failure.RecoveryAction = RecoveryRetryStep
		} else {
			failure.Category = FailureInvalidRequest
			failure.RecoveryAction = RecoveryStopDeterministic
		}
	}
	return failure
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func redactDiagnostic(raw string) string {
	parts := strings.Fields(raw)
	for i, part := range parts {
		if strings.EqualFold(part, "bearer") && i+1 < len(parts) {
			parts[i+1] = "[REDACTED]"
		}
		if strings.HasPrefix(strings.ToLower(part), "sk-") {
			parts[i] = "[REDACTED]"
		}
	}
	return strings.Join(parts, " ")
}
