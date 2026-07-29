// Package errs defines the standalone agent's structured error contract.
package errs

import (
	"errors"
	"fmt"
)

type Category string
type Subtype string

const (
	CategoryValidation    Category = "validation"
	CategoryInternal      Category = "internal"
	CategoryNetwork       Category = "network"
	CategoryAuthorization Category = "authorization"
	CategoryConfig        Category = "config"
	CategoryAPI           Category = "api"
)

const (
	SubtypeInvalidArgument     Subtype = "invalid_argument"
	SubtypeFailedPrecondition  Subtype = "failed_precondition"
	SubtypeInvalidResponse     Subtype = "invalid_response"
	SubtypeFileIO              Subtype = "file_io"
	SubtypeStorage             Subtype = "storage"
	SubtypeUnknown             Subtype = "unknown"
	SubtypeNetworkTransport    Subtype = "network_transport"
	SubtypeMissingScope        Subtype = "missing_scope"
	SubtypeInvalidConfig       Subtype = "invalid_config"
	SubtypeNotConfigured       Subtype = "not_configured"
	SubtypeServerError         Subtype = "server_error"
	SubtypeModelNonConvergence Subtype = "model_non_convergence"
)

type Problem struct {
	Category      Category `json:"type"`
	Subtype       Subtype  `json:"subtype"`
	Code          int      `json:"code,omitempty"`
	Message       string   `json:"message"`
	Identity      string   `json:"identity,omitempty"`
	Param         string   `json:"param,omitempty"`
	Field         string   `json:"field,omitempty"`
	Hint          string   `json:"hint,omitempty"`
	LogID         string   `json:"log_id,omitempty"`
	MissingScopes []string `json:"missing_scopes,omitempty"`
	Retryable     bool     `json:"retryable,omitempty"`
	cause         error
}

type ValidationError = Problem
type InternalError = Problem
type NetworkError = Problem
type PermissionError = Problem
type ConfigError = Problem

func (p *Problem) Error() string {
	if p == nil {
		return ""
	}
	return p.Message
}

func (p *Problem) Unwrap() error {
	if p == nil {
		return nil
	}
	return p.cause
}

func (p *Problem) WithCause(err error) *Problem {
	p.cause = err
	return p
}

func (p *Problem) WithHint(format string, args ...any) *Problem {
	p.Hint = fmt.Sprintf(format, args...)
	return p
}

func (p *Problem) WithParam(param string) *Problem {
	p.Param = param
	return p
}

func (p *Problem) WithField(field string) *Problem {
	p.Field = field
	return p
}

func (p *Problem) WithIdentity(identity string) *Problem {
	p.Identity = identity
	return p
}

func (p *Problem) WithMissingScopes(scopes ...string) *Problem {
	p.MissingScopes = append([]string(nil), scopes...)
	return p
}

func (p *Problem) WithRetryable(retryable bool) *Problem {
	p.Retryable = retryable
	return p
}

func (p *Problem) WithCode(code int) *Problem {
	p.Code = code
	return p
}

func NewValidationError(subtype Subtype, format string, args ...any) *Problem {
	return newProblem(CategoryValidation, subtype, format, args...)
}

func NewInternalError(subtype Subtype, format string, args ...any) *Problem {
	return newProblem(CategoryInternal, subtype, format, args...)
}

func NewNetworkError(subtype Subtype, format string, args ...any) *Problem {
	return newProblem(CategoryNetwork, subtype, format, args...).WithRetryable(true)
}

func NewPermissionError(subtype Subtype, format string, args ...any) *Problem {
	return newProblem(CategoryAuthorization, subtype, format, args...)
}

func NewConfigError(subtype Subtype, format string, args ...any) *Problem {
	return newProblem(CategoryConfig, subtype, format, args...)
}

func NewAPIError(subtype Subtype, format string, args ...any) *Problem {
	return newProblem(CategoryAPI, subtype, format, args...)
}

func ProblemOf(err error) (*Problem, bool) {
	var problem *Problem
	ok := errors.As(err, &problem)
	return problem, ok
}

func UnwrapTypedError(err error) (*Problem, bool) {
	return ProblemOf(err)
}

func newProblem(category Category, subtype Subtype, format string, args ...any) *Problem {
	return &Problem{
		Category: category,
		Subtype:  subtype,
		Message:  fmt.Sprintf(format, args...),
	}
}
