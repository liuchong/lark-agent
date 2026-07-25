package lark

import (
	"encoding/json"
	"fmt"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type Identity = string

const (
	IdentityUser Identity = "user"
	IdentityBot  Identity = "bot"
)

type errorEnvelope struct {
	OK       bool     `json:"ok"`
	Identity Identity `json:"identity"`
	Error    struct {
		Category      errs.Category `json:"type"`
		Subtype       errs.Subtype  `json:"subtype"`
		Code          int           `json:"code"`
		Message       string        `json:"message"`
		Param         string        `json:"param"`
		Hint          string        `json:"hint"`
		LogID         string        `json:"log_id"`
		MissingScopes []string      `json:"missing_scopes"`
		Retryable     bool          `json:"retryable"`
	} `json:"error"`
}

func ParseProblem(data []byte) (*errs.Problem, error) {
	var envelope errorEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode lark error envelope: %w", err)
	}
	if envelope.OK || envelope.Error.Category == "" || envelope.Error.Subtype == "" ||
		envelope.Error.Message == "" {
		return nil, fmt.Errorf("invalid lark error envelope")
	}
	return &errs.Problem{
		Category:      envelope.Error.Category,
		Subtype:       envelope.Error.Subtype,
		Code:          envelope.Error.Code,
		Message:       envelope.Error.Message,
		Identity:      envelope.Identity,
		Param:         envelope.Error.Param,
		Hint:          envelope.Error.Hint,
		LogID:         envelope.Error.LogID,
		MissingScopes: append([]string(nil), envelope.Error.MissingScopes...),
		Retryable:     envelope.Error.Retryable,
	}, nil
}
