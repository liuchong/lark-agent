package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Execution is one model-visible tool result.
type Execution struct {
	Content  string             `json:"content"`
	Sources  []domain.SourceRef `json:"sources,omitempty"`
	Decision *domain.Decision   `json:"decision,omitempty"`
	Receipt  *ToolReceipt       `json:"receipt,omitempty"`
}

// ToolPermission controls whether the registry may execute a tool directly.
type ToolPermission string

const (
	ToolPermissionAllow ToolPermission = "allow"
	ToolPermissionAsk   ToolPermission = "ask"
	ToolPermissionDeny  ToolPermission = "deny"
)

// ToolRisk describes the external or operational risk of a tool.
type ToolRisk string

const (
	ToolRiskLow    ToolRisk = "low"
	ToolRiskMedium ToolRisk = "medium"
	ToolRiskHigh   ToolRisk = "high"
)

// ToolReceipt is an audit key for a real tool execution.
type ToolReceipt struct {
	ToolName     string             `json:"tool_name"`
	Permission   ToolPermission     `json:"permission"`
	Risk         ToolRisk           `json:"risk,omitempty"`
	ArgumentHash string             `json:"argument_hash"`
	ResultDigest string             `json:"result_digest"`
	Sources      []domain.SourceRef `json:"sources,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
}

// Definition binds a model-visible schema to its controlled implementation.
type Definition struct {
	Info       *schema.ToolInfo
	Permission ToolPermission
	Risk       ToolRisk
	SideEffect bool
	Execute    func(context.Context, json.RawMessage) (Execution, error)
}

// Registry is the sole model-callable tool catalog.
type Registry struct {
	definitions map[string]Definition
}

type workItemContextKey struct{}

// WithWorkItemDedup makes the current durable work identity available to tools.
func WithWorkItemDedup(ctx context.Context, dedupKey string) context.Context {
	return context.WithValue(ctx, workItemContextKey{}, dedupKey)
}

func workItemDedup(ctx context.Context) string {
	value, _ := ctx.Value(workItemContextKey{}).(string)
	return value
}

// NewRegistry validates and indexes model-callable tools.
func NewRegistry(definitions ...Definition) (*Registry, error) {
	registry := &Registry{definitions: make(map[string]Definition, len(definitions))}
	for _, definition := range definitions {
		if definition.Info == nil || strings.TrimSpace(definition.Info.Name) == "" {
			return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "agent tool name is required")
		}
		if definition.Permission == "" {
			definition.Permission = ToolPermissionAllow
		}
		if definition.Execute == nil {
			return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "agent tool %s has no executor", definition.Info.Name)
		}
		if _, exists := registry.definitions[definition.Info.Name]; exists {
			return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "duplicate agent tool: %s", definition.Info.Name)
		}
		registry.definitions[definition.Info.Name] = definition
	}
	return registry, nil
}

// Infos returns a deterministic model tool catalog.
func (r *Registry) Infos() []*schema.ToolInfo {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	infos := make([]*schema.ToolInfo, 0, len(names))
	for _, name := range names {
		infos = append(infos, r.definitions[name].Info)
	}
	return infos
}

// Execute invokes one registered tool by its native call name.
func (r *Registry) Execute(ctx context.Context, name string, arguments json.RawMessage) (Execution, error) {
	if r == nil {
		return Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "agent tool registry is not configured")
	}
	definition, ok := r.definitions[name]
	if !ok {
		return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "unknown agent tool: %s", name)
	}
	if definition.Permission == ToolPermissionDeny {
		return Execution{}, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"tool permission denies execution: %s",
			name,
		)
	}
	execution, err := definition.Execute(ctx, arguments)
	if err != nil {
		return execution, err
	}
	if execution.Receipt == nil {
		execution.Receipt = newToolReceipt(definition, arguments, execution)
	}
	return execution, nil
}

func newToolReceipt(definition Definition, arguments json.RawMessage, execution Execution) *ToolReceipt {
	argSum := sha256.Sum256([]byte(arguments))
	resultSum := sha256.Sum256([]byte(execution.Content))
	return &ToolReceipt{
		ToolName:     definition.Info.Name,
		Permission:   definition.Permission,
		Risk:         definition.Risk,
		ArgumentHash: fmt.Sprintf("sha256:%x", argSum[:]),
		ResultDigest: fmt.Sprintf("sha256:%x", resultSum[:]),
		Sources:      append([]domain.SourceRef(nil), execution.Sources...),
		CreatedAt:    time.Now().UTC(),
	}
}
