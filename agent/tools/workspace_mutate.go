package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/workspace"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// WorkspaceMutationOptions controls structured workspace writes.
type WorkspaceMutationOptions struct {
	ApprovalRequired bool
	Approvals        ShellApprovalStore
	CodingPlanMode   bool
}

// WorkspaceMutationDefinitions exposes owner-only structured file mutation tools.
func WorkspaceMutationDefinitions(scope *workspace.Scope, options WorkspaceMutationOptions) []Definition {
	return []Definition{
		editWorkspaceDefinition(scope, options),
		writeWorkspaceDefinition(scope, options),
	}
}

func editWorkspaceDefinition(scope *workspace.Scope, options WorkspaceMutationOptions) Definition {
	return Definition{
		SideEffect:         true,
		OwnerOnly:          true,
		WorkspaceWriteOnly: true,
		Risk:               ToolRiskMedium,
		Info: toolInfo("edit_workspace", "Replace exact unique text in an existing workspace file. Multiple replacements in one call all match the original file and must not overlap. Prefer this over shell for file edits. Read the file again before citing it.", map[string]*schema.ParameterInfo{
			"path": {Type: schema.String, Required: true},
			"edits": {
				Type:     schema.Array,
				Required: true,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.Object,
					SubParams: map[string]*schema.ParameterInfo{
						"old_text": {Type: schema.String, Required: true},
						"new_text": {Type: schema.String, Required: true},
					},
				},
			},
		}),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path  string `json:"path"`
				Edits []struct {
					OldText string `json:"old_text"`
					NewText string `json:"new_text"`
				} `json:"edits"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if strings.TrimSpace(args.Path) == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "edit_workspace path is required").WithParam("path")
			}
			if err := denyWorkspaceMutation(ctx, options, "edit_workspace"); err != nil {
				return Execution{}, err
			}
			if blocked, execution, err := maybeRequestWorkspaceMutationApproval(ctx, options, "edit_workspace", raw); blocked || err != nil {
				return execution, err
			}
			edits := make([]workspace.TextEdit, 0, len(args.Edits))
			for _, edit := range args.Edits {
				edits = append(edits, workspace.TextEdit{OldText: edit.OldText, NewText: edit.NewText})
			}
			report, source, err := scope.EditText(args.Path, edits)
			if err != nil {
				return Execution{}, err
			}
			return jsonExecution(report, []domain.SourceRef{source}, nil)
		},
	}
}

func writeWorkspaceDefinition(scope *workspace.Scope, options WorkspaceMutationOptions) Definition {
	return Definition{
		SideEffect:         true,
		OwnerOnly:          true,
		WorkspaceWriteOnly: true,
		Risk:               ToolRiskMedium,
		Info: toolInfo("write_workspace", "Create a new workspace file or overwrite an entire existing file. Use edit_workspace for unique replacements in an existing file. Read the file again before citing it.", map[string]*schema.ParameterInfo{
			"path":    {Type: schema.String, Required: true},
			"content": {Type: schema.String, Required: true},
		}),
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if strings.TrimSpace(args.Path) == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "write_workspace path is required").WithParam("path")
			}
			if err := denyWorkspaceMutation(ctx, options, "write_workspace"); err != nil {
				return Execution{}, err
			}
			if blocked, execution, err := maybeRequestWorkspaceMutationApproval(ctx, options, "write_workspace", raw); blocked || err != nil {
				return execution, err
			}
			report, source, err := scope.WriteText(args.Path, args.Content)
			if err != nil {
				return Execution{}, err
			}
			return jsonExecution(report, []domain.SourceRef{source}, nil)
		},
	}
}

func denyWorkspaceMutation(_ context.Context, options WorkspaceMutationOptions, name string) error {
	if options.CodingPlanMode {
		return errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"coding plan mode denies %s; submit the plan and leave plan mode before changing production files",
			name,
		).WithParam("path")
	}
	return nil
}

func maybeRequestWorkspaceMutationApproval(
	ctx context.Context,
	options WorkspaceMutationOptions,
	name string,
	raw json.RawMessage,
) (bool, Execution, error) {
	if !options.ApprovalRequired {
		return false, Execution{}, nil
	}
	if options.Approvals == nil {
		return true, Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "workspace mutation approval store is not configured")
	}
	dedupKey := workItemDedup(ctx)
	if dedupKey == "" {
		return true, Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "workspace mutation approval requires a durable work item")
	}
	command := name + " " + strings.TrimSpace(string(raw))
	approvedID, approved, err := options.Approvals.ConsumeShellApproval(ctx, dedupKey, command, ".")
	if err != nil {
		return true, Execution{}, err
	}
	if !approved {
		actionID, err := options.Approvals.RequestShellApproval(ctx, dedupKey, command, ".")
		if err != nil {
			return true, Execution{}, err
		}
		execution, execErr := jsonExecution(map[string]any{
			"approval_required": true,
			"action_id":         actionID,
			"error":             "approval is required before executing this exact workspace mutation",
		}, nil, nil)
		return true, execution, execErr
	}
	_ = approvedID
	return false, Execution{}, nil
}
